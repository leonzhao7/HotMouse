package worker

import (
	"encoding/json"
	"hotmouse/backend/internal/core"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeExecutionResponseIsPreservedBeforeValidationAndSummary(t *testing.T) {
	for _, test := range []struct {
		name, response, wantError string
	}{
		{"plain_text", "I'll start by reading the existing decode artifacts to understand what was produced.", "JSON object not found"},
		{"invalid_json", `{"accepted":true,"data":`, "invalid JSON response"},
		{"invalid_schema", `{"accepted":true,"data":{"description":"initial answer","unexpected":true}}`, `unexpected execute data key "unexpected"`},
		{"empty", "", "provider returned no completed assistant response"},
		{"valid", "  {\"accepted\":true,\"data\":{\"description\":\"initial answer\"}}\n", ""},
		{"valid_with_explanation", "{\"accepted\":true,\"data\":{\"description\":\"initial answer\"}}\n说明：附件 `source.py`，flag 格式 HTB{...}。", ""},
		{"valid_fenced_with_explanation", "```json\n{\"accepted\":true,\"data\":{\"description\":\"initial answer\"}}\n```\n说明：[web] 附件 `source.py`。", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			script := `printf '%s\n' "$HOTMOUSE_TASK_TYPE" >> "$TEST_CALLS"
if test "$HOTMOUSE_TASK_TYPE" = execute; then
  cat "$TEST_DIR/original-event.json"
else
  printf '%s\n' '{"type":"result","is_error":false,"result":"{\"accepted\":true,\"data\":{\"description\":\"recovery summary only\"}}\nExplanation: evidence in \u0060output.txt\u0060, format HTB{...}."}'
fi`
			db, project, step, marker := fakePi(t, script, core.DefaultWorkerRuntime())
			directory := filepath.Dir(marker)
			body, err := os.ReadFile(filepath.Join(directory, "pi"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "claude"), body, 0o700); err != nil {
				t.Fatal(err)
			}
			event, err := json.Marshal(map[string]any{"type": "result", "is_error": false, "result": test.response})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "original-event.json"), event, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
			if _, err := db.SQL.Exec("UPDATE worker_config SET driver='claude'"); err != nil {
				t.Fatal(err)
			}
			runner := New(db)
			_, executeErr := runner.executeForAgent(t.Context(), project, step, "bounded fixture", "local/worker/01")
			if (executeErr != nil) != (test.wantError != "") {
				t.Fatalf("execution error=%v wantError=%q", executeErr, test.wantError)
			}
			before, err := db.GetProject(t.Context(), project)
			if err != nil {
				t.Fatal(err)
			}
			if got := before.Steps[0]; !got.LLMResponseRecorded || got.LLMResponse != test.response || got.LLMResponseError != test.wantError {
				t.Fatalf("execution response not preserved before summary: %+v", got)
			}
			if _, err := runner.executeAsDispatcherCommit(t.Context(), project, step, "bounded fixture", "local/dispatcher/"+project); err != nil {
				t.Fatal(err)
			}
			path := db.Path
			if err := db.SQL.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := core.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.SQL.Close()
			after, err := reopened.GetProject(t.Context(), project)
			if err != nil {
				t.Fatal(err)
			}
			if got := after.Steps[0]; !got.LLMResponseRecorded || got.LLMResponse != test.response || got.LLMResponseError != test.wantError {
				t.Fatalf("summary or database reopen replaced execution response: %+v", got)
			}
			if test.wantError != "" && after.Facts[1].Description != "recovery summary only" {
				t.Fatalf("summary was not committed separately: %+v", after.Facts)
			}
			calls, err := os.ReadFile(marker)
			if err != nil {
				t.Fatal(err)
			}
			wantCalls := "execute\n"
			if test.wantError != "" {
				wantCalls += "execute_conclude\n"
			}
			if string(calls) != wantCalls {
				t.Fatalf("original action was replayed: %q", calls)
			}
		})
	}
}

