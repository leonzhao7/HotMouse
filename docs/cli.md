# HotMouse 命令行接口设计

状态：已实现。`backend/cmd/hotmouse` 提供下述命令树；`serve` 启动服务，其他业务命令调用 HTTP API。

本设计以现有 Go、Iris、SQLite 后端为基础，覆盖项目图协议、认证、本地 Worker、漏洞审核、模板、系统设置、审计和通知。接口范围依据 [routes.md](routes.md)；实现入口见 [handler.go](../backend/internal/httpapi/handler.go)。

## 1. 基本结构

统一提供一个二进制 `hotmouse`：

```text
hotmouse [全局参数] <资源或命令> <动作> [位置参数] [命令参数]
```

`hotmouse serve` 在前台运行服务，其他业务命令通过 HTTP 调用服务。客户端不直接打开 SQLite，Worker 随服务进程运行，继续使用现有的单进程串行调度。

资源名使用单数，动作固定为 `list`、`show`、`create`、`update`、`delete`，生命周期动作单独命名。`--help` 显示参数、必填条件、示例和可能产生的副作用；直接运行 `hotmouse` 或资源名只显示帮助，退出码为 0。

### 完整命令树

```text
hotmouse
├── serve
├── status
├── version
├── completion <bash|zsh|fish|powershell>
├── config
│   ├── path
│   ├── show
│   ├── get <KEY>
│   ├── set <KEY> <VALUE>
│   ├── unset <KEY>
│   └── profile
│       ├── list
│       ├── add <NAME>
│       ├── use <NAME>
│       └── remove <NAME>
├── auth
│   ├── captcha
│   ├── register
│   ├── login
│   ├── logout
│   ├── status
│   ├── whoami
│   └── password
├── project
│   ├── list
│   ├── show [PROJECT]
│   ├── create
│   ├── use <PROJECT>
│   ├── rename [PROJECT]
│   ├── stop [PROJECT]
│   ├── resume [PROJECT]
│   ├── complete [PROJECT]
│   ├── reopen [PROJECT]
│   ├── delete [PROJECT]
│   ├── campaign [PROJECT]
│   ├── timeline [PROJECT]
│   ├── context [PROJECT]
│   ├── recall [PROJECT]
│   ├── export [PROJECT]
│   └── wait [PROJECT]
├── goal
│   ├── list
│   ├── show <GOAL>
│   ├── create
│   ├── update <GOAL>
│   ├── complete <GOAL>
│   └── delete <GOAL>
├── step
│   ├── list
│   ├── show <STEP>
│   ├── create
│   ├── update <STEP>
│   ├── abandon <STEP>
│   ├── claim <STEP>
│   ├── heartbeat <STEP>
│   ├── release <STEP>
│   ├── conclude <STEP>
│   └── execute <STEP>
├── route
│   ├── list
│   ├── create
│   ├── update <ROUTE>
│   └── records
├── experience
│   ├── search
│   ├── show <ID>
│   └── create
├── flag
│   ├── list
│   ├── capture
│   ├── begin <FLAG>
│   └── resolve <FLAG> <ATTEMPT>
├── decide
│   ├── request
│   ├── show
│   ├── claim
│   ├── heartbeat
│   └── release
├── fact
│   ├── list
│   └── show <FACT>
├── hint
│   ├── list
│   ├── show <HINT>
│   └── add
├── finding
│   ├── list
│   ├── show <FINDING>
│   └── create
├── vulnerability
│   ├── list
│   ├── show <VULNERABILITY>
│   ├── summary
│   ├── review <VULNERABILITY>
│   ├── batch-review <VULNERABILITY>...
│   └── refresh
├── template
│   ├── list
│   ├── show <TEMPLATE>
│   ├── create
│   └── delete <TEMPLATE>
├── worker
│   ├── status
│   ├── history
│   ├── test
│   ├── enable
│   ├── disable
│   └── config
│       ├── show
│       ├── set <KEY> <VALUE>
│       ├── apply
│       └── env
│           ├── list
│           ├── set <KEY>
│           └── unset <KEY>
├── settings
│   ├── show
│   ├── set <KEY> <VALUE>
│   ├── apply
│   ├── health
│   └── cleanup
├── audit
│   └── list
└── notification
    ├── list
    ├── unread-count
    ├── read <ID>...
    └── clear
```

兼容名称：`intent` 是 `step` 的别名，`reason` 是 `decide` 的别名，均调用规范路由。服务端已有的其他历史 URL 不形成新的 CLI 命令。

## 2. 全局约定

### 参数

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `--help, -h` | — | 显示当前命令帮助 |
| `--version` | — | 等价于 `version` |
| `--config PATH` | 系统配置目录下的 `hotmouse/config.yaml` | 客户端配置文件 |
| `--profile NAME` | 配置的当前 profile，初始为 `local` | 选择服务连接配置 |
| `--server URL` | `http://127.0.0.1:8080` | 服务基地址；核心接口不统一添加 `/api` |
| `--project, -p ID` | 环境变量或 profile 的默认项目 | 选择项目，适用范围见下文 |
| `--token-file PATH` | 未设置 | 从文件读取内部令牌，发送 `X-HotMouse-Internal-Token` |
| `--timeout DURATION` | `30s` | 单次 HTTP 请求超时，必须大于 0 |
| `--output, -o FORMAT` | `table` | `table`、`json`、`yaml`；监看模式另支持 `ndjson` |
| `--select PATH` | 未设置 | 提取响应对象的单个标量，如 `project.id`、`fact.id` |
| `--quiet, -q` | `false` | 抑制成功输出，保留错误和退出码 |
| `--yes, -y` | `false` | 跳过已定义的删除、清理确认 |
| `--non-interactive` | `false` | 禁止读取交互输入 |
| `--color MODE` | `auto` | `auto`、`always`、`never`；尊重 `NO_COLOR` |
| `--verbose` | `false` | 将诊断日志写入 stderr，并隐藏认证信息 |

全局参数可以放在子命令前后。`serve` 只接受对服务有意义的参数和帮助、日志参数；对其他不适用参数返回参数错误。各客户端命令也拒绝没有定义用途的参数，例如 `auth login --project ...`。

`--select` 使用不带前导点的对象路径，结果只能是字符串、数字或布尔值，按纯文本输出一行；路径不存在、值为 null 或集合时返回参数错误。它与显式 `--output`、`--quiet`、`--watch` 互斥。`--quiet` 与 `--watch`、导出文件操作互斥。

### 配置和项目选择

