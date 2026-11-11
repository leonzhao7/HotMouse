package worker

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hotmouse/backend/internal/core"
	"hotmouse/backend/internal/ctfplatform"
)

func testWorkflowPython(t *testing.T) string {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	python, err = filepath.Abs(python)
	if err != nil {
		t.Fatal(err)
	}
	return python
}

func TestPreparationReusesAttachmentAndInventoryAcrossWorkers(t *testing.T) {
	python := testWorkflowPython(t)
	r, project := strategyWorker(t)
	step := workerStep(t, r, project, "solve supplied archive and checkpoint its result")
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	entry, _ := zw.Create("rsa/input.txt")
	_, _ = entry.Write([]byte("n=17\ne=3\nc=8\n"))
	_ = zw.Close()
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		downloads.Add(1)
		_, _ = w.Write(archive.Bytes())
	}))
	defer server.Close()
	challenge, _ := json.Marshal(ctfplatform.Challenge{ID: "rsa", FlagCount: 2})
	delivery, _ := json.Marshal(ctfplatform.Delivery{ChallengeID: "rsa", Attachments: []ctfplatform.Attachment{{Name: "task.zip", URL: server.URL + "/task.zip"}}})
	if _, err := r.DB.SQL.Exec(`INSERT INTO platform_projects(project_id,platform,scope_id,challenge_id,challenge_json,delivery_json) VALUES(?,'test','scope','rsa',?,?)`, project, string(challenge), string(delivery)); err != nil {
		t.Fatal(err)
	}
	graph, err := r.DB.ModelContext(t.Context(), project, "execute", step.ID)
	if err != nil {
		t.Fatal(err)
	}
	var first map[string]string
	for _, identity := range []string{"worker-one", "worker-two"} {
		cfg := r.config(t.Context())
		cfg.driver, cfg.command, cfg.identity = "command", "unused", identity
		cfg.env = map[string]string{"HOTMOUSE_PYTHON": python}
		workspace, err := r.ensureWorkspace(project, cfg, identity)
		if err != nil {
			t.Fatal(err)
		}
		vars, err := r.commandVars(t.Context(), project, step.ID, step.Description, graph, "execute", cfg, workspace)
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"HOTMOUSE_CAPABILITIES_FILE", "HOTMOUSE_PREFLIGHT_FILE", "HOTMOUSE_CTF_HELPER"} {
			if _, err := os.Stat(vars[key]); err != nil {
				t.Fatalf("missing %s: %v", key, err)
			}
		}
		if vars["HOTMOUSE_PYTHON"] != python || !strings.Contains(vars["HOTMOUSE_WORKER_PROMPT"], "rsa/input.txt") || !strings.Contains(vars["HOTMOUSE_WORKER_PROMPT"], "flag_count: 2") {
			t.Fatal("explicit interpreter, archive listing or platform requirements missing")
		}
		if first != nil && (vars["HOTMOUSE_PREFLIGHT_FILE"] != first["HOTMOUSE_PREFLIGHT_FILE"] || vars["HOTMOUSE_CAPABILITIES_FILE"] != first["HOTMOUSE_CAPABILITIES_FILE"]) {
			t.Fatal("subsequent worker did not reuse saved preparation")
		}
		first = vars
	}
	if downloads.Load() != 1 {
		t.Fatalf("attachment downloaded %d times", downloads.Load())
	}
	data, _ := os.ReadFile(first["HOTMOUSE_CAPABILITIES_FILE"])
	for _, namespace := range []string{"Crypto.Cipher.ChaCha20", "Cryptodome.Cipher.ChaCha20", "pycryptodomex", "pycryptodome"} {
		if !bytes.Contains(data, []byte(namespace)) {
			t.Fatalf("namespace mapping missing: %s", namespace)
		}
	}
	report, _ := os.ReadFile(first["HOTMOUSE_PREFLIGHT_FILE"])
	if !reusablePreparation(report) {
		t.Fatal("existing preparation is not reusable")
	}
	var prepared struct {
		Attachments []struct{ Path string }
	}
	_ = json.Unmarshal(report, &prepared)
	if err := os.Remove(prepared.Attachments[0].Path); err != nil {
		t.Fatal(err)
	}
	if reusablePreparation(report) {
		t.Fatal("missing attachment reused a stale preparation report")
	}
}

