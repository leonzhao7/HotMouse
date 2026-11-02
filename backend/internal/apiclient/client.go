package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Error struct {
	Result     any    `json:"-" yaml:"-"`
	ExitCode   int    `json:"-" yaml:"-"`
	Code       string `json:"code" yaml:"code"`
	Message    string `json:"message" yaml:"message"`
	HTTPStatus *int   `json:"http_status" yaml:"http_status"`
	Retryable  bool   `json:"retryable" yaml:"retryable"`
}

func (e *Error) Error() string { return e.Message }
func Failure(code int, message string) *Error {
	names := map[int]string{1: "execution_failed", 2: "invalid_argument", 3: "unauthorized", 4: "not_found", 5: "conflict", 6: "unavailable", 7: "partial_failure", 130: "interrupted", 143: "interrupted"}
	return &Error{ExitCode: code, Code: names[code], Message: message}
}

type Client struct {
	Server     string
	Token      string
	Cookie     *http.Cookie
	HTTP       *http.Client
	SaveCookie func(*http.Cookie) error
	Log        io.Writer
}

func New(server string, timeout time.Duration) *Client {
	return &Client{Server: server, HTTP: &http.Client{Timeout: timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (c *Client) Do(ctx context.Context, method, path string, body any, sessionOnly bool) (any, error) {
	data, err := c.Request(ctx, method, path, body, sessionOnly)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]any{"ok": true}, nil
	}
	var value any
	if err = json.Unmarshal(data, &value); err != nil {
		return nil, Failure(1, "service returned invalid JSON")
	}
	return value, nil
}
func (c *Client) Request(ctx context.Context, method, path string, body any, sessionOnly bool) ([]byte, error) {
	var in io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return nil, Failure(2, "invalid request body")
		}
		in = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, c.Server+path, in)
	if e != nil {
		return nil, Failure(2, "invalid request URL")
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if !sessionOnly && c.Token != "" {
		req.Header.Set("X-HotMouse-Internal-Token", c.Token)
	} else if cookie := c.Cookie; cookie != nil {
		valid := cookie.Expires.IsZero() || time.Now().Before(cookie.Expires)
		cookiePath := cookie.Path
		if cookiePath == "" {
			cookiePath = "/"
		}
		matches := req.URL.Path == cookiePath || strings.HasPrefix(req.URL.Path, cookiePath) && (strings.HasSuffix(cookiePath, "/") || len(req.URL.Path) > len(cookiePath) && req.URL.Path[len(cookiePath)] == '/')
		if valid && cookie.MaxAge >= 0 && cookieDomain(cookie, req.URL.Hostname()) && (!cookie.Secure || req.URL.Scheme == "https") && matches {
			req.AddCookie(cookie)
		}
	}
	if c.Log != nil {
		fmt.Fprintf(c.Log, "%s %s\n", method, req.URL.EscapedPath())
	}
	resp, e := c.HTTP.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		failure := Failure(6, "request failed (connection or timeout); query the result before retrying a write")
		failure.Retryable = method == http.MethodGet
		return nil, failure
	}
	defer resp.Body.Close()
	var chosen *http.Cookie
	for _, cookie := range resp.Cookies() {
		if !cookieDomain(cookie, req.URL.Hostname()) {
			continue
		}
		if cookie.Name == "session_token" {
			chosen = cookie
			break
		}
		if cookie.Name == "hotmouse_session" {
			chosen = cookie
		}
	}
	if chosen != nil {
		if chosen.MaxAge > 0 {
			chosen.Expires = time.Now().Add(time.Duration(chosen.MaxAge) * time.Second)
		}
		if chosen.Path == "" {
			chosen.Path = "/"
			if index := strings.LastIndex(req.URL.Path, "/"); index > 0 {
				chosen.Path = req.URL.Path[:index]
			}
		}
		if chosen.MaxAge < 0 || chosen.Value == "" {
			chosen = nil
		}
		c.Cookie = chosen
		if c.SaveCookie != nil {
			if e = c.SaveCookie(chosen); e != nil {
				return nil, Failure(1, "request completed, but saving the session cookie failed: "+e.Error())
			}
		}
	}
	data, e := io.ReadAll(resp.Body)
	if e != nil {
		failure := Failure(6, "response read failed; the operation may have completed")
		failure.Retryable = method == http.MethodGet
		return nil, failure
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		exit := 1
		switch resp.StatusCode {
		case 400, 422:
			exit = 2
		case 401, 403:
			exit = 3
		case 404:
			exit = 4
		case 409:
			exit = 5
		case 429:
			exit = 6
		}
		message := http.StatusText(resp.StatusCode)
		var payload struct {
			Detail string `json:"detail"`
		}
		if json.Unmarshal(data, &payload) == nil && payload.Detail != "" {
			message = payload.Detail
		}
		failure := Failure(exit, message)
		var result any
		if json.Unmarshal(data, &result) == nil {
			failure.Result = result
		}
		status := resp.StatusCode
		failure.HTTPStatus = &status
		failure.Retryable = method == http.MethodGet && status == 429
		return nil, failure
	}
	return data, nil
}
func Path(parts ...string) string {
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = url.PathEscape(p)
	}
	return "/" + strings.Join(out, "/")
}

func cookieDomain(cookie *http.Cookie, host string) bool {
	if cookie.Domain == "" {
		return true
	}
	domain := strings.TrimPrefix(strings.ToLower(cookie.Domain), ".")
	host = strings.ToLower(host)
	return host == domain || strings.HasSuffix(host, "."+domain)
}
