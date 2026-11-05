package worker

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFailedSummaryYieldsToIndependentStepsAndSuccessfulCommits(t *testing.T) {
	r, p := strategyWorker(t)
	failed := workerStep(t, r, p, "failed summary")
	ready := workerStep(t, r, p, "independent action")
	if _, err := r.DB.SQL.Exec(`INSERT INTO worker_execute_recovery(project_id,step_id,status,session_id,summary_attempts,created_at) VALUES(?,?,'summary_pending','saved-session',1,?)`, p, failed.ID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := r.nextStepCommit(t.Context(), p); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("failed summary ran ahead of independent work: %v", err)
	}
	if _, err := r.DB.HeartbeatStep(t.Context(), p, ready.ID, "local/worker/01"); err != nil {
		t.Fatal(err)
	}
	if err := r.saveExecuteStarted(t.Context(), p, ready.ID, "local/worker/01"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := r.nextStepCommit(t.Context(), p); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("failed summary ran while independent work was executing: %v", err)
	}
	payload := []byte(`{"accepted":true,"data":{"description":"new prerequisite","result":{"type":"progress"}}}`)
	if err := r.saveExecuteRecovery(t.Context(), p, ready.ID, payload, "local/worker/01"); err != nil {
		t.Fatal(err)
	}
	r.releaseStep(p, ready.ID, "local/worker/01")
	if _, step, _, err := r.nextStepCommit(t.Context(), p); err != nil || step != ready.ID {
		t.Fatalf("successful result was not committed first: step=%q err=%v", step, err)
	}
	if !r.commitNextStepResult(t.Context(), p) {
		t.Fatal("independent result was not committed")
	}
	if _, _, _, err := r.nextStepCommit(t.Context(), p); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("failed retry ran ahead of planning from the new prerequisite: %v", err)
	}
	events, err := r.DB.PendingDecideEvents(t.Context(), p)
	if err != nil || len(events) == 0 {
		t.Fatalf("new prerequisite did not trigger planning: events=%v err=%v", events, err)
	}
	if err := r.DB.AcknowledgeDecideEvents(t.Context(), p, events[len(events)-1].ID); err != nil {
		t.Fatal(err)
	}
	if _, step, _, err := r.nextStepCommit(t.Context(), p); err != nil || step != failed.ID {
		t.Fatalf("failed summary was not resumed after independent work: step=%q err=%v", step, err)
	}
}

func TestSummaryRetriesRotateAndRespectPersistentCooldown(t *testing.T) {
	r, p := strategyWorker(t)
	first := workerStep(t, r, p, "first summary")
	second := workerStep(t, r, p, "second summary")
	for _, s := range []string{first.ID, second.ID} {
		if _, err := r.DB.SQL.Exec(`INSERT INTO worker_execute_recovery(project_id,step_id,status,session_id,summary_attempts,created_at) VALUES(?,?,'summary_pending','saved-session',0,?)`, p, s, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.DB.SQL.Exec(`UPDATE worker_execute_recovery SET summary_attempts=1,retry_after=? WHERE project_id=? AND step_id=?`, time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano), p, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, s, _, err := r.nextStepCommit(t.Context(), p); err != nil || s != second.ID {
		t.Fatalf("untried summary did not run first: step=%q err=%v", s, err)
	}
	if _, err := r.DB.SQL.Exec(`UPDATE worker_execute_recovery SET summary_attempts=2 WHERE project_id=? AND step_id=?`, p, second.ID); err != nil {
		t.Fatal(err)
	}
	if _, s, _, err := r.nextStepCommit(t.Context(), p); err != nil || s != second.ID {
		t.Fatalf("retry cooldown was ignored: step=%q err=%v", s, err)
	}
	if _, err := r.DB.SQL.Exec(`UPDATE worker_execute_recovery SET retry_after='' WHERE project_id=?`, p); err != nil {
		t.Fatal(err)
	}
	if _, s, _, err := r.nextStepCommit(t.Context(), p); err != nil || s != first.ID {
		t.Fatalf("retries did not rotate by attempt count: step=%q err=%v", s, err)
	}
}

func TestDeferredSummaryDoesNotInvokeDecideWithoutCommittedFacts(t *testing.T) {
	r, p := strategyWorker(t)
	step := workerStep(t, r, p, "interrupted action")
	if _, err := r.DB.SQL.Exec(`INSERT INTO worker_execute_recovery(project_id,step_id,status,session_id,summary_attempts,retry_after,created_at) VALUES(?,?,'summary_pending','saved-session',1,?,?)`, p, step.ID, time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := r.DB.RequestDecide(t.Context(), p, "environment_failure"); err != nil {
		t.Fatal(err)
	}
	r.RunOnce(t.Context())
	r.dispatchProject(t.Context(), p)
	var count int
	if err := r.DB.SQL.QueryRow(`SELECT count(*) FROM worker_task_history WHERE task_type='decide'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("deferred summary caused a new Decide call: count=%d err=%v", count, err)
	}
}

func TestNeedsAttentionStepAllowsAlternativeDecision(t *testing.T) {
	r, p := strategyWorker(t)
	step := workerStep(t, r, p, "interrupted action")
	marker := filepath.Join(t.TempDir(), "tasks")
	env, _ := json.Marshal(map[string]string{"MARKER": marker})
	command := `printf '%s\n' "$HOTMOUSE_TASK_TYPE" >> "$MARKER"; printf '%s' '{"accepted":true,"data":{"steps":[{"from":["origin"],"description":"alternative action"}]}}'`
	if _, err := r.DB.SQL.Exec(`UPDATE worker_config SET driver='command',command=?,environment_json=? WHERE id=1`, command, string(env)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.DB.SQL.Exec(`INSERT INTO worker_execute_recovery(project_id,step_id,status,session_id,summary_attempts,created_at) VALUES(?,?,'needs_attention','saved-session',3,?)`, p, step.ID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	r.dispatchProject(t.Context(), p)
	calls, err := os.ReadFile(marker)
	if err != nil || string(calls) != "decide\n" {
		t.Fatalf("needs_attention step blocked alternative decision: calls=%q err=%v", calls, err)
	}
}
