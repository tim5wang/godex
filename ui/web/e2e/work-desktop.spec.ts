import { test, expect } from "@playwright/test";
import { mockBackend } from "./fixtures";

test.beforeEach(async ({ page }) => {
  await mockBackend(page);
  await page.addInitScript(() => {
    localStorage.setItem("godex:web:locale", "zh");
    // The existing shell defaults to an open mobile app-nav drawer. Keep it
    // closed in this feature suite so tests exercise the desktop underneath.
    localStorage.setItem("godex.web.layout.v1", JSON.stringify({ taskCenterDrawerOpen: false }));
  });
});

test("renders the prototype at every viewport, with shared resources and a recoverable brief", async ({ page }, info) => {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.goto("/work-desktop?context=speech");
  await expect(page.getByRole("heading", { name: "语音服务 · 长期维护", exact: true })).toBeVisible();
  await expect(page.getByText("交互原型 · 演示数据", { exact: true })).toBeVisible();
  await expect(page.getByText("当前生产状态未核实", { exact: true })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ path: `../../.godex/tmp/work-desktop-${info.project.name}.png`, fullPage: true });

  await page.getByRole("button", { name: "恢复上下文", exact: true }).click();
  const brief = page.getByRole("dialog", { name: "工作恢复简报" });
  await expect(brief.getByText("由演示检查点和事件拼装，不是 AI 推理或实时生产查询。", { exact: true })).toBeVisible();
  await expect(brief.getByText("固定模型权重，对比两组解码参数", { exact: true })).toBeVisible();
  expect(await brief.evaluate((element) => element.getBoundingClientRect().width <= window.innerWidth)).toBe(true);
  if (info.project.name === "chromium-desktop") await page.screenshot({ path: "../../.godex/tmp/work-desktop-recovery.png" });
  await brief.getByRole("button", { name: /关闭|Close/, exact: true }).click();

  await page.getByRole("button", { name: /共享语音服务/ }).click();
  const resource = page.getByRole("dialog", { name: "资源详情" });
  await expect(resource.getByText("service://demo/speech-recognition")).toBeVisible();
  await resource.getByRole("button", { name: "业务甲 · 接入语音能力", exact: true }).click();
  await expect(page.getByRole("heading", { name: "业务甲 · 接入语音能力", exact: true })).toBeVisible();
  await expect(page).toHaveURL(/context=onboarding/);
  await expect(page.getByText("短句门禁通过并提供可复现报告后，才能继续业务验收。", { exact: true })).toBeVisible();
  await page.goBack();
  await expect(page.getByRole("heading", { name: "语音服务 · 长期维护", exact: true })).toBeVisible();
  if (info.project.name === "chromium-desktop") {
    await page.setViewportSize({ width: 1920, height: 1080 });
    await page.screenshot({ path: "../../.godex/tmp/work-desktop-wide.png", fullPage: true });
  }
  expect(errors).toEqual([]);
});

