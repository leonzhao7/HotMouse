package core

import (
	"database/sql"
	"fmt"
)

func addColumn(d *sql.DB, table, name, declaration string) error {
	rows, err := d.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notnull, pk int
		var col, typ string
		var def sql.NullString
		if err := rows.Scan(&cid, &col, &typ, &notnull, &def, &pk); err != nil {
			rows.Close()
			return err
		}
		if col == name {
			found = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil || found {
		return err
	}
	_, err = d.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + name + ` ` + declaration)
	return err
}
func migrateStrategy(d *sql.DB) error {
	for _, col := range []struct{ table, name, decl string }{
		{"facts", "title", `TEXT NOT NULL DEFAULT ''`},
		{"facts", "detail", `TEXT NOT NULL DEFAULT ''`},
		{"facts", "result_type", `TEXT NOT NULL DEFAULT ''`},
		{"steps", "route_id", `TEXT`},
		{"steps", "result_json", `TEXT NOT NULL DEFAULT ''`},
		{"steps", "llm_response", `TEXT NOT NULL DEFAULT ''`},
		{"steps", "llm_response_recorded", `INTEGER NOT NULL DEFAULT 0`},
		{"steps", "llm_response_error", `TEXT NOT NULL DEFAULT ''`},
		{"worker_task_history", "last_activity_at", `TEXT`},
		{"projects", "failure_reason", `TEXT NOT NULL DEFAULT ''`},
		{"worker_retry_state", "graph_fingerprint", `TEXT NOT NULL DEFAULT ''`},
		{"worker_retry_state", "event_through", `INTEGER NOT NULL DEFAULT 0`},
	} {
		if err := addColumn(d, col.table, col.name, col.decl); err != nil {
			return err
		}
	}
	_, err := d.Exec(strategySchema)
	if err != nil {
		return err
	}
	// Existing steps are grouped by goal; explicit routes can split future work.
	t, err := d.Begin()
	if err != nil {
		return err
	}
	_, err = t.Exec(`INSERT OR IGNORE INTO routes(id,project_id,title,goal_id,created_at) SELECT 'route_'||COALESCE(goal_id,'unassigned'),project_id,COALESCE(goal_id,'Unassigned route'),goal_id,MIN(created_at) FROM steps WHERE route_id IS NULL GROUP BY project_id,goal_id;
 UPDATE steps SET route_id='route_'||COALESCE(goal_id,'unassigned') WHERE route_id IS NULL;
 CREATE TEMP TABLE route_backfill AS SELECT h.id AS history_id,s.project_id,s.route_id,MAX(0,CAST((julianday(h.finished_at)-julianday(h.started_at))*86400000 AS INTEGER)) AS elapsed_ms FROM worker_task_history h JOIN steps s ON s.project_id=h.project_id AND s.id=h.step_id JOIN routes r ON r.project_id=s.project_id AND r.id=s.route_id WHERE h.task_type='execute' AND h.finished_at IS NOT NULL AND h.status NOT IN ('claim_failed','disabled') AND NOT EXISTS(SELECT 1 FROM route_investments i WHERE i.history_id=h.id);
 INSERT INTO route_investments(history_id,project_id,route_id,elapsed_ms) SELECT history_id,project_id,route_id,elapsed_ms FROM route_backfill;
 UPDATE routes SET invested_ms=invested_ms+COALESCE((SELECT SUM(elapsed_ms) FROM route_backfill i WHERE i.project_id=routes.project_id AND i.route_id=routes.id),0);
 DROP TABLE route_backfill;
 INSERT OR IGNORE INTO flags(project_id,challenge_id,value,fact_id,created_at)
 SELECT f.project_id,COALESCE(json_extract(f.data_json,'$.challenge_id'),''),COALESCE(json_extract(f.data_json,'$.flag'),json_extract(f.data_json,'$.value')),f.fact_id,f.created_at FROM findings f JOIN facts a ON a.project_id=f.project_id AND a.id=f.fact_id WHERE f.kind='flag' AND json_valid(f.data_json) AND typeof(COALESCE(json_extract(f.data_json,'$.flag'),json_extract(f.data_json,'$.value')))='text' AND length(trim(COALESCE(json_extract(f.data_json,'$.flag'),json_extract(f.data_json,'$.value'))))>0 AND a.result_type NOT IN ('environment_failure','no_information','needs_attention');`)
	if err != nil {
		t.Rollback()
		return err
	}
	return t.Commit()
}
func requireValid(name, value string) error { return fmt.Errorf("invalid %s %q", name, value) }

