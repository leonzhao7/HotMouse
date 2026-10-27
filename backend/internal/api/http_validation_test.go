package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kataras/iris/v12"

	"hotmouse/backend/internal/domain"
)

func newTestApp(test *testing.T) *iris.Application {
	test.Helper()
	app := NewApp(newTestService(test))
	if err := app.Build(); err != nil {
		test.Fatal(err)
	}
	return app
}

func createHTTPGraph(test *testing.T, app *iris.Application) domain.Graph {
	test.Helper()
	response := serveJSON(test, app, http.MethodPost, "/project", `{"name":"project","goal":"goal","facts":["one","two"]}`)
	if response.Code != http.StatusCreated {
		test.Fatalf("create HTTP graph: status=%d body=%s", response.Code, response.Body.String())
	}
	var graph domain.Graph
	if err := json.Unmarshal(response.Body.Bytes(), &graph); err != nil {
		test.Fatal(err)
	}
	return graph
}

func assertHTTPError(test *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	test.Helper()
	if response.Code != status {
		test.Fatalf("response status = %d; want %d; body = %s", response.Code, status, response.Body.String())
	}
	var body errorBody
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Error.Code != code || body.Error.Message == "" {
		test.Fatalf("error response = %s, decode error = %v; want code %q", response.Body.String(), err, code)
	}
	if !strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") {
		test.Fatalf("error was not JSON: %v", response.Header())
	}
}

func TestHTTPProjectCompletionAndReadResources(test *testing.T) {
	app := newTestApp(test)
	graph := createHTTPGraph(test, app)
	goalPath := fmt.Sprintf("/project/%d/goal", graph.ID)
	factPath := fmt.Sprintf("/project/%d/fact", graph.ID)
	stepPath := fmt.Sprintf("/project/%d/step", graph.ID)
	response := serveJSON(test, app, http.MethodPost, goalPath, "")
	assertHTTPError(test, response, http.StatusConflict, "goal_unreachable")
	response = serveJSON(test, app, http.MethodPost, goalPath, `{"content":"mutate goal"}`)
	assertHTTPError(test, response, http.StatusBadRequest, "invalid_json")
	stepBody := fmt.Sprintf(`{"from_node_id":%d,"to_node_id":%d}`, graph.Facts[0].ID, graph.Facts[1].ID)
	response = serveJSON(test, app, http.MethodPost, stepPath, stepBody)
	if response.Code != http.StatusCreated {
		test.Fatalf("fact to fact step: %d %s", response.Code, response.Body.String())
	}
	assertHTTPError(test, serveJSON(test, app, http.MethodPost, goalPath, ""), http.StatusConflict, "goal_unreachable")
	assertHTTPError(test, serveJSON(test, app, http.MethodPost, stepPath, stepBody), http.StatusConflict, "duplicate_step")
	reverseBody := fmt.Sprintf(`{"from_node_id":%d,"to_node_id":%d}`, graph.Facts[1].ID, graph.Facts[0].ID)
	assertHTTPError(test, serveJSON(test, app, http.MethodPost, stepPath, reverseBody), http.StatusConflict, "duplicate_step")
	goalOrigin := fmt.Sprintf(`{"from_node_id":%d,"to_node_id":%d}`, graph.Goal.ID, graph.Facts[0].ID)
	assertHTTPError(test, serveJSON(test, app, http.MethodPost, stepPath, goalOrigin), http.StatusBadRequest, "invalid_graph")
	goalStepBody := fmt.Sprintf(`{"from_node_id":%d,"to_node_id":%d}`, graph.Facts[1].ID, graph.Goal.ID)
	response = serveJSON(test, app, http.MethodPost, stepPath, goalStepBody)
	if response.Code != http.StatusCreated {
		test.Fatalf("fact to goal step: %d %s", response.Code, response.Body.String())
	}
	request := httptest.NewRequest(http.MethodPost, goalPath, nil)
	response = httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		test.Fatalf("complete without request body or content type: %d %s", response.Code, response.Body.String())
	}
	var completed domain.Project
	if err := json.Unmarshal(response.Body.Bytes(), &completed); err != nil || completed.Status != domain.ProjectCompleted || completed.CompletedAt == nil {
		test.Fatalf("completion response = %+v, error = %v", completed, err)
	}
	repeated := serveJSON(test, app, http.MethodPost, goalPath, `{}`)
	if repeated.Code != http.StatusOK || repeated.Body.String() != response.Body.String() {
		test.Fatalf("non-idempotent completion: %s -> %s", response.Body.String(), repeated.Body.String())
	}
	assertHTTPError(test, serveJSON(test, app, http.MethodPost, factPath, `{"content":"late fact"}`), http.StatusConflict, "project_completed")
	lateStepBody := fmt.Sprintf(`{"from_node_id":%d,"to_node_id":%d}`, graph.Facts[0].ID, graph.Goal.ID)
	assertHTTPError(test, serveJSON(test, app, http.MethodPost, stepPath, lateStepBody), http.StatusConflict, "project_completed")
	var facts []domain.Node
	response = serveJSON(test, app, http.MethodGet, factPath, "")
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &facts) != nil || len(facts) != 2 {
		test.Fatalf("facts response: %d %s", response.Code, response.Body.String())
	}
	var steps []domain.Step
	response = serveJSON(test, app, http.MethodGet, stepPath, "")
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &steps) != nil || len(steps) != 2 {
		test.Fatalf("steps response: %d %s", response.Code, response.Body.String())
	}
	var goal domain.Node
	response = serveJSON(test, app, http.MethodGet, goalPath, "")
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &goal) != nil || !sameNode(goal, graph.Goal) {
		test.Fatalf("goal changed during completion: %d %s", response.Code, response.Body.String())
	}
}

