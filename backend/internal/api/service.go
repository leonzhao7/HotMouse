package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"hotmouse/backend/internal/domain"
)

var (
	ErrNotFound        = errors.New("project or node not found")
	ErrInvalid         = errors.New("invalid project graph")
	ErrConflict        = errors.New("a step already exists between these nodes")
	ErrGraphChanged    = errors.New("project graph changed while dispatch was running")
	ErrGoalUnreachable = errors.New("at least one step must point to the goal before completion")
	ErrProjectClosed   = errors.New("completed projects cannot accept new graph data")
)

type Service struct {
	db                    *gorm.DB
	projectChangedMu      sync.RWMutex
	projectChangedHandler func(uint)
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

func (service *Service) SetProjectChangedHandler(handler func(uint)) {
	service.projectChangedMu.Lock()
	defer service.projectChangedMu.Unlock()
	service.projectChangedHandler = handler
}

func (service *Service) notifyProjectChanged(projectID uint) {
	service.projectChangedMu.RLock()
	handler := service.projectChangedHandler
	service.projectChangedMu.RUnlock()
	if handler != nil {
		handler(projectID)
	}
}

type DispatchStep struct {
	FromFactIndex int    `json:"from_fact_index"`
	ToFactIndex   *int   `json:"to_fact_index,omitempty"`
	ToGoal        bool   `json:"to_goal"`
	Description   string `json:"description"`
}

type DispatchResult struct {
	Facts       []string
	Steps       []DispatchStep
	GoalReached bool
}

type CreateProjectInput struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Goal        string   `json:"goal"`
	Facts       []string `json:"facts"`
}

func (service *Service) CreateProject(ctx context.Context, input CreateProjectInput) (domain.Graph, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Goal = strings.TrimSpace(input.Goal)
	input.Description = strings.TrimSpace(input.Description)
	if input.Name == "" || input.Goal == "" || len(input.Facts) == 0 {
		return domain.Graph{}, fmt.Errorf("%w: name, goal, and at least one fact are required", ErrInvalid)
	}
	facts := make([]domain.Node, len(input.Facts))
	for index, content := range input.Facts {
		content = strings.TrimSpace(content)
		if content == "" {
			return domain.Graph{}, fmt.Errorf("%w: fact content cannot be empty", ErrInvalid)
		}
		facts[index] = domain.Node{Kind: domain.NodeKindFact, Content: content}
	}
	graph := domain.Graph{
		Project: domain.Project{Name: input.Name, Description: input.Description, Status: domain.ProjectOpen},
		Goal:    domain.Node{Kind: domain.NodeKindGoal, Content: input.Goal},
		Facts:   facts,
		Steps:   make([]domain.Step, 0),
	}
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&graph.Project).Error; err != nil {
			return err
		}
		graph.Goal.ProjectID = graph.ID
		if err := tx.Create(&graph.Goal).Error; err != nil {
			return err
		}
		for index := range graph.Facts {
			graph.Facts[index].ProjectID = graph.ID
			if err := tx.Create(&graph.Facts[index]).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return domain.Graph{}, err
	}
	service.notifyProjectChanged(graph.ID)
	return graph, nil
}

func (service *Service) ListProjects(ctx context.Context) ([]domain.Project, error) {
	projects := make([]domain.Project, 0)
	err := service.db.WithContext(ctx).Order("id ASC").Find(&projects).Error
	return projects, err
}

func (service *Service) ListLLMLogs(ctx context.Context) ([]domain.LLMLog, error) {
	logs := make([]domain.LLMLog, 0)
	err := service.db.WithContext(ctx).Order("id DESC").Limit(200).Find(&logs).Error
	return logs, err
}

func (service *Service) GetGoal(ctx context.Context, projectID uint) (domain.Node, error) {
	var goal domain.Node
	err := service.db.WithContext(ctx).Where("project_id = ? AND kind = ?", projectID, domain.NodeKindGoal).First(&goal).Error
	return goal, mapNotFound(err)
}

func (service *Service) ExportProject(ctx context.Context, projectID uint) (domain.Graph, error) {
	graph := domain.Graph{
		Facts: make([]domain.Node, 0),
		Steps: make([]domain.Step, 0),
	}
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&graph.Project, projectID).Error; err != nil {
			return mapNotFound(err)
		}
		if err := tx.Where("project_id = ? AND kind = ?", projectID, domain.NodeKindGoal).First(&graph.Goal).Error; err != nil {
			return mapNotFound(err)
		}
		if err := tx.Where("project_id = ? AND kind = ?", projectID, domain.NodeKindFact).Order("id ASC").Find(&graph.Facts).Error; err != nil {
			return err
		}
		return tx.Where("project_id = ?", projectID).Order("id ASC").Find(&graph.Steps).Error
	})
	if err != nil {
		return domain.Graph{}, err
	}
	return graph, nil
}

