package worker

import (
	"context"
	"fmt"
)

const maxDecideFailures = 3

type decideAttempt struct {
	fingerprint  string
	eventThrough int64
}

func (r *Runner) failExhaustedDecideProject(ctx context.Context, project string, attempt decideAttempt) bool {
	var failures int
	err := r.DB.SQL.QueryRowContext(ctx, `SELECT failure_count FROM worker_retry_state WHERE project_id=? AND task_type='decide' AND graph_fingerprint=? AND event_through=?`, project, attempt.fingerprint, attempt.eventThrough).Scan(&failures)
	if err != nil || failures < maxDecideFailures {
		return false
	}
	reason := fmt.Sprintf("规划连续失败 %d 次，且当前没有可执行步骤；系统无法自动继续。", failures)
	var lastError string
	if err := r.DB.SQL.QueryRowContext(ctx, `SELECT substr(detail,1,500) FROM worker_task_history WHERE project_id=? AND task_type='decide' AND status IN ('failed','invalid_payload','rejected') ORDER BY id DESC LIMIT 1`, project).Scan(&lastError); err == nil && lastError != "" {
		reason += " 最近错误：" + lastError
	}
	failed, err := r.DB.FailProjectIfDecideExhausted(ctx, project, attempt.fingerprint, attempt.eventThrough, maxDecideFailures, reason)
	if err != nil {
		r.logger.Printf("decide exhaustion check project=%s: %v", project, err)
	} else if failed {
		r.logger.Printf("project failed project=%s: %s", project, reason)
	}
	return failed
}
