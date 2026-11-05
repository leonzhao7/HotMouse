package worker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hotmouse/backend/internal/core"
)

func fakePi(t *testing.T, script string, runtime core.WorkerRuntime) (*core.DB, string, string, string) {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "pi")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := core.Open(filepath.Join(dir, "worker.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.SQL.Close() })
	marker := filepath.Join(dir, "calls")
	env, _ := json.Marshal(map[string]string{"PI_CLI": binary, "TEST_CALLS": marker, "TEST_DIR": dir})
	limits, _ := json.Marshal(runtime)
	if _, err := db.SQL.Exec("UPDATE worker_config SET driver='pi',environment_json=?,runtime_json=? WHERE id=1", string(env), string(limits)); err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(t.Context(), core.CreateProject{Title: "provider", Origin: "local fixture", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	step, err := db.CreateStep(t.Context(), project.Project.ID, core.CreateStep{From: []string{"origin"}, Description: "bounded fixture", Creator: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return db, project.Project.ID, step.ID, marker
}

const piSummaryEvent = `printf '%s\n' '{"type":"message_end","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"{\"accepted\":true,\"data\":{\"description\":\"confirmed fixture result\"}}"}]}}'`

func TestProviderTimeoutSummarizesSessionAndKillsChildren(t *testing.T) {
	runtime := core.DefaultWorkerRuntime()
	runtime.ExecuteTimeoutSeconds = 1
	script := `printf '%s\n' "$HOTMOUSE_TASK_TYPE" >> "$TEST_CALLS"
case "$HOTMOUSE_TASK_TYPE" in
execute)
  printf '%s\n' '{"type":"session","id":"session-timeout"}'
  (sleep 3; printf replayed > "$TEST_DIR/late-action") &
  wait
  ;;
execute_conclude)
  case "$HOTMOUSE_WORKER_ID" in */dispatcher/*) ;; *) exit 8;; esac
  case "$*" in *"--tools read --session session-timeout"*) ;; *) exit 9;; esac
  ` + piSummaryEvent + `
  ;;
esac`
	db, project, step, marker := fakePi(t, script, runtime)
	result, err := New(db).ExecuteNow(t.Context(), project, step)
	if err != nil || result.Fact == nil || result.Fact.Description != "confirmed fixture result" {
		t.Fatalf("timeout summary result=%+v err=%v", result, err)
	}
	calls, _ := os.ReadFile(marker)
	if string(calls) != "execute\nexecute_conclude\n" {
		t.Fatalf("unexpected provider phases: %q", calls)
	}
	// A surviving child would write this marker three seconds after it started.
	timer := time.NewTimer(2200 * time.Millisecond)
	defer timer.Stop()
	<-timer.C
	if _, err := os.Stat(filepath.Join(filepath.Dir(marker), "late-action")); !os.IsNotExist(err) {
		t.Fatalf("timed-out child survived: %v", err)
	}
	var recoveryRows int
	if err := db.SQL.QueryRow("SELECT count(*) FROM worker_execute_recovery").Scan(&recoveryRows); err != nil || recoveryRows != 0 {
		t.Fatalf("finished recovery rows=%d err=%v", recoveryRows, err)
	}
}

func TestProviderSummaryRecoverySurvivesDatabaseReopenWithoutActionReplay(t *testing.T) {
	runtime := core.DefaultWorkerRuntime()
	runtime.FormatRetries = 0
	script := `printf '%s\n' "$HOTMOUSE_TASK_TYPE" >> "$TEST_CALLS"
if test "$HOTMOUSE_TASK_TYPE" = execute; then
  printf '%s\n' '{"type":"session","id":"session-persisted"}'
  exit 1
fi
if ! test -e "$TEST_DIR/summary-ready"; then exit 1; fi
` + piSummaryEvent
	db, project, step, marker := fakePi(t, script, runtime)
	if _, err := New(db).ExecuteNow(t.Context(), project, step); err == nil {
		t.Fatal("first summary unexpectedly succeeded")
	}
	var status, session string
	var attempts int
	if err := db.SQL.QueryRow("SELECT status,session_id,summary_attempts FROM worker_execute_recovery WHERE project_id=? AND step_id=?", project, step).Scan(&status, &session, &attempts); err != nil || status != "summary_pending" || session != "session-persisted" || attempts != 1 {
		t.Fatalf("checkpoint status=%s session=%s attempts=%d err=%v", status, session, attempts, err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(marker), "summary-ready"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec("DELETE FROM worker_retry_state"); err != nil {
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
	// The interrupted step's retry deadline survives restart; simulate expiry.
	if _, err := reopened.SQL.Exec(`UPDATE worker_execute_recovery SET retry_after='' WHERE project_id=? AND step_id=?`, project, step); err != nil {
		t.Fatal(err)
	}
	New(reopened).RunOnce(t.Context())
	detail, err := reopened.GetProject(t.Context(), project)
	if err != nil || detail.Steps[0].To == nil {
		t.Fatalf("recovered step was not concluded: %+v err=%v", detail.Steps, err)
	}
	calls, _ := os.ReadFile(marker)
	if string(calls) != "execute\nexecute_conclude\nexecute_conclude\n" {
		t.Fatalf("recovery repeated original action: %q", calls)
	}
}

func TestPiSummaryRepairsFormatWithOnlyReadTools(t *testing.T) {
	script := `printf '%s\n' "$HOTMOUSE_TASK_TYPE" >> "$TEST_CALLS"
case "$HOTMOUSE_TASK_TYPE" in
execute) printf '%s\n' '{"type":"session","id":"session-format"}'; exit 1 ;;
execute_conclude) printf '%s\n' '{"type":"message_end","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"{\"accepted\":true,\"data\":{\"description\":\"partial\"}"}]}}' ;;
execute_conclude_format)
 case "$*" in *"--tools read --session session-format"*) ;; *) exit 9;; esac
 ` + piSummaryEvent + ` ;;
esac`
	db, project, step, marker := fakePi(t, script, core.DefaultWorkerRuntime())
	if _, err := New(db).ExecuteNow(t.Context(), project, step); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(marker)
	if string(calls) != "execute\nexecute_conclude\nexecute_conclude_format\n" {
		t.Fatalf("unexpected format repair phases: %q", calls)
	}
}

func TestProviderRecoveryStopsOnIdentityChangeOrExhaustion(t *testing.T) {
	for _, mode := range []string{"identity", "exhausted"} {
		t.Run(mode, func(t *testing.T) {
			runtime := core.DefaultWorkerRuntime()
			runtime.FormatRetries = 0
			if mode == "exhausted" {
				runtime.RecoveryAttempts = 1
			}
			db, project, step, marker := fakePi(t, `printf '%s\n' "$HOTMOUSE_TASK_TYPE" >> "$TEST_CALLS"
if test "$HOTMOUSE_TASK_TYPE" = execute; then printf '%s\n' '{"type":"session","id":"held-session"}'; fi
exit 1`, runtime)
			if _, err := New(db).ExecuteNow(t.Context(), project, step); err == nil {
				t.Fatal("expected summary failure")
			}
			if mode == "identity" {
				var envJSON string
				if err := db.SQL.QueryRow("SELECT environment_json FROM worker_config").Scan(&envJSON); err != nil {
					t.Fatal(err)
				}
				var env map[string]string
				_ = json.Unmarshal([]byte(envJSON), &env)
				env["PI_MODEL"] = "changed-model"
				b, _ := json.Marshal(env)
				if _, err := db.SQL.Exec("UPDATE worker_config SET environment_json=?", string(b)); err != nil {
					t.Fatal(err)
				}
				if _, err := New(db).ExecuteNow(t.Context(), project, step); err == nil {
					t.Fatal("resumed incompatible session")
				}
			}
			var status string
			if err := db.SQL.QueryRow("SELECT status FROM worker_execute_recovery").Scan(&status); err != nil || status != "needs_attention" {
				t.Fatalf("status=%s err=%v", status, err)
			}
			calls, _ := os.ReadFile(marker)
			if strings.Count(string(calls), "execute\n") != 1 || string(calls) != "execute\nexecute_conclude\n" {
				t.Fatalf("held session invoked provider again: %q", calls)
			}
		})
	}
}

func TestCancelledProcessDoesNotRunSummary(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, project, step, marker := fakePi(t, `printf '%s\n' "$HOTMOUSE_TASK_TYPE" >> "$TEST_CALLS"
printf '%s\n' '{"type":"session","id":"cancel-session"}'
sleep 30`, core.DefaultWorkerRuntime())
	go func() { time.Sleep(150 * time.Millisecond); cancel() }()
	if _, err := New(db).ExecuteNow(ctx, project, step); err == nil {
		t.Fatal("cancelled execution succeeded")
	}
	calls, _ := os.ReadFile(marker)
	if string(calls) != "execute\n" {
		t.Fatalf("cancellation started summary: %q", calls)
	}
	var status string
	if err := db.SQL.QueryRow("SELECT status FROM worker_execute_recovery").Scan(&status); err != nil || status != "summary_pending" {
		t.Fatalf("cancelled session was lost: status=%s err=%v", status, err)
	}
}
