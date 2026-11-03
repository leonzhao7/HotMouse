package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/kataras/iris/v12"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"hotmouse/backend/internal/apiclient"
	"hotmouse/backend/internal/core"
	"hotmouse/backend/internal/httpapi"
	"hotmouse/backend/internal/worker"
)

func (a *app) versionResult(_ *cobra.Command) error {
	return a.emit(map[string]any{"version": Version, "commit": Commit, "build_time": BuildTime, "go_version": runtime.Version()})
}
func (a *app) versionCommand() *cobra.Command {
	c := a.leaf("version", "显示本地版本", 0, 0, "none", func(_ *cobra.Command, _ []string) (any, error) {
		return map[string]any{"version": Version, "commit": Commit, "build_time": BuildTime, "go_version": runtime.Version()}, nil
	})
	return c
}
func (a *app) completionCommand() *cobra.Command {
	c := &cobra.Command{Use: "completion <bash|zsh|fish|powershell>", Short: "输出 shell 补全脚本", Args: cobra.ExactArgs(1), Annotations: map[string]string{"scope": "none"}}
	c.RunE = func(_ *cobra.Command, args []string) error {
		switch args[0] {
		case "bash":
			return a.root.GenBashCompletionV2(a.out, true)
		case "zsh":
			return a.root.GenZshCompletion(a.out)
		case "fish":
			return a.root.GenFishCompletion(a.out, true)
		case "powershell":
			return a.root.GenPowerShellCompletionWithDesc(a.out)
		default:
			return invalid("unsupported shell")
		}
	}
	return c
}
func (a *app) serveCommand() *cobra.Command {
	c := &cobra.Command{Use: "serve", Short: "前台运行 HTTP 服务及项目 Dispatcher 和 Agent Worker", Args: cobra.NoArgs}
	c.Flags().String("addr", env("HOTMOUSE_ADDR", "127.0.0.1:8080"), "监听地址")
	c.Flags().String("db", env("HOTMOUSE_DB", "hotmouse.db"), "SQLite 文件；同一文件只允许一个服务")
	c.Flags().Bool("autorun", os.Getenv("HOTMOUSE_AUTORUN") == "1", "启动项目 Dispatcher 和 Agent Worker 池")
	auth := "required"
	if os.Getenv("HOTMOUSE_AUTH_REQUIRED") == "0" {
		auth = "disabled"
	}
	c.Flags().String("auth", auth, "required 或 disabled")
	c.Flags().Duration("shutdown-timeout", 15*time.Second, "等待 HTTP 与 Worker 停止的最长时间")
	c.Example = "  hotmouse serve --db ./data/hotmouse.db --autorun"
	c.RunE = func(c *cobra.Command, _ []string) error {
		addr, _ := c.Flags().GetString("addr")
		path, _ := c.Flags().GetString("db")
		autorun, _ := c.Flags().GetBool("autorun")
		policy, _ := c.Flags().GetString("auth")
		shutdown, _ := c.Flags().GetDuration("shutdown-timeout")
		if !oneOf(policy, "required", "disabled") || shutdown <= 0 {
			return invalid("invalid auth policy or shutdown timeout")
		}
		if path == "" {
			return invalid("db must be a file path")
		}
		path, e := filepath.Abs(path)
		if e != nil {
			return invalid("db: %s", e)
		}
		if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
			return failed("create database directory: %s", e)
		}
		if real, e := filepath.EvalSymlinks(path); e == nil {
			path = real
		} else if dir, e := filepath.EvalSymlinks(filepath.Dir(path)); e == nil {
			path = filepath.Join(dir, filepath.Base(path))
		}
		lock := flock.New(path+".lock", flock.SetPermissions(0600))
		held, e := lock.TryLock()
		if e != nil {
			return failed("database lock: %s", e)
		}
		if !held {
			return apiclient.Failure(5, "another HotMouse service owns this database")
		}
		defer lock.Close()
		db, e := core.Open(path)
		if e != nil {
			return failed("open database: %s", e)
		}
		defer db.SQL.Close()
		previous, present := os.LookupEnv("HOTMOUSE_AUTH_REQUIRED")
		value := "1"
		if policy == "disabled" {
			value = "0"
		}
		if e = os.Setenv("HOTMOUSE_AUTH_REQUIRED", value); e != nil {
			return e
		}
		defer func() {
			if present {
				_ = os.Setenv("HOTMOUSE_AUTH_REQUIRED", previous)
			} else {
				_ = os.Unsetenv("HOTMOUSE_AUTH_REQUIRED")
			}
		}()
		serviceCtx, cancel := context.WithCancel(c.Context())
		defer cancel()
		api := iris.New()
		api.Logger().SetOutput(a.errOut)
		httpapi.Register(api, httpapi.New(db))
		if e = api.Build(); e != nil {
			return failed("build server: %s", e)
		}
		listener, e := net.Listen("tcp", addr)
		if e != nil {
			return failed("listen: %s", e)
		}
		server := &http.Server{Handler: api, BaseContext: func(net.Listener) context.Context { return serviceCtx }, ReadHeaderTimeout: 10 * time.Second}
		done := make(chan error, 1)
		go func() { done <- server.Serve(listener) }()
		var workerDone <-chan struct{}
		if autorun {
			runner := worker.New(db)
			runner.SetLogOutput(os.Stdout)
			workerDone = runner.Start(serviceCtx)
		}
		fmt.Fprintf(a.errOut, "HotMouse listening on %s (db=%s, autorun=%t)\n", listener.Addr(), path, autorun)
		var serveErr error
		select {
		case <-c.Context().Done():
		case serveErr = <-done:
		}
		cancel()
		shutdownCtx, finish := context.WithTimeout(context.Background(), shutdown)
		defer finish()
		if e = server.Shutdown(shutdownCtx); e != nil {
			_ = server.Close()
			return failed("server shutdown: %s", e)
		}
		if workerDone != nil {
			select {
			case <-workerDone:
			case <-shutdownCtx.Done():
				return failed("worker shutdown timed out")
			}
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return failed("server: %s", serveErr)
		}
		if c.Context().Err() != nil {
			return c.Context().Err()
		}
		return nil
	}
	return c
}

