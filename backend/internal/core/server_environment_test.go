package core

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func clearServerEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"HOTMOUSE_WORKER_DRIVER", "HOTMOUSE_LLM_BASE_URL", "HOTMOUSE_LLM_API_KEY", "HOTMOUSE_LLM_MODEL", "HOTMOUSE_LLM_API",
		"PI_BASE_URL", "PI_API_KEY", "PI_MODEL", "PI_PROVIDER_API", "PI_MODEL_CONTEXT_WINDOW", "OPTCHAT_HOME",
		"HOTMOUSE_WORKER_COUNT", "HOTMOUSE_PROJECT_CONCURRENCY", "HOTMOUSE_PROJECT_MAX_WORKERS", "HOTMOUSE_PROJECT_BUDGET_SECONDS",
		"HOTMOUSE_STEP_TIMEOUT_SECONDS", "HOTMOUSE_DECIDE_LEASE_TIMEOUT_SECONDS",
		"HOTMOUSE_WORKER_UNHEALTHY_RETRY_AFTER_SECONDS", "HOTMOUSE_WORKER_REJECTED_RETRY_AFTER_SECONDS",
		"HOTMOUSE_DECIDE_TIMEOUT_SECONDS", "HOTMOUSE_EXECUTE_TIMEOUT_SECONDS", "HOTMOUSE_CONCLUDE_TIMEOUT_SECONDS",
		"HOTMOUSE_CONCLUDE_RESERVE_SECONDS", "HOTMOUSE_RECOVERY_ATTEMPTS", "HOTMOUSE_FORMAT_RETRIES", "HOTMOUSE_BASH_TIMEOUT_SECONDS",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("PI_OPTCHAT_EXTENSION", "")
	if err := os.Unsetenv("PI_OPTCHAT_EXTENSION"); err != nil {
		t.Fatal(err)
	}
}

