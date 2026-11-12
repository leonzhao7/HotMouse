package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"hotmouse/backend/internal/core"
	"hotmouse/backend/internal/worker"
)

// A real worker process and deterministic coordinator; the platform is local
// and no planning or summary/model invocation is needed to deliver its output.
func TestAutomaticOutputSubmissionCompletesWithoutModelSummary(t *testing.T) {
	var mu sync.Mutex
	accepted, active := 0, false
	submissions := map[string]int{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/openapi/v1/challenges":
			state := "stopped"
			if active {
				state = "available"
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"unique_code": "staged", "description": "Original staged challenge", "flag_count": 2, "correct_flag_count": accepted, "is_completed": accepted == 2, "container_status": state, "container_addr": []string{"10.0.0.1:1337"}}})
		case "/openapi/v1/challenges/start":
			active = true
			fmt.Fprint(w, `{"unique_code":"staged","container_addr":["10.0.0.1:1337"]}`)
		case "/openapi/v1/challenges/submit":
			var body struct {
				Flag string `json:"flag"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			submissions[body.Flag]++
			correct := body.Flag == "flag{first_automatic}" || body.Flag == "flag{second_automatic}"
			if correct {
				accepted++
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"correct": correct, "correct_flag_count": accepted, "total_flag_count": 2})
		case "/openapi/v1/challenges/close":
			active = false
			fmt.Fprint(w, `{"unique_code":"staged","closed":true}`)
		default:
			t.Errorf("unexpected request: %s", r.URL)
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	t.Setenv("BENCHMARK_BASE_URL", upstream.URL)
	t.Setenv("BENCHMARK_TOKEN", "local-fixture-task")
	t.Setenv("HOTMOUSE_PYTHON", "/usr/bin/python3")
	directory := t.TempDir()
	db, err := core.Open(filepath.Join(directory, "automatic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	h := New(db)
	h.Runner.SetLogOutput(io.Discard)
	h.Competitions.Backend.Plan = nil
	_, scope, err := h.platform("tsec")
	if err != nil {
		t.Fatal(err)
	}
	run, err := db.CreateCompetitionRun(t.Context(), "tsec", scope, core.CompetitionConfig{Rules: "local fixture", ChallengeTimeoutSeconds: 60}, []string{"staged"})
	if err != nil {
		t.Fatal(err)
	}
	h.Competitions.RunOnce(t.Context())
	run, _ = db.GetCompetitionRun(t.Context(), run.ID)
	if run.Items[0].ProjectID == nil {
		t.Fatal("challenge was not imported")
	}
	project := *run.Items[0].ProjectID
	if !strings.Contains(run.Items[0].InitialDescription, "Original staged challenge") || !strings.Contains(run.Items[0].InitialDescription, "10.0.0.1:1337") {
		t.Fatal("initial description omitted task or target")
	}
	probe, _ := db.TargetProbe(t.Context(), project)
	if err := db.SaveTargetProbeAnalysis(t.Context(), project, probe.RunID, core.TargetProbeAnalysis{Reason: "local fixture"}); err != nil {
		t.Fatal(err)
	}
	_, err = db.CreateStep(t.Context(), project, core.CreateStep{From: []string{"origin"}, Description: "emit staged results", Creator: "test"})
	if err != nil {
		t.Fatal(err)
	}
	command := `printf '%s\n' "$HOTMOUSE_TASK_TYPE" >> "$AUTO_CALLS"
printf 'flag{incorrect_candidate}\n'
while [ ! -f "$AUTO_STAGE_ONE" ]; do sleep .02; done
printf 'flag{first_'
sleep .05
printf 'automatic}\nflag{incorrect_candidate}\n'
while [ ! -f "$AUTO_STAGE_TWO" ]; do sleep .02; done
printf 'flag{second_automatic}' >&2
while :; do sleep .02; done`
	env, _ := json.Marshal(map[string]string{"AUTO_CALLS": filepath.Join(directory, "calls"), "AUTO_STAGE_ONE": filepath.Join(directory, "one"), "AUTO_STAGE_TWO": filepath.Join(directory, "two")})
	if _, err := db.SQL.Exec(`UPDATE worker_config SET driver='command',command=?,environment_json=?,workspace_root=?`, command, string(env), filepath.Join(directory, "workers")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); worker.NewWorker(h.Runner, "local/worker/01").RunOnce(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("fixture worker did not stop")
		}
	}()
	awaitFlags := func(count int) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			flags, err := db.ListFlags(t.Context(), project)
			if err == nil && len(flags) == count {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("expected %d automatic candidates", count)
	}
	awaitFlags(1)
	h.Competitions.RunOnce(t.Context())
	flags, _ := db.ListFlags(t.Context(), project)
	if flags[0].Status != "rejected" {
		t.Fatal("incorrect output was not submitted for deterministic validation")
	}
	_ = os.WriteFile(filepath.Join(directory, "one"), nil, 0o600)
	awaitFlags(2)
	h.Competitions.RunOnce(t.Context())
	run, _ = db.GetCompetitionRun(t.Context(), run.ID)
	if run.Items[0].Status != "running" || !h.Runner.ProjectBusy(project) {
		t.Fatal("partial acceptance stopped the worker")
	}
	_ = os.WriteFile(filepath.Join(directory, "two"), nil, 0o600)
	awaitFlags(3)
	h.Competitions.RunOnce(t.Context())
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("all accepted results did not cancel the running worker")
	}
	h.Competitions.RunOnce(t.Context())
	run, _ = db.GetCompetitionRun(t.Context(), run.ID)
	if run.Items[0].Status != "succeeded" {
		t.Fatalf("output was not sufficient to finish: %+v", run.Items[0])
	}
	calls, _ := os.ReadFile(filepath.Join(directory, "calls"))
	if string(calls) != "execute\n" {
		t.Fatalf("delivery invoked planning/summary: %q", calls)
	}
	mu.Lock()
	defer mu.Unlock()
	if accepted != 2 || len(submissions) != 3 || submissions["flag{incorrect_candidate}"] != 1 {
		t.Fatalf("duplicate/wrong acceptance: %d %+v", accepted, submissions)
	}
}
