// Package logging writes bounded, credential-redacted diagnostics to the terminal.
package logging

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"
)

const textLimit = 4096

type Logger struct {
	logger    *slog.Logger
	level     *slog.LevelVar
	redactor  *redactor
	component string
	logEnv    bool
}

func New(output io.Writer, component string) *Logger {
	if output == nil {
		output = io.Discard
	}
	level := &slog.LevelVar{}
	configured := strings.ToLower(os.Getenv("HOTMOUSE_LOG_LEVEL"))
	switch configured {
	case "debug":
		level.Set(slog.LevelDebug)
	case "warn", "warning":
		level.Set(slog.LevelWarn)
	case "error":
		level.Set(slog.LevelError)
	default:
		level.Set(slog.LevelInfo)
	}
	l := &Logger{
		logger: slog.New(slog.NewTextHandler(output, &slog.HandlerOptions{Level: level})),
		level:  level, redactor: newRedactor(os.Environ()), component: component,
		logEnv: os.Getenv("HOTMOUSE_LOG_ENV") != "0",
	}
	if configured != "" && configured != "debug" && configured != "info" && configured != "warn" && configured != "warning" && configured != "error" {
		l.Warn("logging.invalid_level", "configured", configured, "fallback", "info")
	}
	return l
}

type callbackWriter func(string)

func (w callbackWriter) Write(data []byte) (int, error) {
	w(strings.TrimSuffix(string(data), "\n"))
	return len(data), nil
}

func NewCallback(output func(string), component string) *Logger {
	return New(callbackWriter(output), component)
}

func (l *Logger) SetLevel(level slog.Level) { l.level.Set(level) }

func (l *Logger) With(args ...any) *Logger {
	copy := *l
	copy.logger = l.logger.With(l.safeArgs(args)...)
	return &copy
}

func (l *Logger) WithComponent(component string) *Logger {
	copy := *l
	copy.component = component
	return &copy
}

// WithEnvironment also masks credentials supplied only to a child process.
func (l *Logger) WithEnvironment(environment []string) *Logger {
	copy := *l
	copy.redactor = l.redactor.withEnvironment(environment)
	return &copy
}

func (l *Logger) Debug(message string, args ...any) { l.write(slog.LevelDebug, message, args) }
func (l *Logger) Info(message string, args ...any)  { l.write(slog.LevelInfo, message, args) }
func (l *Logger) Warn(message string, args ...any)  { l.write(slog.LevelWarn, message, args) }
func (l *Logger) Error(message string, args ...any) { l.write(slog.LevelError, message, args) }

// Printf preserves existing Worker diagnostic messages through the same logger.
func (l *Logger) Printf(format string, args ...any) { l.Info(fmt.Sprintf(format, args...)) }

func (l *Logger) write(level slog.Level, message string, args []any) {
	if l == nil || !l.logger.Enabled(context.Background(), level) {
		return
	}
	fields := []any{"component", l.component}
	fields = append(fields, l.safeArgs(args)...)
	l.logger.Log(context.Background(), level, l.SafeText(message), fields...)
}

func (l *Logger) safeArgs(args []any) []any {
	result := make([]any, 0, len(args))
	for i := 0; i < len(args); i += 2 {
		key := fmt.Sprint(args[i])
		value := any("missing")
		if i+1 < len(args) {
			value = args[i+1]
		}
		if sensitiveName(key) {
			if text, ok := value.(string); ok && text != "" {
				value = "[redacted]"
			}
		}
		switch v := value.(type) {
		case string:
			value = l.SafeText(v)
		case error:
			if v != nil {
				value = l.SafeText(v.Error())
			}
		case []byte:
			value = l.SafeText(string(v))
		case map[string]any, map[string]string, []string, []any:
			data, err := json.Marshal(v)
			if err == nil {
				var object any
				if json.Unmarshal(data, &object) == nil {
					data, _ = json.Marshal(l.redactor.jsonValue(object))
				}
				value = boundedText(string(data))
			}
		}
		result = append(result, key, value)
	}
	return result
}

func (l *Logger) SafeText(value string) string {
	if trimmed := strings.TrimSpace(value); strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var object any
		if json.Unmarshal([]byte(trimmed), &object) == nil {
			if data, err := json.Marshal(l.redactor.jsonValue(object)); err == nil {
				return boundedText(string(data))
			}
		}
	}
	return boundedText(l.redactor.text(value))
}

func boundedText(value string) string {
	if len(value) > textLimit {
		value = value[:textLimit]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
		value += " [truncated]"
	}
	return value
}

func (l *Logger) Runtime() {
	cwd, _ := os.Getwd()
	hostname, _ := os.Hostname()
	l.Info("runtime.start", "pid", os.Getpid(), "uid", os.Getuid(), "gid", os.Getgid(),
		"hostname", hostname, "cwd", cwd, "os", runtime.GOOS, "arch", runtime.GOARCH,
		"go_version", runtime.Version(), "cpus", runtime.NumCPU(), "gomaxprocs", runtime.GOMAXPROCS(0), "log_level", l.level.Level().String())
	l.Environment("runtime", os.Environ())
}

func (l *Logger) Environment(scope string, environment []string) {
	if l == nil || !l.logEnv {
		return
	}
	values := map[string]string{}
	for _, entry := range environment {
		if key, value, ok := strings.Cut(entry, "="); ok {
			values[key] = value
		}
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	l.Info("environment.snapshot", "scope", scope, "count", len(names))
	for _, name := range names {
		value := values[name]
		state := "set"
		if value == "" {
			state = "empty"
		} else if sensitiveName(name) {
			value = "[redacted]"
		} else if strings.HasPrefix(name, "HOTMOUSE_") && (strings.Contains(name, "PROMPT") || strings.Contains(name, "CONTEXT_YAML") || strings.Contains(name, "GRAPH_YAML") || strings.HasSuffix(name, "DESCRIPTION")) {
			value = fmt.Sprintf("[task content: %d bytes]", len(value))
		}
		l.Info("environment.variable", "scope", scope, "name", name, "state", state, "value", value)
	}
}

// MergeEnvironment returns the exact last-value-wins environment passed to exec.
func MergeEnvironment(base []string, overrides ...map[string]string) []string {
	values := map[string]string{}
	for _, entry := range base {
		if key, value, ok := strings.Cut(entry, "="); ok {
			values[key] = value
		}
	}
	for _, override := range overrides {
		for key, value := range override {
			values[key] = value
		}
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]string, 0, len(names))
	for _, name := range names {
		result = append(result, name+"="+values[name])
	}
	return result
}

type lineWriter struct{ logger *Logger }

func (w lineWriter) Write(data []byte) (int, error) {
	w.logger.Info("runtime.message", "text", strings.TrimSpace(string(data)))
	return len(data), nil
}

// Writer bridges libraries that already write their own diagnostic lines.
func (l *Logger) Writer() io.Writer { return lineWriter{logger: l} }
