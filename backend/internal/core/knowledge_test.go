package core

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"hotmouse/backend/internal/knowledge"
)

func TestExecutionKnowledgeUsesPrerequisitesWithoutImportingFacts(t *testing.T) {
	db := testDB(t)
	p := strategyProject(t, db)
	source := strategyStep(t, db, p, nil)
	result, err := db.ConcludeStep(t.Context(), p, source.ID, Conclude{Worker: "test", Description: "RSA crypto parameters extracted", Result: &ExecutionResult{Type: "progress", Detail: "private saved parameter evidence"}})
	if err != nil {
		t.Fatal(err)
	}
	step, err := db.CreateStep(t.Context(), p, CreateStep{From: []string{result.Fact.ID}, Description: "continue evaluating the confirmed algorithm", Creator: "test"})
	if err != nil {
		t.Fatal(err)
	}
	unrelated := strategyStep(t, db, p, nil)
	if _, err := db.ConcludeStep(t.Context(), p, unrelated.ID, Conclude{Worker: "test", Description: "Unrelated SSRF service fingerprint", Result: &ExecutionResult{Type: "progress"}}); err != nil {
		t.Fatal(err)
	}
	context, err := db.ModelContext(t.Context(), p, "execute", step.ID)
	if err != nil {
		t.Fatal(err)
	}
	var view struct {
		Knowledge []knowledge.Summary `yaml:"related_knowledge"`
	}
	if err := yaml.Unmarshal([]byte(context), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Knowledge) == 0 || len(view.Knowledge) > 3 || view.Knowledge[0].ID != "kb041" {
		t.Fatalf("prerequisite technique was crowded out: %+v", view.Knowledge)
	}
	catalog, _ := knowledge.Load()
	entry, _ := catalog.Get("kb041")
	if strings.Contains(context, entry.Detail) || strings.Contains(context, "private saved parameter evidence") {
		t.Fatal("verbose reference/evidence leaked into compact context")
	}
	var facts, experiences int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM facts WHERE project_id=?`, p).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM experiences`).Scan(&experiences); err != nil {
		t.Fatal(err)
	}
	if facts != 3 || experiences != 0 {
		t.Fatalf("bundled knowledge was imported as project evidence: facts=%d experiences=%d", facts, experiences)
	}
}
