package worker

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type executeCheckpoint struct {
	payload                                              []byte
	status, session, driver, identity, workspace, reason string
	attempts                                             int
}

func (r *Runner) loadExecuteRecovery(ctx context.Context, project, step string) (*executeCheckpoint, error) {
	var x executeCheckpoint
	var payload string
	err := r.DB.SQL.QueryRowContext(ctx, `SELECT payload_json,status,session_id,driver,worker_identity,workspace_dir,summary_attempts,interruption_reason FROM worker_execute_recovery WHERE project_id=? AND step_id=?`, project, step).Scan(&payload, &x.status, &x.session, &x.driver, &x.identity, &x.workspace, &x.attempts, &x.reason)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	x.payload = []byte(payload)
	if x.status == "needs_attention" {
		return nil, fmt.Errorf("worker recovery requires operator attention")
	}
	if strings.TrimSpace(payload) == "" && (x.status != "summary_pending" || x.session == "") {
		return nil, fmt.Errorf("worker recovery requires attention (status=%s)", x.status)
	}
	return &x, nil
}

func (r *Runner) saveProviderCheckpoint(ctx context.Context, project, step string, x *executeCheckpoint) error {
	res, err := r.DB.SQL.ExecContext(ctx, `UPDATE worker_execute_recovery SET status=?,session_id=?,driver=?,worker_identity=?,workspace_dir=?,summary_attempts=?,interruption_reason=? WHERE project_id=? AND step_id=?`, x.status, x.session, x.driver, x.identity, x.workspace, x.attempts, x.reason, project, step)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("provider checkpoint no longer exists")
	}
	return nil
}

func (r *Runner) preserveProviderCheckpoint(project, step string, x *executeCheckpoint) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return r.saveProviderCheckpoint(ctx, project, step, x)
}

