package worker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hotmouse/backend/internal/core"
)

func TestAmbiguousOrDamagedJSONCannotConclude(t *testing.T) {
	for _, response := range []string{
		"{\"accepted\":true,\"data\":{\"description\":\"partial\"}",
		"{\"accepted\":true,\"accepted\":false,\"data\":{\"description\":\"ambiguous\"}}",
		"{\"accepted\":true,\"data\":{\"description\":\"first\",\"description\":\"second\"}}",
		"{\"accepted\":true,\"data\":{\"description\":\"first\"}} {\"description\":\"second\"}",
		"Result:\n\x60\x60\x60json\n{\"description\":\"confirmed\"}\n\x60\x60\x60\n{\"description\":\"another\"}",
		"[{\"description\":\"nested object\"}]",
	} {
		var got map[string]json.RawMessage
		if err := decodeObject([]byte(response), &got); err == nil {
			t.Errorf("invalid response accepted as JSON: %q", response)
		}
		if _, err := parseExecute([]byte(response)); err == nil {
			t.Errorf("invalid response accepted as Fact: %q", response)
		}
	}
}

func TestWorkspaceOwnershipIsolationAndDeferredCleanup(t *testing.T) {
	root := t.TempDir()
	db, err := core.Open(filepath.Join(root, "first.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	other, err := core.Open(filepath.Join(root, "second.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.SQL.Close()
	if db.DefaultWorkspaceRoot() == other.DefaultWorkspaceRoot() {
		t.Fatal("independent databases share a default workspace")
	}
	shared := filepath.Join(root, "shared")
	if _, err := db.SQL.Exec("UPDATE worker_config SET workspace_root=?,completed_action='remove'", shared); err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(t.Context(), core.CreateProject{Title: "workspace", Origin: "fixture", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	fixtureTargetProbeNotRequired(t, db, project.Project.ID)
	runner := New(db)
	cfg := runner.config(t.Context())
	dir, err := runner.ensureWorkspace(project.Project.ID, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(other).ensureWorkspace(project.Project.ID, cfg); err == nil {
		t.Fatal("second database adopted the first database's workspace")
	}
	// Project completion during execution must defer removal until the worker exits.
	db.WorkerMu.Lock()
	_, err = db.CompleteGoal(t.Context(), project.Project.ID, "g001", core.Completion{From: []string{"origin"}, Description: "done", Worker: "fixture"})
	db.WorkerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("completion removed a running task's cwd: %v", err)
	}
	runner.cleanupCompletedWorkspaces(t.Context())
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("deferred cleanup did not remove owned workspace: %v", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.ensureWorkspace(project.Project.ID, cfg); err == nil {
		t.Fatal("adopted a pre-existing empty directory")
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.Symlink(target, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.ensureWorkspace(project.Project.ID, cfg); err == nil {
		t.Fatal("accepted a symlink workspace")
	}
}

func TestPiCustomProviderCredentialsRemainInPrivateFile(t *testing.T) {
	dir := t.TempDir()
	cfg := workerConfig{driver: "pi", env: map[string]string{"PI_CLI": "/fixture/pi", "PI_MODEL": "fixture-model", "PI_BASE_URL": "http://127.0.0.1:12345", "PI_API_KEY": "fixture-secret", "PI_PROVIDER_API": "openai", "PI_MODEL_CONTEXT_WINDOW": "64000"}}
	args, err := providerArgv(cfg, "execute", "", dir, "bounded prompt")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(args, " "), "fixture-secret") {
		t.Fatal("provider key appeared in argv")
	}
	path := filepath.Join(dir, ".hotmouse", "pi-agent", "models.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("provider config permissions=%v", info.Mode().Perm())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var models map[string]any
	if err := json.Unmarshal(body, &models); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "openai-completions") || !strings.Contains(string(body), "64000") {
		t.Fatal("custom provider configuration was not normalized")
	}
}

func TestNativeDecideUsesReadOnlyProviderPhase(t *testing.T) {
	script := "printf '%s\\n' \"$HOTMOUSE_TASK_TYPE\" >> \"$TEST_CALLS\"\ncase \"$*\" in *'--tools read'*) ;; *) exit 9;; esac\nprintf '%s\\n' '{\"type\":\"message_end\",\"message\":{\"role\":\"assistant\",\"stopReason\":\"stop\",\"content\":[{\"type\":\"text\",\"text\":\"{\\\"accepted\\\":true,\\\"data\\\":{\\\"steps\\\":[{\\\"from\\\":[\\\"origin\\\"],\\\"description\\\":\\\"bounded action\\\",\\\"goal_id\\\":\\\"g001\\\"}]}}\"}]}}'\n"
	db, project, step, marker := fakePi(t, script, core.DefaultWorkerRuntime())
	if _, err := db.SQL.Exec("DELETE FROM step_sources WHERE project_id=?; DELETE FROM steps WHERE project_id=?", project, project); err != nil {
		t.Fatal(err)
	}
	_ = step
	New(db).decide(context.Background())
	detail, err := db.GetProject(t.Context(), project)
	if err != nil || len(detail.Steps) != 1 || detail.Steps[0].Description != "bounded action" {
		t.Fatalf("native Decide=%+v err=%v", detail.Steps, err)
	}
	calls, _ := os.ReadFile(marker)
	if string(calls) != "decide\n" {
		t.Fatalf("native Decide phase=%q", calls)
	}
}
