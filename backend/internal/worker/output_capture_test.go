package worker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hotmouse/backend/internal/core"
)

func TestAutomaticProviderCapturePrecedesReportAndSurvivesCancellation(t *testing.T) {
	t.Setenv("HOTMOUSE_PYTHON", "/usr/bin/python3")
	script := `printf '%s\n' "$HOTMOUSE_TASK_TYPE" >> "$TEST_CALLS"
printf '%s\n' '{"type":"session","id":"automatic-session"}'
printf '%s\n' '{"type":"tool_execution_update","partialResult":{"content":[{"type":"text","text":"flag{streamed_from_tool}"}]}}'
while :; do sleep .02; done`
	db, project, step, marker := fakePi(t, script, core.DefaultWorkerRuntime())
	r := New(db)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := r.executeForAgent(ctx, project, step, "bounded fixture", "local/worker/01")
		done <- err
	}()
	deadline := time.Now().Add(15 * time.Second)
	var flags []core.Flag
	for time.Now().Before(deadline) {
		flags, _ = db.ListFlags(t.Context(), project)
		if len(flags) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled provider unexpectedly produced a valid report")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("provider did not stop")
	}
	flags, _ = db.ListFlags(t.Context(), project)
	if len(flags) != 1 || flags[0].Value != "flag{streamed_from_tool}" || flags[0].Status != "captured" {
		t.Fatalf("tool update was lost before final response: %+v", flags)
	}
	calls, _ := os.ReadFile(marker)
	if string(calls) != "execute\n" {
		t.Fatalf("capture depended on another model phase: %q", calls)
	}
}

func TestAutomaticFlagExtractionUsesOutputsNotInputsOrReasoning(t *testing.T) {
	for _, driver := range []string{"pi", "codex", "claudecode"} {
		t.Run(driver, func(t *testing.T) {
			r, project := strategyWorker(t)
			step := workerStep(t, r, project, "produce staged output")
			if _, err := r.DB.HeartbeatStep(t.Context(), project, step.ID, "solver"); err != nil {
				t.Fatal(err)
			}
			capture, err := r.newOutputCapture(t.Context(), project, step.ID, "solver", t.TempDir(), driver)
			if err != nil {
				t.Fatal(err)
			}
			defer capture.close()
			var events []string
			switch driver {
			case "pi":
				events = []string{
					`{"type":"tool_execution_start","args":{"command":"echo flag{input_only}"}}`,
					`{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"flag{prompt_only}"}]}}`,
					`{"type":"message_update","message":{"role":"assistant","content":[{"type":"thinking","thinking":"flag{reasoning_only}"},{"type":"toolCall","arguments":{"command":"flag{input_only}"}}]}}`,
					`{"type":"tool_execution_update","partialResult":{"content":[{"type":"text","text":"stage 1: flag{first_observed}"}]}}`,
					`{"type":"tool_execution_end","result":{"content":[{"type":"text","text":"stage 1: flag{first_observed} flag{test} flag{...}"}]}}`,
					`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"flag{second_observed}; report is malformed"}]}}`,
				}
			case "codex":
				events = []string{
					`{"type":"item.started","item":{"type":"command_execution","command":"echo flag{input_only}"}}`,
					`{"type":"item.completed","item":{"type":"reasoning","text":"flag{reasoning_only}"}}`,
					`{"type":"item.updated","item":{"type":"command_execution","aggregated_output":"flag{first_observed}"}}`,
					`{"type":"item.completed","item":{"type":"command_execution","aggregated_output":"flag{first_observed} flag{example}"}}`,
					`{"type":"item.completed","item":{"type":"agent_message","text":"flag{second_observed}"}}`,
				}
			case "claudecode":
				events = []string{
					`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"flag{reasoning_only}"},{"type":"tool_use","input":{"command":"flag{input_only}"}}]}}`,
					`{"type":"user","message":{"content":[{"type":"text","text":"flag{prompt_only}"}]}}`,
					`{"type":"user","message":{"content":[{"type":"tool_result","content":"flag{first_observed}"}]}}`,
					`{"type":"user","message":{"content":[{"type":"tool_result","content":[{"type":"text","text":"flag{first_observed} flag{redacted}"}]}]}}`,
					`{"type":"result","result":"flag{second_observed}; invalid final JSON"}`,
				}
			}
			for _, event := range events {
				capture.stream("stdout", []byte(event+"\n"))
				capture.providerEvent([]byte(event))
			}
			flags, err := r.DB.ListFlags(t.Context(), project)
			if err != nil || len(flags) != 2 {
				t.Fatalf("captured inputs/reasoning or lost output: %+v %v", flags, err)
			}
			detail, _ := r.DB.GetProject(t.Context(), project)
			if detail.Steps[0].To != nil || detail.Project.Status != "active" {
				t.Fatal("candidate capture concluded the step or claimed success")
			}
			for _, flag := range flags {
				if flag.Status != "captured" || flag.FactID == "origin" {
					t.Fatalf("missing candidate provenance: %+v", flag)
				}
				for _, fact := range detail.Facts {
					if fact.ID != flag.FactID {
						continue
					}
					var req core.FlagCheckpoint
					if err := json.Unmarshal([]byte(fact.Detail), &req); err != nil {
						t.Fatal(err)
					}
					data, err := os.ReadFile(req.SourcePath)
					if err != nil || !strings.Contains(string(data), flag.Value) || fmt.Sprintf("%x", sha256.Sum256(data)) != req.SourceSHA256 {
						t.Fatalf("raw snapshot not preserved: %+v %v", req, err)
					}
				}
			}
		})
	}
}

