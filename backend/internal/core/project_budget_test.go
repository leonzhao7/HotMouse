package core

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func budgetProject(t *testing.T, db *DB, budget *int) ProjectDetail {
	t.Helper()
	p, err := db.CreateProject(t.Context(), CreateProject{Title: "budget fixture", Origin: "offline artifact", Goal: "verify artifact", BudgetSeconds: budget})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProjectBudgetSnapshotsSettingsAndRoutesHaveNoSeparateBudget(t *testing.T) {
	db := testDB(t)
	if _, err := db.SQL.Exec(`UPDATE settings SET default_project_budget_seconds=3600 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	first := budgetProject(t, db, nil)
	if first.Project.BudgetSeconds != 3600 {
		t.Fatal("creation did not use the saved default")
	}
	// Change settings before the project's first step and explicit route.
	if _, err := db.SQL.Exec(`UPDATE settings SET default_project_budget_seconds=7200 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	goal := "g001"
	step, err := db.CreateStep(t.Context(), first.Project.ID, CreateStep{From: []string{"origin"}, GoalID: &goal, Description: "inspect artifact", Creator: "test"})
	if err != nil {
		t.Fatal(err)
	}
	route, err := db.CreateRoute(t.Context(), first.Project.ID, CreateRoute{Title: "different approach"})
	if err != nil || route.BudgetSeconds != 0 {
		t.Fatalf("new route introduced another budget: %+v %v", route, err)
	}
	detail, err := db.GetProject(t.Context(), first.Project.ID)
	if err != nil || detail.Project.BudgetSeconds != 3600 || len(detail.Routes) != 2 {
		t.Fatalf("project snapshot changed: %+v %v", detail, err)
	}
	for _, r := range detail.Routes {
		if r.BudgetSeconds != 0 {
			t.Fatalf("route introduced another budget: %+v", r)
		}
	}
	override := 1800
	if _, err := db.CreateRoute(t.Context(), first.Project.ID, CreateRoute{Title: "short confirmation", BudgetSeconds: &override}); err == nil {
		t.Fatal("independent route budget accepted")
	}
	second := budgetProject(t, db, nil)
	if second.Project.BudgetSeconds != 7200 {
		t.Fatal("later project missed the changed default")
	}
	zero := 0
	unlimited := budgetProject(t, db, &zero)
	if unlimited.Project.BudgetSeconds != 0 {
		t.Fatal("explicit zero was mistaken for an omitted budget")
	}
	negative := -1
	if _, err := db.CreateProject(t.Context(), CreateProject{Title: "invalid", Origin: "artifact", Goal: "verify", BudgetSeconds: &negative}); err == nil {
		t.Fatal("negative project budget was accepted")
	}
	for _, doc := range []struct{ phase, step string }{{"execute", step.ID}, {"decide", ""}} {
		value, err := db.ModelContext(t.Context(), first.Project.ID, doc.phase, doc.step)
		if err != nil {
			t.Fatal(err)
		}
		var view struct {
			Project struct {
				Budget int `yaml:"budget_seconds"`
			} `yaml:"project"`
		}
		if err := yaml.Unmarshal([]byte(value), &view); err != nil || view.Project.Budget != 3600 {
			t.Fatalf("model context lost project budget: %v %+v", err, view)
		}
		if strings.Count(value, "budget_seconds:") != 1 {
			t.Fatal("model context contains independent route budgets")
		}
	}
	data, err := json.Marshal(detail.Routes)
	if err != nil || strings.Contains(string(data), "budget_seconds") || strings.Contains(string(data), "budget_spent_ms") {
		t.Fatalf("route API exposes old budget fields: %s %v", data, err)
	}
}

func TestInheritedBudgetControlsStopLossAndSupportsUnlimited(t *testing.T) {
	for _, seconds := range []int{12, 0} {
		t.Run(map[int]string{12: "bounded", 0: "unlimited"}[seconds], func(t *testing.T) {
			db := testDB(t)
			if _, err := db.SQL.Exec(`UPDATE settings SET default_project_budget_seconds=? WHERE id=1`, seconds); err != nil {
				t.Fatal(err)
			}
			p := budgetProject(t, db, nil)
			if _, err := db.CreateStep(t.Context(), p.Project.ID, CreateStep{From: []string{"origin"}, Description: "bounded inspection", Creator: "test"}); err != nil {
				t.Fatal(err)
			}
			if _, err := db.SQL.Exec(`UPDATE routes SET invested_ms=13000 WHERE project_id=?`, p.Project.ID); err != nil {
				t.Fatal(err)
			}
			if err := db.RefreshRouteLimits(t.Context()); err != nil {
				t.Fatal(err)
			}
			meta, err := db.GetProjectMeta(t.Context(), p.Project.ID)
			if err != nil || (meta.Status == "failed") != (seconds > 0) {
				t.Fatalf("configured budget did not control actual scheduling: %+v %v", meta, err)
			}
		})
	}
}

func TestProjectBudgetMigrationPreservesExistingRouteAndSavedDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p := budgetProject(t, db, nil)
	duration := 4700
	route, err := db.CreateRoute(t.Context(), p.Project.ID, CreateRoute{Title: "existing custom route"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE routes SET budget_seconds=?,invested_ms=1900,no_progress_count=1 WHERE project_id=?`, duration, p.Project.ID); err != nil {
		t.Fatal(err)
	}
	db.SQL.Close()
	legacy, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`ALTER TABLE projects DROP COLUMN budget_seconds; ALTER TABLE settings DROP COLUMN default_project_budget_seconds;`); err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	legacy.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := db.GetProject(t.Context(), p.Project.ID)
	if err != nil || meta.Project.BudgetSeconds != duration || meta.Routes[0].ID != route.ID || meta.Routes[0].BudgetSeconds != duration || meta.Routes[0].InvestedMilliseconds != 1900 || meta.Routes[0].NoProgressCount != 1 || meta.Project.Status != "active" {
		t.Fatalf("migration rewrote existing project/route: %+v %v", meta, err)
	}
	if _, err := db.SQL.Exec(`UPDATE settings SET default_project_budget_seconds=9000 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	db.SQL.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.SQL.Close() })
	meta, err = db.GetProject(t.Context(), p.Project.ID)
	if err != nil || meta.Project.BudgetSeconds != duration || meta.Routes[0].BudgetSeconds != duration {
		t.Fatal("restart overwrote the project snapshot", err)
	}
	var saved int
	if err := db.SQL.QueryRow(`SELECT default_project_budget_seconds FROM settings WHERE id=1`).Scan(&saved); err != nil || saved != 9000 {
		t.Fatal("restart reset configured default", err)
	}
	var violations int
	if err := db.SQL.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("migration broke references: %d %v", violations, err)
	}
}

