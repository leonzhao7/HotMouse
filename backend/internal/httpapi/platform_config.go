package httpapi

import (
	"database/sql"
	"os"
	"strings"

	"github.com/kataras/iris/v12"
	"hotmouse/backend/internal/ctfplatform"
)

// Saved service configuration takes precedence over startup environment values.
// The environment map remains immutable; background coordinators read saved
// credentials from SQLite whenever they create a platform connection.
func (h *Handler) platformConfig(id string) (ctfplatform.Config, string, error) {
	var config ctfplatform.Config
	err := h.DB.SQL.QueryRow(`SELECT base_url,token FROM platform_configs WHERE id=?`, id).Scan(&config.BaseURL, &config.Token)
	if err == nil {
		return config, "saved", nil
	}
	if err != sql.ErrNoRows {
		return config, "", err
	}
	if config, ok := h.PlatformConfigs[id]; ok {
		return config, "environment", nil
	}
	return config, "none", nil
}

func (h *Handler) platformRegistered(id string) bool {
	for _, descriptor := range h.Platforms.Descriptors() {
		if descriptor.ID == id {
			return true
		}
	}
	return false
}

func (h *Handler) platformConfigResponse(id string) (iris.Map, error) {
	if !h.platformRegistered(id) {
		return nil, &ctfplatform.Error{HTTPStatus: 404, Code: "platform_not_found", Message: "platform is not registered"}
	}
	config, source, err := h.platformConfig(id)
	if err != nil {
		return nil, err
	}
	return iris.Map{"base_url": config.BaseURL, "token_set": config.Token != "", "configured": source != "none", "source": source}, nil
}

func (h *Handler) registerPlatformConfig(app *iris.Application) {
	app.Get("/api/platforms/{platform:string}/config", func(ctx iris.Context) {
		value, err := h.platformConfigResponse(ctx.Params().Get("platform"))
		if err != nil {
			platformError(ctx, err)
			return
		}
		ctx.JSON(value)
	})
	app.Put("/api/platforms/{platform:string}/config", func(ctx iris.Context) {
		id := ctx.Params().Get("platform")
		if !h.platformRegistered(id) {
			platformError(ctx, &ctfplatform.Error{HTTPStatus: 404, Code: "platform_not_found", Message: "platform is not registered"})
			return
		}
		var request struct {
			BaseURL string  `json:"base_url"`
			Token   *string `json:"token"`
		}
		if !read(ctx, &request) {
			return
		}
		// Serialize with competition starts and imports. Changing a live task's
		// credentials would prevent automatic flag submission and container cleanup.
		h.platformMu.Lock()
		defer h.platformMu.Unlock()
		previous, _, err := h.platformConfig(id)
		if err != nil {
			platformError(ctx, err)
			return
		}
		config := previous
		config.BaseURL = strings.TrimRight(strings.TrimSpace(request.BaseURL), "/")
		if request.Token != nil && *request.Token != "***" {
			config.Token = *request.Token
		}
		if len(config.BaseURL) > 4096 || len(config.Token) > 8192 {
			platformError(ctx, &ctfplatform.Error{HTTPStatus: 422, Code: "invalid_configuration", Message: "platform URL or token is too long"})
			return
		}
		if _, err := h.Platforms.Connect(id, config); err != nil {
			platformError(ctx, err)
			return
		}
		if previous.ScopeID(id) != config.ScopeID(id) {
			var active bool
			err := h.DB.SQL.QueryRowContext(ctx.Request().Context(), `SELECT EXISTS(SELECT 1 FROM competition_runs WHERE platform=? AND status IN ('planning','running','stopping')) OR EXISTS(SELECT 1 FROM platform_projects b JOIN projects p ON p.id=b.project_id WHERE b.platform=? AND p.status='active')`, id, id).Scan(&active)
			if err != nil {
				platformError(ctx, err)
				return
			}
			if active {
				platformError(ctx, &ctfplatform.Error{HTTPStatus: 409, Code: "platform_task_active", Message: "stop active competitions and platform projects before changing task credentials"})
				return
			}
		}
		for _, path := range []string{h.DB.Path, h.DB.Path + "-wal", h.DB.Path + "-shm"} {
			if err := os.Chmod(path, 0600); err != nil && !os.IsNotExist(err) {
				platformError(ctx, err)
				return
			}
		}
		if _, err := h.DB.SQL.ExecContext(ctx.Request().Context(), `INSERT INTO platform_configs(id,base_url,token,updated_at) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET base_url=excluded.base_url,token=excluded.token,updated_at=excluded.updated_at`, id, config.BaseURL, config.Token, nowString()); err != nil {
			platformError(ctx, err)
			return
		}
		value, err := h.platformConfigResponse(id)
		if err != nil {
			platformError(ctx, err)
			return
		}
		ctx.JSON(value)
	})
}
