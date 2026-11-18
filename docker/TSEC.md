# TSec 无人值守参赛镜像

`hotmouse-tsec:latest` 启动后直接运行 `hotmouse competition run tsec`，自动获取题目、恢复或启动靶机、调用 HotMouse Dispatcher 和多 Worker 解题、提交所有 flag、关闭靶机并开始下一题。无需启动工作台、登录、上传规则或在比赛过程中输入任何信息。技能、离线资料、原生 Pi 和 pi-optchat 共享记忆与工作台相同。

接口协议来自 [docs/tsec-api.md](../docs/tsec-api.md)。本文的运行环境已经接入平台 VPN；容器通过宿主网络使用现有 VPN 路由和本地模型网关。API 没有 VPN 配置下载或认证接口，因此 VPN 由运行环境在启动容器前提供。

## 配置和启动

在项目根目录执行：

```bash
./docker/build.sh tsec
cp docker/.tsec.env.example docker/.tsec.env
chmod 600 docker/.tsec.env
```

启动前在 `.tsec.env` 填写平台下发的 `BENCHMARK_BASE_URL`、`BENCHMARK_TOKEN`，以及 `HOTMOUSE_LLM_BASE_URL`、`HOTMOUSE_LLM_API_KEY`、`HOTMOUSE_LLM_MODEL`。`HOTMOUSE_LLM_API` 默认 `openai`，也支持 `openai-responses` 和 `anthropic-messages`。模型地址按原值传递，不自动追加 `/v1`。缺失必需配置时程序直接记录错误并退出，不会请求人工输入或使用 mock 解题。

可选设置 `HOTMOUSE_LLM_REASONING_EFFORT=low`、`medium` 或 `high`。Chat Completions 请求会携带对应的 `reasoning_effort`；不配置或留空时不发送。该变量不写入数据库，移除后重新启动即可恢复默认行为。

Docker 启动时可用 `docker run ... -e HOTMOUSE_LLM_REASONING_EFFORT=high hotmouse-tsec:latest` 覆盖文件配置。Compose 可用 `HOTMOUSE_LLM_REASONING_EFFORT=high docker compose --env-file docker/.tsec.env -f docker/tsec.compose.yaml up -d --no-build`，启动环境中的值优先于 `.tsec.env`。

```bash
docker compose --env-file docker/.tsec.env -f docker/tsec.compose.yaml up -d --no-build
docker compose --env-file docker/.tsec.env -f docker/tsec.compose.yaml logs -f
```

`.tsec.env.example` 中的全部 21 个变量都支持启动时覆盖，优先级为：启动环境变量 > `--env-file` 指定文件的值 > 镜像默认值。Compose 必须用 `--env-file` 加载 `.tsec.env`，才能同时保留文件配置并让启动环境变量覆盖。修改配置后重新执行 `up -d`；`restart` 不会重新读取配置。`0` 原样传递，可将项目预算设为不限；显式空值也会传入容器，可用于清空可选的网络检查地址。必需配置为空时仍会校验失败。

例如保留文件中的平台和模型配置，只在本次启动调整 Worker 池大小：

```bash
HOTMOUSE_WORKER_COUNT=8 \
  docker compose --env-file docker/.tsec.env -f docker/tsec.compose.yaml up -d --no-build
```

自定义配置文件使用 `--env-file /path/to/task.env`。全部配置由运行环境注入时，Compose 可使用 `--env-file /dev/null` 启动，无需创建 `.tsec.env`；平台地址、任务令牌、模型密钥和模型名称仍须在启动环境中提供。

Compose 的 `network_mode: host` 用于 Linux 宿主机，共享运行环境已经建立的 VPN 网络。无需映射前端端口、挂载 Docker socket 或赋予网络管理权限。可选填写 `TSEC_NETWORK_CHECK_URL` 为 VPN 内的 HTTP(S) 健康检查地址；程序在拉题前自动等待该地址可达。平台凭据不会发送到健康检查地址。

TSec 默认 `TSEC_TARGET_PROBE_ENABLED=0`，关闭自动靶机探活，包括后台 LLM 目标识别、周期 TCP 测活和关联的首页预处理。解题 Worker 直接使用题目提供的地址和已有证据获取内容；保留数据卷恢复原比赛时也应用此开关。设置为 `1` 可启用工作台的原有流程：TCP 测活每 20 秒一个窗口，任意必需端点连续 3 个窗口失败就终止项目，比赛调度随后关闭靶机并重试。`TSEC_RETRY_INTERVAL` 只控制启动阶段的网络检查、拉题等重试间隔，靶机探活的窗口与失败阈值独立于这个参数。

切换已有容器的探活设置时，使用新镜像并重新创建容器，例如：

```bash
TSEC_TARGET_PROBE_ENABLED=0 \
  docker compose --env-file docker/.tsec.env -f docker/tsec.compose.yaml up -d --no-build --force-recreate
```

也可直接运行：

```bash
docker run --rm --network host \
  --env-file docker/.tsec.env \
  -v hotmouse-tsec-data:/data \
  -v hotmouse-tsec-home:/home/node \
  hotmouse-tsec:latest
```

