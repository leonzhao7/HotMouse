package core

import (
	"context"
	"database/sql"
	"fmt"
	"gopkg.in/yaml.v3"
	"html"
	"sort"
	"strings"
	"time"
)

func nextID(tx *sql.Tx, project, kind, prefix string) (string, error) {
	table := map[string]string{"fact": "facts", "goal": "goals", "step": "steps", "finding": "findings", "hint": "hints", "route": "routes"}[kind]
	floor := 0
	if table != "" {
		query := fmt.Sprintf(`SELECT COALESCE(MAX(CAST(substr(id,2) AS INTEGER)),0) FROM %s WHERE project_id=?`, table)
		if err := tx.QueryRow(query, project).Scan(&floor); err != nil {
			return "", err
		}
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO scoped_counters(project_id,kind,value) VALUES(?,?,?)`, project, kind, floor); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`UPDATE scoped_counters SET value=MAX(value,?)+1 WHERE project_id=? AND kind=?`, floor, project, kind); err != nil {
		return "", err
	}
	var n int
	if err := tx.QueryRow(`SELECT value FROM scoped_counters WHERE project_id=? AND kind=?`, project, kind).Scan(&n); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%03d", prefix, n), nil
}

const projectSelectColumns = `id,title,status,bootstrap_enabled,max_workers,created_at,decide_worker,decide_trigger,decide_started_at,decide_last_heartbeat_at,failure_reason`

func projectFrom(row interface{ Scan(...any) error }) (Project, error) {
	var p Project
	var boot int
	var dw, dt, ds, dh sql.NullString
	err := row.Scan(&p.ID, &p.Title, &p.Status, &boot, &p.MaxWorkers, &p.CreatedAt, &dw, &dt, &ds, &dh, &p.FailureReason)
	if err != nil {
		return p, err
	}
	p.BootstrapEnabled = boot != 0
	if dw.Valid {
		p.Decide = &DecideLease{Worker: dw.String, Trigger: dt.String, StartedAt: ds.String, LastHeartbeatAt: dh.String}
	}
	return p, nil
}
func queryProject(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (Project, error) {
	return projectFrom(q.QueryRowContext(ctx, `SELECT `+projectSelectColumns+` FROM projects WHERE id=?`, id))
}

func (d *DB) CreateProject(ctx context.Context, req CreateProject) (ProjectDetail, error) {
	if err := requireText("title", req.Title); err != nil {
		return ProjectDetail{}, err
	}
	if err := requireText("origin", req.Origin); err != nil {
		return ProjectDetail{}, err
	}
	if err := requireText("goal", req.Goal); err != nil {
		return ProjectDetail{}, err
	}
	maxWorkers := req.MaxWorkers
	if maxWorkers == 0 {
		maxWorkers = 1
	}
	if maxWorkers < 1 || maxWorkers > 32 {
		return ProjectDetail{}, fmt.Errorf("max_workers must be between 1 and 32")
	}
	var out ProjectDetail
	err := tx(ctx, d, func(t *sql.Tx) error {
		nowv := now()
		var n int
		if err := t.QueryRow(`UPDATE counters SET value=value+1 WHERE name='project' RETURNING value`).Scan(&n); err != nil {
			return err
		}
		pid := fmt.Sprintf("proj_%03d", n)
		if _, err := t.Exec(`INSERT INTO projects(id,title,max_workers,created_at) VALUES(?,?,?,?)`, pid, clean(req.Title), maxWorkers, nowv); err != nil {
			return err
		}
		if _, err := t.Exec(`INSERT INTO project_target_health(project_id) VALUES(?)`, pid); err != nil {
			return err
		}
		if _, err := t.Exec(`INSERT INTO facts(id,project_id,description) VALUES('origin',?,?)`, pid, clean(req.Origin)); err != nil {
			return err
		}
		if _, err := t.Exec(`INSERT INTO goals(id,project_id,description,created_at) VALUES('g001',?,?,?)`, pid, clean(req.Goal), nowv); err != nil {
			return err
		}
		for _, h := range req.Hints {
			h.Content = normalizeHint(h.Content)
			h.Creator = clean(h.Creator)
			if err := requireText("hint content", h.Content); err != nil {
				return err
			}
			if err := requireText("hint creator", h.Creator); err != nil {
				return err
			}
			hid, err := nextID(t, pid, "hint", "h")
			if err != nil {
				return err
			}
			if _, err = t.Exec(`INSERT INTO hints(id,project_id,content,creator,created_at) VALUES(?,?,?,?,?)`, hid, pid, h.Content, h.Creator, nowv); err != nil {
				return err
			}
		}
		var getErr error
		out, getErr = d.getProjectTx(ctx, t, pid)
		return getErr
	})
	if err == nil {
		d.ensureProjectContextFile(ctx, out.Project.ID, req.Origin, req.Goal)
	}
	return out, err
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (d *DB) getProjectTx(ctx context.Context, q queryer, id string) (ProjectDetail, error) {
	var p Project
	var err error
	p, err = queryProject(ctx, q, id)
	if err == sql.ErrNoRows {
		return ProjectDetail{}, ErrNotFound
	}
	if err != nil {
		return ProjectDetail{}, err
	}
	out := ProjectDetail{Project: p, Facts: []Fact{}, Steps: []Step{}, Goals: []Goal{}, Findings: []Finding{}, Hints: []Hint{}}
	rows, err := q.QueryContext(ctx, `SELECT id,description,title,detail,result_type FROM facts WHERE project_id=? ORDER BY rowid`, id)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var x Fact
		if err = rows.Scan(&x.ID, &x.Description, &x.Title, &x.Detail, &x.ResultType); err != nil {
			rows.Close()
			return out, err
		}
		out.Facts = append(out.Facts, x)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()
	rows, err = q.QueryContext(ctx, `SELECT id,description,parent_goal_id,status,priority,created_at,completed_at,completion_description,completed_by FROM goals WHERE project_id=? ORDER BY priority,created_at`, id)
	if err != nil {
		return out, err
	}
	var goalIDs []string
	for rows.Next() {
		var x Goal
		var par, ca, cd, cb sql.NullString
		if err = rows.Scan(&x.ID, &x.Description, &par, &x.Status, &x.Priority, &x.CreatedAt, &ca, &cd, &cb); err != nil {
			rows.Close()
			return out, err
		}
		x.ParentGoalID = nullable(par)
		x.CompletedAt = nullable(ca)
		x.CompletionDescription = nullable(cd)
		x.CompletedBy = nullable(cb)
		goalIDs = append(goalIDs, x.ID)
		out.Goals = append(out.Goals, x)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()
	for i, goalID := range goalIDs {
		out.Goals[i].From = goalSources(q, id, goalID)
	}
	rows, err = q.QueryContext(ctx, `SELECT id,to_fact_id,description,goal_id,priority,creator,worker,last_heartbeat_at,created_at,concluded_at,abandoned,route_id,result_json,llm_response,llm_response_recorded,llm_response_error,COALESCE((SELECT status FROM worker_execute_recovery x WHERE x.project_id=steps.project_id AND x.step_id=steps.id),'') FROM steps WHERE project_id=? ORDER BY priority DESC,created_at`, id)
	if err != nil {
		return out, err
	}
	var stepIDs []string
	for rows.Next() {
		var x Step
		var to, g, w, h, co, route sql.NullString
		var resultJSON string
		var ab int
		if err = rows.Scan(&x.ID, &to, &x.Description, &g, &x.Priority, &x.Creator, &w, &h, &x.CreatedAt, &co, &ab, &route, &resultJSON, &x.LLMResponse, &x.LLMResponseRecorded, &x.LLMResponseError, &x.RecoveryStatus); err != nil {
			rows.Close()
			return out, err
		}
		x.To = nullable(to)
		x.GoalID = nullable(g)
		x.Worker = nullable(w)
		x.LastHeartbeatAt = nullable(h)
		x.ConcludedAt = nullable(co)
		x.RouteID = nullable(route)
		x.Result = decodeResult(resultJSON)
		x.Abandoned = ab != 0
		stepIDs = append(stepIDs, x.ID)
		out.Steps = append(out.Steps, x)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()
	for i, stepID := range stepIDs {
		out.Steps[i].From = stepSources(q, id, stepID)
	}
	rows, err = q.QueryContext(ctx, `SELECT id,title,description,severity,kind,data_json,fact_id,created_at FROM findings WHERE project_id=? ORDER BY created_at`, id)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var x Finding
		var data string
		var f sql.NullString
		if err = rows.Scan(&x.ID, &x.Title, &x.Description, &x.Severity, &x.Kind, &data, &f, &x.CreatedAt); err != nil {
			rows.Close()
			return out, err
		}
		x.Data = parseData(data)
		x.FactID = nullable(f)
		out.Findings = append(out.Findings, x)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()
	rows, err = q.QueryContext(ctx, `SELECT id,content,creator,created_at FROM hints WHERE project_id=? ORDER BY created_at`, id)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var x Hint
		if err = rows.Scan(&x.ID, &x.Content, &x.Creator, &x.CreatedAt); err != nil {
			rows.Close()
			return out, err
		}
		x.Content = normalizeHint(x.Content)
		out.Hints = append(out.Hints, x)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()
	out.Routes, err = listRoutes(ctx, q, id)
	if err != nil {
		return out, err
	}
	out.TargetProbe, err = readTargetProbe(q.QueryRowContext(ctx, `SELECT `+targetProbeSelect+` FROM project_target_health WHERE project_id=?`, id))
	return out, err
}
func nullable(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	x := v.String
	return &x
}
func goalSources(q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, p, g string) []string {
	r, e := q.QueryContext(context.Background(), `SELECT fact_id FROM goal_sources WHERE project_id=? AND goal_id=? ORDER BY rowid`, p, g)
	if e != nil {
		return nil
	}
	defer r.Close()
	a := []string{}
	for r.Next() {
		var x string
		_ = r.Scan(&x)
		a = append(a, x)
	}
	if err := r.Err(); err != nil {
		return nil
	}
	return a
}
func stepSources(q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, p, s string) []string {
	r, e := q.QueryContext(context.Background(), `SELECT fact_id FROM step_sources WHERE project_id=? AND step_id=? ORDER BY rowid`, p, s)
	if e != nil {
		return nil
	}
	defer r.Close()
	a := []string{}
	for r.Next() {
		var x string
		_ = r.Scan(&x)
		a = append(a, x)
	}
	if err := r.Err(); err != nil {
		return nil
	}
	return a
}
func (d *DB) GetProject(ctx context.Context, id string) (ProjectDetail, error) {
	if err := d.expireLeases(ctx, id); err != nil {
		return ProjectDetail{}, err
	}
	return d.getProjectTx(ctx, d.SQL, id)
}

// GetProjectReadOnly loads the project graph without reclaiming expired
// leases. Timeline and mutation-adjacent reads use this shape.
func (d *DB) GetProjectReadOnly(ctx context.Context, id string) (ProjectDetail, error) {
	return d.getProjectTx(ctx, d.SQL, id)
}

// GetProjectMeta reads project metadata without reclaiming leases. The
// title/delete endpoints only validate existence and must not mutate worker
// lease state as a side effect.
func (d *DB) GetProjectMeta(ctx context.Context, id string) (Project, error) {
	p, err := queryProject(ctx, d.SQL, id)
	if err == sql.ErrNoRows {
		return Project{}, ErrNotFound
	}
	return p, err
}

func (d *DB) SetProjectMaxWorkers(ctx context.Context, id string, maxWorkers int) (Project, error) {
	if maxWorkers < 1 || maxWorkers > 32 {
		return Project{}, fmt.Errorf("max_workers must be between 1 and 32")
	}
	res, err := d.SQL.ExecContext(ctx, `UPDATE projects SET max_workers=? WHERE id=?`, maxWorkers, id)
	if err != nil {
		return Project{}, err
	}
	if count, _ := res.RowsAffected(); count == 0 {
		return Project{}, ErrNotFound
	}
	return d.GetProjectMeta(ctx, id)
}

func (d *DB) expireLeases(ctx context.Context, project string) error {
	var stepTimeout, decideTimeout int
	if err := d.SQL.QueryRowContext(ctx, `SELECT step_timeout,decide_timeout FROM settings WHERE id=1`).Scan(&stepTimeout, &decideTimeout); err != nil {
		return err
	}
	_, err := d.SQL.ExecContext(ctx, `UPDATE steps SET worker=NULL WHERE project_id=? AND to_fact_id IS NULL AND abandoned=0 AND worker IS NOT NULL AND last_heartbeat_at IS NOT NULL AND (julianday(?) - julianday(last_heartbeat_at))*86400 > ?`, project, now(), stepTimeout)
	if err != nil {
		return err
	}
	_, err = d.SQL.ExecContext(ctx, `UPDATE projects SET decide_worker=NULL,decide_trigger=NULL,decide_started_at=NULL,decide_last_heartbeat_at=NULL WHERE id=? AND decide_worker IS NOT NULL AND decide_last_heartbeat_at IS NOT NULL AND (julianday(?) - julianday(decide_last_heartbeat_at))*86400 > ?`, project, now(), decideTimeout)
	return err
}

func expireStepLeasesTx(t *sql.Tx, project string) error {
	var timeout int
	if err := t.QueryRow(`SELECT step_timeout FROM settings WHERE id=1`).Scan(&timeout); err != nil {
		return err
	}
	_, err := t.Exec(`UPDATE steps SET worker=NULL WHERE project_id=? AND to_fact_id IS NULL AND abandoned=0 AND worker IS NOT NULL AND last_heartbeat_at IS NOT NULL AND (julianday(?) - julianday(last_heartbeat_at))*86400 > ?`, project, now(), timeout)
	return err
}

func expireDecideLeaseTx(t *sql.Tx, project string) error {
	var timeout int
	if err := t.QueryRow(`SELECT decide_timeout FROM settings WHERE id=1`).Scan(&timeout); err != nil {
		return err
	}
	_, err := t.Exec(`UPDATE projects SET decide_worker=NULL,decide_trigger=NULL,decide_started_at=NULL,decide_last_heartbeat_at=NULL WHERE id=? AND decide_worker IS NOT NULL AND decide_last_heartbeat_at IS NOT NULL AND (julianday(?) - julianday(decide_last_heartbeat_at))*86400 > ?`, project, now(), timeout)
	return err
}

// expireLeasesTx mirrors the project read paths, which reclaim both lease types.
func expireLeasesTx(t *sql.Tx, project string) error {
	if err := expireStepLeasesTx(t, project); err != nil {
		return err
	}
	return expireDecideLeaseTx(t, project)
}

func (d *DB) HeartbeatDecide(ctx context.Context, project, worker string) (Project, error) {
	var out Project
	err := tx(ctx, d, func(t *sql.Tx) error {
		if err := requireText("worker", worker); err != nil {
			return err
		}
		worker = clean(worker)
		if _, err := checkActive(t, project); err != nil {
			return err
		}
		if err := expireDecideLeaseTx(t, project); err != nil {
			return err
		}
		var current sql.NullString
		if err := t.QueryRow(`SELECT decide_worker FROM projects WHERE id=?`, project).Scan(&current); err != nil {
			return err
		}
		if !current.Valid {
			return fmt.Errorf("project decide is not currently claimed")
		}
		if current.String != worker {
			return fmt.Errorf("project decide is currently claimed by %s", current.String)
		}
		if _, err := t.Exec(`UPDATE projects SET decide_last_heartbeat_at=? WHERE id=?`, now(), project); err != nil {
			return err
		}
		var err error
		out, err = projectFrom(t.QueryRow(`SELECT `+projectSelectColumns+` FROM projects WHERE id=?`, project))
		return err
	})
	return out, err
}

func (d *DB) UpdateStep(ctx context.Context, project, id string, r UpdateStep) (Step, error) {
	var out Step
	err := tx(ctx, d, func(t *sql.Tx) error { return d.updateStepTx(ctx, t, project, id, r, &out) })
	return out, err
}

func (d *DB) updateStepTx(ctx context.Context, t *sql.Tx, project, id string, r UpdateStep, out *Step) error {
	if _, err := checkActive(t, project); err != nil {
		return err
	}
	var to, worker sql.NullString
	var abandoned int
	if err := t.QueryRow(`SELECT to_fact_id,worker,abandoned FROM steps WHERE project_id=? AND id=?`, project, id).Scan(&to, &worker, &abandoned); err != nil {
		return err
	}
	var recovering int
	if err := t.QueryRow(`SELECT count(*) FROM worker_execute_recovery WHERE project_id=? AND step_id=?`, project, id).Scan(&recovering); err != nil {
		return err
	}
	if recovering > 0 {
		return fmt.Errorf("step has an execution recovery checkpoint and cannot be updated")
	}
	if to.Valid || abandoned != 0 || worker.Valid {
		return fmt.Errorf("closed, abandoned, or claimed steps cannot be updated")
	}
	if r.Abandoned != nil && !*r.Abandoned {
		return fmt.Errorf("abandoned must be true when provided")
	}
	if r.GoalID != nil {
		var status string
		if err := t.QueryRow(`SELECT status FROM goals WHERE project_id=? AND id=?`, project, *r.GoalID).Scan(&status); err != nil {
			return err
		}
		if status != "active" {
			return fmt.Errorf("goal %s is completed", *r.GoalID)
		}
	}
	var newRoute any
	if r.GoalID != nil {
		var currentGoal sql.NullString
		if err := t.QueryRow(`SELECT goal_id FROM steps WHERE project_id=? AND id=?`, project, id).Scan(&currentGoal); err != nil {
			return err
		}
		if !currentGoal.Valid || currentGoal.String != *r.GoalID {
			routeID, err := resolveRouteTx(t, project, CreateStep{GoalID: r.GoalID})
			if err != nil {
				return err
			}
			newRoute = routeID
		}
	}
	if _, err := t.Exec(`UPDATE steps SET priority=COALESCE(?,priority),goal_id=COALESCE(?,goal_id),route_id=COALESCE(?,route_id),abandoned=COALESCE(?,abandoned) WHERE project_id=? AND id=?`, r.Priority, r.GoalID, newRoute, r.Abandoned, project, id); err != nil {
		return err
	}
	var err error
	*out, err = d.stepTx(ctx, t, project, id)
	return err
}
func (d *DB) ListProjects(ctx context.Context) ([]ProjectSummary, error) {
	if _, err := d.SQL.ExecContext(ctx, `UPDATE steps SET worker=NULL WHERE to_fact_id IS NULL AND abandoned=0 AND worker IS NOT NULL AND last_heartbeat_at IS NOT NULL AND (julianday(?) - julianday(last_heartbeat_at))*86400 > (SELECT step_timeout FROM settings WHERE id=1)`, now()); err != nil {
		return nil, err
	}
	if _, err := d.SQL.ExecContext(ctx, `UPDATE projects SET decide_worker=NULL,decide_trigger=NULL,decide_started_at=NULL,decide_last_heartbeat_at=NULL WHERE decide_worker IS NOT NULL AND decide_last_heartbeat_at IS NOT NULL AND (julianday(?) - julianday(decide_last_heartbeat_at))*86400 > (SELECT decide_timeout FROM settings WHERE id=1)`, now()); err != nil {
		return nil, err
	}
	rows, e := d.SQL.QueryContext(ctx, `SELECT `+projectSelectColumns+` FROM projects ORDER BY created_at`)
	if e != nil {
		return nil, e
	}
	var out []ProjectSummary
	var projects []Project
	for rows.Next() {
		p, e := projectFrom(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		projects = append(projects, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for _, p := range projects {
		s := ProjectSummary{Project: p}
		_ = d.SQL.QueryRow(`SELECT count(*) FROM facts WHERE project_id=?`, p.ID).Scan(&s.FactCount)
		_ = d.SQL.QueryRow(`SELECT count(*) FROM steps WHERE project_id=?`, p.ID).Scan(&s.StepCount)
		_ = d.SQL.QueryRow(`SELECT count(*) FROM steps WHERE project_id=? AND concluded_at IS NULL AND worker IS NOT NULL AND abandoned=0`, p.ID).Scan(&s.WorkingStepCount)
		_ = d.SQL.QueryRow(`SELECT count(*) FROM steps s LEFT JOIN routes r ON r.project_id=s.project_id AND r.id=s.route_id WHERE s.project_id=? AND s.to_fact_id IS NULL AND s.worker IS NULL AND s.abandoned=0 AND COALESCE(r.status,'active')<>'shelved' AND NOT EXISTS(SELECT 1 FROM worker_execute_recovery x WHERE x.project_id=s.project_id AND x.step_id=s.id)`, p.ID).Scan(&s.UnclaimedStepCount)
		_ = d.SQL.QueryRow(`SELECT count(*) FROM goals WHERE project_id=?`, p.ID).Scan(&s.GoalCount)
		_ = d.SQL.QueryRow(`SELECT count(*) FROM findings WHERE project_id=?`, p.ID).Scan(&s.FindingCount)
		_ = d.SQL.QueryRow(`SELECT count(*) FROM hints WHERE project_id=?`, p.ID).Scan(&s.HintCount)
		s.IntentCount = s.StepCount
		s.WorkingIntentCount = s.WorkingStepCount
		s.UnclaimedIntentCount = s.UnclaimedStepCount
		out = append(out, s)
	}
	return out, nil
}

func checkActive(t *sql.Tx, id string) (Project, error) {
	p, e := projectFrom(t.QueryRow(`SELECT `+projectSelectColumns+` FROM projects WHERE id=?`, id))
	if e == sql.ErrNoRows {
		return p, ErrNotFound
	}
	if e != nil {
		return p, e
	}
	if p.Status != "active" {
		return p, fmt.Errorf("project is %s", p.Status)
	}
	return p, nil
}
func validateFacts(t *sql.Tx, p string, ids []string) error {
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			return fmt.Errorf("fact ids must not contain duplicates")
		}
		seen[id] = struct{}{}
		var n int
		if e := t.QueryRow(`SELECT count(*) FROM facts WHERE project_id=? AND id=?`, p, id).Scan(&n); e != nil {
			return e
		}
		if n == 0 {
			return fmt.Errorf("fact %s not found", id)
		}
	}
	return nil
}

func normalizeFactIDs(ids []string) ([]string, error) {
	cleaned := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, fmt.Errorf("fact ids must not be empty")
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("fact ids must not contain duplicates")
		}
		seen[id] = struct{}{}
		cleaned = append(cleaned, id)
	}
	return cleaned, nil
}

func normalizeHint(value string) string {
	return html.UnescapeString(strings.TrimSpace(value))
}

func (d *DB) CreateStep(ctx context.Context, p string, r CreateStep) (Step, error) {
	var out Step
	err := tx(ctx, d, func(t *sql.Tx) error { return d.createStepTx(ctx, t, p, r, &out) })
	return out, err
}

func (d *DB) createStepTx(ctx context.Context, t *sql.Tx, p string, r CreateStep, out *Step) error {
	if _, e := checkActive(t, p); e != nil {
		return e
	}
	if len(r.From) == 0 {
		return fmt.Errorf("from must not be empty")
	}
	var err error
	r.From, err = normalizeFactIDs(r.From)
	if err != nil {
		return err
	}
	if err := requireText("description", r.Description); err != nil {
		return err
	}
	if err := requireText("creator", r.Creator); err != nil {
		return err
	}
	if e := validateFacts(t, p, r.From); e != nil {
		return e
	}
	if r.GoalID != nil {
		var st string
		if e := t.QueryRow(`SELECT status FROM goals WHERE project_id=? AND id=?`, p, *r.GoalID).Scan(&st); e != nil {
			return e
		}
		if st != "active" {
			return fmt.Errorf("goal is completed")
		}
	}
	id, e := nextID(t, p, "step", "s")
	if e != nil {
		return e
	}
	v := now()
	var worker any
	if r.Worker != nil {
		value := clean(*r.Worker)
		if err := requireText("worker", value); err != nil {
			return err
		}
		worker = value
	}
	routeID, routeErr := resolveRouteTx(t, p, r)
	if routeErr != nil {
		return routeErr
	}
	if r.GoalID == nil {
		var routeGoal sql.NullString
		if err := t.QueryRow(`SELECT goal_id FROM routes WHERE project_id=? AND id=?`, p, routeID).Scan(&routeGoal); err != nil {
			return err
		}
		r.GoalID = nullable(routeGoal)
		if r.GoalID != nil {
			var status string
			if err := t.QueryRow(`SELECT status FROM goals WHERE project_id=? AND id=?`, p, *r.GoalID).Scan(&status); err != nil {
				return err
			}
			if status != "active" {
				return fmt.Errorf("route goal is completed")
			}
		}
	}

	if _, e = t.Exec(`INSERT INTO steps(id,project_id,description,goal_id,priority,creator,worker,last_heartbeat_at,created_at,route_id) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, p, clean(r.Description), r.GoalID, r.Priority, clean(r.Creator), worker, func() any {
		if r.Worker != nil {
			return v
		}
		return nil
	}(), v, routeID); e != nil {
		return e
	}
	for _, f := range r.From {
		if _, e = t.Exec(`INSERT INTO step_sources(step_id,project_id,fact_id) VALUES(?,?,?)`, id, p, f); e != nil {
			return e
		}
	}
	s, e := d.stepTx(ctx, t, p, id)
	*out = s
	return e
}
func (d *DB) stepTx(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, p, id string) (Step, error) {
	r, e := q.QueryContext(ctx, `SELECT id,to_fact_id,description,goal_id,priority,creator,worker,last_heartbeat_at,created_at,concluded_at,abandoned,route_id,result_json,llm_response,llm_response_recorded,llm_response_error,COALESCE((SELECT status FROM worker_execute_recovery x WHERE x.project_id=steps.project_id AND x.step_id=steps.id),'') FROM steps WHERE project_id=? AND id=?`, p, id)
	if e != nil {
		return Step{}, e
	}
	defer r.Close()
	if !r.Next() {
		return Step{}, ErrNotFound
	}
	var x Step
	var to, g, w, h, c, route sql.NullString
	var resultJSON string
	var ab int
	if e = r.Scan(&x.ID, &to, &x.Description, &g, &x.Priority, &x.Creator, &w, &h, &x.CreatedAt, &c, &ab, &route, &resultJSON, &x.LLMResponse, &x.LLMResponseRecorded, &x.LLMResponseError, &x.RecoveryStatus); e != nil {
		return x, e
	}
	x.To = nullable(to)
	x.GoalID = nullable(g)
	x.Worker = nullable(w)
	x.LastHeartbeatAt = nullable(h)
	x.ConcludedAt = nullable(c)
	x.RouteID = nullable(route)
	x.Result = decodeResult(resultJSON)
	x.Abandoned = ab != 0
	if err := r.Close(); err != nil {
		return x, err
	}
	x.From = stepSources(q, p, id)
	return x, nil
}
func (d *DB) HeartbeatStep(ctx context.Context, p, id, worker string) (Step, error) {
	return d.heartbeatStep(ctx, p, id, worker, "agent")
}

