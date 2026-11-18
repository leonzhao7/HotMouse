package main

import (
	"context"
	"fmt"
	"hotmouse/backend/internal/ctfy"
	"hotmouse/backend/internal/tpctf"
	"hotmouse/backend/internal/worker"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	mode := os.Getenv("CTFY_MODE")
	workDir := os.Getenv("CTFY_WORK_DIR")
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	_, taskErr := os.Stat(filepath.Join(workDir, "task.json"))
	if mode == "competition" || mode == "" && taskErr != nil && os.Getenv("CTFY_EVAL_DRY_RUN") != "1" {
		os.Exit(tpctf.Run(ctx, func(message string) { fmt.Fprintln(os.Stderr, message) }, worker.RunHostedAgent))
	}
	if mode != "" && mode != "hosted" {
		fmt.Fprintln(os.Stderr, "CTFY_MODE must be competition or hosted")
		os.Exit(1)
	}
	code := ctfy.Run(ctx, func(message string) { fmt.Fprintln(os.Stdout, message) }, func(message string) { fmt.Fprintln(os.Stderr, message) }, worker.RunHostedAgent)
	os.Exit(code)
}
