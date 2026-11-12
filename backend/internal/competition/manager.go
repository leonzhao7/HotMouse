// Package competition coordinates a whole platform task while the existing
// per-project Dispatchers and Worker pool solve individual challenges.
package competition

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"hotmouse/backend/internal/core"
	"hotmouse/backend/internal/ctfplatform"
	"hotmouse/backend/internal/logging"
)

type Plan struct {
	Summary      string   `json:"summary"`
	ChallengeIDs []string `json:"challenge_ids"`
}

type Backend struct {
	Connect func(string) (ctfplatform.Platform, string, error)
	Import  func(context.Context, core.CompetitionRun, string) (string, error)
	Retry   func(context.Context, core.CompetitionRun, ctfplatform.Platform, ctfplatform.Challenge, string) error
	Lookup  func(context.Context, core.CompetitionRun, string) (string, error)
	Submit  func(context.Context, string, int64) (*ctfplatform.SubmissionResult, error)
	Plan    func(context.Context, core.CompetitionRun, []ctfplatform.Challenge) (Plan, error)
	Cancel  func(string)
	Busy    func(string) bool
}

type Manager struct {
	DB       *core.DB
	Backend  Backend
	Interval time.Duration
	Now      func() time.Time
	Logger   *logging.Logger
	started  atomic.Bool
	wake     chan struct{}
	stepMu   sync.Mutex
	cancelMu sync.Mutex
	current  map[string]context.CancelFunc
}

func New(db *core.DB, backend Backend) *Manager {
	return &Manager{DB: db, Backend: backend, Interval: time.Second, Now: time.Now, wake: make(chan struct{}, 1), current: map[string]context.CancelFunc{}}
}
func (m *Manager) Running() bool { return m.started.Load() }
func (m *Manager) Wake() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Manager) Start(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	if !m.started.CompareAndSwap(false, true) {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		defer m.started.Store(false)
		m.log("competition.coordinator.start")
		defer m.log("competition.coordinator.stop")
		interval := m.Interval
		if interval <= 0 {
			interval = time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			m.RunOnce(ctx)
			select {
			case <-ctx.Done():
				return
			case <-m.wake:
			case <-ticker.C:
			}
		}
	}()
	return done
}

func (m *Manager) Stop(ctx context.Context, id string) error {
	if err := m.DB.RequestCompetitionStop(ctx, id); err != nil {
		return err
	}
	m.cancelMu.Lock()
	if cancel := m.current[id]; cancel != nil {
		cancel()
	}
	m.cancelMu.Unlock()
	m.log("competition.stop_requested", "run", id)
	m.Wake()
	return nil
}

func (m *Manager) RunOnce(ctx context.Context) {
	m.stepMu.Lock()
	defer m.stepMu.Unlock()
	ids, err := m.DB.CompetitionRunIDs(ctx, true)
	if err != nil {
		m.warn("competition.list_failed", "error", err)
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		runCtx, cancel := context.WithCancel(ctx)
		m.cancelMu.Lock()
		m.current[id] = cancel
		m.cancelMu.Unlock()
		err := m.advance(runCtx, id)
		cancel()
		m.cancelMu.Lock()
		delete(m.current, id)
		m.cancelMu.Unlock()
		if err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
			_, _ = m.DB.SQL.ExecContext(ctx, `UPDATE competition_runs SET last_error=? WHERE id=? AND status IN ('planning','running','stopping')`, err.Error(), id)
			m.warn("competition.advance_failed", "run", id, "error", err)
		}
	}
}

func (m *Manager) log(event string, fields ...any) {
	if m.Logger != nil {
		m.Logger.Info(event, fields...)
	}
}
func (m *Manager) warn(event string, fields ...any) {
	if m.Logger != nil {
		m.Logger.Warn(event, fields...)
	}
}
func due(deadline string, now time.Time) bool {
	stamp, err := time.Parse(time.RFC3339Nano, deadline)
	return err != nil || !now.Before(stamp)
}
func timedOut(item core.CompetitionItem, run core.CompetitionRun, now time.Time) bool {
	if item.StartedAt == nil {
		return false
	}
	stamp, err := time.Parse(time.RFC3339Nano, *item.StartedAt)
	return err == nil && !now.Before(stamp.Add(time.Duration(run.ChallengeTimeoutSeconds)*time.Second))
}

