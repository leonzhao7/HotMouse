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

func TestStartupAnalysisPrecedesPlanningAndPrecreatedSteps(t *testing.T) {
	for _, precreated := range []bool{false, true} {
		t.Run(fmt.Sprint(precreated), func(t *testing.T) {
			r, p := startupWorker(t)
			if precreated {
				workerStep(t, r, p, "inspect")
			}
			marker := filepath.Join(t.TempDir(), "tasks")
			env, _ := json.Marshal(map[string]string{"TASK_MARKER": marker})
			command := `printf '%s\n' "$HOTMOUSE_TASK_TYPE" >> "$TASK_MARKER"
case "$HOTMOUSE_TASK_TYPE" in
target_probe) printf '%s' '{"accepted":true,"data":{"required":false,"reason":"offline fixture","targets":[]}}';;
decide) printf '%s' '{"accepted":true,"data":{"steps":[{"from":["origin"],"description":"inspect","goal_id":"g001"}]}}';;
execute) printf '%s' '{"accepted":true,"data":{"description":"confirmed","result":{"type":"progress"}}}';;
esac`
			if _, err := r.DB.SQL.Exec(`UPDATE worker_config SET driver='command',command=?,environment_json=?`, command, string(env)); err != nil {
				t.Fatal(err)
			}
			NewAgentWorker(r, "local/worker/01").RunOnce(t.Context())
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("execution started before startup analysis: %v", err)
			}
			r.RunOnce(t.Context())
			if !precreated {
				r.RunOnce(t.Context())
			}
			calls, err := os.ReadFile(marker)
			want := "target_probe\nexecute\n"
			if !precreated {
				want = "target_probe\ndecide\nexecute\n"
			}
			if err != nil || string(calls) != want {
				t.Fatalf("startup ordering: calls=%q want=%q err=%v", calls, want, err)
			}
			state, err := r.DB.TargetProbe(t.Context(), p)
			if err != nil || state.Status != "ready" || state.Analysis.Required {
				t.Fatalf("startup analysis not saved: %+v err=%v", state, err)
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
	deadline := time.Now().Add(3 * time.Second)
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
	t.Fatal("target monitor was blocked by the planning process")
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

func TestMalformedStartupAnalysisRetainsGateAndBoundsRetries(t *testing.T) {
	r, p := startupWorker(t)
	workerStep(t, r, p, "pending work")
	if _, err := r.DB.SQL.Exec(`UPDATE worker_config SET driver='command',command='printf "{}"'`); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		if _, err := r.DB.SQL.Exec(`UPDATE project_target_health SET retry_after='' WHERE project_id=?`, p); err != nil {
			t.Fatal(err)
		}
		r.dispatchProject(t.Context(), p)
		NewAgentWorker(r, "local/worker/01").RunOnce(t.Context())
		state, err := r.DB.TargetProbe(t.Context(), p)
		if err != nil || state.Status != "pending" || state.AnalysisFailures != i {
			t.Fatalf("invalid startup bypassed gate: %+v err=%v", state, err)
		}
	}
	detail, err := r.DB.GetProject(t.Context(), p)
	if err != nil || detail.Project.Status != "failed" || detail.Steps[0].LLMResponseRecorded {
		t.Fatalf("invalid startup executed work or failed to terminate: %+v err=%v", detail, err)
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
