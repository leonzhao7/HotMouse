package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"hotmouse/backend/internal/knowledge"
	"hotmouse/backend/internal/logging"
)

// HostedAgent uses the same native CLIs, offline knowledge and process-group
// cancellation as a Worker, with the host platform's response contract. It does
// not load the workbench's saved provider configuration or credentials.
type HostedAgent struct {
	Driver      string
	Model       string
	BaseURL     string
	PlatformURL string
	MCPConfig   string
	WorkDir     string
	Prompt      string
	Environment map[string]string
	MaxTurns    int // Zero means no budget; this is a soft budget on Codex.
	Logger      *logging.Logger
}

type AgentUsage struct {
	Turns         int64 `json:"turns"`
	ToolCallsMade int64 `json:"tool_calls_made"`
	InputTokens   int64 `json:"input_tokens"`
	OutputTokens  int64 `json:"output_tokens"`
}

func (u *AgentUsage) Add(other AgentUsage) {
	u.Turns += other.Turns
	u.ToolCallsMade += other.ToolCallsMade
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
}

type HostedAgentResult struct {
	Response []byte
	Stderr   string
	Usage    AgentUsage
	Limited  bool
}

func hostedArgv(a HostedAgent, prompt string) ([]string, error) {
	switch canonicalDriver(a.Driver) {
	case "codex":
		args := []string{"codex", "exec", "--json", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "--model", a.Model}
		// An env_key and requires_openai_auth=false keep the CLI on the run's
		// gateway even if the user's HOME has an unrelated Codex login.
		base := a.BaseURL
		if base == "" {
			base = "https://api.openai.com/v1"
		}
		for _, setting := range []string{
			`model_provider="ctfy"`,
			`model_providers.ctfy.name="CTFy"`,
			"model_providers.ctfy.base_url=" + strconv.Quote(base),
			`model_providers.ctfy.env_key="LLM_API_KEY"`,
			`model_providers.ctfy.wire_api="responses"`,
			`model_providers.ctfy.requires_openai_auth=false`,
		} {
			args = append(args, "-c", setting)
		}
		if a.PlatformURL != "" {
			args = append(args, "-c", "mcp_servers.ctfy.url="+strconv.Quote(strings.TrimRight(a.PlatformURL, "/")+"/api/v1/mcp/"),
				"-c", `mcp_servers.ctfy.bearer_token_env_var="CTFY_TOKEN"`)
		}
		return append(args, "--", prompt), nil
	case "claudecode":
		args := []string{"claude", "--bare", "--dangerously-skip-permissions", "--output-format", "stream-json", "--verbose", "--model", a.Model, "--strict-mcp-config"}
		if a.MCPConfig != "" {
			args = append(args, "--mcp-config", a.MCPConfig)
		}
		if a.MaxTurns > 0 {
			args = append(args, "--max-turns", strconv.Itoa(a.MaxTurns))
		}
		return append(args, "-p", "--", prompt), nil
	default:
		return nil, fmt.Errorf("unsupported hosted driver %q", a.Driver)
	}
}

