package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kataras/iris/v12"
	"hotmouse/backend/internal/core"
)

func TestDispatcherConfigAPIIsIndependentAndPreservesSecrets(t *testing.T) {
	t.Setenv("HOTMOUSE_AUTH_REQUIRED", "0")
	db, err := core.Open(filepath.Join(t.TempDir(), "dispatcher.db"))
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
		response := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		app.ServeHTTP(response, req)
		return response
	}
	configured := request("PUT", "/api/workers/config", `{"driver":"mock","environment":{"API_KEY":"worker-secret"},"dispatcher":{"driver":"command","command":"test \"$ROLE_KEY\" = planner-secret","model":"planner-model","environment":{"ROLE_KEY":"planner-secret","REMOVE_KEY":"old"}}}`)
	if configured.Code != 200 || strings.Contains(configured.Body.String(), "planner-secret\"}") || strings.Contains(configured.Body.String(), "worker-secret") {
		t.Fatalf("configuration rejected or secret leaked: %d %s", configured.Code, configured.Body.String())
	}
	updated := request("PUT", "/api/workers/config", `{"driver":"mock","dispatcher":{"model":"new-model","environment":{"ROLE_KEY":"***"},"environment_remove":["REMOVE_KEY"]}}`)
	if updated.Code != 200 {
		t.Fatalf("masked update failed: %s", updated.Body.String())
	}
	legacy := request("PUT", "/api/workers/config", `{"driver":"mock","environment":{"API_KEY":"***"}}`)
	if legacy.Code != 200 {
		t.Fatal(legacy.Body.String())
	}
	revealed := request("GET", "/api/workers/config?reveal=1", "")
	var config struct {
		Environment map[string]string      `json:"environment"`
		Dispatcher  *core.DispatcherConfig `json:"dispatcher"`
	}
	if err := json.Unmarshal(revealed.Body.Bytes(), &config); err != nil {
		t.Fatal(err)
	}
	if config.Environment["API_KEY"] != "worker-secret" || config.Dispatcher.Model != "new-model" || config.Dispatcher.Environment["ROLE_KEY"] != "planner-secret" || config.Dispatcher.Environment["REMOVE_KEY"] != "" {
		t.Fatalf("independent configuration changed incorrectly: %+v", config)
	}
	if revealed.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("revealed secrets may be cached")
	}
	if check := request("POST", "/api/workers/config/test?role=dispatcher", ""); check.Code != 200 {
		t.Fatalf("dispatcher health check used worker environment: %s", check.Body.String())
	}
	for _, invalid := range []string{`{"driver":"unknown"}`, `{"driver":"command","command":""}`, `{"environment":{"NEW_KEY":"***"}}`, `{"environment_remove":["ROLE_KEY"],"environment":{"ROLE_KEY":"new"}}`} {
		if response := request("PUT", "/api/workers/config", `{"driver":"mock","dispatcher":`+invalid+`}`); response.Code == 200 {
			t.Fatalf("invalid dispatcher update accepted: %s", invalid)
		}
	}
	if response := request("PUT", "/api/workers/config", `{"driver":"command","command":"printf worker","enabled":false}`); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	if response := request("PUT", "/api/workers/config", `{"dispatcher":{"model":"preserve-worker"}}`); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var workerDriver, workerCommand string
	var enabled int
	if err := db.SQL.QueryRow(`SELECT driver,command,enabled FROM worker_config WHERE id=1`).Scan(&workerDriver, &workerCommand, &enabled); err != nil || workerDriver != "command" || workerCommand != "printf worker" || enabled != 0 {
		t.Fatal("dispatcher-only update changed Worker settings")
	}
	if response := request("PUT", "/api/workers/config", `{"driver":"mock","command":"","enabled":true,"dispatcher":{"driver":"pi","command":"","environment":{"PI_CLI":"/nonexistent-hotmouse-test-cli"}}}`); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	health := request("GET", "/api/settings/health", "")
	var report struct {
		Summary struct {
			DispatcherReachable bool `json:"dispatcher_reachable"`
			OnlineWorkers       int  `json:"online_workers"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(health.Body.Bytes(), &report); err != nil || report.Summary.DispatcherReachable || report.Summary.OnlineWorkers != 4 {
		t.Fatalf("dispatcher health reused Worker readiness: %s err=%v", health.Body.String(), err)
	}
	if response := request("PUT", "/api/workers/config", `{"driver":"mock","dispatcher":null}`); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var raw string
	if err := db.SQL.QueryRow(`SELECT dispatcher_json FROM worker_config WHERE id=1`).Scan(&raw); err != nil || raw != "null" {
		t.Fatalf("shared-provider fallback did not persist: %q err=%v", raw, err)
	}
}
