package core

import (
	"database/sql"
	"strings"
)

// SQLite requires rebuilding the table to extend its status CHECK constraint.
// Keep the projects name unchanged for all existing child foreign keys.
func migrateProjectFailure(d *sql.DB) error {
	var definition string
	if err := d.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='projects'`).Scan(&definition); err != nil {
		return err
	}
	if strings.Contains(definition, "'failed'") {
		return nil
	}
	if _, err := d.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer d.Exec(`PRAGMA foreign_keys=ON`)
	t, err := d.Begin()
	if err != nil {
		return err
	}
	defer t.Rollback()
	if _, err := t.Exec(`CREATE TABLE projects_with_failure (
 id TEXT PRIMARY KEY, title TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','stopped','completed','failed')),
 failure_reason TEXT NOT NULL DEFAULT '', bootstrap_enabled INTEGER NOT NULL DEFAULT 1,
 max_workers INTEGER NOT NULL DEFAULT 1, budget_seconds INTEGER NOT NULL DEFAULT 1200 CHECK(budget_seconds>=0), budget_baseline_ms INTEGER NOT NULL DEFAULT 0 CHECK(budget_baseline_ms>=0), created_at TEXT NOT NULL,
 decide_worker TEXT, decide_trigger TEXT, decide_started_at TEXT, decide_last_heartbeat_at TEXT);
 INSERT INTO projects_with_failure(id,title,status,failure_reason,bootstrap_enabled,max_workers,budget_seconds,budget_baseline_ms,created_at,decide_worker,decide_trigger,decide_started_at,decide_last_heartbeat_at)
 SELECT id,title,status,failure_reason,bootstrap_enabled,max_workers,budget_seconds,budget_baseline_ms,created_at,decide_worker,decide_trigger,decide_started_at,decide_last_heartbeat_at FROM projects;
 DROP TABLE projects;
 ALTER TABLE projects_with_failure RENAME TO projects;`); err != nil {
		return err
	}
	return t.Commit()
}
