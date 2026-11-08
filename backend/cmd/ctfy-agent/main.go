package main

import (
	"context"
	"fmt"
	"hotmouse/backend/internal/ctfy"
	"hotmouse/backend/internal/worker"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	code := ctfy.Run(ctx, func(message string) { fmt.Fprintln(os.Stdout, message) }, func(message string) { fmt.Fprintln(os.Stderr, message) }, worker.RunHostedAgent)
	os.Exit(code)
}
