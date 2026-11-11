import { expect, test } from "@playwright/test";

async function login(page) {
  await page.goto("/");
  await page.getByLabel("用户名", { exact: true }).fill("admin");
  await page.getByLabel("密码", { exact: true }).fill("admin123");
  const captcha = page.getByRole("button", { name: "刷新验证码" });
  await expect(captcha).toHaveText(/\d+ \+ \d+/);
  const numbers = (await captcha.innerText()).match(/\d+/g).map(Number);
  await page
    .getByLabel("验证码", { exact: true })
    .fill(String(numbers[0] + numbers[1]));
  await page.getByRole("button", { name: "进入工作台" }).click();
  await expect(page.getByRole("heading", { name: "工作台总览" })).toBeVisible({
    timeout: 15000,
  });
}

test("TSec configuration, rules upload, competition progress and controls", async ({
  page,
}) => {
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await login(page);
  const response = await page.request.post("/projects", {
    data: {
      title: "TSec Web challenge",
      origin: "owned local test target",
      goal: "collect test flags",
    },
  });
  expect(response.ok()).toBe(true);
  const project = (await response.json()).project.id;
  const originalRules = "只攻击当前比赛靶场。\n不使用付费提示。\n";
  const initial = {
    id: "competition_001",
    platform: "tsec",
    status: "completed",
    rules: "CLI competition rules",
    rules_summary: "Use original rules",
    concurrency: 1,
    max_workers: 1,
    challenge_timeout_seconds: 1200,
    created_at: new Date().toISOString(),
    finished_at: new Date().toISOString(),
    last_error: "",
    items: [
      {
        challenge_id: "e1-01",
        ordinal: 0,
        project_id: project,
        status: "succeeded",
        outcome: "succeeded",
      },
      {
        challenge_id: "e1-02",
        ordinal: 1,
        status: "failed",
        outcome: "failed",
        last_error: "challenge time limit reached",
      },
      {
        challenge_id: "e1-03",
        ordinal: 2,
        status: "skipped",
        outcome: "skipped",
      },
    ],
  };
  const runs = [initial];
  let startBody,
    concurrencyBody,
    stops = 0;
  // Browser tests control coordinator timing; Go integration tests exercise
  // the same start/concurrency/stop endpoints against the real scheduler.
  await page.route("**/api/competitions/status", (route) =>
    route.fulfill({ json: { scheduler_enabled: true } }),
  );
  await page.route("**/api/platforms/tsec/runs**", async (route) => {
    const url = new URL(route.request().url());
    if (route.request().method() === "GET")
      return route.fulfill({ json: runs });
    if (route.request().method() === "PATCH") {
      concurrencyBody = route.request().postDataJSON();
      runs.at(-1).concurrency = concurrencyBody.concurrency;
      return route.fulfill({ json: runs.at(-1) });
    }
    if (url.pathname.endsWith("/stop")) {
      stops++;
      const current = runs.at(-1);
      current.status = "stopped";
      current.finished_at = new Date().toISOString();
      current.items = current.items.map((item) =>
        ["running", "queued"].includes(item.status)
          ? { ...item, status: "skipped", outcome: "skipped" }
          : item,
      );
      return route.fulfill({ json: current });
    }
    startBody = route.request().postDataJSON();
    const current = {
      ...initial,
      ...startBody,
      id: "competition_002",
      status: "running",
      finished_at: null,
      items: initial.items.map((item, index) => ({
        challenge_id: item.challenge_id,
        ordinal: index,
        project_id: index === 0 ? project : null,
        status: index === 0 ? "running" : "queued",
        outcome: "",
        last_error: "",
        started_at: index === 0 ? new Date().toISOString() : null,
      })),
    };
    runs.push(current);
    await route.fulfill({ json: current });
  });
  await page
    .getByRole("navigation", { name: "主导航" })
    .getByRole("link", { name: "比赛", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "比赛", exact: true }),
  ).toBeVisible();
  await expect(page.getByLabel("平台", { exact: true })).toHaveValue("tsec");
  await expect(
    page.getByText("competition_001", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("progressbar", { name: "题目处理进度" }),
  ).toHaveAttribute("value", "3");
  await page.getByRole("button", { name: "配置平台", exact: true }).click();
  let dialog = page.getByRole("dialog", { name: "配置平台 · TSec Benchmark" });
  await dialog.getByLabel("平台地址").fill("http://127.0.0.1:31337");
  await dialog.getByLabel("任务令牌").fill("browser-test-task-token");
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "启动比赛", exact: true }),
  ).toBeEnabled();
  // Configuration uses the real backend and never reveals saved tokens.
  const config = await page.request.get("/api/platforms/tsec/config");
  expect(await config.json()).toMatchObject({
    base_url: "http://127.0.0.1:31337",
    token_set: true,
    source: "saved",
  });
  expect(await config.text()).not.toContain("browser-test-task-token");
  await page.getByRole("button", { name: "配置平台", exact: true }).click();
  dialog = page.getByRole("dialog", { name: "配置平台 · TSec Benchmark" });
  await expect(dialog.getByLabel("任务令牌")).toHaveValue("");
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  await page.getByRole("button", { name: "启动比赛", exact: true }).click();
  dialog = page.getByRole("dialog", { name: "启动比赛" });
  await dialog.getByLabel("规则文件").setInputFiles({
    name: "rules.md",
    mimeType: "text/markdown",
    buffer: Buffer.from(originalRules),
  });
  await expect(dialog.getByLabel("比赛规则", { exact: true })).toHaveValue(
    originalRules,
  );
  await expect(dialog.getByLabel("同时解答题目数")).toHaveAttribute("max", "3");
  await dialog.getByLabel("每题超时（分钟）").fill("2.5");
  await dialog.getByRole("button", { name: "启动比赛", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  expect(startBody).toEqual({
    rules: originalRules,
    concurrency: 1,
    max_workers: 1,
    challenge_timeout_seconds: 150,
  });
  await expect(
    page.getByText("competition_002", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("progressbar", { name: "题目处理进度" }),
  ).toHaveAttribute("value", "0");
  await expect(
    page.getByRole("button", { name: "启动比赛", exact: true }),
  ).toBeDisabled();
  const activeRow = page.getByRole("row").filter({ hasText: "e1-01" });
  await expect(activeRow.getByText("运行中", { exact: true })).toBeVisible();
  await expect(
    activeRow.getByRole("link", { name: project, exact: true }),
  ).toHaveAttribute("href", `#/projects/${project}`);
  await page.getByRole("button", { name: "调整并发", exact: true }).click();
  dialog = page.getByRole("dialog", { name: "调整题目并发" });
  await dialog.getByLabel("同时解答题目数").fill("2");
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  expect(concurrencyBody).toEqual({ concurrency: 2 });
  runs.at(-1).items[0].status = "succeeded";
  runs.at(-1).items[0].outcome = "succeeded";
  runs.at(-1).items[1].status = "running";
  await page.getByRole("button", { name: "刷新", exact: true }).click();
  await expect(
    page.getByRole("progressbar", { name: "题目处理进度" }),
  ).toHaveAttribute("value", "1");
  await page.reload();
  await expect(
    page.getByText("competition_002", { exact: true }),
  ).toBeVisible();
  await page.screenshot({
    path: "test-results/competitions-desktop.png",
    fullPage: true,
    animations: "disabled",
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(
    page.getByRole("heading", { name: "比赛进度", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "展开导航" }).click();
  await expect(
    page
      .getByRole("navigation", { name: "主导航" })
      .getByRole("link", { name: "比赛", exact: true }),
  ).toBeVisible();
  // Click the exposed backdrop beside the sidebar on the 390 px viewport.
  await page.getByRole("button", { name: "关闭导航" }).click({
    position: { x: 380, y: 420 },
  });
  await expect(page.locator(".sidebar")).not.toHaveClass(/open/);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await page.screenshot({
    path: "test-results/competitions-mobile.png",
    fullPage: true,
    animations: "disabled",
  });
  await page.getByRole("button", { name: "停止比赛", exact: true }).click();
  dialog = page.getByRole("dialog", { name: "停止比赛" });
  await dialog.getByRole("button", { name: "停止比赛", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  expect(stops).toBe(1);
  await expect(page.getByText("已停止", { exact: true })).toBeVisible();
  await expect(
    page.getByRole("button", { name: "启动比赛", exact: true }),
  ).toBeEnabled();
  await page.getByLabel("比赛记录").selectOption("competition_001");
  await expect(
    page.getByText("competition_001", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("challenge time limit reached", { exact: true }),
  ).toBeVisible();
  await page.getByLabel("题目状态").selectOption("failed");
  await expect(page.getByRole("row").filter({ hasText: "e1-02" })).toHaveCount(
    1,
  );
  await expect(page.getByRole("row").filter({ hasText: "e1-01" })).toHaveCount(
    0,
  );
  expect(errors).toEqual([]);
});
