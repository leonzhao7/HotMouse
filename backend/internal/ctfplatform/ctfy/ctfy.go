// Package ctfy adapts the TPCTF/CTFy agent_play API to HotMouse's platform
// contract. The API has a competition-scoped instance lifecycle, so this
// adapter selects one registered competition and keeps the instance id for
// each challenge while a HotMouse service is running.
package ctfy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"hotmouse/backend/internal/ctfplatform"
	"hotmouse/backend/internal/tpctf"
)

var Info = ctfplatform.Descriptor{
	ID: "ctfy", Name: "CTFy / TPCTF", Authentication: "Bearer agent_play",
	DeliveryMode: "instance", SubmissionMode: "api", SupportsClose: true,
	// The platform may return a simple or engagement topology per instance.
	// The latter requires the VPN supplied by the platform.
	RequiresVPN: false,
}

type Client struct {
	api           *tpctf.Client
	competitionID string
	maxInstances  int
	mu            sync.Mutex
	instances     map[string]string
}

var _ ctfplatform.Platform = (*Client)(nil)
var _ ctfplatform.InstanceVisibilityLister = (*Client)(nil)
var _ ctfplatform.InstanceCloser = (*Client)(nil)
var _ ctfplatform.DeliverySubmitter = (*Client)(nil)
var _ ctfplatform.DeliveryCloser = (*Client)(nil)
var _ ctfplatform.InstanceCloserByID = (*Client)(nil)

func New(config ctfplatform.Config) (*Client, error) {
	api, err := tpctf.NewClient(strings.TrimRight(strings.TrimSpace(config.BaseURL), "/"), config.Token)
	if err != nil {
		return nil, &ctfplatform.Error{HTTPStatus: 422, Code: "invalid_configuration", Message: err.Error()}
	}
	competitionID := ""
	if config.Options != nil {
		competitionID = strings.TrimSpace(config.Options["competition_id"])
	}
	return &Client{api: api, competitionID: competitionID, instances: map[string]string{}}, nil
}

func (c *Client) Descriptor() ctfplatform.Descriptor {
	c.mu.Lock()
	defer c.mu.Unlock()
	info := Info
	info.MaxActiveInstances = c.maxInstances
	return info
}

func upstream(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var apiErr *tpctf.APIError
	if errors.As(err, &apiErr) {
		return &ctfplatform.Error{HTTPStatus: apiErr.Status, Code: apiErr.Code, Detail: apiErr.Detail, Message: apiErr.Error()}
	}
	return &ctfplatform.Error{HTTPStatus: 502, Code: "platform_transport_error", Message: "CTFy request failed: " + err.Error()}
}

func (c *Client) competition(ctx context.Context) (tpctf.Competition, error) {
	competitions, err := c.api.Competitions(ctx)
	if err != nil {
		return tpctf.Competition{}, upstream(err)
	}
	c.mu.Lock()
	configuredID := c.competitionID
	c.mu.Unlock()
	if configuredID != "" {
		for _, competition := range competitions {
			if competition.ID == configuredID {
				c.mu.Lock()
				c.maxInstances = competition.MaxInstances
				c.mu.Unlock()
				return competition, nil
			}
		}
		return tpctf.Competition{}, &ctfplatform.Error{HTTPStatus: 404, Code: "competition_not_found", Message: "configured CTFy competition is not visible"}
	}
	candidates := make([]tpctf.Competition, 0, len(competitions))
	ended := make([]tpctf.Competition, 0, len(competitions))
	for _, competition := range competitions {
		if competition.Registered {
			if competition.Phase == "past" {
				ended = append(ended, competition)
			} else {
				candidates = append(candidates, competition)
			}
		}
	}
	if len(candidates) == 0 {
		candidates = ended
	}
	if len(candidates) != 1 {
		return tpctf.Competition{}, &ctfplatform.Error{HTTPStatus: 422, Code: "competition_selection_required", Message: fmt.Sprintf("set CTFY_COMPETITION_ID when %d active registered competitions are visible", len(candidates))}
	}
	c.mu.Lock()
	c.competitionID = candidates[0].ID
	c.maxInstances = candidates[0].MaxInstances
	c.mu.Unlock()
	return candidates[0], nil
}

