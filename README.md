# HotMouse

以事实、目标和步骤组织安全评估的本地工作台。Go / Iris / SQLite 后端提供 HTTP API、完整 CLI、项目级 Dispatcher 和多 Agent Worker；React 前端提供项目图与管理界面。

一条命令启动前端和后端：

```bash
./start.sh
```

脚本会检查端口、按需安装前端依赖、编译后端，默认启用自动调度并启动 Vite。前端默认监听 **http://127.0.0.1:9981**，后端默认监听 **http://127.0.0.1:9982**。前端同源代理后端的 `/api`、`/projects` 和 `/healthz`。按 Ctrl+C 停止前后端。需要 Bash 4.3+、Go/CGO 与 C 编译器、Node.js、npm、curl 和 `setsid`（Linux 的 util-linux）。日志位于 `.hotmouse/run/`，数据库默认保存至 `backend/data/hotmouse.db`。

运行构建后的前端：

```bash
./start.sh --production
```

生产模式也默认使用前端 **9981**、后端 **9982**。自定义监听端口或关闭自动调度：

```bash
HOTMOUSE_BACKEND_PORT=9983 HOTMOUSE_FRONTEND_PORT=9984 HOTMOUSE_AUTORUN=0 ./start.sh
```

其他配置见 `./start.sh --help`。脚本可从任意目录运行，相对数据库与日志路径以项目根目录为基准。

也可以分别启动后端：

```bash
cd backend
go build -o hotmouse ./cmd/hotmouse
./hotmouse serve --db ./data/hotmouse.db --autorun
```

另开终端启动前端：

```bash
cd frontend
npm ci
npm run dev
```

打开 **http://127.0.0.1:5173** 注册或登录。后端默认监听 `127.0.0.1:8080`。

- [前端使用、构建与验证](frontend/README.md)
- [Docker 镜像构建与启动](docker/README.md)
- [后端与 Worker 配置](backend/README.md)
- [完整命令行接口](docs/cli.md)
- [HTTP 接口范围](docs/routes.md)
- [紧凑上下文、路线调度与经验台账](docs/strategy.md)
- [内置 CTF 知识库与 sapper-v2 技能迁移](docs/knowledge.md)

前端构建后可通过 `cd frontend && npm run build && npm start` 在 `127.0.0.1:4173` 运行，并同源代理后端 API。
