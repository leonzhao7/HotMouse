import { fileURLToPath, URL } from 'node:url'

import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// 前端端口。start.sh 默认 8864；独立部署时用 FRONTEND_PORT 覆盖。
const frontendPort = Number(process.env.FRONTEND_PORT ?? 8864)

// 后端地址。后端只提供 /project* 路由且不发送 CORS 头，开发时由 Vite
// 代理 /api/* 过去。start.sh 会按实际后端端口注入 HOTMOUSE_API_URL。
const backend = process.env.HOTMOUSE_API_URL ?? 'http://127.0.0.1:8865'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    host: true,
    port: frontendPort,
    // 端口被占用时直接失败，而不是自动递增到下一个端口 —— 下一个端口
    // 很可能就是后端所在的 8865，会让请求打到错误的服务上。
    strictPort: true,
    proxy: {
      // 前端统一走 /api 前缀，避免与浏览器路由 (/projects/:id) 冲突。
      // rewrite 去掉 /api，还原后端的 /project 路由。
      '/api': {
        target: backend,
        changeOrigin: true,
        rewrite: (path) => path.replace(/^\/api/, ''),
      },
    },
  },
})
