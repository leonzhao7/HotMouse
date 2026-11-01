package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"hotmouse/backend/internal/domain"
)

var ErrInvalidSettings = errors.New("invalid settings")

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

	WorkerCommand string            `json:"worker_command"`
	WorkerEnv     map[string]string `json:"worker_env"`

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
	dispatch := normalizeProfile(input.DispatchLLMURL, input.DispatchAPIKey, input.DispatchModel, input.DispatchMaxTokens,
		strings.TrimSpace(input.LLMURL), strings.TrimSpace(input.APIKey), strings.TrimSpace(input.Model), input.MaxTokens)
	if dispatch.llmURL == "" || dispatch.model == "" || dispatch.maxTokens <= 0 || dispatch.maxTokens > maxTokensLimit {
		return domain.Settings{}, fmt.Errorf("%w: dispatch_llm_url and dispatch_model are required; dispatch_max_tokens must be between 1 and %d", ErrInvalidSettings, maxTokensLimit)
	}
	if input.MaxIterations == 0 {
		input.MaxIterations = defaultIterations
	}
	if input.MaxIterations < 1 || input.MaxIterations > maxIterationsLimit {
		return domain.Settings{}, fmt.Errorf("%w: max_iterations must be between 1 and %d", ErrInvalidSettings, maxIterationsLimit)
	}
	if err := validateWorkerEnvironment(input.WorkerEnv); err != nil {
		return domain.Settings{}, fmt.Errorf("%w: %v", ErrInvalidSettings, err)
	}
	workerPrompt := input.WorkerPrompt
	planPrompt := input.DispatchPlanPrompt
	summaryPrompt := input.DispatchSummaryPrompt
	settings := domain.Settings{
		ID:                    settingsRowID,
		LLMURL:                dispatch.llmURL,
		APIKey:                dispatch.apiKey,
		Model:                 dispatch.model,
		MaxTokens:             dispatch.maxTokens,
		WorkerCommand:         strings.TrimSpace(input.WorkerCommand),
		WorkerEnv:             cloneStringMap(input.WorkerEnv),
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
	if settings.WorkerEnv == nil {
		settings.WorkerEnv = map[string]string{}
	}
	if settings.DispatchLLMURL == "" {
		settings.DispatchLLMURL = settings.LLMURL
		settings.DispatchAPIKey = settings.APIKey
		settings.DispatchModel = settings.Model
		settings.DispatchMaxTokens = settings.MaxTokens
	}
	settings.LLMURL = settings.DispatchLLMURL
	settings.APIKey = settings.DispatchAPIKey
	settings.Model = settings.DispatchModel
	settings.MaxTokens = settings.DispatchMaxTokens
	if settings.MaxIterations == 0 {
		settings.MaxIterations = defaultIterations
	}
	settings.WorkerPrompt = nonBlankOrDefault(settings.WorkerPrompt, domain.DefaultWorkerPrompt)
	settings.DispatchPlanPrompt = nonBlankOrDefault(settings.DispatchPlanPrompt, domain.DefaultPlanPrompt)
	settings.DispatchSummaryPrompt = nonBlankOrDefault(settings.DispatchSummaryPrompt, domain.DefaultSummaryPrompt)
	return settings
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return map[string]string{}
	}
	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}

func validateWorkerEnvironment(values map[string]string) error {
	for key, value := range values {
		if key == "" || strings.ContainsAny(key, "=\x00") {
			return fmt.Errorf("worker_env key %q is invalid", key)
		}
		if strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("worker_env value for %q contains a NUL byte", key)
		}
	}
	return nil
}

func nonBlankOrDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
