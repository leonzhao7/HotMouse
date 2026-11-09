package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
	"hotmouse/backend/internal/apiclient"
	"hotmouse/backend/internal/clientconfig"
)

var Version = "dev"
var Commit = "unknown"
var BuildTime = "unknown"

type app struct {
	ctx                                                                                         context.Context
	root                                                                                        *cobra.Command
	in                                                                                          io.Reader
	out, errOut                                                                                 io.Writer
	input                                                                                       *bufio.Reader
	store                                                                                       *clientconfig.Store
	profile                                                                                     string
	values, sources                                                                             map[string]string
	client                                                                                      *apiclient.Client
	configPath, profileFlag, server, projectFlag, tokenFile, timeout, output, selectPath, color string
	quiet, yes, nonInteractive, verbose, version                                                bool
	stdinUsed                                                                                   bool
}

func Run(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) int {
	a := &app{ctx: ctx, in: in, out: out, errOut: errOut, output: env("HOTMOUSE_OUTPUT", "table")}
	a.input = bufio.NewReader(in)
	root := a.command()
	root.SetArgs(args)
	root.SetIn(in)
	root.SetOut(out)
	root.SetErr(errOut)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	var failure *apiclient.Error
	if !errors.As(err, &failure) {
		code := 2
		if errors.Is(err, context.Canceled) {
			code = 130
		}
		if errors.Is(err, context.DeadlineExceeded) {
			code = 6
		}
		failure = apiclient.Failure(code, err.Error())
	}
	a.printError(failure)
	return failure.ExitCode
}
func env(name, fallback string) string {
	if v, ok := os.LookupEnv(name); ok {
		return v
	}
	return fallback
}
func invalid(format string, args ...any) error {
	return apiclient.Failure(2, fmt.Sprintf(format, args...))
}
func failed(format string, args ...any) error {
	return apiclient.Failure(1, fmt.Sprintf(format, args...))
}

func group(use, short string) *cobra.Command {
	return &cobra.Command{Use: use, Short: short, Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error { return c.Help() }}
}
func (a *app) command() *cobra.Command {
	root := group("hotmouse", "HotMouse 项目图、Agent 协议和本地 Worker")
	a.root = root
	root.SilenceErrors = true
	root.SilenceUsage = true
	f := root.PersistentFlags()
	f.StringVar(&a.configPath, "config", "", "客户端配置文件")
	f.StringVar(&a.profileFlag, "profile", "", "服务连接 Profile")
	f.StringVar(&a.server, "server", "", "服务基地址，默认 http://127.0.0.1:8080")
	f.StringVarP(&a.projectFlag, "project", "p", "", "项目 ID；不按标题猜测")
	f.StringVar(&a.tokenFile, "token-file", "", "内部令牌文件（- 表示 stdin）")
	f.StringVar(&a.timeout, "timeout", "", "单次 HTTP 超时，默认 30s")
	f.StringVarP(&a.output, "output", "o", a.output, "table、json、yaml；监看支持 ndjson")
	f.StringVar(&a.selectPath, "select", "", "提取对象中的标量，如 project.id")
	f.BoolVarP(&a.quiet, "quiet", "q", false, "抑制成功输出")
	f.BoolVarP(&a.yes, "yes", "y", false, "跳过删除或清理确认")
	f.BoolVar(&a.nonInteractive, "non-interactive", false, "禁止交互输入")
	f.StringVar(&a.color, "color", "", "auto、always、never")
	f.BoolVar(&a.verbose, "verbose", false, "将脱敏诊断写入 stderr")
	f.BoolVar(&a.version, "version", false, "显示版本")
	root.PersistentPreRunE = a.prepare
	root.RunE = func(c *cobra.Command, _ []string) error {
		if a.version {
			return a.versionResult(c)
		}
		return c.Help()
	}
	root.AddCommand(a.serveCommand(), a.versionCommand(), a.completionCommand(), a.configCommand(), a.authCommand())
	a.graphCommands(root)
	a.strategyCommands(root)
	a.knowledgeCommands(root)
	a.productCommands(root)
	a.platformCommands(root)
	a.competitionCommands(root)
	root.AddCommand(a.leaf("status", "查询服务、健康和 Worker 状态", 0, 0, "none", func(c *cobra.Command, _ []string) (any, error) {
		service, e := a.get(c, "/healthz")
		if e != nil {
			return nil, e
		}
		health, e := a.get(c, "/api/settings/health")
		if e != nil {
			return nil, e
		}
		worker, e := a.get(c, "/api/workers/observability")
		if e != nil {
			return nil, e
		}
		result := map[string]any{"service": service, "health": health, "worker": worker}
		if healthBad(health) {
			return result, failed("service health reports error")
		}
		return result, nil
	}))
	a.attachCompletion(root)
	return root
}

