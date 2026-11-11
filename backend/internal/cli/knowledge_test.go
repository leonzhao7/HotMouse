package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKnowledgeCLIWorksWithNoServerAndExportsSkills(t *testing.T) {
	t.Setenv("HOTMOUSE_SERVER", "http://127.0.0.1:1")
	t.Setenv("HOTMOUSE_OUTPUT", "json")
	t.Setenv("HOTMOUSE_PROFILE", "default")
	directory := t.TempDir()
	config := filepath.Join(directory, "client.yaml")
	call := func(want int, args ...string) any {
		t.Helper()
		var out, errOut bytes.Buffer
		code := Run(t.Context(), append([]string{"--config", config}, args...), strings.NewReader(""), &out, &errOut)
		if code != want {
			t.Fatalf("%v: code=%d out=%s err=%s", args, code, out.String(), errOut.String())
		}
		if want != 0 {
			return nil
		}
		var value any
		if err := json.Unmarshal(out.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if object(call(0, "kb", "status"))["entry_count"] != float64(56) {
		t.Fatal("offline CLI did not load all entries")
	}
	matches := array(call(0, "kb", "search", "Linux 提权", "--tag", "privesc", "--limit", "2"))
	if len(matches) != 2 || object(matches[0])["id"] != "kb012" {
		t.Fatalf("bad CLI search: %+v", matches)
	}
	if len(array(call(0, "kb", "list"))) != 56 || object(call(0, "kb", "show", "kb041"))["detail"] == "" {
		t.Fatal("offline full content missing")
	}
	if len(array(call(0, "kb", "skills"))) != 13 || object(call(0, "kb", "skill", "hotmouse-ctf"))["content"] == "" || object(call(0, "kb", "skill", "web-exploit"))["content"] == "" {
		t.Fatal("offline skills missing")
	}
	export := filepath.Join(directory, "bundle")
	call(0, "kb", "export", export)
	if _, err := os.ReadFile(filepath.Join(export, "skills", "hotmouse-ctf", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	call(4, "kb", "show", "kb999")
	call(4, "kb", "skill", "unknown")
	call(2, "kb", "search")
	call(2, "kb", "search", "RSA", "--query", "crypto")
	call(2, "kb", "search", "RSA", "--limit", "51")
	call(2, "kb", "search", "RSA", "--limit", "0")
}
