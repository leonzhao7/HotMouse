package ctfy

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type config struct {
	driver, model, key, baseURL, platformURL, token string
	maxTurns                                        int
	environment                                     map[string]string
}

// A present but empty platform value must never fall back to a personal key.
func platformValue(name string, sdkNames ...string) string {
	if value, present := os.LookupEnv(name); present {
		return value
	}
	for _, name := range sdkNames {
		if value := os.Getenv(name); value != "" {
			return value
		}
	}
	return ""
}

func loadConfig(task Task) (config, error) {
	cfg := config{environment: map[string]string{}}
	flavor := os.Getenv("LLM_FLAVOR")
	switch flavor {
	case "openai":
		cfg.driver = "codex"
		cfg.key = platformValue("LLM_API_KEY", "OPENAI_API_KEY")
		cfg.baseURL = platformValue("LLM_BASE_URL", "OPENAI_BASE_URL")
		cfg.model = platformValue("LLM_MODEL", "OPENAI_MODEL")
	case "anthropic":
		cfg.driver = "claudecode"
		cfg.key = platformValue("LLM_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY")
		cfg.baseURL = platformValue("LLM_BASE_URL", "ANTHROPIC_BASE_URL")
		cfg.model = platformValue("LLM_MODEL", "ANTHROPIC_MODEL")
	default:
		return cfg, fmt.Errorf("LLM_FLAVOR must be openai or anthropic")
	}
	if strings.TrimSpace(cfg.key) == "" {
		return cfg, fmt.Errorf("LLM_API_KEY is empty: this run has no model funding")
	}
	if cfg.model == "" {
		return cfg, fmt.Errorf("LLM_MODEL is required")
	}
	if cfg.baseURL == "" {
		return cfg, fmt.Errorf("LLM_BASE_URL is required for a hosted run")
	}
	if err := validateURL(cfg.baseURL); err != nil {
		return cfg, fmt.Errorf("invalid LLM_BASE_URL: %w", err)
	}
	if driver := os.Getenv("HOTMOUSE_CTFY_DRIVER"); driver != "" && driver != "auto" {
		if driver == "claude" {
			driver = "claudecode"
		}
		if driver != cfg.driver {
			return cfg, fmt.Errorf("HOTMOUSE_CTFY_DRIVER %q cannot use the %s funding flavor", driver, flavor)
		}
	}
	cfg.platformURL = strings.TrimRight(platformValue("CTFY_PLATFORM_URL"), "/")
	if cfg.platformURL == "" {
		cfg.platformURL = strings.TrimRight(task.PlatformURL, "/")
	}
	if cfg.platformURL == "" {
		return cfg, fmt.Errorf("CTFY_PLATFORM_URL or task.platform_url is required")
	}
	if err := validateURL(cfg.platformURL); err != nil {
		return cfg, fmt.Errorf("invalid CTFY_PLATFORM_URL: %w", err)
	}
	cfg.token = os.Getenv("CTFY_TOKEN")
	if strings.TrimSpace(cfg.token) == "" {
		return cfg, fmt.Errorf("CTFY_TOKEN is required outside a dry run")
	}
	if task.MaxTurns != nil {
		cfg.maxTurns = *task.MaxTurns
	}
	if budget, present := os.LookupEnv("CTFY_EVAL_MAX_TURNS"); present {
		value, err := strconv.Atoi(budget)
		if err != nil || value < 1 {
			return cfg, fmt.Errorf("CTFY_EVAL_MAX_TURNS must be a positive integer or absent")
		}
		cfg.maxTurns = value
	}
	cfg.environment["LLM_API_KEY"] = cfg.key
	cfg.environment["LLM_MODEL"] = cfg.model
	cfg.environment["CTFY_PLATFORM_URL"] = cfg.platformURL
	cfg.environment["CTFY_TOKEN"] = cfg.token
	// The other flavor must not participate in provider selection. These
	// overrides apply to child processes only; credentials stay in memory.
	for _, name := range []string{"OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_MODEL", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL"} {
		cfg.environment[name] = ""
	}
	if flavor == "openai" {
		cfg.environment["OPENAI_API_KEY"] = cfg.key
		cfg.environment["OPENAI_BASE_URL"] = cfg.baseURL
		cfg.environment["OPENAI_MODEL"] = cfg.model
	} else {
		cfg.environment["ANTHROPIC_API_KEY"] = cfg.key
		cfg.environment["ANTHROPIC_AUTH_TOKEN"] = cfg.key
		cfg.environment["ANTHROPIC_BASE_URL"] = cfg.baseURL
		for _, name := range []string{"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL"} {
			cfg.environment[name] = cfg.model
		}
		cfg.environment["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] = "1"
		cfg.environment["DISABLE_AUTOUPDATER"] = "1"
	}
	return cfg, nil
}

func validateURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("expected an HTTP(S) base URL without credentials, query or fragment")
	}
	return nil
}

func (c config) redact(value string) string {
	for _, secret := range []string{c.key, c.token} {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	return value
}
