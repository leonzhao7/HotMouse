package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"

	"hotmouse/backend/internal/api"
	"hotmouse/backend/internal/domain"
	llmlog "hotmouse/backend/internal/logging"
)

const (
	maxResponseSize = 1 << 20
)

type Store interface {
	GetSettings(context.Context) (domain.Settings, error)
	ExportProject(context.Context, uint) (domain.Graph, error)
	ApplyDispatchResult(context.Context, domain.Graph, api.DispatchResult) (domain.Graph, bool, error)
}

type Agent interface {
	Run(context.Context, domain.Settings, string, io.Writer) error
}

type AgentFunc func(context.Context, domain.Settings, string, io.Writer) error

func (agent AgentFunc) Run(ctx context.Context, settings domain.Settings, prompt string, output io.Writer) error {
	return agent(ctx, settings, prompt, output)
}

type Dispatcher struct {
	store     Store
	agent     Agent
	llmLogger *llmlog.Logger

	mu      sync.Mutex
	queue   []uint
	pending map[uint]struct{}
	active  map[uint]*projectRun
	wake    chan struct{}
}

type projectRun struct {
	cancel context.CancelFunc
}

type plan struct {
	FromFactID uint   `json:"from_fact_id"`
	Action     string `json:"action"`
	Rationale  string `json:"rationale"`
}

type summary struct {
	Facts       []string           `json:"facts"`
	Steps       []api.DispatchStep `json:"steps"`
	GoalReached bool               `json:"goal_reached"`
}

func New(store Store, agent Agent) *Dispatcher {
	return NewWithLogger(store, agent, nil)
}

func NewWithLogger(store Store, agent Agent, llmLogger *llmlog.Logger) *Dispatcher {
	return &Dispatcher{
		store:     store,
		agent:     agent,
		llmLogger: llmLogger,
		pending:   make(map[uint]struct{}),
		active:    make(map[uint]*projectRun),
		wake:      make(chan struct{}, 1),
	}
}

func (dispatcher *Dispatcher) Enqueue(projectID uint) {
	if projectID == 0 {
		return
	}
	dispatcher.mu.Lock()
	if _, exists := dispatcher.pending[projectID]; exists {
		dispatcher.mu.Unlock()
		return
	}
	if _, exists := dispatcher.active[projectID]; exists {
		dispatcher.mu.Unlock()
		return
	}
	dispatcher.pending[projectID] = struct{}{}
	dispatcher.queue = append(dispatcher.queue, projectID)
	dispatcher.mu.Unlock()
	select {
	case dispatcher.wake <- struct{}{}:
	default:
	}
}

// Cancel stops a queued or currently running dispatch for a project. The
// project status is persisted by the API service; this method only interrupts
// dispatcher work that may otherwise continue after the project is aborted.
func (dispatcher *Dispatcher) Cancel(projectID uint) {
	if projectID == 0 {
		return
	}
	dispatcher.mu.Lock()
	if run, active := dispatcher.active[projectID]; active {
		run.cancel()
	}
	if _, pending := dispatcher.pending[projectID]; pending {
		delete(dispatcher.pending, projectID)
		for index, queuedID := range dispatcher.queue {
			if queuedID == projectID {
				dispatcher.queue = append(dispatcher.queue[:index], dispatcher.queue[index+1:]...)
				break
			}
		}
	}
	dispatcher.mu.Unlock()
}

func (dispatcher *Dispatcher) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-dispatcher.wake:
		}
		for {
			projectID, ok := dispatcher.pop()
			if !ok {
				break
			}
			if err := dispatcher.dispatchProject(ctx, projectID); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("dispatch project %d failed: %v", projectID, err)
			}
			if ctx.Err() != nil {
				return
			}
		}
	}
}

func (dispatcher *Dispatcher) pop() (uint, bool) {
	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	if len(dispatcher.queue) == 0 {
		return 0, false
	}
	projectID := dispatcher.queue[0]
	dispatcher.queue = dispatcher.queue[1:]
	delete(dispatcher.pending, projectID)
	return projectID, true
}

