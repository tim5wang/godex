import { test, expect } from "@playwright/test";
import { mockBackend } from "./fixtures";

/**
 * Nodes page layout contract.
 *
 * The page used to open with two onboarding forms and a warning banner ABOVE the
 * node list, so every visit started with scrolling past forms to reach the thing
 * you came for. These tests pin the three rules that fix it, because each one is
 * easy to regress by "just adding a card":
 *
 *   1. the list comes first; node onboarding is a collapsed section,
 *   2. each node is ONE compact card whose colour is its status,
 *   3. the status colour is derived from heartbeat AND relay — a green node must
 *      actually be openable remotely.
 *
 * The backend is mocked (as in the other e2e specs), so this covers the layout,
 * not the API.
 */
const MINUTE = 60_000;

const NODES = [
  {
    id: "node-new",
    name: "新机器",
    status: "online",
    relay_status: "connected",
    version: "v1.5.0",
    endpoint: "http://10.0.0.9:8080",
    workspace_dir: "/srv/godex-new",
    last_seen: new Date(Date.now() - 3 * MINUTE).toISOString(),
    capabilities: ["shell", "files", "terminal", "browser"],
  },
  {
    id: "node-degraded",
    name: "半挂机器",
    status: "online",
    relay_status: "disconnected",
    version: "v1.4.0",
    endpoint: "http://10.0.0.8:8080",
    workspace_dir: "/srv/godex-degraded",
    last_seen: new Date(Date.now() - 6 * 60 * MINUTE).toISOString(),
    capabilities: ["shell"],
  },
  {
    id: "node-old",
    name: "老机器",
    status: "offline",
    relay_status: "disconnected",
    version: "v1.3.0",
    endpoint: "http://10.0.0.7:8080",
    workspace_dir: "/srv/godex-old",
    last_seen: new Date(Date.now() - 40 * 60 * MINUTE).toISOString(),
    capabilities: ["shell"],
  },
];

async function openNodesPage(page: import("@playwright/test").Page) {
  // Pin the locale: the assertions below read the Chinese copy, and the i18n
  // parity test only proves the keys resolve — not which string a component picks.
  await page.addInitScript(() => window.localStorage.setItem("godex:web:locale", "zh"));
  await mockBackend(page);
  await page.route("**/api/control/nodes*", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(NODES) });
  });
  await page.route("**/api/config*", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ effective_values: {} }) });
  });
  await page.goto("/nodes");
  await page.waitForSelector(".node-card", { timeout: 10_000 });
}

test("lists nodes first, above a collapsed onboarding section", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await openNodesPage(page);

  await expect(page.locator(".node-card")).toHaveCount(3);

  // The settings section is present (its copy is reachable) but shut.
  const settings = page.locator(".node-settings-collapse");
  await expect(settings).toContainText("节点设置");
  await expect(page.locator(".node-settings-collapse .ant-collapse-panel-active")).toHaveCount(0);

  // ...and it takes a header row's worth of space, not a screen's: the first node
  // card must be visible without scrolling.
  const settingsBox = await settings.boundingBox();
  const firstCard = await page.locator(".node-card").first().boundingBox();
  expect(settingsBox!.height).toBeLessThan(120);
  expect(firstCard!.y).toBeGreaterThan(settingsBox!.y);
  expect(firstCard!.y).toBeLessThan(900);

  // Opening it brings the onboarding cards in — nothing was deleted, only folded.
  await page.locator(".node-settings-collapse .ant-collapse-header").click();
  await expect(page.locator(".node-settings-collapse .ant-collapse-panel-active")).toHaveCount(1);
  await expect.poll(async () => (await settings.boundingBox())!.height).toBeGreaterThan(200);
});

test("colours each card by readiness: healthy, degraded, offline", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await openNodesPage(page);

  await expect(page.locator(".node-card--healthy")).toHaveCount(1);
  await expect(page.locator(".node-card--degraded")).toHaveCount(1);
  await expect(page.locator(".node-card--offline")).toHaveCount(1);

  // The online-but-unrelayed node must NOT read as plainly online, since remote
  // chat/terminal/files need the relay.
  await expect(page.locator(".node-card--degraded")).toContainText("降级");
  await expect(page.locator(".node-card--healthy")).toContainText("在线");
  await expect(page.locator(".node-card--offline")).toContainText("离线");

  // Sort: usable first, then degraded, then offline.
  await expect(page.locator(".node-card").first()).toContainText("新机器");
  await expect(page.locator(".node-card").nth(1)).toContainText("半挂机器");
  await expect(page.locator(".node-card").last()).toContainText("老机器");

  // Compact: name + one status tag + id + a two-column meta grid + capped caps.
  const card = await page.locator(".node-card").first().boundingBox();
  expect(card!.height).toBeLessThan(220);
});

test("filters by search text and by status", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await openNodesPage(page);

  await page.getByPlaceholder("搜索名称 / ID / 地址").fill("10.0.0.8");
  await expect(page.locator(".node-card")).toHaveCount(1);
  await expect(page.locator(".node-card").first()).toContainText("半挂机器");

  await page.getByPlaceholder("搜索名称 / ID / 地址").fill("nothing-matches-this");
  await expect(page.locator(".node-card")).toHaveCount(0);
  await expect(page.getByText("没有匹配的节点")).toBeVisible();

  // Clearing the query brings the list back, and the status filter narrows it.
  await page.getByPlaceholder("搜索名称 / ID / 地址").fill("");
  await expect(page.locator(".node-card")).toHaveCount(3);
  await page.getByText("离线 1", { exact: true }).click();
  await expect(page.locator(".node-card")).toHaveCount(1);
  await expect(page.locator(".node-card").first()).toContainText("老机器");
});

test("keeps one column of compact cards on a phone", async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 812 });
  await openNodesPage(page);

  const boxes = await page.locator(".node-card").evaluateAll((els) =>
    els.map((el) => {
      const rect = el.getBoundingClientRect();
      return { top: Math.round(rect.top), width: Math.round(rect.width) };
    }),
  );
  // Stacked, not side by side: every card is roughly the full container width.
  const widths = new Set(boxes.map((b) => b.width));
  expect(widths.size).toBe(1);
});
