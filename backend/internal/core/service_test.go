package core

import (
	"context"
	"database/sql"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func testDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "hotmouse.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.SQL.Close() })
	return db
}

func TestConcludeProjectReopenPreservesEvidenceEdges(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	project, err := db.CreateProject(ctx, CreateProject{Title: "test", Origin: "target", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	pid := project.Project.ID
	goal := project.Goals[0].ID
	step, err := db.CreateStep(ctx, pid, CreateStep{From: []string{"origin"}, Description: "inspect", Creator: "test", GoalID: &goal})
	if err != nil {
		t.Fatal(err)
	}
	result, err := db.ConcludeStep(ctx, pid, step.ID, Conclude{Worker: "local", Description: "confirmed", Finding: &CreateFinding{Title: "issue", Description: "proof", Severity: "high", Kind: "security_vulnerability", Data: map[string]any{"evidence": []any{"request"}}}})
	if err != nil {
		t.Fatal(err)
	}
	var vulnID, sourceStep string
	var sourceFacts, evidence string
	if err := db.SQL.QueryRow(`SELECT id,source_step_id,source_fact_ids_json,evidence_json FROM vulnerabilities WHERE project_id=?`, pid).Scan(&vulnID, &sourceStep, &sourceFacts, &evidence); err != nil {
		t.Fatal(err)
	}
	if vulnID != "vuln_"+pid+"_v001" || sourceStep != step.ID || sourceFacts != "[\"origin\"]" || evidence != "[\"request\"]" {
		t.Fatalf("bad vulnerability projection: %s %s %s %s", vulnID, sourceStep, sourceFacts, evidence)
	}
	if _, err = db.CompleteGoal(ctx, pid, goal, Completion{From: []string{result.Fact.ID}, Description: "done", Worker: "local"}); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := db.SQL.QueryRow(`SELECT status FROM projects WHERE id=?`, pid).Scan(&status); err != nil || status != "completed" {
		t.Fatalf("project status=%q err=%v", status, err)
	}
	reopened, err := db.Reopen(ctx, pid, Reopen{Description: "feedback", Creator: "human"})
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Goal == nil || len(reopened.Goal.From) != 0 {
		t.Fatalf("goal not reset: %+v", reopened.Goal)
	}
	if len(reopened.Step.From) != 1 || reopened.Step.From[0] != result.Fact.ID {
		t.Fatalf("reopen step lost completion source: %+v", reopened.Step)
	}
	var n int
	if err := db.SQL.QueryRow(`SELECT count(*) FROM goal_sources WHERE project_id=?`, pid).Scan(&n); err != nil || n != 0 {
		t.Fatalf("goal source rows=%d err=%v", n, err)
	}
	var linked string
	if err := db.SQL.QueryRow(`SELECT fact_id FROM step_sources WHERE project_id=? AND step_id=?`, pid, reopened.Step.ID).Scan(&linked); err != nil || linked != result.Fact.ID {
		t.Fatalf("reopen edge=%q err=%v", linked, err)
	}
}

func TestExpiredStepLeaseCanBeReclaimedOnHeartbeat(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	p, err := db.CreateProject(ctx, CreateProject{Title: "test", Origin: "target", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := db.CreateStep(ctx, p.Project.ID, CreateStep{From: []string{"origin"}, Description: "inspect", Creator: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.HeartbeatStep(ctx, p.Project.ID, s.ID, "worker-a"); err != nil {
		t.Fatal(err)
	}
	_, err = db.SQL.Exec(`UPDATE steps SET last_heartbeat_at=? WHERE project_id=? AND id=?`, time.Now().UTC().Add(-time.Minute).Format("2006-01-02T15:04:05Z"), p.Project.ID, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := db.HeartbeatStep(ctx, p.Project.ID, s.ID, "worker-b")
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Worker == nil || *claimed.Worker != "worker-b" {
		t.Fatalf("lease not reclaimed: %+v", claimed.Worker)
	}
}

func TestProjectStepsExposeRecoveryStatus(t *testing.T) {
	db := testDB(t)
	project, err := db.CreateProject(t.Context(), CreateProject{Title: "recovery", Origin: "target", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	step, err := db.CreateStep(t.Context(), project.Project.ID, CreateStep{From: []string{"origin"}, Description: "inspect", Creator: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO worker_execute_recovery(project_id,step_id,status,created_at) VALUES(?,?,'needs_attention',?)`, project.Project.ID, step.ID, now()); err != nil {
		t.Fatal(err)
	}
	detail, err := db.GetProject(t.Context(), project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Steps) != 1 || detail.Steps[0].RecoveryStatus != "needs_attention" {
		t.Fatalf("step recovery status not exposed: %+v", detail.Steps)
	}
}

func TestProjectWorkerLimitDefaultsAndEnforcesClaims(t *testing.T) {
	db := testDB(t)
	project, err := db.CreateProject(t.Context(), CreateProject{Title: "limit", Origin: "target", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	if project.Project.MaxWorkers != 1 {
		t.Fatalf("default max workers=%d, want 1", project.Project.MaxWorkers)
	}
	first, err := db.CreateStep(t.Context(), project.Project.ID, CreateStep{From: []string{"origin"}, Description: "first", Creator: "test"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.CreateStep(t.Context(), project.Project.ID, CreateStep{From: []string{"origin"}, Description: "second", Creator: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.HeartbeatStep(t.Context(), project.Project.ID, first.ID, "worker-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.HeartbeatStep(t.Context(), project.Project.ID, second.ID, "worker-b"); err == nil || !strings.Contains(err.Error(), "worker limit") {
		t.Fatalf("second claim exceeded project worker limit: %v", err)
	}
	updated, err := db.SetProjectMaxWorkers(t.Context(), project.Project.ID, 2)
	if err != nil || updated.MaxWorkers != 2 {
		t.Fatalf("update project worker limit=%+v err=%v", updated, err)
	}
	if _, err := db.HeartbeatStep(t.Context(), project.Project.ID, second.ID, "worker-b"); err != nil {
		t.Fatalf("claim after increasing project limit: %v", err)
	}
	if _, err := db.SetProjectMaxWorkers(t.Context(), project.Project.ID, 33); err == nil {
		t.Fatal("accepted an out-of-range project worker limit")
	}
}

func TestFailedLoginCounterCommitsBeforeError(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	u, err := db.Register(ctx, "user", "secret123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.SQL.Exec(`UPDATE settings SET max_failed_login_attempts=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if _, _, err = db.Login(ctx, u.Username, "wrong"); err == nil {
		t.Fatal("wrong password accepted")
	}
	var failed int
	var locked string
	if err := db.SQL.QueryRow(`SELECT failed_attempts,locked_until FROM users WHERE id=?`, u.ID).Scan(&failed, &locked); err != nil {
		t.Fatal(err)
	}
	if failed != 0 || locked == "" {
		t.Fatalf("failed login was rolled back: failed=%d locked=%q", failed, locked)
	}
}

func TestRegisteredUserUsesUUIDStringID(t *testing.T) {
	db := testDB(t)
	user, err := db.Register(context.Background(), "protocol-user", "secret123")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^user_[0-9a-f]{32}$`).MatchString(user.ID) {
		t.Fatalf("user ID does not match user_<uuid> format: %q", user.ID)
	}
	var idType string
	if err := db.SQL.QueryRow(`SELECT type FROM pragma_table_info('users') WHERE name='id'`).Scan(&idType); err != nil || idType != "TEXT" {
		t.Fatalf("users.id type=%q err=%v", idType, err)
	}
}

func TestListProjectsUsesCreationOrderAndOpenStepCounts(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	first, err := db.CreateProject(ctx, CreateProject{Title: "first", Origin: "one", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.CreateProject(ctx, CreateProject{Title: "second", Origin: "two", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE projects SET created_at=CASE id WHEN ? THEN '2026-01-01T00:00:00Z' WHEN ? THEN '2026-01-02T00:00:00Z' END WHERE id IN (?,?)`, first.Project.ID, second.Project.ID, first.Project.ID, second.Project.ID); err != nil {
		t.Fatal(err)
	}
	step, err := db.CreateStep(ctx, first.Project.ID, CreateStep{From: []string{"origin"}, Description: "claimed", Creator: "test", Worker: strptr("worker")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO steps(id,project_id,description,creator,worker,created_at,abandoned) VALUES('s002',?,'abandoned','test','worker','2026-01-01T00:00:01Z',1)`, first.Project.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO steps(id,project_id,description,creator,worker,last_heartbeat_at,created_at) VALUES('s003',?,'expired','test','old-worker','2020-01-01T00:00:00Z','2020-01-01T00:00:00Z')`, first.Project.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE steps SET concluded_at='2026-01-01T00:00:02Z',to_fact_id='f001' WHERE project_id=? AND id=?`, first.Project.ID, step.ID); err != nil {
		t.Fatal(err)
	}
	projects, err := db.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 || projects[0].ID != first.Project.ID || projects[1].ID != second.Project.ID {
		t.Fatalf("projects not returned oldest first: %+v", projects)
	}
	if projects[0].WorkingStepCount != 0 || projects[0].UnclaimedStepCount != 1 {
		t.Fatalf("open step counts did not include the reclaimed lease: %+v", projects[0])
	}
	var worker, heartbeat sql.NullString
	if err := db.SQL.QueryRow(`SELECT worker,last_heartbeat_at FROM steps WHERE project_id=? AND id='s003'`, first.Project.ID).Scan(&worker, &heartbeat); err != nil {
		t.Fatal(err)
	}
	if worker.Valid || !heartbeat.Valid || heartbeat.String != "2020-01-01T00:00:00Z" {
		t.Fatalf("expired lease did not preserve heartbeat: worker=%+v heartbeat=%+v", worker, heartbeat)
	}
}

func TestStepUpdateAndDecideOnlyOperationsPreserveLeaseBoundaries(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	project, err := db.CreateProject(ctx, CreateProject{Title: "lease behavior", Origin: "target", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	rootGoal := project.Goals[0].ID
	if _, err := db.CreateGoal(ctx, project.Project.ID, CreateGoal{Description: "child", Creator: "test", ParentGoalID: &rootGoal}); err != nil {
		t.Fatal(err)
	}
	step, err := db.CreateStep(ctx, project.Project.ID, CreateStep{From: []string{"origin"}, Description: "inspect", Creator: "test", Worker: strptr("step-worker")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClaimDecide(ctx, project.Project.ID, ClaimDecide{Worker: "decide-worker", Trigger: "manual"}); err != nil {
		t.Fatal(err)
	}
	stale := "2020-01-01T00:00:00Z"
	if _, err := db.SQL.Exec(`UPDATE steps SET last_heartbeat_at=? WHERE project_id=? AND id=?`, stale, project.Project.ID, step.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE projects SET decide_last_heartbeat_at=? WHERE id=?`, stale, project.Project.ID); err != nil {
		t.Fatal(err)
	}

	priority := 5
	if _, err := db.UpdateStep(ctx, project.Project.ID, step.ID, UpdateStep{Priority: &priority}); err == nil {
		t.Fatal("claimed step update unexpectedly succeeded")
	}
	if _, err := db.GetProjectReadOnly(ctx, project.Project.ID); err != nil {
		t.Fatal(err)
	}
	assertWorkers := func(stepWorker, decideWorker string) {
		t.Helper()
		var gotStep, gotDecide sql.NullString
		if err := db.SQL.QueryRow(`SELECT worker FROM steps WHERE project_id=? AND id=?`, project.Project.ID, step.ID).Scan(&gotStep); err != nil {
			t.Fatal(err)
		}
		if err := db.SQL.QueryRow(`SELECT decide_worker FROM projects WHERE id=?`, project.Project.ID).Scan(&gotDecide); err != nil {
			t.Fatal(err)
		}
		if stepWorker == "" && gotStep.Valid || stepWorker != "" && (!gotStep.Valid || gotStep.String != stepWorker) {
			t.Fatalf("step worker=%+v, want %q", gotStep, stepWorker)
		}
		if decideWorker == "" && gotDecide.Valid || decideWorker != "" && (!gotDecide.Valid || gotDecide.String != decideWorker) {
			t.Fatalf("decide worker=%+v, want %q", gotDecide, decideWorker)
		}
	}
	assertWorkers("step-worker", "decide-worker")

	if _, err := db.SetProjectStatus(ctx, project.Project.ID, "active"); err != nil {
		t.Fatal(err)
	}
	assertWorkers("step-worker", "")

	if _, err := db.ClaimDecide(ctx, project.Project.ID, ClaimDecide{Worker: "decide-worker", Trigger: "manual"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE projects SET decide_last_heartbeat_at=? WHERE id=?`, stale, project.Project.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Reopen(ctx, project.Project.ID, Reopen{Description: "not completed", Creator: "test"}); err == nil {
		t.Fatal("active project reopened")
	}
	// The failed transaction rolls the lease expiry back with the rejected
	// reopen, matching the source endpoint's atomic transaction.
	assertWorkers("step-worker", "decide-worker")
	if _, err := db.CompleteGoal(ctx, project.Project.ID, rootGoal, Completion{From: []string{"origin"}, Description: "native goal completion", Worker: "human"}); err != nil {
		t.Fatal(err)
	}
	assertWorkers("step-worker", "decide-worker")
}

func strptr(value string) *string { return &value }

func TestGraphInputNormalizationAndCompletionBoundaries(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	project, err := db.CreateProject(ctx, CreateProject{Title: "graph", Origin: "target", Goal: "root", Hints: []CreateHint{{Content: "&lt;hint&gt;", Creator: " analyst "}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(project.Hints) != 1 || project.Hints[0].Content != "<hint>" || project.Hints[0].Creator != "analyst" {
		t.Fatalf("hint normalization mismatch: %+v", project.Hints)
	}
	if _, err := db.CreateProject(ctx, CreateProject{Title: "invalid", Origin: "target", Goal: "goal", Hints: []CreateHint{{Content: "hint", Creator: "   "}}}); err == nil {
		t.Fatal("blank project hint creator accepted")
	}
	step, err := db.CreateStep(ctx, project.Project.ID, CreateStep{From: []string{" origin "}, Description: "inspect", Creator: "test"})
	if err != nil || len(step.From) != 1 || step.From[0] != "origin" {
		t.Fatalf("fact id normalization failed: step=%+v err=%v", step, err)
	}
	if _, err := db.CreateStep(ctx, project.Project.ID, CreateStep{From: []string{"origin", " origin "}, Description: "duplicate", Creator: "test"}); err == nil {
		t.Fatal("duplicate fact ids accepted")
	}
	falseValue := false
	if _, err := db.UpdateStep(ctx, project.Project.ID, step.ID, UpdateStep{Abandoned: &falseValue}); err == nil {
		t.Fatal("abandoned=false update accepted")
	}
	claimed, err := db.HeartbeatStep(ctx, project.Project.ID, step.ID, " worker ")
	if err != nil || claimed.Worker == nil || *claimed.Worker != "worker" {
		t.Fatalf("worker value was not normalized: step=%+v err=%v", claimed, err)
	}
	released, err := db.ReleaseStep(ctx, project.Project.ID, step.ID, " worker ")
	if err != nil || released.Worker != nil || released.LastHeartbeatAt == nil {
		t.Fatalf("release did not preserve the last heartbeat: step=%+v err=%v", released, err)
	}
	if _, err := db.ConcludeStep(ctx, project.Project.ID, step.ID, Conclude{Worker: "worker", Description: "done"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ReleaseStep(ctx, project.Project.ID, step.ID, "worker"); err == nil {
		t.Fatal("concluded step was releasable")
	}
	subgoal, err := db.CreateGoal(ctx, project.Project.ID, CreateGoal{Description: "subgoal", ParentGoalID: &project.Goals[0].ID, Creator: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CompleteGoal(ctx, project.Project.ID, project.Goals[0].ID, Completion{From: []string{"f001"}, Description: "root done", Worker: "worker"}); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := db.SQL.QueryRow(`SELECT status FROM projects WHERE id=?`, project.Project.ID).Scan(&status); err != nil || status != "active" {
		t.Fatalf("root completion ignored active subgoal: status=%q err=%v", status, err)
	}
	if _, err := db.CompleteGoal(ctx, project.Project.ID, subgoal.ID, Completion{From: []string{"f001"}, Description: "subgoal done", Worker: "worker"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL.QueryRow(`SELECT status FROM projects WHERE id=?`, project.Project.ID).Scan(&status); err != nil || status != "completed" {
		t.Fatalf("all goals did not complete project: status=%q err=%v", status, err)
	}
}

func TestExportUsesProjectGraphShapeAndTimelineEvents(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	project, err := db.CreateProject(ctx, CreateProject{Title: "export", Origin: "target", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO hints(id,project_id,content,creator,created_at) VALUES('h001',?,?,?,?)`, project.Project.ID, "probe &gt; response &amp; body", "worker", now()); err != nil {
		t.Fatal(err)
	}

	yamlText, err := db.ExportYAML(ctx, project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	var exported struct {
		Project struct {
			Title  string `yaml:"title"`
			Origin string `yaml:"origin"`
		} `yaml:"project"`
		Hints []struct {
			Content string `yaml:"content"`
		} `yaml:"hints"`
	}
	if err := yaml.Unmarshal([]byte(yamlText), &exported); err != nil {
		t.Fatal(err)
	}
	if exported.Project.Title != "export" || exported.Project.Origin != "target" {
		t.Fatalf("unexpected exported project: %+v", exported.Project)
	}
	if len(exported.Hints) != 1 || exported.Hints[0].Content != "probe > response & body" {
		t.Fatalf("unexpected exported hints: %+v", exported.Hints)
	}

	timeline, err := db.ExportTimeline(ctx, project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"PROJECT CREATED", "GOAL CREATED", "HINT by worker"} {
		if !strings.Contains(timeline, marker) {
			t.Fatalf("timeline missing %q: %s", marker, timeline)
		}
	}
}