func (dispatcher *Dispatcher) dispatchProject(ctx context.Context, projectID uint) error {
	ctx, finish := dispatcher.startProject(ctx, projectID)
	defer finish()
	settings, err := dispatcher.store.GetSettings(ctx)
	if err != nil {
		return fmt.Errorf("load LLM settings: %w", err)
	}
	iterations := settings.MaxIterations
	if iterations <= 0 {
		iterations = domain.DefaultMaxIterations
	}
	for iteration := 0; iteration < iterations; iteration++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		graph, err := dispatcher.store.ExportProject(ctx, projectID)
		if err != nil {
			return fmt.Errorf("load project graph: %w", err)
		}
		if graph.Status != domain.ProjectOpen {
			return nil
		}

		selectedPlan, err := dispatcher.planNextStep(ctx, settings, graph)
		if err != nil {
			return fmt.Errorf("plan next step: %w", err)
		}
		fromFact, found := factByID(graph.Facts, selectedPlan.FromFactID)
		if !found {
			return fmt.Errorf("planner selected unknown fact ID %d", selectedPlan.FromFactID)
		}

		workerPrompt := renderPrompt(settings.WorkerPrompt, domain.DefaultWorkerPrompt, map[string]string{
			"goal":    graph.Goal.Content,
			"fact":    fromFact.Content,
			"fact_id": fmt.Sprintf("%d", fromFact.ID),
			"action":  selectedPlan.Action,
		})
		workerOutput, err := dispatcher.runAgent(ctx, settings.ForWorker(), workerPrompt)
		if err != nil {
			return fmt.Errorf("run planned worker action: %w", err)
		}
		result, err := dispatcher.summarize(ctx, settings, graph, selectedPlan, workerOutput)
		if err != nil {
			return fmt.Errorf("summarize worker result: %w", err)
		}

		updatedGraph, changed, err := dispatcher.store.ApplyDispatchResult(ctx, graph, api.DispatchResult{
			Facts:       result.Facts,
			Steps:       result.Steps,
			GoalReached: result.GoalReached,
		})
		if errors.Is(err, api.ErrGraphChanged) {
			continue
		}
		if err != nil {
			return fmt.Errorf("apply worker summary: %w", err)
		}
		if updatedGraph.Status == domain.ProjectCompleted {
			log.Printf("dispatch completed project %d", projectID)
			return nil
		}
		if !changed {
			log.Printf("dispatch paused project %d: worker produced no new graph data", projectID)
			return nil
		}
	}
	log.Printf("dispatch paused project %d after %d iterations", projectID, iterations)
	return nil
}

func (dispatcher *Dispatcher) startProject(parent context.Context, projectID uint) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	run := &projectRun{cancel: cancel}
	dispatcher.mu.Lock()
	dispatcher.active[projectID] = run
	dispatcher.mu.Unlock()
	return ctx, func() {
		dispatcher.mu.Lock()
		if current, ok := dispatcher.active[projectID]; ok && current == run {
			delete(dispatcher.active, projectID)
		}
		dispatcher.mu.Unlock()
		cancel()
	}
}

func (dispatcher *Dispatcher) planNextStep(ctx context.Context, settings domain.Settings, graph domain.Graph) (plan, error) {
	graphJSON, err := json.Marshal(graph)
	if err != nil {
		return plan{}, fmt.Errorf("encode project graph: %w", err)
	}
	prompt := renderPrompt(settings.DispatchPlanPrompt, domain.DefaultPlanPrompt, map[string]string{
		"graph_json": string(graphJSON),
	})
	content, err := dispatcher.runDispatchAgent(ctx, settings.ForDispatch(), prompt, "plan")
	if err != nil {
		return plan{}, err
	}
	var selected plan
	if err := decodeObject(content, &selected); err != nil {
		return plan{}, err
	}
	selected.Action = strings.TrimSpace(selected.Action)
	if selected.FromFactID == 0 || selected.Action == "" {
		return plan{}, errors.New("planner response must include a valid fact ID and action")
	}
	return selected, nil
}

