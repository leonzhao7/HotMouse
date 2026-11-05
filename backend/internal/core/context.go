package core

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ensureProjectContextFile creates optional project context files only when
// an operator configured an existing root.
// Failure to write a convenience file never makes project creation fail.
func (d *DB) ensureProjectContextFile(ctx context.Context, projectID, origin, goal string) {
	var root string
	if err := d.SQL.QueryRowContext(ctx, `SELECT context_root FROM worker_config WHERE id=1`).Scan(&root); err != nil {
		return
	}
	root = ResolveProjectContextRoot(root)
	if root == "" {
		return
	}
	projects := filepath.Join(root, "projects")
	if err := os.MkdirAll(projects, 0o755); err != nil {
		return
	}
	path := filepath.Join(projects, projectID+".project-context.yaml")
	if _, err := os.Stat(path); err == nil {
		return
	}
	payload := map[string]any{
		"schema_version": 1,
		"inherits":       "../default.project-context.yaml",
		"project": map[string]any{
			"project_id":     projectID,
			"target_summary": origin,
			"goal":           goal,
			"notes":          "",
		},
		"override": map[string]any{
			"authorization": map[string]any{},
			"scope":         map[string]any{},
			"output":        map[string]any{},
		},
	}
	b, err := yaml.Marshal(payload)
	if err != nil {
		return
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		defer file.Close()
		_, _ = file.Write(b)
	}
}

func ResolveProjectContextRoot(root string) string {
	if candidate := strings.TrimSpace(root); candidate != "" {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			absolute, _ := filepath.Abs(candidate)
			return absolute
		}
		return ""
	}
	for _, key := range []string{"HOTMOUSE_PROJECT_CONTEXT_ROOT", "CAIRN_PROJECT_CONTEXT_ROOT"} {
		if candidate := strings.TrimSpace(os.Getenv(key)); candidate != "" {
			if info, err := os.Stat(candidate); err == nil && info.IsDir() {
				absolute, _ := filepath.Abs(candidate)
				return absolute
			}
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		for dir := cwd; ; dir = filepath.Dir(dir) {
			candidate := filepath.Join(dir, ".hotmouse", "context")
			if info, err := os.Stat(candidate); err == nil && info.IsDir() {
				return candidate
			}
			if filepath.Dir(dir) == dir {
				break
			}
		}
	}
	return ""
}

func (d *DB) DefaultWorkspaceRoot() string {
	base := filepath.Dir(d.Path)
	if base == "." || base == "" {
		base = os.TempDir()
	}
	name := strings.TrimSuffix(filepath.Base(d.Path), filepath.Ext(d.Path))
	if name == "" || name == "." {
		name = "hotmouse"
	}
	return filepath.Join(base, "workspaces", name)
}

// Database ownership prevents two independent SQLite files with the same
// project IDs from adopting or cleaning one another's workspace.
func (d *DB) WorkspaceMarker(project string) string {
	sum := sha256.Sum256([]byte(d.Path))
	return project + "\n" + fmt.Sprintf("%x", sum[:]) + "\n"
}

func (d *DB) WorkerWorkspaceMarker(project, worker string) string {
	return d.WorkspaceMarker(project) + worker + "\n"
}

func (d *DB) cleanupCompletedProjectWorkspace(ctx context.Context, projectID string) {
	var root, action string
	if err := d.SQL.QueryRowContext(ctx, `SELECT workspace_root,completed_action FROM worker_config WHERE id=1`).Scan(&root, &action); err != nil || action != "remove" {
		return
	}
	if root == "" {
		root = d.DefaultWorkspaceRoot()
	}
	root, err := filepath.Abs(filepath.Clean(root))
	if err != nil || filepath.Base(projectID) != projectID || projectID == "." || projectID == ".." {
		return
	}
	dir := filepath.Join(root, projectID)
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return
	}
	marker := filepath.Join(dir, ".hotmouse-project")
	markerInfo, err := os.Lstat(marker)
	if err != nil || !markerInfo.Mode().IsRegular() {
		return
	}
	b, err := os.ReadFile(marker)
	if err == nil && string(b) == d.WorkspaceMarker(projectID) {
		_ = os.RemoveAll(dir)
	}
}
