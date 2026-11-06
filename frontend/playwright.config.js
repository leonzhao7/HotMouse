import { defineConfig } from "@playwright/test";
import { existsSync } from "node:fs";

const executablePath =
  process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE ||
  (existsSync("/usr/bin/chromium") ? "/usr/bin/chromium" : undefined);
const backendPort = process.env.HOTMOUSE_E2E_BACKEND_PORT || 18080;
const frontendPort = process.env.HOTMOUSE_E2E_FRONTEND_PORT || 15173;
export default defineConfig({
  testDir: "./tests",
  timeout: 60000,
  fullyParallel: false,
  workers: 1,
  reporter: "list",
  use: {
    baseURL: `http://127.0.0.1:${frontendPort}`,
    viewport: { width: 1440, height: 1000 },
    trace: "retain-on-failure",
    launchOptions: { executablePath, args: ["--no-sandbox"] },
  },
  webServer: [
    {
      command: "node scripts/e2e-backend.mjs",
      url: `http://127.0.0.1:${backendPort}/healthz`,
      timeout: 120000,
      reuseExistingServer: false,
    },
    {
      command: `npm run dev -- --port ${frontendPort}`,
      url: `http://127.0.0.1:${frontendPort}`,
      env: { HOTMOUSE_BACKEND_URL: `http://127.0.0.1:${backendPort}` },
      reuseExistingServer: false,
    },
  ],
});
