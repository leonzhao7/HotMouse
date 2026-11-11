package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func toolkitFixture(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "toolkit with spaces")
	if err := os.MkdirAll(filepath.Join(root, "weapons"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"README.md":         "Local test toolkit",
		"weapons/decode.py": "import base64, sys\nprint(base64.b64decode(sys.argv[1]).decode())\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestToolkitDiscoveryAndProviderOverride(t *testing.T) {
	checkout := t.TempDir()
	local := filepath.Join(checkout, "backend", "pentest_toolkit")
	if err := os.MkdirAll(local, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, start := range []string{checkout, filepath.Join(checkout, "backend"), filepath.Join(checkout, "workspaces", "project", "workers", "one")} {
		got, err := toolkitDirectory("", start)
		if err != nil || got != local {
			t.Fatalf("discovery from %s: got %s, %v", start, got, err)
		}
	}
	configured := toolkitFixture(t)
	t.Setenv(toolkitDirectoryVariable, local)
	vars := map[string]string{"HOTMOUSE_WORKER_PROMPT": "stage contract"}
	if err := bindToolkit(vars, workerConfig{env: map[string]string{toolkitDirectoryVariable: configured}}, "execute"); err != nil {
		t.Fatal(err)
	}
	if vars[toolkitDirectoryVariable] != configured || !strings.Contains(vars["HOTMOUSE_WORKER_PROMPT"], filepath.Join(configured, "weapons")) {
		t.Fatal("provider override or tool paths not bound")
	}
	for _, invalid := range []string{filepath.Join(checkout, "missing"), filepath.Join(configured, "README.md")} {
		if _, err := toolkitDirectory(invalid, checkout); err == nil {
			t.Fatalf("explicit invalid toolkit silently ignored: %s", invalid)
		}
	}
	if got, err := toolkitDirectory("", t.TempDir()); err != nil || got != "" {
		t.Fatalf("standalone binary requires a toolkit: %s, %v", got, err)
	}
}
