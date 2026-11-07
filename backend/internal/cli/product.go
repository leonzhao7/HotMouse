package cli

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"hotmouse/backend/internal/apiclient"
)

func (a *app) productCommands(root *cobra.Command) {
	root.AddCommand(a.vulnerabilities(), a.templates(), a.workers(), a.settings(), a.audit(), a.notifications())
}
func (a *app) vulnerabilityList(c *cobra.Command) (any, error) {
	q := url.Values{}
	for flag, key := range map[string]string{"project": "project_id", "severity": "severity", "status": "status"} {
		if c.Flags().Changed(flag) {
			v, _ := c.Flags().GetString(flag)
			if v == "" {
				return nil, invalid("%s must not be empty", flag)
			}
			q.Set(key, v)
		}
	}
	if s := q.Get("severity"); s != "" && !oneOf(s, "critical", "high", "medium", "low") {
		return nil, invalid("invalid vulnerability severity")
	}
	if s := q.Get("status"); s != "" && !oneOf(s, "confirmed", "ignored") {
		return nil, invalid("invalid vulnerability status")
	}
	return a.get(c, "/api/vulnerabilities?"+q.Encode())
}
func (a *app) vulnerabilities() *cobra.Command {
	g := group("vulnerability", "查询合并漏洞并审核 confirmed/ignored 状态")
	c := a.leaf("list", "查询全局漏洞；项目过滤必须显式提供", 0, 0, "filter", func(c *cobra.Command, _ []string) (any, error) {
		v, e := a.vulnerabilityList(c)
		if e != nil {
			return nil, e
		}
		return filterList(c, v, nil, nil)
	})
	listFlags(c, "severity", "status")
	g.AddCommand(c)
	g.AddCommand(a.leaf("show <VULNERABILITY>", "按完整代表 ID 查询合并漏洞", 1, 1, "none", func(c *cobra.Command, args []string) (any, error) {
		v, e := a.get(c, "/api/vulnerabilities")
		if e != nil {
			return nil, e
		}
		return findID(array(v), args[0])
	}))
	summary := a.leaf("summary", "按严重性统计；显式过滤时在客户端统计合并列表", 0, 0, "filter", func(c *cobra.Command, _ []string) (any, error) {
		if !c.Flags().Changed("project") && !c.Flags().Changed("severity") && !c.Flags().Changed("status") {
			return a.get(c, "/api/vulnerabilities/summary")
		}
		v, e := a.vulnerabilityList(c)
		if e != nil {
			return nil, e
		}
		counts := map[string]any{"critical": 0, "high": 0, "medium": 0, "low": 0}
		for _, item := range array(v) {
			s := textValue(object(item)["severity"])
			if count, ok := counts[s].(int); ok {
				counts[s] = count + 1
			}
		}
		return counts, nil
	})
	summary.Flags().String("severity", "", "严重性过滤")
	summary.Flags().String("status", "", "审核状态过滤")
	g.AddCommand(summary)
	review := a.leaf("review <VULNERABILITY>", "审核合并组的所有底层漏洞记录", 1, 1, "none", func(c *cobra.Command, args []string) (any, error) {
		status, _ := c.Flags().GetString("status")
		if !oneOf(status, "confirmed", "ignored") {
			return nil, invalid("status must be confirmed or ignored")
		}
		return a.request(c, "PATCH", apiclient.Path("api", "vulnerabilities", args[0], "status"), map[string]any{"status": status})
	})
	review.Flags().String("status", "", "confirmed 或 ignored，必填")
	g.AddCommand(review)
	batch := a.leaf("batch-review [VULNERABILITY...]", "批量审核；missing_ids 非空时返回退出码 7", 0, math.MaxInt, "none", func(c *cobra.Command, args []string) (any, error) {
		var body map[string]any
		if c.Flags().Changed("file") {
			if len(args) > 0 || c.Flags().Changed("status") {
				return nil, invalid("file conflicts with IDs or status")
			}
			var e error
			body, e = a.inputObject(c)
			if e != nil {
				return nil, e
			}
		} else {
			status, _ := c.Flags().GetString("status")
			body = map[string]any{"ids": args, "status": status}
		}
		if e := validateObject(body, []field{list("ids", true), str("status", true)}); e != nil {
			return nil, e
		}
		if !oneOf(textValue(body["status"]), "confirmed", "ignored") {
			return nil, invalid("status must be confirmed or ignored")
		}
		ids, _ := stringsList(body["ids"])
		seen := map[string]bool{}
		unique := []string{}
		for _, id := range ids {
			if !seen[id] {
				unique = append(unique, id)
				seen[id] = true
			}
		}
		body["ids"] = unique
		v, e := a.request(c, "POST", "/api/vulnerabilities/batch/status", body)
		if e != nil {
			return nil, e
		}
		if len(array(object(v)["missing_ids"])) > 0 {
			return v, apiclient.Failure(7, "some vulnerability IDs were not found; successful IDs remain updated")
		}
		return v, nil
	})
	batch.Flags().String("status", "", "confirmed 或 ignored")
	batch.Flags().String("file", "", "包含 ids/status 的 JSON/YAML 对象")
	g.AddCommand(batch)
	refresh := a.leaf("refresh", "重建 Finding 漏洞投影；响应为全局统计", 0, 0, "filter", func(c *cobra.Command, _ []string) (any, error) {
		all, _ := c.Flags().GetBool("all")
		p := c.Flags().Changed("project")
		if all == p {
			return nil, invalid("provide either --project or --all")
		}
		path := "/api/vulnerabilities/refresh"
		if p {
			if a.projectFlag == "" {
				return nil, invalid("project must not be empty")
			}
			path = apiclient.Path("api", "projects", a.projectFlag, "vulnerabilities", "refresh")
		}
		return a.request(c, "POST", path, nil)
	})
	refresh.Flags().Bool("all", false, "明确刷新全部项目")
	g.AddCommand(refresh)
	return g
}
func (a *app) templates() *cobra.Command {
	g := group("template", "管理当前身份的项目模板；内置模板只读")
	listCmd := a.leaf("list", "列出可见模板", 0, 0, "none", func(c *cobra.Command, _ []string) (any, error) {
		builtin, _ := c.Flags().GetBool("builtin")
		custom, _ := c.Flags().GetBool("custom")
		if builtin && custom {
			return nil, invalid("builtin and custom conflict")
		}
		v, e := a.get(c, "/api/templates")
		if e != nil {
			return nil, e
		}
		out := []any{}
		for _, item := range array(v) {
			isBuiltin := object(item)["is_builtin"] == true
			if (!builtin || isBuiltin) && (!custom || !isBuiltin) {
				out = append(out, item)
			}
		}
		return out, nil
	})
	listCmd.Flags().Bool("builtin", false, "仅内置模板")
	listCmd.Flags().Bool("custom", false, "仅自定义模板")
	g.AddCommand(listCmd)
	g.AddCommand(a.leaf("show <TEMPLATE>", "查询模板正文", 1, 1, "none", func(c *cobra.Command, args []string) (any, error) {
		v, e := a.get(c, "/api/templates")
		if e != nil {
			return nil, e
		}
		return findID(array(v), args[0])
	}))
	schema := []field{str("title", true), str("origin", true), str("goal", true), {Name: "hints", Kind: "hints"}}
	create := a.leaf("create", "创建自定义模板；最多 50 个、10 条提示", 0, 0, "none", func(c *cobra.Command, _ []string) (any, error) {
		body, e := a.body(c, schema)
		if e != nil {
			return nil, e
		}
		creator, e := a.creator(c, "template")
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
		for _, k := range []string{"title", "origin", "goal"} {
			if len(textValue(body[k])) > 200 {
				return nil, invalid("template %s must be at most 200 bytes", k)
			}
		}
		if len(array(body["hints"])) > 10 {
			return nil, invalid("template accepts at most 10 hints")
		}
		return a.request(c, "POST", "/api/templates", body)
	})
	a.bodyFlags(create, schema)
	create.Flags().StringArray("hint", nil, "追加提示，可重复")
	create.Flags().String("creator", "", "提示创建者，默认 template")
	create.Flags().String("creator-file", "", "提示创建者的 UTF-8 文件")
	g.AddCommand(create)
	del := a.leaf("delete <TEMPLATE>", "删除自定义模板", 1, 1, "none", func(c *cobra.Command, args []string) (any, error) {
		if e := a.confirm(c, "删除模板 "+args[0]); e != nil {
			return nil, e
		}
		_, e := a.request(c, "DELETE", apiclient.Path("api", "templates", args[0]), nil)
		if e != nil {
			return nil, e
		}
		return map[string]any{"deleted": true, "id": args[0]}, nil
	})
	del.Annotations["confirm"] = "true"
	g.AddCommand(del)
	return g
}

