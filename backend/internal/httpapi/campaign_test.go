package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kataras/iris/v12"
	"hotmouse/backend/internal/core"
)

func TestCampaignSynthesisUsesEvidenceSignals(t *testing.T) {
	t.Setenv("HOTMOUSE_AUTH_REQUIRED", "0")
	db, err := core.Open(filepath.Join(t.TempDir(), "campaign.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	project, err := db.CreateProject(t.Context(), core.CreateProject{Title: "Project One", Origin: "http://example.test", Goal: "verify target"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateStep(t.Context(), project.Project.ID, core.CreateStep{From: []string{"origin"}, Description: "continue checking access boundary", Creator: "tester"}); err != nil {
		t.Fatal(err)
	}
	factID := "origin"
	if _, err := db.CreateFinding(t.Context(), project.Project.ID, core.CreateFinding{
		Title:       "cross-job unauthorized read",
		Description: "confirmed unauthorized read is real and returns 200",
		Severity:    "high",
		Kind:        "security_vulnerability",
		FactID:      &factID,
		Data:        map[string]any{"evidence": []any{"raw response"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddHint(t.Context(), project.Project.ID, core.CreateHint{Content: "Replay script keeps x &amp; y", Creator: "tester"}); err != nil {
		t.Fatal(err)
	}

	app := iris.New()
	Register(app, New(db))
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, httptest.NewRequest("GET", "/api/projects/"+project.Project.ID+"/campaign", nil))
	if recorder.Code != 200 {
		t.Fatalf("campaign status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		GoalStatus string `json:"goal_status"`
		Counts     struct {
			Facts           int `json:"facts"`
			OpenSteps       int `json:"open_steps"`
			Vulnerabilities int `json:"vulnerabilities"`
		} `json:"counts"`
		Lead      string   `json:"lead"`
		OpenSteps []string `json:"open_steps"`
		Blockers  []string `json:"blockers"`
		NextSteps []string `json:"next_steps"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.GoalStatus != "in_progress" || body.Counts.Facts != 1 || body.Counts.OpenSteps != 1 || body.Counts.Vulnerabilities != 1 {
		t.Fatalf("unexpected campaign state: %s", recorder.Body.String())
	}
	if !strings.Contains(body.Lead, "cross-job unauthorized read") || len(body.OpenSteps) != 1 || len(body.NextSteps) == 0 {
		t.Fatalf("campaign synthesis omitted expected signals: %s", recorder.Body.String())
	}
}
