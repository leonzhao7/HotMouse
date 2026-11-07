package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
)

type TargetEndpoint struct {
	Host string `json:"host" yaml:"host"`
	Port int    `json:"port" yaml:"port"`
}

func (t TargetEndpoint) Address() string {
	return net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
}

type TargetProbeAnalysis struct {
	Required bool             `json:"required" yaml:"required"`
	Reason   string           `json:"reason" yaml:"reason"`
	Targets  []TargetEndpoint `json:"targets" yaml:"targets"`
}

type TargetProbeState struct {
	Preprocessing       PreprocessingState   `json:"preprocessing" yaml:"preprocessing"`
	RunID               string               `json:"run_id" yaml:"run_id"`
	Status              string               `json:"status" yaml:"status"`
	Analysis            *TargetProbeAnalysis `json:"analysis,omitempty" yaml:"analysis,omitempty"`
	ConsecutiveFailures []int                `json:"consecutive_failures,omitempty" yaml:"consecutive_failures,omitempty"`
	LastCheckedAt       string               `json:"last_checked_at,omitempty" yaml:"last_checked_at,omitempty"`
	LastError           string               `json:"last_error,omitempty" yaml:"last_error,omitempty"`
	AnalysisFailures    int                  `json:"analysis_failures" yaml:"analysis_failures"`
	RetryAfter          string               `json:"retry_after,omitempty" yaml:"retry_after,omitempty"`
}

const targetProbeSelect = `run_id,status,analysis_json,failure_counts_json,last_checked_at,last_error,analysis_failures,retry_after,preprocessing_json`

func readTargetProbe(row *sql.Row) (TargetProbeState, error) {
	var state TargetProbeState
	var analysis, failures, preprocessing string
	err := row.Scan(&state.RunID, &state.Status, &analysis, &failures, &state.LastCheckedAt, &state.LastError, &state.AnalysisFailures, &state.RetryAfter, &preprocessing)
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal([]byte(preprocessing), &state.Preprocessing); err != nil {
		return state, fmt.Errorf("invalid saved preprocessing: %w", err)
	}
	if analysis != "" {
		if err := json.Unmarshal([]byte(analysis), &state.Analysis); err != nil {
			return state, fmt.Errorf("invalid saved target probe analysis: %w", err)
		}
	}
	if err := json.Unmarshal([]byte(failures), &state.ConsecutiveFailures); err != nil {
		return state, fmt.Errorf("invalid saved target probe counters: %w", err)
	}
	return state, nil
}

func (d *DB) TargetProbe(ctx context.Context, project string) (TargetProbeState, error) {
	return readTargetProbe(d.SQL.QueryRowContext(ctx, `SELECT `+targetProbeSelect+` FROM project_target_health WHERE project_id=?`, project))
}

// Rotate the monitor generation without discarding successful classification or
// preprocessing. Old probe/analysis results cannot overwrite a resumed run.
func renewTargetProbeRunTx(t *sql.Tx, project string) error {
	var analysis string
	if err := t.QueryRow(`SELECT analysis_json FROM project_target_health WHERE project_id=?`, project).Scan(&analysis); err != nil {
		return err
	}
	if analysis == "" {
		// Older stop/resume code cleared classification. Recover the last saved
		// successful response so existing projects can resume without another LLM call.
		var raw string
		err := t.QueryRow(`SELECT detail FROM worker_task_history WHERE project_id=? AND task_type='target_probe' AND status='completed' ORDER BY id DESC LIMIT 1`, project).Scan(&raw)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		var response struct {
			Accepted *bool           `json:"accepted"`
			Data     json.RawMessage `json:"data"`
		}
		if err == nil && json.Unmarshal([]byte(raw), &response) == nil {
			// Both wrapped and bare analysis responses are supported by workers.
			data := []byte(raw)
			if response.Accepted != nil {
				data = nil
				if *response.Accepted {
					data = response.Data
				}
			}
			var saved TargetProbeAnalysis
			if json.Unmarshal(data, &saved) == nil && ValidateTargetProbeAnalysis(saved) == nil {
				data, err = json.Marshal(saved)
				if err != nil {
					return err
				}
				analysis = string(data)
			}
		}
	}
	_, err := t.Exec(`UPDATE project_target_health SET run_id=lower(hex(randomblob(16))),status=CASE WHEN ?='' THEN 'pending' ELSE 'ready' END,analysis_json=?,failure_counts_json='[]',last_checked_at='',last_error='',analysis_failures=0,retry_after='' WHERE project_id=?`, analysis, analysis, project)
	return err
}