func TestHTTPRejectsInvalidBodies(test *testing.T) {
	app := newTestApp(test)
	valid := `{"name":"project","goal":"goal","facts":["fact"]}`
	cases := []struct {
		name string
		body string
		code string
	}{
		{"empty", "", "invalid_json"},
		{"malformed", "{", "invalid_json"},
		{"trailing JSON", valid + "{}", "invalid_json"},
		{"trailing garbage", valid + "junk", "invalid_json"},
		{"null", "null", "invalid_json"},
		{"array", "[]", "invalid_json"},
		{"wrong field type", `{"name":1}`, "invalid_json"},
		{"unknown field", `{"name":"project","goal":"goal","facts":["fact"],"id":2}`, "invalid_json"},
		{"missing fields", `{}`, "invalid_graph"},
		{"blank name", `{"name":" ","goal":"goal","facts":["fact"]}`, "invalid_graph"},
		{"null goal", `{"name":"project","goal":null,"facts":["fact"]}`, "invalid_graph"},
		{"empty facts", `{"name":"project","goal":"goal","facts":[]}`, "invalid_graph"},
		{"blank fact", `{"name":"project","goal":"goal","facts":[" "]}`, "invalid_graph"},
	}
	for _, entry := range cases {
		test.Run(entry.name, func(test *testing.T) {
			assertHTTPError(test, serveJSON(test, app, http.MethodPost, "/project", entry.body), http.StatusBadRequest, entry.code)
		})
	}
	response := serveJSON(test, app, http.MethodPost, "/project", `{"name":"`+strings.Repeat("x", 1<<20)+`"}`)
	assertHTTPError(test, response, http.StatusRequestEntityTooLarge, "body_too_large")
	response = serveJSON(test, app, http.MethodPost, "/project", valid+strings.Repeat(" ", 1<<20))
	assertHTTPError(test, response, http.StatusRequestEntityTooLarge, "body_too_large")
	request := httptest.NewRequest(http.MethodPost, "/project", strings.NewReader(valid))
	request.Header.Set("Content-Type", "text/plain")
	response = httptest.NewRecorder()
	app.ServeHTTP(response, request)
	assertHTTPError(test, response, http.StatusUnsupportedMediaType, "unsupported_media_type")
	response = serveJSON(test, app, http.MethodGet, "/project", "")
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != "[]" {
		test.Fatalf("invalid requests wrote data: %s", response.Body.String())
	}
}

