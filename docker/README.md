# Docker 镜像

镜像包含 Go/SQLite 后端、构建后的 React 前端、Pi 和 pi-optchat、13 个技能、pentest_toolkit 和完整 Python 依赖。另保留 CTFy 专用 Agent 使用的 Codex/Claude Code CLI。运行时无需 Go 编译器或前端源码。需要 Docker Engine、BuildKit 和 Docker Compose v2；构建需下载基础镜像、Go/npm/Python 依赖和 Debian 软件包。

## 构建和启动

在项目根目录执行：

```bash
./docker/build.sh
docker compose -f docker/compose.yaml up -d --no-build
```

打开 **http://127.0.0.1:9981** 注册或登录。后端仅在容器内监听 `127.0.0.1:9982`，前端同源代理 API。默认开启认证和自动调度；工作台镜像的 Dispatcher 和执行 Worker 固定使用原生 Pi，可通过下面的环境变量完成模型和运行限制配置，也可以在 Worker 页面查看和修改模型设置。

也可以直接构建并启动：

```bash
docker compose -f docker/compose.yaml up -d --build
```

端口已被本机开发服务占用时，可改为：

```bash
HOTMOUSE_PORT=19981 docker compose -f docker/compose.yaml up -d
```

查看状态、日志和停止服务：

```bash
docker compose -f docker/compose.yaml ps
docker compose -f docker/compose.yaml logs -f
docker compose -f docker/compose.yaml down
```

`down` 保留数据卷。前端或后端退出时容器会停止；Compose 按 `unless-stopped` 重启服务。容器收到停止信号时，会停止后端、前端和它们的子进程，并等待后端关闭数据库。健康检查通过前端入口检查后端。

## 启动环境变量

在项目根目录复制示例，填写 LLM 地址、密钥和模型：

```bash
cp docker/.env.example docker/.env
chmod 600 docker/.env
# 编辑 docker/.env 后启动。
docker compose --env-file docker/.env -f docker/compose.yaml up -d --build
```

修改变量后再次运行 `up -d`，Compose 会重建配置发生变化的容器；仅执行 `restart` 不会重新读取 `.env`。这些设置在数据库迁移后、Worker 池启动前一次性应用，也适用于已有数据卷。显式提供的环境变量覆盖保存的对应字段；空值保留已保存配置，首次使用时采用下表默认值。非法参数会使服务启动失败，错误指出变量名，凭据沿用日志脱敏规则。

| 环境变量 | 默认值 / 范围 | 用途 |
| --- | --- | --- |
| `HOTMOUSE_LLM_BASE_URL` | 无 | LLM API 地址，例如 `http://host.docker.internal:8000/v1` |
| `HOTMOUSE_LLM_API_KEY` | 无 | LLM API 密钥 |
| `HOTMOUSE_LLM_MODEL` | 无 | 模型名称 |
| `HOTMOUSE_LLM_API` | `openai` | `openai` 使用 Chat Completions；也支持 `openai-responses`、`anthropic-messages` |
| `HOTMOUSE_WORKER_COUNT` | 4；1..32 | 全局执行 Worker 池大小，重启服务生效 |
| `HOTMOUSE_PROJECT_CONCURRENCY` | Compose 为 1；0..32 | 自动调度同时处理的题目/项目数，0 为不限 |
| `HOTMOUSE_PROJECT_MAX_WORKERS` | 1；1..32 | 新项目每题的执行 Worker 上限 |
| `HOTMOUSE_PROJECT_BUDGET_SECONDS` | 1200；≥0 | 新项目的共享执行时间预算，0 为不限 |
| `HOTMOUSE_DECIDE_TIMEOUT_SECONDS` | 300；1..86400 | 规划及启动目标分析的单次最长时间 |
| `HOTMOUSE_EXECUTE_TIMEOUT_SECONDS` | 300；1..86400 | 每次执行 Step 的最长时间 |
| `HOTMOUSE_CONCLUDE_TIMEOUT_SECONDS` | 90；1..3600 | 恢复总结的单次最长时间 |
| `HOTMOUSE_CONCLUDE_RESERVE_SECONDS` | 0；0..120 | 从执行时限中预留给总结的时间 |
| `HOTMOUSE_RECOVERY_ATTEMPTS` | 3；1..10 | 每个中断执行会话的最多恢复总结尝试数 |
| `HOTMOUSE_FORMAT_RETRIES` | 1；0..1 | 中断恢复总结的结构化输出格式修复重试数 |
| `HOTMOUSE_BASH_TIMEOUT_SECONDS` | 90；1..86400 | Pi 前台 bash 工具的单次时间上限 |
| `HOTMOUSE_STEP_TIMEOUT_SECONDS` | 15；≥5 | Step 心跳租约超时 |
| `HOTMOUSE_DECIDE_LEASE_TIMEOUT_SECONDS` | 15；≥5 | Dispatcher 规划心跳租约超时 |
| `HOTMOUSE_WORKER_UNHEALTHY_RETRY_AFTER_SECONDS` | 5；1..3600 | Worker 不可用后的重试间隔 |
| `HOTMOUSE_WORKER_REJECTED_RETRY_AFTER_SECONDS` | 5；1..3600 | 模型拒绝任务后的重试间隔 |
| `PI_MODEL_CONTEXT_WINDOW` | 模型默认；正整数 | 自定义模型的上下文窗口大小 |