普通配置值优先级：显式命令参数 > 环境变量 > 选中 profile > 内置默认值。`--config` / `HOTMOUSE_CONFIG` 先确定配置文件，再由 `--profile` / `HOTMOUSE_PROFILE` / `current_profile` 选择 profile。

项目级命令必须解析到一个项目：位置参数 `PROJECT` > `--project` > `HOTMOUSE_PROJECT` > profile 的 `project`。同时提供不同的位置项目和 `--project` 时直接报错，避免操作错项目。`g001`、`s001`、`f001`、`v001`、`h001` 均为项目内 ID，必须与项目一起使用；项目、模板、漏洞 ID 按服务返回值原样传递，不自动补前缀或按标题猜测。

`project list`、`project create` 不接受默认项目过滤。`vulnerability list/summary`、`worker history`、`audit list` 默认查看全局结果，只在显式提供 `--project` 时过滤；它们不读取默认项目。`vulnerability refresh` 必须显式提供 `--project` 或 `--all`。其余项目图命令使用上述默认项目解析规则。

`project use` 验证项目后，只修改当前 profile 的默认项目，不修改服务数据。默认项目绑定到规范化的服务 URL；使用 `--server` 临时切换到另一个服务时，不继承原服务的默认项目或凭据。

### 数据输入

- 文本参数提供对应的 `--NAME-file PATH`，例如 `--origin-file`、`--description-file`、`--content-file`。同一字段的文本值和文件值互斥；文件按 UTF-8 读取。
- 支持结构化请求的写命令提供 `--file PATH`，读取单个 JSON/YAML 对象；`--file -` 从 stdin 读取，通过内容判断格式。文件对象使用 API 字段名，拒绝未知字段和多文档输入。
- `--file` 与构造请求体的业务参数互斥。项目选择、输出参数仍可使用。请求文件缺少 creator/worker 时，按命令规定的默认规则补齐。
- `--from FACT`、`--hint TEXT`、`--evidence TEXT` 可以重复；`--from` 同时支持逗号分隔，去重并保留顺序。其他文本中的逗号不做拆分。
- 每次调用最多有一个 stdin 消费者；`--file -`、文本文件 `-`、密码输入和令牌文件 `-` 相互冲突时直接报错。
- 更新操作只修改明确提供的字段。未提供、空字符串、0、false 和 null 必须区别处理；不支持的清空操作直接拒绝。

### 输出和脚本行为

stdout 只输出结果；提示、诊断、错误和进度写入 stderr。`table` 列表输出表格，详情输出字段和内容块，修改操作返回修改后的资源摘要。非 TTY 不使用颜色、光标控制或分页器。

`json` / `yaml` 默认返回接口原始对象或数组，不增加统一成功包装；客户端组合命令的结果形状在对应章节固定。空列表为 `[]`，属于成功。无响应体的删除成功返回 `{ "deleted": true, "id": "..." }`；无响应体的其他动作返回 `{ "ok": true }`。JSON/YAML 模式不截断文本，不改变 API 字段名和时间值。时间格式使用 RFC 3339；表格可显示当地时间。

错误在 JSON/NDJSON 模式下向 stderr 输出一条 JSON，在 YAML 模式下输出 YAML，其他模式输出简短文字。错误结构固定为：

```json
{
  "error": {
    "code": "conflict",
    "message": "step is currently claimed by worker-a",
    "http_status": 409,
    "retryable": false
  }
}
```

本地错误的 `http_status` 为 null。服务端 `detail` 原样保留为 message；HTTP 状态到 CLI 错误码的映射固定，后续新增服务端结构化 code 时再细分。

退出码 1–7 的 code 分别固定为 `execution_failed`、`invalid_argument`、`unauthorized`、`not_found`、`conflict`、`unavailable`、`partial_failure`，中断为 `interrupted`。retryable 只在可安全重试的读请求连接/超时/限流错误时为 true；写请求结果不明时为 false。

| 退出码 | 含义 |
| --- | --- |
| `0` | 操作成功或条件满足 |
| `1` | 服务 5xx、Worker 测试失败、健康检查失败或其他执行错误 |
| `2` | 命令/输入/配置错误，或 HTTP 400/422 校验错误 |
| `3` | 认证或权限错误，HTTP 401/403 |
| `4` | 资源不存在，HTTP 404 |
| `5` | 状态冲突，HTTP 409；或等待期间项目进入非预期终态 |
| `6` | 连接失败、请求/等待超时或 HTTP 429 |
| `7` | 批量操作部分成功 |
| `130` / `143` | 被 SIGINT / SIGTERM 中断 |

端点的语义优先于通用 HTTP 映射：`worker test` 返回 `ok=false` 时退出 1；批量审核的 `missing_ids` 非空时退出 7。当前服务会把部分状态错误返回为 400/403，CLI 按实际 HTTP 状态映射并保留 detail，不通过猜测英文错误文本改变退出码。

不会自动重试写请求。请求超时可能发生在服务已经提交之后；应先查询结果再决定是否重试。批量审核即使退出 7，stdout 仍保留服务返回的 `ids`、`missing_ids`、`count` 和 `status`。

### 确认和监看

仅 `project delete`、`goal delete`、`template delete`、`notification clear`、`settings cleanup`、`config profile remove` 要求确认；TTY 下显示具体目标和作用范围，默认回答为否。非交互环境必须提供 `--yes`，否则在发送写请求前退出 2。停止、恢复、认领、释放、完成、审核和配置更新直接执行。

支持监看的命令：`project show/campaign/timeline`、`worker status/history`、`audit list`、`notification list`。使用 `--watch --interval 2s`，interval 最小为 `1s`。监看立即查询一次，此后串行轮询；不补发积压请求。

监看模式只允许 `table` 或 `ndjson`：TTY 表格原地刷新，非 TTY 表格按批次打印；NDJSON 每次输出一条 `{ "type": "snapshot", "resource": "...", "observed_at": "...", "data": ... }`。这是完整查询快照，不承诺事件流或历史数据完整性。任何请求错误终止监看并返回对应退出码。

## 3. 服务与本地配置

### 服务启动

```bash
hotmouse serve --addr 127.0.0.1:8080 --db ./data/hotmouse.db --autorun
hotmouse status
hotmouse version --output json
hotmouse completion bash > hotmouse.bash
```

| 命令 | 参数和行为 |
| --- | --- |
| `serve` | `--addr`、`--db`、`--autorun[=true\|false]`、`--auth required\|disabled`、`--shutdown-timeout 15s`；前台运行，日志写入 stderr |
| `status` | 聚合 `/healthz`、`/api/settings/health`、`/api/workers/observability`；返回 `{service, health, worker}`，任何查询失败即失败；worker 保留 observability 原始响应 |
| `version` | 本地返回 `{version, commit, build_time, go_version}`，无需服务 |
| `completion` | 输出指定 shell 的补全脚本，资源 ID 补全可调用相应只读查询 |