直接运行镜像时，`--env KEY=value` 或 `-e KEY=value` 覆盖 `--env-file` 中的同名值，例如在镜像名前加入 `-e HOTMOUSE_WORKER_COUNT=8 -e HOTMOUSE_PROJECT_BUDGET_SECONDS=1200`。使用 `-e KEY` 则读取宿主机已导出的同名变量。TSec 镜像内置与示例文件一致的运行默认值，平台凭据、模型密钥和模型名称没有内置值。

使用专属数据卷保存本次任务。切换跑分任务时使用新数据卷，避免另一个活跃任务的项目占用 Worker 池。重启相同任务时保留原卷，继续已有实例、证据、共享记忆和答案记录。

## 自动运行和恢复

- 启动时自动读取题目列表。网络错误、资源暂时不可用和空列表会等待后重试；无效任务令牌或任务已结束时明确退出。
- 优先恢复平台已启动的实例，再按难度和关卡排序。遵守 TSec 最多 3 道活跃题目的限制，持续刷新列表并加入新发现的题目。
- 每题使用原有 Dispatcher / Worker 调度、专项技能和持久化共享记忆。默认规则要求立即保存原始输出及 flag checkpoint，不等待完整报告。
- 主进程提交保存的所有 flag，并根据 `correct_flag_count`、`flag_count` 和 `is_completed` 确认整题通过。多 flag 题不会在第一个 flag 通过后提前结束。
- 错误 flag 不重复提交。结果未知时先检查平台进度，题目尚未完成则间隔 5 秒补交；TSec 的 `duplicate` 响应视为已接受，原未知记录保留。重启后未保存判定的 pending 尝试自动恢复为 unknown。
- 解题失败或超时后取消 Worker，等待实例关闭，再处理下一题。失败题在一轮结束后自动重新排队，复用已有证据并刷新实例地址和解题预算。默认不调用会扣分的 hint 接口。
- 全部题目通过后自动退出；平台任务终止或认证失效时结束运行。收到停止信号后尽力取消 Worker 并关闭本任务的活跃靶机，无需人工清理正常停止的实例。

## 运行参数和结果

| 变量 | 默认值 | 用途 |
| --- | --- | --- |
| `HOTMOUSE_COMPETITION_CONCURRENCY` | `3`，限制为 1..3 | 同时解答的题目数 |
| `HOTMOUSE_COMPETITION_MAX_WORKERS` | `3` | 每题的执行并发 |
| `HOTMOUSE_COMPETITION_CHALLENGE_TIMEOUT` | `30m` | 每题准备、解题和提交的总时限 |
| `HOTMOUSE_WORKER_COUNT` | `16` | 全局 Worker 池大小 |
| `HOTMOUSE_PROJECT_CONCURRENCY` | `3`，`0` 为不限 | Worker 池同时调度的项目数 |
| `HOTMOUSE_PROJECT_MAX_WORKERS` | `3` | 新项目的默认 Worker 上限 |
| `HOTMOUSE_PROJECT_BUDGET_SECONDS` | `0`（不限） | 每题所有 Worker 累计共享的执行预算 |
| `TSEC_RETRY_INTERVAL` | `8s` | 启动失败或网络未就绪后的重试间隔 |
| `TSEC_SHUTDOWN_TIMEOUT` | `30s` | 停止和实例回收等待时间 |
| `TSEC_NETWORK_CHECK_URL` | 空 | 可选 VPN 健康检查地址 |
| `TSEC_TARGET_PROBE_ENABLED` | `0` | `0` 关闭自动靶机探活及关联的后台启动流程；`1` 启用 |
| `TSEC_VPN_MODE` | `external` | 使用运行环境已经接入的 VPN；其它值校验失败 |
| `TSEC_RESULT_FILE` | `/data/tsec-result.json` | 最终比赛状态、队列和项目关联 |
| `HOTMOUSE_COMPETITION_RULES_FILE` | 空 | 可选附加赛事规则文件 |
| `HOTMOUSE_LOG_LEVEL` | `info` | 日志级别 |
| `HOTMOUSE_LOG_ENV` | `0` | 是否记录经过脱敏的启动环境快照 |

表中为 TSec 镜像默认值。本地 CLI 未设置题目并发或每题 Worker 上限时读取系统配置，未设置结果路径时保存到数据库旁。

比赛状态保存在 `/data/hotmouse.db`；工作区和共享记忆保存在数据卷中。默认结果为 `/data/tsec-result.json`，通过临时文件原子写入。保留卷重启时已完成的比赛不会重新解题。

本地也可运行同一入口：

```bash
go -C backend build -o ../hotmouse ./cmd/hotmouse
./hotmouse competition run tsec --db ./data/tsec.db \
  --concurrency 2 --max-workers 2 --challenge-timeout 20m
```

后端验证使用 `go -C backend test ./...`，覆盖无服务、无规则、无输入启动，多 flag 提交，网络和平台启动重试，未知提交恢复，实例回收和完成后的重启。真实评测需使用平台下发的任务及运行环境 VPN。

构建完成后，可使用本地模拟平台和模型网关验证默认容器入口。该检查运行镜像中的原生 Pi，实际读取测试靶机、提交两个 flag、确认实例回收和结果文件：

```bash
python3 docker/smoke-tsec.py
python3 docker/smoke-tsec.py --target-probe-enabled 1
```
