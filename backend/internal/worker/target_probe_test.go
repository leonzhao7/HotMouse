package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hotmouse/backend/internal/core"
)

func startupWorker(t *testing.T) (*Runner, string) {
	t.Helper()
	db, err := core.Open(filepath.Join(t.TempDir(), "startup.db"))
	if err != nil {
		t.Fatal(err)
	}
	r := New(db)
	t.Cleanup(func() { r.StopTargetProbes(); db.SQL.Close() })
	p, err := db.CreateProject(t.Context(), core.CreateProject{Title: "remote CTF", Origin: "CTF challenge at 127.0.0.1:31337", Goal: "obtain the flag"})
	if err != nil {
		t.Fatal(err)
	}
	return r, p.Project.ID
}

func TestBackgroundAnalysisDoesNotDelayPlanningOrExecution(t *testing.T) {
	for _, entry := range []string{"run_once_plan", "run_once_execute", "dispatcher", "agent", "execute_now"} {
		t.Run(entry, func(t *testing.T) {
			r, project := startupWorker(t)
			r.Interval = 5 * time.Millisecond
			var step core.Step
			if entry != "run_once_plan" && entry != "dispatcher" {
				step = workerStep(t, r, project, "inspect")
			}
			dir := t.TempDir()
			probeMarker, workMarker, release := filepath.Join(dir, "probe"), filepath.Join(dir, "work"), filepath.Join(dir, "release")
			env, _ := json.Marshal(map[string]string{"PROBE_MARKER": probeMarker, "WORK_MARKER": workMarker, "PROBE_RELEASE": release})
			command := `case "$HOTMOUSE_TASK_TYPE" in
 target_probe) printf '%s\n' "$PWD" >> "$PROBE_MARKER"; while [ ! -f "$PROBE_RELEASE" ]; do sleep 0.01; done; printf '%s' '{"required":false,"reason":"offline fixture","targets":[]}';;
 decide) printf '%s' "$PWD" > "$WORK_MARKER"; printf '%s' '{"accepted":true,"data":{"steps":[{"from":["origin"],"description":"inspect","goal_id":"g001"}]}}';;
 execute) printf '%s' "$PWD" > "$WORK_MARKER"; printf '%s' '{"accepted":true,"data":{"description":"confirmed","result":{"type":"progress"}}}';;
 esac`
			if _, err := r.DB.SQL.Exec(`UPDATE worker_config SET driver='command',command=?,environment_json=?`, command, string(env)); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			r.SetLifecycleContext(ctx)
			done := make(chan struct{})
			var workErr error
			go func() {
				defer close(done)
				switch entry {
				case "dispatcher":
					NewDispatcher(r, project).RunOnce(ctx)
				case "agent":
					NewAgentWorker(r, "local/worker/01").RunOnce(ctx)
				case "execute_now":
					_, workErr = r.ExecuteNow(ctx, project, step.ID)
				default:
					r.RunOnce(ctx)
				}
			}()
			defer func() { cancel(); <-done }()
			// Provider capability inventory can take longer on a cold cache.
			deadline := time.Now().Add(20 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(workMarker); err == nil {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if _, err := os.Stat(workMarker); err != nil {
				t.Fatalf("solving waited for slow target analysis: %v", err)
			}
			awaitMarker(t, probeMarker)
			awaitTask(t, done)
			if workErr != nil {
				t.Fatal(workErr)
			}
			state, err := r.DB.TargetProbe(t.Context(), project)
			if err != nil || state.Status != "pending" {
				t.Fatalf("analysis was not concurrent with solving: %+v err=%v", state, err)
			}
			probeCWD, _ := os.ReadFile(probeMarker)
			workCWD, _ := os.ReadFile(workMarker)
			if strings.TrimSpace(string(probeCWD)) == strings.TrimSpace(string(workCWD)) {
				t.Fatal("background analysis shared the solving provider workspace")
			}
			for i := 0; i < 3; i++ {
				r.startTargetStartup(ctx, project)
			}
			r.probeMu.Lock()
			startup := r.targetStartups[project]
			r.probeMu.Unlock()
			if err := os.WriteFile(release, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			awaitTask(t, startup.done)
			state, err = r.DB.TargetProbe(t.Context(), project)
			if err != nil || state.Status != "ready" || state.Preprocessing.Status != "skipped" {
				t.Fatalf("background analysis was not saved: %+v err=%v", state, err)
			}
			var calls int
			if err := r.DB.SQL.QueryRow(`SELECT count(*) FROM worker_task_history WHERE project_id=? AND task_type='target_probe'`, project).Scan(&calls); err != nil || calls != 1 {
				t.Fatalf("duplicate target analysis: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestBackgroundStartupCancelsAndResumeReusesCompletedAnalysis(t *testing.T) {
	for _, action := range []string{"shutdown", "stop_project"} {
		t.Run(action, func(t *testing.T) {
			r, project := startupWorker(t)
			r.Interval = 5 * time.Millisecond
			r.probeInterval = 20 * time.Millisecond
			r.probeDial = func(context.Context, core.TargetEndpoint) error { return nil }
			dir := t.TempDir()
			marker, release := filepath.Join(dir, "analysis"), filepath.Join(dir, "release")
			env, _ := json.Marshal(map[string]string{"PROBE_MARKER": marker, "PROBE_RELEASE": release})
			command := `printf started > "$PROBE_MARKER"; while [ ! -f "$PROBE_RELEASE" ]; do sleep 0.01; done; printf '%s' '{"required":true,"reason":"remote CTF","targets":[{"host":"127.0.0.1","port":31337}]}'`
			if _, err := r.DB.SQL.Exec(`UPDATE worker_config SET driver='command',command=?,environment_json=?`, command, string(env)); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			r.SetLifecycleContext(ctx)
			r.startTargetStartup(ctx, project)
			awaitMarker(t, marker)
			r.probeMu.Lock()
			startup := r.targetStartups[project]
			r.probeMu.Unlock()
			if action == "shutdown" {
				r.StopTargetProbes()
			} else {
				if _, err := r.DB.SetProjectStatus(t.Context(), project, "stopped"); err != nil {
					t.Fatal(err)
				}
				r.CancelProject(project)
			}
			awaitTask(t, startup.done)
			state, err := r.DB.TargetProbe(t.Context(), project)
			if err != nil || state.Status != "pending" || state.AnalysisFailures != 0 || r.ProjectBusy(project) {
				t.Fatalf("canceled background task retained work or counted a failure: %+v err=%v", state, err)
			}
			if action == "stop_project" {
				if _, err := r.DB.SetProjectStatus(t.Context(), project, "active"); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(release, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			r.startTargetStartup(ctx, project)
			r.probeMu.Lock()
			startup = r.targetStartups[project]
			r.probeMu.Unlock()
			awaitTask(t, startup.done)
			// Monitoring must outlive the completed background analysis invocation.
			deadline := time.Now().Add(3 * time.Second)
			for {
				state, err = r.DB.TargetProbe(t.Context(), project)
				if err != nil {
					t.Fatal(err)
				}
				if state.LastCheckedAt != "" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("monitor stopped when background analysis finished")
				}
				time.Sleep(5 * time.Millisecond)
			}
			if _, err := r.DB.SetProjectStatus(t.Context(), project, "stopped"); err != nil {
				t.Fatal(err)
			}
			if _, err := r.DB.SetProjectStatus(t.Context(), project, "active"); err != nil {
				t.Fatal(err)
			}
			r.startTargetStartup(ctx, project)
			r.probeMu.Lock()
			startup = r.targetStartups[project]
			r.probeMu.Unlock()
			awaitTask(t, startup.done)
			var calls int
			if err := r.DB.SQL.QueryRow(`SELECT count(*) FROM worker_task_history WHERE project_id=? AND task_type='target_probe'`, project).Scan(&calls); err != nil || calls != 2 {
				t.Fatalf("resume repeated completed target analysis: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestRemoteProbeFailsProjectWhilePlannerIsRunning(t *testing.T) {
	r, p := startupWorker(t)
	r.Interval = 5 * time.Millisecond
	r.probeInterval = 30 * time.Millisecond
	marker := filepath.Join(t.TempDir(), "planner-started")
	// Start failing after the planner enters, so this verifies cancellation
	// during planning without depending on startup completing within 90 ms.
	r.probeDial = func(context.Context, core.TargetEndpoint) error {
		if _, err := os.Stat(marker); err != nil {
			return nil
		}
		return fmt.Errorf("connection refused")
	}
	logs := &processLogRecorder{stderr: make(chan struct{})}
	r.SetLogOutput(logs)
	env, _ := json.Marshal(map[string]string{"PLANNER_MARKER": marker})
	command := `if [ "$HOTMOUSE_TASK_TYPE" = target_probe ]; then printf '%s' '{"accepted":true,"data":{"required":true,"reason":"remote CTF","targets":[{"host":"127.0.0.1","port":31337}]}}'; else printf started > "$PLANNER_MARKER"; exec sleep 10; fi`
	if _, err := r.DB.SQL.Exec(`UPDATE worker_config SET driver='command',command=?,environment_json=?`, command, string(env)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := r.Start(ctx)
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		project, err := r.DB.GetProjectMeta(t.Context(), p)
		if err != nil {
			t.Fatal(err)
		}
		if project.Status == "failed" {
			if !strings.Contains(project.FailureReason, "连续 3 次探活失败") {
				t.Fatalf("wrong failure: %+v", project)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("planner never started: %v", err)
			}
			state, err := r.DB.TargetProbe(t.Context(), p)
			if err != nil || state.ConsecutiveFailures[0] != 3 {
				t.Fatalf("failure threshold incorrect: %+v err=%v", state, err)
			}
			// Recording the project failure and logging it occur in sequence.
			r.StopTargetProbes()
			for _, expected := range []string{"target.probe.failed", "consecutive_failures=1", "consecutive_failures=3", "failure_limit=3", "connection refused", "target=127.0.0.1:31337"} {
				if !strings.Contains(logs.String(), expected) {
					t.Fatalf("probe diagnostics omitted %q: %s", expected, logs.String())
				}
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("target monitor was blocked by the planning process: %s", logs.String())
}

func TestTargetProbeUsesTCPAndCancelsWithoutCountingFailure(t *testing.T) {
	r, p := startupWorker(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	address := listener.Addr().(*net.TCPAddr)
	endpoint := core.TargetEndpoint{Host: "127.0.0.1", Port: address.Port}
	if results := r.probeTargets(t.Context(), []core.TargetEndpoint{endpoint}); len(results) != 1 || results[0] != "" {
		t.Fatalf("TCP listener deemed unreachable: %v", results)
	}
	connection, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	connection.Close()
	state, err := r.DB.TargetProbe(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.DB.SaveTargetProbeAnalysis(t.Context(), p, state.RunID, core.TargetProbeAnalysis{Required: true, Reason: "remote CTF", Targets: []core.TargetEndpoint{endpoint}}); err != nil {
		t.Fatal(err)
	}
	r.probeDial = func(ctx context.Context, _ core.TargetEndpoint) error { <-ctx.Done(); return ctx.Err() }
	ctx, cancel := context.WithCancel(t.Context())
	if _, err := r.ensureTargetProbe(ctx, p); err != nil {
		t.Fatal(err)
	}
	first := r.targetMonitors[p]
	if _, err := r.ensureTargetProbe(ctx, p); err != nil || r.targetMonitors[p] != first {
		t.Fatalf("duplicate target monitor started: %v", err)
	}
	cancel()
	select {
	case <-first.done:
	case <-time.After(time.Second):
		t.Fatal("probe did not stop after cancellation")
	}
	state, err = r.DB.TargetProbe(t.Context(), p)
	if err != nil || state.ConsecutiveFailures[0] != 0 || state.LastCheckedAt != "" {
		t.Fatalf("cancellation counted as target outage: %+v err=%v", state, err)
	}
}

func TestMalformedBackgroundAnalysisDoesNotBlockWorkAndBoundsRetries(t *testing.T) {
	r, project := startupWorker(t)
	r.Interval = 5 * time.Millisecond
	step := workerStep(t, r, project, "pending work")
	command := `if [ "$HOTMOUSE_TASK_TYPE" = target_probe ]; then printf '{}'; else printf '%s' '{"accepted":true,"data":{"description":"confirmed","result":{"type":"progress"}}}'; fi`
	if _, err := r.DB.SQL.Exec(`UPDATE worker_config SET driver='command',command=?`, command); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r.startTargetStartup(ctx, project)
	for i := 1; i <= 3; i++ {
		deadline := time.Now().Add(3 * time.Second)
		for {
			state, err := r.DB.TargetProbe(t.Context(), project)
			if err != nil {
				t.Fatal(err)
			}
			if state.AnalysisFailures == i {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("background retry did not finish: %+v", state)
			}
			time.Sleep(5 * time.Millisecond)
		}
		if i == 1 {
			NewAgentWorker(r, "local/worker/01").RunOnce(ctx)
			before, err := stepSnapshot(t, r, project, step.ID)
			if err != nil || !before.LLMResponseRecorded {
				t.Fatalf("analysis failure blocked execution: %+v err=%v", before, err)
			}
		}
		if i < 3 {
			if _, err := r.DB.SQL.Exec(`UPDATE project_target_health SET retry_after='' WHERE project_id=?`, project); err != nil {
				t.Fatal(err)
			}
		}
	}
	meta, err := r.DB.GetProjectMeta(t.Context(), project)
	if err != nil || meta.Status != "failed" || !strings.Contains(meta.FailureReason, "目标探活分析连续失败 3 次") {
		t.Fatalf("background analysis did not retain its failure limit: %+v err=%v", meta, err)
	}
}

func TestTargetProbeResponseValidationAndReadOnlyProviders(t *testing.T) {
	for _, raw := range []string{
		`{}`, `{"required":null,"reason":"x","targets":[]}`,
		`{"required":true,"reason":"x","targets":[]}`,
		`{"required":false,"reason":"x","targets":[{"host":"ctf.example","port":80}]}`,
		`{"required":true,"reason":"x","targets":[{"host":"https://ctf.example/a","port":443}]}`,
		`{"required":true,"reason":"x","targets":[{"host":"ctf.example","port":0}]}`,
		`{"required":true,"reason":"x","targets":[{"host":"ctf.example","port":80,"command":"curl"}]}`,
		`{"required":true,"reason":"x","targets":[{"host":"ctf.example","port":80},{"host":"CTF.example","port":80}]}`,
	} {
		if _, err := parseTargetProbeAnalysis([]byte(raw)); err == nil {
			t.Fatalf("invalid target analysis accepted: %s", raw)
		}
	}
	for _, driver := range []string{"codex", "claude", "pi"} {
		args, err := providerArgv(workerConfig{driver: driver, env: map[string]string{}}, "target_probe", "", t.TempDir(), "startup prompt")
		if err != nil {
			t.Fatal(err)
		}
		command := strings.Join(args, " ")
		if driver == "codex" && !strings.Contains(command, "--sandbox read-only") || driver == "claude" && !strings.Contains(command, "--tools Read") || driver == "pi" && !strings.Contains(command, "--tools read") {
			t.Fatalf("startup provider allows execution tools: %s", command)
		}
	}
	if analysis, err := parseTargetProbeAnalysis([]byte(`{"accepted":true,"data":{"required":true,"reason":"remote CTF","targets":[{"host":"::1","port":31337}]}}`)); err != nil || analysis.Targets[0].Address() != "[::1]:31337" {
		t.Fatalf("IPv6 analysis rejected: %+v err=%v", analysis, err)
	}
}

func TestStartupCorrectsLLMPortsFromHostnameDigits(t *testing.T) {
	r, p := startupWorker(t)
	origin := "CTF 挑战题，类别为 [web]，附件在/tmp/author/ae7f901f70e6d0aba59666d7de981b24.zip，目标是[http://oj-10-30-15-1-54549.adworld.xctf.org.cn/]和[http://oj-10-30-15-1-37669.adworld.xctf.org.cn/]"
	if _, err := r.DB.SQL.Exec(`UPDATE facts SET description=? WHERE project_id=? AND id='origin'`, origin, p); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "startup-prompt")
	env, _ := json.Marshal(map[string]string{"PROMPT_MARKER": marker})
	command := `printf '%s' "$HOTMOUSE_WORKER_PROMPT" > "$PROMPT_MARKER"; printf '%s' '{"accepted":true,"data":{"required":true,"reason":"remote CTF web services","targets":[{"host":"oj-10-30-15-1-54549.adworld.xctf.org.cn","port":54549},{"host":"oj-10-30-15-1-37669.adworld.xctf.org.cn","port":37669}]}}'`
	if _, err := r.DB.SQL.Exec(`UPDATE worker_config SET driver='command',command=?,environment_json=?`, command, string(env)); err != nil {
		t.Fatal(err)
	}
	// This regression checks configuration, without contacting the remote hosts.
	r.probeDial = func(context.Context, core.TargetEndpoint) error { return nil }
	if ready, err := r.ensureTargetProbe(t.Context(), p); err != nil || !ready {
		t.Fatalf("startup analysis failed: ready=%v err=%v", ready, err)
	}
	state, err := r.DB.TargetProbe(t.Context(), p)
	if err != nil || len(state.Analysis.Targets) != 2 || state.Analysis.Targets[0].Port != 80 || state.Analysis.Targets[1].Port != 80 {
		t.Fatalf("hostname-derived ports reached monitor: %+v err=%v", state, err)
	}
	prompt, err := os.ReadFile(marker)
	candidates := `[{"host":"oj-10-30-15-1-54549.adworld.xctf.org.cn","port":80},{"host":"oj-10-30-15-1-37669.adworld.xctf.org.cn","port":80}]`
	if err != nil || !strings.Contains(string(prompt), candidates) || !strings.Contains(string(prompt), "NEVER infer a port from digits or hyphens inside a hostname") {
		t.Fatalf("startup prompt lacks parsed URL ports or hostname rule: %s err=%v", prompt, err)
	}
}
