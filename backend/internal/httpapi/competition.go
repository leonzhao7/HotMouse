package httpapi

import (
	"context"
	"fmt"
	"strings"

	"github.com/kataras/iris/v12"
	"hotmouse/backend/internal/competition"
	"hotmouse/backend/internal/core"
	"hotmouse/backend/internal/ctfplatform"
)

func newCompetitionManager(h *Handler) *competition.Manager {
	return competition.New(h.DB, competition.Backend{
		Connect: h.platform,
		Import: func(ctx context.Context, run core.CompetitionRun, challenge string) (string, error) {
			budget := run.ChallengeTimeoutSeconds
			project, err := h.importPlatformChallenge(ctx, run.Platform, platformImportRequest{ChallengeID: challenge, MaxWorkers: run.MaxWorkers, BudgetSeconds: &budget, CompetitionID: run.ID, Rules: run.Rules, RulesSummary: run.RulesSummary})
			return project.Project.ID, err
		},
		Lookup: func(ctx context.Context, run core.CompetitionRun, challenge string) (string, error) {
			project, err := h.DB.FindPlatformProject(ctx, run.Platform, run.ScopeID, challenge)
			if err == nil && project != "" {
				err = h.ensureCompetitionRules(ctx, run.ID, run.Rules, run.RulesSummary, project)
			}
			return project, err
		},
		Submit: func(ctx context.Context, project string, flag int64) (*ctfplatform.SubmissionResult, error) {
			result, err := h.submitPlatformFlag(ctx, project, flag)
			return result.Result, err
		},
		Plan:   h.Runner.PlanCompetition,
		Cancel: h.Runner.CancelProject,
		Busy:   h.Runner.ProjectBusy,
	})
}

func competitionHints(id, rules, summary string) []core.CreateHint {
	if rules == "" {
		return nil
	}
	content := "Follow these competition rules for authorization, scope, solving and output requirements:\n" + rules
	if summary != "" {
		content += "\n\nOverall coordinator interpretation (the original rules take precedence):\n" + summary
	}
	return []core.CreateHint{{Content: content, Creator: "competition:" + id}}
}

func (h *Handler) ensureCompetitionRules(ctx context.Context, id, rules, summary, project string) error {
	hints := competitionHints(id, rules, summary)
	if len(hints) == 0 {
		return nil
	}
	detail, err := h.DB.GetProject(ctx, project)
	if err != nil {
		return err
	}
	for _, hint := range detail.Hints {
		if hint.Creator == hints[0].Creator {
			return nil
		}
	}
	_, err = h.DB.AddHint(ctx, project, hints[0])
	return err
}

// StartCompetition is shared by the API and the service's rules-file entry.
func (h *Handler) StartCompetition(ctx context.Context, id string, config core.CompetitionConfig) (core.CompetitionRun, error) {
	if !h.Competitions.Running() {
		return core.CompetitionRun{}, &ctfplatform.Error{HTTPStatus: 503, Code: "competition_scheduler_disabled", Message: "start the service with --autorun to run a competition"}
	}
	if err := config.Normalize(); err != nil {
		return core.CompetitionRun{}, &ctfplatform.Error{HTTPStatus: 422, Code: "invalid_input", Message: err.Error()}
	}
	platform, scope, err := h.platform(id)
	if err != nil {
		return core.CompetitionRun{}, err
	}
	if cap := platform.Descriptor().MaxActiveInstances; cap > 0 && config.Concurrency > cap {
		return core.CompetitionRun{}, &ctfplatform.Error{HTTPStatus: 422, Code: "concurrency_limit", Message: fmt.Sprintf("%s allows at most %d concurrent challenges", id, cap)}
	}
	challenges, err := platform.ListChallenges(ctx)
	if err != nil {
		return core.CompetitionRun{}, err
	}
	ids := []string{}
	for _, challenge := range challenges {
		ids = append(ids, challenge.ID)
	}
	run, err := h.DB.CreateCompetitionRun(ctx, id, scope, config, ids)
	if err == nil {
		h.Competitions.Wake()
	}
	return run, err
}

func (h *Handler) competitionRun(ctx iris.Context) (core.CompetitionRun, bool) {
	run, err := h.DB.GetCompetitionRun(ctx.Request().Context(), ctx.Params().Get("run_id"))
	if err == nil && run.Platform != ctx.Params().Get("platform") {
		err = core.ErrNotFound
	}
	if err != nil {
		platformError(ctx, err)
		return run, false
	}
	return run, true
}

