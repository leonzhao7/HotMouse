package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"hotmouse/backend/internal/domain"
)

var (
	ErrNotFound        = errors.New("project or node not found")
	ErrInvalid         = errors.New("invalid project graph")
	ErrConflict        = errors.New("a step already exists between these nodes")
	ErrGoalUnreachable = errors.New("at least one step must point to the goal before completion")
	ErrProjectClosed   = errors.New("completed projects cannot accept new graph data")
)

type Service struct {
	db *gorm.DB
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
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
	return graph, nil
}

func (service *Service) ListProjects(ctx context.Context) ([]domain.Project, error) {
	projects := make([]domain.Project, 0)
	err := service.db.WithContext(ctx).Order("id ASC").Find(&projects).Error
	return projects, err
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
