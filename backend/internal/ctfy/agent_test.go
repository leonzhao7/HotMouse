package ctfy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hotmouse/backend/internal/worker"
)

func taskFixture(t *testing.T, raw string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CTFY_WORK_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "task.json"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDryRunWithoutCredentials(t *testing.T) {
	dir := taskFixture(t, `{"schema_version":3,"dry_run":true}`)
	called := false
	code := Run(context.Background(), func(string) {}, func(string) {}, func(context.Context, worker.HostedAgent) (worker.HostedAgentResult, error) {
		called = true
		return worker.HostedAgentResult{}, nil
	})
	if code != 0 || called {
		t.Fatalf("code=%d called=%t", code, called)
	}
	data, err := os.ReadFile(filepath.Join(dir, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got Result
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != 3 || got.StopReason != "dry_run" {
		t.Fatalf("result=%+v", got)
	}
}

func TestHostedRunUsesCTFyCredentialsAndReplacesRoundResult(t *testing.T) {
	dir := taskFixture(t, `{"schema_version":3,"run_mode":"instance","max_turns":10}`)
	t.Setenv("LLM_FLAVOR", "anthropic")
	t.Setenv("LLM_API_KEY", "run-key")
	t.Setenv("LLM_BASE_URL", "https://gateway.example/v1")
	t.Setenv("LLM_MODEL", "funded-model")
	t.Setenv("CTFY_PLATFORM_URL", "https://ctfy.example")
	t.Setenv("CTFY_TOKEN", "platform-token")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("briefing"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "result.json"), []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	called := false
	code := Run(context.Background(), func(string) {}, func(string) {}, func(_ context.Context, a worker.HostedAgent) (worker.HostedAgentResult, error) {
		called = true
		if a.Driver != "claudecode" || a.Model != "funded-model" || a.BaseURL != "https://gateway.example/v1" || a.MaxTurns != 10 {
			t.Fatalf("agent=%+v", a)
		}
		if a.Environment["ANTHROPIC_AUTH_TOKEN"] != "run-key" || a.Environment["CTFY_TOKEN"] != "platform-token" || a.Environment["OPENAI_API_KEY"] != "" {
			t.Fatal("wrong credential mapping")
		}
		return worker.HostedAgentResult{Response: []byte(`{"schema_version":3,"answers":{"flag":"ctfy{ok}"},"writeup":"run-key platform-token"}`), Usage: worker.AgentUsage{Turns: 2, ToolCallsMade: 3}}, nil
	})
	if code != 0 || !called {
		t.Fatalf("code=%d called=%t", code, called)
	}
	data, err := os.ReadFile(filepath.Join(dir, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "run-key") || strings.Contains(string(data), "platform-token") || strings.Contains(string(data), "stale") {
		t.Fatal("result leaked credentials or stale data")
	}
	var got Result
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Answers["flag"] != "ctfy{ok}" || got.Turns != 2 || got.ToolCallsMade != 3 {
		t.Fatalf("result=%+v", got)
	}
}

func TestFailsClosedWithoutGateway(t *testing.T) {
	taskFixture(t, `{"schema_version":3,"run_mode":"qa"}`)
	t.Setenv("LLM_FLAVOR", "openai")
	t.Setenv("LLM_API_KEY", "funded")
	t.Setenv("LLM_BASE_URL", "")
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("LLM_MODEL", "model")
	t.Setenv("CTFY_PLATFORM_URL", "https://ctfy.example")
	t.Setenv("CTFY_TOKEN", "token")
	called := false
	code := Run(context.Background(), func(string) {}, func(string) {}, func(context.Context, worker.HostedAgent) (worker.HostedAgentResult, error) {
		called = true
		return worker.HostedAgentResult{}, nil
	})
	if code == 0 || called {
		t.Fatalf("code=%d called=%t", code, called)
	}
}

func TestResponseResultRequiresJSON(t *testing.T) {
	if _, err := responseResult([]byte("FLAG{printed}")); err == nil {
		t.Fatal("stdout accepted")
	}
	got, err := responseResult([]byte("```json\n{\"schema_version\":3,\"answers\":{\"flag\":\"ctfy{ok}\"}}\n```"))
	if err != nil || got.Answers["flag"] != "ctfy{ok}" {
		t.Fatalf("result=%+v err=%v", got, err)
	}
}
