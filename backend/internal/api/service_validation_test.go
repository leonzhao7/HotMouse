package api

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"hotmouse/backend/internal/domain"
	"hotmouse/backend/internal/storage"
)

func createTestGraph(test *testing.T, service *Service, name string) domain.Graph {
	test.Helper()
	graph, err := service.CreateProject(context.Background(), CreateProjectInput{
		Name: name, Goal: "goal", Facts: []string{"first", "second", "third"},
	})
	if err != nil {
		test.Fatalf("create graph: %v", err)
	}
	return graph
}

func TestCreateProjectRequiredFields(test *testing.T) {
	cases := map[string]CreateProjectInput{
		"missing name":  {Goal: "goal", Facts: []string{"fact"}},
		"blank name":    {Name: " \t\n", Goal: "goal", Facts: []string{"fact"}},
		"missing goal":  {Name: "project", Facts: []string{"fact"}},
		"blank goal":    {Name: "project", Goal: "\u3000", Facts: []string{"fact"}},
		"missing facts": {Name: "project", Goal: "goal"},
		"empty facts":   {Name: "project", Goal: "goal", Facts: []string{}},
		"blank fact":    {Name: "project", Goal: "goal", Facts: []string{"valid", "\t"}},
	}
	for name, input := range cases {
		test.Run(name, func(test *testing.T) {
			service := newTestService(test)
			if _, err := service.CreateProject(context.Background(), input); !errors.Is(err, ErrInvalid) {
				test.Fatalf("create error = %v; want ErrInvalid", err)
			}
			assertEmptyGraphTables(test, service)
		})
	}
}

func TestCreateProjectRollbackOnDatabaseFailure(test *testing.T) {
	service := newTestService(test)
	if err := service.db.Exec(`CREATE TRIGGER reject_test_fact BEFORE INSERT ON nodes
		WHEN NEW.content = 'rejected' BEGIN SELECT RAISE(ABORT, 'test insert failure'); END`).Error; err != nil {
		test.Fatal(err)
	}
	_, err := service.CreateProject(context.Background(), CreateProjectInput{
		Name: "atomic", Goal: "goal", Facts: []string{"first fact", "rejected"},
	})
	if err == nil {
		test.Fatal("expected simulated insert failure")
	}
	assertEmptyGraphTables(test, service)
}

func assertEmptyGraphTables(test *testing.T, service *Service) {
	test.Helper()
	for _, model := range []any{&domain.Project{}, &domain.Node{}, &domain.Step{}} {
		var count int64
		if err := service.db.Model(model).Count(&count).Error; err != nil || count != 0 {
			test.Fatalf("table %T count = %d, error = %v; want zero", model, count, err)
		}
	}
}