`serve` 默认 `--db hotmouse.db`、autorun=false、auth=required。监听默认改为 `127.0.0.1:8080`；现有 `HOTMOUSE_ADDR` 显式设置仍优先于内置默认值。`--autorun=false` 可覆盖环境中的 `HOTMOUSE_AUTORUN=1`。

SIGINT/SIGTERM 停止调度、取消正在执行的本地任务、关闭 HTTP 服务并释放数据库；达到 shutdown timeout 后退出。每个 SQLite 文件只允许一个 HotMouse 服务进程，启动时获取进程级文件锁。

`worker enable` 只打开持久化的 Worker 配置开关，不会启动未启用的 autorun 循环。`project stop` 会修改项目状态并释放开放租约，正在运行的外部进程是否立即停止取决于 Worker 对租约变化的处理，命令不承诺同步结束外部进程。

### 客户端配置

Linux/macOS 使用 `os.UserConfigDir()` 下的 `hotmouse/config.yaml`，Windows 使用对应的系统配置目录；不手工拼接 HOME。初始内置 profile 为 `local`，只有修改配置时才创建文件。

```yaml
schema_version: 1
current_profile: local
profiles:
  local:
    server: http://127.0.0.1:8080
    project: proj_001
    creator: cli
    worker: agent-a
    timeout: 30s
    output: table
    color: auto
    token_file: /run/secrets/hotmouse-internal-token
```

可读写键为 `server`、`project`、`creator`、`worker`、`timeout`、`output`、`color`、`token_file`。profile 名须唯一，不允许通过键写入任意对象路径。`config show` 显示选中 profile 的持久化配置；`--effective` 显示合并环境和默认值后的配置，并附带各字段来源，令牌值始终隐藏。`config get` 返回键对应的值；`config path` 返回路径。

```bash
hotmouse config profile add staging --server https://hotmouse.example.test
hotmouse config profile use staging
hotmouse config set timeout 45s
hotmouse config unset project
hotmouse config show --effective
```

`profile remove` 不允许删除当前 profile，需先切换；删除同时移除该 profile 的本地凭据记录，但不吊销服务端会话。配置和凭据使用原子写入，POSIX 下配置文件权限为 0600、目录为 0700，Windows 使用用户私有 ACL。

| 环境变量 | 对应配置/行为 |
| --- | --- |
| `HOTMOUSE_CONFIG` / `HOTMOUSE_PROFILE` | 客户端配置文件 / profile |
| `HOTMOUSE_SERVER` / `HOTMOUSE_PROJECT` | 客户端服务 URL / 默认项目 |
| `HOTMOUSE_CREATOR` / `HOTMOUSE_WORKER` | creator / 租约操作的 worker 默认值 |
| `HOTMOUSE_TIMEOUT` / `HOTMOUSE_OUTPUT` | 请求超时 / 输出格式 |
| `HOTMOUSE_INTERNAL_TOKEN` | 内部令牌；优先级低于显式 `--token-file`，高于 profile 的 token_file |
| `HOTMOUSE_ADDR` / `HOTMOUSE_DB` | 服务监听 / 数据库；已有变量继续支持 |
| `HOTMOUSE_AUTORUN` / `HOTMOUSE_AUTH_REQUIRED` | 服务调度 / 认证；按现有 1、0 语义解析 |
| `HOTMOUSE_PROJECT_CONTEXT_ROOT` | 服务端项目上下文目录，继续遵循后端原有解析顺序 |

配置里的 `project` 与 `server` 绑定；修改 server 时清除默认项目。相对 token_file 路径相对于配置文件目录解析，`--token-file` 相对于当前目录解析。业务 HTTP URL 不允许静默跳转到另一个 origin，认证数据仅发往绑定的服务。

## 4. 认证

```text
hotmouse auth captcha
hotmouse auth register --username NAME
hotmouse auth login --username NAME
hotmouse auth logout
hotmouse auth status
hotmouse auth whoami
hotmouse auth password
```

交互式注册/登录先请求算术验证码，显示 question，再读取答案和隐藏回显的密码；验证码不自动求解。注册成功也保存服务返回的会话。输入校验沿用后端：用户名长度 3–32，字符规则由服务校验；密码长度 8–72。

自动化注册/登录使用 `--password-stdin --captcha-id ID --captcha-answer ANSWER --non-interactive`；先通过 `auth captcha --output json` 获取挑战。密码不能通过命令行参数、profile 或 verbose 日志传递。

`auth password` 交互读取当前密码和新密码，或用 `--file PATH` 提供 `{current_password, new_password}`；敏感请求文件也可为 `-`。新密码除长度要求外，还须包含大小写字母、数字和特殊字符，沿用后端现有校验。修改后按服务语义保留当前会话，不自行假设其他会话仍有效。

会话 cookie 保存在配置目录内单独的 `credentials.json`，按 profile 和规范化服务 URL 隔离，遵守 cookie 的过期、Secure 和刷新规则。CLI 只保存一个有效 cookie 名，优先 `session_token`，兼容旧 `hotmouse_session`。密码不保存，认证命令成功输出用户信息或状态，不输出会话令牌。

业务认证优先级：显式/环境/profile 内部令牌 > 该服务的会话 cookie。`auth whoami/password/logout` 明确使用会话 cookie；内部令牌没有可映射的普通用户身份，只有令牌时这些命令退出 3。`auth status` 返回 `{authenticated, mode, user}`，无有效凭据时为 `{authenticated: false, mode: none, user: null}`；该命令将凭据检查的 HTTP 401 转换为未认证状态，查询成功退出 0，连接错误仍失败。whoami 无会话仍退出 3。

auth status 的会话通过 me 验证，mode=session、user 为用户对象；内部令牌通过受保护健康接口检查，正常接受时 mode=internal、user=null。如果健康对象的 auth check 表明认证已关闭，则返回 authenticated=false、mode=disabled、user=null，因为请求被接受不能证明令牌有效。没有凭据时只报告本地 none 状态，不探测服务认证策略。

`auth logout` 调用服务吊销当前会话，成功后删除本地 cookie；服务明确返回会话已失效时也删除本地 cookie。网络失败时保留凭据并报告失败，避免声称会话已吊销。内部令牌不通过 logout 撤销。

