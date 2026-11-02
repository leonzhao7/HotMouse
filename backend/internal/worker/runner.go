package worker

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hotmouse/backend/internal/core"
)

// Runner is one serialized Decide -> Execute loop. The optional command is a
// local model/agent adapter: it receives a graph snapshot and task contract in
// environment variables and returns one JSON contract on stdout.
type Runner struct {
	DB       *core.DB
	Interval time.Duration
}

type workerConfig struct {
	driver          string
	command         string
	env             map[string]string
	enabled         bool
	workspaceRoot   string
	completedAction string
	contextRoot     string
	runtime         core.WorkerRuntime
}

func New(db *core.DB) *Runner { return &Runner{DB: db, Interval: time.Second} }

func (r *Runner) Start(ctx context.Context) {
	if r.Interval <= 0 {
		r.Interval = time.Second
	}
	go func() {
		t := time.NewTicker(r.Interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				r.RunOnce(ctx)
			}
		}
	}()
}

func (r *Runner) RunOnce(ctx context.Context) {
	if !r.DB.WorkerMu.TryLock() {
		return
	}
	defer r.DB.WorkerMu.Unlock()
	defer r.cleanupCompletedWorkspaces(context.Background())
	r.cleanupCompletedWorkspaces(ctx)
	var project, step, description string
	now := time.Now().UTC().Format(time.RFC3339Nano)
	err := r.DB.SQL.QueryRowContext(ctx, `SELECT s.project_id,s.id,s.description FROM worker_execute_recovery x JOIN steps s ON s.project_id=x.project_id AND s.id=x.step_id JOIN projects p ON p.id=s.project_id WHERE p.status='active' AND s.abandoned=0 AND ((x.payload_json<>'' AND x.status IN ('running','commit_pending')) OR (x.status='summary_pending' AND x.session_id<>'' AND s.to_fact_id IS NULL)) AND (s.to_fact_id IS NOT NULL OR (s.worker IS NULL AND NOT EXISTS (SELECT 1 FROM worker_retry_state r WHERE r.project_id=s.project_id AND r.task_type='execute' AND julianday(r.retry_after)>julianday(?)))) ORDER BY x.created_at LIMIT 1`, now).Scan(&project, &step, &description)
	if err == sql.ErrNoRows {
		err = r.DB.SQL.QueryRowContext(ctx, `SELECT s.project_id,s.id,s.description FROM steps s WHERE s.to_fact_id IS NULL AND s.abandoned=0 AND s.worker IS NULL AND s.project_id IN (SELECT id FROM projects WHERE status='active') AND NOT EXISTS (SELECT 1 FROM worker_execute_recovery x WHERE x.project_id=s.project_id AND x.step_id=s.id) AND NOT EXISTS (SELECT 1 FROM worker_retry_state r WHERE r.project_id=s.project_id AND r.task_type='execute' AND julianday(r.retry_after)>julianday(?)) ORDER BY s.priority DESC,s.created_at LIMIT 1`, now).Scan(&project, &step, &description)
	}
	if err == nil {
		_, _ = r.execute(ctx, project, step, description)
		return
	}
	rows, err := r.DB.SQL.QueryContext(ctx, `SELECT DISTINCT s.project_id FROM steps s JOIN projects p ON p.id=s.project_id WHERE p.status='active' AND s.to_fact_id IS NULL AND s.abandoned=0`)
	if err != nil {
		return
	}
	projects := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			projects = append(projects, id)
		}
	}
	_ = rows.Close()
	if len(projects) > 0 {
		for _, id := range projects {
			_, _ = r.DB.GetProject(ctx, id)
		}
	}
	r.decide(ctx)
}

func (r *Runner) ExecuteNow(ctx context.Context, project, step string) (core.ConcludeResult, error) {
	if !r.DB.WorkerMu.TryLock() {
		return core.ConcludeResult{}, fmt.Errorf("local worker is busy")
	}
	defer r.DB.WorkerMu.Unlock()
	defer r.cleanupCompletedWorkspaces(context.Background())
	var description string
	if err := r.DB.SQL.QueryRowContext(ctx, `SELECT description FROM steps WHERE project_id=? AND id=?`, project, step).Scan(&description); err != nil {
		return core.ConcludeResult{}, err
	}
	return r.execute(ctx, project, step, description)
}