`HOTMOUSE_LLM_*` 会应用到执行 Worker 和已配置的独立 Dispatcher。也支持 `PI_BASE_URL`、`PI_API_KEY`、`PI_MODEL`、`PI_PROVIDER_API`，同一字段优先使用 `HOTMOUSE_LLM_*`。开始使用环境变量配置 LLM 时，地址、密钥和模型必须完整；已有配置允许只覆盖其中一个字段。未提供这些变量时，工作台仍可启动并通过 Worker 页面配置模型。

例如 `.env.example` 中的 6 个 Worker、2 道题并发、每题最多 3 个 Worker，允许最多 6 个执行 Worker 同时工作。Dispatcher、启动分析和 OptChat 内存进程独立运行。超出题目并发上限的项目保留 active 状态并排队；已有项目结束后补上空出的名额。手动调用 Step 执行接口仍属于显式执行。

项目预算累计所有 Worker 和路线的执行耗时，多个 Worker 会共同消耗同一份预算。预算和每题 Worker 上限在创建时保存；改变默认值影响后续新项目，已有项目可在工作台或 CLI 中单独调整。比赛导入的新项目同样使用系统预算。`HOTMOUSE_COMPETITION_CHALLENGE_TIMEOUT` 则是每题准备及解题的总时限，支持 `20m`、`1h` 等 duration，与项目执行预算分别计算。

Pi 和 pi-optchat 安装在 `/opt/hotmouse/pi`，已有 `/home/node` 数据卷不会遮住镜像中的安装包。镜像通过 `PI_OPTCHAT_EXTENSION` 显式加载共享内存模块；每个项目拥有一个持久化 profile，Worker 各自保留原生会话。`docker run -e PI_OPTCHAT_EXTENSION= ...` 可关闭共享内存；`OPTCHAT_HOME` 可指定 profile 存储目录。镜像内的 `HOTMOUSE_WORKER_DRIVER=pi` 固定驱动，API 也会拒绝切换到其它驱动或自定义执行命令。

直接使用 Docker 时也可读取同一文件：

```bash
docker run -d --name hotmouse --env-file docker/.env \
  -p 127.0.0.1:9981:9981 --add-host host.docker.internal:host-gateway \
  -v hotmouse-data:/data -v hotmouse-home:/home/node hotmouse:latest
```

## 构建选项

`build.sh` 可以从任意目录执行，额外参数原样传给 `docker build`：

```bash
# 单独构建平台镜像。
./docker/build.sh tsec   # hotmouse-tsec:latest，Dockerfile target: tsec
./docker/build.sh ctfy   # hotmouse-ctfy:latest，Dockerfile target: ctfy-agent

# 省略类型或指定 workbench，构建 hotmouse:latest。
./docker/build.sh workbench

HOTMOUSE_IMAGE=hotmouse:v1 ./docker/build.sh --no-cache
HOTMOUSE_IMAGE=hotmouse:v1 docker compose -f docker/compose.yaml up -d --no-build

# 跳过额外的 Codex/Claude Code CLI，仍安装工作台必需的 Pi 和 pi-optchat。
HOTMOUSE_IMAGE=hotmouse:pi ./docker/build.sh --build-arg AGENT_PACKAGES=

# 自定义 Pi 与 pi-optchat 版本，需保持 SDK 兼容。
./docker/build.sh --build-arg PI_PACKAGE=@earendil-works/pi-coding-agent@1.5.0 \
  --build-arg PI_OPTCHAT_PACKAGE=pi-optchat@0.11.12

# 导出镜像，便于离线服务器加载。
docker save hotmouse:latest -o hotmouse-image.tar
docker load -i hotmouse-image.tar
```

