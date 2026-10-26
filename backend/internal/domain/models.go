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
	ID         uint      `json:"id" gorm:"primaryKey"`
	ProjectID  uint      `json:"project_id" gorm:"not null;index"`
	FromNodeID uint      `json:"from_node_id" gorm:"not null;check:step_distinct_nodes,from_node_id <> to_node_id"`
	ToNodeID   uint      `json:"to_node_id" gorm:"not null;index"`
	CreatedAt  time.Time `json:"created_at"`
}

type Graph struct {
	Project
	Goal  Node   `json:"goal"`
	Facts []Node `json:"facts"`
	Steps []Step `json:"steps"`
}
