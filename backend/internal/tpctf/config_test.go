package tpctf

import (
	"strings"
	"testing"
)

func TestConfigReasoningEffort(t *testing.T) {
	for _, effort := range []string{"", "low", "medium", "high", "xhigh"} {
		t.Run(effort, func(t *testing.T) {
			for key, value := range map[string]string{
				"CTFY_BASE_URL": "https://platform.example/api/v1", "CTFY_TOKEN": "fixture-token", "CTFY_WORK_DIR": t.TempDir(),
				"CTFY_VPN_MODE": "external", "CTFY_AUTHOR_ENABLED": "0", "CTFY_MODEL": "fixture",
				"HOTMOUSE_LLM_BASE_URL": "https://gateway.example/v1", "HOTMOUSE_LLM_API_KEY": "fixture-key", "HOTMOUSE_LLM_API": "openai-completions",
				"HOTMOUSE_LLM_REASONING_EFFORT": effort,
			} {
				t.Setenv(key, value)
			}
			cfg, err := LoadConfig()
			if effort == "xhigh" {
				if err == nil || !strings.Contains(err.Error(), "HOTMOUSE_LLM_REASONING_EFFORT") {
					t.Fatalf("invalid effort was accepted: %v", err)
				}
				return
			}
			if err != nil || cfg.Agent.Environment["HOTMOUSE_LLM_REASONING_EFFORT"] != effort {
				t.Fatalf("effort did not reach the competition agent: %v", err)
			}
		})
	}
}
