package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kataras/iris/v12"
	"hotmouse/backend/internal/core"
	"hotmouse/backend/internal/httpapi"
)

type harness struct {
	t      *testing.T
	db     *core.DB
	server *httptest.Server
	config string
}

func newHarness(t *testing.T, auth bool) *harness {
	t.Helper()
	for _, name := range []string{"HOTMOUSE_CONFIG", "HOTMOUSE_PROFILE", "HOTMOUSE_SERVER", "HOTMOUSE_PROJECT", "HOTMOUSE_CREATOR", "HOTMOUSE_WORKER", "HOTMOUSE_INTERNAL_TOKEN", "HOTMOUSE_OUTPUT", "HOTMOUSE_TIMEOUT"} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
	t.Setenv("HOTMOUSE_OUTPUT", "table")
	t.Setenv("HOTMOUSE_TIMEOUT", "30s")
	if auth {
		t.Setenv("HOTMOUSE_AUTH_REQUIRED", "1")
	} else {
		t.Setenv("HOTMOUSE_AUTH_REQUIRED", "0")
	}
	dir := t.TempDir()
	db, e := core.Open(filepath.Join(dir, "db.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	app := iris.New()
	app.Logger().SetLevel("disable")
	httpapi.Register(app, httpapi.New(db))
	if e = app.Build(); e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(app)
	t.Cleanup(func() { server.Close(); db.SQL.Close() })
	return &harness{t: t, db: db, server: server, config: filepath.Join(dir, "client", "config.yaml")}
}
func (h *harness) run(input string, args ...string) (int, string, string) {
	h.t.Helper()
	base := []string{"--config", h.config}
	local := len(args) > 0 && (args[0] == "config" || args[0] == "version")
	if !local {
		base = append(base, "--server", h.server.URL)
	}
	hasSelect := false
	hasOutput := false
	for _, arg := range args {
		hasSelect = hasSelect || arg == "--select"
		hasOutput = hasOutput || arg == "--output" || arg == "-o"
	}
	if !hasSelect && !hasOutput {
		base = append(base, "--output", "json")
	}
	var out, errOut bytes.Buffer
	code := Run(h.t.Context(), append(base, args...), strings.NewReader(input), &out, &errOut)
	return code, out.String(), errOut.String()
}
func (h *harness) ok(args ...string) any {
	h.t.Helper()
	code, out, errOut := h.run("", args...)
	if code != 0 {
		h.t.Fatalf("%v: code=%d out=%s err=%s", args, code, out, errOut)
	}
	var v any
	if e := json.Unmarshal([]byte(out), &v); e != nil {
		h.t.Fatalf("invalid JSON %v: %q: %v", args, out, e)
	}
	return v
}
func (h *harness) expect(code int, args ...string) {
	h.t.Helper()
	got, out, errOut := h.run("", args...)
	if got != code {
		h.t.Fatalf("%v: want %d got %d out=%s err=%s", args, code, got, out, errOut)
	}
	if code != 0 && !json.Valid([]byte(errOut)) {
		h.t.Fatalf("invalid error JSON: %s", errOut)
	}
}

func TestEvidenceChainAndProductCommands(t *testing.T) {
	h := newHarness(t, false)
	h.ok("config", "set", "worker", "agent-a")
	project := h.ok("project", "create", "--title", "demo", "--origin", "local demo", "--goal", "verify", "--hint", "bounded checks", "--use")
	p := textValue(object(object(project)["project"])["id"])
	step := h.ok("step", "create", "--from", "origin,origin", "--description", "verify login", "--goal", "g001", "--priority", "9")
	if object(step)["worker"] != nil {
		t.Fatal("default worker claimed new step")
	}
	id := textValue(object(step)["id"])
	h.ok("intent", "claim", id)
	h.expect(5, "step", "claim", id, "--worker", "another")
	h.ok("step", "heartbeat", id)
	h.ok("step", "release", id)
	updated := h.ok("step", "update", id, "--priority", "0")
	if object(updated)["priority"] != float64(0) {
		t.Fatal("zero priority lost")
	}
	h.ok("step", "claim", id)
	finding := filepath.Join(t.TempDir(), "finding.yaml")
	os.WriteFile(finding, []byte("title: demo issue\ndescription: reproducible proof\nseverity: medium\nkind: security_vulnerability\ndata:\n  evidence: [proof]\n"), 0600)
	result := h.ok("step", "conclude", id, "--description", "observed proof", "--finding-file", finding)
	factID := textValue(object(object(result)["fact"])["id"])
	h.ok("fact", "show", factID)
	h.ok("finding", "list")
	h.ok("finding", "show", textValue(object(object(result)["finding"])["id"]))
	vulnerabilities := array(h.ok("vulnerability", "list", "--project", p))
	if len(vulnerabilities) != 1 {
		t.Fatalf("vulnerabilities: %#v", vulnerabilities)
	}
	vulnID := textValue(object(vulnerabilities[0])["id"])
	h.ok("vulnerability", "show", vulnID)
	code, out, errOut := h.run("", "vulnerability", "batch-review", vulnID, "missing", "--status", "ignored")
	if code != 7 || !strings.Contains(out, "missing_ids") {
		t.Fatalf("partial review: %d %s", code, out)
	}
	summary := object(h.ok("vulnerability", "summary", "--project", p, "--status", "ignored"))
	if summary["medium"] != float64(1) {
		t.Fatalf("filtered summary: %#v", summary)
	}
	h.ok("vulnerability", "review", vulnID, "--status", "confirmed")
	h.ok("vulnerability", "refresh", "--project", p)
	h.ok("project", "campaign")
	h.ok("project", "timeline")
	h.ok("hint", "add", "--content", "new guidance")
	h.ok("hint", "show", "h001")
	h.ok("goal", "show", "g001")
	h.ok("goal", "update", "g001", "--priority", "0")
	h.ok("goal", "complete", "g001", "--from", factID, "--description", "evidence sufficient")
	h.ok("project", "wait", "--max-wait", "1s")
	h.expect(5, "project", "wait", "--until", "stopped", "--max-wait", "1s")
	code, out, errOut = h.run("", "project", "export", "--output", "table")
	if code != 2 {
		t.Fatalf("export output conflict: %d %s %s", code, out, errOut)
	}
	var raw, errRaw bytes.Buffer
	code = Run(t.Context(), []string{"--config", h.config, "--server", h.server.URL, "project", "export"}, strings.NewReader(""), &raw, &errRaw)
	if code != 0 || !strings.Contains(raw.String(), "facts:") {
		t.Fatalf("raw export: %d %s %s", code, &raw, &errRaw)
	}
	h.ok("project", "reopen", "--description", "new feedback")
	h.ok("project", "stop")
	h.ok("project", "resume")
	h.ok("project", "rename", "--title", "renamed")
	h.ok("notification", "unread-count")
	h.expect(2, "notification", "read")
	h.ok("notification", "read", "--all")
	h.ok("notification", "list", "--unread")
	h.ok("audit", "list", "--project", p)
	h.ok("notification", "clear", "--yes")
	h.expect(2, "project", "delete")
	h.ok("project", "delete", "--yes")
	h.expect(2, "project", "show")
}

func TestWorkerSettingsTemplatesAndNativeExecution(t *testing.T) {
	h := newHarness(t, false)
	h.ok("worker", "config", "set", "name", "custom")
	code, out, errOut := h.run("secret-value\n", "worker", "config", "env", "set", "API_KEY", "--value-stdin")
	if code != 0 || strings.Contains(out, "secret-value") || strings.Contains(errOut, "secret-value") {
		t.Fatalf("secret leaked: %d %s %s", code, out, errOut)
	}
	h.ok("worker", "disable")
	h.ok("worker", "config", "set", "runtime.format_retries", "0")
	cfg := object(h.ok("worker", "config", "show"))
	if cfg["enabled"] != false || cfg["name"] != "custom" {
		t.Fatalf("configuration reset: %#v", cfg)
	}
	var envJSON string
	h.db.SQL.QueryRow(`SELECT environment_json FROM worker_config`).Scan(&envJSON)
	if !strings.Contains(envJSON, "secret-value") {
		t.Fatal("secret overwritten")
	}
	h.ok("worker", "config", "env", "unset", "API_KEY")
	h.db.SQL.QueryRow(`SELECT environment_json FROM worker_config`).Scan(&envJSON)
	if strings.Contains(envJSON, "API_KEY") {
		t.Fatal("unset did not delete key")
	}
	h.ok("worker", "enable")
	h.ok("worker", "test")
	h.ok("settings", "set", "step_timeout", "20")
	settings := object(h.ok("settings", "show"))
	if settings["decide_timeout"] != float64(15) {
		t.Fatal("settings reset")
	}
	tmpl := h.ok("template", "create", "--title", "custom", "--origin", "demo", "--goal", "verify", "--hint", "bounded checks")
	tid := textValue(object(tmpl)["id"])
	h.ok("template", "show", tid)
	h.ok("template", "list", "--custom")
	p := h.ok("project", "create", "--template", tid, "--title", "expanded", "--origin", "new demo", "--hint", "second hint", "--use")
	if len(array(object(p)["hints"])) != 2 {
		t.Fatal("template hints not appended")
	}
	step := h.ok("step", "create", "--from", "origin", "--description", "mock action")
	h.ok("step", "execute", textValue(object(step)["id"]))
	h.ok("worker", "history")
	h.ok("worker", "status")
	h.ok("settings", "health")
	h.ok("status")
	h.expect(2, "settings", "cleanup")
	h.ok("settings", "cleanup", "--yes")
	h.ok("template", "delete", tid, "--yes")
}

func TestInputValidationBeforeMutationAndReadLeaseBoundaries(t *testing.T) {
	h := newHarness(t, false)
	p := h.ok("project", "create", "--title", "demo", "--origin", "demo", "--goal", "verify", "--use")
	pid := textValue(object(object(p)["project"])["id"])
	invalidInputs := []string{"title: x\norigin: y\ngoal: z\nunknown: secret\n", "title: x\ntitle: y\norigin: y\ngoal: z\n", "title: x\norigin: y\ngoal: z\n---\n{}\n", "title: null\norigin: y\ngoal: z\n"}
	for _, input := range invalidInputs {
		code, out, errOut := h.run(input, "project", "create", "--file", "-")
		if code != 2 || out != "" {
			t.Fatalf("invalid input accepted: %d %s %s", code, out, errOut)
		}
	}
	h.expect(2, "project", "show", pid, "--project", "different")
	h.expect(2, "auth", "login", "--project", pid)
	h.expect(2, "step", "create", "--file", "-", "--description-file", "-")
	if code, _, _ := h.run("title: x\norigin: y\ngoal: z\n", "project", "create", "--file", "-", "--title", "override"); code != 2 {
		t.Fatal("file and body flags were not rejected")
	}
	h.expect(2, "goal", "update", "g001", "--file", filepath.Join(t.TempDir(), "missing"))
	h.expect(2, "vulnerability", "refresh")
	h.expect(2, "worker", "config", "env", "set", "API_TOKEN", "--value", "visible")
	h.ok("step", "create", "--from", "origin", "--description", "leased", "--worker", "stale")
	h.db.SQL.Exec(`UPDATE steps SET last_heartbeat_at='2000-01-01T00:00:00Z' WHERE project_id=?`, pid)
	h.expect(2, "step", "update", "s001", "--priority", "1")
	var worker string
	h.db.SQL.QueryRow(`SELECT worker FROM steps WHERE project_id=? AND id='s001'`, pid).Scan(&worker)
	if worker != "stale" {
		t.Fatal("patch pre-read reclaimed lease")
	}
	h.ok("project", "rename", "--title", "rename")
	h.db.SQL.QueryRow(`SELECT worker FROM steps WHERE project_id=? AND id='s001'`, pid).Scan(&worker)
	if worker != "stale" {
		t.Fatal("rename pre-read reclaimed lease")
	}
	h.ok("step", "show", "s001")
	var got *string
	h.db.SQL.QueryRow(`SELECT worker FROM steps WHERE project_id=? AND id='s001'`, pid).Scan(&got)
	if got != nil {
		t.Fatal("show did not reclaim lease")
	}
	h.expect(4, "finding", "list", "--project", "missing")
	h.expect(4, "fact", "show", "missing")
	h.expect(2, "project", "timeline", "--since", "not-a-time")
	code, selected, errOut := h.run("", "step", "create", "--from", "origin", "--description", "selected", "--select", "id")
	if code != 0 || strings.TrimSpace(selected) != "s002" {
		t.Fatalf("select: %d %q %s", code, selected, errOut)
	}
}

func TestAuthCookiesProfileIsolationAndLogout(t *testing.T) {
	h := newHarness(t, true)
	challenge := object(h.ok("auth", "captcha"))
	var left, right int
	fmt.Sscanf(textValue(challenge["question"]), "%d + %d = ?", &left, &right)
	code, out, errOut := h.run("safe-password\n", "auth", "register", "--username", "operator", "--password-stdin", "--captcha-id", textValue(challenge["captcha_id"]), "--captcha-answer", fmt.Sprint(left+right), "--non-interactive", "--verbose")
	if code != 0 || strings.Contains(out, "safe-password") || strings.Contains(errOut, "safe-password") {
		t.Fatalf("register: %d %s %s", code, out, errOut)
	}
	h.ok("auth", "whoami")
	status := object(h.ok("auth", "status"))
	if status["mode"] != "session" {
		t.Fatalf("auth status: %#v", status)
	}
	h.ok("config", "profile", "add", "other")
	h.expect(3, "--profile", "other", "auth", "whoami")
	var received atomic.Bool
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" || r.Header.Get("X-HotMouse-Internal-Token") != "" {
			received.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, "[]")
	}))
	defer second.Close()
	code, _, errOut = h.run("", "--server", second.URL, "project", "list")
	if code != 0 || received.Load() {
		t.Fatalf("credentials crossed server boundary: %d %s", code, errOut)
	}
	h.ok("auth", "logout")
	h.expect(3, "auth", "whoami")
	status = object(h.ok("auth", "status"))
	if status["authenticated"] != false {
		t.Fatal("logout kept authentication")
	}
}

