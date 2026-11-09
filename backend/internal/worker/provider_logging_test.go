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
		`{"type":"message_end","message":{"role":"user","content":"private task text"}}`,
		`{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"Connection error. fixture-credential"}}`,
		`{"type":"auto_retry_start","attempt":1,"maxAttempts":3,"delayMs":2000,"errorMessage":"Connection error. fixture-credential"}`,
		`{"type":"auto_retry_end","success":false,"attempt":3,"finalError":"Connection error."}`,
		`{"type":"auto_retry_end","success":true,"attempt":1}`,
		`{"type":"message_end","message":{"role":"assistant","stopReason":"stop","model":"fixture-model","usage":{"input":10,"output":3},"content":[{"type":"text","text":"private task text"}]}}`,
	}
	for _, event := range events {
		logProviderEvent(logger, "pi", []byte(event))
	}
	text := output.String()
	for _, expected := range []string{"provider.turn.start", "provider.error", "Connection error.", "provider.retry.start", "attempt=1", "max_attempts=3", "delay_ms=2000", "provider.retry.failed", "provider.retry.completed", "provider.message.completed", "model=fixture-model"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing %q in diagnostics: %s", expected, text)
		}
	}
	for _, private := range []string{"fixture-credential", "private task text"} {
		if strings.Contains(text, private) {
			t.Fatalf("diagnostics exposed %q", private)
		}
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