func (d *DB) HeartbeatStepAsDispatcher(ctx context.Context, p, id, worker string) (Step, error) {
	return d.heartbeatStep(ctx, p, id, worker, "dispatcher")
}

func (d *DB) heartbeatStep(ctx context.Context, p, id, worker, role string) (Step, error) {
	if err := d.RefreshRouteLimits(ctx); err != nil {
		return Step{}, err
	}
	var out Step
	e := tx(ctx, d, func(t *sql.Tx) error {
		if err := requireText("worker", worker); err != nil {
			return err
		}
		worker = clean(worker)
		if _, e := checkActive(t, p); e != nil {
			return e
		}
		if err := expireStepLeasesTx(t, p); err != nil {
			return err
		}
		var current sql.NullString
		var currentRole string
		var concluded sql.NullString
		var ab int
		if e := t.QueryRow(`SELECT worker,worker_role,concluded_at,abandoned FROM steps WHERE project_id=? AND id=?`, p, id).Scan(&current, &currentRole, &concluded, &ab); e != nil {
			return e
		}
		if concluded.Valid || ab != 0 {
			return fmt.Errorf("step is closed")
		}
		if current.Valid && current.String != worker {
			return fmt.Errorf("step is currently claimed by %s", current.String)
		}
		if current.Valid && currentRole != role {
			return fmt.Errorf("step lease role cannot be changed while claimed")
		}
		if !current.Valid {
			if role == "agent" {
				var maxWorkers, activeWorkers, alreadyAssigned int
				if err := t.QueryRow(`SELECT max_workers FROM projects WHERE id=?`, p).Scan(&maxWorkers); err != nil {
					return err
				}
				if err := t.QueryRow(`SELECT count(DISTINCT worker),COALESCE(MAX(worker=?),0) FROM steps WHERE project_id=? AND to_fact_id IS NULL AND abandoned=0 AND worker IS NOT NULL AND worker_role='agent'`, worker, p).Scan(&activeWorkers, &alreadyAssigned); err != nil {
					return err
				}
				if alreadyAssigned != 0 {
					return fmt.Errorf("worker already has an active step in this project")
				}
				if activeWorkers >= maxWorkers {
					return fmt.Errorf("project worker limit reached (%d)", maxWorkers)
				}
			}
			var shelved int
			if err := t.QueryRow(`SELECT count(*) FROM steps s JOIN routes r ON r.id=s.route_id AND r.project_id=s.project_id WHERE s.project_id=? AND s.id=? AND r.status='shelved' AND NOT EXISTS(SELECT 1 FROM worker_execute_recovery x WHERE x.project_id=s.project_id AND x.step_id=s.id)`, p, id).Scan(&shelved); err != nil {
				return err
			}
			if shelved > 0 {
				return fmt.Errorf("route is shelved")
			}
		}
		if _, err := t.Exec(`UPDATE steps SET worker=?,worker_role=?,last_heartbeat_at=? WHERE project_id=? AND id=?`, worker, role, now(), p, id); err != nil {
			return err
		}
		var err error
		out, err = d.stepTx(ctx, t, p, id)
		return err
	})
	return out, e
}
func (d *DB) ReleaseStep(ctx context.Context, p, id, worker string) (Step, error) {
	var out Step
	err := tx(ctx, d, func(t *sql.Tx) error {
		if err := requireText("worker", worker); err != nil {
			return err
		}
		worker = clean(worker)
		if _, err := checkActive(t, p); err != nil {
			return err
		}
		if err := expireStepLeasesTx(t, p); err != nil {
			return err
		}
		var w, to sql.NullString
		if e := t.QueryRow(`SELECT worker,to_fact_id FROM steps WHERE project_id=? AND id=?`, p, id).Scan(&w, &to); e != nil {
			return e
		}
		if to.Valid {
			return fmt.Errorf("step already concluded")
		}
		if w.Valid && w.String != worker {
			return fmt.Errorf("step is currently claimed by %s", w.String)
		}
		if w.Valid {
			if _, e := t.Exec(`UPDATE steps SET worker=NULL WHERE project_id=? AND id=?`, p, id); e != nil {
				return e
			}
		}
		var e error
		out, e = d.stepTx(ctx, t, p, id)
		return e
	})
	return out, err
}
func (d *DB) ConcludeStep(ctx context.Context, p, id string, r Conclude) (ConcludeResult, error) {
	var out ConcludeResult
	e := tx(ctx, d, func(t *sql.Tx) error {
		if err := requireText("worker", r.Worker); err != nil {
			return err
		}
		if err := requireText("description", r.Description); err != nil {
			return err
		}
		r.Worker = clean(r.Worker)
		r.Description = clean(r.Description)
		if r.Result == nil {
			r.Result = &ExecutionResult{Type: "unclassified"}
		}
		if err := r.Result.Validate(); err != nil {
			return err
		}
		if r.Finding != nil && (r.Result.Type == "environment_failure" || r.Result.Type == "no_information" || r.Result.Type == "needs_attention") {
			return fmt.Errorf("a %s result cannot assert a finding", r.Result.Type)
		}
		if _, e := checkActive(t, p); e != nil {
			return e
		}
		if err := expireStepLeasesTx(t, p); err != nil {
			return err
		}
		var w sql.NullString
		var to sql.NullString
		var desc string
		var abandoned int
		if e := t.QueryRow(`SELECT worker,to_fact_id,description,abandoned FROM steps WHERE project_id=? AND id=?`, p, id).Scan(&w, &to, &desc, &abandoned); e != nil {
			return e
		}
		if to.Valid {
			return fmt.Errorf("step already concluded")
		}
		if abandoned != 0 {
			return fmt.Errorf("step is abandoned")
		}
		if w.Valid && w.String != r.Worker {
			return fmt.Errorf("step is currently claimed by %s", w.String)
		}
		fid, e := nextID(t, p, "fact", "f")
		if e != nil {
			return e
		}
		v := now()
		if _, e = t.Exec(`INSERT INTO facts(id,project_id,description,title,detail,result_type) VALUES(?,?,?,?,?,?)`, fid, p, clean(r.Description), clean(r.Title), r.Result.Detail, r.Result.Type); e != nil {
			return e
		}
		if _, e = t.Exec(`UPDATE steps SET to_fact_id=?,concluded_at=?,worker=?,last_heartbeat_at=?,result_json=? WHERE project_id=? AND id=?`, fid, v, r.Worker, v, jsonData(r.Result), p, id); e != nil {
			return e
		}
		s, e := d.stepTx(ctx, t, p, id)
		if e != nil {
			return e
		}
		out.Fact = &Fact{ID: fid, Description: clean(r.Description), Title: clean(r.Title), Detail: r.Result.Detail, ResultType: r.Result.Type}
		out.Step = &s
		if r.Finding != nil {
			f := r.Finding
			if f.Data == nil {
				f.Data = map[string]any{}
			}
			if f.Severity == "" {
				f.Severity = "info"
			}
			if !validSeverity(f.Severity) {
				return fmt.Errorf("invalid severity")
			}
			if f.Kind == "" {
				f.Kind = "finding"
			}
			f.Title, f.Description, f.Kind = clean(f.Title), clean(f.Description), clean(f.Kind)
			if err := requireText("finding title", f.Title); err != nil {
				return err
			}
			if err := requireText("finding description", f.Description); err != nil {
				return err
			}
			if err := requireText("finding kind", f.Kind); err != nil {
				return err
			}
			vid, e := nextID(t, p, "finding", "v")
			if e != nil {
				return e
			}
			if _, e = t.Exec(`INSERT INTO findings(id,project_id,title,description,severity,kind,data_json,fact_id,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, vid, p, clean(f.Title), clean(f.Description), f.Severity, f.Kind, jsonData(f.Data), fid, v); e != nil {
				return e
			}
			ff := Finding{ID: vid, Title: clean(f.Title), Description: clean(f.Description), Severity: f.Severity, Kind: f.Kind, Data: f.Data, FactID: &fid, CreatedAt: v}
			out.Finding = &ff
			if err := syncVulnerabilityTx(t, p, ff, id, r.Worker, v); err != nil {
				return err
			}
			if err := syncFlagTx(t, p, ff); err != nil {
				return err
			}
			if err := queueDecideTx(t, p, "finding", ff.ID); err != nil {
				return err
			}
		}
		return recordResultTx(t, p, id, *r.Result)
	})
	return out, e
}

// CreateFinding stores a native Finding and updates the denormalized product
// projection in one transaction. Findings without a reportable kind/severity
// remain valid graph records but do not appear in the vulnerability list.
func (d *DB) CreateFinding(ctx context.Context, p string, r CreateFinding) (Finding, error) {
	var out Finding
	err := tx(ctx, d, func(t *sql.Tx) error {
		if _, err := checkActive(t, p); err != nil {
			return err
		}
		if r.FactID != nil {
			if err := validateFacts(t, p, []string{*r.FactID}); err != nil {
				return err
			}
		}
		if err := requireText("title", r.Title); err != nil {
			return err
		}
		if err := requireText("description", r.Description); err != nil {
			return err
		}
		if r.Severity == "" {
			r.Severity = "info"
		}
		if !validSeverity(r.Severity) {
			return fmt.Errorf("invalid severity")
		}
		if r.Kind == "" {
			r.Kind = "finding"
		}
		r.Kind = clean(r.Kind)
		if err := requireText("kind", r.Kind); err != nil {
			return err
		}
		if r.Data == nil {
			r.Data = map[string]any{}
		}
		id, err := nextID(t, p, "finding", "v")
		if err != nil {
			return err
		}
		v := now()
		if _, err = t.Exec(`INSERT INTO findings(id,project_id,title,description,severity,kind,data_json,fact_id,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, id, p, clean(r.Title), clean(r.Description), r.Severity, r.Kind, jsonData(r.Data), r.FactID, v); err != nil {
			return err
		}
		out = Finding{ID: id, Title: clean(r.Title), Description: clean(r.Description), Severity: r.Severity, Kind: r.Kind, Data: r.Data, FactID: r.FactID, CreatedAt: v}
		if err := syncVulnerabilityTx(t, p, out, "", "", v); err != nil {
			return err
		}
		if err := syncFlagTx(t, p, out); err != nil {
			return err
		}
		return queueDecideTx(t, p, "finding", out.ID)
	})
	return out, err
}

func (d *DB) RefreshVulnerabilities(ctx context.Context, project string) (int, error) {
	var count int
	err := tx(ctx, d, func(t *sql.Tx) error {
		if _, err := queryProject(ctx, t, project); err != nil {
			return err
		}
		rows, err := t.Query(`SELECT id,title,description,severity,kind,data_json,fact_id,created_at FROM findings WHERE project_id=? ORDER BY created_at`, project)
		if err != nil {
			return err
		}
		findings := []Finding{}
		for rows.Next() {
			var f Finding
			var raw string
			var fid sql.NullString
			if err := rows.Scan(&f.ID, &f.Title, &f.Description, &f.Severity, &f.Kind, &raw, &fid, &f.CreatedAt); err != nil {
				return err
			}
			f.Data = parseData(raw)
			f.FactID = nullable(fid)
			findings = append(findings, f)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, f := range findings {
			if err := syncVulnerabilityTx(t, project, f, "", "", f.CreatedAt); err != nil {
				return err
			}
			if f.FactID != nil && validSeverity(f.Severity) && (f.Kind == "security_vulnerability" || f.Kind == "vulnerability") && f.Severity != "info" {
				count++
			}
		}
		return rows.Err()
	})
	return count, err
}

func syncVulnerabilityTx(t *sql.Tx, project string, f Finding, stepID, worker, discovered string) error {
	if f.Kind != "security_vulnerability" && f.Kind != "vulnerability" || f.Severity == "info" || f.FactID == nil || clean(*f.FactID) == "" {
		_, err := t.Exec(`DELETE FROM vulnerabilities WHERE project_id=? AND finding_id=?`, project, f.ID)
		return err
	}
	if discovered == "" {
		discovered = f.CreatedAt
	}
	vulnerabilityID := "vuln_" + project + "_" + f.ID
	if _, err := t.Exec(`DELETE FROM vulnerabilities WHERE project_id=? AND finding_id=? AND id<>?`, project, f.ID, vulnerabilityID); err != nil {
		return err
	}
	if stepID == "" {
		var sid, sw sql.NullString
		_ = t.QueryRow(`SELECT id,worker FROM steps WHERE project_id=? AND to_fact_id=? ORDER BY concluded_at DESC,created_at DESC LIMIT 1`, project, *f.FactID).Scan(&sid, &sw)
		if sid.Valid {
			stepID = sid.String
			if sw.Valid {
				worker = sw.String
			}
		}
	}
	var sourceFacts []string
	if stepID != "" {
		rows, err := t.Query(`SELECT fact_id FROM step_sources WHERE project_id=? AND step_id=? ORDER BY rowid`, project, stepID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			sourceFacts = append(sourceFacts, id)
		}
		rows.Close()
	}
	evidence := findingEvidence(f)
	_, err := t.Exec(`INSERT INTO vulnerabilities(id,project_id,finding_id,fact_id,title,description,severity,discovered_at,source_step_id,source_worker,source_fact_ids_json,evidence_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET title=excluded.title,description=excluded.description,severity=excluded.severity,source_step_id=excluded.source_step_id,source_worker=excluded.source_worker,source_fact_ids_json=excluded.source_fact_ids_json,evidence_json=excluded.evidence_json`, vulnerabilityID, project, f.ID, *f.FactID, f.Title, f.Description, f.Severity, discovered, nullableDB(stepID), nullableDB(worker), jsonData(sourceFacts), jsonData(evidence))
	return err
}

// findingEvidence preserves the proof fields used by the
// vulnerability projection while keeping Findings as the source of truth.
// Report packets and screenshots remain outside this local migration.
func findingEvidence(f Finding) []string {
	items := []string{}
	appendText := func(value any) {
		if text, ok := value.(string); ok && clean(text) != "" {
			text = clean(text)
			for _, existing := range items {
				if existing == text {
					return
				}
			}
			items = append(items, text)
		}
	}
	appendValue := func(value any) {
		switch values := value.(type) {
		case []any:
			for _, item := range values {
				appendText(item)
			}
		case []string:
			for _, item := range values {
				appendText(item)
			}
		default:
			appendText(value)
		}
	}
	appendValue(f.Data["evidence"])
	for _, key := range []string{"location", "proof", "impact"} {
		appendValue(f.Data[key])
	}
	appendValue(f.Data["remediation"])
	if len(items) == 0 {
		appendText(f.Description)
	}
	return items
}

func validSeverity(s string) bool {
	switch s {
	case "critical", "high", "medium", "low", "info":
		return true
	default:
		return false
	}
}

func nullableDB(s string) any {
	if clean(s) == "" {
		return nil
	}
	return s
}

func (d *DB) CompleteGoal(ctx context.Context, p, g string, r Completion) (Goal, error) {
	return d.completeGoal(ctx, p, g, r, false)
}

// CompleteProject implements the legacy project completion endpoint. It
// expires only the Decide lease, while the native Goal endpoint does
// not expire leases.
func (d *DB) CompleteProject(ctx context.Context, p string, r Completion) (Goal, error) {
	return d.completeGoal(ctx, p, "", r, true)
}

func (d *DB) completeGoal(ctx context.Context, p, g string, r Completion, legacyProject bool) (Goal, error) {
	var out Goal
	e := tx(ctx, d, func(t *sql.Tx) error {
		if legacyProject {
			if _, err := checkActive(t, p); err != nil {
				return err
			}
			if err := expireDecideLeaseTx(t, p); err != nil {
				return err
			}
		}
		if err := requireText("worker", r.Worker); err != nil {
			return err
		}
		if err := requireText("description", r.Description); err != nil {
			return err
		}
		r.Worker = clean(r.Worker)
		r.Description = clean(r.Description)
		if _, e := checkActive(t, p); e != nil {
			return e
		}
		var st, desc, created string
		var par sql.NullString
		loadGoal := func() error {
			return t.QueryRow(`SELECT status,description,created_at,parent_goal_id FROM goals WHERE project_id=? AND id=?`, p, g).Scan(&st, &desc, &created, &par)
		}
		if !legacyProject {
			if err := loadGoal(); err != nil {
				return err
			}
			if st == "completed" {
				return fmt.Errorf("goal already completed")
			}
		}
		if len(r.From) == 0 {
			return fmt.Errorf("from must not be empty")
		}
		var normalizeErr error
		r.From, normalizeErr = normalizeFactIDs(r.From)
		if normalizeErr != nil {
			return normalizeErr
		}
		if e := validateFacts(t, p, r.From); e != nil {
			return e
		}
		for _, factID := range r.From {
			var kind string
			if err := t.QueryRow(`SELECT result_type FROM facts WHERE project_id=? AND id=?`, p, factID).Scan(&kind); err != nil {
				return err
			}
			if kind == "environment_failure" || kind == "no_information" || kind == "needs_attention" {
				return fmt.Errorf("fact %s is not goal completion evidence", factID)
			}
		}
		if legacyProject {
			if err := t.QueryRow(`SELECT id FROM goals WHERE project_id=? AND status='active' AND parent_goal_id IS NULL ORDER BY priority,created_at LIMIT 1`, p).Scan(&g); err != nil {
				if err == sql.ErrNoRows {
					return fmt.Errorf("no active goal to complete")
				}
				return err
			}
			if err := loadGoal(); err != nil {
				return err
			}
		}
		v := now()
		if _, err := t.Exec(`UPDATE goals SET status='completed',completed_at=?,completion_description=?,completed_by=? WHERE project_id=? AND id=?`, v, r.Description, r.Worker, p, g); err != nil {
			return err
		}
		for _, f := range r.From {
			if _, err := t.Exec(`INSERT INTO goal_sources(goal_id,project_id,fact_id) VALUES(?,?,?)`, g, p, f); err != nil {
				return err
			}
		}
		var active int
		t.QueryRow(`SELECT count(*) FROM goals WHERE project_id=? AND status!='completed'`, p).Scan(&active)
		if active == 0 {
			if _, err := t.Exec(`UPDATE projects SET status='completed',decide_worker=NULL,decide_trigger=NULL,decide_started_at=NULL,decide_last_heartbeat_at=NULL WHERE id=?`, p); err != nil {
				return err
			}
			if _, err := t.Exec(`UPDATE steps SET worker=NULL WHERE project_id=? AND concluded_at IS NULL`, p); err != nil {
				return err
			}
		}
		var priority int
		_ = t.QueryRow(`SELECT priority FROM goals WHERE project_id=? AND id=?`, p, g).Scan(&priority)
		out = Goal{ID: g, Description: desc, ParentGoalID: nullable(par), Status: "completed", Priority: priority, CreatedAt: created, CompletedAt: &v, CompletionDescription: &r.Description, CompletedBy: &r.Worker, From: r.From}
		return queueDecideTx(t, p, "goal_completed", g)
	})
	if e == nil && d.WorkerMu.TryLock() {
		defer d.WorkerMu.Unlock()
		var status string
		if d.SQL.QueryRowContext(ctx, `SELECT status FROM projects WHERE id=?`, p).Scan(&status) == nil && status == "completed" {
			d.cleanupCompletedProjectWorkspace(ctx, p)
		}
	}
	return out, e
}
func (d *DB) CreateGoal(ctx context.Context, p string, r CreateGoal) (Goal, error) {
	var out Goal
	err := tx(ctx, d, func(t *sql.Tx) error { return d.createGoalTx(ctx, t, p, r, &out) })
	return out, err
}

func (d *DB) createGoalTx(ctx context.Context, t *sql.Tx, p string, r CreateGoal, out *Goal) error {
	if _, e := checkActive(t, p); e != nil {
		return e
	}
	if err := requireText("description", r.Description); err != nil {
		return err
	}
	if err := requireText("creator", r.Creator); err != nil {
		return err
	}
	if r.ParentGoalID != nil {
		var st string
		if e := t.QueryRow(`SELECT status FROM goals WHERE project_id=? AND id=?`, p, *r.ParentGoalID).Scan(&st); e != nil {
			return e
		}
		if st != "active" {
			return fmt.Errorf("parent goal is completed")
		}
	}
	id, e := nextID(t, p, "goal", "g")
	if e != nil {
		return e
	}
	v := now()
	_, e = t.Exec(`INSERT INTO goals(id,project_id,description,parent_goal_id,priority,created_at) VALUES(?,?,?,?,?,?)`, id, p, clean(r.Description), r.ParentGoalID, r.Priority, v)
	*out = Goal{ID: id, Description: clean(r.Description), ParentGoalID: r.ParentGoalID, Status: "active", Priority: r.Priority, CreatedAt: v, From: []string{}}
	return e
}

func (d *DB) UpdateGoal(ctx context.Context, p, id string, r UpdateGoal) (Goal, error) {
	var out Goal
	err := tx(ctx, d, func(t *sql.Tx) error {
		if _, err := checkActive(t, p); err != nil {
			return err
		}
		var status, desc, created string
		var parent, completed, completion, worker sql.NullString
		var priority int
		if err := t.QueryRow(`SELECT description,parent_goal_id,status,priority,created_at,completed_at,completion_description,completed_by FROM goals WHERE project_id=? AND id=?`, p, id).Scan(&desc, &parent, &status, &priority, &created, &completed, &completion, &worker); err != nil {
			return err
		}
		if status != "active" {
			return fmt.Errorf("goal is completed")
		}
		if r.Description != nil {
			if err := requireText("description", *r.Description); err != nil {
				return err
			}
			desc = clean(*r.Description)
		}
		if r.Priority != nil {
			priority = *r.Priority
		}
		if _, err := t.Exec(`UPDATE goals SET description=?,priority=? WHERE project_id=? AND id=?`, desc, priority, p, id); err != nil {
			return err
		}
		out = Goal{ID: id, Description: desc, ParentGoalID: nullable(parent), Status: status, Priority: priority, CreatedAt: created, CompletedAt: nullable(completed), CompletionDescription: nullable(completion), CompletedBy: nullable(worker), From: goalSources(t, p, id)}
		return nil
	})
	return out, err
}

func (d *DB) DeleteGoal(ctx context.Context, p, id string) error {
	return tx(ctx, d, func(t *sql.Tx) error {
		if _, err := checkActive(t, p); err != nil {
			return err
		}
		var parent sql.NullString
		var status string
		if err := t.QueryRow(`SELECT parent_goal_id,status FROM goals WHERE project_id=? AND id=?`, p, id).Scan(&parent, &status); err != nil {
			return err
		}
		if !parent.Valid {
			return fmt.Errorf("top-level goals cannot be deleted")
		}
		if status != "active" {
			return fmt.Errorf("goal is completed")
		}
		var refs int
		if err := t.QueryRow(`SELECT count(*) FROM goals WHERE project_id=? AND parent_goal_id=?`, p, id).Scan(&refs); err != nil {
			return err
		}
		if refs > 0 {
			return fmt.Errorf("goal still has sub-goals")
		}
		if err := t.QueryRow(`SELECT count(*) FROM steps WHERE project_id=? AND goal_id=?`, p, id).Scan(&refs); err != nil {
			return err
		}
		if refs > 0 {
			return fmt.Errorf("goal is still referenced by steps")
		}
		if err := t.QueryRow(`SELECT count(*) FROM routes WHERE project_id=? AND goal_id=?`, p, id).Scan(&refs); err != nil {
			return err
		}
		if refs > 0 {
			return fmt.Errorf("goal is still referenced by routes")
		}
		res, err := t.Exec(`DELETE FROM goals WHERE project_id=? AND id=?`, p, id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}
func (d *DB) AddHint(ctx context.Context, p string, r CreateHint) (Hint, error) {
	var out Hint
	e := tx(ctx, d, func(t *sql.Tx) error {
		r.Content = normalizeHint(r.Content)
		r.Creator = clean(r.Creator)
		var st string
		if e := t.QueryRow(`SELECT status FROM projects WHERE id=?`, p).Scan(&st); e != nil {
			return e
		}
		if e := requireText("content", r.Content); e != nil {
			return e
		}
		if e := requireText("creator", r.Creator); e != nil {
			return e
		}
		id, e := nextID(t, p, "hint", "h")
		if e != nil {
			return e
		}
		v := now()
		_, e = t.Exec(`INSERT INTO hints(id,project_id,content,creator,created_at) VALUES(?,?,?,?,?)`, id, p, r.Content, r.Creator, v)
		out = Hint{ID: id, Content: r.Content, Creator: r.Creator, CreatedAt: v}
		if e != nil {
			return e
		}
		return queueDecideTx(t, p, "hint", id)
	})
	return out, e
}
func (d *DB) SetProjectStatus(ctx context.Context, p, status string) (Project, error) {
	var out Project
	e := tx(ctx, d, func(t *sql.Tx) error {
		if err := expireDecideLeaseTx(t, p); err != nil {
			return err
		}
		var cur string
		if e := t.QueryRow(`SELECT status FROM projects WHERE id=?`, p).Scan(&cur); e != nil {
			return e
		}
		if cur == "completed" {
			return fmt.Errorf("completed projects cannot change status")
		}
		if status != "active" && status != "stopped" {
			return fmt.Errorf("invalid status")
		}
		if status == "active" && cur != "active" {
			if err := resumeRoutesTx(t, p); err != nil {
				return err
			}
		}
		if _, e := t.Exec(`UPDATE projects SET status=?,failure_reason='',decide_worker=CASE WHEN ?='stopped' THEN NULL ELSE decide_worker END,decide_trigger=CASE WHEN ?='stopped' THEN NULL ELSE decide_trigger END,decide_started_at=CASE WHEN ?='stopped' THEN NULL ELSE decide_started_at END,decide_last_heartbeat_at=CASE WHEN ?='stopped' THEN NULL ELSE decide_last_heartbeat_at END WHERE id=?`, status, status, status, status, status, p); e != nil {
			return e
		}
		if status == "stopped" {
			if _, e := t.Exec(`UPDATE project_target_health SET run_id=lower(hex(randomblob(16))),preprocessing_json='{"status":"pending"}',status='pending',analysis_json='',failure_counts_json='[]',last_checked_at='',last_error='',analysis_failures=0,retry_after='' WHERE project_id=?`, p); e != nil {
				return e
			}
			_, e := t.Exec(`UPDATE steps SET worker=NULL WHERE project_id=? AND concluded_at IS NULL`, p)
			if e != nil {
				return e
			}
		}
		if status == "active" && cur != "active" {
			if _, e := t.Exec(`UPDATE project_target_health SET run_id=lower(hex(randomblob(16))),preprocessing_json='{"status":"pending"}',status='pending',analysis_json='',failure_counts_json='[]',last_checked_at='',last_error='',analysis_failures=0,retry_after='' WHERE project_id=?`, p); e != nil {
				return e
			}
			if _, e := t.Exec(`DELETE FROM worker_decide_state WHERE project_id=?`, p); e != nil {
				return e
			}
			if _, e := t.Exec(`DELETE FROM worker_retry_state WHERE project_id=? AND task_type='decide'`, p); e != nil {
				return e
			}
			if err := queueDecideTx(t, p, "project_resumed", ""); err != nil {
				return err
			}
		}
		var err error
		out, err = projectFrom(t.QueryRow(`SELECT `+projectSelectColumns+` FROM projects WHERE id=?`, p))
		return err
	})
	return out, e
}

func (d *DB) ClaimDecide(ctx context.Context, p string, r ClaimDecide) (Project, error) {
	var out Project
	e := tx(ctx, d, func(t *sql.Tx) error {
		if err := requireText("worker", r.Worker); err != nil {
			return err
		}
		if err := requireText("trigger", r.Trigger); err != nil {
			return err
		}
		r.Worker, r.Trigger = clean(r.Worker), clean(r.Trigger)
		if _, e := checkActive(t, p); e != nil {
			return e
		}
		if err := expireDecideLeaseTx(t, p); err != nil {
			return err
		}
		var w sql.NullString
		if e := t.QueryRow(`SELECT decide_worker FROM projects WHERE id=?`, p).Scan(&w); e != nil {
			return e
		}
		if w.Valid && w.String != r.Worker {
			return fmt.Errorf("project decide is currently claimed by %s", w.String)
		}
		v := now()
		if _, err := t.Exec(`UPDATE projects SET decide_worker=?,decide_trigger=?,decide_started_at=COALESCE(decide_started_at,?),decide_last_heartbeat_at=? WHERE id=?`, r.Worker, r.Trigger, v, v, p); err != nil {
			return err
		}
		var err error
		out, err = projectFrom(t.QueryRow(`SELECT `+projectSelectColumns+` FROM projects WHERE id=?`, p))
		return err
	})
	return out, e
}
func (d *DB) ReleaseDecide(ctx context.Context, p, w string) error {
	return tx(ctx, d, func(t *sql.Tx) error {
		if err := requireText("worker", w); err != nil {
			return err
		}
		w = clean(w)
		if _, err := checkActive(t, p); err != nil {
			return err
		}
		if err := expireDecideLeaseTx(t, p); err != nil {
			return err
		}
		var c sql.NullString
		if e := t.QueryRow(`SELECT decide_worker FROM projects WHERE id=?`, p).Scan(&c); e != nil {
			return e
		}
		if c.Valid && c.String != w {
			return fmt.Errorf("project decide is currently claimed by %s", c.String)
		}
		_, e := t.Exec(`UPDATE projects SET decide_worker=NULL,decide_trigger=NULL,decide_started_at=NULL,decide_last_heartbeat_at=NULL WHERE id=?`, p)
		return e
	})
}
func (d *DB) Reopen(ctx context.Context, p string, r Reopen) (ReopenResult, error) {
	var out ReopenResult
	e := tx(ctx, d, func(t *sql.Tx) error {
		if err := expireDecideLeaseTx(t, p); err != nil {
			return err
		}
		if err := requireText("description", r.Description); err != nil {
			return err
		}
		if err := requireText("creator", r.Creator); err != nil {
			return err
		}
		r.Description, r.Creator = clean(r.Description), clean(r.Creator)
		var st string
		if e := t.QueryRow(`SELECT status FROM projects WHERE id=?`, p).Scan(&st); e != nil {
			return e
		}
		if st != "completed" {
			return fmt.Errorf("project is not completed")
		}
		if err := resumeRoutesTx(t, p); err != nil {
			return err
		}
		v := now()
		rows, e := t.Query(`SELECT id,description,parent_goal_id,priority,created_at FROM goals WHERE project_id=? AND status='completed'`, p)
		if e != nil {
			return e
		}
		var first *Goal
		var ids []string
		for rows.Next() {
			var id, desc, created string
			var par sql.NullString
			var pri int
			if e = rows.Scan(&id, &desc, &par, &pri, &created); e != nil {
				rows.Close()
				return e
			}
			if first == nil {
				g := Goal{ID: id, Description: desc, ParentGoalID: nullable(par), Status: "active", Priority: pri, CreatedAt: created, From: []string{}}
				first = &g
			}
			ids = append(ids, id)
		}
		rows.Close()
		// Preserve the facts that proved the completed goals. The external
		// feedback step uses these as its input edges, while clearing the old
		// goal_sources lets the reopened goals be completed with fresh evidence.
		sourceRows, e := t.Query(`SELECT DISTINCT fact_id FROM goal_sources WHERE project_id=? ORDER BY rowid`, p)
		if e != nil {
			return e
		}
		var reopenSources []string
		for sourceRows.Next() {
			var factID string
			if e = sourceRows.Scan(&factID); e != nil {
				sourceRows.Close()
				return e
			}
			reopenSources = append(reopenSources, factID)
		}
		if e = sourceRows.Err(); e != nil {
			sourceRows.Close()
			return e
		}
		sourceRows.Close()
		if len(reopenSources) == 0 {
			reopenSources = []string{"origin"}
		}
		if _, e = t.Exec(`DELETE FROM goal_sources WHERE project_id=?`, p); e != nil {
			return e
		}
		if _, e = t.Exec(`UPDATE goals SET status='active',completed_at=NULL,completion_description=NULL,completed_by=NULL WHERE project_id=?`, p); e != nil {
			return e
		}
		fid, e := nextID(t, p, "fact", "f")
		if e != nil {
			return e
		}
		sid, e := nextID(t, p, "step", "s")
		if e != nil {
			return e
		}
		if _, e = t.Exec(`INSERT INTO facts(id,project_id,description) VALUES(?,?,?)`, fid, p, r.Description); e != nil {
			return e
		}
		if _, e = t.Exec(`INSERT INTO steps(id,project_id,description,creator,worker,last_heartbeat_at,created_at,to_fact_id,concluded_at) VALUES(?,?, 'external_feedback',?,?,?,?,?,?)`, sid, p, r.Creator, r.Creator, v, v, fid, v); e != nil {
			return e
		}
		for _, sourceID := range reopenSources {
			if _, e = t.Exec(`INSERT INTO step_sources(step_id,project_id,fact_id) VALUES(?,?,?)`, sid, p, sourceID); e != nil {
				return e
			}
		}
		_, e = t.Exec(`UPDATE projects SET status='active',decide_worker=NULL,decide_trigger=NULL,decide_started_at=NULL,decide_last_heartbeat_at=NULL WHERE id=?`, p)
		if e == nil {
			_, e = t.Exec(`UPDATE project_target_health SET run_id=lower(hex(randomblob(16))),preprocessing_json='{"status":"pending"}',status='pending',analysis_json='',failure_counts_json='[]',last_checked_at='',last_error='',analysis_failures=0,retry_after='' WHERE project_id=?`, p)
		}
		if e != nil {
			return e
		}
		pp, e := projectFrom(t.QueryRow(`SELECT `+projectSelectColumns+` FROM projects WHERE id=?`, p))
		if e != nil {
			return e
		}
		out.Project = pp
		out.Fact = Fact{ID: fid, Description: r.Description}
		out.Step = Step{ID: sid, Description: "external_feedback", Creator: r.Creator, Worker: &r.Creator, CreatedAt: v, To: &fid, ConcludedAt: &v, From: reopenSources}
		out.Goal = first
		_ = ids
		return nil
	})
	return out, e
}

type exportProject struct {
	Title  string `yaml:"title"`
	Origin string `yaml:"origin"`
}

type exportFact struct {
	Title       string `yaml:"title,omitempty"`
	Detail      string `yaml:"detail,omitempty"`
	ResultType  string `yaml:"result_type,omitempty"`
	ID          string `yaml:"id"`
	Description string `yaml:"description"`
}

type exportGoal struct {
	ID                    string   `yaml:"id"`
	Description           string   `yaml:"description"`
	ParentGoalID          *string  `yaml:"parent_goal_id"`
	Status                string   `yaml:"status"`
	Priority              int      `yaml:"priority"`
	CreatedAt             string   `yaml:"created_at"`
	CompletedAt           *string  `yaml:"completed_at"`
	CompletionDescription *string  `yaml:"completion_description"`
	CompletedBy           *string  `yaml:"completed_by"`
	From                  []string `yaml:"from"`
}

type exportHint struct {
	Content   string `yaml:"content"`
	Creator   string `yaml:"creator"`
	CreatedAt string `yaml:"created_at"`
}

type exportStep struct {
	ID          string           `yaml:"id"`
	RouteID     *string          `yaml:"route_id,omitempty"`
	Result      *ExecutionResult `yaml:"result,omitempty"`
	From        []string         `yaml:"from"`
	To          *string          `yaml:"to"`
	Description string           `yaml:"description"`
	GoalID      *string          `yaml:"goal_id"`
	Priority    int              `yaml:"priority"`
	Creator     string           `yaml:"creator"`
	Worker      *string          `yaml:"worker"`
	Abandoned   bool             `yaml:"abandoned"`
	CreatedAt   string           `yaml:"created_at"`
	ConcludedAt *string          `yaml:"concluded_at"`
}

type exportFinding struct {
	ID          string         `yaml:"id"`
	Title       string         `yaml:"title"`
	Description string         `yaml:"description"`
	Severity    string         `yaml:"severity"`
	Kind        string         `yaml:"kind"`
	Data        map[string]any `yaml:"data"`
	FactID      *string        `yaml:"fact_id"`
	CreatedAt   string         `yaml:"created_at"`
}

type exportDocument struct {
	Flags        []Flag          `yaml:"flags,omitempty"`
	Routes       []Route         `yaml:"routes,omitempty"`
	RouteRecords []RouteRecord   `yaml:"route_records,omitempty"`
	Project      exportProject   `yaml:"project"`
	Goals        []exportGoal    `yaml:"goals,omitempty"`
	Hints        []exportHint    `yaml:"hints,omitempty"`
	Facts        []exportFact    `yaml:"facts"`
	Steps        []exportStep    `yaml:"steps,omitempty"`
	Findings     []exportFinding `yaml:"findings,omitempty"`
}

func exportTimestamp(value string) string {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return value
	}
	return parsed.Local().Format("2006-01-02 15:04:05")
}

func (d *DB) ExportYAML(ctx context.Context, p string) (string, error) {
	detail, err := d.GetProject(ctx, p)
	if err != nil {
		return "", err
	}
	origin := ""
	facts := make([]exportFact, 0, len(detail.Facts))
	for _, fact := range detail.Facts {
		facts = append(facts, exportFact{ID: fact.ID, Description: fact.Description, Title: fact.Title, Detail: fact.Detail, ResultType: fact.ResultType})
		if fact.ID == "origin" {
			origin = fact.Description
		}
	}
	goals := make([]exportGoal, 0, len(detail.Goals))
	for _, goal := range detail.Goals {
		goals = append(goals, exportGoal{ID: goal.ID, Description: goal.Description, ParentGoalID: goal.ParentGoalID, Status: goal.Status, Priority: goal.Priority, CreatedAt: exportTimestamp(goal.CreatedAt), CompletedAt: mapExportTime(goal.CompletedAt), CompletionDescription: goal.CompletionDescription, CompletedBy: goal.CompletedBy, From: goal.From})
	}
	hints := make([]exportHint, 0, len(detail.Hints))
	for _, hint := range detail.Hints {
		hints = append(hints, exportHint{Content: normalizeHint(hint.Content), Creator: hint.Creator, CreatedAt: exportTimestamp(hint.CreatedAt)})
	}
	steps := make([]exportStep, 0, len(detail.Steps))
	orderedSteps := append([]Step(nil), detail.Steps...)
	sort.SliceStable(orderedSteps, func(i, j int) bool { return orderedSteps[i].CreatedAt < orderedSteps[j].CreatedAt })
	for _, step := range orderedSteps {
		steps = append(steps, exportStep{ID: step.ID, RouteID: step.RouteID, Result: step.Result, From: step.From, To: step.To, Description: step.Description, GoalID: step.GoalID, Priority: step.Priority, Creator: step.Creator, Worker: step.Worker, Abandoned: step.Abandoned, CreatedAt: exportTimestamp(step.CreatedAt), ConcludedAt: mapExportTime(step.ConcludedAt)})
	}
	findings := make([]exportFinding, 0, len(detail.Findings))
	for _, finding := range detail.Findings {
		findings = append(findings, exportFinding{ID: finding.ID, Title: finding.Title, Description: finding.Description, Severity: finding.Severity, Kind: finding.Kind, Data: finding.Data, FactID: finding.FactID, CreatedAt: exportTimestamp(finding.CreatedAt)})
	}
	records, err := d.RouteRecords(ctx, p)
	if err != nil {
		return "", err
	}
	flags, err := d.ListFlags(ctx, p)
	if err != nil {
		return "", err
	}
	doc := exportDocument{Flags: flags, Routes: detail.Routes, RouteRecords: records, Project: exportProject{Title: detail.Project.Title, Origin: origin}, Goals: goals, Hints: hints, Facts: facts, Steps: steps, Findings: findings}
	b, err := yaml.Marshal(doc)
	return string(b), err
}

func mapExportTime(value *string) *string {
	if value == nil {
		return nil
	}
	formatted := exportTimestamp(*value)
	return &formatted
}

type exportEvent struct {
	at   string
	seq  int
	text string
}

// ExportTimeline emits the project's human-readable graph replay format.
func (d *DB) ExportTimeline(ctx context.Context, p string) (string, error) {
	detail, err := d.GetProject(ctx, p)
	if err != nil {
		return "", err
	}
	facts := map[string]string{}
	for _, fact := range detail.Facts {
		facts[fact.ID] = fact.Description
	}
	goalText := make([]string, 0)
	for _, goal := range detail.Goals {
		if goal.ParentGoalID == nil {
			goalText = append(goalText, goal.Description)
		}
	}
	events := []exportEvent{}
	seq := 0
	add := func(at, text string) {
		events = append(events, exportEvent{at: at, seq: seq, text: text})
		seq++
	}
	origin := facts["origin"]
	add(detail.Project.CreatedAt, fmt.Sprintf("[%s] PROJECT CREATED\n  origin: %s\n  goal: %s", exportTimestamp(detail.Project.CreatedAt), origin, strings.Join(goalText, ", ")))
	goals := append([]Goal(nil), detail.Goals...)
	sort.SliceStable(goals, func(i, j int) bool { return goals[i].CreatedAt < goals[j].CreatedAt })
	for _, goal := range goals {
		add(goal.CreatedAt, fmt.Sprintf("[%s] GOAL CREATED %s\n  %s", exportTimestamp(goal.CreatedAt), goal.ID, goal.Description))
	}
	for _, hint := range detail.Hints {
		add(hint.CreatedAt, fmt.Sprintf("[%s] HINT by %s\n  %s", exportTimestamp(hint.CreatedAt), hint.Creator, normalizeHint(hint.Content)))
	}
	steps := append([]Step(nil), detail.Steps...)
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].CreatedAt < steps[j].CreatedAt })
	for _, step := range steps {
		meta := fmt.Sprintf("  from: %s", strings.Join(step.From, ", "))
		if step.GoalID != nil {
			meta += "\n  goal: " + *step.GoalID
		}
		if step.Priority != 0 {
			meta += fmt.Sprintf("\n  priority: %d", step.Priority)
		}
		if step.Worker != nil && step.ConcludedAt == nil {
			meta += "\n  worker: " + *step.Worker + " (in progress)"
		}
		add(step.CreatedAt, fmt.Sprintf("[%s] STEP DECLARED %s by %s\n%s\n  %s", exportTimestamp(step.CreatedAt), step.ID, step.Creator, meta, step.Description))
		if step.ConcludedAt == nil || step.To == nil {
			continue
		}
		actor := step.Creator
		if step.Worker != nil && *step.Worker != "" {
			actor = *step.Worker
		}
		add(*step.ConcludedAt, fmt.Sprintf("[%s] STEP CONCLUDED %s by %s\n  from: %s\n  produced: %s\n  %s", exportTimestamp(*step.ConcludedAt), step.ID, actor, strings.Join(step.From, ", "), *step.To, facts[*step.To]))
	}
	for _, goal := range goals {
		if goal.CompletedAt == nil {
			continue
		}
		actor := "unknown"
		if goal.CompletedBy != nil && *goal.CompletedBy != "" {
			actor = *goal.CompletedBy
		}
		evidence := goal.CompletionDescription
		if evidence == nil || *evidence == "" {
			evidence = &goal.Description
		}
		add(*goal.CompletedAt, fmt.Sprintf("[%s] GOAL COMPLETED %s by %s\n  from: %s\n  %s", exportTimestamp(*goal.CompletedAt), goal.ID, actor, strings.Join(goal.From, ", "), *evidence))
	}
	for _, finding := range detail.Findings {
		add(finding.CreatedAt, fmt.Sprintf("[%s] FINDING %s [%s] %s\n  %s", exportTimestamp(finding.CreatedAt), finding.Kind, finding.Severity, finding.Title, finding.Description))
	}
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].at == events[j].at {
			return events[i].seq < events[j].seq
		}
		return events[i].at < events[j].at
	})
	var builder strings.Builder
	for i, event := range events {
		if i > 0 {
			builder.WriteString("\n\n")
		}
		builder.WriteString(event.text)
	}
	if len(events) > 0 {
		builder.WriteByte('\n')
	}
	return builder.String(), nil
}