var runtimeLimits = map[string][2]int{"decide_timeout_seconds": {1, 86400}, "execute_timeout_seconds": {1, 86400}, "conclude_timeout_seconds": {1, 3600}, "conclude_reserve_seconds": {0, 120}, "recovery_attempts": {1, 10}, "format_retries": {0, 1}}

func canonicalDriver(value string) string {
	switch value {
	case "shell":
		return "command"
	case "claudecode", "claude_code":
		return "claude"
	}
	return value
}
func workerPatchValid(patch map[string]any) error {
	if len(patch) == 0 {
		return invalid("provide at least one configuration field")
	}
	for k, v := range patch {
		switch k {
		case "name", "driver", "command", "workspace_root", "context_root", "completed_action":
			s, ok := v.(string)
			if !ok {
				return invalid("%s must be a string", k)
			}
			if k == "name" && strings.TrimSpace(s) == "" {
				return invalid("name must not be empty")
			}
			if k == "driver" {
				s = canonicalDriver(s)
				if !oneOf(s, "mock", "command", "pi", "codex", "claude") {
					return invalid("invalid driver")
				}
				patch[k] = s
			}
			if k == "completed_action" && !oneOf(s, "keep", "remove") {
				return invalid("completed_action must be keep or remove")
			}
		case "enabled":
			if _, ok := v.(bool); !ok {
				return invalid("enabled must be a boolean")
			}
		case "dispatcher":
			if v == nil {
				continue
			}
			config, ok := v.(map[string]any)
			if !ok {
				return invalid("dispatcher must be an object or null")
			}
			rest := map[string]any{}
			for field, value := range config {
				switch field {
				case "model":
					if _, ok := value.(string); !ok {
						return invalid("dispatcher model must be a string")
					}
				case "driver", "command", "environment", "environment_remove":
					rest[field] = value
				default:
					return invalid("unknown dispatcher configuration field %q", field)
				}
			}
			if len(rest) > 0 {
				if err := workerPatchValid(rest); err != nil {
					return err
				}
			}
			if driver, ok := rest["driver"]; ok {
				config["driver"] = driver
			}
		case "worker_count":
			count, ok := integer(v)
			if !ok || count < 1 || count > 32 {
				return invalid("worker_count must be between 1 and 32")
			}
		case "runtime":
			m, ok := v.(map[string]any)
			if !ok {
				return invalid("runtime must be an object")
			}
			for name, n := range m {
				limits, ok := runtimeLimits[name]
				if !ok {
					return invalid("unknown runtime field %s", name)
				}
				value, ok := integer(n)
				if !ok || value < limits[0] || value > limits[1] {
					return invalid("invalid runtime %s", name)
				}
			}
		case "environment":
			m, ok := v.(map[string]any)
			if !ok {
				return invalid("environment must be an object")
			}
			for name, v := range m {
				if !validEnvName(name) {
					return invalid("invalid environment variable name")
				}
				if _, ok := v.(string); !ok {
					return invalid("environment values must be strings")
				}
			}
		case "environment_remove":
			ss, ok := stringsList(v)
			if !ok || len(ss) == 0 {
				return invalid("environment_remove must be a non-empty string array")
			}
			for _, name := range ss {
				if !validEnvName(name) {
					return invalid("invalid environment variable name")
				}
				if _, ok := object(patch["environment"])[name]; ok {
					return invalid("environment key cannot be set and removed together")
				}
			}
		default:
			return invalid("unknown worker configuration field %q", k)
		}
	}
	return nil
}
func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
func secretName(name string) bool {
	name = strings.ToLower(name)
	return strings.Contains(name, "key") || strings.Contains(name, "token") || strings.Contains(name, "secret") || strings.Contains(name, "password")
}
func (a *app) updateWorker(c *cobra.Command, patch map[string]any) (any, error) {
	if e := workerPatchValid(patch); e != nil {
		return nil, e
	}
	v, e := a.get(c, "/api/workers/config")
	if e != nil {
		return nil, e
	}
	current := object(v)
	body := map[string]any{}
	for _, k := range []string{"name", "driver", "command", "enabled"} {
		body[k] = current[k]
	}
	for k, v := range patch {
		body[k] = v
	}
	return a.request(c, "PUT", "/api/workers/config", body)
}
func (a *app) workers() *cobra.Command {
	g := group("worker", "管理项目 Dispatcher 和 Agent Worker 池；历史只覆盖最近 20 条")
	status := a.leaf("status", "显示本地 Worker、执行和恢复状态", 0, 0, "none", func(c *cobra.Command, _ []string) (any, error) { return a.get(c, "/api/workers/observability") })
	watchFlags(status)
	g.AddCommand(status)
	history := a.leaf("history", "查询最近 20 条历史；筛选不会检索更早记录", 0, 0, "filter", func(c *cobra.Command, _ []string) (any, error) {
		limit, _ := c.Flags().GetInt("limit")
		if limit < 1 || limit > 20 {
			return nil, invalid("history limit must be 1–20")
		}
		task, _ := c.Flags().GetString("task-type")
		if task != "" && !oneOf(task, "decide", "execute") {
			return nil, invalid("task-type must be decide or execute")
		}
		path := "/api/workers/history"
		name, _ := c.Flags().GetString("name")
		if name != "" {
			path = apiclient.Path("api", "workers", name, "history")
		}
		v, e := a.get(c, path)
		if e != nil {
			return nil, e
		}
		return filterList(c, v, map[string]string{"project": "project_id", "task-type": "task_type", "status": "status"}, nil)
	})
	listFlags(history, "name", "task-type", "status")
	_ = history.Flags().Set("limit", "20")
	history.Flags().Lookup("limit").Changed = false
	history.Flags().Lookup("limit").DefValue = "20"
	watchFlags(history)
	g.AddCommand(history)
	g.AddCommand(a.leaf("test", "测试已配置命令/CLI；自定义命令会以 healthcheck 任务实际执行", 0, 0, "none", func(c *cobra.Command, _ []string) (any, error) {
		v, e := a.request(c, "POST", "/api/workers/config/test", nil)
		if e != nil {
			var failure *apiclient.Error
			if errors.As(e, &failure) && object(failure.Result)["ok"] == false {
				failure.ExitCode = 1
				failure.Code = "execution_failed"
				return failure.Result, failure
			}
			return nil, e
		}
		if object(v)["ok"] != true {
			return v, failed("worker test failed")
		}
		return v, nil
	}))
	for _, action := range []string{"enable", "disable"} {
		g.AddCommand(a.leaf(action, "修改 enabled；不会启动未启用的 autorun 循环", 0, 0, "none", func(c *cobra.Command, _ []string) (any, error) {
			return a.updateWorker(c, map[string]any{"enabled": c.Name() == "enable"})
		}))
	}
	config := group("config", "修改 Worker 配置；GET→合并→PUT 尚无并发版本保护")
	config.AddCommand(a.leaf("show", "显示脱敏 Worker 配置", 0, 0, "none", func(c *cobra.Command, _ []string) (any, error) { return a.get(c, "/api/workers/config") }))
	config.AddCommand(a.leaf("set <KEY> <VALUE>", "修改一个配置字段；runtime.KEY 接受整数", 2, 2, "none", func(c *cobra.Command, args []string) (any, error) {
		key, value := args[0], args[1]
		patch := map[string]any{}
		switch {
		case key == "dispatcher" && value == "null":
			patch[key] = nil
		case strings.HasPrefix(key, "dispatcher."):
			patch["dispatcher"] = map[string]any{strings.TrimPrefix(key, "dispatcher."): value}
		case key == "worker_count":
			count, err := strconv.Atoi(value)
			if err != nil {
				return nil, invalid("worker_count must be integer")
			}
			patch[key] = count
		case strings.HasPrefix(key, "runtime."):
			n, e := strconv.Atoi(value)
			if e != nil {
				return nil, invalid("runtime value must be integer")
			}
			patch["runtime"] = map[string]any{strings.TrimPrefix(key, "runtime."): n}
		case key == "enabled":
			b, e := strconv.ParseBool(value)
			if e != nil {
				return nil, invalid("enabled must be boolean")
			}
			patch[key] = b
		default:
			patch[key] = value
		}
		return a.updateWorker(c, patch)
	}))
	apply := a.leaf("apply", "从文件局部更新 Worker 配置并保留未提供字段", 0, 0, "none", func(c *cobra.Command, _ []string) (any, error) {
		patch, e := a.inputObject(c)
		if e != nil {
			return nil, e
		}
		return a.updateWorker(c, patch)
	})
	apply.Flags().String("file", "", "JSON/YAML 配置对象（- 表示 stdin）")
	config.AddCommand(apply)
	environment := group("env", "管理服务端 Worker 环境变量，秘密始终脱敏")
	environment.AddCommand(a.leaf("list", "显示脱敏 environment", 0, 0, "none", func(c *cobra.Command, _ []string) (any, error) {
		v, e := a.get(c, "/api/workers/config")
		if e != nil {
			return nil, e
		}
		return object(v)["environment"], nil
	}))
	set := a.leaf("set <KEY>", "设置环境变量；秘密只从 stdin/文件读取", 1, 1, "none", func(c *cobra.Command, args []string) (any, error) {
		key := args[0]
		if !validEnvName(key) {
			return nil, invalid("invalid environment variable name")
		}
		count := 0
		for _, f := range []string{"value", "value-stdin", "value-file"} {
			if c.Flags().Changed(f) {
				count++
			}
		}
		if count != 1 {
			return nil, invalid("provide exactly one of value, value-stdin or value-file")
		}
		var value string
		if c.Flags().Changed("value") {
			if secretName(key) {
				return nil, invalid("secret values require --value-stdin or --value-file")
			}
			value, _ = c.Flags().GetString("value")
		} else {
			path, _ := c.Flags().GetString("value-file")
			if c.Flags().Changed("value-stdin") {
				use, _ := c.Flags().GetBool("value-stdin")
				if !use {
					return nil, invalid("value-stdin must be true")
				}
				path = "-"
			}
			b, e := a.readFile(path)
			if e != nil {
				return nil, e
			}
			value = strings.TrimRight(string(b), "\r\n")
		}
		return a.updateWorker(c, map[string]any{"environment": map[string]any{key: value}})
	})
	set.Flags().String("value", "", "非秘密值")
	set.Flags().Bool("value-stdin", false, "从 stdin 读取值")
	set.Flags().String("value-file", "", "从文件读取值")
	environment.AddCommand(set)
	environment.AddCommand(a.leaf("unset <KEY>", "真正删除一个环境变量键", 1, 1, "none", func(c *cobra.Command, args []string) (any, error) {
		return a.updateWorker(c, map[string]any{"environment_remove": args})
	}))
	config.AddCommand(environment)
	g.AddCommand(config)
	return g
}

