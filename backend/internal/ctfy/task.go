// Package ctfy implements the CTFY-0001 v3 container contract.
package ctfy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const SchemaVersion = 3

type Task struct {
	SchemaVersion int    `json:"schema_version"`
	RunMode       string `json:"run_mode"`
	DryRun        bool   `json:"dry_run"`
	PlatformURL   string `json:"platform_url"`
	CompetitionID string `json:"competition_id"`
	MaxTurns      *int   `json:"max_turns"`
	Challenge     struct {
		ID string `json:"id"`
	} `json:"challenge"`
}

func readTask(workDir string) (Task, error) {
	var task Task
	raw, err := os.ReadFile(filepath.Join(workDir, "task.json"))
	if err != nil {
		return task, fmt.Errorf("read task.json: %w", err)
	}
	if err := json.Unmarshal(raw, &task); err != nil {
		return task, fmt.Errorf("parse task.json: %w", err)
	}
	return task, nil
}

func (t Task) Validate() error {
	if t.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported task schema_version %d; supported: %d", t.SchemaVersion, SchemaVersion)
	}
	switch t.RunMode {
	case "instance", "qa", "defence", "awd", "engagement":
	default:
		return fmt.Errorf("unsupported run_mode %q", t.RunMode)
	}
	if t.MaxTurns != nil && *t.MaxTurns < 1 {
		return fmt.Errorf("task max_turns must be positive or null")
	}
	if t.RunMode == "awd" && t.CompetitionID == "" {
		return fmt.Errorf("an awd task requires competition_id")
	}
	return nil
}
