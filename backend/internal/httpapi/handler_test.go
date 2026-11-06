package httpapi

import (
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kataras/iris/v12"
	"hotmouse/backend/internal/core"
)

func TestVulnerabilityFiltersAndMissingWorkerBinary(t *testing.T) {
	t.Setenv("HOTMOUSE_AUTH_REQUIRED", "0")
	db, err := core.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	project, err := db.CreateProject(t.Context(), core.CreateProject{Title: "filter test", Origin: "target", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ id, severity, status string }{
		{"vuln_high_ignored", "high", "ignored"},
		{"vuln_medium_confirmed", "medium", "confirmed"},
	} {
		_, err = db.SQL.Exec(`INSERT INTO vulnerabilities(id,project_id,finding_id,fact_id,title,description,severity,status,discovered_at) VALUES(?,?,?,?,?,?,?,?,?)`, item.id, project.Project.ID, "finding_"+item.id, "origin", item.id, "proof", item.severity, item.status, "2026-11-02T00:00:00Z")
		if err != nil {
			t.Fatal(err)
		}
	}

	app := iris.New()
	Register(app, New(db))
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	request := func(method, path string) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		app.ServeHTTP(recorder, httptest.NewRequest(method, path, nil))
		return recorder
	}
	filtered := request("GET", "/api/vulnerabilities?severity=high&project_id="+project.Project.ID+"&status=ignored")
	if filtered.Code != 200 {
		t.Fatalf("filtered list status=%d body=%s", filtered.Code, filtered.Body.String())
	}
	var rows []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(filtered.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "vuln_proj_001_title_vuln_high_ignored" {
		t.Fatalf("unexpected filtered vulnerabilities: %+v", rows)
	}
	noMatch := request("GET", "/api/vulnerabilities?severity=high&status=confirmed")
	var noMatchRows []any
	if noMatch.Code != 200 || json.Unmarshal(noMatch.Body.Bytes(), &noMatchRows) != nil || len(noMatchRows) != 0 {
		t.Fatalf("AND filters status=%d body=%s", noMatch.Code, noMatch.Body.String())
	}
	if got := request("GET", "/api/vulnerabilities?project_id=proj_missing"); got.Code != 404 {
		t.Fatalf("missing project status=%d body=%s", got.Code, got.Body.String())
	}
	if _, err := db.SQL.Exec(`UPDATE projects SET created_at=? WHERE id=?`, time.Now().UTC().Add(-48*time.Hour).Format("2006-01-02T15:04:05Z"), project.Project.ID); err != nil {
		t.Fatal(err)
	}
	idleHealth := request("GET", "/api/settings/health")
	var idlePayload struct {
		Summary struct {
			Status string `json:"status"`
		} `json:"summary"`
		Alerts []any `json:"alerts"`
		Checks []struct {
			Key    string `json:"key"`
			Status string `json:"status"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(idleHealth.Body.Bytes(), &idlePayload); err != nil || idlePayload.Summary.Status != "warning" || len(idlePayload.Alerts) == 0 {
		t.Fatalf("idle project alert missing: %s err=%v", idleHealth.Body.String(), err)
	}
	authBypassReported := false
	for _, check := range idlePayload.Checks {
		if check.Key == "auth" && check.Status == "warning" {
			authBypassReported = true
			break
		}
	}
	if !authBypassReported {
		t.Fatalf("development auth bypass was reported healthy: %s", idleHealth.Body.String())
	}

	if _, err := db.SQL.Exec(`UPDATE worker_config SET driver='pi',command='',enabled=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	workerTest := request("POST", "/api/workers/config/test")
	if workerTest.Code != 502 {
		t.Fatalf("missing pi binary status=%d body=%s", workerTest.Code, workerTest.Body.String())
	}
	workerHealth := request("GET", "/api/settings/health")
	var workerPayload struct {
		Summary struct {
			Status        string `json:"status"`
			OfflineWorker int    `json:"offline_workers"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(workerHealth.Body.Bytes(), &workerPayload); err != nil || workerPayload.Summary.Status != "error" || workerPayload.Summary.OfflineWorker != 1 {
		t.Fatalf("unavailable worker not reported: %s err=%v", workerHealth.Body.String(), err)
	}
	var workers []struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(request("GET", "/api/workers").Body.Bytes(), &workers); err != nil || len(workers) != 1 || workers[0].Status != "offline" {
		t.Fatalf("missing worker binary status was not reflected in worker list: %+v err=%v", workers, err)
	}
}

func TestWorkerWorkspaceConfigAndLegacyUpdate(t *testing.T) {
	t.Setenv("HOTMOUSE_AUTH_REQUIRED", "0")
	db, err := core.Open(filepath.Join(t.TempDir(), "worker-config.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	app := iris.New()
	Register(app, New(db))
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	request := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest("PUT", "/api/workers/config", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		app.ServeHTTP(recorder, req)
		return recorder
	}
	configured := request(`{"name":"local","driver":"mock","worker_count":3,"workspace_root":"/tmp/hotmouse-workspaces","completed_action":"remove","context_root":"/tmp/hotmouse-context","runtime":{"execute_timeout_seconds":60,"recovery_attempts":2},"environment":{"PI_API_KEY":"fixture-secret","PI_MODEL":"fixture-model"}}`)
	if configured.Code != 200 {
		t.Fatalf("configure workspace status=%d body=%s", configured.Code, configured.Body.String())
	}
	legacyUpdate := request(`{"name":"local","driver":"mock","command":"","environment":{"PI_API_KEY":"***","PI_MODEL":"new-model"},"enabled":true}`)
	if legacyUpdate.Code != 200 {
		t.Fatalf("legacy worker update status=%d body=%s", legacyUpdate.Code, legacyUpdate.Body.String())
	}
	var response struct {
		WorkspaceRoot   string             `json:"workspace_root"`
		CompletedAction string             `json:"completed_action"`
		ContextRoot     string             `json:"context_root"`
		Runtime         core.WorkerRuntime `json:"runtime"`
		WorkerCount     int                `json:"worker_count"`
	}
	if err := json.Unmarshal(legacyUpdate.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.WorkspaceRoot != "/tmp/hotmouse-workspaces" || response.CompletedAction != "remove" || response.ContextRoot != "/tmp/hotmouse-context" {
		t.Fatalf("legacy update lost workspace settings: %+v", response)
	}
	if response.Runtime.ExecuteTimeoutSeconds != 60 || response.Runtime.RecoveryAttempts != 2 {
		t.Fatalf("runtime was lost: %+v", response.Runtime)
	}
	if response.WorkerCount != 3 {
		t.Fatalf("worker count was lost: %d", response.WorkerCount)
	}
	var envJSON string
	if err := db.SQL.QueryRow(`SELECT environment_json FROM worker_config`).Scan(&envJSON); err != nil {
		t.Fatal(err)
	}
	var env map[string]string
	_ = json.Unmarshal([]byte(envJSON), &env)
	if env["PI_API_KEY"] != "fixture-secret" || env["PI_MODEL"] != "new-model" {
		t.Fatalf("masked credential update failed: model=%s secret retained=%t", env["PI_MODEL"], env["PI_API_KEY"] == "fixture-secret")
	}
	if invalid := request(`{"name":"local","driver":"mock","completed_action":"delete"}`); invalid.Code != 400 {
		t.Fatalf("invalid cleanup action status=%d body=%s", invalid.Code, invalid.Body.String())
	}
	if invalid := request(`{"name":"local","driver":"mock","runtime":{"execute_timeout_seconds":0}}`); invalid.Code != 400 {
		t.Fatalf("invalid runtime status=%d body=%s", invalid.Code, invalid.Body.String())
	}
	if invalid := request(`{"name":"local","driver":"mock","worker_count":0}`); invalid.Code != 400 {
		t.Fatalf("invalid worker count status=%d body=%s", invalid.Code, invalid.Body.String())
	}
}

func TestProjectWorkerLimitAPI(t *testing.T) {
	t.Setenv("HOTMOUSE_AUTH_REQUIRED", "0")
	db, err := core.Open(filepath.Join(t.TempDir(), "project-worker-limit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	project, err := db.CreateProject(t.Context(), core.CreateProject{Title: "limit", Origin: "target", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	app := iris.New()
	Register(app, New(db))
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	request := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest("PUT", "/projects/"+project.Project.ID+"/worker-limit", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		app.ServeHTTP(recorder, req)
		return recorder
	}
	updated := request(`{"max_workers":3}`)
	var response core.Project
	if updated.Code != 200 || json.Unmarshal(updated.Body.Bytes(), &response) != nil || response.MaxWorkers != 3 {
		t.Fatalf("project worker limit update status=%d body=%s project=%+v", updated.Code, updated.Body.String(), response)
	}
	if invalid := request(`{"max_workers":33}`); invalid.Code != 400 {
		t.Fatalf("out-of-range project worker limit status=%d body=%s", invalid.Code, invalid.Body.String())
	}
}

func TestConfiguredPiCLIUsedByWorkerHealth(t *testing.T) {
	t.Setenv("HOTMOUSE_AUTH_REQUIRED", "0")
	t.Setenv("PATH", t.TempDir())
	db, err := core.Open(filepath.Join(t.TempDir(), "worker-health.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	cli := filepath.Join(t.TempDir(), "configured-pi")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\nprintf 'pi fixture 1.0\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	environment, _ := json.Marshal(map[string]string{"PI_CLI": cli})
	if _, err := db.SQL.Exec(`UPDATE worker_config SET driver='pi',command='',environment_json=?,enabled=1 WHERE id=1`, string(environment)); err != nil {
		t.Fatal(err)
	}
	app := iris.New()
	Register(app, New(db))
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	request := func(method, path string) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		app.ServeHTTP(recorder, httptest.NewRequest(method, path, nil))
		return recorder
	}
	if response := request("POST", "/api/workers/config/test"); response.Code != 200 {
		t.Fatalf("configured PI_CLI was not tested: status=%d body=%s", response.Code, response.Body.String())
	}
	var workers []struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(request("GET", "/api/workers").Body.Bytes(), &workers); err != nil || len(workers) != 1 || workers[0].Status != "idle" {
		t.Fatalf("configured PI_CLI was not reflected in worker status: %+v err=%v", workers, err)
	}
	var health struct {
		Checks []struct {
			Key    string `json:"key"`
			Status string `json:"status"`
		} `json:"checks"`
	}
	if response := request("GET", "/api/settings/health"); json.Unmarshal(response.Body.Bytes(), &health) != nil {
		t.Fatalf("invalid settings health response: %s", response.Body.String())
	}
	for _, check := range health.Checks {
		if check.Key == "dispatcher" && check.Status == "ok" {
			return
		}
	}
	t.Fatalf("configured PI_CLI was not reflected in settings health: %+v", health.Checks)
}

func TestProjectReadAndLegacyCompletionLeaseSideEffects(t *testing.T) {
	t.Setenv("HOTMOUSE_AUTH_REQUIRED", "0")
	db, err := core.Open(filepath.Join(t.TempDir(), "project-leases.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	project, err := db.CreateProject(t.Context(), core.CreateProject{Title: "lease routes", Origin: "target", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	rootGoal := project.Goals[0].ID
	if _, err := db.CreateGoal(t.Context(), project.Project.ID, core.CreateGoal{Description: "child", Creator: "test", ParentGoalID: &rootGoal}); err != nil {
		t.Fatal(err)
	}
	step, err := db.CreateStep(t.Context(), project.Project.ID, core.CreateStep{From: []string{"origin"}, Description: "inspect", Creator: "test", Worker: stringPointer("step-worker")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClaimDecide(t.Context(), project.Project.ID, core.ClaimDecide{Worker: "decide-worker", Trigger: "manual"}); err != nil {
		t.Fatal(err)
	}
	stale := "2020-01-01T00:00:00Z"
	setStaleLeases := func() {
		t.Helper()
		if _, err := db.SQL.Exec(`UPDATE steps SET last_heartbeat_at=? WHERE project_id=? AND id=?`, stale, project.Project.ID, step.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.SQL.Exec(`UPDATE projects SET decide_last_heartbeat_at=? WHERE id=?`, stale, project.Project.ID); err != nil {
			t.Fatal(err)
		}
	}
	setStaleLeases()

	app := iris.New()
	Register(app, New(db))
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		app.ServeHTTP(recorder, req)
		return recorder
	}
	assertWorkers := func(stepWant, decideWant string) {
		t.Helper()
		var gotStep, gotDecide sql.NullString
		if err := db.SQL.QueryRow(`SELECT worker FROM steps WHERE project_id=? AND id=?`, project.Project.ID, step.ID).Scan(&gotStep); err != nil {
			t.Fatal(err)
		}
		if err := db.SQL.QueryRow(`SELECT decide_worker FROM projects WHERE id=?`, project.Project.ID).Scan(&gotDecide); err != nil {
			t.Fatal(err)
		}
		if stepWant == "" && gotStep.Valid || stepWant != "" && (!gotStep.Valid || gotStep.String != stepWant) {
			t.Fatalf("step worker=%+v, want %q", gotStep, stepWant)
		}
		if decideWant == "" && gotDecide.Valid || decideWant != "" && (!gotDecide.Valid || gotDecide.String != decideWant) {
			t.Fatalf("decide worker=%+v, want %q", gotDecide, decideWant)
		}
	}

	base := "/api/projects/" + project.Project.ID
	if response := request("GET", base+"/timeline", ""); response.Code != 200 {
		t.Fatalf("timeline status=%d body=%s", response.Code, response.Body.String())
	}
	assertWorkers("step-worker", "decide-worker")
	if response := request("PUT", "/projects/"+project.Project.ID+"/title", `{"title":"renamed"}`); response.Code != 200 {
		t.Fatalf("title status=%d body=%s", response.Code, response.Body.String())
	}
	assertWorkers("step-worker", "decide-worker")
	if response := request("GET", base+"/campaign", ""); response.Code != 200 {
		t.Fatalf("campaign status=%d body=%s", response.Code, response.Body.String())
	}
	assertWorkers("", "")

	if _, err := db.HeartbeatStep(t.Context(), project.Project.ID, step.ID, "step-worker"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClaimDecide(t.Context(), project.Project.ID, core.ClaimDecide{Worker: "decide-worker", Trigger: "manual"}); err != nil {
		t.Fatal(err)
	}
	setStaleLeases()
	if response := request("POST", "/projects/"+project.Project.ID+"/complete", `{"from":["origin"],"description":"goal verified","worker":"human"}`); response.Code != 200 {
		t.Fatalf("legacy completion status=%d body=%s", response.Code, response.Body.String())
	}
	assertWorkers("step-worker", "")
	if _, err := db.ClaimDecide(t.Context(), project.Project.ID, core.ClaimDecide{Worker: "decide-worker", Trigger: "manual"}); err != nil {
		t.Fatal(err)
	}
	setStaleLeases()
	if response := request("POST", "/projects/"+project.Project.ID+"/complete", `{"from":["origin"],"description":"no root goal","worker":"human"}`); response.Code != 409 {
		t.Fatalf("missing root goal status=%d body=%s", response.Code, response.Body.String())
	}
	assertWorkers("step-worker", "decide-worker")
}

func TestCoreValidationUsesUnprocessableEntity(t *testing.T) {
	t.Setenv("HOTMOUSE_AUTH_REQUIRED", "0")
	db, err := core.Open(filepath.Join(t.TempDir(), "validation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	project, err := db.CreateProject(t.Context(), core.CreateProject{Title: "validation", Origin: "target", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	app := iris.New()
	Register(app, New(db))
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		app.ServeHTTP(recorder, req)
		return recorder
	}
	base := "/projects/" + project.Project.ID
	if response := request("POST", base+"/steps", `{}`); response.Code != iris.StatusUnprocessableEntity {
		t.Fatalf("empty step status=%d body=%s", response.Code, response.Body.String())
	}
	if response := request("POST", base+"/goals", `{}`); response.Code != iris.StatusUnprocessableEntity {
		t.Fatalf("empty goal status=%d body=%s", response.Code, response.Body.String())
	}
	if response := request("POST", base+"/findings", `{}`); response.Code != iris.StatusUnprocessableEntity {
		t.Fatalf("empty finding status=%d body=%s", response.Code, response.Body.String())
	}
	if response := request("PATCH", base+"/goals/"+project.Goals[0].ID, `{"status":"completed"}`); response.Code != iris.StatusUnprocessableEntity {
		t.Fatalf("goal status patch status=%d body=%s", response.Code, response.Body.String())
	}
	if response := request("POST", base+"/steps", `{"from":`); response.Code != iris.StatusUnprocessableEntity {
		t.Fatalf("malformed step JSON status=%d body=%s", response.Code, response.Body.String())
	}
	if response := request("GET", base+"/export?format=yaml", ""); response.Code != iris.StatusOK || !strings.Contains(response.Body.String(), "project:") {
		t.Fatalf("YAML export status=%d body=%s", response.Code, response.Body.String())
	}
	if response := request("GET", base+"/export?format=timeline", ""); response.Code != iris.StatusOK || !strings.Contains(response.Body.String(), "PROJECT CREATED") || !strings.Contains(response.Body.String(), "GOAL CREATED") {
		t.Fatalf("timeline export status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSessionCookieUsesSecureOnTLS(t *testing.T) {
	t.Setenv("HOTMOUSE_AUTH_REQUIRED", "0")
	db, err := core.Open(filepath.Join(t.TempDir(), "cookie.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	app := iris.New()
	h := New(db)
	app.Get("/fixture", func(ctx iris.Context) {
		h.setSessionCookie(ctx, "fixture-token")
		ctx.StatusCode(iris.StatusOK)
	})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	loginRecorder := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "http://example.test/fixture", nil)
	request.TLS = &tls.ConnectionState{}
	app.ServeHTTP(loginRecorder, request)
	secure := false
	for _, cookie := range loginRecorder.Result().Cookies() {
		if cookie.Name == "session_token" {
			secure = cookie.Secure
		}
	}
	if !secure {
		t.Fatal("session cookie is not Secure on TLS")
	}
	localRecorder := httptest.NewRecorder()
	app.ServeHTTP(localRecorder, httptest.NewRequest("GET", "http://localhost/fixture", nil))
	for _, cookie := range localRecorder.Result().Cookies() {
		if cookie.Name == "session_token" && cookie.Secure {
			t.Fatal("HTTP localhost session cookie should remain usable for local development")
		}
	}
	remoteRecorder := httptest.NewRecorder()
	app.ServeHTTP(remoteRecorder, httptest.NewRequest("GET", "http://example.test/fixture", nil))
	for _, cookie := range remoteRecorder.Result().Cookies() {
		if cookie.Name == "session_token" && !cookie.Secure {
			t.Fatal("remote HTTP session cookie should be Secure")
		}
	}
}

func TestAuthAPIUsesUUIDUserIDAndAuditReferences(t *testing.T) {
	t.Setenv("HOTMOUSE_AUTH_REQUIRED", "1")
	db, err := core.Open(filepath.Join(t.TempDir(), "auth-api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	app := iris.New()
	Register(app, New(db))
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		app.ServeHTTP(recorder, req)
		return recorder
	}
	captchaResponse := request("GET", "/api/auth/captcha", "")
	var captcha struct {
		ID       string `json:"captcha_id"`
		Question string `json:"question"`
	}
	if captchaResponse.Code != 200 || json.Unmarshal(captchaResponse.Body.Bytes(), &captcha) != nil {
		t.Fatalf("captcha status=%d body=%s", captchaResponse.Code, captchaResponse.Body.String())
	}
	var left, right int
	if _, err := fmt.Sscanf(captcha.Question, "%d + %d = ?", &left, &right); err != nil {
		t.Fatalf("invalid captcha question %q: %v", captcha.Question, err)
	}
	registered := request("POST", "/api/auth/register", fmt.Sprintf(`{"username":"protocol-user","password":"secret123","captcha_id":%q,"captcha_answer":%q}`, captcha.ID, fmt.Sprint(left+right)))
	var user struct {
		ID string `json:"id"`
	}
	if registered.Code != 201 || json.Unmarshal(registered.Body.Bytes(), &user) != nil || !strings.HasPrefix(user.ID, "user_") || len(user.ID) != len("user_")+32 {
		t.Fatalf("registration user ID mismatch: status=%d body=%s", registered.Code, registered.Body.String())
	}
	var sessionToken string
	for _, cookie := range registered.Result().Cookies() {
		if cookie.Name == "session_token" {
			sessionToken = cookie.Value
		}
	}
	if sessionToken == "" {
		t.Fatalf("registration omitted session cookie: %+v", registered.Result().Cookies())
	}
	meRequest := httptest.NewRequest("GET", "/api/auth/me", nil)
	meRequest.AddCookie(&http.Cookie{Name: "session_token", Value: sessionToken})
	meResponse := httptest.NewRecorder()
	app.ServeHTTP(meResponse, meRequest)
	var me struct {
		ID string `json:"id"`
	}
	if meResponse.Code != 200 || json.Unmarshal(meResponse.Body.Bytes(), &me) != nil || me.ID != user.ID {
		t.Fatalf("/me did not preserve string user ID: status=%d body=%s", meResponse.Code, meResponse.Body.String())
	}
	projectRequest := httptest.NewRequest("POST", "/projects", strings.NewReader(`{"title":"audit owner","origin":"target","goal":"verify"}`))
	projectRequest.Header.Set("Content-Type", "application/json")
	projectRequest.AddCookie(&http.Cookie{Name: "session_token", Value: sessionToken})
	projectResponse := httptest.NewRecorder()
	app.ServeHTTP(projectResponse, projectRequest)
	if projectResponse.Code != 201 {
		t.Fatalf("authenticated project creation status=%d body=%s", projectResponse.Code, projectResponse.Body.String())
	}
	var auditUser sql.NullString
	if err := db.SQL.QueryRow(`SELECT user_id FROM audit_log WHERE action='POST' ORDER BY id DESC LIMIT 1`).Scan(&auditUser); err != nil || !auditUser.Valid || auditUser.String != user.ID {
		t.Fatalf("audit user ID=%+v want %q err=%v", auditUser, user.ID, err)
	}
}

func TestAuthValidationAndBrowserRejectProtocol(t *testing.T) {
	t.Setenv("HOTMOUSE_AUTH_REQUIRED", "1")
	db, err := core.Open(filepath.Join(t.TempDir(), "auth-boundaries.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	app := iris.New()
	Register(app, New(db))
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body, accept string) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		app.ServeHTTP(recorder, req)
		return recorder
	}
	if response := request("POST", "/api/auth/register", `{}`, ""); response.Code != 422 {
		t.Fatalf("missing registration fields status=%d body=%s", response.Code, response.Body.String())
	}
	if response := request("POST", "/api/auth/register", `{"username":"ab","password":"short"}`, ""); response.Code != 422 {
		t.Fatalf("invalid registration fields were checked after captcha status=%d body=%s", response.Code, response.Body.String())
	}
	response := request("GET", "/projects", "", "text/html")
	if response.Code != 302 || response.Header().Get("Location") != "/" {
		t.Fatalf("browser auth rejection status=%d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	cleared := false
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == "session_token" && cookie.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatalf("browser auth rejection did not clear session cookie: %+v", response.Result().Cookies())
	}
}

func TestTemplateAndVulnerabilityValidationStatuses(t *testing.T) {
	t.Setenv("HOTMOUSE_AUTH_REQUIRED", "0")
	db, err := core.Open(filepath.Join(t.TempDir(), "validation-status.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	app := iris.New()
	Register(app, New(db))
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		app.ServeHTTP(recorder, req)
		return recorder
	}
	if response := request("POST", "/api/templates", `{}`); response.Code != 422 {
		t.Fatalf("missing template fields status=%d body=%s", response.Code, response.Body.String())
	}
	if response := request("GET", "/api/vulnerabilities?severity=bogus", ""); response.Code != 422 {
		t.Fatalf("invalid vulnerability severity status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestTemplatesUseUUIDIDsAndPerUserOwnership(t *testing.T) {
	t.Setenv("HOTMOUSE_AUTH_REQUIRED", "1")
	db, err := core.Open(filepath.Join(t.TempDir(), "templates-api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	first, err := db.Register(t.Context(), "template-one", "secret123")
	if err != nil {
		t.Fatal(err)
	}
	firstToken, _, err := db.Login(t.Context(), first.Username, "secret123")
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.Register(t.Context(), "template-two", "secret123")
	if err != nil {
		t.Fatal(err)
	}
	secondToken, _, err := db.Login(t.Context(), second.Username, "secret123")
	if err != nil {
		t.Fatal(err)
	}
	app := iris.New()
	Register(app, New(db))
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body, token string) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.AddCookie(&http.Cookie{Name: "session_token", Value: token})
		}
		app.ServeHTTP(recorder, req)
		return recorder
	}
	listed := request("GET", "/api/templates", "", firstToken)
	var templates []struct {
		ID      string `json:"id"`
		Builtin bool   `json:"is_builtin"`
		UserID  string `json:"user_id"`
		Title   string `json:"title"`
	}
	if listed.Code != 200 || json.Unmarshal(listed.Body.Bytes(), &templates) != nil || len(templates) < 4 {
		t.Fatalf("built-in templates missing: status=%d body=%s", listed.Code, listed.Body.String())
	}
	custom := request("POST", "/api/templates", `{"title":"Local template","origin":"target","goal":"verify","hints":[{"content":"inspect","creator":"analyst"}]}`, firstToken)
	var created struct {
		ID     string `json:"id"`
		UserID string `json:"user_id"`
	}
	if custom.Code != 201 || json.Unmarshal(custom.Body.Bytes(), &created) != nil || !strings.HasPrefix(created.ID, "tmpl_") || created.UserID != first.ID {
		t.Fatalf("custom template protocol mismatch: status=%d body=%s user=%+v", custom.Code, custom.Body.String(), created)
	}
	other := request("GET", "/api/templates", "", secondToken)
	if strings.Contains(other.Body.String(), "Local template") {
		t.Fatalf("template leaked across users: %s", other.Body.String())
	}
	if response := request("DELETE", "/api/templates/"+created.ID, "", secondToken); response.Code != 403 {
		t.Fatalf("cross-user delete status=%d body=%s", response.Code, response.Body.String())
	}
	if response := request("DELETE", "/api/templates/"+created.ID, "", firstToken); response.Code != 204 {
		t.Fatalf("owner delete status=%d body=%s", response.Code, response.Body.String())
	}
}

func stringPointer(value string) *string { return &value }

func TestWorkerStatusUsesLiveLeasesAndHistoryMetrics(t *testing.T) {
	t.Setenv("HOTMOUSE_AUTH_REQUIRED", "0")
	db, err := core.Open(filepath.Join(t.TempDir(), "worker-status.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	project, err := db.CreateProject(t.Context(), core.CreateProject{Title: "worker status", Origin: "target", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	step, err := db.CreateStep(t.Context(), project.Project.ID, core.CreateStep{From: []string{"origin"}, Description: strings.Repeat("inspect ", 20), Creator: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE worker_config SET worker_count=3 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := db.SQL.Exec(`UPDATE steps SET worker='local/worker/01',last_heartbeat_at=? WHERE project_id=? AND id=?`, now.Format(time.RFC3339), project.Project.ID, step.ID); err != nil {
		t.Fatal(err)
	}
	for _, duration := range []time.Duration{10 * time.Second, 30 * time.Second} {
		started := now.Add(-2*time.Minute - duration).Format(time.RFC3339)
		finished := now.Add(-2 * time.Minute).Format(time.RFC3339)
		if _, err := db.SQL.Exec(`INSERT INTO worker_task_history(worker,project_id,task_type,status,started_at,finished_at) VALUES('local',?,'execute','completed',?,?)`, project.Project.ID, started, finished); err != nil {
			t.Fatal(err)
		}
	}
	submittedStart := now.Add(-time.Minute).Format(time.RFC3339)
	submittedFinish := now.Add(-40 * time.Second).Format(time.RFC3339)
	if _, err := db.SQL.Exec(`INSERT INTO worker_task_history(worker,project_id,task_type,status,started_at,finished_at) VALUES('local/worker/01',?,'execute','submitted',?,?)`, project.Project.ID, submittedStart, submittedFinish); err != nil {
		t.Fatal(err)
	}
	app := iris.New()
	Register(app, New(db))
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	request := func() *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		app.ServeHTTP(recorder, httptest.NewRequest("GET", "/api/workers", nil))
		return recorder
	}
	var workers []struct {
		Status                  string   `json:"status"`
		CurrentTask             string   `json:"current_task"`
		TasksCompleted          int      `json:"tasks_completed"`
		AverageDuration         *float64 `json:"avg_duration_seconds"`
		LastHeartbeatSecondsAgo *float64 `json:"last_heartbeat_seconds_ago"`
		ProjectID               string   `json:"project_id"`
		StepID                  string   `json:"step_id"`
	}
	response := request()
	if err := json.Unmarshal(response.Body.Bytes(), &workers); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || len(workers) != 1 || workers[0].Status != "busy" || workers[0].ProjectID != project.Project.ID || workers[0].StepID != step.ID || workers[0].CurrentTask == "" || len([]rune(workers[0].CurrentTask)) > 120 || workers[0].TasksCompleted != 3 || workers[0].AverageDuration == nil || *workers[0].AverageDuration != 20 || workers[0].LastHeartbeatSecondsAgo == nil {
		t.Fatalf("unexpected live worker status=%d body=%s decoded=%+v", response.Code, response.Body.String(), workers)
	}
	var observability struct {
		Summary struct {
			Total        int `json:"total"`
			Online       int `json:"online"`
			Busy         int `json:"busy"`
			Idle         int `json:"idle"`
			RunningTasks int `json:"running_tasks"`
		} `json:"summary"`
		Outcomes struct {
			Success int `json:"success"`
		} `json:"outcomes"`
		TaskMix []struct {
			TaskType string `json:"task_type"`
			Count    int    `json:"count"`
		} `json:"task_mix"`
		Running []struct {
			StepID         string  `json:"step_id"`
			RunningSeconds float64 `json:"running_seconds"`
		} `json:"running_tasks"`
		Recent []any `json:"recent_history"`
	}
	requestCtx := httptest.NewRecorder()
	app.ServeHTTP(requestCtx, httptest.NewRequest("GET", "/api/workers/observability", nil))
	if err := json.Unmarshal(requestCtx.Body.Bytes(), &observability); err != nil {
		t.Fatal(err)
	}
	if requestCtx.Code != 200 || observability.Summary.Total != 3 || observability.Summary.Online != 3 || observability.Summary.Busy != 1 || observability.Summary.Idle != 2 || observability.Summary.RunningTasks != 1 || observability.Outcomes.Success != 3 || len(observability.TaskMix) != 1 || observability.TaskMix[0].TaskType != "execute" || observability.TaskMix[0].Count != 3 || len(observability.Running) != 1 || observability.Running[0].StepID != step.ID || len(observability.Recent) != 3 {
		t.Fatalf("worker observability incomplete: status=%d body=%s decoded=%+v", requestCtx.Code, requestCtx.Body.String(), observability)
	}
	if _, err := db.SQL.Exec(`UPDATE steps SET last_heartbeat_at=? WHERE project_id=? AND id=?`, now.Add(-time.Hour).Format(time.RFC3339), project.Project.ID, step.ID); err != nil {
		t.Fatal(err)
	}
	workers = nil
	response = request()
	if err := json.Unmarshal(response.Body.Bytes(), &workers); err != nil || len(workers) != 1 || workers[0].Status != "idle" {
		t.Fatalf("expired lease was reported as busy: status=%d body=%s decoded=%+v err=%v", response.Code, response.Body.String(), workers, err)
	}
}

func TestSettingsHealthSurfacesInterruptedExecuteRecovery(t *testing.T) {
	t.Setenv("HOTMOUSE_AUTH_REQUIRED", "0")
	db, err := core.Open(filepath.Join(t.TempDir(), "worker-recovery-health.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	project, err := db.CreateProject(t.Context(), core.CreateProject{Title: "recovery health", Origin: "target", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	step, err := db.CreateStep(t.Context(), project.Project.ID, core.CreateStep{From: []string{"origin"}, Description: "interrupted action", Creator: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO worker_execute_recovery(project_id,step_id,payload_json,status,worker_name,created_at) VALUES(?,?,'','needs_attention','local',?)`, project.Project.ID, step.ID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	app := iris.New()
	Register(app, New(db))
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	request := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		app.ServeHTTP(recorder, httptest.NewRequest("GET", path, nil))
		return recorder
	}
	var health struct {
		Summary struct {
			Status string `json:"status"`
		} `json:"summary"`
		Stats struct {
			RecoveryAttention int `json:"worker_recovery_attention"`
		} `json:"stats"`
		Alerts []struct {
			Title string `json:"title"`
		} `json:"alerts"`
	}
	response := request("/api/settings/health")
	if err := json.Unmarshal(response.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || health.Summary.Status != "warning" || health.Stats.RecoveryAttention != 1 || len(health.Alerts) == 0 || health.Alerts[0].Title != "Worker step execution failed" {
		t.Fatalf("interrupted execution was not visible in health: status=%d body=%s", response.Code, response.Body.String())
	}
	var observability struct {
		Summary struct {
			AttentionSteps int `json:"attention_steps"`
		} `json:"summary"`
	}
	response = request("/api/workers/observability")
	if err := json.Unmarshal(response.Body.Bytes(), &observability); err != nil || observability.Summary.AttentionSteps != 1 {
		t.Fatalf("interrupted execution was not visible in worker observability: status=%d body=%s err=%v", response.Code, response.Body.String(), err)
	}
}

func TestVulnerabilityMergeAndReviewStatusInheritance(t *testing.T) {
	t.Setenv("HOTMOUSE_AUTH_REQUIRED", "0")
	db, err := core.Open(filepath.Join(t.TempDir(), "vulnerability-merge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	project, err := db.CreateProject(t.Context(), core.CreateProject{Title: "merge test", Origin: "target", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	seed := func(id, fact, title, description, severity, status, when string) {
		t.Helper()
		evidence, _ := json.Marshal([]string{description})
		_, err := db.SQL.Exec(`INSERT INTO vulnerabilities(id,project_id,finding_id,fact_id,title,description,severity,status,discovered_at,source_fact_ids_json,evidence_json) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, project.Project.ID, "finding_"+id, fact, title, description, severity, status, when, `["origin"]`, string(evidence))
		if err != nil {
			t.Fatal(err)
		}
	}
	seed("raw_v1", "f001", "SQL Injection on /api/login", "SQL injection confirmed at /api/login", "high", "confirmed", "2026-11-01T00:00:00Z")
	seed("raw_v2", "f002", "SQL injection on login endpoint", "SQL injection confirmed at /api/login", "medium", "ignored", "2026-11-02T00:00:00Z")
	app := iris.New()
	Register(app, New(db))
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		app.ServeHTTP(recorder, httptest.NewRequest(method, path, strings.NewReader(body)))
		return recorder
	}
	list := request("GET", "/api/vulnerabilities?project_id="+project.Project.ID, "")
	var merged []struct {
		ID             string   `json:"id"`
		Severity       string   `json:"severity"`
		Status         string   `json:"status"`
		RelatedFactIDs []string `json:"related_fact_ids"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &merged); err != nil {
		t.Fatal(err)
	}
	const mergedID = "vuln_proj_001_sql_injection_path_api_login"
	if list.Code != 200 || len(merged) != 1 || merged[0].ID != mergedID || merged[0].Severity != "medium" || merged[0].Status != "confirmed" || len(merged[0].RelatedFactIDs) != 2 {
		t.Fatalf("unexpected merged response status=%d body=%s decoded=%+v", list.Code, list.Body.String(), merged)
	}
	summary := request("GET", "/api/vulnerabilities/summary", "")
	var counts map[string]int
	if err := json.Unmarshal(summary.Body.Bytes(), &counts); err != nil || counts["medium"] != 1 || counts["high"] != 0 {
		t.Fatalf("summary did not count merged winners: %s err=%v", summary.Body.String(), err)
	}
	updated := request("PATCH", "/api/vulnerabilities/"+mergedID+"/status", `{"status":"ignored"}`)
	if updated.Code != 200 {
		t.Fatalf("merged status update status=%d body=%s", updated.Code, updated.Body.String())
	}
	for _, id := range []string{"raw_v1", "raw_v2"} {
		var status string
		if err := db.SQL.QueryRow(`SELECT status FROM vulnerabilities WHERE id=?`, id).Scan(&status); err != nil || status != "ignored" {
			t.Fatalf("member %s status=%q err=%v", id, status, err)
		}
	}
	seed("raw_v3", "f003", "SQL Injection at /api/login", "new SQL injection confirmed at /api/login", "high", "confirmed", "2026-11-03T00:00:00Z")
	seed("raw_v4", "f004", "RCE at /api/login", "remote command execution confirmed at /api/login", "low", "confirmed", "2026-11-04T00:00:00Z")
	list = request("GET", "/api/vulnerabilities?project_id="+project.Project.ID, "")
	merged = nil
	if err := json.Unmarshal(list.Body.Bytes(), &merged); err != nil {
		t.Fatal(err)
	}
	if list.Code != 200 || len(merged) != 2 {
		t.Fatalf("different vulnerability classes were merged: %s", list.Body.String())
	}
	var sqlInjection, rce *struct {
		ID             string   `json:"id"`
		Severity       string   `json:"severity"`
		Status         string   `json:"status"`
		RelatedFactIDs []string `json:"related_fact_ids"`
	}
	for i := range merged {
		item := &merged[i]
		if strings.Contains(item.ID, "sql_injection") {
			sqlInjection = item
		} else {
			rce = item
		}
	}
	if sqlInjection == nil || sqlInjection.Status != "ignored" || len(sqlInjection.RelatedFactIDs) != 3 || rce == nil || rce.ID != "vuln_proj_001_remote_command_execution_path_api_login" {
		t.Fatalf("review state was not inherited by later matching finding: %s", list.Body.String())
	}
}
