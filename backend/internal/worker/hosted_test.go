package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostedCodexGatewayConfiguration(t *testing.T) {
	args, err := hostedArgv(HostedAgent{Driver: "codex", Model: "funded", BaseURL: "https://gateway.example/v1", PlatformURL: "https://ctfy.example"}, "briefing")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, item := range []string{"https://gateway.example/v1", "LLM_API_KEY", "CTFY_TOKEN", "https://ctfy.example/api/v1/mcp/", "briefing"} {
		if !strings.Contains(joined, item) {
			t.Errorf("missing %q in %q", item, joined)
		}
	}
}

func TestHostedAgentReadsNativeSuccessEvents(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
set -eu
test -f "$HOTMOUSE_SKILLS_DIR/web-exploit/SKILL.md"
test -f "$HOTMOUSE_PENTEST_TOOLKIT_DIR/weapons/decode.py"
test -f "$HOTMOUSE_KNOWLEDGE_DIR/entries/kb006.json"
for arg in "$@"; do prompt="$arg"; done
case "$prompt" in *"name: flag-hunting"*"Hosted agent:"*) ;; *) exit 3 ;; esac
case "$prompt" in *"name: hotmouse-ctf"*) exit 4 ;; esac
printf '%s\n' '{"type":"item.completed","item":{"type":"command_execution"}}' '{"type":"item.completed","item":{"type":"agent_message","text":"{\"schema_version\":3,\"answers\":{\"flag\":\"ctfy{ok}\"}}"}}' '{"type":"turn.completed","usage":{"input_tokens":11,"output_tokens":7}}'
`
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	result, err := RunHostedAgent(context.Background(), HostedAgent{Driver: "codex", Model: "model", BaseURL: "https://gateway.example/v1", WorkDir: dir, Prompt: "briefing", Environment: map[string]string{"LLM_API_KEY": "fixture", toolkitDirectoryVariable: toolkitFixture(t)}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result.Response), `"ctfy{ok}"`) || result.Usage.Turns != 1 || result.Usage.ToolCallsMade != 1 || result.Usage.InputTokens != 11 || result.Usage.OutputTokens != 7 {
		t.Fatalf("response=%q usage=%+v", result.Response, result.Usage)
	}
}

func TestHostedPiUsesGatewayEnvironmentReferenceAndNativeTools(t *testing.T) {
	dir := t.TempDir()
	args, err := hostedArgv(HostedAgent{Driver: "pi", Model: "zhipu-fixture", BaseURL: "https://session.example", API: "openai", WorkDir: dir, Environment: map[string]string{"LLM_API_KEY": "pk_must_not_persist"}}, "solve this")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".hotmouse", "pi-agent", "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "pk_must_not_persist") || !strings.Contains(string(data), "openai-completions") || !strings.Contains(string(data), "https://session.example") {
		t.Fatalf("unsafe gateway config: %s", data)
	}
	joined := strings.Join(args, " ")
	for _, part := range []string{"--no-context-files", "--no-extensions", "read,write,edit,bash,grep,find,ls", "--provider hotmouse", "--model zhipu-fixture"} {
		if !strings.Contains(joined, part) {
			t.Errorf("missing %s", part)
		}
	}
}
