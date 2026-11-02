package apiclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRedirectsNeverForwardCredentialsOrReplayWrites(t *testing.T) {
	var requests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); fmt.Fprint(w, `{}`) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	client := New(source.URL, time.Second)
	client.Token = "internal-secret"
	_, err := client.Do(t.Context(), "POST", "/projects", map[string]any{"title": "demo"}, false)
	if err == nil || requests.Load() != 0 {
		t.Fatalf("redirect followed: err=%v requests=%d", err, requests.Load())
	}
}

func TestCookieScopeExpiryAndAliases(t *testing.T) {
	var cookie string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		cookie = r.Header.Get("Cookie")
		mu.Unlock()
		if r.URL.Path == "/api/login" {
			http.SetCookie(w, &http.Cookie{Name: "hotmouse_session", Value: "legacy", Path: "/api"})
			http.SetCookie(w, &http.Cookie{Name: "session_token", Value: "canonical", Path: "/api", MaxAge: 60})
		}
		fmt.Fprint(w, `{}`)
	}))
	defer server.Close()
	client := New(server.URL, time.Second)
	client.SaveCookie = func(c *http.Cookie) error { return nil }
	if _, e := client.Do(t.Context(), "POST", "/api/login", nil, true); e != nil {
		t.Fatal(e)
	}
	if client.Cookie.Name != "session_token" || client.Cookie.Expires.Before(time.Now().Add(50*time.Second)) {
		t.Fatalf("cookie not canonical/expiring: %#v", client.Cookie)
	}
	checks := []struct {
		path    string
		edit    func()
		present bool
	}{
		{"/api/me", func() {}, true}, {"/apix", func() {}, false}, {"/api/me", func() { client.Cookie.Secure = true }, false},
		{"/api/me", func() { client.Cookie.Secure = false; client.Cookie.Domain = "other.test" }, false},
		{"/api/me", func() { client.Cookie.Domain = ""; client.Cookie.Expires = time.Now().Add(-time.Second) }, false},
	}
	for _, check := range checks {
		check.edit()
		if _, e := client.Do(t.Context(), "GET", check.path, nil, true); e != nil {
			t.Fatal(e)
		}
		mu.Lock()
		got := cookie
		mu.Unlock()
		if (got != "") != check.present {
			t.Fatalf("cookie scope %s: %q", check.path, got)
		}
	}
}

func TestTransportErrorsDoNotRetryWrites(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		time.Sleep(40 * time.Millisecond)
		fmt.Fprint(w, `{}`)
	}))
	defer server.Close()
	client := New(server.URL, 5*time.Millisecond)
	for _, method := range []string{"GET", "POST"} {
		_, err := client.Do(t.Context(), method, "/test", nil, false)
		var e *Error
		if !errors.As(err, &e) || e.ExitCode != 6 || e.Retryable != (method == "GET") {
			t.Fatalf("%s: %#v", method, err)
		}
	}
	if requests.Load() != 2 {
		t.Fatalf("requests replayed: %d", requests.Load())
	}
}

func TestHTTPErrorShapePreservesDetailWithoutResponseInErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(409)
		fmt.Fprint(w, `{"detail":"currently claimed","extra":"hidden"}`)
	}))
	defer server.Close()
	client := New(server.URL, time.Second)
	_, err := client.Do(t.Context(), "POST", "/step", nil, false)
	var failure *Error
	if !errors.As(err, &failure) || failure.ExitCode != 5 || failure.Message != "currently claimed" || failure.Retryable {
		t.Fatalf("error mapping: %#v", err)
	}
	b, _ := json.Marshal(failure)
	if strings.Contains(string(b), "hidden") {
		t.Fatalf("error included raw response: %s", b)
	}
}
