package cli

import (
	"net/url"

	"github.com/spf13/cobra"
	"hotmouse/backend/internal/apiclient"
)

func (a *app) platformCommands(root *cobra.Command) {
	platforms := group("platform", "独立 CTF 平台接入、题目下发与 flag 提交")
	platforms.AddCommand(a.leaf("list", "列出平台能力与服务端配置状态", 0, 0, "none", func(c *cobra.Command, _ []string) (any, error) {
		return a.get(c, "/api/platforms")
	}))
	list := a.leaf("challenges <PLATFORM>", "列出当前平台任务的题目与作答进度", 1, 1, "none", func(c *cobra.Command, args []string) (any, error) {
		return a.get(c, apiclient.Path("api", "platforms", args[0], "challenges"))
	})
	watchFlags(list)
	platforms.AddCommand(list)
	for _, action := range []string{"start", "hint", "close"} {
		short := map[string]string{"start": "下发题目并获取全部靶场地址", "hint": "显式获取题目提示（TSec 会扣分）", "close": "关闭题目容器并释放资源"}[action]
		command := a.leaf(action+" <PLATFORM> <CHALLENGE>", short, 2, 2, "none", func(c *cobra.Command, args []string) (any, error) {
			path := apiclient.Path("api", "platforms", args[0], action) + "?" + url.Values{"challenge_id": {args[1]}}.Encode()
			method := "POST"
			if action == "hint" {
				method = "GET"
			}
			return a.request(c, method, path, nil)
		})
		platforms.AddCommand(command)
	}
	importCommand := a.leaf("import <PLATFORM> <CHALLENGE>", "下发题目并创建解题项目（同一任务和题目复用项目）", 2, 2, "none", func(c *cobra.Command, args []string) (any, error) {
		body := map[string]any{"challenge_id": args[1]}
		if c.Flags().Changed("title") {
			value, _ := c.Flags().GetString("title")
			body["title"] = value
		}
		if c.Flags().Changed("max-workers") {
			value, _ := c.Flags().GetInt("max-workers")
			if value < 1 || value > 32 {
				return nil, invalid("max-workers must be 1..32")
			}
			body["max_workers"] = value
		}
		if c.Flags().Changed("budget-seconds") {
			value, _ := c.Flags().GetInt("budget-seconds")
			if value < 0 {
				return nil, invalid("budget-seconds must be nonnegative")
			}
			body["budget_seconds"] = value
		}
		value, err := a.request(c, "POST", apiclient.Path("api", "platforms", args[0], "import"), body)
		if err != nil {
			return nil, err
		}
		use, _ := c.Flags().GetBool("use")
		if use {
			if err := a.saveProject(textValue(object(object(value)["project"])["id"])); err != nil {
				return value, err
			}
		}
		return value, nil
	})
	importCommand.Flags().String("title", "", "项目标题，默认平台名称与题目 ID")
	importCommand.Flags().Int("max-workers", 1, "项目 Worker 并发上限，1..32")
	importCommand.Flags().Int("budget-seconds", 0, "项目预算秒数；省略使用系统默认，0 不限时")
	importCommand.Flags().Bool("use", false, "保存为当前 Profile 默认项目")
	platforms.AddCommand(importCommand)
	fields := []field{str("flag", true)}
	submit := a.leaf("submit <PLATFORM> <CHALLENGE>", "直接提交 flag；项目台账内的 flag 使用 flag submit", 2, 2, "none", func(c *cobra.Command, args []string) (any, error) {
		body, err := a.body(c, fields)
		if err != nil {
			return nil, err
		}
		body["challenge_id"] = args[1]
		return a.request(c, "POST", apiclient.Path("api", "platforms", args[0], "submit"), body)
	})
	a.bodyFlags(submit, fields)
	platforms.AddCommand(submit)
	root.AddCommand(platforms)
}
