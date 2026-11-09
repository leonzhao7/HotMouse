package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kataras/iris/v12"
	"hotmouse/backend/internal/core"
	"hotmouse/backend/internal/ctfplatform"
)

func TestPlatformImportAndFlagLedger(t *testing.T) {
	t.Setenv("HOTMOUSE_AUTH_REQUIRED", "0")
	var starts, hints, submits atomic.Int32
	var mode atomic.Value
	mode.Store("accepted")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("BENCHMARK_TOKEN") != "task-one" {
			t.Errorf("wrong task header: %q", r.Header.Get("BENCHMARK_TOKEN"))
		}
		switch r.URL.Path {
		case "/openapi/v1/challenges":
			fmt.Fprint(w, `[{"unique_code":"web_01","description":"two services","difficulty":"medium","level":2,"total_score":100,"flag_count":2,"correct_flag_count":0,"is_completed":false,"container_status":"stopped","container_addr":[]}]`)
		case "/openapi/v1/challenges/start":
			starts.Add(1)
			fmt.Fprint(w, `{"unique_code":"web_01","container_addr":["10.0.0.1:8080","10.0.0.2:22"]}`)
		case "/openapi/v1/challenges/hint":
			hints.Add(1)
			fmt.Fprint(w, `{"unique_code":"web_01","hint":"costs score"}`)
		case "/openapi/v1/challenges/submit":
			submits.Add(1)
			var body struct {
				Code string `json:"unique_code"`
				Flag string `json:"flag"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Code != "web_01" {
				t.Errorf("submission: %#v %v", body, err)
			}
			switch mode.Load().(string) {
			case "accepted":
				fmt.Fprint(w, `{"correct":true,"awarded":50,"correct_flag_count":1,"total_flag_count":2,"matched_flag_index":0}`)
			case "duplicate":
				w.WriteHeader(409)
				fmt.Fprint(w, `{"code":"duplicate","message":"already accepted","detail":{}}`)
			case "rejected":
				fmt.Fprint(w, `{"correct":false,"awarded":0,"correct_flag_count":1,"total_flag_count":2,"matched_flag_index":null}`)
			case "unknown":
				fmt.Fprint(w, `{"unexpected":"response"}`)
			case "invalid_state":
				w.WriteHeader(409)
				fmt.Fprint(w, `{"code":"invalid_state","message":"task expired","detail":{}}`)
			}
		case "/openapi/v1/challenges/close":
			fmt.Fprint(w, `{"unique_code":"web_01","closed":true}`)
		default:
			t.Errorf("unexpected upstream request %s", r.URL)
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	t.Setenv("BENCHMARK_BASE_URL", upstream.URL)
	t.Setenv("BENCHMARK_TOKEN", "task-one")
	db, err := core.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	handler := New(db)
	app := iris.New()
	app.Logger().SetLevel("disable")
	Register(app, handler)
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
	for _, body := range []string{`{"challenge_id":"web_01","max_workers":33}`, `{"challenge_id":"web_01","budget_seconds":-1}`, `{"challenge_id":"missing"}`} {
		if response := request("POST", "/api/platforms/tsec/import", body); response.Code < 400 {
			t.Fatalf("invalid import accepted: %s", response.Body)
		}
	}
	if starts.Load() != 0 {
		t.Fatal("invalid local input started an instance")
	}
	imported := request("POST", "/api/platforms/tsec/import", `{"challenge_id":"web_01","max_workers":2,"budget_seconds":300}`)
	if imported.Code != 200 {
		t.Fatalf("import: %d %s", imported.Code, imported.Body)
	}
	var detail core.ProjectDetail
	if err := json.Unmarshal(imported.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	p := detail.Project.ID
	if detail.Platform == nil || detail.Platform.ChallengeID != "web_01" || detail.Project.BudgetSeconds != 300 || detail.Project.MaxWorkers != 2 {
		t.Fatalf("binding: %#v", detail)
	}
	if !strings.Contains(detail.Facts[0].Description, "10.0.0.1:8080") || !strings.Contains(detail.Facts[0].Description, "10.0.0.2:22") {
		t.Fatalf("addresses lost: %+v", detail.Facts)
	}
	if strings.Contains(imported.Body.String(), "task-one") {
		t.Fatal("task token persisted")
	}
	if repeated := request("POST", "/api/platforms/tsec/import", `{"challenge_id":"web_01"}`); repeated.Code != 200 || !strings.Contains(repeated.Body.String(), p) || starts.Load() != 1 {
		t.Fatalf("import not idempotent: %s", repeated.Body)
	}
	if hints.Load() != 0 {
		t.Fatal("import fetched a paid hint")
	}
	if hint := request("GET", "/api/platforms/tsec/hint?challenge_id=web_01", ""); hint.Code != 200 || hints.Load() != 1 {
		t.Fatalf("hint route: %s", hint.Body)
	}
	for _, scenario := range []struct {
		mode, want string
		status     int
	}{
		{"accepted", "accepted", 200}, {"duplicate", "accepted", 200}, {"rejected", "rejected", 200}, {"unknown", "submission_unknown", 502}, {"invalid_state", "rejected", 409},
	} {
		mode.Store(scenario.mode)
		flag, err := db.CaptureFlag(t.Context(), p, core.CaptureFlag{ChallengeID: "web_01", Value: "flag{" + scenario.mode + "}", FactID: "origin"})
		if err != nil {
			t.Fatal(err)
		}
		path := fmt.Sprintf("/projects/%s/flags/%d/submit", p, flag.ID)
		before := submits.Load()
		response := request("POST", path, "")
		if response.Code != scenario.status {
			t.Fatalf("%s status: %d %s", scenario.mode, response.Code, response.Body)
		}
		flags, err := db.ListFlags(t.Context(), p)
		if err != nil {
			t.Fatal(err)
		}
		last := flags[len(flags)-1]
		if last.Status != scenario.want || len(last.Attempts) != 1 || last.Attempts[0].FinishedAt == nil || submits.Load() != before+1 {
			t.Fatalf("%s ledger: %#v", scenario.mode, last)
		}
		if scenario.want == "accepted" || scenario.want == "submission_unknown" {
			if retried := request("POST", path, ""); retried.Code != 409 || submits.Load() != before+1 {
				t.Fatalf("unsafe repeat: %d %s", retried.Code, retried.Body)
			}
		}
	}
	wrong, err := db.CaptureFlag(t.Context(), p, core.CaptureFlag{ChallengeID: "another", Value: "flag{wrong-challenge}", FactID: "origin"})
	if err != nil {
		t.Fatal(err)
	}
	before := submits.Load()
	if response := request("POST", fmt.Sprintf("/projects/%s/flags/%d/submit", p, wrong.ID), ""); response.Code != 422 || submits.Load() != before {
		t.Fatalf("cross challenge submit: %s", response.Body)
	}
	handler.PlatformConfigs["tsec"] = ctfplatform.Config{BaseURL: upstream.URL, Token: "task-two"}
	if response := request("POST", fmt.Sprintf("/projects/%s/flags/%d/submit", p, wrong.ID), ""); response.Code != 409 || !strings.Contains(response.Body.String(), "platform_task_changed") || submits.Load() != before {
		t.Fatalf("cross task submit: %s", response.Body)
	}
	handler.PlatformConfigs["tsec"] = ctfplatform.Config{BaseURL: upstream.URL, Token: "task-one"}
	if response := request("POST", "/api/platforms/tsec/close?challenge_id=web_01", ""); response.Code != 200 || !strings.Contains(response.Body.String(), `"closed":true`) {
		t.Fatalf("close route: %s", response.Body)
	}
}

func TestPlatformConfigurationAndAuthentication(t *testing.T) {
	t.Setenv("BENCHMARK_BASE_URL", "")
	t.Setenv("BENCHMARK_TOKEN", "")
	t.Setenv("HOTMOUSE_AUTH_REQUIRED", "1")
	db, err := core.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	app := iris.New()
	Register(app, New(db))
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	request := func(path string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		app.ServeHTTP(recorder, httptest.NewRequest("GET", path, nil))
		return recorder
	}
	if response := request("/api/platforms"); response.Code != 401 {
		t.Fatalf("unauthenticated platform API: %d", response.Code)
	}
	t.Setenv("HOTMOUSE_AUTH_REQUIRED", "0")
	if response := request("/api/platforms"); response.Code != 200 || !strings.Contains(response.Body.String(), `"configured":false`) {
		t.Fatalf("configuration status: %s", response.Body)
	}
	if response := request("/api/platforms/tsec/challenges"); response.Code != 503 || !strings.Contains(response.Body.String(), "platform_not_configured") {
		t.Fatalf("missing config: %s", response.Body)
	}
	if response := request("/api/platforms/other/challenges"); response.Code != 404 || !strings.Contains(response.Body.String(), "platform_not_found") {
		t.Fatalf("missing adapter: %s", response.Body)
	}
}

type fileTestPlatform struct {
	submit func(context.Context, ctfplatform.Submission) (ctfplatform.SubmissionResult, error)
}

func (*fileTestPlatform) Descriptor() ctfplatform.Descriptor {
	return ctfplatform.Descriptor{ID: "files", Name: "File platform", Authentication: "local", DeliveryMode: "files", SubmissionMode: "api"}
}
func (*fileTestPlatform) ListChallenges(context.Context) ([]ctfplatform.Challenge, error) {
	description := "offline analysis"
	return []ctfplatform.Challenge{{ID: "archive", Title: "Archive", Description: &description, FlagCount: 1}}, nil
}
func (*fileTestPlatform) Deliver(context.Context, string) (ctfplatform.Delivery, error) {
	return ctfplatform.Delivery{ChallengeID: "archive", Mode: "files", Attachments: []ctfplatform.Attachment{{Name: "challenge.zip", Path: "/tmp/challenge.zip"}}}, nil
}
func (p *fileTestPlatform) SubmitFlag(ctx context.Context, submission ctfplatform.Submission) (ctfplatform.SubmissionResult, error) {
	return p.submit(ctx, submission)
}

func TestFilePlatformAndInterruptedLedger(t *testing.T) {
	db, err := core.Open(filepath.Join(t.TempDir(), "files.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	platform := &fileTestPlatform{}
	handler := New(db)
	if err := handler.Platforms.Register(platform.Descriptor(), func(ctfplatform.Config) (ctfplatform.Platform, error) { return platform, nil }); err != nil {
		t.Fatal(err)
	}
	handler.PlatformConfigs["files"] = ctfplatform.Config{Options: map[string]string{"task_id": "archive-task"}}
	project, err := handler.importPlatformChallenge(t.Context(), "files", platformImportRequest{ChallengeID: "archive"})
	if err != nil || project.Platform == nil || !strings.Contains(project.Facts[0].Description, "/tmp/challenge.zip") {
		t.Fatalf("file delivery: %#v %v", project, err)
	}
	flag, err := db.CaptureFlag(t.Context(), project.Project.ID, core.CaptureFlag{Value: "flag{archive}", FactID: "origin"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	platform.submit = func(context.Context, ctfplatform.Submission) (ctfplatform.SubmissionResult, error) {
		cancel()
		return ctfplatform.SubmissionResult{}, context.Canceled
	}
	_, err = handler.submitPlatformFlag(ctx, project.Project.ID, flag.ID)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("interruption error: %v", err)
	}
	flags, err := db.ListFlags(t.Context(), project.Project.ID)
	if err != nil || len(flags) != 1 || flags[0].Status != "submission_unknown" || flags[0].Attempts[0].FinishedAt == nil {
		t.Fatalf("interrupted submission not durable: %#v %v", flags, err)
	}
}

func TestPlatformImportFailureCleanup(t *testing.T) {
	for _, status := range []string{"stopped", "available"} {
		t.Run(status, func(t *testing.T) {
			var starts, closes atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/openapi/v1/challenges":
					fmt.Fprintf(w, `[{"unique_code":"web","flag_count":1,"container_status":%q,"container_addr":["10.0.0.1:80"]}]`, status)
				case "/openapi/v1/challenges/start":
					starts.Add(1)
					fmt.Fprint(w, `{"unique_code":"web","container_addr":["10.0.0.1:80"]}`)
				case "/openapi/v1/challenges/close":
					closes.Add(1)
					fmt.Fprint(w, `{"unique_code":"web","closed":true}`)
				default:
					w.WriteHeader(404)
				}
			}))
			defer upstream.Close()
			db, err := core.Open(filepath.Join(t.TempDir(), "cleanup.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.SQL.Close()
			if _, err := db.SQL.Exec(`CREATE TRIGGER fail_platform_import BEFORE INSERT ON platform_projects BEGIN SELECT RAISE(ABORT,'forced import failure'); END`); err != nil {
				t.Fatal(err)
			}
			handler := New(db)
			handler.PlatformConfigs["tsec"] = ctfplatform.Config{BaseURL: upstream.URL, Token: "task-token"}
			if _, err := handler.importPlatformChallenge(t.Context(), "tsec", platformImportRequest{ChallengeID: "web"}); err == nil {
				t.Fatal("database failure ignored")
			}
			want := int32(0)
			if status == "stopped" {
				want = 1
			}
			if starts.Load() != want || closes.Load() != want {
				t.Fatalf("%s instance starts=%d closes=%d", status, starts.Load(), closes.Load())
			}
			var count int
			if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM projects`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("failed import left project: %d %v", count, err)
			}
		})
	}
}