func (a *app) prepare(c *cobra.Command, _ []string) error {
	if c.HasSubCommands() && !a.version {
		return nil
	}
	if a.version && c != a.root {
		return invalid("--version is only valid on hotmouse")
	}
	if c.Name() == "serve" {
		allowed := map[string]bool{"verbose": true, "color": true, "help": true}
		var err error
		a.root.PersistentFlags().VisitAll(func(f *pflag.Flag) {
			if !f.Changed {
				return
			}
			if !allowed[f.Name] {
				err = invalid("--%s is not applicable to serve", f.Name)
			}
		})
		return err
	}
	local := c == a.root || c.Name() == "version" || c.Name() == "completion" || strings.HasPrefix(c.CommandPath(), "hotmouse config ") || c.Annotations["local"] == "true"
	if local {
		var wrong string
		a.root.PersistentFlags().VisitAll(func(f *pflag.Flag) {
			if !f.Changed {
				return
			}
			if oneOf(f.Name, "server", "timeout", "token-file") {
				allowed := c.CommandPath() == "hotmouse config profile add" && f.Name == "server"
				if c.CommandPath() == "hotmouse config show" {
					effective, _ := c.Flags().GetBool("effective")
					allowed = effective
				}
				if !allowed {
					wrong = f.Name
				}
			}
		})
		if wrong != "" {
			return invalid("--%s is not applicable to %s", wrong, c.CommandPath())
		}
	}
	if c.Name() == "completion" && (c.Flags().Changed("output") || a.selectPath != "" || a.quiet) {
		return invalid("completion outputs a raw shell script")
	}
	for _, name := range []string{"select", "profile", "token-file"} {
		if c.Flags().Changed(name) && c.Flags().Lookup(name).Value.String() == "" {
			return invalid("--%s must not be empty", name)
		}
	}
	if c.Annotations["local"] == "true" {
		return a.prepareKnowledge(c)
	}
	path := a.configPath
	if path == "" {
		path = os.Getenv("HOTMOUSE_CONFIG")
	}
	s, e := clientconfig.Load(path)
	if e != nil {
		return invalid("configuration: %s", e)
	}
	a.store = s
	name := a.profileFlag
	if name == "" {
		name = env("HOTMOUSE_PROFILE", s.Config.CurrentProfile)
	}
	p, ok := s.Config.Profiles[name]
	if !ok {
		return invalid("profile %q does not exist", name)
	}
	a.profile = name
	a.values = map[string]string{}
	a.sources = map[string]string{}
	for k, v := range clientconfig.Defaults {
		a.values[k] = v
		a.sources[k] = "default"
	}
	for k, v := range p {
		a.values[k] = v
		a.sources[k] = "profile"
	}
	for k, v := range map[string]string{"server": "HOTMOUSE_SERVER", "project": "HOTMOUSE_PROJECT", "creator": "HOTMOUSE_CREATOR", "worker": "HOTMOUSE_WORKER", "timeout": "HOTMOUSE_TIMEOUT", "output": "HOTMOUSE_OUTPUT"} {
		if val, ok := os.LookupEnv(v); ok {
			a.values[k] = val
			a.sources[k] = "environment"
		}
	}
	for k, v := range map[string]string{"server": a.server, "project": a.projectFlag, "token_file": a.tokenFile, "timeout": a.timeout, "output": a.output, "color": a.color} {
		flag := k
		if k == "token_file" {
			flag = "token-file"
		}
		if c.Flags().Changed(flag) {
			a.values[k] = v
			a.sources[k] = "flag"
		}
	}
	normalized, e := clientconfig.NormalizeServer(a.values["server"])
	if e != nil {
		return invalid("%s", e)
	}
	a.values["server"] = normalized
	original := p["server"]
	if original == "" {
		original = clientconfig.DefaultServer
	}
	original, _ = clientconfig.NormalizeServer(original)
	if original != normalized {
		if a.sources["project"] == "profile" {
			delete(a.values, "project")
			delete(a.sources, "project")
		}
		if a.sources["token_file"] == "profile" {
			delete(a.values, "token_file")
			delete(a.sources, "token_file")
		}
	}
	for _, k := range []string{"timeout", "color"} {
		if e = clientconfig.Validate(k, a.values[k]); e != nil {
			return invalid("%s", e)
		}
	}
	a.output = a.values["output"]
	a.color = a.values["color"]
	if a.output != "table" && a.output != "json" && a.output != "yaml" && a.output != "ndjson" {
		return invalid("output must be table, json, yaml or ndjson")
	}
	watch, _ := c.Flags().GetBool("watch")
	if a.output == "ndjson" && !watch {
		return invalid("ndjson requires --watch")
	}
	if a.selectPath != "" && (c.Flags().Changed("output") || a.quiet || watch) {
		return invalid("--select conflicts with --output, --quiet and --watch")
	}
	if a.quiet && watch {
		return invalid("--quiet conflicts with --watch")
	}
	scope := c.Annotations["scope"]
	if c.Flags().Changed("project") && scope == "none" {
		return invalid("--project is not applicable to %s", c.CommandPath())
	}
	if c.Flags().Changed("project") && strings.TrimSpace(a.projectFlag) == "" {
		return invalid("project must not be empty")
	}
	if c.Flags().Changed("yes") && c.Annotations["confirm"] != "true" {
		return invalid("--yes is not applicable to %s", c.CommandPath())
	}
	if c.Flags().Changed("interval") && !watch && c.Name() != "wait" {
		return invalid("--interval requires --watch")
	}
	if watch {
		if a.output != "table" && a.output != "ndjson" {
			return invalid("--watch requires table or ndjson output")
		}
		d, _ := c.Flags().GetDuration("interval")
		if d < time.Second {
			return invalid("interval must be at least 1s")
		}
	}
	stdinCount := 0
	usesToken := !local && (!strings.HasPrefix(c.CommandPath(), "hotmouse auth ") || c.Name() == "status")
	c.Flags().Visit(func(f *pflag.Flag) {
		if f.Name == "token-file" && !usesToken {
			return
		}
		if (f.Name == "file" || strings.HasSuffix(f.Name, "-file")) && f.Value.String() == "-" {
			stdinCount++
		}
		if strings.HasSuffix(f.Name, "-stdin") && f.Value.String() == "true" {
			stdinCount++
		}
	})
	if usesToken && a.values["token_file"] == "-" && !c.Flags().Changed("token-file") && strings.TrimSpace(os.Getenv("HOTMOUSE_INTERNAL_TOKEN")) == "" {
		stdinCount++
	}
	if stdinCount > 1 {
		return invalid("only one input may consume stdin")
	}
	if c.Flags().Changed("limit") {
		limit, _ := c.Flags().GetInt("limit")
		if limit < 1 {
			return invalid("limit must be positive")
		}
	}
	if since, _ := c.Flags().GetString("since"); since != "" {
		if _, e := time.Parse(time.RFC3339, since); e != nil {
			return invalid("since must be RFC3339")
		}
	}
	if c.Name() == "list" {
		resource := c.Parent().Name()
		if resource == "goal" {
			if status, _ := c.Flags().GetString("status"); status != "" && !oneOf(status, "active", "completed") {
				return invalid("invalid goal status")
			}
		}
		if resource == "finding" {
			if severity, _ := c.Flags().GetString("severity"); severity != "" && !oneOf(severity, "critical", "high", "medium", "low", "info") {
				return invalid("invalid finding severity")
			}
		}
	}
	return nil
}

