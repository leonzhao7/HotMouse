package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/kataras/iris/v12"
	"hotmouse/backend/internal/competition"
	"hotmouse/backend/internal/core"
	"hotmouse/backend/internal/ctfplatform"
)

func newCompetitionManager(h *Handler) *competition.Manager {
	manager := competition.New(h.DB, competition.Backend{
		Connect: h.platform,
		Import: func(ctx context.Context, run core.CompetitionRun, challenge string) (string, error) {
			project, err := h.importPlatformChallenge(ctx, run.Platform, platformImportRequest{ChallengeID: challenge, MaxWorkers: run.MaxWorkers, CompetitionID: run.ID, Rules: run.Rules, RulesSummary: run.RulesSummary})
			return project.Project.ID, err
		},
		Retry: h.retryCompetitionProject,
		Lookup: func(ctx context.Context, run core.CompetitionRun, challenge string) (string, error) {
			project, err := h.DB.FindPlatformProject(ctx, run.Platform, run.ScopeID, challenge)
			if err == nil && project != "" {
				err = h.ensureCompetitionRules(ctx, run.ID, run.Rules, run.RulesSummary, project)
			}
			return project, err
		},
		Submit: func(ctx context.Context, project string, flag int64) (*ctfplatform.SubmissionResult, error) {
			result, err := h.submitPlatformFlagAttempt(ctx, project, flag, h.Competitions.Unattended)
			return result.Result, err
		},
		Plan:   h.Runner.PlanCompetition,
		Cancel: h.Runner.CancelProject,
		Busy:   h.Runner.ProjectBusy,
	})
	h.Runner.SetFlagCaptureNotifier(manager.Wake)
	return manager
}

func (h *Handler) retryCompetitionProject(ctx context.Context, run core.CompetitionRun, platform ctfplatform.Platform, challenge ctfplatform.Challenge, project string) error {
	h.platformMu.Lock()
	defer h.platformMu.Unlock()
	delivery, err := deliverPlatformChallenge(ctx, platform, challenge)
	if err != nil {
		return err
	}
	challengeData, err := json.Marshal(challenge)
	if err != nil {
		return err
	}
	deliveryData, err := json.Marshal(delivery)
	if err != nil {
		return err
	}
	origin := platformChallengeOrigin(run.Platform, platform.Descriptor().RequiresVPN, challenge, delivery)
	return h.DB.RefreshCompetitionProject(ctx, project, string(challengeData), string(deliveryData), origin)
}

