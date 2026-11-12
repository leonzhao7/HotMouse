package worker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"hotmouse/backend/internal/core"
)

// Capture is deliberately independent of assistant response schemas and LLM
// summaries. Only public worker text and actual tool output are candidates;
// prompts, tool inputs and reasoning are never scanned.
type outputCapture struct {
	mu                    sync.Mutex
	runner                *Runner
	ctx                   context.Context
	project, step, worker string
	driver, directory     string
	patterns              []*regexp.Regexp
	streams               map[string]*os.File
	tails                 map[string]string
	seen                  map[string]bool
	fullOutputs           map[string]outputFileCursor
}

type outputFileCursor struct {
	offset int64
	tail   string
}

var flagPrefix = regexp.MustCompile(`\b([A-Za-z][A-Za-z0-9_-]{0,31})\{`)

func outputFlagPatterns(origin string, binding *core.PlatformBinding) []*regexp.Regexp {
	prefixes := []string{"flag", "ctf", "gctf", "htb", "picoctf", "ictf", "bctf", "sun", "utflag", "ductf", "corctf", "uiuctf"}
	var explicit string
	if binding != nil {
		var challenge struct {
			Description *string        `json:"description"`
			Metadata    map[string]any `json:"metadata"`
		}
		if json.Unmarshal(binding.Challenge, &challenge) == nil {
			if challenge.Description != nil {
				origin += "\n" + *challenge.Description
			}
			if format, ok := challenge.Metadata["flag_format"].(string); ok {
				origin += "\n" + format
			}
			explicit, _ = challenge.Metadata["flag_pattern"].(string)
		}
	}
	for _, match := range flagPrefix.FindAllStringSubmatch(origin, -1) {
		prefixes = append(prefixes, match[1])
	}
	escaped, seen := []string{}, map[string]bool{}
	for _, prefix := range prefixes {
		prefix = strings.ToLower(prefix)
		if !seen[prefix] {
			escaped = append(escaped, regexp.QuoteMeta(prefix))
			seen[prefix] = true
		}
	}
	patterns := []*regexp.Regexp{regexp.MustCompile(`(?i)\b(?:` + strings.Join(escaped, "|") + `)\{[^\s{}]+\}`)}
	if explicit != "" && len(explicit) <= 4096 {
		if pattern, err := regexp.Compile(explicit); err == nil {
			patterns = append(patterns, pattern)
		}
	}
	return patterns
}

func placeholderFlag(value string) bool {
	body := value
	if at := strings.IndexByte(value, '{'); at >= 0 && strings.HasSuffix(value, "}") {
		body = value[at+1 : len(value)-1]
	}
	switch strings.ToLower(strings.TrimSpace(body)) {
	case "", "...", "…", "test", "example", "placeholder", "redacted", "fake", "dummy", "flag", "your_flag_here":
		return true
	}
	return strings.ContainsAny(body, "<>")
}

func (r *Runner) newOutputCapture(ctx context.Context, project, step, worker, workspace, driver string) (*outputCapture, error) {
	detail, err := r.DB.GetProject(ctx, project)
	if err != nil {
		return nil, err
	}
	id, err := newSessionID()
	if err != nil {
		return nil, err
	}
	directory := filepath.Join(workspace, ".hotmouse", "results", step, "outputs", id)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	origin := ""
	for _, fact := range detail.Facts {
		if fact.ID == "origin" {
			origin = fact.Description
			break
		}
	}
	return &outputCapture{runner: r, ctx: ctx, project: project, step: step, worker: worker, driver: driver, directory: directory,
		patterns: outputFlagPatterns(origin, detail.Platform), streams: map[string]*os.File{}, tails: map[string]string{}, seen: map[string]bool{}, fullOutputs: map[string]outputFileCursor{}}, nil
}

func (c *outputCapture) warn(err error) {
	if err != nil {
		c.runner.logger.Warn("worker.output.capture_failed", "project", c.project, "step", c.step, "error", err)
	}
}

// stdout/stderr are durable while a tool/script is still running, including
// partial lines and output beyond the process's in-memory retention limit.
func (c *outputCapture) stream(stream string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f := c.streams[stream]
	if f == nil {
		var err error
		f, err = os.OpenFile(filepath.Join(c.directory, stream+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			c.warn(err)
			return
		}
		c.streams[stream] = f
	}
	if _, err := f.Write(data); err != nil {
		c.warn(err)
		return
	}
	c.warn(f.Sync())
	if c.driver == "command" || stream == "stderr" {
		text := c.tails[stream] + string(data)
		c.captureText(text, "process "+stream, false)
		if len(text) > 32<<10 {
			text = text[len(text)-(32<<10):]
		}
		c.tails[stream] = text
	}
}

func (c *outputCapture) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, f := range c.streams {
		c.warn(f.Close())
	}
}

