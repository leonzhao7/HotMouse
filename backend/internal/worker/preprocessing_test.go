package worker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hotmouse/backend/internal/core"
)

func TestWebPreprocessingPreservesResponsesWhileExecutionProceeds(t *testing.T) {
	r, project := startupWorker(t)
	var requests atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	largeBody := strings.Repeat("homepage evidence\n", 100000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests.Add(1)
		if req.URL.Path == "/entry" {
			if req.URL.RawQuery != "challenge=1" {
				t.Errorf("initial query was lost: %s", req.URL)
			}
			close(started)
			select {
			case <-release:
			case <-req.Context().Done():
				return
			}
			w.Header().Add("Set-Cookie", "session=fixture; Path=/")
			w.Header().Add("Set-Cookie", "other=preserved; Path=/")
			w.Header().Set("Location", "/homepage")
			w.WriteHeader(http.StatusFound)
			_, _ = w.Write([]byte("redirect body"))
			return
		}
		if cookie, err := req.Cookie("session"); err != nil || cookie.Value != "fixture" {
			t.Error("redirect cookies were lost")
		}
		w.Header().Set("X-Challenge", "complete-headers")
		w.Header().Set("Trailer", "X-Evidence-Trailer")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(largeBody))
		w.Header().Set("X-Evidence-Trailer", "complete-trailers")
	}))
	defer server.Close()
	if _, err := r.DB.SQL.Exec(`UPDATE facts SET description=? WHERE project_id=? AND id='origin'`, "Web CTF target ["+server.URL+"/entry?challenge=1]. Platform https://scoreboard.example/", project); err != nil {
		t.Fatal(err)
	}
	state, _ := r.DB.TargetProbe(t.Context(), project)
	if err := r.DB.SaveTargetProbeAnalysis(t.Context(), project, state.RunID, core.TargetProbeAnalysis{Required: true, Reason: "web CTF", Targets: core.TargetProbeURLCandidates(server.URL)}); err != nil {
		t.Fatal(err)
	}
	r.probeDial = func(context.Context, core.TargetEndpoint) error { return nil }
	step := workerStep(t, r, project, "precreated execution")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r.startTargetStartup(ctx, project)
	r.probeMu.Lock()
	done := r.targetStartups[project].done
	r.probeMu.Unlock()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("preprocessing did not start")
	}
	NewAgentWorker(r, "local/worker/01").RunOnce(t.Context())
	before, _ := stepSnapshot(t, r, project, step.ID)
	if !before.LLMResponseRecorded {
		t.Fatal("slow background preprocessing blocked execution")
	}
	pending, _ := r.DB.TargetProbe(t.Context(), project)
	if pending.Preprocessing.Status != "pending" {
		t.Fatal("preprocessing was not concurrent with execution")
	}
	close(release)
	awaitTask(t, done)
	state, err := r.DB.TargetProbe(t.Context(), project)
	if err != nil || state.Preprocessing.Status != "ready" || state.Preprocessing.FactID == "" || len(state.Preprocessing.Pages) != 1 {
		t.Fatalf("preprocessing evidence missing: %+v err=%v", state, err)
	}
	payload, err := os.ReadFile(state.Preprocessing.Pages[0].ResponsePath)
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Responses []homepageResponse `json:"responses"`
	}
	if err := json.Unmarshal(payload, &record); err != nil {
		t.Fatal(err)
	}
	if len(record.Responses) != 2 {
		t.Fatalf("redirect response lost: %s", payload)
	}
	first, last := record.Responses[0], record.Responses[1]
	if len(first.Headers.Values("Set-Cookie")) != 2 || first.Body != "redirect body" {
		t.Fatalf("initial response incomplete: %+v", first)
	}
	if !last.Complete || last.StatusCode != 403 || last.Body != largeBody || last.Headers.Get("X-Challenge") != "complete-headers" || last.Trailers.Get("X-Evidence-Trailer") != "complete-trailers" {
		t.Fatal("full homepage body, headers, trailers or error status lost")
	}
	raw, err := os.ReadFile(last.RawResponsePath)
	if err != nil || !bytes.Contains(raw, []byte("X-Challenge: complete-headers")) || !bytes.Contains(raw, []byte("homepage evidence")) {
		t.Fatalf("raw HTTP artifact missing: %v", err)
	}
	graph, err := r.DB.ModelContext(t.Context(), project, "decide", "")
	if err != nil || !strings.Contains(graph, state.Preprocessing.FactID) || !strings.Contains(graph, state.Preprocessing.Pages[0].ResponsePath) || strings.Contains(graph, largeBody) {
		t.Fatal("planner did not receive compact evidence access")
	}
	detail, _ := r.DB.GetProject(t.Context(), project)
	found := false
	for _, fact := range detail.Facts {
		if fact.ID == state.Preprocessing.FactID {
			var full struct {
				Responses [][]homepageResponse `json:"responses"`
			}
			if err := json.Unmarshal([]byte(fact.Detail), &full); err != nil {
				t.Fatal(err)
			}
			found = len(full.Responses) == 1 && len(full.Responses[0]) == 2 && full.Responses[0][1].Body == largeBody
		}
	}
	if !found {
		t.Fatal("full response was not persisted in fact detail")
	}
	if ready, err := r.ensureStartup(t.Context(), project); err != nil || !ready {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatal("preprocessing fetched the homepage again")
	}
}