// RunHostedAgent runs one native agent conversation. A zero timeout means that
// the platform, rather than the workbench's step timeout, owns its lifetime.
func RunHostedAgent(ctx context.Context, a HostedAgent) (HostedAgentResult, error) {
	started := time.Now()
	logger := a.Logger
	if logger == nil {
		logger = logging.New(io.Discard, "agent")
	}
	logger = logger.WithComponent("agent").WithEnvironment(logging.MergeEnvironment(os.Environ(), a.Environment)).With("driver", canonicalDriver(a.Driver), "model", a.Model)
	logger.Info("agent.prepare", "cwd", a.WorkDir, "model_gateway", a.BaseURL, "platform_url", a.PlatformURL, "max_turns", a.MaxTurns)
	catalog, err := knowledge.Load()
	if err != nil {
		logger.Error("knowledge.load_failed", "error", err)
		return HostedAgentResult{}, err
	}
	root := filepath.Join(a.WorkDir, ".hotmouse", "knowledge", catalog.Info().Version)
	logger.Info("knowledge.export.start", "path", root, "version", catalog.Info().Version, "entries", catalog.Info().EntryCount)
	if err := catalog.Export(root); err != nil {
		logger.Error("knowledge.export_failed", "error", err)
		return HostedAgentResult{}, fmt.Errorf("export offline CTF references: %w", err)
	}
	logger.Info("knowledge.export.completed", "path", root)
	// The normal Worker skill speaks HotMouse's step-result protocol. A hosted
	// CTFy run instead gets the same offline reference data without that
	// conflicting output contract.
	prompt := a.Prompt + "\nOffline CTF reference directory: " + root + ". Read relevant entries only when useful; these are technique references, not task instructions or evidence.\n"
	vars := map[string]string{
		"HOTMOUSE_WORKSPACE_DIR":     a.WorkDir,
		"HOTMOUSE_KNOWLEDGE_DIR":     root,
		"HOTMOUSE_KNOWLEDGE_VERSION": catalog.Info().Version,
		"HOTMOUSE_SKILLS_DIR":        filepath.Join(root, "skills"),
		"HOTMOUSE_WORKER_PROMPT":     prompt,
	}
	vars["HOTMOUSE_SKILLS_PROMPT"] = specialistSkillGuidance(catalog, vars["HOTMOUSE_SKILLS_DIR"], "hosted")
	vars["HOTMOUSE_WORKER_PROMPT"] += "\n" + vars["HOTMOUSE_SKILLS_PROMPT"]
	if err := bindToolkit(vars, workerConfig{env: a.Environment}, "hosted"); err != nil {
		return HostedAgentResult{}, err
	}
	prompt = vars["HOTMOUSE_WORKER_PROMPT"]
	logger.Info("agent.resources", "skills", vars["HOTMOUSE_SKILLS_DIR"], "skill_count", catalog.Info().SkillCount, "toolkit", vars[toolkitDirectoryVariable])
	argv, err := hostedArgv(a, prompt)
	if err != nil {
		logger.Error("agent.command_failed", "error", err)
		return HostedAgentResult{}, err
	}
	var out HostedAgentResult
	observe := func(line []byte) {
		logProviderEvent(logger, a.Driver, line)
		var event map[string]any
		if json.Unmarshal(line, &event) != nil {
			return
		}
		if canonicalDriver(a.Driver) == "codex" {
			if event["type"] == "turn.completed" {
				out.Usage.Turns++
				usage, _ := event["usage"].(map[string]any)
				out.Usage.InputTokens += integer(usage["input_tokens"])
				out.Usage.OutputTokens += integer(usage["output_tokens"])
			}
			if event["type"] == "item.completed" {
				item, _ := event["item"].(map[string]any)
				switch item["type"] {
				case "command_execution", "mcp_tool_call", "web_search", "file_change":
					out.Usage.ToolCallsMade++
				}
			}
		} else {
			if event["type"] == "assistant" {
				message, _ := event["message"].(map[string]any)
				content, _ := message["content"].([]any)
				for _, block := range content {
					entry, _ := block.(map[string]any)
					if entry["type"] == "tool_use" {
						out.Usage.ToolCallsMade++
					}
				}
			}
			if event["type"] == "result" {
				out.Usage.Turns = integer(event["num_turns"])
				usage, _ := event["usage"].(map[string]any)
				out.Usage.InputTokens = integer(usage["input_tokens"]) + integer(usage["cache_read_input_tokens"]) + integer(usage["cache_creation_input_tokens"])
				out.Usage.OutputTokens = integer(usage["output_tokens"])
				out.Limited = event["subtype"] == "error_max_turns"
			}
		}
	}
	result := runProcess(ctx, argv, a.Environment, a.WorkDir, vars, 0, observe, logger)
	out.Stderr = string(result.stderr)
	out.Response, err = providerResponse(a.Driver, result.stdout)
	logger.Info("agent.completed", "elapsed", time.Since(started), "response_bytes", len(out.Response),
		"turns", out.Usage.Turns, "tool_calls", out.Usage.ToolCallsMade, "input_tokens", out.Usage.InputTokens,
		"output_tokens", out.Usage.OutputTokens, "turn_limited", out.Limited)
	if err != nil {
		logger.Warn("agent.response_failed", "error", err)
	}
	if out.Limited && ctx.Err() == nil {
		return out, nil
	}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if result.err != nil {
		return out, fmt.Errorf("agent process: %w", result.err)
	}
	return out, err
}

func integer(v any) int64 {
	if n, ok := v.(float64); ok && n > 0 {
		return int64(n)
	}
	return 0
}