func TestProjectReadAndAppend(test *testing.T) {
	service := newTestService(test)
	ctx := context.Background()
	projects, err := service.ListProjects(ctx)
	if err != nil || projects == nil || len(projects) != 0 {
		test.Fatalf("empty projects = %+v, %v", projects, err)
	}
	graph, err := service.CreateProject(ctx, CreateProjectInput{
		Name: " project ", Description: " context ", Goal: " goal ", Facts: []string{" first ", "second"},
	})
	if err != nil {
		test.Fatal(err)
	}
	if graph.Name != "project" || graph.Description != "context" || graph.Goal.Content != "goal" || graph.Facts[0].Content != "first" {
		test.Fatalf("inputs were not trimmed: %+v", graph)
	}
	if graph.Goal.ProjectID != graph.ID || graph.Goal.Kind != domain.NodeKindGoal || graph.Goal.CreatedAt.IsZero() {
		test.Fatalf("invalid goal node: %+v", graph.Goal)
	}
	for _, fact := range graph.Facts {
		if fact.ProjectID != graph.ID || fact.Kind != domain.NodeKindFact || fact.ID == 0 {
			test.Fatalf("invalid fact: %+v", fact)
		}
	}
	goal, err := service.GetGoal(ctx, graph.ID)
	if err != nil || !sameNode(goal, graph.Goal) {
		test.Fatalf("read goal = %+v, %v; want %+v", goal, err, graph.Goal)
	}
	added, err := service.AddFact(ctx, graph.ID, " added ")
	if err != nil || added.Content != "added" {
		test.Fatalf("append fact = %+v, %v", added, err)
	}
	facts, err := service.ListFacts(ctx, graph.ID)
	if err != nil || len(facts) != 3 || facts[0].ID != graph.Facts[0].ID || facts[2].ID != added.ID {
		test.Fatalf("read facts = %+v, %v", facts, err)
	}
	steps, err := service.ListSteps(ctx, graph.ID)
	if err != nil || steps == nil || len(steps) != 0 {
		test.Fatalf("empty steps = %+v, %v", steps, err)
	}
	if _, err := service.AddFact(ctx, graph.ID, "\u3000"); !errors.Is(err, ErrInvalid) {
		test.Fatalf("blank fact error = %v", err)
	}
	unknownOperations := []func() error{
		func() error { _, err := service.GetGoal(ctx, 999); return err },
		func() error { _, err := service.ListFacts(ctx, 999); return err },
		func() error { _, err := service.ListSteps(ctx, 999); return err },
		func() error { _, err := service.AddFact(ctx, 999, "fact"); return err },
		func() error { _, err := service.CompleteProject(ctx, 999); return err },
	}
	for index, operation := range unknownOperations {
		if err := operation(); !errors.Is(err, ErrNotFound) {
			test.Fatalf("unknown project operation %d error = %v", index, err)
		}
	}
}

func TestStepEndpointValidation(test *testing.T) {
	service := newTestService(test)
	graph := createTestGraph(test, service, "graph")
	other := createTestGraph(test, service, "other")
	cases := []struct {
		name      string
		projectID uint
		fromID    uint
		toID      uint
		want      error
	}{
		{"missing source", graph.ID, 0, graph.Goal.ID, ErrInvalid},
		{"missing target", graph.ID, graph.Facts[0].ID, 0, ErrInvalid},
		{"self step", graph.ID, graph.Facts[0].ID, graph.Facts[0].ID, ErrInvalid},
		{"goal source", graph.ID, graph.Goal.ID, graph.Facts[0].ID, ErrInvalid},
		{"unknown project", 999, graph.Facts[0].ID, graph.Goal.ID, ErrNotFound},
		{"unknown source", graph.ID, 999, graph.Goal.ID, ErrNotFound},
		{"unknown target", graph.ID, graph.Facts[0].ID, 999, ErrNotFound},
		{"foreign source", graph.ID, other.Facts[0].ID, graph.Goal.ID, ErrNotFound},
		{"foreign fact target", graph.ID, graph.Facts[0].ID, other.Facts[0].ID, ErrNotFound},
		{"foreign goal target", graph.ID, graph.Facts[0].ID, other.Goal.ID, ErrNotFound},
		{"overflow source", graph.ID, ^uint(0), graph.Goal.ID, ErrInvalid},
		{"overflow target", graph.ID, graph.Facts[0].ID, ^uint(0), ErrInvalid},
	}
	for _, entry := range cases {
		test.Run(entry.name, func(test *testing.T) {
			if _, err := service.AddStep(context.Background(), entry.projectID, entry.fromID, entry.toID); !errors.Is(err, entry.want) {
				test.Fatalf("add step error = %v; want %v", err, entry.want)
			}
		})
	}
	steps, err := service.ListSteps(context.Background(), graph.ID)
	if err != nil || len(steps) != 0 {
		test.Fatalf("invalid edges persisted: %+v, %v", steps, err)
	}
	for index := range graph.Facts {
		fromID := graph.Facts[index].ID
		toID := graph.Facts[(index+1)%len(graph.Facts)].ID
		if _, err := service.AddStep(context.Background(), graph.ID, fromID, toID); err != nil {
			test.Fatalf("directed fact cycle should be allowed: %v", err)
		}
	}
}

