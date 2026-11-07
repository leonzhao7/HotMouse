package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/kataras/iris/v12"
	"hotmouse/backend/internal/core"
)

func TestSettingsBudgetPreservesLegacyWritesAndCreatesConfiguredProjects(t *testing.T) {
	t.Setenv("HOTMOUSE_AUTH_REQUIRED", "0")
	db, err := core.Open(filepath.Join(t.TempDir(), "budget.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.SQL.Close() })
	app := iris.New()
	app.Logger().SetLevel("disable")
	Register(app, New(db))
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	request := func(method, path string, body any, status int, out any) {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("%s %s: status=%d body=%s", method, path, rec.Code, rec.Body.String())
		}
		if out != nil {
			if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
				t.Fatal(err)
			}
		}
	}
	var settings map[string]any
	request("GET", "/api/settings", nil, 200, &settings)
	settings["default_project_budget_seconds"] = 3600
	request("PUT", "/api/settings", settings, 200, &settings)
	if settings["default_project_budget_seconds"] != float64(3600) {
		t.Fatal("settings did not save configured budget")
	}
	delete(settings, "default_project_budget_seconds")
	request("PUT", "/api/settings", settings, 200, &settings)
	if settings["default_project_budget_seconds"] != float64(3600) {
		t.Fatal("legacy settings client cleared new budget field")
	}
	settings["default_project_budget_seconds"] = -1
	request("PUT", "/api/settings", settings, 400, nil)
	var project core.ProjectDetail
	request("POST", "/projects", map[string]any{"title": "configured project", "origin": "offline fixture", "goal": "verify"}, 201, &project)
	if project.Project.BudgetSeconds != 3600 {
		t.Fatal("project API creation ignored saved configuration")
	}
	settings["default_project_budget_seconds"] = 0
	request("PUT", "/api/settings", settings, 200, &settings)
	var unlimited core.ProjectDetail
	request("POST", "/projects", map[string]any{"title": "unlimited", "origin": "offline fixture", "goal": "verify"}, 201, &unlimited)
	if unlimited.Project.BudgetSeconds != 0 {
		t.Fatal("API cannot configure an unlimited project")
	}
	var route core.Route
	request("POST", "/projects/"+project.Project.ID+"/routes", map[string]any{"title": "later route"}, 200, &route)
	if route.BudgetSeconds != 0 {
		t.Fatal("route API created an independent budget")
	}
	request("POST", "/projects/"+project.Project.ID+"/routes", map[string]any{"title": "obsolete budget", "budget_seconds": 1}, 400, nil)
	var changed core.Project
	request("PUT", "/projects/"+project.Project.ID+"/budget", map[string]any{"budget_seconds": 1800}, 200, &changed)
	if changed.BudgetSeconds != 1800 {
		t.Fatal("project budget API ignored new value")
	}
	request("PUT", "/projects/"+project.Project.ID+"/budget", map[string]any{}, 400, nil)
	request("PUT", "/projects/"+project.Project.ID+"/budget", map[string]any{"budget_seconds": -1}, 400, nil)
	var overridden core.ProjectDetail
	request("POST", "/projects", map[string]any{"title": "override", "origin": "offline fixture", "goal": "verify", "budget_seconds": 1800}, 201, &overridden)
	if overridden.Project.BudgetSeconds != 1800 {
		t.Fatal("API ignored explicit creation override")
	}
	request("POST", "/projects", map[string]any{"title": "invalid", "origin": "offline fixture", "goal": "verify", "budget_seconds": -1}, 400, nil)
}
