package core

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	"hotmouse/backend/internal/ctfplatform"
	"hotmouse/backend/internal/knowledge"
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
	ID           int64  `yaml:"id"`
	ChallengeID  string `yaml:"challenge_id"`
	Status       string `yaml:"status"`
	FactID       string `yaml:"fact_id"`
	LastResponse string `yaml:"last_platform_response,omitempty"`
}

type modelPlatform struct {
	Name         string                `yaml:"name"`
	ChallengeID  string                `yaml:"challenge_id"`
	SnapshotNote string                `yaml:"snapshot_note"`
	Challenge    ctfplatform.Challenge `yaml:"challenge"`
	Delivery     ctfplatform.Delivery  `yaml:"delivery"`
}

type modelWorkingState struct {
	FactID     string         `yaml:"fact_id"`
	StepID     string         `yaml:"step_id"`
	ResultType string         `yaml:"result_type"`
	Summary    string         `yaml:"summary"`
	Evidence   map[string]any `yaml:"evidence,omitempty"`
}

// Keep every action available to the planner, without its verbose evidence.
// Keyword recall alone cannot reliably prevent repeating old work.
type modelHistoricalStep struct {
	ID          string   `yaml:"id"`
	From        []string `yaml:"from"`
	Description string   `yaml:"description"`
	Status      string   `yaml:"status"`
	To          *string  `yaml:"to,omitempty"`
	ResultType  string   `yaml:"result_type,omitempty"`
}

