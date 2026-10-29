package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"hotmouse/backend/internal/domain"
)

func TestListLLMLogsReturnsLatest200Interactions(t *testing.T) {
	service := newTestService(t)
	logs := make([]domain.LLMLog, 205)
	for index := range logs {
		logs[index] = domain.LLMLog{
			Operation:        "plan",
			URL:              "https://llm.example/v1/chat/completions",
			Model:            "model-a",
			MaxTokens:        512,
			Prompt:           fmt.Sprintf("request-%d", index),
			Response:         fmt.Sprintf("response-%d", index),
			StatusCode:       http.StatusOK,
			PromptTokens:     index + 1,
			CompletionTokens: index + 2,
			TotalTokens:      index*2 + 3,
		}
	}
	if err := service.db.Create(&logs).Error; err != nil {
		t.Fatalf("seed LLM logs: %v", err)
	}

	app := NewApp(service)
	if err := app.Build(); err != nil {
		t.Fatalf("build Iris app: %v", err)
	}
	response := serveJSON(t, app, http.MethodGet, "/llm-log", "")
	if response.Code != http.StatusOK {
		t.Fatalf("list LLM logs status = %d, body = %s", response.Code, response.Body.String())
	}
	var listed []domain.LLMLog
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode LLM logs: %v", err)
	}
	if len(listed) != 200 {
		t.Fatalf("listed %d LLM logs, want 200", len(listed))
	}
	if listed[0].Prompt != "request-204" || listed[0].Response != "response-204" || listed[0].TotalTokens != 411 {
		t.Fatalf("first log = %+v, want newest interaction", listed[0])
	}
	if listed[len(listed)-1].Prompt != "request-5" {
		t.Fatalf("last prompt = %q, want request-5", listed[len(listed)-1].Prompt)
	}
}