func platformChallengeOrigin(platformID string, requiresVPN bool, challenge ctfplatform.Challenge, delivery ctfplatform.Delivery) string {
	origin := fmt.Sprintf("CTF platform: %s\nChallenge ID: %s\nDifficulty: %s; level: %d; total score: %d\nFlags: %d total, %d already accepted\n", platformID, challenge.ID, challenge.Difficulty, challenge.Level, challenge.TotalScore, challenge.FlagCount, challenge.CorrectFlagCount)
	if challenge.Description != nil {
		origin += "Description: " + *challenge.Description + "\n"
	}
	if len(challenge.Metadata) != 0 {
		if metadata, err := json.MarshalIndent(challenge.Metadata, "", "  "); err == nil {
			origin += "Platform challenge metadata (including answer question IDs and prompts where supplied):\n" + string(metadata) + "\n"
		}
	}
	if len(delivery.Metadata) != 0 {
		if metadata, err := json.MarshalIndent(delivery.Metadata, "", "  "); err == nil {
			origin += "Instance delivery metadata:\n" + string(metadata) + "\n"
		}
	}
	for _, address := range delivery.Addresses {
		origin += "Target address: " + address + "\n"
	}
	for _, attachment := range delivery.Attachments {
		origin += fmt.Sprintf("Attachment: %s %s %s\n", attachment.Name, attachment.URL, attachment.Path)
	}
	topology, _ := delivery.Metadata["network_topology"].(string)
	if topology == "engagement" {
		origin += "This CTFy instance uses the engagement topology and requires its WireGuard VPN.\n"
		if address, ok := delivery.Metadata["vpn_config_url"].(string); ok && address != "" {
			origin += "VPN config URL: " + address + "\n"
		}
	} else if requiresVPN {
		origin += "Target addresses require the platform VPN connection.\n"
	}
	if platformID == "ctfy" {
		origin += "For CTFy answers, save each verified answer with both challenge_id and the exact question_id from the platform metadata. For multi-select questions, encode the selected string array as a JSON array in the value. The coordinator submits answers; do not call platform endpoints from a Worker.\n"
	}
	return origin
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
	defaultConcurrency := config.Concurrency == 0
	if defaultConcurrency {
		if err := h.DB.SQL.QueryRowContext(ctx, `SELECT project_concurrency FROM worker_config WHERE id=1`).Scan(&config.Concurrency); err != nil {
			return core.CompetitionRun{}, err
		}
	}
	if config.MaxWorkers == 0 {
		if err := h.DB.SQL.QueryRowContext(ctx, `SELECT default_project_max_workers FROM settings WHERE id=1`).Scan(&config.MaxWorkers); err != nil {
			return core.CompetitionRun{}, err
		}
	}
	if err := config.NormalizeForPlatform(id); err != nil {
		return core.CompetitionRun{}, &ctfplatform.Error{HTTPStatus: 422, Code: "invalid_input", Message: err.Error()}
	}
	h.platformMu.Lock()
	defer h.platformMu.Unlock()
	platform, scope, err := h.platform(id)
	if err != nil {
		return core.CompetitionRun{}, err
	}
	challenges, err := listPlatformChallengesWithVisibility(ctx, platform)
	if err != nil {
		return core.CompetitionRun{}, err
	}
	if h.Competitions.Unattended && len(challenges) == 0 {
		return core.CompetitionRun{}, &ctfplatform.Error{HTTPStatus: 503, Code: "challenge_list_empty", Message: "no competition challenges are available yet"}
	}
	if id == "ctfy" {
		for _, challenge := range challenges {
			if challenge.Metadata["instance_visibility"] == "incomplete" {
				return core.CompetitionRun{}, &ctfplatform.Error{HTTPStatus: 409, Code: "instance_list_incomplete", Message: "CTFy reports running team instances but does not expose their IDs to this token; use a token that can list the team's instances before starting the competition"}
			}
		}
		sort.SliceStable(challenges, func(i, j int) bool {
			active := func(challenge ctfplatform.Challenge) bool {
				return challenge.InstanceStatus == "ready" || challenge.InstanceStatus == "starting" || challenge.InstanceStatus == "pending"
			}
			return active(challenges[i]) && !active(challenges[j])
		})
	}
	if cap := platform.Descriptor().MaxActiveInstances; cap > 0 && config.Concurrency > cap {
		if defaultConcurrency {
			config.Concurrency = cap
		} else {
			return core.CompetitionRun{}, &ctfplatform.Error{HTTPStatus: 422, Code: "concurrency_limit", Message: fmt.Sprintf("%s allows at most %d concurrent challenges", id, cap)}
		}
	}
	ids := []string{}
	for _, challenge := range challenges {
		if id == "ctfy" && challenge.Completed {
			continue
		}
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
	app.Get("/api/competitions/status", func(ctx iris.Context) {
		ctx.JSON(iris.Map{"scheduler_enabled": h.Competitions.Running()})
	})
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
		if err == nil {
			_, err = platform.ListChallenges(ctx.Request().Context())
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
	adapter, scope, err := h.platform(platform)
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
			if run.Status == "planning" || run.Status == "running" {
				if cap := adapter.Descriptor().MaxActiveInstances; cap > 0 && config.Concurrency > cap {
					return run, fmt.Errorf("%s allows at most %d concurrent challenges", platform, cap)
				}
				if err := h.DB.ConfigureCompetitionRun(ctx, id, config); err != nil {
					return run, err
				}
				h.Competitions.Wake()
				return h.DB.GetCompetitionRun(ctx, id)
			}
			return run, nil
		}
	}
	return h.StartCompetition(ctx, platform, config)
}
