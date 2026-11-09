package cli

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kataras/iris/v12"
	"hotmouse/backend/internal/httpapi"
)

func TestCompetitionCLIEntryRulesConcurrencyAndStop(t *testing.T) {
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/openapi/v1/challenges":
			fmt.Fprint(w, `[{"unique_code":"one","flag_count":1,"container_status":"stopped","container_addr":[]},{"unique_code":"two","flag_count":1,"container_status":"stopped","container_addr":[]}]`)
		case "/openapi/v1/challenges/start":
			fmt.Fprintf(w, `{"unique_code":%q,"container_addr":["10.0.0.1:80"]}`, r.URL.Query().Get("unique_code"))
		case "/openapi/v1/challenges/close":
			fmt.Fprintf(w, `{"unique_code":%q,"closed":true}`, r.URL.Query().Get("unique_code"))
		default:
			w.WriteHeader(404)
		}
	}))
	defer platform.Close()
	t.Setenv("BENCHMARK_BASE_URL", platform.URL)
	t.Setenv("BENCHMARK_TOKEN", "task-token")
	h := newHarness(t, false)
	h.server.Close()
	handler := httpapi.New(h.db)
	app := iris.New()
	app.Logger().SetLevel("disable")
	httpapi.Register(app, handler)
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	h.server = httptest.NewServer(app)
	ctx, cancel := context.WithCancel(t.Context())
	done := handler.Competitions.Start(ctx)
	t.Cleanup(func() { cancel(); <-done; h.server.Close() })
	code, out, errOut := h.run("只攻击下发靶场，不使用提示。", "competition", "start", "tsec", "--rules-file", "-", "--concurrency", "2", "--challenge-timeout-seconds", "60")
	if code != 0 || !strings.Contains(out, `"rules": "只攻击下发靶场，不使用提示。"`) {
		t.Fatalf("start: %d %s %s", code, out, errOut)
	}
	h.ok("competition", "status", "tsec", "competition_001")
	h.ok("competition", "list", "tsec")
	h.ok("competition", "concurrency", "tsec", "competition_001", "--concurrency", "1")
	h.expect(2, "competition", "concurrency", "tsec", "competition_001", "--concurrency", "4")
	h.expect(5, "competition", "start", "tsec", "--rules", "another run")
	h.ok("competition", "stop", "tsec", "competition_001")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		run, err := h.db.GetCompetitionRun(t.Context(), "competition_001")
		if err != nil {
			t.Fatal(err)
		}
		if run.Status == "stopped" {
			resumed, err := handler.ResumeOrStartCompetition(t.Context(), "tsec", run.CompetitionConfig)
			if err != nil || resumed.ID != run.ID || resumed.Status != "stopped" {
				t.Fatalf("startup restarted a stopped competition: %#v %v", resumed, err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("competition stop did not drain")
}

func TestCompetitionRequiresAnEnabledCoordinator(t *testing.T) {
	h := newHarness(t, false)
	h.expect(6, "competition", "start", "tsec", "--rules", "scope rules")
}

func TestServeCompetitionRulesFileEntry(t *testing.T) {
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/openapi/v1/challenges":
			fmt.Fprint(w, `[{"unique_code":"one","flag_count":1,"container_status":"stopped","container_addr":[]}]`)
		case "/openapi/v1/challenges/start":
			fmt.Fprint(w, `{"unique_code":"one","container_addr":["10.0.0.1:80"]}`)
		case "/openapi/v1/challenges/close":
			fmt.Fprint(w, `{"unique_code":"one","closed":true}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer platform.Close()
	t.Setenv("BENCHMARK_BASE_URL", platform.URL)
	t.Setenv("BENCHMARK_TOKEN", "task-token")
	t.Setenv("HOTMOUSE_LOG_LEVEL", "error")
	t.Setenv("HOTMOUSE_LOG_ENV", "0")
	t.Setenv("HOTMOUSE_COMPETITION_CONCURRENCY", "2")
	dir := t.TempDir()
	path := filepath.Join(dir, "serve.db")
	rulesFile := filepath.Join(dir, "rules.md")
	rules := "只攻击当前题目靶场。\n每道题完成后继续下一题。"
	if err := os.WriteFile(rulesFile, []byte(rules), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan int, 1)
	go func() {
		result <- Run(ctx, []string{"serve", "--db", path, "--addr", "127.0.0.1:0", "--auth", "disabled", "--competition-rules-file", rulesFile, "--shutdown-timeout", "5s"}, strings.NewReader(""), io.Discard, io.Discard)
	}()
	db, err := sql.Open("sqlite3", path+"?_busy_timeout=100")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	deadline := time.Now().Add(5 * time.Second)
	created := false
	for time.Now().Before(deadline) {
		var saved string
		var concurrency int
		if err := db.QueryRow(`SELECT rules,concurrency FROM competition_runs ORDER BY created_at LIMIT 1`).Scan(&saved, &concurrency); err == nil {
			if saved != rules || concurrency != 2 {
				t.Fatalf("startup configuration: rules=%q concurrency=%d", saved, concurrency)
			}
			created = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case code := <-result:
		if code != 130 {
			t.Fatalf("competition service exit code %d", code)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("competition service did not shut down")
	}
	if !created {
		t.Fatal("rules-file entry did not create competition")
	}
}
