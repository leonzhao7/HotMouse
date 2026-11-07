package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStrategyCLIConcludeRecallExperienceAndFlagLedger(t *testing.T) {
	h := newHarness(t, false)
	h.ok("config", "set", "worker", "test")
	project := object(h.ok("project", "create", "--title", "strategy", "--origin", "authorized target", "--goal", "verify", "--use"))
	p := textValue(object(project["project"])["id"])
	route := object(h.ok("route", "create", "--title", "authentication route", "--goal", "g001", "--no-progress-limit", "1"))
	routeID := textValue(route["id"])
	step := object(h.ok("step", "create", "--from", "origin", "--description", "inspect authentication", "--goal", "g001", "--route", routeID))
	stepID := textValue(step["id"])
	snapshot := object(h.ok("project", "context", "--phase", "execute", "--step", stepID))
	if !strings.Contains(textValue(snapshot["yaml"]), "id: "+stepID) {
		t.Fatal("context missing current step")
	}
	resultFile := filepath.Join(t.TempDir(), "result.yaml")
	if err := os.WriteFile(resultFile, []byte("type: progress\ndetail: saved authentication evidence\nevidence:\n  request: recorded\n"), 0600); err != nil {
		t.Fatal(err)
	}
	conclusion := object(h.ok("step", "conclude", stepID, "--description", "authentication response confirmed", "--title", "authentication", "--result-file", resultFile))
	factID := textValue(object(conclusion["fact"])["id"])
	if object(object(conclusion["step"])["result"])["type"] != "progress" {
		t.Fatal("structured result was lost")
	}
	if len(array(h.ok("project", "recall", "--query", "authentication"))) == 0 {
		t.Fatal("history recall empty")
	}
	fact := object(h.ok("fact", "show", factID))
	if fact["detail"] != "saved authentication evidence" {
		t.Fatal("full fact detail missing")
	}
	experience := object(h.ok("experience", "create", "--title", "authentication review", "--conditions", "login endpoint", "--method", "compare responses", "--failure-conditions", "changed session", "--tags", "web", "--source-project-id", p, "--source-step-id", stepID, "--source-fact-ids", factID))
	if len(array(h.ok("experience", "search", "--query", "authentication", "--tag", "web"))) != 1 {
		t.Fatal("experience recall empty")
	}
	h.ok("experience", "show", textValue(experience["id"]))
	flag := object(h.ok("flag", "capture", "--value", "flag{cli}", "--fact", factID, "--challenge-id", "challenge-1"))
	attempt := object(h.ok("flag", "begin", textValue(flag["id"])))
	h.ok("flag", "resolve", textValue(flag["id"]), textValue(attempt["id"]), "--status", "submission_unknown", "--response", "timeout")
	h.expect(5, "flag", "begin", textValue(flag["id"]))
	h.ok("flag", "resolve", textValue(flag["id"]), textValue(attempt["id"]), "--status", "accepted", "--response", "platform confirmed")
	ledger := array(h.ok("flag", "list"))
	if len(ledger) != 1 || object(ledger[0])["status"] != "accepted" {
		t.Fatalf("bad ledger: %+v", ledger)
	}
	h.ok("decide", "request", "--reason", "review alternatives")
	h.ok("route", "update", routeID, "--status", "shelved", "--reason", "premise invalidated", "--reopen-condition", "new evidence", "--next-direction", "independent path")
	if len(array(h.ok("route", "records"))) != 1 {
		t.Fatal("stop-loss record missing")
	}
}
