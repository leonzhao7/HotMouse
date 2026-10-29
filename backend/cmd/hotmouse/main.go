package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kataras/iris/v12"

	"hotmouse/backend/internal/api"
	"hotmouse/backend/internal/dispatch"
	"hotmouse/backend/internal/domain"
	llmlog "hotmouse/backend/internal/logging"
	"hotmouse/backend/internal/storage"
	"hotmouse/backend/internal/worker"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	return runCommand(os.Args[1:])
}

func runCommand(args []string) error {
	if len(args) > 0 && args[0] == "worker" {
		return runWorker(args[1:])
	}
	return runAPI()
}

func runAPI() error {
	dsn := envOrDefault("DATABASE_DSN", "hotmouse.db")
	address := envOrDefault("HTTP_ADDR", ":8080")

	db, err := storage.Open(dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("get database connection: %w", err)
	}
	defer sqlDB.Close()

	service := api.NewService(db)
	dispatcher := dispatch.NewWithLogger(service, dispatch.AgentFunc(worker.RunConfigured), llmlog.NewWithSink(log.Default(), storage.NewLLMLogSink(db)))
	service.SetProjectChangedHandler(dispatcher.Enqueue)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go dispatcher.Run(ctx)
	app := api.NewApp(service)
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = app.Shutdown(shutdownContext)
	}()
	log.Printf("HotMouse API listening on %s", address)
	if err := app.Listen(address, iris.WithoutServerError(iris.ErrServerClosed)); err != nil {
		return fmt.Errorf("serve HTTP: %w", err)
	}
	return nil
}

func runWorker(args []string) error {
	return runWorkerCommand(args, worker.RunConfigured)
}

func runWorkerCommand(args []string, runAgent func(context.Context, domain.Settings, string, io.Writer) error) error {
	flags := flag.NewFlagSet("worker", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	prompt := flags.String("prompt", "", "prompt sent to the LLM agent")
	outputPath := flags.String("output", "worker-output.txt", "file used to save the agent output")
	if err := flags.Parse(args); err != nil {
		return err
	}
	promptValue := strings.TrimSpace(*prompt)
	if flags.NArg() > 1 || (flags.NArg() == 1 && promptValue != "") {
		return errors.New("worker accepts one prompt argument; use either --prompt or a positional argument")
	}
	if flags.NArg() == 1 {
		promptValue = strings.TrimSpace(flags.Arg(0))
	}
	if promptValue == "" {
		return errors.New("worker requires a non-empty prompt; use --prompt or one positional argument")
	}
	if strings.TrimSpace(*outputPath) == "" {
		return errors.New("worker requires a non-empty --output path")
	}

	db, err := storage.Open(envOrDefault("DATABASE_DSN", "hotmouse.db"))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("get database connection: %w", err)
	}
	defer sqlDB.Close()

	settings, err := api.NewService(db).GetSettings(context.Background())
	if err != nil {
		return fmt.Errorf("load LLM settings: %w", err)
	}
	settings = settings.ForWorker()

	outputDirectory := filepath.Dir(*outputPath)
	tempFile, err := os.CreateTemp(outputDirectory, "."+filepath.Base(*outputPath)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create worker output: %w", err)
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath)
	if err := tempFile.Chmod(0o600); err != nil {
		_ = tempFile.Close()
		return fmt.Errorf("secure worker output: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintln(os.Stderr, "Worker started; streaming agent output:")
	if err := runAgent(ctx, settings, promptValue, io.MultiWriter(tempFile, os.Stdout)); err != nil {
		_ = tempFile.Close()
		return fmt.Errorf("run worker: %w", err)
	}
	if err := tempFile.Sync(); err != nil {
		_ = tempFile.Close()
		return fmt.Errorf("sync worker output: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("close worker output: %w", err)
	}
	if err := os.Rename(tempPath, *outputPath); err != nil {
		return fmt.Errorf("save worker output: %w", err)
	}
	fmt.Fprintf(os.Stderr, "\nSaved agent output to %s\n", *outputPath)
	return nil
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