当前模板归属依赖用户会话，内部令牌没有用户 ID；令牌调用时继续使用后端现有的匿名模板空间。需要个人模板时使用会话认证，并移除令牌覆盖。

## 5. 项目与项目图

以下命令中 `[PROJECT]` 可以省略并使用默认项目。其他资源命令使用 `--project`，例如 `hotmouse goal show g001 -p proj_001`。

### 项目

| 命令 | 参数 / 返回 |
| --- | --- |
| `project list` | `--status active\|stopped\|completed\|failed`、`--query TEXT`、`--limit N`；返回 ProjectSummary 数组 |
| `project show [PROJECT]` | 返回完整 ProjectDetail：project、facts、goals、steps、findings、hints；支持监看 |
| `project create` | `--title TEXT --origin TEXT --goal TEXT` 必填；`--hint TEXT` 可重复、`--creator NAME`、`--template ID`、`--use`；支持 `--file`，返回 ProjectDetail |
| `project use PROJECT` | 验证并保存默认项目；返回 `{project, server}` |
| `project rename [PROJECT]` | `--title TEXT` 必填；返回 Project |
| `project stop/resume [PROJECT]` | 设置 stopped/active；返回 Project |
| `project complete [PROJECT]` | `--from FACT --description TEXT --worker NAME` 必填；支持 `--file`，返回被完成的 Goal |
| `project reopen [PROJECT]` | `--description TEXT` 必填、`--creator NAME`；支持 `--file`，返回 `{project, fact, step, goal}` |
| `project delete [PROJECT]` | 确认后删除项目和数据库关联记录；返回 `{deleted, id}` |
| `project campaign [PROJECT]` | 返回服务端 Campaign 汇总原始对象；支持监看 |
| `project timeline [PROJECT]` | 返回事件数组；`--since RFC3339`、`--limit N` 为客户端过滤；支持监看 |
| `project export [PROJECT]` | `--format yaml\|timeline` 默认 yaml；`--out PATH`、`--overwrite` |
| `project wait [PROJECT]` | `--until completed\|stopped\|failed` 默认 completed、`--max-wait 30m`、`--interval 2s`；返回满足条件时的 ProjectDetail |

创建时 title、origin、goal 都不能是空文本。使用模板时先读取模板，在客户端展开为创建请求：显式 title/origin/goal 覆盖模板对应字段，显式 hints 追加到模板 hints。模板正文中的 `[URL]` 等占位符不做自动替换，应通过 `--origin` / `--goal` 明确提供内容。`--file` 与 `--template` 互斥。

creator 默认取命令参数、`HOTMOUSE_CREATOR`、profile.creator，最后为 `cli`。创建时仅有 origin 事实和初始顶层目标，不隐式创建或执行步骤。`--use` 在创建成功后另行保存默认项目；若本地写入失败，保留创建结果、返回退出码 1，并在错误中说明项目已创建。

`project complete` 对应已有兼容接口，由服务选择一个 active 顶层目标完成，不能用它强制设置项目为 completed。完整项目只有在所有目标均 completed 后才自动结束。确定目标的脚本优先使用 `goal complete GOAL`；不额外提供会绕过证据链的 `project update --status completed`。

`project reopen` 仅适用于 completed 项目，会重新激活目标，并生成外部反馈 Step 和 Fact。stopped 项目使用 `resume`；failed 项目可用 `resume` 清除失败状态并重新触发规划。删除当前默认项目成功后清除本地默认值；删除不额外承诺清理 Worker 工作目录。

`project wait` 只观察，不启动 Worker：等待 completed 时遇到 stopped 或 failed 退出 5；等待 stopped 时遇到 completed 或 failed 退出 5；等待 failed 时遇到 completed 或 stopped 退出 5。超时退出 6。每个 HTTP 请求仍使用全局 timeout。成功前进度写入 stderr，stdout 只输出最终结果。

`project export` 将后端原生 YAML 或 timeline 文本写入 stdout 或 `--out` 文件，不使用通用 JSON 包装，拒绝 `--output`、`--select` 和 `--watch`。`--out -` 等同 stdout；文件已存在时必须提供 `--overwrite`，`--yes` 不代替此参数。文件写入采用临时文件加原子替换。

现有 graph YAML 的 Step 条目不含 ID，且并非完整数据库快照，因此不提供与 export 对称的 graph import 或 restore。

### 目标

| 命令 | 参数 / 行为 |
| --- | --- |
| `goal list` | `--status active\|completed`、`--parent GOAL`、`--limit N`；从 ProjectDetail 提取 Goals |
| `goal show GOAL` | 返回单个 Goal |
| `goal create` | `--description TEXT` 必填、`--parent GOAL`、`--priority INT` 默认 0、`--creator NAME`；支持 `--file` |
| `goal update GOAL` | `--description TEXT`、`--priority INT`，至少提供一个；支持 `--file` |
| `goal complete GOAL` | `--from FACT --description TEXT --worker NAME` 必填；支持 `--file` |
| `goal delete GOAL` | 确认后删除符合服务约束的子目标 |

完成必须引用当前项目内至少一个现有 Fact，并给出结论说明及 worker 身份。`--worker` 从命令参数、`HOTMOUSE_WORKER`、profile.worker 解析，没有默认身份则报错。

不提供通过普通 update 修改目标 status、parent 或单独 reopen 的参数。顶层目标不可删除；已完成目标、有子目标或被 Step 引用的目标也不可删除。完成最后一个活动目标可能结束项目并按 Worker 的 completed_action 清理工作目录。

### 步骤

| 命令 | 参数 / 行为 |
| --- | --- |
| `step list` | `--state pending\|working\|concluded\|abandoned`、`--goal GOAL`、`--worker NAME`、`--limit N` |
| `step show STEP` | 返回单个 Step，表格显示来源 Fact、目标、worker 和心跳 |
| `step create` | `--from FACT --description TEXT` 必填；`--goal GOAL`、`--priority INT` 默认 0、`--creator NAME`、可选 `--worker NAME`；支持 `--file` |
| `step update STEP` | `--priority INT`、`--goal GOAL`，至少一个；支持 `--file` |
| `step abandon STEP` | 写入 abandoned=true；返回 Step |
| `step claim STEP` | `--worker NAME`；通过 heartbeat 原子认领或续租，返回 Step |
| `step heartbeat STEP` | `--worker NAME`；单次续租，返回 Step |
| `step release STEP` | `--worker NAME`；释放当前 worker 的租约，返回 Step |
| `step conclude STEP` | `--description TEXT --worker NAME`；可选 `--finding-file PATH`；支持 `--file`，返回 `{fact, step, finding}` |
| `step execute STEP` | 调用服务内本地 Worker，同步返回 `{fact, step, finding}`；可选 `--timeout` |

