package cli

import (
	"net/url"

	"github.com/spf13/cobra"
	"hotmouse/backend/internal/apiclient"
)

func (a *app) strategyCommands(root *cobra.Command) {
	routes := group("route", "管理路线与无进展止损")
	routes.AddCommand(a.leaf("list", "列出当前项目路线", 0, 0, "project", func(c *cobra.Command, _ []string) (any, error) {
		p, e := a.project(c, nil)
		if e != nil {
			return nil, e
		}
		return a.get(c, apiclient.Path("projects", p, "routes"))
	}))
	routes.AddCommand(a.leaf("records", "查看搁置与重新开启记录", 0, 0, "project", func(c *cobra.Command, _ []string) (any, error) {
		p, e := a.project(c, nil)
		if e != nil {
			return nil, e
		}
		return a.get(c, apiclient.Path("projects", p, "route-records"))
	}))
	routes.AddCommand(a.graphWrite("create", "创建独立路线", "route", "POST", false, "", []field{str("title", true), alias("goal_id", "goal"), number("no_progress_limit")}, false))
	routes.AddCommand(a.graphWrite("update <ROUTE>", "调整无进展阈值或搁置/重新开启路线；状态变化须说明原因", "route", "PATCH", true, "", []field{str("status", false), number("no_progress_limit"), str("reason", false), str("reopen_condition", false), str("next_direction", false)}, false))
	root.AddCommand(routes)
	experiences := group("experience", "维护跨项目方法与经验")
	search := a.leaf("search", "按关键词和标签检索经验摘要", 0, 0, "none", func(c *cobra.Command, _ []string) (any, error) {
		q, _ := c.Flags().GetString("query")
		tag, _ := c.Flags().GetString("tag")
		limit, _ := c.Flags().GetString("limit")
		return a.get(c, "/api/experiences?q="+url.QueryEscape(q)+"&tag="+url.QueryEscape(tag)+"&limit="+url.QueryEscape(limit))
	})
	search.Flags().String("query", "", "关键词")
	search.Flags().String("tag", "", "标签")
	search.Flags().String("limit", "5", "最多 50 条")
	experiences.AddCommand(search)
	experiences.AddCommand(a.leaf("show <ID>", "读取经验全文", 1, 1, "none", func(c *cobra.Command, args []string) (any, error) {
		return a.get(c, apiclient.Path("api", "experiences", args[0]))
	}))
	create := a.leaf("create", "保存方法、适用条件、失败条件与来源证据", 0, 0, "none", func(c *cobra.Command, _ []string) (any, error) {
		v, e := a.body(c, experienceSchema())
		if e != nil {
			return nil, e
		}
		return a.request(c, "POST", "/api/experiences", v)
	})
	a.bodyFlags(create, experienceSchema())
	experiences.AddCommand(create)
	root.AddCommand(experiences)
	flags := group("flag", "保存 flag 与平台结果台账")
	flags.AddCommand(a.leaf("submit <FLAG>", "向项目绑定的平台提交 flag 并记录判定结果", 1, 1, "project", func(c *cobra.Command, args []string) (any, error) {
		p, err := a.project(c, nil)
		if err != nil {
			return nil, err
		}
		return a.request(c, "POST", apiclient.Path("projects", p, "flags", args[0], "submit"), nil)
	}))
	flags.AddCommand(a.leaf("list", "列出 flag、状态与提交尝试", 0, 0, "project", func(c *cobra.Command, _ []string) (any, error) {
		p, e := a.project(c, nil)
		if e != nil {
			return nil, e
		}
		return a.get(c, apiclient.Path("projects", p, "flags"))
	}))
	flags.AddCommand(a.graphWrite("capture", "引用事实保存 flag（重复值自动去重）", "flag", "POST", false, "", []field{str("value", true), alias("fact_id", "fact"), str("challenge_id", false)}, false))
	flags.AddCommand(a.leaf("begin <FLAG>", "先记录提交尝试；由外部适配器执行平台请求", 1, 1, "project", func(c *cobra.Command, args []string) (any, error) {
		p, e := a.project(c, nil)
		if e != nil {
			return nil, e
		}
		return a.request(c, "POST", apiclient.Path("projects", p, "flags", args[0], "attempts"), map[string]any{})
	}))
	resolveSchema := []field{str("status", true), str("response", false)}
	resolve := a.leaf("resolve <FLAG> <ATTEMPT>", "记录 accepted/rejected/submission_unknown；未知结果必须先核对", 2, 2, "project", func(c *cobra.Command, args []string) (any, error) {
		p, e := a.project(c, nil)
		if e != nil {
			return nil, e
		}
		v, e := a.body(c, resolveSchema)
		if e != nil {
			return nil, e
		}
		return a.request(c, "PATCH", apiclient.Path("projects", p, "flags", args[0], "attempts", args[1]), v)
	})
	a.bodyFlags(resolve, resolveSchema)
	flags.AddCommand(resolve)
	root.AddCommand(flags)
}
func experienceSchema() []field {
	return []field{str("title", true), str("conditions", true), str("method", true), str("failure_conditions", false), str("detail", false), list("tags", false), str("source_project_id", false), str("source_step_id", false), list("source_fact_ids", false)}
}
func (a *app) projectStrategyCommands(g *cobra.Command) {
	budgetSchema := []field{number("budget_seconds")}
	budgetSchema[0].Required = true
	budget := a.leaf("budget [PROJECT]", "修改所有路线共用的项目预算（秒，0 为不限）", 0, 1, "project", func(c *cobra.Command, args []string) (any, error) {
		p, err := a.project(c, args)
		if err != nil {
			return nil, err
		}
		body, err := a.body(c, budgetSchema)
		if err != nil {
			return nil, err
		}
		return a.request(c, "PUT", apiclient.Path("projects", p, "budget"), body)
	})
	a.bodyFlags(budget, budgetSchema)
	budget.Flags().Lookup("budget-seconds").Usage = "项目本轮共享预算（秒，0 为不限）；保留已有累计投入"
	g.AddCommand(budget)
	context := a.leaf("context [PROJECT]", "查看 Decide/Execute 紧凑 YAML 上下文", 0, 1, "project", func(c *cobra.Command, args []string) (any, error) {
		p, e := a.project(c, args)
		if e != nil {
			return nil, e
		}
		phase, _ := c.Flags().GetString("phase")
		step, _ := c.Flags().GetString("step")
		return a.get(c, apiclient.Path("projects", p, "context")+"?phase="+url.QueryEscape(phase)+"&step_id="+url.QueryEscape(step))
	})
	context.Flags().String("phase", "decide", "decide 或 execute")
	context.Flags().String("step", "", "Execute 的步骤 ID")
	g.AddCommand(context)
	recall := a.leaf("recall [PROJECT]", "检索当前项目事实、步骤、发现与完整证据", 0, 1, "project", func(c *cobra.Command, args []string) (any, error) {
		p, e := a.project(c, args)
		if e != nil {
			return nil, e
		}
		q, _ := c.Flags().GetString("query")
		limit, _ := c.Flags().GetString("limit")
		return a.get(c, apiclient.Path("projects", p, "history")+"?q="+url.QueryEscape(q)+"&limit="+url.QueryEscape(limit))
	})
	recall.Flags().String("query", "", "关键词")
	recall.Flags().String("limit", "5", "最多 50 条")
	g.AddCommand(recall)
}
