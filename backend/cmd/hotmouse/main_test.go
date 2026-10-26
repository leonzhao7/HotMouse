package main

import (
	"path/filepath"
	"strings"
	"testing"
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
