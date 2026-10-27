package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hotmouse/backend/internal/api"
	"hotmouse/backend/internal/domain"
	"hotmouse/backend/internal/storage"
)

func TestEnvOrDefault(test *testing.T) {
	test.Setenv("HOTMOUSE_TEST_SETTING", "")
	if got := envOrDefault("HOTMOUSE_TEST_SETTING", "fallback"); got != "fallback" {
		test.Fatalf("empty setting = %q", got)
	}
	test.Setenv("HOTMOUSE_TEST_SETTING", "configured")
	if got := envOrDefault("HOTMOUSE_TEST_SETTING", "fallback"); got != "configured" {
		test.Fatalf("configured setting = %q", got)
	}
}

func TestRunReportsDatabaseFailure(test *testing.T) {
	test.Setenv("DATABASE_DSN", filepath.Join(test.TempDir(), "missing", "hotmouse.db"))
	if err := run(); err == nil || !strings.Contains(err.Error(), "open database") {
		test.Fatalf("startup error = %v; want database error", err)
	}
}

func TestRunWorkerStreamsAndSavesOutput(test *testing.T) {
	dsn := filepath.Join(test.TempDir(), "hotmouse.db")
	db, err := storage.Open(dsn)
	if err != nil {
		test.Fatalf("open database: %v", err)
	}
	if _, err := api.NewService(db).UpdateSettings(context.Background(), api.UpdateSettingsInput{
		LLMURL:    "https://example.test/v1",
		APIKey:    "worker-key",
		Model:     "worker-model",
		MaxTokens: 256,
	}); err != nil {
		test.Fatalf("save settings: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		test.Fatalf("get database connection: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		test.Fatalf("close setup database: %v", err)
	}

	test.Setenv("DATABASE_DSN", dsn)
	outputPath := filepath.Join(test.TempDir(), "worker-output.txt")
	fakeAgent := func(_ context.Context, _ domain.Settings, prompt string, output io.Writer) error {
		if prompt != "summarize this" {
			test.Fatalf("worker prompt = %q", prompt)
		}
		_, err := io.WriteString(output, "saved result")
		return err
	}
	if err := runWorkerCommand([]string{"--output", outputPath, "summarize this"}, fakeAgent); err != nil {
		test.Fatalf("run worker: %v", err)
	}
	content, err := os.ReadFile(outputPath)
	if err != nil {
		test.Fatalf("read worker output: %v", err)
	}
	if string(content) != "saved result" {
		test.Fatalf("saved output = %q", string(content))
	}
	info, err := os.Stat(outputPath)
	if err != nil {
		test.Fatalf("stat worker output: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		test.Fatalf("worker output permissions = %o, want 600", info.Mode().Perm())
	}
}
