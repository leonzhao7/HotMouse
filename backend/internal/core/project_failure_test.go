package core

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestProjectFailsOnlyAfterItsPlanHasNoRunnableWork(t *testing.T) {
	db := testDB(t)
	project, err := db.CreateProject(t.Context(), CreateProject{Title: "stalled", Origin: "target", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	step, err := db.CreateStep(t.Context(), project.Project.ID, CreateStep{From: []string{"origin"}, Description: "inspect", Creator: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO worker_decide_state(project_id,fingerprint,updated_at) VALUES(?,?,?)`, project.Project.ID, "current", now()); err != nil {
		t.Fatal(err)
	}
	if failed, err := db.FailProjectIfStalled(t.Context(), project.Project.ID, "current", "no progress"); err != nil || failed {
		t.Fatalf("project failed while a runnable step existed: failed=%v err=%v", failed, err)
	}
	if _, err := db.SQL.Exec(`UPDATE routes SET status='shelved' WHERE project_id=?`, project.Project.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO worker_execute_recovery(project_id,step_id,status,created_at) VALUES(?,?,'needs_attention',?)`, project.Project.ID, step.ID, now()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE decide_events SET handled_at=? WHERE project_id=?`, now(), project.Project.ID); err != nil {
		t.Fatal(err)
	}
	current, err := db.GetProject(t.Context(), project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := ProjectDecisionFingerprint(current)
	if _, err := db.SQL.Exec(`UPDATE worker_decide_state SET fingerprint=? WHERE project_id=?`, fingerprint, project.Project.ID); err != nil {
		t.Fatal(err)
	}
	if failed, err := db.FailProjectIfStalled(t.Context(), project.Project.ID, "stale", "no progress"); err != nil || failed {
		t.Fatalf("project failed against stale decision: failed=%v err=%v", failed, err)
	}
	if failed, err := db.FailProjectIfStalled(t.Context(), project.Project.ID, fingerprint, "no progress"); err != nil || !failed {
		t.Fatalf("stalled project was not failed: failed=%v err=%v", failed, err)
	}
	detail, err := db.GetProject(t.Context(), project.Project.ID)
	if err != nil || detail.Project.Status != "failed" || detail.Project.FailureReason != "no progress" {
		t.Fatalf("failure state not exposed: project=%+v err=%v", detail.Project, err)
	}
	projects, err := db.ListProjects(t.Context())
	if err != nil || len(projects) != 1 || projects[0].Status != "failed" || projects[0].UnclaimedStepCount != 0 {
		t.Fatalf("failed project summary is incorrect: projects=%+v err=%v", projects, err)
	}
	resumed, err := db.SetProjectStatus(t.Context(), project.Project.ID, "active")
	if err != nil || resumed.Status != "active" || resumed.FailureReason != "" {
		t.Fatalf("failed project did not resume: project=%+v err=%v", resumed, err)
	}
	var events int
	if err := db.SQL.QueryRow(`SELECT count(*) FROM decide_events WHERE project_id=? AND handled_at IS NULL AND reason='project_resumed'`, project.Project.ID).Scan(&events); err != nil || events != 1 {
		t.Fatalf("resume did not queue a new decision: events=%d err=%v", events, err)
	}
}

func TestProjectFailureMigrationPreservesExistingProjectReferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-project.db")
	legacy, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`PRAGMA foreign_keys=ON;
CREATE TABLE projects (
 id TEXT PRIMARY KEY, title TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','stopped','completed')),
 bootstrap_enabled INTEGER NOT NULL DEFAULT 1, max_workers INTEGER NOT NULL DEFAULT 1,
 created_at TEXT NOT NULL, decide_worker TEXT, decide_trigger TEXT,
 decide_started_at TEXT, decide_last_heartbeat_at TEXT);
CREATE TABLE facts(id TEXT NOT NULL, project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE, description TEXT NOT NULL, PRIMARY KEY(id,project_id));
INSERT INTO projects(id,title,created_at) VALUES('proj_legacy','legacy','2026-11-01T00:00:00Z');
INSERT INTO facts(id,project_id,description) VALUES('origin','proj_legacy','target');`)
	if closeErr := legacy.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	if _, err := db.SQL.Exec(`UPDATE projects SET status='failed',failure_reason='blocked' WHERE id='proj_legacy'`); err != nil {
		t.Fatal(err)
	}
	project, err := db.GetProjectMeta(context.Background(), "proj_legacy")
	if err != nil || project.Status != "failed" || project.FailureReason != "blocked" {
		t.Fatalf("migrated project state is incorrect: project=%+v err=%v", project, err)
	}
	var violations int
	if err := db.SQL.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("project migration broke child references: violations=%d err=%v", violations, err)
	}
}

func TestExhaustedPlanningPreservesNewInputAndPendingWork(t *testing.T) {
	for _, name := range []string{"exhausted", "below_limit", "new_event", "new_hint", "runnable", "recovering", "planning", "stopped"} {
		t.Run(name, func(t *testing.T) {
			db := testDB(t)
			project, err := db.CreateProject(t.Context(), CreateProject{Title: "planning", Origin: "target", Goal: "verify"})
			if err != nil {
				t.Fatal(err)
			}
			p := project.Project.ID
			if err := db.RequestDecide(t.Context(), p, "plan required"); err != nil {
				t.Fatal(err)
			}
			if name == "runnable" || name == "recovering" {
				step, err := db.CreateStep(t.Context(), p, CreateStep{From: []string{"origin"}, Description: "pending", Creator: "test"})
				if err != nil {
					t.Fatal(err)
				}
				if name == "recovering" {
					if _, err := db.SQL.Exec(`INSERT INTO worker_execute_recovery(project_id,step_id,status,created_at) VALUES(?,?,'summary_pending',?)`, p, step.ID, now()); err != nil {
						t.Fatal(err)
					}
				}
			}
			detail, err := db.GetProject(t.Context(), p)
			if err != nil {
				t.Fatal(err)
			}
			fingerprint := ProjectDecisionFingerprint(detail)
			var eventThrough int64
			if err := db.SQL.QueryRow(`SELECT max(id) FROM decide_events WHERE project_id=? AND handled_at IS NULL`, p).Scan(&eventThrough); err != nil {
				t.Fatal(err)
			}
			failures := 3
			if name == "below_limit" {
				failures = 2
			}
			if _, err := db.SQL.Exec(`INSERT INTO worker_retry_state(project_id,task_type,failure_count,retry_after,last_outcome,graph_fingerprint,event_through) VALUES(?,'decide',?,?,'unhealthy',?,?)`, p, failures, now(), fingerprint, eventThrough); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "new_event":
				err = db.RequestDecide(t.Context(), p, "fresh direction")
			case "new_hint":
				_, err = db.AddHint(t.Context(), p, CreateHint{Content: "fresh evidence", Creator: "test"})
			case "planning":
				_, err = db.ClaimDecide(t.Context(), p, ClaimDecide{Worker: "planner", Trigger: "new attempt"})
			case "stopped":
				_, err = db.SetProjectStatus(t.Context(), p, "stopped")
			}
			if err != nil {
				t.Fatal(err)
			}
			failed, err := db.FailProjectIfDecideExhausted(t.Context(), p, fingerprint, eventThrough, 3, "planning exhausted")
			if err != nil || failed != (name == "exhausted") {
				t.Fatalf("failed=%v err=%v", failed, err)
			}
		})
	}
}
