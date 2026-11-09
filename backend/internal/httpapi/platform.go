package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kataras/iris/v12"
	"hotmouse/backend/internal/core"
	"hotmouse/backend/internal/ctfplatform"
	"hotmouse/backend/internal/ctfplatform/tsec"
)

// Only platform registration/configuration belongs here. All wire protocols
// live in adapters, so adding a platform does not change project/flag handlers.
func configurePlatforms() (*ctfplatform.Registry, map[string]ctfplatform.Config) {
	registry := ctfplatform.NewRegistry()
	if err := tsec.Register(registry); err != nil {
		panic(err)
	}
	configs := map[string]ctfplatform.Config{}
	base, token := os.Getenv("BENCHMARK_BASE_URL"), os.Getenv("BENCHMARK_TOKEN")
	if base != "" || token != "" {
		configs["tsec"] = ctfplatform.Config{BaseURL: strings.TrimRight(strings.TrimSpace(base), "/"), Token: token}
	}
	return registry, configs
}

func (h *Handler) platform(id string) (ctfplatform.Platform, string, error) {
	config, configured := h.PlatformConfigs[id]
	// Check registration first so unsupported and unconfigured platforms have
	// distinct errors even when no credentials have been supplied.
	if !configured {
		found := false
		for _, descriptor := range h.Platforms.Descriptors() {
			found = found || descriptor.ID == id
		}
		if !found {
			return nil, "", &ctfplatform.Error{HTTPStatus: 404, Code: "platform_not_found", Message: "platform is not registered"}
		}
		return nil, "", &ctfplatform.Error{HTTPStatus: 503, Code: "platform_not_configured", Message: "configure platform credentials in the HotMouse service environment"}
	}
	platform, err := h.Platforms.Connect(id, config)
	return platform, config.ScopeID(id), err
}

func platformError(ctx iris.Context, err error) {
	var upstream *ctfplatform.Error
	if errors.As(err, &upstream) {
		status := upstream.HTTPStatus
		if status < 400 || status > 599 {
			status = http.StatusBadGateway
		}
		ctx.StopWithJSON(status, upstream)
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		ctx.StopWithJSON(504, iris.Map{"code": "request_interrupted", "message": "platform operation was interrupted; reconcile progress before retrying"})
		return
	}
	bad(ctx, err)
}

type platformImportRequest struct {
	CompetitionID string `json:"-"`
	Rules         string `json:"-"`
	RulesSummary  string `json:"-"`
	ChallengeID   string `json:"challenge_id"`
	Title         string `json:"title"`
	MaxWorkers    int    `json:"max_workers"`
	BudgetSeconds *int   `json:"budget_seconds"`
}

func (r platformImportRequest) validate() error {
	if strings.TrimSpace(r.ChallengeID) == "" || r.MaxWorkers < 0 || r.MaxWorkers > 32 || r.BudgetSeconds != nil && *r.BudgetSeconds < 0 {
		return &ctfplatform.Error{HTTPStatus: 422, Code: "invalid_input", Message: "challenge_id is required; max_workers must be 1..32 (or omitted), budget_seconds must be nonnegative"}
	}
	return nil
}

