package core

type Fact struct {
	ID          string `json:"id" yaml:"id"`
	Description string `json:"description" yaml:"description"`
	Title       string `json:"title,omitempty" yaml:"title,omitempty"`
	Detail      string `json:"detail,omitempty" yaml:"detail,omitempty"`
	ResultType  string `json:"result_type,omitempty" yaml:"result_type,omitempty"`
}

type Goal struct {
	ID                    string   `json:"id" yaml:"id"`
	Description           string   `json:"description" yaml:"description"`
	ParentGoalID          *string  `json:"parent_goal_id" yaml:"parent_goal_id"`
	Status                string   `json:"status" yaml:"status"`
	Priority              int      `json:"priority" yaml:"priority"`
	CreatedAt             string   `json:"created_at" yaml:"created_at"`
	CompletedAt           *string  `json:"completed_at" yaml:"completed_at"`
	CompletionDescription *string  `json:"completion_description" yaml:"completion_description"`
	CompletedBy           *string  `json:"completed_by" yaml:"completed_by"`
	From                  []string `json:"from" yaml:"from"`
}

type Step struct {
	ID                  string           `json:"id" yaml:"id"`
	From                []string         `json:"from" yaml:"from"`
	To                  *string          `json:"to" yaml:"to"`
	Description         string           `json:"description" yaml:"description"`
	GoalID              *string          `json:"goal_id" yaml:"goal_id"`
	Priority            int              `json:"priority" yaml:"priority"`
	Creator             string           `json:"creator" yaml:"creator"`
	Worker              *string          `json:"worker" yaml:"worker"`
	LastHeartbeatAt     *string          `json:"last_heartbeat_at" yaml:"last_heartbeat_at"`
	CreatedAt           string           `json:"created_at" yaml:"created_at"`
	ConcludedAt         *string          `json:"concluded_at" yaml:"concluded_at"`
	Abandoned           bool             `json:"abandoned" yaml:"abandoned"`
	RouteID             *string          `json:"route_id,omitempty" yaml:"route_id,omitempty"`
	Result              *ExecutionResult `json:"result,omitempty" yaml:"result,omitempty"`
	LLMResponse         string           `json:"llm_response,omitempty" yaml:"llm_response,omitempty"`
	LLMResponseRecorded bool             `json:"llm_response_recorded" yaml:"llm_response_recorded,omitempty"`
	LLMResponseError    string           `json:"llm_response_error,omitempty" yaml:"llm_response_error,omitempty"`
}

type Finding struct {
	ID          string         `json:"id" yaml:"id"`
	Title       string         `json:"title" yaml:"title"`
	Description string         `json:"description" yaml:"description"`
	Severity    string         `json:"severity" yaml:"severity"`
	Kind        string         `json:"kind" yaml:"kind"`
	Data        map[string]any `json:"data" yaml:"data"`
	FactID      *string        `json:"fact_id" yaml:"fact_id"`
	CreatedAt   string         `json:"created_at" yaml:"created_at"`
}

type Hint struct {
	ID        string `json:"id" yaml:"id"`
	Content   string `json:"content" yaml:"content"`
	Creator   string `json:"creator" yaml:"creator"`
	CreatedAt string `json:"created_at" yaml:"created_at"`
}

type DecideLease struct {
	Worker          string `json:"worker" yaml:"worker"`
	Trigger         string `json:"trigger" yaml:"trigger"`
	StartedAt       string `json:"started_at" yaml:"started_at"`
	LastHeartbeatAt string `json:"last_heartbeat_at" yaml:"last_heartbeat_at"`
}

type Project struct {
	ID               string       `json:"id" yaml:"id"`
	Title            string       `json:"title" yaml:"title"`
	Status           string       `json:"status" yaml:"status"`
	BootstrapEnabled bool         `json:"bootstrap_enabled" yaml:"bootstrap_enabled"`
	MaxWorkers       int          `json:"max_workers" yaml:"max_workers"`
	CreatedAt        string       `json:"created_at" yaml:"created_at"`
	Decide           *DecideLease `json:"decide" yaml:"decide"`
}

