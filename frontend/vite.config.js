import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  const target = env.HOTMOUSE_BACKEND_URL || "http://127.0.0.1:8080";
  const proxy = Object.fromEntries(
    ["/api", "/projects", "/healthz"].map((path) => [
      path,
      { target, changeOrigin: true },
    ]),
  );
  return {
    plugins: [react()],
    server: { port: 5173, strictPort: true, proxy },
    preview: { port: 4173, strictPort: true, proxy },
    build: {
      outDir: "dist",
      emptyOutDir: true,
      rollupOptions: {
        output: {
          manualChunks: {
            "graph-core": ["cytoscape"],
            "graph-layout": ["cytoscape-dagre"],
          },
        },
      },
    },
  };
});
