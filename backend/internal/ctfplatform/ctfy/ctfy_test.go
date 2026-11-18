package ctfy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"hotmouse/backend/internal/ctfplatform"
	"hotmouse/backend/internal/tpctf"
)

func TestExistingInstancesAreListedAdoptedSubmittedAndClosed(t *testing.T) {
	instances := []tpctf.Instance{
		{ID: "instance-one", ChallengeID: "one", CompetitionID: "event", Status: "ready"},
		{ID: "instance-two", ChallengeID: "two", CompetitionID: "event", Status: "starting"},
		{ID: "other-event", ChallengeID: "three", CompetitionID: "elsewhere", Status: "ready"},
	}
	starts, submits, closes, newCloses := 0, 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer pf_play" {
			t.Errorf("missing participant token")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/competitions":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []tpctf.Competition{{ID: "event", Phase: "running", Registered: true, MaxInstances: 2}}, "total": 1})
		case "GET /api/v1/competitions/event/challenges":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []tpctf.Challenge{{ID: "three", Name: "New challenge", Questions: []tpctf.Question{{ID: "flag"}}}, {ID: "one", Name: "Existing one", Questions: []tpctf.Question{{ID: "flag"}}}, {ID: "two", Name: "Existing two", Questions: []tpctf.Question{{ID: "flag"}}}}, "total": 3})
		case "GET /api/v1/instances":
			if r.URL.Query().Get("competition_id") != "event" {
				t.Errorf("instance query is not scoped to the competition: %s", r.URL)
			}
			listed := append([]tpctf.Instance{}, instances...)
			if starts > 0 && newCloses == 0 {
				listed = append(listed, tpctf.Instance{ID: "new-instance", ChallengeID: "three", CompetitionID: "event", Status: "ready"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": listed, "total": len(listed)})
		case "GET /api/v1/challenges/one":
			_ = json.NewEncoder(w).Encode(tpctf.Challenge{ID: "one", Name: "Existing one", Questions: []tpctf.Question{{ID: "flag"}}})
		case "GET /api/v1/challenges/three":
			_ = json.NewEncoder(w).Encode(tpctf.Challenge{ID: "three", Name: "New challenge", Questions: []tpctf.Question{{ID: "flag"}}})
		case "GET /api/v1/instances/instance-one":
			status := "ready"
			if closes > 0 {
				status = "stopped"
			}
			_ = json.NewEncoder(w).Encode(tpctf.Instance{ID: "instance-one", CompetitionID: "event", ChallengeID: "one", Status: status, URLs: []string{"https://target.example"}, Questions: []tpctf.Question{{ID: "flag", Unlocked: true}}})
		case "POST /api/v1/instances":
			starts++
			_ = json.NewEncoder(w).Encode(tpctf.Instance{ID: "new-instance", ChallengeID: "three", CompetitionID: "event", Status: "starting"})
		case "GET /api/v1/instances/new-instance":
			status := "ready"
			if newCloses > 0 {
				status = "stopped"
			}
			_ = json.NewEncoder(w).Encode(tpctf.Instance{ID: "new-instance", ChallengeID: "three", CompetitionID: "event", Status: status, Questions: []tpctf.Question{{ID: "flag", Unlocked: true}}})
		case "POST /api/v1/submissions":
			submits++
			var answer struct {
				InstanceID string `json:"instance_id"`
				QuestionID string `json:"question_id"`
				Answer     string `json:"answer"`
			}
			if err := json.NewDecoder(r.Body).Decode(&answer); err != nil || answer.InstanceID != "instance-one" || answer.QuestionID != "flag" || answer.Answer != "flag{one}" {
				t.Errorf("wrong answer target: %+v %v", answer, err)
			}
			_ = json.NewEncoder(w).Encode(tpctf.Verdict{Correct: true, FullySolved: true})
		case "DELETE /api/v1/instances/instance-one":
			closes++
			_ = json.NewEncoder(w).Encode(tpctf.Instance{ID: "instance-one", Status: "stopping"})
		case "DELETE /api/v1/instances/new-instance":
			newCloses++
			_ = json.NewEncoder(w).Encode(tpctf.Instance{ID: "new-instance", Status: "stopping"})
		default:
			t.Errorf("unexpected upstream request %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := New(ctfplatform.Config{BaseURL: server.URL + "/api/v1", Token: "pf_play", Options: map[string]string{"competition_id": "event"}})
	if err != nil {
		t.Fatal(err)
	}
	challenges, err := client.ListChallenges(t.Context())
	if err != nil || len(challenges) != 3 || challenges[0].InstanceStatus != "stopped" || challenges[1].InstanceStatus != "ready" || challenges[2].InstanceStatus != "starting" {
		t.Fatalf("active instance mapping: %+v %v", challenges, err)
	}
	if challenges[1].Metadata["instance_id"] != "instance-one" || challenges[0].Metadata["instance_id"] != nil {
		t.Fatalf("instance identity leaked or missing: %+v", challenges)
	}
	delivery, err := client.Deliver(t.Context(), "one")
	if err != nil || delivery.Metadata["instance_id"] != "instance-one" || len(delivery.Addresses) == 0 || starts != 0 {
		t.Fatalf("existing target was not adopted: %+v, starts=%d, err=%v", delivery, starts, err)
	}
	if delivery.Metadata["started_by_hotmouse"] != false {
		t.Fatalf("adopted instance must not be treated as newly started: %+v", delivery.Metadata)
	}
	result, err := client.SubmitDelivery(t.Context(), delivery, ctfplatform.Submission{ChallengeID: "one", QuestionID: "flag", Flag: "flag{one}"})
	if err != nil || !result.Correct || submits != 1 {
		t.Fatalf("existing target submission: %+v, submits=%d, err=%v", result, submits, err)
	}
	closed, err := client.CloseDelivery(t.Context(), delivery)
	if err != nil || !closed.Closed || closes != 1 {
		t.Fatalf("existing target cleanup: %+v, closes=%d, err=%v", closed, closes, err)
	}
	// The next list response may lag behind DELETE; the saved delivery remains
	// authoritative for cleanup and does not require an in-memory client cache.
	if _, err := client.CloseDelivery(t.Context(), delivery); err != nil {
		t.Fatal(err)
	}
	newDelivery, err := client.Deliver(t.Context(), "three")
	if err != nil || starts != 1 || newDelivery.Metadata["instance_id"] != "new-instance" {
		t.Fatalf("new challenge should start through the platform: %+v, starts=%d, err=%v", newDelivery, starts, err)
	}
	if newDelivery.Metadata["started_by_hotmouse"] != true {
		t.Fatalf("new instance must retain cleanup ownership: %+v", newDelivery.Metadata)
	}
	restarted, err := New(ctfplatform.Config{BaseURL: server.URL + "/api/v1", Token: "pf_play", Options: map[string]string{"competition_id": "event"}})
	if err != nil {
		t.Fatal(err)
	}
	closed, err = restarted.Close(t.Context(), "three")
	if err != nil || !closed.Closed || newCloses != 1 {
		t.Fatalf("restart could not find and close the active instance: %+v, closes=%d, err=%v", closed, newCloses, err)
	}
	closed, err = restarted.Close(t.Context(), "missing")
	if err != nil || !closed.Closed {
		t.Fatalf("no-instance cleanup should be complete: %+v, err=%v", closed, err)
	}
}

func TestUnlistedTeamInstancesAreNotReportedAsStopped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/competitions":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []tpctf.Competition{{ID: "event", Phase: "running", Registered: true}}, "total": 1})
		case "GET /api/v1/competitions/event/challenges":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []tpctf.Challenge{{ID: "one", Name: "One"}, {ID: "two", Name: "Two"}}, "total": 2})
		case "GET /api/v1/instances":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []tpctf.Instance{}, "total": 0})
		case "GET /api/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"competition_teams": []map[string]any{{"competition_id": "event", "running_instances": 2}}})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := New(ctfplatform.Config{BaseURL: server.URL + "/api/v1", Token: "pf_play", Options: map[string]string{"competition_id": "event"}})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := client.ListChallengesWithInstanceVisibility(t.Context())
	if err != nil || len(rows) != 2 {
		t.Fatalf("list challenges: %+v %v", rows, err)
	}
	for _, row := range rows {
		if row.InstanceStatus != "unknown" || row.Metadata["instance_visibility"] != "incomplete" || row.Metadata["team_running_instances"] != 2 {
			t.Fatalf("unlisted team instance should leave challenge status unknown: %+v", row)
		}
	}
}