func (r *Runner) execute(parent context.Context, project, step, description string) (core.ConcludeResult, error) {
	started := time.Now().UTC().Format(time.RFC3339Nano)
	worker := r.workerName()
	history := r.historyStart(project, step, "execute", started, worker)
	checkpoint, err := r.loadExecuteRecovery(parent, project, step)
	if err != nil {
		r.historyFinish(history, "failed", err.Error())
		return core.ConcludeResult{}, err
	}
	var output []byte
	recovery := checkpoint != nil && len(checkpoint.payload) > 0
	if recovery {
		output = checkpoint.payload
	}
	if recovery {
		conclusion, parseErr := parseExecute(output)
		if parseErr != nil {
			r.markExecuteAttention(project, step)
			r.historyFinish(history, "commit_pending", parseErr.Error())
			return core.ConcludeResult{}, parseErr
		}
		conclusion.Worker = worker
		if committed, ok, verifyErr := r.committedConclusion(parent, project, step, conclusion); verifyErr != nil {
			r.markExecuteAttention(project, step)
			r.historyFinish(history, "commit_pending", verifyErr.Error())
			return core.ConcludeResult{}, verifyErr
		} else if ok {
			r.clearExecuteRecovery(project, step)
			r.historyFinish(history, "completed", conclusion.Description)
			r.recordTaskOutcome(project, "execute", "success")
			return committed, nil
		}
		var existingFact sql.NullString
		if err := r.DB.SQL.QueryRowContext(parent, `SELECT to_fact_id FROM steps WHERE project_id=? AND id=?`, project, step).Scan(&existingFact); err != nil {
			r.historyFinish(history, "failed", err.Error())
			return core.ConcludeResult{}, err
		}
		if existingFact.Valid {
			err := fmt.Errorf("committed step does not match its recovery payload")
			r.markExecuteAttention(project, step)
			r.historyFinish(history, "commit_pending", err.Error())
			return core.ConcludeResult{}, err
		}
	}
	leaseCtx, cancel := context.WithCancel(parent)
	defer cancel()
	if _, err := r.DB.HeartbeatStep(leaseCtx, project, step, worker); err != nil {
		r.historyFinish(history, "claim_failed", err.Error())
		return core.ConcludeResult{}, err
	}
	stopHeartbeat := r.keepStepAlive(leaseCtx, cancel, project, step, worker)
	defer stopHeartbeat()
	if !recovery {
		graph, err := r.DB.ExportYAML(leaseCtx, project)
		if err != nil {
			r.releaseStep(project, step, worker)
			r.historyFinish(history, "failed", err.Error())
			r.recordTaskOutcome(project, "execute", "failed")
			return core.ConcludeResult{}, err
		}
		cfg := r.config(leaseCtx)
		if !cfg.enabled {
			r.releaseStep(project, step, worker)
			r.historyFinish(history, "disabled", "worker disabled")
			return core.ConcludeResult{}, fmt.Errorf("worker disabled")
		}
		output = []byte(fmt.Sprintf("local worker completed step: %s", description))
		provider := builtinProvider(cfg)
		if checkpoint != nil && !provider {
			_, _ = r.DB.SQL.ExecContext(leaseCtx, `UPDATE worker_execute_recovery SET status='needs_attention' WHERE project_id=? AND step_id=?`, project, step)
			r.releaseStep(project, step, worker)
			err := fmt.Errorf("provider recovery requires its original driver")
			r.historyFinish(history, "failed", err.Error())
			return core.ConcludeResult{}, err
		}
		if checkpoint == nil && (cfg.command != "" || provider) {
			if err := r.saveExecuteStarted(leaseCtx, project, step, worker); err != nil {
				r.releaseStep(project, step, worker)
				r.historyFinish(history, "failed", err.Error())
				r.recordTaskOutcome(project, "execute", "failed")
				return core.ConcludeResult{}, err
			}
		}
		workspace, workspaceErr := r.ensureWorkspace(project, cfg)
		if workspaceErr != nil {
			r.markExecuteAttention(project, step)
			r.releaseStep(project, step, worker)
			r.historyFinish(history, "failed", workspaceErr.Error())
			r.recordTaskOutcome(project, "execute", "failed")
			return core.ConcludeResult{}, workspaceErr
		}
		if cfg.command != "" || provider {
			if provider {
				output, err = r.executeProvider(leaseCtx, cfg, workspace, project, step, description, graph, checkpoint)
			} else {
				var vars map[string]string
				vars, err = r.commandVars(leaseCtx, project, step, description, graph, "execute", cfg, workspace)
				if err == nil {
					process := runProcess(leaseCtx, []string{"sh", "-c", cfg.command}, cfg.env, workspace, vars, time.Duration(cfg.runtime.ExecuteTimeoutSeconds)*time.Second, nil)
					output, err = process.stdout, process.err
				}
			}
			if err != nil {
				r.markExecuteAttention(project, step)
				r.releaseStep(project, step, worker)
				status, outcome := "failed", "unhealthy"
				if provider {
					var recoveryStatus string
					_ = r.DB.SQL.QueryRow(`SELECT status FROM worker_execute_recovery WHERE project_id=? AND step_id=?`, project, step).Scan(&recoveryStatus)
					if recoveryStatus == "summary_pending" {
						status, outcome = "summary_pending", "failed"
					}
				}
				r.historyFinish(history, status, err.Error())
				r.recordTaskOutcome(project, "execute", outcome)
				return core.ConcludeResult{}, err
			}
		} else if cfg.driver != "mock" {
			r.releaseStep(project, step, worker)
			r.historyFinish(history, "failed", "worker command is empty")
			r.recordTaskOutcome(project, "execute", "failed")
			return core.ConcludeResult{}, fmt.Errorf("worker command is empty")
		}
	}
	conclusion, err := parseExecute(output)
	if err != nil {
		if isWorkerRejection(err) {
			r.clearExecuteRecovery(project, step)
		} else if !recovery {
			r.markExecuteAttention(project, step)
		}
		r.releaseStep(project, step, worker)
		status := "invalid_payload"
		if isWorkerRejection(err) {
			status = "rejected"
			r.recordTaskOutcome(project, "execute", "rejected")
		} else {
			r.recordTaskOutcome(project, "execute", "failed")
		}
		r.historyFinish(history, status, err.Error())
		return core.ConcludeResult{}, err
	}
	conclusion.Worker = worker
	if !recovery {
		if err := r.saveExecuteRecovery(leaseCtx, project, step, output); err != nil {
			r.markExecuteAttention(project, step)
			r.releaseStep(project, step, worker)
			r.historyFinish(history, "failed", err.Error())
			r.recordTaskOutcome(project, "execute", "failed")
			return core.ConcludeResult{}, err
		}
	}
	result, err := r.DB.ConcludeStep(leaseCtx, project, step, conclusion)
	if err != nil {
		if committed, ok, verifyErr := r.committedConclusion(leaseCtx, project, step, conclusion); verifyErr != nil {
			r.markExecuteAttention(project, step)
			r.releaseStep(project, step, worker)
			r.historyFinish(history, "commit_pending", verifyErr.Error())
			r.recordTaskOutcome(project, "execute", "failed")
			return core.ConcludeResult{}, verifyErr
		} else if ok {
			result, err = committed, nil
		}
	}
	if err != nil {
		r.markExecuteAttention(project, step)
		r.releaseStep(project, step, worker)
		r.historyFinish(history, "commit_pending", err.Error())
		r.recordTaskOutcome(project, "execute", "failed")
		return core.ConcludeResult{}, err
	}
	r.clearExecuteRecovery(project, step)
	r.historyFinish(history, "completed", conclusion.Description)
	r.recordTaskOutcome(project, "execute", "success")
	return result, nil
}

