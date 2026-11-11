package core

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestFlagCheckpointLeaseProvenanceAndReplay(t *testing.T) {
	db := testDB(t)
	project := strategyProject(t, db)
	if _, err := db.SQL.Exec(`INSERT INTO platform_projects(project_id,platform,scope_id,challenge_id,challenge_json,delivery_json) VALUES(?,'test','scope','task','{}','{}')`, project); err != nil {
		t.Fatal(err)
	}
	step := strategyStep(t, db, project, nil)
	if _, err := db.HeartbeatStep(t.Context(), project, step.ID, "solver"); err != nil {
		t.Fatal(err)
	}
	req := FlagCheckpoint{Value: "flag{raw}", SourcePath: "/raw/snapshot", SourceSHA256: strings.Repeat("a", 64), FormatNote: "format and two-flag requirement from task description"}
	if _, err := db.CheckpointFlag(t.Context(), project, step.ID, "other", req); err == nil {
		t.Fatal("another worker could checkpoint for this step")
	}
	missing := req
	missing.SourcePath = ""
	if _, err := db.CheckpointFlag(t.Context(), project, step.ID, "solver", missing); err == nil {
		t.Fatal("checkpoint without provenance was accepted")
	}
	first, err := db.CheckpointFlag(t.Context(), project, step.ID, "solver", req)
	if err != nil {
		t.Fatal(err)
	}
	again, err := db.CheckpointFlag(t.Context(), project, step.ID, "solver", req)
	if err != nil || first.ID != again.ID || first.FactID != again.FactID || first.ChallengeID != "task" {
		t.Fatalf("checkpoint replay duplicated source or flag: %+v %+v %v", first, again, err)
	}
	detail, _ := db.GetProject(t.Context(), project)
	if detail.Steps[0].To != nil || len(detail.Facts) != 2 || detail.Project.Status != "active" {
		t.Fatal("checkpoint closed execution, lost provenance or claimed success")
	}
	if _, err := db.ConcludeStep(t.Context(), project, step.ID, Conclude{Worker: "solver", Description: "solver notes complete", Result: &ExecutionResult{Type: "progress"}, Finding: &CreateFinding{Kind: "flag", Title: "flag", Description: "raw output verified", Data: map[string]any{"value": req.Value}}}); err != nil {
		t.Fatal(err)
	}
	flags, _ := db.ListFlags(t.Context(), project)
	if len(flags) != 1 || flags[0].FactID != first.FactID {
		t.Fatal("final finding without challenge_id duplicated the early checkpoint")
	}
	if _, err := db.CheckpointFlag(t.Context(), project, step.ID, "solver", req); err == nil {
		t.Fatal("closed step accepted a new execution checkpoint")
	}
}

func TestModelContextSharesPlatformRequirementsAndWorkingState(t *testing.T) {
	db := testDB(t)
	binding := &PlatformBinding{Platform: "test", ScopeID: "scope", ChallengeID: "multi", Challenge: json.RawMessage(`{"id":"multi","flag_count":3,"correct_flag_count":1,"metadata":{"flag_format":"CTF{...}"}}`), Delivery: json.RawMessage(`{"challenge_id":"multi","addresses":["http://10.0.0.1/"],"attachments":[{"name":"task.zip","path":"/saved/task.zip"}]}`)}
	p, err := db.CreateProject(t.Context(), CreateProject{Title: "multi", Origin: "local inputs", Goal: "all flags accepted", PlatformBinding: binding})
	if err != nil {
		t.Fatal(err)
	}
	project := p.Project.ID
	step := strategyStep(t, db, project, nil)
	_, err = db.ConcludeStep(t.Context(), project, step.ID, Conclude{Worker: "test", Description: "JWT baseline saved", Result: &ExecutionResult{Type: "progress", Evidence: map[string]any{"artifacts": []string{"/saved/task.zip"}, "protocol": "JWT with verified claims", "python": "/api/venv/bin/python", "remaining_hypothesis": "validate claims confusion", "knowledge_refs": []string{"kb012"}, "raw_unrelated": strings.Repeat("excluded", 5000)}}})
	if err != nil {
		t.Fatal(err)
	}
	flag, err := db.CaptureFlag(t.Context(), project, CaptureFlag{Value: "CTF{rejected}", FactID: "origin", ChallengeID: "multi"})
	if err != nil {
		t.Fatal(err)
	}
	attempt, _ := db.BeginFlagAttempt(t.Context(), project, flag.ID)
	_, _ = db.ResolveFlagAttempt(t.Context(), project, flag.ID, attempt.ID, ResolveFlagAttempt{Status: "rejected", Response: `{"correct":false,"correct_flag_count":1,"total_flag_count":3}`})
	text, err := db.ModelContext(t.Context(), project, "decide", "")
	if err != nil {
		t.Fatal(err)
	}
	var view modelContext
	if err := yaml.Unmarshal([]byte(text), &view); err != nil {
		t.Fatal(err)
	}
	if view.Platform.Challenge.FlagCount != 3 || view.Platform.Challenge.CorrectFlagCount != 1 || view.Platform.Delivery.Attachments[0].Path != "/saved/task.zip" || len(view.WorkingState) != 1 || view.WorkingState[0].Evidence["python"] != "/api/venv/bin/python" || view.Flags[0].LastResponse == "" || strings.Contains(text, "raw_unrelated") {
		t.Fatalf("missing or excessive planning context: %s", text)
	}
}