type ProjectSummary struct {
	Project
	FactCount            int `json:"fact_count"`
	IntentCount          int `json:"intent_count"`
	WorkingIntentCount   int `json:"working_intent_count"`
	UnclaimedIntentCount int `json:"unclaimed_intent_count"`
	StepCount            int `json:"step_count"`
	WorkingStepCount     int `json:"working_step_count"`
	UnclaimedStepCount   int `json:"unclaimed_step_count"`
	GoalCount            int `json:"goal_count"`
	FindingCount         int `json:"finding_count"`
	HintCount            int `json:"hint_count"`
}

type ProjectDetail struct {
	Project  Project   `json:"project" yaml:"project"`
	Facts    []Fact    `json:"facts" yaml:"facts"`
	Steps    []Step    `json:"steps" yaml:"steps"`
	Goals    []Goal    `json:"goals" yaml:"goals"`
	Findings []Finding `json:"findings" yaml:"findings"`
	Hints    []Hint    `json:"hints" yaml:"hints"`
	Routes   []Route   `json:"routes" yaml:"routes"`
}

type CreateProject struct {
	Title      string       `json:"title"`
	Origin     string       `json:"origin"`
	Goal       string       `json:"goal"`
	Hints      []CreateHint `json:"hints"`
	MaxWorkers int          `json:"max_workers,omitempty"`
}
type CreateHint struct {
	Content string `json:"content"`
	Creator string `json:"creator"`
}
type CreateGoal struct {
	Description  string  `json:"description"`
	ParentGoalID *string `json:"parent_goal_id"`
	Priority     int     `json:"priority"`
	Creator      string  `json:"creator"`
}
type UpdateGoal struct {
	Priority    *int    `json:"priority"`
	Description *string `json:"description"`
	// Status is retained only to reject the old client's attempt to complete a
	// goal through PATCH. Completion must go through the evidence-bearing route.
	Status *string `json:"status"`
}
type CreateStep struct {
	From        []string `json:"from"`
	Description string   `json:"description"`
	GoalID      *string  `json:"goal_id"`
	Priority    int      `json:"priority"`
	Creator     string   `json:"creator"`
	Worker      *string  `json:"worker"`
	RouteID     *string  `json:"route_id"`
}
type UpdateStep struct {
	Priority  *int    `json:"priority"`
	GoalID    *string `json:"goal_id"`
	Abandoned *bool   `json:"abandoned"`
	Status    *string `json:"status"`
}
type CreateFinding struct {
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Severity    string         `json:"severity"`
	Kind        string         `json:"kind"`
	Data        map[string]any `json:"data"`
	FactID      *string        `json:"fact_id"`
}
type Heartbeat struct {
	Worker string `json:"worker"`
}
type ClaimDecide struct {
	Worker  string `json:"worker"`
	Trigger string `json:"trigger"`
}
type Conclude struct {
	Worker      string           `json:"worker"`
	Description string           `json:"description"`
	Finding     *CreateFinding   `json:"finding"`
	Title       string           `json:"title"`
	Result      *ExecutionResult `json:"result"`
}
type Completion struct {
	From        []string `json:"from"`
	Description string   `json:"description"`
	Worker      string   `json:"worker"`
}
type Reopen struct {
	Description string `json:"description"`
	Creator     string `json:"creator"`
}
type ConcludeResult struct {
	Fact    *Fact    `json:"fact"`
	Step    *Step    `json:"step"`
	Finding *Finding `json:"finding"`
}
type ReopenResult struct {
	Project Project `json:"project"`
	Fact    Fact    `json:"fact"`
	Step    Step    `json:"step"`
	Goal    *Goal   `json:"goal"`
}

type Settings struct {
	StepTimeout                      int `json:"step_timeout"`
	DecideTimeout                    int `json:"decide_timeout"`
	WorkerUnhealthyRetryAfterSeconds int `json:"worker_unhealthy_retry_after_seconds"`
	WorkerRejectedRetryAfterSeconds  int `json:"worker_rejected_retry_after_seconds"`
	MaxFailedLoginAttempts           int `json:"max_failed_login_attempts"`
	RateLimitWindowMinutes           int `json:"rate_limit_window_minutes"`
	SessionDurationHours             int `json:"session_duration_hours"`
	LogRetentionDays                 int `json:"log_retention_days"`
	ExportRetentionDays              int `json:"export_retention_days"`
	NotificationRetentionDays        int `json:"notification_retention_days"`
	ProjectIdleAlertHours            int `json:"project_idle_alert_hours"`
}
