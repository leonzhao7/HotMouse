package httpapi

import (
	"context"
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

func platformWebHarness(t *testing.T, path string) (*core.DB, *Handler, func(string, string, string) *httptest.ResponseRecorder) {
	t.Helper()
	db, err := core.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.SQL.Close() })
	h := New(db)
	app := iris.New()
	app.Logger().SetLevel("disable")
	Register(app, h)
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
	return db, h, request
}

func TestPlatformWebConfigurationPersistsAndProtectsActiveTasks(t *testing.T) {
	t.Setenv("HOTMOUSE_AUTH_REQUIRED", "1")
	t.Setenv("BENCHMARK_BASE_URL", "")
	t.Setenv("BENCHMARK_TOKEN", "")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("BENCHMARK_TOKEN") != "web-task-secret" {
			t.Errorf("saved task token was not used")
		}
		fmt.Fprint(w, `[{"unique_code":"e1-01","flag_count":1,"container_status":"stopped","container_addr":[]}]`)
	}))
	defer upstream.Close()
	path := filepath.Join(t.TempDir(), "web.db")
	db, h, request := platformWebHarness(t, path)
	for _, method := range []string{"GET", "PUT"} {
		if response := request(method, "/api/platforms/tsec/config", `{}`); response.Code != 401 {
			t.Fatalf("unauthenticated config access: %d %s", response.Code, response.Body)
		}
	}
	if response := request("GET", "/api/competitions/status", ""); response.Code != 401 {
		t.Fatalf("unauthenticated scheduler access: %d", response.Code)
	}
	t.Setenv("HOTMOUSE_AUTH_REQUIRED", "0")
	if response := request("PUT", "/api/platforms/unknown/config", `{}`); response.Code != 404 {
		t.Fatalf("unknown platform: %d %s", response.Code, response.Body)
	}
	for _, body := range []string{`{"base_url":"file:///tmp/platform","token":"secret"}`, fmt.Sprintf(`{"base_url":%q,"token":"***"}`, upstream.URL)} {
		if response := request("PUT", "/api/platforms/tsec/config", body); response.Code != 422 {
			t.Fatalf("invalid config: %d %s", response.Code, response.Body)
		}
	}
	body := fmt.Sprintf(`{"base_url":%q,"token":"web-task-secret"}`, upstream.URL+"/")
	if response := request("PUT", "/api/platforms/tsec/config", body); response.Code != 200 || strings.Contains(response.Body.String(), "web-task-secret") || !strings.Contains(response.Body.String(), `"token_set":true`) {
		t.Fatalf("save/redaction: %d %s", response.Code, response.Body)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if info, err := os.Stat(path + suffix); err == nil && info.Mode().Perm() != 0600 {
			t.Fatalf("credential database %s has mode %v", suffix, info.Mode().Perm())
		}
	}
	if response := request("GET", "/api/platforms", ""); response.Code != 200 || !strings.Contains(response.Body.String(), `"configured":true`) {
		t.Fatalf("saved platform not listed as configured: %d %s", response.Code, response.Body)
	}
	if response := request("GET", "/api/platforms/tsec/challenges", ""); response.Code != 200 {
		t.Fatalf("saved credentials not usable: %d %s", response.Code, response.Body)
	}
	_, scope, err := h.platform("tsec")
	if err != nil {
		t.Fatal(err)
	}
	run, err := db.CreateCompetitionRun(t.Context(), "tsec", scope, core.CompetitionConfig{Rules: "only test targets"}, []string{"e1-01"})
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"", `,"token":"***"`} {
		if response := request("PUT", "/api/platforms/tsec/config", fmt.Sprintf(`{"base_url":%q%s}`, upstream.URL, token)); response.Code != 200 {
			t.Fatalf("unchanged token should be retained during active run: %d %s", response.Code, response.Body)
		}
	}
	if response := request("PUT", "/api/platforms/tsec/config", fmt.Sprintf(`{"base_url":%q,"token":"another-task"}`, upstream.URL)); response.Code != 409 || !strings.Contains(response.Body.String(), "platform_task_active") {
		t.Fatalf("active task credentials changed: %d %s", response.Code, response.Body)
	}
	if err := db.FinishCompetitionRun(t.Context(), run.ID, "stopped", ""); err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(t.Context(), core.CreateProject{Title: "manual platform project", Origin: "test", Goal: "test", PlatformBinding: &core.PlatformBinding{Platform: "tsec", ScopeID: scope, ChallengeID: "e1-01", Challenge: json.RawMessage(`{}`), Delivery: json.RawMessage(`{}`)}})
	if err != nil {
		t.Fatal(err)
	}
	if response := request("PUT", "/api/platforms/tsec/config", fmt.Sprintf(`{"base_url":%q,"token":"another-task"}`, upstream.URL)); response.Code != 409 {
		t.Fatalf("active imported project lost its task configuration: %d %s", response.Code, response.Body)
	}
	if err := db.FailCompetitionProject(t.Context(), project.Project.ID, "test ended"); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL.Close(); err != nil {
		t.Fatal(err)
	}
	// Saved Web credentials remain authoritative after a service restart,
	// even if the old environment is still present.
	t.Setenv("BENCHMARK_BASE_URL", "https://old-platform.example")
	t.Setenv("BENCHMARK_TOKEN", "old-environment-secret")
	_, _, restarted := platformWebHarness(t, path)
	response := restarted("GET", "/api/platforms/tsec/config", "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"source":"saved"`) || strings.Contains(response.Body.String(), "secret") {
		t.Fatalf("restart config/redaction: %d %s", response.Code, response.Body)
	}
	if response := restarted("GET", "/api/platforms/tsec/challenges", ""); response.Code != 200 {
		t.Fatalf("saved credentials did not survive restart: %d %s", response.Code, response.Body)
	}
	if response := restarted("PUT", "/api/platforms/tsec/config", fmt.Sprintf(`{"base_url":%q,"token":"new-task"}`, upstream.URL)); response.Code != 200 {
		t.Fatalf("ended task should permit new credentials: %d %s", response.Code, response.Body)
	}
}

