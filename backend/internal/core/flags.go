package core

import (
	"context"
	"database/sql"
	"fmt"
)

type CaptureFlag struct {
	ChallengeID string `json:"challenge_id" yaml:"challenge_id"`
	Value       string `json:"value" yaml:"value"`
	FactID      string `json:"fact_id" yaml:"fact_id"`
}
type Flag struct {
	CaptureFlag `yaml:",inline"`
	ID          int64         `json:"id" yaml:"id"`
	ProjectID   string        `json:"project_id" yaml:"project_id"`
	Status      string        `json:"status" yaml:"status"`
	CreatedAt   string        `json:"created_at" yaml:"created_at"`
	Attempts    []FlagAttempt `json:"attempts" yaml:"attempts"`
}
type FlagAttempt struct {
	ID         int64   `json:"id" yaml:"id"`
	FlagID     int64   `json:"flag_id" yaml:"flag_id"`
	Status     string  `json:"status" yaml:"status"`
	Response   string  `json:"response" yaml:"response"`
	StartedAt  string  `json:"started_at" yaml:"started_at"`
	FinishedAt *string `json:"finished_at" yaml:"finished_at"`
}
type ResolveFlagAttempt struct {
	Status   string `json:"status"`
	Response string `json:"response"`
}

func captureFlagTx(t *sql.Tx, project string, req CaptureFlag) (Flag, error) {
	req.Value = clean(req.Value)
	req.ChallengeID = clean(req.ChallengeID)
	req.FactID = clean(req.FactID)
	if req.ChallengeID == "" {
		// A final Finding may omit challenge_id after an early checkpoint used
		// the platform binding. Use the same ledger identity for both paths.
		err := t.QueryRow(`SELECT challenge_id FROM platform_projects WHERE project_id=?`, project).Scan(&req.ChallengeID)
		if err != nil && err != sql.ErrNoRows {
			return Flag{}, err
		}
	}
	if err := requireText("flag value", req.Value); err != nil {
		return Flag{}, err
	}
	if err := validateFacts(t, project, []string{req.FactID}); err != nil {
		return Flag{}, err
	}
	var kind string
	if err := t.QueryRow(`SELECT result_type FROM facts WHERE project_id=? AND id=?`, project, req.FactID).Scan(&kind); err != nil {
		return Flag{}, err
	}
	if kind == "environment_failure" || kind == "needs_attention" || kind == "no_information" {
		return Flag{}, fmt.Errorf("flag source must be target evidence")
	}
	if _, err := t.Exec(`INSERT OR IGNORE INTO flags(project_id,challenge_id,value,fact_id,created_at) VALUES(?,?,?,?,?)`, project, req.ChallengeID, req.Value, req.FactID, now()); err != nil {
		return Flag{}, err
	}
	var f Flag
	f.Attempts = []FlagAttempt{}
	err := t.QueryRow(`SELECT id,project_id,challenge_id,value,fact_id,status,created_at FROM flags WHERE project_id=? AND challenge_id=? AND value=?`, project, req.ChallengeID, req.Value).Scan(&f.ID, &f.ProjectID, &f.ChallengeID, &f.Value, &f.FactID, &f.Status, &f.CreatedAt)
	return f, err
}
func (d *DB) CaptureFlag(ctx context.Context, project string, req CaptureFlag) (Flag, error) {
	var out Flag
	err := tx(ctx, d, func(t *sql.Tx) error {
		if _, err := queryProject(ctx, t, project); err != nil {
			return err
		}
		var err error
		out, err = captureFlagTx(t, project, req)
		return err
	})
	return out, err
}
func syncFlagTx(t *sql.Tx, project string, f Finding) error {
	if f.Kind != "flag" {
		return nil
	}
	value, _ := f.Data["flag"].(string)
	if value == "" {
		value, _ = f.Data["value"].(string)
	}
	// Legacy flag findings without a value remain readable as findings.
	if value == "" || f.FactID == nil {
		return nil
	}
	challenge, _ := f.Data["challenge_id"].(string)
	_, err := captureFlagTx(t, project, CaptureFlag{ChallengeID: challenge, Value: value, FactID: *f.FactID})
	return err
}
func (d *DB) ListFlags(ctx context.Context, project string) ([]Flag, error) {
	if _, err := queryProject(ctx, d.SQL, project); err != nil {
		return nil, err
	}
	rows, err := d.SQL.QueryContext(ctx, `SELECT id,project_id,challenge_id,value,fact_id,status,created_at FROM flags WHERE project_id=? ORDER BY id`, project)
	if err != nil {
		return nil, err
	}
	out := []Flag{}
	for rows.Next() {
		var f Flag
		f.Attempts = []FlagAttempt{}
		if err := rows.Scan(&f.ID, &f.ProjectID, &f.ChallengeID, &f.Value, &f.FactID, &f.Status, &f.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range out {
		rows, err := d.SQL.QueryContext(ctx, `SELECT id,flag_id,status,response,started_at,finished_at FROM flag_attempts WHERE flag_id=? ORDER BY id`, out[i].ID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			a, err := scanFlagAttempt(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			out[i].Attempts = append(out[i].Attempts, a)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
func scanFlagAttempt(row interface{ Scan(...any) error }) (FlagAttempt, error) {
	var a FlagAttempt
	var finished sql.NullString
	err := row.Scan(&a.ID, &a.FlagID, &a.Status, &a.Response, &a.StartedAt, &finished)
	a.FinishedAt = nullable(finished)
	return a, err
}
func (d *DB) BeginFlagAttempt(ctx context.Context, project string, flagID int64) (FlagAttempt, error) {
	var out FlagAttempt
	err := tx(ctx, d, func(t *sql.Tx) error {
		var status string
		if err := t.QueryRow(`SELECT status FROM flags WHERE project_id=? AND id=?`, project, flagID).Scan(&status); err != nil {
			return err
		}
		if status == "accepted" || status == "submission_pending" || status == "submission_unknown" {
			return fmt.Errorf("flag already %s; reconcile the existing attempt before submitting", status)
		}
		stamp := now()
		res, err := t.Exec(`INSERT INTO flag_attempts(flag_id,started_at) VALUES(?,?)`, flagID, stamp)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := t.Exec(`UPDATE flags SET status='submission_pending' WHERE id=?`, flagID); err != nil {
			return err
		}
		out = FlagAttempt{ID: id, FlagID: flagID, Status: "submission_pending", StartedAt: stamp}
		return nil
	})
	return out, err
}
func (d *DB) ResolveFlagAttempt(ctx context.Context, project string, flagID, attemptID int64, req ResolveFlagAttempt) (FlagAttempt, error) {
	var out FlagAttempt
	err := tx(ctx, d, func(t *sql.Tx) error {
		switch req.Status {
		case "accepted", "rejected", "submission_unknown":
		default:
			return requireValid("flag attempt status", req.Status)
		}
		a, err := scanFlagAttempt(t.QueryRow(`SELECT a.id,a.flag_id,a.status,a.response,a.started_at,a.finished_at FROM flag_attempts a JOIN flags f ON f.id=a.flag_id WHERE f.project_id=? AND f.id=? AND a.id=?`, project, flagID, attemptID))
		if err != nil {
			return err
		}
		if a.Status != "submission_pending" && a.Status != "submission_unknown" {
			if a.Status == req.Status && a.Response == req.Response {
				out = a
				return nil
			}
			return fmt.Errorf("flag attempt already resolved")
		}
		stamp := now()
		if _, err := t.Exec(`UPDATE flag_attempts SET status=?,response=?,finished_at=? WHERE id=?`, req.Status, req.Response, stamp, attemptID); err != nil {
			return err
		}
		if _, err := t.Exec(`UPDATE flags SET status=? WHERE id=?`, req.Status, flagID); err != nil {
			return err
		}
		a.Status = req.Status
		a.Response = req.Response
		a.FinishedAt = &stamp
		out = a
		return queueDecideTx(t, project, "flag_"+req.Status, fmt.Sprint(flagID))
	})
	return out, err
}
