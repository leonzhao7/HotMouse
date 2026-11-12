package core

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const competitionSchema = `
CREATE TABLE IF NOT EXISTS competition_runs (
 id TEXT PRIMARY KEY, platform TEXT NOT NULL, scope_id TEXT NOT NULL,
 rules TEXT NOT NULL, rules_summary TEXT NOT NULL DEFAULT '',
 concurrency INTEGER NOT NULL, max_workers INTEGER NOT NULL,
 challenge_timeout_seconds INTEGER NOT NULL,
 status TEXT NOT NULL DEFAULT 'planning' CHECK(status IN ('planning','running','stopping','completed','stopped','failed')),
 last_error TEXT NOT NULL DEFAULT '', planning_attempts INTEGER NOT NULL DEFAULT 0,
 retry_after TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, finished_at TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS competition_active_task ON competition_runs(platform,scope_id) WHERE status IN ('planning','running','stopping');
CREATE TABLE IF NOT EXISTS competition_items (
 run_id TEXT NOT NULL REFERENCES competition_runs(id) ON DELETE CASCADE,
 challenge_id TEXT NOT NULL, ordinal INTEGER NOT NULL,
 project_id TEXT REFERENCES projects(id) ON DELETE SET NULL,
 status TEXT NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','starting','running','closing','succeeded','failed','skipped')),
 outcome TEXT NOT NULL DEFAULT '', last_error TEXT NOT NULL DEFAULT '',
 started_at TEXT, finished_at TEXT, retry_after TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(run_id,challenge_id), UNIQUE(run_id,ordinal)
);
INSERT OR IGNORE INTO counters(name,value) VALUES('competition',0);
`

type CompetitionConfig struct {
	Rules                   string `json:"rules"`
	Concurrency             int    `json:"concurrency"`
	MaxWorkers              int    `json:"max_workers"`
	ChallengeTimeoutSeconds int    `json:"challenge_timeout_seconds"`
}

func (c *CompetitionConfig) Normalize() error {
	if !utf8.ValidString(c.Rules) || strings.TrimSpace(c.Rules) == "" || len(c.Rules) > 64<<10 {
		return fmt.Errorf("competition rules must contain 1..65536 bytes of text")
	}
	if c.Concurrency == 0 {
		c.Concurrency = 1
	}
	if c.MaxWorkers == 0 {
		c.MaxWorkers = 1
	}
	if c.ChallengeTimeoutSeconds == 0 {
		c.ChallengeTimeoutSeconds = 1200
	}
	if c.Concurrency < 1 || c.Concurrency > 32 || c.MaxWorkers < 1 || c.MaxWorkers > 32 || c.ChallengeTimeoutSeconds < 1 || c.ChallengeTimeoutSeconds > 86400 {
		return fmt.Errorf("concurrency and max_workers must be 1..32; challenge_timeout_seconds must be 1..86400")
	}
	return nil
}

type CompetitionRun struct {
	CompetitionConfig
	ID               string            `json:"id"`
	Platform         string            `json:"platform"`
	ScopeID          string            `json:"scope_id"`
	RulesSummary     string            `json:"rules_summary"`
	Status           string            `json:"status"`
	LastError        string            `json:"last_error"`
	PlanningAttempts int               `json:"planning_attempts"`
	RetryAfter       string            `json:"retry_after"`
	CreatedAt        string            `json:"created_at"`
	FinishedAt       *string           `json:"finished_at"`
	Items            []CompetitionItem `json:"items"`
}
type CompetitionItem struct {
	InitialDescription string  `json:"initial_description"`
	RunID              string  `json:"run_id"`
	ChallengeID        string  `json:"challenge_id"`
	Ordinal            int     `json:"ordinal"`
	ProjectID          *string `json:"project_id"`
	Status             string  `json:"status"`
	Outcome            string  `json:"outcome"`
	LastError          string  `json:"last_error"`
	StartedAt          *string `json:"started_at"`
	FinishedAt         *string `json:"finished_at"`
	RetryAfter         string  `json:"retry_after"`
}

const competitionColumns = `id,platform,scope_id,rules,rules_summary,concurrency,max_workers,challenge_timeout_seconds,status,last_error,planning_attempts,retry_after,created_at,finished_at`

