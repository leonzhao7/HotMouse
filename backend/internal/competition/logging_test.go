package competition

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"hotmouse/backend/internal/core"
	"hotmouse/backend/internal/ctfplatform"
	"hotmouse/backend/internal/logging"
)

func TestCompetitionLifecycleLogsIncludeStrategyAndCleanup(t *testing.T) {
	t.Setenv("HOTMOUSE_LOG_LEVEL", "info")
	f := newFixture(t, 2, 1, 2)
	var output bytes.Buffer
	f.manager.Logger = logging.New(&output, "competition")
	f.manager.RunOnce(t.Context())
	for _, expected := range []string{"competition.planning.start", "competition.rules_processed", "strategy=", "Use the exact supplied scope", "challenge_order=", "competition.challenge.starting", "competition.challenge_started", "total=2"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("missing %q: %s", expected, output.String())
		}
	}
	output.Reset()
	f.manager.RunOnce(t.Context())
	f.clock = f.clock.Add(29 * time.Second)
	f.manager.RunOnce(t.Context())
	if output.Len() != 0 {
		t.Fatalf("unchanged run floods terminal: %s", output.String())
	}
	f.clock = f.clock.Add(time.Second)
	f.manager.RunOnce(t.Context())
	if output.Len() != 0 {
		t.Fatalf("unchanged run emits periodic progress: %s", output.String())
	}
	output.Reset()
	id := f.platform.ids[0]
	f.capture(t, id, "flag{first-secret}")
	f.manager.RunOnce(t.Context())
	f.capture(t, id, "flag{second-secret}")
	f.platform.closeFailures = 1
	f.manager.RunOnce(t.Context())
	f.clock = f.clock.Add(6 * time.Second)
	f.manager.RunOnce(t.Context())
	if err := f.db.FailCompetitionProject(t.Context(), f.projects[f.platform.ids[1]], "solver exhausted available routes"); err != nil {
		t.Fatal(err)
	}
	f.manager.RunOnce(t.Context())
	f.clock = f.clock.Add(5 * time.Second)
	f.manager.RunOnce(t.Context())
	f.capture(t, f.platform.ids[1], "flag{retry-first}")
	f.capture(t, f.platform.ids[1], "flag{retry-second}")
	f.manager.RunOnce(t.Context())
	for _, expected := range []string{"competition.flag.submit", "competition.flag.accepted", "accepted_flags=1", "accepted_flags=2", "total_flags=2", "competition.challenge.closing", "competition.cleanup.retry", "level=WARN", "resource still stopping", "retry_after=", "competition.cleanup.completed", "solver exhausted available routes", "competition.challenge.requeued", "competition.challenge_retried", "competition.finished", "status=completed"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("lifecycle missing %q: %s", expected, output.String())
		}
	}
	for _, event := range []string{"competition.progress", "competition.challenge.progress"} {
		if strings.Contains(output.String(), event) {
			t.Fatalf("unexpected progress log %q: %s", event, output.String())
		}
	}
	if strings.Contains(output.String(), "first-secret") || strings.Contains(output.String(), "second-secret") {
		t.Fatalf("submission logs exposed flag values: %s", output.String())
	}
}

func TestCompetitionPlanningErrorsShowRetryAndFinalFailure(t *testing.T) {
	t.Setenv("HOTMOUSE_LOG_LEVEL", "info")
	f := newFixture(t, 1, 1, 1)
	var output bytes.Buffer
	f.manager.Logger = logging.New(&output, "competition")
	f.manager.Backend.Plan = func(context.Context, core.CompetitionRun, []ctfplatform.Challenge) (Plan, error) {
		return Plan{}, fmt.Errorf("planner connection lost")
	}
	for attempt := 0; attempt < 3; attempt++ {
		f.manager.RunOnce(t.Context())
		f.clock = f.clock.Add(6 * time.Second)
	}
	for _, expected := range []string{"competition.planning.retry", "retry_in=5s", "planner connection lost", "competition.planning.failed", "attempts=3"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("planning error missing %q: %s", expected, output.String())
		}
	}
	if run := f.run(t); run.Status != "failed" {
		t.Fatalf("planning failure did not finish run: %+v", run)
	}
}
