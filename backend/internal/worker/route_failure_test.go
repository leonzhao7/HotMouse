package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestManualResumeAllowsPlanningAndPendingWorkerExecution(t *testing.T) {
	r, project := strategyWorker(t)
	step := workerStep(t, r, project, "continue previous pending work")
	if _, err := r.DB.SQL.Exec(`UPDATE routes SET status='shelved',shelved_reason='budget_exhausted',invested_ms=1300000 WHERE project_id=?`, project); err != nil {
		t.Fatal(err)
	}
	r.dispatchProject(t.Context(), project)
	if _, err := r.DB.SetProjectStatus(t.Context(), project, "active"); err != nil {
		t.Fatal(err)
	}
	fixtureTargetProbeNotRequired(t, r.DB, project)
	marker := filepath.Join(t.TempDir(), "tasks")
	env, _ := json.Marshal(map[string]string{"MARKER": marker})
	command := `printf '%s\n' "$HOTMOUSE_TASK_TYPE" >> "$MARKER"; if [ "$HOTMOUSE_TASK_TYPE" = execute ]; then printf '%s' '{"accepted":true,"data":{"description":"resumed result","result":{"type":"progress","detail":"fresh confirmed evidence"}}}'; else printf '%s' '{"accepted":true,"data":{}}'; fi`
	if _, err := r.DB.SQL.Exec(`UPDATE worker_config SET driver='command',command=?,environment_json=?`, command, string(env)); err != nil {
		t.Fatal(err)
	}
	r.dispatchProject(t.Context(), project)
	NewAgentWorker(r, "local/worker/01").RunOnce(t.Context())
	r.dispatchProject(t.Context(), project)
	detail, err := r.DB.GetProject(t.Context(), project)
	if err != nil || detail.Project.Status != "active" || len(detail.Steps) != 1 || detail.Steps[0].ID != step.ID || detail.Steps[0].To == nil {
		t.Fatalf("resumed project did not execute previous pending work: %+v err=%v", detail, err)
	}
	calls, err := os.ReadFile(marker)
	if err != nil || !strings.Contains(string(calls), "decide\n") || !strings.Contains(string(calls), "execute\n") {
		t.Fatalf("resumed project did not invoke planner and worker: %q err=%v", calls, err)
	}
}
