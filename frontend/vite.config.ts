import { fileURLToPath, URL } from 'node:url'

import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// The Go backend exposes only /project* and serves no static assets, so dev
// traffic for those routes is proxied to it. Override with HOTMOUSE_API_URL
// when start.sh runs on a non-default port.
const backend = process.env.HOTMOUSE_API_URL ?? 'http://127.0.0.1:8865'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    port: 5173,
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
