package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProjectRoutesCreateAndAppendOnlyOperations(t *testing.T) {
	app := NewApp(newTestService(t))
	if err := app.Build(); err != nil {
		t.Fatalf("build Iris app: %v", err)
	}
	createBody := `{"name":"roadmap","goal":"launch","facts":["research"]}`
	createResponse := serveJSON(t, app, http.MethodPost, "/project", createBody)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createResponse.Code, createResponse.Body.String())
	}
	var project struct {
		ID uint `json:"id"`
	}
	if err := json.Unmarshal(createResponse.Body.Bytes(), &project); err != nil {
		t.Fatalf("decode project: %v", err)
	}
	if project.ID == 0 {
		t.Fatal("create response omitted project ID")
	}

	goalResponse := serveJSON(t, app, http.MethodGet, "/project/1/goal", "")
	if goalResponse.Code != http.StatusOK {
		t.Fatalf("goal status = %d, body = %s", goalResponse.Code, goalResponse.Body.String())
	}
	var goal struct {
		ID uint `json:"id"`
	}
	if err := json.Unmarshal(goalResponse.Body.Bytes(), &goal); err != nil || goal.ID == 0 {
		t.Fatalf("decode goal = %+v, err = %v", goal, err)
	}
	addFactResponse := serveJSON(t, app, http.MethodPost, "/project/1/fact", `{"content":"prototype"}`)
	if addFactResponse.Code != http.StatusCreated {
		t.Fatalf("add fact status = %d, body = %s", addFactResponse.Code, addFactResponse.Body.String())
	}
	var fact struct {
		ID uint `json:"id"`
	}
	if err := json.Unmarshal(addFactResponse.Body.Bytes(), &fact); err != nil || fact.ID == 0 {
		t.Fatalf("decode fact = %+v, err = %v", fact, err)
	}
	stepBody, err := json.Marshal(map[string]any{
		"from_node_id": fact.ID,
		"to_node_id":   goal.ID,
		"description":  "  connects the prototype to the launch  ",
	})
	if err != nil {
		t.Fatalf("encode step: %v", err)
	}
	stepResponse := serveJSON(t, app, http.MethodPost, "/project/1/step", string(stepBody))
	if stepResponse.Code != http.StatusCreated {
		t.Fatalf("add step status = %d, body = %s", stepResponse.Code, stepResponse.Body.String())
	}
	var step struct {
		ID          uint   `json:"id"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(stepResponse.Body.Bytes(), &step); err != nil || step.ID == 0 {
		t.Fatalf("decode step = %+v, err = %v", step, err)
	}
	if step.Description != "connects the prototype to the launch" {
		t.Fatalf("step description = %q, want trimmed value", step.Description)
	}
	stepsResponse := serveJSON(t, app, http.MethodGet, "/project/1/step", "")
	if stepsResponse.Code != http.StatusOK {
		t.Fatalf("list steps status = %d, body = %s", stepsResponse.Code, stepsResponse.Body.String())
	}
	var listedSteps []struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal(stepsResponse.Body.Bytes(), &listedSteps); err != nil || len(listedSteps) != 1 {
		t.Fatalf("decode listed steps = %+v, err = %v", listedSteps, err)
	}
	if listedSteps[0].Description != "connects the prototype to the launch" {
		t.Fatalf("listed step description = %q", listedSteps[0].Description)
	}
	completeResponse := serveJSON(t, app, http.MethodPost, "/project/1/goal", "")
	if completeResponse.Code != http.StatusOK {
		t.Fatalf("complete status = %d, body = %s", completeResponse.Code, completeResponse.Body.String())
	}
	var completed struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(completeResponse.Body.Bytes(), &completed); err != nil || completed.Status != "completed" {
		t.Fatalf("completion response = %+v, err = %v", completed, err)
	}
	projectsResponse := serveJSON(t, app, http.MethodGet, "/project", "")
	if projectsResponse.Code != http.StatusOK {
		t.Fatalf("list projects status = %d", projectsResponse.Code)
	}
	unsupportedRoute := serveJSON(t, app, http.MethodGet, "/project/1", "")
	if unsupportedRoute.Code != http.StatusNotFound {
		t.Fatalf("unexpected project detail route status = %d", unsupportedRoute.Code)
	}
}

func serveJSON(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
