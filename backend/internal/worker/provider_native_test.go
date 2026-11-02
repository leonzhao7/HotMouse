package worker

import (
	"hotmouse/backend/internal/core"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
