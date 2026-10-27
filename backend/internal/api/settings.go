package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"hotmouse/backend/internal/domain"
)

var ErrInvalidSettings = errors.New("invalid LLM settings")

const (
	settingsRowID  = 1
	maxTokensLimit = 10_000_000
)

// UpdateSettingsInput mirrors the PUT /settings body. APIKey may be empty for
// local LLM endpoints that do not require authentication.
type UpdateSettingsInput struct {
	LLMURL    string `json:"llm_url"`
	APIKey    string `json:"api_key"`
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
}

// GetSettings returns the stored settings, or an empty configuration when
// nothing has been saved yet. Callers always receive a usable object.
// Find is used instead of First so an unsaved configuration is not logged as
// a missing-record error on every page load.
func (service *Service) GetSettings(ctx context.Context) (domain.Settings, error) {
	var settings domain.Settings
	if err := service.db.WithContext(ctx).Where("id = ?", settingsRowID).Limit(1).Find(&settings).Error; err != nil {
		return domain.Settings{}, err
	}
	if settings.ID == 0 {
		return domain.Settings{ID: settingsRowID}, nil
	}
	return settings, nil
}

// UpdateSettings replaces the singleton row. The API key is stored as given
// because the service has no other way to authenticate with the LLM provider.
func (service *Service) UpdateSettings(ctx context.Context, input UpdateSettingsInput) (domain.Settings, error) {
	input.LLMURL = strings.TrimSpace(input.LLMURL)
	input.APIKey = strings.TrimSpace(input.APIKey)
	input.Model = strings.TrimSpace(input.Model)
	if input.LLMURL == "" || input.Model == "" || input.MaxTokens <= 0 || input.MaxTokens > maxTokensLimit {
		return domain.Settings{}, fmt.Errorf("%w: llm_url and model are required; max_tokens must be between 1 and %d", ErrInvalidSettings, maxTokensLimit)
	}
	settings := domain.Settings{
		ID:        settingsRowID,
		LLMURL:    input.LLMURL,
		APIKey:    input.APIKey,
		Model:     input.Model,
		MaxTokens: input.MaxTokens,
	}
	if err := service.db.WithContext(ctx).Save(&settings).Error; err != nil {
		return domain.Settings{}, err
	}
	return settings, nil
}
