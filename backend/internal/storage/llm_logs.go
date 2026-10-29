package storage

import (
	"context"

	"gorm.io/gorm"

	"hotmouse/backend/internal/domain"
	llmlog "hotmouse/backend/internal/logging"
)

type LLMLogSink struct {
	db *gorm.DB
}

func NewLLMLogSink(db *gorm.DB) *LLMLogSink {
	return &LLMLogSink{db: db}
}

func (sink *LLMLogSink) SaveLLMLog(ctx context.Context, record llmlog.Record) error {
	entry := domain.LLMLog{
		StartedAt:        record.StartedAt,
		CompletedAt:      record.CompletedAt,
		Operation:        record.Request.Operation,
		URL:              record.Request.URL,
		Model:            record.Request.Model,
		MaxTokens:        record.Request.MaxTokens,
		Prompt:           record.Request.Prompt,
		Response:         record.Response.Content,
		StatusCode:       record.Response.StatusCode,
		PromptTokens:     record.Response.PromptTokens,
		CompletionTokens: record.Response.CompletionTokens,
		TotalTokens:      record.Response.TotalTokens,
		Error:            record.Response.Error,
	}
	return sink.db.WithContext(ctx).Create(&entry).Error
}
