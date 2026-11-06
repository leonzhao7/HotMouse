package cli

import (
	"encoding/json"
	"testing"
)

func TestCLIConfiguresDispatcherWithoutChangingWorkers(t *testing.T) {
	h := newHarness(t, false)
	h.ok("worker", "config", "set", "driver", "mock")
	h.ok("worker", "config", "set", "dispatcher.driver", "claudecode")
	h.ok("worker", "config", "set", "dispatcher.model", "planner-model")
	h.ok("worker", "config", "set", "worker_count", "3")
	code, _, errOut := h.run(`{"dispatcher":{"environment":{"API_KEY":"planner-secret","DELETE":"old"}}}`, "worker", "config", "apply", "--file", "-")
	if code != 0 {
		t.Fatal(errOut)
	}
	code, _, errOut = h.run(`{"dispatcher":{"environment":{"API_KEY":"***"},"environment_remove":["DELETE"]}}`, "worker", "config", "apply", "--file", "-")
	if code != 0 {
		t.Fatal(errOut)
	}
	config := object(h.ok("worker", "config", "show"))
	if config["driver"] != "mock" || config["worker_count"] != float64(3) {
		t.Fatal("dispatcher changed Worker configuration")
	}
	dispatcher := object(config["dispatcher"])
	if dispatcher["driver"] != "claude" || dispatcher["model"] != "planner-model" || object(dispatcher["environment"])["API_KEY"] != "***" {
		t.Fatalf("dispatcher settings missing: %#v", dispatcher)
	}
	var raw string
	if err := h.db.SQL.QueryRow(`SELECT dispatcher_json FROM worker_config WHERE id=1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal([]byte(raw), &saved); err != nil {
		t.Fatal(err)
	}
	if object(saved["environment"])["API_KEY"] != "planner-secret" || object(saved["environment"])["DELETE"] != nil {
		t.Fatal("dispatcher secret overwritten or deleted key retained")
	}
	h.ok("worker", "config", "set", "dispatcher", "null")
	if object(h.ok("worker", "config", "show"))["dispatcher"] != nil {
		t.Fatal("shared configuration was not restored")
	}
}
