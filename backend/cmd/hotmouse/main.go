package main

import (
	"context"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"

	"hotmouse/backend/internal/cli"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	var interrupted atomic.Int32
	go func() {
		select {
		case sig := <-signals:
			if sig == syscall.SIGTERM {
				interrupted.Store(143)
			} else {
				interrupted.Store(130)
			}
			cancel()
		case <-ctx.Done():
		}
	}()
	code := cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	signal.Stop(signals)
	cancel()
	if interrupted.Load() != 0 {
		code = int(interrupted.Load())
	}
	os.Exit(code)
}
