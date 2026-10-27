package api

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"hotmouse/backend/internal/domain"
	"hotmouse/backend/internal/storage"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := storage.Open(dsn)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})
	return NewService(db)
}

func TestCreateProjectCreatesOpenGraphWithoutSteps(t *testing.T) {
	service := newTestService(t)
	graph, err := service.CreateProject(context.Background(), CreateProjectInput{
		Name:  "decision",
		Goal:  "ship",
		Facts: []string{"requirements", "design"},
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if graph.ID == 0 || graph.Goal.ID == 0 || graph.Status != domain.ProjectOpen {
		t.Fatalf("unexpected created graph: %+v", graph)
	}
	if len(graph.Facts) != 2 || len(graph.Steps) != 0 {
		t.Fatalf("unexpected graph contents: %+v", graph)
	}
}

func TestCompleteProjectRequiresGoalStep(t *testing.T) {
	service := newTestService(t)
	graph, err := service.CreateProject(context.Background(), CreateProjectInput{
		Name: "incomplete", Goal: "finish", Facts: []string{"start"},
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if _, err := service.CompleteProject(context.Background(), graph.ID); !errors.Is(err, ErrGoalUnreachable) {
		t.Fatalf("complete without goal edge error = %v, want ErrGoalUnreachable", err)
	}
	if _, err := service.AddStep(context.Background(), graph.ID, graph.Facts[0].ID, graph.Goal.ID, ""); err != nil {
		t.Fatalf("add goal step: %v", err)
	}
	completed, err := service.CompleteProject(context.Background(), graph.ID)
	if err != nil {
		t.Fatalf("complete project: %v", err)
	}
	if completed.Status != domain.ProjectCompleted || completed.CompletedAt == nil {
		t.Fatalf("unexpected completed project: %+v", completed)
	}
	if _, err := service.AddFact(context.Background(), graph.ID, "late fact"); !errors.Is(err, ErrProjectClosed) {
		t.Fatalf("add fact after completion error = %v, want ErrProjectClosed", err)
	}
	if _, err := service.AddStep(context.Background(), graph.ID, graph.Facts[0].ID, graph.Goal.ID, ""); !errors.Is(err, ErrProjectClosed) {
		t.Fatalf("add step after completion error = %v, want ErrProjectClosed", err)
	}
}

func TestAddStepEnforcesDirectionUniquenessAndProjectOwnership(t *testing.T) {
	service := newTestService(t)
	graph, err := service.CreateProject(context.Background(), CreateProjectInput{
		Name: "graph", Goal: "goal", Facts: []string{"one", "two"},
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if _, err := service.AddStep(context.Background(), graph.ID, graph.Goal.ID, graph.Facts[0].ID, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("goal as origin error = %v, want ErrInvalid", err)
	}
	if _, err := service.AddStep(context.Background(), graph.ID, graph.Facts[0].ID, graph.Goal.ID, ""); err != nil {
		t.Fatalf("add goal edge: %v", err)
	}
	if _, err := service.AddStep(context.Background(), graph.ID, graph.Goal.ID, graph.Facts[0].ID, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("reverse goal edge error = %v, want ErrInvalid", err)
	}
	if _, err := service.AddStep(context.Background(), graph.ID, graph.Facts[0].ID, graph.Goal.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate edge error = %v, want ErrConflict", err)
	}
	if _, err := service.AddStep(context.Background(), graph.ID, graph.Facts[1].ID, graph.Facts[0].ID, ""); err != nil {
		t.Fatalf("add fact reverse edge: %v", err)
	}
	if _, err := service.AddStep(context.Background(), graph.ID, graph.Facts[0].ID, graph.Facts[1].ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("reverse duplicate edge error = %v, want ErrConflict", err)
	}
	other, err := service.CreateProject(context.Background(), CreateProjectInput{
		Name: "other", Goal: "other goal", Facts: []string{"other fact"},
	})
	if err != nil {
		t.Fatalf("create second project: %v", err)
	}
	if _, err := service.AddStep(context.Background(), graph.ID, graph.Facts[0].ID, other.Facts[0].ID, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-project edge error = %v, want ErrNotFound", err)
	}
}

func TestAddStepStoresTrimmedDescription(t *testing.T) {
	service := newTestService(t)
	graph, err := service.CreateProject(context.Background(), CreateProjectInput{
		Name: "described", Goal: "goal", Facts: []string{"one", "two"},
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	step, err := service.AddStep(context.Background(), graph.ID, graph.Facts[0].ID, graph.Goal.ID, "  ship the prototype  ")
	if err != nil {
		t.Fatalf("add step with description: %v", err)
	}
	if step.Description != "ship the prototype" {
		t.Fatalf("returned description = %q, want trimmed value", step.Description)
	}
	steps, err := service.ListSteps(context.Background(), graph.ID)
	if err != nil || len(steps) != 1 {
		t.Fatalf("list steps = %+v, err = %v", steps, err)
	}
	if steps[0].Description != "ship the prototype" {
		t.Fatalf("stored description = %q, want trimmed value", steps[0].Description)
	}

	blank, err := service.AddStep(context.Background(), graph.ID, graph.Facts[1].ID, graph.Goal.ID, "   ")
	if err != nil {
		t.Fatalf("add step with blank description: %v", err)
	}
	if blank.Description != "" {
		t.Fatalf("blank description = %q, want empty", blank.Description)
	}
	steps, err = service.ListSteps(context.Background(), graph.ID)
	if err != nil || len(steps) != 2 || steps[1].Description != "" {
		t.Fatalf("blank description was not stored as empty: %+v, %v", steps, err)
	}
}
