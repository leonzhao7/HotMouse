package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"hotmouse/backend/internal/core"
)

func TestShelvedRoutePreventsPlanningDespitePendingEvents(t *testing.T) {
	for _, historical := range []bool{false, true} {
		t.Run(map[bool]string{false: "new stop-loss", true: "stored shelved route"}[historical], func(t *testing.T) {
			r, project := strategyWorker(t)
			step := workerStep(t, r, project, "stop-loss action")
			goal := "g001"
			independent, err := r.DB.CreateRoute(t.Context(), project, core.CreateRoute{Title: "independent", GoalID: &goal})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.DB.CreateStep(t.Context(), project, core.CreateStep{From: []string{"origin"}, Description: "independent executable work", Creator: "test", GoalID: &goal, RouteID: &independent.ID}); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(t.TempDir(), "invocations")
			env, _ := json.Marshal(map[string]string{"MARKER": marker})
			if _, err := r.DB.SQL.Exec(`UPDATE worker_config SET driver='command',command='printf invoked >> "$MARKER"; printf "{}"',environment_json=?`, string(env)); err != nil {
				t.Fatal(err)
			}
			if err := r.DB.RequestDecide(t.Context(), project, "event already pending"); err != nil {
				t.Fatal(err)
			}
			if historical {
				if _, err := r.DB.SQL.Exec(`UPDATE routes SET status='shelved',shelved_reason='budget_exhausted' WHERE project_id=? AND id=?`, project, *step.RouteID); err != nil {
					t.Fatal(err)
				}
			} else {
				limit := 1
				if _, err := r.DB.UpdateRoute(t.Context(), project, *step.RouteID, core.UpdateRoute{NoProgressLimit: &limit}); err != nil {
					t.Fatal(err)
				}
				if _, err := r.DB.ConcludeStep(t.Context(), project, step.ID, core.Conclude{Worker: "test", Description: "no new information", Result: &core.ExecutionResult{Type: "no_information", Detail: "saved response"}}); err != nil {
					t.Fatal(err)
				}
			}
			r.dispatchProject(t.Context(), project)
			r.RunOnce(t.Context())
			NewAgentWorker(r, "local/worker/01").RunOnce(t.Context())
			meta, err := r.DB.GetProjectMeta(t.Context(), project)
			if err != nil || meta.Status != "failed" {
				t.Fatalf("shelved project remained active: %+v err=%v", meta, err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("failed project invoked planner or worker: %v", err)
			}
		})
	}
}
