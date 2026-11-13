package worker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hotmouse/backend/internal/core"
)

func TestAutomaticPoolLimitsProjectsAndWorkersAndFillsReleasedSlot(t *testing.T) {
	db, err := core.Open(filepath.Join(t.TempDir(), "concurrency.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	r := New(db)
	r.Interval = 20 * time.Millisecond
	marker := t.TempDir()
	environment, _ := json.Marshal(map[string]string{"CONCURRENCY_MARKERS": marker})
	command := `if [ "$HOTMOUSE_TASK_TYPE" = execute ]; then printf started > "$CONCURRENCY_MARKERS/$HOTMOUSE_PROJECT_ID"; while :; do sleep 0.02; done; else printf '{}'; fi`
	if _, err := db.SQL.Exec(`UPDATE worker_config SET driver='command',command=?,environment_json=?,worker_count=4,project_concurrency=2 WHERE id=1`, command, string(environment)); err != nil {
		t.Fatal(err)
	}
	projects := []string{}
	for index := 0; index < 3; index++ {
		project, err := db.CreateProject(t.Context(), core.CreateProject{Title: "queued fixture", Origin: "local fixture", Goal: "verify", MaxWorkers: 1})
		if err != nil {
			t.Fatal(err)
		}
		id := project.Project.ID
		projects = append(projects, id)
		fixtureTargetProbeNotRequired(t, db, id)
		for step := 0; step < 2; step++ {
			priority := 0
			if index == 2 {
				priority = 100 // A queued project's priority cannot bypass the cap.
			}
			if _, err := db.CreateStep(t.Context(), id, core.CreateStep{From: []string{"origin"}, Description: "bounded fixture " + string(rune('a'+step)), Creator: "fixture", Priority: priority}); err != nil {
				t.Fatal(err)
			}
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := r.Start(ctx)
	defer func() {
		cancel()
		awaitTask(t, done)
	}()
	awaitMarker(t, filepath.Join(marker, projects[0]))
	awaitMarker(t, filepath.Join(marker, projects[1]))
	NewProjectDispatcher(r, projects[2]).RunOnce(t.Context())
	var working, activeProjects, queuedHistory int
	if err := db.SQL.QueryRow(`SELECT count(*),count(DISTINCT project_id) FROM steps WHERE worker IS NOT NULL AND worker_role='agent' AND to_fact_id IS NULL`).Scan(&working, &activeProjects); err != nil {
		t.Fatal(err)
	}
	if working != 2 || activeProjects != 2 {
		t.Fatalf("pool bypassed project or per-project worker caps: workers=%d projects=%d", working, activeProjects)
	}
	if err := db.SQL.QueryRow(`SELECT count(*) FROM worker_task_history WHERE project_id=?`, projects[2]).Scan(&queuedHistory); err != nil {
		t.Fatal(err)
	}
	if queuedHistory != 0 {
		t.Fatal("queued project started planning, analysis or execution")
	}
	if _, err := os.Stat(filepath.Join(marker, projects[2])); !os.IsNotExist(err) {
		t.Fatalf("queued project's high-priority step started: %v", err)
	}
	if _, err := db.SetProjectStatus(t.Context(), projects[0], "stopped"); err != nil {
		t.Fatal(err)
	}
	r.CancelProject(projects[0])
	awaitMarker(t, filepath.Join(marker, projects[2]))
	if err := db.SQL.QueryRow(`SELECT count(DISTINCT s.project_id) FROM steps s JOIN projects p ON p.id=s.project_id WHERE p.status='active' AND s.worker IS NOT NULL AND s.worker_role='agent' AND s.to_fact_id IS NULL`).Scan(&activeProjects); err != nil || activeProjects != 2 {
		t.Fatalf("released project slot was not filled correctly: active=%d err=%v", activeProjects, err)
	}
	// Resuming an older project must not displace projects already solving and
	// open an additional slot while their external processes are still running.
	if _, err := db.SetProjectStatus(t.Context(), projects[0], "active"); err != nil {
		t.Fatal(err)
	}
	NewProjectDispatcher(r, projects[0]).RunOnce(t.Context())
	time.Sleep(100 * time.Millisecond)
	if err := db.SQL.QueryRow(`SELECT count(*) FROM steps WHERE project_id=? AND worker IS NOT NULL AND worker_role='agent'`, projects[0]).Scan(&working); err != nil || working != 0 {
		t.Fatalf("resumed project bypassed occupied slots: workers=%d err=%v", working, err)
	}
}

func TestPiOptchatReportsExtensionInitializationFailurePromptly(t *testing.T) {
	db, err := core.Open(filepath.Join(t.TempDir(), "optchat-load.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	r := New(db)
	defer r.stopPiOptchatOwners("")
	root := t.TempDir()
	plugin := filepath.Join(root, "index.ts")
	if err := os.WriteFile(plugin, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(root, "pi")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\nprintf '%s\\n' '{\"type\":\"extension_error\",\"error\":\"fixture-init-error\"}'\nwhile :; do sleep 0.02; done\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := workerConfig{driver: "pi", env: map[string]string{"PI_CLI": cli, "PI_OPTCHAT_EXTENSION": plugin, "OPTCHAT_HOME": root}}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	_, err = r.preparePiOptchat(ctx, cfg, map[string]string{"HOTMOUSE_PROJECT_ID": "fixture", "HOTMOUSE_PROJECT_DIR": root})
	if err == nil || !strings.Contains(err.Error(), "fixture-init-error") {
		t.Fatalf("extension failure was hidden by a startup timeout: %v", err)
	}
}
