package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

const schema = `
PRAGMA foreign_keys = ON;
PRAGMA journal_mode = WAL;
CREATE TABLE IF NOT EXISTS settings (id INTEGER PRIMARY KEY CHECK(id=1), step_timeout INTEGER NOT NULL DEFAULT 15, decide_timeout INTEGER NOT NULL DEFAULT 15, worker_unhealthy_retry_after_seconds INTEGER NOT NULL DEFAULT 5, worker_rejected_retry_after_seconds INTEGER NOT NULL DEFAULT 5, session_duration_hours INTEGER NOT NULL DEFAULT 24, max_failed_login_attempts INTEGER NOT NULL DEFAULT 5, rate_limit_window_minutes INTEGER NOT NULL DEFAULT 15, log_retention_days INTEGER NOT NULL DEFAULT 30, export_retention_days INTEGER NOT NULL DEFAULT 30, notification_retention_days INTEGER NOT NULL DEFAULT 14, project_idle_alert_hours INTEGER NOT NULL DEFAULT 12);
INSERT OR IGNORE INTO settings(id) VALUES(1);
CREATE TABLE IF NOT EXISTS counters (name TEXT PRIMARY KEY, value INTEGER NOT NULL DEFAULT 0);
INSERT OR IGNORE INTO counters(name,value) VALUES('project',0);
CREATE TABLE IF NOT EXISTS projects (id TEXT PRIMARY KEY, title TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','stopped','completed','failed')), failure_reason TEXT NOT NULL DEFAULT '', bootstrap_enabled INTEGER NOT NULL DEFAULT 1, max_workers INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, decide_worker TEXT, decide_trigger TEXT, decide_started_at TEXT, decide_last_heartbeat_at TEXT);
CREATE TABLE IF NOT EXISTS facts (id TEXT NOT NULL, project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE, description TEXT NOT NULL, PRIMARY KEY(id,project_id));
CREATE TABLE IF NOT EXISTS goals (id TEXT NOT NULL, project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE, description TEXT NOT NULL, parent_goal_id TEXT, status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','completed')), priority INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, completed_at TEXT, completion_description TEXT, completed_by TEXT, PRIMARY KEY(id,project_id), FOREIGN KEY(parent_goal_id,project_id) REFERENCES goals(id,project_id));
CREATE TABLE IF NOT EXISTS goal_sources (goal_id TEXT NOT NULL, project_id TEXT NOT NULL, fact_id TEXT NOT NULL, PRIMARY KEY(goal_id,project_id,fact_id), FOREIGN KEY(goal_id,project_id) REFERENCES goals(id,project_id) ON DELETE CASCADE, FOREIGN KEY(fact_id,project_id) REFERENCES facts(id,project_id) ON DELETE CASCADE);
CREATE TABLE IF NOT EXISTS steps (id TEXT NOT NULL, project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE, to_fact_id TEXT, description TEXT NOT NULL, goal_id TEXT, priority INTEGER NOT NULL DEFAULT 0, creator TEXT NOT NULL, worker TEXT, worker_role TEXT NOT NULL DEFAULT 'agent' CHECK(worker_role IN ('agent','dispatcher')), last_heartbeat_at TEXT, created_at TEXT NOT NULL, concluded_at TEXT, abandoned INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(id,project_id), FOREIGN KEY(goal_id,project_id) REFERENCES goals(id,project_id));
CREATE TABLE IF NOT EXISTS step_sources (step_id TEXT NOT NULL, project_id TEXT NOT NULL, fact_id TEXT NOT NULL, PRIMARY KEY(step_id,project_id,fact_id), FOREIGN KEY(step_id,project_id) REFERENCES steps(id,project_id) ON DELETE CASCADE, FOREIGN KEY(fact_id,project_id) REFERENCES facts(id,project_id) ON DELETE CASCADE);
CREATE TABLE IF NOT EXISTS findings (id TEXT NOT NULL, project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE, title TEXT NOT NULL, description TEXT NOT NULL, severity TEXT NOT NULL DEFAULT 'info', kind TEXT NOT NULL DEFAULT 'finding', data_json TEXT NOT NULL DEFAULT '{}', fact_id TEXT, created_at TEXT NOT NULL, PRIMARY KEY(id,project_id), FOREIGN KEY(fact_id,project_id) REFERENCES facts(id,project_id));
CREATE TABLE IF NOT EXISTS vulnerabilities (id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE, finding_id TEXT NOT NULL, fact_id TEXT NOT NULL, title TEXT NOT NULL, description TEXT NOT NULL, severity TEXT NOT NULL CHECK(severity IN ('critical','high','medium','low')), status TEXT NOT NULL DEFAULT 'confirmed' CHECK(status IN ('confirmed','ignored')), discovered_at TEXT NOT NULL, source_step_id TEXT, source_worker TEXT, source_fact_ids_json TEXT NOT NULL DEFAULT '[]', evidence_json TEXT NOT NULL DEFAULT '[]');
CREATE TABLE IF NOT EXISTS vulnerability_review_status (project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE, identity_key TEXT NOT NULL, status TEXT NOT NULL CHECK(status IN ('confirmed','ignored')), updated_at TEXT NOT NULL, PRIMARY KEY(project_id,identity_key));
CREATE TABLE IF NOT EXISTS hints (id TEXT NOT NULL, project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE, content TEXT NOT NULL, creator TEXT NOT NULL, created_at TEXT NOT NULL, PRIMARY KEY(id,project_id));
CREATE TABLE IF NOT EXISTS scoped_counters (project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE, kind TEXT NOT NULL, value INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(project_id,kind));
CREATE TABLE IF NOT EXISTS users (id TEXT PRIMARY KEY, username TEXT NOT NULL, username_lower TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL, created_at TEXT NOT NULL, disabled INTEGER NOT NULL DEFAULT 0, failed_attempts INTEGER NOT NULL DEFAULT 0, locked_until TEXT);
CREATE TABLE IF NOT EXISTS sessions (token TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE, expires_at TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS login_attempts (id INTEGER PRIMARY KEY AUTOINCREMENT, username_lower TEXT NOT NULL, attempted_at TEXT NOT NULL, success INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS idx_login_attempts_window ON login_attempts(username_lower,success,attempted_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username_lower ON users(lower(username));
CREATE TABLE IF NOT EXISTS audit_log (id INTEGER PRIMARY KEY AUTOINCREMENT, created_at TEXT NOT NULL, actor TEXT NOT NULL DEFAULT 'system', action TEXT NOT NULL, target_type TEXT, target_id TEXT, summary TEXT NOT NULL DEFAULT '', detail TEXT, user_id TEXT, resource TEXT, detail_json TEXT NOT NULL DEFAULT '{}');
CREATE TABLE IF NOT EXISTS notifications (id INTEGER PRIMARY KEY AUTOINCREMENT, created_at TEXT NOT NULL, level TEXT NOT NULL DEFAULT 'info', title TEXT NOT NULL, body TEXT, link TEXT, read INTEGER NOT NULL DEFAULT 0, user_id TEXT, kind TEXT, read_at TEXT);
CREATE TABLE IF NOT EXISTS project_templates (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id TEXT NOT NULL DEFAULT 'anonymous', name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', origin TEXT NOT NULL DEFAULT '', goal TEXT NOT NULL DEFAULT '', hints_json TEXT NOT NULL DEFAULT '[]', created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS templates (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, title TEXT NOT NULL, origin TEXT NOT NULL, goal TEXT NOT NULL, hints_json TEXT NOT NULL DEFAULT '[]', created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS worker_config (id INTEGER PRIMARY KEY CHECK(id=1), name TEXT NOT NULL DEFAULT 'local', driver TEXT NOT NULL DEFAULT 'mock', command TEXT NOT NULL DEFAULT '', environment_json TEXT NOT NULL DEFAULT '{}', enabled INTEGER NOT NULL DEFAULT 1, updated_at TEXT NOT NULL DEFAULT '', workspace_root TEXT NOT NULL DEFAULT '', completed_action TEXT NOT NULL DEFAULT 'keep', context_root TEXT NOT NULL DEFAULT '', runtime_json TEXT NOT NULL DEFAULT '{}', worker_count INTEGER NOT NULL DEFAULT 4);
INSERT OR IGNORE INTO worker_config(id,updated_at) VALUES(1,'');
CREATE TABLE IF NOT EXISTS worker_task_history (id INTEGER PRIMARY KEY AUTOINCREMENT, worker TEXT NOT NULL, project_id TEXT, step_id TEXT, task_type TEXT NOT NULL, status TEXT NOT NULL, started_at TEXT NOT NULL, finished_at TEXT, detail TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS worker_retry_state (project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE, task_type TEXT NOT NULL CHECK(task_type IN ('decide','execute')), failure_count INTEGER NOT NULL DEFAULT 0, retry_after TEXT NOT NULL, last_outcome TEXT NOT NULL, PRIMARY KEY(project_id,task_type));
CREATE TABLE IF NOT EXISTS worker_execute_recovery (project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE, step_id TEXT NOT NULL, payload_json TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'running' CHECK(status IN ('running','summary_pending','commit_pending','needs_attention')), worker_name TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, session_id TEXT NOT NULL DEFAULT '', driver TEXT NOT NULL DEFAULT '', worker_identity TEXT NOT NULL DEFAULT '', workspace_dir TEXT NOT NULL DEFAULT '', summary_attempts INTEGER NOT NULL DEFAULT 0, interruption_reason TEXT NOT NULL DEFAULT '', PRIMARY KEY(project_id,step_id));
CREATE TABLE IF NOT EXISTS worker_decide_state (project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE, fingerprint TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS project_target_health (project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE, run_id TEXT NOT NULL DEFAULT (lower(hex(randomblob(16)))), status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','ready')), analysis_json TEXT NOT NULL DEFAULT '', failure_counts_json TEXT NOT NULL DEFAULT '[]', last_checked_at TEXT NOT NULL DEFAULT '', last_error TEXT NOT NULL DEFAULT '', analysis_failures INTEGER NOT NULL DEFAULT 0, retry_after TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS platform_projects (project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE, platform TEXT NOT NULL, scope_id TEXT NOT NULL, challenge_id TEXT NOT NULL, challenge_json TEXT NOT NULL, delivery_json TEXT NOT NULL, UNIQUE(platform,scope_id,challenge_id));
CREATE TABLE IF NOT EXISTS platform_configs (id TEXT PRIMARY KEY, base_url TEXT NOT NULL, token TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS idx_steps_open ON steps(project_id, concluded_at, abandoned, priority DESC, created_at);
CREATE INDEX IF NOT EXISTS idx_sessions_expiry ON sessions(expires_at);
`

