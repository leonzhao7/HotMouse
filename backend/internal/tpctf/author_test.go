package tpctf

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hotmouse/backend/internal/worker"
)

func templateFixture(t *testing.T, name string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	content := []byte("template instructions")
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}
func TestAuthoringUsesSeparateTokenAndDoesNotRepeatAcceptedUpload(t *testing.T) {
	uploads, models := 0, 0
	slug := ""
	template := templateFixture(t, "official/README.md")
	r := fixtureRunner(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer pf_author" {
			t.Error("wrong authoring token")
		}
		switch req.Method + " " + req.URL.Path {
		case "GET /api/v1/competitions/cid/authoring/quota":
			respond(w, map[string]any{"upload_window": map[string]any{"starts_at": "round1", "remaining": 1}, "smoke_charged_to": "team", "smoke_remaining": 5})
		case "GET /api/v1/competitions/cid/topics":
			respond(w, map[string]any{"topics": []any{map[string]any{"id": "topic1", "released": true}}})
		case "GET /api/v1/authoring/template":
			_, _ = w.Write(template)
		case "GET /api/v1/competitions/cid/authored-challenges":
			if uploads == 0 {
				respond(w, []Authored{})
			} else {
				respond(w, []Authored{{ID: "AUTHORED-1", Slug: slug, Review: "pending", Smoke: "solved"}})
			}
		case "GET /api/v1/competitions/cid/authored-challenges/AUTHORED-1/audit":
			respond(w, map[string]any{"errors": 0})
		case "POST /api/v1/competitions/cid/authored-challenges":
			uploads++
			if err := req.ParseMultipartForm(4 << 20); err != nil {
				t.Error(err)
			}
			slug = req.FormValue("slug")
			if req.FormValue("ai_assisted") != "yes" || req.FormValue("provenance") != "original" {
				t.Error("missing declarations")
			}
			file, _, err := req.FormFile("file")
			if err != nil {
				t.Error(err)
			} else {
				defer file.Close()
				data, _ := io.ReadAll(file)
				if len(data) == 0 {
					t.Error("empty archive")
				}
			}
			respond(w, Authored{ID: "AUTHORED-1", Slug: slug, Review: "pending"})
		default:
			t.Errorf("unexpected %s %s", req.Method, req.URL.Path)
		}
	})
	author, _ := NewClient(r.Config.BaseURL, "pf_author")
	r.AuthorClient = author
	r.Config.AuthorCategory = "web"
	r.Config.AuthorStack = "python-flask"
	r.Config.License = "platform-only"
	r.RunAgent = func(_ context.Context, a worker.HostedAgent) (worker.HostedAgentResult, error) {
		models++
		root := filepath.Join(a.WorkDir, "challenge")
		if err := os.MkdirAll(filepath.Join(root, "solution"), 0700); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"metadata.yaml", "docker-compose.yml", "solution/exploit.py", "solution/WRITEUP.md"} {
			if err := os.WriteFile(filepath.Join(root, name), []byte("fixture"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		return worker.HostedAgentResult{}, nil
	}
	if err := r.author(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, s := range r.State.Authors {
		s.RetryAt = time.Time{}
	}
	if err := r.author(t.Context()); err != nil {
		t.Fatal(err)
	}
	if uploads != 1 || models != 1 {
		t.Fatalf("uploads=%d models=%d", uploads, models)
	}
}
func TestTemplateTraversalIsRejected(t *testing.T) {
	root := t.TempDir()
	if err := unpackTemplate(filepath.Join(root, "template"), templateFixture(t, "../escape")); err == nil {
		t.Fatal("template traversal accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "escape")); !os.IsNotExist(err) {
		t.Fatal("template escaped extraction directory")
	}
}
