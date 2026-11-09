package worker

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
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

func TestCompetitionPlannerReportsTimeoutInHistory(t *testing.T) {
	runtime := core.DefaultWorkerRuntime()
	runtime.DecideTimeoutSeconds = 1
	db, _, _, _ := fakePi(t, `printf '%s\n' '{"type":"session","id":"planning-timeout"}'; sleep 30`, runtime)
	run := core.CompetitionRun{ID: "competition_001", CompetitionConfig: core.CompetitionConfig{Rules: "respect scope"}, Items: []core.CompetitionItem{{ChallengeID: "one"}}}
	_, err := New(db).PlanCompetition(t.Context(), run, []ctfplatform.Challenge{{ID: "one"}})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "decide timed out after 1s") {
		t.Fatalf("planner timeout omitted its cause: %v", err)
	}
	var status, detail string
	if err := db.SQL.QueryRow(`SELECT status,detail FROM worker_task_history WHERE task_type='competition_plan'`).Scan(&status, &detail); err != nil || status != "failed" || !strings.Contains(detail, "decide timed out after 1s") {
		t.Fatalf("planner timeout history: status=%s detail=%s err=%v", status, detail, err)
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