func challengeView(competitionID string, row tpctf.Challenge, instance tpctf.Instance) ctfplatform.Challenge {
	description := row.Description
	attachments := make([]ctfplatform.Attachment, 0, len(row.Attachments))
	for _, attachment := range row.Attachments {
		address := attachment.DownloadURL
		if address == "" {
			address = attachment.URL
		}
		attachments = append(attachments, ctfplatform.Attachment{Name: attachment.Name, URL: address})
	}
	status := "stopped"
	metadata := map[string]any{"competition_id": competitionID, "category": row.Category, "tags": row.Tags, "scoring": row.Scoring, "closed": row.Closed, "authored": row.Own, "questions": row.Questions}
	if instance.ID != "" {
		status = instance.Status
		metadata["instance_id"] = instance.ID
	}
	return ctfplatform.Challenge{
		ID: row.ID, Title: row.Name, Description: &description, Difficulty: row.Difficulty,
		FlagCount: len(row.Questions), CorrectFlagCount: len(row.SolvedIDs), Completed: row.Solved,
		InstanceStatus: status, Addresses: instanceAddresses(instance), Attachments: attachments,
		Metadata: metadata,
	}
}

func activeInstances(instances []tpctf.Instance, competitionID string) map[string]tpctf.Instance {
	active := make(map[string]tpctf.Instance)
	for _, instance := range instances {
		if instance.ID == "" || instance.ChallengeID == "" || instance.CompetitionID != "" && instance.CompetitionID != competitionID {
			continue
		}
		switch instance.Status {
		case "ready", "starting", "pending":
			active[instance.ChallengeID] = instance
		}
	}
	return active
}

func (c *Client) ListChallenges(ctx context.Context) ([]ctfplatform.Challenge, error) {
	competition, err := c.competition(ctx)
	if err != nil {
		return nil, err
	}
	if competition.Phase == "upcoming" {
		return nil, &ctfplatform.Error{HTTPStatus: 403, Code: "competition_not_started", Message: "CTFy competition has not started"}
	}
	if competition.Phase == "past" {
		return nil, &ctfplatform.Error{HTTPStatus: 403, Code: "competition_not_in_session", Message: "CTFy competition has ended"}
	}
	c.mu.Lock()
	c.maxInstances = competition.MaxInstances
	c.mu.Unlock()
	rows, err := c.api.Challenges(ctx, competition.ID)
	if err != nil {
		return nil, upstream(err)
	}
	instances, err := c.api.Instances(ctx, competition.ID)
	if err != nil {
		return nil, upstream(err)
	}
	active := activeInstances(instances, competition.ID)
	out := make([]ctfplatform.Challenge, 0, len(rows))
	for _, row := range rows {
		if row.Closed || row.Own || row.Scoring == "closed" {
			continue
		}
		out = append(out, challengeView(competition.ID, row, active[row.ID]))
	}
	return out, nil
}

func (c *Client) ListChallengesWithInstanceVisibility(ctx context.Context) ([]ctfplatform.Challenge, error) {
	challenges, err := c.ListChallenges(ctx)
	if err != nil || len(challenges) == 0 {
		return challenges, err
	}
	visible := 0
	for _, challenge := range challenges {
		if challenge.InstanceStatus == "ready" || challenge.InstanceStatus == "starting" || challenge.InstanceStatus == "pending" {
			visible++
		}
	}
	if visible > 0 {
		return challenges, nil
	}
	// /me can report team instances that this token cannot enumerate through
	// /instances. Their challenge IDs are unknown, so do not assign them to rows.
	competitionID, _ := challenges[0].Metadata["competition_id"].(string)
	teamRunning, err := c.api.TeamRunningInstances(ctx, competitionID)
	if err != nil || teamRunning == 0 {
		return challenges, nil
	}
	for i := range challenges {
		if challenges[i].InstanceStatus != "stopped" {
			continue
		}
		challenges[i].InstanceStatus = "unknown"
		challenges[i].Metadata["instance_visibility"] = "incomplete"
		challenges[i].Metadata["team_running_instances"] = teamRunning
	}
	return challenges, nil
}

func wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func instanceAddresses(instance tpctf.Instance) []string {
	addresses := append([]string{}, instance.URLs...)
	for _, service := range instance.Services {
		if service.Host == "" {
			continue
		}
		if service.Type == "http" || service.Type == "https" {
			scheme := service.Type
			if scheme == "http" && service.Port == 443 {
				scheme = "https"
			}
			path := service.Path
			if path == "" {
				path = "/"
			}
			addresses = append(addresses, fmt.Sprintf("%s://%s%s", scheme, net.JoinHostPort(service.Host, fmt.Sprint(service.Port)), path))
		} else {
			addresses = append(addresses, net.JoinHostPort(service.Host, fmt.Sprint(service.Port)))
		}
	}
	return addresses
}

func (c *Client) ready(ctx context.Context, competitionID, instanceID string) (tpctf.Instance, error) {
	for {
		instance, err := c.api.Instance(ctx, competitionID, instanceID)
		if err != nil {
			return instance, upstream(err)
		}
		switch instance.Status {
		case "ready":
			return instance, nil
		case "error", "stopped":
			return instance, &ctfplatform.Error{HTTPStatus: 502, Code: "instance_start_failed", Message: "CTFy instance failed: " + instance.ErrorCode}
		}
		delay := 250 * time.Millisecond
		if instance.PollAfter > 0 && time.Duration(instance.PollAfter*float64(time.Second)) > delay {
			delay = time.Duration(instance.PollAfter * float64(time.Second))
		}
		if err := wait(ctx, delay); err != nil {
			return instance, err
		}
	}
}

func (c *Client) Deliver(ctx context.Context, challengeID string) (ctfplatform.Delivery, error) {
	if strings.TrimSpace(challengeID) == "" {
		return ctfplatform.Delivery{}, &ctfplatform.Error{HTTPStatus: 422, Code: "invalid_input", Message: "challenge_id must not be empty"}
	}
	competition, err := c.competition(ctx)
	if err != nil {
		return ctfplatform.Delivery{}, err
	}
	detail, err := c.api.Challenge(ctx, competition.ID, challengeID)
	if err != nil {
		return ctfplatform.Delivery{}, upstream(err)
	}
	instances, err := c.api.Instances(ctx, competition.ID)
	if err != nil {
		return ctfplatform.Delivery{}, upstream(err)
	}
	instance := activeInstances(instances, competition.ID)[challengeID]
	startedByHotMouse := false
	if instance.ID == "" {
		instance, err = c.api.Start(ctx, competition.ID, challengeID)
		startedByHotMouse = err == nil
	}
	if err != nil {
		var apiErr *tpctf.APIError
		if errors.As(err, &apiErr) && apiErr.Code == "instance_already_running" {
			var body struct {
				InstanceID string `json:"instance_id"`
				ID         string `json:"id"`
			}
			_ = json.Unmarshal(apiErr.Detail, &body)
			if body.InstanceID == "" {
				body.InstanceID = body.ID
			}
			if body.InstanceID == "" {
				return ctfplatform.Delivery{}, &ctfplatform.Error{HTTPStatus: 409, Code: "instance_already_running", Message: "CTFy returned no existing instance id"}
			}
			instance.ID = body.InstanceID
		} else {
			return ctfplatform.Delivery{}, upstream(err)
		}
	}
	if instance.ID == "" {
		return ctfplatform.Delivery{}, &ctfplatform.Error{HTTPStatus: 502, Code: "protocol_error", Message: "CTFy start response is missing instance id"}
	}
	instance, err = c.ready(ctx, competition.ID, instance.ID)
	if err != nil {
		return ctfplatform.Delivery{}, err
	}
	c.mu.Lock()
	c.instances[challengeID] = instance.ID
	c.mu.Unlock()
	metadata := map[string]any{
		"competition_id": competition.ID, "instance_id": instance.ID,
		"started_by_hotmouse": startedByHotMouse,
		"challenge_name":      detail.Name, "challenge_description": detail.Description,
		"category": detail.Category, "difficulty": detail.Difficulty,
		"scoring": detail.Scoring, "questions": instance.Questions,
		"services": instance.Services, "entry_urls": instance.URLs,
		"challenge_questions": detail.Questions, "network_topology": instance.Topology,
	}
	if instance.VPN != nil {
		metadata["vpn_config_url"] = instance.VPN.ConfigURL
	}
	return ctfplatform.Delivery{ChallengeID: challengeID, Mode: "instance", Addresses: instanceAddresses(instance), Attachments: attachments(detail), Metadata: metadata}, nil
}

