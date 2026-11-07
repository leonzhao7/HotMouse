package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"hotmouse/backend/internal/apiclient"
	"hotmouse/backend/internal/clientconfig"
)

func (a *app) graphCommands(root *cobra.Command) {
	root.AddCommand(a.projectCommands())
	for _, resource := range []string{"goal", "step", "fact", "hint", "finding", "decide"} {
		g := group(resource, "管理项目内 "+resource)
		if resource == "step" {
			g.Aliases = []string{"intent"}
		}
		if resource == "decide" {
			g.Aliases = []string{"reason"}
		}
		if resource != "decide" {
			plural := resource + "s"
			listCmd := a.leaf("list", "查询项目内资源；详情查询会回收过期租约", 0, 0, "project", func(c *cobra.Command, _ []string) (any, error) {
				p, e := a.project(c, nil)
				if e != nil {
					return nil, e
				}
				v, e := a.get(c, apiclient.Path("projects", p))
				if e != nil {
					return nil, e
				}
				values := object(v)[plural]
				if resource == "finding" {
					values, e = a.get(c, apiclient.Path("projects", p, "findings"))
					if e != nil {
						return nil, e
					}
				}
				if resource == "step" {
					state, _ := c.Flags().GetString("state")
					if state != "" && !oneOf(state, "pending", "working", "concluded", "abandoned") {
						return nil, invalid("invalid step state")
					}
					if state != "" {
						filtered := []any{}
						for _, v := range array(values) {
							if stepState(object(v)) == state {
								filtered = append(filtered, v)
							}
						}
						values = filtered
					}
				}
				return filterList(c, values, map[string]string{"status": "status", "parent": "parent_goal_id", "goal": "goal_id", "worker": "worker", "severity": "severity", "kind": "kind", "fact": "fact_id"}, []string{"id", "title", "description", "content"})
			})
			switch resource {
			case "goal":
				listFlags(listCmd, "status", "parent")
			case "step":
				listFlags(listCmd, "state", "goal", "worker")
			case "finding":
				listFlags(listCmd, "severity", "kind", "fact")
			case "fact":
				listFlags(listCmd, "query")
			default:
				listFlags(listCmd)
			}
			g.AddCommand(listCmd)
			g.AddCommand(a.leaf("show <ID>", "查询单个项目内资源", 1, 1, "project", func(c *cobra.Command, args []string) (any, error) {
				p, e := a.project(c, nil)
				if e != nil {
					return nil, e
				}
				if resource == "fact" || resource == "step" || resource == "finding" {
					return a.get(c, apiclient.Path("projects", p, plural, args[0]))
				}
				v, e := a.get(c, apiclient.Path("projects", p))
				if e != nil {
					return nil, e
				}
				if resource == "finding" {
					v, e = a.get(c, apiclient.Path("projects", p, "findings"))
					if e != nil {
						return nil, e
					}
					return findID(array(v), args[0])
				}
				return findID(array(object(v)[plural]), args[0])
			}))
		}
		switch resource {
		case "goal":
			create := []field{str("description", true), alias("parent_goal_id", "parent"), number("priority"), str("creator", true)}
			g.AddCommand(a.graphWrite("create", "创建目标", resource, "POST", false, "", create, false))
			update := []field{str("description", false), number("priority")}
			g.AddCommand(a.graphWrite("update <GOAL>", "更新活动目标的描述或优先级", resource, "PATCH", true, "", update, false))
			g.AddCommand(a.graphWrite("complete <GOAL>", "引用事实完成目标；最后一个目标可能结束项目", resource, "POST", true, "complete", completionSchema(), false))
			g.AddCommand(a.graphWrite("delete <GOAL>", "删除未完成、无引用的子目标；顶层目标不可删除", resource, "DELETE", true, "", nil, true))
		case "step":
			create := []field{list("from", true), str("description", true), alias("goal_id", "goal"), number("priority"), str("creator", true), str("worker", false), alias("route_id", "route")}
			g.AddCommand(a.graphWrite("create", "声明一个有事实来源的步骤；仅显式 worker 创建即认领", resource, "POST", false, "", create, false))
			g.AddCommand(a.graphWrite("update <STEP>", "修改未认领开放步骤的目标或优先级", resource, "PATCH", true, "", []field{number("priority"), alias("goal_id", "goal")}, false))
			abandon := a.leaf("abandon <STEP>", "放弃未认领开放步骤；不能恢复", 1, 1, "project", func(c *cobra.Command, args []string) (any, error) {
				p, e := a.project(c, nil)
				if e != nil {
					return nil, e
				}
				return a.request(c, "PATCH", apiclient.Path("projects", p, "steps", args[0]), map[string]any{"abandoned": true})
			})
			g.AddCommand(abandon)
			for _, action := range []string{"claim", "heartbeat", "release"} {
				suffix := action
				if action == "claim" {
					suffix = "heartbeat"
				}
				g.AddCommand(a.graphWrite(action+" <STEP>", "执行租约操作；heartbeat 也可能原子认领", resource, "POST", true, suffix, []field{str("worker", true)}, false))
			}
			conclude := a.graphWrite("conclude <STEP>", "提交结论并生成 Fact，可同事务记录 Finding", resource, "POST", true, "conclude", []field{str("description", true), str("title", false), str("worker", true), {Name: "result", Kind: "result"}, {Name: "finding", Kind: "finding"}}, false)
			conclude.Flags().String("result-file", "", "结构化执行结果对象：type、detail、evidence")
			conclude.Flags().String("finding-file", "", "嵌套 Finding 对象，不指定 fact_id")
			g.AddCommand(conclude)
			g.AddCommand(a.leaf("execute <STEP>", "由服务内配置的 Worker 执行；长任务请设置 --timeout", 1, 1, "project", func(c *cobra.Command, args []string) (any, error) {
				p, e := a.project(c, nil)
				if e != nil {
					return nil, e
				}
				return a.request(c, "POST", apiclient.Path("api", "projects", p, "steps", args[0], "execute"), nil)
			}))
		case "hint":
			g.AddCommand(a.graphWrite("add", "追加调度提示", resource, "POST", false, "", []field{str("content", true), str("creator", true)}, false))
		case "finding":
			schema := findingSchema(true)
			for i := range schema {
				if schema[i].Name == "severity" {
					schema[i].Default = "info"
				}
				if schema[i].Name == "kind" {
					schema[i].Default = "finding"
				}
			}
			create := a.graphWrite("create", "记录发现；漏洞投影需要 kind、严重性及绑定事实", resource, "POST", false, "", schema, false)
			create.Flags().String("data-file", "", "Finding data 的 JSON/YAML 对象")
			create.Flags().StringArray("evidence", nil, "追加到 data.evidence 的文本，可重复")
			g.AddCommand(create)
		case "decide":
			g.AddCommand(a.graphWrite("request", "持久化排队一次中途重规划", "decide", "POST", false, "request", []field{str("reason", true)}, false))
			g.AddCommand(a.leaf("show", "显示当前决定租约", 0, 0, "project", func(c *cobra.Command, _ []string) (any, error) {
				p, e := a.project(c, nil)
				if e != nil {
					return nil, e
				}
				v, e := a.get(c, apiclient.Path("projects", p))
				if e != nil {
					return nil, e
				}
				return object(object(v)["project"])["decide"], nil
			}))
			for _, action := range []string{"claim", "heartbeat", "release"} {
				schema := []field{str("worker", true)}
				if action == "claim" {
					schema = append(schema, str("trigger", true))
				}
				g.AddCommand(a.graphWrite(action, "维护决定租约；claim 不会启动模型规划", resource, "POST", false, action, schema, false))
			}
		}
		root.AddCommand(g)
	}
}
func completionSchema() []field {
	return []field{list("from", true), str("description", true), str("worker", true)}
}
func stepState(m map[string]any) string {
	if m["to"] != nil || m["concluded_at"] != nil {
		return "concluded"
	}
	if m["abandoned"] == true {
		return "abandoned"
	}
	if textValue(m["worker"]) != "" {
		return "working"
	}
	return "pending"
}
func (a *app) graphWrite(use, short, resource, method string, id bool, suffix string, schema []field, confirm bool) *cobra.Command {
	min, max := 0, 0
	if id {
		min, max = 1, 1
	}
	c := a.leaf(use, short, min, max, "project", func(c *cobra.Command, args []string) (any, error) {
		p, e := a.project(c, nil)
		if e != nil {
			return nil, e
		}
		path := []string{"projects", p, resource + "s"}
		if resource == "decide" {
			path[2] = "decide"
		}
		if id {
			path = append(path, args[0])
		}
		if suffix != "" {
			path = append(path, suffix)
		}
		if confirm {
			if e = a.confirm(c, "删除 "+resource+" "+p+"/"+args[0]); e != nil {
				return nil, e
			}
		}
		var body any
		if schema != nil {
			b, e := a.body(c, schema)
			if e != nil {
				return nil, e
			}
			if len(b) == 0 {
				return nil, invalid("provide at least one field to update")
			}
			if c.Name() == "conclude" && c.Flags().Changed("finding-file") {
				file, _ := c.Flags().GetString("finding-file")
				data, e := a.readFile(file)
				if e != nil {
					return nil, e
				}
				finding, e := decodeObject(data)
				if e != nil {
					return nil, e
				}
				b["finding"] = finding
				if e = validateObject(b, schema); e != nil {
					return nil, e
				}
			}
			if c.Name() == "conclude" && c.Flags().Changed("result-file") {
				file, _ := c.Flags().GetString("result-file")
				data, e := a.readFile(file)
				if e != nil {
					return nil, e
				}
				result, e := decodeObject(data)
				if e != nil {
					return nil, e
				}
				b["result"] = result
				if e := validateObject(b, schema); e != nil {
					return nil, e
				}
			}
			if resource == "finding" {
				if e = a.findingExtra(c, b); e != nil {
					return nil, e
				}
			}
			body = b
		}
		v, e := a.request(c, method, apiclient.Path(path...), body)
		if e != nil {
			return nil, e
		}
		if method == "DELETE" {
			v = map[string]any{"deleted": true, "id": args[0]}
		}
		return v, nil
	})
	if schema != nil {
		a.bodyFlags(c, schema)
	}
	if confirm {
		c.Annotations["confirm"] = "true"
	}
	return c
}