func TestProjectBudgetMigrationAggregatesLegacyBaselinesOnlyOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-baselines.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.SQL.Close() })
	seconds := 75
	p := budgetProject(t, db, &seconds)
	first, err := db.CreateRoute(t.Context(), p.Project.ID, CreateRoute{Title: "first legacy route"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.CreateRoute(t.Context(), p.Project.ID, CreateRoute{Title: "second legacy route"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetProjectStatus(t.Context(), p.Project.ID, "stopped"); err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct {
		id                     string
		budget, invested, base int
	}{{first.ID, 12, 12000, 9000}, {second.ID, 24, 6000, 1000}} {
		if _, err := db.SQL.Exec(`UPDATE routes SET budget_seconds=?,invested_ms=?,budget_baseline_ms=? WHERE project_id=? AND id=?`, r.budget, r.invested, r.base, p.Project.ID, r.id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.SQL.Exec(`ALTER TABLE projects DROP COLUMN budget_baseline_ms`); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := db.GetProjectMeta(t.Context(), p.Project.ID)
	if err != nil || meta.BudgetSeconds != seconds || meta.Status != "stopped" || meta.InvestedMilliseconds != 18000 || meta.BudgetBaselineMilliseconds != 10000 || meta.BudgetSpentMilliseconds != 8000 {
		t.Fatalf("migration changed historical investment or allowance: %+v %v", meta, err)
	}
	// A saved project baseline must survive later route changes and restarts.
	if _, err := db.SQL.Exec(`UPDATE routes SET budget_baseline_ms=invested_ms WHERE project_id=?`, p.Project.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	meta, err = db.GetProjectMeta(t.Context(), p.Project.ID)
	if err != nil || meta.BudgetBaselineMilliseconds != 10000 || meta.BudgetSpentMilliseconds != 8000 || meta.BudgetSeconds != seconds || meta.Status != "stopped" {
		t.Fatalf("restart recalculated the saved project baseline: %+v %v", meta, err)
	}
}

func TestSharedProjectBudgetAggregatesRoutesAndResetsOnlyOnExplicitResume(t *testing.T) {
	db := testDB(t)
	seconds := 10
	p := budgetProject(t, db, &seconds)
	first, err := db.CreateRoute(t.Context(), p.Project.ID, CreateRoute{Title: "first approach"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.CreateRoute(t.Context(), p.Project.ID, CreateRoute{Title: "second approach"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE routes SET invested_ms=CASE WHEN id=? THEN 6000 ELSE 3999 END WHERE project_id=?`, first.ID, p.Project.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.RefreshRouteLimits(t.Context()); err != nil {
		t.Fatal(err)
	}
	meta, err := db.GetProjectMeta(t.Context(), p.Project.ID)
	if err != nil || meta.Status != "active" || meta.BudgetSpentMilliseconds != 9999 {
		t.Fatalf("unexpected shared accounting: %+v %v", meta, err)
	}
	if _, err := db.SQL.Exec(`UPDATE routes SET invested_ms=4000 WHERE project_id=? AND id=?`, p.Project.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.RefreshRouteLimits(t.Context()); err != nil {
		t.Fatal(err)
	}
	detail, err := db.GetProject(t.Context(), p.Project.ID)
	if err != nil || detail.Project.Status != "failed" || detail.Project.BudgetSpentMilliseconds != 10000 || !strings.Contains(detail.Project.FailureReason, "budget_exhausted") {
		t.Fatalf("routes did not share one allowance: %+v %v", detail.Project, err)
	}
	for _, route := range detail.Routes {
		if route.Status != "active" {
			t.Fatal("project exhaustion created an independent route stop-loss")
		}
	}
	if _, err := db.SetProjectStatus(t.Context(), p.Project.ID, "active"); err != nil {
		t.Fatal(err)
	}
	meta, err = db.GetProjectMeta(t.Context(), p.Project.ID)
	if err != nil || meta.BudgetSpentMilliseconds != 0 || meta.InvestedMilliseconds != 10000 || meta.BudgetSeconds != seconds {
		t.Fatalf("resume changed saved budget or history: %+v %v", meta, err)
	}
	if _, err := db.SQL.Exec(`UPDATE routes SET invested_ms=4500 WHERE project_id=? AND id=?`, p.Project.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetProjectStatus(t.Context(), p.Project.ID, "active"); err != nil {
		t.Fatal(err)
	}
	meta, err = db.GetProjectMeta(t.Context(), p.Project.ID)
	if err != nil || meta.BudgetSpentMilliseconds != 500 {
		t.Fatalf("duplicate resume replenished budget: %+v %v", meta, err)
	}
	zero := 0
	unlimited := budgetProject(t, db, &zero)
	if _, err := db.CreateRoute(t.Context(), unlimited.Project.ID, CreateRoute{Title: "legacy route"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE routes SET budget_seconds=1,invested_ms=50000 WHERE project_id=?`, unlimited.Project.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.RefreshRouteLimits(t.Context()); err != nil {
		t.Fatal(err)
	}
	meta, err = db.GetProjectMeta(t.Context(), unlimited.Project.ID)
	if err != nil || meta.Status != "active" {
		t.Fatal("legacy route cap stopped an unlimited project", err)
	}
}
