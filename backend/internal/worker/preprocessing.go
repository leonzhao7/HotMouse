package worker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"hotmouse/backend/internal/core"
)

var homepageURLPattern = regexp.MustCompile("(?i)https?://[^\\s<>\"'`]+")

func homepageEndpointKey(endpoint core.TargetEndpoint) string {
	host := strings.ToLower(strings.TrimSuffix(endpoint.Host, "."))
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	return net.JoinHostPort(host, strconv.Itoa(endpoint.Port))
}

// Preserve the supplied entry path/query. Endpoint selection comes from the
// existing startup analysis, so platform and attachment URLs are excluded.
func homepageURLs(texts []string, targets []core.TargetEndpoint) []string {
	allowed := map[string]bool{}
	for _, target := range targets {
		allowed[homepageEndpointKey(target)] = true
	}
	seen := map[string]bool{}
	var urls []string
	for _, text := range texts {
		for _, raw := range homepageURLPattern.FindAllString(text, -1) {
			raw = strings.TrimRight(raw, ")]},.;")
			// Keep the closing bracket of a bare IPv6 authority.
			if strings.Count(raw, "[") > strings.Count(raw, "]") {
				raw += "]"
			}
			u, err := url.Parse(raw)
			if err != nil || u.Hostname() == "" {
				continue
			}
			endpoints := core.TargetProbeURLCandidates(raw)
			if len(endpoints) != 1 || !allowed[homepageEndpointKey(endpoints[0])] {
				continue
			}
			u.Scheme = strings.ToLower(u.Scheme)
			u.Fragment = ""
			if u.Path == "" {
				u.Path = "/"
			}
			canonical := u.String()
			if !seen[canonical] {
				seen[canonical] = true
				urls = append(urls, canonical)
			}
		}
	}
	return urls
}

type homepageResponse struct {
	URL              string      `json:"url"`
	Status           string      `json:"status"`
	StatusCode       int         `json:"status_code"`
	Protocol         string      `json:"protocol"`
	Headers          http.Header `json:"headers"`
	Trailers         http.Header `json:"trailers,omitempty"`
	TransferEncoding []string    `json:"transfer_encoding,omitempty"`
	Body             string      `json:"body"`
	BodyEncoding     string      `json:"body_encoding"`
	BodyBytes        int64       `json:"body_bytes"`
	Complete         bool        `json:"complete"`
	RawResponsePath  string      `json:"raw_response_path,omitempty"`
}

func (r *Runner) ensureStartup(ctx context.Context, project string) (bool, error) {
	if ready, err := r.ensureTargetProbe(ctx, project); err != nil || !ready {
		return false, err
	}
	state, err := r.DB.TargetProbe(ctx, project)
	if err != nil {
		return false, err
	}
	if state.Preprocessing.Status != "pending" {
		return true, nil
	}
	return r.preprocessWeb(ctx, project, state)
}

