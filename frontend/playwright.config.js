import { defineConfig } from "@playwright/test";
import { existsSync } from "node:fs";

const executablePath =
  process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE ||
  (existsSync("/usr/bin/chromium") ? "/usr/bin/chromium" : undefined);
export default defineConfig({
  testDir: "./tests",
  timeout: 60000,
  fullyParallel: false,
  workers: 1,
  reporter: "list",
  use: {
    baseURL: "http://127.0.0.1:15173",
    viewport: { width: 1440, height: 1000 },
    trace: "retain-on-failure",
    launchOptions: { executablePath, args: ["--no-sandbox"] },
  },
  webServer: [
    {
      command: "node scripts/e2e-backend.mjs",
      url: "http://127.0.0.1:18080/healthz",
      timeout: 120000,
      reuseExistingServer: false,
    },
    {
      command: "npm run dev -- --port 15173",
      url: "http://127.0.0.1:15173",
      env: { HOTMOUSE_BACKEND_URL: "http://127.0.0.1:18080" },
      reuseExistingServer: false,
    },
  ],
});