func TestHomepageURLsPreserveEntryAndSelectChallengeServices(t *testing.T) {
	texts := []string{"Target [http://ctf-31337.example/challenge/index.php?id=2] or https://[::1]:8443/. Platform: https://score.example/"}
	urls := homepageURLs(texts, []core.TargetEndpoint{{Host: "ctf-31337.example", Port: 80}, {Host: "::1", Port: 8443}})
	if len(urls) != 2 || urls[0] != "http://ctf-31337.example/challenge/index.php?id=2" || urls[1] != "https://[::1]:8443/" {
		t.Fatalf("incorrect startup URLs: %v", urls)
	}
}

func TestHomepageBinaryBodyAndConnectionFailure(t *testing.T) {
	body := []byte{0xff, 0x00, 0xfe, 0x42}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
	responses, err := fetchHomepage(t.Context(), server.URL, t.TempDir())
	if err != nil || len(responses) != 1 || responses[0].BodyEncoding != "base64" || responses[0].Body != base64.StdEncoding.EncodeToString(body) {
		t.Fatalf("binary response corrupted: %+v err=%v", responses, err)
	}
	server.Close()
	r, project := startupWorker(t)
	if _, err := r.DB.SQL.Exec(`UPDATE facts SET description=? WHERE project_id=? AND id='origin'`, "web CTF "+server.URL, project); err != nil {
		t.Fatal(err)
	}
	state, _ := r.DB.TargetProbe(t.Context(), project)
	if err := r.DB.SaveTargetProbeAnalysis(t.Context(), project, state.RunID, core.TargetProbeAnalysis{Required: true, Reason: "web CTF", Targets: core.TargetProbeURLCandidates(server.URL)}); err != nil {
		t.Fatal(err)
	}
	r.probeDial = func(context.Context, core.TargetEndpoint) error { return nil }
	if ready, err := r.ensureStartup(t.Context(), project); err != nil || !ready {
		t.Fatalf("fetch failure left startup stuck: %v", err)
	}
	state, _ = r.DB.TargetProbe(t.Context(), project)
	if state.Preprocessing.Pages[0].Error == "" {
		t.Fatal("connection error was hidden")
	}
}

func TestIncompleteHomepageBodyIsRecordedAsFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("partial"))
	}))
	defer server.Close()
	responses, err := fetchHomepage(t.Context(), server.URL, t.TempDir())
	if err == nil || len(responses) != 1 || responses[0].Complete || responses[0].Body != "partial" {
		t.Fatalf("partial body was accepted as a complete response: %+v err=%v", responses, err)
	}
}

func TestCancelledPreprocessingCannotSaveEvidenceAndResumeReusesEvidence(t *testing.T) {
	r, project := startupWorker(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); _, _ = w.Write([]byte("homepage")) }))
	defer server.Close()
	if _, err := r.DB.SQL.Exec(`UPDATE facts SET description=? WHERE project_id=? AND id='origin'`, "web CTF "+server.URL, project); err != nil {
		t.Fatal(err)
	}
	saveAnalysis := func() core.TargetProbeState {
		state, _ := r.DB.TargetProbe(t.Context(), project)
		if err := r.DB.SaveTargetProbeAnalysis(t.Context(), project, state.RunID, core.TargetProbeAnalysis{Required: true, Reason: "web CTF", Targets: core.TargetProbeURLCandidates(server.URL)}); err != nil {
			t.Fatal(err)
		}
		return state
	}
	old := saveAnalysis()
	r.probeDial = func(context.Context, core.TargetEndpoint) error { return nil }
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.ensureStartup(ctx, project); err == nil {
		t.Fatal("cancelled startup succeeded")
	}
	state, _ := r.DB.TargetProbe(t.Context(), project)
	if state.Preprocessing.Status != "pending" {
		t.Fatal("cancellation saved completed preprocessing")
	}
	if _, err := r.ensureStartup(t.Context(), project); err != nil {
		t.Fatal(err)
	}
	old, _ = r.DB.TargetProbe(t.Context(), project)
	if _, err := r.DB.SetProjectStatus(t.Context(), project, "stopped"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.DB.SetProjectStatus(t.Context(), project, "active"); err != nil {
		t.Fatal(err)
	}
	newRun, err := r.DB.TargetProbe(t.Context(), project)
	if err != nil || newRun.Analysis == nil || newRun.Preprocessing.Status != "ready" || newRun.Preprocessing.FactID != old.Preprocessing.FactID {
		t.Fatalf("resume discarded startup evidence: %+v %v", newRun, err)
	}
	if err := r.DB.SavePreprocessing(t.Context(), project, old.RunID, core.PreprocessingState{Status: "skipped"}, "", ""); err == nil {
		t.Fatal("stale preprocessing affected resumed project")
	}
	if _, err := r.ensureStartup(t.Context(), project); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("resume repeated initial preprocessing: %d", requests.Load())
	}
}
