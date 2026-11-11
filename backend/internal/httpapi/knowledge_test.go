package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kataras/iris/v12"
	"hotmouse/backend/internal/core"
	"hotmouse/backend/internal/knowledge"
)

func TestKnowledgeAPISearchDetailAndAuthentication(t *testing.T) {
	t.Setenv("HOTMOUSE_AUTH_REQUIRED", "0")
	db, err := core.Open(filepath.Join(t.TempDir(), "knowledge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.SQL.Close() })
	app := iris.New()
	app.Logger().SetLevel("disable")
	Register(app, New(db))
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	get := func(path string, status int, value any) {
		t.Helper()
		response := httptest.NewRecorder()
		app.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
		if response.Code != status {
			t.Fatalf("%s: status=%d body=%s", path, response.Code, response.Body.String())
		}
		if value != nil {
			if err := json.Unmarshal(response.Body.Bytes(), value); err != nil {
				t.Fatal(err)
			}
		}
	}
	var entries []knowledge.Summary
	get("/api/knowledge/entries", 200, &entries)
	if len(entries) != 56 {
		t.Fatal("API lost knowledge entries")
	}
	get("/api/knowledge/entries?q=SSRF&tag=ssrf&limit=2", 200, &entries)
	if len(entries) != 1 || entries[0].ID != "kb006" {
		t.Fatalf("API search ignored technique or tag: %+v", entries)
	}
	var entry knowledge.Entry
	get("/api/knowledge/entries/kb006", 200, &entry)
	if entry.Detail == "" || len(entry.Commands) == 0 {
		t.Fatal("API full entry missing")
	}
	var info knowledge.Info
	get("/api/knowledge", 200, &info)
	if info.SkillCount != 13 || info.EntryCount != 56 {
		t.Fatal("API bundle info missing")
	}
	var skill knowledge.Skill
	get("/api/knowledge/skills/hotmouse-ctf", 200, &skill)
	if skill.Content == "" {
		t.Fatal("API skill body missing")
	}
	get("/api/knowledge/skills/web-exploit", 200, &skill)
	if skill.Name != "web-exploit" || !strings.Contains(skill.Content, "name: web-exploit") {
		t.Fatal("API imported skill missing")
	}
	get("/api/knowledge/entries/kb999", 404, nil)
	get("/api/knowledge/skills/unknown", 404, nil)
	get("/api/knowledge/entries?limit=garbage", 400, nil)
	get("/api/knowledge/entries?limit=0", 400, nil)
	t.Setenv("HOTMOUSE_AUTH_REQUIRED", "1")
	for _, path := range []string{"/api/knowledge", "/api/knowledge/entries", "/api/knowledge/skills"} {
		get(path, 401, nil)
	}
}