func TestHTTPOnlyAllowedRoutesAndAppendMethods(test *testing.T) {
	app := newTestApp(test)
	graph := createHTTPGraph(test, app)
	allowed := map[string]bool{
		"/project":                    true,
		"/project/{id:uint64}/export": true,
		"/project/{id:uint64}/goal":   true,
		"/project/{id:uint64}/fact":   true,
		"/project/{id:uint64}/step":   true,
		"/settings":                   true,
	}
	methodPaths := 0
	for _, route := range app.GetRoutes() {
		if route.StatusCode != 0 {
			if route.StatusCode != http.StatusNotFound && route.StatusCode != http.StatusMethodNotAllowed {
				test.Fatalf("unexpected error handler: %d", route.StatusCode)
			}
			continue
		}
		methodPaths++
		validMethod := route.Method == http.MethodGet || route.Method == http.MethodPost ||
			(route.Tmpl().Src == "/settings" && route.Method == http.MethodPut)
		if !allowed[route.Tmpl().Src] || !validMethod {
			test.Fatalf("unexpected route: %s %s", route.Method, route.Path)
		}
	}
	if methodPaths != 11 {
		test.Fatalf("registered %d method/path pairs, want 11", methodPaths)
	}
	paths := []string{"/project", fmt.Sprintf("/project/%d/export", graph.ID), fmt.Sprintf("/project/%d/fact", graph.ID), fmt.Sprintf("/project/%d/step", graph.ID), fmt.Sprintf("/project/%d/goal", graph.ID)}
	for _, path := range paths {
		for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete} {
			assertHTTPError(test, serveJSON(test, app, method, path, `{}`), http.StatusMethodNotAllowed, "method_not_allowed")
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete} {
		assertHTTPError(test, serveJSON(test, app, method, "/settings", `{}`), http.StatusMethodNotAllowed, "method_not_allowed")
	}
	for _, path := range []string{"/", "/health", "/api/project", "/settings/1", "/project/1", "/project/1/complete", "/project/1/facts", "/project/1/fact/2", "/project/nope/goal", "/project/-1/goal"} {
		assertHTTPError(test, serveJSON(test, app, http.MethodGet, path, ""), http.StatusNotFound, "not_found")
	}
	for _, path := range []string{"/project/0/goal", "/project/9223372036854775808/goal"} {
		assertHTTPError(test, serveJSON(test, app, http.MethodGet, path, ""), http.StatusBadRequest, "invalid_project_id")
	}
	for _, suffix := range []string{"goal", "fact", "step"} {
		path := "/project/999/" + suffix
		assertHTTPError(test, serveJSON(test, app, http.MethodGet, path, ""), http.StatusNotFound, "not_found")
	}
}

func TestHTTPRejectsCrossProjectSteps(test *testing.T) {
	app := newTestApp(test)
	graph := createHTTPGraph(test, app)
	other := createHTTPGraph(test, app)
	body := fmt.Sprintf(`{"from_node_id":%d,"to_node_id":%d}`, graph.Facts[0].ID, other.Goal.ID)
	path := fmt.Sprintf("/project/%d/step", graph.ID)
	assertHTTPError(test, serveJSON(test, app, http.MethodPost, path, body), http.StatusNotFound, "not_found")
}

func TestHTTPDoesNotExposeDatabaseErrors(test *testing.T) {
	service := newTestService(test)
	app := NewApp(service)
	if err := app.Build(); err != nil {
		test.Fatal(err)
	}
	sqlDB, err := service.db.DB()
	if err != nil {
		test.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		test.Fatal(err)
	}
	response := serveJSON(test, app, http.MethodGet, "/project", "")
	assertHTTPError(test, response, http.StatusInternalServerError, "internal_error")
	if strings.Contains(response.Body.String(), "closed") || strings.Contains(response.Body.String(), "SELECT") {
		test.Fatalf("internal error leaked database details: %s", response.Body.String())
	}
}
