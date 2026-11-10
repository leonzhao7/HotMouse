package worker

import (
	"bytes"
	"strings"
	"testing"

	"hotmouse/backend/internal/logging"
)

func TestPiConnectionErrorsAndRetriesAreVisibleAtInfoLevel(t *testing.T) {
	t.Setenv("HOTMOUSE_LOG_LEVEL", "info")
	var output bytes.Buffer
	logger := logging.New(&output, "test").WithEnvironment([]string{"PI_API_KEY=fixture-credential"})
	events := []string{
		`{"type":"turn_start"}`,
		`{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"private task text"}]}}`,
		`{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"Connection error. fixture-credential"}}`,
		`{"type":"auto_retry_start","attempt":1,"maxAttempts":3,"delayMs":2000,"errorMessage":"Connection error. fixture-credential"}`,
		`{"type":"auto_retry_end","success":false,"attempt":3,"finalError":"Connection error."}`,
		`{"type":"auto_retry_end","success":true,"attempt":1}`,
		`{"type":"message_end","message":{"role":"assistant","stopReason":"stop","model":"fixture-model","usage":{"input":10,"output":3},"content":[{"type":"text","text":"public progress summary"},{"type":"thinking","thinking":"private reasoning"}]}}`,
	}
	for _, event := range events {
		logProviderEvent(logger, "pi", []byte(event))
	}
	text := output.String()
	for _, expected := range []string{"provider.turn.start", "provider.error", "Connection error.", "provider.retry.start", "attempt=1", "max_attempts=3", "delay_ms=2000", "provider.retry.failed", "provider.retry.completed", "provider.message.completed", "model=fixture-model", "public progress summary"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing %q in diagnostics: %s", expected, text)
		}
	}
	for _, private := range []string{"fixture-credential", "private task text", "private reasoning"} {
		if strings.Contains(text, private) {
			t.Fatalf("diagnostics exposed %q", private)
		}
	}
}

func TestNativeProviderLogsPublicActionsAndResults(t *testing.T) {
	t.Setenv("HOTMOUSE_LOG_LEVEL", "info")
	for _, tc := range []struct {
		driver string
		events []string
	}{
		{"codex", []string{
			`{"type":"item.completed","item":{"id":"1","type":"agent_message","text":"检查入口响应，验证重定向假设 fixture-credential"}}`,
			`{"type":"item.completed","item":{"id":"2","type":"command_execution","command":"curl http://challenge/","aggregated_output":"HTTP 302","exit_code":0}}`,
			`{"type":"item.completed","item":{"id":"3","type":"reasoning","text":"private reasoning"}}`,
		}},
		{"claudecode", []string{
			`{"type":"assistant","message":{"content":[{"type":"text","text":"检查入口响应，验证重定向假设 fixture-credential"},{"type":"thinking","thinking":"private reasoning"},{"type":"tool_use","id":"2","name":"Bash","input":{"command":"curl http://challenge/","api_key":"fixture-tool-key"}}]}}`,
			`{"type":"user","message":{"content":[{"type":"text","text":"private task text"},{"type":"tool_result","tool_use_id":"2","content":"HTTP 302"}]}}`,
		}},
		{"pi", []string{
			`{"type":"message_end","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"检查入口响应，验证重定向假设 fixture-credential"},{"type":"thinking","thinking":"private reasoning"}]}}`,
			`{"type":"tool_execution_start","toolCallId":"2","toolName":"bash","args":{"command":"curl http://challenge/","api_key":"fixture-tool-key"}}`,
			`{"type":"tool_execution_end","toolCallId":"2","toolName":"bash","isError":false,"result":{"content":[{"type":"text","text":"HTTP 302"}]}}`,
		}},
	} {
		t.Run(tc.driver, func(t *testing.T) {
			var output bytes.Buffer
			logger := logging.New(&output, "test").WithEnvironment([]string{"LLM_API_KEY=fixture-credential"})
			for _, event := range tc.events {
				logProviderEvent(logger, tc.driver, []byte(event))
			}
			for _, expected := range []string{"验证重定向假设", "curl http://challenge/", "HTTP 302", "[redacted]"} {
				if !strings.Contains(output.String(), expected) {
					t.Fatalf("missing %q in %s logs: %s", expected, tc.driver, output.String())
				}
			}
			for _, private := range []string{"fixture-credential", "fixture-tool-key", "private reasoning", "private task text"} {
				if strings.Contains(output.String(), private) {
					t.Fatalf("exposed %q in %s logs: %s", private, tc.driver, output.String())
				}
			}
		})
	}
}

func TestPiResponsePreservesConnectionErrorAndAllowsSuccessfulRetry(t *testing.T) {
	failure := `{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"Connection error.","content":[]}}`
	if _, err := providerResponse("pi", []byte(failure)); err == nil || !strings.Contains(err.Error(), "Connection error.") {
		t.Fatalf("lost connection failure: %v", err)
	}
	success := `{"type":"message_end","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"{\"accepted\":true}"}]}}`
	answer, err := providerResponse("pi", []byte(failure+"\n"+success))
	if err != nil || string(answer) != `{"accepted":true}` {
		t.Fatalf("successful retry was rejected: %s %v", answer, err)
	}
}
