package storage

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"hotmouse/backend/internal/domain"
)

func openTestDatabase(test *testing.T, dsn string) *gorm.DB {
	test.Helper()
	db, err := Open(dsn)
	if err != nil {
		test.Fatalf("open database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		test.Fatal(err)
	}
	test.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func seedGraph(test *testing.T, db *gorm.DB, name string) domain.Graph {
	test.Helper()
	graph := domain.Graph{
		Project: domain.Project{Name: name, Status: domain.ProjectOpen},
		Goal:    domain.Node{Kind: domain.NodeKindGoal, Content: "goal"},
		Facts: []domain.Node{
			{Kind: domain.NodeKindFact, Content: "first"},
			{Kind: domain.NodeKindFact, Content: "second"},
		},
	}
	if err := db.Create(&graph.Project).Error; err != nil {
		test.Fatal(err)
	}
	graph.Goal.ProjectID = graph.ID
	if err := db.Create(&graph.Goal).Error; err != nil {
		test.Fatal(err)
	}
	for index := range graph.Facts {
		graph.Facts[index].ProjectID = graph.ID
		if err := db.Create(&graph.Facts[index]).Error; err != nil {
			test.Fatal(err)
		}
	}
	return graph
}

func TestDatabaseEnforcesAppendOnlyGraph(test *testing.T) {
	db := openTestDatabase(test, ":memory:")
	graph := seedGraph(test, db, "immutable")
	step := domain.Step{ProjectID: graph.ID, FromNodeID: graph.Facts[0].ID, ToNodeID: graph.Goal.ID}
	if err := db.Create(&step).Error; err != nil {
		test.Fatal(err)
	}
	cases := []struct {
		name  string
		query string
		args  []any
	}{
		{"update fact", "UPDATE nodes SET content = 'changed' WHERE id = ?", []any{graph.Facts[0].ID}},
		{"update goal", "UPDATE nodes SET content = 'changed' WHERE id = ?", []any{graph.Goal.ID}},
		{"move node", "UPDATE nodes SET project_id = 999 WHERE id = ?", []any{graph.Facts[0].ID}},
		{"delete fact", "DELETE FROM nodes WHERE id = ?", []any{graph.Facts[0].ID}},
		{"delete goal", "DELETE FROM nodes WHERE id = ?", []any{graph.Goal.ID}},
		{"replace fact", "INSERT OR REPLACE INTO nodes(id, project_id, kind, content) VALUES(?, ?, 'fact', 'changed')", []any{graph.Facts[0].ID, graph.ID}},
		{"replace goal", "INSERT OR REPLACE INTO nodes(id, project_id, kind, content) VALUES(?, ?, 'goal', 'changed')", []any{graph.Goal.ID, graph.ID}},
		{"upsert fact", "INSERT INTO nodes(id, project_id, kind, content) VALUES(?, ?, 'fact', 'changed') ON CONFLICT(id) DO UPDATE SET content = excluded.content", []any{graph.Facts[0].ID, graph.ID}},
		{"update step", "UPDATE steps SET to_node_id = ? WHERE id = ?", []any{graph.Facts[1].ID, step.ID}},
		{"delete step", "DELETE FROM steps WHERE id = ?", []any{step.ID}},
		{"replace step", "INSERT OR REPLACE INTO steps(id, project_id, from_node_id, to_node_id) VALUES(?, ?, ?, ?)", []any{step.ID, graph.ID, graph.Facts[0].ID, graph.Goal.ID}},
		{"delete project", "DELETE FROM projects WHERE id = ?", []any{graph.ID}},
	}
	for _, entry := range cases {
		test.Run(entry.name, func(test *testing.T) {
			if err := db.Exec(entry.query, entry.args...).Error; err == nil {
				test.Fatal("database accepted mutation of append-only data")
			}
		})
	}
	var fact domain.Node
	if err := db.First(&fact, graph.Facts[0].ID).Error; err != nil || fact.Content != graph.Facts[0].Content {
		test.Fatalf("fact changed: %+v, %v", fact, err)
	}
	var storedStep domain.Step
	if err := db.First(&storedStep, step.ID).Error; err != nil || storedStep.ToNodeID != graph.Goal.ID {
		test.Fatalf("step changed: %+v, %v", storedStep, err)
	}
}

func TestDatabaseGuardsNodeKindsOwnershipAndPairUniqueness(test *testing.T) {
	db := openTestDatabase(test, ":memory:")
	graph := seedGraph(test, db, "graph")
	other := seedGraph(test, db, "other")
	duplicateGoal := domain.Node{ProjectID: graph.ID, Kind: domain.NodeKindGoal, Content: "second goal"}
	if err := db.Create(&duplicateGoal).Error; !errors.Is(err, gorm.ErrDuplicatedKey) {
		test.Fatalf("second goal error = %v; want duplicate key", err)
	}
	invalidNodes := []domain.Node{
		{ProjectID: 999, Kind: domain.NodeKindFact, Content: "orphan"},
		{ProjectID: graph.ID, Kind: "unknown", Content: "invalid"},
		{ProjectID: graph.ID, Kind: domain.NodeKindFact, Content: " "},
	}
	for index := range invalidNodes {
		if err := db.Create(&invalidNodes[index]).Error; err == nil {
			test.Fatalf("accepted invalid node: %+v", invalidNodes[index])
		}
	}
	invalidSteps := []domain.Step{
		{ProjectID: graph.ID, FromNodeID: graph.Goal.ID, ToNodeID: graph.Facts[0].ID},
		{ProjectID: graph.ID, FromNodeID: graph.Facts[0].ID, ToNodeID: graph.Facts[0].ID},
		{ProjectID: graph.ID, FromNodeID: other.Facts[0].ID, ToNodeID: graph.Goal.ID},
		{ProjectID: graph.ID, FromNodeID: graph.Facts[0].ID, ToNodeID: other.Goal.ID},
		{ProjectID: other.ID, FromNodeID: graph.Facts[0].ID, ToNodeID: graph.Goal.ID},
		{ProjectID: graph.ID, FromNodeID: graph.Facts[0].ID, ToNodeID: 999},
	}
	for index := range invalidSteps {
		if err := db.Create(&invalidSteps[index]).Error; err == nil {
			test.Fatalf("accepted invalid step: %+v", invalidSteps[index])
		}
	}
	step := domain.Step{ProjectID: graph.ID, FromNodeID: graph.Facts[0].ID, ToNodeID: graph.Facts[1].ID}
	if err := db.Create(&step).Error; err != nil {
		test.Fatal(err)
	}
	for _, pair := range [][2]uint{{step.FromNodeID, step.ToNodeID}, {step.ToNodeID, step.FromNodeID}} {
		duplicate := domain.Step{ProjectID: graph.ID, FromNodeID: pair[0], ToNodeID: pair[1]}
		if err := db.Create(&duplicate).Error; !errors.Is(err, gorm.ErrDuplicatedKey) {
			test.Fatalf("duplicate pair error = %v; want duplicate key", err)
		}
	}
}

func TestDatabaseCompletionRequiresGoalAndClosesGraph(test *testing.T) {
	db := openTestDatabase(test, ":memory:")
	graph := seedGraph(test, db, "completion")
	completedAt := time.Now().UTC()
	completion := map[string]any{"status": domain.ProjectCompleted, "completed_at": completedAt}
	if err := db.Model(&domain.Project{}).Where("id = ?", graph.ID).Updates(completion).Error; err == nil {
		test.Fatal("completed graph without a goal step")
	}
	step := domain.Step{ProjectID: graph.ID, FromNodeID: graph.Facts[0].ID, ToNodeID: graph.Goal.ID}
	if err := db.Create(&step).Error; err != nil {
		test.Fatal(err)
	}
	if err := db.Model(&domain.Project{}).Where("id = ?", graph.ID).Updates(completion).Error; err != nil {
		test.Fatalf("complete with goal step: %v", err)
	}
	fact := domain.Node{ProjectID: graph.ID, Kind: domain.NodeKindFact, Content: "late fact"}
	if err := db.Create(&fact).Error; err == nil {
		test.Fatal("accepted fact after completion")
	}
	lateStep := domain.Step{ProjectID: graph.ID, FromNodeID: graph.Facts[1].ID, ToNodeID: graph.Goal.ID}
	if err := db.Create(&lateStep).Error; err == nil {
		test.Fatal("accepted step after completion")
	}
	if err := db.Model(&domain.Project{}).Where("id = ?", graph.ID).Updates(map[string]any{"status": domain.ProjectOpen, "completed_at": nil}).Error; err == nil {
		test.Fatal("reopened completed project")
	}
}

func TestSQLitePersistsGraphAndConstraintsAcrossReopen(test *testing.T) {
	path := filepath.Join(test.TempDir(), "hotmouse.db")
	db := openTestDatabase(test, path)
	graph := seedGraph(test, db, "persistent")
	step := domain.Step{ProjectID: graph.ID, FromNodeID: graph.Facts[0].ID, ToNodeID: graph.Goal.ID, Description: "persisted note"}
	if err := db.Create(&step).Error; err != nil {
		test.Fatal(err)
	}
	if err := db.Model(&domain.Project{}).Where("id = ?", graph.ID).Updates(map[string]any{"status": domain.ProjectCompleted, "completed_at": time.Now().UTC()}).Error; err != nil {
		test.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		test.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		test.Fatal(err)
	}
	reopened := openTestDatabase(test, path)
	var project domain.Project
	if err := reopened.First(&project, graph.ID).Error; err != nil || project.Status != domain.ProjectCompleted || project.CompletedAt == nil {
		test.Fatalf("reopened project = %+v, %v", project, err)
	}
	var nodes []domain.Node
	if err := reopened.Where("project_id = ?", graph.ID).Find(&nodes).Error; err != nil || len(nodes) != 3 {
		test.Fatalf("reopened nodes = %+v, %v", nodes, err)
	}
	var persisted domain.Step
	if err := reopened.First(&persisted, step.ID).Error; err != nil || persisted.FromNodeID != step.FromNodeID || persisted.ToNodeID != step.ToNodeID || persisted.Description != step.Description {
		test.Fatalf("reopened step = %+v, %v", persisted, err)
	}
	if err := reopened.Exec("UPDATE nodes SET content = 'changed' WHERE id = ?", graph.Goal.ID).Error; err == nil {
		test.Fatal("append-only guard was lost on reopen")
	}
}

func TestOpenMigratesLegacyStepsTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	legacy.Exec(`CREATE TABLE steps (
		id integer PRIMARY KEY AUTOINCREMENT,
		project_id integer NOT NULL,
		from_node_id integer NOT NULL,
		to_node_id integer NOT NULL,
		created_at datetime
	)`)
	if err := legacy.Exec(`INSERT INTO steps(project_id, from_node_id, to_node_id, created_at) VALUES(1, 2, 3, ?)`, time.Now().UTC()).Error; err != nil {
		t.Fatal(err)
	}
	legacySQL, err := legacy.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := legacySQL.Close(); err != nil {
		t.Fatal(err)
	}

	db := openTestDatabase(t, path)
	var migrated domain.Step
	if err := db.First(&migrated, 1).Error; err != nil {
		t.Fatalf("read migrated step: %v", err)
	}
	if migrated.Description != "" {
		t.Fatalf("legacy step description = %q, want empty", migrated.Description)
	}
	if err := db.Model(&migrated).Update("description", "after migration").Error; err == nil {
		t.Fatal("description update bypassed the append-only trigger")
	}
}

func TestOpenInvalidPath(test *testing.T) {
	if _, err := Open(filepath.Join(test.TempDir(), "missing", "hotmouse.db")); err == nil {
		test.Fatal("expected invalid database path to fail")
	}
}