func (service *Service) ApplyDispatchResult(ctx context.Context, expected domain.Graph, result DispatchResult) (domain.Graph, bool, error) {
	for index := range result.Facts {
		result.Facts[index] = strings.TrimSpace(result.Facts[index])
		if result.Facts[index] == "" {
			return domain.Graph{}, false, fmt.Errorf("%w: dispatched facts cannot be empty", ErrInvalid)
		}
	}
	if len(result.Facts) > 20 || len(result.Steps) > 100 {
		return domain.Graph{}, false, fmt.Errorf("%w: dispatch result exceeds graph limits", ErrInvalid)
	}

	var changed bool
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current domain.Graph
		current.Facts = make([]domain.Node, 0)
		current.Steps = make([]domain.Step, 0)
		if err := tx.First(&current.Project, expected.ID).Error; err != nil {
			return mapNotFound(err)
		}
		if current.Status != domain.ProjectOpen {
			return ErrProjectClosed
		}
		if err := tx.Where("project_id = ? AND kind = ?", expected.ID, domain.NodeKindGoal).First(&current.Goal).Error; err != nil {
			return mapNotFound(err)
		}
		if err := tx.Where("project_id = ? AND kind = ?", expected.ID, domain.NodeKindFact).Order("id ASC").Find(&current.Facts).Error; err != nil {
			return err
		}
		if err := tx.Where("project_id = ?", expected.ID).Order("id ASC").Find(&current.Steps).Error; err != nil {
			return err
		}
		if !sameGraphVersion(expected, current) {
			return ErrGraphChanged
		}

		for index := range result.Facts {
			fact := domain.Node{ProjectID: expected.ID, Kind: domain.NodeKindFact, Content: result.Facts[index]}
			if err := tx.Create(&fact).Error; err != nil {
				return err
			}
			current.Facts = append(current.Facts, fact)
			changed = true
		}

		knownPairs := make(map[[2]uint]struct{}, len(current.Steps)+len(result.Steps))
		for _, step := range current.Steps {
			knownPairs[nodePair(step.FromNodeID, step.ToNodeID)] = struct{}{}
		}
		for _, candidate := range result.Steps {
			if candidate.FromFactIndex < 0 || candidate.FromFactIndex >= len(current.Facts) || (candidate.ToGoal == (candidate.ToFactIndex != nil)) {
				return fmt.Errorf("%w: dispatched steps require valid fact indexes and one destination", ErrInvalid)
			}
			fromNodeID := current.Facts[candidate.FromFactIndex].ID
			toNodeID := current.Goal.ID
			if candidate.ToFactIndex != nil {
				if *candidate.ToFactIndex < 0 || *candidate.ToFactIndex >= len(current.Facts) {
					return fmt.Errorf("%w: dispatched step destination index is out of range", ErrInvalid)
				}
				toNodeID = current.Facts[*candidate.ToFactIndex].ID
			}
			if fromNodeID == toNodeID {
				return fmt.Errorf("%w: dispatched steps cannot connect a node to itself", ErrInvalid)
			}
			pair := nodePair(fromNodeID, toNodeID)
			if _, exists := knownPairs[pair]; exists {
				continue
			}
			step := domain.Step{
				ProjectID:   expected.ID,
				FromNodeID:  fromNodeID,
				ToNodeID:    toNodeID,
				Description: strings.TrimSpace(candidate.Description),
			}
			if err := tx.Create(&step).Error; err != nil {
				return err
			}
			knownPairs[pair] = struct{}{}
			changed = true
		}

		if result.GoalReached {
			var incoming int64
			if err := tx.Table("steps").Joins("JOIN nodes ON nodes.id = steps.to_node_id").
				Where("steps.project_id = ? AND nodes.project_id = ? AND nodes.kind = ?", expected.ID, expected.ID, domain.NodeKindGoal).
				Count(&incoming).Error; err != nil {
				return err
			}
			if incoming > 0 {
				completedAt := time.Now().UTC()
				if err := tx.Model(&current.Project).Updates(map[string]any{
					"status":       domain.ProjectCompleted,
					"completed_at": completedAt,
				}).Error; err != nil {
					return err
				}
				changed = true
			}
		}
		return nil
	})
	if err != nil {
		return domain.Graph{}, false, err
	}
	graph, err := service.ExportProject(ctx, expected.ID)
	return graph, changed, err
}

func (service *Service) ListFacts(ctx context.Context, projectID uint) ([]domain.Node, error) {
	if err := service.ensureProject(ctx, projectID); err != nil {
		return nil, err
	}
	facts := make([]domain.Node, 0)
	err := service.db.WithContext(ctx).Where("project_id = ? AND kind = ?", projectID, domain.NodeKindFact).Order("id ASC").Find(&facts).Error
	return facts, err
}

