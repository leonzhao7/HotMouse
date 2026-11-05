package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kataras/iris/v12"
	iriscontext "github.com/kataras/iris/v12/context"
	"hotmouse/backend/internal/core"
	"hotmouse/backend/internal/worker"
)

type Handler struct {
	DB        *core.DB
	captchaMu sync.Mutex
	captchas  map[string]captchaChallenge
}

type captchaChallenge struct {
	answer  string
	expires time.Time
}

var registrationUsernamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func New(db *core.DB) *Handler { return &Handler{DB: db, captchas: map[string]captchaChallenge{}} }
func bad(ctx iris.Context, err error) {
	status := iris.StatusBadRequest
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "must not be empty") || strings.Contains(lower, "from must") || strings.Contains(lower, "fact ids") || strings.Contains(lower, "invalid severity") || strings.Contains(lower, "invalid status") || strings.Contains(lower, "invalid username") || strings.Contains(lower, "password must") || strings.Contains(lower, "finding title") || strings.Contains(lower, "finding description") || strings.Contains(lower, "finding kind") {
		status = iris.StatusUnprocessableEntity
	}
	if err == sql.ErrNoRows || err == core.ErrNotFound || strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "no rows") {
		status = iris.StatusNotFound
	}
	if strings.HasPrefix(err.Error(), "project is ") {
		status = iris.StatusForbidden
	}
	if strings.Contains(err.Error(), "currently claimed") || strings.Contains(err.Error(), "already") || strings.Contains(err.Error(), "completed") || strings.Contains(err.Error(), "not completed") || strings.Contains(err.Error(), "no active goal") || strings.Contains(strings.ToLower(err.Error()), "limit reached") {
		status = iris.StatusConflict
	}
	if strings.Contains(lower, "too many login attempts") {
		status = iris.StatusTooManyRequests
	}
	if strings.Contains(lower, "invalid captcha") {
		status = iris.StatusBadRequest
	} else if strings.Contains(lower, "authentication") || strings.Contains(lower, "invalid credentials") {
		status = iris.StatusUnauthorized
	}
	ctx.StopWithJSON(status, iris.Map{"detail": err.Error()})
}
func read[T any](ctx iris.Context, v *T) bool {
	if err := ctx.ReadJSON(v); err != nil {
		ctx.StopWithJSON(iris.StatusUnprocessableEntity, iris.Map{"detail": err.Error()})
		return false
	}
	return true
}

func readAuth[T any](ctx iris.Context, v *T) bool {
	if err := ctx.ReadJSON(v); err != nil {
		ctx.StopWithJSON(iris.StatusUnprocessableEntity, iris.Map{"detail": err.Error()})
		return false
	}
	return true
}

func clearSessionCookies(ctx iris.Context) {
	secure := sessionCookieSecure(ctx)
	ctx.RemoveCookie("session_token", secure)
	ctx.RemoveCookie("hotmouse_session", secure)
}

func badAuth(ctx iris.Context, err error) {
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "captcha") || strings.Contains(lower, "invalid credentials") || strings.Contains(lower, "authentication") || strings.Contains(lower, "too many login attempts") {
		bad(ctx, err)
		return
	}
	ctx.StopWithJSON(iris.StatusUnprocessableEntity, iris.Map{"detail": err.Error()})
}

func registrationValidationError(username, password string) string {
	if strings.TrimSpace(username) == "" {
		return "username must not be empty"
	}
	if len(username) < 3 || len(username) > 32 || !registrationUsernamePattern.MatchString(username) {
		return "invalid username"
	}
	if password == "" {
		return "password must not be empty"
	}
	if len(password) < 8 || len(password) > 72 {
		return "password must be between 8 and 72 characters"
	}
	return ""
}

func rejectAuth(ctx iris.Context) bool {
	// Navigation requests go back to the login shell, while API clients receive
	// a JSON 401.
	clearSessionCookies(ctx)
	if strings.Contains(strings.ToLower(ctx.GetHeader("Accept")), "text/html") {
		ctx.Redirect("/", iris.StatusFound)
		return false
	}
	ctx.StopWithJSON(iris.StatusUnauthorized, iris.Map{"detail": "authentication required"})
	return false
}
func (h *Handler) auth(ctx iris.Context) bool {
	if ctx.Path() == "/" || ctx.Path() == "/healthz" || ctx.Path() == "/captcha" || strings.HasPrefix(ctx.Path(), "/auth/") || strings.HasPrefix(ctx.Path(), "/api/auth/") || ctx.Path() == "/register" || ctx.Path() == "/login" || ctx.Path() == "/logout" || ctx.Path() == "/me" {
		return true
	}
	if os.Getenv("HOTMOUSE_AUTH_REQUIRED") == "0" {
		return true
	}
	if configured := internalToken(); configured != "" {
		for _, header := range []string{"X-HotMouse-Internal-Token", "X-Cairn-Internal-Token"} {
			presented := ctx.GetHeader(header)
			if presented != "" && subtle.ConstantTimeCompare([]byte(presented), []byte(configured)) == 1 {
				ctx.Values().Set("auth_kind", "internal")
				return true
			}
		}
	}
	tok := ctx.GetCookie("session_token")
	if tok == "" {
		tok = ctx.GetCookie("hotmouse_session")
	}
	if tok != "" {
		if u, e := h.DB.Session(ctx.Request().Context(), tok); e == nil {
			ctx.Values().Set("user_id", u.ID)
			ctx.Values().Set("auth_kind", "session")
			// Session() applies the sliding expiration server-side. Refresh the
			// browser cookie as well so its client-side lifetime follows it.
			h.setSessionCookie(ctx, tok)
			return true
		}
	}
	return rejectAuth(ctx)
}

