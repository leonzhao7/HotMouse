// Package tpctf implements the TPCTF 2026 participant API (2026-11-13 v1.0).
// Unlike CTFY-0001 hosted runs, this client owns the competition lifecycle.
package tpctf

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type APIError struct {
	Status     int             `json:"-"`
	Code       string          `json:"code"`
	Detail     json.RawMessage `json:"detail"`
	RetryAfter time.Duration   `json:"-"`
}

func (e *APIError) Error() string { return fmt.Sprintf("TPCTF HTTP %d: %s", e.Status, e.Code) }
func errorCode(err error) string {
	var e *APIError
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}
func fatalAPI(err error) bool {
	switch errorCode(err) {
	case "http_401", "http_403", "token_competition_forbidden", "token_missing_scope", "not_registered", "team_disqualified", "team_missing", "review_required_to_play":
		return true
	}
	return false
}

type Client struct {
	base  *url.URL
	token string
	http  *http.Client
}

func NewClient(baseURL, token string) (*Client, error) {
	base, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || base.Host == "" || (base.Scheme != "https" && base.Scheme != "http") || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("CTFY_BASE_URL must be an HTTP(S) API base URL")
	}
	if strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n") {
		return nil, fmt.Errorf("CTFY_TOKEN is required")
	}
	return &Client{base: base, token: token, http: &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) endpoint(path string) string {
	return strings.TrimRight(c.base.String(), "/") + "/" + strings.TrimLeft(path, "/")
}
func (c *Client) request(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	data, err := c.raw(ctx, method, c.endpoint(path), reader, "application/json", true, 8<<20)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) || json.Unmarshal(data, out) != nil {
		return fmt.Errorf("TPCTF %s %s: invalid JSON response", method, path)
	}
	return nil
}
func (c *Client) raw(ctx context.Context, method, address string, reader io.Reader, contentType string, auth bool, max int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, address, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if auth {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if reader != nil {
		req.Header.Set("Content-Type", contentType)
	}
	response, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("TPCTF transport failure; writes are not automatically replayed")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, max+1))
	if err != nil || int64(len(data)) > max {
		return nil, fmt.Errorf("TPCTF response incomplete or too large")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		failure := &APIError{Status: response.StatusCode, Code: "http_" + strconv.Itoa(response.StatusCode)}
		_ = json.Unmarshal(data, failure)
		if n, err := strconv.Atoi(response.Header.Get("Retry-After")); err == nil {
			failure.RetryAfter = time.Duration(n) * time.Second
		} else if when, err := http.ParseTime(response.Header.Get("Retry-After")); err == nil {
			failure.RetryAfter = time.Until(when)
		}
		var detail struct {
			RetryAfter float64 `json:"retry_after_s"`
			Next       string  `json:"next_session_starts_at"`
		}
		_ = json.Unmarshal(failure.Detail, &detail)
		if time.Duration(detail.RetryAfter*float64(time.Second)) > failure.RetryAfter {
			failure.RetryAfter = time.Duration(detail.RetryAfter * float64(time.Second))
		}
		if when, err := time.Parse(time.RFC3339, detail.Next); err == nil && time.Until(when) > failure.RetryAfter {
			failure.RetryAfter = time.Until(when)
		}
		return nil, failure
	}
	return data, nil
}