func (d *DB) CreateCompetitionRun(ctx context.Context, platform, scope string, config CompetitionConfig, challenges []string) (CompetitionRun, error) {
	if err := config.Normalize(); err != nil {
		return CompetitionRun{}, err
	}
	if platform == "" || scope == "" {
		return CompetitionRun{}, fmt.Errorf("platform and task scope are required")
	}
	var id string
	err := tx(ctx, d, func(t *sql.Tx) error {
		var active string
		if err := t.QueryRow(`SELECT id FROM competition_runs WHERE platform=? AND scope_id=? AND status IN ('planning','running','stopping')`, platform, scope).Scan(&active); err == nil {
			return fmt.Errorf("competition %s already active for this task", active)
		} else if err != sql.ErrNoRows {
			return err
		}
		var counter int
		if err := t.QueryRow(`UPDATE counters SET value=value+1 WHERE name='competition' RETURNING value`).Scan(&counter); err != nil {
			return err
		}
		id = fmt.Sprintf("competition_%03d", counter)
		if _, err := t.Exec(`INSERT INTO competition_runs(id,platform,scope_id,rules,concurrency,max_workers,challenge_timeout_seconds,created_at) VALUES(?,?,?,?,?,?,?,?)`, id, platform, scope, config.Rules, config.Concurrency, config.MaxWorkers, config.ChallengeTimeoutSeconds, now()); err != nil {
			return err
		}
		seen := map[string]bool{}
		for ordinal, challenge := range challenges {
			if strings.TrimSpace(challenge) == "" || seen[challenge] {
				return fmt.Errorf("challenge IDs must be nonempty and unique")
			}
			seen[challenge] = true
			if _, err := t.Exec(`INSERT INTO competition_items(run_id,challenge_id,ordinal) VALUES(?,?,?)`, id, challenge, ordinal); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return CompetitionRun{}, err
	}
	return d.GetCompetitionRun(ctx, id)
}

func (d *DB) GetCompetitionRun(ctx context.Context, id string) (CompetitionRun, error) {
	var out CompetitionRun
	var finished sql.NullString
	err := d.SQL.QueryRowContext(ctx, `SELECT `+competitionColumns+` FROM competition_runs WHERE id=?`, id).Scan(&out.ID, &out.Platform, &out.ScopeID, &out.Rules, &out.RulesSummary, &out.Concurrency, &out.MaxWorkers, &out.ChallengeTimeoutSeconds, &out.Status, &out.LastError, &out.PlanningAttempts, &out.RetryAfter, &out.CreatedAt, &finished)
	if err == sql.ErrNoRows {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	out.FinishedAt = nullable(finished)
	out.Items = []CompetitionItem{}
	rows, err := d.SQL.QueryContext(ctx, `SELECT i.run_id,i.challenge_id,i.ordinal,i.project_id,i.status,i.outcome,i.last_error,i.started_at,i.finished_at,i.retry_after,COALESCE(f.description,'') FROM competition_items i LEFT JOIN facts f ON f.project_id=i.project_id AND f.id='origin' WHERE i.run_id=? ORDER BY i.ordinal`, id)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var item CompetitionItem
		var project, started, finished sql.NullString
		if err := rows.Scan(&item.RunID, &item.ChallengeID, &item.Ordinal, &project, &item.Status, &item.Outcome, &item.LastError, &started, &finished, &item.RetryAfter, &item.InitialDescription); err != nil {
			return out, err
		}
		item.ProjectID, item.StartedAt, item.FinishedAt = nullable(project), nullable(started), nullable(finished)
		out.Items = append(out.Items, item)
	}
	return out, rows.Err()
}

func (d *DB) CompetitionRunIDs(ctx context.Context, activeOnly bool) ([]string, error) {
	query := `SELECT id FROM competition_runs`
	if activeOnly {
		query += ` WHERE status IN ('planning','running','stopping')`
	}
	rows, err := d.SQL.QueryContext(ctx, query+` ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (d *DB) SaveCompetitionPlan(ctx context.Context, id, summary string, order []string) error {
	return tx(ctx, d, func(t *sql.Tx) error {
		var status string
		if err := t.QueryRow(`SELECT status FROM competition_runs WHERE id=?`, id).Scan(&status); err != nil {
			return err
		}
		if status != "planning" {
			return fmt.Errorf("competition is no longer planning")
		}
		var count int
		if err := t.QueryRow(`SELECT COUNT(*) FROM competition_items WHERE run_id=?`, id).Scan(&count); err != nil {
			return err
		}
		if count != len(order) {
			return fmt.Errorf("competition plan must include every challenge exactly once")
		}
		seen := map[string]bool{}
		// Negative ordinals make reordering safe under the unique constraint.
		if _, err := t.Exec(`UPDATE competition_items SET ordinal=-ordinal-1 WHERE run_id=?`, id); err != nil {
			return err
		}
		for ordinal, challenge := range order {
			if seen[challenge] {
				return fmt.Errorf("duplicate challenge in competition plan")
			}
			seen[challenge] = true
			result, err := t.Exec(`UPDATE competition_items SET ordinal=? WHERE run_id=? AND challenge_id=?`, ordinal, id, challenge)
			if err != nil {
				return err
			}
			if n, _ := result.RowsAffected(); n != 1 {
				return fmt.Errorf("unknown challenge in competition plan")
			}
		}
		_, err := t.Exec(`UPDATE competition_runs SET rules_summary=?,status='running',last_error='',retry_after='' WHERE id=? AND status='planning'`, summary, id)
		return err
	})
}

func (d *DB) SetCompetitionConcurrency(ctx context.Context, id string, limit int) error {
	if limit < 1 || limit > 32 {
		return fmt.Errorf("concurrency must be 1..32")
	}
	result, err := d.SQL.ExecContext(ctx, `UPDATE competition_runs SET concurrency=? WHERE id=? AND status IN ('planning','running')`, limit, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return fmt.Errorf("competition is not active")
	}
	return nil
}

func (d *DB) RequestCompetitionStop(ctx context.Context, id string) error {
	_, err := d.SQL.ExecContext(ctx, `UPDATE competition_runs SET status='stopping' WHERE id=? AND status IN ('planning','running')`, id)
	return err
}

func (d *DB) ClaimCompetitionItem(ctx context.Context, run, challenge string, stamp time.Time) (bool, error) {
	result, err := d.SQL.ExecContext(ctx, `UPDATE competition_items SET status='starting',started_at=? WHERE run_id=? AND challenge_id=? AND status='queued'
 AND EXISTS(SELECT 1 FROM competition_runs r WHERE r.id=run_id AND r.status='running' AND (SELECT COUNT(*) FROM competition_items i WHERE i.run_id=r.id AND i.status IN ('starting','running','closing'))<r.concurrency)`, stamp.UTC().Format(time.RFC3339Nano), run, challenge)
	if err != nil {
		return false, err
	}
	n, _ := result.RowsAffected()
	return n == 1, nil
}

func (d *DB) SaveCompetitionItem(ctx context.Context, item CompetitionItem) error {
	_, err := d.SQL.ExecContext(ctx, `UPDATE competition_items SET project_id=?,status=?,outcome=?,last_error=?,started_at=?,finished_at=?,retry_after=? WHERE run_id=? AND challenge_id=?`, item.ProjectID, item.Status, item.Outcome, item.LastError, item.StartedAt, item.FinishedAt, item.RetryAfter, item.RunID, item.ChallengeID)
	return err
}

func (d *DB) FinishCompetitionRun(ctx context.Context, id, status, message string) error {
	if status != "completed" && status != "stopped" && status != "failed" {
		return fmt.Errorf("invalid competition final status")
	}
	_, err := d.SQL.ExecContext(ctx, `UPDATE competition_runs SET status=?,last_error=?,finished_at=? WHERE id=? AND status IN ('planning','running','stopping')`, status, message, now(), id)
	return err
}

func (d *DB) FailCompetitionProject(ctx context.Context, project, reason string) error {
	return tx(ctx, d, func(t *sql.Tx) error {
		_, err := failActiveProjectTx(t, project, reason, "competition coordinator stopped this challenge")
		return err
	})
}

// Success is recorded with the platform's confirmation as evidence. The
// coordinator calls this only after the platform reports every flag accepted.
func (d *DB) CompleteCompetitionProject(ctx context.Context, project, confirmation string) error {
	return tx(ctx, d, func(t *sql.Tx) error {
		var status string
		if err := t.QueryRow(`SELECT status FROM projects WHERE id=?`, project).Scan(&status); err != nil {
			return err
		}
		if status == "completed" {
			return nil
		}
		fact, err := nextID(t, project, "fact", "f")
		if err != nil {
			return err
		}
		if _, err := t.Exec(`INSERT INTO facts(id,project_id,description,detail,result_type) VALUES(?,?,?,?,'progress')`, fact, project, "Competition platform confirmed that all challenge flags were accepted", confirmation); err != nil {
			return err
		}
		if _, err := t.Exec(`INSERT OR IGNORE INTO goal_sources(goal_id,project_id,fact_id) SELECT id,project_id,? FROM goals WHERE project_id=? AND status='active'`, fact, project); err != nil {
			return err
		}
		if _, err := t.Exec(`UPDATE goals SET status='completed',completed_at=?,completion_description=?,completed_by='competition' WHERE project_id=? AND status='active'`, now(), "All flags accepted by the competition platform", project); err != nil {
			return err
		}
		if _, err := t.Exec(`UPDATE projects SET status='completed',failure_reason='',decide_worker=NULL,decide_trigger=NULL,decide_started_at=NULL,decide_last_heartbeat_at=NULL WHERE id=?`, project); err != nil {
			return err
		}
		if _, err := t.Exec(`UPDATE steps SET worker=NULL WHERE project_id=? AND to_fact_id IS NULL`, project); err != nil {
			return err
		}
		return queueDecideTx(t, project, "competition_completed", fact)
	})
}
