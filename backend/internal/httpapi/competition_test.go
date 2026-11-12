package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"hotmouse/backend/internal/core"
)

func TestCompetitionTSecPendingRecoveryAndQueueProgress(t *testing.T) {
	var mu sync.Mutex
	states := map[string]string{"one": "stopped", "two": "stopped", "three": "stopped"}
	completed := map[string]bool{}
	starts, closes, submits := map[string]int{}, map[string]int{}, map[string]int{}
	listUnavailable := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("BENCHMARK_TOKEN") != "competition-task" {
			t.Error("missing platform authentication")
		}
		id := r.URL.Query().Get("unique_code")
		switch r.URL.Path {
		case "/openapi/v1/challenges":
			if listUnavailable {
				w.WriteHeader(http.StatusServiceUnavailable)
				fmt.Fprint(w, `{"code":"temporarily_unavailable","message":"retry later"}`)
				return
			}
			rows := []map[string]any{}
			for _, code := range []string{"one", "two", "three"} {
				correct := 0
				if completed[code] {
					correct = 1
				}
				address := "10.0.0.1:80"
				if code == "two" && starts[code] > 1 {
					address = "10.0.0.2:8080"
				}
				rows = append(rows, map[string]any{"unique_code": code, "flag_count": 1, "correct_flag_count": correct, "is_completed": completed[code], "container_status": states[code], "container_addr": []string{address}})
			}
			_ = json.NewEncoder(w).Encode(rows)
		case "/openapi/v1/challenges/start":
			starts[id]++
			if id == "one" || id == "two" && starts[id] == 2 {
				states[id] = "pending"
				w.WriteHeader(http.StatusServiceUnavailable)
				fmt.Fprint(w, `{"code":"startup_timeout","message":"instance is still starting"}`)
				return
			}
			states[id] = "available"
			_ = json.NewEncoder(w).Encode(map[string]any{"unique_code": id, "container_addr": []string{"10.0.0.1:80"}})
		case "/openapi/v1/challenges/submit":
			var body struct {
				ID   string `json:"unique_code"`
				Flag string `json:"flag"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Flag != "flag{"+body.ID+"}" {
				t.Errorf("invalid flag submission: %+v %v", body, err)
			}
			submits[body.ID]++
			completed[body.ID] = true
			fmt.Fprint(w, `{"correct":true,"awarded":100,"correct_flag_count":1,"total_flag_count":1,"matched_flag_index":0}`)
		case "/openapi/v1/challenges/close":
			closes[id]++
			states[id] = "stopped"
			_ = json.NewEncoder(w).Encode(map[string]any{"unique_code": id, "closed": true})
		default:
			t.Errorf("unexpected platform request: %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	t.Setenv("BENCHMARK_BASE_URL", upstream.URL)
	t.Setenv("BENCHMARK_TOKEN", "competition-task")
	db, err := core.Open(filepath.Join(t.TempDir(), "competition.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	h := New(db)
	h.Competitions.Backend.Plan = nil
	clock := time.Now().UTC()
	h.Competitions.Now = func() time.Time { return clock }
	_, scope, err := h.platform("tsec")
	if err != nil {
		t.Fatal(err)
	}
	rules := "只攻击比赛下发的靶场，不使用付费提示。"
	run, err := db.CreateCompetitionRun(t.Context(), "tsec", scope, core.CompetitionConfig{Rules: rules, Concurrency: 1, MaxWorkers: 2, ChallengeTimeoutSeconds: 60}, []string{"one", "two", "three"})
	if err != nil {
		t.Fatal(err)
	}
	advance := func() core.CompetitionRun {
		t.Helper()
		h.Competitions.RunOnce(t.Context())
		current, err := db.GetCompetitionRun(t.Context(), run.ID)
		if err != nil {
			t.Fatal(err)
		}
		return current
	}
	if current := advance(); current.Items[0].Status != "starting" || current.Items[0].ProjectID != nil {
		t.Fatalf("uncertain startup should retain its slot: %+v", current.Items)
	}
	for _, state := range []string{"pending", "stop_pending"} {
		mu.Lock()
		states["one"] = state
		mu.Unlock()
		clock = clock.Add(6 * time.Second)
		current := advance()
		if current.Items[0].ProjectID != nil || current.Items[1].Status != "queued" {
			t.Fatalf("%s must wait without opening another challenge: %+v", state, current.Items)
		}
	}
	mu.Lock()
	listUnavailable = true
	mu.Unlock()
	clock = clock.Add(6 * time.Second)
	advance()
	mu.Lock()
	if starts["one"] != 1 || starts["two"] != 0 {
		t.Errorf("startup retried without confirmed availability: %v", starts)
	}
	listUnavailable = false
	states["one"] = "available"
	mu.Unlock()
	current := advance()
	if current.Items[0].Status != "running" || current.Items[0].ProjectID == nil {
		t.Fatalf("available instance not recovered: %+v", current.Items)
	}
	project := *current.Items[0].ProjectID
	detail, err := db.GetProject(t.Context(), project)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Project.MaxWorkers != 2 || detail.Project.BudgetSeconds != 60 || len(detail.Hints) != 1 || !strings.Contains(detail.Hints[0].Content, rules) {
		t.Fatalf("competition configuration not passed to project: %+v", detail)
	}
	capture := func(item core.CompetitionItem) {
		t.Helper()
		if item.ProjectID == nil {
			t.Fatal("missing running project")
		}
		if _, err := db.CaptureFlag(t.Context(), *item.ProjectID, core.CaptureFlag{ChallengeID: item.ChallengeID, Value: "flag{" + item.ChallengeID + "}", FactID: "origin"}); err != nil {
			t.Fatal(err)
		}
	}
	capture(current.Items[0])
	current = advance()
	if current.Items[0].Status != "succeeded" || current.Items[1].Status != "running" {
		t.Fatalf("successful challenge did not advance queue: %+v", current.Items)
	}
	flags, err := db.ListFlags(t.Context(), project)
	if err != nil || len(flags) != 1 || flags[0].Status != "accepted" || len(flags[0].Attempts) != 1 {
		t.Fatalf("automatic submission not recorded: %+v %v", flags, err)
	}
	retryProject := *current.Items[1].ProjectID
	initialDescription := current.Items[1].InitialDescription
	probe, err := db.TargetProbe(t.Context(), retryProject)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveTargetProbeAnalysis(t.Context(), retryProject, probe.RunID, core.TargetProbeAnalysis{Required: true, Reason: "remote challenge", Targets: []core.TargetEndpoint{{Host: "10.0.0.1", Port: 80}}}); err != nil {
		t.Fatal(err)
	}
	if err := db.SavePreprocessing(t.Context(), retryProject, probe.RunID, core.PreprocessingState{Status: "skipped", Reason: "no HTTP URL"}, "", ""); err != nil {
		t.Fatal(err)
	}
	route, err := db.CreateRoute(t.Context(), retryProject, core.CreateRoute{Title: "first attempt"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE routes SET status='shelved',shelved_reason='no progress',invested_ms=60000,no_progress_count=3 WHERE project_id=? AND id=?`, retryProject, route.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.FailCompetitionProject(t.Context(), retryProject, "solver exhausted its attempts"); err != nil {
		t.Fatal(err)
	}
	current = advance()
	if current.Items[1].Status != "failed" || current.Items[2].Status != "running" {
		t.Fatalf("failed challenge did not advance queue: %+v", current.Items)
	}
	capture(current.Items[2])
	current = advance()
	if current.Status != "running" || current.Items[1].Status != "queued" || current.Items[2].Status != "succeeded" {
		t.Fatalf("competition did not queue its unsolved challenge for another round: %+v", current)
	}
	clock = clock.Add(5 * time.Second)
	current = advance()
	if current.Items[1].Status != "starting" || *current.Items[1].ProjectID != retryProject {
		t.Fatalf("uncertain retry lost the original project: %+v", current)
	}
	clock = clock.Add(6 * time.Second)
	advance()
	mu.Lock()
	if starts["two"] != 2 {
		t.Errorf("pending retry was started more than once: %v", starts)
	}
	states["two"] = "available"
	mu.Unlock()
	current = advance()
	if current.Items[1].Status != "running" || *current.Items[1].ProjectID != retryProject || current.Items[1].InitialDescription != initialDescription {
		t.Fatalf("retry did not preserve its project and initial description: %+v", current)
	}
	detail, err = db.GetProject(t.Context(), retryProject)
	if err != nil || detail.Project.Status != "active" || detail.Project.BudgetSpentMilliseconds != 0 || detail.Project.InvestedMilliseconds != 60000 {
		t.Fatalf("retry did not grant a fresh budget while preserving investment: %+v %v", detail.Project, err)
	}
	if detail.TargetProbe.RunID == probe.RunID || detail.TargetProbe.Status != "pending" || detail.TargetProbe.Analysis != nil || detail.TargetProbe.Preprocessing.Status != "pending" {
		t.Fatalf("retry reused stale target health: %+v", detail.TargetProbe)
	}
	if len(detail.Routes) != 1 || detail.Routes[0].Status != "active" || detail.Routes[0].NoProgressCount != 0 || !strings.Contains(string(detail.Platform.Delivery), "10.0.0.2:8080") {
		t.Fatalf("retry did not reopen the route and refresh delivery: %+v", detail)
	}
	sources, err := db.InitialTargetSources(t.Context(), retryProject)
	if err != nil || !strings.Contains(strings.Join(sources, "\n"), "10.0.0.2:8080") || strings.Contains(strings.Join(sources, "\n"), "10.0.0.1:80") {
		t.Fatalf("startup still uses the previous instance: %v %v", sources, err)
	}
	capture(current.Items[1])
	current = advance()
	if current.Status != "completed" || current.Items[1].Status != "succeeded" {
		t.Fatalf("successful retry did not complete the competition: %+v", current)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, id := range []string{"one", "two", "three"} {
		want := 1
		if id == "two" {
			want = 2
		}
		if starts[id] != want || closes[id] != want {
			t.Errorf("challenge %s lifecycle: starts=%d closes=%d", id, starts[id], closes[id])
		}
	}
	if submits["one"] != 1 || submits["two"] != 1 || submits["three"] != 1 {
		t.Errorf("unexpected submissions: %v", submits)
	}
}
