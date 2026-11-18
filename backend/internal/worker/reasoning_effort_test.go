package worker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPiReasoningEffortAppliesToAllPhasesAndHostedAgents(t *testing.T) {
	t.Setenv("HOTMOUSE_LLM_REASONING_EFFORT", "high")
	for _, phase := range []string{"execute", "decide", "target_probe", "summary", "hosted"} {
		t.Run(phase, func(t *testing.T) {
			dir := t.TempDir()
			env := map[string]string{"PI_MODEL": "fixture", "PI_BASE_URL": "https://gateway.example/v1", "PI_API_KEY": "fixture-key", "PI_PROVIDER_API": "openai-completions"}
			var args []string
			var err error
			if phase == "hosted" {
				args, err = hostedArgv(HostedAgent{Driver: "pi", Model: "fixture", BaseURL: env["PI_BASE_URL"], API: env["PI_PROVIDER_API"], WorkDir: dir, Environment: map[string]string{"LLM_API_KEY": "fixture-key"}}, "fixture")
			} else {
				args, err = providerArgv(workerConfig{driver: "pi", env: env}, phase, "", dir, "fixture")
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(strings.Join(args, " "), "--thinking high") {
				t.Fatalf("reasoning effort did not reach %s: %v", phase, args)
			}
		})
	}
	cfg := workerConfig{driver: "pi", env: map[string]string{"HOTMOUSE_LLM_REASONING_EFFORT": "invalid"}}
	if _, err := providerArgv(cfg, "execute", "", t.TempDir(), "fixture"); err == nil {
		t.Fatal("invalid per-worker effort was accepted")
	}
}

func TestPiReasoningEffortRequestsAndRemoval(t *testing.T) {
	cli, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("installed Pi CLI required for HTTP request verification")
	}
	dir := t.TempDir()
	type request struct {
		path string
		body map[string]json.RawMessage
	}
	requests := make(chan request, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("invalid request body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests <- request{path: r.URL.Path, body: body}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"model\":\"fixture-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"model\":\"fixture-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	cfg := workerConfig{driver: "pi", env: map[string]string{"PI_CLI": cli, "PI_MODEL": "fixture-model", "PI_BASE_URL": server.URL + "/v1", "PI_API_KEY": "local-fixture", "PI_PROVIDER_API": "openai-completions"}}
	for _, effort := range []string{"", "low", "medium", "high", ""} {
		// Reuse one workspace to catch stale reasoning configuration after removal.
		t.Setenv("HOTMOUSE_LLM_REASONING_EFFORT", effort)
		args, err := providerArgv(cfg, "decide", "", dir, "Reply ok")
		if err != nil {
			t.Fatal(err)
		}
		result := runProcess(t.Context(), args, cfg.env, dir, map[string]string{"PI_CODING_AGENT_DIR": filepath.Join(dir, ".hotmouse", "pi-agent")}, 20*time.Second, nil)
		if result.err != nil {
			t.Fatalf("effort %q: %v: %s", effort, result.err, result.stderr)
		}
		select {
		case got := <-requests:
			if got.path != "/v1/chat/completions" {
				t.Fatalf("wrong endpoint: %s", got.path)
			}
			value, present := got.body["reasoning_effort"]
			if effort == "" && present || effort != "" && string(value) != fmt.Sprintf("%q", effort) {
				t.Fatalf("effort %q sent reasoning_effort=%s (present=%v)", effort, value, present)
			}
		default:
			t.Fatalf("effort %q did not produce a request", effort)
		}
	}
}

func TestPiReasoningEffortIdentityAndExplicitOverride(t *testing.T) {
	t.Setenv("HOTMOUSE_LLM_REASONING_EFFORT", "")
	cfg := workerConfig{driver: "pi", env: map[string]string{}}
	before := providerIdentity(cfg, "worker", "workspace")
	t.Setenv("HOTMOUSE_LLM_REASONING_EFFORT", "high")
	if providerIdentity(cfg, "worker", "workspace") == before {
		t.Fatal("reasoning change did not invalidate the saved provider identity")
	}
	cfg.env["HOTMOUSE_LLM_REASONING_EFFORT"] = ""
	if providerIdentity(cfg, "worker", "workspace") != before {
		t.Fatal("explicit empty effort did not override the process environment")
	}
	args, err := providerArgv(cfg, "decide", "", t.TempDir(), "fixture")
	if err != nil || strings.Contains(strings.Join(args, " "), "--thinking") {
		t.Fatalf("explicit empty effort enabled thinking: %v %v", args, err)
	}
}
