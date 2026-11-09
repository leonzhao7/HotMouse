package tsec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hotmouse/backend/internal/ctfplatform"
)

func TestDocumentedLifecycle(t *testing.T) {
	const id = "web/+?&中文"
	const flag = "  flag{exact_value}  "
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("BENCHMARK_TOKEN") != "task-token" || r.Header.Get("Authorization") != "" {
			t.Errorf("incorrect authentication: %v", r.Header)
		}
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/prefix/openapi/v1/challenges":
			if r.Method != "GET" || r.URL.RawQuery != "" {
				t.Errorf("list request: %s", r.URL)
			}
			fmt.Fprint(w, `[{"unique_code":"web/+?&中文","description":null,"difficulty":"hard","level":3,"total_score":100,"flag_count":2,"correct_flag_count":1,"is_completed":false,"container_status":"available","container_addr":["10.0.0.1:8080","10.0.0.2:22"]},{"unique_code":"pending","container_status":"pending","container_addr":["stale:1"]}]`)
		case "/prefix/openapi/v1/challenges/start":
			checkQuery(t, r, "POST", id)
			data, _ := io.ReadAll(r.Body)
			if len(data) != 0 {
				t.Error("start must use query, not body")
			}
			json.NewEncoder(w).Encode(map[string]any{"unique_code": id, "container_addr": []string{"10.0.0.1:8080", "10.0.0.2:22"}})
		case "/prefix/openapi/v1/challenges/hint":
			checkQuery(t, r, "GET", id)
			json.NewEncoder(w).Encode(map[string]any{"unique_code": id, "hint": nil})
		case "/prefix/openapi/v1/challenges/submit":
			if r.Method != "POST" || r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("submit request: %s %s", r.Method, r.URL)
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if len(body) != 2 || body["unique_code"] != id || body["flag"] != flag {
				t.Errorf("submit body: %#v", body)
			}
			fmt.Fprint(w, `{"correct":true,"awarded":50,"cumulative_score":80,"correct_flag_count":2,"total_flag_count":3,"matched_flag_index":0}`)
		case "/prefix/openapi/v1/challenges/close":
			checkQuery(t, r, "POST", id)
			json.NewEncoder(w).Encode(map[string]any{"unique_code": id, "closed": true})
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	client, err := New(ctfplatform.Config{BaseURL: server.URL + "/prefix/", Token: "task-token"})
	if err != nil {
		t.Fatal(err)
	}
	challenges, err := client.ListChallenges(t.Context())
	if err != nil || len(challenges) != 2 {
		t.Fatalf("list: %v %#v", err, challenges)
	}
	if challenges[0].Description != nil || challenges[0].FlagCount != 2 || challenges[0].CorrectFlagCount != 1 || len(challenges[0].Addresses) != 2 || len(challenges[1].Addresses) != 0 {
		t.Fatalf("challenge conversion: %#v", challenges)
	}
	delivery, err := client.Deliver(t.Context(), id)
	if err != nil || delivery.ChallengeID != id || len(delivery.Addresses) != 2 {
		t.Fatalf("delivery: %#v %v", delivery, err)
	}
	hint, err := client.GetHint(t.Context(), id)
	if err != nil || hint.Content != nil {
		t.Fatalf("hint: %#v %v", hint, err)
	}
	result, err := client.SubmitFlag(t.Context(), ctfplatform.Submission{ChallengeID: id, Flag: flag})
	if err != nil || !result.Correct || result.Awarded != 50 || result.MatchedFlagIndex == nil || *result.MatchedFlagIndex != 0 {
		t.Fatalf("submission: %#v %v", result, err)
	}
	closed, err := client.Close(t.Context(), id)
	if err != nil || !closed.Closed {
		t.Fatalf("close: %#v %v", closed, err)
	}
	if len(calls) != 5 {
		t.Fatalf("unexpected requests: %v", calls)
	}
}

func checkQuery(t *testing.T, r *http.Request, method, id string) {
	t.Helper()
	if r.Method != method || len(r.URL.Query()) != 1 || r.URL.Query().Get("unique_code") != id {
		t.Errorf("query request: %s %s", r.Method, r.URL)
	}
}

