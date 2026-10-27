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
		if err := tx.AutoMigrate(&domain.Project{}, &domain.Node{}, &domain.Step{}, &domain.Settings{}); err != nil {
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
	`CREATE TRIGGER IF NOT EXISTS projects_completion_guard BEFORE UPDATE OF status, completed_at ON projects
	 WHEN OLD.status = 'completed' OR NEW.status <> 'completed' OR NEW.completed_at IS NULL
	 OR NOT EXISTS (SELECT 1 FROM steps JOIN nodes ON nodes.id = steps.to_node_id
	                WHERE steps.project_id = NEW.id AND nodes.project_id = NEW.id AND nodes.kind = 'goal')
	 BEGIN SELECT RAISE(ABORT, 'completion requires an incoming goal step'); END`,
}