func (service *Service) AddFact(ctx context.Context, projectID uint, content string) (domain.Node, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return domain.Node{}, fmt.Errorf("%w: fact content is required", ErrInvalid)
	}
	fact := domain.Node{ProjectID: projectID, Kind: domain.NodeKindFact, Content: content}
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := ensureOpenProject(tx, projectID); err != nil {
			return err
		}
		return tx.Create(&fact).Error
	})
	if err != nil {
		return domain.Node{}, err
	}
	service.notifyProjectChanged(projectID)
	return fact, nil
}

func (service *Service) ListSteps(ctx context.Context, projectID uint) ([]domain.Step, error) {
	if err := service.ensureProject(ctx, projectID); err != nil {
		return nil, err
	}
	steps := make([]domain.Step, 0)
	err := service.db.WithContext(ctx).Where("project_id = ?", projectID).Order("id ASC").Find(&steps).Error
	return steps, err
}

func (service *Service) AddStep(ctx context.Context, projectID, fromNodeID, toNodeID uint, description string) (domain.Step, error) {
	maxID := ^uint(0) >> 1
	if fromNodeID == 0 || toNodeID == 0 || fromNodeID == toNodeID || fromNodeID > maxID || toNodeID > maxID {
		return domain.Step{}, fmt.Errorf("%w: a step requires two distinct positive node IDs", ErrInvalid)
	}
	step := domain.Step{
		ProjectID:   projectID,
		FromNodeID:  fromNodeID,
		ToNodeID:    toNodeID,
		Description: strings.TrimSpace(description),
	}
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := ensureOpenProject(tx, projectID); err != nil {
			return err
		}
		var fromNode, toNode domain.Node
		if err := tx.Where("id = ? AND project_id = ?", fromNodeID, projectID).First(&fromNode).Error; err != nil {
			return mapNotFound(err)
		}
		if err := tx.Where("id = ? AND project_id = ?", toNodeID, projectID).First(&toNode).Error; err != nil {
			return mapNotFound(err)
		}
		if fromNode.Kind != domain.NodeKindFact {
			return fmt.Errorf("%w: goal nodes cannot be step origins", ErrInvalid)
		}
		if err := tx.Create(&step).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return ErrConflict
			}
			return err
		}
		return nil
	})
	if err != nil {
		return domain.Step{}, err
	}
	service.notifyProjectChanged(projectID)
	return step, nil
}

func (service *Service) CompleteProject(ctx context.Context, projectID uint) (domain.Project, error) {
	var project domain.Project
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&project, projectID).Error; err != nil {
			return mapNotFound(err)
		}
		if project.Status == domain.ProjectCompleted {
			return nil
		}
		var incoming int64
		if err := tx.Table("steps").Joins("JOIN nodes ON nodes.id = steps.to_node_id").
			Where("steps.project_id = ? AND nodes.project_id = ? AND nodes.kind = ?", projectID, projectID, domain.NodeKindGoal).
			Count(&incoming).Error; err != nil {
			return err
		}
		if incoming == 0 {
			return ErrGoalUnreachable
		}
		completedAt := time.Now().UTC()
		if err := tx.Model(&project).Updates(map[string]any{
			"status":       domain.ProjectCompleted,
			"completed_at": completedAt,
		}).Error; err != nil {
			return err
		}
		project.Status = domain.ProjectCompleted
		project.CompletedAt = &completedAt
		return nil
	})
	if err != nil {
		return domain.Project{}, err
	}
	service.notifyProjectChanged(projectID)
	return project, nil
}

func (service *Service) ensureProject(ctx context.Context, projectID uint) error {
	var project domain.Project
	return mapNotFound(service.db.WithContext(ctx).First(&project, projectID).Error)
}

func ensureOpenProject(db *gorm.DB, projectID uint) error {
	var project domain.Project
	if err := db.First(&project, projectID).Error; err != nil {
		return mapNotFound(err)
	}
	if project.Status != domain.ProjectOpen {
		return ErrProjectClosed
	}
	return nil
}

func mapNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}

func sameGraphVersion(expected, current domain.Graph) bool {
	if expected.ID != current.ID || expected.Project.Status != current.Project.Status || expected.Goal.ID != current.Goal.ID || len(expected.Facts) != len(current.Facts) || len(expected.Steps) != len(current.Steps) {
		return false
	}
	for index := range expected.Facts {
		if expected.Facts[index].ID != current.Facts[index].ID || expected.Facts[index].Content != current.Facts[index].Content {
			return false
		}
	}
	for index := range expected.Steps {
		if expected.Steps[index].ID != current.Steps[index].ID {
			return false
		}
	}
	return true
}

func nodePair(fromNodeID, toNodeID uint) [2]uint {
	if fromNodeID > toNodeID {
		return [2]uint{toNodeID, fromNodeID}
	}
	return [2]uint{fromNodeID, toNodeID}
}
