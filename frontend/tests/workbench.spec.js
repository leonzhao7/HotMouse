import { expect, test } from "@playwright/test";

async function login(page) {
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "登录工作台" })).toBeVisible();
  await expect(
    page.getByRole("button", { name: "注册", exact: true }),
  ).toHaveCount(0);
  await page.getByLabel("用户名", { exact: true }).fill("admin");
  await page.getByLabel("密码", { exact: true }).fill("admin123");
  await expect(page.getByRole("button", { name: "刷新验证码" })).toHaveText(
    /\d+ \+ \d+/,
  );
  const numbers = (
    await page.getByRole("button", { name: "刷新验证码" }).innerText()
  )
    .match(/\d+/g)
    .map(Number);
  await page
    .getByLabel("验证码", { exact: true })
    .fill(String(numbers[0] + numbers[1]));
  await page.getByRole("button", { name: "进入工作台" }).click();
  await expect(page.getByRole("heading", { name: "工作台总览" })).toBeVisible({
    timeout: 15000,
  });
}
async function createProject(page, title) {
  await page
    .getByRole("button", { name: "新建项目", exact: true })
    .first()
    .click();
  const dialog = page.getByRole("dialog", { name: "新建项目" });
  await dialog.getByLabel("项目名称").fill(title);
  await dialog
    .getByLabel("初始事实（范围）")
    .fill("Local test target at http://127.0.0.1");
  await dialog
    .getByLabel("目标", { exact: true })
    .fill("Collect reproducible evidence");
  await dialog.getByRole("button", { name: "创建项目", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: new RegExp(title) }),
  ).toBeVisible();
  return decodeURIComponent(new URL(page.url()).hash.split("/").at(-1));
}
test("default admin login, session persistence, theme and responsive navigation", async ({
  page,
}) => {
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await login(page);
  await page.reload();
  await expect(page.getByRole("heading", { name: "工作台总览" })).toBeVisible();
  await page.getByRole("button", { name: "切换浅色主题" }).click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
  await page.reload();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole("button", { name: "展开导航" }).click();
  await page
    .getByRole("navigation", { name: "主导航" })
    .getByRole("link", { name: "项目", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "项目", exact: true }),
  ).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await page.screenshot({
    path: "test-results/mobile-projects.png",
    fullPage: true,
    animations: "disabled",
  });
  expect(errors).toEqual([]);
});
test("offline CTF knowledge can be searched, filtered and read in a project", async ({
  page,
}) => {
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await login(page);
  await createProject(page, "UI knowledge project");
  await page.getByRole("tab", { name: "CTF 知识", exact: true }).click();
  await expect(page.getByText("56 条离线知识", { exact: false })).toBeVisible();
  const search = page.getByRole("textbox", { name: "检索技术、产品或 CVE" });
  await search.fill("SSRF");
  const entry = page.locator(".entity-card").filter({ hasText: "kb006" });
  await expect(entry).toBeVisible();
  await entry.getByRole("button", { name: "查看方法" }).click();
  let dialog = page.getByRole("dialog", { name: "SSRF 利用" });
  await expect(dialog.getByText("来源：", { exact: false })).toBeVisible();
  await expect(dialog.getByRole("heading", { name: "命令示例" })).toBeVisible();
  await dialog.getByRole("button", { name: "关闭", exact: true }).click();
  await search.fill("");
  await page.getByLabel("知识分类").selectOption("crypto");
  await expect(
    page.locator(".entity-card").filter({ hasText: "kb041" }),
  ).toBeVisible();
  await expect(entry).toHaveCount(0);
  await page
    .locator(".entity-card")
    .filter({ hasText: "hotmouse-dispatcher" })
    .getByRole("button", { name: "查看技能" })
    .click();
  dialog = page.getByRole("dialog", { name: "hotmouse-dispatcher" });
  await expect(dialog.locator("pre")).toContainText("HotMouse Dispatcher");
  expect(errors).toEqual([]);
});
test("project graph, manual step conclusion, finding review and goal completion", async ({
  page,
}) => {
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await login(page);
  const projectID = await createProject(page, "UI evidence project");
  await page.getByRole("button", { name: "步骤", exact: true }).click();
  let dialog = page.getByRole("dialog", { name: "添加步骤" });
  await dialog
    .getByLabel("描述", { exact: true })
    .fill("Verify access control");
  await dialog.getByLabel("关联目标").selectOption("g001");
  await dialog.locator("input[type=checkbox]").check();
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await page.getByRole("tab", { name: /^步骤/ }).click();
  await page
    .getByRole("button", { name: /s001.*Verify access control/ })
    .click();
  await page.getByRole("button", { name: "记录结果", exact: true }).click();
  dialog = page.getByRole("dialog", { name: "记录步骤结果" });
  await dialog
    .getByLabel("结果事实描述")
    .fill("Confirmed unauthorized reading of a local fixture.");
  await dialog.getByRole("button", { name: "提交结果" }).click();
  await expect(dialog).not.toBeVisible();
  await expect(
    page.locator(".inspector").getByText("已结束", { exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "关闭详情" }).click();
  const resultRow = page.getByRole("row").filter({ hasText: "s001" });
  await expect(resultRow.locator("td").last()).toHaveText("未保存原始返回");
  await page.getByRole("tab", { name: /^发现/ }).click();
  await page.getByRole("button", { name: "记录发现" }).click();
  dialog = page.getByRole("dialog", { name: "记录发现" });
  await dialog
    .getByLabel("发现描述")
    .fill("Reproducible access control vulnerability");
  await dialog.getByLabel("发现标题").fill("Fixture access bypass");
  await dialog.getByLabel("证据事实").selectOption("f001");
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await page.getByRole("tab", { name: "关系图" }).click();
  const graph = page.locator(".graph-canvas");
  await expect
    .poll(() => graph.evaluate((el) => el._cyreg?.cy?.nodes().length || 0))
    .toBe(3);
  await graph.evaluate(
    () =>
      new Promise((resolve) =>
        requestAnimationFrame(() => requestAnimationFrame(resolve)),
      ),
  );
  const position = await graph.evaluate((el) =>
    el._cyreg.cy.getElementById("f001").renderedPosition(),
  );
  const box = await graph.boundingBox();
  await page.mouse.click(box.x + position.x, box.y + position.y);
  await expect(page.locator(".inspector").getByText("事实详情")).toBeVisible();
  await page.getByRole("button", { name: "关闭详情" }).click();
  await page.screenshot({
    path: "test-results/workspace-desktop.png",
    fullPage: true,
    animations: "disabled",
  });
  await page.goto(
    `/#/vulnerabilities?project_id=${encodeURIComponent(projectID)}`,
  );
  await page.getByRole("button", { name: /Fixture access bypass/ }).click();
  dialog = page.getByRole("dialog", { name: "Fixture access bypass" });
  await expect(
    dialog
      .getByText("Confirmed unauthorized reading of a local fixture.", {
        exact: true,
      })
      .first(),
  ).toBeVisible();
  await dialog.getByRole("button", { name: "标为已忽略" }).click();
  await expect(dialog.getByText("已忽略", { exact: true })).toBeVisible();
  await dialog.getByRole("button", { name: "重新确认" }).click();
  await expect(dialog.getByText("已确认", { exact: true })).toBeVisible();
  await dialog.getByRole("button", { name: "关闭", exact: true }).click();
  await page.goto(`/#/projects/${encodeURIComponent(projectID)}`);
  await page.getByRole("tab", { name: /^目标/ }).click();
  await page
    .getByRole("button", { name: /g001.*Collect reproducible evidence/ })
    .click();
  await page.getByRole("button", { name: "完成目标" }).click();
  dialog = page.getByRole("dialog", { name: "完成目标" });
  await dialog.getByLabel("完成说明").fill("Evidence collected");
  await dialog
    .locator("label")
    .filter({ hasText: "f001" })
    .locator("input")
    .check();
  await dialog.getByRole("button", { name: "提交完成证据" }).click();
  await expect(dialog).not.toBeVisible();
  await expect(page.getByRole("button", { name: "重新打开" })).toBeVisible();
  expect(errors).toEqual([]);
});
test("templates, Worker execution, secret preservation and real environment removal", async ({
  page,
}) => {
  await login(page);
  await page.goto("/#/templates");
  await page.getByRole("button", { name: "新建模板" }).click();
  let dialog = page.getByRole("dialog", { name: "新建模板" });
  await dialog.getByLabel("模板名称").fill("UI custom template");
  await dialog.getByLabel("初始事实").fill("Local fixture");
  await dialog.getByLabel("目标", { exact: true }).fill("Inspect fixture");
  await dialog.getByRole("button", { name: "保存模板" }).click();
  await expect(dialog).not.toBeVisible();
  await page
    .locator(".template-card")
    .filter({ hasText: "UI custom template" })
    .getByRole("button", { name: "创建项目" })
    .click();
  await page
    .getByRole("dialog", { name: "新建项目" })
    .getByRole("button", { name: "创建项目" })
    .click();
  await page.getByRole("button", { name: "步骤", exact: true }).click();
  dialog = page.getByRole("dialog", { name: "添加步骤" });
  await dialog.getByLabel("描述", { exact: true }).fill("Run mock check");
  await dialog.locator("input[type=checkbox]").check();
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await page.getByRole("tab", { name: /^步骤/ }).click();
  await page.getByRole("button", { name: /s001.*Run mock check/ }).click();
  await page.getByRole("button", { name: "Worker 执行", exact: true }).click();
  await expect(
    page.locator(".inspector").getByText("已结束", { exact: true }),
  ).toBeVisible();
  await page.goto("/#/workers");
  await page.getByRole("button", { name: "编辑配置" }).click();
  dialog = page.getByRole("dialog", { name: "编辑本地 Worker" });
  await dialog.getByRole("button", { name: "添加环境变量" }).click();
  await dialog.getByLabel("环境变量 1 名称").fill("API_TOKEN");
  await dialog.getByLabel("环境变量 1 值").fill("keep-this-secret");
  await dialog.getByRole("button", { name: "添加环境变量" }).click();
  await dialog.getByLabel("环境变量 2 名称").fill("DELETE_KEY");
  await dialog.getByLabel("环境变量 2 值").fill("remove-this");
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await page.getByRole("button", { name: "编辑配置" }).click();
  dialog = page.getByRole("dialog", { name: "编辑本地 Worker" });
  await expect(dialog.getByLabel("环境变量 1 值")).toHaveValue("***");
  await dialog.getByRole("button", { name: "删除环境变量 DELETE_KEY" }).click();
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  const config = await (await page.request.get("/api/workers/config")).json();
  expect(config.environment).toEqual({ API_TOKEN: "***" });
  await page.getByRole("button", { name: "测试已保存配置" }).click();
  await expect(page.getByText("检查通过", { exact: true })).toBeVisible();
});
test("Dispatcher LLM configuration preserves separate secrets and can return to shared settings", async ({
  page,
}) => {
  await login(page);
  const current = await (await page.request.get("/api/workers/config")).json();
  await page.request.put("/api/workers/config", {
    data: { ...current, dispatcher: null },
  });
  await page.goto("/#/workers");
  await page.getByRole("button", { name: "编辑配置" }).click();
  let dialog = page.getByRole("dialog", { name: "编辑本地 Worker" });
  await dialog.getByLabel("Dispatcher 使用独立 LLM").check();
  await dialog
    .getByLabel("Dispatcher 驱动", { exact: true })
    .selectOption("pi");
  await dialog.getByLabel("Dispatcher 命令", { exact: true }).fill("");
  await dialog
    .getByLabel("Dispatcher 模型", { exact: true })
    .fill("planner-model");
  await dialog
    .getByRole("button", { name: "添加 Dispatcher 环境变量", exact: true })
    .click();
  await dialog
    .getByLabel("Dispatcher 环境变量 1 名称", { exact: true })
    .fill("PI_API_KEY");
  await dialog
    .getByLabel("Dispatcher 环境变量 1 值", { exact: true })
    .fill("separate-planner-secret");
  await dialog
    .getByRole("button", { name: "添加 Dispatcher 环境变量", exact: true })
    .click();
  await dialog
    .getByLabel("Dispatcher 环境变量 2 名称", { exact: true })
    .fill("DELETE_PLANNER_KEY");
  await dialog
    .getByLabel("Dispatcher 环境变量 2 值", { exact: true })
    .fill("remove-planner-secret");
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(
    page.getByText("pi · planner-model", { exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "编辑配置" }).click();
  dialog = page.getByRole("dialog", { name: "编辑本地 Worker" });
  const plannerSecret = dialog
    .locator(".environment-rows > div")
    .filter({ has: page.locator('input[value="PI_API_KEY"]') })
    .locator('input[aria-label$="值"]');
  await expect(plannerSecret).toHaveValue("***");
  await dialog.getByLabel("显示输入值", { exact: true }).click();
  await expect(dialog.getByLabel("显示输入值", { exact: true })).toBeChecked();
  await expect(plannerSecret).toHaveValue("separate-planner-secret");
  await dialog.getByLabel("显示输入值", { exact: true }).uncheck();
  await dialog
    .getByRole("button", {
      name: "删除 Dispatcher 环境变量 DELETE_PLANNER_KEY",
      exact: true,
    })
    .click();
  await dialog
    .getByLabel("Dispatcher 模型", { exact: true })
    .fill("updated-planner-model");
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  const updated = await (
    await page.request.get("/api/workers/config?reveal=1")
  ).json();
  expect(updated.dispatcher.environment).toEqual({
    PI_API_KEY: "separate-planner-secret",
  });
  expect(updated.dispatcher.model).toBe("updated-planner-model");
  const masked = await (await page.request.get("/api/workers/config")).json();
  expect(masked.environment).toEqual(current.environment);
  expect(masked.driver).toBe(current.driver);
  await page.getByRole("button", { name: "编辑配置" }).click();
  dialog = page.getByRole("dialog", { name: "编辑本地 Worker" });
  await dialog.getByLabel("Dispatcher 使用独立 LLM").uncheck();
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  expect(
    (await (await page.request.get("/api/workers/config")).json()).dispatcher,
  ).toBeNull();
});
test("step list shows full LLM responses and retains them after refresh", async ({
  page,
}) => {
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await login(page);
  const projectID = await createProject(page, "UI LLM response project");
  const configuration = await (
    await page.request.get("/api/workers/config")
  ).json();
  const rawResponse = JSON.stringify({
    accepted: true,
    data: {
      description: "LLM confirmed the fixture response",
      result: {
        type: "progress",
        detail: "Full model evidence\nSecond line of evidence",
        evidence: {
          response: "<script>window.llmResponseExecuted=true</script>",
        },
      },
    },
  });
  const createStep = async (description) => {
    const response = await page.request.post(`/projects/${projectID}/steps`, {
      data: { from: ["origin"], description, creator: "ui-test" },
    });
    expect(response.ok()).toBe(true);
    return response.json();
  };
  try {
    const configured = await page.request.put("/api/workers/config", {
      data: {
        ...configuration,
        driver: "command",
        command: `if [ "$HOTMOUSE_TASK_TYPE" = target_probe ]; then printf '%s' '{"accepted":true,"data":{"required":false,"reason":"local model output fixture","targets":[]}}'; else printf '%s' '${rawResponse}'; fi`,
        enabled: true,
      },
    });
    expect(configured.ok()).toBe(true);
    const completed = await createStep("Inspect model output");
    const pending = await createStep("Pending inspection");
    const execution = await page.request.post(
      `/api/projects/${projectID}/steps/${completed.id}/execute`,
    );
    expect(execution.ok()).toBe(true);
    const executedStep = (await execution.json()).step;
    expect(executedStep.llm_response).toBe(rawResponse);
    expect(executedStep.llm_response_recorded).toBe(true);
    await page.reload();
    await page.getByRole("tab", { name: /^步骤/ }).click();
    await expect(
      page.getByRole("columnheader", { name: "LLM 返回", exact: true }),
    ).toBeVisible();
    const row = page.getByRole("row").filter({ hasText: completed.id });
    await expect(row).toContainText("LLM confirmed the fixture response");
    await expect(row.locator("pre")).not.toBeVisible();
    await row.getByText("查看完整返回", { exact: true }).click();
    expect(await row.locator("pre").textContent()).toBe(rawResponse);
    await expect(row.locator("pre")).toContainText("Second line of evidence");
    await expect(row.locator("pre")).toContainText(
      "<script>window.llmResponseExecuted=true</script>",
    );
    expect(
      await page.evaluate(() => window.llmResponseExecuted),
    ).toBeUndefined();
    const pendingRow = page.getByRole("row").filter({ hasText: pending.id });
    await expect(pendingRow.locator("td").last()).toHaveText("尚未返回");
    await row.getByRole("button", { name: /Inspect model output/ }).click();
    const inspector = page.locator(".inspector");
    await inspector.getByText("查看完整返回", { exact: true }).click();
    await expect(inspector.locator(".step-response-detail pre")).toContainText(
      "Second line of evidence",
    );
    await page.getByRole("button", { name: "关闭详情" }).click();
    await page.reload();
    await page.getByRole("tab", { name: /^步骤/ }).click();
    await expect(row).toContainText("LLM confirmed the fixture response");
    const detail = await (
      await page.request.get(`/projects/${projectID}/steps/${completed.id}`)
    ).json();
    expect(detail.llm_response).toBe(rawResponse);
    await row.getByText("查看完整返回", { exact: true }).click();
    await page.screenshot({
      path: "test-results/step-llm-response.png",
      fullPage: true,
      animations: "disabled",
    });
    await page.setViewportSize({ width: 390, height: 844 });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    expect(errors).toEqual([]);
  } finally {
    const restored = await page.request.put("/api/workers/config", {
      data: configuration,
    });
    expect(restored.ok()).toBe(true);
  }
});

test("LLM response column keeps the original reply separate from recovery results", async ({
  page,
}) => {
  await login(page);
  const projectID = await createProject(page, "UI original reply project");
  const created = await page.request.post(`/projects/${projectID}/steps`, {
    data: {
      from: ["origin"],
      description: "Read decode artifacts",
      creator: "ui-test",
    },
  });
  expect(created.ok()).toBe(true);
  const step = await created.json();
  const detail = await (
    await page.request.get(`/projects/${projectID}`)
  ).json();
  const original =
    "I'll start by reading the existing decode artifacts to understand what was produced.\n";
  Object.assign(detail.steps[0], {
    llm_response: original,
    llm_response_recorded: true,
    llm_response_error: "JSON object not found",
    to: "f001",
    result: { type: "environment_failure", detail: "Recovery summary only" },
  });
  detail.facts.push({ id: "f001", description: "Recovery summary only" });
  await page.route(`**/projects/${projectID}`, (route) =>
    route.fulfill({ json: detail }),
  );
  await page.reload();
  await page.getByRole("tab", { name: /^步骤/ }).click();
  const row = page.getByRole("row").filter({ hasText: step.id });
  const responseCell = row.locator("td").last();
  await expect(responseCell).toContainText(original.trim());
  await expect(responseCell).toContainText("JSON object not found");
  await expect(responseCell).not.toContainText("Recovery summary only");
  await responseCell.getByText("查看完整返回", { exact: true }).click();
  expect(await responseCell.locator("pre").textContent()).toBe(original);
  await row.getByRole("button", { name: /Read decode artifacts/ }).click();
  await expect(page.locator(".inspector")).toContainText(
    "Recovery summary only",
  );
  await expect(page.locator(".inspector .step-response")).toContainText(
    original.trim(),
  );
  await page.getByRole("button", { name: "关闭详情" }).click();
  detail.steps[0].llm_response = "";
  await page.getByRole("button", { name: "刷新", exact: true }).click();
  await expect(responseCell).toContainText("返回为空");
  await expect(responseCell).not.toContainText("Recovery summary only");
  detail.steps[0].llm_response_recorded = false;
  detail.steps[0].llm_response = "Legacy recovery summary";
  await page.getByRole("button", { name: "刷新", exact: true }).click();
  await expect(responseCell).toHaveText("未保存原始返回");
});

test("settings persist, notifications can be marked read, and expired sessions return to login", async ({
  page,
}) => {
  await login(page);
  await page.goto("/#/settings");
  const input = page.getByLabel("项目闲置提醒（小时）");
  await input.fill("48");
  await page.getByRole("button", { name: "保存设置" }).click();
  await expect(page.getByText("设置已保存", { exact: true })).toBeVisible();
  await page.reload();
  await expect(input).toHaveValue("48");
  await page.getByRole("button", { name: /^通知/ }).click();
  const dialog = page.getByRole("dialog", { name: "通知中心" });
  await expect(dialog.locator(".notification-item").first()).toBeVisible();
  await dialog.getByRole("button", { name: "全部标为已读" }).click();
  await expect(dialog.locator(".notification-item.unread")).toHaveCount(0);
  await dialog.getByRole("button", { name: "关闭", exact: true }).click();
  await page.context().clearCookies();
  await page.getByRole("button", { name: "检查健康" }).click();
  await expect(page.getByRole("heading", { name: "登录工作台" })).toBeVisible();
  await expect(page.getByText("登录会话已过期，请重新登录。")).toBeVisible();
});

test("configured project budget is inherited at creation and retained after default changes", async ({
  page,
}) => {
  await login(page);
  const initial = await (await page.request.get("/api/settings")).json();
  try {
    await page.goto("/#/settings");
    const input = page.getByLabel("默认项目预算（秒，0为不限）");
    await input.fill("3600");
    await page.getByRole("button", { name: "保存设置" }).click();
    await expect(page.getByText("设置已保存", { exact: true })).toBeVisible();
    await page.reload();
    await expect(input).toHaveValue("3600");
    await page.goto("/#/projects");
    const projectID = await createProject(page, "UI configured budget");
    let detail = await (
      await page.request.get(`/projects/${projectID}`)
    ).json();
    expect(detail.project.budget_seconds).toBe(3600);
    const step = await page.request.post(`/projects/${projectID}/steps`, {
      data: {
        from: ["origin"],
        goal_id: "g001",
        description: "inspect offline budget fixture",
        creator: "ui-test",
      },
    });
    expect(step.ok()).toBe(true);
    await page.goto("/#/settings");
    await input.fill("7200");
    await page.getByRole("button", { name: "保存设置" }).click();
    await expect(page.getByText("设置已保存", { exact: true })).toBeVisible();
    await page.goto(`/#/projects/${projectID}`);
    await page.getByRole("tab", { name: "路线与止损" }).click();
    await page.getByRole("button", { name: "添加路线", exact: true }).click();
    const dialog = page.getByRole("dialog", { name: "添加路线" });
    await expect(dialog.getByLabel("路线预算（秒，0 为不限）")).toHaveCount(0);
    await dialog
      .getByLabel("标题", { exact: true })
      .fill("another budget route");
    await dialog.getByRole("button", { name: "保存", exact: true }).click();
    await expect(dialog).not.toBeVisible();
    detail = await (await page.request.get(`/projects/${projectID}`)).json();
    expect(detail.project.budget_seconds).toBe(3600);
    expect(detail.routes).toHaveLength(2);
    expect(
      detail.routes.every((route) => !Object.hasOwn(route, "budget_seconds")),
    ).toBe(true);
    await expect(page.getByLabel("项目共享预算")).toContainText("3600 秒");
    await page
      .getByRole("button", { name: "修改项目预算", exact: true })
      .click();
    const projectBudgetDialog = page.getByRole("dialog", {
      name: "修改项目预算",
    });
    await projectBudgetDialog
      .getByLabel("项目共享预算（秒，0 为不限）")
      .fill("1800");
    await projectBudgetDialog
      .getByRole("button", { name: "保存", exact: true })
      .click();
    await expect(projectBudgetDialog).not.toBeVisible();
    await expect(page.getByLabel("项目共享预算")).toContainText("1800 秒");
    await page.goto("/#/projects");
    const secondID = await createProject(page, "UI changed default budget");
    const second = await (
      await page.request.get(`/projects/${secondID}`)
    ).json();
    expect(second.project.budget_seconds).toBe(7200);
    await page.goto("/#/settings");
    await input.fill("0");
    await page.getByRole("button", { name: "保存设置" }).click();
    await expect(page.getByText("设置已保存", { exact: true })).toBeVisible();
    await page.goto("/#/projects");
    const thirdID = await createProject(page, "UI unlimited budget");
    const third = await (await page.request.get(`/projects/${thirdID}`)).json();
    expect(third.project.budget_seconds).toBe(0);
  } finally {
    const latest = await (await page.request.get("/api/settings")).json();
    const restored = await page.request.put("/api/settings", {
      data: {
        ...latest,
        default_project_budget_seconds: initial.default_project_budget_seconds,
      },
    });
    expect(restored.ok()).toBe(true);
  }
});

test("route loss records, history recall, shared experience and flag reconciliation", async ({
  page,
}) => {
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await login(page);
  const projectID = await createProject(page, "UI strategy project");
  await page.getByRole("tab", { name: "路线与止损" }).click();
  await page.getByRole("button", { name: "添加路线", exact: true }).click();
  let dialog = page.getByRole("dialog", { name: "添加路线" });
  await dialog
    .getByLabel("标题", { exact: true })
    .fill("Bounded authentication route");
  await dialog.getByLabel("关联目标").selectOption("g001");
  await dialog.getByLabel("连续无信息上限").fill("1");
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  const routeResponse = await page.request.get(`/projects/${projectID}/routes`);
  const route = (await routeResponse.json())[0];
  const stepResponse = await page.request.post(`/projects/${projectID}/steps`, {
    data: {
      from: ["origin"],
      description: "inspect authentication",
      goal_id: "g001",
      route_id: route.id,
      creator: "ui-test",
    },
  });
  expect(stepResponse.ok()).toBe(true);
  const step = await stepResponse.json();
  await page.reload();
  await page.getByRole("tab", { name: /^步骤/ }).click();
  await page
    .getByRole("button", {
      name: new RegExp(`${step.id}.*inspect authentication`),
    })
    .click();
  await page.getByRole("button", { name: "记录结果", exact: true }).click();
  dialog = page.getByRole("dialog", { name: "记录步骤结果" });
  await dialog
    .getByLabel("结果事实描述")
    .fill("No new authentication evidence");
  await dialog.getByLabel("结果类型").selectOption("no_information");
  await dialog
    .getByLabel("详细证据")
    .fill("Original evidence notes remain available");
  await dialog.getByRole("button", { name: "提交结果", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await page.getByRole("tab", { name: "路线与止损" }).click();
  await expect(page.getByText("已搁置", { exact: true })).toBeVisible();
  await expect(page.getByText("搁置原因：no_information_limit")).toBeVisible();
  let projectState = await (
    await page.request.get(`/projects/${projectID}`)
  ).json();
  expect(projectState.project.status).toBe("failed");
  expect(projectState.project.failure_reason).toContain("no_information_limit");
  await expect(
    page.getByRole("button", { name: "继续推进", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "继续推进", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "停止", exact: true }),
  ).toBeVisible();
  projectState = await (
    await page.request.get(`/projects/${projectID}`)
  ).json();
  expect(projectState.project.status).toBe("active");
  expect(projectState.project.failure_reason).toBeFalsy();
  expect(projectState.routes[0].status).toBe("active");
  expect(projectState.routes[0].no_progress_count).toBe(0);
  expect(projectState.project.budget_spent_ms).toBe(0);
  expect(projectState.routes[0]).not.toHaveProperty("budget_seconds");
  expect(projectState.routes[0].result_count).toBe(1);
  await expect(page.getByText("可投入", { exact: true })).toBeVisible();
  await expect(page.getByLabel("项目共享预算")).toContainText(
    "本轮累计执行 0 秒",
  );
  await page.getByRole("button", { name: "调整路线" }).click();
  dialog = page.getByRole("dialog", { name: "调整路线" });
  await dialog.getByLabel("调整原因").fill("Use independent verified method");
  await dialog.getByLabel("重新开启条件").fill("New evidence is available");
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await page.getByRole("tab", { name: "历史检索" }).click();
  await page
    .getByPlaceholder("检索当前项目的历史与证据")
    .fill("authentication");
  await page
    .getByRole("tabpanel", { name: "历史检索" })
    .getByRole("button", { name: /f001.*No new authentication evidence/ })
    .click();
  dialog = page.getByRole("dialog", { name: "完整记录" });
  await expect(dialog).toContainText(
    "Original evidence notes remain available",
  );
  await dialog.getByRole("button", { name: "关闭", exact: true }).click();
  await page.getByRole("tab", { name: "经验库" }).click();
  await page.getByRole("button", { name: "保存经验", exact: true }).click();
  dialog = page.getByRole("dialog", { name: "保存经验" });
  await dialog
    .getByLabel("标题", { exact: true })
    .fill("Authentication review method");
  await dialog.getByLabel("适用条件").fill("Authorized login interface");
  await dialog.getByLabel("已验证方法").fill("Compare recorded responses");
  await dialog.getByLabel("失败条件").fill("Session state differs");
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(
    page.getByRole("button", { name: /Authentication review method/ }),
  ).toBeVisible();
  await page.getByRole("tab", { name: "flag 台账" }).click();
  await page.getByRole("button", { name: "保存 flag", exact: true }).click();
  dialog = page.getByRole("dialog", { name: "保存 flag" });
  await dialog.getByLabel("flag", { exact: true }).fill("flag{ui-ledger}");
  await dialog.getByLabel("来源事实").selectOption("origin");
  await dialog.getByLabel("题目编号").fill("ui-challenge");
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await page.getByRole("button", { name: "登记提交尝试" }).click();
  await page.getByRole("button", { name: "核对并记录平台结果" }).click();
  dialog = page.getByRole("dialog", { name: "记录平台结果" });
  await dialog.getByLabel("平台结果").selectOption("submission_unknown");
  await dialog.getByLabel("平台回复").fill("Request timed out");
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(page.getByRole("button", { name: "登记提交尝试" })).toHaveCount(
    0,
  );
  await page.getByRole("button", { name: "核对并记录平台结果" }).click();
  dialog = page.getByRole("dialog", { name: "记录平台结果" });
  await dialog.getByLabel("平台结果").selectOption("accepted");
  await dialog.getByLabel("平台回复").fill("Platform confirmed acceptance");
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(page.getByText("平台已接受", { exact: true })).toBeVisible();
  expect(errors).toEqual([]);
});
