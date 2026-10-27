package domain

import "time"

const (
	NodeKindFact     = "fact"
	NodeKindGoal     = "goal"
	ProjectOpen      = "open"
	ProjectCompleted = "completed"
)

type Project struct {
	ID          uint       `json:"id" gorm:"primaryKey"`
	Name        string     `json:"name" gorm:"not null;check:project_name_required,length(trim(name)) > 0"`
	Description string     `json:"description,omitempty" gorm:"type:text"`
	Status      string     `json:"status" gorm:"not null;check:project_state,(status = 'open' AND completed_at IS NULL) OR (status = 'completed' AND completed_at IS NOT NULL)"`
	CreatedAt   time.Time  `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type Node struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	ProjectID uint      `json:"project_id" gorm:"not null;index:idx_nodes_project_kind"`
	Kind      string    `json:"kind" gorm:"not null;index:idx_nodes_project_kind;check:node_kind,kind IN ('fact','goal')"`
	Content   string    `json:"content" gorm:"not null;type:text;check:node_content_required,length(trim(content)) > 0"`
	CreatedAt time.Time `json:"created_at"`
}

type Step struct {
	ID          uint      `json:"id" gorm:"primaryKey"`
	ProjectID   uint      `json:"project_id" gorm:"not null;index"`
	FromNodeID  uint      `json:"from_node_id" gorm:"not null;check:step_distinct_nodes,from_node_id <> to_node_id"`
	ToNodeID    uint      `json:"to_node_id" gorm:"not null;index"`
	Description string    `json:"description,omitempty" gorm:"not null;default:'';type:text"`
	CreatedAt   time.Time `json:"created_at"`
}

type Graph struct {
	Project
	Goal  Node   `json:"goal"`
	Facts []Node `json:"facts"`
	Steps []Step `json:"steps"`
}

// Settings is the singleton row (ID 1) holding LLM connection details.
// The graph tables stay append-only; settings are the only mutable record.
type Settings struct {
	ID        uint       `json:"-" gorm:"primaryKey;check:settings_singleton,id = 1"`
	LLMURL    string     `json:"llm_url" gorm:"not null;check:settings_llm_url_required,length(trim(llm_url)) > 0"`
	APIKey    string     `json:"api_key" gorm:"not null"`
	Model     string     `json:"model" gorm:"not null;check:settings_model_required,length(trim(model)) > 0"`
	MaxTokens int        `json:"max_tokens" gorm:"not null;check:settings_max_tokens,max_tokens BETWEEN 1 AND 10000000"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}