func (h *Handler) importPlatformChallenge(ctx context.Context, id string, request platformImportRequest) (project core.ProjectDetail, err error) {
	if err := request.validate(); err != nil {
		return core.ProjectDetail{}, err
	}
	// Serialize imports to avoid two starts for the same challenge. The database
	// binding is also unique and is created in the project transaction.
	h.platformMu.Lock()
	defer h.platformMu.Unlock()
	platform, scope, err := h.platform(id)
	if err != nil {
		return core.ProjectDetail{}, err
	}
	existing, err := h.DB.FindPlatformProject(ctx, id, scope, request.ChallengeID)
	if err != nil {
		return core.ProjectDetail{}, err
	}
	if existing != "" {
		if err := h.ensureCompetitionRules(ctx, request.CompetitionID, request.Rules, request.RulesSummary, existing); err != nil {
			return core.ProjectDetail{}, err
		}
		return h.DB.GetProject(ctx, existing)
	}
	challenges, err := platform.ListChallenges(ctx)
	if err != nil {
		return core.ProjectDetail{}, err
	}
	var challenge *ctfplatform.Challenge
	for i := range challenges {
		if challenges[i].ID == request.ChallengeID {
			challenge = &challenges[i]
			break
		}
	}
	if challenge == nil {
		return core.ProjectDetail{}, &ctfplatform.Error{HTTPStatus: 404, Code: "challenge_not_found", Message: "challenge is not in the current platform task"}
	}
	if challenge.Completed {
		return core.ProjectDetail{}, &ctfplatform.Error{HTTPStatus: 409, Code: "challenge_completed", Message: "challenge is already completed"}
	}
	var delivery ctfplatform.Delivery
	started := false
	if platform.Descriptor().DeliveryMode == "instance" && challenge.InstanceStatus == "available" && len(challenge.Addresses) > 0 {
		delivery = ctfplatform.Delivery{ChallengeID: challenge.ID, Mode: "instance", Addresses: challenge.Addresses, Attachments: challenge.Attachments}
	} else {
		delivery, err = platform.Deliver(ctx, challenge.ID)
		if err != nil {
			return core.ProjectDetail{}, err
		}
		started = challenge.InstanceStatus == "stopped" && delivery.Mode == "instance"
	}
	defer func() {
		if err == nil || !started {
			return
		}
		if closer, ok := platform.(ctfplatform.InstanceCloser); ok {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			result, closeErr := closer.Close(cleanup, challenge.ID)
			if closeErr != nil || !result.Closed {
				err = fmt.Errorf("project import failed: %w; container cleanup failed, close challenge %s explicitly", err, challenge.ID)
			}
		}
	}()
	challengeData, err := json.Marshal(challenge)
	if err != nil {
		return core.ProjectDetail{}, err
	}
	deliveryData, err := json.Marshal(delivery)
	if err != nil {
		return core.ProjectDetail{}, err
	}
	title := request.Title
	if strings.TrimSpace(title) == "" {
		title = platform.Descriptor().Name + ": " + challenge.Title
	}
	origin := fmt.Sprintf("CTF platform: %s\nChallenge ID: %s\nDifficulty: %s; level: %d; total score: %d\nFlags: %d total, %d already accepted\n", id, challenge.ID, challenge.Difficulty, challenge.Level, challenge.TotalScore, challenge.FlagCount, challenge.CorrectFlagCount)
	if challenge.Description != nil {
		origin += "Description: " + *challenge.Description + "\n"
	}
	for _, address := range delivery.Addresses {
		origin += "Target address: " + address + "\n"
	}
	for _, attachment := range delivery.Attachments {
		origin += fmt.Sprintf("Attachment: %s %s %s\n", attachment.Name, attachment.URL, attachment.Path)
	}
	if platform.Descriptor().RequiresVPN {
		origin += "Target addresses require the platform VPN connection.\n"
	}
	goal := fmt.Sprintf("Solve challenge %s and capture every remaining flag (%d total, %d already accepted). Record flag findings with challenge_id=%s and their exact value backed by target evidence.", challenge.ID, challenge.FlagCount, challenge.CorrectFlagCount, challenge.ID)
	project, err = h.DB.CreateProject(ctx, core.CreateProject{
		Title: title, Origin: origin, Goal: goal, MaxWorkers: request.MaxWorkers, BudgetSeconds: request.BudgetSeconds,
		Hints:           competitionHints(request.CompetitionID, request.Rules, request.RulesSummary),
		PlatformBinding: &core.PlatformBinding{Platform: id, ScopeID: scope, ChallengeID: challenge.ID, Challenge: challengeData, Delivery: deliveryData},
	})
	return project, err
}

type platformFlagOutcome struct {
	Attempt   core.FlagAttempt              `json:"attempt"`
	Result    *ctfplatform.SubmissionResult `json:"result,omitempty"`
	Duplicate bool                          `json:"duplicate"`
}

func (h *Handler) submitPlatformFlag(ctx context.Context, project string, flagID int64) (platformFlagOutcome, error) {
	detail, err := h.DB.GetProject(ctx, project)
	if err != nil {
		return platformFlagOutcome{}, err
	}
	if detail.Platform == nil {
		return platformFlagOutcome{}, &ctfplatform.Error{HTTPStatus: 409, Code: "project_not_bound", Message: "project has no platform challenge binding"}
	}
	binding := detail.Platform
	platform, scope, err := h.platform(binding.Platform)
	if err != nil {
		return platformFlagOutcome{}, err
	}
	if scope != binding.ScopeID {
		return platformFlagOutcome{}, &ctfplatform.Error{HTTPStatus: 409, Code: "platform_task_changed", Message: "configured credentials refer to a different task; restore the original task credentials"}
	}
	flags, err := h.DB.ListFlags(ctx, project)
	if err != nil {
		return platformFlagOutcome{}, err
	}
	var flag *core.Flag
	for i := range flags {
		if flags[i].ID == flagID {
			flag = &flags[i]
			break
		}
	}
	if flag == nil {
		return platformFlagOutcome{}, core.ErrNotFound
	}
	if flag.ChallengeID != "" && flag.ChallengeID != binding.ChallengeID {
		return platformFlagOutcome{}, &ctfplatform.Error{HTTPStatus: 422, Code: "challenge_mismatch", Message: "flag belongs to a different challenge"}
	}
	submission := ctfplatform.Submission{ChallengeID: binding.ChallengeID, Flag: flag.Value}
	if validator, ok := platform.(ctfplatform.SubmissionValidator); ok {
		if err := validator.ValidateSubmission(submission); err != nil {
			return platformFlagOutcome{}, err
		}
	}
	attempt, err := h.DB.BeginFlagAttempt(ctx, project, flag.ID)
	if err != nil {
		return platformFlagOutcome{}, err
	}
	result, submitErr := platform.SubmitFlag(ctx, submission)
	out := platformFlagOutcome{Attempt: attempt}
	status := "submission_unknown"
	var response []byte
	if submitErr == nil {
		status = "rejected"
		if result.Correct {
			status = "accepted"
		}
		out.Result = &result
		response, _ = json.Marshal(result)
	} else {
		var upstream *ctfplatform.Error
		if errors.As(submitErr, &upstream) {
			response, _ = json.Marshal(upstream)
			if upstream.Code == "duplicate" && upstream.HTTPStatus == 409 {
				status, out.Duplicate = "accepted", true
			} else if upstream.HTTPStatus >= 400 && upstream.HTTPStatus < 500 {
				status = "rejected"
			}
		} else {
			response, _ = json.Marshal(iris.Map{"code": "request_interrupted", "message": "submission result is unknown"})
		}
	}
	// Cancellation after the remote write must still leave a durable result.
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	out.Attempt, err = h.DB.ResolveFlagAttempt(finish, project, flag.ID, attempt.ID, core.ResolveFlagAttempt{Status: status, Response: string(response)})
	if err != nil {
		return out, fmt.Errorf("platform request finished but saving attempt %d failed: %w", attempt.ID, err)
	}
	if out.Duplicate {
		return out, nil
	}
	return out, submitErr
}

