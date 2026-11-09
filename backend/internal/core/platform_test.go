package core

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestPlatformBindingTransactionAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.SQL.Close() })
	binding := &PlatformBinding{Platform: "tsec", ScopeID: "scope-one", ChallengeID: "web", Challenge: json.RawMessage(`{"id":"web"}`), Delivery: json.RawMessage(`{"addresses":["10.0.0.1:80"]}`)}
	req := CreateProject{Title: "web", Origin: "target", Goal: "capture flags", PlatformBinding: binding, Hints: []CreateHint{{Content: "invalid missing creator"}}}
	if _, err := db.CreateProject(t.Context(), req); err == nil {
		t.Fatal("invalid hint accepted")
	}
	if project, err := db.FindPlatformProject(t.Context(), "tsec", "scope-one", "web"); err != nil || project != "" {
		t.Fatalf("binding survived rollback: %q %v", project, err)
	}
	req.Hints = nil
	first, err := db.CreateProject(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.CreateProject(t.Context(), req)
	if err != nil || first.Project.ID != second.Project.ID {
		t.Fatalf("non-idempotent import: %v %#v", err, second)
	}
	binding.ScopeID = "scope-two"
	other, err := db.CreateProject(t.Context(), req)
	if err != nil || other.Project.ID == first.Project.ID {
		t.Fatalf("task scope reused project: %v", err)
	}
	db.SQL.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := db.GetProject(t.Context(), first.Project.ID)
	if err != nil || reloaded.Platform == nil || reloaded.Platform.ScopeID != "scope-one" {
		t.Fatalf("binding lost after restart: %#v %v", reloaded.Platform, err)
	}
	if _, err := db.SQL.Exec(`DELETE FROM projects WHERE id=?`, first.Project.ID); err != nil {
		t.Fatal(err)
	}
	if project, err := db.FindPlatformProject(t.Context(), "tsec", "scope-one", "web"); err != nil || project != "" {
		t.Fatalf("orphan binding: %q %v", project, err)
	}
}
