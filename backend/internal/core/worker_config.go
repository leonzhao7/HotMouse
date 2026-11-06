package core

import (
	"database/sql"
	"fmt"
	"strings"
)

// A nil dispatcher configuration preserves the legacy shared provider.
// Once configured, its provider environment is independent of the workers.
type DispatcherConfig struct {
	Driver      string            `json:"driver"`
	Command     string            `json:"command"`
	Model       string            `json:"model"`
	Environment map[string]string `json:"environment"`
}

type WorkerRuntime struct {
	DecideTimeoutSeconds   int `json:"decide_timeout_seconds"`
	ExecuteTimeoutSeconds  int `json:"execute_timeout_seconds"`
	ConcludeTimeoutSeconds int `json:"conclude_timeout_seconds"`
	ConcludeReserveSeconds int `json:"conclude_reserve_seconds"`
	RecoveryAttempts       int `json:"recovery_attempts"`
	FormatRetries          int `json:"format_retries"`
}

func DefaultWorkerRuntime() WorkerRuntime {
	return WorkerRuntime{DecideTimeoutSeconds: 300, ExecuteTimeoutSeconds: 300, ConcludeTimeoutSeconds: 90, RecoveryAttempts: 3, FormatRetries: 1}
}

func (r WorkerRuntime) Validate() error {
	if r.DecideTimeoutSeconds < 1 || r.DecideTimeoutSeconds > 86400 || r.ExecuteTimeoutSeconds < 1 || r.ExecuteTimeoutSeconds > 86400 || r.ConcludeTimeoutSeconds < 1 || r.ConcludeTimeoutSeconds > 3600 || r.ConcludeReserveSeconds < 0 || r.ConcludeReserveSeconds > 120 || r.RecoveryAttempts < 1 || r.RecoveryAttempts > 10 || r.FormatRetries < 0 || r.FormatRetries > 1 {
		return fmt.Errorf("invalid worker runtime limits")
	}
	return nil
}

func migrateExecuteRecovery(db *sql.DB) error {
	var definition string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='worker_execute_recovery'`).Scan(&definition); err != nil {
		return err
	}
	if strings.Contains(definition, "summary_pending") {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`CREATE TABLE worker_execute_recovery_new (
		project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE, step_id TEXT NOT NULL,
		payload_json TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'running' CHECK(status IN ('running','summary_pending','commit_pending','needs_attention')),
		worker_name TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL,
		session_id TEXT NOT NULL DEFAULT '', driver TEXT NOT NULL DEFAULT '', worker_identity TEXT NOT NULL DEFAULT '', workspace_dir TEXT NOT NULL DEFAULT '',
		summary_attempts INTEGER NOT NULL DEFAULT 0, interruption_reason TEXT NOT NULL DEFAULT '', PRIMARY KEY(project_id,step_id));
		INSERT INTO worker_execute_recovery_new(project_id,step_id,payload_json,status,worker_name,created_at)
		SELECT project_id,step_id,payload_json,status,worker_name,created_at FROM worker_execute_recovery;
		DROP TABLE worker_execute_recovery;
		ALTER TABLE worker_execute_recovery_new RENAME TO worker_execute_recovery;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}