func attachments(challenge tpctf.Challenge) []ctfplatform.Attachment {
	out := make([]ctfplatform.Attachment, 0, len(challenge.Attachments))
	for _, item := range challenge.Attachments {
		address := item.DownloadURL
		if address == "" {
			address = item.URL
		}
		out = append(out, ctfplatform.Attachment{Name: item.Name, URL: address})
	}
	return out
}

func (c *Client) instanceFor(ctx context.Context, challengeID string) (tpctf.Instance, string, error) {
	competition, err := c.competition(ctx)
	if err != nil {
		return tpctf.Instance{}, "", err
	}
	c.mu.Lock()
	instanceID := c.instances[challengeID]
	c.mu.Unlock()
	if instanceID == "" {
		return tpctf.Instance{}, competition.ID, &ctfplatform.Error{HTTPStatus: 409, Code: "instance_not_running", Message: "start this CTFy challenge before submitting a flag"}
	}
	instance, err := c.api.Instance(ctx, competition.ID, instanceID)
	if err != nil {
		return instance, competition.ID, upstream(err)
	}
	return instance, competition.ID, nil
}

func answerValue(question tpctf.Question, flag string) any {
	if len(question.Choices) == 0 {
		return flag
	}
	var values []string
	if json.Unmarshal([]byte(flag), &values) == nil {
		return values
	}
	return []string{flag}
}

func (c *Client) SubmitFlag(ctx context.Context, submission ctfplatform.Submission) (ctfplatform.SubmissionResult, error) {
	if submission.InstanceID != "" && submission.CompetitionID != "" {
		instance, err := c.api.Instance(ctx, submission.CompetitionID, submission.InstanceID)
		if err != nil {
			return ctfplatform.SubmissionResult{}, upstream(err)
		}
		if instance.ChallengeID != "" && instance.ChallengeID != submission.ChallengeID || instance.CompetitionID != "" && instance.CompetitionID != submission.CompetitionID {
			return ctfplatform.SubmissionResult{}, &ctfplatform.Error{HTTPStatus: 409, Code: "wrong_team_for_instance", Message: "CTFy instance identity does not match the submitted challenge"}
		}
		return c.submit(ctx, instance, submission)
	}
	instance, _, err := c.instanceFor(ctx, submission.ChallengeID)
	if err != nil {
		return ctfplatform.SubmissionResult{}, err
	}
	return c.submit(ctx, instance, submission)
}

func deliveryString(delivery ctfplatform.Delivery, key string) string {
	if delivery.Metadata == nil {
		return ""
	}
	value, _ := delivery.Metadata[key].(string)
	return value
}

