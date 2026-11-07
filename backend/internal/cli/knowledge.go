package cli

import (
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"hotmouse/backend/internal/apiclient"
	"hotmouse/backend/internal/knowledge"
)

// Bundled reference commands need neither a client Profile nor credentials.
// Preserve global output validation while avoiding service configuration IO.
func (a *app) prepareKnowledge(c *cobra.Command) error {
	if !oneOf(a.output, "table", "json", "yaml") {
		return invalid("offline knowledge output must be table, json or yaml")
	}
	if a.color == "" {
		a.color = "auto"
	}
	if !oneOf(a.color, "auto", "always", "never") {
		return invalid("invalid color")
	}
	if a.selectPath != "" && (c.Flags().Changed("output") || a.quiet) {
		return invalid("--select conflicts with --output and --quiet")
	}
	if c.Flags().Changed("project") || c.Flags().Changed("yes") {
		return invalid("--project and --yes are not applicable to offline knowledge")
	}
	return nil
}

func (a *app) knowledgeCommands(root *cobra.Command) {
	g := group("kb", "离线 CTF 知识与 HotMouse 技能（无需启动服务）")
	leaf := func(use, short string, minArgs, maxArgs int, run func(*cobra.Command, []string, *knowledge.Catalog) (any, error)) *cobra.Command {
		c := a.leaf(use, short, minArgs, maxArgs, "none", func(c *cobra.Command, args []string) (any, error) {
			catalog, err := knowledge.Load()
			if err != nil {
				return nil, failed("load knowledge: %s", err)
			}
			return run(c, args, catalog)
		})
		c.Annotations["local"] = "true"
		g.AddCommand(c)
		return c
	}
	leaf("status", "查看来源提交、版本、条目数与分类", 0, 0, func(_ *cobra.Command, _ []string, catalog *knowledge.Catalog) (any, error) {
		return catalog.Info(), nil
	})
	list := leaf("list", "列出知识摘要，可按标签筛选", 0, 0, func(c *cobra.Command, _ []string, catalog *knowledge.Catalog) (any, error) {
		tag, _ := c.Flags().GetString("tag")
		return catalog.List(tag), nil
	})
	list.Flags().StringP("tag", "t", "", "分类标签，如 web、crypto、pwn")
	search := leaf("search [QUERY]", "按中英文关键词检索方法、产品与 CVE", 0, 1, func(c *cobra.Command, args []string, catalog *knowledge.Catalog) (any, error) {
		query, _ := c.Flags().GetString("query")
		if len(args) > 0 {
			if c.Flags().Changed("query") {
				return nil, invalid("QUERY conflicts with --query")
			}
			query = args[0]
		}
		if strings.TrimSpace(query) == "" {
			return nil, invalid("knowledge query must not be empty")
		}
		tag, _ := c.Flags().GetString("tag")
		limit, _ := c.Flags().GetInt("limit")
		if limit < 1 || limit > 50 {
			return nil, invalid("limit must be between 1 and 50")
		}
		return catalog.Search(query, tag, limit), nil
	})
	search.Flags().String("query", "", "关键词（也可用 QUERY 参数）")
	search.Flags().StringP("tag", "t", "", "限定分类标签")
	search.Flags().IntP("limit", "n", 5, "返回 1 到 50 条")
	leaf("show <ID>", "读取条目全文与命令示例", 1, 1, func(_ *cobra.Command, args []string, catalog *knowledge.Catalog) (any, error) {
		entry, err := catalog.Get(args[0])
		if err != nil {
			return nil, apiclient.Failure(4, err.Error())
		}
		return entry, nil
	})
	leaf("skills", "列出可用的 Dispatcher 与执行技能", 0, 0, func(_ *cobra.Command, _ []string, catalog *knowledge.Catalog) (any, error) {
		return catalog.Skills(), nil
	})
	leaf("skill <NAME>", "读取技能 SKILL.md 全文", 1, 1, func(_ *cobra.Command, args []string, catalog *knowledge.Catalog) (any, error) {
		skill, err := catalog.Skill(args[0])
		if err != nil {
			return nil, apiclient.Failure(4, err.Error())
		}
		return skill, nil
	})
	leaf("export <DIRECTORY>", "导出技能、参考资料与知识；保留不同内容的已有文件", 1, 1, func(_ *cobra.Command, args []string, catalog *knowledge.Catalog) (any, error) {
		root, err := filepath.Abs(args[0])
		if err != nil {
			return nil, invalid("knowledge directory: %s", err)
		}
		if err := catalog.Export(root); err != nil {
			return nil, failed("export knowledge: %s", err)
		}
		return map[string]any{"directory": root, "version": catalog.Info().Version, "entry_count": catalog.Info().EntryCount, "skill_count": catalog.Info().SkillCount}, nil
	})
	root.AddCommand(g)
}
