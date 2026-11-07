package cli

import "testing"

func TestCLIConfiguredProjectBudgetAndExplicitOverride(t *testing.T) {
	h := newHarness(t, false)
	h.ok("settings", "set", "default_project_budget_seconds", "3600")
	first := object(object(h.ok("project", "create", "--title", "configured", "--origin", "offline artifact", "--goal", "verify", "--use"))["project"])
	if first["budget_seconds"] != float64(3600) {
		t.Fatal("CLI project creation ignored setting")
	}
	h.ok("settings", "set", "default_project_budget_seconds", "7200")
	route := object(h.ok("route", "create", "--title", "later route"))
	if _, exists := route["budget_seconds"]; exists {
		t.Fatal("CLI route exposes an independent budget")
	}
	current := object(object(h.ok("project", "show"))["project"])
	if current["budget_seconds"] != float64(3600) {
		t.Fatal("creating a route altered project budget")
	}
	changed := object(h.ok("project", "budget", "--budget-seconds", "1800"))
	if changed["budget_seconds"] != float64(1800) {
		t.Fatal("project budget command ignored new value")
	}
	h.expect(2, "route", "create", "--title", "independent cap", "--budget-seconds", "1")
	second := object(object(h.ok("project", "create", "--title", "latest default", "--origin", "offline artifact", "--goal", "verify"))["project"])
	if second["budget_seconds"] != float64(7200) {
		t.Fatal("new project did not use latest configuration")
	}
	unlimited := object(object(h.ok("project", "create", "--title", "unlimited", "--origin", "offline artifact", "--goal", "verify", "--budget-seconds", "0"))["project"])
	if unlimited["budget_seconds"] != float64(0) {
		t.Fatal("CLI discarded explicit unlimited budget")
	}
	h.expect(2, "settings", "set", "default_project_budget_seconds", "-1")
}