func (c *Client) SubmitDelivery(ctx context.Context, delivery ctfplatform.Delivery, submission ctfplatform.Submission) (ctfplatform.SubmissionResult, error) {
	if delivery.ChallengeID != submission.ChallengeID {
		return ctfplatform.SubmissionResult{}, &ctfplatform.Error{HTTPStatus: 422, Code: "challenge_mismatch", Message: "saved CTFy delivery belongs to another challenge"}
	}
	instanceID := deliveryString(delivery, "instance_id")
	competitionID := deliveryString(delivery, "competition_id")
	if instanceID == "" || competitionID == "" {
		return ctfplatform.SubmissionResult{}, &ctfplatform.Error{HTTPStatus: 409, Code: "instance_metadata_missing", Message: "saved CTFy delivery is missing its instance identifiers"}
	}
	instance, err := c.api.Instance(ctx, competitionID, instanceID)
	if err != nil {
		return ctfplatform.SubmissionResult{}, upstream(err)
	}
	if instance.ChallengeID != "" && instance.ChallengeID != submission.ChallengeID || instance.CompetitionID != "" && instance.CompetitionID != competitionID {
		return ctfplatform.SubmissionResult{}, &ctfplatform.Error{HTTPStatus: 409, Code: "wrong_team_for_instance", Message: "CTFy instance identity does not match the saved challenge delivery"}
	}
	return c.submit(ctx, instance, submission)
}

func (c *Client) submit(ctx context.Context, instance tpctf.Instance, submission ctfplatform.Submission) (ctfplatform.SubmissionResult, error) {
	var question *tpctf.Question
	questionID := submission.QuestionID
	if questionID == "" {
		questionID = "flag"
	}
	for i := range instance.Questions {
		candidate := &instance.Questions[i]
		if candidate.ID == questionID && candidate.Unlocked && !candidate.Correct && (candidate.AttemptsRemaining == nil || *candidate.AttemptsRemaining > 0) {
			question = candidate
			break
		}
	}
	if question == nil {
		for i := range instance.Questions {
			candidate := &instance.Questions[i]
			if candidate.ID == questionID {
				if !candidate.Unlocked {
					return ctfplatform.SubmissionResult{}, &ctfplatform.Error{HTTPStatus: 409, Code: "question_locked", Message: "CTFy question is locked"}
				}
				if candidate.Correct || candidate.AttemptsRemaining != nil && *candidate.AttemptsRemaining <= 0 {
					return ctfplatform.SubmissionResult{}, &ctfplatform.Error{HTTPStatus: 409, Code: "question_not_accepting_answers", Message: "CTFy question is already answered or its attempt cap is exhausted"}
				}
			}
		}
		return ctfplatform.SubmissionResult{}, &ctfplatform.Error{HTTPStatus: 422, Code: "unknown_question_id", Message: "CTFy instance has no such question"}
	}
	verdict, err := c.api.Submit(ctx, instance.ID, question.ID, answerValue(*question, submission.Flag))
	if err != nil {
		return ctfplatform.SubmissionResult{}, upstream(err)
	}
	correctCount := 0
	for _, item := range instance.Questions {
		if item.Correct {
			correctCount++
		}
	}
	if verdict.Correct {
		correctCount++
	}
	fullySolved := verdict.FullySolved
	return ctfplatform.SubmissionResult{Correct: verdict.Correct, ChallengeComplete: &fullySolved, CorrectFlagCount: correctCount, TotalFlagCount: len(instance.Questions), Metadata: map[string]any{"question_id": question.ID, "ranked": verdict.Ranked, "unranked_reason": verdict.Reason, "solve_rank": verdict.SolveRank, "solve_time_s": verdict.SolveTime}}, nil
}

