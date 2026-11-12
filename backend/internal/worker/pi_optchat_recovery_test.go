package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyPiOptchatRecoverySelectsExecutionAndPreservesFormatRetries(t *testing.T) {
	home, execution, state := t.TempDir(), t.TempDir(), t.TempDir()
	config := &piOptchatConfig{home: home, profile: "test-project"}
	runs := filepath.Join(home, "profiles", config.profile, "runs")
	if err := os.MkdirAll(runs, 0o700); err != nil {
		t.Fatal(err)
	}
	writeRun := func(name, task, cwd string, started int64, connected bool) string {
		t.Helper()
		path := filepath.Join(runs, name+".jsonl")
		if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(map[string]any{"task": task, "cwd": cwd, "started": started, "sessionFile": path, "connected": connected})
		if err := os.WriteFile(filepath.Join(runs, name+".optchat.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	task := "You are the HotMouse bounded step execution worker.\nCurrent Step ID: s002\nCurrent Step Description: inspect saved data"
	writeRun("other-step", strings.ReplaceAll(task, "s002", "s001"), execution, 10, true)
	writeRun("other-worker", task, t.TempDir(), 20, true)
	writeRun("older-execution", task, execution, 1, true)
	source := writeRun("execution", task, execution, 2, true)
	writeRun("summary", "The current Step was interrupted.\nCurrent Step ID: s002\n", execution, 30, true)
	writeRun("format", "Repair only the previous summary's JSON serialization.", execution, 40, true)
	writeRun("delegated-execution", task, execution, 50, false)
	session, err := copyLegacyPiOptchatSession(config, "optchat:test-project", "s002", execution, state)
	if err != nil {
		t.Fatal(err)
	}
	if session == source || filepath.Dir(session) != filepath.Join(state, ".hotmouse", "pi-sessions") {
		t.Fatalf("recovery did not isolate its runtime transcript: %s", session)
	}
	data, err := os.ReadFile(session)
	if err != nil || string(data) != "execution" {
		t.Fatalf("recovery selected an unrelated session: %s %v", data, err)
	}
	if err := os.WriteFile(session, []byte("execution plus previous malformed summary"), 0o600); err != nil {
		t.Fatal(err)
	}
	again, err := copyLegacyPiOptchatSession(config, "optchat:test-project", "s002", execution, state)
	if err != nil || again != session {
		t.Fatalf("format retry changed the session: %s %v", again, err)
	}
	data, _ = os.ReadFile(session)
	if string(data) != "execution plus previous malformed summary" {
		t.Fatal("format retry lost the preceding summary response")
	}
	if _, err := copyLegacyPiOptchatSession(config, "optchat:test-project", "s999", execution, state); err == nil {
		t.Fatal("recovery guessed another step's execution session")
	}
}
