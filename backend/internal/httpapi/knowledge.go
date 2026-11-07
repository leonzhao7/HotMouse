package httpapi

import (
	"strconv"
	"strings"

	"github.com/kataras/iris/v12"
	"hotmouse/backend/internal/knowledge"
)

func (h *Handler) registerKnowledge(app *iris.Application) {
	withCatalog := func(handler func(iris.Context, *knowledge.Catalog)) func(iris.Context) {
		return func(ctx iris.Context) {
			catalog, err := knowledge.Load()
			if err != nil {
				ctx.StopWithJSON(iris.StatusInternalServerError, iris.Map{"detail": err.Error()})
				return
			}
			handler(ctx, catalog)
		}
	}
	app.Get("/api/knowledge", withCatalog(func(ctx iris.Context, catalog *knowledge.Catalog) {
		ctx.JSON(catalog.Info())
	}))
	app.Get("/api/knowledge/entries", withCatalog(func(ctx iris.Context, catalog *knowledge.Catalog) {
		query, tag := strings.TrimSpace(ctx.URLParam("q")), ctx.URLParam("tag")
		limit, err := strconv.Atoi(ctx.URLParamDefault("limit", "50"))
		if err != nil || limit < 1 || limit > 50 {
			ctx.StopWithJSON(iris.StatusBadRequest, iris.Map{"detail": "limit must be between 1 and 50"})
			return
		}
		if query == "" {
			ctx.JSON(catalog.List(tag))
		} else {
			ctx.JSON(catalog.Search(query, tag, limit))
		}
	}))
	app.Get("/api/knowledge/entries/{entry_id:string}", withCatalog(func(ctx iris.Context, catalog *knowledge.Catalog) {
		entry, err := catalog.Get(ctx.Params().Get("entry_id"))
		if err != nil {
			bad(ctx, err)
			return
		}
		ctx.JSON(entry)
	}))
	app.Get("/api/knowledge/skills", withCatalog(func(ctx iris.Context, catalog *knowledge.Catalog) {
		ctx.JSON(catalog.Skills())
	}))
	app.Get("/api/knowledge/skills/{skill_name:string}", withCatalog(func(ctx iris.Context, catalog *knowledge.Catalog) {
		skill, err := catalog.Skill(ctx.Params().Get("skill_name"))
		if err != nil {
			bad(ctx, err)
			return
		}
		ctx.JSON(skill)
	}))
}