创建 Step 时必须提供至少一个现有来源 Fact。只有显式 `--worker` 或请求文件中的 worker 才表示创建即认领；不会因为 profile 设置了 worker 就自动认领新 Step。其他租约操作的 worker 默认规则与 goal complete 相同。

`state` 仅为 CLI 视图，由持久字段派生：to/concluded_at 存在为 concluded，其次 abandoned=true 为 abandoned，其次 worker 非空为 working，剩余为 pending。JSON/YAML 保留原始字段，不注入派生 state。priority 数值越大，Step 调度越优先。

已认领、已结束、已放弃的 Step 不能 update 或 abandon；不提供解除 abandoned、修改 description/from、清空 goal、删除 Step 等后端未支持的操作。release 后才可以调整未结束 Step。

claim 与 heartbeat 当前是同一个原子服务操作：heartbeat 也可能认领已释放或过期的 Step。命令不承诺“仅续租且不认领”。不提供 `--force` 抢占其他 worker。

conclude 为 Step 生成一个新 Fact，并可在同一事务中记录 Finding；finding 的 fact_id 由服务绑定新 Fact，客户端不为嵌套 Finding 指定 fact_id。`--finding-file` 内容为 `{title, description, severity, kind, data}`，不能与完整 `--file` 同时使用。

execute 由服务端已配置 Worker 认领和执行，不接受用于换人的 `--worker`。默认 HTTP timeout 30s 可能小于任务耗时，应显式设为例如 `15m`。请求取消可能触发恢复记录，重试前查看 Step 和 `worker status/history`；服务会按已有恢复协议处理可恢复任务，不保证所有任务可重跑。

### 决策租约

```text
hotmouse decide show --project PROJECT
hotmouse decide claim --project PROJECT --worker NAME --trigger TEXT
hotmouse decide heartbeat --project PROJECT --worker NAME
hotmouse decide release --project PROJECT --worker NAME
```

show 从 ProjectDetail.project.decide 提取租约，无租约时 JSON/YAML 返回 null，表格显示 unclaimed。claim 的 trigger 是非空自由文本，可填写 `bootstrap`、`idle` 或 `manual`；claim 同一 worker 可续租。heartbeat 只维护已有决定租约，release 仅释放当前持有者的租约。

这些命令供外部 Agent 实现决策协议；claim 不会触发本地模型做计划。外部 Agent 认领后通过 goal/step/finding 命令写入结果，再 release。

### 事实、提示和发现

| 命令 | 参数 / 行为 |
| --- | --- |
| `fact list` / `fact show FACT` | 从 ProjectDetail 提取事实；list 支持 `--query TEXT`、`--limit N` |
| `hint list` / `hint show HINT` | 从 ProjectDetail 提取提示；list 支持 `--limit N` |
| `hint add` | `--content TEXT` 必填、`--creator NAME`；支持 `--file` |
| `finding list` | `--severity critical\|high\|medium\|low\|info`、`--kind KIND`、`--fact FACT`、`--limit N` |
| `finding show FINDING` | 返回项目内单个 Finding |
| `finding create` | `--title TEXT --description TEXT` 必填；`--severity` 默认 info、`--kind` 默认 finding、`--fact FACT`、`--data-file PATH`、重复 `--evidence TEXT`；支持 `--file` |

Fact 是来源/执行结论，保留不可变证据链，没有独立 create/update/delete。补充事实应声明 Step 后 conclude；hint 是调度指导文本，不替代 Fact。

finding create 的 data-file 必须是单个 JSON/YAML 对象，重复 evidence 追加到 data.evidence；原 evidence 非字符串数组时拒绝追加，避免静默覆盖。Finding 支持 info 级别；只有 kind 为 `vulnerability` / `security_vulnerability`、severity 非 info 且绑定 Fact 时，才投影到漏洞列表。

hint、finding 没有更新和删除接口，CLI 不模拟此类修改。

### 租约读取副作用

项目列表、详情、Campaign、graph YAML 和 graph timeline 导出会回收过期 Step/Decide 租约；从详情提取的 goal/step/fact/hint/decide 查询也继承这一行为。产品 timeline 不触发租约回收。

CLI 不为每个写命令预先读取 ProjectDetail。尤其是 goal complete、step update、project rename/delete，要保持当前后端规定的租约回收边界；确认时展示已解析 ID，不额外用详情查询做状态“刷新”。

## 6. 漏洞和模板

### 漏洞

| 命令 | 参数 / 行为 |
| --- | --- |
| `vulnerability list` | 显式 `--project ID`、`--severity critical\|high\|medium\|low`、`--status confirmed\|ignored`、`--limit N` |
| `vulnerability show ID` | 查询合并后的漏洞列表并按完整 ID 提取详情 |
| `vulnerability summary` | 可显式按 project、severity、status 过滤；返回 `{critical, high, medium, low}` |
| `vulnerability review ID` | `--status confirmed\|ignored` 必填；返回更新后的漏洞 |
| `vulnerability batch-review ID...` | `--status confirmed\|ignored` 必填，或 `--file PATH` 提供 `{ids, status}`；去重 ID 后提交单次批量请求 |
| `vulnerability refresh` | 必须显式提供 `--project ID` 或 `--all`；重建 Finding 的漏洞投影，返回服务全局 summary |

漏洞列表是 Finding 的合并投影，ID 应从最新列表读取，不能用 Finding ID 猜测；对合并后的代表 ID 审核会更新该组底层记录。confirmed/ignored 是审核状态，不扩展成 fixed、pending 或 verified。

summary 无过滤时直接使用 summary 接口；有过滤时查询过滤后的合并列表，在客户端按严重性统计，避免把全局数字当项目数字。refresh 的现有接口即使针对单项目执行，响应也是全局 summary，因此命令不将它解释为本次更新数量。

不单独提供 vulnerability create/update/delete；新增漏洞通过 finding create 或 step conclude 记录来源。当前不提供截图上传、附件、报告、CSV/Markdown 导出或导出记录管理。

### 项目模板

| 命令 | 参数 / 行为 |
| --- | --- |
| `template list` | `--builtin` 或 `--custom`，二者互斥；返回可见模板数组 |
| `template show ID` | 从可见列表提取模板；返回模板对象 |
| `template create` | `--title TEXT --origin TEXT --goal TEXT` 必填、重复 `--hint TEXT`、`--creator NAME` 默认 template；支持 `--file` |
| `template delete ID` | 确认后删除当前身份可管理的自定义模板 |