func (m *Manager) advance(ctx context.Context, id string) error {
	run, err := m.DB.GetCompetitionRun(ctx, id)
	if err != nil {
		return err
	}
	platform, scope, err := m.Backend.Connect(run.Platform)
	if err != nil {
		return err
	}
	if scope != run.ScopeID {
		return m.abort(ctx, run, "platform_task_changed: restore the credentials for this competition task")
	}
	now := m.Now()
	if run.Status == "planning" {
		if !due(run.RetryAfter, now) {
			return nil
		}
		m.log("competition.planning.start", "run", id, "attempt", run.PlanningAttempts+1, "challenges", len(run.Items))
		challenges, err := platform.ListChallenges(ctx)
		if err == nil {
			plan := Plan{Summary: "Use the supplied competition rules for every challenge", ChallengeIDs: []string{}}
			for _, item := range run.Items {
				plan.ChallengeIDs = append(plan.ChallengeIDs, item.ChallengeID)
			}
			if m.Backend.Plan != nil {
				plan, err = m.Backend.Plan(ctx, run, challenges)
			}
			if err == nil {
				err = m.DB.SaveCompetitionPlan(ctx, id, plan.Summary, plan.ChallengeIDs)
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			attempts := run.PlanningAttempts + 1
			_, saveErr := m.DB.SQL.ExecContext(ctx, `UPDATE competition_runs SET planning_attempts=?,last_error=?,retry_after=? WHERE id=? AND status='planning'`, attempts, err.Error(), m.Now().Add(5*time.Second).UTC().Format(time.RFC3339Nano), id)
			if saveErr != nil {
				return saveErr
			}
			if attempts >= 3 {
				m.warn("competition.planning.failed", "run", id, "attempts", attempts, "error", err)
				return m.DB.FinishCompetitionRun(ctx, id, "failed", "competition rule planning failed: "+err.Error())
			}
			m.warn("competition.planning.retry", "run", id, "attempt", attempts, "retry_in", "5s", "error", err)
			return err
		}
		run, err = m.DB.GetCompetitionRun(ctx, id)
		if err != nil {
			return err
		}
		order := make([]string, 0, len(run.Items))
		for _, item := range run.Items {
			order = append(order, item.ChallengeID)
		}
		m.log("competition.rules_processed", "run", id, "challenges", len(run.Items), "concurrency", run.Concurrency, "strategy", run.RulesSummary, "challenge_order", order)
	}
	remote := map[string]ctfplatform.Challenge{}
	listed := false
	if run.Status != "stopping" {
		challenges, listErr := platform.ListChallenges(ctx)
		if listErr != nil {
			var failure *ctfplatform.Error
			if errors.As(listErr, &failure) && (failure.Code == "invalid_state" || failure.Code == "task_not_found") {
				return m.abort(ctx, run, failure.Error())
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			_, _ = m.DB.SQL.ExecContext(ctx, `UPDATE competition_runs SET last_error=? WHERE id=?`, listErr.Error(), id)
			if run.LastError != listErr.Error() {
				m.warn("competition.platform.list_failed", "run", id, "error", listErr, "action", "retry; new challenges wait for platform listing")
			}
		} else {
			listed = true
			for _, challenge := range challenges {
				remote[challenge.ID] = challenge
			}
			_, _ = m.DB.SQL.ExecContext(ctx, `UPDATE competition_runs SET last_error='' WHERE id=?`, id)
			if run.LastError != "" {
				m.log("competition.platform.recovered", "run", id)
			}
		}
	}
	// Retire old work and release resources before opening any new instances.
	for _, item := range run.Items {
		if item.Status == "queued" || terminal(item.Status) {
			continue
		}
		if err := m.advanceItem(ctx, run, platform, remote, &item); err != nil {
			return err
		}
	}
	run, err = m.DB.GetCompetitionRun(ctx, id)
	if err != nil {
		return err
	}
	if run.Status == "stopping" {
		for _, item := range run.Items {
			if item.Status == "queued" {
				item.Status, item.Outcome = "skipped", "skipped"
				stamp := now.UTC().Format(time.RFC3339Nano)
				item.FinishedAt = &stamp
				if err := m.DB.SaveCompetitionItem(ctx, item); err != nil {
					return err
				}
			}
		}
	} else if run.Status == "running" {
		active := 0
		for _, item := range run.Items {
			if item.Status == "starting" || item.Status == "running" || item.Status == "closing" {
				active++
			}
		}
		for _, item := range run.Items {
			if item.Status != "queued" || active >= run.Concurrency || !due(item.RetryAfter, now) {
				continue
			}
			// A failed list cannot safely establish which new containers can start.
			if _, known := remote[item.ChallengeID]; !known {
				if listed {
					item.Status, item.Outcome, item.LastError = "failed", "failed", "challenge disappeared from the platform task"
					stamp := now.UTC().Format(time.RFC3339Nano)
					item.FinishedAt = &stamp
					if err := m.DB.SaveCompetitionItem(ctx, item); err != nil {
						return err
					}
					m.warn("competition.challenge_missing", "run", id, "challenge", item.ChallengeID, "error", item.LastError)
				}
				continue
			}
			claimed, err := m.DB.ClaimCompetitionItem(ctx, id, item.ChallengeID, now)
			if err != nil {
				return err
			}
			if !claimed {
				continue
			}
			stamp := now.UTC().Format(time.RFC3339Nano)
			item.Status, item.StartedAt = "starting", &stamp
			active++
			m.log("competition.challenge.starting", "run", id, "challenge", item.ChallengeID, "title", remote[item.ChallengeID].Title, "position", item.Ordinal+1, "total", len(run.Items), "timeout_seconds", run.ChallengeTimeoutSeconds)
			if err := m.advanceItem(ctx, run, platform, remote, &item); err != nil {
				return err
			}
		}
	}
	run, err = m.DB.GetCompetitionRun(ctx, id)
	if err != nil {
		return err
	}
	for _, item := range run.Items {
		if !terminal(item.Status) {
			return nil
		}
	}
	if run.Status == "running" {
		retryIDs := []string{}
		unavailable := false
		for _, item := range run.Items {
			if item.Status != "failed" {
				continue
			}
			if !listed {
				continue
			}
			challenge, known := remote[item.ChallengeID]
			if !known {
				unavailable = true
				continue
			}
			if challenge.Completed {
				item.Status, item.Outcome, item.LastError = "closing", "succeeded", ""
				if err := m.DB.SaveCompetitionItem(ctx, item); err != nil {
					return err
				}
				if err := m.advanceItem(ctx, run, platform, remote, &item); err != nil {
					return err
				}
				if item.Status != "succeeded" {
					return nil
				}
				continue
			}
			retryIDs = append(retryIDs, item.ChallengeID)
		}
		if !listed {
			return nil
		}
		if unavailable && len(retryIDs) == 0 {
			return nil
		}
		if len(retryIDs) > 0 {
			retryAt := now.Add(5 * time.Second)
			queued, err := m.DB.RequeueCompetitionItems(ctx, id, retryIDs, retryAt)
			if err != nil {
				return err
			}
			if queued {
				for _, challenge := range retryIDs {
					m.log("competition.challenge.requeued", "run", id, "challenge", challenge, "retry_after", retryAt.UTC().Format(time.RFC3339Nano))
				}
			}
			return nil
		}
	}
	status := "completed"
	if run.Status == "stopping" {
		status = "stopped"
	}
	m.log("competition.finished", "run", id, "status", status)
	return m.DB.FinishCompetitionRun(ctx, id, status, "")
}

func terminal(status string) bool {
	return status == "succeeded" || status == "failed" || status == "skipped"
}

func (m *Manager) advanceItem(ctx context.Context, run core.CompetitionRun, platform ctfplatform.Platform, remote map[string]ctfplatform.Challenge, item *core.CompetitionItem) error {
	now := m.Now()
	previousStatus := item.Status
	if item.ProjectID == nil && m.Backend.Lookup != nil {
		project, err := m.Backend.Lookup(ctx, run, item.ChallengeID)
		if err != nil {
			return err
		}
		if project != "" {
			item.ProjectID = &project
		}
	}
	if run.Status == "stopping" && item.Status != "closing" {
		item.Status, item.Outcome, item.LastError = "closing", "skipped", "competition stopped"
	}
	if item.Status == "starting" {
		if challenge, known := remote[item.ChallengeID]; known && challenge.Completed {
			item.Status, item.Outcome = "closing", "succeeded"
		} else if timedOut(*item, run, now) {
			item.Status, item.Outcome, item.LastError = "closing", "failed", "challenge startup timeout"
		} else if item.ProjectID != nil {
			project, err := m.DB.GetProject(ctx, *item.ProjectID)
			if err == core.ErrNotFound {
				item.Status, item.Outcome, item.LastError = "closing", "failed", "challenge project was deleted"
			} else if err != nil {
				return err
			} else if project.Project.Status == "active" {
				item.Status = "running"
			} else if challenge, known := remote[item.ChallengeID]; known && challenge.InstanceStatus != "pending" && challenge.InstanceStatus != "stop_pending" && due(item.RetryAfter, now) {
				if m.Backend.Retry == nil {
					item.LastError = "competition project retry is unavailable"
					item.RetryAfter = now.Add(5 * time.Second).UTC().Format(time.RFC3339Nano)
				} else if err := m.Backend.Retry(ctx, run, platform, challenge, *item.ProjectID); err != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					item.LastError = err.Error()
					item.RetryAfter = now.Add(5 * time.Second).UTC().Format(time.RFC3339Nano)
					m.warn("competition.challenge.retry_failed", "run", run.ID, "challenge", item.ChallengeID, "retry_after", item.RetryAfter, "error", err)
				} else {
					item.Status = "running"
					item.LastError, item.RetryAfter = "", ""
					m.log("competition.challenge_retried", "run", run.ID, "challenge", item.ChallengeID, "project", *item.ProjectID)
				}
			}
		} else if challenge, known := remote[item.ChallengeID]; known && challenge.InstanceStatus != "pending" && challenge.InstanceStatus != "stop_pending" && due(item.RetryAfter, now) {
			m.log("competition.challenge.import", "run", run.ID, "challenge", item.ChallengeID, "instance_status", challenge.InstanceStatus)
			project, err := m.Backend.Import(ctx, run, item.ChallengeID)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				item.LastError = err.Error()
				item.RetryAfter = now.Add(5 * time.Second).UTC().Format(time.RFC3339Nano)
				m.warn("competition.challenge.import_failed", "run", run.ID, "challenge", item.ChallengeID, "retry_after", item.RetryAfter, "error", err)
				var failure *ctfplatform.Error
				if errors.As(err, &failure) && failure.Code == "challenge_not_found" {
					item.Status, item.Outcome = "closing", "failed"
				}
			} else {
				item.ProjectID = &project
				item.Status = "running"
				item.LastError, item.RetryAfter = "", ""
				m.log("competition.challenge_started", "run", run.ID, "challenge", item.ChallengeID, "project", project)
			}
		}
	}
	if item.Status == "running" && item.ProjectID != nil {
		project, err := m.DB.GetProject(ctx, *item.ProjectID)
		if err == core.ErrNotFound {
			item.Status, item.Outcome, item.LastError = "closing", "failed", "challenge project was deleted"
		} else if err != nil {
			return err
		} else {
			solved := false
			if challenge, known := remote[item.ChallengeID]; known {
				solved = challenge.Completed
			}
			flags, err := m.DB.ListFlags(ctx, *item.ProjectID)
			if err != nil {
				return err
			}
			for _, flag := range flags {
				if solved || flag.Status != "captured" {
					continue
				}
				m.log("competition.flag.submit", "run", run.ID, "challenge", item.ChallengeID, "project", *item.ProjectID, "flag_id", flag.ID)
				result, err := m.Backend.Submit(ctx, *item.ProjectID, flag.ID)
				if err != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					item.LastError = err.Error()
					m.warn("competition.flag.submit_failed", "run", run.ID, "challenge", item.ChallengeID, "flag_id", flag.ID, "error", err)
				} else if result != nil {
					fields := []any{"run", run.ID, "challenge", item.ChallengeID, "flag_id", flag.ID, "correct", result.Correct, "accepted_flags", result.CorrectFlagCount, "total_flags", result.TotalFlagCount}
					if result.Correct {
						m.log("competition.flag.accepted", fields...)
					} else {
						m.warn("competition.flag.rejected", fields...)
					}
					expected := max(result.TotalFlagCount, remote[item.ChallengeID].FlagCount)
					if result.Correct && expected > 0 && result.CorrectFlagCount >= expected {
						solved = true
						confirmation := remote[item.ChallengeID]
						confirmation.ID, confirmation.Completed = item.ChallengeID, true
						confirmation.FlagCount, confirmation.CorrectFlagCount = expected, result.CorrectFlagCount
						remote[item.ChallengeID] = confirmation
					}
				}
			}
			if solved {
				item.Status, item.Outcome = "closing", "succeeded"
				item.LastError = ""
			} else if project.Project.Status != "active" {
				item.Status, item.Outcome = "closing", "failed"
				if item.LastError == "" {
					item.LastError = project.Project.FailureReason
					if item.LastError == "" {
						item.LastError = "project ended before all platform flags were accepted"
					}
				}
			} else if timedOut(*item, run, now) {
				item.Status, item.Outcome, item.LastError = "closing", "failed", "challenge time limit reached"
			}
		}
	}
	if err := m.DB.SaveCompetitionItem(ctx, *item); err != nil {
		return err
	}
	if item.Status != "closing" {
		return nil
	}
	if previousStatus != "closing" {
		fields := []any{"run", run.ID, "challenge", item.ChallengeID, "outcome", item.Outcome, "reason", item.LastError, "action", "cancel workers and release instance"}
		if item.Outcome == "failed" {
			m.warn("competition.challenge.closing", fields...)
		} else {
			m.log("competition.challenge.closing", fields...)
		}
	}
	if item.ProjectID != nil {
		var err error
		if item.Outcome == "succeeded" {
			confirmation, _ := json.Marshal(remote[item.ChallengeID])
			err = m.DB.CompleteCompetitionProject(ctx, *item.ProjectID, string(confirmation))
		} else {
			err = m.DB.FailCompetitionProject(ctx, *item.ProjectID, item.LastError)
		}
		if err != nil && err != core.ErrNotFound {
			return err
		}
		if m.Backend.Cancel != nil {
			m.Backend.Cancel(*item.ProjectID)
		}
		if m.Backend.Busy != nil && m.Backend.Busy(*item.ProjectID) {
			return nil
		}
	}
	if !due(item.RetryAfter, now) {
		return nil
	}
	if closer, ok := platform.(ctfplatform.InstanceCloser); ok {
		m.log("competition.cleanup.start", "run", run.ID, "challenge", item.ChallengeID)
		closed, err := closer.Close(ctx, item.ChallengeID)
		if err != nil || !closed.Closed {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				item.LastError = "container cleanup: " + err.Error()
			} else {
				item.LastError = "container cleanup not confirmed"
			}
			item.RetryAfter = now.Add(5 * time.Second).UTC().Format(time.RFC3339Nano)
			m.warn("competition.cleanup.retry", "run", run.ID, "challenge", item.ChallengeID, "retry_after", item.RetryAfter, "error", item.LastError)
			return m.DB.SaveCompetitionItem(ctx, *item)
		}
		m.log("competition.cleanup.completed", "run", run.ID, "challenge", item.ChallengeID)
	}
	item.Status = item.Outcome
	item.RetryAfter = ""
	stamp := now.UTC().Format(time.RFC3339Nano)
	item.FinishedAt = &stamp
	m.log("competition.challenge_finished", "run", run.ID, "challenge", item.ChallengeID, "status", item.Status, "reason", item.LastError)
	return m.DB.SaveCompetitionItem(ctx, *item)
}

