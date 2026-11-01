package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"

	"hotmouse/backend/internal/domain"
	llmlog "hotmouse/backend/internal/logging"
)

const maxEventLineSize = 4 << 20

var ErrInvalidConfiguration = errors.New("LLM settings are incomplete")

type chatRequest struct {
	Model         string         `json:"model"`
	Messages      []chatMessage  `json:"messages"`
	MaxTokens     int            `json:"max_tokens"`
	Stream        bool           `json:"stream"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	Usage *llmlog.TokenUsage `json:"usage"`
}

func Run(ctx context.Context, settings domain.Settings, prompt string, output io.Writer) error {
	return runWithClient(ctx, settings, prompt, output, http.DefaultClient)
}

// RunConfigured runs the configured external worker command. The command
// is tokenized without invoking a shell; {{prompt}} is replaced in command
// arguments, and configured environment values can use worker placeholders.
// The prompt is also provided on stdin for commands that read from stdin.
func RunConfigured(ctx context.Context, settings domain.Settings, prompt string, output io.Writer) error {
	if strings.TrimSpace(settings.WorkerCommand) == "" {
		return errors.New("worker command is required")
	}
	if strings.TrimSpace(prompt) == "" || output == nil {
		return errors.New("worker requires a non-empty prompt and an output writer")
	}
	replacements := workerPlaceholders(settings, prompt)
	args, err := commandArguments(settings.WorkerCommand, prompt)
	if err != nil {
		return fmt.Errorf("invalid worker command: %w", err)
	}
	env, err := workerEnvironment(settings.WorkerEnv, replacements)
	if err != nil {
		return fmt.Errorf("invalid worker environment: %w", err)
	}
	command := exec.CommandContext(ctx, args[0], args[1:]...)
	command.Stdin = strings.NewReader(prompt)
	command.Stdout = output
	command.Stderr = os.Stderr
	command.Env = env
	if err := command.Run(); err != nil {
		return fmt.Errorf("run worker command: %w", err)
	}
	return nil
}

func commandArguments(command, prompt string) ([]string, error) {
	return commandArgumentsWithReplacements(command, map[string]string{"prompt": prompt})
}

func commandArgumentsWithReplacements(command string, replacements map[string]string) ([]string, error) {
	parts, err := shellLikeSplit(command)
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		return nil, errors.New("command is empty")
	}
	for index := range parts {
		parts[index] = replacePlaceholders(parts[index], replacements)
	}
	return parts, nil
}

func workerPlaceholders(settings domain.Settings, prompt string) map[string]string {
	replacements := map[string]string{
		"prompt":                  prompt,
		"llm_url":                 settings.LLMURL,
		"api_key":                 settings.APIKey,
		"model":                   settings.Model,
		"max_tokens":              fmt.Sprint(settings.MaxTokens),
		"worker_command":          settings.WorkerCommand,
		"dispatch_llm_url":        settings.DispatchLLMURL,
		"dispatch_api_key":        settings.DispatchAPIKey,
		"dispatch_model":          settings.DispatchModel,
		"dispatch_max_tokens":     fmt.Sprint(settings.DispatchMaxTokens),
		"max_iterations":          fmt.Sprint(settings.MaxIterations),
		"worker_prompt":           settings.WorkerPrompt,
		"dispatch_plan_prompt":    settings.DispatchPlanPrompt,
		"dispatch_summary_prompt": settings.DispatchSummaryPrompt,
	}
	for name, value := range settings.WorkerTemplateValues {
		replacements[name] = value
	}
	return replacements
}

func replacePlaceholders(value string, replacements map[string]string) string {
	pairs := make([]string, 0, len(replacements)*2)
	for name, replacement := range replacements {
		pairs = append(pairs, "{{"+name+"}}", replacement)
	}
	return strings.NewReplacer(pairs...).Replace(value)
}

func workerEnvironment(config map[string]string, replacements map[string]string) ([]string, error) {
	overrides := make(map[string]string, len(config))
	keys := make([]string, 0, len(config))
	for key, value := range config {
		if key == "" || strings.ContainsAny(key, "=\x00") {
			return nil, fmt.Errorf("environment variable name %q is invalid", key)
		}
		expanded := replacePlaceholders(value, replacements)
		if strings.ContainsRune(expanded, '\x00') {
			return nil, fmt.Errorf("environment variable %q contains a NUL byte", key)
		}
		overrides[key] = expanded
		keys = append(keys, key)
	}
	sort.Strings(keys)

	base := os.Environ()
	result := make([]string, 0, len(base)+len(keys))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if _, overridden := overrides[key]; !overridden {
			result = append(result, entry)
		}
	}
	for _, key := range keys {
		result = append(result, key+"="+overrides[key])
	}
	return result, nil
}

func shellLikeSplit(value string) ([]string, error) {
	var parts []string
	var current strings.Builder
	var quote rune
	escaped := false
	started := false
	flush := func() {
		if started {
			parts = append(parts, current.String())
			current.Reset()
			started = false
		}
	}
	for _, character := range value {
		switch {
		case escaped:
			current.WriteRune(character)
			started = true
			escaped = false
		case character == '\\' && quote != '\'':
			started = true
			escaped = true
		case quote != 0:
			if character == quote {
				quote = 0
			} else {
				current.WriteRune(character)
			}
		case character == '\'' || character == '"':
			started = true
			quote = character
		case character == ' ' || character == '\t' || character == '\n' || character == '\r':
			flush()
		default:
			started = true
			current.WriteRune(character)
		}
	}
	if escaped {
		return nil, errors.New("unterminated escape")
	}
	if quote != 0 {
		return nil, errors.New("unterminated quote")
	}
	flush()
	return parts, nil
}

func runWithClient(ctx context.Context, settings domain.Settings, prompt string, output io.Writer, client *http.Client) error {
	if strings.TrimSpace(prompt) == "" || output == nil || settings.LLMURL == "" || settings.Model == "" || settings.MaxTokens <= 0 {
		return ErrInvalidConfiguration
	}
	if client == nil {
		return errors.New("HTTP client is required")
	}
	endpoint, err := chatCompletionsURL(settings.LLMURL)
	if err != nil {
		return fmt.Errorf("invalid LLM URL: %w", err)
	}

	body, err := json.Marshal(chatRequest{
		Model:         settings.Model,
		Messages:      []chatMessage{{Role: "user", Content: prompt}},
		MaxTokens:     settings.MaxTokens,
		Stream:        true,
		StreamOptions: streamOptionsFor(ctx),
	})
	if err != nil {
		return fmt.Errorf("encode LLM request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create LLM request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	if settings.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+settings.APIKey)
	}
	llmLogger := llmlog.FromContext(ctx)
	var llmCall *llmlog.Call
	llmResponse := llmlog.LLMResponse{URL: endpoint, Model: settings.Model}
	if llmLogger != nil {
		llmCall = llmLogger.Start(ctx, llmlog.LLMRequest{
			Operation: llmlog.OperationFromContext(ctx),
			URL:       endpoint,
			Model:     settings.Model,
			MaxTokens: settings.MaxTokens,
			Prompt:    prompt,
		})
		defer func() { llmCall.Finish(llmResponse) }()
	}

	response, err := client.Do(request)
	if err != nil {
		llmResponse.Error = err.Error()
		return fmt.Errorf("request LLM: %w", err)
	}
	defer response.Body.Close()
	llmResponse.StatusCode = response.StatusCode
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		message, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		if readErr != nil {
			llmResponse.Error = readErr.Error()
			return fmt.Errorf("LLM returned HTTP %d; reading error response: %w", response.StatusCode, readErr)
		}
		llmResponse.Error = strings.TrimSpace(string(message))
		return fmt.Errorf("LLM returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(message)))
	}

	responseContent := new(strings.Builder)
	responseOutput := io.Writer(output)
	if llmLogger != nil {
		responseOutput = io.MultiWriter(responseContent, output)
	}
	usage, err := consumeEventsWithUsage(response.Body, responseOutput)
	if llmLogger != nil {
		llmResponse.Content = responseContent.String()
		if usage != nil {
			llmResponse.PromptTokens = usage.PromptTokens
			llmResponse.CompletionTokens = usage.CompletionTokens
			llmResponse.TotalTokens = usage.TotalTokens
		}
		if err != nil {
			llmResponse.Error = err.Error()
		}
	}
	return err
}

func streamOptionsFor(ctx context.Context) *streamOptions {
	if llmlog.FromContext(ctx) == nil {
		return nil
	}
	return &streamOptions{IncludeUsage: true}
}

func chatCompletionsURL(base string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return "", err
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", errors.New("URL must use http or https and include a host")
	}
	if !strings.HasSuffix(strings.TrimRight(parsed.Path, "/"), "/chat/completions") {
		parsed.Path = strings.TrimRight(parsed.Path, "/") + "/chat/completions"
	}
	return parsed.String(), nil
}

func consumeEvents(source io.Reader, output io.Writer) error {
	_, err := consumeEventsWithUsage(source, output)
	return err
}

func consumeEventsWithUsage(source io.Reader, output io.Writer) (*llmlog.TokenUsage, error) {
	scanner := bufio.NewScanner(source)
	scanner.Buffer(make([]byte, 64<<10), maxEventLineSize)
	var data []string
	var usage *llmlog.TokenUsage
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			done, eventUsage, err := writeEventWithUsage(data, output)
			if eventUsage != nil {
				usage = eventUsage
			}
			data = data[:0]
			if err != nil || done {
				return usage, err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if !found {
			field, value = line, ""
		}
		if field == "data" {
			data = append(data, strings.TrimPrefix(value, " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return usage, fmt.Errorf("read LLM stream: %w", err)
	}
	_, eventUsage, err := writeEventWithUsage(data, output)
	if eventUsage != nil {
		usage = eventUsage
	}
	return usage, err
}

func writeEvent(data []string, output io.Writer) (bool, error) {
	done, _, err := writeEventWithUsage(data, output)
	return done, err
}

func writeEventWithUsage(data []string, output io.Writer) (bool, *llmlog.TokenUsage, error) {
	if len(data) == 0 {
		return false, nil, nil
	}
	payload := strings.Join(data, "\n")
	if payload == "[DONE]" {
		return true, nil, nil
	}
	var chunk chatChunk
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return false, nil, fmt.Errorf("decode LLM stream event: %w", err)
	}
	if chunk.Error != nil {
		return false, chunk.Usage, fmt.Errorf("LLM stream error: %s", chunk.Error.Message)
	}
	for _, choice := range chunk.Choices {
		if choice.Delta.Content == "" {
			continue
		}
		if _, err := io.WriteString(output, choice.Delta.Content); err != nil {
			return false, chunk.Usage, fmt.Errorf("write LLM output: %w", err)
		}
	}
	return false, chunk.Usage, nil
}
