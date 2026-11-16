import { test, expect, type Page } from "@playwright/test";
import { mockBackend } from "./fixtures";

// This suite never starts a daemon or calls a model. Native protocol semantics
// are separately exercised with an in-process fake app-server in Go tests.
const thread = { id: "native-thread", name: "Codex 原生测试", preview: "Original task", cwd: "/tmp/godex-playwright", createdAt: 1, updatedAt: 2, model: "native-model", reasoningEffort: "high", modelProvider: "native-provider", status: { type: "active" }, canAcceptDirectInput: true };
const models = [
  { id: "catalog-a", model: "native-model", displayName: "Native Model A", defaultReasoningEffort: "high", supportedReasoningEfforts: [{ reasoningEffort: "low", description: "Low" }, { reasoningEffort: "high", description: "High" }] },
  { id: "catalog-b", model: "native-model-b", displayName: "Native Model B", defaultReasoningEffort: "medium", supportedReasoningEfforts: [{ reasoningEffort: "medium", description: "Medium" }, { reasoningEffort: "ultra", description: "Ultra" }] },
];
const liveTurn = { id: "native-turn", status: "inProgress", items: [{ id: "user", type: "userMessage", content: [{ type: "text", text: "原生端发送的任务" }] }, { id: "tool", type: "commandExecution", command: "go test ./...", status: "inProgress", aggregatedOutput: "running" }, { id: "reply", type: "agentMessage", text: "正在检查原生任务" }] };
const snapshot = { thread, turns: { data: [liveTurn], nextCursor: "earlier-page" }, approvals: [], sequence: 10 };

async function setup(page: Page, ready = true, executable?: string, watchFailure?: { status: number; code?: string }, locale: "zh" | "en" = "zh") {
  await page.addInitScript((value) => localStorage.setItem("godex:web:locale", value), locale);
  await mockBackend(page);
  // No Godex provider is configured: an external runtime must remain accessible.
  await page.route("**/api/providers**", (route) => route.fulfill({ json: { providers: [] } }));
  const submissions: Array<Record<string, unknown>> = [];
  const settingsUpdates: Array<Record<string, unknown>> = [];
  let godexOpens = 0;
  let accessChecks = 0;
  page.on("request", (request) => { if (request.method() === "POST" && new URL(request.url()).pathname === "/api/sessions") godexOpens++; });
  await page.route("**/api/external/codex/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith("/status")) return route.fulfill({ json: { ready, executable, reason: ready ? undefined : "shared control socket is missing", socket: "/tmp/codex-control.sock", workspaceRoots: ["/tmp/godex-playwright"] } });
    if (path.endsWith("/threads")) return route.fulfill({ json: { data: [thread], nextCursor: null } });
    if (path.endsWith("/access")) { accessChecks++; return route.fulfill({ json: { thread, loadedInRuntime: true } }); }
    if (path.endsWith("/models")) return route.fulfill({ json: { data: models } });
    if (path.endsWith("/settings")) { const settings = route.request().postDataJSON(); settingsUpdates.push(settings); return route.fulfill({ json: { ...thread, model: settings.model, reasoningEffort: settings.effort } }); }
    if (path.endsWith("/history")) {
      const earlier = new URL(route.request().url()).searchParams.has("cursor") && new URL(route.request().url()).searchParams.get("cursor");
      return route.fulfill({ json: earlier ? { ...snapshot, turns: { data: [{ id: "earliest-turn", status: "completed", items: [{ id: "earliest", type: "userMessage", content: [{ type: "text", text: "最早的一条历史消息" }] }, { id: "compact", type: "contextCompaction" }] }], nextCursor: null } } : snapshot });
    }
    if (path.endsWith("/messages")) { submissions.push(route.request().postDataJSON()); return route.fulfill({ json: { queuedSubmission: { id: "queued" } } }); }
    if (path.endsWith("/queue")) return route.fulfill({ json: { data: [], nextCursor: null } });
    return route.fulfill({ json: { ok: true } });
  });
  await page.addInitScript(({ initial, failure }) => {
    const originalFetch = window.fetch.bind(window);
    const state = { opened: 0, closed: 0, holdSnapshot: false, snapshot: initial, controllers: [] as ReadableStreamDefaultController<Uint8Array>[], failure: failure || null };
    Object.assign(window, { __codexStreams: state });
    window.fetch = async (input, init) => {
      if (!String(input).includes("/external/codex/threads/native-thread/events")) return originalFetch(input, init);
      state.opened++;
      if (state.failure) return new Response(JSON.stringify({ error: "thread native-thread already has an active writer", code: state.failure.code, retryable: false, outcomeUnknown: false }), { status: state.failure.status, headers: { "Content-Type": "application/json" } });
      const stream = new ReadableStream<Uint8Array>({ start(controller) {
        state.controllers.push(controller);
        if (!state.holdSnapshot) controller.enqueue(new TextEncoder().encode(`data: ${JSON.stringify({ method: "codex/snapshot", params: initial })}\n\n`));
        init?.signal?.addEventListener("abort", () => { state.closed++; try { controller.close(); } catch { /* closed */ } }, { once: true });
      } });
      return new Response(stream, { headers: { "Content-Type": "text/event-stream" } });
    };
  }, { initial: snapshot, failure: watchFailure });
  return { submissions, settingsUpdates, godexOpens: () => godexOpens, accessChecks: () => accessChecks };
}