func (a *app) projectCommands() *cobra.Command {
	g := group("project", "管理项目生命周期、图、Campaign 和导出")
	listCmd := a.leaf("list", "列出全局项目；查询会回收过期租约", 0, 0, "none", func(c *cobra.Command, _ []string) (any, error) {
		status, _ := c.Flags().GetString("status")
		if status != "" && !oneOf(status, "active", "stopped", "completed", "failed") {
			return nil, invalid("invalid project status")
		}
		v, e := a.get(c, "/projects")
		if e != nil {
			return nil, e
		}
		return filterList(c, v, map[string]string{"status": "status"}, []string{"id", "title"})
	})
	listFlags(listCmd, "status", "query")
	g.AddCommand(listCmd)
	show := a.leaf("show [PROJECT]", "查询完整项目图；查询会回收过期租约", 0, 1, "project", func(c *cobra.Command, args []string) (any, error) {
		p, e := a.project(c, args)
		if e != nil {
			return nil, e
		}
		return a.get(c, apiclient.Path("projects", p))
	})
	watchFlags(show)
	g.AddCommand(show)
	schema := []field{str("title", true), str("origin", true), str("goal", true), number("budget_seconds"), {Name: "hints", Kind: "hints"}}
	create := a.leaf("create", "创建项目、origin 和顶层目标；不隐式执行步骤", 0, 0, "none", func(c *cobra.Command, _ []string) (any, error) {
		var body map[string]any
		template, _ := c.Flags().GetString("template")
		if template != "" {
			if c.Flags().Changed("file") {
				return nil, invalid("--template conflicts with --file")
			}
			values, e := a.get(c, "/api/templates")
			if e != nil {
				return nil, e
			}
			v, e := findID(array(values), template)
			if e != nil {
				return nil, e
			}
			m := object(v)
			body = map[string]any{"title": m["title"], "origin": m["origin"], "goal": m["goal"], "hints": m["hints"]}
			optional := append([]field(nil), schema...)
			for i := range optional {
				optional[i].Required = false
			}
			patch, e := a.body(c, optional)
			if e != nil {
				return nil, e
			}
			for k, v := range patch {
				body[k] = v
			}
		} else {
			var e error
			body, e = a.body(c, schema)
			if e != nil {
				return nil, e
			}
		}
		creator, e := a.creator(c, "cli")
		if e != nil {
			return nil, e
		}
		hints, _ := c.Flags().GetStringArray("hint")
		for _, h := range hints {
			body["hints"] = append(array(body["hints"]), map[string]any{"content": h, "creator": creator})
		}
		if e = validateObject(body, schema); e != nil {
			return nil, e
		}
		v, e := a.request(c, "POST", "/projects", body)
		if e != nil {
			return nil, e
		}
		use, _ := c.Flags().GetBool("use")
		if use {
			if e = a.saveProject(textValue(object(object(v)["project"])["id"])); e != nil {
				return v, failed("project created, but saving the default project failed: %s", e)
			}
		}
		return v, nil
	})
	a.bodyFlags(create, schema)
	create.Flags().Lookup("budget-seconds").Usage = "项目预算（秒，0 为不限）；省略时自动读取系统配置"
	create.Flags().StringArray("hint", nil, "追加提示，可重复")
	create.Flags().String("creator", "", "显式提示的创建者，默认 cli")
	create.Flags().String("creator-file", "", "显式提示创建者的 UTF-8 文件")
	create.Flags().String("template", "", "从模板展开创建请求，显式正文覆盖模板")
	create.Flags().Bool("use", false, "创建成功后保存默认项目")
	g.AddCommand(create)
	g.AddCommand(a.leaf("use <PROJECT>", "验证并保存当前 Profile 的默认项目", 1, 1, "project", func(c *cobra.Command, args []string) (any, error) {
		p, e := a.project(c, args)
		if e != nil {
			return nil, e
		}
		if _, e = a.get(c, apiclient.Path("projects", p)); e != nil {
			return nil, e
		}
		if e = a.saveProject(p); e != nil {
			return nil, failed("save default project: %s", e)
		}
		return map[string]any{"project": p, "server": a.values["server"]}, nil
	}))
	for _, action := range []string{"rename", "stop", "resume", "complete", "reopen", "delete"} {
		var fs []field
		switch action {
		case "rename":
			fs = []field{str("title", true)}
		case "complete":
			fs = completionSchema()
		case "reopen":
			fs = []field{str("description", true), str("creator", true)}
		}
		c := a.leaf(action+" [PROJECT]", "执行项目 "+action+"；complete 只完成一个顶层目标", 0, 1, "project", func(c *cobra.Command, args []string) (any, error) {
			p, e := a.project(c, args)
			if e != nil {
				return nil, e
			}
			path := apiclient.Path("projects", p)
			method := "POST"
			var body any
			switch c.Name() {
			case "delete":
				method = "DELETE"
				if e = a.confirm(c, "删除项目 "+p+" 和数据库关联记录"); e != nil {
					return nil, e
				}
			case "stop", "resume":
				method = "PUT"
				path += "/status"
				status := "stopped"
				if c.Name() == "resume" {
					status = "active"
				}
				body = map[string]any{"status": status}
			default:
				path += "/" + c.Name()
				if c.Name() == "rename" {
					method = "PUT"
					path = apiclient.Path("projects", p, "title")
				}
				b, e := a.body(c, fs)
				if e != nil {
					return nil, e
				}
				body = b
			}
			v, e := a.request(c, method, path, body)
			if e != nil {
				return nil, e
			}
			if c.Name() == "delete" {
				v = map[string]any{"deleted": true, "id": p}
				profile := a.store.Config.Profiles[a.profile]
				server := profile["server"]
				if server == "" {
					server = clientconfig.DefaultServer
				}
				server, _ = clientconfig.NormalizeServer(server)
				if server == a.values["server"] && profile["project"] == p {
					delete(profile, "project")
					if e = a.store.Save(); e != nil {
						return v, failed("project deleted, but clearing the default failed: %s", e)
					}
				}
			}
			return v, nil
		})
		if fs != nil {
			a.bodyFlags(c, fs)
		}
		if action == "delete" {
			c.Annotations["confirm"] = "true"
		}
		g.AddCommand(c)
	}
	for _, action := range []string{"campaign", "timeline"} {
		c := a.leaf(action+" [PROJECT]", "查询项目 "+action, 0, 1, "project", func(c *cobra.Command, args []string) (any, error) {
			p, e := a.project(c, args)
			if e != nil {
				return nil, e
			}
			v, e := a.get(c, apiclient.Path("api", "projects", p, c.Name()))
			if e != nil {
				return nil, e
			}
			if c.Name() == "timeline" {
				return filterList(c, v, nil, nil)
			}
			return v, nil
		})
		watchFlags(c)
		if action == "timeline" {
			listFlags(c, "since")
		}
		g.AddCommand(c)
	}
	export := a.leaf("export [PROJECT]", "原生 YAML/timeline 导出；已有文件需 --overwrite", 0, 1, "project", func(c *cobra.Command, args []string) (any, error) {
		if c.Flags().Changed("output") || a.selectPath != "" || a.quiet {
			return nil, invalid("export uses raw text and conflicts with output, select and quiet")
		}
		format, _ := c.Flags().GetString("format")
		if !oneOf(format, "yaml", "timeline") {
			return nil, invalid("format must be yaml or timeline")
		}
		p, e := a.project(c, args)
		if e != nil {
			return nil, e
		}
		client, e := a.api(c)
		if e != nil {
			return nil, e
		}
		b, e := client.Request(c.Context(), "GET", apiclient.Path("projects", p, "export")+"?format="+format, nil, false)
		if e != nil {
			return nil, e
		}
		out, _ := c.Flags().GetString("out")
		if out == "" || out == "-" {
			if _, e = a.out.Write(b); e != nil {
				return nil, failed("write export: %s", e)
			}
		} else {
			overwrite, _ := c.Flags().GetBool("overwrite")
			if e = clientconfig.WritePrivate(out, b, overwrite); e != nil {
				if errors.Is(e, os.ErrExist) {
					return nil, invalid("file exists; use --overwrite")
				}
				return nil, failed("write export: %s", e)
			}
		}
		return nil, nil
	})
	export.Annotations["raw"] = "true"
	export.Flags().String("format", "yaml", "yaml 或 timeline；不是通用输出格式")
	export.Flags().String("out", "", "目标文件，省略或 - 为 stdout")
	export.Flags().Bool("overwrite", false, "允许替换已有导出文件")
	g.AddCommand(export)
	wait := a.leaf("wait [PROJECT]", "观察项目直到指定终态；不会启动 Worker", 0, 1, "project", func(c *cobra.Command, args []string) (any, error) {
		p, e := a.project(c, args)
		if e != nil {
			return nil, e
		}
		until, _ := c.Flags().GetString("until")
		maxWait, _ := c.Flags().GetDuration("max-wait")
		interval, _ := c.Flags().GetDuration("interval")
		if !oneOf(until, "completed", "failed", "stopped") || maxWait <= 0 || interval < time.Second {
			return nil, invalid("invalid wait condition, max-wait or interval")
		}
		ctx, cancel := context.WithTimeout(c.Context(), maxWait)
		defer cancel()
		client, e := a.api(c)
		if e != nil {
			return nil, e
		}
		for {
			v, e := client.Do(ctx, "GET", apiclient.Path("projects", p), nil, false)
			if e != nil {
				if ctx.Err() == context.DeadlineExceeded {
					return nil, apiclient.Failure(6, "project wait timed out")
				}
				return nil, e
			}
			status := textValue(object(object(v)["project"])["status"])
			if status == until {
				return v, nil
			}
			if status == "completed" || status == "stopped" || status == "failed" {
				return nil, apiclient.Failure(5, "project entered unexpected terminal state: "+status)
			}
			fmt.Fprintf(a.errOut, "%s: %s (waiting for %s)\n", p, status, until)
			timer := time.NewTimer(interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				if c.Context().Err() != nil {
					return nil, c.Context().Err()
				}
				return nil, apiclient.Failure(6, "project wait timed out")
			case <-timer.C:
			}
		}
	})
	wait.Flags().String("until", "completed", "completed、failed 或 stopped")
	wait.Flags().Duration("max-wait", 30*time.Minute, "等待总时限，与 HTTP timeout 不同")
	wait.Flags().Duration("interval", 2*time.Second, "轮询间隔，最小 1s")
	g.AddCommand(wait)
	a.projectStrategyCommands(g)
	return g
}
func (a *app) saveProject(p string) error {
	profile := a.store.Config.Profiles[a.profile]
	old := profile["server"]
	if old == "" {
		old = clientconfig.DefaultServer
	}
	old, _ = clientconfig.NormalizeServer(old)
	if old != a.values["server"] {
		delete(profile, "token_file")
	}
	profile["server"] = a.values["server"]
	profile["project"] = p
	return a.store.Save()
}
func (a *app) creator(c *cobra.Command, fallback string) (string, error) {
	v := a.values["creator"]
	if a.sources["creator"] == "default" {
		v = fallback
	}
	if c.Flags().Changed("creator") {
		v, _ = c.Flags().GetString("creator")
	}
	if c.Flags().Changed("creator-file") {
		if c.Flags().Changed("creator") {
			return "", invalid("creator conflicts with creator-file")
		}
		path, _ := c.Flags().GetString("creator-file")
		b, e := a.readFile(path)
		if e != nil {
			return "", e
		}
		v = string(b)
		if !utf8.Valid(b) {
			return "", invalid("creator file must be UTF-8")
		}
	}
	if strings.TrimSpace(v) == "" {
		return "", invalid("creator must not be empty")
	}
	return v, nil
}
