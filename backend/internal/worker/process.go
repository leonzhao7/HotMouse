package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
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
}

func (w *capture) Write(p []byte) (int, error) {
	n := len(p)
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

func runProcess(parent context.Context, argv []string, env map[string]string, cwd string, vars map[string]string, timeout time.Duration, onLine func([]byte)) processResult {
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
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	for k, v := range vars {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	// The shell and its descendants share a process group. Cancellation first
	// lets the CLI flush its session, then kills the entire remaining group.
	configureProcessGroup(cmd)
	var killTimer *time.Timer
	cmd.Cancel = func() error {
		err := terminateProcessGroup(cmd, false)
		killTimer = time.AfterFunc(5*time.Second, func() { _ = terminateProcessGroup(cmd, true) })
		return err
	}
	cmd.WaitDelay = 6 * time.Second
	out, stderr := &capture{limit: 16 << 20, line: onLine}, &capture{limit: 1 << 20}
	cmd.Stdout, cmd.Stderr = out, stderr
	err := cmd.Run()
	if killTimer != nil {
		killTimer.Stop()
	}
	if (ctx.Err() != nil || errors.Is(err, exec.ErrWaitDelay)) && cmd.Process != nil {
		_ = terminateProcessGroup(cmd, true)
	}
	if out.line != nil && len(out.pending) > 0 {
		out.line(out.pending)
	}
	if out.limited {
		err = fmt.Errorf("worker output exceeded 16 MiB")
	}
	return processResult{stdout: out.data.Bytes(), stderr: stderr.data.Bytes(), err: err, timedOut: ctx.Err() == context.DeadlineExceeded, outputLimited: out.limited}
}
