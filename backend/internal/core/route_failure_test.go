package core

import (
	"strings"
	"testing"
	"time"
)

func TestShelvedRouteImmediatelyFailsProjectWithOtherWork(t *testing.T) {
	for _, mode := range []string{"budget", "no_information", "needs_attention", "explicit", "plan"} {
		t.Run(mode, func(t *testing.T) {
			db := testDB(t)
			project := strategyProject(t, db)
			goal, limit, budget := "g001", 1, 0
			if mode == "budget" {
				budget = 1
			}
			if _, err := db.SetProjectBudget(t.Context(), project, budget); err != nil {
				t.Fatal(err)
			}
			route, err := db.CreateRoute(t.Context(), project, CreateRoute{Title: "stop-loss route", GoalID: &goal, NoProgressLimit: &limit})
			if err != nil {
				t.Fatal(err)
			}
			target := strategyStep(t, db, project, &route.ID)
			independent, err := db.CreateRoute(t.Context(), project, CreateRoute{Title: "independent route", GoalID: &goal})
			if err != nil {
				t.Fatal(err)
			}
			working := strategyStep(t, db, project, &independent.ID)
			if _, err := db.HeartbeatStep(t.Context(), project, working.ID, "worker"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ClaimDecide(t.Context(), project, ClaimDecide{Worker: "planner", Trigger: "already planning"}); err != nil {
				t.Fatal(err)
			}
			reason := "premise_invalidated"
			var conclusion ConcludeResult
			switch mode {
			case "budget":
				reason = "budget_exhausted"
				started := time.Now().UTC().Add(-2 * time.Second).Format(time.RFC3339Nano)
				history, err := db.SQL.Exec(`INSERT INTO worker_task_history(worker,project_id,step_id,task_type,status,started_at) VALUES('test',?,?,'execute','running',?)`, project, target.ID, started)
				if err != nil {
					t.Fatal(err)
				}
				id, _ := history.LastInsertId()
				err = db.FinishWorkerTask(t.Context(), id, "failed", "saved execution evidence")
				if err != nil {
					t.Fatal(err)
				}
			case "no_information", "needs_attention":
				reason = mode
				if mode == "no_information" {
					reason = "no_information_limit"
				}
				conclusion, err = db.ConcludeStep(t.Context(), project, target.ID, Conclude{Worker: "test", Description: "saved terminal observation", Result: &ExecutionResult{Type: mode, Detail: "full evidence remains available"}})
				if err != nil {
					t.Fatal(err)
				}
			case "explicit":
				status := "shelved"
				if _, err := db.UpdateRoute(t.Context(), project, route.ID, UpdateRoute{Status: &status, Reason: reason}); err != nil {
					t.Fatal(err)
				}
			case "plan":
				status, abandoned := "shelved", true
				plan := DecisionPlan{
					RouteUpdates: []PlanRouteUpdate{{ID: route.ID, Change: UpdateRoute{Status: &status, Reason: reason}}},
					StepUpdates:  []PlanStepUpdate{{ID: target.ID, Change: UpdateStep{Abandoned: &abandoned}}},
					Steps:        []CreateStep{{From: []string{"origin"}, Description: target.Description, Creator: "planner"}},
				}
				if err := db.ApplyDecisionPlan(t.Context(), project, "planner", plan); err != nil {
					t.Fatal(err)
				}
			}
			detail, err := db.GetProject(t.Context(), project)
			if err != nil || detail.Project.Status != "failed" || (mode != "budget" && !strings.Contains(detail.Project.FailureReason, route.ID)) || !strings.Contains(detail.Project.FailureReason, reason) || detail.Project.Decide != nil {
				t.Fatalf("route failure left project running: %+v err=%v", detail.Project, err)
			}
			if len(detail.Steps) != 2 {
				t.Fatal("terminal plan added new work")
			}
			for _, step := range detail.Steps {
				if step.ID == working.ID && (step.Worker != nil || step.Abandoned) {
					t.Fatal("terminal failure did not release execution while preserving its record")
				}
			}
			if conclusion.Fact != nil {
				fact, err := db.NodeDetail(t.Context(), project, "fact", conclusion.Fact.ID)
				if err != nil || fact.(Fact).Detail != "full evidence remains available" {
					t.Fatal("failure rolled back the triggering evidence")
				}
			}
			events, err := db.PendingDecideEvents(t.Context(), project)
			if err != nil || len(events) != 0 {
				t.Fatalf("route failure requested replanning: %+v err=%v", events, err)
			}
			if err := db.RequestDecide(t.Context(), project, "try another route"); err == nil {
				t.Fatal("failed project allowed a new plan request")
			}
			if _, err := db.HeartbeatStep(t.Context(), project, target.ID, "new-worker"); err == nil {
				t.Fatal("failed project allowed new work")
			}
			if err := db.RefreshRouteLimits(t.Context()); err != nil {
				t.Fatal(err)
			}
			records, err := db.RouteRecords(t.Context(), project)
			if mode == "budget" {
				if err != nil || len(records) != 0 {
					t.Fatalf("project budget created a route budget record: %+v %v", records, err)
				}
				return
			}
			if err != nil || len(records) != 1 || records[0].Reason != reason {
				t.Fatalf("stop-loss record missing or duplicated: %+v err=%v", records, err)
			}
		})
	}
}

func TestManualResumeOverridesPreviousRouteStopLoss(t *testing.T) {
	for _, reason := range []string{"budget_exhausted", "no_information_limit", "needs_attention", "premise_invalidated"} {
		t.Run(reason, func(t *testing.T) {
			db := testDB(t)
			project := strategyProject(t, db)
			budget, limit := 1, 1
			if _, err := db.SetProjectBudget(t.Context(), project, budget); err != nil {
				t.Fatal(err)
			}
			route, err := db.CreateRoute(t.Context(), project, CreateRoute{Title: "retry route", NoProgressLimit: &limit})
			if err != nil {
				t.Fatal(err)
			}
			completed := strategyStep(t, db, project, &route.ID)
			result, err := db.ConcludeStep(t.Context(), project, completed.ID, Conclude{Worker: "test", Description: "saved evidence", Result: &ExecutionResult{Type: "progress", Detail: "full original evidence"}})
			if err != nil {
				t.Fatal(err)
			}
			pending := strategyStep(t, db, project, &route.ID)
			if _, err := db.SQL.Exec(`UPDATE routes SET invested_ms=2000,no_progress_count=1 WHERE project_id=? AND id=?`, project, route.ID); err != nil {
				t.Fatal(err)
			}
			status := "shelved"
			if _, err := db.UpdateRoute(t.Context(), project, route.ID, UpdateRoute{Status: &status, Reason: reason}); err != nil {
				t.Fatal(err)
			}
			if _, err := db.SetProjectStatus(t.Context(), project, "active"); err != nil {
				t.Fatal(err)
			}
			if err := db.RefreshRouteLimits(t.Context()); err != nil {
				t.Fatal(err)
			}
			detail, err := db.GetProject(t.Context(), project)
			if err != nil || detail.Project.Status != "active" || detail.Project.FailureReason != "" {
				t.Fatalf("manual resume failed: %+v err=%v", detail.Project, err)
			}
			got := detail.Routes[0]
			if got.Status != "active" || got.ShelvedReason != "" || got.NoProgressCount != 0 || detail.Project.BudgetSeconds != budget || detail.Project.BudgetSpentMilliseconds != 0 || got.InvestedMilliseconds != 2000 || got.ResultCount != 1 {
				t.Fatalf("resume did not grant a fresh window while retaining history: %+v", got)
			}
			fact, err := db.NodeDetail(t.Context(), project, "fact", result.Fact.ID)
			if err != nil || fact.(Fact).Detail != "full original evidence" || len(detail.Steps) != 2 {
				t.Fatal("resume changed previous evidence or steps")
			}
			if _, err := db.HeartbeatStep(t.Context(), project, pending.ID, "new-worker"); err != nil {
				t.Fatalf("resumed worker could not claim pending work: %v", err)
			}
			// Repeated active requests must not repeatedly grant extra budget.
			if _, err := db.SQL.Exec(`UPDATE routes SET invested_ms=2500 WHERE project_id=? AND id=?`, project, route.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.SetProjectStatus(t.Context(), project, "active"); err != nil {
				t.Fatal(err)
			}
			detail, err = db.GetProject(t.Context(), project)
			if err != nil || detail.Routes[0].BudgetSpentMilliseconds != 500 {
				t.Fatal("duplicate resume reset the current budget")
			}
			records, err := db.RouteRecords(t.Context(), project)
			if err != nil || len(records) != 2 || records[0].Reason != reason || records[1].Action != "reopened" || records[1].Reason != "operator_resume" {
				t.Fatalf("missing resume audit: %+v err=%v", records, err)
			}
			// Only fresh results or investment can trigger the next stop-loss.
			if reason == "no_information_limit" {
				_, err = db.ConcludeStep(t.Context(), project, pending.ID, Conclude{Worker: "new-worker", Description: "fresh no-information result", Result: &ExecutionResult{Type: "no_information"}})
			} else {
				_, err = db.SQL.Exec(`UPDATE routes SET invested_ms=3000 WHERE project_id=? AND id=?`, project, route.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := db.RefreshRouteLimits(t.Context()); err != nil {
				t.Fatal(err)
			}
			meta, err := db.GetProjectMeta(t.Context(), project)
			if err != nil || meta.Status != "failed" {
				t.Fatalf("fresh stop-loss was disabled: %+v err=%v", meta, err)
			}
		})
	}
}

func TestManualResumeSettlesPreviousExecutionBeforeReset(t *testing.T) {
	db := testDB(t)
	project := strategyProject(t, db)
	step := strategyStep(t, db, project, nil)
	start := time.Now().UTC().Add(-2 * time.Hour)
	end := start.Add(time.Minute)
	res, err := db.SQL.Exec(`INSERT INTO worker_task_history(worker,project_id,step_id,task_type,status,started_at,last_activity_at) VALUES('old-worker',?,?,'execute','running',?,?)`, project, step.ID, start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	history, _ := res.LastInsertId()
	if _, err := db.SQL.Exec(`INSERT INTO worker_execute_recovery(project_id,step_id,status,created_at,session_id,workspace_dir) VALUES(?,?,'summary_pending',?,'original-session','original-evidence-directory')`, project, step.ID, start.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	status := "shelved"
	if _, err := db.UpdateRoute(t.Context(), project, *step.RouteID, UpdateRoute{Status: &status, Reason: "premise_invalidated"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetProjectStatus(t.Context(), project, "active"); err != nil {
		t.Fatal(err)
	}
	if err := db.RefreshRouteLimits(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishWorkerTask(t.Context(), history, "failed", "late completion from previous run"); err != nil {
		t.Fatal(err)
	}
	if err := db.ReconcileInterruptedInvestments(t.Context()); err != nil {
		t.Fatal(err)
	}
	detail, err := db.GetProject(t.Context(), project)
	if err != nil || detail.Project.Status != "active" || detail.Routes[0].InvestedMilliseconds != 60000 || detail.Routes[0].BudgetSpentMilliseconds != 0 {
		t.Fatalf("old attempt consumed fresh budget: %+v err=%v", detail, err)
	}
	var session, workspace string
	if err := db.SQL.QueryRow(`SELECT session_id,workspace_dir FROM worker_execute_recovery WHERE project_id=? AND step_id=?`, project, step.ID).Scan(&session, &workspace); err != nil || session != "original-session" || workspace != "original-evidence-directory" {
		t.Fatalf("resume changed the recovery checkpoint: %v", err)
	}
}

func TestManualResumeResetsAllRoutesFromStoppedProject(t *testing.T) {
	path := t.TempDir() + "/resume.db"
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	project := strategyProject(t, db)
	for range 2 {
		step := strategyStep(t, db, project, nil)
		if _, err := db.CreateRoute(t.Context(), project, CreateRoute{Title: "another route", GoalID: step.GoalID}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.SetProjectStatus(t.Context(), project, "stopped"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE routes SET invested_ms=1300000,no_progress_count=3 WHERE project_id=?`, project); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetProjectStatus(t.Context(), project, "active"); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.SQL.Close() })
	if err := db.RefreshRouteLimits(t.Context()); err != nil {
		t.Fatal(err)
	}
	detail, err := db.GetProject(t.Context(), project)
	if err != nil || detail.Project.Status != "active" || len(detail.Routes) != 3 {
		t.Fatalf("resume did not persist across restart: %+v err=%v", detail, err)
	}
	for _, route := range detail.Routes {
		if route.BudgetSpentMilliseconds != 0 || route.NoProgressCount != 0 || route.InvestedMilliseconds != 1300000 {
			t.Fatalf("route was not reset: %+v", route)
		}
	}
}

func TestReopenCompletedProjectGrantsSharedBudgetAndPreservesStartupEvidence(t *testing.T) {
	db := testDB(t)
	project := strategyProject(t, db)
	state := readyTargetProbe(t, db, project, TargetEndpoint{Host: "ctf.example", Port: 443})
	if err := db.SavePreprocessing(t.Context(), project, state.RunID, PreprocessingState{Status: "ready", Pages: []PreprocessingPage{{URL: "https://ctf.example/", StatusCode: 200, ResponsePath: "/saved/response.json"}}}, "saved initial evidence", "full response"); err != nil {
		t.Fatal(err)
	}
	saved, err := db.TargetProbe(t.Context(), project)
	if err != nil {
		t.Fatal(err)
	}
	step := strategyStep(t, db, project, nil)
	result := strategyResult(t, db, project, step, "progress")
	if _, err := db.CompleteGoal(t.Context(), project, *step.GoalID, Completion{From: []string{result.Fact.ID}, Description: "confirmed completion", Worker: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE routes SET invested_ms=1300000,status='shelved',shelved_reason='budget_exhausted' WHERE project_id=?`, project); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Reopen(t.Context(), project, Reopen{Description: "operator asks for further verification", Creator: "human"}); err != nil {
		t.Fatal(err)
	}
	if err := db.RefreshRouteLimits(t.Context()); err != nil {
		t.Fatal(err)
	}
	detail, err := db.GetProject(t.Context(), project)
	if err != nil || detail.Project.Status != "active" || detail.Routes[0].Status != "active" || detail.Project.BudgetSpentMilliseconds != 0 || detail.Project.InvestedMilliseconds != 1300000 {
		t.Fatalf("reopened project failed due to old route budget: %+v err=%v", detail, err)
	}
	if detail.TargetProbe.RunID == saved.RunID || detail.TargetProbe.Status != "ready" || jsonData(detail.TargetProbe.Analysis) != jsonData(saved.Analysis) || jsonData(detail.TargetProbe.Preprocessing) != jsonData(saved.Preprocessing) {
		t.Fatalf("reopen discarded initial evidence or reused the old run: %+v", detail.TargetProbe)
	}
	if err := db.SavePreprocessing(t.Context(), project, saved.RunID, PreprocessingState{Status: "skipped"}, "", ""); err == nil {
		t.Fatal("old preprocessing result affected reopened project")
	}
}