type DB struct {
	SQL      *sql.DB
	WorkerMu sync.Mutex
	// Path is used to derive the default dedicated workspace for local workers.
	Path string
}

func Open(path string) (*DB, error) {
	if path == "" {
		path = "hotmouse.db"
	}
	if err := ensureDir(path); err != nil {
		return nil, err
	}
	d, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(1)
	d.SetMaxIdleConns(1)
	d.SetConnMaxLifetime(0)
	if _, err = d.Exec(schema); err != nil {
		d.Close()
		return nil, fmt.Errorf("initialize sqlite: %w", err)
	}
	if _, err = d.Exec(competitionSchema); err != nil {
		d.Close()
		return nil, fmt.Errorf("initialize competition scheduler: %w", err)
	}
	if _, err = d.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		d.Close()
		return nil, err
	}
	// Keep databases created by earlier migration iterations readable.
	_, _ = d.Exec(`ALTER TABLE project_templates ADD COLUMN hints_json TEXT NOT NULL DEFAULT '[]'`)
	_, _ = d.Exec(`ALTER TABLE project_templates ADD COLUMN user_id TEXT NOT NULL DEFAULT 'anonymous'`)
	_, _ = d.Exec(`ALTER TABLE settings ADD COLUMN worker_unhealthy_retry_after_seconds INTEGER NOT NULL DEFAULT 5`)
	_, _ = d.Exec(`ALTER TABLE settings ADD COLUMN worker_rejected_retry_after_seconds INTEGER NOT NULL DEFAULT 5`)
	_, _ = d.Exec(`ALTER TABLE worker_execute_recovery ADD COLUMN status TEXT NOT NULL DEFAULT 'running'`)
	_, _ = d.Exec(`ALTER TABLE worker_execute_recovery ADD COLUMN worker_name TEXT NOT NULL DEFAULT ''`)
	_, _ = d.Exec(`ALTER TABLE worker_config ADD COLUMN workspace_root TEXT NOT NULL DEFAULT ''`)
	_, _ = d.Exec(`ALTER TABLE worker_config ADD COLUMN completed_action TEXT NOT NULL DEFAULT 'keep'`)
	_, _ = d.Exec(`ALTER TABLE worker_config ADD COLUMN context_root TEXT NOT NULL DEFAULT ''`)
	_, _ = d.Exec(`ALTER TABLE worker_config ADD COLUMN runtime_json TEXT NOT NULL DEFAULT '{}'`)
	_, _ = d.Exec(`ALTER TABLE worker_config ADD COLUMN worker_count INTEGER NOT NULL DEFAULT 4`)
	if err := addColumnIfMissing(d, "worker_config", "dispatcher_json", `TEXT NOT NULL DEFAULT 'null'`); err != nil {
		d.Close()
		return nil, fmt.Errorf("migrate dispatcher config: %w", err)
	}
	if err := addColumnIfMissing(d, "project_target_health", "preprocessing_json", `TEXT NOT NULL DEFAULT '{"status":"pending"}'`); err != nil {
		d.Close()
		return nil, fmt.Errorf("migrate preprocessing: %w", err)
	}
	_, _ = d.Exec(`ALTER TABLE projects ADD COLUMN max_workers INTEGER NOT NULL DEFAULT 1`)
	_, _ = d.Exec(`ALTER TABLE steps ADD COLUMN worker_role TEXT NOT NULL DEFAULT 'agent' CHECK(worker_role IN ('agent','dispatcher'))`)
	_, _ = d.Exec(`UPDATE steps SET worker_role='dispatcher' WHERE worker LIKE '%/dispatcher/%'`)
	if err := migrateUserIDs(d); err != nil {
		d.Close()
		return nil, fmt.Errorf("migrate auth user IDs: %w", err)
	}
	if err := migrateLegacyTemplates(d); err != nil {
		d.Close()
		return nil, fmt.Errorf("migrate legacy templates: %w", err)
	}
	if err := migrateActivitySchema(d); err != nil {
		d.Close()
		return nil, fmt.Errorf("migrate activity schema: %w", err)
	}
	if err := migrateExecuteRecovery(d); err != nil {
		d.Close()
		return nil, fmt.Errorf("migrate worker recovery: %w", err)
	}
	if err := addColumnIfMissing(d, "worker_execute_recovery", "retry_after", `TEXT NOT NULL DEFAULT ''`); err != nil {
		d.Close()
		return nil, fmt.Errorf("migrate worker recovery retry: %w", err)
	}
	if err := migrateStrategy(d); err != nil {
		d.Close()
		return nil, fmt.Errorf("migrate strategy and memory: %w", err)
	}
	if err := migrateProjectBudget(d); err != nil {
		d.Close()
		return nil, fmt.Errorf("migrate project budget: %w", err)
	}
	if err := migrateProjectFailure(d); err != nil {
		d.Close()
		return nil, fmt.Errorf("migrate project failure state: %w", err)
	}
	if _, err := d.Exec(`INSERT OR IGNORE INTO project_target_health(project_id) SELECT id FROM projects`); err != nil {
		d.Close()
		return nil, fmt.Errorf("initialize target probe state: %w", err)
	}
	if err := ensureDefaultAdmin(d); err != nil {
		d.Close()
		return nil, fmt.Errorf("initialize default admin: %w", err)
	}
	absPath, _ := filepath.Abs(path)
	return &DB{SQL: d, Path: absPath}, nil
}
func ensureDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "." {
		return nil
	}
	return mkdirAll(dir)
}

var mkdirAll = func(path string) error { return os.MkdirAll(path, 0o755) }

// now is kept in one format so SQLite lease comparisons and API output agree.
func now() string { return time.Now().UTC().Format("2006-01-02T15:04:05Z") }
func tx(ctx context.Context, d *DB, fn func(*sql.Tx) error) error {
	t, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err = fn(t); err != nil {
		_ = t.Rollback()
		return err
	}
	return t.Commit()
}
func clean(s string) string { return strings.TrimSpace(s) }
func jsonData(v any) string { b, _ := json.Marshal(v); return string(b) }
func parseData(s string) map[string]any {
	var v map[string]any
	if json.Unmarshal([]byte(s), &v) != nil || v == nil {
		return map[string]any{}
	}
	return v
}
func requireText(name, value string) error {
	if clean(value) == "" {
		return fmt.Errorf("%s must not be empty", name)
	}
	return nil
}

var ErrNotFound = errors.New("not found")
