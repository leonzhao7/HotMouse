package competition

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hotmouse/backend/internal/core"
	"hotmouse/backend/internal/ctfplatform"
)

type fakePlatform struct {
	ids            []string
	active         map[string]bool
	correct        map[string]int
	flags          int
	starts, closes []string
	closeFailures  int
	listError      error
}

func (f *fakePlatform) Descriptor() ctfplatform.Descriptor {
	return ctfplatform.Descriptor{ID: "test", DeliveryMode: "instance", SupportsClose: true, MaxActiveInstances: 3}
}
func (f *fakePlatform) ListChallenges(context.Context) ([]ctfplatform.Challenge, error) {
	if f.listError != nil {
		return nil, f.listError
	}
	rows := []ctfplatform.Challenge{}
	for _, id := range f.ids {
		status := "stopped"
		if f.active[id] {
			status = "available"
		}
		rows = append(rows, ctfplatform.Challenge{ID: id, Title: id, FlagCount: f.flags, CorrectFlagCount: f.correct[id], Completed: f.correct[id] >= f.flags, InstanceStatus: status})
	}
	return rows, nil
}
func (f *fakePlatform) Deliver(_ context.Context, id string) (ctfplatform.Delivery, error) {
	f.active[id] = true
	f.starts = append(f.starts, id)
	return ctfplatform.Delivery{ChallengeID: id, Mode: "instance", Addresses: []string{"10.0.0.1:80"}}, nil
}
func (f *fakePlatform) SubmitFlag(_ context.Context, s ctfplatform.Submission) (ctfplatform.SubmissionResult, error) {
	f.correct[s.ChallengeID]++
	return ctfplatform.SubmissionResult{Correct: true, Awarded: 50, CorrectFlagCount: f.correct[s.ChallengeID], TotalFlagCount: f.flags}, nil
}
func (f *fakePlatform) Close(_ context.Context, id string) (ctfplatform.CloseResult, error) {
	if f.closeFailures > 0 {
		f.closeFailures--
		return ctfplatform.CloseResult{}, fmt.Errorf("resource still stopping")
	}
	delete(f.active, id)
	f.closes = append(f.closes, id)
	return ctfplatform.CloseResult{ChallengeID: id, Closed: true}, nil
}

type fixture struct {
	db        *core.DB
	manager   *Manager
	platform  *fakePlatform
	projects  map[string]string
	clock     time.Time
	planCalls int
	rules     string
}

