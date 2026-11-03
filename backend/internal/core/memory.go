package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"unicode"
)

type HistoryMatch struct {
	ID          string `json:"id" yaml:"id"`
	Kind        string `json:"kind" yaml:"kind"`
	Title       string `json:"title,omitempty" yaml:"title,omitempty"`
	Description string `json:"description" yaml:"description"`
	ResultType  string `json:"result_type,omitempty" yaml:"result_type,omitempty"`
	HasDetail   bool   `json:"has_detail" yaml:"has_detail"`
	Score       int    `json:"score" yaml:"score"`
}
type CreateExperience struct {
	Title             string   `json:"title" yaml:"title"`
	Conditions        string   `json:"conditions" yaml:"conditions"`
	Method            string   `json:"method" yaml:"method"`
	FailureConditions string   `json:"failure_conditions" yaml:"failure_conditions"`
	Detail            string   `json:"detail,omitempty" yaml:"detail,omitempty"`
	Tags              []string `json:"tags" yaml:"tags"`
	SourceProjectID   *string  `json:"source_project_id" yaml:"source_project_id"`
	SourceStepID      *string  `json:"source_step_id" yaml:"source_step_id"`
	SourceFactIDs     []string `json:"source_fact_ids" yaml:"source_fact_ids"`
}
type Experience struct {
	CreateExperience `yaml:",inline"`
	ID               int64  `json:"id" yaml:"id"`
	CreatedAt        string `json:"created_at" yaml:"created_at"`
	Score            int    `json:"score,omitempty" yaml:"score,omitempty"`
}

func recallTokens(s string) []string {
	seen := map[string]bool{}
	var word []rune
	flush := func() {
		if len(word) > 0 {
			seen[string(word)] = true
			word = nil
		}
	}
	for _, r := range strings.ToLower(s) {
		if unicode.Is(unicode.Han, r) {
			flush()
			seen[string(r)] = true
		} else if unicode.IsLetter(r) || unicode.IsDigit(r) {
			word = append(word, r)
		} else {
			flush()
		}
	}
	flush()
	tokens := []string{}
	for token := range seen {
		tokens = append(tokens, token)
	}
	sort.Strings(tokens)
	return tokens
}

