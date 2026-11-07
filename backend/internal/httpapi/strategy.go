package httpapi

import (
	"fmt"
	"strconv"

	"github.com/kataras/iris/v12"
	"hotmouse/backend/internal/core"
)

func (h *Handler) registerStrategy(app *iris.Application) {
	app.Put("/projects/{project_id:string}/budget", func(ctx iris.Context) {
		var req struct {
			BudgetSeconds *int `json:"budget_seconds"`
		}
		if !read(ctx, &req) {
			return
		}
		if req.BudgetSeconds == nil {
			bad(ctx, fmt.Errorf("budget_seconds is required"))
			return
		}
		v, err := h.DB.SetProjectBudget(ctx.Request().Context(), ctx.Params().Get("project_id"), *req.BudgetSeconds)
		if err != nil {
			bad(ctx, err)
			return
		}
		ctx.JSON(v)
	})
	app.Get("/projects/{project_id:string}/context", func(ctx iris.Context) {
		phase := ctx.URLParamDefault("phase", "decide")
		value, err := h.DB.ModelContext(ctx.Request().Context(), ctx.Params().Get("project_id"), phase, ctx.URLParam("step_id"))
		if err != nil {
			bad(ctx, err)
			return
		}
		ctx.JSON(iris.Map{"phase": phase, "yaml": value})
	})
	app.Get("/projects/{project_id:string}/history", func(ctx iris.Context) {
		limit, _ := strconv.Atoi(ctx.URLParam("limit"))
		v, err := h.DB.SearchHistory(ctx.Request().Context(), ctx.Params().Get("project_id"), ctx.URLParam("q"), limit)
		if err != nil {
			bad(ctx, err)
			return
		}
		ctx.JSON(v)
	})
	for _, kind := range []string{"fact", "step", "finding"} {
		app.Get("/projects/{project_id:string}/"+kind+"s/{node_id:string}", func(ctx iris.Context) {
			v, err := h.DB.NodeDetail(ctx.Request().Context(), ctx.Params().Get("project_id"), kind, ctx.Params().Get("node_id"))
			if err != nil {
				bad(ctx, err)
				return
			}
			ctx.JSON(v)
		})
	}
	app.Get("/projects/{project_id:string}/routes", func(ctx iris.Context) {
		v, err := h.DB.GetProject(ctx.Request().Context(), ctx.Params().Get("project_id"))
		if err != nil {
			bad(ctx, err)
			return
		}
		ctx.JSON(v.Routes)
	})
	app.Get("/projects/{project_id:string}/route-records", func(ctx iris.Context) {
		project := ctx.Params().Get("project_id")
		if _, err := h.DB.GetProject(ctx.Request().Context(), project); err != nil {
			bad(ctx, err)
			return
		}
		v, err := h.DB.RouteRecords(ctx.Request().Context(), project)
		if err != nil {
			bad(ctx, err)
			return
		}
		ctx.JSON(v)
	})
	app.Post("/projects/{project_id:string}/routes", func(ctx iris.Context) {
		var req core.CreateRoute
		if !read(ctx, &req) {
			return
		}
		v, err := h.DB.CreateRoute(ctx.Request().Context(), ctx.Params().Get("project_id"), req)
		if err != nil {
			bad(ctx, err)
			return
		}
		ctx.JSON(v)
	})
	app.Patch("/projects/{project_id:string}/routes/{route_id:string}", func(ctx iris.Context) {
		var req core.UpdateRoute
		if !read(ctx, &req) {
			return
		}
		v, err := h.DB.UpdateRoute(ctx.Request().Context(), ctx.Params().Get("project_id"), ctx.Params().Get("route_id"), req)
		if err != nil {
			bad(ctx, err)
			return
		}
		ctx.JSON(v)
	})
	app.Get("/projects/{project_id:string}/decide/events", func(ctx iris.Context) {
		project := ctx.Params().Get("project_id")
		if _, err := h.DB.GetProject(ctx.Request().Context(), project); err != nil {
			bad(ctx, err)
			return
		}
		v, err := h.DB.PendingDecideEvents(ctx.Request().Context(), project)
		if err != nil {
			bad(ctx, err)
			return
		}
		ctx.JSON(v)
	})
	app.Post("/projects/{project_id:string}/decide/request", func(ctx iris.Context) {
		var req struct {
			Reason string `json:"reason"`
		}
		if !read(ctx, &req) {
			return
		}
		if err := h.DB.RequestDecide(ctx.Request().Context(), ctx.Params().Get("project_id"), req.Reason); err != nil {
			bad(ctx, err)
			return
		}
		ctx.JSON(iris.Map{"queued": true})
	})
	app.Get("/api/experiences", func(ctx iris.Context) {
		limit, _ := strconv.Atoi(ctx.URLParam("limit"))
		v, err := h.DB.SearchExperiences(ctx.Request().Context(), ctx.URLParam("q"), ctx.URLParam("tag"), limit)
		if err != nil {
			bad(ctx, err)
			return
		}
		ctx.JSON(v)
	})
	app.Get("/api/experiences/{experience_id:uint64}", func(ctx iris.Context) {
		id, _ := strconv.ParseInt(ctx.Params().Get("experience_id"), 10, 64)
		v, err := h.DB.GetExperience(ctx.Request().Context(), id)
		if err != nil {
			bad(ctx, err)
			return
		}
		ctx.JSON(v)
	})
	app.Post("/api/experiences", func(ctx iris.Context) {
		var req core.CreateExperience
		if !read(ctx, &req) {
			return
		}
		v, err := h.DB.CreateExperience(ctx.Request().Context(), req)
		if err != nil {
			bad(ctx, err)
			return
		}
		ctx.JSON(v)
	})
	app.Get("/projects/{project_id:string}/flags", func(ctx iris.Context) {
		v, err := h.DB.ListFlags(ctx.Request().Context(), ctx.Params().Get("project_id"))
		if err != nil {
			bad(ctx, err)
			return
		}
		ctx.JSON(v)
	})
	app.Post("/projects/{project_id:string}/flags", func(ctx iris.Context) {
		var req core.CaptureFlag
		if !read(ctx, &req) {
			return
		}
		v, err := h.DB.CaptureFlag(ctx.Request().Context(), ctx.Params().Get("project_id"), req)
		if err != nil {
			bad(ctx, err)
			return
		}
		ctx.JSON(v)
	})
	app.Post("/projects/{project_id:string}/flags/{flag_id:uint64}/attempts", func(ctx iris.Context) {
		id, _ := strconv.ParseInt(ctx.Params().Get("flag_id"), 10, 64)
		v, err := h.DB.BeginFlagAttempt(ctx.Request().Context(), ctx.Params().Get("project_id"), id)
		if err != nil {
			bad(ctx, err)
			return
		}
		ctx.JSON(v)
	})
	app.Patch("/projects/{project_id:string}/flags/{flag_id:uint64}/attempts/{attempt_id:uint64}", func(ctx iris.Context) {
		var req core.ResolveFlagAttempt
		if !read(ctx, &req) {
			return
		}
		flag, _ := strconv.ParseInt(ctx.Params().Get("flag_id"), 10, 64)
		attempt, _ := strconv.ParseInt(ctx.Params().Get("attempt_id"), 10, 64)
		v, err := h.DB.ResolveFlagAttempt(ctx.Request().Context(), ctx.Params().Get("project_id"), flag, attempt, req)
		if err != nil {
			bad(ctx, err)
			return
		}
		ctx.JSON(v)
	})
}
