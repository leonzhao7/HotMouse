package ctfy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"hotmouse/backend/internal/logging"
	"hotmouse/backend/internal/worker"
)

type agentRunner func(context.Context, worker.HostedAgent) (worker.HostedAgentResult, error)

func Run(ctx context.Context, output, diagnostic func(string), run agentRunner) (code int) {
	started := time.Now()
	logger := logging.NewCallback(diagnostic, "ctfy")
	logger.Runtime()
	diagnostic = func(message string) { logger.Error("ctfy.error", "error", message) }
	defer func() {
		if code == 0 {
			logger.Info("ctfy.exit", "exit_code", code, "elapsed", time.Since(started))
		} else {
			logger.Error("ctfy.exit", "exit_code", code, "elapsed", time.Since(started))
		}
	}()
	workDir := os.Getenv("CTFY_WORK_DIR")
	if workDir == "" {
		var err error
		workDir, err = os.Getwd()
		if err != nil {
			diagnostic(fmt.Sprintf("ctfy agent: determine working directory: %s", err))
			return 1
		}
	}
	workDir, err := filepath.Abs(workDir)
	if err != nil {
		diagnostic(fmt.Sprintf("ctfy agent: resolve working directory: %s", err))
		return 1
	}
	logger.Info("ctfy.workspace", "path", workDir)
	for _, name := range []string{"task.json", "README.md", "mcp.json", "attachments"} {
		path := filepath.Join(workDir, name)
		if info, err := os.Stat(path); err == nil {
			logger.Info("ctfy.file", "path", path, "present", true, "bytes", info.Size(), "mode", info.Mode().String())
		} else {
			logger.Info("ctfy.file", "path", path, "present", false, "error", err)
		}
	}
	// A previous round's answer must never be reused, even if this round fails
	// before reaching the model. The platform preserves the same work directory.
	if err := os.Remove(filepath.Join(workDir, "result.json")); err != nil && !os.IsNotExist(err) {
		diagnostic("ctfy agent: clear previous result: " + err.Error())
		return 1
	}
	task, err := readTask(workDir)
	if err != nil {
		diagnostic("ctfy agent: " + err.Error())
		return 1
	}
	logger.Info("ctfy.task.loaded", "schema_version", task.SchemaVersion, "run_mode", task.RunMode,
		"dry_run", task.DryRun || os.Getenv("CTFY_EVAL_DRY_RUN") == "1", "challenge", task.Challenge.ID,
		"competition", task.CompetitionID)
	if task.DryRun || os.Getenv("CTFY_EVAL_DRY_RUN") == "1" {
		if task.SchemaVersion != SchemaVersion {
			diagnostic(fmt.Sprintf("ctfy agent: unsupported task schema_version %d", task.SchemaVersion))
			return 1
		}
		if err := writeResult(workDir, emptyResult("dry_run"), config{}); err != nil {
			diagnostic("ctfy agent: write dry-run result: " + err.Error())
			return 1
		}
		output("CTFy dry run complete")
		logger.Info("ctfy.dry_run.completed", "result", filepath.Join(workDir, "result.json"))
		return 0
	}
	if err := task.Validate(); err != nil {
		diagnostic("ctfy agent: " + err.Error())
		return 1
	}
	cfg, err := loadConfig(task)
	if err != nil {
		diagnostic("ctfy agent: " + err.Error())
		return 1
	}
	logger = logger.WithEnvironment(logging.MergeEnvironment(nil, cfg.environment))
	logger.Info("ctfy.config", "driver", cfg.driver, "model", cfg.model, "model_gateway", cfg.baseURL,
		"platform_url", cfg.platformURL, "max_turns", cfg.maxTurns)
	briefing, err := os.ReadFile(filepath.Join(workDir, "README.md"))
	if err != nil {
		diagnostic("ctfy agent: read README.md: " + err.Error())
		return 1
	}
	if len(briefing) == 0 || len(briefing) > 4<<20 {
		diagnostic("ctfy agent: README.md must be 1 byte to 4 MiB")
		return 1
	}
	logger.Info("ctfy.briefing.loaded", "bytes", len(briefing))
	maxTurns := cfg.maxTurns
	if task.RunMode == "awd" {
		maxTurns = 0
	}
	prompt := string(briefing) + "\n\nCTFy agent instructions: Work only on this task and its stated scope. You are running in the platform-supplied working directory; inspect task.json, attachments/, and mcp.json as needed. Use the platform's authenticated API or MCP for gated questions, submissions, reports and patches. Never start another instance. After each correct gated answer, re-read the challenge. Use only the model gateway configured for this run. Never put keys or tokens in files. Finish by returning one JSON object with schema_version 3, answers keyed by question ID, optional writeup and stop_reason. Answers in this JSON are submitted by the platform; prose alone is not. You may also write result.json yourself. The harness will validate and save your result.\n"
	if task.RunMode == "awd" {
		prompt += "This AWD agent starts once for the entire match. Keep attacking and defending until the last round has ended; check the match clock and refresh flags each round. Do not exit while rounds remain.\n"
	}
	if task.RunMode == "engagement" {
		prompt += "Save and revise your engagement report through the platform PUT endpoint before ending the run; result.json is not a report submission.\n"
	}
	output(fmt.Sprintf("CTFy v%d %s run started with %s", task.SchemaVersion, task.RunMode, cfg.driver))
	logger.Info("ctfy.agent.start", "driver", cfg.driver, "max_turns", maxTurns, "prompt_bytes", len(prompt))
	response, runErr := run(ctx, worker.HostedAgent{Driver: cfg.driver, Model: cfg.model, BaseURL: cfg.baseURL, PlatformURL: cfg.platformURL, MCPConfig: filepath.Join(workDir, "mcp.json"), WorkDir: workDir, Prompt: prompt, Environment: cfg.environment, MaxTurns: maxTurns, Logger: logger})
	result := emptyResult("")
	if file, err := os.ReadFile(filepath.Join(workDir, "result.json")); err == nil {
		result, err = parseResult(file)
		if err != nil {
			runErr = fmt.Errorf("agent-written result.json: %w", err)
		}
	} else if len(response.Response) > 0 {
		result, err = responseResult(response.Response)
		if err != nil {
			// The agent may already have submitted through CTFy's API. A
			// missing rollup is advice in v3, not a failed run.
			if runErr == nil {
				result = emptyResult("completed_without_rollup")
			} else {
				runErr = err
			}
		}
	} else if runErr == nil {
		result.StopReason = "completed_without_rollup"
	}
	// Model-generated counters are estimates; native CLI events are the source
	// of the usage actually observed in this invocation.
	result.AgentUsage = response.Usage
	if runErr != nil {
		result.StopReason = "agent_error"
		if response.Limited {
			result.StopReason = "turn_limit"
		}
	}
	if err := writeResult(workDir, result, cfg); err != nil {
		diagnostic("ctfy agent: write result.json: " + err.Error())
		if runErr != nil {
			diagnostic("ctfy agent: " + cfg.redact(runErr.Error()))
		}
		return 1
	}
	output(fmt.Sprintf("CTFy result saved: %d answers, %d turns, %d tool calls", len(result.Answers), result.Turns, result.ToolCallsMade))
	logger.Info("ctfy.result.saved", "path", filepath.Join(workDir, "result.json"), "answers", len(result.Answers),
		"turns", result.Turns, "tool_calls", result.ToolCallsMade, "input_tokens", result.InputTokens,
		"output_tokens", result.OutputTokens, "stop_reason", result.StopReason)
	if runErr != nil {
		diagnostic("ctfy agent: " + cfg.redact(runErr.Error()))
		if response.Stderr != "" {
			diagnostic(cfg.redact(response.Stderr))
		}
		return 1
	}
	return 0
}