func (m *Manager) abort(ctx context.Context, run core.CompetitionRun, message string) error {
	m.warn("competition.aborted", "run", run.ID, "reason", message)
	for _, item := range run.Items {
		if terminal(item.Status) {
			continue
		}
		if item.ProjectID == nil && m.Backend.Lookup != nil {
			project, err := m.Backend.Lookup(ctx, run, item.ChallengeID)
			if err != nil {
				return err
			}
			if project != "" {
				item.ProjectID = &project
			}
		}
		if item.ProjectID != nil {
			if err := m.DB.FailCompetitionProject(ctx, *item.ProjectID, message); err != nil && err != core.ErrNotFound {
				return err
			}
			if m.Backend.Cancel != nil {
				m.Backend.Cancel(*item.ProjectID)
			}
		}
		item.Status, item.Outcome, item.LastError = "failed", "failed", message
		stamp := m.Now().UTC().Format(time.RFC3339Nano)
		item.FinishedAt = &stamp
		if err := m.DB.SaveCompetitionItem(ctx, item); err != nil {
			return err
		}
	}
	return m.DB.FinishCompetitionRun(ctx, run.ID, "failed", message)
}

func ValidatePlan(plan Plan, ids []string) error {
	if len(plan.Summary) > 64<<10 || len(plan.ChallengeIDs) != len(ids) {
		return fmt.Errorf("competition plan must include every challenge exactly once and a bounded summary")
	}
	remaining := map[string]bool{}
	for _, id := range ids {
		remaining[id] = true
	}
	for _, id := range plan.ChallengeIDs {
		if !remaining[id] {
			return fmt.Errorf("unknown or duplicate challenge %q in competition plan", id)
		}
		delete(remaining, id)
	}
	return nil
}