内置模板只读。自定义模板按身份隔离，当前服务限制每个用户 50 个模板，title/origin/goal 按服务校验为 1–200，hints 最多 10 条。字段长度按当前 Go 后端字节校验，不提前承诺 Unicode 字符计数。

不提供 template update，因为当前没有更新路由；复制模板内容并 create 可生成另一个模板。`project create --template ID` 在客户端展开模板，不需要新增创建项目的后端接口。

## 7. 本地 Worker

```bash
hotmouse worker config set driver pi
hotmouse worker config set command ''
hotmouse worker config env set PI_CLI --value /opt/pi/bin/pi
hotmouse worker config env set PI_API_KEY --value-stdin
hotmouse worker config set runtime.execute_timeout_seconds 600
hotmouse worker test
hotmouse worker enable
hotmouse worker status --watch
hotmouse worker history --project proj_001 --output json
```

| 命令 | 参数 / 行为 |
| --- | --- |
| `worker status` | 返回 `/api/workers/observability` 原始对象，包含单个本地 Worker、执行和恢复信息；支持监看 |
| `worker history` | `--name NAME`、显式 `--project ID`、`--task-type decide\|execute`、`--status TEXT`、`--limit 20`；支持监看 |
| `worker test` | 测试当前配置；返回 `{ok, status, detail}`，ok=false 退出 1 |
| `worker enable/disable` | 只修改 enabled，返回 WorkerConfig |
| `worker config show` | 返回已脱敏配置 |
| `worker config set KEY VALUE` | 修改一个配置键，返回 WorkerConfig |
| `worker config apply --file PATH` | 按文件中明确提供的字段更新配置，未提供字段保持原值 |
| `worker config env list` | 返回脱敏 environment 对象 |
| `worker config env set KEY` | `--value TEXT`、`--value-stdin` 或 `--value-file PATH` 三选一；更新一个环境变量 |
| `worker config env unset KEY` | 通过 environment_remove 真正删除环境变量 |

env set 对名称包含 key/token/secret/password 的变量禁止 `--value` 明文参数，应使用 stdin/文件；其他变量可使用 `--value`。客户端同样进行脱敏，不提供从已脱敏响应反推或“显示秘密”的开关。

### 可修改配置

| KEY | 类型 / 约束 |
| --- | --- |
| `name` | 非空字符串，本地 Worker 显示/执行身份 |
| `driver` | `mock`、`command`、`pi`、`codex`、`claude` |
| `command` | 命令字符串；空字符串表示清除自定义命令 |
| `enabled` | boolean，也可通过 enable/disable 修改 |
| `workspace_root` | 服务主机路径；空字符串恢复后端默认路径 |
| `completed_action` | `keep` 或 `remove` |
| `context_root` | 服务主机上已存在的目录；空字符串恢复环境/目录发现 |
| `runtime.decide_timeout_seconds` | 1–86400，默认 300 |
| `runtime.execute_timeout_seconds` | 1–86400，默认 300 |
| `runtime.conclude_timeout_seconds` | 1–3600，默认 90 |
| `runtime.conclude_reserve_seconds` | 0–120，默认 0 |
| `runtime.recovery_attempts` | 1–10，默认 3 |
| `runtime.format_retries` | 0–1，默认 1 |

CLI 接受历史 driver `shell` → command、`claudecode` / `claude_code` → claude 并规范化写入。已有非空 command 会被后端优先使用，切换到内置 driver 时如需使用内置适配器，应显式清空 command。

Worker 配置在服务主机生效，相对路径由服务的当前目录解析，不解析为远程客户端的目录。默认 workspace root 由后端依据 SQLite 路径生成；remove 只删除具有匹配 `.hotmouse-project` 标记的项目目录。

上下文沿用已有目录结构：`default.project-context.yaml` 与 `projects/PROJECT.project-context.yaml`。项目创建会尽力创建项目上下文文件，但不保证成功；后端按固定默认文件和项目文件进行合并，现有 inherits 字段不代表任意文件继承能力。上下文是 Agent 指导信息，不能解释为网络访问控制。

客户端不检查本机是否存在服务端 context_root。当前后端在显式配置的目录不存在时不加载上下文，也不会继续使用环境变量或自动发现目录；应配置服务主机上已有的目录。

CLI 不提供远程文件编辑或未实现的 `project context set`。本地部署可以直接编辑上下文文件，远程部署通过服务主机管理文件。

当前 Worker config PUT 会默认填充 name、driver、command、enabled，所以实现 set/apply/enable/disable 时必须先读取配置、合并用户修改，再完整发送这些字段；runtime 和 environment 仅提交明确修改部分。环境响应中的 `***` 不能覆盖真实秘密。

environment 更新按键合并，空字符串不会删除键。`env unset` 使用后端 `environment_remove: [KEY...]` 删除指定键，禁止同一请求同时设置和删除同一键。读取再更新没有版本保护，多客户端并发修改仍可能覆盖；配置命令帮助注明此限制，后续可增加 PATCH 与配置版本。

Worker test 对内置 driver 检查本地 CLI 和 `--version`，不验证模型服务凭据、模型权限或网络可达性。自定义 command 的测试会实际执行配置命令，并设置 `HOTMOUSE_TASK_TYPE=healthcheck`；适配命令应按该任务类型处理。

history 当前服务只返回最近 20 条，可按 worker 名从服务端筛选；project/task-type/status 在客户端对这 20 条过滤。`--limit` 范围为 1–20，帮助和表格输出注明历史窗口，不能宣称查询到完整历史。

不提供独立 `worker start/run` 进程、worker pool、容器调度或任意 concurrency 参数。需要自动执行时使用 `serve --autorun`；手动执行已有 Step 使用 `step execute`。

## 8. 系统设置、审计和通知

### 系统设置

```text
hotmouse settings show
hotmouse settings set step_timeout 30
hotmouse settings apply --file settings.yaml
hotmouse settings health
hotmouse settings cleanup --yes
```

settings show 返回完整 Settings，set 更新一个键，apply 按单个 JSON/YAML 对象修改明确提供的键。当前 API 为完整 PUT，两种更新都先 GET，再合并并发送完整 Settings；至少提供一个修改字段，拒绝未知字段。