func TestCompetitionWebControls(t *testing.T) {
	t.Setenv("HOTMOUSE_AUTH_REQUIRED", "0")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/openapi/v1/challenges":
			fmt.Fprint(w, `[{"unique_code":"e1-01","flag_count":1,"container_status":"stopped","container_addr":[]}]`)
		case "/openapi/v1/challenges/start":
			fmt.Fprint(w, `{"unique_code":"e1-01","container_addr":["10.0.0.1:80"]}`)
		case "/openapi/v1/challenges/close":
			fmt.Fprint(w, `{"unique_code":"e1-01","closed":true}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	t.Setenv("BENCHMARK_BASE_URL", upstream.URL)
	t.Setenv("BENCHMARK_TOKEN", "web-task")
	db, h, request := platformWebHarness(t, filepath.Join(t.TempDir(), "competition.db"))
	body := `{"rules":"only test targets\nkeep original rules","concurrency":1,"max_workers":2,"challenge_timeout_seconds":150}`
	if response := request("POST", "/api/platforms/tsec/runs", body); response.Code != 503 || !strings.Contains(response.Body.String(), "competition_scheduler_disabled") {
		t.Fatalf("disabled coordinator: %d %s", response.Code, response.Body)
	}
	h.Competitions.Backend.Plan = nil
	h.Competitions.Interval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(t.Context())
	done := h.Competitions.Start(ctx)
	t.Cleanup(func() { cancel(); <-done })
	if response := request("GET", "/api/competitions/status", ""); !strings.Contains(response.Body.String(), `"scheduler_enabled":true`) {
		t.Fatalf("coordinator status: %d %s", response.Code, response.Body)
	}
	if response := request("POST", "/api/platforms/tsec/runs", `{"rules":"test","concurrency":4}`); response.Code != 422 {
		t.Fatalf("TSec concurrency cap not enforced: %d %s", response.Code, response.Body)
	}
	response := request("POST", "/api/platforms/tsec/runs", body)
	var run core.CompetitionRun
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &run) != nil || run.MaxWorkers != 2 || run.ChallengeTimeoutSeconds != 150 || run.Rules != "only test targets\nkeep original rules" {
		t.Fatalf("competition create: %d %s", response.Code, response.Body)
	}
	path := "/api/platforms/tsec/runs/" + run.ID
	if response := request("PATCH", path, `{"concurrency":2}`); response.Code != 200 || !strings.Contains(response.Body.String(), `"concurrency":2`) {
		t.Fatalf("competition concurrency: %d %s", response.Code, response.Body)
	}
	if response := request("PATCH", path, `{"concurrency":4}`); response.Code != 422 {
		t.Fatalf("invalid concurrency accepted: %d %s", response.Code, response.Body)
	}
	if response := request("POST", "/api/platforms/tsec/runs", body); response.Code != 409 {
		t.Fatalf("duplicate active competition accepted: %d %s", response.Code, response.Body)
	}
	waitFor := func(status string) core.CompetitionRun {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			current, err := db.GetCompetitionRun(t.Context(), run.ID)
			if err == nil && (status == "running" && current.Items[0].ProjectID != nil || current.Status == status && status != "running") {
				return current
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("competition did not reach %s", status)
		return core.CompetitionRun{}
	}
	current := waitFor("running")
	if response := request("GET", path, ""); response.Code != 200 || !strings.Contains(response.Body.String(), *current.Items[0].ProjectID) {
		t.Fatalf("project progress not exposed: %d %s", response.Code, response.Body)
	}
	if response := request("POST", path+"/stop", `{}`); response.Code != 200 {
		t.Fatalf("competition stop: %d %s", response.Code, response.Body)
	}
	if current := waitFor("stopped"); current.Items[0].Status != "skipped" {
		t.Fatalf("stopped competition did not clean up: %+v", current)
	}
}