func (r *Runner) executeProvider(ctx context.Context, cfg workerConfig, workspace, project, step, description, graph string, x *executeCheckpoint) ([]byte, error) {
	workerIdentity := cfg.identity
	if workerIdentity == "" {
		workerIdentity = r.workerName()
	}
	identity := providerIdentity(cfg, workerIdentity, workspace)
	if x != nil {
		if x.status != "summary_pending" || x.driver != canonicalDriver(cfg.driver) || x.identity != identity || x.workspace != workspace {
			x.status = "needs_attention"
			_ = r.preserveProviderCheckpoint(project, step, x)
			return nil, fmt.Errorf("provider recovery does not match current worker configuration")
		}
		return r.summarizeProvider(ctx, cfg, project, step, description, x)
	}
	x = &executeCheckpoint{status: "running", driver: canonicalDriver(cfg.driver), identity: identity, workspace: workspace}
	if x.driver == "claudecode" {
		var err error
		x.session, err = newSessionID()
		if err != nil {
			return nil, err
		}
	}
	if err := r.saveProviderCheckpoint(ctx, project, step, x); err != nil {
		return nil, err
	}
	vars, varsErr := r.commandVars(ctx, project, step, description, graph, "execute", cfg, workspace)
	if varsErr != nil {
		return nil, varsErr
	}
	seconds := cfg.runtime.ExecuteTimeoutSeconds
	if x.driver == "pi" {
		reserve := min(cfg.runtime.ConcludeReserveSeconds, seconds/2)
		seconds -= reserve
	}
	vars["HOTMOUSE_WORKER_PROMPT"] += fmt.Sprintf("\nExecution budget: %d seconds. Return the confirmed result promptly; keep every command bounded within this budget.\n", seconds)
	output, session, result, err := r.runProvider(ctx, cfg, "execute", x.session, workspace, vars, time.Duration(seconds)*time.Second, func(id string) error { x.session = id; return r.saveProviderCheckpoint(ctx, project, step, x) })
	x.session = session
	if err == nil {
		err = validateProviderExecute(output)
		if err == nil || isWorkerRejection(err) {
			return output, nil
		}
	}
	if x.session == "" {
		return nil, err
	}
	x.status = "summary_pending"
	x.reason = "invalid_response"
	if result.timedOut {
		x.reason = "execution_timeout"
	} else if result.outputLimited || (err != nil && strings.Contains(err.Error(), "output_limit")) {
		x.reason = "output_limit"
	} else if ctx.Err() != nil {
		x.reason = "cancelled"
	} else if result.err != nil {
		x.reason = "process_exit"
	}
	if checkpointErr := r.preserveProviderCheckpoint(project, step, x); checkpointErr != nil {
		return nil, checkpointErr
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return r.summarizeProvider(ctx, cfg, project, step, description, x)
}

func (r *Runner) summarizeProvider(ctx context.Context, cfg workerConfig, project, step, description string, x *executeCheckpoint) ([]byte, error) {
	if x.attempts >= cfg.runtime.RecoveryAttempts {
		x.status = "needs_attention"
		_ = r.preserveProviderCheckpoint(project, step, x)
		return nil, fmt.Errorf("provider summary recovery attempts exhausted")
	}
	// A fresh claim and graph read ensure stopped/abandoned work cannot resume.
	detail, err := r.DB.GetProject(ctx, project)
	if err != nil {
		return nil, err
	}
	if detail.Project.Status != "active" {
		return nil, fmt.Errorf("project is %s", detail.Project.Status)
	}
	graph, err := r.DB.ModelContext(ctx, project, "execute", step)
	if err != nil {
		return nil, err
	}
	x.attempts++
	x.status = "summary_pending"
	if err := r.saveProviderCheckpoint(ctx, project, step, x); err != nil {
		return nil, err
	}
	vars, err := r.commandVars(ctx, project, step, description, graph, "execute_conclude", cfg, x.workspace)
	if err != nil {
		return nil, err
	}
	vars["HOTMOUSE_WORKER_PROMPT"] = summaryPrompt(graph, step, description, x.reason) + "\n\nProject context:\n" + vars["HOTMOUSE_PROJECT_CONTEXT_YAML"]
	summaryCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.runtime.ConcludeTimeoutSeconds)*time.Second)
	defer cancel()
	retries := 0
	if x.driver == "pi" {
		retries = cfg.runtime.FormatRetries
	}
	for attempt := 0; attempt <= retries; attempt++ {
		output, _, _, runErr := r.runProvider(summaryCtx, cfg, "summary", x.session, x.workspace, vars, time.Duration(cfg.runtime.ConcludeTimeoutSeconds)*time.Second, nil)
		err = runErr
		if err == nil {
			err = validateProviderExecute(output)
		}
		if err == nil {
			return output, nil
		}
		if isWorkerRejection(err) || runErr != nil || summaryCtx.Err() != nil {
			break
		}
		vars["HOTMOUSE_TASK_TYPE"] = "execute_conclude_format"
		vars["HOTMOUSE_WORKER_PROMPT"] = `Repair only the previous summary's JSON serialization. Return exactly one complete raw JSON object: {"accepted":true,"data":{"description":"confirmed result","result":{"type":"progress","detail":"evidence"},"finding":{...}}}. Finding is optional; preserve all confirmed fields including result. Do not invent evidence or perform new actions. Close the outer envelope and all brackets; escape quotes. No Markdown or prose.`
	}
	if x.attempts >= cfg.runtime.RecoveryAttempts || isWorkerRejection(err) {
		x.status = "needs_attention"
	}
	if checkpointErr := r.preserveProviderCheckpoint(project, step, x); checkpointErr != nil {
		return nil, checkpointErr
	}
	return nil, fmt.Errorf("provider summary failed: %w", err)
}

func summaryPrompt(graph, step, description, reason string) string {
	return `The current Step was interrupted. Resume this session ONLY to summarize evidence already obtained. Do not execute commands, make network requests, change files, or repeat any prior action. If nothing was verified, say so and describe the execution limitation; do not present it as a confirmed target fact.
Return exactly one complete raw JSON object with no Markdown:
{"accepted":true,"data":{"description":"concise confirmed result or blocker","result":{"type":"progress","detail":"saved evidence"},"finding":{"kind":"security_vulnerability","title":"title","severity":"high","description":"evidence","data":{}}}}
Finding is optional. Use {"accepted":false,"reason":"..."} when you cannot summarize.
Only execution_timeout means a deadline was reached; output_limit means exhausted output allowance; invalid_response means parsing/schema failed; process_exit means the CLI exited unsuccessfully; cancelled means execution was interrupted. Do not relabel the reason or invent a network failure.
Classify result.type as progress, negative, blocked, environment_failure, no_information or needs_attention based only on saved evidence. An interrupted process alone proves no target conclusion.

Interruption reason: ` + reason + "\nStep: " + step + "\nDescription: " + description + "\nLatest FGS YAML:\n" + graph
}