func internalToken() string {
	for _, key := range []string{"HOTMOUSE_INTERNAL_TOKEN", "CAIRN_INTERNAL_TOKEN"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func (h *Handler) Register(ctx iris.Context) {
	var r struct {
		Username      string `json:"username"`
		Password      string `json:"password"`
		CaptchaID     string `json:"captcha_id"`
		CaptchaAnswer string `json:"captcha_answer"`
	}
	if !readAuth(ctx, &r) {
		return
	}
	if detail := registrationValidationError(r.Username, r.Password); detail != "" {
		ctx.StopWithJSON(iris.StatusUnprocessableEntity, iris.Map{"detail": detail})
		return
	}
	if value := strings.ToLower(strings.TrimSpace(os.Getenv("CAIRN_ALLOW_REGISTRATION"))); value != "" && value != "1" && value != "true" && value != "yes" && value != "on" {
		ctx.StopWithJSON(iris.StatusForbidden, iris.Map{"detail": "registration is disabled"})
		return
	}
	if !h.verifyCaptcha(r.CaptchaID, r.CaptchaAnswer) {
		bad(ctx, fmt.Errorf("invalid captcha"))
		return
	}
	u, e := h.DB.Register(ctx.Request().Context(), r.Username, r.Password)
	if e != nil {
		badAuth(ctx, e)
		return
	}
	ctx.StatusCode(iris.StatusCreated)
	if tok, _, err := h.DB.Login(ctx.Request().Context(), r.Username, r.Password); err == nil {
		h.setSessionCookie(ctx, tok)
	}
	ctx.JSON(u)
}
func (h *Handler) Login(ctx iris.Context) {
	var r struct {
		Username      string `json:"username"`
		Password      string `json:"password"`
		CaptchaID     string `json:"captcha_id"`
		CaptchaAnswer string `json:"captcha_answer"`
	}
	if !readAuth(ctx, &r) {
		return
	}
	if strings.TrimSpace(r.Username) == "" || r.Password == "" {
		ctx.StopWithJSON(iris.StatusUnprocessableEntity, iris.Map{"detail": "username and password must not be empty"})
		return
	}
	if !h.verifyCaptcha(r.CaptchaID, r.CaptchaAnswer) {
		bad(ctx, fmt.Errorf("invalid captcha"))
		return
	}
	tok, u, e := h.DB.Login(ctx.Request().Context(), r.Username, r.Password)
	if e != nil {
		badAuth(ctx, e)
		return
	}
	h.setSessionCookie(ctx, tok)
	ctx.JSON(u)
}
func (h *Handler) setSessionCookie(ctx iris.Context, tok string) {
	duration := 24
	_ = h.DB.SQL.QueryRowContext(ctx.Request().Context(), `SELECT session_duration_hours FROM settings WHERE id=1`).Scan(&duration)
	if duration <= 0 {
		duration = 24
	}
	options := []iris.CookieOption{
		iris.CookieExpires(time.Duration(duration) * time.Hour),
		iris.CookieHTTPOnly(true),
		iris.CookieSameSite(iris.SameSiteStrictMode),
		sessionCookieSecure(ctx),
	}
	ctx.SetCookieKV("session_token", tok, options...)
	// Keep the historical alias for existing local clients. Both cookies carry
	// the same server-side session token.
	ctx.SetCookieKV("hotmouse_session", tok, options...)
}
func (h *Handler) Logout(ctx iris.Context) {
	tok := ctx.GetCookie("session_token")
	if tok == "" {
		tok = ctx.GetCookie("hotmouse_session")
	}
	_ = h.DB.Logout(ctx.Request().Context(), tok)
	secure := sessionCookieSecure(ctx)
	ctx.RemoveCookie("session_token", secure)
	ctx.RemoveCookie("hotmouse_session", secure)
	ctx.StatusCode(iris.StatusNoContent)
}

func sessionCookieSecure(ctx iris.Context) iris.CookieOption {
	req := ctx.Request()
	host := strings.ToLower(req.URL.Hostname())
	if host == "" {
		host = req.Host
		if splitHost, _, err := net.SplitHostPort(host); err == nil {
			host = splitHost
		} else {
			host = strings.Trim(host, "[]")
		}
		host = strings.ToLower(host)
	}
	scheme := strings.ToLower(req.URL.Scheme)
	if req.TLS != nil {
		scheme = "https"
	} else if scheme == "" {
		scheme = "http"
	}
	secure := !(scheme == "http" && (host == "localhost" || host == "127.0.0.1" || host == "::1"))
	return func(_ *iriscontext.Context, cookie *iris.Cookie, _ uint8) {
		cookie.Secure = secure
	}
}

func (h *Handler) Me(ctx iris.Context) {
	tok := ctx.GetCookie("session_token")
	if tok == "" {
		tok = ctx.GetCookie("hotmouse_session")
	}
	u, e := h.DB.Session(ctx.Request().Context(), tok)
	if e != nil {
		clearSessionCookies(ctx)
		bad(ctx, e)
		return
	}
	ctx.JSON(u)
}
func (h *Handler) Captcha(ctx iris.Context) {
	left, _ := rand.Int(rand.Reader, big.NewInt(8))
	right, _ := rand.Int(rand.Reader, big.NewInt(8))
	a, b := left.Int64()+2, right.Int64()+2
	buf := make([]byte, 24)
	_, _ = rand.Read(buf)
	id := fmt.Sprintf("%x", buf)
	h.captchaMu.Lock()
	for key, challenge := range h.captchas {
		if time.Now().After(challenge.expires) {
			delete(h.captchas, key)
		}
	}
	h.captchas[id] = captchaChallenge{answer: fmt.Sprint(a + b), expires: time.Now().Add(5 * time.Minute)}
	h.captchaMu.Unlock()
	ctx.JSON(iris.Map{"id": id, "captcha_id": id, "question": fmt.Sprintf("%d + %d = ?", a, b), "expires_in": 300})
}
func (h *Handler) verifyCaptcha(id, answer string) bool {
	h.captchaMu.Lock()
	defer h.captchaMu.Unlock()
	c, ok := h.captchas[id]
	delete(h.captchas, id)
	return ok && time.Now().Before(c.expires) && strings.TrimSpace(answer) == c.answer
}
func (h *Handler) ChangePassword(ctx iris.Context) {
	var r struct {
		OldPassword     string `json:"old_password"`
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !readAuth(ctx, &r) {
		return
	}
	if r.OldPassword == "" && r.CurrentPassword == "" || r.NewPassword == "" {
		ctx.StopWithJSON(iris.StatusUnprocessableEntity, iris.Map{"detail": "current_password and new_password are required"})
		return
	}
	if r.OldPassword == "" {
		r.OldPassword = r.CurrentPassword
	}
	tok := ctx.GetCookie("session_token")
	if tok == "" {
		tok = ctx.GetCookie("hotmouse_session")
	}
	if e := h.DB.ChangePassword(ctx.Request().Context(), tok, r.OldPassword, r.NewPassword); e != nil {
		if strings.Contains(strings.ToLower(e.Error()), "new password") {
			ctx.StopWithJSON(iris.StatusUnprocessableEntity, iris.Map{"detail": e.Error()})
		} else {
			if strings.Contains(strings.ToLower(e.Error()), "authentication required") {
				clearSessionCookies(ctx)
			}
			bad(ctx, e)
		}
		return
	}
	ctx.StatusCode(iris.StatusNoContent)
}
func (h *Handler) Health(ctx iris.Context) {
	ctx.JSON(iris.Map{"status": "ok", "service": "hotmouse", "database": "sqlite", "worker_mode": "dispatcher_pool"})
}
func (h *Handler) GetSettings(ctx iris.Context) {
	var s core.Settings
	e := h.DB.SQL.QueryRowContext(ctx.Request().Context(), `SELECT step_timeout,decide_timeout,worker_unhealthy_retry_after_seconds,worker_rejected_retry_after_seconds,max_failed_login_attempts,rate_limit_window_minutes,session_duration_hours,log_retention_days,export_retention_days,notification_retention_days,project_idle_alert_hours FROM settings WHERE id=1`).Scan(&s.StepTimeout, &s.DecideTimeout, &s.WorkerUnhealthyRetryAfterSeconds, &s.WorkerRejectedRetryAfterSeconds, &s.MaxFailedLoginAttempts, &s.RateLimitWindowMinutes, &s.SessionDurationHours, &s.LogRetentionDays, &s.ExportRetentionDays, &s.NotificationRetentionDays, &s.ProjectIdleAlertHours)
	if e != nil {
		bad(ctx, e)
		return
	}
	ctx.JSON(s)
}
func (h *Handler) UpdateSettings(ctx iris.Context) {
	var s core.Settings
	if !read(ctx, &s) {
		return
	}
	if s.StepTimeout < 5 || s.DecideTimeout < 5 || s.WorkerUnhealthyRetryAfterSeconds < 1 || s.WorkerUnhealthyRetryAfterSeconds > 3600 || s.WorkerRejectedRetryAfterSeconds < 1 || s.WorkerRejectedRetryAfterSeconds > 3600 || s.MaxFailedLoginAttempts < 1 || s.MaxFailedLoginAttempts > 50 || s.RateLimitWindowMinutes < 1 || s.RateLimitWindowMinutes > 1440 || s.SessionDurationHours < 1 || s.SessionDurationHours > 168 || s.LogRetentionDays < 1 || s.LogRetentionDays > 3650 || s.ExportRetentionDays < 1 || s.ExportRetentionDays > 3650 || s.NotificationRetentionDays < 1 || s.NotificationRetentionDays > 3650 || s.ProjectIdleAlertHours < 1 || s.ProjectIdleAlertHours > 720 {
		bad(ctx, fmt.Errorf("invalid settings"))
		return
	}
	_, e := h.DB.SQL.ExecContext(ctx.Request().Context(), `UPDATE settings SET step_timeout=?,decide_timeout=?,worker_unhealthy_retry_after_seconds=?,worker_rejected_retry_after_seconds=?,max_failed_login_attempts=?,rate_limit_window_minutes=?,session_duration_hours=?,log_retention_days=?,export_retention_days=?,notification_retention_days=?,project_idle_alert_hours=? WHERE id=1`, s.StepTimeout, s.DecideTimeout, s.WorkerUnhealthyRetryAfterSeconds, s.WorkerRejectedRetryAfterSeconds, s.MaxFailedLoginAttempts, s.RateLimitWindowMinutes, s.SessionDurationHours, s.LogRetentionDays, s.ExportRetentionDays, s.NotificationRetentionDays, s.ProjectIdleAlertHours)
	if e != nil {
		bad(ctx, e)
		return
	}
	_, _ = h.DB.SQL.ExecContext(ctx.Request().Context(), `INSERT INTO audit_log(user_id,action,target_type,target_id,summary,detail,resource,detail_json,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, ctx.Values().Get("user_id"), "settings.update", "settings", "settings", "System settings updated", coreJSON(map[string]any{"settings": s}), "settings", coreJSON(map[string]any{"settings": s}), nowString())
	_, _ = h.DB.SQL.ExecContext(ctx.Request().Context(), `INSERT INTO notifications(level,title,body,read,created_at) VALUES('success','System settings updated','Scheduling and retention policy changed',0,?)`, nowString())
	h.GetSettings(ctx)
}
func (h *Handler) SettingsHealth(ctx iris.Context) {
	var projects, active, vulns, users, audit, unread, history, recoveryAttention int
	_ = h.DB.SQL.QueryRow(`SELECT count(*) FROM projects`).Scan(&projects)
	_ = h.DB.SQL.QueryRow(`SELECT count(*) FROM projects WHERE status='active'`).Scan(&active)
	_ = h.DB.SQL.QueryRow(`SELECT count(*) FROM vulnerabilities`).Scan(&vulns)
	_ = h.DB.SQL.QueryRow(`SELECT count(*) FROM users`).Scan(&users)
	_ = h.DB.SQL.QueryRow(`SELECT count(*) FROM audit_log`).Scan(&audit)
	_ = h.DB.SQL.QueryRow(`SELECT count(*) FROM notifications WHERE read=0`).Scan(&unread)
	_ = h.DB.SQL.QueryRow(`SELECT count(*) FROM worker_task_history`).Scan(&history)
	_ = h.DB.SQL.QueryRow(`SELECT count(*) FROM worker_execute_recovery WHERE status='needs_attention' OR (status='running' AND payload_json='')`).Scan(&recoveryAttention)
	var enabled int
	var driver, command, environment string
	_ = h.DB.SQL.QueryRow(`SELECT driver,command,environment_json,enabled FROM worker_config WHERE id=1`).Scan(&driver, &command, &environment, &enabled)
	var idleHours, maxFailedLogins, rateWindow, sessionHours, logDays, exportDays, notifyDays int
	_ = h.DB.SQL.QueryRow(`SELECT project_idle_alert_hours,max_failed_login_attempts,rate_limit_window_minutes,session_duration_hours,log_retention_days,export_retention_days,notification_retention_days FROM settings WHERE id=1`).Scan(&idleHours, &maxFailedLogins, &rateWindow, &sessionHours, &logDays, &exportDays, &notifyDays)
	authRequired := os.Getenv("HOTMOUSE_AUTH_REQUIRED") != "0"
	workerReady := enabled != 0
	workerBinaryName := ""
	if workerReady {
		if command == "" {
			workerBinaryName = workerBinaryFor(driver, parseJSON(environment))
			if driver == "mock" {
				workerReady = true
			} else if workerBinaryName == "" {
				workerReady = false
			} else if _, err := exec.LookPath(workerBinaryName); err != nil {
				workerReady = false
			}
		} else if _, err := exec.LookPath("sh"); err != nil {
			workerReady = false
		}
	}
	online := 0
	if workerReady {
		online = 1
	}
	status := "ok"
	if !workerReady {
		if active > 0 {
			status = "error"
		} else {
			status = "warning"
		}
	}
	alerts := []iris.Map{}
	if recoveryAttention > 0 {
		alerts = append(alerts, iris.Map{"level": "warning", "title": "Worker steps need attention", "detail": fmt.Sprintf("%d interrupted or unrecoverable Execute step(s) are held to prevent repeating external actions.", recoveryAttention)})
	}
	if idleHours < 1 {
		idleHours = 12
	}
	rows, _ := h.DB.SQL.Query(`SELECT p.id,p.title,p.created_at,p.decide_last_heartbeat_at,MAX(s.created_at),MAX(s.concluded_at),MAX(h.created_at) FROM projects p LEFT JOIN steps s ON s.project_id=p.id LEFT JOIN hints h ON h.project_id=p.id WHERE p.status='active' GROUP BY p.id,p.title,p.created_at,p.decide_last_heartbeat_at ORDER BY p.created_at`)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var id, title, created string
			var decide, stepCreated, concluded, hintCreated sql.NullString
			if rows.Scan(&id, &title, &created, &decide, &stepCreated, &concluded, &hintCreated) != nil {
				continue
			}
			latest := created
			for _, value := range []sql.NullString{decide, stepCreated, concluded, hintCreated} {
				if value.Valid && value.String > latest {
					latest = value.String
				}
			}
			if at, err := time.Parse("2006-01-02T15:04:05Z", latest); err == nil {
				hours := time.Since(at).Hours()
				if hours >= float64(idleHours) {
					alerts = append(alerts, iris.Map{"level": "warning", "title": "Project " + title + " has been idle", "detail": fmt.Sprintf("%s has had no activity for approximately %.1f hours (threshold %d).", id, hours, idleHours)})
				}
			}
		}
	}
	authStatus := "ok"
	authSummary := fmt.Sprintf("%d failed attempts per %d-minute window", maxFailedLogins, rateWindow)
	authDetail := fmt.Sprintf("Sessions expire after %d hours; captcha is enabled.", sessionHours)
	if !authRequired {
		authStatus = "warning"
		authSummary = "authentication is disabled for local development"
		authDetail = fmt.Sprintf("Session and captcha checks are bypassed; configured session duration is %d hours.", sessionHours)
	}
	authCheck := iris.Map{"key": "auth", "label": "Authentication policy", "status": authStatus, "summary": authSummary, "detail": authDetail}
	retentionStatus := "ok"
	if logDays < 1 || exportDays < 1 || notifyDays < 1 {
		retentionStatus = "warning"
	}
	retentionCheck := iris.Map{"key": "retention", "label": "Retention policy", "status": retentionStatus, "summary": fmt.Sprintf("logs %d days, exports %d days", logDays, exportDays), "detail": fmt.Sprintf("Notifications are kept %d days; unread notifications: %d; worker history rows: %d.", notifyDays, unread, history)}
	var workerCount int
	_ = h.DB.SQL.QueryRow(`SELECT worker_count FROM worker_config WHERE id=1`).Scan(&workerCount)
	checks := []iris.Map{{"key": "server", "label": "API service", "status": "ok", "summary": "product API is reachable", "detail": "Project, dispatcher, worker and settings APIs are served by this process."}, {"key": "database", "label": "SQLite database", "status": "ok", "summary": fmt.Sprintf("%d projects and %d vulnerabilities", projects, vulns), "detail": fmt.Sprintf("%d users and %d audit entries.", users, audit)}, {"key": "dispatcher", "label": "Project Dispatcher", "status": func() string {
		if !workerReady {
			if active > 0 {
				return "error"
			}
			return "warning"
		}
		return "ok"
	}(), "summary": func() string {
		if !workerReady {
			if enabled == 0 {
				return "worker disabled"
			}
			if workerBinaryName != "" {
				return workerBinaryName + " is not available on PATH"
			}
			return "worker command is not available"
		}
		return fmt.Sprintf("one dispatcher per project; %d agent workers configured", workerCount)
	}(), "detail": "Dispatcher maintains project facts and planning; agent workers execute steps."}, authCheck, retentionCheck}
	if !workerReady && active > 0 {
		alerts = append(alerts, iris.Map{"level": "danger", "title": "Active projects have no available worker", "detail": "The local worker is disabled or its configured command is unavailable."})
	}
	if len(alerts) > 0 && status == "ok" {
		status = "warning"
	}
	if !authRequired && status == "ok" {
		status = "warning"
	}
	if retentionStatus == "warning" && status == "ok" {
		status = "warning"
	}
	ctx.JSON(iris.Map{"generated_at": nowString(), "summary": iris.Map{"status": status, "server_reachable": true, "dispatcher_reachable": workerReady, "active_projects": active, "online_workers": online * workerCount, "offline_workers": func() int {
		if enabled != 0 && !workerReady {
			return 1
		}
		return 0
	}()}, "stats": iris.Map{"users": users, "projects": projects, "active_projects": active, "vulnerabilities": vulns, "audit_entries": audit, "notifications_unread": unread, "worker_task_history": history, "worker_recovery_attention": recoveryAttention}, "checks": checks, "alerts": alerts})
}
func (h *Handler) SettingsCleanup(ctx iris.Context) {
	var logDays, notifyDays int
	_ = h.DB.SQL.QueryRowContext(ctx.Request().Context(), `SELECT log_retention_days,notification_retention_days FROM settings WHERE id=1`).Scan(&logDays, &notifyDays)
	if logDays < 1 {
		logDays = 30
	}
	if notifyDays < 1 {
		notifyDays = 14
	}
	cutoff := nowString()
	deleted := map[string]int{}
	queries := map[string]struct {
		query string
		at    string
	}{"expired_sessions": {`DELETE FROM sessions WHERE expires_at <= ?`, cutoff}, "audit_log": {`DELETE FROM audit_log WHERE created_at < ?`, time.Now().UTC().AddDate(0, 0, -logDays).Format("2006-01-02T15:04:05Z")}, "worker_task_history": {`DELETE FROM worker_task_history WHERE COALESCE(finished_at,started_at) < ?`, time.Now().UTC().AddDate(0, 0, -logDays).Format("2006-01-02T15:04:05Z")}, "login_attempts": {`DELETE FROM login_attempts WHERE attempted_at < ?`, time.Now().UTC().AddDate(0, 0, -logDays).Format("2006-01-02T15:04:05Z")}, "notifications": {`DELETE FROM notifications WHERE read_at IS NOT NULL AND created_at < ?`, time.Now().UTC().AddDate(0, 0, -notifyDays).Format("2006-01-02T15:04:05Z")}}
	for name, item := range queries {
		res, e := h.DB.SQL.ExecContext(ctx.Request().Context(), item.query, item.at)
		if e != nil {
			bad(ctx, e)
			return
		}
		n, _ := res.RowsAffected()
		deleted[name] = int(n)
	}
	total := 0
	for _, n := range deleted {
		total += n
	}
	summary := fmt.Sprintf("cleaned %d historical records", total)
	_, _ = h.DB.SQL.ExecContext(ctx.Request().Context(), `INSERT INTO audit_log(action,target_type,target_id,summary,detail,resource,detail_json,created_at) VALUES('settings.cleanup','settings','settings','System cleanup completed',?,?,?,?)`, coreJSON(map[string]any{"deleted": deleted}), "settings", coreJSON(map[string]any{"deleted": deleted}), cutoff)
	_, _ = h.DB.SQL.ExecContext(ctx.Request().Context(), `INSERT INTO notifications(level,title,body,read,created_at) VALUES('info','System cleanup completed',?,0,?)`, summary, cutoff)
	ctx.JSON(iris.Map{"ran_at": cutoff, "deleted": deleted, "summary": summary})
}

func (h *Handler) Timeline(ctx iris.Context) {
	p := ctx.Params().Get("project_id")
	v, e := h.DB.GetProjectReadOnly(ctx.Request().Context(), p)
	if e != nil {
		bad(ctx, e)
		return
	}
	type event struct {
		at   string
		seq  int
		body iris.Map
	}
	var all []event
	seq := 0
	factByID := map[string]string{}
	for _, f := range v.Facts {
		factByID[f.ID] = f.Description
	}
	// Project detail orders steps by priority for graph consumers. Timeline
	// events follow their declaration order for chronological replay.
	steps := append([]core.Step(nil), v.Steps...)
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].CreatedAt < steps[j].CreatedAt })
	for _, s := range steps {
		all = append(all, event{s.CreatedAt, seq, iris.Map{"id": "step_declaration:" + s.ID, "event_type": "step_declaration", "description": s.Description, "timestamp": s.CreatedAt, "actor": s.Creator, "node_id": s.ID}})
		seq++
		if s.ConcludedAt == nil {
			continue
		}
		actor := s.Creator
		if s.Worker != nil && *s.Worker != "" {
			actor = *s.Worker
		}
		all = append(all, event{*s.ConcludedAt, seq, iris.Map{"id": "step_conclusion:" + s.ID, "event_type": "step_conclusion", "description": s.Description, "timestamp": *s.ConcludedAt, "actor": actor, "node_id": s.ID}})
		seq++
		if s.To != nil {
			if desc, ok := factByID[*s.To]; ok {
				all = append(all, event{*s.ConcludedAt, seq, iris.Map{"id": "fact_discovery:" + *s.To, "event_type": "fact_discovery", "description": desc, "timestamp": *s.ConcludedAt, "actor": nil, "node_id": *s.To}})
				seq++
			}
		}
	}
	for _, g := range v.Goals {
		if g.CompletedAt == nil {
			continue
		}
		all = append(all, event{*g.CompletedAt, seq, iris.Map{"id": "goal_completion:" + g.ID, "event_type": "goal_completion", "description": g.Description, "timestamp": *g.CompletedAt, "actor": g.CompletedBy, "node_id": g.ID}})
		seq++
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].at == all[j].at {
			return all[i].seq < all[j].seq
		}
		return all[i].at < all[j].at
	})
	events := make([]iris.Map, 0, len(all))
	for _, e := range all {
		events = append(events, e.body)
	}
	ctx.JSON(events)
}
func (h *Handler) Campaign(ctx iris.Context) {
	v, e := h.campaign(ctx.Request().Context(), ctx.Params().Get("project_id"))
	if e != nil {
		bad(ctx, e)
		return
	}
	ctx.JSON(v)
}

func (h *Handler) WorkerConfig(ctx iris.Context) {
	var name, driver, command, env, updated, workspaceRoot, completedAction, contextRoot, runtimeJSON string
	var enabled int
	var workerCount int
	e := h.DB.SQL.QueryRowContext(ctx.Request().Context(), `SELECT name,driver,command,environment_json,enabled,updated_at,workspace_root,completed_action,context_root,runtime_json,worker_count FROM worker_config WHERE id=1`).Scan(&name, &driver, &command, &env, &enabled, &updated, &workspaceRoot, &completedAction, &contextRoot, &runtimeJSON, &workerCount)
	if e != nil {
		bad(ctx, e)
		return
	}
	runtime := core.DefaultWorkerRuntime()
	_ = json.Unmarshal([]byte(runtimeJSON), &runtime)
	environment := parseJSON(env)
	if ctx.URLParam("reveal") != "1" && ctx.URLParam("reveal") != "true" {
		environment = maskEnvironment(environment)
	} else {
		ctx.Header("Cache-Control", "no-store")
	}
	ctx.JSON(iris.Map{"name": name, "driver": driver, "command": command, "environment": environment, "enabled": enabled != 0, "updated_at": updated, "worker_mode": "dispatcher_pool", "worker_count": workerCount, "workspace_root": workspaceRoot, "completed_action": completedAction, "context_root": contextRoot, "runtime": runtime})
}
func (h *Handler) UpdateWorkerConfig(ctx iris.Context) {
	var r struct {
		Name              string            `json:"name"`
		Driver            string            `json:"driver"`
		Command           string            `json:"command"`
		WorkspaceRoot     *string           `json:"workspace_root"`
		CompletedAction   *string           `json:"completed_action"`
		ContextRoot       *string           `json:"context_root"`
		Runtime           json.RawMessage   `json:"runtime"`
		WorkerCount       *int              `json:"worker_count"`
		Environment       map[string]string `json:"environment"`
		EnvironmentRemove []string          `json:"environment_remove"`
		Enabled           *bool             `json:"enabled"`
	}
	if !read(ctx, &r) {
		return
	}
	if r.Name == "" {
		r.Name = "local"
	}
	workerCount := 4
	if err := h.DB.SQL.QueryRowContext(ctx.Request().Context(), `SELECT worker_count FROM worker_config WHERE id=1`).Scan(&workerCount); err != nil {
		bad(ctx, err)
		return
	}
	if r.WorkerCount != nil {
		workerCount = *r.WorkerCount
	}
	if workerCount < 1 || workerCount > 32 {
		bad(ctx, fmt.Errorf("worker_count must be between 1 and 32"))
		return
	}
	if r.Driver == "" {
		r.Driver = "mock"
	}
	if !supportedDriver(r.Driver) {
		bad(ctx, fmt.Errorf("unsupported local worker driver"))
		return
	}
	var workspaceRoot, completedAction, contextRoot, runtimeJSON, existingEnv string
	if err := h.DB.SQL.QueryRowContext(ctx.Request().Context(), `SELECT workspace_root,completed_action,context_root,runtime_json,environment_json FROM worker_config WHERE id=1`).Scan(&workspaceRoot, &completedAction, &contextRoot, &runtimeJSON, &existingEnv); err != nil {
		bad(ctx, err)
		return
	}
	runtime := core.DefaultWorkerRuntime()
	_ = json.Unmarshal([]byte(runtimeJSON), &runtime)
	if len(r.Runtime) > 0 {
		if string(r.Runtime) == "null" {
			bad(ctx, fmt.Errorf("runtime must be an object"))
			return
		}
		if err := json.Unmarshal(r.Runtime, &runtime); err != nil {
			bad(ctx, err)
			return
		}
	}
	if err := runtime.Validate(); err != nil {
		bad(ctx, err)
		return
	}
	if r.WorkspaceRoot != nil {
		workspaceRoot = strings.TrimSpace(*r.WorkspaceRoot)
	}
	if r.ContextRoot != nil {
		contextRoot = strings.TrimSpace(*r.ContextRoot)
	}
	if r.CompletedAction != nil {
		completedAction = strings.TrimSpace(*r.CompletedAction)
	}
	if completedAction == "" {
		completedAction = "keep"
	}
	if completedAction != "keep" && completedAction != "remove" {
		bad(ctx, fmt.Errorf("cleanup action must be keep or remove"))
		return
	}
	en := 1
	if r.Enabled != nil && !*r.Enabled {
		en = 0
	}
	env := parseJSON(existingEnv)
	for _, key := range r.EnvironmentRemove {
		if strings.TrimSpace(key) == "" {
			bad(ctx, fmt.Errorf("environment_remove keys must not be empty"))
			return
		}
		if _, exists := r.Environment[key]; exists {
			bad(ctx, fmt.Errorf("environment key cannot be set and removed together"))
			return
		}
		delete(env, key)
	}
	for k, v := range r.Environment {
		if v == "***" {
			continue
		}
		env[k] = v
	}
	runtimeBytes, _ := json.Marshal(runtime)
	_, e := h.DB.SQL.ExecContext(ctx.Request().Context(), `UPDATE worker_config SET name=?,driver=?,command=?,environment_json=?,enabled=?,updated_at=?,workspace_root=?,completed_action=?,context_root=?,runtime_json=?,worker_count=? WHERE id=1`, r.Name, r.Driver, r.Command, coreJSON(env), en, nowString(), workspaceRoot, completedAction, contextRoot, string(runtimeBytes), workerCount)
	if e != nil {
		bad(ctx, e)
		return
	}
	h.WorkerConfig(ctx)
}
func maskEnvironment(env map[string]any) map[string]any {
	for k, v := range env {
		lk := strings.ToLower(k)
		if strings.Contains(lk, "key") || strings.Contains(lk, "token") || strings.Contains(lk, "secret") || strings.Contains(lk, "password") {
			if s, ok := v.(string); ok && s != "" {
				env[k] = "***"
			}
		}
	}
	return env
}
func (h *Handler) WorkerList(ctx iris.Context) {
	worker, err := h.workerSnapshot(ctx.Request().Context())
	if err != nil {
		bad(ctx, err)
		return
	}
	ctx.JSON([]iris.Map{worker})
}
func (h *Handler) WorkerObservability(ctx iris.Context) {
	requestCtx := ctx.Request().Context()
	worker, err := h.workerSnapshot(ctx.Request().Context())
	if err != nil {
		bad(ctx, err)
		return
	}
	status := worker["status"].(string)
	var attention, pending int
	if err := h.DB.SQL.QueryRowContext(requestCtx, `SELECT count(CASE WHEN status='needs_attention' OR (status='running' AND payload_json='') THEN 1 END),count(CASE WHEN status='summary_pending' THEN 1 END) FROM worker_execute_recovery`).Scan(&attention, &pending); err != nil {
		bad(ctx, err)
		return
	}
	workerName := worker["name"].(string)
	var workerCount int
	_ = h.DB.SQL.QueryRowContext(requestCtx, `SELECT worker_count FROM worker_config WHERE id=1`).Scan(&workerCount)
	projectNames := map[string]string{}
	projectRows, err := h.DB.SQL.QueryContext(requestCtx, `SELECT id,title FROM projects`)
	if err != nil {
		bad(ctx, err)
		return
	}
	for projectRows.Next() {
		var id, title string
		if err := projectRows.Scan(&id, &title); err == nil {
			projectNames[id] = title
		}
	}
	if err := projectRows.Err(); err != nil {
		projectRows.Close()
		bad(ctx, err)
		return
	}
	projectRows.Close()

	type historyRow struct {
		worker, projectID, projectName, taskType, status, started, detail string
		stepID, finished                                                  sql.NullString
	}
	historyRows, err := h.DB.SQL.QueryContext(requestCtx, `SELECT h.worker,h.project_id,COALESCE(p.title,''),h.task_type,h.status,h.started_at,h.finished_at,h.step_id,h.detail FROM worker_task_history h LEFT JOIN projects p ON p.id=h.project_id WHERE (h.worker LIKE ? OR h.worker=?) ORDER BY h.id DESC LIMIT 40`, workerName+"/%", workerName)
	if err != nil {
		bad(ctx, err)
		return
	}
	history := []historyRow{}
	for historyRows.Next() {
		var row historyRow
		if err := historyRows.Scan(&row.worker, &row.projectID, &row.projectName, &row.taskType, &row.status, &row.started, &row.finished, &row.stepID, &row.detail); err != nil {
			historyRows.Close()
			bad(ctx, err)
			return
		}
		history = append(history, row)
	}
	if err := historyRows.Err(); err != nil {
		historyRows.Close()
		bad(ctx, err)
		return
	}
	historyRows.Close()

	outcomes := iris.Map{"success": 0, "failed": 0, "rejected": 0, "released": 0}
	taskMix := map[string]int{}
	recent := []iris.Map{}
	for _, row := range history {
		outcome := historyOutcome(row.status)
		if outcome == "running" {
			continue
		}
		outcomes[outcome] = outcomes[outcome].(int) + 1
		taskMix[row.taskType]++
		if len(recent) < 12 {
			recent = append(recent, iris.Map{"worker_name": row.worker, "project_id": row.projectID, "project_name": row.projectName, "task_type": row.taskType, "outcome": outcome, "started_at": row.started, "completed_at": nullableString(row.finished), "duration_seconds": historyDuration(row.started, row.finished), "step_id": nullableString(row.stepID), "intent_id": nullableString(row.stepID)})
		}
	}
	var running int
	_ = h.DB.SQL.QueryRowContext(requestCtx, `SELECT count(*) FROM worker_task_history WHERE status='running' AND (worker LIKE ? OR worker=?)`, workerName+"/%", workerName).Scan(&running)
	if worker["running"].(bool) && running == 0 {
		running = 1
	}
	onlineWorkers := workerCount * boolCount(status == "idle" || status == "busy")
	busyWorkers := running
	if busyWorkers > onlineWorkers {
		busyWorkers = onlineWorkers
	}
	idleWorkers := onlineWorkers - busyWorkers
	taskMixRows := []iris.Map{}
	for taskType, count := range taskMix {
		taskMixRows = append(taskMixRows, iris.Map{"task_type": taskType, "count": count})
	}
	sort.Slice(taskMixRows, func(i, j int) bool {
		left, _ := taskMixRows[i]["count"].(int)
		right, _ := taskMixRows[j]["count"].(int)
		if left == right {
			return taskMixRows[i]["task_type"].(string) < taskMixRows[j]["task_type"].(string)
		}
		return left > right
	})

	runningTasks := []iris.Map{}
	for _, row := range history {
		if row.status != "running" {
			continue
		}
		var currentSeconds any = historyElapsed(row.started)
		runningTasks = append(runningTasks, iris.Map{"worker_name": row.worker, "project_id": row.projectID, "project_name": row.projectName, "task_type": row.taskType, "current_task": row.detail, "step_id": nullableString(row.stepID), "intent_id": nullableString(row.stepID), "running_seconds": currentSeconds})
	}
	if len(runningTasks) == 0 && worker["running"].(bool) {
		runningTasks = append(runningTasks, iris.Map{"worker_name": workerName, "project_id": worker["project_id"], "project_name": projectNames[worker["project_id"].(string)], "task_type": worker["task_type"], "current_task": worker["current_task"], "step_id": worker["step_id"], "intent_id": worker["step_id"], "running_seconds": nil})
	}
	rejections := []iris.Map{}
	retryRows, err := h.DB.SQL.QueryContext(requestCtx, `SELECT project_id,task_type,retry_after FROM worker_retry_state WHERE last_outcome='rejected' AND julianday(retry_after)>julianday(?)`, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		bad(ctx, err)
		return
	}
	for retryRows.Next() {
		var projectID, taskType, retryAfter string
		if err := retryRows.Scan(&projectID, &taskType, &retryAfter); err != nil {
			retryRows.Close()
			bad(ctx, err)
			return
		}
		seconds := 0.0
		if at, err := time.Parse(time.RFC3339Nano, retryAfter); err == nil {
			seconds = time.Until(at).Seconds()
			if seconds < 0 {
				seconds = 0
			}
		}
		rejections = append(rejections, iris.Map{"worker_name": workerName, "project_id": projectID, "project_name": projectNames[projectID], "task_type": taskType, "rejected": true, "seconds_remaining": seconds})
	}
	if err := retryRows.Err(); err != nil {
		retryRows.Close()
		bad(ctx, err)
		return
	}
	retryRows.Close()
	ctx.JSON(iris.Map{
		"worker_mode": "dispatcher_pool",
		"summary": iris.Map{
			"total": workerCount, "configured_workers": workerCount, "online": onlineWorkers,
			"busy": busyWorkers, "idle": idleWorkers,
			"offline": workerCount * boolCount(status == "offline"), "disabled": workerCount * boolCount(status == "disabled"),
			"running_tasks": running, "running_projects": running, "rejection_count": len(rejections),
			"max_workers": workerCount, "max_running_projects": workerCount, "max_project_workers": workerCount, "attention_steps": attention, "summary_pending_steps": pending,
		},
		"workers":  []iris.Map{worker},
		"outcomes": outcomes, "task_mix": taskMixRows, "running_tasks": runningTasks,
		"running_task_count": running, "recent_history": recent, "rejections": rejections,
	})
}

func historyOutcome(status string) string {
	switch status {
	case "completed", "submitted":
		return "success"
	case "rejected":
		return "rejected"
	case "released":
		return "released"
	case "running":
		return "running"
	default:
		return "failed"
	}
}

func historyDuration(started string, finished sql.NullString) any {
	if !finished.Valid {
		return nil
	}
	start, startErr := time.Parse(time.RFC3339Nano, started)
	finish, finishErr := time.Parse(time.RFC3339Nano, finished.String)
	if startErr != nil || finishErr != nil || finish.Before(start) {
		return nil
	}
	return finish.Sub(start).Seconds()
}

func historyElapsed(started string) any {
	start, err := time.Parse(time.RFC3339Nano, started)
	if err != nil {
		return nil
	}
	seconds := time.Since(start).Seconds()
	if seconds < 0 {
		return float64(0)
	}
	return seconds
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (h *Handler) workerSnapshot(ctx context.Context) (iris.Map, error) {
	var name, driver, command, environment string
	var enabled int
	var workerCount int
	if err := h.DB.SQL.QueryRowContext(ctx, `SELECT name,driver,command,environment_json,enabled,worker_count FROM worker_config WHERE id=1`).Scan(&name, &driver, &command, &environment, &enabled, &workerCount); err != nil {
		return nil, err
	}
	if strings.TrimSpace(name) == "" {
		name = "local"
	}
	var completed, failed int
	var average sql.NullFloat64
	if err := h.DB.SQL.QueryRowContext(ctx, `SELECT count(*),AVG((julianday(finished_at)-julianday(started_at))*86400) FROM worker_task_history WHERE (worker LIKE ? OR worker=?) AND status IN ('completed','submitted') AND finished_at IS NOT NULL`, name+"/%", name).Scan(&completed, &average); err != nil {
		return nil, err
	}
	if err := h.DB.SQL.QueryRowContext(ctx, `SELECT count(*) FROM worker_task_history WHERE (worker LIKE ? OR worker=?) AND status IN ('failed','rejected','claim_failed','invalid_payload','commit_pending','summary_pending')`, name+"/%", name).Scan(&failed); err != nil {
		return nil, err
	}
	var task, projectID, taskType string
	var stepID sql.NullString
	var running int
	err := h.DB.SQL.QueryRowContext(ctx, `
		SELECT project_id,task_type,step_id,current_task FROM (
			SELECT p.id AS project_id,'execute' AS task_type,s.id AS step_id,s.description AS current_task,s.last_heartbeat_at AS heartbeat,st.step_timeout AS timeout
			FROM steps s JOIN projects p ON p.id=s.project_id CROSS JOIN settings st
			WHERE p.status='active' AND s.to_fact_id IS NULL AND s.abandoned=0 AND (s.worker LIKE ? OR s.worker=?) AND s.last_heartbeat_at IS NOT NULL
			UNION ALL
			SELECT p.id,'decide',NULL,'Decide: '||p.title,p.decide_last_heartbeat_at,st.decide_timeout
			FROM projects p CROSS JOIN settings st
			WHERE p.status='active' AND (p.decide_worker LIKE ? OR p.decide_worker=?) AND p.decide_last_heartbeat_at IS NOT NULL
		) WHERE (julianday(?) - julianday(heartbeat))*86400 <= timeout
		ORDER BY julianday(heartbeat) DESC LIMIT 1`, name+"/%", name, name+"/%", name, time.Now().UTC().Format(time.RFC3339Nano)).Scan(&projectID, &taskType, &stepID, &task)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if err == nil {
		running = 1
	}
	var heartbeat sql.NullFloat64
	if err := h.DB.SQL.QueryRowContext(ctx, `
		SELECT MIN(seconds_ago) FROM (
			SELECT (julianday(?) - julianday(last_heartbeat_at))*86400 AS seconds_ago FROM steps WHERE (worker LIKE ? OR worker=?) AND last_heartbeat_at IS NOT NULL
			UNION ALL
			SELECT (julianday(?) - julianday(decide_last_heartbeat_at))*86400 FROM projects WHERE (decide_worker LIKE ? OR decide_worker=?) AND decide_last_heartbeat_at IS NOT NULL
		)`, time.Now().UTC().Format(time.RFC3339Nano), name+"/%", name, time.Now().UTC().Format(time.RFC3339Nano), name+"/%", name).Scan(&heartbeat); err != nil {
		return nil, err
	}
	status := "idle"
	ready := localWorkerReady(enabled != 0, driver, command, environment)
	switch {
	case enabled == 0:
		status = "disabled"
	case !ready:
		status = "offline"
	case running > 0:
		status = "busy"
	}
	var avg any
	if average.Valid {
		avg = float64(int(average.Float64*10+0.5)) / 10
	}
	var lastHeartbeat any
	if heartbeat.Valid {
		lastHeartbeat = heartbeat.Float64
		if heartbeat.Float64 < 0 {
			lastHeartbeat = float64(0)
		}
	}
	var currentTask any
	if running > 0 {
		currentTask = task
		if runes := []rune(task); len(runes) > 120 {
			currentTask = string(runes[:120])
		}
	}
	return iris.Map{
		"name": name, "type": driver, "driver": driver, "enabled": enabled != 0, "status": status,
		"running": running > 0, "worker_mode": "dispatcher_pool", "worker_count": workerCount, "current_task": currentTask,
		"project_id": nullableAny(projectID, running > 0), "step_id": nullableAny(stepID.String, running > 0 && stepID.Valid), "task_type": nullableAny(taskType, running > 0),
		"tasks_completed": completed, "completed_tasks": completed, "failed_tasks": failed,
		"avg_duration_seconds": avg, "last_heartbeat_seconds_ago": lastHeartbeat,
	}, nil
}

func nullableAny(value string, valid bool) any {
	if !valid || value == "" {
		return nil
	}
	return value
}

func localWorkerReady(enabled bool, driver, command, environment string) bool {
	if !enabled || !supportedDriver(driver) {
		return false
	}
	if strings.TrimSpace(command) != "" {
		_, err := exec.LookPath("sh")
		return err == nil
	}
	if driver == "mock" {
		return true
	}
	binary := workerBinaryFor(driver, parseJSON(environment))
	if binary == "" {
		return false
	}
	_, err := exec.LookPath(binary)
	return err == nil
}
func (h *Handler) WorkerTest(ctx iris.Context) {
	var driver, command, environment string
	var enabled int
	if err := h.DB.SQL.QueryRowContext(ctx.Request().Context(), `SELECT driver,command,environment_json,enabled FROM worker_config WHERE id=1`).Scan(&driver, &command, &environment, &enabled); err != nil {
		bad(ctx, err)
		return
	}
	if enabled == 0 {
		ctx.StatusCode(iris.StatusBadRequest)
		ctx.JSON(iris.Map{"ok": false, "status": "disabled", "detail": "local worker is disabled"})
		return
	}
	if !supportedDriver(driver) {
		ctx.StatusCode(iris.StatusBadRequest)
		ctx.JSON(iris.Map{"ok": false, "status": "invalid", "detail": "unsupported local worker driver"})
		return
	}
	command = strings.TrimSpace(command)
	if command != "" {
		testCtx, cancel := context.WithTimeout(ctx.Request().Context(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(testCtx, "sh", "-c", command)
		cmd.Env = os.Environ()
		cmd.Env = append(cmd.Env, "HOTMOUSE_TASK_TYPE=healthcheck")
		for k, v := range parseJSON(environment) {
			if value, ok := v.(string); ok {
				cmd.Env = append(cmd.Env, k+"="+value)
			}
		}
		if output, err := cmd.CombinedOutput(); err != nil {
			ctx.StatusCode(iris.StatusBadGateway)
			ctx.JSON(iris.Map{"ok": false, "status": "error", "detail": strings.TrimSpace(string(output))})
			return
		}
		ctx.JSON(iris.Map{"ok": true, "status": "ok", "detail": "local worker command is available"})
		return
	}
	if driver != "mock" {
		binary := workerBinaryFor(driver, parseJSON(environment))
		if binary == "" {
			ctx.StatusCode(iris.StatusBadRequest)
			ctx.JSON(iris.Map{"ok": false, "status": "invalid", "detail": "worker command is empty"})
			return
		}
		if _, err := exec.LookPath(binary); err != nil {
			ctx.StatusCode(iris.StatusBadGateway)
			ctx.JSON(iris.Map{"ok": false, "status": "error", "detail": binary + " is not available on PATH"})
			return
		}
		testCtx, cancel := context.WithTimeout(ctx.Request().Context(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(testCtx, binary, "--version")
		cmd.Env = os.Environ()
		for k, v := range parseJSON(environment) {
			if value, ok := v.(string); ok {
				cmd.Env = append(cmd.Env, k+"="+value)
			}
		}
		if output, err := cmd.CombinedOutput(); err != nil {
			ctx.StatusCode(iris.StatusBadGateway)
			ctx.JSON(iris.Map{"ok": false, "status": "error", "detail": strings.TrimSpace(string(output))})
			return
		}
		ctx.JSON(iris.Map{"ok": true, "status": "ok", "detail": binary + " is available", "binary": binary})
		return
	}
	ctx.JSON(iris.Map{"ok": true, "status": "ok", "detail": "local worker configuration accepted"})
}
func supportedDriver(driver string) bool {
	switch driver {
	case "mock", "command", "shell", "pi", "codex", "claude", "claudecode", "claude_code":
		return true
	default:
		return false
	}
}
func workerBinary(driver string) string {
	switch driver {
	case "codex":
		return "codex"
	case "claude", "claudecode", "claude_code":
		return "claude"
	case "pi":
		return "pi"
	default:
		return ""
	}
}

func workerBinaryFor(driver string, environment map[string]any) string {
	if driver == "pi" {
		if configured, ok := environment["PI_CLI"].(string); ok && strings.TrimSpace(configured) != "" {
			return strings.TrimSpace(configured)
		}
		if configured := strings.TrimSpace(os.Getenv("PI_CLI")); configured != "" {
			return configured
		}
	}
	return workerBinary(driver)
}
func (h *Handler) WorkerHistory(ctx iris.Context) {
	query := `SELECT w.id,w.worker,w.project_id,COALESCE(p.title,''),w.step_id,w.task_type,w.status,w.started_at,w.finished_at,w.detail FROM worker_task_history w LEFT JOIN projects p ON p.id=w.project_id`
	args := []any{}
	if name := ctx.Params().Get("worker_name"); name != "" {
		query += ` WHERE w.worker=?`
		args = append(args, name)
	}
	query += ` ORDER BY w.id DESC LIMIT 20`
	rows, e := h.DB.SQL.QueryContext(ctx.Request().Context(), query, args...)
	if e != nil {
		bad(ctx, e)
		return
	}
	defer rows.Close()
	out := []iris.Map{}
	for rows.Next() {
		var id int64
		var worker, task, status, started, detail, projectName string
		var project, step, finished sql.NullString
		if e = rows.Scan(&id, &worker, &project, &projectName, &step, &task, &status, &started, &finished, &detail); e != nil {
			bad(ctx, e)
			return
		}
		outcome := status
		if status == "completed" {
			outcome = "success"
		}
		if status == "invalid_payload" || status == "claim_failed" || status == "disabled" {
			outcome = "failed"
		}
		var duration any
		if finished.Valid {
			start, _ := time.Parse(time.RFC3339, started)
			end, _ := time.Parse(time.RFC3339, finished.String)
			if !start.IsZero() && !end.IsZero() {
				duration = end.Sub(start).Seconds()
			}
		}
		out = append(out, iris.Map{"id": id, "worker": worker, "worker_name": worker, "project_id": nullableString(project), "project_name": projectName, "step_id": nullableString(step), "task_type": task, "status": status, "outcome": outcome, "description": detail, "duration_seconds": duration, "started_at": started, "finished_at": nullableString(finished), "detail": detail})
	}
	ctx.JSON(out)
}
func (h *Handler) ExecuteStep(ctx iris.Context) {
	runner := worker.New(h.DB)
	runner.SetLogOutput(os.Stdout)
	result, e := runner.ExecuteNow(ctx.Request().Context(), ctx.Params().Get("project_id"), ctx.Params().Get("step_id"))
	if e != nil {
		bad(ctx, e)
		return
	}
	ctx.JSON(result)
}
func (h *Handler) ListActivity(ctx iris.Context) {
	limit := ctx.URLParamIntDefault("limit", 100)
	if limit < 1 {
		limit = 1
	}
	if limit > 500 {
		limit = 500
	}
	rows, e := h.DB.SQL.QueryContext(ctx.Request().Context(), `SELECT a.id,CASE WHEN a.user_id IS NOT NULL THEN COALESCE(u.username,a.actor) ELSE a.actor END,a.action,COALESCE(a.target_type,'api'),COALESCE(a.target_id,a.resource),COALESCE(NULLIF(a.summary,''),a.action),COALESCE(a.detail,a.detail_json),a.created_at FROM audit_log a LEFT JOIN users u ON u.id=a.user_id ORDER BY datetime(a.created_at) DESC,a.id DESC LIMIT ?`, limit)
	if e != nil {
		bad(ctx, e)
		return
	}
	defer rows.Close()
	out := []iris.Map{}
	for rows.Next() {
		var id int
		var actor, action, targetType, targetID, summary, detail, created sql.NullString
		if err := rows.Scan(&id, &actor, &action, &targetType, &targetID, &summary, &detail, &created); err != nil {
			bad(ctx, err)
			return
		}
		out = append(out, iris.Map{"id": id, "actor": actor.String, "action": action.String, "target_type": targetType.String, "target_id": targetID.String, "summary": summary.String, "detail": nullableAnyString(detail), "created_at": created.String})
	}
	if err := rows.Err(); err != nil {
		bad(ctx, err)
		return
	}
	ctx.JSON(out)
}
func (h *Handler) ListNotifications(ctx iris.Context) {
	limit := ctx.URLParamIntDefault("limit", 50)
	if limit < 1 {
		limit = 1
	}
	if limit > 200 {
		limit = 200
	}
	rows, e := h.DB.SQL.QueryContext(ctx.Request().Context(), `SELECT id,COALESCE(NULLIF(level,''),kind,'info'),title,body,link,read,created_at FROM notifications ORDER BY datetime(created_at) DESC,id DESC LIMIT ?`, limit)
	if e != nil {
		bad(ctx, e)
		return
	}
	defer rows.Close()
	out := []iris.Map{}
	for rows.Next() {
		var id int
		var kind, title, body, link, created sql.NullString
		var read int
		if err := rows.Scan(&id, &kind, &title, &body, &link, &read, &created); err != nil {
			bad(ctx, err)
			return
		}
		out = append(out, iris.Map{"id": id, "level": kind.String, "title": title.String, "body": nullableAnyString(body), "link": nullableAnyString(link), "read": read != 0, "created_at": created.String})
	}
	if err := rows.Err(); err != nil {
		bad(ctx, err)
		return
	}
	ctx.JSON(out)
}

func nullableAnyString(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}
func (h *Handler) NotificationUnread(ctx iris.Context) {
	var n int
	_ = h.DB.SQL.QueryRow(`SELECT count(*) FROM notifications WHERE read=0`).Scan(&n)
	ctx.JSON(iris.Map{"count": n})
}
func (h *Handler) NotificationRead(ctx iris.Context) {
	var body struct {
		IDs []int64 `json:"ids"`
	}
	_ = ctx.ReadJSON(&body)
	var e error
	if len(body.IDs) == 0 {
		_, e = h.DB.SQL.ExecContext(ctx.Request().Context(), `UPDATE notifications SET read=1,read_at=? WHERE read=0`, nowString())
	} else {
		args := make([]any, 0, len(body.IDs)+1)
		args = append(args, nowString())
		marks := make([]string, len(body.IDs))
		for i, id := range body.IDs {
			marks[i] = "?"
			args = append(args, id)
		}
		_, e = h.DB.SQL.ExecContext(ctx.Request().Context(), `UPDATE notifications SET read=1,read_at=? WHERE id IN (`+strings.Join(marks, ",")+")", args...)
	}
	if e != nil {
		bad(ctx, e)
		return
	}
	h.NotificationUnread(ctx)
}
func (h *Handler) NotificationClear(ctx iris.Context) {
	_, e := h.DB.SQL.ExecContext(ctx.Request().Context(), `DELETE FROM notifications`)
	if e != nil {
		bad(ctx, e)
		return
	}
	ctx.JSON(iris.Map{"status": "cleared"})
}
func (h *Handler) ListTemplates(ctx iris.Context) {
	owner := h.templateOwner(ctx)
	rows, e := h.DB.SQL.QueryContext(ctx.Request().Context(), `SELECT id,title,origin,goal,hints_json,created_at FROM templates WHERE user_id=? ORDER BY created_at,id`, owner)
	if e != nil {
		bad(ctx, e)
		return
	}
	defer rows.Close()
	out := builtinTemplates()
	for rows.Next() {
		var id, title, origin, goal, hints, created string
		if e := rows.Scan(&id, &title, &origin, &goal, &hints, &created); e != nil {
			bad(ctx, e)
			return
		}
		out = append(out, iris.Map{"id": id, "title": title, "origin": origin, "goal": goal, "hints": parseList(hints), "is_builtin": false, "created_at": created, "user_id": owner})
	}
	if e := rows.Err(); e != nil {
		bad(ctx, e)
		return
	}
	ctx.JSON(out)
}

func templateValidation(ctx iris.Context, detail string) {
	ctx.StopWithJSON(iris.StatusUnprocessableEntity, iris.Map{"detail": detail})
}

func builtinTemplates() []iris.Map {
	hint := func(content string) iris.Map { return iris.Map{"content": content, "creator": "template"} }
	return []iris.Map{
		{"id": "builtin-web-app", "title": "Web 应用评估", "origin": "目标 Web 应用，地址为 [URL]，技术栈为 [technology stack]", "goal": "识别并记录 Web 应用中所有可利用的漏洞", "hints": []iris.Map{hint("从侦察开始：枚举子域名、目录和所用技术"), hint("测试身份认证机制是否存在绕过漏洞"), hint("检查所有用户输入是否存在注入点")}, "is_builtin": true, "user_id": nil},
		{"id": "builtin-internal-network", "title": "内网渗透测试", "origin": "可从假定已突破的立足点访问的内网网段 [CIDR]", "goal": "实现域控制权获取，并记录横向移动和权限提升路径", "hints": []iris.Map{hint("枚举内网网段中的存活主机和服务"), hint("识别 Active Directory 资产：域控制器、用户和用户组"), hint("在共享、脚本和服务配置中搜寻凭据")}, "is_builtin": true, "user_id": nil},
		{"id": "builtin-external-network", "title": "外网渗透测试", "origin": "[organization] 面向互联网的资产：在范围内的 IP 段和域名", "goal": "发现并利用对外暴露的服务，以获得初始立足点", "hints": []iris.Map{hint("枚举外部攻击面：开放端口、服务和暴露的应用"), hint("识别服务版本指纹，并检查是否存在已知可利用的漏洞"), hint("测试暴露的登录入口是否使用弱口令或默认凭据")}, "is_builtin": true, "user_id": nil},
		{"id": "builtin-ctf", "title": "CTF 挑战", "origin": "CTF 挑战目标，地址为 [host:port]，类别为 [web/pwn/crypto/forensics]", "goal": "解出挑战并取得 flag", "hints": []iris.Map{hint("确定挑战类别，并收集所有提供的文件或接口"), hint("针对该类别探测预期的漏洞类型"), hint("开发漏洞利用或解题方案并夺取 flag")}, "is_builtin": true, "user_id": nil},
	}
}
func (h *Handler) CreateTemplate(ctx iris.Context) {
	var r struct {
		Name, Title, Description, Origin, Goal string
		Hints                                  []map[string]string `json:"hints"`
	}
	if !read(ctx, &r) {
		return
	}
	title := r.Title
	if title == "" {
		title = r.Name
	}
	if title == "" {
		templateValidation(ctx, "title must not be empty")
		return
	}
	if len(title) == 0 || len(title) > 200 || len(r.Origin) == 0 || len(r.Origin) > 200 || len(r.Goal) == 0 || len(r.Goal) > 200 {
		templateValidation(ctx, "title, origin, and goal must be 1 to 200 characters")
		return
	}
	if len(r.Hints) > 10 {
		templateValidation(ctx, "hints must contain at most 10 items")
		return
	}
	if r.Hints == nil {
		r.Hints = []map[string]string{}
	}
	for _, hint := range r.Hints {
		if strings.TrimSpace(hint["content"]) == "" {
			templateValidation(ctx, "template hint content must not be empty")
			return
		}
		if hint["creator"] == "" {
			hint["creator"] = "template"
		}
	}
	owner := h.templateOwner(ctx)
	var count int
	_ = h.DB.SQL.QueryRowContext(ctx.Request().Context(), `SELECT count(*) FROM templates WHERE user_id=?`, owner).Scan(&count)
	if count >= 50 {
		bad(ctx, fmt.Errorf("template limit reached (50)"))
		return
	}
	id := templateID()
	_, e := h.DB.SQL.ExecContext(ctx.Request().Context(), `INSERT INTO templates(id,user_id,title,origin,goal,hints_json,created_at) VALUES(?,?,?,?,?,?,?)`, id, owner, title, r.Origin, r.Goal, coreJSONList(r.Hints), nowString())
	if e != nil {
		bad(ctx, e)
		return
	}
	ctx.StatusCode(iris.StatusCreated)
	ctx.JSON(iris.Map{"id": id, "title": title, "origin": r.Origin, "goal": r.Goal, "hints": r.Hints, "is_builtin": false, "user_id": owner})
}
func (h *Handler) templateOwner(ctx iris.Context) string {
	tok := ctx.GetCookie("session_token")
	if tok == "" {
		tok = ctx.GetCookie("hotmouse_session")
	}
	if u, e := h.DB.Session(ctx.Request().Context(), tok); e == nil {
		return u.ID
	}
	return "anonymous"
}
func coreJSONList(v any) string { b, _ := json.Marshal(v); return string(b) }
func templateID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "tmpl_" + fmt.Sprintf("%x", b)
}
func parseList(raw string) []map[string]string {
	var v []map[string]string
	_ = json.Unmarshal([]byte(raw), &v)
	if v == nil {
		return []map[string]string{}
	}
	return v
}
func (h *Handler) DeleteTemplate(ctx iris.Context) {
	id := ctx.Params().Get("template_id")
	owner := h.templateOwner(ctx)
	var current string
	if e := h.DB.SQL.QueryRowContext(ctx.Request().Context(), `SELECT user_id FROM templates WHERE id=?`, id).Scan(&current); e != nil {
		bad(ctx, core.ErrNotFound)
		return
	}
	if current != owner {
		ctx.StopWithJSON(iris.StatusForbidden, iris.Map{"detail": "template is owned by another user"})
		return
	}
	_, e := h.DB.SQL.ExecContext(ctx.Request().Context(), `DELETE FROM templates WHERE id=? AND user_id=?`, id, owner)
	if e != nil {
		bad(ctx, e)
		return
	}
	ctx.StatusCode(iris.StatusNoContent)
}

func (h *Handler) ListProjects(ctx iris.Context) {
	v, e := h.DB.ListProjects(ctx.Request().Context())
	if e != nil {
		bad(ctx, e)
		return
	}
	ctx.JSON(v)
}
func (h *Handler) CreateProject(ctx iris.Context) {
	var r core.CreateProject
	if !read(ctx, &r) {
		return
	}
	v, e := h.DB.CreateProject(ctx.Request().Context(), r)
	if e != nil {
		bad(ctx, e)
		return
	}
	ctx.StatusCode(iris.StatusCreated)
	ctx.JSON(v)
}
func (h *Handler) GetProject(ctx iris.Context) {
	v, e := h.DB.GetProject(ctx.Request().Context(), ctx.Params().Get("project_id"))
	if e != nil {
		bad(ctx, e)
		return
	}
	ctx.JSON(v)
}
func (h *Handler) DeleteProject(ctx iris.Context) {
	id := ctx.Params().Get("project_id")
	_, e := h.DB.GetProjectMeta(ctx.Request().Context(), id)
	if e != nil {
		bad(ctx, e)
		return
	}
	_, e = h.DB.SQL.ExecContext(ctx.Request().Context(), `DELETE FROM projects WHERE id=?`, id)
	if e != nil {
		bad(ctx, e)
		return
	}
	ctx.StatusCode(iris.StatusNoContent)
}
func (h *Handler) UpdateTitle(ctx iris.Context) {
	var r struct {
		Title string `json:"title"`
	}
	if !read(ctx, &r) {
		return
	}
	if strings.TrimSpace(r.Title) == "" {
		bad(ctx, fmt.Errorf("title must not be empty"))
		return
	}
	res, e := h.DB.SQL.ExecContext(ctx.Request().Context(), `UPDATE projects SET title=? WHERE id=?`, strings.TrimSpace(r.Title), ctx.Params().Get("project_id"))
	if e != nil {
		bad(ctx, e)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		bad(ctx, core.ErrNotFound)
		return
	}
	v, e := h.DB.GetProjectMeta(ctx.Request().Context(), ctx.Params().Get("project_id"))
	if e != nil {
		bad(ctx, e)
		return
	}
	ctx.JSON(v)
}
func (h *Handler) UpdateProjectWorkerLimit(ctx iris.Context) {
	var request struct {
		MaxWorkers int `json:"max_workers"`
	}
	if !read(ctx, &request) {
		return
	}
	project, err := h.DB.SetProjectMaxWorkers(ctx.Request().Context(), ctx.Params().Get("project_id"), request.MaxWorkers)
	if err != nil {
		bad(ctx, err)
		return
	}
	ctx.JSON(project)
}
func (h *Handler) UpdateStatus(ctx iris.Context) {
	var r struct {
		Status string `json:"status"`
	}
	if !read(ctx, &r) {
		return
	}
	v, e := h.DB.SetProjectStatus(ctx.Request().Context(), ctx.Params().Get("project_id"), r.Status)
	if e != nil {
		bad(ctx, e)
		return
	}
	ctx.JSON(v)
}

func (h *Handler) CreateGoal(ctx iris.Context) {
	var r core.CreateGoal
	if !read(ctx, &r) {
		return
	}
	v, e := h.DB.CreateGoal(ctx.Request().Context(), ctx.Params().Get("project_id"), r)
	if e != nil {
		bad(ctx, e)
		return
	}
	ctx.StatusCode(iris.StatusCreated)
	ctx.JSON(v)
}
func (h *Handler) UpdateGoal(ctx iris.Context) {
	var r core.UpdateGoal
	if !read(ctx, &r) {
		return
	}
	if r.Status != nil {
		ctx.StopWithJSON(iris.StatusUnprocessableEntity, iris.Map{"detail": "status cannot be changed through goal patch"})
		return
	}
	p, g := ctx.Params().Get("project_id"), ctx.Params().Get("goal_id")
	v, e := h.DB.UpdateGoal(ctx.Request().Context(), p, g, r)
	if e != nil {
		bad(ctx, e)
		return
	}
	ctx.JSON(v)
}
func (h *Handler) DeleteGoal(ctx iris.Context) {
	p, g := ctx.Params().Get("project_id"), ctx.Params().Get("goal_id")
	if e := h.DB.DeleteGoal(ctx.Request().Context(), p, g); e != nil {
		bad(ctx, e)
		return
	}
	ctx.StatusCode(iris.StatusNoContent)
}
func (h *Handler) CompleteGoal(ctx iris.Context) {
	var r core.Completion
	if !read(ctx, &r) {
		return
	}
	v, e := h.DB.CompleteGoal(ctx.Request().Context(), ctx.Params().Get("project_id"), ctx.Params().Get("goal_id"), r)
	if e != nil {
		bad(ctx, e)
		return
	}
	ctx.JSON(v)
}
func (h *Handler) CreateStep(ctx iris.Context) {
	var r core.CreateStep
	if !read(ctx, &r) {
		return
	}
	v, e := h.DB.CreateStep(ctx.Request().Context(), ctx.Params().Get("project_id"), r)
	if e != nil {
		bad(ctx, e)
		return
	}
	ctx.StatusCode(iris.StatusCreated)
	ctx.JSON(v)
}
func (h *Handler) HeartbeatStep(ctx iris.Context) {
	var r core.Heartbeat
	if !read(ctx, &r) {
		return
	}
	v, e := h.DB.HeartbeatStep(ctx.Request().Context(), ctx.Params().Get("project_id"), ctx.Params().Get("step_id"), r.Worker)
	if e != nil {
		bad(ctx, e)
		return
	}
	ctx.JSON(v)
}
func (h *Handler) ReleaseStep(ctx iris.Context) {
	var r core.Heartbeat
	if !read(ctx, &r) {
		return
	}
	v, e := h.DB.ReleaseStep(ctx.Request().Context(), ctx.Params().Get("project_id"), ctx.Params().Get("step_id"), r.Worker)
	if e != nil {
		bad(ctx, e)
		return
	}
	ctx.JSON(v)
}
func (h *Handler) ConcludeStep(ctx iris.Context) {
	var r core.Conclude
	if !read(ctx, &r) {
		return
	}
	v, e := h.DB.ConcludeStep(ctx.Request().Context(), ctx.Params().Get("project_id"), ctx.Params().Get("step_id"), r)
	if e != nil {
		bad(ctx, e)
		return
	}
	ctx.JSON(v)
}
func (h *Handler) CreateIntent(ctx iris.Context)    { h.CreateStep(ctx) }
func (h *Handler) HeartbeatIntent(ctx iris.Context) { h.HeartbeatStep(ctx) }
func (h *Handler) ReleaseIntent(ctx iris.Context)   { h.ReleaseStep(ctx) }
func (h *Handler) ConcludeIntent(ctx iris.Context)  { h.ConcludeStep(ctx) }
func (h *Handler) PatchStep(ctx iris.Context) {
	var r core.UpdateStep
	if !read(ctx, &r) {
		return
	}
	if r.Status != nil {
		ctx.StopWithJSON(iris.StatusUnprocessableEntity, iris.Map{"detail": "status cannot be changed through step patch"})
		return
	}
	id, p := ctx.Params().Get("step_id"), ctx.Params().Get("project_id")
	vStep, e := h.DB.UpdateStep(ctx.Request().Context(), p, id, r)
	if e != nil {
		bad(ctx, e)
		return
	}
	ctx.JSON(vStep)
}
func (h *Handler) AddHint(ctx iris.Context) {
	var r core.CreateHint
	if !read(ctx, &r) {
		return
	}
	v, e := h.DB.AddHint(ctx.Request().Context(), ctx.Params().Get("project_id"), r)
	if e != nil {
		bad(ctx, e)
		return
	}
	ctx.StatusCode(iris.StatusCreated)
	ctx.JSON(v)
}
func (h *Handler) CreateFinding(ctx iris.Context) {
	var r core.CreateFinding
	if !read(ctx, &r) {
		return
	}
	f, e := h.DB.CreateFinding(ctx.Request().Context(), ctx.Params().Get("project_id"), r)
	if e != nil {
		bad(ctx, e)
		return
	}
	ctx.StatusCode(iris.StatusCreated)
	ctx.JSON(f)
}
func (h *Handler) ListFindings(ctx iris.Context) {
	p := ctx.Params().Get("project_id")
	rows, e := h.DB.SQL.QueryContext(ctx.Request().Context(), `SELECT id,title,description,severity,kind,data_json,fact_id,created_at FROM findings WHERE project_id=? ORDER BY created_at`, p)
	if e != nil {
		bad(ctx, e)
		return
	}
	defer rows.Close()
	out := []core.Finding{}
	for rows.Next() {
		var f core.Finding
		var data, fid sql.NullString
		if e = rows.Scan(&f.ID, &f.Title, &f.Description, &f.Severity, &f.Kind, &data, &fid, &f.CreatedAt); e != nil {
			bad(ctx, e)
			return
		}
		f.Data = parseJSON(data.String)
		f.FactID = nullableString(fid)
		out = append(out, f)
	}
	ctx.JSON(out)
}

type vulnerability struct {
	ID                      string   `json:"id"`
	ProjectID               string   `json:"project_id"`
	ProjectName             string   `json:"project_name"`
	FactID                  string   `json:"fact_id"`
	Title                   string   `json:"title"`
	Description             string   `json:"description"`
	Severity                string   `json:"severity"`
	Status                  string   `json:"status"`
	DiscoveredAt            string   `json:"discovered_at"`
	FindingID               string   `json:"finding_id"`
	SourceStepID            *string  `json:"source_step_id"`
	SourceStepDescription   *string  `json:"source_step_description"`
	SourceIntentID          *string  `json:"source_intent_id"`
	SourceIntentDescription *string  `json:"source_intent_description"`
	SourceWorker            *string  `json:"source_worker"`
	SourceFactIDs           []string `json:"source_fact_ids"`
	RelatedFactIDs          []string `json:"related_fact_ids"`
	Evidence                []string `json:"evidence"`
	Score                   int      `json:"-"`
	rawMembers              []vulnerability
	rawIDs                  []string
}

func (h *Handler) ListVulnerabilities(ctx iris.Context) {
	severity := strings.TrimSpace(strings.ToLower(ctx.URLParam("severity")))
	projectID := strings.TrimSpace(ctx.URLParam("project_id"))
	status := strings.TrimSpace(strings.ToLower(ctx.URLParam("status")))
	if severity != "" && !validVulnerabilitySeverity(severity) {
		ctx.StopWithJSON(iris.StatusUnprocessableEntity, iris.Map{"detail": "invalid vulnerability severity"})
		return
	}
	if status != "" && status != "confirmed" && status != "ignored" {
		ctx.StopWithJSON(iris.StatusUnprocessableEntity, iris.Map{"detail": "invalid vulnerability status"})
		return
	}
	if projectID != "" {
		var exists int
		if e := h.DB.SQL.QueryRowContext(ctx.Request().Context(), `SELECT count(*) FROM projects WHERE id=?`, projectID).Scan(&exists); e != nil {
			bad(ctx, e)
			return
		}
		if exists == 0 {
			bad(ctx, core.ErrNotFound)
			return
		}
	}
	where := []string{}
	args := []any{}
	if severity != "" {
		where = append(where, "v.severity=?")
		args = append(args, severity)
	}
	if projectID != "" {
		where = append(where, "v.project_id=?")
		args = append(args, projectID)
	}
	if status != "" {
		where = append(where, "COALESCE(v.status,'confirmed')=?")
		args = append(args, status)
	}
	filter := ""
	if len(where) > 0 {
		filter = " WHERE " + strings.Join(where, " AND ")
	}
	out, e := queryVulnerabilities(ctx.Request().Context(), h.DB, filter, args...)
	if e != nil {
		bad(ctx, e)
		return
	}
	merged, e := mergeVulnerabilities(ctx.Request().Context(), h.DB, out)
	if e != nil {
		bad(ctx, e)
		return
	}
	ctx.JSON(merged)
}
func validVulnerabilitySeverity(s string) bool {
	switch s {
	case "critical", "high", "medium", "low":
		return true
	default:
		return false
	}
}
func (h *Handler) VulnerabilitySummary(ctx iris.Context) {
	var c struct{ Critical, High, Medium, Low int }
	items, e := queryVulnerabilities(ctx.Request().Context(), h.DB, "")
	if e != nil {
		bad(ctx, e)
		return
	}
	merged, e := mergeVulnerabilities(ctx.Request().Context(), h.DB, items)
	if e != nil {
		bad(ctx, e)
		return
	}
	for _, item := range merged {
		switch item.Severity {
		case "critical":
			c.Critical++
		case "high":
			c.High++
		case "medium":
			c.Medium++
		case "low":
			c.Low++
		}
	}
	ctx.JSON(iris.Map{"critical": c.Critical, "high": c.High, "medium": c.Medium, "low": c.Low})
}
func (h *Handler) UpdateVulnerabilityStatus(ctx iris.Context) {
	var r struct {
		Status string `json:"status"`
	}
	if !read(ctx, &r) {
		return
	}
	if r.Status != "confirmed" && r.Status != "ignored" {
		ctx.StopWithJSON(iris.StatusUnprocessableEntity, iris.Map{"detail": "invalid vulnerability status"})
		return
	}
	id := ctx.Params().Get("vulnerability_id")
	items, e := queryVulnerabilities(ctx.Request().Context(), h.DB, "")
	if e != nil {
		bad(ctx, e)
		return
	}
	merged, e := mergeVulnerabilities(ctx.Request().Context(), h.DB, items)
	if e != nil {
		bad(ctx, e)
		return
	}
	var target *vulnerability
	for i := range merged {
		if merged[i].ID == id {
			target = &merged[i]
			break
		}
	}
	if target == nil {
		bad(ctx, core.ErrNotFound)
		return
	}
	if e = updateMergedVulnerabilityStatus(ctx.Request().Context(), h.DB, []vulnerability{*target}, r.Status); e != nil {
		bad(ctx, e)
		return
	}
	target.Status = r.Status
	_, _ = h.DB.SQL.ExecContext(ctx.Request().Context(), `INSERT INTO audit_log(action,resource,detail_json,created_at) VALUES('vulnerability.status',?,?,?)`, id, coreJSON(map[string]any{"status": r.Status}), nowString())
	level := "success"
	if r.Status == "ignored" {
		level = "warning"
	}
	_, _ = h.DB.SQL.ExecContext(ctx.Request().Context(), `INSERT INTO notifications(kind,title,body,created_at) VALUES(?,?,?,?)`, level, "Vulnerability status updated", target.ProjectName+" · "+target.FactID+" · "+r.Status)
	ctx.JSON(target)
}
func (h *Handler) BatchVulnerabilityStatus(ctx iris.Context) {
	var r struct {
		IDs    []string `json:"ids"`
		Status string   `json:"status"`
	}
	if !read(ctx, &r) {
		return
	}
	if r.Status != "confirmed" && r.Status != "ignored" {
		ctx.StopWithJSON(iris.StatusUnprocessableEntity, iris.Map{"detail": "invalid vulnerability status"})
		return
	}
	items, e := queryVulnerabilities(ctx.Request().Context(), h.DB, "")
	if e != nil {
		bad(ctx, e)
		return
	}
	merged, e := mergeVulnerabilities(ctx.Request().Context(), h.DB, items)
	if e != nil {
		bad(ctx, e)
		return
	}
	byID := make(map[string]vulnerability, len(merged))
	for _, item := range merged {
		byID[item.ID] = item
	}
	missing := []string{}
	updated := []string{}
	targets := []vulnerability{}
	for _, id := range r.IDs {
		if target, ok := byID[id]; !ok {
			missing = append(missing, id)
		} else {
			updated = append(updated, id)
			targets = append(targets, target)
		}
	}
	if len(updated) == 0 {
		bad(ctx, core.ErrNotFound)
		return
	}
	if e = updateMergedVulnerabilityStatus(ctx.Request().Context(), h.DB, targets, r.Status); e != nil {
		bad(ctx, e)
		return
	}
	_, _ = h.DB.SQL.ExecContext(ctx.Request().Context(), `INSERT INTO audit_log(action,resource,detail_json,created_at) VALUES('vulnerability.batch_status','vulnerabilities',?,?)`, coreJSON(map[string]any{"ids": r.IDs, "status": r.Status}), nowString())
	level := "success"
	if r.Status == "ignored" {
		level = "warning"
	}
	_, _ = h.DB.SQL.ExecContext(ctx.Request().Context(), `INSERT INTO notifications(kind,title,body,created_at) VALUES(?,?,?,?)`, level, "Vulnerability batch status updated", fmt.Sprintf("%d vulnerabilities are now %s", len(updated), r.Status))
	ctx.JSON(iris.Map{"count": len(updated), "status": r.Status, "ids": updated, "missing_ids": missing})
}
func (h *Handler) RefreshVulnerabilities(ctx iris.Context) {
	p := ctx.Params().Get("project_id")
	if p != "" {
		_, e := h.DB.RefreshVulnerabilities(ctx.Request().Context(), p)
		if e != nil {
			bad(ctx, e)
			return
		}
		_, _ = h.DB.SQL.ExecContext(ctx.Request().Context(), `INSERT INTO audit_log(action,resource,detail_json,created_at) VALUES('vulnerability.refresh',?,?,?)`, p, `{}`, nowString())
		_, _ = h.DB.SQL.ExecContext(ctx.Request().Context(), `INSERT INTO notifications(kind,title,body,created_at) VALUES(?,?,?,?)`, "info", "Vulnerabilities refreshed", "Existing Finding projections were refreshed")
		h.VulnerabilitySummary(ctx)
		return
	}
	rows, e := h.DB.SQL.QueryContext(ctx.Request().Context(), `SELECT id FROM projects`)
	if e != nil {
		bad(ctx, e)
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		_, err := h.DB.RefreshVulnerabilities(ctx.Request().Context(), id)
		if err != nil {
			bad(ctx, err)
			return
		}
	}
	_, _ = h.DB.SQL.ExecContext(ctx.Request().Context(), `INSERT INTO audit_log(action,resource,detail_json,created_at) VALUES('vulnerability.refresh','vulnerabilities',?,?)`, `{}`, nowString())
	_, _ = h.DB.SQL.ExecContext(ctx.Request().Context(), `INSERT INTO notifications(kind,title,body,created_at) VALUES(?,?,?,?)`, "info", "Vulnerabilities refreshed", "Existing Finding projections were refreshed")
	h.VulnerabilitySummary(ctx)
}
func (h *Handler) ClaimDecide(ctx iris.Context) {
	var r core.ClaimDecide
	if !read(ctx, &r) {
		return
	}
	v, e := h.DB.ClaimDecide(ctx.Request().Context(), ctx.Params().Get("project_id"), r)
	if e != nil {
		bad(ctx, e)
		return
	}
	ctx.JSON(v)
}
func (h *Handler) HeartbeatDecide(ctx iris.Context) {
	var r core.Heartbeat
	if !read(ctx, &r) {
		return
	}
	v, e := h.DB.HeartbeatDecide(ctx.Request().Context(), ctx.Params().Get("project_id"), r.Worker)
	if e != nil {
		bad(ctx, e)
		return
	}
	ctx.JSON(v)
}
func (h *Handler) ReleaseDecide(ctx iris.Context) {
	var r core.Heartbeat
	if !read(ctx, &r) {
		return
	}
	e := h.DB.ReleaseDecide(ctx.Request().Context(), ctx.Params().Get("project_id"), r.Worker)
	if e != nil {
		bad(ctx, e)
		return
	}
	v, e := h.DB.GetProjectMeta(ctx.Request().Context(), ctx.Params().Get("project_id"))
	if e != nil {
		bad(ctx, e)
		return
	}
	ctx.JSON(v)
}
func (h *Handler) CompleteProject(ctx iris.Context) {
	var r core.Completion
	if !read(ctx, &r) {
		return
	}
	goal, e := h.DB.CompleteProject(ctx.Request().Context(), ctx.Params().Get("project_id"), r)
	if e != nil {
		bad(ctx, e)
		return
	}
	ctx.JSON(goal)
}
func (h *Handler) ReopenProject(ctx iris.Context) {
	var r core.Reopen
	if !read(ctx, &r) {
		return
	}
	v, e := h.DB.Reopen(ctx.Request().Context(), ctx.Params().Get("project_id"), r)
	if e != nil {
		bad(ctx, e)
		return
	}
	ctx.JSON(v)
}
func (h *Handler) Export(ctx iris.Context) {
	format := ctx.URLParamDefault("format", "yaml")
	project := ctx.Params().Get("project_id")
	if format == "yaml" {
		v, e := h.DB.ExportYAML(ctx.Request().Context(), project)
		if e != nil {
			bad(ctx, e)
			return
		}
		ctx.ContentType("text/plain")
		ctx.WriteString(v)
		return
	}
	if format != "timeline" {
		bad(ctx, fmt.Errorf("Supported formats: yaml, timeline"))
		return
	}
	v, e := h.DB.ExportTimeline(ctx.Request().Context(), project)
	if e != nil {
		bad(ctx, e)
		return
	}
	ctx.ContentType("text/plain")
	ctx.WriteString(v)
}

func parseStringList(raw string) []string {
	var v []string
	_ = json.Unmarshal([]byte(raw), &v)
	if v == nil {
		return []string{}
	}
	return v
}
func jsonList(v []string) string { b, _ := json.Marshal(v); return string(b) }

func coreJSON(v map[string]any) string {
	if v == nil {
		return "{}"
	}
	b, _ := json.Marshal(v)
	return string(b)
}
func parseJSON(s string) map[string]any {
	var v map[string]any
	_ = json.Unmarshal([]byte(s), &v)
	if v == nil {
		v = map[string]any{}
	}
	return v
}
func nullableString(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}
func nowString() string { return time.Now().UTC().Format("2006-01-02T15:04:05Z") }

func Register(app *iris.Application, h *Handler) {
	app.UseRouter(func(ctx iris.Context) {
		if !h.auth(ctx) {
			return
		}
		ctx.Next()
		if ctx.Method() != "GET" && ctx.Path() != "/healthz" {
			user := ctx.Values().Get("user_id")
			_, _ = h.DB.SQL.Exec(`INSERT INTO audit_log(user_id,action,resource,detail_json,created_at) VALUES(?,?,?,?,?)`, user, ctx.Method(), ctx.Path(), `{}`, nowString())
		}
	})
	app.Get("/", h.Health)
	app.Get("/healthz", h.Health)
	app.Post("/auth/register", h.Register)
	app.Post("/auth/login", h.Login)
	app.Post("/auth/logout", h.Logout)
	app.Get("/auth/me", h.Me)
	app.Get("/auth/captcha", h.Captcha)
	app.Post("/register", h.Register)
	app.Post("/login", h.Login)
	app.Post("/logout", h.Logout)
	app.Get("/me", h.Me)
	app.Post("/api/auth/register", h.Register)
	app.Post("/api/auth/login", h.Login)
	app.Post("/api/auth/logout", h.Logout)
	app.Get("/api/auth/me", h.Me)
	app.Get("/api/auth/captcha", h.Captcha)
	app.Get("/captcha", h.Captcha)
	app.Put("/api/auth/password", h.ChangePassword)
	app.Put("/auth/password", h.ChangePassword)
	h.registerStrategy(app)
	app.Get("/projects", h.ListProjects)
	app.Post("/projects", h.CreateProject)
	app.Get("/projects/{project_id:string}", h.GetProject)
	app.Delete("/projects/{project_id:string}", h.DeleteProject)
	app.Put("/projects/{project_id:string}/title", h.UpdateTitle)
	app.Put("/projects/{project_id:string}/worker-limit", h.UpdateProjectWorkerLimit)
	app.Put("/projects/{project_id:string}/status", h.UpdateStatus)
	app.Post("/projects/{project_id:string}/goals", h.CreateGoal)
	app.Post("/projects/{project_id:string}/goals/{goal_id:string}/complete", h.CompleteGoal)
	app.Patch("/projects/{project_id:string}/goals/{goal_id:string}", h.UpdateGoal)
	app.Delete("/projects/{project_id:string}/goals/{goal_id:string}", h.DeleteGoal)
	app.Post("/projects/{project_id:string}/steps", h.CreateStep)
	app.Patch("/projects/{project_id:string}/steps/{step_id:string}", h.PatchStep)
	app.Post("/projects/{project_id:string}/steps/{step_id:string}/heartbeat", h.HeartbeatStep)
	app.Post("/projects/{project_id:string}/steps/{step_id:string}/release", h.ReleaseStep)
	app.Post("/projects/{project_id:string}/steps/{step_id:string}/conclude", h.ConcludeStep)
	app.Post("/projects/{project_id:string}/intents", h.CreateIntent)
	app.Post("/projects/{project_id:string}/intents/{intent_id:string}/heartbeat", h.HeartbeatIntent)
	app.Post("/projects/{project_id:string}/intents/{intent_id:string}/release", h.ReleaseIntent)
	app.Post("/projects/{project_id:string}/intents/{intent_id:string}/conclude", h.ConcludeIntent)
	app.Post("/projects/{project_id:string}/hints", h.AddHint)
	app.Post("/projects/{project_id:string}/findings", h.CreateFinding)
	app.Get("/projects/{project_id:string}/findings", h.ListFindings)
	app.Get("/api/vulnerabilities", h.ListVulnerabilities)
	app.Get("/api/vulnerabilities/summary", h.VulnerabilitySummary)
	app.Patch("/api/vulnerabilities/{vulnerability_id:string}/status", h.UpdateVulnerabilityStatus)
	app.Post("/api/vulnerabilities/batch/status", h.BatchVulnerabilityStatus)
	app.Post("/api/projects/{project_id:string}/vulnerabilities/refresh", h.RefreshVulnerabilities)
	app.Post("/api/vulnerabilities/refresh", h.RefreshVulnerabilities)
	app.Get("/api/projects/{project_id:string}/timeline", h.Timeline)
	app.Get("/projects/{project_id:string}/timeline", h.Timeline)
	app.Get("/projects/{project_id:string}/export", h.Export)
	app.Get("/api/projects/{project_id:string}/campaign", h.Campaign)
	app.Get("/projects/{project_id:string}/campaign", h.Campaign)
	app.Get("/api/settings", h.GetSettings)
	app.Put("/api/settings", h.UpdateSettings)
	app.Get("/api/settings/health", h.SettingsHealth)
	app.Post("/api/settings/cleanup", h.SettingsCleanup)
	app.Get("/settings", h.GetSettings)
	app.Put("/settings", h.UpdateSettings)
	app.Get("/settings/health", h.SettingsHealth)
	app.Post("/settings/cleanup", h.SettingsCleanup)
	app.Get("/api/workers", h.WorkerList)
	app.Get("/api/workers/observability", h.WorkerObservability)
	app.Get("/api/workers/history", h.WorkerHistory)
	app.Get("/api/workers/{worker_name:string}/history", h.WorkerHistory)
	app.Get("/api/workers/config", h.WorkerConfig)
	app.Put("/api/workers/config", h.UpdateWorkerConfig)
	app.Post("/api/workers/config/test", h.WorkerTest)
	app.Post("/api/projects/{project_id:string}/steps/{step_id:string}/execute", h.ExecuteStep)
	app.Get("/api/audit", h.ListActivity)
	app.Get("/api/notifications", h.ListNotifications)
	app.Get("/api/notifications/unread-count", h.NotificationUnread)
	app.Post("/api/notifications/read", h.NotificationRead)
	app.Delete("/api/notifications", h.NotificationClear)
	app.Get("/api/templates", h.ListTemplates)
	app.Post("/api/templates", h.CreateTemplate)
	app.Delete("/api/templates/{template_id:string}", h.DeleteTemplate)
	for _, prefix := range []string{"decide", "reason"} {
		app.Post("/projects/{project_id:string}/"+prefix+"/claim", h.ClaimDecide)
		app.Post("/projects/{project_id:string}/"+prefix+"/heartbeat", h.HeartbeatDecide)
		app.Post("/projects/{project_id:string}/"+prefix+"/release", h.ReleaseDecide)
	}
	app.Post("/projects/{project_id:string}/complete", h.CompleteProject)
	app.Post("/projects/{project_id:string}/reopen", h.ReopenProject)
}