// Public content only. Never recursively search arbitrary event dictionaries:
// that would turn tool inputs or model reasoning into target output.
func publicOutput(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case map[string]any:
		return publicOutput(value["content"])
	case []any:
		var parts []string
		for _, block := range value {
			entry, _ := block.(map[string]any)
			if entry["type"] == "text" {
				if text, ok := entry["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func (c *outputCapture) toolOutput(value any, complete bool) {
	c.captureText(publicOutput(value), "tool result", complete)
	result, _ := value.(map[string]any)
	details, _ := result["details"].(map[string]any)
	path, _ := details["fullOutputPath"].(string)
	if path == "" {
		path, _ = result["full_output_path"].(string)
	}
	if path == "" {
		return
	}
	file, err := os.Open(path)
	if err != nil {
		c.warn(err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	// Native tools may display only a truncated tail but save the full output.
	// Copy bounded chunks, retaining enough overlap for flags spanning chunks.
	cursor := c.fullOutputs[path]
	if info.Size() < cursor.offset {
		cursor = outputFileCursor{}
	}
	if _, err := file.Seek(cursor.offset, io.SeekStart); err != nil {
		c.warn(err)
		return
	}
	defer func() { c.fullOutputs[path] = cursor }()
	buffer := make([]byte, 32<<10)
	for {
		if c.ctx.Err() != nil {
			return
		}
		n, err := file.Read(buffer)
		if n > 0 {
			cursor.offset += int64(n)
			text := cursor.tail + string(buffer[:n])
			c.captureText(text, "saved full tool output", true)
			if len(text) > 32<<10 {
				text = text[len(text)-(32<<10):]
			}
			cursor.tail = text
		}
		if err != nil {
			if err != io.EOF {
				c.warn(err)
			}
			break
		}
	}
}

func (c *outputCapture) providerEvent(raw []byte) {
	var event map[string]any
	if json.Unmarshal(raw, &event) != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	kind, _ := event["type"].(string)
	message, _ := event["message"].(map[string]any)
	switch c.driver {
	case "pi":
		switch kind {
		case "tool_execution_update":
			c.toolOutput(event["partialResult"], false)
		case "tool_execution_end":
			c.toolOutput(event["result"], true)
		case "message_update", "message_end":
			if message["role"] == "assistant" {
				c.captureText(publicOutput(message["content"]), "worker message", kind == "message_end")
			}
		}
	case "codex":
		if kind == "item.completed" || kind == "item.updated" {
			item, _ := event["item"].(map[string]any)
			switch item["type"] {
			case "agent_message":
				text, _ := item["text"].(string)
				c.captureText(text, "worker message", kind == "item.completed")
			case "command_execution":
				text, _ := item["aggregated_output"].(string)
				c.captureText(text, "tool result", kind == "item.completed")
			case "mcp_tool_call":
				c.captureText(publicOutput(item["result"]), "tool result", kind == "item.completed")
			}
		}
	case "claudecode":
		switch kind {
		case "assistant":
			c.captureText(publicOutput(message["content"]), "worker message", true)
		case "user":
			content, _ := message["content"].([]any)
			for _, block := range content {
				entry, _ := block.(map[string]any)
				if entry["type"] == "tool_result" {
					c.captureText(publicOutput(entry["content"]), "tool result", true)
				}
			}
		case "result":
			text, _ := event["result"].(string)
			c.captureText(text, "worker message", true)
		}
	}
}

// Immutable snapshots retain the exact bytes that produced a candidate even
// when subsequent output grows, is truncated, or the provider is cancelled.
func (c *outputCapture) captureText(text, source string, persist bool) {
	if text == "" {
		return
	}
	var values []string
	for _, pattern := range c.patterns {
		for _, value := range pattern.FindAllString(text, -1) {
			if len(value) <= 16<<10 && utf8.RuneCountInString(value) <= 4096 && !placeholderFlag(value) && !c.seen[value] {
				values = append(values, value)
			}
		}
	}
	if !persist && len(values) == 0 {
		return
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
	path := filepath.Join(c.directory, digest+".txt")
	if err := writeWorkflowFile(path, []byte(text)); err != nil {
		c.warn(err)
		return
	}
	for _, value := range values {
		if c.seen[value] {
			continue
		}
		req := core.FlagCheckpoint{Value: value, SourcePath: path, SourceSHA256: digest,
			FormatNote: "Automatically extracted by code from " + source + "; format match is a candidate, platform acceptance pending"}
		data, _ := json.Marshal(req)
		candidate := filepath.Join(filepath.Dir(filepath.Dir(c.directory)), fmt.Sprintf("auto-%x.candidate.json", sha256.Sum256([]byte(value))))
		if err := writeWorkflowFile(candidate, data); err != nil {
			c.warn(err)
			continue
		}
		// Drain immediately while the execution lease still exists. The watcher
		// also retries the durable candidate if a transient DB error occurs.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(c.ctx), 3*time.Second)
		flag, err := c.runner.ingestFlagCheckpoint(ctx, c.project, c.step, c.worker, candidate)
		cancel()
		if err != nil {
			c.warn(err)
			continue
		}
		c.seen[value] = true
		receipt, _ := json.Marshal(map[string]any{"flag_id": flag.ID, "fact_id": flag.FactID, "status": flag.Status})
		c.warn(writeWorkflowFile(candidate+".receipt.json", receipt))
	}
}