func (r *Runner) committedConclusion(ctx context.Context, project, stepID string, conclusion core.Conclude) (core.ConcludeResult, bool, error) {
	detail, err := r.DB.GetProject(ctx, project)
	if err != nil {
		return core.ConcludeResult{}, false, err
	}
	var step *core.Step
	for i := range detail.Steps {
		if detail.Steps[i].ID == stepID {
			step = &detail.Steps[i]
			break
		}
	}
	if step == nil || step.To == nil {
		return core.ConcludeResult{}, false, nil
	}
	var fact *core.Fact
	for i := range detail.Facts {
		if detail.Facts[i].ID == *step.To {
			fact = &detail.Facts[i]
			break
		}
	}
	if fact == nil || fact.Description != strings.TrimSpace(conclusion.Description) {
		return core.ConcludeResult{}, false, nil
	}
	var finding *core.Finding
	for i := range detail.Findings {
		if detail.Findings[i].FactID != nil && *detail.Findings[i].FactID == fact.ID {
			finding = &detail.Findings[i]
			break
		}
	}
	if conclusion.Finding == nil {
		if finding != nil {
			return core.ConcludeResult{}, false, nil
		}
	} else {
		if finding == nil || !sameFinding(*finding, *conclusion.Finding) {
			return core.ConcludeResult{}, false, nil
		}
	}
	return core.ConcludeResult{Fact: fact, Step: step, Finding: finding}, true, nil
}

func sameFinding(existing core.Finding, expected core.CreateFinding) bool {
	if expected.Severity == "" {
		expected.Severity = "info"
	}
	if expected.Kind == "" {
		expected.Kind = "finding"
	}
	if expected.Data == nil {
		expected.Data = map[string]any{}
	}
	if existing.Title != strings.TrimSpace(expected.Title) || existing.Description != strings.TrimSpace(expected.Description) || existing.Severity != expected.Severity || existing.Kind != expected.Kind {
		return false
	}
	existingData, _ := json.Marshal(existing.Data)
	expectedData, _ := json.Marshal(expected.Data)
	return string(existingData) == string(expectedData)
}

func (r *Runner) decide(parent context.Context) {
	project := r.nextDecideProject(parent)
	if project == "" {
		return
	}
	r.decideProject(parent, project)
}

func (r *Runner) nextDecideProject(ctx context.Context) string {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := r.DB.SQL.QueryContext(ctx, `SELECT p.id FROM projects p WHERE p.status='active' AND NOT EXISTS (SELECT 1 FROM worker_retry_state r WHERE r.project_id=p.id AND r.task_type='decide' AND julianday(r.retry_after)>julianday(?)) ORDER BY p.created_at,p.id`, now)
	if err != nil {
		return ""
	}
	projects := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			projects = append(projects, id)
		}
	}
	rows.Close()
	cfg := r.config(ctx)
	if !cfg.enabled {
		return ""
	}
	for _, project := range projects {
		detail, err := r.DB.GetProject(ctx, project)
		if err != nil {
			continue
		}
		if detail.Project.Decide != nil && detail.Project.Decide.Worker != r.workerName() {
			continue
		}
		open := false
		for _, step := range detail.Steps {
			if step.To == nil && !step.Abandoned {
				open = true
				break
			}
		}
		if open || (cfg.driver == "mock" && len(detail.Steps) > 0) {
			continue
		}
		if cfg.driver != "mock" {
			var previous string
			if err := r.DB.SQL.QueryRowContext(ctx, `SELECT fingerprint FROM worker_decide_state WHERE project_id=?`, project).Scan(&previous); err == nil && previous == graphFingerprint(detail) {
				continue
			}
		}
		return project
	}
	return ""
}

