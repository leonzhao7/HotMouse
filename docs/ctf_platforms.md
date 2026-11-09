# CTF 平台接入

平台接入由独立的 Go 模块 `backend/internal/ctfplatform` 提供。第一版实现 TSec Benchmark 的五个接口，支持通过 HotMouse HTTP API 或 CLI 获取题目、启动容器、导入项目、获取提示、提交 flag 和关闭容器。TSec 原始协议见 [tsec_challenges_api.md](tsec_challenges_api.md)。

支持比赛总调度和单题操作两种入口。比赛总调度接收平台提供的规则提示词，解读规则、管理整场题目队列，并自动提交 flag、释放容器和开始下一题。每道题继续使用现有 Dispatcher / Worker 解题机制。

## 比赛总调度入口

先设置 `BENCHMARK_BASE_URL`、`BENCHMARK_TOKEN`，连接靶场 VPN，并配置 HotMouse 的 Dispatcher / Worker 模型。总调度使用已保存的 Dispatcher 模型；每题的执行继续使用 Worker 配置。

启动服务时直接传入比赛规则文件：

```bash
./hotmouse serve --db ./data/hotmouse.db \
  --competition-rules-file ./rules.md \
  --competition-platform tsec \
  --competition-concurrency 2 \
  --competition-max-workers 1 \
  --competition-challenge-timeout 20m
```

规则文件入口自动启动 Worker 池及比赛总调度，无需另加 `--autorun`。也可以向一个已经以 `--autorun` 启动的服务提交比赛：

```bash
hotmouse competition start tsec --rules-file ./rules.md \
  --concurrency 2 --max-workers 1 --challenge-timeout-seconds 1200
hotmouse competition status tsec competition_001 --watch --output json
hotmouse competition concurrency tsec competition_001 --concurrency 3
hotmouse competition stop tsec competition_001
```

`--concurrency` 控制同时解答的**题目数**，TSec 最大为 3，默认 1。`--max-workers` 控制每道题内部的 Worker 上限，默认 1；执行进程还受服务全局 `worker_count` 限制。减少题目并发不会中断已有解题，等当前题目退出后再遵守新上限。`--challenge-timeout-seconds` 默认 1200 秒，从启动题目开始计时，包含准备与解题；新项目也以相同秒数作为项目投资预算。

总调度启动时获取题目列表，由 Dispatcher 模型读取总规则并返回规则摘要与完整题目顺序。原始规则及摘要在新项目创建事务中作为提示保存，首次规划、靶场识别和后续执行均可读取，原文优先于摘要。模型不能增加、漏掉题目或修改配置的并发数；实际下发、提交、关闭和队列切换由一个总调度协程负责。自定义 Dispatcher 命令需要支持 `HOTMOUSE_TASK_TYPE=competition_plan`，返回 `{"summary":"规则摘要","challenge_ids":["题目ID", "..."]}`；请求环境包含 `HOTMOUSE_COMPETITION_RULES` 和 `HOTMOUSE_COMPETITION_CHALLENGES`。mock 模式按平台顺序排列，用于本地验证。

题目流程：

```text
queued → starting → running → closing → succeeded / failed
```

每次调度优先处理已有题目，再填补空闲名额：

- Worker 保存的新 flag 自动提交，错误答案不会重复提交；多 flag 题继续解答剩余 flag。
- 平台确认全部 flag 通过时，记录成功证据并完成项目。项目失败、提前结束或达到每题总时限时，记录失败；已保存且未提交的 flag 仍会处理。
- 退出解题时取消该项目的执行，等待在途 Worker 返回，再关闭容器。关闭未确认时停留在 `closing` 并保留名额，确认后自动开始下一题。
- 提交结果未知时保留 `submission_unknown`，不自动重放该 flag；继续通过题目列表检查通关进度，超时后释放该题并继续队列。
- 单题失败不终止比赛。全部题目处理完后比赛为 `completed`，每题的 `succeeded / failed` 结果仍可查询。
- 停止比赛取消当前任务并关闭活跃容器，未开始的题目为 `skipped`。任务结束或认证失效时停止全局调度，避免继续启动题目。

`competition_runs / competition_items` 保存规则、配置、项目关联、队列状态和时间戳；`--autorun` 服务重启后恢复未结束的队列，复用已启动的项目和容器。同一任务只允许一个活跃比赛。规则文件入口是幂等的：同一任务、同一规则已有运行时直接使用已有记录，包括用户已停止或已完成的比赛；要主动开始新的运行，使用 `competition start`。恢复时保留已保存的并发设置；运行中用 `competition concurrency` 调整。

