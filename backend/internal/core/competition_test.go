package core

import (
	"path/filepath"
	"testing"
	"time"
)

func TestCompetitionInitialDescriptionSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "description.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	run, err := db.CreateCompetitionRun(t.Context(), "test", "scope", CompetitionConfig{Rules: "test rules"}, []string{"started", "queued"})
	if err != nil {
		t.Fatal(err)
	}
	origin := "Original challenge description\nTarget address: 10.0.0.1:1337\nAttachment: task.zip\n<script>plain task text</script>"
	project, err := db.CreateProject(t.Context(), CreateProject{Title: "task", Origin: origin, Goal: "solve"})
	if err != nil {
		t.Fatal(err)
	}
	item := run.Items[0]
	item.ProjectID = &project.Project.ID
	if err := db.SaveCompetitionItem(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	loaded, err := db.GetCompetitionRun(t.Context(), run.ID)
	if err != nil || loaded.Items[0].InitialDescription != origin || loaded.Items[1].InitialDescription != "" {
		t.Fatalf("original description not returned for existing runs: %+v %v", loaded, err)
	}
}

func TestCompetitionQueuePersistsAndClaimsRespectConcurrency(t *testing.T) {
	path := filepath.Join(t.TempDir(), "competition.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.SQL.Close() })
	config := CompetitionConfig{Rules: "do not fetch hints", Concurrency: 2, ChallengeTimeoutSeconds: 60}
	run, err := db.CreateCompetitionRun(t.Context(), "tsec", "task-one", config, []string{"one", "two", "three"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateCompetitionRun(t.Context(), "tsec", "task-one", config, []string{"one"}); err == nil {
		t.Fatal("duplicate active competition allowed")
	}
	if err := db.SaveCompetitionPlan(t.Context(), run.ID, "rules interpreted", []string{"three", "one", "two"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveCompetitionPlan(t.Context(), run.ID, "bad plan", []string{"three", "one", "one"}); err == nil {
		t.Fatal("duplicate plan accepted")
	}
	loaded, err := db.GetCompetitionRun(t.Context(), run.ID)
	if err != nil || loaded.Items[0].ChallengeID != "three" || loaded.RulesSummary != "rules interpreted" {
		t.Fatalf("plan rollback: %#v %v", loaded, err)
	}
	for i, id := range []string{"three", "one", "two"} {
		claimed, err := db.ClaimCompetitionItem(t.Context(), run.ID, id, time.Now())
		if err != nil || claimed != (i < 2) {
			t.Fatalf("claim %s: %v %v", id, claimed, err)
		}
	}
	db.SQL.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err = db.GetCompetitionRun(t.Context(), run.ID)
	if err != nil || loaded.Status != "running" || loaded.Rules != config.Rules || loaded.Items[0].Status != "starting" || loaded.Concurrency != 2 {
		t.Fatalf("restart: %#v %v", loaded, err)
	}
	if err := db.RequestCompetitionStop(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	if claimed, err := db.ClaimCompetitionItem(t.Context(), run.ID, "two", time.Now()); err != nil || claimed {
		t.Fatal("claimed work after stop")
	}
}
