package core

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
)

type legacyUser struct {
	id           int64
	username     string
	passwordHash string
	createdAt    string
	failed       int
	lockedUntil  sql.NullString
	newID        string
}

func migrateUserIDs(db *sql.DB) error {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	rows, err := conn.QueryContext(ctx, `PRAGMA table_info(users)`)
	if err != nil {
		return err
	}
	userIDType := ""
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		if name == "id" {
			userIDType = strings.ToUpper(strings.TrimSpace(columnType))
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if userIDType == "TEXT" {
		return nil
	}
	if userIDType == "" {
		return fmt.Errorf("users table has no id column")
	}

	legacyRows, err := conn.QueryContext(ctx, `SELECT id,username,password_hash,created_at,failed_attempts,locked_until FROM users ORDER BY id`)
	if err != nil {
		return err
	}
	var users []legacyUser
	for legacyRows.Next() {
		var user legacyUser
		if err := legacyRows.Scan(&user.id, &user.username, &user.passwordHash, &user.createdAt, &user.failed, &user.lockedUntil); err != nil {
			legacyRows.Close()
			return err
		}
		user.newID, err = newUserID()
		if err != nil {
			legacyRows.Close()
			return err
		}
		users = append(users, user)
	}
	if err := legacyRows.Err(); err != nil {
		legacyRows.Close()
		return err
	}
	legacyRows.Close()

	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`) }()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := func(cause error) error {
		_ = tx.Rollback()
		return cause
	}
	statements := []string{
		`CREATE TEMP TABLE auth_user_id_map(old_id INTEGER PRIMARY KEY,new_id TEXT NOT NULL UNIQUE)`,
		`DROP INDEX IF EXISTS idx_users_username_lower`,
		`ALTER TABLE sessions RENAME TO sessions_integer_ids`,
		`ALTER TABLE users RENAME TO users_integer_ids`,
		`CREATE TABLE users (id TEXT PRIMARY KEY,username TEXT NOT NULL,username_lower TEXT NOT NULL UNIQUE,password_hash TEXT NOT NULL,created_at TEXT NOT NULL,disabled INTEGER NOT NULL DEFAULT 0,failed_attempts INTEGER NOT NULL DEFAULT 0,locked_until TEXT)`,
		`CREATE TABLE sessions (token TEXT PRIMARY KEY,user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,expires_at TEXT NOT NULL,created_at TEXT NOT NULL)`,
		`ALTER TABLE audit_log RENAME TO audit_log_integer_ids`,
		`CREATE TABLE audit_log (id INTEGER PRIMARY KEY AUTOINCREMENT,created_at TEXT NOT NULL,actor TEXT NOT NULL DEFAULT 'system',action TEXT NOT NULL,target_type TEXT,target_id TEXT,summary TEXT NOT NULL DEFAULT '',detail TEXT,user_id TEXT,resource TEXT,detail_json TEXT NOT NULL DEFAULT '{}')`,
		`INSERT INTO audit_log(id,user_id,action,resource,detail_json,created_at,summary,target_type,target_id,detail) SELECT id,CAST(user_id AS TEXT),action,resource,detail_json,created_at,action,'api',resource,detail_json FROM audit_log_integer_ids`,
		`DROP TABLE audit_log_integer_ids`,
		`ALTER TABLE notifications RENAME TO notifications_integer_ids`,
		`CREATE TABLE notifications (id INTEGER PRIMARY KEY AUTOINCREMENT,created_at TEXT NOT NULL,level TEXT NOT NULL DEFAULT 'info',title TEXT NOT NULL,body TEXT,link TEXT,read INTEGER NOT NULL DEFAULT 0,user_id TEXT,kind TEXT,read_at TEXT)`,
		`INSERT INTO notifications(id,user_id,kind,level,title,body,read_at,read,created_at) SELECT id,CAST(user_id AS TEXT),kind,kind,title,body,read_at,CASE WHEN read_at IS NULL THEN 0 ELSE 1 END,created_at FROM notifications_integer_ids`,
		`DROP TABLE notifications_integer_ids`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return rollback(err)
		}
	}
	for _, user := range users {
		if _, err := tx.ExecContext(ctx, `INSERT INTO auth_user_id_map(old_id,new_id) VALUES(?,?)`, user.id, user.newID); err != nil {
			return rollback(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO users(id,username,username_lower,password_hash,created_at,disabled,failed_attempts,locked_until) VALUES(?,?,?,?,?,0,?,?)`, user.newID, user.username, strings.ToLower(user.username), user.passwordHash, user.createdAt, user.failed, nullableDB(user.lockedUntil.String)); err != nil {
			return rollback(err)
		}
	}
	var oldSessions, newSessions int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sessions_integer_ids`).Scan(&oldSessions); err != nil {
		return rollback(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sessions(token,user_id,expires_at,created_at) SELECT s.token,m.new_id,s.expires_at,s.created_at FROM sessions_integer_ids s JOIN auth_user_id_map m ON m.old_id=s.user_id`); err != nil {
		return rollback(err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sessions`).Scan(&newSessions); err != nil {
		return rollback(err)
	}
	if oldSessions != newSessions {
		return rollback(fmt.Errorf("could not map all %d sessions", oldSessions))
	}
	for _, statement := range []string{
		`UPDATE audit_log SET user_id=(SELECT new_id FROM auth_user_id_map WHERE old_id=audit_log.user_id) WHERE user_id IS NOT NULL AND EXISTS(SELECT 1 FROM auth_user_id_map WHERE old_id=audit_log.user_id)`,
		`UPDATE notifications SET user_id=(SELECT new_id FROM auth_user_id_map WHERE old_id=notifications.user_id) WHERE user_id IS NOT NULL AND EXISTS(SELECT 1 FROM auth_user_id_map WHERE old_id=notifications.user_id)`,
		`UPDATE project_templates SET user_id=(SELECT m.new_id FROM auth_user_id_map m WHERE CAST(m.old_id AS TEXT)=project_templates.user_id) WHERE EXISTS(SELECT 1 FROM auth_user_id_map m WHERE CAST(m.old_id AS TEXT)=project_templates.user_id)`,
		`UPDATE templates SET user_id=(SELECT m.new_id FROM auth_user_id_map m WHERE CAST(m.old_id AS TEXT)=templates.user_id) WHERE EXISTS(SELECT 1 FROM auth_user_id_map m WHERE CAST(m.old_id AS TEXT)=templates.user_id)`,
		`DROP TABLE sessions_integer_ids`,
		`DROP TABLE users_integer_ids`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username_lower ON users(lower(username))`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_expiry ON sessions(expires_at)`,
		`DROP TABLE auth_user_id_map`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return rollback(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func newUserID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return "user_" + hex.EncodeToString(bytes), nil
}

func migrateLegacyTemplates(db *sql.DB) error {
	_, err := db.Exec(`INSERT OR IGNORE INTO templates(id,user_id,title,origin,goal,hints_json,created_at)
SELECT 'tmpl_legacy_' || CAST(id AS TEXT),user_id,name,origin,goal,hints_json,created_at
FROM project_templates
WHERE NOT EXISTS (SELECT 1 FROM templates t WHERE t.id='tmpl_legacy_' || CAST(project_templates.id AS TEXT))`)
	return err
}