test("keeps checkpoint edits, drafts and topic notes on their owning stream without backend writes", async ({ page }) => {
  test.skip((page.viewportSize()?.width ?? 0) < 1024);
  const writes: string[] = [];
  page.on("request", (request) => {
    if (request.method() !== "GET" && /\/v1\/(work-context|taskboard)|\/sessions/.test(request.url())) writes.push(request.url());
  });
  await page.goto("/work-desktop?context=speech");
  await page.getByRole("button", { name: "编辑检查点", exact: true }).click();
  let dialog = page.getByRole("dialog");
  await dialog.getByLabel("当前进展", { exact: true }).fill("确认范围后继续参数对比");
  await dialog.getByLabel("下一步 · 每行一项", { exact: true }).fill("跑一轮固定参数评测");
  await dialog.getByRole("button", { name: "保存演示修改", exact: true }).click();
  await expect(page.getByText("确认范围后继续参数对比", { exact: true }).first()).toBeVisible();
  await page.getByRole("button", { name: "委派工作", exact: true }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByLabel("委派目标", { exact: true }).fill("本地主线委派测试");
  await dialog.getByLabel("验收条件", { exact: true }).fill("固定数据集并附报告");
  await expect(dialog.getByText("检查点 r4", { exact: true })).toBeVisible();
  await dialog.getByRole("button", { name: "保存委派草稿", exact: true }).click();
  await expect(page.getByRole("button", { name: /本地主线委派测试/ })).toBeVisible();
  await page.getByRole("tab", { name: "主题对话" }).click();
  await page.getByRole("textbox", { name: "记录一条主线笔记", exact: true }).fill("下次继续时先核实评测集版本");
  await page.getByRole("button", { name: "记录笔记", exact: true }).click();
  await expect(page.getByText("下次继续时先核实评测集版本", { exact: true })).toBeVisible();
  await page.getByRole("textbox", { name: "记录一条主线笔记", exact: true }).fill("尚未提交的主题草稿");
  await page.locator(".wd-stream-list").getByRole("button", { name: /模型效果 · 对齐与更新/ }).click();
  await expect(page.getByText("下次继续时先核实评测集版本", { exact: true })).toHaveCount(0);
  await page.getByRole("tab", { name: "工作委派", exact: true }).click();
  await expect(page.getByRole("button", { name: /本地主线委派测试/ })).toHaveCount(0);
  await page.locator(".wd-stream-list").getByRole("button", { name: /语音服务 · 长期维护/ }).click();
  await expect(page.getByRole("button", { name: /本地主线委派测试/ })).toBeVisible();
  await page.getByRole("tab", { name: "主题对话" }).click();
  await expect(page.getByRole("textbox", { name: "记录一条主线笔记", exact: true })).toHaveValue("尚未提交的主题草稿");
  await expect(page.getByText("下次继续时先核实评测集版本", { exact: true })).toBeVisible();
  await page.reload();
  await expect(page.getByText("下次继续时先核实评测集版本", { exact: true })).toHaveCount(0);
  expect(writes).toEqual([]);
});

test("filters recall by scope, opens evidence and reviews demo candidates", async ({ page }) => {
  await page.goto("/work-desktop?context=speech&tab=knowledge");
  await expect(page.getByText("退化可能来自解码参数", { exact: true })).toBeVisible();
  await expect(page.getByText("r12 整体改善，但短句回归", { exact: true })).toHaveCount(0);
  await page.getByRole("combobox", { name: "检索范围", exact: true }).click();
  await page.getByText("当前及直接关联", { exact: true }).click();
  await expect(page.getByText("r12 整体改善，但短句回归", { exact: true })).toBeVisible();
  await page.getByRole("textbox", { name: "搜索结论、关键词或历史线索", exact: true }).fill("demo-id-v7");
  await expect(page.locator(".wd-knowledge")).toHaveCount(1);
  await page.getByRole("button", { name: "查看来源", exact: true }).click();
  const source = page.getByRole("dialog", { name: "证据与来源" });
  await expect(source.getByText("artifact://demo/evaluation/r12#short-utterances", { exact: true })).toBeVisible();
  await source.getByRole("button", { name: /关闭|Close/, exact: true }).click();
  await page.getByRole("textbox", { name: "搜索结论、关键词或历史线索", exact: true }).fill("解码参数");
  await page.getByRole("button", { name: "确认此候选", exact: true }).click();
  await expect(page.locator(".wd-knowledge").getByText("已确认（示例）", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "确认此候选", exact: true })).toHaveCount(0);
});

test("opens without a configured provider and handles English and unknown-stream fallback", async ({ page }) => {
  await page.route("**/api/providers", (route) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ providers: [] }) }));
  await page.goto("/work-desktop?context=missing&tab=not-a-tab");
  await expect(page.getByRole("heading", { name: "语音服务 · 长期维护", exact: true })).toBeVisible();
  await expect(page.getByText("该桌面不存在，已展示默认主线。", { exact: true })).toBeVisible();
  await expect(page.getByRole("tab", { name: "概览", exact: true })).toHaveAttribute("aria-selected", "true");
  await page.addInitScript(() => localStorage.setItem("godex:web:locale", "en"));
  await page.reload();
  await expect(page.getByRole("heading", { name: "Speech service · operations", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Restore context", exact: true })).toBeVisible();
});

test("creates a demo desktop, validates input and enforces archive read-only", async ({ page }) => {
  await page.goto("/work-desktop");
  await page.getByRole("button", { name: "新建桌面", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: "创建演示桌面", exact: true }).click();
  await expect(dialog.getByText("请输入非空内容").first()).toBeVisible();
  await dialog.getByLabel("主线名称", { exact: true }).fill("新服务维护主线");
  await dialog.getByLabel("背景", { exact: true }).fill("长期维护服务和模型更新");
  await dialog.getByLabel("目标", { exact: true }).fill("能快速找回工作背景");
  await dialog.getByRole("button", { name: "创建演示桌面", exact: true }).click();
  await expect(page.getByRole("heading", { name: "新服务维护主线", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "归档主线", exact: true }).click();
  await expect(page.getByRole("button", { name: "编辑检查点", exact: true })).toBeDisabled();
  await expect(page.getByRole("button", { name: "委派工作", exact: true })).toBeDisabled();
  await page.getByRole("button", { name: "恢复主线", exact: true }).click();
  await expect(page.getByRole("button", { name: "编辑检查点", exact: true })).toBeEnabled();
});