async function join(page: Page) {
  await page.getByRole("button", { name: "加入协作", exact: true }).click();
  await page.locator(".ant-modal").getByRole("button", { name: "加入协作", exact: true }).click();
  await expect(page.locator(".codex-conversation textarea")).toBeEnabled();
}

async function loadEarlier(page: Page) {
  // Scrolling a short mobile feed to its loader may load the page before a
  // pointer click lands and remove that button. Exercise infinite backscroll
  // directly instead of racing an element that has already done its work.
  await expect(page.getByText("原生端发送的任务", { exact: true })).toBeVisible();
  await page.locator(".codex-conversation .chat-feed-v2-scroll").evaluate((element) => {
    element.scrollTop = 0;
    element.dispatchEvent(new Event("scroll"));
  });
  await expect(page.getByText("最早的一条历史消息", { exact: true })).toBeVisible();
}

test("joining reuses a fresh preflight and upgrades live observation without reconnecting", async ({ page }) => {
  const harness = await setup(page);
  await page.goto("/chat/codex/native-thread?codex_node=local");
  await expect(page.getByText("已载入共享运行时", { exact: true })).toBeVisible();
  expect(harness.accessChecks()).toBe(1);
  await page.getByRole("button", { name: "实时查看", exact: true }).click();
  await expect(page.getByText("执行中", { exact: true })).toBeVisible();
  expect(harness.accessChecks()).toBe(1);
  await join(page);
  await expect.poll(() => page.evaluate(() => (window as unknown as { __codexStreams: { opened: number } }).__codexStreams.opened)).toBe(1);
  expect(harness.accessChecks()).toBe(1);
});

test("joining shares an in-flight preflight instead of sending a duplicate check", async ({ page }) => {
  await setup(page);
  let checks = 0;
  let release!: () => void;
  const gate = new Promise<void>((resolve) => { release = resolve; });
  await page.route("**/api/external/codex/threads/native-thread/access", async (route) => {
    checks++; await gate;
    await route.fulfill({ json: { thread, loadedInRuntime: true } });
  });
  try {
    await page.goto("/chat/codex/native-thread?codex_node=local");
    await expect.poll(() => checks).toBe(1);
    await page.getByRole("button", { name: "加入协作", exact: true }).click();
    release();
    await page.locator(".ant-modal").getByRole("button", { name: "加入协作", exact: true }).click();
    await expect(page.locator(".codex-conversation textarea")).toBeEnabled();
    expect(checks).toBe(1);
  } finally { release(); }
});

