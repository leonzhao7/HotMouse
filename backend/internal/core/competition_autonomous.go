package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// AppendCompetitionChallenges adds newly discovered tasks without reordering or
// replaying any existing item. The coordinator calls it before deciding to exit.
func (d *DB) AppendCompetitionChallenges(ctx context.Context, run string, ids []string) error {
	return tx(ctx, d, func(t *sql.Tx) error {
		var status string
		if err := t.QueryRow(`SELECT status FROM competition_runs WHERE id=?`, run).Scan(&status); err != nil {
			return err
		}
		if status != "running" {
			return nil
		}
		var ordinal int
		if err := t.QueryRow(`SELECT COALESCE(MAX(ordinal)+1,0) FROM competition_items WHERE run_id=?`, run).Scan(&ordinal); err != nil {
			return err
		}
		for _, id := range ids {
			if strings.TrimSpace(id) == "" {
				return fmt.Errorf("challenge ID must not be empty")
			}
			result, err := t.Exec(`INSERT OR IGNORE INTO competition_items(run_id,challenge_id,ordinal) VALUES(?,?,?)`, run, id, ordinal)
			if err != nil {
				return err
			}
			count, err := result.RowsAffected()
			if err != nil {
				return err
			}
			ordinal += int(count)
		}
		return nil
	})
}

// RecoverPendingTSecSubmissions is only called while owning the database lock,
// before starting any coordinator. A crashed process cannot know whether its
// write reached TSec; retain that uncertainty for an idempotent API retry.
func (d *DB) RecoverPendingTSecSubmissions(ctx context.Context, scope string) error {
	return tx(ctx, d, func(t *sql.Tx) error {
		stamp := now()
		const flags = `SELECT f.id FROM flags f JOIN platform_projects p ON p.project_id=f.project_id WHERE p.platform='tsec' AND p.scope_id=? AND f.status='submission_pending'`
		if _, err := t.Exec(`UPDATE flag_attempts SET status='submission_unknown',response=?,finished_at=? WHERE status='submission_pending' AND flag_id IN (`+flags+`)`, `{"code":"process_restarted","message":"submission acknowledgement was not saved; retry through the idempotent TSec API"}`, stamp, scope); err != nil {
			return err
		}
		_, err := t.Exec(`UPDATE flags SET status='submission_unknown' WHERE id IN (`+flags+`)`, scope)
		return err
	})
}

// ValidateAutonomousWorker rejects a mock or incomplete provider before an
// unattended process starts target instances and consumes competition time.
func (d *DB) ValidateAutonomousWorker(ctx context.Context) error {
	var driver, command, environmentJSON, dispatcherJSON string
	if err := d.SQL.QueryRowContext(ctx, `SELECT driver,command,environment_json,dispatcher_json FROM worker_config WHERE id=1`).Scan(&driver, &command, &environmentJSON, &dispatcherJSON); err != nil {
		return err
	}
	if driver != "pi" || command != "" {
		return fmt.Errorf("autonomous competition requires the native Pi worker")
	}
	var environment map[string]string
	if err := json.Unmarshal([]byte(environmentJSON), &environment); err != nil {
		return err
	}
	if err := validateServerLLMEnvironment(environment); err != nil {
		return err
	}
	var dispatcher *DispatcherConfig
	if err := json.Unmarshal([]byte(dispatcherJSON), &dispatcher); err != nil {
		return err
	}
	if dispatcher != nil {
		if dispatcher.Driver != "pi" || dispatcher.Command != "" {
			return fmt.Errorf("autonomous competition requires the native Pi dispatcher")
		}
		if dispatcher.Model != "" {
			if dispatcher.Environment == nil {
				dispatcher.Environment = map[string]string{}
			}
			dispatcher.Environment["PI_MODEL"] = dispatcher.Model
		}
		if err := validateServerLLMEnvironment(dispatcher.Environment); err != nil {
			return fmt.Errorf("dispatcher: %w", err)
		}
	}
	return nil
}
