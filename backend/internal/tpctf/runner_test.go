package tpctf

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hotmouse/backend/internal/logging"
	"hotmouse/backend/internal/worker"
)

func fixtureRunner(t *testing.T, handler http.HandlerFunc) *Runner {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClient(server.URL+"/api/v1", "pf_play")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s, err := loadState(dir, "cid", client.base.String())
	if err != nil {
		t.Fatal(err)
	}
	return &Runner{Client: client, State: s, Log: logging.New(io.Discard, "test"), Config: Config{WorkDir: dir, BaseURL: client.base.String(), CompetitionID: "cid", VPNMode: "external", PollInterval: time.Millisecond, RetryInterval: time.Hour, StartTimeout: time.Second, ChallengeTimeout: time.Second, Agent: worker.HostedAgent{Driver: "pi", Environment: map[string]string{"LLM_API_KEY": "pk_model", "CTFY_TOKEN": "", "CTFY_AUTHOR_TOKEN": ""}}}}
}
func respond(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
func TestCompetitionWaitsSolvesGatedQuestionsAndReleasesBeforeNext(t *testing.T) {
	polls, starts, stops, submits, models := 0, 0, 0, 0, 0
	solved := map[string]bool{}
	active := false
	stopping := false
	r := fixtureRunner(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer pf_play" {
			t.Error("missing authorization")
		}
		switch req.Method + " " + req.URL.Path {
		case "GET /api/v1/competitions":
			polls++
			phase := "running"
			if polls == 1 {
				phase = "upcoming"
			}
			if stops == 2 && !active {
				phase = "past"
			}
			respond(w, map[string]any{"items": []Competition{{ID: "cid", Phase: phase, Registered: true, MaxInstances: 1}}, "total": 1})
		case "GET /api/v1/competitions/cid/challenges":
			respond(w, map[string]any{"items": []Challenge{{ID: "a", Solved: solved["flag"]}, {ID: "b", Solved: solved["bflag"]}}, "total": 2})
		case "GET /api/v1/challenges/a":
			respond(w, Challenge{ID: "a", Solved: solved["flag"], Questions: []Question{{ID: "intro"}, {ID: "flag", Requires: []string{"intro"}}}})
		case "GET /api/v1/challenges/b":
			respond(w, Challenge{ID: "b", Solved: solved["bflag"]})
		case "POST /api/v1/instances":
			if active {
				t.Error("allocated before teardown")
			}
			active = true
			starts++
			respond(w, Instance{ID: map[bool]string{true: "ia", false: "ib"}[starts == 1], Status: "starting"})
		case "GET /api/v1/instances/ia", "GET /api/v1/instances/ib":
			id := strings.TrimPrefix(req.URL.Path, "/api/v1/instances/")
			status := "ready"
			if stopping {
				status = "stopped"
				active = false
				stopping = false
			}
			questions := []Question{{ID: "intro", Unlocked: true, Correct: solved["intro"]}, {ID: "flag", Unlocked: solved["intro"], Correct: solved["flag"]}}
			if id == "ib" {
				questions = []Question{{ID: "bflag", Unlocked: true, Correct: solved["bflag"]}}
			}
			respond(w, Instance{ID: id, Status: status, Questions: questions})
		case "POST /api/v1/submissions":
			var body struct {
				InstanceID string `json:"instance_id"`
				QuestionID string `json:"question_id"`
				Answer     any    `json:"answer"`
			}
			_ = json.NewDecoder(req.Body).Decode(&body)
			if body.QuestionID == "flag" {
				if !solved["intro"] {
					t.Error("submitted locked question")
				}
				array, ok := body.Answer.([]any)
				if !ok || len(array) != 2 {
					t.Error("lost multi-choice answer")
				}
			}
			solved[body.QuestionID] = true
			submits++
			respond(w, Verdict{Correct: true, FullySolved: body.QuestionID != "intro", Ranked: true})
		case "DELETE /api/v1/instances/ia", "DELETE /api/v1/instances/ib":
			stops++
			stopping = true
			respond(w, Instance{Status: "stopping"})
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL)
			http.NotFound(w, req)
		}
	})
	r.RunAgent = func(ctx context.Context, a worker.HostedAgent) (worker.HostedAgentResult, error) {
		models++
		if a.Environment["CTFY_TOKEN"] != "" || a.Environment["CTFY_AUTHOR_TOKEN"] != "" {
			t.Error("participant token in subprocess")
		}
		if _, err := os.ReadFile(filepath.Join(a.WorkDir, "task.json")); err != nil {
			t.Fatal(err)
		}
		answer := `{"answers":{"intro":"answer"}}`
		if models == 2 {
			answer = `{"answers":{"flag":["a","c"]}}`
		}
		if models == 3 {
			answer = `{"answers":{"bflag":"flag{b}"}}`
		}
		return worker.HostedAgentResult{Response: []byte(answer)}, nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := r.Loop(ctx); err != nil {
		t.Fatal(err)
	}
	if starts != 2 || stops != 2 || submits != 3 || models != 3 {
		t.Fatalf("starts=%d stops=%d submits=%d models=%d", starts, stops, submits, models)
	}
}
func TestResumeDoesNotReplayAmbiguousOrExhaustedAnswer(t *testing.T) {
	starts, submits, stops, models := 0, 0, 0, 0
	zero := 0
	r := fixtureRunner(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.Method + " " + req.URL.Path {
		case "GET /api/v1/challenges/a":
			respond(w, Challenge{ID: "a"})
		case "GET /api/v1/instances/saved":
			respond(w, Instance{ID: "saved", Status: "ready", Questions: []Question{{ID: "flag", Unlocked: true}, {ID: "exhausted", Unlocked: true, AttemptsRemaining: &zero}}})
		case "POST /api/v1/instances":
			starts++
			respond(w, Instance{ID: "new"})
		case "POST /api/v1/submissions":
			submits++
			respond(w, Verdict{})
		case "DELETE /api/v1/instances/saved":
			stops++
			respond(w, Instance{Status: "stopping"})
		default:
			t.Errorf("unexpected %s %s", req.Method, req.URL.Path)
		}
	})
	s := r.State.challenge("a")
	s.InstanceID = "saved"
	s.Submitted[digest([]any{"a", "flag", "flag{maybe}"})] = "pending"
	if err := r.State.save(r.Config.WorkDir); err != nil {
		t.Fatal(err)
	}
	restored, err := loadState(r.Config.WorkDir, "cid", r.Config.BaseURL)
	if err != nil {
		t.Fatal(err)
	}
	r.State = restored
	r.RunAgent = func(context.Context, worker.HostedAgent) (worker.HostedAgentResult, error) {
		models++
		return worker.HostedAgentResult{Response: []byte(`{"answers":{"flag":"flag{maybe}","exhausted":"guess"}}`)}, nil
	}
	if err = r.solve(t.Context(), "a"); err != nil {
		t.Fatal(err)
	}
	if starts != 0 || submits != 0 || stops != 1 || models != 1 {
		t.Fatalf("starts=%d submits=%d stops=%d models=%d", starts, submits, stops, models)
	}
}
func TestDownloadAuthenticationAndBackoff(t *testing.T) {
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "" {
			t.Error("token leaked to attachment host")
		}
		_, _ = w.Write([]byte("attachment"))
	}))
	defer external.Close()
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"code":"competition_paused","detail":{"retry_after_s":7}}`))
	}))
	defer platform.Close()
	c, _ := NewClient(platform.URL, "pf_secret")
	if _, err := c.Download(t.Context(), external.URL, 100, false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Download(t.Context(), external.URL, 100, true); err == nil {
		t.Fatal("cross-origin VPN accepted")
	}
	_, err := c.Competitions(t.Context())
	if errorCode(err) != "competition_paused" || retryDelay(err, time.Second) != 7*time.Second {
		t.Fatalf("error=%v delay=%s", err, retryDelay(err, time.Second))
	}
}
func TestVPNRejectsExecutableHooks(t *testing.T) {
	if err := validateVPN([]byte("[Interface]\nPrivateKey = test\nPostUp = echo pwned\n")); err == nil {
		t.Fatal("accepted hook")
	}
	if err := validateVPN([]byte("[Interface]\nAddress = 10.0.0.2/32\nPrivateKey = test\n[Peer]\nPublicKey = peer\nAllowedIPs = 10.0.0.0/24\nEndpoint = vpn.example:123\n")); err != nil {
		t.Fatal(err)
	}
}
