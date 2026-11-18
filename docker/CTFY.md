# TPCTF 2026 自动参赛镜像

`ctfy` 镜像实现 2026-11-13 v1.0 参赛接口。容器启动即运行整场比赛，自动发现赛道、等待开赛、刷新题目、启动靶机、连接 WireGuard、调用模型解题、逐问题提交答案，并关闭靶机。无需启动工作台或手动创建项目。

## 构建与启动

在仓库根目录执行：

```bash
./docker/build.sh ctfy
cp docker/.ctfy.env.example docker/.ctfy.env
chmod 600 docker/.ctfy.env
```

编辑 `docker/.ctfy.env`，填写：

- `CTFY_TOKEN`：平台“AI 智能体——参赛”模板的 `agent_play` 令牌。
- `CTFY_COMPETITION_ID`：赛道编号。仅有一个已报名且未结束的比赛时可留空自动选择。
- `OPENAI_BASE_URL`、`OPENAI_API_KEY`：平台“本地 Agent 会话”生成的完整网关地址及 `pk_` 凭据。
- `CTFY_MODEL`：平台允许的模型 ID。

启动并查看日志：

```bash
docker compose -f docker/ctfy.compose.yaml up -d
docker compose -f docker/ctfy.compose.yaml logs -f
```

重新构建并启动可用 `docker compose -f docker/ctfy.compose.yaml up -d --build`。停止使用 `docker compose -f docker/ctfy.compose.yaml down`；持久卷保留实例记录、提交摘要和解题文件。切换比赛或平台时使用新的持久卷/工作目录，避免混用状态。

也可直接运行：

```bash
docker run --rm --name hotmouse-ctfy \
  --cap-add NET_ADMIN --device /dev/net/tun \
  --env-file docker/.ctfy.env \
  -e CTFY_MODE=competition -e CTFY_WORK_DIR=/data/ctfy \
  -v hotmouse-ctfy-data:/data/ctfy \
  hotmouse-ctfy:latest
```

镜像包含 `wireguard-tools`、`wireguard-go`、`iproute2`、`iptables`、DNS 配置工具和 `ncat`。自动 VPN 使用 root 和 `NET_ADMIN`，无需 `--privileged`、宿主网络或 Docker socket。宿主机需提供 `/dev/net/tun`；程序先尝试内核 WireGuard，失败后让 `wg-quick` 使用 `wireguard-go`。

如果 VPN 已在宿主机连接，可使用 `CTFY_VPN_MODE=external` 并配置能访问该网络的容器网络，例如 Linux 上 `--network host`。该模式不会自动建立 VPN，应确认返回的私网地址从容器可达。

## 模型网关

整场参赛使用镜像内的 Pi 原生工具，并复用 HotMouse 离线知识库与渗透工具。`HOTMOUSE_LLM_API=openai` 使用 Chat Completions，`openai-responses` 使用 Responses，`anthropic-messages` 使用 Anthropic Messages；后者改填 `ANTHROPIC_BASE_URL` / `ANTHROPIC_API_KEY`。也兼容 `HOTMOUSE_LLM_BASE_URL/API_KEY/MODEL` 和 `LLM_BASE_URL/API_KEY/MODEL`，优先级见 `.ctfy.env.example` 的显式设置。

`HOTMOUSE_LLM_REASONING_EFFORT` 可选 `low`、`medium`、`high`；Chat Completions 请求发送 `reasoning_effort`，Responses 请求发送 `reasoning.effort`。不配置或留空时保持原行为，不发送推理强度参数。

网关地址按原值传给模型客户端，程序不会自行追加 `/v1`，也不会回退到厂商地址。正式赛填写主办方指定的智谱模型，初选赛和正式赛均使用平台生成的会话网关。入围赛允许使用自有服务时，将相应服务地址与模型填入同一组变量。模型 ID 需自行提供，接口文档没有定义可自动发现模型的接口。

模型子进程不接收参赛/出题令牌。主进程负责实例管理、提交与上传。模型配置仅保存 `LLM_API_KEY` 变量引用；凭据不会写入镜像或比赛状态。日志默认 `info`，凭据脱敏；`HOTMOUSE_LOG_LEVEL=debug` 可显示更多诊断。

## 调度、恢复与提交

默认串行运行一道题，兼容入围赛一实例限制，并避免多个 WireGuard 配置包含重叠私网路由。完成或超时后先关闭实例，确认回收后启动下一题。每次扫描优先恢复已记录的实例，然后按难度尝试其他题目。比赛仍在进行时持续发现新题。

