package tpctf

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"hotmouse/backend/internal/core"
	"hotmouse/backend/internal/worker"
)

type Config struct {
	BaseURL, Token, AuthorToken, CompetitionID, WorkDir         string
	Agent                                                       worker.HostedAgent
	PollInterval, RetryInterval, ChallengeTimeout, StartTimeout time.Duration
	VPNMode                                                     string
	Author                                                      bool
	AuthorCategory, AuthorStack, License, Terms                 string
}

func first(names ...string) string {
	for _, name := range names {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	return ""
}
func LoadConfig() (Config, error) {
	effort := os.Getenv("HOTMOUSE_LLM_REASONING_EFFORT")
	if err := core.ValidateLLMReasoningEffort(effort); err != nil {
		return Config{}, err
	}
	c := Config{BaseURL: first("CTFY_BASE_URL"), Token: os.Getenv("CTFY_TOKEN"), AuthorToken: os.Getenv("CTFY_AUTHOR_TOKEN"), CompetitionID: os.Getenv("CTFY_COMPETITION_ID"), WorkDir: os.Getenv("CTFY_WORK_DIR"), VPNMode: first("CTFY_VPN_MODE"), Author: os.Getenv("CTFY_AUTHOR_ENABLED") == "1", AuthorCategory: "web", AuthorStack: "python-flask", License: "platform-only", Terms: os.Getenv("CTFY_TERMS_VERSION")}
	if c.BaseURL == "" {
		c.BaseURL = "https://ctfy.tsinghua.edu.cn/api/v1"
	}
	if c.WorkDir == "" {
		c.WorkDir = "/data/ctfy"
	}
	var err error
	c.WorkDir, err = filepath.Abs(c.WorkDir)
	if err != nil {
		return c, err
	}
	if c.VPNMode == "" {
		c.VPNMode = "auto"
	}
	if c.VPNMode != "auto" && c.VPNMode != "external" {
		return c, fmt.Errorf("CTFY_VPN_MODE must be auto or external")
	}
	for _, option := range []struct {
		name  string
		dest  *time.Duration
		value int
	}{{"CTFY_POLL_SECONDS", &c.PollInterval, 15}, {"CTFY_RETRY_SECONDS", &c.RetryInterval, 120}, {"CTFY_CHALLENGE_TIMEOUT_SECONDS", &c.ChallengeTimeout, 1200}, {"CTFY_START_TIMEOUT_SECONDS", &c.StartTimeout, 600}} {
		seconds := option.value
		if value := os.Getenv(option.name); value != "" {
			seconds, err = strconv.Atoi(value)
			if err != nil || seconds < 1 {
				return c, fmt.Errorf("%s must be a positive integer", option.name)
			}
		}
		*option.dest = time.Duration(seconds) * time.Second
	}
	api := first("HOTMOUSE_LLM_API", "LLM_API", "LLM_FLAVOR")
	if api == "" {
		api = "openai"
	}
	if api == "anthropic" {
		api = "anthropic-messages"
	}
	switch api {
	case "openai", "openai-completions", "openai-responses", "anthropic-messages":
	default:
		return c, fmt.Errorf("unsupported model API %q", api)
	}
	// The session's complete gateway URL is passed verbatim, without adding /v1.
	base := first("HOTMOUSE_LLM_BASE_URL", "LLM_BASE_URL", "OPENAI_BASE_URL")
	key := first("HOTMOUSE_LLM_API_KEY", "LLM_API_KEY", "OPENAI_API_KEY")
	model := first("CTFY_MODEL", "HOTMOUSE_LLM_MODEL", "LLM_MODEL", "OPENAI_MODEL")
	if api == "anthropic-messages" {
		base = first("HOTMOUSE_LLM_BASE_URL", "LLM_BASE_URL", "ANTHROPIC_BASE_URL")
		key = first("HOTMOUSE_LLM_API_KEY", "LLM_API_KEY", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN")
		model = first("CTFY_MODEL", "HOTMOUSE_LLM_MODEL", "LLM_MODEL", "ANTHROPIC_MODEL")
	}
	if key == "" || base == "" || model == "" {
		return c, fmt.Errorf("configure the model gateway BASE_URL, API_KEY and CTFY_MODEL (see docker/CTFY.md)")
	}
	if _, err := NewClient(base, key); err != nil {
		return c, fmt.Errorf("invalid model gateway URL or credential")
	}
	c.Agent = worker.HostedAgent{Driver: "pi", Model: model, BaseURL: base, API: api, Environment: map[string]string{"LLM_API_KEY": key, "PI_MODEL_CONTEXT_WINDOW": os.Getenv("PI_MODEL_CONTEXT_WINDOW"), "HOTMOUSE_LLM_REASONING_EFFORT": effort, "CTFY_TOKEN": "", "CTFY_AUTHOR_TOKEN": "", "CTFY_MODE": "competition"}}
	// Only the harness submits answers and uploads archives. Model subprocesses
	// need the gateway credential, not participant/authoring tokens.
	if c.Author {
		if c.AuthorToken == "" {
			return c, fmt.Errorf("CTFY_AUTHOR_ENABLED=1 requires CTFY_AUTHOR_TOKEN")
		}
		if c.Terms == "" {
			return c, fmt.Errorf("CTFY_AUTHOR_ENABLED=1 requires CTFY_TERMS_VERSION from the event contribution agreement")
		}
		if v := os.Getenv("CTFY_AUTHOR_CATEGORY"); v != "" {
			c.AuthorCategory = v
		}
		if v := os.Getenv("CTFY_AUTHOR_STACK"); v != "" {
			c.AuthorStack = v
		}
		if c.AuthorCategory != "web" && c.AuthorCategory != "pwn" {
			return c, fmt.Errorf("TPCTF accepts only web and pwn authoring")
		}
		if v := os.Getenv("CTFY_AUTHOR_LICENSE"); v != "" {
			c.License = v
		}
	}
	if _, err := NewClient(c.BaseURL, c.Token); err != nil {
		return c, err
	}
	return c, nil
}
func (c Config) redact(s string) string {
	for _, secret := range []string{c.Token, c.AuthorToken, c.Agent.Environment["LLM_API_KEY"]} {
		// Real platform credentials are long opaque values. Avoid turning a
		// deliberately short local fixture into a one-character replacement
		// that makes diagnostics unreadable.
		if len(secret) >= 8 {
			s = strings.ReplaceAll(s, secret, "[redacted]")
		}
	}
	return s
}
