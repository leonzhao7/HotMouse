package worker

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"hotmouse/backend/internal/logging"
)

type processLogRecorder struct {
	mu      sync.Mutex
	buffer  bytes.Buffer
	stderr  chan struct{}
	noticed sync.Once
}

func (r *processLogRecorder) Write(data []byte) (int, error) {
	r.mu.Lock()
	n, err := r.buffer.Write(data)
	r.mu.Unlock()
	if bytes.Contains(data, []byte("process.stderr")) {
		r.noticed.Do(func() { close(r.stderr) })
	}
	return n, err
}

func (r *processLogRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buffer.String()
}

func TestProcessLogsBeforeExitWithoutChangingProtocolOutput(t *testing.T) {
	t.Setenv("HOTMOUSE_LOG_LEVEL", "debug")
	t.Setenv("HOTMOUSE_LOG_ENV", "1")
	t.Setenv("LLM_API_KEY", "inherited-credential")
	directory := t.TempDir()
	release := filepath.Join(directory, "release")
	recorder := &processLogRecorder{stderr: make(chan struct{})}
	logger := logging.New(recorder, "test")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan processResult, 1)
	command := `printf '%s\n' "$LLM_API_KEY" >&2; printf '%s\n' '{"answers":{"flag":"fixture-answer"}}'; while [ ! -f "$RELEASE" ]; do sleep 0.01; done`
	go func() {
		done <- runProcess(ctx, []string{"sh", "-c", command}, map[string]string{"LLM_API_KEY": "child-credential", "RELEASE": release}, directory, nil, 5*time.Second, nil, logger)
	}()
	select {
	case <-recorder.stderr:
	case <-time.After(2 * time.Second):
		t.Fatal("child stderr was buffered instead of logged while the process ran")
	}
	select {
	case <-done:
		t.Fatal("child exited before its release marker")
	default:
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	var result processResult
	select {
	case result = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("child did not finish after release")
	}
	if result.err != nil || string(result.stdout) != "{\"answers\":{\"flag\":\"fixture-answer\"}}\n" || string(result.stderr) != "child-credential\n" {
		t.Fatal("logging altered the child's protocol output or result")
	}
	text := recorder.String()
	if strings.Contains(text, "child-credential") || strings.Contains(text, "inherited-credential") {
		t.Fatal("process logs exposed an inherited or overridden credential")
	}
	if strings.Contains(text, "environment.snapshot") || strings.Contains(text, "environment.variable") {
		t.Fatal("child process logs repeated the startup environment snapshot")
	}
	for _, expected := range []string{"process.start", "process.started", "process.stderr", "process.stdout", "process.exit", "exit_code=0", "stdout_bytes=", "stderr_bytes="} {
		if !strings.Contains(text, expected) {
			t.Fatalf("process diagnostics omitted %q", expected)
		}
	}
}