var settingsLimits = map[string][2]int{"default_project_budget_seconds": {0, math.MaxInt}, "step_timeout": {5, math.MaxInt}, "decide_timeout": {5, math.MaxInt}, "worker_unhealthy_retry_after_seconds": {1, 3600}, "worker_rejected_retry_after_seconds": {1, 3600}, "max_failed_login_attempts": {1, 50}, "rate_limit_window_minutes": {1, 1440}, "session_duration_hours": {1, 168}, "log_retention_days": {1, 3650}, "export_retention_days": {1, 3650}, "notification_retention_days": {1, 3650}, "project_idle_alert_hours": {1, 720}}

func (a *app) updateSettings(c *cobra.Command, patch map[string]any) (any, error) {
	if len(patch) == 0 {
		return nil, invalid("provide at least one settings field")
	}
	for key, v := range patch {
		limits, exists := settingsLimits[key]
		if !exists {
			return nil, invalid("unknown settings key")
		}
		n, ok := integer(v)
		if !ok || n < limits[0] || n > limits[1] {
			return nil, invalid("invalid settings %s", key)
		}
	}
	v, e := a.get(c, "/api/settings")
	if e != nil {
		return nil, e
	}
	body := object(v)
	for k, v := range patch {
		body[k] = v
	}
	return a.request(c, "PUT", "/api/settings", body)
}
func (a *app) settings() *cobra.Command {
	g := group("settings", "系统项目预算、租约、登录和保留策略；局部更新尚无并发版本保护")
	g.AddCommand(a.leaf("show", "显示完整 Settings", 0, 0, "none", func(c *cobra.Command, _ []string) (any, error) { return a.get(c, "/api/settings") }))
	g.AddCommand(a.leaf("set <KEY> <VALUE>", "修改一个整数设置；单位按键名，租约为秒", 2, 2, "none", func(c *cobra.Command, args []string) (any, error) {
		n, e := strconv.Atoi(args[1])
		if e != nil {
			return nil, invalid("settings value must be integer")
		}
		return a.updateSettings(c, map[string]any{args[0]: n})
	}))
	apply := a.leaf("apply", "从文件局部更新设置", 0, 0, "none", func(c *cobra.Command, _ []string) (any, error) {
		patch, e := a.inputObject(c)
		if e != nil {
			return nil, e
		}
		return a.updateSettings(c, patch)
	})
	apply.Flags().String("file", "", "Settings JSON/YAML 对象")
	g.AddCommand(apply)
	g.AddCommand(a.leaf("health", "查询健康对象；summary.status=error 时退出 1", 0, 0, "none", func(c *cobra.Command, _ []string) (any, error) {
		v, e := a.get(c, "/api/settings/health")
		if e != nil {
			return nil, e
		}
		if healthBad(v) {
			return v, failed("service health reports error")
		}
		return v, nil
	}))
	cleanup := a.leaf("cleanup", "按保留策略清理会话、旧审计、历史、登录尝试和旧已读通知", 0, 0, "none", func(c *cobra.Command, _ []string) (any, error) {
		if e := a.confirm(c, "按服务保留策略清理历史记录和过期会话"); e != nil {
			return nil, e
		}
		return a.request(c, "POST", "/api/settings/cleanup", nil)
	})
	cleanup.Annotations["confirm"] = "true"
	g.AddCommand(cleanup)
	return g
}
func (a *app) audit() *cobra.Command {
	g := group("audit", "查询审计窗口，最多最近 500 条")
	c := a.leaf("list", "查询审计；过滤只作用于服务返回窗口", 0, 0, "filter", func(c *cobra.Command, _ []string) (any, error) {
		limit, _ := c.Flags().GetInt("limit")
		if limit < 1 || limit > 500 {
			return nil, invalid("audit limit must be 1–500")
		}
		v, e := a.get(c, fmt.Sprintf("/api/audit?limit=%d", limit))
		if e != nil {
			return nil, e
		}
		if c.Flags().Changed("project") {
			p := a.projectFlag
			out := []any{}
			for _, v := range array(v) {
				m := object(v)
				matches := m["target_id"] == p
				for _, key := range []string{"target_id", "resource"} {
					path := textValue(m[key])
					segments := strings.Split(path, "/")
					for i, s := range segments {
						if s == p && i > 0 && segments[i-1] == "projects" {
							matches = true
						}
					}
				}
				if matches {
					out = append(out, v)
				}
			}
			v = out
		}
		return filterList(c, v, map[string]string{"actor": "actor", "action": "action"}, nil)
	})
	listFlags(c, "actor", "action", "since")
	_ = c.Flags().Set("limit", "100")
	c.Flags().Lookup("limit").Changed = false
	c.Flags().Lookup("limit").DefValue = "100"
	watchFlags(c)
	g.AddCommand(c)
	return g
}
func (a *app) notifications() *cobra.Command {
	g := group("notification", "管理全局通知窗口与未读状态")
	listCmd := a.leaf("list", "查询最近通知窗口，最多 200 条", 0, 0, "none", func(c *cobra.Command, _ []string) (any, error) {
		limit, _ := c.Flags().GetInt("limit")
		if limit < 1 || limit > 200 {
			return nil, invalid("notification limit must be 1–200")
		}
		v, e := a.get(c, fmt.Sprintf("/api/notifications?limit=%d", limit))
		if e != nil {
			return nil, e
		}
		unread, _ := c.Flags().GetBool("unread")
		if unread {
			out := []any{}
			for _, v := range array(v) {
				if object(v)["read"] != true {
					out = append(out, v)
				}
			}
			v = out
		}
		return filterList(c, v, map[string]string{"level": "level"}, nil)
	})
	listFlags(listCmd, "level")
	_ = listCmd.Flags().Set("limit", "50")
	listCmd.Flags().Lookup("limit").Changed = false
	listCmd.Flags().Lookup("limit").DefValue = "50"
	listCmd.Flags().Bool("unread", false, "仅未读通知")
	watchFlags(listCmd)
	g.AddCommand(listCmd)
	g.AddCommand(a.leaf("unread-count", "查询全部未读通知数", 0, 0, "none", func(c *cobra.Command, _ []string) (any, error) { return a.get(c, "/api/notifications/unread-count") }))
	read := a.leaf("read [ID...]", "标记指定通知已读；全部必须显式 --all", 0, math.MaxInt, "none", func(c *cobra.Command, args []string) (any, error) {
		all, _ := c.Flags().GetBool("all")
		if all && len(args) > 0 || !all && len(args) == 0 {
			return nil, invalid("provide notification IDs or --all")
		}
		ids := []int64{}
		for _, arg := range args {
			n, e := strconv.ParseInt(arg, 10, 64)
			if e != nil || n <= 0 {
				return nil, invalid("notification IDs must be positive integers")
			}
			ids = append(ids, n)
		}
		return a.request(c, "POST", "/api/notifications/read", map[string]any{"ids": ids})
	})
	read.Flags().Bool("all", false, "明确标记所有通知已读")
	g.AddCommand(read)
	clear := a.leaf("clear", "删除全部通知", 0, 0, "none", func(c *cobra.Command, _ []string) (any, error) {
		if e := a.confirm(c, "删除服务全部通知"); e != nil {
			return nil, e
		}
		return a.request(c, "DELETE", "/api/notifications", nil)
	})
	clear.Annotations["confirm"] = "true"
	g.AddCommand(clear)
	return g
}