func (c *Client) Close(ctx context.Context, challengeID string) (ctfplatform.CloseResult, error) {
	competition, err := c.competition(ctx)
	if err != nil {
		return ctfplatform.CloseResult{}, err
	}
	c.mu.Lock()
	instanceID := c.instances[challengeID]
	c.mu.Unlock()
	if instanceID == "" {
		instances, err := c.api.Instances(ctx, competition.ID)
		if err != nil {
			return ctfplatform.CloseResult{}, upstream(err)
		}
		for _, instance := range instances {
			if instance.ChallengeID != challengeID || instance.ID == "" || instance.CompetitionID != "" && instance.CompetitionID != competition.ID || instance.Status == "stopped" || instance.Status == "error" {
				continue
			}
			instanceID = instance.ID
			break
		}
	}
	if instanceID == "" {
		return ctfplatform.CloseResult{ChallengeID: challengeID, Closed: true}, nil
	}
	return c.CloseInstance(ctx, competition.ID, instanceID)
}

func (c *Client) CloseDelivery(ctx context.Context, delivery ctfplatform.Delivery) (ctfplatform.CloseResult, error) {
	competitionID := deliveryString(delivery, "competition_id")
	instanceID := deliveryString(delivery, "instance_id")
	if competitionID == "" || instanceID == "" {
		return ctfplatform.CloseResult{}, &ctfplatform.Error{HTTPStatus: 409, Code: "instance_metadata_missing", Message: "saved CTFy delivery is missing its instance identifiers"}
	}
	result, err := c.CloseInstance(ctx, competitionID, instanceID)
	if result.ChallengeID == "" {
		result.ChallengeID = delivery.ChallengeID
	}
	return result, err
}

func (c *Client) CloseInstance(ctx context.Context, competitionID, instanceID string) (ctfplatform.CloseResult, error) {
	if strings.TrimSpace(competitionID) == "" || strings.TrimSpace(instanceID) == "" {
		return ctfplatform.CloseResult{}, &ctfplatform.Error{HTTPStatus: 422, Code: "invalid_input", Message: "competition_id and instance_id are required"}
	}
	instance, err := c.api.Instance(ctx, competitionID, instanceID)
	if err != nil {
		if errorNotFound(err) {
			c.forgetInstance(instanceID)
			return ctfplatform.CloseResult{Closed: true}, nil
		}
		return ctfplatform.CloseResult{}, upstream(err)
	}
	if instance.Status == "stopped" {
		c.forgetInstance(instanceID)
		return ctfplatform.CloseResult{ChallengeID: instance.ChallengeID, Closed: true}, nil
	}
	if instance.Status != "stopping" {
		if err = c.api.Stop(ctx, competitionID, instanceID); err != nil {
			if errorNotFound(err) {
				c.forgetInstance(instanceID)
				return ctfplatform.CloseResult{Closed: true}, nil
			}
			return ctfplatform.CloseResult{}, upstream(err)
		}
	}
	for {
		instance, err = c.api.Instance(ctx, competitionID, instanceID)
		if err != nil {
			if errorNotFound(err) {
				c.forgetInstance(instanceID)
				return ctfplatform.CloseResult{Closed: true}, nil
			}
			return ctfplatform.CloseResult{}, upstream(err)
		}
		if instance.Status == "stopped" {
			c.forgetInstance(instanceID)
			return ctfplatform.CloseResult{ChallengeID: instance.ChallengeID, Closed: true}, nil
		}
		if err = wait(ctx, 250*time.Millisecond); err != nil {
			return ctfplatform.CloseResult{ChallengeID: instance.ChallengeID, Closed: false}, err
		}
	}
}

func (c *Client) forgetInstance(instanceID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for challengeID, current := range c.instances {
		if current == instanceID {
			delete(c.instances, challengeID)
		}
	}
}

func errorNotFound(err error) bool {
	var apiErr *tpctf.APIError
	return errors.As(err, &apiErr) && apiErr.Status == 404
}

func Register(registry *ctfplatform.Registry) error {
	return registry.Register(Info, func(config ctfplatform.Config) (ctfplatform.Platform, error) { return New(config) })
}