func TestWatchCancellationAndExportOverwrite(t *testing.T) {
	h := newHarness(t, false)
	h.ok("project", "create", "--title", "demo", "--origin", "demo", "--goal", "verify", "--use")
	ctx, cancel := context.WithCancel(t.Context())
	var out, errOut bytes.Buffer
	writer := &snapshotWriter{Buffer: &out, cancel: cancel}
	code := Run(ctx, []string{"--config", h.config, "--server", h.server.URL, "worker", "status", "--watch", "--output", "ndjson"}, strings.NewReader(""), writer, &errOut)
	if code != 130 {
		t.Fatalf("watch cancellation: %d %s", code, &errOut)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 || !json.Valid([]byte(lines[0])) {
		t.Fatalf("invalid snapshot: %s", &out)
	}
	file := filepath.Join(t.TempDir(), "export.yaml")
	run := func(extra ...string) int {
		var o, e bytes.Buffer
		args := append([]string{"--config", h.config, "--server", h.server.URL, "project", "export", "--out", file}, extra...)
		return Run(t.Context(), args, strings.NewReader(""), &o, &e)
	}
	if run() != 0 || run() != 2 || run("--overwrite") != 0 {
		t.Fatal("export overwrite semantics")
	}
}

type snapshotWriter struct {
	*bytes.Buffer
	cancel context.CancelFunc
}

func (w *snapshotWriter) Write(p []byte) (int, error) {
	n, e := w.Buffer.Write(p)
	if bytes.Contains(p, []byte("\n")) {
		w.cancel()
	}
	return n, e
}

func TestServerLockAndGracefulShutdown(t *testing.T) {
	t.Setenv("HOTMOUSE_LOG_LEVEL", "info")
	t.Setenv("HOTMOUSE_LOG_ENV", "1")
	t.Setenv("OPENAI_API_KEY", "serve-test-credential")
	dir := t.TempDir()
	db := filepath.Join(dir, "serve.db")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var logs bytes.Buffer
	result := make(chan int, 1)
	go func() {
		result <- Run(ctx, []string{"serve", "--db", db, "--addr", "127.0.0.1:0", "--auth", "disabled", "--autorun", "--shutdown-timeout", "5s"}, strings.NewReader(""), &bytes.Buffer{}, &logs)
	}()
	// The lock exists before SQLite initialization; wait for schema creation to
	// avoid racing the first service's acquisition.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if st, e := os.Stat(db); e == nil && st.Size() > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server did not initialize")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var out, errOut bytes.Buffer
	code := Run(t.Context(), []string{"serve", "--db", db, "--addr", "127.0.0.1:0"}, strings.NewReader(""), &out, &errOut)
	if code != 5 {
		t.Fatalf("second service code=%d err=%s", code, &errOut)
	}
	cancel()
	select {
	case code = <-result:
		if code != 130 {
			t.Fatalf("shutdown code=%d logs=%s", code, &logs)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("shutdown did not finish")
	}
	if strings.Contains(logs.String(), "serve-test-credential") {
		t.Fatal("server terminal logs exposed a model credential")
	}
	for _, event := range []string{"runtime.start", "environment.variable", "database.open.completed", "server.listening", "worker.pool.start", "server.shutdown.completed"} {
		if !strings.Contains(logs.String(), event) {
			t.Fatalf("server logs omitted %q", event)
		}
	}
}

func TestDecideGoalsStructuredInputAndProfileSelection(t *testing.T) {
	h := newHarness(t, false)
	h.ok("config", "set", "worker", "agent-a")
	code, out, errOut := h.run("title: file project\norigin: demo\ngoal: verify\nhints:\n  - content: guidance\n", "project", "create", "--file", "-", "--use")
	if code != 0 {
		t.Fatalf("structured project: %d %s %s", code, out, errOut)
	}
	if v := h.ok("decide", "show"); v != nil {
		t.Fatalf("unclaimed decide: %#v", v)
	}
	h.ok("reason", "claim", "--trigger", "manual")
	h.ok("decide", "heartbeat")
	h.expect(5, "decide", "release", "--worker", "other")
	h.ok("decide", "release")
	goal := h.ok("goal", "create", "--description", "subgoal", "--parent", "g001")
	gid := textValue(object(goal)["id"])
	h.expect(2, "goal", "delete", gid)
	h.ok("goal", "delete", gid, "--yes")
	step := h.ok("step", "create", "--from", "origin", "--description", "abandon")
	sid := textValue(object(step)["id"])
	h.ok("step", "abandon", sid)
	if len(array(h.ok("step", "list", "--state", "abandoned"))) != 1 {
		t.Fatal("abandoned filter")
	}
	data := filepath.Join(t.TempDir(), "data.yaml")
	os.WriteFile(data, []byte("evidence: [first]\nlocation: demo\n"), 0600)
	finding := h.ok("finding", "create", "--title", "observation", "--description", "proof", "--data-file", data, "--evidence", "second")
	if len(array(object(object(finding)["data"])["evidence"])) != 2 {
		t.Fatal("evidence append")
	}
	h.ok("finding", "list", "--severity", "info")
	code, out, errOut = h.run("runtime:\n  format_retries: 0\nenabled: false\n", "worker", "config", "apply", "--file", "-")
	if code != 0 {
		t.Fatalf("worker apply: %d %s %s", code, out, errOut)
	}
	code, out, errOut = h.run("step_timeout: 25\n", "settings", "apply", "--file", "-")
	if code != 0 {
		t.Fatalf("settings apply: %d %s %s", code, out, errOut)
	}
	if object(h.ok("settings", "show"))["decide_timeout"] != float64(15) {
		t.Fatal("settings partial update reset")
	}
	h.expect(1, "worker", "test")
	h.ok("config", "profile", "add", "other", "--server", h.server.URL)
	h.ok("config", "profile", "use", "other")
	if object(h.ok("config", "show", "--effective"))["profile"] != "other" {
		t.Fatal("current profile ignored")
	}
	h.expect(2, "project", "show")
	h.expect(2, "config", "profile", "remove", "other", "--yes")
	h.ok("config", "profile", "use", "local")
	h.ok("config", "profile", "remove", "other", "--yes")
	h.expect(2, "version", "--server", h.server.URL)
}

func TestPasswordChangeAndExplicitLogin(t *testing.T) {
	h := newHarness(t, true)
	login := func(action, password string) int {
		challenge := object(h.ok("auth", "captcha"))
		var left, right int
		fmt.Sscanf(textValue(challenge["question"]), "%d + %d = ?", &left, &right)
		code, _, _ := h.run(password+"\n", "auth", action, "--username", "operator", "--password-stdin", "--captcha-id", textValue(challenge["captcha_id"]), "--captcha-answer", fmt.Sprint(left+right))
		return code
	}
	if login("register", "old-password") != 0 {
		t.Fatal("register")
	}
	code, out, errOut := h.run("current_password: old-password\nnew_password: New-password1!\n", "auth", "password", "--file", "-")
	if code != 0 {
		t.Fatalf("password: %d %s %s", code, out, errOut)
	}
	h.ok("auth", "whoami")
	h.ok("auth", "logout")
	if login("login", "old-password") != 3 {
		t.Fatal("old password still valid")
	}
	if login("login", "New-password1!") != 0 {
		t.Fatal("new password rejected")
	}
}

func TestEnvironmentTokenOverridesProfileStdinAndProfileTokenStaysBound(t *testing.T) {
	h := newHarness(t, true)
	h.ok("config", "set", "server", h.server.URL)
	h.ok("config", "set", "token_file", "-")
	t.Setenv("HOTMOUSE_INTERNAL_TOKEN", "environment-token")
	code, out, errOut := h.run("title: token\norigin: demo\ngoal: verify\n", "project", "create", "--file", "-")
	if code != 0 {
		t.Fatalf("overridden token incorrectly consumed stdin: %d %s %s", code, out, errOut)
	}
	t.Setenv("HOTMOUSE_INTERNAL_TOKEN", "")
	var credentialReceived atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" || r.Header.Get("X-HotMouse-Internal-Token") != "" {
			credentialReceived.Store(true)
		}
		fmt.Fprint(w, "[]")
	}))
	defer other.Close()
	code, _, errOut = h.run("", "project", "list", "--server", other.URL)
	if code != 0 || credentialReceived.Load() {
		t.Fatalf("profile token crossed server boundary: %d %s", code, errOut)
	}
}

func TestPendingStdinCanBeInterrupted(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan int, 1)
	var out, errOut bytes.Buffer
	go func() {
		result <- Run(ctx, []string{"--config", filepath.Join(t.TempDir(), "config.yaml"), "--output", "json", "project", "create", "--file", "-"}, reader, &out, &errOut)
	}()
	cancel()
	select {
	case code := <-result:
		if code != 130 {
			t.Fatalf("stdin interruption: %d %s", code, &errOut)
		}
	case <-time.After(time.Second):
		t.Fatal("pending stdin blocked cancellation")
	}
}