func (dispatcher *Dispatcher) summarize(ctx context.Context, settings domain.Settings, graph domain.Graph, selected plan, workerOutput string) (summary, error) {
	graphJSON, err := json.Marshal(graph)
	if err != nil {
		return summary{}, fmt.Errorf("encode project graph: %w", err)
	}
	prompt := renderPrompt(settings.DispatchSummaryPrompt, domain.DefaultSummaryPrompt, map[string]string{
		"graph_json":         string(graphJSON),
		"chosen_action_json": mustJSON(selected),
		"worker_output":      workerOutput,
	})
	content, err := dispatcher.runDispatchAgent(ctx, settings.ForDispatch(), prompt, "summary")
	if err != nil {
		return summary{}, err
	}
	var result summary
	if err := decodeObject(content, &result); err != nil {
		return summary{}, err
	}
	if len(result.Facts) > 20 || len(result.Steps) > 100 {
		return summary{}, errors.New("summarizer response exceeds graph limits")
	}
	return result, nil
}

func renderPrompt(value, fallback string, replacements map[string]string) string {
	if strings.TrimSpace(value) == "" {
		value = fallback
	}
	pairs := make([]string, 0, len(replacements)*2)
	for key, replacement := range replacements {
		pairs = append(pairs, "{{"+key+"}}", replacement)
	}
	return strings.NewReplacer(pairs...).Replace(value)
}

func (dispatcher *Dispatcher) runAgent(ctx context.Context, settings domain.Settings, prompt string) (string, error) {
	var output cappedBuffer
	if err := dispatcher.agent.Run(ctx, settings, prompt, &output); err != nil {
		return "", err
	}
	return output.String(), nil
}

func (dispatcher *Dispatcher) runDispatchAgent(ctx context.Context, settings domain.Settings, prompt, operation string) (string, error) {
	if dispatcher.llmLogger == nil {
		return dispatcher.runAgent(ctx, settings, prompt)
	}
	ctx = llmlog.WithLLMLogger(ctx, dispatcher.llmLogger)
	ctx = llmlog.WithOperation(ctx, operation)
	return dispatcher.runAgent(ctx, settings, prompt)
}

func decodeObject(content string, target any) error {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "```") {
		if lineEnd := strings.IndexByte(content, '\n'); lineEnd >= 0 {
			content = strings.TrimSpace(content[lineEnd+1:])
		}
		content = strings.TrimSpace(strings.TrimSuffix(content, "```"))
	}
	start := strings.IndexByte(content, '{')
	end := strings.LastIndexByte(content, '}')
	if start < 0 || end < start {
		return errors.New("LLM response does not contain a JSON object")
	}
	decoder := json.NewDecoder(strings.NewReader(content[start : end+1]))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode LLM JSON response: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("LLM response contains trailing JSON data")
	}
	return nil
}

func factByID(facts []domain.Node, id uint) (domain.Node, bool) {
	for _, fact := range facts {
		if fact.ID == id {
			return fact, true
		}
	}
	return domain.Node{}, false
}

func mustJSON(value any) string {
	content, _ := json.Marshal(value)
	return string(content)
}

type cappedBuffer struct {
	bytes.Buffer
}

func (buffer *cappedBuffer) Write(content []byte) (int, error) {
	remaining := maxResponseSize - buffer.Len()
	if remaining <= 0 {
		return 0, errors.New("LLM response exceeds 1 MiB")
	}
	if len(content) > remaining {
		written, _ := buffer.Buffer.Write(content[:remaining])
		return written, errors.New("LLM response exceeds 1 MiB")
	}
	return buffer.Buffer.Write(content)
}
