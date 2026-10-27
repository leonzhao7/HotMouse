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
	"strings"

	"hotmouse/backend/internal/domain"
)

const maxEventLineSize = 4 << 20

var ErrInvalidConfiguration = errors.New("LLM settings are incomplete")

type chatRequest struct {
	Model     string        `json:"model"`
	Messages  []chatMessage `json:"messages"`
	MaxTokens int           `json:"max_tokens"`
	Stream    bool          `json:"stream"`
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
}

func Run(ctx context.Context, settings domain.Settings, prompt string, output io.Writer) error {
	return runWithClient(ctx, settings, prompt, output, http.DefaultClient)
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
		Model:     settings.Model,
		Messages:  []chatMessage{{Role: "user", Content: prompt}},
		MaxTokens: settings.MaxTokens,
		Stream:    true,
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

	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("request LLM: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		message, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		if readErr != nil {
			return fmt.Errorf("LLM returned HTTP %d; reading error response: %w", response.StatusCode, readErr)
		}
		return fmt.Errorf("LLM returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(message)))
	}
	return consumeEvents(response.Body, output)
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
	scanner := bufio.NewScanner(source)
	scanner.Buffer(make([]byte, 64<<10), maxEventLineSize)
	var data []string
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			done, err := writeEvent(data, output)
			data = data[:0]
			if err != nil || done {
				return err
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
		return fmt.Errorf("read LLM stream: %w", err)
	}
	_, err := writeEvent(data, output)
	return err
}

func writeEvent(data []string, output io.Writer) (bool, error) {
	if len(data) == 0 {
		return false, nil
	}
	payload := strings.Join(data, "\n")
	if payload == "[DONE]" {
		return true, nil
	}
	var chunk chatChunk
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return false, fmt.Errorf("decode LLM stream event: %w", err)
	}
	if chunk.Error != nil {
		return false, fmt.Errorf("LLM stream error: %s", chunk.Error.Message)
	}
	for _, choice := range chunk.Choices {
		if choice.Delta.Content == "" {
			continue
		}
		if _, err := io.WriteString(output, choice.Delta.Content); err != nil {
			return false, fmt.Errorf("write LLM output: %w", err)
		}
	}
	return false, nil
}
