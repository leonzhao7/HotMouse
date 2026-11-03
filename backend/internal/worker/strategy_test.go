package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hotmouse/backend/internal/core"
)

func strategyWorker(t *testing.T) (*Runner, string) {
	t.Helper()
	db, err := core.Open(filepath.Join(t.TempDir(), "strategy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.SQL.Close() })
	p, err := db.CreateProject(t.Context(), core.CreateProject{Title: "strategy", Origin: "target", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	return New(db), p.Project.ID
}
func workerStep(t *testing.T, r *Runner, p, description string) core.Step {
	t.Helper()
	goal := "g001"
	s, err := r.DB.CreateStep(t.Context(), p, core.CreateStep{From: []string{"origin"}, Description: description, Creator: "test", GoalID: &goal})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestEventDecideAdjustsQueueBeforeNextExecute(t *testing.T) {
	r, p := strategyWorker(t)
	first := workerStep(t, r, p, "inspect first")
	queued := workerStep(t, r, p, "obsolete premise")
	marker := filepath.Join(t.TempDir(), "tasks")
	env, _ := json.Marshal(map[string]string{"MARKER": marker})
	command := `printf '%s\n' "$HOTMOUSE_TASK_TYPE" >> "$MARKER"; if [ "$HOTMOUSE_TASK_TYPE" = execute ]; then printf '%s' '{"accepted":true,"data":{"description":"new credentials","result":{"type":"progress","detail":"confirmed source"}}}'; else printf '%s' '{"accepted":true,"data":{"step_updates":[{"id":"` + queued.ID + `","action":"abandon"}]}}'; fi`
	if _, err := r.DB.SQL.Exec(`UPDATE worker_config SET driver='command',command=?,environment_json=? WHERE id=1`, command, string(env)); err != nil {
		t.Fatal(err)
	}
	r.RunOnce(t.Context())
	r.RunOnce(t.Context())
	detail, err := r.DB.GetProject(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]core.Step{}
	for _, step := range detail.Steps {
		byID[step.ID] = step
	}
	if byID[first.ID].To == nil || !byID[queued.ID].Abandoned || byID[queued.ID].To != nil {
		t.Fatalf("queue was not revised: %+v", detail.Steps)
	}
	calls, err := os.ReadFile(marker)
	if err != nil || string(calls) != "execute\ndecide\n" {
		t.Fatalf("schedule order: %q %v", calls, err)
	}
	events, err := r.DB.PendingDecideEvents(t.Context(), p)
	if err != nil || len(events) != 0 {
		t.Fatalf("event not acknowledged: %+v %v", events, err)
	}
}
func TestEventDecideFailureRetainsEventAndNeverMutatesClaimedStep(t *testing.T) {
	r, p := strategyWorker(t)
	queued := workerStep(t, r, p, "claimed by other")
	if _, err := r.DB.HeartbeatStep(t.Context(), p, queued.ID, "other"); err != nil {
		t.Fatal(err)
	}
	if err := r.DB.RequestDecide(t.Context(), p, "premise invalidated"); err != nil {
		t.Fatal(err)
	}
	command := `printf '%s' '{"step_updates":[{"id":"` + queued.ID + `","action":"abandon"}]}'`
	if _, err := r.DB.SQL.Exec(`UPDATE worker_config SET driver='command',command=? WHERE id=1`, command); err != nil {
		t.Fatal(err)
	}
	r.RunOnce(t.Context())
	r.RunOnce(t.Context())
	events, err := r.DB.PendingDecideEvents(t.Context(), p)
	if err != nil || len(events) != 1 {
		t.Fatalf("failed event discarded: %+v %v", events, err)
	}
	detail, err := r.DB.GetProject(t.Context(), p)
	if err != nil || detail.Steps[0].Worker == nil || *detail.Steps[0].Worker != "other" || detail.Steps[0].Abandoned {
		t.Fatalf("claimed step changed: %+v %v", detail.Steps, err)
	}
	var count int
	if err := r.DB.SQL.QueryRow(`SELECT count(*) FROM worker_task_history WHERE task_type='decide'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("event retry cooldown ignored: %d %v", count, err)
	}
}
func TestStructuredResultRecoveryDoesNotReexecuteOrDoubleCount(t *testing.T) {
	r, p := strategyWorker(t)
	step := workerStep(t, r, p, "saved action")
	payload := []byte(`{"accepted":true,"data":{"description":"confirmed negative","title":"negative evidence","result":{"type":"negative","detail":"full original evidence","evidence":{"request":"recorded"}}}}`)
	conclude, err := parseExecute(payload)
	if err != nil {
		t.Fatal(err)
	}
	conclude.Worker = r.workerName()
	if _, err := r.DB.ConcludeStep(t.Context(), p, step.ID, conclude); err != nil {
		t.Fatal(err)
	}
	if err := r.saveExecuteRecovery(t.Context(), p, step.ID, payload); err != nil {
		t.Fatal(err)
	}
	r.RunOnce(t.Context())
	detail, err := r.DB.GetProject(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Facts) != 2 || detail.Routes[0].NegativeCount != 1 {
		t.Fatalf("duplicate recovery accounting: %+v", detail)
	}
	var count int
	if err := r.DB.SQL.QueryRow(`SELECT count(*) FROM worker_execute_recovery`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("checkpoint not cleared: %d %v", count, err)
	}
	mismatch := conclude
	mismatch.Result = &core.ExecutionResult{Type: "progress", Detail: conclude.Result.Detail, Evidence: conclude.Result.Evidence}
	if _, ok, err := r.committedConclusion(t.Context(), p, step.ID, mismatch); err != nil || ok {
		t.Fatalf("mismatched result verified: %v %v", ok, err)
	}
}
func TestStructuredExecuteTypesAndSchema(t *testing.T) {
	for _, kind := range []string{"progress", "negative", "blocked", "environment_failure", "no_information", "needs_attention"} {
		payload, _ := json.Marshal(map[string]any{"description": "summary", "result": map[string]any{"type": kind, "detail": "evidence"}})
		result, err := parseExecute(payload)
		if err != nil || result.Result == nil || result.Result.Type != kind {
			t.Fatalf("type %s: %+v %v", kind, result, err)
		}
	}
	for _, payload := range []string{`{"description":"x","result":null}`, `{"description":"x","result":{"type":"success"}}`, `{"description":"x","result":{"type":"progress","invented":"x"}}`} {
		if _, err := parseExecute([]byte(payload)); err == nil {
			t.Fatalf("accepted malformed result %s", payload)
		}
	}
	if !strings.Contains(executePrompt("yaml", "s001", "bounded task"), "environment_failure") {
		t.Fatal("prompt missing result classification")
	}
}
func TestDecideAcknowledgesOnlyEventsSeenAtStart(t *testing.T) {
	r, p := strategyWorker(t)
	if err := r.DB.RequestDecide(t.Context(), p, "first"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := r.DB.PendingDecideEvents(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.DB.RequestDecide(t.Context(), p, "arrived while planning"); err != nil {
		t.Fatal(err)
	}
	if err := r.DB.AcknowledgeDecideEvents(t.Context(), p, snapshot[len(snapshot)-1].ID); err != nil {
		t.Fatal(err)
	}
	pending, err := r.DB.PendingDecideEvents(t.Context(), p)
	if err != nil || len(pending) != 1 || pending[0].Reason != "arrived while planning" {
		t.Fatalf("new event lost: %+v %v", pending, err)
	}
}

func TestRecoveryTreatsEmptyAndOmittedEvidenceEqually(t *testing.T) {
	r, p := strategyWorker(t)
	step := workerStep(t, r, p, "empty evidence map")
	raw := []byte(`{"description":"confirmed","result":{"type":"progress","evidence":{}}}`)
	conclusion, err := parseExecute(raw)
	if err != nil {
		t.Fatal(err)
	}
	conclusion.Worker = r.workerName()
	if _, err := r.DB.ConcludeStep(t.Context(), p, step.ID, conclusion); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := r.committedConclusion(t.Context(), p, step.ID, conclusion); err != nil || !ok {
		t.Fatalf("empty evidence recovery mismatched: %v %v", ok, err)
	}
}
