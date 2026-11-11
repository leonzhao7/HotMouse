package core

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// The execution process can persist a capture before its full report finishes.
// Source bytes and the format are checked by the worker's deterministic handoff.
type FlagCheckpoint struct {
	Value              string `json:"value"`
	ChallengeID        string `json:"challenge_id"`
	SourcePath         string `json:"source_path"`
	OriginalSourcePath string `json:"original_source_path"`
	SourceSHA256       string `json:"source_sha256"`
	FormatNote         string `json:"format_note"`
	FormatPattern      string `json:"format_pattern,omitempty"`
}

func (d *DB) CheckpointFlag(ctx context.Context, project, step, worker string, req FlagCheckpoint) (Flag, error) {
	var out Flag
	err := tx(ctx, d, func(t *sql.Tx) error {
		if _, err := checkActive(t, project); err != nil {
			return err
		}
		var owner sql.NullString
		var closed sql.NullString
		var abandoned bool
		if err := t.QueryRow(`SELECT worker,to_fact_id,abandoned FROM steps WHERE project_id=? AND id=?`, project, step).Scan(&owner, &closed, &abandoned); err != nil {
			return err
		}
		if !owner.Valid || owner.String != worker || closed.Valid || abandoned {
			return fmt.Errorf("flag checkpoint requires the current open step lease")
		}
		if strings.TrimSpace(req.SourcePath) == "" || len(req.SourceSHA256) != 64 || strings.TrimSpace(req.FormatNote) == "" {
			return fmt.Errorf("flag checkpoint requires raw source, digest and challenge format notes")
		}
		if req.Value != strings.TrimSpace(req.Value) {
			return fmt.Errorf("preserve the exact flag without surrounding output whitespace")
		}
		binding, err := platformBinding(ctx, t, project)
		if err != nil {
			return err
		}
		if binding != nil {
			if req.ChallengeID != "" && req.ChallengeID != binding.ChallengeID {
				return fmt.Errorf("flag belongs to another platform challenge")
			}
			req.ChallengeID = binding.ChallengeID
		}
		// Replay after a timeout/restart never creates another fact or submission.
		var existing int64
		err = t.QueryRow(`SELECT id FROM flags WHERE project_id=? AND challenge_id=? AND value=?`, project, req.ChallengeID, req.Value).Scan(&existing)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == nil {
			return t.QueryRow(`SELECT id,project_id,challenge_id,value,fact_id,status,created_at FROM flags WHERE id=?`, existing).Scan(&out.ID, &out.ProjectID, &out.ChallengeID, &out.Value, &out.FactID, &out.Status, &out.CreatedAt)
		}
		fact, err := nextID(t, project, "fact", "f")
		if err != nil {
			return err
		}
		description := "Flag candidate verified against saved raw output; platform acceptance pending"
		if _, err := t.Exec(`INSERT INTO facts(id,project_id,description,title,detail,result_type) VALUES(?,?,?,'Flag checkpoint',?,'progress')`, fact, project, description, jsonData(req)); err != nil {
			return err
		}
		out, err = captureFlagTx(t, project, CaptureFlag{Value: req.Value, ChallengeID: req.ChallengeID, FactID: fact})
		// The coordinator polls the ledger. Avoid a planning event while the
		// current execution is still validating/delivering the same result.
		return err
	})
	return out, err
}