type modelContext struct {
	Platform       *modelPlatform        `yaml:"platform,omitempty"`
	WorkingState   []modelWorkingState   `yaml:"working_state,omitempty"`
	TargetAnalysis *TargetProbeAnalysis  `yaml:"target_analysis,omitempty"`
	TargetHealth   map[string]any        `yaml:"target_health"`
	Preprocessing  PreprocessingState    `yaml:"preprocessing"`
	Flags          []modelFlag           `yaml:"flags,omitempty"`
	SchemaVersion  int                   `yaml:"schema_version"`
	Phase          string                `yaml:"phase"`
	Project        map[string]any        `yaml:"project"`
	Goals          []Goal                `yaml:"goals"`
	CurrentStep    *modelStep            `yaml:"current_step,omitempty"`
	OpenSteps      []modelStep           `yaml:"open_steps,omitempty"`
	StepHistory    []modelHistoricalStep `yaml:"step_history,omitempty"`
	RequiredFacts  []modelFact           `yaml:"required_facts"`
	RecentFacts    []modelFact           `yaml:"recent_facts,omitempty"`
	Hints          []Hint                `yaml:"hints,omitempty"`
	Routes         []Route               `yaml:"routes,omitempty"`
	Events         []DecideEvent         `yaml:"decide_events,omitempty"`
	History        []HistoryMatch        `yaml:"related_history,omitempty"`
	Experiences    []Experience          `yaml:"experiences,omitempty"`
	Knowledge      []knowledge.Summary   `yaml:"related_knowledge,omitempty"`
	DetailAccess   map[string]string     `yaml:"detail_access"`
	Omitted        map[string]int        `yaml:"omitted"`
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
	out := modelContext{TargetAnalysis: detail.TargetProbe.Analysis, Preprocessing: detail.TargetProbe.Preprocessing, SchemaVersion: 1, Phase: phase, Project: map[string]any{"id": project, "title": detail.Project.Title, "status": detail.Project.Status, "budget_seconds": detail.Project.BudgetSeconds, "budget_spent_ms": detail.Project.BudgetSpentMilliseconds, "invested_ms": detail.Project.InvestedMilliseconds}, Goals: []Goal{}, RequiredFacts: []modelFact{}, Hints: detail.Hints,
		DetailAccess: map[string]string{
			"fact":       "GET /projects/" + project + "/facts/{id}; hotmouse --project " + project + " fact show {id}",
			"step":       "GET /projects/" + project + "/steps/{id}; hotmouse --project " + project + " step show {id}",
			"flags":      "GET /projects/" + project + "/flags; hotmouse --project " + project + " flag list",
			"finding":    "GET /projects/" + project + "/findings/{id}; hotmouse --project " + project + " finding show {id}",
			"history":    "GET /projects/" + project + "/history?q={query}; hotmouse --project " + project + " project recall --query {query}",
			"experience": "GET /api/experiences/{id}; hotmouse experience show {id}",
			"knowledge":  "GET /api/knowledge/entries/{id}; hotmouse kb show {id} --output json; references only, not project evidence",
			"kb_search":  "GET /api/knowledge/entries?q={query}; hotmouse kb search {query} --output json",
		}, Omitted: map[string]int{"closed_steps": 0, "facts": 0, "findings": len(detail.Findings)}}
	out.TargetHealth = map[string]any{"status": detail.TargetProbe.Status, "last_checked_at": detail.TargetProbe.LastCheckedAt, "last_error": detail.TargetProbe.LastError, "consecutive_failures": detail.TargetProbe.ConsecutiveFailures}
	if detail.Platform != nil {
		binding := detail.Platform
		out.Platform = &modelPlatform{Name: binding.Platform, ChallengeID: binding.ChallengeID, SnapshotNote: "Delivery is the current platform target snapshot and overrides historical addresses; use flags.last_platform_response for subsequent submission progress"}
		if err := json.Unmarshal(binding.Challenge, &out.Platform.Challenge); err != nil {
			return "", err
		}
		if err := json.Unmarshal(binding.Delivery, &out.Platform.Delivery); err != nil {
			return "", err
		}
	}
	// Structured evidence supplies saved entry points, artifacts, protocol,
	// verified observations and the remaining hypothesis without a status reread.
	for i := len(detail.Steps) - 1; i >= 0 && len(out.WorkingState) < 6; i-- {
		s := detail.Steps[i]
		if s.To == nil || s.Result == nil {
			continue
		}
		state := modelWorkingState{FactID: *s.To, StepID: s.ID, ResultType: s.Result.Type, Evidence: map[string]any{}}
		for _, f := range detail.Facts {
			if f.ID == *s.To {
				state.Summary = brief(f.Description, 320)
				break
			}
		}
		for _, key := range []string{"entry_points", "artifacts", "protocol", "verified", "remaining_hypothesis", "next_check", "knowledge_refs", "python", "capabilities_path"} {
			if value, ok := s.Result.Evidence[key]; ok {
				encoded, _ := json.Marshal(value)
				if len(encoded) <= 1600 {
					state.Evidence[key] = value
				} else {
					state.Evidence[key] = "Read step " + s.ID + " detail for " + key
				}
			}
		}
		out.WorkingState = append(out.WorkingState, state)
	}
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
	required := map[string]bool{"origin": true, "competition_target": true}
	if id := detail.TargetProbe.Preprocessing.FactID; id != "" {
		required[id] = true
	}
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
			status := "pending"
			if s.Worker != nil {
				status = "working"
			}
			if s.Abandoned {
				status = "abandoned"
			}
			if s.To != nil {
				status = "completed"
			}
			if recovery := recoveries[s.ID]; recovery != "" && s.To == nil {
				status = recovery
			}
			history := modelHistoricalStep{ID: s.ID, From: s.From, Description: s.Description, Status: status, To: s.To}
			if s.Result != nil {
				history.ResultType = s.Result.Type
			}
			out.StepHistory = append(out.StepHistory, history)
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
		view := modelFlag{ID: flag.ID, ChallengeID: flag.ChallengeID, Status: flag.Status, FactID: flag.FactID}
		if len(flag.Attempts) > 0 {
			view.LastResponse = brief(flag.Attempts[len(flag.Attempts)-1].Response, 1000)
		}
		out.Flags = append(out.Flags, view)
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
	catalog, err := knowledge.Load()
	if err != nil {
		return "", fmt.Errorf("load bundled knowledge: %w", err)
	}
	knowledgeQuery := append([]string{detail.Project.Title}, query...)
	for _, f := range out.RequiredFacts {
		knowledgeQuery = append(knowledgeQuery, f.Title, brief(f.Description, 600))
	}
	if phase == "decide" {
		for _, f := range out.RecentFacts {
			knowledgeQuery = append(knowledgeQuery, f.Title, f.Description)
		}
	}
	out.Knowledge = catalog.Recall(strings.Join(knowledgeQuery, " "), 3)
	sort.Slice(out.RequiredFacts, func(i, j int) bool { return out.RequiredFacts[i].ID < out.RequiredFacts[j].ID })
	b, err := yaml.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("marshal model context: %w", err)
	}
	return string(b), nil
}