func (a *app) attachCompletion(root *cobra.Command) {
	for _, c := range root.Commands() {
		a.attachCompletion(c)
	}
	if root != a.root && !root.HasSubCommands() && root.Example == "" {
		root.Example = commandExample(root)
	}
	if root.Annotations["scope"] == "project" && root.Annotations["positional"] == "true" {
		root.ValidArgsFunction = func(c *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
			if len(args) > 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			if e := a.prepare(c, args); e != nil {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			resource := c.Parent().Name()
			var values []any
			if resource == "project" {
				v, e := a.get(c, "/projects")
				if e == nil {
					values = array(v)
				}
			} else {
				p, e := a.project(c, nil)
				if e == nil {
					v, e := a.get(c, apiclient.Path("projects", p))
					if e == nil {
						plural := resource + "s"
						if resource == "fact" {
							plural = "facts"
						}
						values = array(object(v)[plural])
					}
				}
			}
			result := []string{}
			for _, v := range values {
				m := object(v)
				id := textValue(m["id"])
				if strings.HasPrefix(id, prefix) {
					result = append(result, id+"\t"+cleanText(textValue(m["description"])))
				}
			}
			return result, cobra.ShellCompDirectiveNoFileComp
		}
	}
}

func commandExample(c *cobra.Command) string {
	example := "  " + c.CommandPath()
	resource := ""
	if c.Parent() != nil {
		resource = c.Parent().Name()
	}
	for _, arg := range strings.Fields(c.Use)[1:] {
		if strings.HasPrefix(arg, "[") {
			continue
		}
		value := "demo"
		switch {
		case strings.Contains(arg, "PROJECT"):
			value = "proj_001"
		case strings.Contains(arg, "GOAL"):
			value = "g001"
		case strings.Contains(arg, "STEP"):
			value = "s001"
		case arg == "<ID>":
			value = map[string]string{"goal": "g001", "step": "s001", "fact": "origin", "finding": "v001", "hint": "h001", "notification": "1"}[resource]
			if value == "" {
				value = "demo"
			}
		case strings.Contains(arg, "KEY"):
			value = "creator"
			if resource == "settings" {
				value = "step_timeout"
			}
			if resource == "config" && c.Parent().Parent() != nil && c.Parent().Parent().Name() == "worker" {
				value = "driver"
			}
		case strings.Contains(arg, "VALUE"):
			if resource == "settings" {
				value = "30"
			}
			if resource == "config" && c.Parent().Parent() != nil && c.Parent().Parent().Name() == "worker" {
				value = "mock"
			}
		case strings.Contains(arg, "bash|"):
			value = "bash"
		}
		example += " " + value
	}
	c.LocalNonPersistentFlags().VisitAll(func(f *pflag.Flag) {
		if len(f.Annotations["required-input"]) > 0 {
			value := "demo"
			switch f.Name {
			case "from":
				value = "origin"
			case "worker":
				value = "agent-a"
			case "creator":
				value = "cli"
			}
			example += " --" + f.Name + " " + value
		}
	})
	if c.Annotations["scope"] == "project" {
		example += " --project proj_001"
	}
	if c.Annotations["confirm"] == "true" {
		example += " --yes"
	}
	if c.Name() == "apply" {
		example += " --file config.yaml"
	}
	switch c.CommandPath() {
	case "hotmouse worker config env set":
		example += " --value demo"
	case "hotmouse goal update", "hotmouse step update":
		example += " --priority 10"
	case "hotmouse vulnerability review":
		example += " --status confirmed"
	case "hotmouse vulnerability batch-review":
		example += " vuln_ID --status confirmed"
	case "hotmouse vulnerability refresh":
		example += " --all"
	case "hotmouse notification read":
		example += " 1"
	}
	return example
}
