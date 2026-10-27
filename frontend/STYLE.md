# 前端风格约束 (GitHub Style)

本目录的前端统一采用 **GitHub 风格**，来源是仓库根目录的 `github-showcase/` 风格包。
本文档是后续所有前端改动的硬性约束，配合 `npm run style:check` 一起生效。

## 快速开始

```sh
cd frontend
npm install
npm run dev        # 开发服务器，默认 http://localhost:8864
npm run style:check # 风格约束检查（CI 必须通过）
npm run build      # 风格检查 + 类型检查 + 生产构建
```

开发时一般直接在仓库根目录用 `start.sh` 同时拉起前后端：

```sh
./start.sh            # 后端 :8865，前端 :8864
./start.sh 9000       # 后端 :9000，前端 :8864
./start.sh 9000 3000  # 后端 :9000，前端 :3000
```

只单独跑前端时，用 `FRONTEND_PORT` 指定端口，用 `HOTMOUSE_API_URL` 指定后端地址：

```sh
FRONTEND_PORT=3000 HOTMOUSE_API_URL=http://127.0.0.1:9000 npm run dev
```

端口被占用时直接启动失败，不重试也不自动换端口。

## 风格来源与复用

| 来源文件 | 复用到 | 说明 |
| --- | --- | --- |
| `github-showcase/github-style.md` | 本文档 | 规则原文，冲突时以它为准 |
| `github-showcase/github-style-tokens.json` | `src/styles/github-style-tokens.json` | 设计令牌，机器可读 |
| `style-pack/github-style-variables.css` | `src/styles/github-style-variables.css` | 灰度与语义色变量 |
| `style-pack/github-style-globals.css` | `src/styles/github-style-globals.css` | 全局样式（已剔除违规工具类） |

`src/index.css` 用 Tailwind 4 的 `@theme` 把上述令牌映射成语义化工具类，
**组件一律使用语义类，不写具体色值**：

| 语义类 | 值 | 用途 |
| --- | --- | --- |
| `bg-canvas` / `bg-canvas-subtle` | `#ffffff` / `#f6f8fa` | 页面底色 / 次级区块 |
| `text-fg` / `text-fg-muted` / `text-fg-subtle` | `#1f2328` / `#656d76` / `#8b949e` | 正文 / 次要文字 / 弱化文字 |
| `border-border` | `#d0d7de` | 所有边框 |
| `bg-nav` | `#24292f` | 顶部导航 |
| `text-accent` | `#0969da` | 链接、主交互 |
| `text-success` / `text-warning` / `text-danger` / `text-info` | `#1f883d` / `#9a6700` / `#cf222e` / `#1d7f98` | 语义状态色 |

## 强制规则

### 必须

- 主交互色用 `#0969da`（`text-accent` / `bg-accent`）
- 背景只用 `#ffffff` 和 `#f6f8fa`
- 文字只用 `#1f2328` 和 `#656d76`
- 所有边框用 `border-border`
- 圆角统一 `rounded-md`（6px）
- 代码/等宽内容用 `font-mono` + `bg-canvas-subtle`
- 语义色：成功 `#1f883d`、警告 `#9a6700`、危险 `#cf222e`
- 焦点态必须有清晰的 ring，不能只改边框颜色
- 动效以状态表达为主，时长 `duration-150`，只用 `transition-colors`

### 禁止

- 渐变背景（`bg-gradient-*`）
- 重阴影（`shadow-lg` / `shadow-xl` / `shadow-2xl`）
- 非语义化的装饰色彩
- 衬线字体（`font-serif`）
- 过大的字号（`text-4xl` 及以上）
- 模糊效果（`blur-*`）
- 大圆角（`rounded-2xl` / `rounded-3xl`）；`rounded-full` 仅限徽章、头像、加载指示器
- 组件里硬编码色值（用语义类或 CSS 变量）

## 目录约定

```
src/
  components/   通用 UI 组件（Button/Card/Field/Badge/Alert/...）
  pages/        路由页面
  lib/          api.ts（后端调用）、types.ts（与 docs/API.md 对齐）、format.ts
  styles/       从 github-showcase 移植的样式文件
```

新增通用组件时优先复用 `src/components/` 里的基类，不要在页面里各写一套按钮/输入框样式。

## 自动检查

`npm run style:check` 扫描 `src/` 下的 `.ts` / `.tsx` / `.css`，命中以下任一情况即失败：

- 禁用类名（见上表 Forbidden Classes）
- 组件中硬编码十六进制色值
- 任意色值工具类（如 `bg-[#0969da]`）
- 未列入白名单的 `rounded-full`

新增例外时，改 `scripts/check-style.mjs` 里的白名单常量，不要删检查规则。

## 后端契约

接口以 `docs/API.md` 为唯一事实来源。前端只使用这 10 个路由：

| Method | Route |
| --- | --- |
| GET | `/project` |
| POST | `/project` |
| GET | `/project/{id}/goal` |
| POST | `/project/{id}/goal` |
| GET | `/project/{id}/fact` |
| POST | `/project/{id}/fact` |
| GET | `/project/{id}/step` |
| POST | `/project/{id}/step` |
| GET | `/settings` |
| PUT | `/settings` |

- 后端没有 CORS，前端开发时通过 Vite 代理把 `/api/*` 转发到后端（见 `vite.config.ts`）。
- 错误响应固定为 `{ "error": { "code": ..., "message": ... } }`，由 `src/lib/api.ts` 统一转成 `ApiError`。
- 项目完成后是终态，不能再追加事实或步骤；UI 需要禁用相关操作。
- 完成项目要求至少有一条指向目标的步骤，否则后端返回 `409 goal_unreachable`。
- `/settings` 是唯一的可修改资源，保存 LLM 的地址、密钥、模型和最大 token 数。