const strategySchema = `
CREATE TABLE IF NOT EXISTS routes (
 id TEXT NOT NULL, project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 title TEXT NOT NULL, goal_id TEXT, status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','shelved')),
 budget_seconds INTEGER NOT NULL DEFAULT 1200 CHECK(budget_seconds>=0), no_progress_limit INTEGER NOT NULL DEFAULT 3 CHECK(no_progress_limit>0),
 invested_ms INTEGER NOT NULL DEFAULT 0, result_count INTEGER NOT NULL DEFAULT 0, progress_count INTEGER NOT NULL DEFAULT 0,
 negative_count INTEGER NOT NULL DEFAULT 0, no_progress_count INTEGER NOT NULL DEFAULT 0, environment_failure_count INTEGER NOT NULL DEFAULT 0,
 shelved_reason TEXT NOT NULL DEFAULT '', reopen_condition TEXT NOT NULL DEFAULT '', next_direction TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL,
 PRIMARY KEY(id,project_id), FOREIGN KEY(goal_id,project_id) REFERENCES goals(id,project_id));
CREATE TABLE IF NOT EXISTS route_records (
 id INTEGER PRIMARY KEY AUTOINCREMENT, project_id TEXT NOT NULL, route_id TEXT NOT NULL, action TEXT NOT NULL, reason TEXT NOT NULL,
 reopen_condition TEXT NOT NULL DEFAULT '', next_direction TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL,
 FOREIGN KEY(route_id,project_id) REFERENCES routes(id,project_id) ON DELETE CASCADE);
CREATE TABLE IF NOT EXISTS route_investments (
 history_id INTEGER PRIMARY KEY REFERENCES worker_task_history(id) ON DELETE CASCADE, project_id TEXT NOT NULL, route_id TEXT NOT NULL, elapsed_ms INTEGER NOT NULL,
 FOREIGN KEY(route_id,project_id) REFERENCES routes(id,project_id) ON DELETE CASCADE);
CREATE TABLE IF NOT EXISTS decide_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT, project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 reason TEXT NOT NULL, source_id TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, handled_at TEXT);
CREATE INDEX IF NOT EXISTS idx_decide_events_pending ON decide_events(project_id,handled_at,id);
CREATE TABLE IF NOT EXISTS experiences (
 id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT NOT NULL, conditions TEXT NOT NULL, method TEXT NOT NULL,
 failure_conditions TEXT NOT NULL DEFAULT '', detail TEXT NOT NULL DEFAULT '', tags_json TEXT NOT NULL DEFAULT '[]',
 source_project_id TEXT REFERENCES projects(id) ON DELETE SET NULL, source_step_id TEXT, source_fact_ids_json TEXT NOT NULL DEFAULT '[]', created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS flags (
 id INTEGER PRIMARY KEY AUTOINCREMENT, project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 challenge_id TEXT NOT NULL DEFAULT '', value TEXT NOT NULL, fact_id TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'captured' CHECK(status IN ('captured','submission_pending','accepted','rejected','submission_unknown')),
 created_at TEXT NOT NULL, UNIQUE(project_id,challenge_id,value), FOREIGN KEY(fact_id,project_id) REFERENCES facts(id,project_id));
CREATE TABLE IF NOT EXISTS flag_attempts (
 id INTEGER PRIMARY KEY AUTOINCREMENT, flag_id INTEGER NOT NULL REFERENCES flags(id) ON DELETE CASCADE,
 status TEXT NOT NULL DEFAULT 'submission_pending' CHECK(status IN ('submission_pending','accepted','rejected','submission_unknown')),
 response TEXT NOT NULL DEFAULT '', started_at TEXT NOT NULL, finished_at TEXT);
`