func (r *Runner) decideProject(parent context.Context, project string) {
	worker := r.workerName()
	history := r.historyStart(project, "", "decide", time.Now().UTC().Format(time.RFC3339Nano), worker)
	if _, err := r.DB.ClaimDecide(parent, project, core.ClaimDecide{Worker: worker, Trigger: "scheduler"}); err != nil {
		r.historyFinish(history, "claim_failed", err.Error())
		return
	}
	leaseCtx, cancel := context.WithCancel(parent)
	defer cancel()
	stopHeartbeat := r.keepDecideAlive(leaseCtx, cancel, project, worker)
	defer stopHeartbeat()
	detail, err := r.DB.GetProject(leaseCtx, project)
	if err != nil {
		r.releaseDecide(project, worker)
		r.historyFinish(history, "failed", err.Error())
		r.recordTaskOutcome(project, "decide", "failed")
		return
	}
	graph, err := r.DB.ExportYAML(leaseCtx, project)
	if err != nil {
		r.releaseDecide(project, worker)
		r.historyFinish(history, "failed", err.Error())
		r.recordTaskOutcome(project, "decide", "failed")
		return
	}
	cfg := r.config(leaseCtx)
	if !cfg.enabled {
		r.releaseDecide(project, worker)
		r.historyFinish(history, "disabled", "worker disabled")
		return
	}
	if cfg.driver == "mock" && len(detail.Steps) > 0 {
		r.releaseDecide(project, worker)
		r.historyFinish(history, "released", "mock worker has no new decision")
		return
	}
	if cfg.driver != "mock" {
		fingerprint := graphFingerprint(detail)
		var previous string
		if err := r.DB.SQL.QueryRowContext(leaseCtx, `SELECT fingerprint FROM worker_decide_state WHERE project_id=?`, project).Scan(&previous); err == nil && previous == fingerprint {
			r.releaseDecide(project, worker)
			r.historyFinish(history, "released", "graph unchanged")
			return
		}
	}
	var payload []byte
	workspace, workspaceErr := r.ensureWorkspace(project, cfg)
	if workspaceErr != nil {
		r.releaseDecide(project, worker)
		r.historyFinish(history, "failed", workspaceErr.Error())
		r.recordTaskOutcome(project, "decide", "failed")
		return
	}
	if cfg.command != "" || builtinProvider(cfg) {
		vars, varsErr := r.commandVars(leaseCtx, project, "", "", graph, "decide", cfg, workspace)
		if varsErr != nil {
			err = varsErr
		} else if builtinProvider(cfg) {
			session := ""
			if canonicalDriver(cfg.driver) == "claudecode" {
				session, err = newSessionID()
			}
			if err == nil {
				payload, _, _, err = r.runProvider(leaseCtx, cfg, "decide", session, workspace, vars, time.Duration(cfg.runtime.DecideTimeoutSeconds)*time.Second, nil)
			}
		} else {
			process := runProcess(leaseCtx, []string{"sh", "-c", cfg.command}, cfg.env, workspace, vars, time.Duration(cfg.runtime.DecideTimeoutSeconds)*time.Second, nil)
			payload, err = process.stdout, process.err
		}
		if err != nil {
			r.releaseDecide(project, worker)
			r.historyFinish(history, "failed", err.Error())
			r.recordTaskOutcome(project, "decide", "unhealthy")
			return
		}
	} else if cfg.driver == "mock" {
		// Mock creates one starter Step per project and waits for explicit graph
		// input after that; it never invents an endless chain of work.
		if len(detail.Steps) == 0 {
			goalID := ""
			for _, g := range detail.Goals {
				if g.Status == "active" && g.ParentGoalID == nil {
					goalID = g.ID
					break
				}
			}
			payload, _ = json.Marshal(map[string]any{"steps": []any{map[string]any{"from": []string{"origin"}, "description": "advance goal: " + firstActiveGoal(detail.Goals), "goal_id": goalID, "priority": 0}}})
		} else {
			payload = []byte(`{}`)
		}
	} else {
		r.releaseDecide(project, worker)
		r.historyFinish(history, "failed", "worker command is empty")
		r.recordTaskOutcome(project, "decide", "failed")
		return
	}
	if err = applyDecide(r.DB, leaseCtx, project, detail, payload, worker); err != nil {
		r.releaseDecide(project, worker)
		status := "invalid_payload"
		if isWorkerRejection(err) {
			status = "rejected"
			r.recordTaskOutcome(project, "decide", "rejected")
		} else {
			r.recordTaskOutcome(project, "decide", "failed")
		}
		r.historyFinish(history, status, err.Error())
		return
	}
	updated, getErr := r.DB.GetProject(leaseCtx, project)
	if getErr != nil {
		r.releaseDecide(project, worker)
		r.historyFinish(history, "failed", getErr.Error())
		r.recordTaskOutcome(project, "decide", "failed")
		return
	}
	state := graphFingerprint(updated)
	_, _ = r.DB.SQL.Exec(`INSERT INTO worker_decide_state(project_id,fingerprint,updated_at) VALUES(?,?,?) ON CONFLICT(project_id) DO UPDATE SET fingerprint=excluded.fingerprint,updated_at=excluded.updated_at`, project, state, time.Now().UTC().Format(time.RFC3339))
	r.releaseDecide(project, worker)
	r.historyFinish(history, "completed", string(payload))
	r.recordTaskOutcome(project, "decide", "success")
}

func isWorkerRejection(err error) bool {
	return err != nil && (err.Error() == "decision rejected" || err.Error() == "execution rejected")
}

