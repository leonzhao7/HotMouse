package core

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestOpenAddsWorkerWorkspaceSettingsToExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.db")
	legacy, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`CREATE TABLE worker_config (
		id INTEGER PRIMARY KEY CHECK(id=1),
		name TEXT NOT NULL DEFAULT 'local',
		driver TEXT NOT NULL DEFAULT 'mock',
		command TEXT NOT NULL DEFAULT '',
		environment_json TEXT NOT NULL DEFAULT '{}',
		enabled INTEGER NOT NULL DEFAULT 1,
		updated_at TEXT NOT NULL DEFAULT ''
	)`)
	if err == nil {
		_, err = legacy.Exec(`INSERT INTO worker_config(id) VALUES(1)`)
	}
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
	var workspaceRoot, completedAction, contextRoot string
	if err := db.SQL.QueryRow(`SELECT workspace_root,completed_action,context_root FROM worker_config WHERE id=1`).Scan(&workspaceRoot, &completedAction, &contextRoot); err != nil {
		t.Fatal(err)
	}
	if workspaceRoot != "" || completedAction != "keep" || contextRoot != "" {
		t.Fatalf("unexpected migrated worker settings: root=%q action=%q context=%q", workspaceRoot, completedAction, contextRoot)
	}
}

func TestRecoverySchemaMigrationPreservesPendingPayload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recovery.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(context.Background(), CreateProject{Title: "legacy", Origin: "fixture", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.SQL.Exec(`DROP TABLE worker_execute_recovery;
	CREATE TABLE worker_execute_recovery (project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,step_id TEXT NOT NULL,payload_json TEXT NOT NULL DEFAULT '',status TEXT NOT NULL DEFAULT 'running' CHECK(status IN ('running','commit_pending','needs_attention')),worker_name TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,PRIMARY KEY(project_id,step_id));
	INSERT INTO worker_execute_recovery(project_id,step_id,payload_json,status,worker_name,created_at) VALUES(?,'s001','{"description":"saved result"}','commit_pending','local','2026-11-01T00:00:00Z');`, project.Project.ID)
	db.SQL.Close()
	if err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	var status, payload string
	if err := db.SQL.QueryRow(`SELECT status,payload_json FROM worker_execute_recovery`).Scan(&status, &payload); err != nil || status != "commit_pending" || payload != `{"description":"saved result"}` {
		t.Fatalf("migration lost checkpoint: status=%s payload=%s err=%v", status, payload, err)
	}
	if _, err := db.SQL.Exec(`UPDATE worker_execute_recovery SET status='summary_pending',session_id='session-fixture'`); err != nil {
		t.Fatal(err)
	}
}

func TestOpenMigratesIntegerAuthIDsAndOwnerReferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-auth.db")
	legacy, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	legacy.SetMaxOpenConns(1)
	_, err = legacy.Exec(`PRAGMA foreign_keys=ON;
CREATE TABLE users(id INTEGER PRIMARY KEY AUTOINCREMENT,username TEXT NOT NULL UNIQUE,password_hash TEXT NOT NULL,created_at TEXT NOT NULL,failed_attempts INTEGER NOT NULL DEFAULT 0,locked_until TEXT);
CREATE TABLE sessions(token TEXT PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,expires_at TEXT NOT NULL,created_at TEXT NOT NULL);
CREATE TABLE audit_log(id INTEGER PRIMARY KEY AUTOINCREMENT,user_id INTEGER,action TEXT NOT NULL,resource TEXT,detail_json TEXT NOT NULL DEFAULT '{}',created_at TEXT NOT NULL);
CREATE TABLE notifications(id INTEGER PRIMARY KEY AUTOINCREMENT,user_id INTEGER,kind TEXT NOT NULL,title TEXT NOT NULL,body TEXT NOT NULL,read_at TEXT,created_at TEXT NOT NULL);
CREATE TABLE project_templates(id INTEGER PRIMARY KEY AUTOINCREMENT,user_id TEXT NOT NULL DEFAULT 'anonymous',name TEXT NOT NULL,description TEXT NOT NULL DEFAULT '',origin TEXT NOT NULL DEFAULT '',goal TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL);
CREATE TABLE templates(id TEXT PRIMARY KEY,user_id TEXT NOT NULL,title TEXT NOT NULL,origin TEXT NOT NULL,goal TEXT NOT NULL,hints_json TEXT NOT NULL DEFAULT '[]',created_at TEXT NOT NULL);`)
	if err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`CREATE UNIQUE INDEX idx_users_username_lower ON users(lower(username))`); err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("legacy-secret"), bcrypt.DefaultCost)
	if err == nil {
		_, err = legacy.Exec(`INSERT INTO users(id,username,password_hash,created_at) VALUES(17,'LegacyUser',?,?)`, string(hash), now())
	}
	if err == nil {
		_, err = legacy.Exec(`INSERT INTO sessions(token,user_id,expires_at,created_at) VALUES('legacy-session',17,?,?)`, time.Now().UTC().Add(time.Hour).Format("2006-01-02T15:04:05Z"), now())
	}
	if err == nil {
		_, err = legacy.Exec(`INSERT INTO audit_log(user_id,action,created_at) VALUES(17,'settings.update',?)`, now())
	}
	if err == nil {
		_, err = legacy.Exec(`INSERT INTO notifications(user_id,kind,title,body,created_at) VALUES(17,'info','legacy','notice',?)`, now())
	}
	if err == nil {
		_, err = legacy.Exec(`INSERT INTO project_templates(user_id,name,created_at) VALUES('17','legacy-template',?)`, now())
	}
	if err == nil {
		_, err = legacy.Exec(`INSERT INTO templates(id,user_id,title,origin,goal,created_at) VALUES('legacy-template','17','legacy','target','verify',?)`, now())
	}
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
	var newID string
	if err := db.SQL.QueryRow(`SELECT id FROM users WHERE username='LegacyUser'`).Scan(&newID); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(newID, "user_") || len(newID) != len("user_")+32 {
		t.Fatalf("legacy user ID was not migrated: %q", newID)
	}
	if user, err := db.Session(context.Background(), "legacy-session"); err != nil || user.ID != newID {
		t.Fatalf("legacy session was not preserved: user=%+v err=%v", user, err)
	}
	if _, user, err := db.Login(context.Background(), "legacyuser", "legacy-secret"); err != nil || user.ID != newID || user.Username != "LegacyUser" {
		t.Fatalf("legacy credentials were not preserved: user=%+v err=%v", user, err)
	}
	for table := range map[string]struct{}{"audit_log": {}, "notifications": {}, "project_templates": {}, "templates": {}} {
		var owner string
		query := fmt.Sprintf(`SELECT user_id FROM %s LIMIT 1`, table)
		if err := db.SQL.QueryRow(query).Scan(&owner); err != nil || owner != newID {
			t.Fatalf("%s owner=%q, want %q; err=%v", table, owner, newID, err)
		}
	}
	for _, table := range []string{"audit_log", "notifications"} {
		var columnType string
		if err := db.SQL.QueryRow(`SELECT type FROM pragma_table_info(?) WHERE name='user_id'`, table).Scan(&columnType); err != nil || columnType != "TEXT" {
			t.Fatalf("%s.user_id type=%q err=%v", table, columnType, err)
		}
	}
	var violations int
	rows, err := db.SQL.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		violations++
	}
	rows.Close()
	if violations != 0 {
		t.Fatalf("auth migration left %d foreign key violations", violations)
	}
}