// Match whole words, splitting Chinese characters like sapper's offline recall.
func recallScore(tokens []string, title, body string) int {
	titleWords := map[string]bool{}
	for _, t := range recallTokens(title) {
		titleWords[t] = true
	}
	bodyWords := map[string]bool{}
	for _, t := range recallTokens(body) {
		bodyWords[t] = true
	}
	score := 0
	for _, t := range tokens {
		if titleWords[t] {
			score += 2
		}
		if bodyWords[t] {
			score++
		}
	}
	return score
}
func searchLimit(limit int) int {
	if limit < 1 {
		return 5
	}
	if limit > 50 {
		return 50
	}
	return limit
}
func (d *DB) SearchHistory(ctx context.Context, project, query string, limit int) ([]HistoryMatch, error) {
	if _, err := queryProject(ctx, d.SQL, project); err == sql.ErrNoRows {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	tokens := recallTokens(query)
	out := []HistoryMatch{}
	if len(tokens) == 0 {
		return out, nil
	}
	// Only the current project's summaries and evidence are searched.
	rows, err := d.SQL.QueryContext(ctx, `SELECT id,'fact',title,description,result_type,detail,detail<>'' FROM facts WHERE project_id=?
 UNION ALL SELECT id,'step','',description,COALESCE(json_extract(NULLIF(result_json,''),'$.type'),''),COALESCE(json_extract(NULLIF(result_json,''),'$.detail'),''),result_json<>'' FROM steps WHERE project_id=?
 UNION ALL SELECT id,'finding',title,description,kind,data_json,data_json<>'{}' FROM findings WHERE project_id=?`, project, project, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var m HistoryMatch
		var detail string
		if err := rows.Scan(&m.ID, &m.Kind, &m.Title, &m.Description, &m.ResultType, &detail, &m.HasDetail); err != nil {
			return nil, err
		}
		m.Score = recallScore(tokens, m.Title, m.Description+" "+detail)
		if m.Score > 0 {
			m.Description = brief(m.Description, 320)
			out = append(out, m)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].ID > out[j].ID
	})
	if len(out) > searchLimit(limit) {
		out = out[:searchLimit(limit)]
	}
	return out, nil
}
func (d *DB) NodeDetail(ctx context.Context, project, kind, id string) (any, error) {
	// Detail endpoints deliberately do not truncate stored evidence.
	switch kind {
	case "fact":
		var f Fact
		err := d.SQL.QueryRowContext(ctx, `SELECT id,description,title,detail,result_type FROM facts WHERE project_id=? AND id=?`, project, id).Scan(&f.ID, &f.Description, &f.Title, &f.Detail, &f.ResultType)
		return f, err
	case "step":
		if err := d.expireLeases(ctx, project); err != nil {
			return nil, err
		}
		return d.stepTx(ctx, d.SQL, project, id)
	case "finding":
		var f Finding
		var raw string
		var fact sql.NullString
		err := d.SQL.QueryRowContext(ctx, `SELECT id,title,description,severity,kind,data_json,fact_id,created_at FROM findings WHERE project_id=? AND id=?`, project, id).Scan(&f.ID, &f.Title, &f.Description, &f.Severity, &f.Kind, &raw, &fact, &f.CreatedAt)
		f.Data = parseData(raw)
		f.FactID = nullable(fact)
		return f, err
	}
	return nil, requireValid("detail kind", kind)
}
func (d *DB) CreateExperience(ctx context.Context, req CreateExperience) (Experience, error) {
	var out Experience
	err := tx(ctx, d, func(t *sql.Tx) error {
		for _, field := range []struct{ name, value string }{{"experience title", req.Title}, {"experience conditions", req.Conditions}, {"experience method", req.Method}} {
			if err := requireText(field.name, field.value); err != nil {
				return err
			}
		}
		if req.SourceProjectID == nil && (req.SourceStepID != nil || len(req.SourceFactIDs) > 0) {
			return requireValid("experience source", "project required")
		}
		if req.SourceProjectID != nil {
			if _, err := queryProject(ctx, t, *req.SourceProjectID); err != nil {
				return err
			}
			if req.SourceStepID != nil {
				var n int
				if err := t.QueryRow(`SELECT count(*) FROM steps WHERE project_id=? AND id=?`, *req.SourceProjectID, *req.SourceStepID).Scan(&n); err != nil {
					return err
				}
				if n == 0 {
					return ErrNotFound
				}
			}
			var err error
			req.SourceFactIDs, err = normalizeFactIDs(req.SourceFactIDs)
			if err != nil {
				return err
			}
			if err := validateFacts(t, *req.SourceProjectID, req.SourceFactIDs); err != nil {
				return err
			}
		}
		req.Title = clean(req.Title)
		req.Conditions = clean(req.Conditions)
		req.Method = clean(req.Method)
		req.FailureConditions = clean(req.FailureConditions)
		tags := map[string]bool{}
		for _, tag := range req.Tags {
			tag = strings.ToLower(clean(tag))
			if tag != "" {
				tags[tag] = true
			}
		}
		req.Tags = []string{}
		for tag := range tags {
			req.Tags = append(req.Tags, tag)
		}
		sort.Strings(req.Tags)
		if req.SourceFactIDs == nil {
			req.SourceFactIDs = []string{}
		}
		stamp := now()
		res, err := t.Exec(`INSERT INTO experiences(title,conditions,method,failure_conditions,detail,tags_json,source_project_id,source_step_id,source_fact_ids_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, req.Title, req.Conditions, req.Method, req.FailureConditions, req.Detail, jsonData(req.Tags), req.SourceProjectID, req.SourceStepID, jsonData(req.SourceFactIDs), stamp)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		out = Experience{CreateExperience: req, ID: id, CreatedAt: stamp}
		return err
	})
	return out, err
}

const experienceColumns = `id,title,conditions,method,failure_conditions,detail,tags_json,source_project_id,source_step_id,source_fact_ids_json,created_at`

func scanExperience(row interface{ Scan(...any) error }) (Experience, error) {
	var e Experience
	var tags, facts string
	var project, step sql.NullString
	err := row.Scan(&e.ID, &e.Title, &e.Conditions, &e.Method, &e.FailureConditions, &e.Detail, &tags, &project, &step, &facts, &e.CreatedAt)
	_ = json.Unmarshal([]byte(tags), &e.Tags)
	_ = json.Unmarshal([]byte(facts), &e.SourceFactIDs)
	e.SourceProjectID = nullable(project)
	e.SourceStepID = nullable(step)
	return e, err
}
func (d *DB) GetExperience(ctx context.Context, id int64) (Experience, error) {
	return scanExperience(d.SQL.QueryRowContext(ctx, `SELECT `+experienceColumns+` FROM experiences WHERE id=?`, id))
}
func (d *DB) SearchExperiences(ctx context.Context, query, tag string, limit int) ([]Experience, error) {
	rows, err := d.SQL.QueryContext(ctx, `SELECT `+experienceColumns+` FROM experiences ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tokens := recallTokens(query)
	out := []Experience{}
	for rows.Next() {
		e, err := scanExperience(rows)
		if err != nil {
			return nil, err
		}
		if tag != "" {
			found := false
			for _, t := range e.Tags {
				if t == strings.ToLower(tag) {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		e.Score = recallScore(tokens, e.Title, e.Conditions+" "+e.Method+" "+e.FailureConditions+" "+strings.Join(e.Tags, " ")+" "+e.Detail)
		if len(tokens) > 0 && e.Score == 0 {
			continue
		}
		e.Detail = ""
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > searchLimit(limit) {
		out = out[:searchLimit(limit)]
	}
	return out, nil
}