func TestAutomaticFlagCaptureHandlesChunksAndRejectedReplay(t *testing.T) {
	r, project := strategyWorker(t)
	step := workerStep(t, r, project, "stream raw bytes")
	_, _ = r.DB.HeartbeatStep(t.Context(), project, step.ID, "solver")
	capture, err := r.newOutputCapture(t.Context(), project, step.ID, "solver", t.TempDir(), "command")
	if err != nil {
		t.Fatal(err)
	}
	defer capture.close()
	capture.stream("stdout", []byte("stage 1\nflag{split_"))
	flags, _ := r.DB.ListFlags(t.Context(), project)
	if len(flags) != 0 {
		t.Fatal("incomplete flag submitted")
	}
	capture.stream("stdout", []byte("candidate}"))
	flags, _ = r.DB.ListFlags(t.Context(), project)
	if len(flags) != 1 {
		t.Fatal("complete flag without newline was not captured")
	}
	attempt, _ := r.DB.BeginFlagAttempt(t.Context(), project, flags[0].ID)
	_, _ = r.DB.ResolveFlagAttempt(t.Context(), project, flags[0].ID, attempt.ID, core.ResolveFlagAttempt{Status: "rejected", Response: "wrong candidate"})
	capture.stream("stdout", []byte("\nflag{split_candidate}\n"))
	capture.stream("stderr", []byte("flag{corrected_candidate}"))
	flags, _ = r.DB.ListFlags(t.Context(), project)
	if len(flags) != 2 || flags[0].Status != "rejected" || len(flags[0].Attempts) != 1 {
		t.Fatalf("rejected output was requeued or stderr lost: %+v", flags)
	}
	data, _ := os.ReadFile(filepath.Join(capture.directory, "stdout.log"))
	if string(data) != "stage 1\nflag{split_candidate}\nflag{split_candidate}\n" {
		t.Fatalf("stage bytes not persisted before process exit: %q", data)
	}
}

func TestAutomaticFlagPatternsRespectTaskFormat(t *testing.T) {
	binding := &core.PlatformBinding{Challenge: json.RawMessage(`{"metadata":{"flag_format":"GAME{...}","flag_pattern":"TOKEN-[A-F0-9]{16}"}}`)}
	patterns := outputFlagPatterns("", binding)
	for _, value := range []string{"GAME{actual_result}", "TOKEN-0123456789ABCDEF", "HTB{actual_result}"} {
		matched := false
		for _, pattern := range patterns {
			matched = matched || pattern.MatchString(value)
		}
		if !matched {
			t.Fatalf("task format was lost: %s", value)
		}
	}
}

func TestAutomaticFlagCaptureReadsFullTruncatedToolOutput(t *testing.T) {
	r, project := strategyWorker(t)
	step := workerStep(t, r, project, "capture truncated tool result")
	_, _ = r.DB.HeartbeatStep(t.Context(), project, step.ID, "solver")
	capture, err := r.newOutputCapture(t.Context(), project, step.ID, "solver", t.TempDir(), "pi")
	if err != nil {
		t.Fatal(err)
	}
	defer capture.close()
	path := filepath.Join(t.TempDir(), "full-output.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", (32<<10)-7)+"\nflag{hidden_in_full_output}"+strings.Repeat("z", 32<<10)), 0o600); err != nil {
		t.Fatal(err)
	}
	event, _ := json.Marshal(map[string]any{"type": "tool_execution_end", "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "truncated display contains no candidate"}}, "details": map[string]any{"fullOutputPath": path}}})
	capture.providerEvent(event)
	flags, err := r.DB.ListFlags(t.Context(), project)
	if err != nil || len(flags) != 1 || flags[0].Value != "flag{hidden_in_full_output}" {
		t.Fatalf("truncated output lost flag spanning chunks: %+v %v", flags, err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString("\nflag{later_full_output}")
	_ = file.Close()
	event, _ = json.Marshal(map[string]any{"type": "tool_execution_update", "partialResult": map[string]any{"details": map[string]any{"fullOutputPath": path}}})
	capture.providerEvent(event)
	capture.providerEvent(event)
	flags, err = r.DB.ListFlags(t.Context(), project)
	if err != nil || len(flags) != 2 || flags[1].Value != "flag{later_full_output}" {
		t.Fatalf("incremental full output was lost or duplicated: %+v %v", flags, err)
	}
}

func TestAutomaticFlagCaptureSurvivesProcessRetentionLimit(t *testing.T) {
	r, project := strategyWorker(t)
	step := workerStep(t, r, project, "capture output beyond retained response")
	_, _ = r.DB.HeartbeatStep(t.Context(), project, step.ID, "solver")
	observations, err := r.newOutputCapture(t.Context(), project, step.ID, "solver", t.TempDir(), "command")
	if err != nil {
		t.Fatal(err)
	}
	defer observations.close()
	bounded := &capture{limit: 8, chunk: func(data []byte) { observations.stream("stdout", data) }}
	_, _ = bounded.Write([]byte("truncated prefix\n"))
	_, _ = bounded.Write([]byte("flag{after_retention_limit}"))
	flags, err := r.DB.ListFlags(t.Context(), project)
	if !bounded.limited || err != nil || len(flags) != 1 || flags[0].Value != "flag{after_retention_limit}" {
		t.Fatalf("retention limit discarded handoff: %+v %v", flags, err)
	}
}