`HOTMOUSE_IMAGE` 对所有镜像类型生效，例如 `HOTMOUSE_IMAGE=hotmouse-tsec:v1 ./docker/build.sh tsec`。额外 Docker 参数仍会透传：`--target` 可覆盖默认构建阶段，`--tag` 可增加镜像标签。

TSec 镜像启动工作台，在运行时接收 `BENCHMARK_BASE_URL` 和 `BENCHMARK_TOKEN`，通过 `hotmouse platform import` 导入题目。使用 Compose 时设置 `HOTMOUSE_IMAGE=hotmouse-tsec:latest`；平台配置和 VPN 访问要求见 [CTF 平台接入](../docs/ctf_platforms.md)。CTFy 镜像启动专用 Agent，平台负责下发任务，运行方式见 [CTFY.md](CTFY.md)。

TSec 整场比赛可挂载规则文件，并设置 `HOTMOUSE_COMPETITION_RULES_FILE` 为容器内路径，容器启动后总调度会自动管理全部题目。比赛默认采用 `HOTMOUSE_PROJECT_CONCURRENCY` 和 `HOTMOUSE_PROJECT_MAX_WORKERS`；`HOTMOUSE_COMPETITION_CONCURRENCY`（TSec 为 1..3）和 `HOTMOUSE_COMPETITION_MAX_WORKERS` 可分别覆盖比赛配置。`HOTMOUSE_COMPETITION_CHALLENGE_TIMEOUT` 默认 `20m`。启动入口恢复已有比赛时，也会更新其并发、每题 Worker 上限和总时限。也可使用 `hotmouse competition start tsec --rules-file ... --concurrency 2`；LLM 可由启动环境配置，完整示例见平台接入文档。

| 构建参数 | 默认值 | 用途 |
| --- | --- | --- |
| `GO_IMAGE` | `golang:1.25-bookworm` | CGO 后端构建环境 |
| `GOPROXY` | `https://proxy.golang.org,direct` | Go 模块下载源，可指定内部镜像 |
| `NPM_REGISTRY` | `https://registry.npmjs.org` | 前端依赖及 Agent CLI 的 npm 下载源 |
| `NODE_IMAGE` | `node:24-bookworm-slim` | 前端构建和运行环境 |
| `PYTHON_IMAGE` | `python:3.13-slim-bookworm` | Python 解释器及工具依赖构建环境 |
| `PI_PACKAGE` | `@earendil-works/pi-coding-agent@1.5.0` | 必需的 Pi CLI 与 SDK |
| `PI_OPTCHAT_PACKAGE` | `pi-optchat@0.11.12` | 必需的 Pi 共享内存扩展，与 Pi 在同一目录安装 |
| `AGENT_PACKAGES` | Codex `0.167.0`、Claude Code `2.1.318` | 额外 CLI 的 npm 包，空值跳过这些额外包 |

覆盖基础镜像时，请使用 Debian Bookworm 系列，保证 CGO 后端与运行环境的系统库兼容。

`build.sh` 自动转发当前 shell 的 HTTP(S) 代理及 `NO_PROXY`，兼容大小写变量。代理仅监听宿主机回环地址时，Linux 构建可追加 `--network=host`；代理连接信息使用 Docker 的预定义构建参数，不保存为镜像环境变量。

从根目录直接构建的等价命令：`DOCKER_BUILDKIT=1 docker build -f docker/Dockerfile -t hotmouse:latest .`。`Dockerfile.dockerignore` 允许必要的源码、`backend/skills`、`backend/pentest_toolkit`、依赖清单、入口脚本和许可证进入构建上下文，排除本机数据库、Worker 产物、登录信息及 `.env` 文件。

