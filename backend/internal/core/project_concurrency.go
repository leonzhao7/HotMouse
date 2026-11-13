package core

// AutomaticProjectsSQL gives planning, startup analysis and execution the same
// project slots. Zero preserves unrestricted scheduling for local installations.
// Waiting projects retain their active status and enter as earlier projects finish.
const AutomaticProjectsSQL = `SELECT id FROM projects WHERE status='active'
 ORDER BY EXISTS(SELECT 1 FROM worker_task_history h WHERE h.project_id=projects.id AND h.finished_at IS NULL) DESC,created_at,id
 LIMIT (SELECT CASE WHEN project_concurrency=0 THEN -1 ELSE project_concurrency END FROM worker_config WHERE id=1)`