环境变量入口适用于 TSec 镜像：

```bash
docker run --rm -p 127.0.0.1:9981:9981 \
  -v hotmouse-data:/data -v hotmouse-home:/home/node \
  -v "$PWD/rules.md:/rules.md:ro" \
  -e BENCHMARK_BASE_URL -e BENCHMARK_TOKEN \
  -e HOTMOUSE_COMPETITION_RULES_FILE=/rules.md \
  -e HOTMOUSE_COMPETITION_CONCURRENCY=2 \
  -e HOTMOUSE_COMPETITION_CHALLENGE_TIMEOUT=20m \
  hotmouse-tsec:latest
```

`HOTMOUSE_COMPETITION_PLATFORM` 默认 `tsec`，`HOTMOUSE_COMPETITION_MAX_WORKERS` 默认 1。Compose 也透传这些变量，规则文件路径须是容器内可读路径。保存的模型配置放在数据卷中，运行容器仍须能够通过 VPN 访问靶场。

## 配置和使用

在**后端服务进程**的环境中设置平台下发的配置，启动或重启后生效：

```bash
export BENCHMARK_BASE_URL='https://your-tsec-platform.example'
export BENCHMARK_TOKEN='your-benchmark-task-token'
cd backend
go build -o hotmouse ./cmd/hotmouse
./hotmouse serve --db ./data/hotmouse.db --autorun
```

必须按平台要求连接 VPN，并让运行 Worker 的主机或容器能通过该 VPN 访问靶场地址。模块不代替 SSLVPN 客户端建立连接。Docker Compose 已透传这两个变量；在启动 Compose 的环境中设置它们即可。一个服务当前配置一个 TSec 跑分任务。

在另一个终端登录 HotMouse 后操作：

```bash
./hotmouse platform list
./hotmouse platform challenges tsec --output json

# import 会启动题目并导入项目，已就绪的容器直接复用。
./hotmouse platform import tsec web_sql_injection_01 \
  --budget-seconds 1200 --max-workers 1 --use
./hotmouse project campaign --watch
./hotmouse flag list --output json

# 使用 flag list 中的台账 ID；先保存提交尝试，再请求平台并记录判定。
./hotmouse flag submit 1

# 多 flag 题目逐个提交；通过 challenges 查看最新作答进度。
./hotmouse platform challenges tsec --output json
./hotmouse platform close tsec web_sql_injection_01
```

`import` 的预算省略时使用系统默认，`0` 表示不限时。题目 ID 对应 TSec `unique_code`。同一平台、跑分任务、题目重复导入会返回原项目，保留已保存的证据和解题进度；它不会重启已关闭的容器，也不会刷新既有项目的地址。需要继续使用已关闭的容器时先显式 `start`，并确认最新入口地址。

也可以独立调用平台接口，不创建项目：

```bash
./hotmouse platform start tsec web_sql_injection_01
./hotmouse platform hint tsec web_sql_injection_01  # TSec 查看提示会扣分
printf '%s' 'flag{example}' | \
  ./hotmouse platform submit tsec web_sql_injection_01 --flag-file -
./hotmouse platform close tsec web_sql_injection_01
```

直接 `platform submit` 不创建本地 flag 台账。`--flag`、`--flag-file` 或 `--file`（仅包含 `flag` 的 JSON/YAML 对象）可提供答案；输入保持原值，文本文件的尾部换行也会作为 flag 提交。

## 题目与提交台账

导入项目的 origin 包含题目说明、难度、关卡、分数、flag 数量和全部靶场地址。项目目标指导 Worker 保存带 `challenge_id` 的 flag finding。地址保留平台原值，如 `10.0.1.5:8080`、`10.0.1.6:22`，不为 SSH / 原始 TCP 服务强加 HTTP 协议。

项目详情的 `platform` 保存题目与下发结果快照。SQLite 的 `platform_projects` 表使用 `platform + scope_id + challenge_id` 唯一约束，绑定与项目在同一事务创建，删除项目时级联删除。`scope_id` 是平台、基地址和任务令牌的 SHA-256 摘要，令牌本身不会写入绑定。更换跑分任务后，即使题目 ID 相同，也会导入新项目；向旧项目提交时返回 `platform_task_changed`。

`flag submit` 使用项目绑定的题目 ID。flag 的题目 ID 为空时使用该绑定，非空且不一致时拒绝提交。适配器检查 flag 的 1~4096 Unicode 字符约束后才开始本地提交尝试。