func (h *Handler) registerPlatforms(app *iris.Application) {
	app.Get("/api/platforms", func(ctx iris.Context) {
		out := []iris.Map{}
		for _, descriptor := range h.Platforms.Descriptors() {
			_, configured := h.PlatformConfigs[descriptor.ID]
			out = append(out, iris.Map{"id": descriptor.ID, "name": descriptor.Name, "configured": configured, "capabilities": descriptor})
		}
		ctx.JSON(out)
	})
	app.Get("/api/platforms/{platform:string}/challenges", func(ctx iris.Context) {
		platform, _, err := h.platform(ctx.Params().Get("platform"))
		if err != nil {
			platformError(ctx, err)
			return
		}
		value, err := platform.ListChallenges(ctx.Request().Context())
		if err != nil {
			platformError(ctx, err)
			return
		}
		ctx.JSON(value)
	})
	app.Post("/api/platforms/{platform:string}/import", func(ctx iris.Context) {
		var request platformImportRequest
		if !read(ctx, &request) {
			return
		}
		value, err := h.importPlatformChallenge(ctx.Request().Context(), ctx.Params().Get("platform"), request)
		if err != nil {
			platformError(ctx, err)
			return
		}
		ctx.JSON(value)
	})
	app.Post("/api/platforms/{platform:string}/submit", func(ctx iris.Context) {
		var request ctfplatform.Submission
		if !read(ctx, &request) {
			return
		}
		platform, _, err := h.platform(ctx.Params().Get("platform"))
		if err != nil {
			platformError(ctx, err)
			return
		}
		value, err := platform.SubmitFlag(ctx.Request().Context(), request)
		if err != nil {
			platformError(ctx, err)
			return
		}
		ctx.JSON(value)
	})
	for _, action := range []string{"start", "hint", "close"} {
		method := http.MethodPost
		if action == "hint" {
			method = http.MethodGet
		}
		app.Handle(method, "/api/platforms/{platform:string}/"+action, func(ctx iris.Context) {
			platform, _, err := h.platform(ctx.Params().Get("platform"))
			if err != nil {
				platformError(ctx, err)
				return
			}
			id := ctx.URLParam("challenge_id")
			if strings.TrimSpace(id) == "" {
				platformError(ctx, &ctfplatform.Error{HTTPStatus: 422, Code: "invalid_input", Message: "challenge_id is required"})
				return
			}
			var value any
			switch action {
			case "start":
				value, err = platform.Deliver(ctx.Request().Context(), id)
			case "hint":
				if provider, ok := platform.(ctfplatform.HintProvider); ok {
					value, err = provider.GetHint(ctx.Request().Context(), id)
				} else {
					err = unsupportedOperation(action)
				}
			case "close":
				if closer, ok := platform.(ctfplatform.InstanceCloser); ok {
					value, err = closer.Close(ctx.Request().Context(), id)
				} else {
					err = unsupportedOperation(action)
				}
			}
			if err != nil {
				platformError(ctx, err)
				return
			}
			ctx.JSON(value)
		})
	}
	app.Post("/projects/{project_id:string}/flags/{flag_id:uint64}/submit", func(ctx iris.Context) {
		flagID, _ := strconv.ParseInt(ctx.Params().Get("flag_id"), 10, 64)
		value, err := h.submitPlatformFlag(ctx.Request().Context(), ctx.Params().Get("project_id"), flagID)
		if err != nil {
			platformError(ctx, err)
			return
		}
		ctx.JSON(value)
	})
}

func unsupportedOperation(operation string) error {
	return &ctfplatform.Error{HTTPStatus: 422, Code: "unsupported_operation", Message: "platform does not support " + operation}
}