func (a *app) leaf(use, short string, min, max int, scope string, run func(*cobra.Command, []string) (any, error)) *cobra.Command {
	c := &cobra.Command{Use: use, Short: short, Args: cobra.RangeArgs(min, max), Annotations: map[string]string{"scope": scope}}
	if max > 0 {
		c.Annotations["positional"] = "true"
	}
	c.RunE = func(c *cobra.Command, args []string) error {
		if watch, _ := c.Flags().GetBool("watch"); watch {
			return a.watch(c, args, run)
		}
		value, e := run(c, args)
		if value != nil || e == nil && c.Annotations["raw"] != "true" {
			if outErr := a.emit(value); outErr != nil {
				return outErr
			}
		}
		return e
	}
	return c
}
func (a *app) project(c *cobra.Command, args []string) (string, error) {
	p := a.values["project"]
	if len(args) > 0 {
		if c.Flags().Changed("project") && args[0] != a.projectFlag {
			return "", invalid("positional project conflicts with --project")
		}
		p = args[0]
	}
	if strings.TrimSpace(p) == "" {
		return "", invalid("a project is required; use --project or project use")
	}
	return p, nil
}
func (a *app) api(c *cobra.Command) (*apiclient.Client, error) {
	if a.client != nil {
		return a.client, nil
	}
	duration, _ := time.ParseDuration(a.values["timeout"])
	client := apiclient.New(a.values["server"], duration)
	cookie, e := a.store.Cookie(a.profile, a.values["server"])
	if e != nil {
		return nil, invalid("credentials: %s", e)
	}
	client.Cookie = cookie
	client.SaveCookie = func(cookie *http.Cookie) error { return a.store.SaveCookie(a.profile, a.values["server"], cookie) }
	useToken := !strings.HasPrefix(c.CommandPath(), "hotmouse auth ") || c.Name() == "status"
	if useToken {
		tokenPath := a.values["token_file"]
		environmentToken := strings.TrimSpace(os.Getenv("HOTMOUSE_INTERNAL_TOKEN"))
		if tokenPath != "" && (a.sources["token_file"] == "flag" || environmentToken == "") {
			if tokenPath != "-" && !filepath.IsAbs(tokenPath) && a.sources["token_file"] == "profile" {
				tokenPath = filepath.Join(filepath.Dir(a.store.Path), tokenPath)
			}
			b, e := a.readFile(tokenPath)
			if e != nil {
				return nil, e
			}
			client.Token = strings.TrimSpace(string(b))
			if client.Token == "" {
				return nil, invalid("token file is empty")
			}
		} else {
			client.Token = environmentToken
		}
	}
	if a.verbose {
		client.Log = a.errOut
	}
	a.client = client
	return client, nil
}
func (a *app) request(c *cobra.Command, method, path string, body any) (any, error) {
	client, e := a.api(c)
	if e != nil {
		return nil, e
	}
	return client.Do(c.Context(), method, path, body, strings.HasPrefix(c.CommandPath(), "hotmouse auth ") && c.Name() != "status")
}
func (a *app) get(c *cobra.Command, path string) (any, error) { return a.request(c, "GET", path, nil) }
func (a *app) confirm(c *cobra.Command, target string) error {
	if a.yes {
		return nil
	}
	if a.nonInteractive || !isTerminal(a.in) {
		return invalid("%s requires --yes in non-interactive mode", target)
	}
	line, e := a.prompt(c, "确认 "+target+"？[y/N]", false)
	if e != nil {
		return e
	}
	if strings.ToLower(strings.TrimSpace(line)) != "y" && strings.ToLower(strings.TrimSpace(line)) != "yes" {
		return invalid("operation cancelled")
	}
	return nil
}
func (a *app) readFile(path string) ([]byte, error) {
	if path == "-" {
		if a.stdinUsed {
			return nil, invalid("stdin has already been consumed")
		}
		a.stdinUsed = true
		type result struct {
			data []byte
			err  error
		}
		done := make(chan result, 1)
		go func() { b, e := io.ReadAll(a.input); done <- result{b, e} }()
		select {
		case <-a.ctx.Done():
			return nil, a.ctx.Err()
		case r := <-done:
			if r.err != nil {
				return nil, invalid("read stdin: %s", r.err)
			}
			return r.data, nil
		}
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, invalid("read file: %s", e)
	}
	return b, nil
}
func isTerminal(value any) bool {
	if f, ok := value.(*os.File); ok {
		return term.IsTerminal(int(f.Fd()))
	}
	return false
}
func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func array(v any) []any {
	s, _ := v.([]any)
	if s == nil {
		return []any{}
	}
	return s
}
func textValue(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
func healthBad(v any) bool { return object(object(v)["summary"])["status"] == "error" }
func findID(values []any, id string) (any, error) {
	for _, v := range values {
		if textValue(object(v)["id"]) == id {
			return v, nil
		}
	}
	return nil, apiclient.Failure(4, "resource not found: "+id)
}
func (a *app) printError(e *apiclient.Error) {
	payload := map[string]any{"error": e}
	if a.output == "json" || a.output == "ndjson" {
		_ = json.NewEncoder(a.errOut).Encode(payload)
	} else if a.output == "yaml" {
		b, _ := yaml.Marshal(payload)
		_, _ = a.errOut.Write(b)
	} else {
		fmt.Fprintf(a.errOut, "错误：%s\n", cleanText(e.Message))
	}
}