| 平台结果 | 本地台账状态 |
| --- | --- |
| `correct: true` | `accepted` |
| `correct: false` | `rejected` |
| `409 duplicate` | `accepted`，返回 `duplicate: true` |
| 其他明确的 4xx 拒绝 | `rejected`，API 保留平台错误 |
| 网络中断、无法解析的响应、5xx | `submission_unknown` |

已经 accepted、pending 或 unknown 的 flag 会阻止重新开始提交。未知结果需先查询平台进度并使用现有 `flag resolve` 台账命令核对；TSec 列表只提供正确 flag 数，无法凭该数字判断某个具体 flag 是否通过。所有写操作均不自动重试。

TSec 同时最多允许 3 道活跃题目，上限由平台强制执行，`invalid_state` 和 `resource_unavailable` 会原样保留。`pending / available / stop_pending / stopped` 状态全部保留，只有 `available` 的列表条目携带地址。导入项目失败时会尝试关闭本次新启动的容器；已有容器不在该补偿操作范围内。

## HTTP API

这些路由使用现有 HotMouse 服务认证，平台认证由适配器在出站请求中添加。客户端不需要传递 `BENCHMARK_TOKEN`。

| 方法 | 路径 | 参数 |
| --- | --- | --- |
| GET | `/api/platforms` | 返回平台能力及 configured 状态 |
| GET | `/api/platforms/tsec/challenges` | 返回标准化题目列表 |
| POST | `/api/platforms/tsec/start?challenge_id=CODE` | 下发 / 启动题目 |
| GET | `/api/platforms/tsec/hint?challenge_id=CODE` | 显式获取提示 |
| POST | `/api/platforms/tsec/close?challenge_id=CODE` | 关闭题目 |
| POST | `/api/platforms/tsec/submit` | JSON：`{"challenge_id":"CODE","flag":"flag{...}"}` |
| POST | `/api/platforms/tsec/import` | JSON：`{"challenge_id":"CODE","title":"可选标题","max_workers":1,"budget_seconds":1200}` |
| POST | `/projects/PROJECT/flags/FLAG_ID/submit` | 使用已保存的 flag，无请求体 |
| POST | `/api/platforms/tsec/runs` | JSON：`{"rules":"比赛规则原文","concurrency":2,"max_workers":1,"challenge_timeout_seconds":1200}` |
| GET | `/api/platforms/tsec/runs` | 列出比赛运行 |
| GET | `/api/platforms/tsec/runs/RUN_ID` | 查询规则、配置、队列及每题结果 |
| PATCH | `/api/platforms/tsec/runs/RUN_ID` | JSON：`{"concurrency":3}` |
| POST | `/api/platforms/tsec/runs/RUN_ID/stop` | 停止比赛 |

标准化字段包括 `id`（TSec `unique_code`）、`completed`（`is_completed`）、`instance_status`（`container_status`）、`addresses`（`container_addr`）。提交评分和 `matched_flag_index` 原样保留，索引 `0` 与 `null` 区分处理。

业务失败保留上游 HTTP 状态、`code`、`message`、`detail`。FastAPI 422 的数组 detail 使用 `validation_error` 标识；传输和协议问题返回 502。未配置平台返回 `503 platform_not_configured`。CLI 的错误输出也保留平台 code 和 message。

## 添加新平台

`ctfplatform.Platform` 定义 `Descriptor / ListChallenges / Deliver / SubmitFlag`。认证由平台构造器及适配器内部处理，配置只存在于内存。`Delivery` 可表达远程实例地址、附件 URL / 本地文件和平台特定 metadata，`SubmissionMode` 表达 API 或结果文件等提交机制。`HintProvider`、`InstanceCloser`、`SubmissionValidator` 是可选接口。

新增适配器时实现这些接口，通过 `Registry.Register` 注册工厂，在服务入口提供配置。项目和台账代码不包含 TSec 路径或认证逻辑。具体协议差异在适配器中转换为统一模型，专有信息可以放进 metadata。注册机制不会自动为尚未实现的平台提供接入能力。

TSec 实现位于 `backend/internal/ctfplatform/tsec`；HTTP / 项目桥接位于 `backend/internal/httpapi/platform.go`；CLI 位于 `backend/internal/cli/platform.go`。现有 CTFy 运行入口可继续独立使用。

验证：

```bash
cd backend
go test ./...
```

测试使用本地模拟 TSec 服务验证五个接口的请求协议、错误码、Unicode 长度、重定向与超时、多地址 / 多 flag、任务隔离、项目事务与重启持久化、flag 台账状态及 CLI 流程。比赛测试还验证 24 道题连续处理、成功与失败接续、动态并发、关闭等待、每题超时、规则传递、重启恢复和停止。真实平台连通性需使用下发的配置和 VPN 验证。