| KEY | 单位 / 范围 / 默认值 |
| --- | --- |
| `step_timeout` | Step 租约秒数，≥5，默认 15 |
| `decide_timeout` | Decide 租约秒数，≥5，默认 15 |
| `worker_unhealthy_retry_after_seconds` | 秒，1–3600，默认 5 |
| `worker_rejected_retry_after_seconds` | 秒，1–3600，默认 5 |
| `max_failed_login_attempts` | 次，1–50，默认 5 |
| `rate_limit_window_minutes` | 分钟，1–1440，默认 15 |
| `session_duration_hours` | 小时，1–168，默认 24 |
| `log_retention_days` | 天，1–3650，默认 30 |
| `export_retention_days` | 天，1–3650，默认 30；保留兼容字段，当前无对应导出记录功能 |
| `notification_retention_days` | 天，1–3650，默认 14 |
| `project_idle_alert_hours` | 小时，1–720，默认 12 |

settings health 返回服务原始健康对象；summary.status=error 时退出 1，ok/warning 时退出 0，保留完整 checks/alerts。根命令 status 使用相同规则，不把命令/CLI 可用解释为模型调用成功。settings cleanup 按现有保留策略删除过期会话、旧审计、Worker 历史、登录尝试和旧已读通知，返回 `{ran_at, deleted, summary}`。

租约 timeout 与 Worker runtime 的任务执行 timeout 是不同设置；命令帮助分别说明单位和作用，settings 的整数不接受 `30s` 等 duration 字符串。

cleanup 当前缺少 dry-run 接口，因此不提供 `--dry-run` 或虚构准确的删除预览。配置更新和 cleanup 不是跨资源的原子事务，不承诺并发写入隔离或自动回滚。

### 审计与通知

| 命令 | 参数 / 行为 |
| --- | --- |
| `audit list` | `--limit 100` 范围 1–500；`--actor TEXT`、`--action TEXT`、显式 `--project ID`、`--since RFC3339`；支持监看 |
| `notification list` | `--limit 50` 范围 1–200；`--unread`、`--level TEXT`；支持监看 |
| `notification unread-count` | 返回 `{count}`，使用独立计数接口 |
| `notification read ID...` | ID 为正整数；或显式 `--all`，两者互斥；返回 `{count}`，代表剩余未读数 |
| `notification clear` | 确认后删除服务全部通知，返回 `{status: cleared}` |

notification read 没有 ID 且没有 `--all` 时拒绝执行，避免把误调用解释为全部已读。clear 作用于全部通知，不提供后端未支持的 `--read-only` 或单个删除。

audit 和 notification 的过滤在服务返回的最近 limit 条窗口内进行，不保证返回 limit 个匹配项，也不宣称完整检索。audit 的 project 过滤匹配结构化 target_id 或精确项目路径段，不做任意子串匹配；记录没有可识别项目关联时不推断归属。

当前服务没有全局游标、offset 或 page，CLI 不提供通用分页参数。对有完整列表的项目图/漏洞列表，limit 在过滤后截取；对有上限的 Worker/audit/notification 窗口，保留上述限制。

所有 limit 必须是正整数，无服务窗口上限的命令在省略 limit 时返回全部匹配项。query 对 ID 和正文/标题作不区分大小写的包含匹配；actor、action、level、kind、status、worker 等离散字段作精确匹配。默认排序保留接口顺序；时间筛选使用含边界的 `timestamp >= since`。

## 9. 使用流程

先在 `backend/` 运行 `go build -o hotmouse ./cmd/hotmouse`，然后将该二进制放入 PATH；以下示例也可用 `./hotmouse` 调用。

### 自动运行项目

终端一启动服务：

```bash
hotmouse serve --db ./data/hotmouse.db --autorun
```

终端二配置和创建项目：

```bash
hotmouse auth register --username operator
hotmouse worker config set driver pi
hotmouse worker config set command ''
hotmouse worker test
hotmouse worker enable

hotmouse project create \
  --title '演示环境评估' \
  --origin '本地演示应用 http://127.0.0.1:3000' \
  --goal '验证登录模块并记录发现' \
  --hint '优先检查身份认证流程' \
  --use

hotmouse project campaign --watch
hotmouse project wait --until completed --max-wait 30m
hotmouse project export --format yaml --out ./project.yaml
```

### 手动步骤和证据链

服务不启用 autorun 时，可以使用手动流程：

```bash
hotmouse project use proj_001
hotmouse config set worker agent-a

hotmouse step create \
  --from origin \
  --goal g001 \
  --description '验证演示应用的登录校验' \
  --priority 10

hotmouse step claim s001
hotmouse step heartbeat s001
hotmouse step conclude s001 \
  --description '已验证登录校验，详细结果见关联发现' \
  --finding-file ./finding.yaml

hotmouse fact show f001
hotmouse goal complete g001 \
  --from f001 \
  --description '登录模块验证已完成，证据已记录'
```

`finding.yaml` 示例：

```yaml
title: 登录校验结果
description: 记录演示环境中复现的问题和影响。
severity: medium
kind: security_vulnerability
data:
  location: http://127.0.0.1:3000/login
  evidence:
    - 复现步骤和响应已在项目工作目录中保存。
  remediation: 修复登录入口的服务端校验。
```

实际使用返回的 Step/Fact/Goal ID；示例中的 s001/f001/g001 仅适用于相应初始图。长任务由 Agent 根据 step_timeout 定期调用 heartbeat，单次 heartbeat 不会在客户端后台持续续租。

### 脚本调用

```bash
PROJECT_ID=$(hotmouse project create \
  --file ./project-request.yaml \
  --select project.id \
  --non-interactive)

STEP_ID=$(hotmouse step create \
  --project "$PROJECT_ID" \
  --from origin \
  --description '执行一个可验证步骤' \
  --select id \
  --non-interactive)

hotmouse step execute "$STEP_ID" \
  --project "$PROJECT_ID" \
  --timeout 15m \
  --output json \
  --non-interactive

hotmouse vulnerability list --project "$PROJECT_ID" --output json
hotmouse worker status --watch --output ndjson
```

`project-request.yaml` 使用 CreateProject 字段，不是 graph export 格式：

```yaml
title: 演示环境评估
origin: 本地演示应用 http://127.0.0.1:3000
goal: 验证登录模块并记录发现
hints:
  - content: 优先检查身份认证流程
    creator: cli
```

## 10. API 对应关系

`P`、`G`、`S` 等占位符必须进行 URL 路径转义。只使用下列规范路由，沿用现有 HTTP 方法和请求字段。

