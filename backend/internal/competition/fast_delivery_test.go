package competition

import (
	"context"
	"strings"
	"testing"

	"hotmouse/backend/internal/core"
	"hotmouse/backend/internal/ctfplatform"
)

func TestCheckpointSubmissionContinuesBeforeStepReport(t *testing.T) {
	f := newFixture(t, 1, 1, 2)
	f.manager.RunOnce(t.Context())
	id := f.platform.ids[0]
	project := f.projects[id]
	step, err := f.db.CreateStep(t.Context(), project, core.CreateStep{From: []string{"origin"}, Creator: "test", Description: "solve both results"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.HeartbeatStep(t.Context(), project, step.ID, "solver"); err != nil {
		t.Fatal(err)
	}
	capture := func(value string) {
		t.Helper()
		_, err := f.db.CheckpointFlag(t.Context(), project, step.ID, "solver", core.FlagCheckpoint{Value: value, ChallengeID: id, SourcePath: "/raw/solver.txt", SourceSHA256: strings.Repeat("a", 64), FormatNote: "two flags as specified in challenge"})
		if err != nil {
			t.Fatal(err)
		}
	}
	capture("flag{first}")
	f.manager.RunOnce(t.Context())
	detail, _ := f.db.GetProject(t.Context(), project)
	if f.platform.correct[id] != 1 || detail.Steps[0].To != nil || f.run(t).Items[0].Status != "running" {
		t.Fatal("checkpoint waited for report or partial acceptance closed the challenge")
	}
	capture("flag{second}")
	f.manager.RunOnce(t.Context())
	if f.platform.correct[id] != 2 || f.run(t).Items[0].Status != "succeeded" {
		t.Fatal("all accepted checkpoints did not complete delivery without a report")
	}
}

func TestSubmissionCannotReduceKnownMultiFlagRequirement(t *testing.T) {
	f := newFixture(t, 1, 1, 2)
	f.manager.RunOnce(t.Context())
	id := f.platform.ids[0]
	f.capture(t, id, "flag{first}")
	submit := f.manager.Backend.Submit
	f.manager.Backend.Submit = func(ctx context.Context, project string, flag int64) (*ctfplatform.SubmissionResult, error) {
		result, err := submit(ctx, project, flag)
		if result != nil {
			result.TotalFlagCount = 1 // Partial/inconsistent response; catalog still requires two.
		}
		return result, err
	}
	f.manager.RunOnce(t.Context())
	if f.run(t).Items[0].Status != "running" {
		t.Fatal("one accepted flag incorrectly completed a known two-flag challenge")
	}
	f.capture(t, id, "flag{second}")
	f.manager.RunOnce(t.Context())
	if f.run(t).Items[0].Status != "succeeded" {
		t.Fatal("two accepted flags did not satisfy the original requirement")
	}
}

func TestRejectedCandidateRequiresCorrectionAndActualAcceptance(t *testing.T) {
	f := newFixture(t, 1, 1, 1)
	f.manager.RunOnce(t.Context())
	id := f.platform.ids[0]
	f.capture(t, id, "flag{wrong-wrapper}")
	flags, _ := f.db.ListFlags(t.Context(), f.projects[id])
	wrong := flags[0].ID
	submit := f.manager.Backend.Submit
	calls := 0
	f.manager.Backend.Submit = func(ctx context.Context, project string, flag int64) (*ctfplatform.SubmissionResult, error) {
		calls++
		if flag != wrong {
			return submit(ctx, project, flag)
		}
		attempt, err := f.db.BeginFlagAttempt(ctx, project, flag)
		if err != nil {
			return nil, err
		}
		_, err = f.db.ResolveFlagAttempt(ctx, project, flag, attempt.ID, core.ResolveFlagAttempt{Status: "rejected", Response: `{"correct":false}`})
		return &ctfplatform.SubmissionResult{Correct: false, TotalFlagCount: 1}, err
	}
	f.manager.RunOnce(t.Context())
	f.manager.RunOnce(t.Context())
	if calls != 1 || f.run(t).Items[0].Status != "running" {
		t.Fatal("rejected candidate was retried or treated as success")
	}
	f.capture(t, id, "correct-original-value")
	f.manager.RunOnce(t.Context())
	if calls != 2 || f.run(t).Items[0].Status != "succeeded" {
		t.Fatal("corrected original value was not delivered and confirmed")
	}
}