func decidePrompt(graph string) string {
	return `You are the HotMouse graph decision worker. Read the FGS YAML snapshot below and return exactly one raw JSON object, with no markdown or explanation.

Allowed response forms:
{"accepted":false,"reason":"..."}
{"accepted":true,"data":{"complete":{"goal_id":"g001","from":["f001"],"description":"evidence"}}}
{"accepted":true,"data":{"steps":[{"from":["f001"],"description":"one bounded verifiable action","goal_id":"g001","priority":1}]}}
{"accepted":true,"data":{"step_updates":[{"id":"s001","priority":1}],"sub_goals":[{"action":"create","description":"decompose a prerequisite","parent_goal_id":"g001","priority":1}]}}
{"accepted":true,"data":{}}

Complete at most one already-satisfied active goal, citing existing fact IDs. Otherwise propose at most three non-overlapping steps whose prerequisites are existing fact IDs. Do not invent fact, goal, or step IDs. Do not update a claimed or closed step. If there are no open steps, do not return an empty data object.

FGS YAML:
` + graph
}

func executePrompt(graph, step, description string) string {
	return `You are the HotMouse bounded step execution worker. Work only on the current Step and return exactly one raw JSON object, with no markdown or explanation.

Allowed response forms:
{"accepted":false,"reason":"policy_refusal"}
{"accepted":true,"data":{"description":"the latest concise confirmed result"}}
{"accepted":true,"data":{"description":"the latest confirmed result","finding":{"kind":"security_vulnerability","title":"Finding title","severity":"high","description":"Evidence-backed finding","data":{}}}}

Report a confirmed result or a confirmed blocker. Do not repeat the graph snapshot, add fact_id/source fields to a finding, or continue beyond this bounded Step. Use only finite commands and preserve concise evidence.

Current Step ID: ` + step + `
Current Step Description: ` + description + `

FGS YAML:
` + graph
}

func graphFingerprint(d core.ProjectDetail) string {
	b, _ := json.Marshal(struct {
		Facts []core.Fact
		Goals []core.Goal
		Steps []core.Step
		Hints []core.Hint
	}{d.Facts, d.Goals, d.Steps, d.Hints})
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:])
}

func firstActiveGoal(goals []core.Goal) string {
	for _, g := range goals {
		if g.Status == "active" && g.ParentGoalID == nil {
			return g.Description
		}
	}
	return "active goal"
}

func applyDecide(db *core.DB, ctx context.Context, project string, detail core.ProjectDetail, raw []byte, worker ...string) error {
	workerName := "local"
	if len(worker) > 0 && strings.TrimSpace(worker[0]) != "" {
		workerName = strings.TrimSpace(worker[0])
	}
	var root map[string]json.RawMessage
	if err := decodeObject(raw, &root); err != nil {
		return fmt.Errorf("invalid decide JSON: %w", err)
	}
	if accepted, ok := root["accepted"]; ok {
		var yes bool
		if err := json.Unmarshal(accepted, &yes); err != nil {
			return fmt.Errorf("accepted must be boolean")
		}
		if !yes {
			return fmt.Errorf("decision rejected")
		}
		var data map[string]json.RawMessage
		if err := json.Unmarshal(root["data"], &data); err != nil {
			return fmt.Errorf("accepted decision data must be an object")
		}
		root = data
	}
	allowed := map[string]bool{"complete": true, "steps": true, "step_updates": true, "sub_goals": true}
	for k := range root {
		if !allowed[k] {
			return fmt.Errorf("unexpected decide data key %q", k)
		}
	}
	if v, ok := root["complete"]; ok {
		if len(root) != 1 {
			return fmt.Errorf("complete cannot coexist with graph mutations")
		}
		var c struct {
			GoalID      string   `json:"goal_id"`
			From        []string `json:"from"`
			Description string   `json:"description"`
		}
		if err := json.Unmarshal(v, &c); err != nil {
			return err
		}
		if strings.TrimSpace(c.GoalID) == "" || strings.TrimSpace(c.Description) == "" || len(c.From) == 0 {
			return fmt.Errorf("complete requires goal_id, from and description")
		}
		_, err := db.CompleteGoal(ctx, project, c.GoalID, core.Completion{From: c.From, Description: c.Description, Worker: workerName})
		return err
	}
	var steps []struct {
		From        []string `json:"from"`
		Description string   `json:"description"`
		GoalID      *string  `json:"goal_id"`
		Priority    int      `json:"priority"`
	}
	if v, ok := root["steps"]; ok {
		if err := json.Unmarshal(v, &steps); err != nil {
			return err
		}
		if len(steps) > 3 {
			return fmt.Errorf("steps must contain at most 3 entries")
		}
	}
	openEmpty := true
	for _, s := range detail.Steps {
		if s.To == nil && !s.Abandoned {
			openEmpty = false
			break
		}
	}
	if openEmpty && len(steps) == 0 && len(root) > 0 {
		return fmt.Errorf("at least one step is required when open_steps is empty")
	}
	for _, s := range steps {
		if len(s.From) == 0 || strings.TrimSpace(s.Description) == "" {
			return fmt.Errorf("step requires non-empty from and description")
		}
		if _, err := db.CreateStep(ctx, project, core.CreateStep{From: s.From, Description: s.Description, GoalID: s.GoalID, Priority: s.Priority, Creator: workerName}); err != nil {
			return err
		}
	}
	var updates []map[string]any
	if v, ok := root["step_updates"]; ok {
		if err := json.Unmarshal(v, &updates); err != nil {
			return err
		}
	}
	for _, u := range updates {
		id, _ := u["id"].(string)
		if id == "" {
			return fmt.Errorf("step update requires id")
		}
		change := core.UpdateStep{}
		if a, _ := u["action"].(string); a == "abandon" {
			b := true
			change.Abandoned = &b
		} else {
			n, ok := u["priority"].(float64)
			if !ok {
				return fmt.Errorf("step update requires priority")
			}
			p := int(n)
			change.Priority = &p
		}
		if _, err := db.UpdateStep(ctx, project, id, change); err != nil {
			return err
		}
	}
	var goals []map[string]any
	if v, ok := root["sub_goals"]; ok {
		if err := json.Unmarshal(v, &goals); err != nil {
			return err
		}
	}
	for _, g := range goals {
		action, _ := g["action"].(string)
		if action != "create" {
			return fmt.Errorf("sub-goal delete is not supported by the local runner")
		}
		desc, _ := g["description"].(string)
		pri, _ := g["priority"].(float64)
		var parentID *string
		if id, ok := g["parent_goal_id"].(string); ok {
			parentID = &id
		}
		if _, err := db.CreateGoal(ctx, project, core.CreateGoal{Description: desc, Priority: int(pri), Creator: workerName, ParentGoalID: parentID}); err != nil {
			return err
		}
	}
	return nil
}

