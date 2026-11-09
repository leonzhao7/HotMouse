package core

import (
	"context"
	"database/sql"
	"encoding/json"
)

// PlatformBinding stores task identity and delivered challenge data, never the
// platform credential. ScopeID separates benchmark tasks with identical codes.
type PlatformBinding struct {
	Platform    string          `json:"platform" yaml:"platform"`
	ScopeID     string          `json:"scope_id" yaml:"scope_id"`
	ChallengeID string          `json:"challenge_id" yaml:"challenge_id"`
	Challenge   json.RawMessage `json:"challenge" yaml:"challenge"`
	Delivery    json.RawMessage `json:"delivery" yaml:"delivery"`
}

func platformBinding(ctx context.Context, q queryer, project string) (*PlatformBinding, error) {
	var out PlatformBinding
	var challenge, delivery string
	err := q.QueryRowContext(ctx, `SELECT platform,scope_id,challenge_id,challenge_json,delivery_json FROM platform_projects WHERE project_id=?`, project).
		Scan(&out.Platform, &out.ScopeID, &out.ChallengeID, &challenge, &delivery)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out.Challenge = json.RawMessage(challenge)
	out.Delivery = json.RawMessage(delivery)
	return &out, nil
}

func (d *DB) FindPlatformProject(ctx context.Context, platform, scope, challenge string) (string, error) {
	var project string
	err := d.SQL.QueryRowContext(ctx, `SELECT project_id FROM platform_projects WHERE platform=? AND scope_id=? AND challenge_id=?`, platform, scope, challenge).Scan(&project)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return project, err
}
