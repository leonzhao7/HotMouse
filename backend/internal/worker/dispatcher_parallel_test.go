package worker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hotmouse/backend/internal/core"
)

func stepSnapshot(t *testing.T, r *Runner, project, id string) (core.Step, error) {
	t.Helper()
	detail, err := r.DB.GetProject(t.Context(), project)
	if err != nil {
		return core.Step{}, err
	}
	for _, step := range detail.Steps {
		if step.ID == id {
			return step, nil
		}
	}
	t.Fatalf("step %s is missing", id)
	return core.Step{}, nil
}

func awaitMarker(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("process did not reach marker %s", path)
}

func awaitTask(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("task did not finish")
	}
}

func TestWorkerClaimsDuringPlanningAndSurvivesAbandonment(t *testing.T) {
	r, project := strategyWorker(t)
	running := workerStep(t, r, project, "execute while planner is busy")
	queued := workerStep(t, r, project, "obsolete queued step")
	dir := t.TempDir()
	planMarker, workerMarker := filepath.Join(dir, "plan-started"), filepath.Join(dir, "worker-started")
	planRelease, workerRelease := filepath.Join(dir, "plan-release"), filepath.Join(dir, "worker-release")
	planPayload, _ := json.Marshal(map[string]any{"steps": []any{map[string]any{"from": []string{"origin"}, "description": "new independent action", "goal_id": "g001"}}, "step_updates": []any{map[string]any{"id": running.ID, "action": "abandon"}, map[string]any{"id": queued.ID, "action": "abandon"}}})
	dispatcher, _ := json.Marshal(core.DispatcherConfig{Driver: "command", Model: "planner-model", Command: `test "$ROLE_SECRET" = planner && test "$HOTMOUSE_LLM_MODEL" = planner-model || exit 11; printf started > "$MARKER"; while [ ! -f "$RELEASE" ]; do sleep 0.01; done; printf '%s' "$PAYLOAD"`, Environment: map[string]string{"ROLE_SECRET": "planner", "MARKER": planMarker, "RELEASE": planRelease, "PAYLOAD": string(planPayload)}})
	env, _ := json.Marshal(map[string]string{"ROLE_SECRET": "worker", "MARKER": workerMarker, "RELEASE": workerRelease})
	command := `test "$ROLE_SECRET" = worker || exit 12; printf started > "$MARKER"; while [ ! -f "$RELEASE" ]; do sleep 0.01; done; printf '%s' '{"accepted":true,"data":{"description":"running step completed","result":{"type":"progress","detail":"original execution preserved"}}}'`
	if _, err := r.DB.SQL.Exec(`UPDATE worker_config SET driver='command',command=?,environment_json=?,dispatcher_json=?`, command, string(env), string(dispatcher)); err != nil {
		t.Fatal(err)
	}
	if err := r.DB.RequestDecide(t.Context(), project, "obsolete premise"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	planDone, workerDone := make(chan struct{}), make(chan struct{})
	go func() { defer close(planDone); NewDispatcher(r, project).RunOnce(ctx) }()
	awaitMarker(t, planMarker)
	go func() { defer close(workerDone); NewAgentWorker(r, "local/worker/01").RunOnce(ctx) }()
	awaitMarker(t, workerMarker) // Cannot happen if the planning lease blocks claims.
	if err := os.WriteFile(planRelease, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	awaitTask(t, planDone)
	active, err := stepSnapshot(t, r, project, running.ID)
	if err != nil || active.Abandoned || active.Worker == nil || active.To != nil {
		t.Fatalf("planner altered running step: %+v err=%v", active, err)
	}
	obsolete, err := stepSnapshot(t, r, project, queued.ID)
	if err != nil || !obsolete.Abandoned || obsolete.Worker != nil {
		t.Fatalf("planner failed to abandon unclaimed step: %+v err=%v", obsolete, err)
	}
	detail, err := r.DB.GetProject(t.Context(), project)
	if err != nil || len(detail.Steps) != 3 {
		t.Fatalf("stale abandonment rejected the remaining plan: %+v err=%v", detail, err)
	}
	select {
	case <-workerDone:
		t.Fatal("planner stopped the running execution")
	default:
	}
	if err := os.WriteFile(workerRelease, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	awaitTask(t, workerDone)
	if !r.commitNextStepResult(t.Context(), project) {
		t.Fatal("running step result was lost")
	}
	active, err = stepSnapshot(t, r, project, running.ID)
	if err != nil || active.To == nil || active.Abandoned {
		t.Fatalf("running execution did not conclude: %+v err=%v", active, err)
	}
}

func TestDispatcherModelSelectionForNativeProviders(t *testing.T) {
	r, _ := strategyWorker(t)
	for _, driver := range []string{"codex", "claude", "pi"} {
		t.Run(driver, func(t *testing.T) {
			config, _ := json.Marshal(core.DispatcherConfig{Driver: driver, Model: "configured-model", Environment: map[string]string{"API_KEY": "planner-only"}})
			if _, err := r.DB.SQL.Exec(`UPDATE worker_config SET environment_json='{"WORKER_KEY":"worker-only"}',dispatcher_json=?`, string(config)); err != nil {
				t.Fatal(err)
			}
			cfg := r.dispatcherConfig(t.Context())
			if cfg.env["WORKER_KEY"] != "" || cfg.env["API_KEY"] != "planner-only" {
				t.Fatal("dispatcher inherited worker credentials")
			}
			args, err := providerArgv(cfg, "decide", "", t.TempDir(), "prompt")
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for i, arg := range args {
				if arg == "--model" && i+1 < len(args) && args[i+1] == "configured-model" {
					found = true
				}
			}
			if !found {
				t.Fatalf("model missing from provider invocation: %v", args)
			}
		})
	}
}

func TestStartupTargetAnalysisUsesIndependentDispatcher(t *testing.T) {
	r, project := startupWorker(t)
	config, _ := json.Marshal(core.DispatcherConfig{Driver: "command", Model: "target-analyst", Command: `test "$ROLE_SECRET" = planner && test "$HOTMOUSE_TASK_TYPE" = target_probe && test "$HOTMOUSE_LLM_MODEL" = target-analyst || exit 1; printf '%s' '{"required":true,"reason":"remote CTF service","targets":[{"host":"127.0.0.1","port":31337}]}'`, Environment: map[string]string{"ROLE_SECRET": "planner"}})
	if _, err := r.DB.SQL.Exec(`UPDATE worker_config SET driver='command',command='exit 2',environment_json='{"ROLE_SECRET":"worker"}',dispatcher_json=?`, string(config)); err != nil {
		t.Fatal(err)
	}
	r.probeDial = func(context.Context, core.TargetEndpoint) error { return nil }
	if ready, err := r.ensureStartup(t.Context(), project); err != nil || !ready {
		t.Fatalf("startup used Worker provider: %v", err)
	}
	state, err := r.DB.TargetProbe(t.Context(), project)
	if err != nil || !state.Analysis.Required || state.Preprocessing.Status != "skipped" {
		t.Fatalf("TCP challenge startup changed: %+v err=%v", state, err)
	}
}