func parseExecute(raw []byte) (core.Conclude, error) {
	var root map[string]json.RawMessage
	if decodeObject(raw, &root) == nil {
		if v, ok := root["accepted"]; ok {
			var yes bool
			if err := json.Unmarshal(v, &yes); err != nil {
				return core.Conclude{}, fmt.Errorf("accepted must be boolean")
			}
			if !yes {
				return core.Conclude{}, fmt.Errorf("execution rejected")
			}
			var data map[string]json.RawMessage
			if err := json.Unmarshal(root["data"], &data); err != nil {
				return core.Conclude{}, fmt.Errorf("accepted execution data must be an object")
			}
			root = data
		}
		for k := range root {
			if k != "description" && k != "finding" {
				return core.Conclude{}, fmt.Errorf("unexpected execute data key %q", k)
			}
		}
		var out core.Conclude
		out.Worker = "local"
		if v, ok := root["description"]; ok {
			if err := json.Unmarshal(v, &out.Description); err != nil {
				return out, err
			}
		}
		if v, ok := root["finding"]; ok && string(v) != "null" {
			var findingFields map[string]json.RawMessage
			if err := json.Unmarshal(v, &findingFields); err != nil {
				return core.Conclude{}, err
			}
			for key := range findingFields {
				if key != "title" && key != "description" && key != "severity" && key != "kind" && key != "data" {
					return core.Conclude{}, fmt.Errorf("unexpected finding field %q", key)
				}
			}
			var f core.CreateFinding
			if err := json.Unmarshal(v, &f); err != nil {
				return out, err
			}
			if f.Title == "" || f.Description == "" {
				return out, fmt.Errorf("finding requires title and description")
			}
			if f.Severity == "" {
				f.Severity = "info"
			}
			if f.Kind == "" {
				f.Kind = "finding"
			}
			out.Finding = &f
		}
		if strings.TrimSpace(out.Description) == "" {
			return out, fmt.Errorf("description is required")
		}
		return out, nil
	}
	if strings.TrimSpace(string(raw)) == "" {
		return core.Conclude{}, fmt.Errorf("description is required")
	}
	if strings.ContainsAny(string(raw), "{}[]\x60") {
		return core.Conclude{}, fmt.Errorf("invalid structured execution response")
	}
	return core.Conclude{Worker: "local", Description: strings.TrimSpace(string(raw))}, nil
}

func (r *Runner) config(ctx context.Context) workerConfig {
	var cfg workerConfig
	var raw, runtimeJSON string
	var enabled int
	err := r.DB.SQL.QueryRowContext(ctx, `SELECT driver,command,environment_json,enabled,workspace_root,completed_action,context_root,runtime_json FROM worker_config WHERE id=1`).Scan(&cfg.driver, &cfg.command, &raw, &enabled, &cfg.workspaceRoot, &cfg.completedAction, &cfg.contextRoot, &runtimeJSON)
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal([]byte(raw), &cfg.env)
	cfg.runtime = core.DefaultWorkerRuntime()
	if err := json.Unmarshal([]byte(runtimeJSON), &cfg.runtime); err != nil || cfg.runtime.Validate() != nil {
		return workerConfig{}
	}
	cfg.enabled = enabled != 0
	cfg.contextRoot = core.ResolveProjectContextRoot(cfg.contextRoot)
	if cfg.completedAction != "remove" {
		cfg.completedAction = "keep"
	}
	return cfg
}
func (r *Runner) workspaceRoot(cfg workerConfig) (string, error) {
	root := strings.TrimSpace(cfg.workspaceRoot)
	if root == "" {
		root = r.DB.DefaultWorkspaceRoot()
	}
	root, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", err
	}
	if root == string(filepath.Separator) || root == "." {
		return "", fmt.Errorf("unsafe worker workspace root")
	}
	return root, nil
}