type Competition struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Phase        string `json:"phase"`
	Registered   bool   `json:"is_registered"`
	StartsAt     string `json:"starts_at"`
	EndsAt       string `json:"ends_at"`
	MaxInstances int    `json:"max_instances_per_team_effective"`
	Authoring    bool   `json:"authoring_enabled"`
}
type Question struct {
	ID                string   `json:"id"`
	Prompt            string   `json:"prompt"`
	Mode              string   `json:"mode"`
	Choices           []string `json:"choices"`
	Requires          []string `json:"requires"`
	Unlocked          bool     `json:"unlocked"`
	Correct           bool     `json:"answered_correctly"`
	AttemptsRemaining *int     `json:"attempts_remaining"`
}
type Attachment struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	DownloadURL string `json:"download_url"`
}
type Challenge struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Category    string       `json:"category"`
	Difficulty  string       `json:"difficulty"`
	Tags        []string     `json:"tags"`
	Questions   []Question   `json:"questions"`
	Attachments []Attachment `json:"attachments"`
	Closed      bool         `json:"closed"`
	Scoring     string       `json:"scoring"`
	Solved      bool         `json:"my_team_fully_solved"`
	SolvedIDs   []string     `json:"my_team_solved_question_ids"`
	Own         bool         `json:"my_team_authored"`
}
type Service struct {
	Label       string `json:"label"`
	Type        string `json:"service_type"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Path        string `json:"path"`
	Access      any    `json:"access"`
	Credentials any    `json:"credentials"`
}
type Instance struct {
	ID            string     `json:"id"`
	Status        string     `json:"status"`
	ChallengeID   string     `json:"challenge_id"`
	CompetitionID string     `json:"competition_id"`
	Services      []Service  `json:"services"`
	URLs          []string   `json:"entry_urls"`
	Questions     []Question `json:"questions"`
	Topology      string     `json:"network_topology"`
	VPN           *struct {
		ConfigURL string `json:"config_url"`
	} `json:"vpn_endpoint"`
	ExpiresAt float64 `json:"expires_at"`
	ErrorCode string  `json:"error_code"`
	PollAfter float64 `json:"poll_after_s"`
}
type Verdict struct {
	Correct           bool    `json:"correct"`
	SolveTime         float64 `json:"solve_time_s"`
	SolveRank         int     `json:"solve_rank"`
	FullySolved       bool    `json:"challenge_fully_solved"`
	Ranked            bool    `json:"ranked"`
	Reason            string  `json:"unranked_reason"`
	AttemptsRemaining *int    `json:"attempts_remaining"`
}

func segment(id string) string { return url.PathEscape(id) }
func list[T any](ctx context.Context, c *Client, path string) ([]T, error) {
	var result []T
	for offset := 0; ; {
		var page struct {
			Items []T `json:"items"`
			Total int `json:"total"`
		}
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		if err := c.request(ctx, http.MethodGet, path+sep+"limit=100&offset="+strconv.Itoa(offset), nil, &page); err != nil {
			return nil, err
		}
		result = append(result, page.Items...)
		offset += len(page.Items)
		if offset >= page.Total || len(page.Items) == 0 {
			return result, nil
		}
	}
}
func (c *Client) Competitions(ctx context.Context) ([]Competition, error) {
	return list[Competition](ctx, c, "competitions")
}
func (c *Client) TeamRunningInstances(ctx context.Context, cid string) (int, error) {
	var me struct {
		CompetitionTeams []struct {
			CompetitionID    string `json:"competition_id"`
			RunningInstances int    `json:"running_instances"`
		} `json:"competition_teams"`
	}
	if err := c.request(ctx, http.MethodGet, "me", nil, &me); err != nil {
		return 0, err
	}
	for _, team := range me.CompetitionTeams {
		if team.CompetitionID == cid {
			return team.RunningInstances, nil
		}
	}
	return 0, nil
}
func (c *Client) Challenges(ctx context.Context, cid string) ([]Challenge, error) {
	return list[Challenge](ctx, c, "competitions/"+segment(cid)+"/challenges")
}
func (c *Client) Instances(ctx context.Context, cid string) ([]Instance, error) {
	return list[Instance](ctx, c, "instances?competition_id="+url.QueryEscape(cid))
}
func (c *Client) Challenge(ctx context.Context, cid, id string) (Challenge, error) {
	var v Challenge
	err := c.request(ctx, "GET", "challenges/"+segment(id)+"?competition_id="+url.QueryEscape(cid), nil, &v)
	return v, err
}
func (c *Client) Start(ctx context.Context, cid, id string) (Instance, error) {
	var v Instance
	err := c.request(ctx, "POST", "instances", map[string]string{"challenge_id": id, "competition_id": cid}, &v)
	return v, err
}
func (c *Client) Instance(ctx context.Context, cid, id string) (Instance, error) {
	var v Instance
	err := c.request(ctx, "GET", "instances/"+segment(id)+"?competition_id="+url.QueryEscape(cid), nil, &v)
	return v, err
}
func (c *Client) Stop(ctx context.Context, cid, id string) error {
	return c.request(ctx, "DELETE", "instances/"+segment(id)+"?competition_id="+url.QueryEscape(cid), nil, nil)
}
func (c *Client) Submit(ctx context.Context, id, qid string, answer any) (Verdict, error) {
	var v struct {
		Verdict
		Correct *bool `json:"correct"`
	}
	err := c.request(ctx, "POST", "submissions", map[string]any{"instance_id": id, "question_id": qid, "answer": answer}, &v)
	if err == nil {
		if v.Correct == nil {
			return Verdict{}, fmt.Errorf("submission response missing correct; reconcile before retrying")
		}
		v.Verdict.Correct = *v.Correct
	}
	return v.Verdict, err
}
func (c *Client) Scoreboard(ctx context.Context, cid string) (json.RawMessage, error) {
	var v json.RawMessage
	err := c.request(ctx, "GET", "competitions/"+segment(cid)+"/scoreboard", nil, &v)
	return v, err
}
func (c *Client) Topics(ctx context.Context, cid string) (json.RawMessage, error) {
	var v json.RawMessage
	err := c.request(ctx, "GET", "competitions/"+segment(cid)+"/topics", nil, &v)
	return v, err
}
func (c *Client) Quota(ctx context.Context, cid string) (Quota, error) {
	var v Quota
	err := c.request(ctx, "GET", "competitions/"+segment(cid)+"/authoring/quota", nil, &v)
	return v, err
}
func (c *Client) Authored(ctx context.Context, cid string) ([]Authored, error) {
	var v []Authored
	err := c.request(ctx, "GET", "competitions/"+segment(cid)+"/authored-challenges", nil, &v)
	return v, err
}
func (c *Client) Audit(ctx context.Context, cid, id string) (json.RawMessage, error) {
	var v json.RawMessage
	err := c.request(ctx, "GET", "competitions/"+segment(cid)+"/authored-challenges/"+segment(id)+"/audit", nil, &v)
	return v, err
}

// Downloads use Bearer authentication only for the configured platform origin.
// Redirects are rejected so a template/VPN request cannot leak credentials.
func (c *Client) Download(ctx context.Context, address string, max int64, platformOnly bool) ([]byte, error) {
	ref, err := url.Parse(address)
	if err != nil {
		return nil, fmt.Errorf("invalid download URL")
	}
	resolved := c.base.ResolveReference(ref)
	same := strings.EqualFold(resolved.Host, c.base.Host) && resolved.Scheme == c.base.Scheme
	if platformOnly && !same {
		return nil, fmt.Errorf("VPN/template download must use the configured platform origin")
	}
	if resolved.User != nil || (resolved.Scheme != "http" && resolved.Scheme != "https") {
		return nil, fmt.Errorf("invalid download URL")
	}
	return c.raw(ctx, "GET", resolved.String(), nil, "", same, max)
}
func (c *Client) Template(ctx context.Context, cid, category, stack string) ([]byte, error) {
	return c.Download(ctx, c.endpoint("authoring/template?"+url.Values{"competition_id": {cid}, "category": {category}, "stack": {stack}}.Encode()), 32<<20, true)
}
func (c *Client) Upload(ctx context.Context, cid, id, archive string, fields map[string]string) (Authored, error) {
	file, err := os.Open(archive)
	if err != nil {
		return Authored{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() == 0 || info.Size() > 64<<20 {
		return Authored{}, fmt.Errorf("archive must contain 1 byte to 64 MiB")
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", filepath.Base(archive))
	if err != nil {
		return Authored{}, err
	}
	if _, err = io.Copy(part, file); err != nil {
		return Authored{}, err
	}
	for k, v := range fields {
		if err = form.WriteField(k, v); err != nil {
			return Authored{}, err
		}
	}
	if err = form.Close(); err != nil {
		return Authored{}, err
	}
	path := "competitions/" + segment(cid) + "/authored-challenges"
	if id != "" {
		path += "/" + segment(id) + "/revisions"
	}
	data, err := c.raw(ctx, "POST", c.endpoint(path), &body, form.FormDataContentType(), true, 8<<20)
	var result Authored
	if err == nil {
		err = json.Unmarshal(data, &result)
	}
	return result, err
}
