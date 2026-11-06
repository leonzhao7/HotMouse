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
			route, err := db.CreateRoute(t.Context(), project, CreateRoute{Title: "stop-loss route", GoalID: &goal, NoProgressLimit: &limit, BudgetSeconds: &budget})
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
			if err != nil || detail.Project.Status != "failed" || !strings.Contains(detail.Project.FailureReason, route.ID) || !strings.Contains(detail.Project.FailureReason, reason) || detail.Project.Decide != nil {
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
			if err != nil || len(records) != 1 || records[0].Reason != reason {
				t.Fatalf("stop-loss record missing or duplicated: %+v err=%v", records, err)
			}
		})
	}
}

func TestResumeRequiresRepairingShelvedRoute(t *testing.T) {
	db := testDB(t)
	project := strategyProject(t, db)
	step := strategyStep(t, db, project, nil)
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
	meta, _ := db.GetProjectMeta(t.Context(), project)
	if meta.Status != "failed" {
		t.Fatal("resume bypassed the shelved route")
	}
	status = "active"
	if _, err := db.UpdateRoute(t.Context(), project, *step.RouteID, UpdateRoute{Status: &status, Reason: "operator repaired premise"}); err != nil {
		t.Fatal(err)
	}
	meta, _ = db.GetProjectMeta(t.Context(), project)
	if meta.Status != "failed" {
		t.Fatal("route repair implicitly resumed project")
	}
	if _, err := db.SetProjectStatus(t.Context(), project, "active"); err != nil {
		t.Fatal(err)
	}
	if err := db.RefreshRouteLimits(t.Context()); err != nil {
		t.Fatal(err)
	}
	meta, _ = db.GetProjectMeta(t.Context(), project)
	if meta.Status != "active" {
		t.Fatal("repaired project could not resume")
	}
}
