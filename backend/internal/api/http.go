package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/kataras/iris/v12"
)

type Handler struct {
	service *Service
}

func NewApp(service *Service) *iris.Application {
	app := iris.New()
	app.Configure(iris.WithFireMethodNotAllowed)
	handler := &Handler{service: service}

	app.Get("/project", handler.listProjects)
	app.Post("/project", handler.createProject)
	app.Get("/project/{id:uint64}/export", handler.exportProject)
	app.Get("/project/{id:uint64}/goal", handler.getGoal)
	app.Post("/project/{id:uint64}/goal", handler.completeProject)
	app.Post("/project/{id:uint64}/abort", handler.abortProject)
	app.Get("/project/{id:uint64}/fact", handler.listFacts)
	app.Post("/project/{id:uint64}/fact", handler.addFact)
	app.Get("/project/{id:uint64}/step", handler.listSteps)
	app.Post("/project/{id:uint64}/step", handler.addStep)
	app.Get("/settings", handler.getSettings)
	app.Put("/settings", handler.updateSettings)
	app.Get("/llm-log", handler.listLLMLogs)
	app.OnErrorCode(http.StatusNotFound, func(ctx iris.Context) {
		ctx.JSON(errorBody{Error: errorDetail{Code: "not_found", Message: "resource not found"}})
	})
	app.OnErrorCode(http.StatusMethodNotAllowed, func(ctx iris.Context) {
		ctx.JSON(errorBody{Error: errorDetail{Code: "method_not_allowed", Message: "method not allowed"}})
	})
	return app
}

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (handler *Handler) listProjects(ctx iris.Context) {
	projects, err := handler.service.ListProjects(ctx.Request().Context())
	if err != nil {
		writeError(ctx, err)
		return
	}
	ctx.JSON(projects)
}

func (handler *Handler) createProject(ctx iris.Context) {
	var input CreateProjectInput
	if !readJSON(ctx, &input, false) {
		return
	}
	project, err := handler.service.CreateProject(ctx.Request().Context(), input)
	if err != nil {
		writeError(ctx, err)
		return
	}
	ctx.StatusCode(http.StatusCreated)
	ctx.JSON(project)
}

func (handler *Handler) exportProject(ctx iris.Context) {
	projectID, ok := readProjectID(ctx)
	if !ok {
		return
	}
	graph, err := handler.service.ExportProject(ctx.Request().Context(), projectID)
	if err != nil {
		writeError(ctx, err)
		return
	}
	content, err := json.MarshalIndent(graph, "", "  ")
	if err != nil {
		writeError(ctx, err)
		return
	}
	ctx.ContentType("application/json")
	ctx.Header("Content-Disposition", "attachment; filename=\"project-"+strconv.FormatUint(uint64(projectID), 10)+".json\"")
	if _, err := ctx.Write(content); err != nil {
		ctx.Application().Logger().Errorf("write project export: %v", err)
	}
}

func (handler *Handler) getGoal(ctx iris.Context) {
	projectID, ok := readProjectID(ctx)
	if !ok {
		return
	}
	goal, err := handler.service.GetGoal(ctx.Request().Context(), projectID)
	if err != nil {
		writeError(ctx, err)
		return
	}
	ctx.JSON(goal)
}

func (handler *Handler) completeProject(ctx iris.Context) {
	projectID, ok := readProjectID(ctx)
	if !ok {
		return
	}
	var input struct{}
	if !readJSON(ctx, &input, true) {
		return
	}
	project, err := handler.service.CompleteProject(ctx.Request().Context(), projectID)
	if err != nil {
		writeError(ctx, err)
		return
	}
	ctx.JSON(project)
}

func (handler *Handler) abortProject(ctx iris.Context) {
	projectID, ok := readProjectID(ctx)
	if !ok {
		return
	}
	var input struct{}
	if !readJSON(ctx, &input, true) {
		return
	}
	project, err := handler.service.AbortProject(ctx.Request().Context(), projectID)
	if err != nil {
		writeError(ctx, err)
		return
	}
	ctx.JSON(project)
}

func (handler *Handler) listFacts(ctx iris.Context) {
	projectID, ok := readProjectID(ctx)
	if !ok {
		return
	}
	facts, err := handler.service.ListFacts(ctx.Request().Context(), projectID)
	if err != nil {
		writeError(ctx, err)
		return
	}
	ctx.JSON(facts)
}

func (handler *Handler) addFact(ctx iris.Context) {
	projectID, ok := readProjectID(ctx)
	if !ok {
		return
	}
	var input struct {
		Content string `json:"content"`
	}
	if !readJSON(ctx, &input, false) {
		return
	}
	fact, err := handler.service.AddFact(ctx.Request().Context(), projectID, input.Content)
	if err != nil {
		writeError(ctx, err)
		return
	}
	ctx.StatusCode(http.StatusCreated)
	ctx.JSON(fact)
}

