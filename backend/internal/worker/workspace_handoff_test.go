package worker

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func workspaceAccessView(t *testing.T, access string) map[string]any {
	t.Helper()
	_, locations, ok := strings.Cut(access, "\nproject_directory:")
	if !ok {
		t.Fatalf("workspace locations missing: %q", access)
	}
	var view map[string]any
	if err := yaml.Unmarshal([]byte("project_directory:"+locations), &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func TestPlanningWorkspaceDoesNotAdvertiseDispatcherOutputDirectory(t *testing.T) {
	r, p := strategyWorker(t)
	cfg := r.config(t.Context())
	cfg.identity = r.dispatcherIdentity(p)
	dispatcher, err := r.ensureWorkspace(p, cfg, cfg.identity)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dispatcher, "saved-evidence.txt")
	if err := os.WriteFile(source, []byte("existing evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	vars, err := r.commandVars(t.Context(), p, "", "", "graph", "decide", cfg, dispatcher)
	if err != nil {
		t.Fatal(err)
	}
	view := workspaceAccessView(t, vars["HOTMOUSE_WORKSPACE_ACCESS"])
	if _, ok := view["writable_directory"]; ok {
		t.Fatalf("planning advertised the dispatcher's writable directory: %v", view)
	}
	if view["step_output_directory"] != "${HOTMOUSE_WORKSPACE_DIR}" {
		t.Fatalf("future worker output directory was bound during planning: %v", view)
	}
	if !strings.Contains(vars["HOTMOUSE_WORKER_PROMPT"], "${HOTMOUSE_WORKSPACE_DIR}/<relative-path>") || !strings.Contains(vars["HOTMOUSE_WORKSPACE_ACCESS"], source) {
		t.Fatalf("planning lost output template or shared evidence: %v", vars)
	}
	if vars["HOTMOUSE_WORKSPACE_DIR"] != dispatcher {
		t.Fatal("planning runtime state must still use the dispatcher's own directory")
	}
}

func TestPlannedOutputsBindToAssignedWorkerAndReuseSharedInputs(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh is not installed")
	}
	r, p := strategyWorker(t)
	cfg := r.config(t.Context())
	cfg.identity = r.dispatcherIdentity(p)
	dispatcher, err := r.ensureWorkspace(p, cfg, cfg.identity)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := r.ensureWorkspace(p, cfg, "local/worker/01")
	if err != nil {
		t.Fatal(err)
	}
	sources := []string{filepath.Join(dispatcher, "saved-evidence.txt"), filepath.Join(peer, "peer-evidence.txt")}
	for _, path := range sources {
		if err := os.WriteFile(path, []byte("evidence\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Single-quote source paths safely; the output placeholder stays literal in
	// the persisted plan, including when a command quotes it as a filename.
	quote := func(path string) string { return "'" + strings.ReplaceAll(path, "'", "'\\''") + "'" }
	description := "cat " + quote(sources[0]) + " " + quote(sources[1]) + " > '${HOTMOUSE_WORKSPACE_DIR}/result.txt'"
	detail, err := r.DB.GetProject(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"accepted": true, "data": map[string]any{"steps": []any{map[string]any{"from": []string{"origin"}, "description": description, "goal_id": "g001"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := applyDecide(r.DB, t.Context(), p, detail, payload, cfg.identity); err != nil {
		t.Fatal(err)
	}
	detail, err = r.DB.GetProject(t.Context(), p)
	if err != nil || len(detail.Steps) != 1 || detail.Steps[0].Description != description {
		t.Fatalf("planned description was prematurely bound: %+v err=%v", detail.Steps, err)
	}
	step := detail.Steps[0]
	graph, err := r.DB.ModelContext(t.Context(), p, "execute", step.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, identity := range []string{"local/worker/01", "local/worker/02"} {
		t.Run(identity, func(t *testing.T) {
			workerCfg := cfg
			workerCfg.identity = identity
			workspace, err := r.ensureWorkspace(p, workerCfg, identity)
			if err != nil {
				t.Fatal(err)
			}
			vars, err := r.commandVars(t.Context(), p, step.ID, step.Description, graph, "execute", workerCfg, workspace)
			if err != nil {
				t.Fatal(err)
			}
			want := "cat " + quote(sources[0]) + " " + quote(sources[1]) + " > '" + filepath.Join(workspace, "result.txt") + "'"
			if vars["HOTMOUSE_STEP_DESCRIPTION"] != want || !strings.Contains(vars["HOTMOUSE_WORKER_PROMPT"], "Current Step Description: "+want) {
				t.Fatalf("step output not bound to assigned worker: %v", vars)
			}
			if vars["HOTMOUSE_GRAPH_YAML"] != graph || vars["HOTMOUSE_MODEL_CONTEXT_YAML"] != graph {
				t.Fatal("binding changed the recorded graph/evidence")
			}
			view := workspaceAccessView(t, vars["HOTMOUSE_WORKSPACE_ACCESS"])
			if view["writable_directory"] != workspace {
				t.Fatalf("execution workspace mismatch: %v", view)
			}
			result := runProcess(t.Context(), []string{shell, "-c", vars["HOTMOUSE_STEP_DESCRIPTION"]}, nil, workspace, vars, 5*time.Second, nil)
			if result.err != nil {
				t.Fatalf("assigned worker could not execute planned output: %v stderr=%q", result.err, result.stderr)
			}
			data, err := os.ReadFile(filepath.Join(workspace, "result.txt"))
			if err != nil || string(data) != "evidence\nevidence\n" {
				t.Fatalf("shared input handoff failed: data=%q err=%v", data, err)
			}
		})
	}
	for _, source := range sources {
		data, err := os.ReadFile(source)
		if err != nil || string(data) != "evidence\n" {
			t.Fatalf("shared evidence changed: %s data=%q err=%v", source, data, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dispatcher, "result.txt")); !os.IsNotExist(err) {
		t.Fatalf("worker output went into the dispatcher's directory: %v", err)
	}
	detail, err = r.DB.GetProject(t.Context(), p)
	if err != nil || detail.Steps[0].Description != description {
		t.Fatalf("execution binding changed the persisted plan: %+v err=%v", detail.Steps, err)
	}
}

func TestRecoveryWorkspacePreservesOriginalEvidenceLocations(t *testing.T) {
	r, p := strategyWorker(t)
	cfg := r.config(t.Context())
	cfg.identity = r.dispatcherIdentity(p)
	dispatcher, err := r.ensureWorkspace(p, cfg, cfg.identity)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := r.ensureWorkspace(p, cfg, "local/worker/01")
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(worker, "confirmed.txt")
	if err := os.WriteFile(source, []byte("saved evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	description := "Read " + source + "; write '${HOTMOUSE_WORKSPACE_DIR}/summary.txt'"
	vars, err := r.commandVars(t.Context(), p, "s001", description, "saved graph", "execute_conclude", cfg, dispatcher)
	if err != nil {
		t.Fatal(err)
	}
	view := workspaceAccessView(t, vars["HOTMOUSE_WORKSPACE_ACCESS"])
	if _, ok := view["writable_directory"]; ok {
		t.Fatalf("summary advertised a new output directory: %v", view)
	}
	if vars["HOTMOUSE_STEP_DESCRIPTION"] != description || !strings.Contains(vars["HOTMOUSE_WORKSPACE_ACCESS"], source) {
		t.Fatal("summary rebound a planned output or lost the worker's saved evidence")
	}
	if !strings.Contains(vars["HOTMOUSE_WORKSPACE_ACCESS"], "do not relocate evidence, create outputs, or perform new actions") {
		t.Fatal("summary workspace guidance permitted new actions")
	}
}
