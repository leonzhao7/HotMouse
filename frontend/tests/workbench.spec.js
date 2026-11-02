import { expect, test } from "@playwright/test";

async function register(page) {
  await page.goto("/");
  await page.getByRole("button", { name: "注册", exact: true }).click();
  await expect(page.getByRole("heading", { name: "创建账户" })).toBeVisible();
  const username = `ui_${Date.now()}_${Math.random().toString(36).slice(2, 7)}`;
  await page.getByLabel("用户名", { exact: true }).fill(username);
  await page.getByLabel("密码", { exact: true }).fill("Testing123!");
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
  await page.getByRole("button", { name: "注册并登录" }).click();
  await expect(page.getByRole("heading", { name: "工作台总览" })).toBeVisible();
  return username;
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
test("register, session persistence, theme and responsive navigation", async ({
  page,
}) => {
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await register(page);
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
test("project graph, manual step conclusion, finding review and goal completion", async ({
  page,
}) => {
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await register(page);
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
  await register(page);
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
test("settings persist, notifications can be marked read, and expired sessions return to login", async ({
  page,
}) => {
  await register(page);
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
