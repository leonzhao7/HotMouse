package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

const routeColumns = `id,title,goal_id,status,budget_seconds,no_progress_limit,invested_ms,budget_baseline_ms,result_count,progress_count,negative_count,no_progress_count,environment_failure_count,shelved_reason,reopen_condition,next_direction,created_at`

func scanRoute(row interface{ Scan(...any) error }) (Route, error) {
	var r Route
	var goal sql.NullString
	err := row.Scan(&r.ID, &r.Title, &goal, &r.Status, &r.BudgetSeconds, &r.NoProgressLimit, &r.InvestedMilliseconds, &r.BudgetBaselineMilliseconds, &r.ResultCount, &r.ProgressCount, &r.NegativeCount, &r.NoProgressCount, &r.EnvironmentFailureCount, &r.ShelvedReason, &r.ReopenCondition, &r.NextDirection, &r.CreatedAt)
	r.GoalID = nullable(goal)
	r.BudgetSpentMilliseconds = max(0, r.InvestedMilliseconds-r.BudgetBaselineMilliseconds)
	return r, err
}
func listRoutes(ctx context.Context, q queryer, project string) ([]Route, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+routeColumns+` FROM routes WHERE project_id=? ORDER BY created_at,id`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Route{}
	for rows.Next() {
		r, err := scanRoute(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func validateRouteConfig(budget, limit *int) error {
	if budget != nil && *budget < 0 {
		return fmt.Errorf("route budget_seconds must be nonnegative")
	}
	if limit != nil && *limit < 1 {
		return fmt.Errorf("route no_progress_limit must be positive")
	}
	return nil
}
func (d *DB) CreateRoute(ctx context.Context, project string, req CreateRoute) (Route, error) {
	var out Route
	err := tx(ctx, d, func(t *sql.Tx) error {
		if _, err := checkActive(t, project); err != nil {
			return err
		}
		if err := requireText("route title", req.Title); err != nil {
			return err
		}
		if err := validateRouteConfig(req.BudgetSeconds, req.NoProgressLimit); err != nil {
			return err
		}
		if req.GoalID != nil {
			var status string
			if err := t.QueryRow(`SELECT status FROM goals WHERE project_id=? AND id=?`, project, *req.GoalID).Scan(&status); err != nil {
				return err
			}
			if status != "active" {
				return fmt.Errorf("route goal is completed")
			}
		}
		id, err := nextID(t, project, "route", "r")
		if err != nil {
			return err
		}
		if _, err := t.Exec(`INSERT INTO routes(id,project_id,title,goal_id,budget_seconds,no_progress_limit,created_at) VALUES(?,?,?,?,COALESCE(?,1200),COALESCE(?,3),?)`, id, project, clean(req.Title), req.GoalID, req.BudgetSeconds, req.NoProgressLimit, now()); err != nil {
			return err
		}
		out, err = scanRoute(t.QueryRow(`SELECT `+routeColumns+` FROM routes WHERE project_id=? AND id=?`, project, id))
		return err
	})
	return out, err
}

// resolveRouteTx preserves a chain's route unless a caller explicitly selects another.
func resolveRouteTx(t *sql.Tx, project string, r CreateStep) (string, error) {
	id := ""
	if r.RouteID != nil {
		id = clean(*r.RouteID)
		if id == "" {
			return "", fmt.Errorf("route_id must not be empty")
		}
	}
	if id == "" {
		for _, fact := range r.From {
			var route sql.NullString
			if err := t.QueryRow(`SELECT route_id FROM steps WHERE project_id=? AND to_fact_id=? AND (? IS NULL OR COALESCE(goal_id,'')=?) ORDER BY created_at DESC LIMIT 1`, project, fact, r.GoalID, r.GoalID).Scan(&route); err != nil && err != sql.ErrNoRows {
				return "", err
			}
			if route.Valid {
				id = route.String
				break
			}
		}
	}
	if id == "" {
		id = "route_unassigned"
		title := "Unassigned route"
		if r.GoalID != nil {
			id = "route_" + *r.GoalID
			title = *r.GoalID
		}
		if _, err := t.Exec(`INSERT OR IGNORE INTO routes(id,project_id,title,goal_id,created_at) VALUES(?,?,?,?,?)`, id, project, title, r.GoalID, now()); err != nil {
			return "", err
		}
	}
	route, err := scanRoute(t.QueryRow(`SELECT `+routeColumns+` FROM routes WHERE project_id=? AND id=?`, project, id))
	if err != nil {
		return "", err
	}
	if route.Status != "active" {
		return "", fmt.Errorf("route %s is shelved: %s", id, route.ShelvedReason)
	}
	if route.GoalID != nil && r.GoalID != nil && *route.GoalID != *r.GoalID {
		return "", fmt.Errorf("route goal does not match step goal")
	}
	return id, nil
}
func queueDecideTx(t *sql.Tx, project, reason, source string) error {
	_, err := t.Exec(`INSERT INTO decide_events(project_id,reason,source_id,created_at) VALUES(?,?,?,?)`, project, reason, source, now())
	return err
}
func (d *DB) RequestDecide(ctx context.Context, project, reason string) error {
	return tx(ctx, d, func(t *sql.Tx) error {
		if _, err := checkActive(t, project); err != nil {
			return err
		}
		if err := requireText("decide reason", reason); err != nil {
			return err
		}
		return queueDecideTx(t, project, clean(reason), "")
	})
}
func (d *DB) PendingDecideEvents(ctx context.Context, project string) ([]DecideEvent, error) {
	rows, err := d.SQL.QueryContext(ctx, `SELECT id,reason,source_id,created_at FROM decide_events WHERE project_id=? AND handled_at IS NULL ORDER BY id`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DecideEvent{}
	for rows.Next() {
		var e DecideEvent
		if err := rows.Scan(&e.ID, &e.Reason, &e.SourceID, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func (d *DB) AcknowledgeDecideEvents(ctx context.Context, project string, through int64) error {
	_, err := d.SQL.ExecContext(ctx, `UPDATE decide_events SET handled_at=? WHERE project_id=? AND id<=? AND handled_at IS NULL`, now(), project, through)
	return err
}
func shelveRouteTx(t *sql.Tx, project, id, reason, condition, direction string) error {
	res, err := t.Exec(`UPDATE routes SET status='shelved',shelved_reason=?,reopen_condition=?,next_direction=? WHERE project_id=? AND id=? AND status='active'`, reason, condition, direction, project, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return err
	}
	if _, err := t.Exec(`INSERT INTO route_records(project_id,route_id,action,reason,reopen_condition,next_direction,created_at) VALUES(?,?,'shelved',?,?,?,?)`, project, id, reason, condition, direction, now()); err != nil {
		return err
	}
	return failShelvedRouteProjectTx(t, project, id, reason)
}

func failShelvedRouteProjectTx(t *sql.Tx, project, id, reason string) error {
	explanation := reason
	switch reason {
	case "budget_exhausted":
		explanation = "本轮时间预算耗尽"
	case "no_information_limit":
		explanation = "连续无进展达到上限"
	case "needs_attention":
		explanation = "步骤无法安全继续"
	}
	_, err := failActiveProjectTx(t, project, fmt.Sprintf("路线 %s 已搁置（%s）：%s", id, reason, explanation), "Project failed because a route was shelved")
	return err
}
func enforceRouteLimitsTx(t *sql.Tx, project, id string) error {
	r, err := scanRoute(t.QueryRow(`SELECT `+routeColumns+` FROM routes WHERE project_id=? AND id=?`, project, id))
	if err != nil {
		return err
	}
	if r.Status == "shelved" {
		return failShelvedRouteProjectTx(t, project, id, r.ShelvedReason)
	}
	if r.Status != "active" {
		return nil
	}
	if r.BudgetSeconds > 0 && r.BudgetSpentMilliseconds >= int64(r.BudgetSeconds)*1000 {
		return shelveRouteTx(t, project, id, "budget_exhausted", "Explicitly resume the project to start a new budget window", "Project failed; review the saved evidence before resuming")
	}
	if r.NoProgressCount >= r.NoProgressLimit {
		return shelveRouteTx(t, project, id, "no_information_limit", "Explicitly resume the project to reset the consecutive no-information count", "Project failed; review prior negative results before resuming")
	}
	return nil
}
func (d *DB) UpdateRoute(ctx context.Context, project, id string, req UpdateRoute) (Route, error) {
	var out Route
	err := tx(ctx, d, func(t *sql.Tx) error { return d.updateRouteTx(ctx, t, project, id, req, &out, true) })
	return out, err
}

func (d *DB) updateRouteTx(ctx context.Context, t *sql.Tx, project, id string, req UpdateRoute, out *Route, allowFailed bool) error {
	// An operator can repair/reopen routes while the project stays failed.
	// Automatic plan mutations still require an active project.
	if p, err := checkActive(t, project); err != nil && (!allowFailed || p.Status != "failed") {
		return err
	}
	if err := validateRouteConfig(req.BudgetSeconds, req.NoProgressLimit); err != nil {
		return err
	}
	r, err := scanRoute(t.QueryRow(`SELECT `+routeColumns+` FROM routes WHERE project_id=? AND id=?`, project, id))
	if err != nil {
		return err
	}
	if req.Status != nil && *req.Status != "active" && *req.Status != "shelved" {
		return requireValid("route status", *req.Status)
	}
	if req.Status != nil && *req.Status != r.Status {
		if err := requireText("route change reason", req.Reason); err != nil {
			return err
		}
	}
	if _, err := t.Exec(`UPDATE routes SET budget_seconds=COALESCE(?,budget_seconds),no_progress_limit=COALESCE(?,no_progress_limit) WHERE project_id=? AND id=?`, req.BudgetSeconds, req.NoProgressLimit, project, id); err != nil {
		return err
	}
	if req.Status != nil && *req.Status == "shelved" {
		if err := shelveRouteTx(t, project, id, clean(req.Reason), clean(req.ReopenCondition), clean(req.NextDirection)); err != nil {
			return err
		}
	} else if req.Status != nil && *req.Status == "active" && r.Status == "shelved" {
		budget := r.BudgetSeconds
		if req.BudgetSeconds != nil {
			budget = *req.BudgetSeconds
		}
		if budget > 0 && r.BudgetSpentMilliseconds >= int64(budget)*1000 {
			return fmt.Errorf("route budget already exhausted; increase budget before reopening")
		}
		if _, err := t.Exec(`UPDATE routes SET status='active',shelved_reason='',no_progress_count=0,reopen_condition=?,next_direction=? WHERE project_id=? AND id=?`, clean(req.ReopenCondition), clean(req.NextDirection), project, id); err != nil {
			return err
		}
		if _, err := t.Exec(`INSERT INTO route_records(project_id,route_id,action,reason,reopen_condition,next_direction,created_at) VALUES(?,?,'reopened',?,?,?,?)`, project, id, clean(req.Reason), clean(req.ReopenCondition), clean(req.NextDirection), now()); err != nil {
			return err
		}
	}
	if err := enforceRouteLimitsTx(t, project, id); err != nil {
		return err
	}
	*out, err = scanRoute(t.QueryRow(`SELECT `+routeColumns+` FROM routes WHERE project_id=? AND id=?`, project, id))
	return err
}
func (d *DB) RouteRecords(ctx context.Context, project string) ([]RouteRecord, error) {
	rows, err := d.SQL.QueryContext(ctx, `SELECT id,route_id,action,reason,reopen_condition,next_direction,created_at FROM route_records WHERE project_id=? ORDER BY id`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RouteRecord{}
	for rows.Next() {
		var r RouteRecord
		if err := rows.Scan(&r.ID, &r.RouteID, &r.Action, &r.Reason, &r.ReopenCondition, &r.NextDirection, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func recordResultTx(t *sql.Tx, project, step string, result ExecutionResult) error {
	var route sql.NullString
	if err := t.QueryRow(`SELECT route_id FROM steps WHERE project_id=? AND id=?`, project, step).Scan(&route); err != nil {
		return err
	}
	if route.Valid {
		progress, negative, noinfo, environment := 0, 0, 0, 0
		switch result.Type {
		case "progress":
			progress = 1
		case "negative":
			negative = 1
		case "no_information", "unclassified":
			noinfo = 1
		case "environment_failure":
			environment = 1
		}
		if _, err := t.Exec(`UPDATE routes SET result_count=result_count+1,progress_count=progress_count+?,negative_count=negative_count+?,environment_failure_count=environment_failure_count+?,no_progress_count=CASE WHEN ?+?>0 THEN 0 ELSE no_progress_count+? END WHERE project_id=? AND id=?`, progress, negative, environment, progress, negative, noinfo, project, route.String); err != nil {
			return err
		}
		if result.Type == "needs_attention" {
			if err := shelveRouteTx(t, project, route.String, "needs_attention", result.Detail, "Project failed; review the saved evidence before resuming"); err != nil {
				return err
			}
		}
		if err := enforceRouteLimitsTx(t, project, route.String); err != nil {
			return err
		}
	}
	switch result.Type {
	case "progress", "negative", "blocked", "environment_failure", "needs_attention":
		return queueActiveDecideTx(t, project, result.Type, step)
	}
	return nil
}

// A result may terminate the project through route stop-loss in the same
// transaction. Such results remain evidence without requesting another plan.
func queueActiveDecideTx(t *sql.Tx, project, reason, source string) error {
	_, err := t.Exec(`INSERT INTO decide_events(project_id,reason,source_id,created_at) SELECT id,?,?,? FROM projects WHERE id=? AND status='active'`, reason, source, now(), project)
	return err
}
func decodeResult(raw string) *ExecutionResult {
	if raw == "" {
		return nil
	}
	var r ExecutionResult
	if json.Unmarshal([]byte(raw), &r) != nil {
		return nil
	}
	return &r
}

// FinishWorkerTask accounts each execution attempt once, including recovery work.
// Infrastructure failures are observations, never target facts or no-progress strikes.
func (d *DB) FinishWorkerTask(ctx context.Context, id int64, status, detail string) error {
	return tx(ctx, d, func(t *sql.Tx) error { return finishWorkerTaskTx(t, id, status, detail, time.Now().UTC()) })
}
func finishWorkerTaskTx(t *sql.Tx, id int64, status, detail string, stamp time.Time) error {
	var project, step sql.NullString
	var task, started string
	var finished sql.NullString
	if err := t.QueryRow(`SELECT project_id,step_id,task_type,started_at,finished_at FROM worker_task_history WHERE id=?`, id).Scan(&project, &step, &task, &started, &finished); err != nil {
		return err
	}
	if finished.Valid {
		return nil
	}
	if _, err := t.Exec(`UPDATE worker_task_history SET status=?,detail=?,finished_at=? WHERE id=?`, status, detail, stamp.Format(time.RFC3339Nano), id); err != nil {
		return err
	}
	if task != "execute" || !project.Valid || !step.Valid || status == "claim_failed" || status == "disabled" {
		return nil
	}
	var route sql.NullString
	if err := t.QueryRow(`SELECT route_id FROM steps WHERE project_id=? AND id=?`, project.String, step.String).Scan(&route); err == sql.ErrNoRows {
		return nil
	} else if err != nil {
		return err
	}
	if !route.Valid {
		return nil
	}
	start, err := time.Parse(time.RFC3339Nano, started)
	if err != nil {
		return err
	}
	elapsed := stamp.Sub(start).Milliseconds()
	if elapsed < 0 {
		elapsed = 0
	}
	if _, err := t.Exec(`INSERT INTO route_investments(history_id,project_id,route_id,elapsed_ms) VALUES(?,?,?,?)`, id, project.String, route.String, elapsed); err != nil {
		return err
	}
	if _, err := t.Exec(`UPDATE routes SET invested_ms=invested_ms+? WHERE project_id=? AND id=?`, elapsed, project.String, route.String); err != nil {
		return err
	}
	if status == "failed" || status == "summary_pending" || status == "invalid_payload" || status == "interrupted" {
		if _, err := t.Exec(`UPDATE routes SET environment_failure_count=environment_failure_count+1 WHERE project_id=? AND id=?`, project.String, route.String); err != nil {
			return err
		}
	}
	if err := enforceRouteLimitsTx(t, project.String, route.String); err != nil {
		return err
	}
	if status == "failed" || status == "summary_pending" || status == "invalid_payload" || status == "interrupted" {
		return queueActiveDecideTx(t, project.String, "environment_failure", step.String)
	}
	return nil
}

// RefreshRouteLimits also covers migrated historical investment after restart.
func (d *DB) RefreshRouteLimits(ctx context.Context) error {
	return tx(ctx, d, func(t *sql.Tx) error {
		rows, err := t.Query(`SELECT r.project_id,r.id FROM routes r JOIN projects p ON p.id=r.project_id WHERE p.status='active' AND (r.status='shelved' OR (r.status='active' AND ((r.budget_seconds>0 AND r.invested_ms-r.budget_baseline_ms>=r.budget_seconds*1000) OR r.no_progress_count>=r.no_progress_limit)))`)
		if err != nil {
			return err
		}
		ids := [][2]string{}
		for rows.Next() {
			var id [2]string
			if err := rows.Scan(&id[0], &id[1]); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := enforceRouteLimitsTx(t, id[0], id[1]); err != nil {
				return err
			}
		}
		return nil
	})
}

// ReconcileInterruptedInvestments closes orphaned task histories at the last
// persisted heartbeat, so service downtime never consumes a route's budget.
func (d *DB) ReconcileInterruptedInvestments(ctx context.Context) error {
	return tx(ctx, d, func(t *sql.Tx) error { return reconcileInterruptedInvestmentsTx(t, "") })
}

func reconcileInterruptedInvestmentsTx(t *sql.Tx, project string) error {
	rows, err := t.Query(`SELECT h.id,CASE WHEN s.to_fact_id IS NOT NULL THEN COALESCE(s.concluded_at,h.last_activity_at,h.started_at) ELSE COALESCE(h.last_activity_at,s.last_heartbeat_at,h.started_at) END,CASE WHEN s.to_fact_id IS NOT NULL THEN 'completed' ELSE 'interrupted' END FROM worker_task_history h JOIN steps s ON s.project_id=h.project_id AND s.id=h.step_id CROSS JOIN settings st WHERE (?='' OR h.project_id=?) AND h.task_type='execute' AND h.finished_at IS NULL AND (s.worker IS NULL OR s.to_fact_id IS NOT NULL OR (julianday(?)-julianday(s.last_heartbeat_at))*86400>st.step_timeout)`, project, project, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	type orphan struct {
		id          int64
		end, status string
	}
	items := []orphan{}
	for rows.Next() {
		var item orphan
		if err := rows.Scan(&item.id, &item.end, &item.status); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range items {
		end, err := time.Parse(time.RFC3339Nano, item.end)
		if err != nil {
			return err
		}
		if err := finishWorkerTaskTx(t, item.id, item.status, "reconciled previous execution at its last recorded activity", end); err != nil {
			return err
		}
	}
	return nil
}

// A manual resume acknowledges all prior route stop-loss decisions. Settle old
// attempts before granting fresh budgets so their late completion cannot consume
// the new window. Lifetime investment and execution evidence remain intact.
func resumeRoutesTx(t *sql.Tx, project string) error {
	if err := reconcileInterruptedInvestmentsTx(t, project); err != nil {
		return err
	}
	if _, err := t.Exec(`INSERT INTO route_records(project_id,route_id,action,reason,created_at) SELECT project_id,id,CASE WHEN status='shelved' THEN 'reopened' ELSE 'budget_reset' END,'operator_resume',? FROM routes WHERE project_id=?`, now(), project); err != nil {
		return err
	}
	_, err := t.Exec(`UPDATE routes SET status='active',shelved_reason='',reopen_condition='',next_direction='',budget_baseline_ms=invested_ms,no_progress_count=0 WHERE project_id=?`, project)
	return err
}
