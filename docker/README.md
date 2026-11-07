# Docker 镜像

镜像包含 Go/SQLite 后端、构建后的 React 前端，以及 Codex、Claude Code、Pi CLI。运行时无需 Go 编译器或前端源码。需要 Docker Engine、BuildKit 和 Docker Compose v2；构建需下载基础镜像、Go/npm 依赖和 Debian 软件包。

## 构建和启动

在项目根目录执行：

```bash
./docker/build.sh
docker compose -f docker/compose.yaml up -d --no-build
```

打开 **http://127.0.0.1:9981** 注册或登录。后端仅在容器内监听 `127.0.0.1:9982`，前端同源代理 API。默认开启认证和自动调度；在 Worker 页面分别配置 Dispatcher 和执行 Worker 的驱动、模型及环境变量。

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

## 构建选项

`build.sh` 可以从任意目录执行，额外参数原样传给 `docker build`：

```bash
HOTMOUSE_IMAGE=hotmouse:v1 ./docker/build.sh --no-cache
HOTMOUSE_IMAGE=hotmouse:v1 docker compose -f docker/compose.yaml up -d --no-build

# 只构建工作台，跳过所有 Agent CLI。
HOTMOUSE_IMAGE=hotmouse:core ./docker/build.sh --build-arg AGENT_PACKAGES=

# 自定义 CLI 包及版本。
./docker/build.sh --build-arg 'AGENT_PACKAGES=@mariozechner/pi-coding-agent@0.73.1'

# 导出镜像，便于离线服务器加载。
docker save hotmouse:latest -o hotmouse-image.tar
docker load -i hotmouse-image.tar
```

| 构建参数 | 默认值 | 用途 |
| --- | --- | --- |
| `GO_IMAGE` | `golang:1.25-bookworm` | CGO 后端构建环境 |
| `GOPROXY` | `https://proxy.golang.org,direct` | Go 模块下载源，可指定内部镜像 |
| `NPM_REGISTRY` | `https://registry.npmjs.org` | 前端依赖及 Agent CLI 的 npm 下载源 |
| `NODE_IMAGE` | `node:24-bookworm-slim` | 前端构建和运行环境 |
| `AGENT_PACKAGES` | Codex `0.167.0`、Claude Code `2.1.318`、Pi `0.73.1` | 空格分隔的 npm 包，空值跳过安装 |

覆盖基础镜像时，请使用 Debian Bookworm 系列，保证 CGO 后端与运行环境的系统库兼容。

`build.sh` 自动转发当前 shell 的 HTTP(S) 代理及 `NO_PROXY`，兼容大小写变量。代理仅监听宿主机回环地址时，Linux 构建可追加 `--network=host`；代理连接信息使用 Docker 的预定义构建参数，不保存为镜像环境变量。

从根目录直接构建的等价命令：`DOCKER_BUILDKIT=1 docker build -f docker/Dockerfile -t hotmouse:latest .`。`Dockerfile.dockerignore` 仅允许必要的源码、依赖清单、入口脚本和许可证进入构建上下文，排除本机数据库、Worker 产物、登录信息及 `.env` 文件。

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