func TestServerEnvironmentUpdatesSavedProvidersAndProjectDefaults(t *testing.T) {
	clearServerEnvironment(t)
	db := testDB(t)
	if _, err := db.SQL.Exec(`UPDATE worker_config SET driver='command',command='old command',environment_json='{"EXTRA":"keep","PI_API_KEY":"old-key"}',runtime_json='{"conclude_timeout_seconds":40}',dispatcher_json='{"driver":"codex","command":"old dispatcher","model":"old-model","environment":{"DISPATCHER_EXTRA":"keep"}}' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"HOTMOUSE_WORKER_DRIVER": "pi", "HOTMOUSE_LLM_BASE_URL": "https://gateway.example/v1",
		"HOTMOUSE_LLM_API_KEY": "fixture-secret-$literal", "HOTMOUSE_LLM_MODEL": "fixture-model",
		"HOTMOUSE_WORKER_COUNT": "6", "HOTMOUSE_PROJECT_CONCURRENCY": "2", "HOTMOUSE_PROJECT_MAX_WORKERS": "3",
		"HOTMOUSE_PROJECT_BUDGET_SECONDS": "600", "HOTMOUSE_EXECUTE_TIMEOUT_SECONDS": "120",
		"HOTMOUSE_CONCLUDE_RESERVE_SECONDS": "10", "HOTMOUSE_RECOVERY_ATTEMPTS": "2", "HOTMOUSE_FORMAT_RETRIES": "0",
		"PI_OPTCHAT_EXTENSION": "/opt/hotmouse/pi/node_modules/pi-optchat/src/index.ts",
	} {
		t.Setenv(name, value)
	}
	if _, err := db.ApplyServerEnvironment(t.Context()); err != nil {
		t.Fatal(err)
	}
	var driver, command, envJSON, runtimeJSON, dispatcherJSON string
	var workers, concurrency int
	if err := db.SQL.QueryRow(`SELECT driver,command,environment_json,runtime_json,dispatcher_json,worker_count,project_concurrency FROM worker_config WHERE id=1`).Scan(&driver, &command, &envJSON, &runtimeJSON, &dispatcherJSON, &workers, &concurrency); err != nil {
		t.Fatal(err)
	}
	var env map[string]string
	var runtime WorkerRuntime
	var dispatcher DispatcherConfig
	if err := json.Unmarshal([]byte(envJSON), &env); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(runtimeJSON), &runtime); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(dispatcherJSON), &dispatcher); err != nil {
		t.Fatal(err)
	}
	if driver != "pi" || command != "" || workers != 6 || concurrency != 2 || env["EXTRA"] != "keep" || env["PI_API_KEY"] != "fixture-secret-$literal" || env["PI_PROVIDER_API"] != "openai" {
		t.Fatalf("worker bootstrap failed: driver=%s workers=%d concurrency=%d", driver, workers, concurrency)
	}
	if dispatcher.Driver != "pi" || dispatcher.Command != "" || dispatcher.Model != "fixture-model" || dispatcher.Environment["PI_API_KEY"] != env["PI_API_KEY"] || dispatcher.Environment["DISPATCHER_EXTRA"] != "keep" || dispatcher.Environment["PI_OPTCHAT_EXTENSION"] != env["PI_OPTCHAT_EXTENSION"] {
		t.Fatal("dispatcher did not receive the shared LLM configuration while preserving unrelated settings")
	}
	if runtime.ExecuteTimeoutSeconds != 120 || runtime.ConcludeTimeoutSeconds != 40 || runtime.ConcludeReserveSeconds != 10 || runtime.RecoveryAttempts != 2 || runtime.FormatRetries != 0 {
		t.Fatalf("partial runtime update lost limits: %+v", runtime)
	}
	project, err := db.CreateProject(t.Context(), CreateProject{Title: "defaults", Origin: "local fixture", Goal: "verify"})
	if err != nil || project.Project.MaxWorkers != 3 || project.Project.BudgetSeconds != 600 {
		t.Fatalf("new project did not inherit environment defaults: %+v %v", project.Project, err)
	}
	zero := 0
	explicit, err := db.CreateProject(t.Context(), CreateProject{Title: "override", Origin: "local fixture", Goal: "verify", MaxWorkers: 1, BudgetSeconds: &zero})
	if err != nil || explicit.Project.MaxWorkers != 1 || explicit.Project.BudgetSeconds != 0 {
		t.Fatalf("explicit project configuration was overwritten: %+v %v", explicit.Project, err)
	}
	// Reopening the same volume with new variables updates saved credentials and
	// future project defaults without rewriting an existing project's allowance.
	path := db.Path
	if err := db.SQL.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	t.Setenv("HOTMOUSE_LLM_API_KEY", "replacement-key")
	t.Setenv("HOTMOUSE_PROJECT_BUDGET_SECONDS", "0")
	t.Setenv("HOTMOUSE_WORKER_COUNT", "2")
	if _, err := db.ApplyServerEnvironment(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL.QueryRow(`SELECT environment_json,worker_count FROM worker_config WHERE id=1`).Scan(&envJSON, &workers); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(envJSON, "replacement-key") || workers != 2 {
		t.Fatal("restart retained stale environment configuration")
	}
	previous, err := db.GetProject(t.Context(), project.Project.ID)
	if err != nil || previous.Project.BudgetSeconds != 600 {
		t.Fatalf("default change rewrote an existing project's budget: %+v %v", previous.Project, err)
	}
	future, err := db.CreateProject(t.Context(), CreateProject{Title: "new allowance", Origin: "local fixture", Goal: "verify"})
	if err != nil || future.Project.BudgetSeconds != 0 {
		t.Fatalf("zero budget did not disable the future project's limit: %+v %v", future.Project, err)
	}
}

func TestServerEnvironmentNoopAndInvalidConfigurationAreAtomic(t *testing.T) {
	for _, invalid := range []struct{ name, value string }{
		{"", ""}, {"HOTMOUSE_WORKER_COUNT", "33"}, {"HOTMOUSE_PROJECT_CONCURRENCY", "-1"},
		{"HOTMOUSE_FORMAT_RETRIES", "2"}, {"HOTMOUSE_PROJECT_BUDGET_SECONDS", "-1"},
		{"HOTMOUSE_LLM_BASE_URL", "https://user:private-secret@example/v1"},
		{"HOTMOUSE_LLM_API", "unsupported"}, {"HOTMOUSE_WORKER_DRIVER", "codex"},
		{"HOTMOUSE_LLM_API_KEY", ""}, {"HOTMOUSE_LLM_MODEL", ""},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			clearServerEnvironment(t)
			db := testDB(t)
			if invalid.name != "" {
				t.Setenv("HOTMOUSE_WORKER_COUNT", "6")
				t.Setenv("HOTMOUSE_LLM_BASE_URL", "https://gateway.example/v1")
				t.Setenv("HOTMOUSE_LLM_API_KEY", "private-secret")
				t.Setenv("HOTMOUSE_LLM_MODEL", "fixture")
				t.Setenv(invalid.name, invalid.value)
			}
			changes, err := db.ApplyServerEnvironment(t.Context())
			if invalid.name == "" {
				if err != nil || len(changes) != 0 {
					t.Fatalf("unconfigured server was changed: %v %v", changes, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), invalid.name) || strings.Contains(err.Error(), "private-secret") {
				t.Fatalf("invalid configuration error did not identify the variable safely: %v", err)
			}
			var driver, raw string
			var count int
			if err := db.SQL.QueryRow(`SELECT driver,worker_count,environment_json FROM worker_config WHERE id=1`).Scan(&driver, &count, &raw); err != nil {
				t.Fatal(err)
			}
			if driver != "mock" || count != 4 || raw != "{}" {
				t.Fatalf("failed/noop bootstrap changed saved configuration: driver=%s workers=%d", driver, count)
			}
		})
	}
}

func TestServerEnvironmentSupportsPiAliasesAndPartialCredentialRotation(t *testing.T) {
	clearServerEnvironment(t)
	db := testDB(t)
	for name, value := range map[string]string{
		"PI_BASE_URL": "https://gateway.example/v1", "PI_API_KEY": "initial-key", "PI_MODEL": "fixture", "PI_PROVIDER_API": "anthropic",
	} {
		t.Setenv(name, value)
	}
	if _, err := db.ApplyServerEnvironment(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOTMOUSE_LLM_API_KEY", "rotated-key")
	t.Setenv("PI_API_KEY", "")
	t.Setenv("PI_BASE_URL", "")
	t.Setenv("PI_MODEL", "")
	t.Setenv("PI_PROVIDER_API", "")
	if _, err := db.ApplyServerEnvironment(t.Context()); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.SQL.QueryRow(`SELECT environment_json FROM worker_config WHERE id=1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var env map[string]string
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatal(err)
	}
	if env["PI_API_KEY"] != "rotated-key" || env["PI_MODEL"] != "fixture" || env["PI_BASE_URL"] != "https://gateway.example/v1" || env["PI_PROVIDER_API"] != "anthropic-messages" {
		t.Fatal("partial credential rotation lost saved model or gateway settings")
	}
}
