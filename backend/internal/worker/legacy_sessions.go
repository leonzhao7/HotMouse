package worker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Pi appends summary messages to its session file. Copy legacy root-level
// sessions into the dispatcher's subtree to keep new runtime state together.
func copyLegacyPiSessions(projectDir, workspace, session string) (string, error) {
	source := filepath.Join(projectDir, ".hotmouse", "pi-sessions")
	entries, err := os.ReadDir(source)
	if os.IsNotExist(err) {
		return session, nil
	}
	if err != nil {
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
