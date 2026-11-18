package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"hotmouse/backend/internal/core"
)

type noCompetitionInput struct{}

func (noCompetitionInput) Read([]byte) (int, error) { panic("unattended competition requested input") }

func setupUnattendedPi(t *testing.T, output string) string {
	t.Helper()
	directory := t.TempDir()
	for _, name := range []string{
		"HOTMOUSE_COMPETITION_RULES_FILE", "HOTMOUSE_COMPETITION_CONCURRENCY", "HOTMOUSE_COMPETITION_MAX_WORKERS", "HOTMOUSE_COMPETITION_CHALLENGE_TIMEOUT",
		"HOTMOUSE_PROJECT_CONCURRENCY", "HOTMOUSE_PROJECT_MAX_WORKERS", "HOTMOUSE_PROJECT_BUDGET_SECONDS", "HOTMOUSE_WORKER_COUNT",
		"TSEC_NETWORK_CHECK_URL", "TSEC_TARGET_PROBE_ENABLED", "TSEC_RESULT_FILE", "TSEC_RETRY_INTERVAL", "TSEC_SHUTDOWN_TIMEOUT", "TSEC_VPN_MODE",
		"PI_MODEL_CONTEXT_WINDOW", "HOTMOUSE_INTERNAL_TOKEN", "HOTMOUSE_PROJECT_CONTEXT_ROOT",
		"HOTMOUSE_LLM_REASONING_EFFORT",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("HOTMOUSE_WORKER_DRIVER", "pi")
	t.Setenv("HOTMOUSE_LLM_BASE_URL", "http://127.0.0.1:1/v1")
	t.Setenv("HOTMOUSE_LLM_API_KEY", "fixture-model-key")
	t.Setenv("HOTMOUSE_LLM_MODEL", "fixture-model")
	t.Setenv("HOTMOUSE_LLM_API", "openai")
	t.Setenv("HOTMOUSE_WORKER_COUNT", "1")
	t.Setenv("HOTMOUSE_PROJECT_CONCURRENCY", "1")
	t.Setenv("HOTMOUSE_PROJECT_MAX_WORKERS", "1")
	t.Setenv("HOTMOUSE_PYTHON", "/usr/bin/python3")
	t.Setenv("HOTMOUSE_LOG_LEVEL", "error")
	t.Setenv("HOTMOUSE_LOG_ENV", "0")
	t.Setenv("PI_OPTCHAT_EXTENSION", "")
	t.Setenv("HOTMOUSE_AUTH_REQUIRED", "1")
	badConfig := filepath.Join(directory, "invalid-client.yaml")
	if err := os.WriteFile(badConfig, []byte("[not: valid"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOTMOUSE_CONFIG", badConfig)
	writeEvent := func(name string, response any) {
		t.Helper()
		body, _ := json.Marshal(response)
		event, _ := json.Marshal(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "stopReason": "stop", "content": []map[string]string{{"type": "text", "text": string(body)}}}})
		if err := os.WriteFile(filepath.Join(directory, name), append(event, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeEvent("decide.json", map[string]any{"accepted": true, "data": map[string]any{"steps": []map[string]any{{"from": []string{"origin"}, "description": "Solve the local fixture and preserve the target output", "goal_id": "g001"}}}})
	writeEvent("probe.json", map[string]any{"accepted": true, "data": map[string]any{"required": false, "reason": "local fixture needs no background target probe"}})
	toolOutput, _ := json.Marshal(map[string]any{"type": "tool_execution_end", "toolName": "bash", "result": map[string]any{"content": []map[string]string{{"type": "text", "text": output}}}})
	if err := os.WriteFile(filepath.Join(directory, "execute.json"), append(toolOutput, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
fixture_dir="$(dirname -- "$0")"
printf '%s\n' "$HOTMOUSE_TASK_TYPE" >> "$fixture_dir/calls"
case "$HOTMOUSE_TASK_TYPE" in
  decide) cat "$fixture_dir/decide.json" ;;
  target_probe) cat "$fixture_dir/probe.json" ;;
  execute)
    touch "$fixture_dir/executing"
    cat "$fixture_dir/execute.json"
    while :; do sleep 1; done
    ;;
  *) exit 8 ;;
esac
`
	if err := os.WriteFile(filepath.Join(directory, "pi"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PI_CLI", filepath.Join(directory, "pi"))
	return directory
}

// Exercise the local command, real child processes, native provider event
// parsing, flag capture, TSec protocol, startup retries and durable completion.
func TestUnattendedCompetitionNeedsNoServerRulesOrInput(t *testing.T) {
	fixture := setupUnattendedPi(t, "flag{autonomous_wrong}\nflag{autonomous_first}\nflag{autonomous_second}\n")
	var mu sync.Mutex
	listCalls, healthCalls, starts, closes := 0, 0, 0, 0
	active := false
	accepted := map[string]bool{}
	submits := map[string]int{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/vpn-health" {
			healthCalls++
			if r.Header.Get("BENCHMARK_TOKEN") != "" {
				t.Error("task token was sent to the VPN health endpoint")
			}
			if healthCalls == 1 {
				w.WriteHeader(503)
				return
			}
			fmt.Fprint(w, "ready")
			return
		}
		if r.Header.Get("BENCHMARK_TOKEN") != "autonomous-task" {
			t.Error("missing benchmark authentication")
		}
		switch r.URL.Path {
		case "/openapi/v1/challenges":
			listCalls++
			if listCalls == 1 {
				w.WriteHeader(503)
				fmt.Fprint(w, `{"code":"resource_unavailable","message":"temporary outage"}`)
				return
			}
			if listCalls == 2 {
				fmt.Fprint(w, `[]`)
				return
			}
			status := "stopped"
			if active {
				status = "available"
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"unique_code": "autonomous", "description": "Capture both fixture flags", "difficulty": "easy", "flag_count": 2, "correct_flag_count": len(accepted), "is_completed": len(accepted) == 2, "container_status": status, "container_addr": []string{"127.0.0.1:1337"}}})
		case "/openapi/v1/challenges/start":
			if r.Method != http.MethodPost || r.URL.Query().Get("unique_code") != "autonomous" {
				t.Error("invalid start request")
			}
			starts++
			active = true
			fmt.Fprint(w, `{"unique_code":"autonomous","container_addr":["127.0.0.1:1337"]}`)
		case "/openapi/v1/challenges/submit":
			var body struct {
				ID   string `json:"unique_code"`
				Flag string `json:"flag"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID != "autonomous" {
				t.Errorf("invalid submit: %+v %v", body, err)
			}
			submits[body.Flag]++
			if body.Flag == "flag{autonomous_first}" && submits[body.Flag] == 1 {
				w.WriteHeader(503)
				fmt.Fprint(w, `{"code":"resource_unavailable","message":"acknowledgement lost"}`)
				return
			}
			correct := body.Flag == "flag{autonomous_first}" || body.Flag == "flag{autonomous_second}"
			if correct {
				accepted[body.Flag] = true
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"correct": correct, "correct_flag_count": len(accepted), "total_flag_count": 2})
		case "/openapi/v1/challenges/close":
			if r.URL.Query().Get("unique_code") != "autonomous" {
				t.Error("invalid close request")
			}
			closes++
			active = false
			fmt.Fprint(w, `{"unique_code":"autonomous","closed":true}`)
		default:
			t.Errorf("unexpected endpoint (including paid hints): %s", r.URL)
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	t.Setenv("BENCHMARK_BASE_URL", upstream.URL)
	t.Setenv("BENCHMARK_TOKEN", "autonomous-task")
	t.Setenv("TSEC_NETWORK_CHECK_URL", upstream.URL+"/vpn-health")
	path := filepath.Join(t.TempDir(), "tsec.db")
	args := []string{"competition", "run", "tsec", "--db", path, "--retry-interval", "10ms", "--shutdown-timeout", "5s", "--output", "json"}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	var out, errOut bytes.Buffer
	if code := Run(ctx, args, noCompetitionInput{}, &out, &errOut); code != 0 {
		t.Fatalf("unattended command failed: code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	var run core.CompetitionRun
	if err := json.Unmarshal(out.Bytes(), &run); err != nil || run.Status != "completed" || len(run.Items) != 1 || run.Items[0].Status != "succeeded" {
		t.Fatalf("unexpected result: %s %v", out.String(), err)
	}
	db, err := core.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	flags, err := db.ListFlags(t.Context(), *run.Items[0].ProjectID)
	db.SQL.Close()
	if err != nil || len(flags) != 3 {
		t.Fatalf("missing flag ledger: %+v %v", flags, err)
	}
	for _, flag := range flags {
		if flag.Value == "flag{autonomous_first}" && (flag.Status != "accepted" || len(flag.Attempts) != 2 || flag.Attempts[0].Status != "submission_unknown") {
			t.Fatalf("interrupted submission was not recovered: %+v", flag)
		}
		if flag.Value == "flag{autonomous_wrong}" && (flag.Status != "rejected" || len(flag.Attempts) != 1) {
			t.Fatalf("wrong answer replayed: %+v", flag)
		}
	}
	report, err := os.ReadFile(filepath.Join(filepath.Dir(path), "tsec-result.json"))
	if err != nil || !bytes.Contains(report, []byte(`"status": "completed"`)) {
		t.Fatalf("missing durable result: %s %v", report, err)
	}
	calls, _ := os.ReadFile(filepath.Join(fixture, "calls"))
	if !bytes.Contains(calls, []byte("decide\n")) || !bytes.Contains(calls, []byte("execute\n")) {
		t.Fatalf("full solver pipeline did not run: %q", calls)
	}
	if bytes.Contains(calls, []byte("target_probe\n")) {
		t.Fatalf("TSec started target probing despite the disabled default: %q", calls)
	}
	// A completed benchmark must stay completed when its container is restarted.
	out.Reset()
	errOut.Reset()
	if code := Run(ctx, args, noCompetitionInput{}, &out, &errOut); code != 0 {
		t.Fatalf("restart failed: %d %s", code, errOut.String())
	}
	after, _ := os.ReadFile(filepath.Join(fixture, "calls"))
	if !bytes.Equal(calls, after) {
		t.Fatalf("restart replayed solving: %q -> %q", calls, after)
	}
	mu.Lock()
	defer mu.Unlock()
	if starts != 1 || closes != 1 || active || submits["flag{autonomous_first}"] != 2 || submits["flag{autonomous_wrong}"] != 1 || healthCalls < 2 || listCalls < 3 {
		t.Fatalf("incomplete autonomous lifecycle: starts=%d closes=%d active=%v submits=%v health=%d list=%d", starts, closes, active, submits, healthCalls, listCalls)
	}
}

func TestUnattendedCompetitionCancellationDrainsInstances(t *testing.T) {
	fixture := setupUnattendedPi(t, "solver is running without any flags")
	var mu sync.Mutex
	active, closes := false, 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/openapi/v1/challenges":
			status := "stopped"
			if active {
				status = "available"
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"unique_code": "autonomous", "flag_count": 1, "container_status": status, "container_addr": []string{"127.0.0.1:1337"}}})
		case "/openapi/v1/challenges/start":
			active = true
			fmt.Fprint(w, `{"unique_code":"autonomous","container_addr":["127.0.0.1:1337"]}`)
		case "/openapi/v1/challenges/close":
			active = false
			closes++
			fmt.Fprint(w, `{"unique_code":"autonomous","closed":true}`)
		default:
			t.Errorf("unexpected request: %s", r.URL)
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	t.Setenv("BENCHMARK_BASE_URL", upstream.URL)
	t.Setenv("BENCHMARK_TOKEN", "autonomous-task")
	path := filepath.Join(t.TempDir(), "tsec.db")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var out, errOut bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- Run(ctx, []string{"competition", "run", "tsec", "--db", path, "--shutdown-timeout", "5s", "--output", "json"}, noCompetitionInput{}, &out, &errOut)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(fixture, "executing")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("solver did not start: %s", errOut.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case code := <-done:
		if code != 130 {
			t.Fatalf("unexpected cancellation code %d: %s", code, errOut.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("unattended command did not drain after cancellation")
	}
	var run core.CompetitionRun
	if err := json.Unmarshal(out.Bytes(), &run); err != nil || run.Status != "stopped" {
		t.Fatalf("cancelled result: %s %v", out.String(), err)
	}
	mu.Lock()
	defer mu.Unlock()
	if active || closes != 1 {
		t.Fatalf("instance was left running: active=%v closes=%d", active, closes)
	}
}

func TestUnattendedCompetitionRejectsIncompleteConfigurationWithoutInput(t *testing.T) {
	setupUnattendedPi(t, "")
	t.Setenv("BENCHMARK_BASE_URL", "https://benchmark.example")
	t.Setenv("BENCHMARK_TOKEN", "task")
	t.Setenv("HOTMOUSE_LLM_API_KEY", "")
	t.Setenv("PI_API_KEY", "")
	var out, errOut bytes.Buffer
	code := Run(t.Context(), []string{"competition", "run", "tsec", "--db", filepath.Join(t.TempDir(), "tsec.db")}, noCompetitionInput{}, &out, &errOut)
	if code != 2 || !strings.Contains(errOut.String(), "HOTMOUSE_LLM_API_KEY") {
		t.Fatalf("missing configuration did not fail promptly: %d %s", code, errOut.String())
	}
}

func TestUnattendedCompetitionRejectsInvalidTargetProbeSetting(t *testing.T) {
	setupUnattendedPi(t, "")
	t.Setenv("BENCHMARK_BASE_URL", "https://benchmark.example")
	t.Setenv("BENCHMARK_TOKEN", "task")
	t.Setenv("TSEC_TARGET_PROBE_ENABLED", "2")
	path := filepath.Join(t.TempDir(), "tsec.db")
	var out, errOut bytes.Buffer
	code := Run(t.Context(), []string{"competition", "run", "tsec", "--db", path}, noCompetitionInput{}, &out, &errOut)
	if code != 2 || !strings.Contains(errOut.String(), "TSEC_TARGET_PROBE_ENABLED") {
		t.Fatalf("invalid probe setting was not rejected: %d %s", code, errOut.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid probe setting created a database: %v", err)
	}
}