func TestCompletionIsScopedAndIdempotent(test *testing.T) {
	service := newTestService(test)
	ctx := context.Background()
	graph := createTestGraph(test, service, "graph")
	other := createTestGraph(test, service, "other")
	if _, err := service.AddStep(ctx, graph.ID, graph.Facts[0].ID, graph.Facts[1].ID); err != nil {
		test.Fatal(err)
	}
	if _, err := service.AddStep(ctx, other.ID, other.Facts[0].ID, other.Goal.ID); err != nil {
		test.Fatal(err)
	}
	if _, err := service.CompleteProject(ctx, graph.ID); !errors.Is(err, ErrGoalUnreachable) {
		test.Fatalf("unrelated goal step enabled completion: %v", err)
	}
	projects, err := service.ListProjects(ctx)
	if err != nil || projects[0].Status != domain.ProjectOpen || projects[0].CompletedAt != nil {
		test.Fatalf("failed completion changed project: %+v, %v", projects, err)
	}
	if _, err := service.AddStep(ctx, graph.ID, graph.Facts[1].ID, graph.Goal.ID); err != nil {
		test.Fatal(err)
	}
	first, err := service.CompleteProject(ctx, graph.ID)
	if err != nil {
		test.Fatal(err)
	}
	repeated, err := service.CompleteProject(ctx, graph.ID)
	if err != nil || first.CompletedAt == nil || repeated.CompletedAt == nil || !repeated.CompletedAt.Equal(*first.CompletedAt) {
		test.Fatalf("completion was not idempotent: first=%+v repeated=%+v err=%v", first, repeated, err)
	}
	goal, err := service.GetGoal(ctx, graph.ID)
	if err != nil || !sameNode(goal, graph.Goal) {
		test.Fatalf("completion mutated goal: %+v, %v", goal, err)
	}
}

func sameNode(first, second domain.Node) bool {
	return first.ID == second.ID && first.ProjectID == second.ProjectID &&
		first.Kind == second.Kind && first.Content == second.Content &&
		first.CreatedAt.Equal(second.CreatedAt)
}

func TestConcurrentStepPairAcrossConnections(test *testing.T) {
	path := filepath.Join(test.TempDir(), "concurrent.db")
	services := make([]*Service, 2)
	for index := range services {
		db, err := storage.Open(path)
		if err != nil {
			test.Fatal(err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			test.Fatal(err)
		}
		test.Cleanup(func() { _ = sqlDB.Close() })
		services[index] = NewService(db)
	}
	graph := createTestGraph(test, services[0], "concurrent")
	const attempts = 12
	results := make(chan error, attempts)
	for index := 0; index < attempts; index++ {
		go func(index int) {
			fromID, toID := graph.Facts[0].ID, graph.Facts[1].ID
			if index%2 != 0 {
				fromID, toID = toID, fromID
			}
			_, err := services[index%len(services)].AddStep(context.Background(), graph.ID, fromID, toID)
			results <- err
		}(index)
	}
	successes := 0
	for index := 0; index < attempts; index++ {
		if err := <-results; err == nil {
			successes++
		} else if !errors.Is(err, ErrConflict) {
			test.Fatalf("concurrent add error = %v", err)
		}
	}
	steps, err := services[0].ListSteps(context.Background(), graph.ID)
	if successes != 1 || err != nil || len(steps) != 1 {
		test.Fatalf("concurrent pair: %d successes, steps=%+v, err=%v", successes, steps, err)
	}
}

func TestCanceledContextDoesNotCreateProject(test *testing.T) {
	service := newTestService(test)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := service.CreateProject(ctx, CreateProjectInput{Name: "project", Goal: "goal", Facts: []string{"fact"}})
	if !errors.Is(err, context.Canceled) {
		test.Fatalf("canceled create error = %v", err)
	}
	assertEmptyGraphTables(test, service)
}
