# CLAUDE.md

HotMouse 是一个 Go 决策支持 API（Iris + GORM + SQLite），前端位于 `frontend/`。

## 目录

- `backend/` — Go 服务，只提供 `/project*` 八个路由，不提供静态资源
- `docs/API.md` — **接口的唯一事实来源**，改后端必须同步改这里
- `frontend/` — Vite + React 19 + TypeScript + Tailwind 4 前端
- `github-showcase/` — GitHub 风格设计包，前端风格的来源
- `start.sh` — 同时启动前后端，后端默认 8865，前端默认 8864

## 约束

1. **前端风格必须遵守 `frontend/STYLE.md`**，它是 `github-showcase/github-style.md` 的项目化落地。
   提交前跑 `cd frontend && npm run style:check`（`npm run build` 也会自动执行）。
   不要引入渐变、重阴影、衬线字体、大圆角或硬编码色值。

2. **接口以 `docs/API.md` 为准**。`frontend/src/lib/types.ts` 和 `frontend/src/lib/api.ts`
   与之逐条对应，改接口时两边一起改。错误信封是
   `{ "error": { "code": string, "message": string } }`。

3. **后端没有 CORS 也没有静态文件服务**。前端开发用 Vite 代理 `/api/*` → 后端，
   见 `frontend/vite.config.ts`；不要改成前端直连绝对地址。

4. **图谱是只追加的**：没有 update/delete 路由。项目完成后是终态，
   前端要禁用追加事实/步骤的入口。

## 常用命令

```sh
# 前后端一起（推荐）
./start.sh                     # 后端 :8865，前端 :8864
./start.sh 9000 3000           # 自定义端口

# 后端
cd backend && go test ./... && go vet ./... && go build ./...

# 前端
cd frontend && npm install
npm run dev                    # :8864
npm run typecheck
npm run style:check
npm run build
```

## 代码风格

- Go：`gofmt`，错误用 `errors.Is` 匹配哨兵错误，HTTP 层不要写业务判断
- TS：strict 模式，`@/` 别名指向 `src/`，不用 `any`，导入类型用 `import type`
- 注释写「为什么」不写「做了什么」
