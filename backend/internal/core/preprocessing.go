package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

type PreprocessingPage struct {
	URL          string `json:"url" yaml:"url"`
	StatusCode   int    `json:"status_code,omitempty" yaml:"status_code,omitempty"`
	ResponsePath string `json:"response_path,omitempty" yaml:"response_path,omitempty"`
	BodyBytes    int64  `json:"body_bytes" yaml:"body_bytes"`
	Error        string `json:"error,omitempty" yaml:"error,omitempty"`
}

type PreprocessingState struct {
	Status      string              `json:"status" yaml:"status"`
	Reason      string              `json:"reason,omitempty" yaml:"reason,omitempty"`
	FactID      string              `json:"fact_id,omitempty" yaml:"fact_id,omitempty"`
	CompletedAt string              `json:"completed_at,omitempty" yaml:"completed_at,omitempty"`
	Pages       []PreprocessingPage `json:"pages,omitempty" yaml:"pages,omitempty"`
}

func (d *DB) InitialTargetSources(ctx context.Context, project string) ([]string, error) {
	var texts []string
	err := tx(ctx, d, func(t *sql.Tx) error {
		var err error
		texts, err = targetProbeSources(t, project)
		return err
	})
	return texts, err
}

// Record background evidence atomically, bound to the active run.
func (d *DB) SavePreprocessing(ctx context.Context, project, runID string, state PreprocessingState, description, detail string) error {
	if state.Status != "ready" && state.Status != "skipped" {
		return fmt.Errorf("invalid preprocessing status")
	}
	return tx(ctx, d, func(t *sql.Tx) error {
		if _, err := checkActive(t, project); err != nil {
			return err
		}
		var currentRun, raw string
		if err := t.QueryRow(`SELECT run_id,preprocessing_json FROM project_target_health WHERE project_id=? AND status='ready'`, project).Scan(&currentRun, &raw); err != nil {
			return err
		}
		if currentRun != runID {
			return fmt.Errorf("preprocessing no longer belongs to the active project run")
		}
		var current PreprocessingState
		if err := json.Unmarshal([]byte(raw), &current); err != nil {
			return err
		}
		if current.Status != "pending" {
			return nil
		}
		state.CompletedAt = now()
		if state.Status == "ready" {
			fid, err := nextID(t, project, "fact", "f")
			if err != nil {
				return err
			}
			state.FactID = fid
			resultType := "environment_failure"
			for _, page := range state.Pages {
				if page.Error == "" {
					resultType = "progress"
					break
				}
			}
			if _, err := t.Exec(`INSERT INTO facts(id,project_id,description,title,detail,result_type) VALUES(?,?,?,'Web 首页预处理',?,?)`, fid, project, description, detail, resultType); err != nil {
				return err
			}
			if err := queueDecideTx(t, project, "web_preprocessed", fid); err != nil {
				return err
			}
		}
		_, err := t.Exec(`UPDATE project_target_health SET preprocessing_json=? WHERE project_id=? AND run_id=?`, jsonData(state), project, runID)
		return err
	})
}
