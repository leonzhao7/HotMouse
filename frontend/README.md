# HotMouse 前端

React + Vite 本地工作台。借鉴 Rabbit 的导航、主题和事实图投影，界面操作接入 HotMouse 现有 HTTP API。

需要 Node.js 20.19+（建议 Node.js 22.12+ 或 24）以及运行中的 HotMouse 后端。

## 开发启动

在项目根目录运行 `./start.sh` 可以同时启动前后端，自动安装缺失或变更的依赖并启用后端自动调度。`./start.sh --production` 构建并运行正式界面。两种模式下，前端默认监听 `http://127.0.0.1:9981`，后端默认监听 `http://127.0.0.1:9982`，前端已配置后端 API 同源代理。端口、数据库和日志配置见根目录 [启动说明](../README.md) 与 `./start.sh --help`。

在一个终端启动后端：

```bash
cd /home/kali/workspace/HotMouse/backend
go build -o hotmouse ./cmd/hotmouse
./hotmouse serve --db ./data/hotmouse.db --autorun
```

在另一个终端启动前端：

```bash
cd /home/kali/workspace/HotMouse/frontend
npm ci
npm run dev
```

打开 **http://127.0.0.1:5173**，在登录页面注册或登录本地账户。前端通过同源代理请求后端，HttpOnly 会话 cookie 由服务管理。CLI 的本地凭据存储与浏览器会话分别管理。

默认后端地址为 `http://127.0.0.1:8080`。修改后端端口时，可在 `frontend/.env.local` 中设置：

```dotenv
HOTMOUSE_BACKEND_URL=http://127.0.0.1:8081
```

重启 Vite 后生效。Vite 同时代理 `/api`、`/projects` 和 `/healthz`，核心图接口保留 `/projects` 路径。

服务以 `--auth disabled` 启动时，界面会依据服务健康中的认证策略进入本地开发模式；无法连接后端会显示连接错误，登录过期会返回登录页。

## 构建与运行

```bash
npm run build
npm start
```

打开 **http://127.0.0.1:4173**。`server.mjs` 使用 Node.js 原生 HTTP 模块提供 `dist/` 并代理 API，适合本地运行。后端本身不托管前端静态文件。

运行服务器可用以下环境变量：

| 变量 | 默认值 | 作用 |
| --- | --- | --- |
| `HOST` | `127.0.0.1` | 前端监听地址 |
| `PORT` | `4173` | 前端监听端口 |
| `HOTMOUSE_BACKEND_URL` | `http://127.0.0.1:8080` | 后端 HTTP(S) origin |

```bash
HOTMOUSE_BACKEND_URL=http://127.0.0.1:8081 PORT=4174 npm start
```

`npm start` 读取进程环境变量；`.env.local` 由 Vite 开发及预览命令读取。也可以将 `dist/` 交给已有 Web 服务器，保持上述 API 路径的同源反向代理。页面使用 hash 路由。

## 界面功能

- 总览：项目进展、最近七天漏洞发现、已确认风险分布、Worker 状态、最近审计和服务健康。
- 项目：按状态和关键词筛选、自定义或从模板创建、删除、重命名、停止、恢复、重新打开、YAML 与原始时间线下载。
- 工作区：可缩放关系图与节点详情，目标 / 步骤 / 事实 / 发现 / 提示 / 时间线 / 项目综述视图。支持子目标、优先级调整、依据事实选择、目标完成证据、手动租约管理、Worker 执行和结果记录。步骤列表显示执行阶段的 LLM 原始返回，可展开完整原文，并保留校验失败的回答和具体错误；恢复总结保留在步骤结果中。未保存原始返回的旧步骤会明确提示。
- 漏洞：项目 / 严重性 / 复核状态筛选、证据事实与来源步骤详情、单条和批量确认 / 忽略、同步发现。
- Worker：单本地 Worker 状态、最近 20 条任务、命令 / 驱动 / 目录 / 运行参数 / 环境变量配置、启停及已保存配置测试。敏感值 `***` 原样保存，删除环境变量行会发送显式移除键。
- 模板：内置和自定义模板、复制为新模板、从模板建项目及删除自定义模板。
- 设置、健康、历史记录清理、审计搜索、通知已读与清空、修改密码、全局搜索、暗色 / 浅色主题和手机导航。

自动规划和执行需要后端启动时带有 `--autorun`。启用 Worker 配置仅改变可用状态。Worker 测试检查本地命令或 CLI 可用性，command 驱动会执行 healthcheck；不验证模型凭据或模型服务连接。

只展示当前后端支持的功能：报告生成、附件与截图上传、报告模板导出、容器部署和多个 Worker 调度不在当前接口范围内。模板没有更新接口，修改内容后可复制成新模板。

## 验证

```bash
npm test
npm run build
npm run test:e2e
```

单元测试检查事实图投影、状态推导、请求与错误处理、配置保留、敏感值与环境键移除。端到端测试自动编译后端，使用临时数据库在 `18080` 启动服务，并在 `15173` 启动 Vite，验证注册、会话、主题、移动导航、项目证据流程、漏洞复核、模板、Worker 执行与配置、设置和通知。

端到端测试需要 Go、CGO 和 C 编译器，以及 Chromium。Linux 会优先使用 `/usr/bin/chromium`；也可设置 `PLAYWRIGHT_CHROMIUM_EXECUTABLE`，或运行 `npx playwright install chromium`。测试数据库和工作区会在退出时删除，截图与失败追踪保存在已忽略的 `test-results/` 中。

```bash
npm run format
```
