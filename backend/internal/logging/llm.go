package logging

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"
)

type contextKey struct{}
type operationKey struct{}

// TokenUsage contains token counts reported by an OpenAI-compatible response.
type TokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type LLMRequest struct {
	Operation string `json:"operation,omitempty"`
	URL       string `json:"url"`
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
	Prompt    string `json:"prompt"`
}

type LLMResponse struct {
	URL              string `json:"url"`
	Model            string `json:"model"`
	StatusCode       int    `json:"status_code"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalTokens      int    `json:"total_tokens"`
	Content          string `json:"content,omitempty"`
	Error            string `json:"error,omitempty"`
}

// Record is the persisted representation of one complete LLM interaction.
type Record struct {
	StartedAt   time.Time
	Request     LLMRequest
	Response    LLMResponse
	CompletedAt time.Time
}

// Sink persists completed LLM interactions.
type Sink interface {
	SaveLLMLog(context.Context, Record) error
}

type SinkFunc func(context.Context, Record) error

func (sink SinkFunc) SaveLLMLog(ctx context.Context, record Record) error {
	return sink(ctx, record)
}

// Logger writes structured LLM interaction records. Credentials are not part
// of either record type and therefore cannot be emitted accidentally.
type Logger struct {
	logger *log.Logger
	sink   Sink
}

func New(logger *log.Logger) *Logger {
	return NewWithSink(logger, nil)
}

func NewWithSink(logger *log.Logger, sink Sink) *Logger {
	if logger == nil {
		logger = log.Default()
	}
	return &Logger{logger: logger, sink: sink}
}

type Call struct {
	logger    *Logger
	ctx       context.Context
	request   LLMRequest
	startedAt time.Time
	once      sync.Once
}

func (logger *Logger) Start(ctx context.Context, request LLMRequest) *Call {
	if logger == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	startedAt := time.Now().UTC()
	logger.write("llm_request", request)
	return &Call{logger: logger, ctx: ctx, request: request, startedAt: startedAt}
}

func (call *Call) Finish(response LLMResponse) {
	if call == nil {
		return
	}
	call.once.Do(func() {
		completedAt := time.Now().UTC()
		if call.logger == nil {
			return
		}
		call.logger.write("llm_response", response)
		if call.logger.sink == nil {
			return
		}
		record := Record{
			StartedAt:   call.startedAt,
			Request:     call.request,
			Response:    response,
			CompletedAt: completedAt,
		}
		if err := call.logger.sink.SaveLLMLog(context.WithoutCancel(call.ctx), record); err != nil {
			call.logger.logger.Printf("llm_log: persist interaction: %v", err)
		}
	})
}

func (logger *Logger) Request(request LLMRequest) {
	logger.write("llm_request", request)
}

func (logger *Logger) Response(response LLMResponse) {
	logger.write("llm_response", response)
}

func (logger *Logger) write(event string, value any) {
	if logger == nil || logger.logger == nil {
		return
	}
	content, err := json.Marshal(struct {
		Event string `json:"event"`
		Data  any    `json:"data"`
	}{Event: event, Data: value})
	if err != nil {
		logger.logger.Printf("%s: encode log record: %v", event, err)
		return
	}
	logger.logger.Print(string(content))
}

func WithLLMLogger(ctx context.Context, logger *Logger) context.Context {
	if logger == nil {
		return ctx
	}
	return context.WithValue(ctx, contextKey{}, logger)
}

func WithOperation(ctx context.Context, operation string) context.Context {
	if operation == "" {
		return ctx
	}
	return context.WithValue(ctx, operationKey{}, operation)
}

func OperationFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	operation, _ := ctx.Value(operationKey{}).(string)
	return operation
}

func FromContext(ctx context.Context) *Logger {
	if ctx == nil {
		return nil
	}
	logger, _ := ctx.Value(contextKey{}).(*Logger)
	return logger
}