func newFixture(t *testing.T, count, concurrency, flagCount int) *fixture {
	t.Helper()
	db, err := core.Open(filepath.Join(t.TempDir(), "competition.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.SQL.Close() })
	f := &fixture{db: db, projects: map[string]string{}, clock: time.Now().UTC(), rules: "只攻击下发的靶场；不使用提示；提交每个 flag。"}
	f.platform = &fakePlatform{active: map[string]bool{}, correct: map[string]int{}, flags: flagCount}
	for i := 0; i < count; i++ {
		f.platform.ids = append(f.platform.ids, fmt.Sprintf("challenge_%02d", i))
	}
	f.manager = New(db, Backend{
		Connect: func(string) (ctfplatform.Platform, string, error) { return f.platform, "task-one", nil },
		Import: func(ctx context.Context, run core.CompetitionRun, id string) (string, error) {
			delivery, err := f.platform.Deliver(ctx, id)
			if err != nil {
				return "", err
			}
			challengeData, _ := json.Marshal(ctfplatform.Challenge{ID: id, FlagCount: f.platform.flags})
			deliveryData, _ := json.Marshal(delivery)
			budget := run.ChallengeTimeoutSeconds
			project, err := db.CreateProject(ctx, core.CreateProject{Title: id, Origin: "test target", Goal: "capture flags", Hints: []core.CreateHint{{Content: run.Rules, Creator: "competition"}}, MaxWorkers: run.MaxWorkers, BudgetSeconds: &budget,
				PlatformBinding: &core.PlatformBinding{Platform: run.Platform, ScopeID: run.ScopeID, ChallengeID: id, Challenge: challengeData, Delivery: deliveryData}})
			if err == nil {
				f.projects[id] = project.Project.ID
			}
			return project.Project.ID, err
		},
		Retry: func(ctx context.Context, _ core.CompetitionRun, platform ctfplatform.Platform, challenge ctfplatform.Challenge, project string) error {
			delivery, err := platform.Deliver(ctx, challenge.ID)
			if err != nil {
				return err
			}
			challengeData, _ := json.Marshal(challenge)
			deliveryData, _ := json.Marshal(delivery)
			return db.RefreshCompetitionProject(ctx, project, string(challengeData), string(deliveryData), "test target")
		},
		Lookup: func(_ context.Context, _ core.CompetitionRun, id string) (string, error) { return f.projects[id], nil },
		Submit: func(ctx context.Context, project string, id int64) (*ctfplatform.SubmissionResult, error) {
			flags, err := db.ListFlags(ctx, project)
			if err != nil {
				return nil, err
			}
			var flag core.Flag
			for _, item := range flags {
				if item.ID == id {
					flag = item
				}
			}
			attempt, err := db.BeginFlagAttempt(ctx, project, id)
			if err != nil {
				return nil, err
			}
			result, err := f.platform.SubmitFlag(ctx, ctfplatform.Submission{ChallengeID: flag.ChallengeID, Flag: flag.Value})
			if err != nil {
				return nil, err
			}
			_, err = db.ResolveFlagAttempt(ctx, project, id, attempt.ID, core.ResolveFlagAttempt{Status: "accepted", Response: "platform accepted"})
			return &result, err
		},
		Plan: func(_ context.Context, run core.CompetitionRun, _ []ctfplatform.Challenge) (Plan, error) {
			f.planCalls++
			if run.Rules != f.rules {
				return Plan{}, fmt.Errorf("original rules changed")
			}
			return Plan{Summary: "Use the exact supplied scope and never fetch hints", ChallengeIDs: f.platform.ids}, nil
		},
	})
	f.manager.Now = func() time.Time { return f.clock }
	if _, err := db.CreateCompetitionRun(t.Context(), "test", "task-one", core.CompetitionConfig{Rules: f.rules, Concurrency: concurrency, ChallengeTimeoutSeconds: 60}, f.platform.ids); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *fixture) run(t *testing.T) core.CompetitionRun {
	t.Helper()
	run, err := f.db.GetCompetitionRun(t.Context(), "competition_001")
	if err != nil {
		t.Fatal(err)
	}
	return run
}
func (f *fixture) capture(t *testing.T, id, value string) {
	t.Helper()
	if _, err := f.db.CaptureFlag(t.Context(), f.projects[id], core.CaptureFlag{ChallengeID: id, Value: value, FactID: "origin"}); err != nil {
		t.Fatal(err)
	}
}

func TestPlanningRetryWaitsAfterSlowFailure(t *testing.T) {
	f := newFixture(t, 1, 1, 1)
	f.manager.Backend.Plan = func(context.Context, core.CompetitionRun, []ctfplatform.Challenge) (Plan, error) {
		f.planCalls++
		f.clock = f.clock.Add(5 * time.Minute)
		return Plan{}, fmt.Errorf("planner timed out")
	}
	f.manager.RunOnce(t.Context())
	run := f.run(t)
	deadline, err := time.Parse(time.RFC3339Nano, run.RetryAfter)
	if err != nil || !deadline.Equal(f.clock.Add(5*time.Second)) || run.PlanningAttempts != 1 {
		t.Fatalf("retry deadline must follow failure: %+v %v", run, err)
	}
	f.manager.RunOnce(t.Context())
	f.clock = f.clock.Add(4 * time.Second)
	f.manager.RunOnce(t.Context())
	if f.planCalls != 1 {
		t.Fatalf("retried slow planner before the delay: %d calls", f.planCalls)
	}
	f.clock = f.clock.Add(time.Second)
	f.manager.RunOnce(t.Context())
	if f.planCalls != 2 {
		t.Fatalf("did not retry after the delay: %d calls", f.planCalls)
	}
}

func TestDozensOfChallengesContinueAfterSuccessAndFailure(t *testing.T) {
	f := newFixture(t, 24, 2, 1)
	for cycle := 0; cycle < 90; cycle++ {
		f.manager.RunOnce(t.Context())
		run := f.run(t)
		if len(f.platform.active) > 2 {
			t.Fatalf("concurrency exceeded: %v", f.platform.active)
		}
		if run.Status == "completed" {
			break
		}
		for _, item := range run.Items {
			if item.Status != "running" {
				continue
			}
			attempts := 0
			for _, id := range f.platform.starts {
				if id == item.ChallengeID {
					attempts++
				}
			}
			if item.Ordinal%3 == 0 && attempts < 3 {
				if err := f.db.FailCompetitionProject(t.Context(), *item.ProjectID, "solver failed"); err != nil {
					t.Fatal(err)
				}
			} else {
				f.capture(t, item.ChallengeID, "flag{"+item.ChallengeID+"}")
			}
		}
		f.clock = f.clock.Add(time.Second)
	}
	run := f.run(t)
	if run.Status != "completed" || len(f.platform.starts) != 40 || len(f.platform.closes) != 40 || f.planCalls != 1 {
		t.Fatalf("queue did not drain: status=%s starts=%d closes=%d plans=%d", run.Status, len(f.platform.starts), len(f.platform.closes), f.planCalls)
	}
	for index, id := range f.platform.starts {
		expected := f.platform.ids[index%24]
		if index >= 24 {
			expected = f.platform.ids[(index-24)%8*3]
		}
		if id != expected {
			t.Fatalf("challenge %d started out of round order: %s, want %s", index, id, expected)
		}
	}
	for _, item := range run.Items {
		if item.Status != "succeeded" {
			t.Fatalf("unfinished item: %#v", item)
		}
		project, err := f.db.GetProject(t.Context(), *item.ProjectID)
		if err != nil || len(project.Hints) != 1 || project.Hints[0].Content != f.rules {
			t.Fatalf("rules not propagated: %#v %v", project.Hints, err)
		}
	}
}

func TestMultiFlagAndCleanupKeepSlotOccupied(t *testing.T) {
	f := newFixture(t, 2, 1, 2)
	f.manager.RunOnce(t.Context())
	id := f.platform.ids[0]
	f.capture(t, id, "flag{first}")
	f.manager.RunOnce(t.Context())
	if len(f.platform.starts) != 1 || f.run(t).Items[0].Status != "running" {
		t.Fatal("partial answer retired challenge")
	}
	f.capture(t, id, "flag{second}")
	f.platform.closeFailures = 1
	f.manager.RunOnce(t.Context())
	if f.run(t).Items[0].Status != "closing" || len(f.platform.starts) != 1 {
		t.Fatal("cleanup failure released concurrency slot")
	}
	f.clock = f.clock.Add(6 * time.Second)
	f.manager.RunOnce(t.Context())
	if len(f.platform.starts) != 2 || len(f.platform.closes) != 1 || f.run(t).Items[0].Status != "succeeded" {
		t.Fatal("next challenge not started after confirmed cleanup")
	}
}

func TestFailedChallengeWaitsForRoundAndRetriesWithSavedFlags(t *testing.T) {
	f := newFixture(t, 2, 1, 2)
	f.manager.RunOnce(t.Context())
	id := f.platform.ids[0]
	project := f.projects[id]
	f.capture(t, id, "flag{first}")
	f.manager.RunOnce(t.Context())
	f.clock = f.clock.Add(61 * time.Second)
	f.manager.RunOnce(t.Context())
	if run := f.run(t); run.Items[0].Status != "failed" || run.Items[1].Status != "running" {
		t.Fatalf("timeout did not yield to the rest of the round: %+v", run)
	}
	f.manager.RunOnce(t.Context())
	if len(f.platform.starts) != 2 {
		t.Fatal("failed challenge retried before the round ended")
	}
	f.capture(t, f.platform.ids[1], "flag{other-first}")
	f.capture(t, f.platform.ids[1], "flag{other-second}")
	f.manager.RunOnce(t.Context())
	run := f.run(t)
	if run.Status != "running" || run.Items[0].Status != "queued" || run.Items[1].Status != "succeeded" {
		t.Fatalf("unsolved challenge was not requeued: %+v", run)
	}
	f.clock = f.clock.Add(4 * time.Second)
	f.manager.RunOnce(t.Context())
	if len(f.platform.starts) != 2 {
		t.Fatal("retried before the round delay")
	}
	f.manager = New(f.db, f.manager.Backend)
	f.manager.Now = func() time.Time { return f.clock }
	f.clock = f.clock.Add(time.Second)
	f.manager.RunOnce(t.Context())
	run = f.run(t)
	if run.Items[0].Status != "running" || *run.Items[0].ProjectID != project || len(f.platform.starts) != 3 || f.planCalls != 1 {
		t.Fatalf("restart did not resume the saved challenge: %+v", run)
	}
	f.capture(t, id, "flag{second}")
	f.manager.RunOnce(t.Context())
	flags, err := f.db.ListFlags(t.Context(), project)
	if err != nil || f.run(t).Status != "completed" || f.platform.correct[id] != 2 || len(flags) != 2 || len(flags[0].Attempts) != 1 || len(flags[1].Attempts) != 1 {
		t.Fatalf("retry lost accepted flags or resubmitted them: %+v %v", flags, err)
	}
}

func TestStopDuringRetryDelayDoesNotRestartChallenge(t *testing.T) {
	f := newFixture(t, 1, 1, 1)
	f.manager.RunOnce(t.Context())
	if err := f.db.FailCompetitionProject(t.Context(), f.projects[f.platform.ids[0]], "no flag found"); err != nil {
		t.Fatal(err)
	}
	f.manager.RunOnce(t.Context())
	if run := f.run(t); run.Status != "running" || run.Items[0].Status != "queued" {
		t.Fatalf("failed challenge did not enter the retry queue: %+v", run)
	}
	if err := f.manager.Stop(t.Context(), "competition_001"); err != nil {
		t.Fatal(err)
	}
	f.clock = f.clock.Add(10 * time.Second)
	f.manager.RunOnce(t.Context())
	if run := f.run(t); run.Status != "stopped" || run.Items[0].Status != "skipped" || len(f.platform.starts) != 1 {
		t.Fatalf("stop restarted a queued retry: %+v", run)
	}
}

func TestRetryWaitsForPlatformListingAndSkipsRemoteCompletion(t *testing.T) {
	f := newFixture(t, 1, 1, 1)
	f.manager.RunOnce(t.Context())
	id := f.platform.ids[0]
	if err := f.db.FailCompetitionProject(t.Context(), f.projects[id], "solver failed"); err != nil {
		t.Fatal(err)
	}
	f.platform.listError = fmt.Errorf("platform temporarily unavailable")
	f.manager.RunOnce(t.Context())
	if run := f.run(t); run.Status != "running" || run.Items[0].Status != "failed" {
		t.Fatalf("listing failure finished the competition: %+v", run)
	}
	f.platform.listError = nil
	f.platform.correct[id] = 1 // Another solver completed it between rounds.
	f.manager.RunOnce(t.Context())
	f.clock = f.clock.Add(5 * time.Second)
	f.manager.RunOnce(t.Context())
	if run := f.run(t); run.Status != "completed" || run.Items[0].Status != "succeeded" || len(f.platform.starts) != 1 {
		t.Fatalf("remote completion restarted an already solved challenge: %+v", run)
	}
}

func TestLocallyCompletedProjectRetriesUntilPlatformAcceptance(t *testing.T) {
	f := newFixture(t, 1, 1, 1)
	f.manager.RunOnce(t.Context())
	id := f.platform.ids[0]
	project := f.projects[id]
	if err := f.db.CompleteCompetitionProject(t.Context(), project, "premature local completion"); err != nil {
		t.Fatal(err)
	}
	f.manager.RunOnce(t.Context())
	f.clock = f.clock.Add(5 * time.Second)
	f.manager.RunOnce(t.Context())
	detail, err := f.db.GetProject(t.Context(), project)
	if err != nil || detail.Project.Status != "active" || len(detail.Goals) == 0 || detail.Goals[0].Status != "active" || f.run(t).Items[0].Status != "running" {
		t.Fatalf("premature completion was not reopened: %+v %v", detail, err)
	}
	f.capture(t, id, "flag{platform-confirmed}")
	f.manager.RunOnce(t.Context())
	if f.run(t).Status != "completed" {
		t.Fatal("platform acceptance did not finish the retried project")
	}
}

func TestUnknownSubmissionIsPreservedAcrossChallengeRetries(t *testing.T) {
	f := newFixture(t, 1, 1, 2)
	f.manager.RunOnce(t.Context())
	id := f.platform.ids[0]
	project := f.projects[id]
	f.capture(t, id, "flag{unknown}")
	submit := f.manager.Backend.Submit
	calls := 0
	f.manager.Backend.Submit = func(ctx context.Context, project string, flagID int64) (*ctfplatform.SubmissionResult, error) {
		calls++
		if calls > 1 {
			return submit(ctx, project, flagID)
		}
		attempt, err := f.db.BeginFlagAttempt(ctx, project, flagID)
		if err != nil {
			return nil, err
		}
		_, err = f.db.ResolveFlagAttempt(ctx, project, flagID, attempt.ID, core.ResolveFlagAttempt{Status: "submission_unknown", Response: "connection lost"})
		return nil, err
	}
	f.manager.RunOnce(t.Context())
	f.clock = f.clock.Add(61 * time.Second)
	f.manager.RunOnce(t.Context())
	f.clock = f.clock.Add(5 * time.Second)
	f.manager.RunOnce(t.Context())
	f.manager.RunOnce(t.Context())
	flags, err := f.db.ListFlags(t.Context(), project)
	if err != nil || calls != 1 || len(flags) != 1 || flags[0].Status != "submission_unknown" || len(flags[0].Attempts) != 1 || f.run(t).Items[0].Status != "running" {
		t.Fatalf("retry resubmitted an unknown candidate: %+v calls=%d err=%v", flags, calls, err)
	}
}

func TestWallTimeoutAndDynamicConcurrency(t *testing.T) {
	f := newFixture(t, 5, 1, 1)
	f.manager.RunOnce(t.Context())
	if err := f.db.SetCompetitionConcurrency(t.Context(), "competition_001", 3); err != nil {
		t.Fatal(err)
	}
	f.manager.RunOnce(t.Context())
	if len(f.platform.starts) != 3 {
		t.Fatal("increasing concurrency did not fill slots")
	}
	if err := f.db.SetCompetitionConcurrency(t.Context(), "competition_001", 1); err != nil {
		t.Fatal(err)
	}
	f.manager.RunOnce(t.Context())
	if len(f.platform.closes) != 0 {
		t.Fatal("lowering concurrency interrupted current challenges")
	}
	f.clock = f.clock.Add(61 * time.Second)
	f.manager.RunOnce(t.Context())
	run := f.run(t)
	if len(f.platform.starts) != 4 || len(f.platform.closes) != 3 || !strings.Contains(run.Items[0].LastError, "time limit") {
		t.Fatalf("timeout did not advance queue: starts=%v closes=%v run=%#v", f.platform.starts, f.platform.closes, run)
	}
}

func TestRestartRecoversAnUnrecordedProjectAndStopDrains(t *testing.T) {
	f := newFixture(t, 4, 2, 1)
	f.manager.RunOnce(t.Context())
	run := f.run(t)
	// Simulate a crash after project creation but before the association save.
	item := run.Items[0]
	item.Status = "starting"
	item.ProjectID = nil
	if err := f.db.SaveCompetitionItem(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	f.manager = New(f.db, f.manager.Backend)
	f.manager.Now = func() time.Time { return f.clock }
	f.manager.RunOnce(t.Context())
	if len(f.platform.starts) != 2 || f.planCalls != 1 || f.run(t).Items[0].ProjectID == nil {
		t.Fatal("restart duplicated project or planning")
	}
	if err := f.manager.Stop(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	f.manager.RunOnce(t.Context())
	if run = f.run(t); run.Status != "stopped" || len(f.platform.starts) != 2 || len(f.platform.closes) != 2 {
		t.Fatalf("stop: %#v", run)
	}
}

func TestUnknownSubmissionIsNotRetriedAndBusyWorkerDelaysCleanup(t *testing.T) {
	f := newFixture(t, 2, 1, 1)
	f.manager.RunOnce(t.Context())
	id := f.platform.ids[0]
	f.capture(t, id, "flag{unknown}")
	calls := 0
	f.manager.Backend.Submit = func(ctx context.Context, project string, flagID int64) (*ctfplatform.SubmissionResult, error) {
		calls++
		attempt, err := f.db.BeginFlagAttempt(ctx, project, flagID)
		if err != nil {
			return nil, err
		}
		_, err = f.db.ResolveFlagAttempt(ctx, project, flagID, attempt.ID, core.ResolveFlagAttempt{Status: "submission_unknown", Response: "connection lost"})
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("connection lost")
	}
	f.manager.RunOnce(t.Context())
	f.manager.RunOnce(t.Context())
	if calls != 1 {
		t.Fatal("unknown flag automatically resubmitted")
	}
	busy := true
	f.manager.Backend.Busy = func(string) bool { return busy }
	f.clock = f.clock.Add(61 * time.Second)
	f.manager.RunOnce(t.Context())
	if len(f.platform.closes) != 0 || len(f.platform.starts) != 1 {
		t.Fatal("closed target while solver was still running")
	}
	busy = false
	f.manager.RunOnce(t.Context())
	if len(f.platform.closes) != 1 || len(f.platform.starts) != 2 {
		t.Fatal("quiescent worker did not release slot")
	}
}

func TestStopCancelsRulePlanningAndDoesNotOpenChallenges(t *testing.T) {
	f := newFixture(t, 2, 1, 1)
	entered := make(chan struct{})
	f.manager.Backend.Plan = func(ctx context.Context, _ core.CompetitionRun, _ []ctfplatform.Challenge) (Plan, error) {
		close(entered)
		<-ctx.Done()
		return Plan{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := f.manager.Start(ctx)
	defer func() { cancel(); <-done }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("rule planner did not start")
	}
	if err := f.manager.Stop(t.Context(), "competition_001"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		run := f.run(t)
		if run.Status == "stopped" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("stop did not interrupt the total prompt planner")
}