每轮解题读取题面、附件、服务入口及已解锁问题；模型写 `answers.json` 或返回 `{"answers":{"question_id":"答案"}}`，多选答案保留为字符串数组。只有结构化答案会提交。主进程检查问题是否解锁、是否已答对、剩余错误次数，并在提交前保存摘要。正确后重新读取题目与实例，继续解锁后续问题。相同答案不会重复试交；网络中断导致判定未知时也不盲目重发。当前提交判定保存在题目目录的 `last-verdict.json`，供模型修正。

可调整的秒数：`CTFY_POLL_SECONDS=15`、`CTFY_RETRY_SECONDS=120`、`CTFY_START_TIMEOUT_SECONDS=600`、`CTFY_CHALLENGE_TIMEOUT_SECONDS=1200`。平台返回的较长 `Retry-After` / `retry_after_s` 会被尊重；鉴权、报名和权限错误会明确退出。达到题目错误次数上限后不继续提交。已解题、本队出题和已关闭题目自动跳过。比赛结束后清理本进程记录的靶机并退出。

工作目录使用文件锁，防止两个进程同时操作同一份状态。平台的 `GET /api/v1/instances?competition_id=...` 可列出当前令牌可见的实例，HotMouse 工作台会据此接管已启动的靶机。独立参赛容器仍优先恢复自己保存的实例编号；如果其他程序已启动靶机，但列表为空且 `instance_already_running` 错误没有实例编号，请核对令牌所属队伍和比赛，或先在网页释放该实例。本容器启动的实例编号会持久化，重启后继续恢复。

## 可选自动出题

智能体团队赛道可在 `.ctfy.env` 设置：

```dotenv
CTFY_AUTHOR_ENABLED=1
CTFY_AUTHOR_TOKEN=pf_替换为agent_author令牌
CTFY_AUTHOR_CATEGORY=web
CTFY_AUTHOR_STACK=python-flask
CTFY_AUTHOR_LICENSE=platform-only
CTFY_TERMS_VERSION=替换为赛事要求的实际协议版本
```

仅在有出题时段、名额和验题次数时运行。程序读取最新主题、下载官方模板，让模型生成 `challenge/`，检查必需文件并打包上传。后续扫描读取静态检查、审核和 smoke 结果；根据反馈修复，同一 slug 重传，已审核通过的题目使用修订接口。每个主题/时段最多尝试上传三次，并遵守服务端剩余次数。主题、题包、反馈保存在 `authoring/`。本地不会挂载 Docker socket 构建靶机，可解性以平台自动构建和参考解验题为准。

令牌权限与贡献者协议版本必须由参赛者配置；文档未提供获取协议版本的接口。默认关闭自动出题，启用后上传会消耗实际配额。

## 保留的 CTFY-0001 托管模式

原先由平台下发 `task.json`、`README.md` 和已有靶机的 schema v3 模式仍可单独构建：

```bash
./docker/build.sh ctfy-hosted
```

镜像为 `hotmouse-ctfy-hosted:latest`，以 node 用户运行，不需要 `NET_ADMIN`，无镜像 VOLUME。也可将参赛镜像的 `CTFY_MODE` 设为 `hosted`；未显式配置模式且当前工作目录存在 `task.json` / 设置了 `CTFY_EVAL_DRY_RUN=1` 时自动识别托管模式，并切换为 node 用户。

托管模式读取平台指定的 `LLM_FLAVOR`、模型凭据与任务目录，使用 Codex / Claude Code，写 `result.json`。它与整场参赛模式的运行配置各自独立。

本地检查托管模式：

```bash
mkdir -p /tmp/ctfy-smoke
chmod 777 /tmp/ctfy-smoke
printf '%s\n' '{"schema_version":3,"dry_run":true}' > /tmp/ctfy-smoke/task.json
docker run --rm -v /tmp/ctfy-smoke:/ctfy/work -w /ctfy/work \
  -e CTFY_WORK_DIR=/ctfy/work -e CTFY_EVAL_DRY_RUN=1 hotmouse-ctfy:latest
cat /tmp/ctfy-smoke/result.json
```

## 验证

`go -C backend test ./...` 运行后端测试；TPCTF 模拟平台覆盖开赛等待、多问题与多选提交、回收后启动下一题、恢复已知实例、判定未知不重发、次数耗尽不提交、下载鉴权及退避。真实参赛还需要有效报名、令牌、模型网关和实际 VPN 接入信息。