func (r *Runner) ensureWorkspace(project string, cfg workerConfig) (string, error) {
	if filepath.Base(project) != project || project == "." || project == ".." {
		return "", fmt.Errorf("invalid project workspace id")
	}
	root, err := r.workspaceRoot(cfg)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("create worker workspace root: %w", err)
	}
	dir := filepath.Join(root, project)
	info, statErr := os.Lstat(dir)
	created := false
	if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("worker workspace is a symlink")
	}
	if statErr == nil && !info.IsDir() {
		return "", fmt.Errorf("worker workspace is not a directory")
	}
	if statErr != nil && !os.IsNotExist(statErr) {
		return "", statErr
	}
	if os.IsNotExist(statErr) {
		if err := os.Mkdir(dir, 0o755); err != nil {
			return "", fmt.Errorf("create project workspace: %w", err)
		}
		created = true
	}
	marker := filepath.Join(dir, ".hotmouse-project")
	markerInfo, markerStatErr := os.Lstat(marker)
	if markerStatErr == nil && !markerInfo.Mode().IsRegular() {
		return "", fmt.Errorf("worker workspace marker is not a regular file")
	}
	if b, readErr := os.ReadFile(marker); readErr == nil {
		if string(b) != r.DB.WorkspaceMarker(project) {
			return "", fmt.Errorf("worker workspace marker belongs to another project")
		}
	} else if os.IsNotExist(readErr) {
		entries, readDirErr := os.ReadDir(dir)
		if readDirErr != nil {
			return "", readDirErr
		}
		if !created || len(entries) != 0 {
			return "", fmt.Errorf("worker workspace exists without a HotMouse marker")
		}
		if err := os.WriteFile(marker, []byte(r.DB.WorkspaceMarker(project)), 0o600); err != nil {
			return "", err
		}
	} else {
		return "", readErr
	}
	return dir, nil
}

func (r *Runner) cleanupCompletedWorkspaces(ctx context.Context) {
	cfg := r.config(ctx)
	if cfg.completedAction != "remove" {
		return
	}
	root, err := r.workspaceRoot(cfg)
	if err != nil {
		return
	}
	rows, err := r.DB.SQL.QueryContext(ctx, `SELECT id FROM projects WHERE status='completed'`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var project string
		if rows.Scan(&project) != nil || filepath.Base(project) != project || project == "." || project == ".." {
			continue
		}
		dir := filepath.Join(root, project)
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		marker := filepath.Join(dir, ".hotmouse-project")
		markerInfo, err := os.Lstat(marker)
		if err != nil || !markerInfo.Mode().IsRegular() {
			continue
		}
		b, err := os.ReadFile(marker)
		if err != nil || string(b) != r.DB.WorkspaceMarker(project) {
			continue
		}
		_ = os.RemoveAll(dir)
	}
}

func (r *Runner) commandVars(ctx context.Context, project, step, description, graph, taskType string, cfg workerConfig, workspace string) (map[string]string, error) {
	vars := map[string]string{
		"HOTMOUSE_TASK_TYPE":        taskType,
		"HOTMOUSE_PROJECT_ID":       project,
		"HOTMOUSE_STEP_ID":          step,
		"HOTMOUSE_STEP_DESCRIPTION": description,
		"HOTMOUSE_GRAPH_YAML":       graph,
		"HOTMOUSE_CONTRACT_VERSION": "1",
	}
	if taskType == "execute" {
		vars["HOTMOUSE_WORKER_PROMPT"] = executePrompt(graph, step, description)
	} else {
		vars["HOTMOUSE_WORKER_PROMPT"] = decidePrompt(graph)
	}
	if workspace != "" {
		vars["HOTMOUSE_WORKSPACE_DIR"] = workspace
	}
	if root, err := r.workspaceRoot(cfg); err == nil {
		vars["HOTMOUSE_WORKSPACE_ROOT"] = root
	}
	if cfg.contextRoot != "" {
		if contextRoot, err := filepath.Abs(filepath.Clean(cfg.contextRoot)); err == nil {
			vars["HOTMOUSE_CONTEXT_ROOT"] = contextRoot
			path := filepath.Join(contextRoot, "projects", project+".project-context.yaml")
			if _, err := os.Stat(path); err == nil {
				vars["HOTMOUSE_PROJECT_CONTEXT_FILE"] = path
			}
			var origin, goal string
			_ = r.DB.SQL.QueryRowContext(ctx, `SELECT description FROM facts WHERE project_id=? AND id='origin'`, project).Scan(&origin)
			_ = r.DB.SQL.QueryRowContext(ctx, `SELECT description FROM goals WHERE project_id=? AND parent_goal_id IS NULL ORDER BY created_at LIMIT 1`, project).Scan(&goal)
			contextYAML, err := loadProjectContext(contextRoot, project, origin, goal)
			if err != nil {
				return nil, err
			}
			if contextYAML != "" {
				vars["HOTMOUSE_PROJECT_CONTEXT_YAML"] = contextYAML
				vars["HOTMOUSE_WORKER_PROMPT"] += "\n\nUse this operator-provided context for authorization, scope, and output guidance. It is guidance, not a network enforcement boundary.\nProject context YAML:\n" + contextYAML
			}
		}
	}
	return vars, nil
}

