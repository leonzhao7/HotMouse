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
	settingsRowID      = 1
	maxTokensLimit     = 10_000_000
	maxIterationsLimit = 100
	defaultIterations  = domain.DefaultMaxIterations
)

// UpdateSettingsInput mirrors the PUT /settings body. APIKey may be empty for
// local LLM endpoints that do not require authentication.
type UpdateSettingsInput struct {
	LLMURL    string `json:"llm_url"`
	APIKey    string `json:"api_key"`
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`

	WorkerLLMURL    string `json:"worker_llm_url"`
	WorkerAPIKey    string `json:"worker_api_key"`
	WorkerModel     string `json:"worker_model"`
	WorkerMaxTokens int    `json:"worker_max_tokens"`
	WorkerCommand   string `json:"worker_command"`

	DispatchLLMURL    string `json:"dispatch_llm_url"`
	DispatchAPIKey    string `json:"dispatch_api_key"`
	DispatchModel     string `json:"dispatch_model"`
	DispatchMaxTokens int    `json:"dispatch_max_tokens"`
	MaxIterations     int    `json:"max_iterations"`

	WorkerPrompt          string `json:"worker_prompt"`
	DispatchPlanPrompt    string `json:"dispatch_plan_prompt"`
	DispatchSummaryPrompt string `json:"dispatch_summary_prompt"`
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
		settings.ID = settingsRowID
	}
	return withSettingsDefaults(settings), nil
}

// UpdateSettings replaces the singleton row. API keys are stored as given
// because the service has no other way to authenticate with the LLM provider.
func (service *Service) UpdateSettings(ctx context.Context, input UpdateSettingsInput) (domain.Settings, error) {
	input.LLMURL = strings.TrimSpace(input.LLMURL)
	input.APIKey = strings.TrimSpace(input.APIKey)
	input.Model = strings.TrimSpace(input.Model)
	if input.LLMURL == "" || input.Model == "" || input.MaxTokens <= 0 || input.MaxTokens > maxTokensLimit {
		return domain.Settings{}, fmt.Errorf("%w: llm_url and model are required; max_tokens must be between 1 and %d", ErrInvalidSettings, maxTokensLimit)
	}
	worker := normalizeProfile(input.WorkerLLMURL, input.WorkerAPIKey, input.WorkerModel, input.WorkerMaxTokens, input.LLMURL, input.APIKey, input.Model, input.MaxTokens)
	dispatch := normalizeProfile(input.DispatchLLMURL, input.DispatchAPIKey, input.DispatchModel, input.DispatchMaxTokens, worker.llmURL, worker.apiKey, worker.model, worker.maxTokens)
	if input.MaxIterations == 0 {
		input.MaxIterations = defaultIterations
	}
	if input.MaxIterations < 1 || input.MaxIterations > maxIterationsLimit {
		return domain.Settings{}, fmt.Errorf("%w: max_iterations must be between 1 and %d", ErrInvalidSettings, maxIterationsLimit)
	}
	if worker.maxTokens <= 0 || worker.maxTokens > maxTokensLimit || dispatch.maxTokens <= 0 || dispatch.maxTokens > maxTokensLimit {
		return domain.Settings{}, fmt.Errorf("%w: worker and dispatch max tokens must be between 1 and %d", ErrInvalidSettings, maxTokensLimit)
	}
	workerPrompt := input.WorkerPrompt
	planPrompt := input.DispatchPlanPrompt
	summaryPrompt := input.DispatchSummaryPrompt
	settings := domain.Settings{
		ID:                    settingsRowID,
		LLMURL:                worker.llmURL,
		APIKey:                worker.apiKey,
		Model:                 worker.model,
		MaxTokens:             worker.maxTokens,
		WorkerLLMURL:          worker.llmURL,
		WorkerAPIKey:          worker.apiKey,
		WorkerModel:           worker.model,
		WorkerMaxTokens:       worker.maxTokens,
		WorkerCommand:         strings.TrimSpace(input.WorkerCommand),
		DispatchLLMURL:        dispatch.llmURL,
		DispatchAPIKey:        dispatch.apiKey,
		DispatchModel:         dispatch.model,
		DispatchMaxTokens:     dispatch.maxTokens,
		MaxIterations:         input.MaxIterations,
		WorkerPrompt:          nonBlankOrDefault(workerPrompt, domain.DefaultWorkerPrompt),
		DispatchPlanPrompt:    nonBlankOrDefault(planPrompt, domain.DefaultPlanPrompt),
		DispatchSummaryPrompt: nonBlankOrDefault(summaryPrompt, domain.DefaultSummaryPrompt),
	}
	if err := service.db.WithContext(ctx).Save(&settings).Error; err != nil {
		return domain.Settings{}, err
	}
	return withSettingsDefaults(settings), nil
}

type llmProfile struct {
	llmURL    string
	apiKey    string
	model     string
	maxTokens int
}

func normalizeProfile(url, key, model string, tokens int, fallbackURL, fallbackKey, fallbackModel string, fallbackTokens int) llmProfile {
	url = strings.TrimSpace(url)
	key = strings.TrimSpace(key)
	model = strings.TrimSpace(model)
	if url == "" && key == "" && model == "" && tokens == 0 {
		return llmProfile{fallbackURL, fallbackKey, fallbackModel, fallbackTokens}
	}
	if url == "" {
		url = fallbackURL
	}
	if model == "" {
		model = fallbackModel
	}
	if tokens == 0 {
		tokens = fallbackTokens
	}
	return llmProfile{url, key, model, tokens}
}

func withSettingsDefaults(settings domain.Settings) domain.Settings {
	if settings.WorkerLLMURL == "" {
		settings.WorkerLLMURL = settings.LLMURL
		settings.WorkerAPIKey = settings.APIKey
		settings.WorkerModel = settings.Model
		settings.WorkerMaxTokens = settings.MaxTokens
	}
	if settings.DispatchLLMURL == "" {
		settings.DispatchLLMURL = settings.LLMURL
		settings.DispatchAPIKey = settings.APIKey
		settings.DispatchModel = settings.Model
		settings.DispatchMaxTokens = settings.MaxTokens
	}
	if settings.MaxIterations == 0 {
		settings.MaxIterations = defaultIterations
	}
	settings.WorkerPrompt = nonBlankOrDefault(settings.WorkerPrompt, domain.DefaultWorkerPrompt)
	settings.DispatchPlanPrompt = nonBlankOrDefault(settings.DispatchPlanPrompt, domain.DefaultPlanPrompt)
	settings.DispatchSummaryPrompt = nonBlankOrDefault(settings.DispatchSummaryPrompt, domain.DefaultSummaryPrompt)
	return settings
}

func nonBlankOrDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
