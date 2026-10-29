package storage

import (
	"fmt"
	"strings"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"hotmouse/backend/internal/domain"
)

func Open(dsn string) (*gorm.DB, error) {
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	dsn += separator + "_foreign_keys=on&_recursive_triggers=on&_busy_timeout=5000&_txlock=immediate"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{TranslateError: true})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := migrateProjectState(tx); err != nil {
			return err
		}
		if err := tx.AutoMigrate(&domain.Project{}, &domain.Node{}, &domain.Step{}, &domain.Settings{}, &domain.LLMLog{}); err != nil {
			return err
		}
		for _, statement := range graphConstraints {
			if err := tx.Exec(statement).Error; err != nil {
				return fmt.Errorf("install graph constraint: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return db, nil
}

func migrateProjectState(tx *gorm.DB) error {
	var schema string
	if err := tx.Raw("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'projects'").Scan(&schema).Error; err != nil {
		return err
	}
	schemaLower := strings.ToLower(schema)
	if schema == "" || (strings.Contains(schemaLower, "aborted") && strings.Contains(schemaLower, "aborted_at")) {
		return nil
	}

	// Triggers on nodes and steps may be rewritten by SQLite to point at the
	// renamed legacy table. Drop every graph trigger before the table swap and
	// recreate them from graphConstraints after migration.
	for _, trigger := range []string{
		"nodes_project_guard", "steps_endpoint_guard", "nodes_no_update", "nodes_no_delete",
		"steps_no_update", "steps_no_delete", "projects_no_delete", "projects_start_open",
		"projects_completion_guard",
	} {
		if err := tx.Exec("DROP TRIGGER IF EXISTS " + trigger).Error; err != nil {
			return fmt.Errorf("drop project trigger %s: %w", trigger, err)
		}
	}
	if err := tx.Exec("ALTER TABLE projects RENAME TO projects_legacy").Error; err != nil {
		return fmt.Errorf("rename legacy projects table: %w", err)
	}
	if err := tx.Exec(`CREATE TABLE projects (
		id integer PRIMARY KEY AUTOINCREMENT,
		name text NOT NULL,
		description text,
		status text NOT NULL,
		created_at datetime,
		completed_at datetime,
		aborted_at datetime,
		CONSTRAINT project_name_required CHECK (length(trim(name)) > 0),
		CONSTRAINT project_state CHECK (
			(status = 'open' AND completed_at IS NULL AND aborted_at IS NULL) OR
			(status = 'completed' AND completed_at IS NOT NULL AND aborted_at IS NULL) OR
			(status = 'aborted' AND completed_at IS NULL AND aborted_at IS NOT NULL)
		)
	)`).Error; err != nil {
		return fmt.Errorf("create projects table: %w", err)
	}
	if err := tx.Exec(`INSERT INTO projects (id, name, description, status, created_at, completed_at)
		SELECT id, name, description, status, created_at, completed_at FROM projects_legacy`).Error; err != nil {
		return fmt.Errorf("copy legacy projects: %w", err)
	}
	if err := tx.Exec("DROP TABLE projects_legacy").Error; err != nil {
		return fmt.Errorf("drop legacy projects table: %w", err)
	}
	return nil
}

var graphConstraints = []string{
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_one_goal_per_project
	 ON nodes(project_id) WHERE kind = 'goal'`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_one_step_per_pair
	 ON steps(project_id, min(from_node_id, to_node_id), max(from_node_id, to_node_id))`,
	`CREATE TRIGGER IF NOT EXISTS nodes_project_guard BEFORE INSERT ON nodes
	 WHEN NOT EXISTS (SELECT 1 FROM projects WHERE id = NEW.project_id AND status = 'open')
	 BEGIN SELECT RAISE(ABORT, 'node requires an open project'); END`,
	`CREATE TRIGGER IF NOT EXISTS steps_endpoint_guard BEFORE INSERT ON steps
	 WHEN NOT EXISTS (SELECT 1 FROM projects WHERE id = NEW.project_id AND status = 'open')
	 OR NOT EXISTS (SELECT 1 FROM nodes WHERE id = NEW.from_node_id AND project_id = NEW.project_id AND kind = 'fact')
	 OR NOT EXISTS (SELECT 1 FROM nodes WHERE id = NEW.to_node_id AND project_id = NEW.project_id)
	 BEGIN SELECT RAISE(ABORT, 'invalid step endpoints'); END`,
	`CREATE TRIGGER IF NOT EXISTS nodes_no_update BEFORE UPDATE ON nodes
	 BEGIN SELECT RAISE(ABORT, 'nodes are append-only'); END`,
	`CREATE TRIGGER IF NOT EXISTS nodes_no_delete BEFORE DELETE ON nodes
	 BEGIN SELECT RAISE(ABORT, 'nodes are append-only'); END`,
	`CREATE TRIGGER IF NOT EXISTS steps_no_update BEFORE UPDATE ON steps
	 BEGIN SELECT RAISE(ABORT, 'steps are append-only'); END`,
	`CREATE TRIGGER IF NOT EXISTS steps_no_delete BEFORE DELETE ON steps
	 BEGIN SELECT RAISE(ABORT, 'steps are append-only'); END`,
	`CREATE TRIGGER IF NOT EXISTS projects_no_delete BEFORE DELETE ON projects
	 BEGIN SELECT RAISE(ABORT, 'projects cannot be deleted'); END`,
	`CREATE TRIGGER IF NOT EXISTS projects_start_open BEFORE INSERT ON projects
	 WHEN NEW.status <> 'open'
	 BEGIN SELECT RAISE(ABORT, 'projects must start open'); END`,
	`DROP TRIGGER IF EXISTS projects_completion_guard`,
	`CREATE TRIGGER IF NOT EXISTS projects_completion_guard BEFORE UPDATE OF status, completed_at, aborted_at ON projects
	 WHEN OLD.status IN ('completed', 'aborted')
	 OR (NEW.status = 'completed' AND (NEW.completed_at IS NULL OR NEW.aborted_at IS NOT NULL
	    OR NOT EXISTS (SELECT 1 FROM steps JOIN nodes ON nodes.id = steps.to_node_id
	                   WHERE steps.project_id = NEW.id AND nodes.project_id = NEW.id AND nodes.kind = 'goal')))
	 OR (NEW.status = 'aborted' AND (NEW.aborted_at IS NULL OR NEW.completed_at IS NOT NULL))
	 OR (NEW.status = 'open' AND (NEW.completed_at IS NOT NULL OR NEW.aborted_at IS NOT NULL))
	 BEGIN SELECT RAISE(ABORT, 'invalid project state transition'); END`,
}