func TestBusinessAndValidationErrors(t *testing.T) {
	for _, sample := range []struct {
		status     int
		code, body string
	}{
		{404, "task_not_found", `{"code":"task_not_found","message":"missing task","detail":{}}`},
		{404, "challenge_not_found", `{"code":"challenge_not_found","message":"missing challenge","detail":{}}`},
		{409, "invalid_state", `{"code":"invalid_state","message":"expired or at capacity","detail":{}}`},
		{409, "duplicate", `{"code":"duplicate","message":"already accepted","detail":{}}`},
		{503, "resource_unavailable", `{"code":"resource_unavailable","message":"not ready","detail":{}}`},
		{500, "internal_error", `{"code":"internal_error","message":"internal","detail":{}}`},
		{422, "validation_error", `{"detail":[{"loc":["body","flag"],"msg":"too short","type":"string_too_short"}]}`},
	} {
		t.Run(sample.code, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(sample.status)
				fmt.Fprint(w, sample.body)
			}))
			defer server.Close()
			client, _ := New(ctfplatform.Config{BaseURL: server.URL, Token: "token"})
			_, err := client.SubmitFlag(t.Context(), ctfplatform.Submission{ChallengeID: "web", Flag: "wrong"})
			var failure *ctfplatform.Error
			if !errors.As(err, &failure) || failure.HTTPStatus != sample.status || failure.Code != sample.code || !json.Valid(failure.Detail) {
				t.Fatalf("error: %#v", err)
			}
			if calls.Load() != 1 {
				t.Fatal("write automatically retried")
			}
		})
	}
}

func TestFlagValidationAndProtocolFailures(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"correct":false,"matched_flag_index":null}`)
	}))
	defer server.Close()
	client, _ := New(ctfplatform.Config{BaseURL: server.URL, Token: "token"})
	for _, flag := range []string{"", strings.Repeat("中", 4097), string([]byte{0xff})} {
		if _, err := client.SubmitFlag(t.Context(), ctfplatform.Submission{ChallengeID: "web", Flag: flag}); err == nil {
			t.Error("invalid flag accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid submissions reached platform")
	}
	for _, flag := range []string{"x", strings.Repeat("中", 4096)} {
		result, err := client.SubmitFlag(t.Context(), ctfplatform.Submission{ChallengeID: "web", Flag: flag})
		if err != nil || result.Correct || result.MatchedFlagIndex != nil {
			t.Fatalf("wrong answer: %#v %v", result, err)
		}
	}
	for _, body := range []string{`{}`, `null`, `{"correct":"false"}`, `<html>error</html>`, `{"correct":true} trailing`} {
		t.Run(body, func(t *testing.T) {
			bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer bad.Close()
			client, _ := New(ctfplatform.Config{BaseURL: bad.URL, Token: "token"})
			_, err := client.SubmitFlag(t.Context(), ctfplatform.Submission{ChallengeID: "web", Flag: "x"})
			var failure *ctfplatform.Error
			if !errors.As(err, &failure) || failure.Code != "protocol_error" {
				t.Fatalf("bad response accepted: %v", err)
			}
		})
	}
}

func TestCancellationAndRedirect(t *testing.T) {
	var forwarded atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1) }))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client, _ := New(ctfplatform.Config{BaseURL: server.URL, Token: "token"})
	if _, err := client.ListChallenges(t.Context()); err == nil {
		t.Fatal("redirect accepted")
	}
	if forwarded.Load() != 0 {
		t.Fatal("task token forwarded to redirect")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client.ListChallenges(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	// Test the client's own timeout, independent of a caller deadline.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer slow.Close()
	client, _ = New(ctfplatform.Config{BaseURL: slow.URL, Token: "token", HTTPClient: &http.Client{Timeout: 10 * time.Millisecond}})
	if _, err := client.ListChallenges(t.Context()); err == nil {
		t.Fatal("timeout ignored")
	}
}

func TestConfigurationAndCredentialRedaction(t *testing.T) {
	for _, base := range []string{"", "file:///tmp", "https://user:password@example.com", "https://example.com?secret=1", "https://example.com#fragment"} {
		if _, err := New(ctfplatform.Config{BaseURL: base, Token: "token"}); err == nil {
			t.Errorf("accepted invalid base %q", base)
		}
	}
	const token = "task-\"token"
	const flag = "flag{\"secret\"}"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		json.NewEncoder(w).Encode(map[string]any{"message": "invalid " + token + " " + flag, "detail": []any{map[string]any{"input": flag, "token": token}}})
	}))
	defer server.Close()
	client, _ := New(ctfplatform.Config{BaseURL: server.URL, Token: token})
	_, err := client.SubmitFlag(t.Context(), ctfplatform.Submission{ChallengeID: "web", Flag: flag})
	data, _ := json.Marshal(err)
	var fields map[string]any
	json.Unmarshal(data, &fields)
	if strings.Contains(fmt.Sprint(fields), token) || strings.Contains(fmt.Sprint(fields), flag) {
		t.Fatalf("credential leaked: %s", data)
	}
}
