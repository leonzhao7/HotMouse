package core

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func strategyProject(t *testing.T, db *DB) string {
	t.Helper()
	p, err := db.CreateProject(t.Context(), CreateProject{Title: "Strategy", Origin: "authorized local target", Goal: "collect confirmed evidence"})
	if err != nil {
		t.Fatal(err)
	}
	return p.Project.ID
}
func strategyStep(t *testing.T, db *DB, p string, route *string, from ...string) Step {
	t.Helper()
	if len(from) == 0 {
		from = []string{"origin"}
	}
	goal := "g001"
	s, err := db.CreateStep(t.Context(), p, CreateStep{From: from, Description: "inspect authentication", Creator: "test", GoalID: &goal, RouteID: route})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func strategyResult(t *testing.T, db *DB, p string, s Step, kind string) ConcludeResult {
	t.Helper()
	r, err := db.ConcludeStep(t.Context(), p, s.ID, Conclude{Worker: "test", Description: "confirmed " + kind, Result: &ExecutionResult{Type: kind}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestModelContextPreservesPrerequisitesWithoutInjectingEvidence(t *testing.T) {
	db := testDB(t)
	p := strategyProject(t, db)
	goalText := strings.TrimSpace(strings.Repeat("goal condition ", 200))
	if _, err := db.UpdateGoal(t.Context(), p, "g001", UpdateGoal{Description: &goalText}); err != nil {
		t.Fatal(err)
	}
	source := strategyStep(t, db, p, nil)
	sourceText := strings.TrimSpace(strings.Repeat("required prerequisite ", 120))
	first, err := db.ConcludeStep(t.Context(), p, source.ID, Conclude{Worker: "test", Description: sourceText, Title: "Authentication source", Result: &ExecutionResult{Type: "progress", Detail: strings.Repeat("source-only-detail", 400)}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		step := strategyStep(t, db, p, nil)
		_, err := db.ConcludeStep(t.Context(), p, step.ID, Conclude{Worker: "test", Description: "unrelated archived observation", Result: &ExecutionResult{Type: "progress"}, Finding: &CreateFinding{Title: "archived evidence", Description: "old diagnostic log", Data: map[string]any{"raw": strings.Repeat("large-private-evidence", 400)}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	current := strategyStep(t, db, p, nil, first.Fact.ID)
	compact, err := db.ModelContext(t.Context(), p, "execute", current.ID)
	if err != nil {
		t.Fatal(err)
	}
	var view struct {
		CurrentStep modelStep   `yaml:"current_step"`
		Required    []modelFact `yaml:"required_facts"`
		Goals       []Goal      `yaml:"goals"`
	}
	if err := yaml.Unmarshal([]byte(compact), &view); err != nil {
		t.Fatal(err)
	}
	if view.CurrentStep.ID != current.ID || len(view.Goals) != 1 || view.Goals[0].Description != goalText {
		t.Fatalf("missing step/goal: %+v", view)
	}
	found := false
	for _, fact := range view.Required {
		if fact.ID == first.Fact.ID {
			found = fact.Description == sourceText && fact.HasDetail
		}
	}
	if !found || !strings.Contains(compact, "finding_ids:") || strings.Contains(compact, "large-private-evidence") || strings.Contains(compact, "source-only-detail") {
		t.Fatalf("bad context selection: %s", compact)
	}
	full, err := db.ExportYAML(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(full, "large-private-evidence") || !strings.Contains(full, "source-only-detail") || !strings.Contains(full, "id: "+current.ID) || len(compact) >= len(full)/4 {
		t.Fatalf("export/compact lengths: %d/%d", len(full), len(compact))
	}
	node, err := db.NodeDetail(t.Context(), p, "fact", first.Fact.ID)
	if err != nil || node.(Fact).Detail != first.Fact.Detail {
		t.Fatalf("detail missing: %+v %v", node, err)
	}
}
func TestRouteLossLimitsAndEnvironmentEvidence(t *testing.T) {
	db := testDB(t)
	p := strategyProject(t, db)
	limit := 2
	zero := 0
	goal := "g001"
	route, err := db.CreateRoute(t.Context(), p, CreateRoute{Title: "Authentication route", GoalID: &goal, NoProgressLimit: &limit, BudgetSeconds: &zero})
	if err != nil {
		t.Fatal(err)
	}
	queued := strategyStep(t, db, p, &route.ID)
	strategyResult(t, db, p, strategyStep(t, db, p, &route.ID), "no_information")
	environment := strategyResult(t, db, p, strategyStep(t, db, p, &route.ID), "environment_failure")
	if _, err := db.CompleteGoal(t.Context(), p, goal, Completion{From: []string{environment.Fact.ID}, Worker: "test", Description: "tools crashed"}); err == nil {
		t.Fatal("environment observation accepted as goal evidence")
	}
	strategyResult(t, db, p, strategyStep(t, db, p, &route.ID), "negative")
	strategyResult(t, db, p, strategyStep(t, db, p, &route.ID), "no_information")
	strategyResult(t, db, p, strategyStep(t, db, p, &route.ID), "no_information")
	detail, err := db.GetProject(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	got := detail.Routes[0]
	if got.Status != "shelved" || got.NoProgressCount != 2 || got.NegativeCount != 1 || got.EnvironmentFailureCount != 1 || got.ProgressCount != 0 {
		t.Fatalf("route counts: %+v", got)
	}
	if _, err := db.HeartbeatStep(t.Context(), p, queued.ID, "test"); err == nil {
		t.Fatal("shelved route allowed a fresh claim")
	}
	active := "active"
	if _, err := db.UpdateRoute(t.Context(), p, route.ID, UpdateRoute{Status: &active}); err == nil {
		t.Fatal("reopened without a reason")
	}
	reopened, err := db.UpdateRoute(t.Context(), p, route.ID, UpdateRoute{Status: &active, Reason: "different method", ReopenCondition: "new credentials", NextDirection: "validate credentials"})
	if err != nil || reopened.NoProgressCount != 0 {
		t.Fatalf("reopen: %+v %v", reopened, err)
	}
	records, err := db.RouteRecords(t.Context(), p)
	if err != nil || len(records) != 2 || records[0].Reason != "no_information_limit" || records[1].Action != "reopened" {
		t.Fatalf("records: %+v %v", records, err)
	}
}
func TestInvestmentIsIdempotentAndSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "investment.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p := strategyProject(t, db)
	budget := 1
	route, err := db.CreateRoute(t.Context(), p, CreateRoute{Title: "timed route", BudgetSeconds: &budget})
	if err != nil {
		t.Fatal(err)
	}
	step := strategyStep(t, db, p, &route.ID)
	started := time.Now().UTC().Add(-2 * time.Second).Format(time.RFC3339Nano)
	res, err := db.SQL.Exec(`INSERT INTO worker_task_history(worker,project_id,step_id,task_type,status,started_at) VALUES('test',?,?,'execute','running',?)`, p, step.ID, started)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if err := db.FinishWorkerTask(t.Context(), id, "failed", "missing tool"); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishWorkerTask(t.Context(), id, "failed", "same failure"); err != nil {
		t.Fatal(err)
	}
	before, err := db.GetProject(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if before.Routes[0].InvestedMilliseconds < 2000 || before.Routes[0].EnvironmentFailureCount != 1 || before.Routes[0].Status != "shelved" {
		t.Fatalf("investment: %+v", before.Routes)
	}
	// Retention cleanup must not erase cumulative route investment.
	if _, err := db.SQL.Exec(`DELETE FROM worker_task_history WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	after, err := db.GetProject(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Routes) != 1 || after.Routes[0].InvestedMilliseconds != before.Routes[0].InvestedMilliseconds {
		t.Fatalf("restart altered accounting: %+v", after.Routes)
	}
	active := "active"
	if _, err := db.UpdateRoute(t.Context(), p, route.ID, UpdateRoute{Status: &active, Reason: "retry"}); err == nil {
		t.Fatal("exhausted budget reopened")
	}
	bigger := 10
	if _, err := db.UpdateRoute(t.Context(), p, route.ID, UpdateRoute{Status: &active, BudgetSeconds: &bigger, Reason: "validated new method"}); err != nil {
		t.Fatal(err)
	}
}
func TestDecisionPlanRollsBackAndProtectsRecovery(t *testing.T) {
	db := testDB(t)
	p := strategyProject(t, db)
	step := strategyStep(t, db, p, nil)
	if _, err := db.HeartbeatStep(t.Context(), p, step.ID, "other"); err != nil {
		t.Fatal(err)
	}
	priority := 10
	goal := "g001"
	plan := DecisionPlan{Steps: []CreateStep{{From: []string{"origin"}, Description: "new step", GoalID: &goal, Creator: "test"}}, Goals: []CreateGoal{{Description: "new goal", Creator: "test"}}, StepUpdates: []PlanStepUpdate{{ID: step.ID, Change: UpdateStep{Priority: &priority}}}}
	if err := db.ApplyDecisionPlan(t.Context(), p, "test", plan); err == nil {
		t.Fatal("updated claimed step")
	}
	detail, _ := db.GetProject(t.Context(), p)
	if len(detail.Steps) != 1 || len(detail.Goals) != 1 {
		t.Fatalf("partial plan: %+v", detail)
	}
	if _, err := db.ReleaseStep(t.Context(), p, step.ID, "other"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO worker_execute_recovery(project_id,step_id,payload_json,status,created_at) VALUES(?,?,'{}','commit_pending',?)`, p, step.ID, now()); err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyDecisionPlan(t.Context(), p, "test", plan); err == nil {
		t.Fatal("updated a recovering step")
	}
	// Even when an earlier valid update runs, a bad later new step rolls it back.
	fresh := strategyStep(t, db, p, nil)
	plan = DecisionPlan{StepUpdates: []PlanStepUpdate{{ID: fresh.ID, Change: UpdateStep{Priority: &priority}}}, Steps: []CreateStep{{From: []string{"missing"}, Description: "invalid", Creator: "test"}}}
	if err := db.ApplyDecisionPlan(t.Context(), p, "test", plan); err == nil {
		t.Fatal("invalid source accepted")
	}
	var actual int
	if err := db.SQL.QueryRow(`SELECT priority FROM steps WHERE project_id=? AND id=?`, p, fresh.ID).Scan(&actual); err != nil || actual != 0 {
		t.Fatalf("priority leaked: %d %v", actual, err)
	}
}
func TestHistoryRecallAndCuratedCrossProjectExperience(t *testing.T) {
	db := testDB(t)
	p := strategyProject(t, db)
	other := strategyProject(t, db)
	first := strategyResult(t, db, p, strategyStep(t, db, p, nil), "negative")
	if _, err := db.SQL.Exec(`UPDATE facts SET title='认证 SQL injection',description='参数过滤',detail='single quote rejected' WHERE project_id=? AND id=?`, p, first.Fact.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO facts(id,project_id,description,title) VALUES('secret',?,'other project token','认证 SQL injection')`, other); err != nil {
		t.Fatal(err)
	}
	matches, err := db.SearchHistory(t.Context(), p, "认证 SQL", 5)
	if err != nil || len(matches) == 0 || matches[0].ID != first.Fact.ID || !matches[0].HasDetail {
		t.Fatalf("recall: %+v %v", matches, err)
	}
	for _, m := range matches {
		if m.ID == "secret" {
			t.Fatal("cross-project facts leaked")
		}
	}
	entry, err := db.CreateExperience(t.Context(), CreateExperience{Title: "SQL injection authentication", Conditions: "SQL-backed login", Method: "compare bounded error responses", FailureConditions: "parameterized queries", Detail: "full writeup", Tags: []string{"web", "sqli"}, SourceProjectID: &p, SourceStepID: &first.Step.ID, SourceFactIDs: []string{first.Fact.ID}})
	if err != nil {
		t.Fatal(err)
	}
	found, err := db.SearchExperiences(t.Context(), "SQL injection", "sqli", 5)
	if err != nil || len(found) != 1 || found[0].Detail != "" || found[0].ID != entry.ID {
		t.Fatalf("experience summaries: %+v %v", found, err)
	}
	full, err := db.GetExperience(t.Context(), entry.ID)
	if err != nil || full.Detail != "full writeup" {
		t.Fatalf("experience detail: %+v %v", full, err)
	}
	step := strategyStep(t, db, other, nil)
	if _, err := db.SQL.Exec(`UPDATE steps SET description='inspect SQL injection authentication' WHERE project_id=? AND id=?`, other, step.ID); err != nil {
		t.Fatal(err)
	}
	compact, err := db.ModelContext(t.Context(), other, "execute", step.ID)
	if err != nil || !strings.Contains(compact, "compare bounded error responses") || strings.Contains(compact, "full writeup") {
		t.Fatalf("experience injection: %s %v", compact, err)
	}
}
func TestFlagLedgerDeduplicatesAndReconcilesUnknownAttempts(t *testing.T) {
	db := testDB(t)
	p := strategyProject(t, db)
	step := strategyStep(t, db, p, nil)
	result, err := db.ConcludeStep(t.Context(), p, step.ID, Conclude{Worker: "test", Description: "captured flag", Result: &ExecutionResult{Type: "progress"}, Finding: &CreateFinding{Kind: "flag", Title: "flag", Description: "confirmed flag file", Data: map[string]any{"flag": "flag{ledger}", "challenge_id": "challenge-1"}}})
	if err != nil {
		t.Fatal(err)
	}
	flags, err := db.ListFlags(t.Context(), p)
	if err != nil || len(flags) != 1 {
		t.Fatalf("automatic capture: %+v %v", flags, err)
	}
	again, err := db.CaptureFlag(t.Context(), p, CaptureFlag{Value: "flag{ledger}", ChallengeID: "challenge-1", FactID: result.Fact.ID})
	if err != nil || again.ID != flags[0].ID {
		t.Fatalf("duplicate capture: %+v %v", again, err)
	}
	attempt, err := db.BeginFlagAttempt(t.Context(), p, again.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.BeginFlagAttempt(t.Context(), p, again.ID); err == nil {
		t.Fatal("parallel submission allowed")
	}
	unknown := ResolveFlagAttempt{Status: "submission_unknown", Response: "request timed out"}
	if _, err := db.ResolveFlagAttempt(t.Context(), p, again.ID, attempt.ID, unknown); err != nil {
		t.Fatal(err)
	}
	if _, err := db.BeginFlagAttempt(t.Context(), p, again.ID); err == nil {
		t.Fatal("unknown result blindly resubmitted")
	}
	accepted := ResolveFlagAttempt{Status: "accepted", Response: "platform confirms accepted"}
	if _, err := db.ResolveFlagAttempt(t.Context(), p, again.ID, attempt.ID, accepted); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ResolveFlagAttempt(t.Context(), p, again.ID, attempt.ID, accepted); err != nil {
		t.Fatal("idempotent resolution failed", err)
	}
	if _, err := db.ResolveFlagAttempt(t.Context(), p, again.ID, attempt.ID, unknown); err == nil {
		t.Fatal("accepted attempt changed")
	}
	flags, err = db.ListFlags(t.Context(), p)
	if err != nil || flags[0].Status != "accepted" || len(flags[0].Attempts) != 1 {
		t.Fatalf("ledger: %+v %v", flags, err)
	}
}
func TestStrategyMigrationBackfillsLegacyRouteInvestment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(schema); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	started := time.Now().UTC().Add(-3 * time.Second).Format(time.RFC3339Nano)
	for _, query := range []string{
		`INSERT INTO projects(id,title,created_at) VALUES('proj_001','legacy','2026-11-01T00:00:00Z')`,
		`INSERT INTO facts(id,project_id,description) VALUES('origin','proj_001','target')`,
		`INSERT INTO goals(id,project_id,description,created_at) VALUES('g001','proj_001','verify','2026-11-01T00:00:00Z')`,
		`INSERT INTO steps(id,project_id,description,goal_id,creator,created_at) VALUES('s001','proj_001','inspect','g001','test','2026-11-01T00:00:00Z')`,
	} {
		if _, err := raw.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := raw.Exec(`INSERT INTO worker_task_history(worker,project_id,step_id,task_type,status,started_at,finished_at) VALUES('test','proj_001','s001','execute','completed',?,?)`, started, stamp); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	detail, err := db.GetProject(context.Background(), "proj_001")
	if err != nil || len(detail.Routes) != 1 || detail.Steps[0].RouteID == nil || detail.Routes[0].InvestedMilliseconds < 2900 {
		t.Fatalf("migration: %+v %v", detail, err)
	}
}

func TestRouteInheritanceWithoutGoalAndAttentionRequiresManualReview(t *testing.T) {
	db := testDB(t)
	p := strategyProject(t, db)
	first := strategyResult(t, db, p, strategyStep(t, db, p, nil), "progress")
	next, err := db.CreateStep(t.Context(), p, CreateStep{From: []string{first.Fact.ID}, Description: "continue from evidence", Creator: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if next.GoalID == nil || *next.GoalID != "g001" || next.RouteID == nil || *next.RouteID != *first.Step.RouteID {
		t.Fatalf("lost inherited route/goal: %+v", next)
	}
	strategyResult(t, db, p, next, "needs_attention")
	active := "active"
	routeID := *next.RouteID
	update := UpdateRoute{Status: &active, Reason: "retry automatically"}
	if err := db.ApplyDecisionPlan(t.Context(), p, "test", DecisionPlan{RouteUpdates: []PlanRouteUpdate{{ID: routeID, Change: update}}}); err == nil {
		t.Fatal("planner bypassed manual attention")
	}
	update.Reason = "operator reviewed evidence"
	if _, err := db.UpdateRoute(t.Context(), p, routeID, update); err != nil {
		t.Fatal(err)
	}
}

func TestInterruptedInvestmentExcludesServiceDowntime(t *testing.T) {
	db := testDB(t)
	p := strategyProject(t, db)
	step := strategyStep(t, db, p, nil)
	start := time.Now().UTC().Add(-time.Hour)
	last := start.Add(10 * time.Second)
	res, err := db.SQL.Exec(`INSERT INTO worker_task_history(worker,project_id,step_id,task_type,status,started_at,last_activity_at) VALUES('test',?,?,'execute','running',?,?)`, p, step.ID, start.Format(time.RFC3339Nano), last.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if err := db.ReconcileInterruptedInvestments(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.ReconcileInterruptedInvestments(t.Context()); err != nil {
		t.Fatal(err)
	}
	detail, err := db.GetProject(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Routes[0].InvestedMilliseconds != 10000 || detail.Routes[0].EnvironmentFailureCount != 1 {
		t.Fatalf("counted downtime or duplicate failure: %+v", detail.Routes[0])
	}
	var status string
	if err := db.SQL.QueryRow(`SELECT status FROM worker_task_history WHERE id=?`, id).Scan(&status); err != nil || status != "interrupted" {
		t.Fatalf("orphan status: %q %v", status, err)
	}
}