工作台、TSec 与 CTFy 镜像都带有 `/usr/local/share/hotmouse/skills` 下的 13 个技能及参考文件，
以及 `/usr/local/share/hotmouse/pentest_toolkit` 下的共享脚本、字典与离线资料。
技能同时嵌入 Go 二进制，Worker 启动时导出到工作区，并将 `HOTMOUSE_SKILLS_DIR` 指向导出目录。
Agent 提示词会提供索引和绝对路径，专项技能按证据读取。
工具库只复制一次，Worker 在自己的工作区保存输出。需要换工具库时可只读挂载其它目录，
并通过 `HOTMOUSE_PENTEST_TOOLKIT_DIR` 指定容器内路径。

镜像按 [skill-tools.json](skill-tools.json) 预装 `backend/skills` 中 11 个技能声明和
示例使用的工具，共 41 个命令。工作台、TSec、CTFy 共用同一个 runtime 阶段。

| 技能 | 命令与 Python 库 |
| --- | --- |
| auth-attack | curl、ssh、nc、python3；Flask、itsdangerous |
| binary-pwn | python3、checksec、gdb、ROPgadget、file、strings、ltrace、strace；pwntools (`pwn`) |
| cloud-exploit | curl、python3、etcdctl |
| code-exec | curl、python3、node |
| detection-evaluation | curl、python3 |
| filter-bypass | curl、gzip、python3 |
| flag-hunting | curl、nmap、nc、ffuf、sqlmap、git-dumper、python3、gdb、ROPgadget、radare2、binwalk、7z、unsquashfs、etcdctl；pwntools |
| multi-stage | ssh、socat、curl |
| recon-fingerprint | curl、nmap、nc、rg |
| reverse-engine | file、strings、objdump、gdb、r2、binwalk、7z、unsquashfs、checksec、ltrace、strace、python3 |
| web-exploit | curl、ffuf、sqlmap、git-dumper |

共享入口包括 hotmouse、python、jq，以及 bash/sh、grep、head、sort、cut、cat、id、
echo、printf、base64、gzip、timeout。`pwntools` / `python3-pwntools` 是 Python 包名，
通过 `from pwn import ...` 使用；其 checksec 命令支持技能中的 `--file=...` 参数。

binwalk、etcd-client（提供 etcdctl）、ltrace、strace 由 Debian Bookworm 软件包安装。
Bookworm 没有 radare2 候选包，因此使用上游 6.2.6 的 amd64、arm64 或 i386 Debian 包，
下载后校验固定 SHA-256，提供 radare2 和 r2。binwalk 使用 `/usr/bin/python3`，
并配有 gzip、bzip2、cpio、tar、7z、unzip、unsquashfs 等提取工具。

`docker/toolkit-requirements.txt` 的 Python 工具在构建时安装到
`/opt/hotmouse/venv`，按提供的 `pip list` 固定全部 75 个包的版本，包含 androguard、
pwntools、scapy、gmpy2、fpylll、sympy、pycryptodome、capstone 和 unicorn。
另保留 PyJWT、Flask、git-dumper，并补充 fpylll 必需但未声明的 cysignals。
解释器为 Python 3.13（networkx 3.7 要求 Python ≥3.12），`HOTMOUSE_PYTHON` 指向此解释器，
venv 的 bin 目录加入 PATH，`python` 和 `python3` 使用同一环境。
原生扩展在单独构建阶段编译，运行镜像仅包含解释器、依赖和所需系统库。
构建时执行 `pip check`、全部固定版本检查，以及格约简、AES、反汇编和数据包读写检查。
最终阶段以默认 `node` 用户执行 [verify-toolkit.py](verify-toolkit.py)，核对技能清单、
PATH、python/python3 的 venv 一致性和命令启动；实际验证 Flask 会话签名、
checksec/ROPgadget、GDB 与 ltrace/strace 子进程调试、r2 补丁、SquashFS 和 binwalk gzip 解包，
以及 etcdctl 默认 v3 API 和 `get --prefix`。任何检查失败都会阻止镜像构建。

```bash
docker run --rm hotmouse:latest python -m pip list
docker run --rm hotmouse:latest python -m pip check
docker run --rm hotmouse:latest python /opt/hotmouse/verify-toolkit.py
docker run --rm hotmouse:latest ls /usr/local/share/hotmouse/skills
docker run --rm hotmouse:latest ls /usr/local/share/hotmouse/pentest_toolkit
```

