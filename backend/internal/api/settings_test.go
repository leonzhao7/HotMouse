package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"hotmouse/backend/internal/domain"
)

func TestGetSettingsReturnsEmptyDefaults(t *testing.T) {
	service := newTestService(t)
	settings, err := service.GetSettings(context.Background())
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	if settings.ID != settingsRowID || settings.LLMURL != "" || settings.APIKey != "" || settings.Model != "" || settings.MaxTokens != 0 {
		t.Fatalf("unexpected default settings: %+v", settings)
	}
	if settings.UpdatedAt != nil {
		t.Fatalf("default settings should have no updated_at: %+v", settings)
	}
	if settings.MaxIterations != domain.DefaultMaxIterations || settings.WorkerPrompt != domain.DefaultWorkerPrompt || settings.DispatchPlanPrompt != domain.DefaultPlanPrompt || settings.DispatchSummaryPrompt != domain.DefaultSummaryPrompt {
		t.Fatalf("settings defaults are incomplete: %+v", settings)
	}
	if settings.WorkerCommand != "" {
		t.Fatalf("default worker command = %q", settings.WorkerCommand)
	}
}

func TestUpdateSettingsStoresWorkerDispatchAndPromptConfiguration(t *testing.T) {
	service := newTestService(t)
	saved, err := service.UpdateSettings(context.Background(), UpdateSettingsInput{
		LLMURL: "https://worker.example/v1", APIKey: "worker-key", Model: "worker", MaxTokens: 500,
		WorkerLLMURL: "https://worker.example/v1", WorkerAPIKey: "worker-key", WorkerModel: "worker", WorkerMaxTokens: 500,
		DispatchLLMURL: "https://dispatch.example/v1", DispatchAPIKey: "dispatch-key", DispatchModel: "dispatch", DispatchMaxTokens: 900,
		MaxIterations: 3, WorkerPrompt: "do {{action}}", DispatchPlanPrompt: "plan {{graph_json}}", DispatchSummaryPrompt: "summarize {{worker_output}}",
		WorkerCommand: "my-worker --prompt {{prompt}}",
	})
	if err != nil {
		t.Fatalf("update settings: %v", err)
	}
	if got := saved.ForWorker(); got.LLMURL != "https://worker.example/v1" || got.APIKey != "worker-key" || got.Model != "worker" || got.MaxTokens != 500 {
		t.Fatalf("worker profile = %+v", got)
	}
	if got := saved.ForDispatch(); got.LLMURL != "https://dispatch.example/v1" || got.APIKey != "dispatch-key" || got.Model != "dispatch" || got.MaxTokens != 900 {
		t.Fatalf("dispatch profile = %+v", got)
	}
	if saved.MaxIterations != 3 || saved.WorkerPrompt != "do {{action}}" || saved.DispatchPlanPrompt != "plan {{graph_json}}" || saved.DispatchSummaryPrompt != "summarize {{worker_output}}" {
		t.Fatalf("custom runtime settings not preserved: %+v", saved)
	}
	if saved.WorkerCommand != "my-worker --prompt {{prompt}}" {
		t.Fatalf("worker command = %q", saved.WorkerCommand)
	}
	loaded, err := service.GetSettings(context.Background())
	if err != nil {
		t.Fatalf("reload settings: %v", err)
	}
	if loaded.DispatchModel != "dispatch" || loaded.MaxIterations != 3 || loaded.DispatchSummaryPrompt != "summarize {{worker_output}}" || loaded.WorkerCommand != "my-worker --prompt {{prompt}}" {
		t.Fatalf("persisted settings = %+v", loaded)
	}
}

func TestUpdateSettingsPersistsAndReplaces(t *testing.T) {
	service := newTestService(t)
	first, err := service.UpdateSettings(context.Background(), UpdateSettingsInput{
		LLMURL:    " https://api.example.com/v1 ",
		APIKey:    " secret ",
		Model:     " small ",
		MaxTokens: 512,
	})
	if err != nil {
		t.Fatalf("update settings: %v", err)
	}
	if first.LLMURL != "https://api.example.com/v1" || first.APIKey != "secret" || first.Model != "small" || first.MaxTokens != 512 {
		t.Fatalf("settings were not normalized: %+v", first)
	}
	if first.UpdatedAt == nil {
		t.Fatal("updated settings should record updated_at")
	}

	second, err := service.UpdateSettings(context.Background(), UpdateSettingsInput{
		LLMURL:    "http://localhost:11434/v1",
		Model:     "local",
		MaxTokens: 2048,
	})
	if err != nil {
		t.Fatalf("replace settings: %v", err)
	}
	if second.APIKey != "" {
		t.Fatalf("replacement should clear the API key: %+v", second)
	}

	stored, err := service.GetSettings(context.Background())
	if err != nil {
		t.Fatalf("get settings after update: %v", err)
	}
	if stored.LLMURL != second.LLMURL || stored.APIKey != "" || stored.Model != "local" || stored.MaxTokens != 2048 {
		t.Fatalf("stored settings = %+v, want %+v", stored, second)
	}
}