func TestWorkflowKeepsDedicatedVenvInterpreter(t *testing.T) {
	python := testWorkflowPython(t)
	dir := t.TempDir()
	venv := filepath.Join(dir, "api", "venv")
	if output, err := exec.Command(python, "-m", "venv", "--without-pip", venv).CombinedOutput(); err != nil {
		t.Skipf("venv unavailable: %v %s", err, output)
	}
	interpreter := filepath.Join(venv, "bin", "python")
	cfg := workerConfig{env: map[string]string{"HOTMOUSE_PYTHON": interpreter}}
	selected, err := workflowPython(cfg, dir)
	if err != nil || selected != interpreter {
		t.Fatalf("dedicated venv interpreter lost: %s %v", selected, err)
	}
	helper := filepath.Join(dir, "helper.py")
	if err := os.WriteFile(helper, fastWorkflowScript, 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(selected, helper, "capabilities").Output()
	var report struct{ Python, Prefix string }
	if err != nil || json.Unmarshal(output, &report) != nil || report.Python != interpreter || report.Prefix != venv {
		t.Fatalf("capabilities ran outside dedicated venv: %s %v", output, err)
	}
}

func TestCheckpointCapturedBeforeReportAndSurvivesInvalidResponse(t *testing.T) {
	python := testWorkflowPython(t)
	r, project := strategyWorker(t)
	step := workerStep(t, r, project, "solve and deliver every flag")
	marker := filepath.Join(t.TempDir(), "continue")
	script := filepath.Join(t.TempDir(), "solver.py")
	code := `import os, pathlib, subprocess, time
source = pathlib.Path(os.environ['HOTMOUSE_WORKSPACE_DIR']) / 'raw.txt'
source.write_text('first=flag{first}\nsecond=flag{second}\n')
for value in ['flag{first}', 'flag{second}']:
    subprocess.run([os.environ['HOTMOUSE_PYTHON'], os.environ['HOTMOUSE_CTF_HELPER'],
      'checkpoint', '--source', str(source), '--value', value, '--format-note',
      'task requires two flag{...} values', '--pattern', r'flag\{[a-z]+\}'], check=True,
      stdout=subprocess.DEVNULL)
source.write_text('output overwritten after checkpoint')
deadline = time.monotonic() + 10
while not pathlib.Path(os.environ['CHECKPOINT_TEST_MARKER']).exists() and time.monotonic() < deadline:
    time.sleep(.05)
print('{"accepted":true,"data":')
`
	if err := os.WriteFile(script, []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	env, _ := json.Marshal(map[string]string{"HOTMOUSE_PYTHON": python, "CHECKPOINT_TEST_SCRIPT": script, "CHECKPOINT_TEST_MARKER": marker})
	if _, err := r.DB.SQL.Exec(`UPDATE worker_config SET driver='command',command=?,environment_json=? WHERE id=1`, `"$HOTMOUSE_PYTHON" "$CHECKPOINT_TEST_SCRIPT"`, string(env)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := r.ExecuteNow(t.Context(), project, step.ID); done <- err }()
	t.Cleanup(func() { _ = os.WriteFile(marker, nil, 0o600) })
	deadline := time.After(25 * time.Second)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		flags, err := r.DB.ListFlags(t.Context(), project)
		if err != nil {
			t.Fatal(err)
		}
		if len(flags) == 2 {
			for _, flag := range flags {
				if flag.FactID == "origin" || flag.Status != "captured" {
					t.Fatalf("candidate lacks its own source or was treated as accepted: %+v", flag)
				}
			}
			break
		}
		select {
		case err := <-done:
			t.Fatalf("execution ended before capture: %v", err)
		case <-deadline:
			t.Fatal("checkpoints did not reach the ledger while report was pending")
		case <-tick.C:
		}
	}
	_ = os.WriteFile(marker, nil, 0o600)
	if err := <-done; err == nil {
		t.Fatal("invalid report unexpectedly parsed")
	}
	flags, _ := r.DB.ListFlags(t.Context(), project)
	if len(flags) != 2 {
		t.Fatal("report failure discarded durable candidates")
	}
	detail, _ := r.DB.GetProject(t.Context(), project)
	for _, fact := range detail.Facts {
		if fact.Title == "Flag checkpoint" {
			var checkpoint core.FlagCheckpoint
			_ = json.Unmarshal([]byte(fact.Detail), &checkpoint)
			raw, err := os.ReadFile(checkpoint.SourcePath)
			if err != nil || !bytes.Contains(raw, []byte(checkpoint.Value)) {
				t.Fatal("original raw result was not preserved")
			}
		}
	}
}

func TestCheckpointRejectsFabricatedOrChangedSource(t *testing.T) {
	python := testWorkflowPython(t)
	r, project := strategyWorker(t)
	step := workerStep(t, r, project, "capture")
	cfg := r.config(t.Context())
	cfg.identity = "test-worker"
	workspace, _ := r.ensureWorkspace(project, cfg, cfg.identity)
	helper := filepath.Join(workspace, "helper.py")
	_ = os.WriteFile(helper, fastWorkflowScript, 0o600)
	source := filepath.Join(workspace, "raw.txt")
	_ = os.WriteFile(source, []byte("flag{raw}"), 0o600)
	inbox := filepath.Join(workspace, ".hotmouse", "results", step.ID)
	checkpoint := func(value, pattern string) error {
		cmd := exec.Command(python, helper, "checkpoint", "--source", source, "--value", value, "--format-note", "specified by task", "--pattern", pattern)
		cmd.Env = append(os.Environ(), "HOTMOUSE_RESULT_INBOX="+inbox)
		return cmd.Run()
	}
	if checkpoint("flag{fabricated}", "") == nil || checkpoint("flag{raw}", `CTF\{.*\}`) == nil {
		t.Fatal("fabricated value or incorrect flag format was accepted")
	}
	if err := checkpoint("flag{raw}", `flag\{.*\}`); err != nil {
		t.Fatal(err)
	}
	paths, _ := filepath.Glob(filepath.Join(inbox, "*.candidate.json"))
	raw, _ := os.ReadFile(paths[0])
	var value core.FlagCheckpoint
	_ = json.Unmarshal(raw, &value)
	_ = os.WriteFile(value.SourcePath, []byte("flag{tampered}"), 0o600)
	if _, err := r.ingestFlagCheckpoint(context.Background(), project, step.ID, cfg.identity, paths[0]); err == nil {
		t.Fatal("changed raw evidence was accepted")
	}
}
