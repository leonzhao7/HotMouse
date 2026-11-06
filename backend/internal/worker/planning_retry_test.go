package worker

import (
	"strings"
	"testing"

	"hotmouse/backend/internal/core"
)

func expireDecideRetry(t *testing.T, r *Runner, project string) {
	t.Helper()
	if _, err := r.DB.SQL.Exec(`UPDATE worker_retry_state SET retry_after='2000-01-01T00:00:00Z' WHERE project_id=? AND task_type='decide'`, project); err != nil {
		t.Fatal(err)
	}
}

func TestDispatcherBoundsFailedPlanningAfterAllStepsConclude(t *testing.T) {
	for _, tc := range []struct{ name, command string }{
		{"provider_failure", "exit 1"},
		{"invalid_payload", `printf '%s' '{'`},
		{"rejection", `printf '%s' '{"accepted":false,"reason":"cannot continue"}'`},
		{"timeout", "exec sleep 10"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, p := strategyWorker(t)
			step := workerStep(t, r, p, "completed work")
			if _, err := r.DB.ConcludeStep(t.Context(), p, step.ID, core.Conclude{Worker: "test", Description: "result recorded", Result: &core.ExecutionResult{Type: "progress"}}); err != nil {
				t.Fatal(err)
			}
			if _, err := r.DB.SQL.Exec(`UPDATE worker_config SET driver='command',command=?,runtime_json='{"decide_timeout_seconds":1}'`, tc.command); err != nil {
				t.Fatal(err)
			}
			for attempt := 1; attempt <= maxDecideFailures; attempt++ {
				expireDecideRetry(t, r, p)
				// New runners must retain the failure count from earlier attempts.
				r = New(r.DB)
				r.dispatchProject(t.Context(), p)
				var failures int
				if err := r.DB.SQL.QueryRow(`SELECT failure_count FROM worker_retry_state WHERE project_id=? AND task_type='decide'`, p).Scan(&failures); err != nil || failures != attempt {
					t.Fatalf("attempt=%d failure_count=%d err=%v", attempt, failures, err)
				}
				detail, err := r.DB.GetProject(t.Context(), p)
				if err != nil {
					t.Fatal(err)
				}
				want := "active"
				if attempt == maxDecideFailures {
					want = "failed"
				}
				if detail.Project.Status != want || len(detail.Steps) != 1 || detail.Steps[0].To == nil {
					t.Fatalf("attempt=%d project=%+v steps=%+v", attempt, detail.Project, detail.Steps)
				}
				if want == "failed" && !strings.Contains(detail.Project.FailureReason, "规划连续失败 3 次") {
					t.Fatalf("missing failure reason: %+v", detail.Project)
				}
			}
			r.dispatchProject(t.Context(), p)
			var calls int
			if err := r.DB.SQL.QueryRow(`SELECT count(*) FROM worker_task_history WHERE project_id=? AND task_type='decide'`, p).Scan(&calls); err != nil || calls != maxDecideFailures {
				t.Fatalf("failed project kept invoking planner: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestPlanningFailureCountResetsForNewInputAndSuccess(t *testing.T) {
	r, p := strategyWorker(t)
	if err := r.DB.RequestDecide(t.Context(), p, "initial plan"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.DB.SQL.Exec(`UPDATE worker_config SET driver='command',command='exit 1'`); err != nil {
		t.Fatal(err)
	}
	checkCount := func(want int) {
		t.Helper()
		var got int
		if err := r.DB.SQL.QueryRow(`SELECT failure_count FROM worker_retry_state WHERE project_id=? AND task_type='decide'`, p).Scan(&got); err != nil || got != want {
			t.Fatalf("failure_count=%d want=%d err=%v", got, want, err)
		}
	}
	for i := 1; i <= 2; i++ {
		expireDecideRetry(t, r, p)
		r.dispatchProject(t.Context(), p)
		checkCount(i)
	}
	// Even an event without a graph mutation gets a fresh planning allowance.
	if err := r.DB.RequestDecide(t.Context(), p, "try a new direction"); err != nil {
		t.Fatal(err)
	}
	expireDecideRetry(t, r, p)
	r.dispatchProject(t.Context(), p)
	checkCount(1)
	if _, err := r.DB.AddHint(t.Context(), p, core.CreateHint{Content: "new prerequisite", Creator: "test"}); err != nil {
		t.Fatal(err)
	}
	expireDecideRetry(t, r, p)
	r.dispatchProject(t.Context(), p)
	checkCount(1)
	command := `printf '%s' '{"accepted":true,"data":{"steps":[{"from":["origin"],"description":"new work","goal_id":"g001"}]}}'`
	if _, err := r.DB.SQL.Exec(`UPDATE worker_config SET command=?`, command); err != nil {
		t.Fatal(err)
	}
	expireDecideRetry(t, r, p)
	r.dispatchProject(t.Context(), p)
	var retries int
	if err := r.DB.SQL.QueryRow(`SELECT count(*) FROM worker_retry_state WHERE project_id=? AND task_type='decide'`, p).Scan(&retries); err != nil || retries != 0 {
		t.Fatalf("successful planning retained failures: count=%d err=%v", retries, err)
	}
	detail, err := r.DB.GetProject(t.Context(), p)
	if err != nil || detail.Project.Status != "active" || len(detail.Steps) != 1 {
		t.Fatalf("successful planner did not advance project: %+v err=%v", detail, err)
	}
}
