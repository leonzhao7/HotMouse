package core

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
)

// ProjectDecisionFingerprint excludes transient provider recovery metadata so
// adding it to API responses does not invalidate decisions saved by older builds.
func ProjectDecisionFingerprint(d ProjectDetail) string {
	steps := d.Steps
	if steps != nil {
		steps = append([]Step{}, steps...)
	}
	for i := range steps {
		steps[i].RecoveryStatus = ""
	}
	b, _ := json.Marshal(struct {
		Facts []Fact
		Goals []Goal
		Steps []Step
		Hints []Hint
	}{d.Facts, d.Goals, steps, d.Hints})
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:])
}

// FailProjectIfStalled closes a project only after a successful decision for
// the current graph produced no runnable work and no asynchronous work remains.
func (d *DB) FailProjectIfStalled(ctx context.Context, project, fingerprint, reason string) (bool, error) {
	return d.failProjectIfNoWork(ctx, project, fingerprint, reason, func(t *sql.Tx) (bool, error) {
		var eligible bool
		err := t.QueryRow(`SELECT EXISTS(SELECT 1 FROM worker_decide_state WHERE project_id=? AND fingerprint=?)
 AND NOT EXISTS(SELECT 1 FROM decide_events WHERE project_id=? AND handled_at IS NULL)`, project, fingerprint, project).Scan(&eligible)
		return eligible, err
	})
}

// Failed planning cannot acknowledge its events. Bound retries for the same
// graph and event batch while preserving new input and any work still in flight.
func (d *DB) FailProjectIfDecideExhausted(ctx context.Context, project, fingerprint string, eventThrough int64, limit int, reason string) (bool, error) {
	if limit < 1 {
		return false, fmt.Errorf("decide attempt limit must be positive")
	}
	return d.failProjectIfNoWork(ctx, project, fingerprint, reason, func(t *sql.Tx) (bool, error) {
		var eligible bool
		err := t.QueryRow(`SELECT EXISTS(SELECT 1 FROM worker_retry_state WHERE project_id=? AND task_type='decide'
 AND graph_fingerprint=? AND event_through=? AND failure_count>=?)
 AND NOT EXISTS(SELECT 1 FROM decide_events WHERE project_id=? AND handled_at IS NULL AND id>?)`, project, fingerprint, eventThrough, limit, project, eventThrough).Scan(&eligible)
		return eligible, err
	})
}

func (d *DB) failProjectIfNoWork(ctx context.Context, project, fingerprint, reason string, eligible func(*sql.Tx) (bool, error)) (bool, error) {
	if err := requireText("failure reason", reason); err != nil {
		return false, err
	}
	failed := false
	err := tx(ctx, d, func(t *sql.Tx) error {
		var status string
		var failureReason string
		var decideWorker sql.NullString
		if err := t.QueryRow(`SELECT status,failure_reason,decide_worker FROM projects WHERE id=?`, project).Scan(&status, &failureReason, &decideWorker); err != nil {
			return err
		}
		if status != "active" || failureReason != "" || decideWorker.Valid {
			return nil
		}
		ready, err := eligible(t)
		if err != nil {
			return err
		}
		if !ready {
			return nil
		}
		detail, err := d.getProjectTx(ctx, t, project)
		if err != nil {
			return err
		}
		if ProjectDecisionFingerprint(detail) != fingerprint {
			return nil
		}
		var activeGoals, pendingRecovery, runnable int
		if err := t.QueryRow(`SELECT count(*) FROM goals WHERE project_id=? AND status='active'`, project).Scan(&activeGoals); err != nil {
			return err
		}
		if activeGoals == 0 {
			return nil
		}
		if err := t.QueryRow(`SELECT count(*) FROM worker_execute_recovery WHERE project_id=? AND status IN ('running','summary_pending','commit_pending')`, project).Scan(&pendingRecovery); err != nil {
			return err
		}
		if pendingRecovery != 0 {
			return nil
		}
		if err := t.QueryRow(`SELECT count(*) FROM steps s LEFT JOIN routes r ON r.project_id=s.project_id AND r.id=s.route_id
 WHERE s.project_id=? AND s.to_fact_id IS NULL AND s.abandoned=0
 AND (s.worker IS NOT NULL OR (COALESCE(r.status,'active')<>'shelved'
      AND NOT EXISTS(SELECT 1 FROM worker_execute_recovery x WHERE x.project_id=s.project_id AND x.step_id=s.id)))`, project).Scan(&runnable); err != nil {
			return err
		}
		if runnable != 0 {
			return nil
		}
		if _, err := t.Exec(`UPDATE projects SET status='failed',failure_reason=?,decide_worker=NULL,decide_trigger=NULL,decide_started_at=NULL,decide_last_heartbeat_at=NULL WHERE id=? AND status='active'`, clean(reason), project); err != nil {
			return err
		}
		if _, err := t.Exec(`UPDATE steps SET worker=NULL WHERE project_id=? AND to_fact_id IS NULL`, project); err != nil {
			return err
		}
		if _, err := t.Exec(`INSERT INTO audit_log(action,target_type,target_id,summary,detail,resource,detail_json,created_at) VALUES('project.failed','project',?,'Project has no executable work',?,?,?,?)`, project, clean(reason), project, jsonData(map[string]string{"reason": clean(reason)}), now()); err != nil {
			return err
		}
		failed = true
		return nil
	})
	return failed, err
}

// Terminal failures stop the project even while planning, executing or recovering.
func failActiveProjectTx(t *sql.Tx, project, reason, summary string) (bool, error) {
	result, err := t.Exec(`UPDATE projects SET status='failed',failure_reason=?,decide_worker=NULL,decide_trigger=NULL,decide_started_at=NULL,decide_last_heartbeat_at=NULL WHERE id=? AND status='active'`, clean(reason), project)
	if err != nil {
		return false, err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return false, nil
	}
	if _, err := t.Exec(`UPDATE steps SET worker=NULL WHERE project_id=? AND to_fact_id IS NULL`, project); err != nil {
		return false, err
	}
	_, err = t.Exec(`INSERT INTO audit_log(action,target_type,target_id,summary,detail,resource,detail_json,created_at) VALUES('project.failed','project',?,?,?,?,?,?)`, project, summary, clean(reason), project, jsonData(map[string]string{"reason": clean(reason)}), now())
	return err == nil, err
}
