package worker

import (
	"context"
	"path/filepath"
	"testing"

	"hotmouse/backend/internal/core"
	"hotmouse/backend/internal/ctfplatform"
)

func TestCompetitionPlannerUsesRulesAndSeparateContract(t *testing.T) {
	db, err := core.Open(filepath.Join(t.TempDir(), "planner.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	command := `[ "$HOTMOUSE_TASK_TYPE" = competition_plan ] || exit 9; [ "$HOTMOUSE_COMPETITION_RULES" = "do not fetch hints" ] || exit 8; printf '%s' '{"summary":"Respect scope and do not fetch hints","challenge_ids":["two","one"]}'`
	if _, err := db.SQL.Exec(`UPDATE worker_config SET driver='command',command=? WHERE id=1`, command); err != nil {
		t.Fatal(err)
	}
	runner := New(db)
	run := core.CompetitionRun{ID: "competition_001", CompetitionConfig: core.CompetitionConfig{Rules: "do not fetch hints"}, Items: []core.CompetitionItem{{ChallengeID: "one"}, {ChallengeID: "two"}}}
	plan, err := runner.PlanCompetition(t.Context(), run, []ctfplatform.Challenge{{ID: "one"}, {ID: "two"}})
	if err != nil || len(plan.ChallengeIDs) != 2 || plan.ChallengeIDs[0] != "two" || plan.Summary == "" {
		t.Fatalf("plan: %#v %v", plan, err)
	}
	var count int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM worker_task_history WHERE task_type='competition_plan' AND project_id IS NULL AND status='completed'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("planning history: %d %v", count, err)
	}
}

func TestCompetitionCancelsAllProjectInvocationsBeforeQuiescence(t *testing.T) {
	runner := New(nil)
	first, finishFirst := runner.projectContext(t.Context(), "one")
	second, finishSecond := runner.projectContext(t.Context(), "one")
	other, finishOther := runner.projectContext(t.Context(), "two")
	defer finishOther()
	runner.CancelProject("one")
	if first.Err() != context.Canceled || second.Err() != context.Canceled || other.Err() != nil || !runner.ProjectBusy("one") {
		t.Fatal("project cancellation was not isolated")
	}
	finishFirst()
	if !runner.ProjectBusy("one") {
		t.Fatal("one invocation still runs")
	}
	finishSecond()
	if runner.ProjectBusy("one") {
		t.Fatal("finished invocations remain busy")
	}
}
