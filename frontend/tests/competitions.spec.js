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
        initial_description:
          "Original sandbox challenge\nTarget address: 10.0.0.1:1337\n<script>plain task text</script>",
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
  await page.route("**/api/platforms/tsec/challenges", (route) =>
    route.fulfill({ json: [] }),
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
        initial_description: item.initial_description || "",
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
  await page.getByLabel("平台", { exact: true }).selectOption("tsec");
  await expect(page.getByLabel("平台", { exact: true })).toHaveValue("tsec");
  await expect(
    page.getByText("competition_001", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("progressbar", { name: "题目处理进度" }),
  ).toHaveAttribute("value", "3");
  const description = page
    .getByRole("row")
    .filter({ hasText: "e1-01" })
    .locator("details.competition-description");
  await description.locator("summary").click();
  await expect(description.locator("pre")).toHaveText(
    initial.items[0].initial_description,
  );
  await expect(description.locator("script")).toHaveCount(0);
  await expect(
    page
      .getByRole("row")
      .filter({ hasText: "e1-03" })
      .getByText("尚未获得初始描述"),
  ).toBeVisible();
  const search = page.getByPlaceholder("搜索题目、项目编号或初始描述");
  await search.fill("sandbox");
  await expect(page.getByRole("row").filter({ hasText: "e1-02" })).toHaveCount(
    0,
  );
  await expect(description).toBeVisible();
  await search.fill("");
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
  await expect(dialog.getByLabel("同时解答题目数")).toHaveValue("");
  await expect(dialog.getByLabel("同时解答题目数")).toHaveAttribute(
    "placeholder",
    /^[1-3]$/,
  );
  await dialog.getByLabel("每题超时（分钟）").fill("2.5");
  await dialog.getByRole("button", { name: "启动比赛", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  expect(startBody).toEqual({
    rules: originalRules,
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

test("CTFy loads challenges after configuration and reports platform errors before a run", async ({
  page,
}) => {
  const errors = [];
  const writes = [];
  let requests = 0;
  let failure = null;
  let challenges = [
    {
      id: "CTFY-web-001",
      title: "Document vault",
      description:
        "Find the vault secret.\n<script>plain challenge text</script>",
      difficulty: "medium",
      flag_count: 2,
      correct_flag_count: 1,
      completed: false,
      instance_status: "ready",
      metadata: { category: "web" },
    },
    {
      id: "CTFY-crypto-002",
      title: "Cipher notes",
      description: "Recover the message",
      flag_count: 1,
      correct_flag_count: 1,
      completed: true,
      instance_status: "ready",
      metadata: { category: "crypto" },
    },
  ];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      new URL(request.url()).pathname.startsWith("/api/platforms/ctfy/")
    )
      writes.push(request.url());
  });
  await page.route("**/api/competitions/status", (route) =>
    route.fulfill({ json: { scheduler_enabled: false } }),
  );
  await page.route("**/api/platforms/tsec/challenges", (route) =>
    route.fulfill({ json: [] }),
  );
  await page.route("**/api/platforms/ctfy/challenges", (route) => {
    requests++;
    return route.fulfill(
      failure
        ? {
            status: failure.status,
            json: { code: failure.code, message: "upstream error" },
          }
        : { json: challenges },
    );
  });
  await login(page);
  await page.goto("/#/competitions?platform=ctfy");
  await expect(page.getByLabel("平台", { exact: true })).toHaveValue("ctfy");
  await expect(page.getByText("待配置", { exact: true })).toBeVisible();
  expect(requests).toBe(0);
  await page.getByRole("button", { name: "配置平台", exact: true }).click();
  let dialog = page.getByRole("dialog", { name: "配置平台 · CTFy / TPCTF" });
  await dialog.getByLabel("平台地址").fill("http://127.0.0.1:31337/api/v1");
  await dialog
    .getByLabel("agent_play 参赛令牌")
    .fill("browser-test-ctfy-token");
  await dialog.getByLabel("competition_id（可选）").fill("ctfy-browser-001");
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  const list = page.locator(".panel").filter({
    has: page.getByRole("heading", { name: "题目列表", exact: true }),
  });
  await expect(list.getByText("共 2 道题", { exact: true })).toBeVisible();
  await expect(
    list.getByText("运行中靶机 2 个", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "启动比赛", exact: true }),
  ).toBeDisabled();
  await expect(page.getByText("还没有比赛记录", { exact: true })).toBeVisible();
  const webRow = list.getByRole("row").filter({ hasText: "CTFY-web-001" });
  await expect(
    webRow.getByText("Document vault", { exact: true }),
  ).toBeVisible();
  await expect(webRow.getByText("部分完成", { exact: true })).toBeVisible();
  await expect(webRow.getByText("运行中", { exact: true })).toBeVisible();
  await expect(list.getByText("运行中", { exact: true })).toHaveCount(2);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({
    path: "test-results/ctfy-existing-instances-mobile.png",
    fullPage: true,
    animations: "disabled",
  });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await expect(webRow.getByText("1 / 2", { exact: true })).toBeVisible();
  await webRow.locator("summary").click();
  await expect(webRow.locator("pre")).toHaveText(challenges[0].description);
  await expect(webRow.locator("script")).toHaveCount(0);
  const search = list.getByPlaceholder("搜索题目编号、名称、描述或分类");
  await search.fill("crypto");
  await expect(webRow).toHaveCount(0);
  await expect(list.getByText("Cipher notes", { exact: true })).toBeVisible();
  await search.fill("");

  const visibleChallenges = challenges;
  challenges = visibleChallenges.map((challenge) => ({
    ...challenge,
    instance_status: "unknown",
    metadata: {
      ...challenge.metadata,
      instance_visibility: "incomplete",
      team_running_instances: 2,
    },
  }));
  await list.getByRole("button", { name: "刷新题目", exact: true }).click();
  await expect(
    list.getByText("运行中靶机 2 个", { exact: true }),
  ).toBeVisible();
  await expect(list.getByText("待核实", { exact: true })).toHaveCount(2);
  await expect(list.locator(".notice.amber")).toContainText(
    "实例列表没有返回详情",
  );
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await page.screenshot({
    path: "test-results/ctfy-unlisted-instances-mobile.png",
    fullPage: true,
    animations: "disabled",
  });
  await page.setViewportSize({ width: 1440, height: 1000 });
  challenges = visibleChallenges;
  await list.getByRole("button", { name: "刷新题目", exact: true }).click();
  await expect(list.getByText("待核实", { exact: true })).toHaveCount(0);

  const config = await page.request.get("/api/platforms/ctfy/config");
  expect(await config.json()).toMatchObject({
    base_url: "http://127.0.0.1:31337/api/v1",
    token_set: true,
    competition_id: "ctfy-browser-001",
  });
  expect(await config.text()).not.toContain("browser-test-ctfy-token");
  failure = { status: 403, code: "competition_not_started" };
  await list.getByRole("button", { name: "刷新题目", exact: true }).click();
  await expect(list.getByRole("alert")).toContainText("比赛尚未开赛");
  await expect(webRow).toHaveCount(0);
  failure = null;
  challenges = [];
  await list.getByRole("button", { name: "重试", exact: true }).click();
  await expect(
    list.getByText("当前没有可解答的题目", { exact: true }),
  ).toBeVisible();
  await expect(list.getByRole("alert")).toHaveCount(0);

  failure = { status: 401, code: "token_revoked" };
  await list.getByRole("button", { name: "刷新题目", exact: true }).click();
  await expect(list.getByRole("alert")).toContainText("平台令牌认证失败");
  await expect(
    page.getByRole("heading", { name: "比赛", exact: true }),
  ).toBeVisible();
  failure = null;
  challenges = [
    {
      id: "CTFY-next-003",
      title: "Next competition",
      flag_count: 1,
      correct_flag_count: 0,
      completed: false,
      metadata: { category: "web" },
    },
  ];
  const beforeSave = requests;
  await page.getByRole("button", { name: "配置平台", exact: true }).click();
  dialog = page.getByRole("dialog", { name: "配置平台 · CTFy / TPCTF" });
  await expect(dialog.getByLabel("agent_play 参赛令牌")).toHaveValue("");
  await expect(dialog.getByLabel("competition_id（可选）")).toHaveValue(
    "ctfy-browser-001",
  );
  await dialog.getByLabel("competition_id（可选）").fill("ctfy-browser-002");
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  await expect(
    list.getByText("Next competition", { exact: true }),
  ).toBeVisible();
  expect(requests).toBeGreaterThan(beforeSave);
  expect(
    (await (await page.request.get("/api/platforms/ctfy/config")).json())
      .competition_id,
  ).toBe("ctfy-browser-002");

  await page.getByLabel("平台", { exact: true }).selectOption("tsec");
  await expect(list.getByText("Next competition", { exact: true })).toHaveCount(
    0,
  );
  await page.getByLabel("平台", { exact: true }).selectOption("ctfy");
  await expect(
    list.getByText("Next competition", { exact: true }),
  ).toBeVisible();
  await page.reload();
  await expect(
    list.getByText("Next competition", { exact: true }),
  ).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await page.screenshot({
    path: "test-results/ctfy-challenges-mobile.png",
    fullPage: true,
    animations: "disabled",
  });
  expect(writes).toEqual([]);
  expect(errors).toEqual([]);
});

test("CTFy starts directly with no rules modal or planning UI", async ({
  page,
}) => {
  const errors = [];
  let startBody = null;
  const runs = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.route("**/api/competitions/status", (route) =>
    route.fulfill({ json: { scheduler_enabled: true } }),
  );
  await page.route("**/api/platforms/ctfy/challenges", (route) =>
    route.fulfill({
      json: [
        {
          id: "running-a",
          title: "Already running",
          instance_status: "ready",
          flag_count: 1,
          correct_flag_count: 0,
        },
      ],
    }),
  );
  await page.route("**/api/platforms/ctfy/runs**", (route) => {
    if (route.request().method() === "GET")
      return route.fulfill({ json: runs });
    if (route.request().method() !== "POST")
      return route.fulfill({ status: 405 });
    startBody = route.request().postDataJSON();
    const run = {
      id: "competition_ctfy_direct",
      platform: "ctfy",
      status: "running",
      rules: "",
      rules_summary: "",
      concurrency: 1,
      max_workers: 1,
      challenge_timeout_seconds: 1200,
      created_at: new Date().toISOString(),
      finished_at: null,
      items: [{ challenge_id: "running-a", ordinal: 0, status: "queued" }],
    };
    runs.push(run);
    return route.fulfill({ json: run });
  });
  await login(page);
  const config = await page.request.put("/api/platforms/ctfy/config", {
    data: {
      base_url: "http://127.0.0.1:31337/api/v1",
      token: "browser-test-ctfy-token",
      competition_id: "ctfy-browser-direct",
    },
  });
  expect(config.ok()).toBe(true);
  await page.goto("/#/competitions?platform=ctfy");
  await expect(
    page.getByRole("button", { name: "启动比赛", exact: true }),
  ).toBeEnabled();
  await expect(page.getByText("还没有比赛记录", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "启动比赛", exact: true }).click();
  await expect(page.getByRole("dialog", { name: "启动比赛" })).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "比赛进度" })).toBeVisible();
  await expect(page.locator(".competition-rules")).toHaveCount(0);
  await expect(
    page
      .locator(".competition-detail")
      .getByRole("row")
      .filter({ hasText: "running-a" }),
  ).toHaveCount(1);
  expect(startBody).toEqual({});
  expect(errors).toEqual([]);
});
