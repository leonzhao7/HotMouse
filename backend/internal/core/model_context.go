package core

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type modelFact struct {
	FindingIDs  []string `yaml:"finding_ids,omitempty"`
	ID          string   `yaml:"id"`
	Title       string   `yaml:"title,omitempty"`
	Description string   `yaml:"description"`
	ResultType  string   `yaml:"result_type,omitempty"`
	HasDetail   bool     `yaml:"has_detail"`
}
type modelStep struct {
	CanUpdate      bool     `yaml:"can_update"`
	RecoveryStatus string   `yaml:"recovery_status,omitempty"`
	ID             string   `yaml:"id"`
	From           []string `yaml:"from"`
	Description    string   `yaml:"description"`
	GoalID         *string  `yaml:"goal_id,omitempty"`
	RouteID        *string  `yaml:"route_id,omitempty"`
	Priority       int      `yaml:"priority"`
	Worker         *string  `yaml:"worker,omitempty"`
}
type modelFlag struct {
	ID          int64  `yaml:"id"`
	ChallengeID string `yaml:"challenge_id"`
	Status      string `yaml:"status"`
	FactID      string `yaml:"fact_id"`
}

type modelContext struct {
	Flags         []modelFlag       `yaml:"flags,omitempty"`
	SchemaVersion int               `yaml:"schema_version"`
	Phase         string            `yaml:"phase"`
	Project       map[string]string `yaml:"project"`
	Goals         []Goal            `yaml:"goals"`
	CurrentStep   *modelStep        `yaml:"current_step,omitempty"`
	OpenSteps     []modelStep       `yaml:"open_steps,omitempty"`
	RequiredFacts []modelFact       `yaml:"required_facts"`
	RecentFacts   []modelFact       `yaml:"recent_facts,omitempty"`
	Hints         []Hint            `yaml:"hints,omitempty"`
	Routes        []Route           `yaml:"routes,omitempty"`
	Events        []DecideEvent     `yaml:"decide_events,omitempty"`
	History       []HistoryMatch    `yaml:"related_history,omitempty"`
	Experiences   []Experience      `yaml:"experiences,omitempty"`
	DetailAccess  map[string]string `yaml:"detail_access"`
	Omitted       map[string]int    `yaml:"omitted"`
}