nuclei 模板作为离线 YAML 资料提供，供本地检索。修改或增加专项技能时，同步更新
`skill-tools.json`；遗漏新技能会在构建时被检测到。宿主机运行仍以实际能力清单为准。

## 终端日志

Docker 镜像默认开启 `HOTMOUSE_LOG_LEVEL=debug` 和 `HOTMOUSE_LOG_ENV=1`，启动后直接向终端写日志。Go 服务、Dispatcher、Worker 和 CTFy Agent 的诊断写入 stderr，由 Docker 收集，无需挂载日志目录。

```bash
docker compose -f docker/compose.yaml logs --tail=200 -f
docker logs --tail=200 -f <容器名或ID>
```

启动日志包含时间、组件、进程 PID、UID/GID、主机名、工作目录、系统架构、Go 版本、监听地址、数据库路径、认证策略和 Worker 数量。运行时记录任务与项目标识、Agent 驱动和模型、模型网关、原生 CLI 的会话及工具事件、实时 stderr、HTTP 状态和耗时、退出码、取消/超时与输出限制。子进程每 30 秒输出一次运行心跳，包含耗时和已产生的输出字节数。

启动时只打印一次进程环境变量，项目、步骤及 Agent 子进程启动时不再重复打印。API Key、Token、密码、Cookie、认证信息以及 URL 中的账号密码和敏感查询值会脱敏；空变量单独标记为 `empty`，便于发现配置覆盖问题。任务提示词和图上下文仅记录长度，单条输出内容最多保留 4 KiB。

| 环境变量 | Docker 默认值 | 用途 |
| --- | --- | --- |
| `HOTMOUSE_LOG_LEVEL` | `debug` | `debug`、`info`、`warn`、`error`；debug 包含子进程 stdout 和成功 HTTP 请求 |
| `HOTMOUSE_LOG_ENV` | `1` | `0` 关闭环境变量快照；不影响其他日志 |

例如将工作台调整为 info 日志：

```bash
HOTMOUSE_LOG_LEVEL=info docker compose -f docker/compose.yaml up -d
```

直接运行镜像时使用 `-e HOTMOUSE_LOG_LEVEL=info` 或 `-e HOTMOUSE_LOG_ENV=0`。本地 `hotmouse serve` 默认 info，`--verbose` 开启 debug。排障完毕后可以调低日志级别。

## 数据和模型配置

容器以 UID/GID `1000:1000` 的非 root 用户运行。Compose 自动建立两个持久卷：

| 容器路径 | 内容 |
| --- | --- |
| `/data/hotmouse.db` | 项目、用户、模型配置与执行状态 |
| `/data/workspaces/hotmouse/` | Dispatcher/Worker 工作目录、结果与恢复证据 |
| `/data/context/` | 项目上下文 YAML，可设置 Worker 的 `context_root` |
| `/home/node/` | CLI 配置、登录凭据及会话 |

模型环境变量可以直接在 Worker 页面配置。需要 CLI 登录时使用：

```bash
docker compose -f docker/compose.yaml exec hotmouse codex login --device-auth
docker compose -f docker/compose.yaml exec hotmouse claude auth login
docker compose -f docker/compose.yaml exec hotmouse pi
```

安装 CLI 不会自动配置模型账户。CLI 凭据保存在 home 卷中，Dispatcher 也可以使用独立的模型环境配置。基础工具包含 Bash、curl、Git、Python 3/venv、OpenSSL、ripgrep、SQLite 和 unzip；其他 CTF 工具可通过派生 Dockerfile 安装。

容器中的 `127.0.0.1` 指向容器自身。访问宿主机提供的目标服务时使用 `host.docker.internal`；目标服务需监听 Docker 网络能访问的地址。题目附件和上下文中的文件路径也须使用容器内可见的路径。

不使用 Compose 时：

```bash
docker run -d --name hotmouse --stop-timeout 30 \
  -p 127.0.0.1:9981:9981 \
  -v hotmouse-data:/data \
  -v hotmouse-home:/home/node \
  --add-host host.docker.internal:host-gateway \
  hotmouse:latest
docker logs -f hotmouse
```

如改用宿主目录绑定挂载，先为数据目录和 home 目录赋予 UID/GID `1000:1000` 写权限。容器 CLI 可执行 `docker exec -it hotmouse hotmouse --help`，默认连接容器内的后端。