func (r *Runner) preprocessWeb(parent context.Context, project string, state core.TargetProbeState) (bool, error) {
	invocation, err := newSessionID()
	if err != nil {
		return false, err
	}
	identity := r.dispatcherIdentity(project)
	lease := identity + "/preprocessing/" + invocation
	if _, err := r.DB.ClaimDecide(parent, project, core.ClaimDecide{Worker: lease, Trigger: "startup web preprocessing"}); err != nil {
		return false, err
	}
	defer r.releaseDecide(project, lease)
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stop := r.keepDecideAlive(ctx, cancel, project, lease)
	defer stop()
	state, err = r.DB.TargetProbe(ctx, project)
	if err != nil {
		return false, err
	}
	if state.Preprocessing.Status != "pending" {
		return true, nil
	}
	texts, err := r.DB.InitialTargetSources(ctx, project)
	if err != nil {
		return false, err
	}
	cfg := r.dispatcherConfig(ctx)
	if !cfg.enabled {
		return false, fmt.Errorf("worker disabled")
	}
	if cfg.contextRoot != "" {
		var origin, goal string
		if err := r.DB.SQL.QueryRowContext(ctx, `SELECT description FROM facts WHERE project_id=? AND id='origin'`, project).Scan(&origin); err != nil {
			return false, err
		}
		if err := r.DB.SQL.QueryRowContext(ctx, `SELECT description FROM goals WHERE project_id=? AND parent_goal_id IS NULL ORDER BY created_at LIMIT 1`, project).Scan(&goal); err != nil {
			return false, err
		}
		scope, err := loadProjectContext(cfg.contextRoot, project, origin, goal)
		if err != nil {
			return false, err
		}
		texts = append(texts, scope)
	}
	var urls []string
	if state.Analysis != nil && state.Analysis.Required {
		urls = homepageURLs(texts, state.Analysis.Targets)
	}
	if len(urls) == 0 {
		err := r.DB.SavePreprocessing(ctx, project, state.RunID, core.PreprocessingState{Status: "skipped", Reason: "No HTTP(S) challenge target in the analyzed initial scope"}, "", "")
		return err == nil, err
	}
	workspace, err := r.ensureWorkspace(project, cfg, identity)
	if err != nil {
		return false, err
	}
	dir := filepath.Join(workspace, ".hotmouse", "preprocessing", state.RunID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	history := r.historyStart(project, "", "preprocess", time.Now().UTC().Format(time.RFC3339Nano), identity)
	cfg.identity = identity
	r.setTaskLogger(history, r.processLogger(cfg, project, "", "preprocess"))
	r.logger.Printf("web preprocessing project=%s urls=%v", project, urls)
	result := core.PreprocessingState{Status: "ready", Pages: make([]core.PreprocessingPage, len(urls))}
	responses := make([][]homepageResponse, len(urls))
	var wg sync.WaitGroup
	errors := make([]error, len(urls))
	for i, target := range urls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pageDir := filepath.Join(dir, fmt.Sprintf("target-%02d", i+1))
			if err := os.MkdirAll(pageDir, 0o700); err != nil {
				errors[i] = err
				return
			}
			pageCtx, pageCancel := context.WithTimeout(ctx, 30*time.Second)
			defer pageCancel()
			chain, fetchErr := fetchHomepage(pageCtx, target, pageDir)
			responses[i] = chain
			page := core.PreprocessingPage{URL: target, ResponsePath: filepath.Join(pageDir, "response.json")}
			for _, response := range chain {
				page.BodyBytes += response.BodyBytes
				page.StatusCode = response.StatusCode
			}
			if fetchErr != nil {
				page.Error = fetchErr.Error()
			}
			payload, err := json.MarshalIndent(map[string]any{"url": target, "responses": chain, "error": page.Error}, "", "  ")
			if err == nil {
				err = os.WriteFile(page.ResponsePath, payload, 0o600)
			}
			errors[i] = err
			result.Pages[i] = page
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		r.historyFinish(history, "interrupted", ctx.Err().Error())
		return false, ctx.Err()
	}
	for _, err := range errors {
		if err != nil {
			r.historyFinish(history, "failed", err.Error())
			return false, err
		}
	}
	detail, err := json.Marshal(map[string]any{"pages": result.Pages, "responses": responses})
	if err != nil {
		r.historyFinish(history, "failed", err.Error())
		return false, err
	}
	var summaries []string
	for _, page := range result.Pages {
		summary := fmt.Sprintf("GET %s: HTTP %d, %d body bytes; response evidence: %s", page.URL, page.StatusCode, page.BodyBytes, page.ResponsePath)
		if page.Error != "" {
			summary += "; fetch error: " + page.Error
		}
		summaries = append(summaries, summary)
		logger := r.processLogger(cfg, project, "", "preprocess")
		if page.Error != "" {
			logger.Warn("web.preprocessing.fetch_failed", "url", page.URL, "error", page.Error, "response_path", page.ResponsePath)
		} else {
			logger.Info("web.preprocessing.completed", "url", page.URL, "http_status", page.StatusCode, "body_bytes", page.BodyBytes, "response_path", page.ResponsePath)
		}
	}
	err = r.DB.SavePreprocessing(ctx, project, state.RunID, result, strings.Join(summaries, "\n"), string(detail))
	if err != nil {
		r.historyFinish(history, "failed", err.Error())
		return false, err
	}
	r.historyFinish(history, "completed", strings.Join(summaries, "\n"))
	return true, nil
}

func fetchHomepage(ctx context.Context, target, directory string) ([]homepageResponse, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableCompression = true
	defer transport.CloseIdleConnections()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Transport: transport, Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	initial, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	current := initial
	var responses []homepageResponse
	for hop := 0; hop < 10; hop++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, current.String(), nil)
		if err != nil {
			return responses, err
		}
		request.Header.Set("User-Agent", "HotMouse/1.0")
		request.Header.Set("Accept-Encoding", "identity")
		response, err := client.Do(request)
		if err != nil {
			return responses, err
		}
		body, readErr := io.ReadAll(response.Body) // No size truncation; the request deadline bounds the read.
		_ = response.Body.Close()
		snapshot := homepageResponse{URL: current.String(), Status: response.Status, StatusCode: response.StatusCode, Protocol: response.Proto, Headers: response.Header.Clone(), Trailers: response.Trailer.Clone(), TransferEncoding: response.TransferEncoding, BodyBytes: int64(len(body)), Complete: readErr == nil, BodyEncoding: "utf-8"}
		if utf8.Valid(body) {
			snapshot.Body = string(body)
		} else {
			snapshot.BodyEncoding = "base64"
			snapshot.Body = base64.StdEncoding.EncodeToString(body)
		}
		response.Body = io.NopCloser(bytes.NewReader(body))
		raw, dumpErr := httputil.DumpResponse(response, true)
		snapshot.RawResponsePath = filepath.Join(directory, fmt.Sprintf("response-%02d.http", hop+1))
		if dumpErr == nil {
			dumpErr = os.WriteFile(snapshot.RawResponsePath, raw, 0o600)
		}
		if dumpErr != nil {
			snapshot.RawResponsePath = ""
		}
		responses = append(responses, snapshot)
		if readErr != nil {
			return responses, readErr
		}
		if dumpErr != nil {
			return responses, dumpErr
		}
		switch response.StatusCode {
		case 301, 302, 303, 307, 308:
			if response.Header.Get("Location") == "" {
				return responses, nil
			}
			next, err := response.Location()
			if err != nil {
				return responses, err
			}
			if (next.Scheme != "http" && next.Scheme != "https") || !strings.EqualFold(next.Hostname(), initial.Hostname()) {
				return responses, fmt.Errorf("redirect outside the initial target host: %s", next)
			}
			current = next
		default:
			return responses, nil // HTTP error statuses are still complete evidence.
		}
	}
	return responses, fmt.Errorf("homepage exceeded 10 redirect responses")
}