func (handler *Handler) listSteps(ctx iris.Context) {
	projectID, ok := readProjectID(ctx)
	if !ok {
		return
	}
	steps, err := handler.service.ListSteps(ctx.Request().Context(), projectID)
	if err != nil {
		writeError(ctx, err)
		return
	}
	ctx.JSON(steps)
}

func (handler *Handler) addStep(ctx iris.Context) {
	projectID, ok := readProjectID(ctx)
	if !ok {
		return
	}
	var input struct {
		FromNodeID  uint   `json:"from_node_id"`
		ToNodeID    uint   `json:"to_node_id"`
		Description string `json:"description"`
	}
	if !readJSON(ctx, &input, false) {
		return
	}
	step, err := handler.service.AddStep(ctx.Request().Context(), projectID, input.FromNodeID, input.ToNodeID, input.Description)
	if err != nil {
		writeError(ctx, err)
		return
	}
	ctx.StatusCode(http.StatusCreated)
	ctx.JSON(step)
}

func (handler *Handler) getSettings(ctx iris.Context) {
	settings, err := handler.service.GetSettings(ctx.Request().Context())
	if err != nil {
		writeError(ctx, err)
		return
	}
	ctx.JSON(settings)
}

func (handler *Handler) updateSettings(ctx iris.Context) {
	var input UpdateSettingsInput
	if !readJSON(ctx, &input, false) {
		return
	}
	settings, err := handler.service.UpdateSettings(ctx.Request().Context(), input)
	if err != nil {
		writeError(ctx, err)
		return
	}
	ctx.JSON(settings)
}

func (handler *Handler) listLLMLogs(ctx iris.Context) {
	logs, err := handler.service.ListLLMLogs(ctx.Request().Context())
	if err != nil {
		writeError(ctx, err)
		return
	}
	ctx.JSON(logs)
}

func readProjectID(ctx iris.Context) (uint, bool) {
	parsed, err := strconv.ParseUint(ctx.Params().Get("id"), 10, strconv.IntSize-1)
	if err != nil || parsed == 0 {
		writeBadRequest(ctx, "invalid_project_id", "project id must be a positive integer")
		return 0, false
	}
	return uint(parsed), true
}

func readJSON(ctx iris.Context, input any, allowEmpty bool) bool {
	contentType := ctx.GetHeader("Content-Type")
	if allowEmpty && ctx.Request().ContentLength == 0 && len(ctx.Request().TransferEncoding) == 0 {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "application/json" {
		ctx.StatusCode(http.StatusUnsupportedMediaType)
		ctx.JSON(errorBody{Error: errorDetail{Code: "unsupported_media_type", Message: "use Content-Type: application/json"}})
		return false
	}
	ctx.SetMaxRequestBodySize(1 << 20)
	decoder := json.NewDecoder(ctx.Request().Body)
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		if allowEmpty && errors.Is(err, io.EOF) {
			return true
		}
		return writeJSONDecodeError(ctx, err)
	}
	if len(raw) == 0 || raw[0] != '{' {
		writeBadRequest(ctx, "invalid_json", "request body must be a JSON object")
		return false
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return writeJSONDecodeError(ctx, err)
	}
	payloadDecoder := json.NewDecoder(bytes.NewReader(raw))
	payloadDecoder.DisallowUnknownFields()
	if err := payloadDecoder.Decode(input); err != nil {
		return writeJSONDecodeError(ctx, err)
	}
	return true
}

func writeJSONDecodeError(ctx iris.Context, err error) bool {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		ctx.StatusCode(http.StatusRequestEntityTooLarge)
		ctx.JSON(errorBody{Error: errorDetail{Code: "body_too_large", Message: "request body exceeds 1 MiB"}})
		return false
	}
	writeBadRequest(ctx, "invalid_json", "request body must be one JSON object with only documented fields")
	return false
}

func writeBadRequest(ctx iris.Context, code, message string) {
	ctx.StatusCode(http.StatusBadRequest)
	ctx.JSON(errorBody{Error: errorDetail{Code: code, Message: message}})
}

func writeError(ctx iris.Context, err error) {
	status := http.StatusInternalServerError
	code := "internal_error"
	switch {
	case errors.Is(err, ErrInvalid):
		status, code = http.StatusBadRequest, "invalid_graph"
	case errors.Is(err, ErrInvalidSettings):
		status, code = http.StatusBadRequest, "invalid_settings"
	case errors.Is(err, ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, ErrConflict):
		status, code = http.StatusConflict, "duplicate_step"
	case errors.Is(err, ErrGoalUnreachable):
		status, code = http.StatusConflict, "goal_unreachable"
	case errors.Is(err, ErrProjectClosed):
		status, code = http.StatusConflict, "project_completed"
	case errors.Is(err, ErrProjectAborted):
		status, code = http.StatusConflict, "project_aborted"
	}
	ctx.StatusCode(status)
	message := err.Error()
	if status == http.StatusInternalServerError {
		ctx.Application().Logger().Errorf("request failed: %v", err)
		message = "an internal error occurred"
	}
	ctx.JSON(errorBody{Error: errorDetail{Code: code, Message: message}})
}
