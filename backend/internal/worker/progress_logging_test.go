package worker

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"hotmouse/backend/internal/core"
)

func TestFailedTaskLogIncludesContextAndRedactsSavedCredentials(t *testing.T) {
	t.Setenv("HOTMOUSE_LOG_LEVEL", "info")
	t.Setenv("HOTMOUSE_LOG_ENV", "0")
	db, err := core.Open(filepath.Join(t.TempDir(), "failure.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	project, err := db.CreateProject(t.Context(), core.CreateProject{Title: "failure", Origin: "target", Goal: "inspect"})
	if err != nil {
		t.Fatal(err)
	}
	fixtureTargetProbeNotRequired(t, db, project.Project.ID)
	step, err := db.CreateStep(t.Context(), project.Project.ID, core.CreateStep{From: []string{"origin"}, Description: "inspect response", Creator: "test"})
	if err != nil {
		t.Fatal(err)
	}
	environment, _ := json.Marshal(map[string]string{"LLM_API_KEY": "saved-worker-credential"})
	command := `printf '%s\n' "connection failed: $LLM_API_KEY" >&2; exit 7`
	if _, err := db.SQL.Exec(`UPDATE worker_config SET driver='command',command=?,environment_json=? WHERE id=1`, command, string(environment)); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	runner := New(db)
	runner.SetLogOutput(&output)
	NewAgentWorker(runner, "local/worker/01").RunOnce(t.Context())
	for _, expected := range []string{"task.finish", "level=ERROR", "status=failed", "project=" + project.Project.ID, "step=" + step.ID, "worker=local/worker/01", "task=execute", "summary=", "exit status 7", "connection failed", "[redacted]", "elapsed="} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("failure log omitted %q: %s", expected, output.String())
		}
	}
	if strings.Contains(output.String(), "saved-worker-credential") {
		t.Fatalf("task diagnostics exposed saved environment credential: %s", output.String())
	}
}
