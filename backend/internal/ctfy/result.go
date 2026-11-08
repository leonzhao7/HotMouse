package ctfy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"hotmouse/backend/internal/worker"
)

type Result struct {
	SchemaVersion int               `json:"schema_version"`
	Answers       map[string]string `json:"answers,omitempty"`
	Solved        []string          `json:"solved"`
	FullySolved   bool              `json:"fully_solved"`
	worker.AgentUsage
	StopReason string `json:"stop_reason"`
	Writeup    string `json:"writeup,omitempty"`
}

func emptyResult(reason string) Result {
	return Result{SchemaVersion: SchemaVersion, Solved: []string{}, StopReason: reason}
}

func parseResult(data []byte) (Result, error) {
	result := emptyResult("")
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		return result, fmt.Errorf("result must be one JSON object")
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return result, fmt.Errorf("invalid result fields: %w", err)
	}
	if result.SchemaVersion != 0 && result.SchemaVersion != SchemaVersion {
		return result, fmt.Errorf("unsupported result schema_version %d", result.SchemaVersion)
	}
	result.SchemaVersion = SchemaVersion
	if result.Solved == nil {
		result.Solved = []string{}
	}
	for id, value := range result.Answers {
		if strings.TrimSpace(id) == "" || strings.TrimSpace(value) == "" {
			return result, fmt.Errorf("answers require nonempty question IDs and strings")
		}
	}
	return result, nil
}

func responseResult(data []byte) (Result, error) {
	// Native CLIs sometimes wrap their final JSON in Markdown. Decode one
	// complete object without ever interpreting stdout as the platform result.
	data = bytes.TrimSpace(data)
	if result, err := parseResult(data); err == nil {
		return result, nil
	}
	for offset, char := range data {
		if char != '{' {
			continue
		}
		var raw json.RawMessage
		if json.NewDecoder(bytes.NewReader(data[offset:])).Decode(&raw) == nil {
			var fields map[string]json.RawMessage
			if json.Unmarshal(raw, &fields) == nil && (fields["schema_version"] != nil || fields["answers"] != nil || fields["stop_reason"] != nil) {
				return parseResult(raw)
			}
		}
	}
	return emptyResult(""), fmt.Errorf("agent produced no valid CTFy result object")
}

func writeResult(workDir string, result Result, cfg config) error {
	result.SchemaVersion = SchemaVersion
	if result.Solved == nil {
		result.Solved = []string{}
	}
	const writeupLimit = 64 << 10
	if len(result.Writeup) > writeupLimit {
		result.Writeup = result.Writeup[:writeupLimit]
		for !utf8.ValidString(result.Writeup) {
			result.Writeup = result.Writeup[:len(result.Writeup)-1]
		}
		result.Writeup += "\n\n[writeup truncated]"
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	// Redact string fields before serialization so quotes in a credential cannot
	// evade masking or damage the JSON representation.
	if cfg.key != "" || cfg.token != "" {
		var values any
		if err := json.Unmarshal(data, &values); err != nil {
			return err
		}
		data, err = json.MarshalIndent(redactJSON(values, cfg), "", "  ")
		if err != nil {
			return err
		}
	}
	file, err := os.CreateTemp(workDir, ".ctfy-result-*.tmp")
	if err != nil {
		return fmt.Errorf("create result file: %w", err)
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(append(data, '\n'))
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), filepath.Join(workDir, "result.json"))
}

func redactJSON(value any, cfg config) any {
	switch v := value.(type) {
	case string:
		return cfg.redact(v)
	case []any:
		for i := range v {
			v[i] = redactJSON(v[i], cfg)
		}
	case map[string]any:
		for key := range v {
			v[key] = redactJSON(v[key], cfg)
		}
	}
	return value
}
