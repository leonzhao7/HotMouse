package core

import (
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDecisionPlanRejectsHistoricalDuplicatesInEveryState(t *testing.T) {
	for _, status := range []string{"pending", "working", "completed", "abandoned", "needs_attention"} {
		t.Run(status, func(t *testing.T) {
			db := testDB(t)
			p := strategyProject(t, db)
			s := strategyStep(t, db, p, nil)
			switch status {
			case "working":
				if _, err := db.HeartbeatStep(t.Context(), p, s.ID, "worker"); err != nil {
					t.Fatal(err)
				}
			case "completed":
				strategyResult(t, db, p, s, "progress")
			case "abandoned":
				abandoned := true
				if _, err := db.UpdateStep(t.Context(), p, s.ID, UpdateStep{Abandoned: &abandoned}); err != nil {
					t.Fatal(err)
				}
			case "needs_attention":
				if _, err := db.SQL.Exec(`INSERT INTO worker_execute_recovery(project_id,step_id,status,created_at) VALUES(?,?,'needs_attention',?)`, p, s.ID, now()); err != nil {
					t.Fatal(err)
				}
			}
			plan := DecisionPlan{Goals: []CreateGoal{{Description: "must not be created", Creator: "planner"}}, Steps: []CreateStep{
				{From: []string{"origin"}, Description: "different bounded action", Creator: "planner"},
				{From: []string{"origin"}, Description: "  INSPECT\n authentication  ", Priority: 9, Creator: "planner"},
			}}
			if err := db.ApplyDecisionPlan(t.Context(), p, "planner", plan); err == nil || !strings.Contains(err.Error(), "duplicates "+s.ID) {
				t.Fatalf("historical duplicate accepted: %v", err)
			}
			detail, err := db.GetProject(t.Context(), p)
			if err != nil || len(detail.Steps) != 1 || len(detail.Goals) != 1 {
				t.Fatalf("rejected plan partially committed: %+v err=%v", detail, err)
			}
		})
	}
}

func TestDecisionPlanRejectsInternalDuplicatesButAllowsOtherProjectHistory(t *testing.T) {
	db := testDB(t)
	p := strategyProject(t, db)
	other := strategyProject(t, db)
	strategyStep(t, db, other, nil)
	step := CreateStep{From: []string{"origin"}, Description: "inspect authentication", Creator: "planner"}
	if err := db.ApplyDecisionPlan(t.Context(), p, "planner", DecisionPlan{Steps: []CreateStep{step, step}}); err == nil {
		t.Fatal("same-batch duplicates accepted")
	}
	if err := db.ApplyDecisionPlan(t.Context(), p, "planner", DecisionPlan{Steps: []CreateStep{step}}); err != nil {
		t.Fatalf("another project's history blocked new work: %v", err)
	}
}

func TestPlanningContextRetainsAllHistoricalActionsWithoutEvidence(t *testing.T) {
	db := testDB(t)
	p := strategyProject(t, db)
	want := map[string]string{}
	for i := 0; i < 24; i++ {
		description := fmt.Sprintf("action %d: %s", i, strings.Repeat("complete action description ", 30))
		s, err := db.CreateStep(t.Context(), p, CreateStep{From: []string{"origin"}, Description: description, Creator: "test"})
		if err != nil {
			t.Fatal(err)
		}
		want[s.ID] = strings.TrimSpace(description)
		if i%3 == 0 {
			if _, err := db.ConcludeStep(t.Context(), p, s.ID, Conclude{Worker: "test", Description: "old result", Result: &ExecutionResult{Type: "progress", Detail: "full evidence excluded from history"}}); err != nil {
				t.Fatal(err)
			}
		} else if i%3 == 1 {
			abandoned := true
			if _, err := db.UpdateStep(t.Context(), p, s.ID, UpdateStep{Abandoned: &abandoned}); err != nil {
				t.Fatal(err)
			}
		}
	}
	graph, err := db.ModelContext(t.Context(), p, "decide", "")
	if err != nil {
		t.Fatal(err)
	}
	var view modelContext
	if err := yaml.Unmarshal([]byte(graph), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.StepHistory) != len(want) || len(view.OpenSteps) != 8 || strings.Contains(graph, "full evidence excluded from history") {
		t.Fatalf("history missing actions or leaking full evidence: %+v", view)
	}
	for _, step := range view.StepHistory {
		if step.Description != want[step.ID] || step.Status == "" {
			t.Fatalf("historical description truncated or status missing: %+v", step)
		}
	}
}
