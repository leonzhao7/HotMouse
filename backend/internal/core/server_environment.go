package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// ApplyServerEnvironment runs after migrations and before the worker pool starts.
// Empty variables are ignored so Compose can pass optional settings through.
// Only explicitly configured values replace saved settings, in one transaction.
func (d *DB) ApplyServerEnvironment(ctx context.Context) ([]string, error) {
	type integerSetting struct {
		name, column string
		min, max     int
	}
	workerSettings := []integerSetting{
		{"HOTMOUSE_WORKER_COUNT", "worker_count", 1, 32},
		{"HOTMOUSE_PROJECT_CONCURRENCY", "project_concurrency", 0, 32},
	}
	systemSettings := []integerSetting{
		{"HOTMOUSE_PROJECT_MAX_WORKERS", "default_project_max_workers", 1, 32},
		{"HOTMOUSE_PROJECT_BUDGET_SECONDS", "default_project_budget_seconds", 0, math.MaxInt32},
		{"HOTMOUSE_STEP_TIMEOUT_SECONDS", "step_timeout", 5, math.MaxInt32},
		{"HOTMOUSE_DECIDE_LEASE_TIMEOUT_SECONDS", "decide_timeout", 5, math.MaxInt32},
		{"HOTMOUSE_WORKER_UNHEALTHY_RETRY_AFTER_SECONDS", "worker_unhealthy_retry_after_seconds", 1, 3600},
		{"HOTMOUSE_WORKER_REJECTED_RETRY_AFTER_SECONDS", "worker_rejected_retry_after_seconds", 1, 3600},
	}
	runtimeSettings := []integerSetting{
		{"HOTMOUSE_DECIDE_TIMEOUT_SECONDS", "decide_timeout_seconds", 1, 86400},
		{"HOTMOUSE_EXECUTE_TIMEOUT_SECONDS", "execute_timeout_seconds", 1, 86400},
		{"HOTMOUSE_CONCLUDE_TIMEOUT_SECONDS", "conclude_timeout_seconds", 1, 3600},
		{"HOTMOUSE_CONCLUDE_RESERVE_SECONDS", "conclude_reserve_seconds", 0, 120},
		{"HOTMOUSE_RECOVERY_ATTEMPTS", "recovery_attempts", 1, 10},
		{"HOTMOUSE_FORMAT_RETRIES", "format_retries", 0, 1},
	}
	changes := []string{}
	readIntegers := func(settings []integerSetting) (map[string]int, error) {
		values := map[string]int{}
		for _, setting := range settings {
			name, raw := serverEnvironmentValue(setting.name)
			if name == "" {
				continue
			}
			value, err := strconv.Atoi(raw)
			if err != nil || value < setting.min || value > setting.max {
				return nil, fmt.Errorf("%s must be an integer between %d and %d", name, setting.min, setting.max)
			}
			values[setting.column] = value
			changes = append(changes, name)
		}
		return values, nil
	}
	workerValues, err := readIntegers(workerSettings)
	if err != nil {
		return nil, err
	}
	systemValues, err := readIntegers(systemSettings)
	if err != nil {
		return nil, err
	}
	runtimeValues, err := readIntegers(runtimeSettings)
	if err != nil {
		return nil, err
	}
	providerValues := map[string]string{}
	llmConfigured := false
	for _, setting := range []struct {
		key   string
		names []string
	}{
		{"PI_BASE_URL", []string{"HOTMOUSE_LLM_BASE_URL", "PI_BASE_URL"}},
		{"PI_API_KEY", []string{"HOTMOUSE_LLM_API_KEY", "PI_API_KEY"}},
		{"PI_MODEL", []string{"HOTMOUSE_LLM_MODEL", "PI_MODEL"}},
		{"PI_PROVIDER_API", []string{"HOTMOUSE_LLM_API", "PI_PROVIDER_API"}},
		{"PI_MODEL_CONTEXT_WINDOW", []string{"PI_MODEL_CONTEXT_WINDOW"}},
		{"PI_OPTCHAT_EXTENSION", []string{"PI_OPTCHAT_EXTENSION"}},
		{"OPTCHAT_HOME", []string{"OPTCHAT_HOME"}},
		{"HOTMOUSE_BASH_TIMEOUT_SECONDS", []string{"HOTMOUSE_BASH_TIMEOUT_SECONDS"}},
	} {
		name, value := serverEnvironmentValue(setting.names...)
		// Preserve Pi's existing explicit opt-out, including for an image default.
		if name == "" && setting.key == "PI_OPTCHAT_EXTENSION" {
			if _, present := os.LookupEnv("PI_OPTCHAT_EXTENSION"); present {
				name = "PI_OPTCHAT_EXTENSION"
			}
		}
		if name == "" {
			continue
		}
		providerValues[setting.key] = value
		changes = append(changes, name)
		if setting.key == "PI_BASE_URL" || setting.key == "PI_API_KEY" || setting.key == "PI_MODEL" || setting.key == "PI_PROVIDER_API" {
			llmConfigured = true
		}
	}
	if raw, ok := providerValues["PI_MODEL_CONTEXT_WINDOW"]; ok {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 {
			return nil, fmt.Errorf("PI_MODEL_CONTEXT_WINDOW must be a positive integer")
		}
	}
	if raw, ok := providerValues["HOTMOUSE_BASH_TIMEOUT_SECONDS"]; ok {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 86400 {
			return nil, fmt.Errorf("HOTMOUSE_BASH_TIMEOUT_SECONDS must be an integer between 1 and 86400")
		}
	}
	_, driver := serverEnvironmentValue("HOTMOUSE_WORKER_DRIVER")
	if driver != "" && driver != "pi" {
		return nil, fmt.Errorf("HOTMOUSE_WORKER_DRIVER must be pi")
	}
	if driver != "" {
		changes = append(changes, "HOTMOUSE_WORKER_DRIVER")
	}
	forcePi := driver == "pi" || llmConfigured
	if len(changes) == 0 {
		return changes, nil
	}
	err = tx(ctx, d, func(t *sql.Tx) error {
		var savedDriver, command, environmentJSON, runtimeJSON, dispatcherJSON string
		if err := t.QueryRow(`SELECT driver,command,environment_json,runtime_json,dispatcher_json FROM worker_config WHERE id=1`).Scan(&savedDriver, &command, &environmentJSON, &runtimeJSON, &dispatcherJSON); err != nil {
			return err
		}
		environment := map[string]string{}
		if err := json.Unmarshal([]byte(environmentJSON), &environment); err != nil {
			return fmt.Errorf("read saved worker environment: %w", err)
		}
		if environment == nil {
			environment = map[string]string{}
		}
		for key, value := range providerValues {
			environment[key] = value
		}
		if llmConfigured {
			if environment["PI_PROVIDER_API"] == "" {
				environment["PI_PROVIDER_API"] = "openai"
			}
			if err := validateServerLLMEnvironment(environment); err != nil {
				return err
			}
		}
		runtime := DefaultWorkerRuntime()
		if err := json.Unmarshal([]byte(runtimeJSON), &runtime); err != nil {
			return fmt.Errorf("read saved worker runtime: %w", err)
		}
		// JSON field names are shared with the API's partial runtime updates.
		if err := json.Unmarshal([]byte(jsonData(runtimeValues)), &runtime); err != nil {
			return err
		}
		if err := runtime.Validate(); err != nil {
			return err
		}
		var dispatcher *DispatcherConfig
		if err := json.Unmarshal([]byte(dispatcherJSON), &dispatcher); err != nil {
			return fmt.Errorf("read saved dispatcher configuration: %w", err)
		}
		if forcePi {
			savedDriver, command = "pi", ""
			if dispatcher != nil {
				dispatcher.Driver, dispatcher.Command = "pi", ""
				if dispatcher.Environment == nil {
					dispatcher.Environment = map[string]string{}
				}
				for key, value := range providerValues {
					dispatcher.Environment[key] = value
				}
				if _, ok := providerValues["PI_MODEL"]; ok {
					dispatcher.Model = providerValues["PI_MODEL"]
				}
				if dispatcher.Model != "" {
					dispatcher.Environment["PI_MODEL"] = dispatcher.Model
				}
				if llmConfigured && dispatcher.Environment["PI_PROVIDER_API"] == "" {
					dispatcher.Environment["PI_PROVIDER_API"] = environment["PI_PROVIDER_API"]
				}
				if llmConfigured {
					if err := validateServerLLMEnvironment(dispatcher.Environment); err != nil {
						return fmt.Errorf("dispatcher: %w", err)
					}
				}
			}
		}
		if _, err := t.Exec(`UPDATE worker_config SET driver=?,command=?,environment_json=?,runtime_json=?,dispatcher_json=?,updated_at=? WHERE id=1`, savedDriver, command, jsonData(environment), jsonData(runtime), jsonData(dispatcher), now()); err != nil {
			return err
		}
		for column, value := range workerValues {
			if _, err := t.Exec(`UPDATE worker_config SET `+column+`=? WHERE id=1`, value); err != nil {
				return err
			}
		}
		for column, value := range systemValues {
			if _, err := t.Exec(`UPDATE settings SET `+column+`=? WHERE id=1`, value); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return changes, nil
}

func serverEnvironmentValue(names ...string) (string, string) {
	for _, name := range names {
		if value := os.Getenv(name); value != "" {
			return name, value
		}
	}
	return "", ""
}

func validateServerLLMEnvironment(environment map[string]string) error {
	for _, setting := range []struct{ key, name string }{
		{"PI_BASE_URL", "HOTMOUSE_LLM_BASE_URL"}, {"PI_API_KEY", "HOTMOUSE_LLM_API_KEY"}, {"PI_MODEL", "HOTMOUSE_LLM_MODEL"},
	} {
		if strings.TrimSpace(environment[setting.key]) == "" {
			return fmt.Errorf("%s is required when configuring an LLM through environment variables", setting.name)
		}
	}
	u, err := url.Parse(environment["PI_BASE_URL"])
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("HOTMOUSE_LLM_BASE_URL must be an HTTP(S) URL without credentials, query or fragment")
	}
	switch environment["PI_PROVIDER_API"] {
	case "openai", "openai-chat-completions", "openai-completions", "openai-responses", "anthropic-messages":
	case "anthropic":
		environment["PI_PROVIDER_API"] = "anthropic-messages"
	default:
		return fmt.Errorf("HOTMOUSE_LLM_API must be openai, openai-completions, openai-responses or anthropic-messages")
	}
	return nil
}
