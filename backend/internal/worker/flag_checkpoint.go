package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"hotmouse/backend/internal/core"
)

func (r *Runner) ingestFlagCheckpoints(ctx context.Context, project, step, worker, workspace string) {
	dir := filepath.Join(workspace, ".hotmouse", "results", step)
	paths, _ := filepath.Glob(filepath.Join(dir, "*.candidate.json"))
	for _, path := range paths {
		if _, err := os.Stat(path + ".receipt.json"); err == nil {
			continue
		}
		flag, err := r.ingestFlagCheckpoint(ctx, project, step, worker, path)
		if err != nil {
			data, _ := json.Marshal(map[string]any{"error": err.Error()})
			_ = writeWorkflowFile(path+".error.json", data)
			continue
		}
		data, _ := json.Marshal(map[string]any{"flag_id": flag.ID, "fact_id": flag.FactID, "status": flag.Status})
		_ = writeWorkflowFile(path+".receipt.json", data)
		_ = os.Remove(path + ".error.json")
	}
}

func (r *Runner) ingestFlagCheckpoint(ctx context.Context, project, step, worker, path string) (core.Flag, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return core.Flag{}, err
	}
	var req core.FlagCheckpoint
	if err := json.Unmarshal(raw, &req); err != nil {
		return core.Flag{}, err
	}
	file, err := os.Open(req.SourcePath)
	if err != nil {
		return core.Flag{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return core.Flag{}, fmt.Errorf("checkpoint source must be a regular raw output of at most 16 MiB")
	}
	source, err := io.ReadAll(io.LimitReader(file, (16<<20)+1))
	if err != nil {
		return core.Flag{}, err
	}
	if len(source) > 16<<20 || fmt.Sprintf("%x", sha256.Sum256(source)) != req.SourceSHA256 || req.Value == "" || !bytes.Contains(source, []byte(req.Value)) {
		return core.Flag{}, fmt.Errorf("checkpoint source digest or exact candidate does not match")
	}
	if req.FormatPattern != "" {
		pattern, err := regexp.Compile("^(?:" + req.FormatPattern + ")$")
		if err != nil || !pattern.MatchString(req.Value) {
			return core.Flag{}, fmt.Errorf("candidate does not match the supplied challenge format")
		}
	}
	return r.DB.CheckpointFlag(ctx, project, step, worker, req)
}

// Poll during the model/tool execution, then drain before parsing/committing its
// report. The checkpoint survives provider cancellation and malformed JSON.
func (r *Runner) watchFlagCheckpoints(ctx context.Context, project, step, worker, workspace string) func() {
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-ticker.C:
				r.ingestFlagCheckpoints(ctx, project, step, worker, workspace)
			}
		}
	}()
	return func() {
		close(stop)
		<-done
		finish, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		r.ingestFlagCheckpoints(finish, project, step, worker, workspace)
	}
}
