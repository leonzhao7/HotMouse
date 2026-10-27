package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"hotmouse/backend/internal/domain"
)

func TestRunSendsPromptAndStreamsChatCompletion(t *testing.T) {
	client := testClient(func(request *http.Request) *http.Response {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/chat/completions" {
			t.Errorf("request = %s %s, want POST /v1/chat/completions", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization = %q", got)
		}
		var payload chatRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if payload.Model != "test-model" || payload.MaxTokens != 128 || !payload.Stream || len(payload.Messages) != 1 || payload.Messages[0].Content != "hello agent" {
			t.Errorf("request payload = %+v", payload)
		}
		return streamResponse(http.StatusOK, fmt.Sprint(
			"data: {\"choices\":[{\"delta\":{\"content\":\"hello \"}}]}\n\n",
			"data: {\"choices\":[{\"delta\":{\"content\":\"world\"}}]}\n\n",
			"data: [DONE]\n\n",
		))
	})

	var output strings.Builder
	err := runWithClient(context.Background(), domain.Settings{
		LLMURL:    "https://example.test/v1",
		APIKey:    "test-key",
		Model:     "test-model",
		MaxTokens: 128,
	}, "hello agent", &output, client)
	if err != nil {
		t.Fatalf("run worker: %v", err)
	}
	if output.String() != "hello world" {
		t.Fatalf("output = %q, want %q", output.String(), "hello world")
	}
}

func TestRunReportsLLMHTTPError(t *testing.T) {
	client := testClient(func(request *http.Request) *http.Response {
		return streamResponse(http.StatusTooManyRequests, `{"error":{"message":"quota exceeded"}}`)
	})
	err := runWithClient(context.Background(), domain.Settings{
		LLMURL:    "https://example.test/v1",
		Model:     "test-model",
		MaxTokens: 128,
	}, "hello", io.Discard, client)
	if err == nil || !strings.Contains(err.Error(), "LLM returned HTTP 429") {
		t.Fatalf("error = %v, want HTTP 429", err)
	}
}

func testClient(roundTrip func(*http.Request) *http.Response) *http.Client {
	return &http.Client{Transport: roundTripFunc(roundTrip)}
}

type roundTripFunc func(*http.Request) *http.Response

func (roundTrip roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request), nil
}

func streamResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestRunRejectsIncompleteConfiguration(t *testing.T) {
	err := Run(context.Background(), domain.Settings{}, "hello", io.Discard)
	if !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("error = %v, want ErrInvalidConfiguration", err)
	}
}

func TestRunConfiguredUsesCommandAndStdin(t *testing.T) {
	t.Setenv("HOTMOUSE_TEST_WORKER_CHILD", "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("get test executable: %v", err)
	}
	commandLine := quoteCommandArgument(executable) + " -test.run=^TestWorkerCommandHelper$"
	var output strings.Builder
	err = RunConfigured(context.Background(), domain.Settings{
		WorkerCommand: commandLine,
	}, "hello from worker", &output)
	if err != nil {
		t.Fatalf("run configured worker: %v", err)
	}
	if output.String() != "hello from worker" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestWorkerCommandHelper(test *testing.T) {
	if os.Getenv("HOTMOUSE_TEST_WORKER_CHILD") != "1" {
		return
	}
	if _, err := io.Copy(os.Stdout, os.Stdin); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func quoteCommandArgument(value string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), `"`, `\"`) + `"`
}

func TestRunConfiguredReplacesPromptPlaceholderWithoutShell(t *testing.T) {
	args, err := commandArguments(`printf '%s' {{prompt}}`, "hello world")
	if err != nil {
		t.Fatalf("parse command: %v", err)
	}
	want := []string{"printf", "%s", "hello world"}
	if fmt.Sprint(args) != fmt.Sprint(want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestRunConfiguredRejectsUnterminatedCommand(t *testing.T) {
	if _, err := commandArguments(`worker "unterminated`, "prompt"); err == nil {
		t.Fatal("accepted unterminated command")
	}
}

func TestShellLikeSplitSupportsQuotedEmptyArguments(t *testing.T) {
	got, err := shellLikeSplit(`worker "" 'text with spaces'`)
	if err != nil {
		t.Fatalf("split command: %v", err)
	}
	want := []string{"worker", "", "text with spaces"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestChatCompletionsURLDoesNotDuplicatePath(t *testing.T) {
	got, err := chatCompletionsURL("https://example.test/v1/chat/completions")
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	if got != "https://example.test/v1/chat/completions" {
		t.Fatalf("URL = %q", got)
	}
}

func TestConsumeEventsSupportsCRLFStreams(t *testing.T) {
	var output strings.Builder
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\r\n\r\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\" second\"}}]}\r\n\r\n"
	if err := consumeEvents(strings.NewReader(stream), &output); err != nil {
		t.Fatalf("consume CRLF stream: %v", err)
	}
	if output.String() != "first second" {
		t.Fatalf("output = %q", output.String())
	}
}
