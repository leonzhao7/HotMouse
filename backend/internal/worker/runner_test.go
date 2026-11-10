package worker

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hotmouse/backend/internal/core"
)

func TestAgentWorkerUsesIdentityAndLogsInvocation(t *testing.T) {
	db, err := core.Open(filepath.Join(t.TempDir(), "agent-worker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	project, err := db.CreateProject(t.Context(), core.CreateProject{Title: "parallel", Origin: "target", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	fixtureTargetProbeNotRequired(t, db, project.Project.ID)
	step, err := db.CreateStep(t.Context(), project.Project.ID, core.CreateStep{From: []string{"origin"}, Description: "inspect", Creator: "test"})
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "worker-id")
	env, _ := json.Marshal(map[string]string{"WORKER_ID_MARKER": marker})
	command := `printf '%s' "$HOTMOUSE_WORKER_ID" > "$WORKER_ID_MARKER"; printf '%s' '{"accepted":true,"data":{"description":"confirmed","result":{"type":"progress","detail":"evidence"}}}'`
	if _, err := db.SQL.Exec(`UPDATE worker_config SET name='local',driver='command',command=?,environment_json=?,worker_count=2 WHERE id=1`, command, string(env)); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	runner := New(db)
	runner.SetLogOutput(&logs)
	NewAgentWorker(runner, "local/worker/02").RunOnce(t.Context())
	identity, err := os.ReadFile(marker)
	if err != nil || string(identity) != "local/worker/02" {
		t.Fatalf("worker identity=%q err=%v", identity, err)
	}
	text := logs.String()
	for _, want := range []string{"process.start", project.Project.ID, step.ID, "local/worker/02", "binary=sh", "driver=command", "step.execute.start", "description=inspect", "step.result", "result_type=progress", "summary=confirmed", "detail=evidence", "status=submitted"} {
		if !strings.Contains(text, want) {
			t.Fatalf("invocation log missing %q: %s", want, text)
		}
	}
}

func TestDispatcherCommitsAgentWorkerResultToProjectGraph(t *testing.T) {
	db, err := core.Open(filepath.Join(t.TempDir(), "dispatcher.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	project, err := db.CreateProject(t.Context(), core.CreateProject{Title: "dispatcher", Origin: "target", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	fixtureTargetProbeNotRequired(t, db, project.Project.ID)
	step, err := db.CreateStep(t.Context(), project.Project.ID, core.CreateStep{From: []string{"origin"}, Description: "inspect", Creator: "test"})
	if err != nil {
		t.Fatal(err)
	}
	command := `if [ "$HOTMOUSE_TASK_TYPE" = decide ]; then printf '%s' '{"accepted":true,"data":{"steps":[{"from":["origin"],"description":"follow up","goal_id":"g001"}]}}'; else printf '%s' '{"accepted":true,"data":{"description":"confirmed","result":{"type":"progress","detail":"evidence"}}}'; fi`
	if _, err := db.SQL.Exec(`UPDATE worker_config SET driver='command',command=?,enabled=1 WHERE id=1`, command); err != nil {
		t.Fatal(err)
	}
	runner := New(db)
	var logs bytes.Buffer
	runner.SetLogOutput(&logs)
	NewAgentWorker(runner, "local/worker/01").RunOnce(t.Context())
	before, err := db.GetProject(t.Context(), project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Facts) != 1 || before.Steps[0].To != nil {
		t.Fatalf("agent worker mutated project graph: facts=%d step=%+v", len(before.Facts), before.Steps[0])
	}
	wantResponse := `{"accepted":true,"data":{"description":"confirmed","result":{"type":"progress","detail":"evidence"}}}`
	if before.Steps[0].LLMResponse != wantResponse {
		t.Fatalf("agent response was not saved before commit: %q", before.Steps[0].LLMResponse)
	}
	var status, worker string
	if err := db.SQL.QueryRow(`SELECT status,worker_name FROM worker_execute_recovery WHERE project_id=? AND step_id=?`, project.Project.ID, step.ID).Scan(&status, &worker); err != nil || status != "commit_pending" || worker != "local/worker/01" {
		t.Fatalf("saved agent result status=%q worker=%q err=%v", status, worker, err)
	}
	NewDispatcher(runner, project.Project.ID).RunOnce(t.Context())
	after, err := db.GetProject(t.Context(), project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Facts) != 2 || after.Steps[0].To == nil || after.Steps[0].Worker == nil || *after.Steps[0].Worker != "local/dispatcher/"+project.Project.ID {
		t.Fatalf("dispatcher did not commit agent result: facts=%d step=%+v", len(after.Facts), after.Steps[0])
	}
	if after.Steps[0].LLMResponse != wantResponse {
		t.Fatalf("dispatcher lost the original response: %q", after.Steps[0].LLMResponse)
	}
	var recoveryCount int
	if err := db.SQL.QueryRow(`SELECT count(*) FROM worker_execute_recovery WHERE project_id=? AND step_id=?`, project.Project.ID, step.ID).Scan(&recoveryCount); err != nil || recoveryCount != 0 {
		t.Fatalf("recovery row was not cleared after commit: count=%d err=%v", recoveryCount, err)
	}
	record, err := db.NodeDetail(t.Context(), project.Project.ID, "step", step.ID)
	if err != nil || record.(core.Step).LLMResponse != wantResponse {
		t.Fatalf("step detail lost the response: %+v err=%v", record, err)
	}
	var agentHistory, dispatcherHistory int
	if err := db.SQL.QueryRow(`SELECT count(*) FROM worker_task_history WHERE project_id=? AND worker='local/worker/01' AND status='submitted'`, project.Project.ID).Scan(&agentHistory); err != nil || agentHistory != 1 {
		t.Fatalf("agent submission history=%d err=%v", agentHistory, err)
	}
	if err := db.SQL.QueryRow(`SELECT count(*) FROM worker_task_history WHERE project_id=? AND worker=? AND task_type='dispatch_commit' AND status='completed'`, project.Project.ID, "local/dispatcher/"+project.Project.ID).Scan(&dispatcherHistory); err != nil || dispatcherHistory != 1 {
		t.Fatalf("dispatcher commit history=%d err=%v", dispatcherHistory, err)
	}
	NewDispatcher(runner, project.Project.ID).RunOnce(t.Context())
	planned, err := db.GetProject(t.Context(), project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	var plannedByDispatcher bool
	for _, plannedStep := range planned.Steps {
		if plannedStep.Description == "follow up" && plannedStep.Creator == "local/dispatcher/"+project.Project.ID {
			plannedByDispatcher = true
		}
	}
	if !plannedByDispatcher {
		t.Fatalf("dispatcher did not own project planning: %+v", planned.Steps)
	}
	for _, want := range []string{"step.committed", "fact=f001", "dispatcher.plan.start", "dispatcher.plan.committed", "dispatcher.step.planned", `description="follow up"`, "step=s002"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("progress omitted %q: %s", want, logs.String())
		}
	}
}

func TestRunnerStartsConfiguredParallelAgentWorkers(t *testing.T) {
	db, err := core.Open(filepath.Join(t.TempDir(), "parallel-workers.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	project, err := db.CreateProject(t.Context(), core.CreateProject{Title: "parallel", Origin: "target", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	fixtureTargetProbeNotRequired(t, db, project.Project.ID)
	if _, err := db.SQL.Exec(`UPDATE projects SET max_workers=2 WHERE id=?`, project.Project.ID); err != nil {
		t.Fatal(err)
	}
	for _, description := range []string{"inspect one", "inspect two"} {
		if _, err := db.CreateStep(t.Context(), project.Project.ID, core.CreateStep{From: []string{"origin"}, Description: description, Creator: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	workerIDs := filepath.Join(t.TempDir(), "worker-identities")
	env, _ := json.Marshal(map[string]string{"WORKER_IDS": workerIDs})
	command := `if [ "$HOTMOUSE_TASK_TYPE" = execute ]; then printf '%s\n' "$HOTMOUSE_WORKER_ID" >> "$WORKER_IDS"; attempt=0; while [ "$(wc -l < "$WORKER_IDS")" -lt 2 ] && [ "$attempt" -lt 150 ]; do attempt=$((attempt+1)); sleep 0.02; done; [ "$(wc -l < "$WORKER_IDS")" -ge 2 ] || exit 9; printf '%s' '{"accepted":true,"data":{"description":"confirmed","result":{"type":"progress","detail":"evidence"}}}'; else printf '{}'; fi`
	if _, err := db.SQL.Exec(`UPDATE worker_config SET driver='command',command=?,environment_json=?,worker_count=2,enabled=1 WHERE id=1`, command, string(env)); err != nil {
		t.Fatal(err)
	}
	runner := New(db)
	runner.Interval = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := runner.Start(ctx)
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			cancel()
			<-done
			contents, _ := os.ReadFile(workerIDs)
			detail, _ := db.GetProject(context.Background(), project.Project.ID)
			history, _ := db.SQL.Query(`SELECT worker,status,detail FROM worker_task_history ORDER BY id`)
			defer history.Close()
			var entries []string
			for history.Next() {
				var worker, status, detail string
				_ = history.Scan(&worker, &status, &detail)
				entries = append(entries, worker+":"+status+":"+detail)
			}
			t.Fatalf("parallel agent workers did not finish both steps: identities=%q steps=%+v history=%v", contents, detail.Steps, entries)
		case <-time.After(20 * time.Millisecond):
			detail, err := db.GetProject(t.Context(), project.Project.ID)
			if err != nil {
				cancel()
				<-done
				t.Fatal(err)
			}
			finished := len(detail.Steps) == 2
			for _, step := range detail.Steps {
				finished = finished && step.To != nil
			}
			if finished {
				cancel()
				<-done
				contents, err := os.ReadFile(workerIDs)
				if err != nil {
					t.Fatal(err)
				}
				identities := map[string]bool{}
				for _, identity := range strings.Fields(string(contents)) {
					identities[identity] = true
				}
				if len(identities) != 2 || !identities["local/worker/01"] || !identities["local/worker/02"] {
					t.Fatalf("configured workers did not execute in parallel: %q", contents)
				}
				return
			}
		}
	}
}

func TestDecideContractAppliesWrappedStepMutation(t *testing.T) {
	db, err := core.Open(filepath.Join(t.TempDir(), "worker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	ctx := context.Background()
	project, err := db.CreateProject(ctx, core.CreateProject{Title: "test", Origin: "target", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	fixtureTargetProbeNotRequired(t, db, project.Project.ID)
	graph, err := db.GetProject(ctx, project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"accepted":true,"data":{"steps":[{"from":["origin"],"description":"inspect target","goal_id":"g001","priority":3}]}}`)
	if err := applyDecide(db, ctx, project.Project.ID, graph, raw); err != nil {
		t.Fatal(err)
	}
	after, err := db.GetProject(ctx, project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Steps) != 1 || after.Steps[0].Description != "inspect target" || after.Steps[0].GoalID == nil || *after.Steps[0].GoalID != "g001" {
		t.Fatalf("decision not applied: %+v", after.Steps)
	}
}

func TestDefaultCommandsUseWorkerPrompt(t *testing.T) {
	for _, driver := range []string{"codex", "claude", "claudecode", "claude_code", "pi"} {
		prompt := "full prompt with quotes ' $() and newlines\nsecond line"
		args, err := providerArgv(workerConfig{driver: driver}, "execute", "test-session", t.TempDir(), prompt)
		if err != nil || args[len(args)-1] != prompt {
			t.Fatalf("%s lost prompt argv: %q err=%v", driver, args, err)
		}
	}
}

func TestDecodeObjectAcceptsFencedJSONWithSurroundingText(t *testing.T) {
	var got map[string]json.RawMessage
	raw := []byte("Result:\n```json\n{\"description\":\"confirmed\"}\n```\nDone.")
	if err := decodeObject(raw, &got); err != nil {
		t.Fatal(err)
	}
	var description string
	if err := json.Unmarshal(got["description"], &description); err != nil || description != "confirmed" {
		t.Fatalf("decoded description=%q err=%v", description, err)
	}
	if err := decodeObject([]byte("null"), &got); err == nil {
		t.Fatal("JSON null was accepted as an object")
	}
}

func TestRejectedExecuteIsRecordedAndReleasesLease(t *testing.T) {
	db, err := core.Open(filepath.Join(t.TempDir(), "worker-rejected.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	ctx := context.Background()
	project, err := db.CreateProject(ctx, core.CreateProject{Title: "test", Origin: "target", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	fixtureTargetProbeNotRequired(t, db, project.Project.ID)
	step, err := db.CreateStep(ctx, project.Project.ID, core.CreateStep{From: []string{"origin"}, Description: "inspect", Creator: "test"})
	if err != nil {
		t.Fatal(err)
	}
	command := `printf '%s' '{"accepted":false,"reason":"policy_refusal"}'`
	if _, err := db.SQL.Exec(`UPDATE worker_config SET name='agent-x',driver='command',command=? WHERE id=1`, command); err != nil {
		t.Fatal(err)
	}
	if _, err := New(db).ExecuteNow(ctx, project.Project.ID, step.ID); !isWorkerRejection(err) {
		t.Fatalf("expected rejected execution, got %v", err)
	}
	var status string
	if err := db.SQL.QueryRow(`SELECT status FROM worker_task_history WHERE step_id=?`, step.ID).Scan(&status); err != nil || status != "rejected" {
		t.Fatalf("history status=%q err=%v", status, err)
	}
	after, err := db.GetProject(ctx, project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Steps[0].Worker != nil {
		t.Fatalf("rejected step lease was not released: %q", *after.Steps[0].Worker)
	}
	if after.Steps[0].LLMResponse != `{"accepted":false,"reason":"policy_refusal"}` {
		t.Fatalf("rejection response was not retained: %q", after.Steps[0].LLMResponse)
	}
	New(db).RunOnce(ctx)
	var historyCount int
	if err := db.SQL.QueryRow(`SELECT count(*) FROM worker_task_history WHERE step_id=?`, step.ID).Scan(&historyCount); err != nil || historyCount != 1 {
		t.Fatalf("rejected task was retried during cooldown: history=%d err=%v", historyCount, err)
	}
	if _, err := db.SQL.Exec(`UPDATE worker_config SET driver='mock',command='' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := New(db).ExecuteNow(ctx, project.Project.ID, step.ID); err != nil {
		t.Fatalf("explicit retry after a provider rejection failed: %v", err)
	}
	var retryRows int
	if err := db.SQL.QueryRow(`SELECT count(*) FROM worker_retry_state WHERE project_id=? AND task_type='execute'`, project.Project.ID).Scan(&retryRows); err != nil || retryRows != 0 {
		t.Fatalf("successful retry left retry state rows=%d err=%v", retryRows, err)
	}
}

func TestInterruptedExecuteRequiresAttentionInsteadOfReplayingExternalAction(t *testing.T) {
	dir := t.TempDir()
	db, err := core.Open(filepath.Join(dir, "worker-backoff.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	ctx := context.Background()
	project, err := db.CreateProject(ctx, core.CreateProject{Title: "test", Origin: "target", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	fixtureTargetProbeNotRequired(t, db, project.Project.ID)
	step, err := db.CreateStep(ctx, project.Project.ID, core.CreateStep{From: []string{"origin"}, Description: "inspect", Creator: "test"})
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "provider-calls")
	env, err := json.Marshal(map[string]string{"HOTMOUSE_CALL_MARKER": marker})
	if err != nil {
		t.Fatal(err)
	}
	command := `if [ "$HOTMOUSE_TASK_TYPE" = execute ]; then printf x >> "$HOTMOUSE_CALL_MARKER"; fi; exit 1`
	if _, err := db.SQL.Exec(`UPDATE worker_config SET name='agent-x',driver='command',command=?,environment_json=? WHERE id=1`, command, string(env)); err != nil {
		t.Fatal(err)
	}
	runner := New(db)
	runner.RunOnce(ctx)
	var failures int
	var outcome, recoveryStatus string
	if err := db.SQL.QueryRow(`SELECT failure_count,last_outcome FROM worker_retry_state WHERE project_id=? AND task_type='execute'`, project.Project.ID).Scan(&failures, &outcome); err != nil || failures != 0 || outcome != "unhealthy" {
		t.Fatalf("retry state failures=%d outcome=%q err=%v", failures, outcome, err)
	}
	if err := db.SQL.QueryRow(`SELECT status FROM worker_execute_recovery WHERE project_id=? AND step_id=?`, project.Project.ID, step.ID).Scan(&recoveryStatus); err != nil || recoveryStatus != "needs_attention" {
		t.Fatalf("interrupted action recovery status=%q err=%v", recoveryStatus, err)
	}
	calls, err := os.ReadFile(marker)
	if err != nil || string(calls) != "x" {
		t.Fatalf("provider invocation marker=%q err=%v", calls, err)
	}
	runner.RunOnce(ctx)
	var historyCount int
	if err := db.SQL.QueryRow(`SELECT count(*) FROM worker_task_history WHERE step_id=?`, step.ID).Scan(&historyCount); err != nil || historyCount != 1 {
		t.Fatalf("operator-attention task was retried: history=%d err=%v", historyCount, err)
	}
	if _, err := runner.ExecuteNow(ctx, project.Project.ID, step.ID); err == nil || !strings.Contains(err.Error(), "cannot be replayed") {
		t.Fatalf("interrupted task was not blocked from explicit replay: %v", err)
	}
	calls, err = os.ReadFile(marker)
	if err != nil || string(calls) != "x" {
		t.Fatalf("provider action was replayed: marker=%q err=%v", calls, err)
	}
}

func TestDecideFailureCooldownSurvivesRunnerIteration(t *testing.T) {
	db, err := core.Open(filepath.Join(t.TempDir(), "worker-decide-backoff.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	ctx := context.Background()
	project, err := db.CreateProject(ctx, core.CreateProject{Title: "test", Origin: "target", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	fixtureTargetProbeNotRequired(t, db, project.Project.ID)
	if _, err := db.SQL.Exec(`UPDATE worker_config SET name='agent-x',driver='command',command='exit 1' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	runner := New(db)
	runner.RunOnce(ctx)
	var outcome string
	if err := db.SQL.QueryRow(`SELECT last_outcome FROM worker_retry_state WHERE project_id=? AND task_type='decide'`, project.Project.ID).Scan(&outcome); err != nil || outcome != "unhealthy" {
		t.Fatalf("decide retry outcome=%q err=%v", outcome, err)
	}
	runner.RunOnce(ctx)
	var historyCount int
	if err := db.SQL.QueryRow(`SELECT count(*) FROM worker_task_history WHERE project_id=?`, project.Project.ID).Scan(&historyCount); err != nil || historyCount != 1 {
		t.Fatalf("decide retried during health cooldown: history=%d err=%v", historyCount, err)
	}
}

func TestRunOnceReclaimsExpiredOpenStepLease(t *testing.T) {
	db, err := core.Open(filepath.Join(t.TempDir(), "worker-expired-lease.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	ctx := context.Background()
	project, err := db.CreateProject(ctx, core.CreateProject{Title: "test", Origin: "target", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	fixtureTargetProbeNotRequired(t, db, project.Project.ID)
	step, err := db.CreateStep(ctx, project.Project.ID, core.CreateStep{From: []string{"origin"}, Description: "inspect", Creator: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.HeartbeatStep(ctx, project.Project.ID, step.ID, "dead-worker"); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	if _, err := db.SQL.Exec(`UPDATE steps SET last_heartbeat_at=? WHERE project_id=? AND id=?`, stale, project.Project.ID, step.ID); err != nil {
		t.Fatal(err)
	}
	New(db).RunOnce(ctx)
	after, err := db.GetProject(ctx, project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Steps[0].Worker != nil {
		t.Fatalf("expired worker lease was not reclaimed: %q", *after.Steps[0].Worker)
	}
}

func TestExecuteRecoveryReplaysCommittedPayloadWithoutRunningCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker-recovery.db")
	db, err := core.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	ctx := context.Background()
	project, err := db.CreateProject(ctx, core.CreateProject{Title: "test", Origin: "target", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	fixtureTargetProbeNotRequired(t, db, project.Project.ID)
	step, err := db.CreateStep(ctx, project.Project.ID, core.CreateStep{From: []string{"origin"}, Description: "replay conclusion", Creator: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE worker_config SET name='agent-x',driver='command',command='exit 1' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	runner := New(db)
	payload := []byte(`{"accepted":true,"data":{"description":"recovered confirmed fact"}}`)
	if err := runner.saveStepResponse(project.Project.ID, step.ID, payload, nil); err != nil {
		t.Fatal(err)
	}
	if err := runner.saveExecuteRecovery(ctx, project.Project.ID, step.ID, payload); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = core.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	runner = New(db)
	runner.RunOnce(ctx)
	after, err := db.GetProject(ctx, project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Steps[0].To == nil || len(after.Facts) != 2 || after.Facts[1].Description != "recovered confirmed fact" {
		t.Fatalf("recovery payload was not committed: facts=%+v steps=%+v", after.Facts, after.Steps)
	}
	if after.Steps[0].LLMResponse != string(payload) {
		t.Fatalf("recovery lost the original response: %q", after.Steps[0].LLMResponse)
	}
	var recoveryCount int
	if err := db.SQL.QueryRow(`SELECT count(*) FROM worker_execute_recovery WHERE project_id=? AND step_id=?`, project.Project.ID, step.ID).Scan(&recoveryCount); err != nil || recoveryCount != 0 {
		t.Fatalf("recovery row was not cleared: count=%d err=%v", recoveryCount, err)
	}
}

func TestExecuteRecoveryClearsPayloadAfterConcludeCommittedBeforeRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker-commit-window.db")
	db, err := core.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	project, err := db.CreateProject(ctx, core.CreateProject{Title: "test", Origin: "target", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	fixtureTargetProbeNotRequired(t, db, project.Project.ID)
	step, err := db.CreateStep(ctx, project.Project.ID, core.CreateStep{From: []string{"origin"}, Description: "commit window", Creator: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE worker_config SET name='agent-x',driver='command',command='exit 1' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	conclusion := core.Conclude{Worker: "agent-x", Description: "already committed fact"}
	if _, err := db.ConcludeStep(ctx, project.Project.ID, step.ID, conclusion); err != nil {
		t.Fatal(err)
	}
	runner := New(db)
	payload := []byte(`{"accepted":true,"data":{"description":"already committed fact"}}`)
	if err := runner.saveStepResponse(project.Project.ID, step.ID, payload, nil); err != nil {
		t.Fatal(err)
	}
	if err := runner.saveExecuteRecovery(ctx, project.Project.ID, step.ID, payload); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = core.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	New(db).RunOnce(ctx)
	after, err := db.GetProject(ctx, project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Facts) != 2 || after.Facts[1].Description != "already committed fact" {
		t.Fatalf("conclude replay duplicated or lost the fact: %+v", after.Facts)
	}
	if after.Steps[0].LLMResponse != string(payload) {
		t.Fatalf("already committed step lost the recovered response: %q", after.Steps[0].LLMResponse)
	}
	var recoveryCount, historyCount int
	var status string
	if err := db.SQL.QueryRow(`SELECT count(*) FROM worker_execute_recovery WHERE project_id=? AND step_id=?`, project.Project.ID, step.ID).Scan(&recoveryCount); err != nil || recoveryCount != 0 {
		t.Fatalf("committed recovery row was not cleared: count=%d err=%v", recoveryCount, err)
	}
	if err := db.SQL.QueryRow(`SELECT count(*),max(status) FROM worker_task_history WHERE step_id=?`, step.ID).Scan(&historyCount, &status); err != nil || historyCount != 1 || status != "completed" {
		t.Fatalf("recovery history count=%d status=%q err=%v", historyCount, status, err)
	}
}

func TestDecideStoresFingerprintAfterGraphMutationAndUsesConfiguredWorker(t *testing.T) {
	db, err := core.Open(filepath.Join(t.TempDir(), "worker-fingerprint.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	ctx := context.Background()
	project, err := db.CreateProject(ctx, core.CreateProject{Title: "test", Origin: "target", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	fixtureTargetProbeNotRequired(t, db, project.Project.ID)
	initial, err := db.GetProject(ctx, project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	response := `{"steps":[{"from":["origin"],"description":"inspect target","goal_id":"g001","priority":2}]}`
	command := "printf '%s' '" + response + "'"
	if _, err := db.SQL.Exec(`UPDATE worker_config SET name='agent-x',driver='command',command=? WHERE id=1`, command); err != nil {
		t.Fatal(err)
	}
	New(db).decide(ctx)
	updated, err := db.GetProject(ctx, project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	var fingerprint string
	if err := db.SQL.QueryRow(`SELECT fingerprint FROM worker_decide_state WHERE project_id=?`, project.Project.ID).Scan(&fingerprint); err != nil {
		t.Fatal(err)
	}
	if fingerprint != graphFingerprint(updated) || fingerprint == graphFingerprint(initial) {
		t.Fatalf("stored fingerprint does not describe updated graph: stored=%s initial=%s updated=%s", fingerprint, graphFingerprint(initial), graphFingerprint(updated))
	}
	dispatcherIdentity := "agent-x/dispatcher/" + project.Project.ID
	if len(updated.Steps) != 1 || updated.Steps[0].Creator != dispatcherIdentity {
		t.Fatalf("step creator did not use configured worker: %+v", updated.Steps)
	}
	var leaseWorker sql.NullString
	if err := db.SQL.QueryRow(`SELECT decide_worker FROM projects WHERE id=?`, project.Project.ID).Scan(&leaseWorker); err != nil {
		t.Fatal(err)
	}
	if leaseWorker.Valid {
		t.Fatalf("decide lease was not released: %q", leaseWorker.String)
	}
	var historyWorker string
	if err := db.SQL.QueryRow(`SELECT worker FROM worker_task_history WHERE project_id=?`, project.Project.ID).Scan(&historyWorker); err != nil || historyWorker != dispatcherIdentity {
		t.Fatalf("history worker=%q err=%v", historyWorker, err)
	}
}

func TestExecuteContractRejectsGoalMutationAndUnknownFields(t *testing.T) {
	if _, err := parseExecute([]byte(`{"description":"result","complete":{"goal_id":"g001"}}`)); err == nil {
		t.Fatal("execute contract accepted a goal mutation")
	}
	if _, err := parseExecute([]byte(`{"description":"result","finding":{"title":"x","description":"y","fact_id":"origin"}}`)); err == nil {
		t.Fatal("execute contract accepted a finding fact_id override")
	}
	if _, err := parseExecute([]byte(`{"accepted":false,"reason":"policy_refusal"}`)); !isWorkerRejection(err) {
		t.Fatalf("expected rejected execution, got %v", err)
	}
	if _, err := parseExecute([]byte(`{"accepted":"no","data":{"description":"result"}}`)); err == nil || isWorkerRejection(err) {
		t.Fatalf("invalid accepted type was not rejected as malformed: %v", err)
	}
}

func TestExecuteUsesPerProjectWorkspaceAndContext(t *testing.T) {
	dir := t.TempDir()
	db, err := core.Open(filepath.Join(dir, "worker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	workspaceRoot := filepath.Join(dir, "workspaces")
	contextRoot := filepath.Join(dir, "context")
	if err := os.Mkdir(contextRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contextRoot, "default.project-context.yaml"), []byte("scope:\n  policy: test-policy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "command-env")
	contextMarker := filepath.Join(dir, "command-context")
	env, _ := json.Marshal(map[string]string{"HOTMOUSE_TEST_MARKER": marker, "HOTMOUSE_CONTEXT_MARKER": contextMarker})
	command := `printf '%s\n%s\n%s' "$PWD" "$HOTMOUSE_WORKSPACE_DIR" "$HOTMOUSE_PROJECT_CONTEXT_FILE" > "$HOTMOUSE_TEST_MARKER"; printf '%s' "$HOTMOUSE_PROJECT_CONTEXT_YAML" > "$HOTMOUSE_CONTEXT_MARKER"; printf '%s' '{"accepted":true,"data":{"description":"confirmed"}}'`
	if _, err := db.SQL.Exec(`UPDATE worker_config SET driver='command',command=?,environment_json=?,workspace_root=?,context_root=? WHERE id=1`, command, string(env), workspaceRoot, contextRoot); err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(context.Background(), core.CreateProject{Title: "workspace", Origin: "target", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	fixtureTargetProbeNotRequired(t, db, project.Project.ID)
	contextFile := filepath.Join(contextRoot, "projects", project.Project.ID+".project-context.yaml")
	createdContext, err := os.ReadFile(contextFile)
	if err != nil || !strings.Contains(string(createdContext), project.Project.ID) {
		t.Fatalf("project context was not created: err=%v body=%q", err, createdContext)
	}
	projectOverride := "schema_version: 1\noverride:\n  scope:\n    policy: project-policy\n    allowed:\n      - project-host\n"
	if err := os.WriteFile(contextFile, []byte(projectOverride), 0o600); err != nil {
		t.Fatal(err)
	}
	step, err := db.CreateStep(context.Background(), project.Project.ID, core.CreateStep{From: []string{"origin"}, Description: "inspect", Creator: "test"})
	if err != nil {
		t.Fatal(err)
	}
	runner := New(db)
	if _, err := runner.ExecuteNow(context.Background(), project.Project.ID, step.ID); err != nil {
		t.Fatal(err)
	}
	lines, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(strings.TrimSpace(string(lines)), "\n")
	expected := filepath.Join(workspaceRoot, project.Project.ID, "workers", workspaceDirectoryName("local/worker/01"))
	if len(parts) != 3 || parts[0] != expected || parts[1] != expected {
		t.Fatalf("command workspace values=%q expected=%q", parts, expected)
	}
	if parts[2] != contextFile {
		t.Fatalf("context path=%q expected=%q", parts[2], contextFile)
	}
	if _, err := os.Stat(filepath.Join(workspaceRoot, project.Project.ID, ".hotmouse-project")); err != nil {
		t.Fatalf("project workspace marker missing: %v", err)
	}
	otherWorkspace, err := runner.ensureWorkspace(project.Project.ID, runner.config(context.Background()), "local/worker/02")
	if err != nil || otherWorkspace == expected {
		t.Fatalf("workers did not receive isolated workspaces: first=%q second=%q err=%v", expected, otherWorkspace, err)
	}
	dispatcherWorkspace, err := runner.ensureWorkspace(project.Project.ID, runner.config(context.Background()), runner.dispatcherIdentity(project.Project.ID))
	if err != nil || dispatcherWorkspace == expected || dispatcherWorkspace == otherWorkspace {
		t.Fatalf("dispatcher did not receive an isolated workspace: agent1=%q agent2=%q dispatcher=%q err=%v", expected, otherWorkspace, dispatcherWorkspace, err)
	}
	mergedContext, err := os.ReadFile(contextMarker)
	if err != nil || !strings.Contains(string(mergedContext), "project-policy") || !strings.Contains(string(mergedContext), "project-host") || !strings.Contains(string(mergedContext), "target") {
		t.Fatalf("merged worker context missing: err=%v body=%q", err, mergedContext)
	}
}

func TestCompletedWorkspaceRemovalRequiresMarker(t *testing.T) {
	dir := t.TempDir()
	db, err := core.Open(filepath.Join(dir, "worker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	root := filepath.Join(dir, "workspaces")
	if _, err := db.SQL.Exec(`UPDATE worker_config SET workspace_root=?,completed_action='remove' WHERE id=1`, root); err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(context.Background(), core.CreateProject{Title: "cleanup", Origin: "target", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	fixtureTargetProbeNotRequired(t, db, project.Project.ID)
	runner := New(db)
	cfg := runner.config(context.Background())
	workspace, err := runner.ensureWorkspace(project.Project.ID, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "result.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CompleteGoal(context.Background(), project.Project.ID, "g001", core.Completion{From: []string{"origin"}, Description: "done", Worker: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("marked completed workspace was not removed: %v", err)
	}
	foreignProject, err := db.CreateProject(context.Background(), core.CreateProject{Title: "foreign", Origin: "target", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	fixtureTargetProbeNotRequired(t, db, foreignProject.Project.ID)
	foreign := filepath.Join(root, foreignProject.Project.ID)
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreign, "result.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CompleteGoal(context.Background(), foreignProject.Project.ID, "g001", core.Completion{From: []string{"origin"}, Description: "done", Worker: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(foreign, "result.txt")); err != nil {
		t.Fatalf("unmarked workspace was removed: %v", err)
	}
	crashedProject, err := db.CreateProject(context.Background(), core.CreateProject{Title: "recovery", Origin: "target", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	fixtureTargetProbeNotRequired(t, db, crashedProject.Project.ID)
	crashedWorkspace, err := runner.ensureWorkspace(crashedProject.Project.ID, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE projects SET status='completed' WHERE id=?`, crashedProject.Project.ID); err != nil {
		t.Fatal(err)
	}
	runner.cleanupCompletedWorkspaces(context.Background())
	if _, err := os.Stat(crashedWorkspace); !os.IsNotExist(err) {
		t.Fatalf("completed workspace was not recovered after worker restart window: %v", err)
	}
}

func TestDispatcherFailsStalledProjectAfterLastDecision(t *testing.T) {
	db, err := core.Open(filepath.Join(t.TempDir(), "stalled-project.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	project, err := db.CreateProject(t.Context(), core.CreateProject{Title: "stalled", Origin: "target", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	fixtureTargetProbeNotRequired(t, db, project.Project.ID)
	step, err := db.CreateStep(t.Context(), project.Project.ID, core.CreateStep{From: []string{"origin"}, Description: "inspect", Creator: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE routes SET status='shelved' WHERE project_id=? AND id=?`, project.Project.ID, *step.RouteID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO worker_execute_recovery(project_id,step_id,status,created_at) VALUES(?,?,'needs_attention',?)`, project.Project.ID, step.ID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE decide_events SET handled_at=? WHERE project_id=?`, time.Now().UTC().Format(time.RFC3339Nano), project.Project.ID); err != nil {
		t.Fatal(err)
	}
	detail, err := db.GetProject(t.Context(), project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := graphFingerprint(detail)
	withoutRecovery := detail
	withoutRecovery.Steps = append([]core.Step(nil), detail.Steps...)
	withoutRecovery.Steps[0].RecoveryStatus = ""
	if graphFingerprint(withoutRecovery) != fingerprint {
		t.Fatal("recovery display metadata changed the decision fingerprint")
	}
	if _, err := db.SQL.Exec(`INSERT INTO worker_decide_state(project_id,fingerprint,updated_at) VALUES(?,?,?)`, project.Project.ID, fingerprint, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	runner := New(db)
	runner.dispatchProject(t.Context(), project.Project.ID)
	updated, err := db.GetProject(t.Context(), project.Project.ID)
	if err != nil || updated.Project.Status != "failed" || updated.Project.FailureReason == "" {
		t.Fatalf("dispatcher left a stalled project active: project=%+v err=%v", updated.Project, err)
	}
}
