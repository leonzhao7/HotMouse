package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"hotmouse/backend/internal/core"
)

func mergeDispatcherConfig(existing string, update json.RawMessage) (string, error) {
	if len(update) == 0 {
		return existing, nil
	}
	if string(update) == "null" {
		return "null", nil
	}
	var current *core.DispatcherConfig
	if err := json.Unmarshal([]byte(existing), &current); err != nil {
		return "", err
	}
	if current == nil {
		current = &core.DispatcherConfig{Environment: map[string]string{}}
	}
	var change struct {
		Driver            *string           `json:"driver"`
		Command           *string           `json:"command"`
		Model             *string           `json:"model"`
		Environment       map[string]string `json:"environment"`
		EnvironmentRemove []string          `json:"environment_remove"`
	}
	decoder := json.NewDecoder(bytes.NewReader(update))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&change); err != nil {
		return "", err
	}
	if change.Driver != nil {
		current.Driver = strings.TrimSpace(*change.Driver)
	}
	if change.Command != nil {
		current.Command = strings.TrimSpace(*change.Command)
	}
	if change.Model != nil {
		current.Model = strings.TrimSpace(*change.Model)
	}
	if !supportedDriver(current.Driver) {
		return "", fmt.Errorf("unsupported dispatcher driver")
	}
	if current.Driver == "command" && current.Command == "" {
		return "", fmt.Errorf("dispatcher command must not be empty")
	}
	if current.Environment == nil {
		current.Environment = map[string]string{}
	}
	for _, key := range change.EnvironmentRemove {
		if strings.TrimSpace(key) == "" {
			return "", fmt.Errorf("dispatcher environment_remove keys must not be empty")
		}
		if _, exists := change.Environment[key]; exists {
			return "", fmt.Errorf("dispatcher environment key cannot be set and removed together")
		}
		delete(current.Environment, key)
	}
	for key, value := range change.Environment {
		if strings.TrimSpace(key) == "" {
			return "", fmt.Errorf("dispatcher environment keys must not be empty")
		}
		if value == "***" {
			if _, exists := current.Environment[key]; !exists {
				return "", fmt.Errorf("unknown masked dispatcher environment key")
			}
			continue
		}
		current.Environment[key] = value
	}
	payload, err := json.Marshal(current)
	return string(payload), err
}