func ValidateTargetProbeAnalysis(analysis TargetProbeAnalysis) error {
	if err := requireText("target probe reason", analysis.Reason); err != nil {
		return err
	}
	if analysis.Required != (len(analysis.Targets) > 0) {
		return fmt.Errorf("required target probing needs targets; disabled probing must have no targets")
	}
	if len(analysis.Targets) > 16 {
		return fmt.Errorf("at most 16 target probe endpoints are allowed")
	}
	seen := map[string]bool{}
	for _, endpoint := range analysis.Targets {
		if endpoint.Port < 1 || endpoint.Port > 65535 {
			return fmt.Errorf("target probe port must be between 1 and 65535")
		}
		host := endpoint.Host
		if host == "" || host != clean(host) || len(host) > 253 {
			return fmt.Errorf("target probe host must be an IP address or domain")
		}
		if net.ParseIP(host) == nil {
			for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
				if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
					return fmt.Errorf("target probe host must be an IP address or domain")
				}
				for _, c := range label {
					if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
						return fmt.Errorf("target probe host must be an IP address or domain")
					}
				}
			}
		}
		key := strings.ToLower(endpoint.Address())
		if seen[key] {
			return fmt.Errorf("duplicate target probe endpoint %s", endpoint.Address())
		}
		seen[key] = true
	}
	return nil
}

// Bind all writes to a project run so late responses cannot affect a resumed project.
func (d *DB) SaveTargetProbeAnalysis(ctx context.Context, project, runID string, analysis TargetProbeAnalysis, scope ...string) error {
	if err := ValidateTargetProbeAnalysis(analysis); err != nil {
		return err
	}
	return tx(ctx, d, func(t *sql.Tx) error {
		if _, err := checkActive(t, project); err != nil {
			return err
		}
		texts, err := targetProbeSources(t, project)
		if err != nil {
			return err
		}
		analysis, err = resolveTargetProbeURLPorts(analysis, append(texts, scope...)...)
		if err != nil {
			return err
		}
		result, err := t.Exec(`UPDATE project_target_health SET status='ready',analysis_json=?,failure_counts_json=?,last_error='',analysis_failures=0,retry_after='' WHERE project_id=? AND run_id=? AND status='pending'`, jsonData(analysis), jsonData(make([]int, len(analysis.Targets))), project, runID)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return fmt.Errorf("target probe analysis no longer belongs to the pending project run")
		}
		return nil
	})
}

func (d *DB) RecordTargetProbeAnalysisFailure(ctx context.Context, project, runID, message, retryAfter string) (bool, error) {
	failed := false
	err := tx(ctx, d, func(t *sql.Tx) error {
		result, err := t.Exec(`UPDATE project_target_health SET analysis_failures=analysis_failures+1,last_error=?,retry_after=? WHERE project_id=? AND run_id=? AND status='pending' AND EXISTS(SELECT 1 FROM projects WHERE id=? AND status='active')`, message, retryAfter, project, runID, project)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n == 0 {
			return nil
		}
		var attempts int
		if err := t.QueryRow(`SELECT analysis_failures FROM project_target_health WHERE project_id=?`, project).Scan(&attempts); err != nil {
			return err
		}
		if attempts >= 3 {
			failed, err = failActiveProjectTx(t, project, "目标探活分析连续失败 3 次："+message, "Target probe analysis failed")
		}
		return err
	})
	return failed, err
}

// Each required endpoint has its own counter; another reachable endpoint must
// not hide its outage. Empty error strings mean that TCP connected successfully.
func (d *DB) RecordTargetProbe(ctx context.Context, project, runID string, errors []string) (bool, error) {
	failed := false
	err := tx(ctx, d, func(t *sql.Tx) error {
		var status string
		if err := t.QueryRow(`SELECT status FROM projects WHERE id=?`, project).Scan(&status); err != nil {
			return err
		}
		state, err := readTargetProbe(t.QueryRow(`SELECT `+targetProbeSelect+` FROM project_target_health WHERE project_id=?`, project))
		if err != nil {
			return err
		}
		if status != "active" || state.RunID != runID || state.Status != "ready" {
			return nil
		}
		if state.Analysis == nil || !state.Analysis.Required || len(errors) != len(state.Analysis.Targets) || len(state.ConsecutiveFailures) != len(errors) {
			return fmt.Errorf("target probe results do not match the analyzed endpoints")
		}
		lastError, reason := "", ""
		for i, message := range errors {
			if message == "" {
				state.ConsecutiveFailures[i] = 0
				continue
			}
			state.ConsecutiveFailures[i]++
			lastError = state.Analysis.Targets[i].Address() + ": " + message
			if state.ConsecutiveFailures[i] >= 3 && reason == "" {
				reason = "目标 " + state.Analysis.Targets[i].Address() + " 连续 3 次探活失败（每 20 秒一次，1 分钟内无法连接）：" + message
			}
		}
		if _, err := t.Exec(`UPDATE project_target_health SET failure_counts_json=?,last_checked_at=?,last_error=? WHERE project_id=? AND run_id=?`, jsonData(state.ConsecutiveFailures), now(), lastError, project, runID); err != nil {
			return err
		}
		if reason != "" {
			failed, err = failActiveProjectTx(t, project, reason, "Target unreachable for one minute")
		}
		return err
	})
	return failed, err
}
