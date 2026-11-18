package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"hotmouse/backend/internal/core"
)

func TestUnattendedTSecRecoversLostAndPendingSubmissions(t *testing.T) {
	var mu sync.Mutex
	accepted := map[string]bool{}
	calls := map[string]int{}
	closed := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/openapi/v1/challenges":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"unique_code": "recover", "flag_count": 2, "correct_flag_count": len(accepted), "is_completed": len(accepted) == 2, "container_status": "available", "container_addr": []string{"127.0.0.1:1337"}}})
		case "/openapi/v1/challenges/start":
			fmt.Fprint(w, `{"unique_code":"recover","container_addr":["127.0.0.1:1337"]}`)
		case "/openapi/v1/challenges/submit":
			var body struct {
				Flag string `json:"flag"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			calls[body.Flag]++
			if accepted[body.Flag] {
				w.WriteHeader(409)
				fmt.Fprint(w, `{"code":"duplicate","message":"already accepted"}`)
				return
			}
			accepted[body.Flag] = true
			if body.Flag == "flag{lost_ack}" {
				connection, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
				} else {
					connection.Close()
				}
				return
			}
			fmt.Fprint(w, `{"correct":true,"correct_flag_count":2,"total_flag_count":2}`)
		case "/openapi/v1/challenges/close":
			closed = true
			fmt.Fprint(w, `{"unique_code":"recover","closed":true}`)
		default:
			t.Errorf("unexpected request: %s", r.URL)
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	t.Setenv("BENCHMARK_BASE_URL", upstream.URL)
	t.Setenv("BENCHMARK_TOKEN", "recovery-task")
	path := filepath.Join(t.TempDir(), "recovery.db")
	db, err := core.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	h := New(db)
	h.Runner.SetLogOutput(io.Discard)
	h.Competitions.Unattended = true
	h.Competitions.Backend.Plan = nil
	_, scope, err := h.platform("tsec")
	if err != nil {
		t.Fatal(err)
	}
	run, err := db.CreateCompetitionRun(t.Context(), "tsec", scope, core.CompetitionConfig{Rules: "local unattended test", ChallengeTimeoutSeconds: 60}, []string{"recover"})
	if err != nil {
		t.Fatal(err)
	}
	h.Competitions.RunOnce(t.Context())
	run, _ = db.GetCompetitionRun(t.Context(), run.ID)
	project := *run.Items[0].ProjectID
	first, err := db.CaptureFlag(t.Context(), project, core.CaptureFlag{ChallengeID: "recover", Value: "flag{lost_ack}", FactID: "origin"})
	if err != nil {
		t.Fatal(err)
	}
	h.Competitions.RunOnce(t.Context())
	flags, _ := db.ListFlags(t.Context(), project)
	if flags[0].Status != "submission_unknown" {
		t.Fatalf("lost acknowledgement was not retained: %+v", flags)
	}
	h.Competitions.RunOnce(t.Context())
	mu.Lock()
	immediateCalls := calls[first.Value]
	mu.Unlock()
	if immediateCalls != 1 {
		t.Fatal("unknown submission was retried without its cooldown")
	}
	h.Competitions.Now = func() time.Time { return time.Now().Add(6 * time.Second) }
	h.Competitions.RunOnce(t.Context())
	flags, _ = db.ListFlags(t.Context(), project)
	if flags[0].Status != "accepted" || len(flags[0].Attempts) != 2 || flags[0].Attempts[0].Status != "submission_unknown" {
		t.Fatalf("duplicate-safe reconciliation failed: %+v", flags)
	}
	second, err := db.CaptureFlag(t.Context(), project, core.CaptureFlag{ChallengeID: "recover", Value: "flag{crashed_request}", FactID: "origin"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.BeginFlagAttempt(t.Context(), project, second.ID); err != nil {
		t.Fatal(err)
	}
	// Simulate a process that crashed after persisting an attempt and before it
	// received a platform response. Reopening must preserve and then recover it.
	h.Runner.StopTargetProbes()
	if err := db.SQL.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = core.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	if err := db.RecoverPendingTSecSubmissions(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	h = New(db)
	h.Runner.SetLogOutput(io.Discard)
	h.Competitions.Unattended = true
	h.Competitions.Now = func() time.Time { return time.Now().Add(6 * time.Second) }
	h.Competitions.RunOnce(t.Context())
	run, _ = db.GetCompetitionRun(t.Context(), run.ID)
	flags, _ = db.ListFlags(t.Context(), project)
	if run.Status != "completed" || flags[1].Status != "accepted" || len(flags[1].Attempts) != 2 || flags[1].Attempts[0].Status != "submission_unknown" {
		t.Fatalf("pending attempt did not recover after reopen: run=%+v flags=%+v", run, flags)
	}
	mu.Lock()
	defer mu.Unlock()
	if !closed || calls[first.Value] != 2 || calls[second.Value] != 1 {
		t.Fatalf("unexpected recovered lifecycle: closed=%v calls=%v", closed, calls)
	}
}
