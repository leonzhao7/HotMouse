package clientconfig

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestStoreCredentialsAndPrivateAtomicWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client", "config.yaml")
	store, e := Load(path)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("read-only load created configuration")
	}
	store.Config.Profiles["local"]["worker"] = "agent"
	if e = store.Save(); e != nil {
		t.Fatal(e)
	}
	cookie := &http.Cookie{Name: "session_token", Value: "session", Expires: time.Now().Add(time.Hour)}
	if e = store.SaveCookie("local", DefaultServer, cookie); e != nil {
		t.Fatal(e)
	}
	if c, e := store.Cookie("other", DefaultServer); e != nil || c != nil {
		t.Fatal("cookie crossed profiles")
	}
	if c, e := store.Cookie("local", "http://localhost:8081"); e != nil || c != nil {
		t.Fatal("cookie crossed services")
	}
	if c, e := store.Cookie("local", DefaultServer); e != nil || c.Value != "session" {
		t.Fatal("cookie not restored")
	}
	if e = store.RemoveCredentials("local"); e != nil {
		t.Fatal(e)
	}
	if c, _ := store.Cookie("local", DefaultServer); c != nil {
		t.Fatal("credentials not removed")
	}
	if runtime.GOOS != "windows" {
		for _, p := range []string{path, store.credentialsPath()} {
			st, _ := os.Stat(p)
			if st.Mode().Perm() != 0600 {
				t.Fatalf("private file mode: %o", st.Mode().Perm())
			}
		}
	}
	b, _ := os.ReadFile(path)
	if e = WritePrivate(path, []byte("other"), false); !errors.Is(e, os.ErrExist) {
		t.Fatalf("overwrite guard: %v", e)
	}
	after, _ := os.ReadFile(path)
	if string(b) != string(after) {
		t.Fatal("failed write changed file")
	}
}

func TestNormalizeAndStrictConfiguration(t *testing.T) {
	server, e := NormalizeServer("https://EXAMPLE.test:443/")
	if e != nil || server != "https://example.test" {
		t.Fatalf("normalize: %s %v", server, e)
	}
	for _, url := range []string{"ftp://example.test", "https://user:password@example.test", "http://example.test?secret=yes", "http://example.test#fragment"} {
		if _, e := NormalizeServer(url); e == nil {
			t.Fatalf("invalid server accepted: %s", url)
		}
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(path, []byte("schema_version: 1\ncurrent_profile: local\nprofiles:\n  local:\n    password: secret\n"), 0600)
	if _, e := Load(path); e == nil {
		t.Fatal("unknown profile field accepted")
	}
}
