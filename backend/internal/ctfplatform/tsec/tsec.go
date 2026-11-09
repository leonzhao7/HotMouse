// Package tsec implements docs/tsec_challenges_api.md.
package tsec

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"hotmouse/backend/internal/ctfplatform"
)

var Info = ctfplatform.Descriptor{
	ID: "tsec", Name: "TSec Benchmark", Authentication: "header:BENCHMARK_TOKEN",
	DeliveryMode: "instance", SubmissionMode: "api", SupportsHints: true,
	HintCostsScore: true, SupportsClose: true, MaxActiveInstances: 3, RequiresVPN: true,
}

type Client struct {
	base  *url.URL
	token string
	http  *http.Client
}

var _ ctfplatform.Platform = (*Client)(nil)
var _ ctfplatform.HintProvider = (*Client)(nil)
var _ ctfplatform.InstanceCloser = (*Client)(nil)

func New(config ctfplatform.Config) (*Client, error) {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(config.BaseURL), "/"))
	if err != nil || base == nil || (base.Scheme != "http" && base.Scheme != "https") || base.Hostname() == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, &ctfplatform.Error{HTTPStatus: 422, Code: "invalid_configuration", Message: "BENCHMARK_BASE_URL must be an HTTP(S) base URL without credentials, query or fragment"}
	}
	if strings.TrimSpace(config.Token) == "" || strings.ContainsAny(config.Token, "\r\n") {
		return nil, &ctfplatform.Error{HTTPStatus: 422, Code: "invalid_configuration", Message: "BENCHMARK_TOKEN is required and must be a valid header value"}
	}
	client := http.Client{Timeout: 30 * time.Second}
	if config.HTTPClient != nil {
		client = *config.HTTPClient
		if client.Timeout == 0 {
			client.Timeout = 30 * time.Second
		}
	}
	// Never forward the task token to a redirect destination or repeat a POST.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{base: base, token: config.Token, http: &client}, nil
}

func (c *Client) Descriptor() ctfplatform.Descriptor { return Info }

func validateID(id string) error {
	if strings.TrimSpace(id) == "" {
		return &ctfplatform.Error{HTTPStatus: 422, Code: "invalid_input", Message: "challenge_id must not be empty"}
	}
	return nil
}

func (c *Client) request(ctx context.Context, method, action, id string, body any, output any) error {
	endpoint := c.base.JoinPath("openapi/v1/challenges", action)
	if id != "" {
		query := endpoint.Query()
		query.Set("unique_code", id)
		endpoint.RawQuery = query.Encode()
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), reader)
	if err != nil {
		return err
	}
	req.Header.Set("BENCHMARK_TOKEN", c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &ctfplatform.Error{HTTPStatus: 502, Code: "transport_error", Message: "TSec request failed; query progress before retrying a write"}
	}
	defer response.Body.Close()
	const maxBody = 8 << 20
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil || len(data) > maxBody {
		return &ctfplatform.Error{HTTPStatus: 502, Code: "protocol_error", Message: "TSec response could not be read completely"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		failure := &ctfplatform.Error{HTTPStatus: response.StatusCode, Code: "http_error", Message: http.StatusText(response.StatusCode)}
		var payload struct {
			Code    string          `json:"code"`
			Message string          `json:"message"`
			Detail  json.RawMessage `json:"detail"`
		}
		if json.Unmarshal(data, &payload) == nil {
			if payload.Code != "" {
				failure.Code = payload.Code
			} else if response.StatusCode == 422 {
				failure.Code = "validation_error"
			}
			if payload.Message != "" {
				failure.Message = redactText(payload.Message, c.token, body)
			}
			// Sanitize JSON string values rather than serialized bytes so quoted
			// tokens and flags cannot escape masking or invalidate the JSON.
			failure.Detail = redactDetail(payload.Detail, c.token, body)
		}
		return failure
	}
	if err := json.Unmarshal(data, output); err != nil || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return &ctfplatform.Error{HTTPStatus: 502, Code: "protocol_error", Message: "TSec returned invalid JSON"}
	}
	return nil
}

func redactDetail(raw json.RawMessage, token string, body any) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	var redact func(any) any
	redact = func(value any) any {
		switch v := value.(type) {
		case string:
			return redactText(v, token, body)
		case []any:
			for i := range v {
				v[i] = redact(v[i])
			}
		case map[string]any:
			for key := range v {
				v[key] = redact(v[key])
			}
		}
		return value
	}
	result, _ := json.Marshal(redact(value))
	return result
}

func redactText(value, token string, body any) string {
	value = strings.ReplaceAll(value, token, "[REDACTED]")
	if submission, ok := body.(submitRequest); ok && submission.Flag != "" {
		value = strings.ReplaceAll(value, submission.Flag, "[REDACTED]")
	}
	return value
}

type challengeResponse struct {
	ID               string   `json:"unique_code"`
	Description      *string  `json:"description"`
	Difficulty       string   `json:"difficulty"`
	Level            int      `json:"level"`
	TotalScore       int      `json:"total_score"`
	FlagCount        int      `json:"flag_count"`
	CorrectFlagCount int      `json:"correct_flag_count"`
	Completed        bool     `json:"is_completed"`
	Status           string   `json:"container_status"`
	Addresses        []string `json:"container_addr"`
}