func TestCodexAndClaudeResumeOnlySummary(t *testing.T) {
	for _, driver := range []string{"codex", "claude"} {
		t.Run(driver, func(t *testing.T) {
			// Use a fixture binary on PATH; no request is sent to a model provider.
			initial := "printf '%s\\n' '{\"type\":\"thread.started\",\"thread_id\":\"fixture-thread\"}'\nexit 1\n"
			summary := "case \"$*\" in *--dangerously-bypass-approvals-and-sandbox*) exit 9;; esac\ncase \"$*\" in *'resume'*'read-only'*'fixture-thread'*) ;; *) exit 8;; esac\nprintf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"{\\\"accepted\\\":true,\\\"data\\\":{\\\"description\\\":\\\"confirmed native fixture\\\"}}\"}}'\n"
			if driver == "claude" {
				initial = "while test $# -gt 0; do if test \"$1\" = --session-id; then shift; printf '%s' \"$1\" > \"$TEST_DIR/seed-session\"; break; fi; shift; done\nexit 1\n"
				summary = "while test $# -gt 0; do if test \"$1\" = --resume; then shift; test \"$1\" = \"$(cat \"$TEST_DIR/seed-session\")\" || exit 9; break; fi; shift; done\nprintf '%s\\n' '{\"type\":\"result\",\"is_error\":false,\"result\":\"{\\\"accepted\\\":true,\\\"data\\\":{\\\"description\\\":\\\"confirmed native fixture\\\"}}\"}'\n"
			}
			script := "printf '%s\\n' \"$HOTMOUSE_TASK_TYPE\" >> \"$TEST_CALLS\"\nif test \"$HOTMOUSE_TASK_TYPE\" = execute; then\n" + initial + "fi\n" + summary
			db, project, step, marker := fakePi(t, script, core.DefaultWorkerRuntime())
			dir := filepath.Dir(marker)
			body, err := os.ReadFile(filepath.Join(dir, "pi"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, driver), body, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			if _, err := db.SQL.Exec("UPDATE worker_config SET driver=?", driver); err != nil {
				t.Fatal(err)
			}
			result, err := New(db).ExecuteNow(t.Context(), project, step)
			if err != nil || result.Fact == nil || result.Fact.Description != "confirmed native fixture" {
				t.Fatalf("native recovery=%+v err=%v", result, err)
			}
			calls, _ := os.ReadFile(marker)
			if string(calls) != "execute\nexecute_conclude\n" {
				t.Fatalf("original native action repeated: %q", calls)
			}
		})
	}
}

func TestMalformedContextStopsProviderBeforeInvocation(t *testing.T) {
	db, project, step, marker := fakePi(t, piSummaryEvent, core.DefaultWorkerRuntime())
	root := filepath.Join(filepath.Dir(marker), "context")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "default.project-context.yaml"), []byte("scope: [unclosed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec("UPDATE worker_config SET context_root=?", root); err != nil {
		t.Fatal(err)
	}
	if _, err := New(db).ExecuteNow(t.Context(), project, step); err == nil || !strings.Contains(err.Error(), "context YAML") {
		t.Fatalf("invalid context accepted: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("provider ran with invalid context: %v", err)
	}
}

func TestDecideSkipsUnchangedAndHeldProjects(t *testing.T) {
	dir := t.TempDir()
	db, err := core.Open(filepath.Join(dir, "worker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	if _, err := db.SQL.Exec("UPDATE worker_config SET driver='command',command=?", "printf '%s' '{\"accepted\":true,\"data\":{\"steps\":[{\"from\":[\"origin\"],\"description\":\"bounded fixture\",\"goal_id\":\"g001\"}]}}'"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		p, err := db.CreateProject(t.Context(), core.CreateProject{Title: "fair scheduler", Origin: "fixture", Goal: "verify"})
		if err != nil {
			t.Fatal(err)
		}
		fixtureTargetProbeNotRequired(t, db, p.Project.ID)
		if i == 0 {
			if _, err := db.SQL.Exec("INSERT INTO worker_decide_state(project_id,fingerprint,updated_at) VALUES(?,?,'now')", p.Project.ID, graphFingerprint(p)); err != nil {
				t.Fatal(err)
			}
		}
		if i == 1 {
			step, err := db.CreateStep(t.Context(), p.Project.ID, core.CreateStep{From: []string{"origin"}, Description: "held for operator", Creator: "test"})
			if err != nil {
				t.Fatal(err)
			}
			if err := New(db).saveExecuteStarted(t.Context(), p.Project.ID, step.ID, "local"); err != nil {
				t.Fatal(err)
			}
			New(db).markExecuteAttention(p.Project.ID, step.ID)
		}
	}
	New(db).RunOnce(t.Context())
	var count int
	if err := db.SQL.QueryRow("SELECT count(*) FROM steps WHERE project_id='proj_003'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("later project was starved: count=%d err=%v", count, err)
	}
}
