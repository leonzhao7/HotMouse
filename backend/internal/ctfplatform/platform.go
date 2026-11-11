// Package ctfplatform defines the platform contract independently of HotMouse's
// project database, workers and HTTP server. Adapters own their authentication,
// challenge delivery and flag submission protocols.
package ctfplatform

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
)

type Descriptor struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Authentication     string `json:"authentication"`
	DeliveryMode       string `json:"delivery_mode"`   // instance, files, inline, ...
	SubmissionMode     string `json:"submission_mode"` // API, result file, ...
	SupportsHints      bool   `json:"supports_hints"`
	HintCostsScore     bool   `json:"hint_costs_score"`
	SupportsClose      bool   `json:"supports_close"`
	MaxActiveInstances int    `json:"max_active_instances"` // 0 means unspecified
	RequiresVPN        bool   `json:"requires_vpn"`
}

type Attachment struct {
	Name string `json:"name" yaml:"name"`
	URL  string `json:"url,omitempty" yaml:"url,omitempty"`
	Path string `json:"path,omitempty" yaml:"path,omitempty"`
}

type Challenge struct {
	ID               string         `json:"id" yaml:"id"`
	Title            string         `json:"title" yaml:"title"`
	Description      *string        `json:"description" yaml:"description"`
	Difficulty       string         `json:"difficulty" yaml:"difficulty"`
	Level            int            `json:"level" yaml:"level"`
	TotalScore       int            `json:"total_score" yaml:"total_score"`
	FlagCount        int            `json:"flag_count" yaml:"flag_count"`
	CorrectFlagCount int            `json:"correct_flag_count" yaml:"correct_flag_count"`
	Completed        bool           `json:"completed" yaml:"completed"`
	InstanceStatus   string         `json:"instance_status" yaml:"instance_status"`
	Addresses        []string       `json:"addresses" yaml:"addresses"`
	Attachments      []Attachment   `json:"attachments" yaml:"attachments"`
	Metadata         map[string]any `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

// Delivery represents both remotely started instances and file/inline tasks.
// Addresses retain their platform-supplied protocols; a host:port is not
// implicitly converted to an HTTP URL (it may be an SSH or raw TCP service).
type Delivery struct {
	ChallengeID string         `json:"challenge_id" yaml:"challenge_id"`
	Mode        string         `json:"mode" yaml:"mode"`
	Addresses   []string       `json:"addresses" yaml:"addresses"`
	Attachments []Attachment   `json:"attachments" yaml:"attachments"`
	Metadata    map[string]any `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

type Submission struct {
	ChallengeID string `json:"challenge_id"`
	Flag        string `json:"flag"`
}

type SubmissionResult struct {
	Correct          bool           `json:"correct"`
	Awarded          int            `json:"awarded"`
	CumulativeScore  int            `json:"cumulative_score"`
	CorrectFlagCount int            `json:"correct_flag_count"`
	TotalFlagCount   int            `json:"total_flag_count"`
	MatchedFlagIndex *int           `json:"matched_flag_index"`
	Metadata         map[string]any `json:"metadata,omitempty"`
}

type Hint struct {
	ChallengeID string  `json:"challenge_id"`
	Content     *string `json:"content"`
}

type CloseResult struct {
	ChallengeID string `json:"challenge_id"`
	Closed      bool   `json:"closed"`
}

type Platform interface {
	Descriptor() Descriptor
	ListChallenges(context.Context) ([]Challenge, error)
	Deliver(context.Context, string) (Delivery, error)
	SubmitFlag(context.Context, Submission) (SubmissionResult, error)
}

// Optional capabilities avoid forcing file-based or push platforms to invent
// container lifecycle and hint operations they do not support.
type HintProvider interface {
	GetHint(context.Context, string) (Hint, error)
}
type InstanceCloser interface {
	Close(context.Context, string) (CloseResult, error)
}
type SubmissionValidator interface {
	ValidateSubmission(Submission) error
}

// Config is supplied by the embedding application, never serialized. Adapters
// may use Options for other authentication modes without changing the contract.
type Config struct {
	BaseURL    string            `json:"-"`
	Token      string            `json:"-"`
	Options    map[string]string `json:"-"`
	HTTPClient *http.Client      `json:"-"`
}

// ScopeID isolates challenge bindings across platform tasks without persisting
// the task credential. Each adapter/application can provide its own task scope.
func (c Config) ScopeID(platform string) string {
	// JSON sorts map keys, giving other authentication modes a stable identity
	// even when their credentials/task ID are carried through Options.
	identity, _ := json.Marshal(struct {
		Platform string
		BaseURL  string
		Token    string
		Options  map[string]string
	}{platform, c.BaseURL, c.Token, c.Options})
	sum := sha256.Sum256(identity)
	return fmt.Sprintf("%x", sum[:])
}

type Factory func(Config) (Platform, error)
type registration struct {
	descriptor Descriptor
	factory    Factory
}
type Registry struct {
	mu        sync.RWMutex
	platforms map[string]registration
}

func NewRegistry() *Registry { return &Registry{platforms: map[string]registration{}} }

func (r *Registry) Register(descriptor Descriptor, factory Factory) error {
	if descriptor.ID == "" || factory == nil {
		return fmt.Errorf("platform ID and factory are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.platforms[descriptor.ID]; exists {
		return fmt.Errorf("platform %q already registered", descriptor.ID)
	}
	r.platforms[descriptor.ID] = registration{descriptor, factory}
	return nil
}

func (r *Registry) Descriptors() []Descriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Descriptor, 0, len(r.platforms))
	for _, entry := range r.platforms {
		out = append(out, entry.descriptor)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (r *Registry) Connect(id string, config Config) (Platform, error) {
	r.mu.RLock()
	entry, exists := r.platforms[id]
	r.mu.RUnlock()
	if !exists {
		return nil, &Error{HTTPStatus: 404, Code: "platform_not_found", Message: "platform is not registered"}
	}
	return entry.factory(config)
}

// Error preserves upstream business codes and both object and FastAPI array
// details. Transport/protocol failures use HTTPStatus 502/504 and must not be
// treated as rejected flags: the submission may already have reached the host.
type Error struct {
	HTTPStatus int             `json:"-"`
	Code       string          `json:"code"`
	Message    string          `json:"message"`
	Detail     json.RawMessage `json:"detail,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }
