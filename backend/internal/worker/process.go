package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"hotmouse/backend/internal/logging"
)

type processResult struct {
	stdout, stderr []byte
	err            error
	timedOut       bool
	outputLimited  bool
}

// capture bounds retained output while still draining pipes and observing
// session events. A large provider response must not block process cancellation.
type capture struct {
	data    bytes.Buffer
	pending []byte
	limit   int
	limited bool
	line    func([]byte)
	total   atomic.Int64
}

func (w *capture) Write(p []byte) (int, error) {
	n := len(p)
	w.total.Add(int64(n))
	remaining := w.limit - w.data.Len()
	if len(p) > remaining {
		w.limited = true
		w.data.Write(p[:remaining])
	} else {
		w.data.Write(p)
	}
	if w.line != nil && len(w.pending)+len(p) <= 2<<20 {
		w.pending = append(w.pending, p...)
		for {
			at := bytes.IndexByte(w.pending, '\n')
			if at < 0 {
				break
			}
			w.line(w.pending[:at])
			w.pending = w.pending[at+1:]
		}
	} else if len(w.pending)+len(p) > 2<<20 {
		w.pending = nil
	}
	return n, nil
}

func runProcess(parent context.Context, argv []string, env map[string]string, cwd string, vars map[string]string, timeout time.Duration, onLine func([]byte), diagnostics ...*logging.Logger) processResult {
	started := time.Now()
	var ctx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(parent, timeout)
	} else {
		ctx, cancel = context.WithCancel(parent)
	}
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = cwd
	cmd.Env = logging.MergeEnvironment(os.Environ(), env, vars)
	var logger *logging.Logger
	if len(diagnostics) > 0 && diagnostics[0] != nil {
		logger = diagnostics[0].WithEnvironment(cmd.Env)
		logger.Info("process.start", "binary", argv[0], "cwd", cwd, "timeout", timeout, "argv", processArguments(argv))
	}
	// The shell and its descendants share a process group. Cancellation first
	// lets the CLI flush its session, then kills the entire remaining group.
	configureProcessGroup(cmd)
	var killTimer *time.Timer
	var killMu sync.Mutex
	cmd.Cancel = func() error {
		if logger != nil {
			logger.Warn("process.cancel", "reason", ctx.Err(), "elapsed", time.Since(started))
		}
		err := terminateProcessGroup(cmd, false)
		killMu.Lock()
		killTimer = time.AfterFunc(5*time.Second, func() { _ = terminateProcessGroup(cmd, true) })
		killMu.Unlock()
		return err
	}
	cmd.WaitDelay = 6 * time.Second
	out, stderr := &capture{limit: 16 << 20}, &capture{limit: 1 << 20}
	out.line = func(line []byte) {
		if logger != nil {
			logger.Debug("process.stdout", "text", string(line))
		}
		if onLine != nil {
			onLine(line)
		}
	}
	if logger != nil {
		stderr.line = func(line []byte) { logger.Info("process.stderr", "text", string(line)) }
	}
	cmd.Stdout, cmd.Stderr = out, stderr
	err := cmd.Start()
	if err == nil {
		if logger == nil {
			err = cmd.Wait()
		} else {
			logger.Info("process.started", "pid", cmd.Process.Pid)
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			heartbeat := time.NewTicker(30 * time.Second)
		wait:
			for {
				select {
				case err = <-done:
					break wait
				case <-heartbeat.C:
					logger.Info("process.running", "pid", cmd.Process.Pid, "elapsed", time.Since(started),
						"stdout_bytes", out.total.Load(), "stderr_bytes", stderr.total.Load())
				}
			}
			heartbeat.Stop()
		}
	}
	killMu.Lock()
	if killTimer != nil {
		killTimer.Stop()
	}
	killMu.Unlock()
	if (ctx.Err() != nil || errors.Is(err, exec.ErrWaitDelay)) && cmd.Process != nil {
		_ = terminateProcessGroup(cmd, true)
	}
	if out.line != nil && len(out.pending) > 0 {
		out.line(out.pending)
	}
	if stderr.line != nil && len(stderr.pending) > 0 {
		stderr.line(stderr.pending)
	}
	if out.limited {
		err = fmt.Errorf("worker output exceeded 16 MiB")
	}
	if logger != nil {
		exitCode := -1
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}
		fields := []any{"exit_code", exitCode, "elapsed", time.Since(started), "stdout_bytes", out.total.Load(),
			"stderr_bytes", stderr.total.Load(), "timed_out", ctx.Err() == context.DeadlineExceeded,
			"canceled", ctx.Err() == context.Canceled, "output_limited", out.limited, "error", err}
		if err == nil {
			logger.Info("process.exit", fields...)
		} else if ctx.Err() != nil {
			logger.Warn("process.exit", fields...)
		} else {
			logger.Error("process.exit", fields...)
		}
	}
	return processResult{stdout: out.data.Bytes(), stderr: stderr.data.Bytes(), err: err, timedOut: ctx.Err() == context.DeadlineExceeded, outputLimited: out.limited}
}

func processArguments(argv []string) []string {
	args := append([]string(nil), argv...)
	if len(args) < 2 {
		return args
	}
	switch filepath.Base(args[0]) {
	case "codex", "claude", "pi":
		args[len(args)-1] = fmt.Sprintf("[prompt: %d bytes]", len(args[len(args)-1]))
	case "sh", "bash":
		for i := 1; i+1 < len(args); i++ {
			if args[i] == "-c" {
				args[i+1] = fmt.Sprintf("[shell command: %d bytes]", len(args[i+1]))
				break
			}
		}
	}
	return args
}
