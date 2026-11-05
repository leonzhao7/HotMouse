package worker

import (
	"archive/zip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkersKeepOutputsAndDiscoverPeerArtifacts(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh is not installed")
	}
	project := filepath.Join(t.TempDir(), "project")
	own := filepath.Join(project, "workers", "one")
	peer := filepath.Join(project, "workers", "two")
	for _, dir := range []string{own, peer} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	shared := filepath.Join(peer, "image.txt")
	rootFile := filepath.Join(project, "input.txt")
	for _, file := range []string{shared, rootFile} {
		if err := os.WriteFile(file, []byte("original"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	vars := map[string]string{"HOTMOUSE_PROJECT_DIR": project, "HOTMOUSE_WORKSPACE_DIR": own, "PEER_FILE": shared}
	command := `printf probe > /dev/null || exit 9
cat "$PEER_FILE" || exit 10
printf '%s' own > "$HOTMOUSE_WORKSPACE_DIR/result.txt" || exit 11`
	result := runProcess(t.Context(), []string{shell, "-c", command}, nil, own, vars, 5*time.Second, nil)
	if result.err != nil || string(result.stdout) != "original" {
		t.Fatalf("shared artifact reuse failed: err=%v stdout=%q stderr=%q", result.err, result.stdout, result.stderr)
	}
	for _, file := range []string{shared, rootFile} {
		data, err := os.ReadFile(file)
		if err != nil || string(data) != "original" {
			t.Fatalf("protected artifact changed: %s data=%q err=%v", file, data, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(own, "result.txt"))
	if err != nil || string(data) != "own" {
		t.Fatalf("own output not writable: data=%q err=%v", data, err)
	}
	access, err := workspaceAccess(project, peer, "execute")
	if err != nil || !strings.Contains(access, filepath.Join(own, "result.txt")) || !strings.Contains(access, shared) {
		t.Fatalf("next worker cannot discover prior outputs: %q err=%v", access, err)
	}
}

func TestWorkerProcessUsesHostFilesystemWithoutBubblewrap(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh is not installed")
	}
	project := t.TempDir()
	workspace := filepath.Join(project, "workers", "one")
	peer := filepath.Join(project, "workers", "two")
	for _, dir := range []string{workspace, peer} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Probe the command launcher directly, using only temporary fixtures and
	// shell builtins. Filesystem access is ordinary host access; the worker's
	// prompt, rather than a mount namespace, governs artifact placement.
	t.Setenv("PATH", t.TempDir())
	rootFile := filepath.Join(project, "root-probe")
	peerFile := filepath.Join(peer, "peer-probe")
	vars := map[string]string{"HOTMOUSE_PROJECT_DIR": project, "HOTMOUSE_WORKSPACE_DIR": workspace, "TEST_ROOT_FILE": rootFile, "TEST_PEER_FILE": peerFile}
	command := `printf root > "$TEST_ROOT_FILE" && printf peer > "$TEST_PEER_FILE" && printf own > result.txt`
	result := runProcess(t.Context(), []string{shell, "-c", command}, nil, workspace, vars, 5*time.Second, nil)
	if result.err != nil {
		t.Fatalf("host process access unexpectedly restricted: %v stderr=%q", result.err, result.stderr)
	}
	for path, want := range map[string]string{rootFile: "root", peerFile: "peer", filepath.Join(workspace, "result.txt"): "own"} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatalf("host filesystem probe %s: data=%q err=%v", path, data, err)
		}
	}
}

func TestWorkerProcessSupportsBashAndArchiveExtraction(t *testing.T) {
	for _, binary := range []string{"bash", "unzip"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skipf("%s is not installed", binary)
		}
	}
	root := t.TempDir()
	project := filepath.Join(root, "project")
	workspace := filepath.Join(project, "workers", "one")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	// A tiny fixture outside the project models acquiring the user's archive;
	// no live project or target evidence is touched by this regression test.
	source := filepath.Join(root, "source.zip")
	file, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create("evidence.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("fixture evidence\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{"HOTMOUSE_PROJECT_DIR": project, "HOTMOUSE_WORKSPACE_DIR": workspace, "TEST_SOURCE_ARCHIVE": source}
	command := `set -e
printf probe > /dev/null
cat /dev/null > null.txt
head -c 1 /dev/zero > zero.bin
head -c 16 /dev/urandom > random.bin
mkdir acquire extract 2>/dev/null
cp "$TEST_SOURCE_ARCHIVE" acquire/input.zip
unzip -l acquire/input.zip > members.txt 2>/dev/null
unzip -o acquire/input.zip -d extract > extraction.log 2>/dev/null
cat extract/evidence.txt`
	result := runProcess(t.Context(), []string{"bash", "--noprofile", "--norc", "-c", command}, nil, workspace, vars, 5*time.Second, nil)
	if result.err != nil || string(result.stdout) != "fixture evidence\n" {
		t.Fatalf("shell device access/extraction failed: err=%v stdout=%q stderr=%q", result.err, result.stdout, result.stderr)
	}
	for name, size := range map[string]int{"null.txt": 0, "zero.bin": 1, "random.bin": 16} {
		data, err := os.ReadFile(filepath.Join(workspace, name))
		if err != nil || len(data) != size {
			t.Fatalf("device output %s: size=%d want=%d err=%v", name, len(data), size, err)
		}
	}
	listing, err := os.ReadFile(filepath.Join(workspace, "members.txt"))
	if err != nil || !strings.Contains(string(listing), "evidence.txt") {
		t.Fatalf("archive listing missing: data=%q err=%v", listing, err)
	}
}

func TestWorkspaceInventoryExcludesProviderState(t *testing.T) {
	project := t.TempDir()
	workspace := filepath.Join(project, "workers", "one")
	if err := os.MkdirAll(filepath.Join(workspace, ".hotmouse"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"evidence.txt", ".hotmouse/credentials.json"} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	access, err := workspaceAccess(project, workspace, "execute")
	if err != nil || !strings.Contains(access, "evidence.txt") || strings.Contains(access, "credentials.json") {
		t.Fatalf("unexpected workspace inventory: %q err=%v", access, err)
	}
}

func TestLegacySummaryPreservesProjectCwdAndWorkerState(t *testing.T) {
	r, p := strategyWorker(t)
	cfg := r.config(t.Context())
	cfg.identity = r.dispatcherIdentity(p)
	writable, err := r.ensureWorkspace(p, cfg, cfg.identity)
	if err != nil {
		t.Fatal(err)
	}
	project, err := projectDirectory(writable)
	if err != nil {
		t.Fatal(err)
	}
	vars, err := r.commandVars(t.Context(), p, "s001", "saved evidence", "yaml", "execute_conclude", cfg, project)
	if err != nil || vars["HOTMOUSE_PROJECT_DIR"] != project || vars["HOTMOUSE_WORKSPACE_DIR"] != writable {
		t.Fatalf("legacy summary workspace values=%v err=%v", vars, err)
	}
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh is not installed")
	}
	result := runProcess(t.Context(), []string{shell, "-c", `test "$PWD" = "$HOTMOUSE_PROJECT_DIR" && printf output > "$HOTMOUSE_WORKSPACE_DIR/result" && test -f "$HOTMOUSE_WORKSPACE_DIR/result"`}, nil, project, vars, 5*time.Second, nil)
	if result.err != nil {
		t.Fatalf("legacy summary access failed: %v stderr=%q", result.err, result.stderr)
	}
}

func TestLegacyPiSessionCopyPreservesSummaryUpdates(t *testing.T) {
	project := t.TempDir()
	source := filepath.Join(project, ".hotmouse", "pi-sessions")
	workspace := filepath.Join(project, "workers", "dispatcher")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, ".hotmouse"), 0o700); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(source, "session.jsonl")
	if err := os.WriteFile(original, []byte("saved evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	copy, err := copyLegacyPiSessions(project, workspace, original)
	if err != nil || copy == original {
		t.Fatalf("legacy session was not relocated: %q err=%v", copy, err)
	}
	if err := os.WriteFile(copy, []byte("summary updated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := copyLegacyPiSessions(project, workspace, original); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(copy)
	if err != nil || string(data) != "summary updated" {
		t.Fatalf("recovery overwrote resumed state: data=%q err=%v", data, err)
	}
}
