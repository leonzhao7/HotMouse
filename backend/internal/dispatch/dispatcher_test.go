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
	_, err = service.UpdateSettings(context.Background(), api.UpdateSettingsInput{
		LLMURL: "https://worker.example/v1", APIKey: "worker-key", Model: "worker-model", MaxTokens: 512,
		WorkerLLMURL: "https://worker.example/v1", WorkerAPIKey: "worker-key", WorkerModel: "worker-model", WorkerMaxTokens: 512,
		DispatchLLMURL: "https://dispatch.example/v1", DispatchAPIKey: "dispatch-key", DispatchModel: "dispatch-model", DispatchMaxTokens: 1024,
		MaxIterations:         1,
		DispatchPlanPrompt:    "PHASE:PLAN {{graph_json}}",
		WorkerPrompt:          "goal={{goal}} fact={{fact}} id={{fact_id}} action={{action}}",
		DispatchSummaryPrompt: "PHASE:SUMMARIZE {{worker_output}} {{chosen_action_json}}",
		WorkerCommand:         "external-worker --prompt {{prompt}}",
	})
	if err != nil {
		t.Fatalf("configure dispatcher: %v", err)
	}

	var calls int
	agent := AgentFunc(func(_ context.Context, settings domain.Settings, prompt string, output io.Writer) error {
		calls++
		switch {
		case strings.Contains(prompt, "PHASE:PLAN"):
			if settings.Model != "dispatch-model" || settings.WorkerCommand != "" || !strings.Contains(prompt, `"goal":{"id":`) {
				t.Errorf("plan call config/prompt = %+v, %s", settings, prompt)
			}
			return writeString(output, fmt.Sprintf(`{"from_fact_id":%d,"action":"Run a user acceptance test","rationale":"The prototype is ready"}`, graph.Facts[0].ID))
		case strings.Contains(prompt, "PHASE:SUMMARIZE"):
			if settings.Model != "dispatch-model" || settings.WorkerCommand != "" || !strings.Contains(prompt, "The test passed") {
				t.Errorf("summary call config/prompt = %+v, %s", settings, prompt)
			}
			return writeString(output, `{"facts":["Users approved the prototype"],"steps":[{"from_fact_index":1,"to_goal":true,"description":"Approval satisfies launch criteria"}],"goal_reached":true}`)
		default:
			if settings.Model != "worker-model" || settings.WorkerCommand != "external-worker --prompt {{prompt}}" || !strings.Contains(prompt, "goal=Launch the product") || !strings.Contains(prompt, "action=Run a user acceptance test") {
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

func TestDispatchHonorsConfiguredIterationLimit(t *testing.T) {
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
		Name: "iteration limit", Goal: "finish", Facts: []string{"start"},
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	_, err = service.UpdateSettings(context.Background(), api.UpdateSettingsInput{
		LLMURL: "https://example.com/v1", Model: "model", MaxTokens: 100, MaxIterations: 2,
	})
	if err != nil {
		t.Fatalf("configure iteration limit: %v", err)
	}
	var calls, summaries int
	agent := AgentFunc(func(_ context.Context, _ domain.Settings, prompt string, output io.Writer) error {
		calls++
		switch {
		case strings.Contains(prompt, "PHASE:PLAN"):
			return writeString(output, fmt.Sprintf(`{"from_fact_id":%d,"action":"continue","rationale":"progress"}`, graph.Facts[0].ID))
		case strings.Contains(prompt, "PHASE:SUMMARIZE"):
			summaries++
			return writeString(output, fmt.Sprintf(`{"facts":["progress %d"],"steps":[],"goal_reached":false}`, summaries))
		default:
			return writeString(output, "worked")
		}
	})
	if err := New(service, agent).dispatchProject(context.Background(), graph.ID); err != nil {
		t.Fatalf("dispatch project: %v", err)
	}
	if calls != 6 || summaries != 2 {
		t.Fatalf("dispatch made %d calls with %d summaries, want 6 and 2", calls, summaries)
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

func TestRenderPromptDoesNotExpandVariablesInsideReplacements(t *testing.T) {
	got := renderPrompt("value={{fact}} action={{action}}", "", map[string]string{
		"fact":   "literal {{action}}",
		"action": "run",
	})
	if got != "value=literal {{action}} action=run" {
		t.Fatalf("rendered prompt = %q", got)
	}
}

func writeString(output io.Writer, content string) error {
	_, err := io.WriteString(output, content)
	return err
}
