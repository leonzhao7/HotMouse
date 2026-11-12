package worker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The old connected integration checkpointed only the profile name. Recover
// the original execution transcript, not a planning/summary/handoff session,
// and copy it once so later format retries retain their preceding response.
func copyLegacyPiOptchatSession(config *piOptchatConfig, session, step, executionWorkspace, stateWorkspace string) (string, error) {
	if config == nil || session != "optchat:"+config.profile || step == "" {
		return session, fmt.Errorf("legacy Pi OptChat recovery requires its original project profile and step")
	}
	runs := filepath.Join(config.home, "profiles", config.profile, "runs")
	entries, err := os.ReadDir(runs)
	if err != nil {
		return session, err
	}
	var source string
	var latest int64
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".optchat.json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(runs, entry.Name()))
		if err != nil {
			return session, err
		}
		var run struct {
			Task        string `json:"task"`
			Cwd         string `json:"cwd"`
			SessionFile string `json:"sessionFile"`
			Started     int64  `json:"started"`
			Connected   bool   `json:"connected"`
		}
		if json.Unmarshal(data, &run) != nil || !run.Connected || run.Cwd != executionWorkspace || run.Started <= latest ||
			!strings.HasPrefix(run.Task, "You are the HotMouse bounded step execution worker.") ||
			!strings.Contains(run.Task, "\nCurrent Step ID: "+step+"\n") {
			continue
		}
		if filepath.Dir(run.SessionFile) != runs || !strings.HasSuffix(run.SessionFile, ".jsonl") {
			return session, fmt.Errorf("legacy Pi OptChat session is outside its profile")
		}
		source, latest = run.SessionFile, run.Started
	}
	if source == "" {
		return session, fmt.Errorf("no original Pi OptChat execution session for step %s", step)
	}
	info, err := os.Lstat(source)
	if err != nil {
		return session, err
	}
	if !info.Mode().IsRegular() {
		return session, fmt.Errorf("legacy Pi OptChat session is not a regular file")
	}
	if err := ensureOwnedDirectory(filepath.Join(stateWorkspace, ".hotmouse")); err != nil {
		return session, err
	}
	target := filepath.Join(stateWorkspace, ".hotmouse", "pi-sessions")
	if err := ensureOwnedDirectory(target); err != nil {
		return session, err
	}
	destination := filepath.Join(target, filepath.Base(source))
	if info, err := os.Lstat(destination); err == nil {
		if !info.Mode().IsRegular() {
			return session, fmt.Errorf("legacy Pi OptChat session destination is not a regular file")
		}
		return destination, nil
	} else if !os.IsNotExist(err) {
		return session, err
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return session, err
	}
	if err := os.WriteFile(destination, data, 0o600); err != nil {
		return session, err
	}
	return destination, nil
}

// Pi appends summary messages to its session file. Copy execution sessions
// into the dispatcher's subtree without replacing prior summary messages.
func copyLegacyPiSessions(projectDir, workspace, session string) (string, error) {
	source := filepath.Join(projectDir, ".hotmouse", "pi-sessions")
	entries, err := os.ReadDir(source)
	if os.IsNotExist(err) {
		return session, nil
	}
	if err != nil {
		return session, err
	}
	if err := ensureOwnedDirectory(filepath.Join(workspace, ".hotmouse")); err != nil {
		return session, err
	}
	target := filepath.Join(workspace, ".hotmouse", "pi-sessions")
	if err := ensureOwnedDirectory(target); err != nil {
		return session, err
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		src, dst := filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name())
		if info, err := os.Lstat(dst); err == nil {
			if !info.Mode().IsRegular() {
				return session, fmt.Errorf("legacy session destination is not a regular file")
			}
		} else if os.IsNotExist(err) {
			data, err := os.ReadFile(src)
			if err != nil {
				return session, err
			}
			if err := os.WriteFile(dst, data, 0o600); err != nil {
				return session, err
			}
		} else {
			return session, err
		}
		if session == src {
			session = dst
		}
	}
	return session, nil
}
