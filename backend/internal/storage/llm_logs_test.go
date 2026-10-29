package storage

import (
	"context"
	"testing"
	"time"

	"hotmouse/backend/internal/domain"
	llmlog "hotmouse/backend/internal/logging"
)

func TestLLMLogSinkPersistsOneCompleteInteraction(t *testing.T) {
	db := openTestDatabase(t, ":memory:")
	sink := NewLLMLogSink(db)
	startedAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	completedAt := startedAt.Add(2 * time.Second)
	record := llmlog.Record{
		StartedAt: startedAt,
		Request: llmlog.LLMRequest{
			Operation: "plan",
			URL:       "https://llm.example/v1/chat/completions",
			Model:     "model-a",
			MaxTokens: 512,
			Prompt:    "choose an action",
		},
		Response: llmlog.LLMResponse{
			URL:              "https://llm.example/v1/chat/completions",
			Model:            "model-a",
			StatusCode:       200,
			PromptTokens:     31,
			CompletionTokens: 12,
			TotalTokens:      43,
			Content:          `{"action":"continue"}`,
		},
		CompletedAt: completedAt,
	}
	if err := sink.SaveLLMLog(context.Background(), record); err != nil {
		t.Fatalf("save LLM log: %v", err)
	}

	var count int64
	if err := db.Model(&domain.LLMLog{}).Count(&count).Error; err != nil {
		t.Fatalf("count LLM logs: %v", err)
	}
	if count != 1 {
		t.Fatalf("stored %d rows, want one request/response row", count)
	}

	var stored domain.LLMLog
	if err := db.First(&stored).Error; err != nil {
		t.Fatalf("read LLM log: %v", err)
	}
	if stored.StartedAt != startedAt || stored.CompletedAt != completedAt || stored.Operation != "plan" ||
		stored.URL != record.Request.URL || stored.Model != "model-a" || stored.MaxTokens != 512 ||
		stored.Prompt != "choose an action" || stored.Response != record.Response.Content || stored.StatusCode != 200 ||
		stored.PromptTokens != 31 || stored.CompletionTokens != 12 || stored.TotalTokens != 43 {
		t.Fatalf("stored interaction does not match request and response: %+v", stored)
	}
}
