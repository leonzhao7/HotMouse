package logging

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDeploymentDiagnosticsRedactCredentials(t *testing.T) {
	t.Setenv("HOTMOUSE_LOG_LEVEL", "debug")
	t.Setenv("HOTMOUSE_LOG_ENV", "1")
	credential := "funded-key-\"quoted\""
	environment := MergeEnvironment([]string{
		"OPENAI_API_KEY=personal-key", "LLM_MODEL=old-model", "PATH=/usr/bin",
	}, map[string]string{
		"LLM_API_KEY": credential, "OPENAI_API_KEY": "", "LLM_MODEL": "funded-model",
		"HTTP_PROXY":             "http://proxy-user:proxy-password@proxy.example:3128",
		"DB_PASS":                "database-password-fixture",
		"HOTMOUSE_AUTH_REQUIRED": "1", "HOTMOUSE_WORKER_PROMPT": "private task briefing",
	})
	var output bytes.Buffer
	logger := New(&output, "test").WithEnvironment(environment)
	logger.Environment("process", environment)
	logger.Info("provider.failure", "text", "gateway echoed "+credential,
		"url", "https://gateway.example/v1?token=query-secret&mode=responses",
		"settings", map[string]any{"apiKey": "nested-secret", "input_tokens": 17, "model": "funded-model"})
	logger.Debug("provider.output", "text", `{"custom_api_key":"stderr-secret","input_tokens":11} Bearer unknown-bearer-secret`)
	logger.Info("server.configuration", "auth_policy", "disabled")
	encoded, _ := json.Marshal(credential)
	text := output.String()
	for _, secret := range []string{credential, string(encoded[1 : len(encoded)-1]), "personal-key", "proxy-user", "proxy-password", "query-secret", "nested-secret", "stderr-secret", "unknown-bearer-secret", "private task briefing", "database-password-fixture"} {
		if strings.Contains(text, secret) {
			t.Fatalf("terminal diagnostics exposed %q", secret)
		}
	}
	for _, expected := range []string{"funded-model", "proxy.example:3128", "gateway.example/v1", "name=OPENAI_API_KEY state=empty", "name=HOTMOUSE_AUTH_REQUIRED state=set value=1", "task content:", "input_tokens", "17", "[redacted]", "auth_policy=disabled"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("deployment diagnostics omitted %q", expected)
		}
	}
}

func TestLoggingLevelAndEnvironmentOptOut(t *testing.T) {
	t.Setenv("HOTMOUSE_LOG_LEVEL", "info")
	t.Setenv("HOTMOUSE_LOG_ENV", "0")
	var output bytes.Buffer
	logger := New(&output, "test")
	logger.Environment("runtime", []string{"VISIBLE_SETTING=environment-disabled"})
	logger.Debug("debug-disabled")
	logger.Info("startup-visible")
	if strings.Contains(output.String(), "environment-disabled") || strings.Contains(output.String(), "debug-disabled") || !strings.Contains(output.String(), "startup-visible") {
		t.Fatal("log level or environment opt-out was ignored")
	}
}

func TestBoundedDiagnosticsKeepUTF8AndRedactBeforeTruncation(t *testing.T) {
	var output bytes.Buffer
	logger := New(&output, "test").WithEnvironment([]string{"API_KEY=tail-credential"})
	logger.Info("large-output", "text", strings.Repeat("测", 2000)+" tail-credential")
	if !utf8.Valid(output.Bytes()) || !strings.Contains(output.String(), "[truncated]") || strings.Contains(output.String(), "tail-credential") || output.Len() > textLimit+512 {
		t.Fatal("large diagnostic was invalid, unbounded, or exposed a credential")
	}
}