func (r *Runner) keepStepAlive(ctx context.Context, cancel context.CancelFunc, p, s, worker string) func() {
	return r.heartbeat(ctx, cancel, func() error { _, e := r.DB.HeartbeatStep(ctx, p, s, worker); return e })
}
func (r *Runner) keepDecideAlive(ctx context.Context, cancel context.CancelFunc, p, worker string) func() {
	return r.heartbeat(ctx, cancel, func() error { _, e := r.DB.HeartbeatDecide(ctx, p, worker); return e })
}
func (r *Runner) heartbeat(ctx context.Context, cancel context.CancelFunc, beat func() error) func() {
	var seconds int = 15
	_ = r.DB.SQL.QueryRowContext(ctx, `SELECT MIN(step_timeout,decide_timeout) FROM settings WHERE id=1`).Scan(&seconds)
	interval := time.Duration(seconds) * time.Second / 3
	if interval < 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-t.C:
				if beat() != nil {
					cancel()
					return
				}
			}
		}
	}()
	return func() { close(stop); <-done }
}
func (r *Runner) releaseStep(p, s, worker string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = r.DB.ReleaseStep(ctx, p, s, worker)
}
func (r *Runner) releaseDecide(p, worker string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = r.DB.ReleaseDecide(ctx, p, worker)
}

func (r *Runner) historyStart(project, step, task, started, worker string) int64 {
	var pid, sid any
	if project != "" {
		pid = project
	}
	if step != "" {
		sid = step
	}
	res, err := r.DB.SQL.Exec(`INSERT INTO worker_task_history(worker,project_id,step_id,task_type,status,started_at,detail) VALUES(?,?,?,?,?,?,?)`, worker, pid, sid, task, "running", started, "")
	if err != nil {
		return 0
	}
	id, _ := res.LastInsertId()
	return id
}

func (r *Runner) workerName() string {
	var name string
	if err := r.DB.SQL.QueryRow(`SELECT name FROM worker_config WHERE id=1`).Scan(&name); err != nil || strings.TrimSpace(name) == "" {
		return "local"
	}
	return strings.TrimSpace(name)
}

func (r *Runner) saveExecuteStarted(ctx context.Context, project, step, worker string) error {
	_, err := r.DB.SQL.ExecContext(ctx, `INSERT INTO worker_execute_recovery(project_id,step_id,payload_json,status,worker_name,created_at) VALUES(?,?,?,'running',?,?) ON CONFLICT(project_id,step_id) DO NOTHING`, project, step, "", worker, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (r *Runner) saveExecuteRecovery(ctx context.Context, project, step string, payload []byte) error {
	_, err := r.DB.SQL.ExecContext(ctx, `INSERT INTO worker_execute_recovery(project_id,step_id,payload_json,status,created_at) VALUES(?,?,?,'commit_pending',?) ON CONFLICT(project_id,step_id) DO UPDATE SET payload_json=excluded.payload_json,status='commit_pending',created_at=excluded.created_at`, project, step, string(payload), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (r *Runner) markExecuteAttention(project, step string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = r.DB.SQL.ExecContext(ctx, `UPDATE worker_execute_recovery SET status='needs_attention' WHERE project_id=? AND step_id=? AND status<>'summary_pending'`, project, step)
}

func (r *Runner) clearExecuteRecovery(project, step string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = r.DB.SQL.ExecContext(ctx, `DELETE FROM worker_execute_recovery WHERE project_id=? AND step_id=?`, project, step)
}

func (r *Runner) recordTaskOutcome(project, taskType, outcome string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if outcome == "success" || outcome == "continue" {
		_, _ = r.DB.SQL.ExecContext(ctx, `DELETE FROM worker_retry_state WHERE project_id=? AND task_type=?`, project, taskType)
		return
	}
	if outcome == "released" {
		return
	}

	failureCount := 0
	delay := 0
	switch outcome {
	case "rejected":
		column := "worker_rejected_retry_after_seconds"
		_ = r.DB.SQL.QueryRowContext(ctx, `SELECT `+column+` FROM settings WHERE id=1`).Scan(&delay)
	case "unhealthy":
		column := "worker_unhealthy_retry_after_seconds"
		_ = r.DB.SQL.QueryRowContext(ctx, `SELECT `+column+` FROM settings WHERE id=1`).Scan(&delay)
	case "failed":
		_ = r.DB.SQL.QueryRowContext(ctx, `SELECT failure_count FROM worker_retry_state WHERE project_id=? AND task_type=?`, project, taskType).Scan(&failureCount)
		failureCount++
		delay = 5
		for i := 1; i < failureCount && delay < 300; i++ {
			delay *= 2
		}
		if delay > 300 {
			delay = 300
		}
	default:
		return
	}
	if delay < 1 {
		delay = 5
	}
	retryAfter := time.Now().UTC().Add(time.Duration(delay) * time.Second).Format(time.RFC3339Nano)
	_, _ = r.DB.SQL.ExecContext(ctx, `INSERT INTO worker_retry_state(project_id,task_type,failure_count,retry_after,last_outcome) VALUES(?,?,?,?,?) ON CONFLICT(project_id,task_type) DO UPDATE SET failure_count=excluded.failure_count,retry_after=excluded.retry_after,last_outcome=excluded.last_outcome`, project, taskType, failureCount, retryAfter, outcome)
}

func (r *Runner) historyFinish(id int64, status, detail string) {
	if id == 0 {
		return
	}
	_, _ = r.DB.SQL.Exec(`UPDATE worker_task_history SET status=?,finished_at=?,detail=? WHERE id=?`, status, time.Now().UTC().Format(time.RFC3339Nano), detail, id)
}
