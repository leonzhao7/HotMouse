package worker

import (
	"encoding/json"

	"hotmouse/backend/internal/logging"
)

// Log complete lifecycle and tool events, rather than waiting for the CLI to exit.
func logProviderEvent(logger *logging.Logger, driver string, raw []byte) {
	if logger == nil {
		return
	}
	var event map[string]any
	if json.Unmarshal(raw, &event) != nil {
		return
	}
	kind, _ := event["type"].(string)
	switch kind {
	case "thread.started", "session":
		logger.Info("provider.session", "session", eventSession(driver, raw))
	case "turn.started":
		logger.Info("provider.turn.start")
	case "agent_start", "turn_start":
		logger.Info("provider.turn.start", "event", kind)
	case "message_end":
		message, _ := event["message"].(map[string]any)
		if message["role"] == "assistant" {
			if message["stopReason"] == "error" || message["stopReason"] == "aborted" {
				logger.Error("provider.error", "type", kind, "reason", message["stopReason"], "error", message["errorMessage"])
			} else {
				logger.Info("provider.message.completed", "reason", message["stopReason"], "model", message["model"], "usage", message["usage"])
				logPublicContent(logger, message["content"])
			}
		}
	case "auto_retry_start":
		logger.Warn("provider.retry.start", "attempt", event["attempt"], "max_attempts", event["maxAttempts"],
			"delay_ms", event["delayMs"], "error", event["errorMessage"])
	case "auto_retry_end":
		if event["success"] == true {
			logger.Info("provider.retry.completed", "attempt", event["attempt"])
		} else {
			logger.Error("provider.retry.failed", "attempt", event["attempt"], "error", event["finalError"])
		}
	case "turn.completed":
		logger.Info("provider.turn.completed", "usage", event["usage"])
	case "error", "turn.failed":
		logger.Error("provider.error", "type", kind, "message", event["message"], "error", event["error"])
	case "item.started", "item.completed":
		item, _ := event["item"].(map[string]any)
		if item == nil {
			return
		}
		fields := []any{"event", kind, "id", item["id"], "type", item["type"]}
		for _, key := range []string{"command", "server", "tool", "status", "exit_code"} {
			if value, ok := item[key]; ok {
				fields = append(fields, key, value)
			}
		}
		if kind == "item.completed" {
			if item["type"] == "agent_message" {
				logger.Info("provider.message", "text", item["text"])
			}
			if output, ok := item["aggregated_output"].(string); ok && output != "" {
				fields = append(fields, "output", output)
			}
		}
		logger.Info("provider.item", fields...)
	case "system":
		logger.Info("provider.system", "subtype", event["subtype"], "session", event["session_id"])
	case "assistant":
		message, _ := event["message"].(map[string]any)
		content, _ := message["content"].([]any)
		for _, block := range content {
			entry, _ := block.(map[string]any)
			if entry["type"] == "tool_use" {
				input, _ := entry["input"].(map[string]any)
				logger.Info("provider.tool.start", "id", entry["id"], "tool", entry["name"], "command", input["command"], "input", input)
			}
		}
		logPublicContent(logger, message["content"])
	case "user":
		message, _ := event["message"].(map[string]any)
		content, _ := message["content"].([]any)
		for _, block := range content {
			entry, _ := block.(map[string]any)
			if entry["type"] == "tool_result" {
				fields := []any{"id", entry["tool_use_id"], "is_error", entry["is_error"], "output", entry["content"]}
				if entry["is_error"] == true {
					logger.Warn("provider.tool.completed", fields...)
				} else {
					logger.Info("provider.tool.completed", fields...)
				}
			}
		}
	case "result":
		logger.Info("provider.result", "subtype", event["subtype"], "is_error", event["is_error"],
			"turns", event["num_turns"], "usage", event["usage"], "duration_ms", event["duration_ms"])
	case "tool_execution_start", "tool_execution_end":
		fields := []any{"event", kind, "tool", event["toolName"], "id", event["toolCallId"], "is_error", event["isError"]}
		if kind == "tool_execution_start" {
			fields = append(fields, "input", event["args"])
		} else {
			fields = append(fields, "output", event["result"])
		}
		if event["isError"] == true {
			logger.Warn("provider.tool", fields...)
		} else {
			logger.Info("provider.tool", fields...)
		}
	default:
		logger.Debug("provider.event", "type", kind)
	}
}

// Only public assistant text is progress. Provider thinking/reasoning blocks
// and incremental deltas are deliberately excluded from terminal summaries.
func logPublicContent(logger *logging.Logger, value any) {
	content, _ := value.([]any)
	for _, block := range content {
		entry, _ := block.(map[string]any)
		if entry["type"] == "text" {
			if text, ok := entry["text"].(string); ok && text != "" {
				logger.Info("provider.message", "text", text)
			}
		}
	}
}
