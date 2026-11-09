package cli

import (
	"github.com/spf13/cobra"
	"hotmouse/backend/internal/apiclient"
)

func (a *app) competitionCommands(root *cobra.Command) {
	commands := group("competition", "比赛总调度、题目队列和自动提交")
	fields := []field{str("rules", true), number("concurrency"), number("max_workers"), number("challenge_timeout_seconds")}
	start := a.leaf("start <PLATFORM>", "接收比赛规则并启动循环解题总调度", 1, 1, "none", func(c *cobra.Command, args []string) (any, error) {
		body, err := a.body(c, fields)
		if err != nil {
			return nil, err
		}
		return a.request(c, "POST", apiclient.Path("api", "platforms", args[0], "runs"), body)
	})
	a.bodyFlags(start, fields)
	start.Example = "  hotmouse competition start tsec --rules-file ./rules.md --concurrency 2 --challenge-timeout-seconds 1200"
	commands.AddCommand(start)
	list := a.leaf("list <PLATFORM>", "列出平台比赛运行", 1, 1, "none", func(c *cobra.Command, args []string) (any, error) {
		return a.get(c, apiclient.Path("api", "platforms", args[0], "runs"))
	})
	commands.AddCommand(list)
	status := a.leaf("status <PLATFORM> <RUN>", "查看总调度状态、队列和每题结果", 2, 2, "none", func(c *cobra.Command, args []string) (any, error) {
		return a.get(c, apiclient.Path("api", "platforms", args[0], "runs", args[1]))
	})
	watchFlags(status)
	commands.AddCommand(status)
	limit := a.leaf("concurrency <PLATFORM> <RUN>", "修改同时解答题目数；减少时等待已有题目退出", 2, 2, "none", func(c *cobra.Command, args []string) (any, error) {
		if !c.Flags().Changed("concurrency") {
			return nil, invalid("--concurrency is required")
		}
		value, _ := c.Flags().GetInt("concurrency")
		if value < 1 || value > 32 {
			return nil, invalid("concurrency must be 1..32")
		}
		return a.request(c, "PATCH", apiclient.Path("api", "platforms", args[0], "runs", args[1]), map[string]any{"concurrency": value})
	})
	limit.Flags().Int("concurrency", 1, "同时解答题目数；TSec 最大 3")
	commands.AddCommand(limit)
	commands.AddCommand(a.leaf("stop <PLATFORM> <RUN>", "停止总调度、取消解题并释放活跃容器", 2, 2, "none", func(c *cobra.Command, args []string) (any, error) {
		return a.request(c, "POST", apiclient.Path("api", "platforms", args[0], "runs", args[1], "stop"), nil)
	}))
	root.AddCommand(commands)
}
