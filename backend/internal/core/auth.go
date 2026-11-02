package core

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type User struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	CreatedAt string `json:"created_at"`
}

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var dummyHash string
var dummyHashOnce sync.Once

const bcryptCost = 12

func timingHash() []byte {
	dummyHashOnce.Do(func() {
		h, _ := bcrypt.GenerateFromPassword([]byte("hotmouse-invalid-user-password"), bcryptCost)
		dummyHash = string(h)
	})
	return []byte(dummyHash)
}

func (d *DB) Register(ctx context.Context, username, password string) (User, error) {
	if err := requireText("username", username); err != nil {
		return User{}, err
	}
	if err := requireText("password", password); err != nil {
		return User{}, err
	}
	if len(password) < 8 {
		return User{}, fmt.Errorf("password must be at least 8 characters")
	}
	if len(password) > 72 {
		return User{}, fmt.Errorf("password is too long")
	}
	if len(username) < 3 || len(username) > 32 || !usernamePattern.MatchString(username) {
		return User{}, fmt.Errorf("invalid username")
	}
	if len(username) > 32 {
		return User{}, fmt.Errorf("username is too long")
	}
	h, e := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if e != nil {
		return User{}, e
	}
	var u User
	id, e := newUserID()
	if e != nil {
		return User{}, e
	}
	e = tx(ctx, d, func(t *sql.Tx) error {
		v := now()
		_, e := t.Exec(`INSERT INTO users(id,username,username_lower,password_hash,created_at) VALUES(?,?,?,?,?)`, id, username, strings.ToLower(username), string(h), v)
		if e != nil {
			return fmt.Errorf("username already exists")
		}
		u = User{ID: id, Username: username, CreatedAt: v}
		return nil
	})
	return u, e
}
func token() (string, error) {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return hex.EncodeToString(b), nil
}
func (d *DB) Login(ctx context.Context, username, password string) (string, User, error) {
	var u User
	var hash string
	var uid string
	var authErr error
	username = strings.TrimSpace(username)
	var maxAttempts, windowMinutes int
	_ = d.SQL.QueryRowContext(ctx, `SELECT max_failed_login_attempts,rate_limit_window_minutes FROM settings WHERE id=1`).Scan(&maxAttempts, &windowMinutes)
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	if windowMinutes <= 0 {
		windowMinutes = 15
	}
	var recent int
	_ = d.SQL.QueryRowContext(ctx, `SELECT count(*) FROM login_attempts WHERE username_lower=lower(?) AND success=0 AND attempted_at >= ?`, username, time.Now().UTC().Add(-time.Duration(windowMinutes)*time.Minute).Format("2006-01-02T15:04:05Z")).Scan(&recent)
	if recent >= maxAttempts {
		return "", u, fmt.Errorf("too many login attempts")
	}
	e := tx(ctx, d, func(t *sql.Tx) error {
		var inWindow int
		cutoff := time.Now().UTC().Add(-time.Duration(windowMinutes) * time.Minute).Format("2006-01-02T15:04:05Z")
		if e := t.QueryRow(`SELECT count(*) FROM login_attempts WHERE username_lower=lower(?) AND success=0 AND attempted_at>=?`, username, cutoff).Scan(&inWindow); e != nil {
			return e
		}
		if inWindow >= maxAttempts {
			authErr = fmt.Errorf("too many login attempts")
			return nil
		}
		var created, locked, canonicalUsername sql.NullString
		var failed, disabled int
		if e := t.QueryRow(`SELECT id,username,password_hash,created_at,failed_attempts,locked_until,disabled FROM users WHERE username_lower=lower(?)`, username).Scan(&uid, &canonicalUsername, &hash, &created, &failed, &locked, &disabled); e != nil {
			_ = bcrypt.CompareHashAndPassword(timingHash(), []byte(password))
			_, _ = t.Exec(`INSERT INTO login_attempts(username_lower,attempted_at,success) VALUES(lower(?),?,0)`, username, now())
			authErr = fmt.Errorf("invalid credentials")
			return nil
		}
		if disabled != 0 {
			_, _ = t.Exec(`INSERT INTO login_attempts(username_lower,attempted_at,success) VALUES(lower(?),?,0)`, username, now())
			authErr = fmt.Errorf("invalid credentials")
			return nil
		}
		if locked.Valid {
			if until, parseErr := time.Parse("2006-01-02T15:04:05Z", locked.String); parseErr == nil && until.After(time.Now().UTC()) {
				_, _ = t.Exec(`INSERT INTO login_attempts(username_lower,attempted_at,success) VALUES(lower(?),?,0)`, username, now())
				authErr = fmt.Errorf("invalid credentials")
				return nil
			}
		}
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
			var max int
			_ = t.QueryRow(`SELECT max_failed_login_attempts FROM settings WHERE id=1`).Scan(&max)
			failed++
			_, _ = t.Exec(`INSERT INTO login_attempts(username_lower,attempted_at,success) VALUES(lower(?),?,0)`, username, now())
			if failed >= max && max > 0 {
				_, _ = t.Exec(`UPDATE users SET failed_attempts=0,locked_until=? WHERE id=?`, time.Now().UTC().Add(time.Duration(windowMinutes)*time.Minute).Format("2006-01-02T15:04:05Z"), uid)
			} else {
				_, _ = t.Exec(`UPDATE users SET failed_attempts=? WHERE id=?`, failed, uid)
			}
			authErr = fmt.Errorf("invalid credentials")
			return nil
		}
		_, _ = t.Exec(`UPDATE users SET failed_attempts=0,locked_until=NULL WHERE id=?`, uid)
		_, _ = t.Exec(`INSERT INTO login_attempts(username_lower,attempted_at,success) VALUES(lower(?),?,1)`, username, now())
		u = User{ID: uid, Username: canonicalUsername.String, CreatedAt: created.String}
		return nil
	})
	if e != nil {
		return "", u, e
	}
	if authErr != nil {
		return "", u, authErr
	}
	tok, e := token()
	if e != nil {
		return "", u, e
	}
	dur := 24
	_ = d.SQL.QueryRow(`SELECT session_duration_hours FROM settings WHERE id=1`).Scan(&dur)
	expires := time.Now().UTC().Add(time.Duration(dur) * time.Hour).Format("2006-01-02T15:04:05Z")
	_, e = d.SQL.ExecContext(ctx, `INSERT INTO sessions(token,user_id,expires_at,created_at) VALUES(?,?,?,?)`, tok, uid, expires, now())
	return tok, u, e
}
func (d *DB) Logout(ctx context.Context, tok string) error {
	_, e := d.SQL.ExecContext(ctx, `DELETE FROM sessions WHERE token=?`, tok)
	return e
}
func (d *DB) Session(ctx context.Context, tok string) (User, error) {
	var u User
	var exp string
	if tok == "" {
		return u, fmt.Errorf("authentication required")
	}
	e := d.SQL.QueryRowContext(ctx, `SELECT u.id,u.username,u.created_at,s.expires_at FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token=?`, tok).Scan(&u.ID, &u.Username, &u.CreatedAt, &exp)
	if e != nil {
		return u, fmt.Errorf("authentication required")
	}
	t, e := time.Parse("2006-01-02T15:04:05Z", exp)
	if e != nil || !t.After(time.Now().UTC()) {
		_, _ = d.SQL.Exec(`DELETE FROM sessions WHERE token=?`, tok)
		return User{}, fmt.Errorf("authentication required")
	}
	var duration int
	_ = d.SQL.QueryRowContext(ctx, `SELECT session_duration_hours FROM settings WHERE id=1`).Scan(&duration)
	if duration <= 0 {
		duration = 24
	}
	_, _ = d.SQL.ExecContext(ctx, `UPDATE sessions SET expires_at=? WHERE token=?`, time.Now().UTC().Add(time.Duration(duration)*time.Hour).Format("2006-01-02T15:04:05Z"), tok)
	return u, nil
}

func (d *DB) ChangePassword(ctx context.Context, tok, oldPassword, newPassword string) error {
	u, err := d.Session(ctx, tok)
	if err != nil {
		return err
	}
	var hash string
	if err := d.SQL.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id=?`, u.ID).Scan(&hash); err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(oldPassword)) != nil {
		return fmt.Errorf("invalid credentials")
	}
	if !validNewPassword(newPassword) {
		return fmt.Errorf("new password must contain upper, lower, digit, and special character")
	}
	next, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcryptCost)
	if err != nil {
		return err
	}
	err = tx(ctx, d, func(t *sql.Tx) error {
		if _, e := t.Exec(`UPDATE users SET password_hash=?,failed_attempts=0,locked_until=NULL WHERE id=?`, string(next), u.ID); e != nil {
			return e
		}
		_, e := t.Exec(`DELETE FROM sessions WHERE user_id=? AND token!=?`, u.ID, tok)
		return e
	})
	return err
}

func validNewPassword(s string) bool {
	if len(s) < 8 || len(s) > 72 {
		return false
	}
	upper, lower, digit, special := false, false, false, false
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= '0' && r <= '9':
			digit = true
		default:
			special = true
		}
	}
	return upper && lower && digit && special
}
