package worker

import (
	"context"
	"time"
)

// Dependencies are established facts (step_sources), not a future step ID or
// priority. Other executable/working steps can advance independently of a
// failed summary; successful commits may establish prerequisites for new work.
func (r *Runner) nextStepCommit(ctx context.Context, project string) (string, string, string, error) {
	var p, step, description string
	err := r.DB.SQL.QueryRowContext(ctx, `SELECT s.project_id,s.id,s.description
 FROM worker_execute_recovery x JOIN steps s ON s.project_id=x.project_id AND s.id=x.step_id
 JOIN projects p ON p.id=s.project_id
 WHERE p.status='active' AND (?='' OR p.id=?) AND s.abandoned=0
 AND EXISTS(SELECT 1 FROM project_target_health h WHERE h.project_id=p.id AND h.status='ready')
 AND ((x.status='commit_pending' AND x.payload_json<>'')
      OR (x.status='summary_pending' AND x.session_id<>'' AND s.to_fact_id IS NULL
          AND (x.retry_after='' OR julianday(x.retry_after)<=julianday(?))
          AND (x.summary_attempts=0 OR (NOT EXISTS (
              SELECT 1 FROM decide_events e JOIN steps source
              ON source.project_id=e.project_id AND source.id=e.source_id
              WHERE e.project_id=s.project_id AND e.handled_at IS NULL AND source.to_fact_id IS NOT NULL
          ) AND NOT EXISTS (
              SELECT 1 FROM steps other LEFT JOIN worker_execute_recovery pending
              ON pending.project_id=other.project_id AND pending.step_id=other.id
              WHERE other.project_id=s.project_id AND other.id<>s.id
              AND other.to_fact_id IS NULL AND other.abandoned=0
              AND (pending.step_id IS NULL OR (pending.status='running' AND other.worker IS NOT NULL))
              AND NOT EXISTS (SELECT 1 FROM routes rt WHERE rt.project_id=other.project_id AND rt.id=other.route_id AND rt.status='shelved')
          )))))
 ORDER BY CASE x.status WHEN 'commit_pending' THEN 0 ELSE 1 END,
 x.summary_attempts,s.priority DESC,x.created_at,s.id LIMIT 1`, project, project, time.Now().UTC().Format(time.RFC3339Nano)).Scan(&p, &step, &description)
	return p, step, description, err
}

func summaryRetryDelay(attempts int) time.Duration {
	return time.Duration(min(5*(1<<min(max(attempts-1, 0), 6)), 300)) * time.Second
}

// Deferring recovery must not turn an interruption without a committed result
// into an extra Decide call. Normal fact, hint and operator events are preserved.
const pendingResultsEventGuard = `(NOT EXISTS (
 SELECT 1 FROM worker_execute_recovery waiting WHERE waiting.project_id=p.id
 AND waiting.status IN ('summary_pending','commit_pending')) OR EXISTS (
 SELECT 1 FROM decide_events fresh LEFT JOIN steps source
 ON source.project_id=fresh.project_id AND source.id=fresh.source_id
 WHERE fresh.project_id=p.id AND fresh.handled_at IS NULL
 AND (fresh.reason<>'environment_failure' OR source.to_fact_id IS NOT NULL)))`

func (r *Runner) waitForResultBeforeDecide(ctx context.Context, project string) bool {
	var wait bool
	err := r.DB.SQL.QueryRowContext(ctx, `SELECT NOT `+pendingResultsEventGuard+` FROM projects p WHERE p.id=?`, project).Scan(&wait)
	return err != nil || wait
}