func brief(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
func factSummary(f Fact, full bool) modelFact {
	description := f.Description
	if !full {
		description = brief(description, 320)
	}
	return modelFact{ID: f.ID, Title: brief(f.Title, 100), Description: description, ResultType: f.ResultType, HasDetail: f.Detail != ""}
}
func compactStep(s Step) modelStep {
	return modelStep{CanUpdate: s.To == nil && !s.Abandoned && s.Worker == nil, ID: s.ID, From: s.From, Description: s.Description, GoalID: s.GoalID, RouteID: s.RouteID, Priority: s.Priority, Worker: s.Worker}
}

// ModelContext selects relevant graph data. Required prerequisites and goal
// descriptions are never truncated; verbose evidence remains available by ID.
func (d *DB) ModelContext(ctx context.Context, project, phase, stepID string) (string, error) {
	if phase != "decide" && phase != "execute" {
		return "", requireValid("context phase", phase)
	}
	detail, err := d.GetProject(ctx, project)
	if err != nil {
		return "", err
	}
	out := modelContext{SchemaVersion: 1, Phase: phase, Project: map[string]string{"id": project, "title": detail.Project.Title, "status": detail.Project.Status}, Goals: []Goal{}, RequiredFacts: []modelFact{}, Hints: detail.Hints,
		DetailAccess: map[string]string{
			"fact":       "GET /projects/" + project + "/facts/{id}; hotmouse --project " + project + " fact show {id}",
			"step":       "GET /projects/" + project + "/steps/{id}; hotmouse --project " + project + " step show {id}",
			"flags":      "GET /projects/" + project + "/flags; hotmouse --project " + project + " flag list",
			"finding":    "GET /projects/" + project + "/findings/{id}; hotmouse --project " + project + " finding show {id}",
			"history":    "GET /projects/" + project + "/history?q={query}; hotmouse --project " + project + " project recall --query {query}",
			"experience": "GET /api/experiences/{id}; hotmouse experience show {id}",
		}, Omitted: map[string]int{"closed_steps": 0, "facts": 0, "findings": len(detail.Findings)}}
	recoveries := map[string]string{}
	rows, err := d.SQL.QueryContext(ctx, `SELECT step_id,status FROM worker_execute_recovery WHERE project_id=?`, project)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			rows.Close()
			return "", err
		}
		recoveries[id] = status
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", err
	}
	summarizeStep := func(s Step) modelStep {
		view := compactStep(s)
		if status, ok := recoveries[s.ID]; ok {
			view.RecoveryStatus = status
			view.CanUpdate = false
		}
		return view
	}
	findingLinks := map[string][]string{}
	for _, finding := range detail.Findings {
		if finding.FactID != nil {
			findingLinks[*finding.FactID] = append(findingLinks[*finding.FactID], finding.ID)
		}
	}
	summarizeFact := func(f Fact, full bool) modelFact {
		view := factSummary(f, full)
		view.FindingIDs = findingLinks[f.ID]
		view.HasDetail = view.HasDetail || len(view.FindingIDs) > 0
		return view
	}
	required := map[string]bool{"origin": true}
	goalIDs := map[string]bool{}
	routeIDs := map[string]bool{}
	query := []string{}
	if phase == "execute" {
		for _, s := range detail.Steps {
			if s.ID == stepID {
				step := summarizeStep(s)
				out.CurrentStep = &step
				for _, f := range s.From {
					required[f] = true
				}
				if s.GoalID != nil {
					goalIDs[*s.GoalID] = true
				}
				if s.RouteID != nil {
					routeIDs[*s.RouteID] = true
				}
				query = append(query, s.Description)
				break
			}
		}
		if out.CurrentStep == nil {
			return "", ErrNotFound
		}
	} else {
		for _, s := range detail.Steps {
			if s.To != nil || s.Abandoned {
				out.Omitted["closed_steps"]++
				continue
			}
			out.OpenSteps = append(out.OpenSteps, summarizeStep(s))
			for _, f := range s.From {
				required[f] = true
			}
		}
		for _, g := range detail.Goals {
			if g.Status == "active" {
				goalIDs[g.ID] = true
				query = append(query, g.Description)
			}
		}
	}
	flags, err := d.ListFlags(ctx, project)
	if err != nil {
		return "", err
	}
	for _, flag := range flags {
		out.Flags = append(out.Flags, modelFlag{ID: flag.ID, ChallengeID: flag.ChallengeID, Status: flag.Status, FactID: flag.FactID})
	}
	// Include the execution goal's ancestry and all evidence referenced by goals.
	for changed := true; changed; {
		changed = false
		for _, g := range detail.Goals {
			if goalIDs[g.ID] && g.ParentGoalID != nil && !goalIDs[*g.ParentGoalID] {
				goalIDs[*g.ParentGoalID] = true
				changed = true
			}
		}
	}
	for _, g := range detail.Goals {
		if goalIDs[g.ID] {
			out.Goals = append(out.Goals, g)
			for _, f := range g.From {
				required[f] = true
			}
		}
	}
	for _, f := range detail.Facts {
		if required[f.ID] {
			out.RequiredFacts = append(out.RequiredFacts, summarizeFact(f, true))
		}
	}
	for i := len(detail.Facts) - 1; i >= 0 && len(out.RecentFacts) < 8; i-- {
		f := detail.Facts[i]
		if !required[f.ID] {
			out.RecentFacts = append(out.RecentFacts, summarizeFact(f, false))
		}
	}
	out.Omitted["facts"] = len(detail.Facts) - len(out.RequiredFacts) - len(out.RecentFacts)
	for _, r := range detail.Routes {
		if phase == "decide" || routeIDs[r.ID] {
			out.Routes = append(out.Routes, r)
		}
	}
	events, err := d.PendingDecideEvents(ctx, project)
	if err != nil {
		return "", err
	}
	if len(events) > 12 {
		events = events[len(events)-12:]
	}
	out.Events = events
	out.History, err = d.SearchHistory(ctx, project, strings.Join(query, " "), 6)
	if err != nil {
		return "", err
	}
	if phase == "execute" {
		filtered := out.History[:0]
		for _, m := range out.History {
			if m.Kind != "step" || m.ID != stepID {
				filtered = append(filtered, m)
			}
		}
		out.History = filtered
	}
	if len(query) > 0 {
		out.Experiences, err = d.SearchExperiences(ctx, strings.Join(query, " "), "", 3)
		if err != nil {
			return "", err
		}
	}
	for i := range out.Experiences {
		e := &out.Experiences[i]
		e.Title = brief(e.Title, 100)
		e.Conditions = brief(e.Conditions, 320)
		e.Method = brief(e.Method, 480)
		e.FailureConditions = brief(e.FailureConditions, 320)
	}
	sort.Slice(out.RequiredFacts, func(i, j int) bool { return out.RequiredFacts[i].ID < out.RequiredFacts[j].ID })
	b, err := yaml.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("marshal model context: %w", err)
	}
	return string(b), nil
}