test("initial live hydration shows connecting and cannot send before its snapshot", async ({ page }) => {
  await setup(page);
  await page.goto("/chat/codex/native-thread?codex_node=local");
  await expect(page.getByText("原生端发送的任务", { exact: true })).toBeVisible();
  await page.evaluate(() => { (window as unknown as { __codexStreams: { holdSnapshot: boolean } }).__codexStreams.holdSnapshot = true; });
  await page.getByRole("button", { name: "加入协作", exact: true }).click();
  await page.locator(".ant-modal").getByRole("button", { name: "加入协作", exact: true }).click();
  await expect(page.getByText("连接中 · 正在同步最近消息", { exact: true })).toBeVisible();
  await expect(page.locator(".codex-conversation textarea")).toBeDisabled();
  await page.evaluate(() => {
    const streams = (window as unknown as { __codexStreams: { snapshot: unknown; controllers: ReadableStreamDefaultController<Uint8Array>[] } }).__codexStreams;
    streams.controllers.at(-1)!.enqueue(new TextEncoder().encode(`data: ${JSON.stringify({ method: "codex/snapshot", params: streams.snapshot })}\n\n`));
  });
  await expect(page.locator(".codex-conversation textarea")).toBeEnabled();
});

test("English locale covers native connection, collaboration and model controls", async ({ page }) => {
  await setup(page, true, undefined, undefined, "en");
  await page.goto("/chat/codex/native-thread?codex_node=local");
  await expect(page.getByRole("button", { name: "Observe live", exact: true })).toBeVisible();
  await expect(page.getByLabel("Codex model", { exact: true })).toBeDisabled();
  await expect(page.getByRole("button", { name: "Load earlier messages", exact: true })).toBeVisible();
  await loadEarlier(page);
  await page.getByRole("button", { name: "Join collaboration", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Join the same Codex conversation?", exact: true });
  await expect(dialog.getByRole("button", { name: "Cancel", exact: true })).toBeVisible();
  await dialog.getByRole("button", { name: "Join collaboration", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(page.getByLabel("Codex model", { exact: true })).toBeEnabled();
  await expect(page.getByText("Context compacted", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "right Compaction summary", exact: true }).click();
  await expect(page.getByText("The native runtime compacted context; earlier history is still available above.", { exact: true })).toBeVisible();
  await expect(page.locator(".codex-conversation").getByText("加入协作", { exact: true })).toHaveCount(0);
});

test("model and effort apply only after explicit collaboration and synchronize native changes", async ({ page }) => {
  const harness = await setup(page);
  await page.goto("/chat/codex/native-thread?codex_node=local");
  const controls = page.getByTestId("codex-model-settings");
  await expect(page.getByLabel("Codex 模型", { exact: true })).toBeDisabled();
  expect(harness.settingsUpdates).toHaveLength(0);
  await join(page);
  await page.getByLabel("Codex 模型", { exact: true }).click();
  await page.locator(".ant-select-dropdown:visible").getByText("Native Model B", { exact: true }).click();
  await expect(controls.getByText("中", { exact: true })).toBeVisible();
  expect(harness.settingsUpdates).toHaveLength(0);
  await page.getByLabel("Codex 思考深度", { exact: true }).click();
  await page.locator(".ant-select-dropdown:visible").getByText("Ultra", { exact: true }).click();
  await controls.getByRole("button", { name: /应\s*用/ }).click();
  await expect.poll(() => harness.settingsUpdates).toEqual([{ model: "native-model-b", effort: "ultra" }]);
  await expect(controls.getByRole("button", { name: /应\s*用/ })).toBeDisabled();
  await expect(page.locator(".codex-conversation textarea")).toBeEnabled();
  await page.evaluate(() => {
    const streams = (window as unknown as { __codexStreams: { controllers: ReadableStreamDefaultController<Uint8Array>[] } }).__codexStreams;
    streams.controllers.at(-1)!.enqueue(new TextEncoder().encode(`data: ${JSON.stringify({ method: "thread/settings/updated", params: { threadId: "native-thread", threadSettings: { model: "native-model", effort: "low" } } })}\n\n`));
  });
  await expect(controls.getByText("Native Model A", { exact: true })).toBeVisible();
  await expect(controls.getByText("低", { exact: true })).toBeVisible();
  await expect(page.locator(".ant-select-dropdown:visible")).toHaveCount(0);
  expect(harness.settingsUpdates).toHaveLength(1);
  expect(harness.submissions).toHaveLength(0);
});

test("unsupported model discovery and settings errors do not disable native chat", async ({ page }) => {
  const harness = await setup(page);
  await page.route("**/api/external/codex/threads/native-thread/models", (route) => route.fulfill({ status: 400, json: { error: "model/list unsupported" } }));
  await page.goto("/chat/codex/native-thread?codex_node=local");
  await join(page);
  await expect(page.getByText("模型选项不可用，不影响继续对话", { exact: true })).toBeVisible();
  await expect(page.locator(".codex-conversation textarea")).toBeEnabled();
  await page.route("**/api/external/codex/threads/native-thread/models", (route) => route.fulfill({ json: { data: models } }));
  await page.getByTestId("codex-model-settings").getByRole("button", { name: /重\s*试/ }).click();
  await page.getByLabel("Codex 思考深度", { exact: true }).click();
  await page.locator(".ant-select-dropdown:visible").getByText("低", { exact: true }).click();
  let settingsAttempts = 0;
  await page.route("**/api/external/codex/threads/native-thread/settings", (route) => { settingsAttempts++; return route.fulfill({ status: 400, json: { error: "thread/settings/update unsupported" } }); });
  await page.getByTestId("codex-model-settings").getByRole("button", { name: /应\s*用/ }).click();
  await expect(page.getByText(/应用失败：thread\/settings\/update unsupported/)).toBeVisible();
  await expect(page.locator(".codex-conversation textarea")).toBeEnabled();
  await expect(page.getByText("执行中", { exact: true })).toBeVisible();
  expect(settingsAttempts).toBe(1);
  expect(harness.submissions).toHaveLength(0);
});

test("shared view uses native runtime membership and can switch to complete disk history", async ({ page }) => {
  await setup(page);
  const views: string[] = [];
  await page.route("**/api/external/codex/threads?**", (route) => {
    const view = new URL(route.request().url()).searchParams.get("view") || "all";
    views.push(view);
    return route.fulfill({ json: { data: view === "loaded" ? [] : [{ ...thread, name: "另一个运行时的历史", status: { type: "notLoaded" } }], nextCursor: null } });
  });
  await page.goto("/chat/codex/connect?codex_node=local");
  // The existing Chat layout uses the rail as a drawer on small viewports.
  const source = page.getByTestId("codex-sessions");
  await expect(page.getByRole("button", { name: "Toggle sidebar", exact: true })).toBeVisible();
  if (!await source.isVisible()) await page.getByRole("button", { name: "Toggle sidebar", exact: true }).click();
  await expect(source.getByText("共享运行时中暂无匹配会话；可切换全部历史", { exact: true })).toBeVisible();
  await source.getByText("全部历史", { exact: true }).click();
  await expect(source.getByText("另一个运行时的历史", { exact: true })).toBeVisible();
  expect(views).toContain("loaded"); expect(views).toContain("all");
});

test("unloaded history asks for explicit resume instead of claiming a different writer", async ({ page }) => {
  const harness = await setup(page);
  await page.route("**/api/external/codex/threads/native-thread/access", (route) => route.fulfill({ json: { thread: { ...thread, status: { type: "notLoaded" }, canAcceptDirectInput: null }, loadedInRuntime: false } }));
  await page.goto("/chat/codex/native-thread?codex_node=local");
  await expect(page.getByText("待载入 · 归属待确认", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "实时查看", exact: true }).click();
  await expect(page.getByRole("dialog", { name: "此会话尚未载入共享运行时", exact: true })).toBeVisible();
  await expect.poll(() => page.evaluate(() => (window as unknown as { __codexStreams: { opened: number } }).__codexStreams.opened)).toBe(0);
  await page.locator(".ant-modal").getByRole("button", { name: "连接查看", exact: true }).click();
  await expect(page.getByText("执行中", { exact: true })).toBeVisible();
  await expect(page.locator(".codex-conversation textarea")).toBeDisabled();
  expect(harness.submissions).toHaveLength(0);
});

test("read-only native threads remain observable but cannot join as writers", async ({ page }) => {
  const harness = await setup(page);
  await page.route("**/api/external/codex/threads/native-thread/access", (route) => route.fulfill({ json: { thread: { ...thread, canAcceptDirectInput: false }, loadedInRuntime: true } }));
  await page.goto("/chat/codex/native-thread?codex_node=local");
  await expect(page.getByText("此原生会话不接受直接输入，仅支持查看", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "加入协作", exact: true })).toBeDisabled();
  await page.getByRole("button", { name: "原生连接指引", exact: true }).click();
  await expect(page.locator(".ant-modal").getByText("codex resume --remote 'unix:///tmp/codex-control.sock' 'native-thread'", { exact: true })).toBeVisible();
  expect(harness.submissions).toHaveLength(0);
});

test("preflight failure does not resume the thread or discard its history", async ({ page }) => {
  await setup(page);
  await page.route("**/api/external/codex/threads/native-thread/access", (route) => route.fulfill({ status: 503, json: { error: "membership check unavailable" } }));
  await page.goto("/chat/codex/native-thread?codex_node=local");
  await expect(page.getByText("原生端发送的任务", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "加入协作", exact: true }).click();
  await expect(page.getByText(/连接检查失败：membership check unavailable/)).toBeVisible();
  await expect.poll(() => page.evaluate(() => (window as unknown as { __codexStreams: { opened: number } }).__codexStreams.opened)).toBe(0);
  await expect(page.locator(".codex-conversation textarea")).toBeDisabled();
});

for (const failure of [{ status: 409, code: "codex_active_writer" }, { status: 400 }]) {
  test(`writer conflict (${failure.status}) preserves read-only history and stops retries until explicitly reconnected`, async ({ page }) => {
    const harness = await setup(page, true, undefined, failure);
    await page.goto("/chat/codex/native-thread?codex_node=local");
    await loadEarlier(page);
    await page.getByRole("button", { name: "加入协作", exact: true }).click();
    await page.locator(".ant-modal").getByRole("button", { name: "加入协作", exact: true }).click();
    await expect(page.locator(".ant-modal")).toHaveCount(0);
    await expect(page.getByText("此会话由另一个 Codex 运行时持有", { exact: true })).toBeVisible();
    await expect(page.getByText("历史快照", { exact: true })).toBeVisible();
    await expect(page.getByText("最早的一条历史消息", { exact: true })).toBeVisible();
    await expect(page.locator(".codex-conversation textarea")).toBeDisabled();
    await expect(page.getByTestId("codex-conversation").getByRole("button", { name: "加入协作", exact: true })).toBeDisabled();
    await page.getByText("如何连接同一运行时", { exact: true }).click();
    await expect(page.getByText("codex resume --remote 'unix:///tmp/codex-control.sock' 'native-thread'", { exact: true })).toBeVisible();
    await page.getByText("如何连接同一运行时", { exact: true }).click();
    await expect.poll(() => page.locator(".codex-conversation .chat-feed-v2-scroll").evaluate((element) => element.clientHeight)).toBeGreaterThan(50);
    await page.screenshot({ path: test.info().outputPath("writer-conflict.png") });
    await page.clock.install();
    await page.clock.fastForward(30_000);
    await expect.poll(() => page.evaluate(() => (window as unknown as { __codexStreams: { opened: number } }).__codexStreams.opened)).toBe(1);
    await page.clock.resume();
    await page.evaluate(() => { (window as unknown as { __codexStreams: { failure: unknown } }).__codexStreams.failure = null; });
    await page.getByRole("button", { name: "已释放占用，重新连接", exact: true }).click();
    await expect(page.getByText("执行中", { exact: true })).toBeVisible();
    // Reconnecting only restores observation, not consent to submit commands.
    await expect(page.locator(".codex-conversation textarea")).toBeDisabled();
    await join(page);
    expect(harness.submissions).toHaveLength(0);
    expect(harness.godexOpens()).toBe(0);
  });
}

test("reuses Chat in read-only mode, preserves pre-compaction history and detaches observation", async ({ page }) => {
  const harness = await setup(page);
  await page.goto("/chat/codex/native-thread?codex_node=local");
  await expect(page.getByTestId("codex-conversation")).toBeVisible();
  await expect(page.getByText("原生端发送的任务", { exact: true })).toBeVisible();
  await expect(page.locator(".codex-conversation textarea")).toBeDisabled();
  await expect(page.locator('.codex-conversation .chat-feed-v2-bubble-turn').getByText("Codex", { exact: true })).toBeVisible();
  await expect(page.locator('.codex-conversation').getByRole("button", { name: /Edit and resend|Fork session from this turn|Save as note|编辑并重新发送|从此轮 fork 出新会话|保存为笔记/ })).toHaveCount(0);
  await loadEarlier(page);
  await expect(page.getByText("最早的一条历史消息", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "实时查看", exact: true }).click();
  await expect(page.getByText("执行中", { exact: true })).toBeVisible();
  await expect(page.locator('.codex-conversation .tool-call-row[data-status="running"]')).toHaveCount(1);
  await page.getByRole("button", { name: "断开观察", exact: true }).click();
  await expect.poll(() => page.evaluate(() => (window as unknown as { __codexStreams: { closed: number } }).__codexStreams.closed)).toBe(1);
  expect(harness.godexOpens()).toBe(0);
  expect(harness.submissions).toHaveLength(0);
});

test("collaborative input defaults to the native queue and can explicitly steer the fenced turn", async ({ page }) => {
  const harness = await setup(page);
  await page.goto("/chat/codex/native-thread?codex_node=local");
  await join(page);
  const input = page.locator(".codex-conversation textarea");
  await input.fill("检查测试"); await input.press("Enter");
  await expect.poll(() => harness.submissions.length).toBe(1);
  expect(harness.submissions[0]).toMatchObject({ text: "检查测试", mode: "queue" });
  expect(harness.submissions[0].requestId).toBeTruthy();
  await page.getByLabel("Codex 发送方式").click();
  await page.getByText("追加到当前任务", { exact: true }).last().click();
  await input.fill("先看性能测试"); await input.press("Enter");
  await expect.poll(() => harness.submissions.length).toBe(2);
  expect(harness.submissions[1]).toMatchObject({ mode: "steer", turnId: "native-turn" });
  expect(harness.godexOpens()).toBe(0);
  await expect(page.locator(".codex-conversation").getByRole("button", { name: /add files|添加附件|添加文件/i })).toHaveCount(0);
});

test("lost submission outcomes survive reload without automatic replay", async ({ page }) => {
  await setup(page);
  let sent = 0;
  await page.route("**/api/external/codex/threads/native-thread/messages", async (route) => { sent++; await route.abort("failed"); });
  await page.goto("/chat/codex/native-thread?codex_node=local");
  await join(page);
  const input = page.locator(".codex-conversation textarea");
  await input.fill("只执行一次"); await input.press("Enter");
  await expect(page.getByText("上次提交的结果需要确认，已暂停发送")).toBeVisible();
  await page.reload();
  await expect(page.getByText("上次提交的结果需要确认，已暂停发送")).toBeVisible();
  await expect(page.locator(".codex-conversation textarea")).toBeDisabled();
  expect(sent).toBe(1);
});

test("unready native runtime gives setup instructions without starting anything", async ({ page }) => {
  const harness = await setup(page, false);
  await page.goto("/chat/codex/connect?codex_node=local");
  await expect(page.getByText("codex app-server daemon start", { exact: true })).toBeVisible();
  await expect(page.getByText("shared control socket is missing", { exact: true })).toBeVisible();
  expect(harness.godexOpens()).toBe(0);
});

test("a link bound to another node cannot submit to the current node", async ({ page }) => {
  await setup(page);
  await page.goto("/chat/codex/native-thread?codex_node=another-node");
  await expect(page.getByText("会话属于另一个节点", { exact: true })).toBeVisible();
  await expect(page.locator(".codex-conversation textarea")).toHaveCount(0);
});

test("setup shows the discovered desktop binary instead of assuming a shell PATH", async ({ page }) => {
  const executable = "/Applications/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex";
  const harness = await setup(page, false, executable);
  await page.goto("/chat/codex/connect?codex_node=local");
  await expect(page.getByText(`'${executable}' app-server daemon start`, { exact: true })).toBeVisible();
  await expect(page.getByText(`'${executable}' --remote 'unix:///tmp/codex-control.sock'`, { exact: true })).toBeVisible();
  expect(harness.godexOpens()).toBe(0);
  expect(harness.submissions).toHaveLength(0);
});

test("live updates, approval resolution and reconnect reuse stable message identities", async ({ page }) => {
  const harness = await setup(page);
  await page.goto("/chat/codex/native-thread?codex_node=local");
  await loadEarlier(page);
  await page.getByRole("button", { name: "实时查看", exact: true }).click();
  await expect(page.getByText("执行中", { exact: true })).toBeVisible();
  await page.evaluate(() => {
    const streams = (window as unknown as { __codexStreams: { controllers: ReadableStreamDefaultController<Uint8Array>[] } }).__codexStreams;
    for (const event of [
      { method: "item/agentMessage/delta", params: { threadId: "native-thread", turnId: "native-turn", itemId: "reply", delta: "，实时更新" } },
      { method: "codex/approval", params: { id: "epoch:77", method: "item/commandExecution/requestApproval", params: { threadId: "native-thread", command: "go test ./..." } } },
    ]) streams.controllers.at(-1)!.enqueue(new TextEncoder().encode(`data: ${JSON.stringify(event)}\n\n`));
  });
  await expect(page.getByText("正在检查原生任务，实时更新", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "仅本次允许" })).toBeDisabled();
  await page.evaluate(() => {
    const streams = (window as unknown as { __codexStreams: { controllers: ReadableStreamDefaultController<Uint8Array>[] } }).__codexStreams;
    streams.controllers.at(-1)!.enqueue(new TextEncoder().encode(`data: ${JSON.stringify({ method: "serverRequest/resolved", params: { threadId: "native-thread", requestId: 77 } })}\n\n`));
  });
  await expect(page.getByRole("button", { name: "仅本次允许" })).toHaveCount(0);
  await page.evaluate(() => (window as unknown as { __codexStreams: { controllers: ReadableStreamDefaultController<Uint8Array>[] } }).__codexStreams.controllers.at(-1)!.close());
  await expect.poll(() => page.evaluate(() => (window as unknown as { __codexStreams: { opened: number } }).__codexStreams.opened)).toBeGreaterThan(1);
  await expect(page.getByText("原生端发送的任务", { exact: true })).toHaveCount(1);
  // Backscroll must survive snapshot rehydration after reconnect.
  await expect(page.getByText("最早的一条历史消息", { exact: true })).toHaveCount(1);
  expect(harness.submissions).toHaveLength(0);
});
