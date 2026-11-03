package worker

import (
	"context"
	"encoding/json"
	"time"
)

func evidenceJSON(v map[string]any) string {
	if len(v) == 0 {
		return "{}"
	}
	b, _ := json.Marshal(v)
	return string(b)
}
func (r *Runner) nextEventProject(ctx context.Context) string {
	cfg := r.config(ctx)
	if !cfg.enabled || cfg.driver == "mock" {
		return ""
	}
	var project string
	err := r.DB.SQL.QueryRowContext(ctx, `SELECT p.id FROM projects p JOIN decide_events e ON e.project_id=p.id WHERE p.status='active' AND e.handled_at IS NULL AND p.decide_worker IS NULL AND NOT EXISTS(SELECT 1 FROM worker_retry_state w WHERE w.project_id=p.id AND w.task_type='decide' AND julianday(w.retry_after)>julianday(?)) GROUP BY p.id ORDER BY MIN(e.id) LIMIT 1`, time.Now().UTC().Format(time.RFC3339Nano)).Scan(&project)
	if err != nil {
		return ""
	}
	return project
}