func (h *Handler) registerCompetitions(app *iris.Application) {
	app.Post("/api/platforms/{platform:string}/runs", func(ctx iris.Context) {
		var config core.CompetitionConfig
		if !read(ctx, &config) {
			return
		}
		run, err := h.StartCompetition(ctx.Request().Context(), ctx.Params().Get("platform"), config)
		if err != nil {
			platformError(ctx, err)
			return
		}
		ctx.JSON(run)
	})
	app.Get("/api/platforms/{platform:string}/runs", func(ctx iris.Context) {
		ids, err := h.DB.CompetitionRunIDs(ctx.Request().Context(), false)
		if err != nil {
			platformError(ctx, err)
			return
		}
		out := []core.CompetitionRun{}
		for _, id := range ids {
			run, err := h.DB.GetCompetitionRun(ctx.Request().Context(), id)
			if err != nil {
				platformError(ctx, err)
				return
			}
			if run.Platform == ctx.Params().Get("platform") {
				out = append(out, run)
			}
		}
		ctx.JSON(out)
	})
	app.Get("/api/platforms/{platform:string}/runs/{run_id:string}", func(ctx iris.Context) {
		if run, ok := h.competitionRun(ctx); ok {
			ctx.JSON(run)
		}
	})
	app.Patch("/api/platforms/{platform:string}/runs/{run_id:string}", func(ctx iris.Context) {
		run, ok := h.competitionRun(ctx)
		if !ok {
			return
		}
		var request struct {
			Concurrency int `json:"concurrency"`
		}
		if !read(ctx, &request) {
			return
		}
		platform, scope, err := h.platform(run.Platform)
		if err == nil && scope != run.ScopeID {
			err = &ctfplatform.Error{HTTPStatus: 409, Code: "platform_task_changed", Message: "restore the original competition task credentials"}
		}
		if err == nil && (request.Concurrency < 1 || request.Concurrency > 32 || platform.Descriptor().MaxActiveInstances > 0 && request.Concurrency > platform.Descriptor().MaxActiveInstances) {
			err = &ctfplatform.Error{HTTPStatus: 422, Code: "concurrency_limit", Message: "concurrency exceeds the platform limit"}
		}
		if err == nil {
			err = h.DB.SetCompetitionConcurrency(ctx.Request().Context(), run.ID, request.Concurrency)
		}
		if err != nil {
			platformError(ctx, err)
			return
		}
		h.Competitions.Wake()
		run, err = h.DB.GetCompetitionRun(ctx.Request().Context(), run.ID)
		if err != nil {
			platformError(ctx, err)
			return
		}
		ctx.JSON(run)
	})
	app.Post("/api/platforms/{platform:string}/runs/{run_id:string}/stop", func(ctx iris.Context) {
		run, ok := h.competitionRun(ctx)
		if !ok {
			return
		}
		if err := h.Competitions.Stop(ctx.Request().Context(), run.ID); err != nil {
			platformError(ctx, err)
			return
		}
		run, err := h.DB.GetCompetitionRun(ctx.Request().Context(), run.ID)
		if err != nil {
			platformError(ctx, err)
			return
		}
		ctx.JSON(run)
	})
}

// A configured startup entry resumes the durable active run for this task.
func (h *Handler) ResumeOrStartCompetition(ctx context.Context, platform string, config core.CompetitionConfig) (core.CompetitionRun, error) {
	_, scope, err := h.platform(platform)
	if err != nil {
		return core.CompetitionRun{}, err
	}
	ids, err := h.DB.CompetitionRunIDs(ctx, false)
	if err != nil {
		return core.CompetitionRun{}, err
	}
	for index := len(ids) - 1; index >= 0; index-- {
		id := ids[index]
		run, err := h.DB.GetCompetitionRun(ctx, id)
		if err != nil {
			return run, err
		}
		if run.Platform == platform && run.ScopeID == scope {
			if strings.TrimSpace(run.Rules) != strings.TrimSpace(config.Rules) {
				if run.Status == "planning" || run.Status == "running" || run.Status == "stopping" {
					return run, fmt.Errorf("competition %s already active with different rules", id)
				}
				continue
			}
			return run, nil
		}
	}
	return h.StartCompetition(ctx, platform, config)
}
