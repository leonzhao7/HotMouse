package competition

import (
	"testing"

	"hotmouse/backend/internal/core"
)

func TestUnattendedCompetitionDiscoversChallengesBeforeFinishing(t *testing.T) {
	f := newFixture(t, 1, 1, 1)
	f.manager.Unattended = true
	f.manager.RunOnce(t.Context())
	f.capture(t, f.platform.ids[0], "flag{first}")
	f.platform.ids = append(f.platform.ids, "new_challenge")
	f.manager.RunOnce(t.Context())
	run := f.run(t)
	if run.Status != "running" || len(run.Items) != 2 || run.Items[1].ChallengeID != "new_challenge" || run.Items[1].Status != "running" {
		t.Fatalf("new challenge was missed before completion: %+v", run)
	}
	f.capture(t, "new_challenge", "flag{new}")
	f.manager.RunOnce(t.Context())
	if run = f.run(t); run.Status != "completed" || len(f.platform.starts) != 2 || len(f.platform.closes) != 2 {
		t.Fatalf("discovered challenge did not complete: %+v", run)
	}
}

func TestUnattendedCompetitionScopeKeepsOtherRunsIdle(t *testing.T) {
	f := newFixture(t, 1, 1, 1)
	other, err := f.db.CreateCompetitionRun(t.Context(), "test", "other-task", core.CompetitionConfig{Rules: f.rules}, f.platform.ids)
	if err != nil {
		t.Fatal(err)
	}
	f.manager.Platform, f.manager.ScopeID = "test", "task-one"
	f.manager.RunOnce(t.Context())
	other, err = f.db.GetCompetitionRun(t.Context(), other.ID)
	if err != nil || other.Status != "planning" || other.Items[0].ProjectID != nil || len(f.platform.starts) != 1 {
		t.Fatalf("unrelated run was advanced: %+v %v", other, err)
	}
}
