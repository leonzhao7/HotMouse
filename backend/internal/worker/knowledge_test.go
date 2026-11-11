package worker

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"hotmouse/backend/internal/knowledge"
)

func TestCommandWorkerCanReadKnowledgeAndCommitResult(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	r, p := strategyWorker(t)
	s := workerStep(t, r, p, "inspect the supplied RSA crypto parameters")
	toolkit := toolkitFixture(t)
	script := filepath.Join(t.TempDir(), "read_knowledge.py")
	code := `import json, os, pathlib, subprocess, sys
assert os.environ['HOTMOUSE_SKILL'] == 'hotmouse-ctf'
root = pathlib.Path(os.environ['HOTMOUSE_KNOWLEDGE_DIR'])
entry = json.loads((root / 'entries/kb041.json').read_text())
assert entry['id'] == 'kb041' and entry['detail']
assert 'kb041.json' in os.environ['HOTMOUSE_WORKER_PROMPT']
assert pathlib.Path(os.environ['HOTMOUSE_SKILL_FILE']).is_file()
skills = pathlib.Path(os.environ['HOTMOUSE_SKILLS_DIR'])
assert 'name: web-exploit' in (skills / 'web-exploit/SKILL.md').read_text()
assert 'name: flag-hunting' in os.environ['HOTMOUSE_WORKER_PROMPT']
toolkit = pathlib.Path(os.environ['HOTMOUSE_PENTEST_TOOLKIT_DIR'])
assert str(toolkit) in os.environ['HOTMOUSE_WORKER_PROMPT']
decoded = subprocess.check_output([sys.executable, str(toolkit / 'weapons/decode.py'), 'aG90bW91c2U='], text=True).strip()
assert decoded == 'hotmouse'
evidence = pathlib.Path(os.environ['HOTMOUSE_WORKSPACE_DIR']) / 'lookup.json'
evidence.write_text(json.dumps({'reference_id': entry['id'], 'title': entry['title'], 'tool_result': decoded}))
print(json.dumps({'accepted': True, 'data': {'description': 'Offline crypto method available; no target assertion', 'result': {'type': 'progress', 'detail': str(evidence), 'evidence': {'reference_id': entry['id']}}}}))
`
	if err := os.WriteFile(script, []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	environment, _ := json.Marshal(map[string]string{"KNOWLEDGE_TEST_PYTHON": python, "KNOWLEDGE_TEST_SCRIPT": script, toolkitDirectoryVariable: toolkit})
	if _, err := r.DB.SQL.Exec(`UPDATE worker_config SET driver='command',command=?,environment_json=? WHERE id=1`, `"$KNOWLEDGE_TEST_PYTHON" "$KNOWLEDGE_TEST_SCRIPT"`, string(environment)); err != nil {
		t.Fatal(err)
	}
	result, err := r.ExecuteNow(t.Context(), p, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Step.To == nil || result.Step.Result == nil || result.Step.Result.Evidence["reference_id"] != "kb041" {
		t.Fatalf("knowledge-backed command result did not commit: %+v", result)
	}
	if _, err := os.ReadFile(result.Fact.Detail); err != nil {
		t.Fatal("command-produced artifact cannot be read", err)
	}
}

func TestCommandVarsBindSkillsAndReadableKnowledgeByRole(t *testing.T) {
	r, p := strategyWorker(t)
	s := workerStep(t, r, p, "Confirm SSRF with existing response evidence")
	for _, task := range []struct{ phase, task, skill string }{
		{"decide", "decide", "hotmouse-dispatcher"},
		{"execute", "execute", "hotmouse-ctf"},
	} {
		cfg := r.config(t.Context())
		cfg.identity = "test/" + task.task
		workspace, err := r.ensureWorkspace(p, cfg, cfg.identity)
		if err != nil {
			t.Fatal(err)
		}
		graph, err := r.DB.ModelContext(t.Context(), p, task.phase, s.ID)
		if err != nil {
			t.Fatal(err)
		}
		vars, err := r.commandVars(t.Context(), p, s.ID, s.Description, graph, task.task, cfg, workspace)
		if err != nil {
			t.Fatal(err)
		}
		if vars["HOTMOUSE_SKILL"] != task.skill || !strings.Contains(vars["HOTMOUSE_WORKER_PROMPT"], "name: "+task.skill) {
			t.Fatalf("role skill not bound: %s", task.task)
		}
		if !strings.HasPrefix(vars["HOTMOUSE_KNOWLEDGE_DIR"], workspace+string(filepath.Separator)) {
			t.Fatal("knowledge written outside worker workspace")
		}
		if _, err := os.ReadFile(vars["HOTMOUSE_SKILL_FILE"]); err != nil {
			t.Fatal("agent cannot read active skill", err)
		}
		for _, name := range []string{"flag-hunting", "web-exploit", "binary-pwn", "cloud-exploit"} {
			file := filepath.Join(vars["HOTMOUSE_SKILLS_DIR"], name, "SKILL.md")
			if _, err := os.ReadFile(file); err != nil || !strings.Contains(vars["HOTMOUSE_WORKER_PROMPT"], file) {
				t.Fatalf("specialist skill inaccessible: %s %v", file, err)
			}
		}
		if task.task == "execute" {
			var view struct {
				Knowledge []knowledge.Summary `yaml:"related_knowledge"`
			}
			if err := yaml.Unmarshal([]byte(graph), &view); err != nil {
				t.Fatal(err)
			}
			if len(view.Knowledge) == 0 || len(view.Knowledge) > 3 || view.Knowledge[0].ID != "kb006" {
				t.Fatalf("SSRF knowledge not recalled: %+v", view.Knowledge)
			}
			full := filepath.Join(vars["HOTMOUSE_KNOWLEDGE_DIR"], "entries", "kb006.json")
			if !strings.Contains(vars["HOTMOUSE_WORKER_PROMPT"], full) {
				t.Fatal("recalled entry cannot be located from prompt")
			}
			if _, err := os.ReadFile(full); err != nil {
				t.Fatal("full knowledge unavailable", err)
			}
		}
	}
}

func TestProbeAndRecoveryDoNotReceiveExecutionSkill(t *testing.T) {
	for _, task := range []string{"target_probe", "execute_conclude", "execute_conclude_format"} {
		vars := map[string]string{"HOTMOUSE_WORKER_PROMPT": "stage-specific contract"}
		if err := bindKnowledge(vars, task, ""); err != nil {
			t.Fatal(err)
		}
		if err := bindToolkit(vars, workerConfig{env: map[string]string{toolkitDirectoryVariable: "/missing/toolkit"}}, task); err != nil {
			t.Fatal("non-action stage tried to bind toolkit", err)
		}
		if vars["HOTMOUSE_SKILL"] != "" || vars["HOTMOUSE_WORKER_PROMPT"] != "stage-specific contract" {
			t.Fatalf("%s received action guidance", task)
		}
	}
}
