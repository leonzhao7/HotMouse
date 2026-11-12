package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hotmouse/backend/internal/core"
)

func TestPiOptchatSharesMemoryAcrossNativeSessions(t *testing.T) {
	cli, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("installed Pi CLI required")
	}
	plugin, err := installedPiOptchat(workerConfig{})
	if err != nil || plugin == "" {
		t.Skip("installed pi-optchat required")
	}
	home, err := os.MkdirTemp("", "hm-oc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("OPTCHAT_HOME", home)
	t.Setenv("HOTMOUSE_PYTHON", "/usr/bin/python3")
	t.Setenv("HOTMOUSE_LOG_ENV", "0")
	var active, maximum atomic.Int32
	var oneRequested, twoRequested atomic.Bool
	arrived := make(chan string, 2)
	release := make(chan struct{})
	var releaseOnce atomic.Bool
	releaseAll := func() {
		if releaseOnce.CompareAndSwap(false, true) {
			close(release)
		}
	}
	seenMemory := atomic.Bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		defer request.Body.Close()
		data, _ := io.ReadAll(request.Body)
		text := string(data)
		if strings.Contains(text, "Read the shared task memory") && strings.Contains(text, "unique_first_task_marker") {
			seenMemory.Store(true)
		}
		name := ""
		if strings.Contains(text, "connected execution one") && oneRequested.CompareAndSwap(false, true) {
			name = "one"
		} else if strings.Contains(text, "connected execution two") && twoRequested.CompareAndSwap(false, true) {
			name = "two"
		}
		if name != "" {
			count := active.Add(1)
			defer active.Add(-1)
			for previous := maximum.Load(); count > previous; previous = maximum.Load() {
				if maximum.CompareAndSwap(previous, count) {
					break
				}
			}
			arrived <- name
			select {
			case <-release:
			case <-request.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk, _ := json.Marshal(map[string]any{"id": "local-fixture", "object": "chat.completion.chunk", "created": 1, "model": "fixture", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": "{\"accepted\":true,\"data\":{\"description\":\"verified_shared_evidence\"}}"}, "finish_reason": nil}}})
		fmt.Fprintf(w, "data: %s\n\n", chunk)
		fmt.Fprint(w, "data: {\"id\":\"local-fixture\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fixture\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	defer releaseAll()
	db, err := core.Open(filepath.Join(t.TempDir(), "optchat.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	r := New(db)
	r.SetLogOutput(os.Stderr)
	defer r.stopPiOptchatOwners("")
	project, err := db.CreateProject(t.Context(), core.CreateProject{Title: "shared profile", Origin: "local fixture", Goal: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	environment := map[string]string{"PI_CLI": cli, "PI_OPTCHAT_EXTENSION": plugin, "OPTCHAT_HOME": home, "PI_MODEL": "fixture", "PI_BASE_URL": server.URL + "/v1", "PI_API_KEY": "local-fixture", "PI_PROVIDER_API": "openai"}
	envJSON, _ := json.Marshal(environment)
	if _, err := db.SQL.Exec("UPDATE worker_config SET driver='pi',environment_json=?", string(envJSON)); err != nil {
		t.Fatal(err)
	}
	projectID := project.Project.ID
	var checkpointed atomic.Int32
	invoke := func(ctx context.Context, identity, prompt string) error {
		cfg := r.config(t.Context())
		cfg.identity = identity
		workspace, err := r.ensureWorkspace(projectID, cfg, identity)
		if err != nil {
			return err
		}
		projectDir := filepath.Dir(filepath.Dir(workspace))
		vars := map[string]string{"HOTMOUSE_PROJECT_ID": projectID, "HOTMOUSE_PROJECT_DIR": projectDir, "HOTMOUSE_WORKSPACE_DIR": workspace, "HOTMOUSE_TASK_TYPE": "execute", "HOTMOUSE_WORKER_PROMPT": prompt}
		_, session, _, err := r.runProvider(ctx, cfg, "execute", "", workspace, vars, 90*time.Second, func(id string) error {
			if id == "" || strings.HasPrefix(id, "optchat:") {
				return fmt.Errorf("missing native Pi checkpoint: %s", id)
			}
			checkpointed.Add(1)
			return nil
		})
		if session == "" || strings.HasPrefix(session, "optchat:") {
			return fmt.Errorf("missing native Pi session: %s", session)
		}
		return err
	}
	firstCtx, cancelFirst := context.WithCancel(t.Context())
	defer cancelFirst()
	secondCtx, cancelSecond := context.WithCancel(t.Context())
	defer cancelSecond()
	type result struct {
		name string
		err  error
	}
	completed := make(chan result, 2)
	go func() {
		completed <- result{name: "one", err: invoke(firstCtx, "worker/one", "connected execution one: remember unique_first_task_marker")}
	}()
	go func() {
		completed <- result{name: "two", err: invoke(secondCtx, "dispatcher/two", "connected execution two: return the local fixture result")}
	}()
	started := map[string]bool{}
	for len(started) < 2 {
		select {
		case name := <-arrived:
			started[name] = true
		case result := <-completed:
			releaseAll()
			t.Fatalf("Pi session ended before concurrent model calls started: %s: %v", result.name, result.err)
		case <-time.After(60 * time.Second):
			releaseAll()
			t.Fatal("same-project Pi sessions did not execute concurrently")
		}
	}
	if maximum.Load() != 2 {
		t.Fatalf("OptChat serialized concurrent sessions: maximum=%d", maximum.Load())
	}
	cancelFirst()
	select {
	case result := <-completed:
		if result.name != "one" || result.err == nil {
			t.Fatalf("canceling one session stopped the wrong worker: %s: %v", result.name, result.err)
		}
	case <-time.After(20 * time.Second):
		releaseAll()
		t.Fatal("cancelled connected Pi session did not stop")
	}
	if !piOptchatAvailable(t.Context(), &piOptchatConfig{profile: r.piOptchatProfile(projectID), home: home, socket: filepath.Join(home, "profiles", r.piOptchatProfile(projectID), "hotmouse.sock")}) {
		releaseAll()
		t.Fatal("cancelling one Pi session stopped the shared profile owner")
	}
	releaseAll()
	select {
	case result := <-completed:
		if result.name != "two" || result.err != nil {
			t.Fatalf("peer Pi session did not finish independently: %s: %v", result.name, result.err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("uncancelled connected Pi session did not finish")
	}
	if err := invoke(t.Context(), "worker/three", "Read the shared task memory and return the fixture result."); err != nil {
		t.Fatal(err)
	}
	if !seenMemory.Load() {
		t.Fatal("a later Pi session did not receive the earlier task's shared memory")
	}
	if checkpointed.Load() != 3 {
		t.Fatalf("native Pi sessions were not checkpointed before their results: %d", checkpointed.Load())
	}
	profile := r.piOptchatProfile(projectID)
	if _, err := os.Stat(filepath.Join(home, "profiles", profile, "config.json")); err != nil {
		t.Fatalf("same challenge did not use its shared OptChat profile: %v", err)
	}
	root, err := r.workspaceRoot(r.config(t.Context()))
	if err != nil {
		t.Fatal(err)
	}
	if matches, _ := filepath.Glob(filepath.Join(root, projectID, "workers", "*", ".hotmouse", "pi-sessions", "*.jsonl")); len(matches) != 3 {
		t.Fatalf("Pi workers did not retain independent native sessions: %v", matches)
	}
}
