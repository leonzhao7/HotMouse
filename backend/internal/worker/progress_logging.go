package worker

import (
	"time"

	"hotmouse/backend/internal/core"
	"hotmouse/backend/internal/logging"
)

type taskLogContext struct {
	logger  *logging.Logger
	task    string
	started time.Time
}

// Retain the task's original logger and credentials without querying SQLite
// for diagnostics or losing redaction if configuration changes during a run.
func (r *Runner) startTaskLog(id int64, project, step, task, started, worker string) {
	r.taskLogsMu.Lock()
	defer r.taskLogsMu.Unlock()
	if r.taskLogs == nil {
		r.taskLogs = map[int64]taskLogContext{}
	}
	stamp, _ := time.Parse(time.RFC3339Nano, started)
	r.taskLogs[id] = taskLogContext{logger: r.logger.With("project", project, "step", step, "task", task, "worker", worker), task: task, started: stamp}
}

func (r *Runner) setTaskLogger(id int64, logger *logging.Logger) {
	r.taskLogsMu.Lock()
	defer r.taskLogsMu.Unlock()
	if entry, ok := r.taskLogs[id]; ok {
		entry.logger = logger
		r.taskLogs[id] = entry
	}
}

func (r *Runner) logTaskFinish(id int64, status, detail string) {
	r.taskLogsMu.Lock()
	entry := r.taskLogs[id]
	delete(r.taskLogs, id)
	r.taskLogsMu.Unlock()
	logger := entry.logger
	if logger == nil {
		logger = r.logger
	}
	fields := []any{"history", id, "status", status, "detail_bytes", len(detail)}
	if !entry.started.IsZero() {
		fields = append(fields, "elapsed", time.Since(entry.started).Round(time.Millisecond))
	}
	// Plans have dedicated graph/strategy logs. Other tasks carry concise
	// results, startup analysis, recovery summaries or actionable errors.
	if status != "completed" || (entry.task != "decide" && entry.task != "competition_plan") {
		fields = append(fields, "summary", detail)
	}
	switch status {
	case "failed", "invalid_payload", "commit_pending", "history_write_failed":
		logger.Error("task.finish", fields...)
	case "interrupted", "summary_pending", "rejected", "disabled", "claim_failed":
		logger.Warn("task.finish", fields...)
	default:
		logger.Info("task.finish", fields...)
	}
}

// Describe committed graph changes so a long run reveals its current approach.
// Comparing saved states also avoids reporting protected step updates as applied.
func logDecision(logger *logging.Logger, before, after core.ProjectDetail) {
	logger.Info("dispatcher.plan.committed", "project_status", after.Project.Status, "facts", len(after.Facts),
		"steps", len(after.Steps), "budget_seconds", after.Project.BudgetSeconds, "budget_spent_ms", after.Project.BudgetSpentMilliseconds)
	steps := map[string]core.Step{}
	for _, step := range before.Steps {
		steps[step.ID] = step
	}
	for _, step := range after.Steps {
		old, exists := steps[step.ID]
		if exists && old.Description == step.Description && old.Priority == step.Priority && old.Abandoned == step.Abandoned {
			continue
		}
		event := "dispatcher.step.planned"
		if exists {
			event = "dispatcher.step.updated"
		}
		logger.Info(event, "step", step.ID, "description", step.Description, "from", step.From,
			"priority", step.Priority, "abandoned", step.Abandoned, "goal", pointerText(step.GoalID), "route", pointerText(step.RouteID))
	}
	goals := map[string]core.Goal{}
	for _, goal := range before.Goals {
		goals[goal.ID] = goal
	}
	for _, goal := range after.Goals {
		old, exists := goals[goal.ID]
		if exists && old.Status == goal.Status {
			continue
		}
		logger.Info("dispatcher.goal", "goal", goal.ID, "status", goal.Status, "description", goal.Description,
			"completion", pointerText(goal.CompletionDescription), "from", goal.From)
	}
	routes := map[string]core.Route{}
	for _, route := range before.Routes {
		routes[route.ID] = route
	}
	for _, route := range after.Routes {
		old, exists := routes[route.ID]
		if exists && old.Status == route.Status && old.ShelvedReason == route.ShelvedReason && old.NextDirection == route.NextDirection && old.ReopenCondition == route.ReopenCondition {
			continue
		}
		logger.Info("dispatcher.route", "route", route.ID, "title", route.Title, "status", route.Status,
			"reason", route.ShelvedReason, "next_direction", route.NextDirection, "reopen_condition", route.ReopenCondition)
	}
	if after.Project.Status == "failed" {
		logger.Warn("project.failed", "reason", after.Project.FailureReason)
	}
}

func logStepResult(logger *logging.Logger, conclusion core.Conclude) {
	fields := []any{"summary", conclusion.Description}
	resultType := "unclassified"
	if conclusion.Result != nil {
		resultType = conclusion.Result.Type
		fields = append(fields, "detail", conclusion.Result.Detail)
	}
	fields = append(fields, "result_type", resultType)
	switch resultType {
	case "blocked", "environment_failure", "needs_attention":
		logger.Warn("step.result", fields...)
	default:
		logger.Info("step.result", fields...)
	}
	if conclusion.Finding != nil {
		logger.Info("step.finding", "title", conclusion.Finding.Title, "severity", conclusion.Finding.Severity)
	}
}

func pointerText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
