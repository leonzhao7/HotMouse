package core

// ExecutionResult describes what was learned independently of any Finding.
// Unclassified preserves old clients without interpreting arbitrary prose as progress.
type ExecutionResult struct {
	Type     string         `json:"type" yaml:"type"`
	Detail   string         `json:"detail,omitempty" yaml:"detail,omitempty"`
	Evidence map[string]any `json:"evidence,omitempty" yaml:"evidence,omitempty"`
}

func (r ExecutionResult) Validate() error {
	switch r.Type {
	case "progress", "negative", "blocked", "environment_failure", "no_information", "needs_attention", "unclassified":
		return nil
	}
	return requireValid("result type", r.Type)
}

type Route struct {
	ID                      string  `json:"id" yaml:"id"`
	Title                   string  `json:"title" yaml:"title"`
	GoalID                  *string `json:"goal_id" yaml:"goal_id"`
	Status                  string  `json:"status" yaml:"status"`
	BudgetSeconds           int     `json:"budget_seconds" yaml:"budget_seconds"`
	NoProgressLimit         int     `json:"no_progress_limit" yaml:"no_progress_limit"`
	InvestedMilliseconds    int64   `json:"invested_ms" yaml:"invested_ms"`
	ResultCount             int     `json:"result_count" yaml:"result_count"`
	ProgressCount           int     `json:"progress_count" yaml:"progress_count"`
	NegativeCount           int     `json:"negative_count" yaml:"negative_count"`
	NoProgressCount         int     `json:"no_progress_count" yaml:"no_progress_count"`
	EnvironmentFailureCount int     `json:"environment_failure_count" yaml:"environment_failure_count"`
	ShelvedReason           string  `json:"shelved_reason" yaml:"shelved_reason"`
	ReopenCondition         string  `json:"reopen_condition" yaml:"reopen_condition"`
	NextDirection           string  `json:"next_direction" yaml:"next_direction"`
	CreatedAt               string  `json:"created_at" yaml:"created_at"`
}
type CreateRoute struct {
	Title           string  `json:"title"`
	GoalID          *string `json:"goal_id"`
	BudgetSeconds   *int    `json:"budget_seconds"`
	NoProgressLimit *int    `json:"no_progress_limit"`
}
type UpdateRoute struct {
	Status          *string `json:"status"`
	BudgetSeconds   *int    `json:"budget_seconds"`
	NoProgressLimit *int    `json:"no_progress_limit"`
	Reason          string  `json:"reason"`
	ReopenCondition string  `json:"reopen_condition"`
	NextDirection   string  `json:"next_direction"`
}
type RouteRecord struct {
	ID              int64  `json:"id" yaml:"id"`
	RouteID         string `json:"route_id" yaml:"route_id"`
	Action          string `json:"action" yaml:"action"`
	Reason          string `json:"reason" yaml:"reason"`
	ReopenCondition string `json:"reopen_condition" yaml:"reopen_condition"`
	NextDirection   string `json:"next_direction" yaml:"next_direction"`
	CreatedAt       string `json:"created_at" yaml:"created_at"`
}
type DecideEvent struct {
	ID        int64  `json:"id" yaml:"id"`
	Reason    string `json:"reason" yaml:"reason"`
	SourceID  string `json:"source_id" yaml:"source_id"`
	CreatedAt string `json:"created_at" yaml:"created_at"`
}
