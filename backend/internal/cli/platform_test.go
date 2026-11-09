package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPlatformCLIWorkflow(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/openapi/v1/challenges":
			fmt.Fprint(w, `[{"unique_code":"web","description":"challenge","flag_count":1,"container_status":"stopped","container_addr":[]}]`)
		case "/openapi/v1/challenges/start":
			fmt.Fprint(w, `{"unique_code":"web","container_addr":["10.0.0.1:8080"]}`)
		case "/openapi/v1/challenges/submit":
			w.WriteHeader(409)
			fmt.Fprint(w, `{"code":"duplicate","message":"flag already submitted","detail":{}}`)
		case "/openapi/v1/challenges/hint":
			fmt.Fprint(w, `{"unique_code":"web","hint":null}`)
		case "/openapi/v1/challenges/close":
			fmt.Fprint(w, `{"unique_code":"web","closed":true}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	t.Setenv("BENCHMARK_BASE_URL", upstream.URL)
	t.Setenv("BENCHMARK_TOKEN", "task-token")
	h := newHarness(t, false)
	h.ok("platform", "list")
	h.ok("platform", "challenges", "tsec")
	h.ok("platform", "start", "tsec", "web")
	project := h.ok("platform", "import", "tsec", "web", "--use", "--budget-seconds", "120", "--max-workers", "2")
	p := textValue(object(object(project)["project"])["id"])
	flag := h.ok("flag", "capture", "--value", "flag{test}", "--fact", "origin", "--challenge-id", "web")
	result := h.ok("flag", "submit", textValue(object(flag)["id"]))
	if object(object(result)["attempt"])["status"] != "accepted" || object(result)["duplicate"] != true {
		t.Fatalf("submission ledger: %#v", result)
	}
	if p == "" {
		t.Fatal("missing imported project")
	}
	h.ok("platform", "hint", "tsec", "web")
	h.ok("platform", "close", "tsec", "web")
	code, _, errOut := h.run("flag{test}", "platform", "submit", "tsec", "web", "--flag-file", "-")
	if code != 5 || !strings.Contains(errOut, `"code":"duplicate"`) || !strings.Contains(errOut, "flag already submitted") {
		t.Fatalf("business error lost: code=%d %s", code, errOut)
	}
}
