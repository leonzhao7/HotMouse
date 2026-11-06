package core

import (
	"path/filepath"
	"strings"
	"testing"
)

func readyTargetProbe(t *testing.T, db *DB, p string, targets ...TargetEndpoint) TargetProbeState {
	t.Helper()
	state, err := db.TargetProbe(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	analysis := TargetProbeAnalysis{Required: true, Reason: "remote CTF", Targets: targets}
	if err := db.SaveTargetProbeAnalysis(t.Context(), p, state.RunID, analysis); err != nil {
		t.Fatal(err)
	}
	state, err = db.TargetProbe(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestTargetProbeFailureResetsOnSuccessAndForcesFailureDuringWork(t *testing.T) {
	db := testDB(t)
	p := strategyProject(t, db)
	s := strategyStep(t, db, p, nil)
	if _, err := db.HeartbeatStep(t.Context(), p, s.ID, "worker"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClaimDecide(t.Context(), p, ClaimDecide{Worker: "planner", Trigger: "planning"}); err != nil {
		t.Fatal(err)
	}
	state := readyTargetProbe(t, db, p, TargetEndpoint{Host: "ctf.example", Port: 31337}, TargetEndpoint{Host: "::1", Port: 8080})
	// The second target remains reachable, and a success resets the first.
	for _, message := range []string{"refused", "refused", "", "refused", "refused"} {
		if failed, err := db.RecordTargetProbe(t.Context(), p, state.RunID, []string{message, ""}); err != nil || failed {
			t.Fatalf("project failed before three consecutive failures: failed=%v err=%v", failed, err)
		}
	}
	if failed, err := db.RecordTargetProbe(t.Context(), p, state.RunID, []string{"refused", ""}); err != nil || !failed {
		t.Fatalf("outage did not force failure with work in flight: failed=%v err=%v", failed, err)
	}
	detail, err := db.GetProject(t.Context(), p)
	if err != nil || detail.Project.Status != "failed" || detail.Project.Decide != nil || detail.Steps[0].Worker != nil || !strings.Contains(detail.Project.FailureReason, "ctf.example:31337") || detail.TargetProbe.ConsecutiveFailures[0] != 3 {
		t.Fatalf("failure state or leases incorrect: %+v err=%v", detail, err)
	}
	if failed, err := db.RecordTargetProbe(t.Context(), p, state.RunID, []string{"refused", ""}); err != nil || failed {
		t.Fatalf("terminal project failed again: failed=%v err=%v", failed, err)
	}
	var audits int
	if err := db.SQL.QueryRow(`SELECT count(*) FROM audit_log WHERE action='project.failed' AND target_id=?`, p).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("failure audit duplicated: count=%d err=%v", audits, err)
	}
}

func TestTargetProbeStateSurvivesRestartAndIgnoresPreviousRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "target-health.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p := strategyProject(t, db)
	state := readyTargetProbe(t, db, p, TargetEndpoint{Host: "127.0.0.1", Port: 31337})
	if _, err := db.RecordTargetProbe(t.Context(), p, state.RunID, []string{"refused"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	saved, err := db.TargetProbe(t.Context(), p)
	if err != nil || saved.RunID != state.RunID || saved.Status != "ready" || saved.ConsecutiveFailures[0] != 1 {
		t.Fatalf("target state lost on restart: %+v err=%v", saved, err)
	}
	if _, err := db.SetProjectStatus(t.Context(), p, "stopped"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetProjectStatus(t.Context(), p, "active"); err != nil {
		t.Fatal(err)
	}
	resumed, err := db.TargetProbe(t.Context(), p)
	if err != nil || resumed.RunID == state.RunID || resumed.Status != "pending" || resumed.Analysis != nil {
		t.Fatalf("resumed run reused old target state: %+v err=%v", resumed, err)
	}
	if failed, err := db.RecordTargetProbe(t.Context(), p, state.RunID, []string{"refused"}); err != nil || failed {
		t.Fatalf("previous run affected resumed project: failed=%v err=%v", failed, err)
	}
	if err := db.SaveTargetProbeAnalysis(t.Context(), p, state.RunID, *state.Analysis); err == nil {
		t.Fatal("old analysis overwritten resumed project")
	}
}

func TestTargetProbeDoesNotFailStoppedOrCompletedProjects(t *testing.T) {
	for _, status := range []string{"stopped", "completed"} {
		t.Run(status, func(t *testing.T) {
			db := testDB(t)
			p := strategyProject(t, db)
			state := readyTargetProbe(t, db, p, TargetEndpoint{Host: "ctf.example", Port: 443})
			if _, err := db.SQL.Exec(`UPDATE projects SET status=? WHERE id=?`, status, p); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 3; i++ {
				if failed, err := db.RecordTargetProbe(t.Context(), p, state.RunID, []string{"timeout"}); err != nil || failed {
					t.Fatalf("terminal project affected: failed=%v err=%v", failed, err)
				}
			}
		})
	}
}