| CLI | HTTP / 客户端逻辑 |
| --- | --- |
| `auth captcha/register/login/logout/whoami/password` | `GET captcha`、`POST register/login/logout`、`GET me`、`PUT password`，均位于 `/api/auth/` |
| `auth status` | 会话用 `/api/auth/me`；内部令牌用 `/api/settings/health`，无凭据返回本地未认证状态 |
| `project list/create` | `GET/POST /projects` |
| `project show/use/wait` | `GET /projects/P`；use 本地保存，wait 轮询 |
| `project rename` | `PUT /projects/P/title` |
| `project stop/resume` | `PUT /projects/P/status`，status=stopped/active |
| `project complete/reopen/delete` | `POST /projects/P/complete`、`POST /projects/P/reopen`、`DELETE /projects/P` |
| `project campaign/timeline` | `GET /api/projects/P/campaign`、`GET /api/projects/P/timeline` |
| `project export` | `GET /projects/P/export?format=yaml\|timeline` |
| `goal/step/fact/hint/decide list/show` | 从 `GET /projects/P` 的详情提取相应数据 |
| `goal create/update/delete/complete` | `POST /projects/P/goals`、`PATCH/DELETE /projects/P/goals/G`、`POST /projects/P/goals/G/complete` |
| `step create/update/abandon` | `POST /projects/P/steps`、`PATCH /projects/P/steps/S`；abandon 发 `{abandoned:true}` |
| `step claim/heartbeat/release/conclude` | `POST /projects/P/steps/S/heartbeat`、`/release`、`/conclude`；claim 使用 heartbeat |
| `step execute` | `POST /api/projects/P/steps/S/execute` |
| `decide claim/heartbeat/release` | `POST /projects/P/decide/claim\|heartbeat\|release` |
| `hint add` | `POST /projects/P/hints` |
| `finding list/show/create` | `GET/POST /projects/P/findings`；show 客户端提取，并确认项目存在 |
| `vulnerability list/show` | `GET /api/vulnerabilities`，支持 project_id/severity/status；show 客户端提取 |
| `vulnerability summary` | `GET /api/vulnerabilities/summary` 或过滤列表的客户端统计 |
| `vulnerability review/batch-review` | `PATCH /api/vulnerabilities/ID/status`、`POST /api/vulnerabilities/batch/status` |
| `vulnerability refresh` | `POST /api/projects/P/vulnerabilities/refresh` 或 `POST /api/vulnerabilities/refresh` |
| `template list/show/create/delete` | `GET/POST /api/templates`、`DELETE /api/templates/ID`；show 客户端提取 |
| `worker status/history/test` | `GET /api/workers/observability`、`GET /api/workers/history` 或 `/api/workers/NAME/history`、`POST /api/workers/config/test` |
| `worker config/enable/disable` | `GET/PUT /api/workers/config`；客户端合并更新，env unset 使用 environment_remove |
| `settings show/set/apply/health/cleanup` | `GET/PUT /api/settings`、`GET /api/settings/health`、`POST /api/settings/cleanup` |
| `audit list` | `GET /api/audit?limit=N` |
| `notification list/unread-count/read/clear` | `GET /api/notifications`、`GET /api/notifications/unread-count`、`POST /api/notifications/read`、`DELETE /api/notifications` |
| `serve/config/version/completion` | 本地操作；status 组合公共健康和受保护的状态接口 |

finding list 的现有接口可能把不存在的项目返回为空列表，实现时先读取 `/projects/P` 确认存在，才能保证缺失项目退出 4；该命令因此会触发详情查询的租约回收。

## 11. 实现划分和验收

实现在 `backend` Go 模块内，保留现有服务代码，通过 Cobra 管理命令树，使用标准库 net/http 与现有 YAML 库处理客户端请求和数据。

```text
backend/cmd/hotmouse/main.go       命令入口
backend/internal/cli/             命令、参数校验、默认值和输出
backend/internal/apiclient/       路由、cookie、令牌、错误和超时
backend/internal/clientconfig/    profile、配置、凭据存储
backend/internal/httpapi/         已有服务 API
backend/internal/core/            已有持久化和协议状态机
backend/internal/worker/          已有服务内串行 Worker
```

实现分层：

1. 命令入口、serve、config/profile、HTTP 客户端、认证、输出和退出码。
2. 项目/目标/步骤/事实/提示/发现/决定租约命令，打通手动执行和证据链。
3. Worker、漏洞审核、模板、系统设置、审计、通知。
4. 监看、等待、补全、服务单实例锁和优雅退出，核对自动化与兼容行为。

实现状态与保留限制：

| 项目 | 实现要求 |
| --- | --- |
| 命令入口 | 已迁移到 serve，裸 hotmouse 显示帮助 |
| Worker env 删除 | 已支持配置字段 `environment_remove`，env unset 实际删除键 |
| 配置局部修改 | 第一版 GET→合并→PUT；明确并发覆盖限制，后续可增加 PATCH/版本校验 |
| 服务进程生命周期 | 已实现同数据库单实例锁、HTTP/Worker 取消与关闭流程 |
| 错误分类 | 第一版使用实际 HTTP 状态；建议后续统一语义错误状态并增加稳定 code |
| Auth 状态可观测性 | 现有接口不能在关闭认证时证明内部令牌有效；可后续提供明确 auth_kind 状态 |
| 历史查询 | 当前 history 20 条、audit 500 条、notification 200 条；后续增加分页前保持窗口限制 |

不因 CLI 设计扩展报告、截图/附件、测试报告、Docker/容器、网络 egress、Worker 池和多 Worker 并发；这些能力继续遵循当前项目范围。

关键验收：

- 注册/登录保存并刷新 cookie，切换 profile/server 不串用凭据、默认项目或令牌；内部令牌和会话身份区分正确。
- 手动完成 `project create → step create → claim → conclude → goal complete → export`，图和证据引用与 HTTP 协议一致。
- 写操作不附带会改变租约边界的额外详情请求；不自动重试可能已提交的请求。
- Worker 配置、settings 更新保留未修改字段和秘密；env 删除实际移除键。
- JSON/YAML stdout 无提示和日志，空列表、缺失 ID、部分成功、超时和中断返回规定的结果与退出码。
- 非交互删除缺少 yes 时不发送写请求；空 notification read 不会全部标记已读。
- 停止和重开遵循既有状态机；step claim/heartbeat 不抢占其他 worker，超时恢复保留服务已有行为。
- 导出拒绝隐式覆盖；监看输出可解析，筛选不伪装成完整历史检索。

自动化测试覆盖真实 HTTP 服务下的项目证据链、租约边界、配置更新、认证与 Profile 隔离、漏洞部分成功、输入校验、监看中断、导出覆盖和服务单实例退出。


路线预算、结构化执行结果 `--result-file`、紧凑上下文、历史检索、经验库与 flag 台账的协议和示例见 [strategy.md](strategy.md)。
