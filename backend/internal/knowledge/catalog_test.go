package knowledge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testCatalog(t *testing.T) *Catalog {
	t.Helper()
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBundleProvenanceAndRecordIsolation(t *testing.T) {
	c := testCatalog(t)
	info := c.Info()
	if info.EntryCount != 56 || info.SkillCount != 13 || info.Commit != "ba3d3d49a3211b7d26e96cee9df9788506c3e16c" || info.License != "MIT" {
		t.Fatalf("incomplete migration: %+v", info)
	}
	entries := c.List("")
	if len(entries) != info.EntryCount {
		t.Fatal("list lost entries")
	}
	entry, err := c.Get("kb002")
	if err != nil || entry.Detail == "" || len(entry.Commands) == 0 {
		t.Fatalf("full technique content missing: %+v %v", entry, err)
	}
	entry.Tags[0], entry.Commands[0] = "changed", "changed"
	info.Tags["web"] = -1
	again, _ := c.Get("kb002")
	if again.Tags[0] == "changed" || again.Commands[0] == "changed" || c.Info().Tags["web"] < 1 {
		t.Fatal("caller can mutate shared catalog")
	}
	for _, skill := range c.Skills() {
		if skill.Content == "" || skill.Description == "" {
			t.Fatal("skill entrypoint was not bundled")
		}
	}
}

func TestSearchRanksTechniqueAndSupportsChineseAndTags(t *testing.T) {
	c := testCatalog(t)
	for _, tc := range []struct{ query, tag, first string }{
		{"SSRF", "", "kb006"},
		{"SQL injection", "sqli", "kb002"},
		{"Linux 提权", "privesc", "kb012"},
		{"RSA crypto", "crypto", "kb041"},
		{"pwn", "", "kb042"},
		{"flag", "flag", "kb019"},
	} {
		matches := c.Search(tc.query, tc.tag, 3)
		if len(matches) == 0 || matches[0].ID != tc.first {
			t.Errorf("%s/%s: want %s first, got %+v", tc.query, tc.tag, tc.first, matches)
		}
		for _, match := range matches {
			entry, _ := c.Get(match.ID)
			if !hasTag(entry, tc.tag) {
				t.Error("search ignored tag filter")
			}
		}
	}
	if len(c.Search("zzzznonexistenttechnique", "", 5)) != 0 || len(c.Search(" ", "", 5)) != 0 {
		t.Fatal("unmatched queries returned unrelated entries")
	}
	if got := c.Recall("capture a CTF flag from an authorized target", 3); len(got) != 0 {
		t.Fatalf("generic goal flooded automatic recall: %+v", got)
	}
	if got := c.Recall("capture a CTF flag; SSRF endpoint", 3); len(got) == 0 || got[0].ID != "kb006" {
		t.Fatalf("specific prerequisite was lost: %+v", got)
	}
}

func TestExportWorksOfflineIsIdempotentAndPreservesEdits(t *testing.T) {
	c := testCatalog(t)
	root := filepath.Join(t.TempDir(), "nested", "bundle")
	if err := c.Export(root); err != nil {
		t.Fatal(err)
	}
	if err := c.Export(root); err != nil {
		t.Fatalf("identical bundle cannot be reused: %v", err)
	}
	for _, name := range []string{"LICENSE.sapper-v2", "provenance.json", "skills/hotmouse-ctf/references/verification.md", "skills/README.md", "skills/web-exploit/SKILL.md", "skills/flag-hunting/SKILL.md", "entries/kb056.json"} {
		if _, err := os.ReadFile(filepath.Join(root, name)); err != nil {
			t.Fatalf("missing exported resource %s: %v", name, err)
		}
	}
	file := filepath.Join(root, "entries", "kb001.json")
	if err := os.WriteFile(file, []byte("operator note"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.Export(root); err == nil || !strings.Contains(err.Error(), "different content") {
		t.Fatalf("edited resource silently replaced: %v", err)
	}
	data, _ := os.ReadFile(file)
	if string(data) != "operator note" {
		t.Fatal("export overwrote existing content")
	}
}

func TestExportRejectsSymlinkDirectory(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "entries")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := testCatalog(t).Export(root); err == nil {
		t.Fatal("export followed a symlink")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("export wrote through the symlink")
	}
}

func TestExportPreservesImportedSkillEdits(t *testing.T) {
	c := testCatalog(t)
	root := t.TempDir()
	if err := c.Export(root); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "skills", "web-exploit", "SKILL.md")
	if err := os.WriteFile(file, []byte("operator's adapted skill"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.Export(root); err == nil || !strings.Contains(err.Error(), "different content") {
		t.Fatalf("imported skill overwritten: %v", err)
	}
}
