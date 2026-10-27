package dispatch

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"hotmouse/backend/internal/api"
	"hotmouse/backend/internal/domain"
	"hotmouse/backend/internal/storage"
)

func TestDispatchRunsWorkerSummarizesAndCompletesProject(t *testing.T) {
	db, err := storage.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name()))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get database connection: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	service := api.NewService(db)
	graph, err := service.CreateProject(context.Background(), api.CreateProjectInput{
		Name: "launch",
		Goal: "Launch the product",
		Facts: []string{
			"A prototype is available",
		},
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	var calls int
	agent := AgentFunc(func(_ context.Context, _ domain.Settings, prompt string, output io.Writer) error {
		calls++
		switch {
		case strings.Contains(prompt, "PHASE:PLAN"):
			return writeString(output, fmt.Sprintf(`{"from_fact_id":%d,"action":"Run a user acceptance test","rationale":"The prototype is ready"}`, graph.Facts[0].ID))
		case strings.Contains(prompt, "PHASE:SUMMARIZE"):
			return writeString(output, `{"facts":["Users approved the prototype"],"steps":[{"from_fact_index":1,"to_goal":true,"description":"Approval satisfies launch criteria"}],"goal_reached":true}`)
		default:
			if !strings.Contains(prompt, "Run a user acceptance test") {
				t.Errorf("worker prompt omitted selected action: %s", prompt)
			}
			return writeString(output, "The test passed and users approved the prototype.")
		}
	})
	dispatcher := New(service, agent)
	if err := dispatcher.dispatchProject(context.Background(), graph.ID); err != nil {
		t.Fatalf("dispatch project: %v", err)
	}
	if calls != 3 {
		t.Fatalf("agent calls = %d, want plan, worker, summary", calls)
	}
	updated, err := service.ExportProject(context.Background(), graph.ID)
	if err != nil {
		t.Fatalf("load updated graph: %v", err)
	}
	if updated.Status != domain.ProjectCompleted || updated.CompletedAt == nil {
		t.Fatalf("project status = %q, completed_at = %v", updated.Status, updated.CompletedAt)
	}
	if len(updated.Facts) != 2 || updated.Facts[1].Content != "Users approved the prototype" {
		t.Fatalf("unexpected facts: %+v", updated.Facts)
	}
	if len(updated.Steps) != 1 || updated.Steps[0].FromNodeID != updated.Facts[1].ID || updated.Steps[0].ToNodeID != updated.Goal.ID {
		t.Fatalf("unexpected steps: %+v", updated.Steps)
	}
}

func TestDispatchRejectsUnknownPlannedFact(t *testing.T) {
	db, err := storage.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name()))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get database connection: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	service := api.NewService(db)
	graph, err := service.CreateProject(context.Background(), api.CreateProjectInput{
		Name: "invalid plan", Goal: "finish", Facts: []string{"start"},
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	dispatcher := New(service, AgentFunc(func(_ context.Context, _ domain.Settings, _ string, output io.Writer) error {
		return writeString(output, `{"from_fact_id":999,"action":"continue","rationale":"unknown"}`)
	}))
	if err := dispatcher.dispatchProject(context.Background(), graph.ID); err == nil {
		t.Fatal("dispatch accepted a plan using an unknown fact")
	}
	updated, err := service.ExportProject(context.Background(), graph.ID)
	if err != nil {
		t.Fatalf("load project graph: %v", err)
	}
	if len(updated.Facts) != 1 || len(updated.Steps) != 0 {
		t.Fatalf("invalid plan mutated graph: %+v", updated)
	}
}

func TestEnqueueDeduplicatesPendingProjects(t *testing.T) {
	dispatcher := New(nil, nil)
	dispatcher.Enqueue(4)
	dispatcher.Enqueue(4)
	dispatcher.Enqueue(7)
	first, ok := dispatcher.pop()
	if !ok || first != 4 {
		t.Fatalf("first queued project = %d, %v", first, ok)
	}
	second, ok := dispatcher.pop()
	if !ok || second != 7 {
		t.Fatalf("second queued project = %d, %v", second, ok)
	}
	if _, ok := dispatcher.pop(); ok {
		t.Fatal("duplicate project remained in queue")
	}
}

func writeString(output io.Writer, content string) error {
	_, err := io.WriteString(output, content)
	return err
}
