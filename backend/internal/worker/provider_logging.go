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
				logger.Info("provider.tool.start", "id", entry["id"], "tool", entry["name"], "command", input["command"])
			}
		}
	case "user":
		message, _ := event["message"].(map[string]any)
		content, _ := message["content"].([]any)
		for _, block := range content {
			entry, _ := block.(map[string]any)
			if entry["type"] == "tool_result" {
				logger.Info("provider.tool.completed", "id", entry["tool_use_id"], "is_error", entry["is_error"])
			}
		}
	case "result":
		logger.Info("provider.result", "subtype", event["subtype"], "is_error", event["is_error"],
			"turns", event["num_turns"], "usage", event["usage"], "duration_ms", event["duration_ms"])
	case "tool_execution_start", "tool_execution_end":
		logger.Info("provider.tool", "event", kind, "tool", event["toolName"], "id", event["toolCallId"], "is_error", event["isError"])
	default:
		logger.Debug("provider.event", "type", kind)
	}
}