func (c *Client) ListChallenges(ctx context.Context) ([]ctfplatform.Challenge, error) {
	var rows []challengeResponse
	if err := c.request(ctx, http.MethodGet, "", "", nil, &rows); err != nil {
		return nil, err
	}
	out := make([]ctfplatform.Challenge, 0, len(rows))
	for _, row := range rows {
		if row.ID == "" {
			return nil, &ctfplatform.Error{HTTPStatus: 502, Code: "protocol_error", Message: "TSec challenge is missing unique_code"}
		}
		addresses := []string{}
		if row.Status == "available" {
			addresses = append(addresses, row.Addresses...)
		}
		out = append(out, ctfplatform.Challenge{ID: row.ID, Title: row.ID, Description: row.Description,
			Difficulty: row.Difficulty, Level: row.Level, TotalScore: row.TotalScore,
			FlagCount: row.FlagCount, CorrectFlagCount: row.CorrectFlagCount, Completed: row.Completed,
			InstanceStatus: row.Status, Addresses: addresses, Attachments: []ctfplatform.Attachment{}})
	}
	return out, nil
}

func (c *Client) Deliver(ctx context.Context, id string) (ctfplatform.Delivery, error) {
	if err := validateID(id); err != nil {
		return ctfplatform.Delivery{}, err
	}
	var response struct {
		ID        string   `json:"unique_code"`
		Addresses []string `json:"container_addr"`
	}
	if err := c.request(ctx, http.MethodPost, "start", id, nil, &response); err != nil {
		return ctfplatform.Delivery{}, err
	}
	if response.ID != id || len(response.Addresses) == 0 {
		return ctfplatform.Delivery{}, &ctfplatform.Error{HTTPStatus: 502, Code: "protocol_error", Message: "TSec start response has no matching challenge or usable addresses; query progress before retrying"}
	}
	return ctfplatform.Delivery{ChallengeID: id, Mode: "instance", Addresses: response.Addresses, Attachments: []ctfplatform.Attachment{}}, nil
}

func (c *Client) GetHint(ctx context.Context, id string) (ctfplatform.Hint, error) {
	if err := validateID(id); err != nil {
		return ctfplatform.Hint{}, err
	}
	var response struct {
		ID   string  `json:"unique_code"`
		Hint *string `json:"hint"`
	}
	if err := c.request(ctx, http.MethodGet, "hint", id, nil, &response); err != nil {
		return ctfplatform.Hint{}, err
	}
	if response.ID != id {
		return ctfplatform.Hint{}, mismatchedResponse()
	}
	return ctfplatform.Hint{ChallengeID: id, Content: response.Hint}, nil
}

type submitRequest struct {
	ID   string `json:"unique_code"`
	Flag string `json:"flag"`
}

func (c *Client) SubmitFlag(ctx context.Context, submission ctfplatform.Submission) (ctfplatform.SubmissionResult, error) {
	if err := c.ValidateSubmission(submission); err != nil {
		return ctfplatform.SubmissionResult{}, err
	}
	// A pointer distinguishes an explicit wrong answer from a missing field.
	var response struct {
		ctfplatform.SubmissionResult
		Correct *bool `json:"correct"`
	}
	err := c.request(ctx, http.MethodPost, "submit", "", submitRequest{submission.ChallengeID, submission.Flag}, &response)
	if err != nil {
		return ctfplatform.SubmissionResult{}, err
	}
	if response.Correct == nil {
		return ctfplatform.SubmissionResult{}, &ctfplatform.Error{HTTPStatus: 502, Code: "protocol_error", Message: "TSec submission response is missing correct"}
	}
	response.SubmissionResult.Correct = *response.Correct
	return response.SubmissionResult, nil
}

func (c *Client) ValidateSubmission(submission ctfplatform.Submission) error {
	if err := validateID(submission.ChallengeID); err != nil {
		return err
	}
	length := utf8.RuneCountInString(submission.Flag)
	if !utf8.ValidString(submission.Flag) || length < 1 || length > 4096 {
		return &ctfplatform.Error{HTTPStatus: 422, Code: "invalid_input", Message: "flag must contain 1 to 4096 Unicode characters"}
	}
	return nil
}

func (c *Client) Close(ctx context.Context, id string) (ctfplatform.CloseResult, error) {
	if err := validateID(id); err != nil {
		return ctfplatform.CloseResult{}, err
	}
	var response struct {
		ID     string `json:"unique_code"`
		Closed *bool  `json:"closed"`
	}
	if err := c.request(ctx, http.MethodPost, "close", id, nil, &response); err != nil {
		return ctfplatform.CloseResult{}, err
	}
	if response.ID != id || response.Closed == nil {
		return ctfplatform.CloseResult{}, mismatchedResponse()
	}
	return ctfplatform.CloseResult{ChallengeID: id, Closed: *response.Closed}, nil
}

func mismatchedResponse() error {
	return &ctfplatform.Error{HTTPStatus: 502, Code: "protocol_error", Message: "TSec returned an incomplete or mismatched challenge response"}
}

func Register(registry *ctfplatform.Registry) error {
	return registry.Register(Info, func(config ctfplatform.Config) (ctfplatform.Platform, error) { return New(config) })
}
