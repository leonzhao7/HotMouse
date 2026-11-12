package worker

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

//go:embed pi_tool_timeout.mjs
var piTimeoutExtension []byte

func canonicalDriver(driver string) string {
	if driver == "claude" || driver == "claude_code" {
		return "claudecode"
	}
	return driver
}

func builtinProvider(cfg workerConfig) bool {
	if cfg.command != "" {
		return false
	}
	switch canonicalDriver(cfg.driver) {
	case "codex", "claudecode", "pi":
		return true
	}
	return false
}

func newSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func providerIdentity(cfg workerConfig, worker, workspace string) string {
	env := map[string]string{}
	for _, key := range []string{"PI_CLI", "PI_MODEL", "PI_BASE_URL", "PI_PROVIDER_API", "PI_CODING_AGENT_DIR", "CODEX_MODEL", "CODEX_BASE_URL", "ANTHROPIC_MODEL", "ANTHROPIC_BASE_URL", "CODEX_HOME", "CLAUDE_CONFIG_DIR"} {
		value, ok := cfg.env[key]
		if !ok {
			value = os.Getenv(key)
		}
		env[key] = value
	}
	b, _ := json.Marshal([]any{canonicalDriver(cfg.driver), worker, workspace, env})
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:])
}

// providerArgv never interpolates session IDs or prompts into a shell command.
// Decide and summary are constrained to read-only tools by the native CLI.
func providerArgv(cfg workerConfig, phase, session, workspace, prompt string) ([]string, error) {
	driver := canonicalDriver(cfg.driver)
	switch driver {
	case "codex":
		args := []string{"codex", "exec"}
		if model := cfg.env["CODEX_MODEL"]; model != "" {
			args = append(args, "--model", model)
		}
		if phase == "summary" {
			args = append(args, "resume", "--json", "--skip-git-repo-check", "-c", `sandbox_mode="read-only"`, "-c", `approval_policy="never"`, "--", session, prompt)
		} else {
			args = append(args, "--json", "--skip-git-repo-check")
			if phase == "decide" || phase == "target_probe" {
				args = append(args, "--sandbox", "read-only", "-c", `approval_policy="never"`)
			} else {
				args = append(args, "--dangerously-bypass-approvals-and-sandbox")
			}
			args = append(args, "--", prompt)
		}
		return args, nil
	case "claudecode":
		args := []string{"claude", "--dangerously-skip-permissions", "--output-format", "json"}
		if phase == "execute" {
			// Stream tool results before the final assistant report exists.
			args = []string{"claude", "--dangerously-skip-permissions", "--output-format", "stream-json", "--verbose"}
		}
		if model := cfg.env["ANTHROPIC_MODEL"]; model != "" {
			args = append(args, "--model", model)
		}
		if phase == "summary" {
			args = append(args, "--resume", session, "--tools", "")
		} else {
			if session != "" {
				args = append(args, "--session-id", session)
			}
			if phase == "decide" || phase == "target_probe" {
				args = append(args, "--tools", "Read")
			}
		}
		if phase != "execute" {
			args = append(args, "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`)
		}
		return append(args, "-p", "--", prompt), nil
	case "pi":
		binary := cfg.env["PI_CLI"]
		if binary == "" {
			binary = os.Getenv("PI_CLI")
		}
		if binary == "" {
			binary = "pi"
		}
		sessions := filepath.Join(workspace, ".hotmouse", "pi-sessions")
		if err := os.MkdirAll(sessions, 0o700); err != nil {
			return nil, err
		}
		args := []string{binary, "--mode", "json", "--session-dir", sessions, "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files", "--tools"}
		if phase == "execute" {
			args = append(args, "read,write,edit,bash,grep,find,ls")
			extension := filepath.Join(workspace, ".hotmouse", "pi_tool_timeout.mjs")
			if err := os.WriteFile(extension, piTimeoutExtension, 0o600); err != nil {
				return nil, err
			}
			args = append(args, "-e", extension)
		} else {
			args = append(args, "read")
		}
		if session != "" {
			args = append(args, "--session", session)
		}
		if cfg.env["PI_MODEL"] != "" && cfg.env["PI_BASE_URL"] != "" && cfg.env["PI_API_KEY"] != "" && cfg.env["PI_PROVIDER_API"] != "" {
			agentDir := filepath.Join(workspace, ".hotmouse", "pi-agent")
			if err := os.MkdirAll(agentDir, 0o700); err != nil {
				return nil, err
			}
			model := map[string]any{"id": cfg.env["PI_MODEL"], "name": cfg.env["PI_MODEL"]}
			if value := cfg.env["PI_MODEL_CONTEXT_WINDOW"]; value != "" {
				window, err := strconv.Atoi(value)
				if err != nil || window < 1 {
					return nil, fmt.Errorf("PI_MODEL_CONTEXT_WINDOW must be positive")
				}
				model["contextWindow"] = window
			}
			api := cfg.env["PI_PROVIDER_API"]
			if api == "openai" || api == "openai-chat-completions" {
				api = "openai-completions"
			}
			data, _ := json.Marshal(map[string]any{"providers": map[string]any{"hotmouse": map[string]any{"baseUrl": cfg.env["PI_BASE_URL"], "api": api, "apiKey": cfg.env["PI_API_KEY"], "models": []any{model}}}})
			modelsPath := filepath.Join(agentDir, "models.json")
			if err := os.WriteFile(modelsPath, data, 0o600); err != nil {
				return nil, err
			}
			if err := os.Chmod(modelsPath, 0o600); err != nil {
				return nil, err
			}
			args = append(args, "--provider", "hotmouse", "--model", cfg.env["PI_MODEL"])
		} else if model := cfg.env["PI_MODEL"]; model != "" {
			args = append(args, "--model", model)
		}
		return append(args, "-p", prompt), nil
	}
	return nil, fmt.Errorf("unsupported provider driver")
}

var codexSessionPattern = regexp.MustCompile(`(?i)session id:\s*([a-f0-9-]+)`)

func eventSession(driver string, raw []byte) string {
	var event struct {
		Type, ID  string
		ThreadID  string `json:"thread_id"`
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(raw, &event) != nil {
		return ""
	}
	switch canonicalDriver(driver) {
	case "codex":
		if event.Type == "thread.started" {
			return event.ThreadID
		}
	case "claudecode":
		return event.SessionID
	case "pi":
		if event.Type == "session" {
			return event.ID
		}
	}
	return ""
}

func providerResponse(driver string, stdout []byte) ([]byte, error) {
	var answer string
	var message map[string]any
	for _, line := range strings.Split(string(stdout), "\n") {
		var event map[string]any
		if json.Unmarshal([]byte(line), &event) != nil {
			continue
		}
		switch canonicalDriver(driver) {
		case "codex":
			if event["type"] == "item.completed" {
				if item, ok := event["item"].(map[string]any); ok && item["type"] == "agent_message" {
					answer, _ = item["text"].(string)
				}
			}
		case "claudecode":
			if event["type"] == "result" {
				answer, _ = event["result"].(string)
				if event["is_error"] == true {
					return []byte(answer), fmt.Errorf("Claude returned a provider error")
				}
			}
		case "pi":
			if candidate, ok := event["message"].(map[string]any); ok && candidate["role"] == "assistant" {
				message = candidate
			}
			if event["type"] == "agent_end" {
				if messages, ok := event["messages"].([]any); ok {
					for _, entry := range messages {
						if candidate, ok := entry.(map[string]any); ok && candidate["role"] == "assistant" {
							message = candidate
						}
					}
				}
			}
		}
	}
	if canonicalDriver(driver) == "pi" && message != nil {
		if content, ok := message["content"].([]any); ok {
			for _, block := range content {
				if entry, ok := block.(map[string]any); ok && entry["type"] == "text" {
					if part, ok := entry["text"].(string); ok {
						answer += part
					}
				}
			}
		}
		stop, _ := message["stopReason"].(string)
		if stop == "length" {
			return []byte(answer), fmt.Errorf("provider output_limit")
		}
		if detail, ok := message["errorMessage"].(string); ok && detail != "" {
			return []byte(answer), fmt.Errorf("provider response is incomplete: %s", detail)
		}
		if stop == "error" || stop == "toolUse" || stop == "aborted" || (message["errorMessage"] != nil && message["errorMessage"] != "") {
			return []byte(answer), fmt.Errorf("provider response is incomplete")
		}
	}
	if strings.TrimSpace(answer) == "" {
		return nil, fmt.Errorf("provider returned no completed assistant response")
	}
	return []byte(answer), nil
}

func validateProviderExecute(raw []byte) error {
	// Native drivers must return structured results; the plain text compatibility
	// path is reserved for operator-provided commands and the deterministic mock.
	var object map[string]json.RawMessage
	if err := decodeObject(raw, &object); err != nil {
		return err
	}
	var accepted bool
	if err := json.Unmarshal(object["accepted"], &accepted); err != nil {
		return fmt.Errorf("provider response requires boolean accepted")
	}
	for key := range object {
		if key != "accepted" && key != "data" && key != "reason" {
			return fmt.Errorf("unexpected provider envelope field")
		}
	}
	_, err := parseExecute(raw)
	return err
}

func (r *Runner) runProvider(ctx context.Context, cfg workerConfig, phase, session, workspace string, vars map[string]string, timeout time.Duration, onSession func(string) error) ([]byte, string, processResult, error) {
	stateWorkspace := workspace
	if writable := vars["HOTMOUSE_WORKSPACE_DIR"]; phase == "summary" && writable != "" && writable != workspace {
		stateWorkspace = writable
		if canonicalDriver(cfg.driver) == "pi" {
			var err error
			session, err = copyLegacyPiSessions(workspace, writable, session)
			if err != nil {
				return nil, session, processResult{}, err
			}
		}
	}
	optchat, err := r.preparePiOptchat(ctx, cfg, vars)
	if err != nil {
		return nil, session, processResult{}, err
	}
	if phase == "summary" && canonicalDriver(cfg.driver) == "pi" && strings.HasPrefix(session, "optchat:") {
		session, err = copyLegacyPiOptchatSession(optchat, session, vars["HOTMOUSE_STEP_ID"], workspace, stateWorkspace)
		if err != nil {
			return nil, session, processResult{}, err
		}
		if onSession != nil {
			if err := onSession(session); err != nil {
				return nil, session, processResult{}, fmt.Errorf("save provider session: %w", err)
			}
		}
	}
	argv, err := providerArgv(cfg, phase, session, stateWorkspace, vars["HOTMOUSE_WORKER_PROMPT"])
	if err != nil {
		return nil, session, processResult{}, err
	}
	if optchat != nil {
		argv = piOptchatArgs(argv, optchat)
	}
	logger := r.processLogger(cfg, vars["HOTMOUSE_PROJECT_ID"], vars["HOTMOUSE_STEP_ID"], vars["HOTMOUSE_TASK_TYPE"]+"/"+phase)
	if canonicalDriver(cfg.driver) == "pi" && cfg.env["PI_MODEL"] != "" && cfg.env["PI_BASE_URL"] != "" && cfg.env["PI_API_KEY"] != "" && cfg.env["PI_PROVIDER_API"] != "" {
		vars["PI_CODING_AGENT_DIR"] = filepath.Join(stateWorkspace, ".hotmouse", "pi-agent")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var checkpointErr error
	seen := session
	var observations *outputCapture
	if phase == "execute" {
		observations, err = r.newOutputCapture(ctx, vars["HOTMOUSE_PROJECT_ID"], vars["HOTMOUSE_STEP_ID"], cfg.identity, stateWorkspace, canonicalDriver(cfg.driver))
		if err != nil {
			return nil, session, processResult{}, err
		}
		defer observations.close()
	}
	observe := func(line []byte) {
		logProviderEvent(logger, cfg.driver, line)
		if observations != nil {
			observations.providerEvent(line)
		}
		if id := eventSession(cfg.driver, line); id != "" && seen == "" {
			seen = id
			if onSession != nil {
				checkpointErr = onSession(id)
				if checkpointErr != nil {
					cancel()
				}
			}
		}
	}
	var onOutput func(string, []byte)
	if observations != nil {
		onOutput = observations.stream
	}
	result := runProcessObserved(runCtx, argv, cfg.env, workspace, vars, timeout, observe, onOutput, logger)
	response, responseErr := providerResponse(cfg.driver, result.stdout)
	if responseErr != nil && logger != nil {
		logger.Warn("provider.response_failed", "error", responseErr, "response_bytes", len(response))
	}
	if seen == "" && canonicalDriver(cfg.driver) == "codex" {
		if match := codexSessionPattern.FindSubmatch(result.stderr); len(match) > 1 {
			seen = string(match[1])
		}
	}
	if checkpointErr != nil {
		return response, seen, result, fmt.Errorf("save provider session: %w", checkpointErr)
	}
	if result.timedOut {
		return response, seen, result, fmt.Errorf("worker provider %s timed out after %s: %w", phase, timeout, context.DeadlineExceeded)
	}
	if result.err != nil {
		return response, seen, result, fmt.Errorf("worker provider process: %w", result.err)
	}
	return response, seen, result, responseErr
}