func TestUpdateSettingsRejectsInvalidInput(t *testing.T) {
	service := newTestService(t)
	cases := map[string]UpdateSettingsInput{
		"missing llm_url": {Model: "model", MaxTokens: 1},
		"blank llm_url":   {LLMURL: "   ", Model: "model", MaxTokens: 1},
		"missing model":   {LLMURL: "https://api.example.com", MaxTokens: 1},
		"blank model":     {LLMURL: "https://api.example.com", Model: "  ", MaxTokens: 1},
		"zero tokens":     {LLMURL: "https://api.example.com", Model: "model", MaxTokens: 0},
		"negative tokens": {LLMURL: "https://api.example.com", Model: "model", MaxTokens: -1},
		"too many tokens": {LLMURL: "https://api.example.com", Model: "model", MaxTokens: maxTokensLimit + 1},
		"too many worker tokens": {
			LLMURL: "https://api.example.com", Model: "model", MaxTokens: 1,
			WorkerLLMURL: "https://worker.example", WorkerModel: "worker", WorkerMaxTokens: maxTokensLimit + 1,
		},
		"too many dispatch iterations": {
			LLMURL: "https://api.example.com", Model: "model", MaxTokens: 1,
			MaxIterations: maxIterationsLimit + 1,
		},
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := service.UpdateSettings(context.Background(), input); !errors.Is(err, ErrInvalidSettings) {
				t.Fatalf("error = %v, want ErrInvalidSettings", err)
			}
		})
	}
}

func TestHTTPSettingsRoundTrip(t *testing.T) {
	app := newTestApp(t)

	response := serveJSON(t, app, http.MethodGet, "/settings", "")
	if response.Code != http.StatusOK {
		t.Fatalf("get default settings status = %d, body = %s", response.Code, response.Body.String())
	}
	var empty domain.Settings
	if err := json.Unmarshal(response.Body.Bytes(), &empty); err != nil {
		t.Fatalf("decode default settings: %v", err)
	}
	if empty.MaxTokens != 0 || empty.LLMURL != "" {
		t.Fatalf("unexpected default settings: %+v", empty)
	}

	body := `{"llm_url":"https://api.example.com/v1","api_key":"secret","model":"small","max_tokens":512}`
	response = serveJSON(t, app, http.MethodPut, "/settings", body)
	if response.Code != http.StatusOK {
		t.Fatalf("put settings status = %d, body = %s", response.Code, response.Body.String())
	}
	var saved domain.Settings
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
		t.Fatalf("decode saved settings: %v", err)
	}
	if saved.LLMURL != "https://api.example.com/v1" || saved.APIKey != "secret" || saved.Model != "small" || saved.MaxTokens != 512 {
		t.Fatalf("unexpected saved settings: %+v", saved)
	}

	response = serveJSON(t, app, http.MethodGet, "/settings", "")
	if response.Code != http.StatusOK {
		t.Fatalf("get saved settings status = %d, body = %s", response.Code, response.Body.String())
	}
	var stored domain.Settings
	if err := json.Unmarshal(response.Body.Bytes(), &stored); err != nil {
		t.Fatalf("decode stored settings: %v", err)
	}
	if stored.LLMURL != saved.LLMURL || stored.APIKey != saved.APIKey || stored.Model != saved.Model || stored.MaxTokens != saved.MaxTokens {
		t.Fatalf("stored settings = %+v, want %+v", stored, saved)
	}
	if stored.UpdatedAt == nil {
		t.Fatal("stored settings should keep updated_at")
	}

	assertHTTPError(t, serveJSON(t, app, http.MethodPut, "/settings", `{"llm_url":"https://api.example.com","max_tokens":10}`), http.StatusBadRequest, "invalid_settings")
	assertHTTPError(t, serveJSON(t, app, http.MethodPut, "/settings", `{"unknown":true}`), http.StatusBadRequest, "invalid_json")
}
