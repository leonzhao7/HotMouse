package core

import (
	"context"
	"database/sql"
	"fmt"
)

const DefaultProjectBudgetSeconds = 1200

// Project budgets snapshot the configured default at creation and account for
// all routes together. Legacy route columns remain only for old database reads.
func migrateProjectBudget(db *sql.DB) error {
	if err := addColumnIfMissing(db, "settings", "default_project_budget_seconds", fmt.Sprintf("INTEGER NOT NULL DEFAULT %d CHECK(default_project_budget_seconds>=0)", DefaultProjectBudgetSeconds)); err != nil {
		return err
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('projects') WHERE name='budget_seconds'`).Scan(&count); err != nil {
		return err
	}
	missingBudget := count == 0
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('projects') WHERE name='budget_baseline_ms'`).Scan(&count); err != nil {
		return err
	}
	missingBaseline := count == 0
	if !missingBudget && !missingBaseline {
		return nil
	}
	t, err := db.Begin()
	if err != nil {
		return err
	}
	defer t.Rollback()
	if missingBudget {
		if _, err := t.Exec(fmt.Sprintf(`ALTER TABLE projects ADD COLUMN budget_seconds INTEGER NOT NULL DEFAULT %d CHECK(budget_seconds>=0)`, DefaultProjectBudgetSeconds)); err != nil {
			return err
		}
		// For pre-budget projects, preserve the main route's configured duration.
		// No existing route, investment, stop-loss decision or status is rewritten.
		if _, err := t.Exec(`UPDATE projects SET budget_seconds=COALESCE((SELECT budget_seconds FROM routes WHERE project_id=projects.id ORDER BY CASE WHEN id='route_g001' THEN 0 ELSE 1 END,created_at,id LIMIT 1),?)`, DefaultProjectBudgetSeconds); err != nil {
			return err
		}
	}
	if missingBaseline {
		if _, err := t.Exec(`ALTER TABLE projects ADD COLUMN budget_baseline_ms INTEGER NOT NULL DEFAULT 0 CHECK(budget_baseline_ms>=0)`); err != nil {
			return err
		}
		if _, err := t.Exec(`UPDATE projects SET budget_baseline_ms=COALESCE((SELECT SUM(budget_baseline_ms) FROM routes WHERE project_id=projects.id),0)`); err != nil {
			return err
		}
	}
	return t.Commit()
}

func enforceProjectBudgetTx(t *sql.Tx, project string) error {
	p, err := projectFrom(t.QueryRow(`SELECT `+projectSelectColumns+` FROM projects WHERE id=?`, project))
	if err != nil {
		return err
	}
	if p.Status != "active" || p.BudgetSeconds == 0 || p.BudgetSpentMilliseconds/1000 < int64(p.BudgetSeconds) {
		return nil
	}
	_, err = failActiveProjectTx(t, project, fmt.Sprintf("项目本轮时间预算耗尽（budget_exhausted）：所有路线合计投入 %d 毫秒，项目预算 %d 秒", p.BudgetSpentMilliseconds, p.BudgetSeconds), "Project time budget exhausted")
	return err
}

func (d *DB) SetProjectBudget(ctx context.Context, project string, seconds int) (Project, error) {
	if seconds < 0 {
		return Project{}, fmt.Errorf("project budget_seconds must be nonnegative")
	}
	var out Project
	err := tx(ctx, d, func(t *sql.Tx) error {
		if _, err := projectFrom(t.QueryRow(`SELECT `+projectSelectColumns+` FROM projects WHERE id=?`, project)); err != nil {
			return err
		}
		if _, err := t.Exec(`UPDATE projects SET budget_seconds=? WHERE id=?`, seconds, project); err != nil {
			return err
		}
		if err := enforceProjectBudgetTx(t, project); err != nil {
			return err
		}
		var err error
		out, err = projectFrom(t.QueryRow(`SELECT `+projectSelectColumns+` FROM projects WHERE id=?`, project))
		return err
	})
	return out, err
}
