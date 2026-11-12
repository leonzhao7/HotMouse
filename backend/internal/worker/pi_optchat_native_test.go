package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hotmouse/backend/internal/core"
)

func TestPiOptchatPreservesToolEnvironmentStreamingLimitsAndSummarySession(t *testing.T) {
	cli, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("installed Pi CLI required")
	}
	plugin, err := installedPiOptchat(workerConfig{})
	if err != nil || plugin == "" {
		t.Skip("installed pi-optchat required")
	}
	home, err := os.MkdirTemp("", "hm-oc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("OPTCHAT_HOME", home)
	t.Setenv("HOTMOUSE_LOG_ENV", "0")
	const invalidSummary = `{"accepted":true,"data":{"description":"saved_native_evidence","result":{"type":"progress","detail":"verified tool output"}}`
	const validResponse = `{"accepted":true,"data":{"description":"saved_native_evidence","result":{"type":"progress","detail":"verified tool output"}}}`
	toolObserved := make(chan string, 1)
	release := make(chan struct{})
	var released atomic.Bool
	releaseAll := func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}
	var issued atomic.Bool
	var repaired atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		defer request.Body.Close()
		data, _ := io.ReadAll(request.Body)
		var payload struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct{ Name string } `json:"function"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Errorf("invalid fixture request: %v", err)
			return
		}
		lastUser, toolOutput := "", ""
		hasPreviousSummary := false
		for _, message := range payload.Messages {
			content := publicOutput(message.Content)
			switch message.Role {
			case "user":
				lastUser = content
			case "tool":
				toolOutput += content
			case "assistant":
				hasPreviousSummary = hasPreviousSummary || content == invalidSummary
			}
		}
		delta := map[string]any{"role": "assistant", "content": validResponse}
		finish := "stop"
		if len(payload.Tools) == 0 {
			// Memory compaction has its own model request, not a worker task.
			delta["content"] = "Verified local fixture evidence."
		} else if strings.Contains(lastUser, "repair_native_summary_serialization") || strings.Contains(lastUser, "first_native_summary") {
			if payload.Model != "dispatcher-fixture" {
				t.Errorf("summary used the owner's model: %s", payload.Model)
			}
			for _, tool := range payload.Tools {
				if tool.Function.Name != "read" && tool.Function.Name != "zoom" && tool.Function.Name != "date" && tool.Function.Name != "search" {
					t.Errorf("summary exposed an execution tool: %s", tool.Function.Name)
				}
			}
			if strings.Contains(lastUser, "repair_native_summary_serialization") {
				if !hasPreviousSummary {
					t.Error("format repair did not resume the previous summary response")
				}
				repaired.Store(true)
			} else {
				delta["content"] = invalidSummary
			}
		} else if strings.Contains(lastUser, "exercise_native_optchat_tool") {
			if payload.Model != "worker-fixture" {
				t.Errorf("execution used the owner's model: %s", payload.Model)
			}
			if issued.CompareAndSwap(false, true) {
				args, _ := json.Marshal(map[string]any{"command": `printf 'TOOL_ENV=%s\nflag{optchat_streamed}\n' "$HOTMOUSE_TEST_TOOL_ENV"; sleep 30`})
				delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "call_native_env", "type": "function", "function": map[string]any{"name": "bash", "arguments": string(args)}}}}
				finish = "tool_calls"
			} else {
				toolObserved <- toolOutput
				select {
				case <-release:
				case <-request.Context().Done():
					return
				}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk, _ := json.Marshal(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "created": 1, "model": payload.Model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}})
		fmt.Fprintf(w, "data: %s\n\n", chunk)
		chunk, _ = json.Marshal(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "created": 1, "model": payload.Model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}))
	defer server.Close()
	defer releaseAll()
	db, err := core.Open(filepath.Join(t.TempDir(), "native.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	r := New(db)
	r.SetLogOutput(os.Stderr)
	defer func() {
		r.optchatMu.Lock()
		owners := make([]*piOptchatOwner, 0, len(r.optchatOwners))
		for _, owner := range r.optchatOwners {
			owners = append(owners, owner)
		}
		r.optchatMu.Unlock()
		r.stopPiOptchatOwners("")
		for _, owner := range owners {
			select {
			case <-owner.done:
			case <-time.After(10 * time.Second):
				t.Error("OptChat memory owner did not stop")
			}
		}
	}()
	detail, err := db.CreateProject(t.Context(), core.CreateProject{Title: "native fixture", Origin: "local fixture", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	project := detail.Project.ID
	step := workerStep(t, r, project, "exercise native tools")
	cfg := workerConfig{driver: "pi", identity: "local/worker/fixture", env: map[string]string{
		"PI_CLI": cli, "PI_OPTCHAT_EXTENSION": plugin, "OPTCHAT_HOME": home, "PI_MODEL": "worker-fixture",
		"PI_BASE_URL": server.URL + "/v1", "PI_API_KEY": "fixture", "PI_PROVIDER_API": "openai", "HOTMOUSE_BASH_TIMEOUT_SECONDS": "1",
	}}
	if _, err := db.HeartbeatStep(t.Context(), project, step.ID, cfg.identity); err != nil {
		t.Fatal(err)
	}
	workspace, err := r.ensureWorkspace(project, cfg, cfg.identity)
	if err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{"HOTMOUSE_PROJECT_ID": project, "HOTMOUSE_PROJECT_DIR": filepath.Dir(filepath.Dir(workspace)),
		"HOTMOUSE_STEP_ID": step.ID, "HOTMOUSE_WORKSPACE_DIR": workspace, "HOTMOUSE_TASK_TYPE": "execute",
		"HOTMOUSE_WORKER_PROMPT": "exercise_native_optchat_tool", "HOTMOUSE_TEST_TOOL_ENV": "worker-private"}
	type result struct {
		session string
		err     error
	}
	completed := make(chan result, 1)
	checkpointed := make(chan string, 1)
	executeCtx, cancelExecute := context.WithCancel(t.Context())
	defer cancelExecute()
	go func() {
		_, session, _, err := r.runProvider(executeCtx, cfg, "execute", "", workspace, vars, 60*time.Second, func(id string) error {
			checkpointed <- id
			return nil
		})
		completed <- result{session: session, err: err}
	}()
	select {
	case output := <-toolObserved:
		if !strings.Contains(output, "TOOL_ENV=worker-private") || !strings.Contains(strings.ToLower(output), "timed out") {
			t.Fatalf("worker environment or bash timeout did not reach the real tool: %s", output)
		}
	case result := <-completed:
		t.Fatalf("Pi finished before real tool output: %v", result.err)
	case <-time.After(45 * time.Second):
		t.Fatal("real Pi tool did not stop at the configured bash timeout")
	}
	session := <-checkpointed
	if session == "" || strings.HasPrefix(session, "optchat:") {
		t.Fatalf("execution was not checkpointed with its native session: %s", session)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		flags, err := db.ListFlags(t.Context(), project)
		if err == nil && len(flags) == 1 && flags[0].Value == "flag{optchat_streamed}" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("real tool flag was lost while the assistant result was still pending: %+v %v", flags, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	releaseAll()
	select {
	case result := <-completed:
		if result.err != nil || result.session != session {
			t.Fatalf("native execution failed: %s %v", result.session, result.err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("native execution did not finish")
	}
	summaryCfg := cfg
	summaryCfg.env = make(map[string]string, len(cfg.env))
	for key, value := range cfg.env {
		summaryCfg.env[key] = value
	}
	summaryCfg.env["PI_MODEL"] = "dispatcher-fixture"
	summaryCfg.identity = "local/dispatcher/" + project
	state, err := r.ensureWorkspace(project, summaryCfg, summaryCfg.identity)
	if err != nil {
		t.Fatal(err)
	}
	vars["HOTMOUSE_WORKSPACE_DIR"] = state
	vars["HOTMOUSE_TASK_TYPE"] = "execute_conclude"
	vars["HOTMOUSE_WORKER_PROMPT"] = "first_native_summary"
	output, _, _, err := r.runProvider(t.Context(), summaryCfg, "summary", session, workspace, vars, 45*time.Second, nil)
	if err != nil || string(output) != invalidSummary || validateProviderExecute(output) == nil {
		t.Fatalf("fixture summary was not retained for format repair: %s %v", output, err)
	}
	vars["HOTMOUSE_TASK_TYPE"] = "execute_conclude_format"
	vars["HOTMOUSE_WORKER_PROMPT"] = "repair_native_summary_serialization"
	output, _, _, err = r.runProvider(t.Context(), summaryCfg, "summary", session, workspace, vars, 45*time.Second, nil)
	if err != nil || validateProviderExecute(output) != nil || !repaired.Load() {
		t.Fatalf("format repair failed to continue its native read-only session: %s %v", output, err)
	}
}
