import { test, expect } from "@playwright/test";
import { mockBackend } from "./fixtures";

/**
 * The work desktop must be reachable through a node's remote access.
 *
 * The node detail page's remote-action buttons listed chat / terminal / files /
 * taskboard / business-agents — but not the desktop, even though the desktop's own
 * API (/work-contexts) is node-scoped, already proxies through the center (the
 * client's nodeProxyPath is a blacklist that leaves only the center's own paths
 * local), and its data layer already keys every query by nodeID. So the only
 * missing piece was the ENTRY, and this pins it end to end: node → open work
 * desktop → the chat page with the left rail switched to the desktop view and the
 * remote banner naming the node.
 *
 * Kept in its own file rather than appended to nodes-page.spec.ts: that file is a
 * separate, in-flight change set, and a test about the work desktop's remote entry
 * does not belong to it.
 */
const NODE = {
  id: "node-new",
  name: "新机器",
  status: "online",
  relay_status: "connected",
  version: "v1.5.0",
  endpoint: "http://10.0.0.9:8080",
  workspace_dir: "/srv/godex-new",
  last_seen: new Date().toISOString(),
  capabilities: ["shell", "files", "terminal", "browser"],
};

const OVERVIEW = {
  node: NODE,
  overview: { node_id: "node-new", version: "v1.5.0", capabilities: ["shell", "files"], sessions: [], jobs: [], approvals: [], recent_events: [] },
};

test("opens the node's work desktop remotely from the detail page", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.addInitScript(() => window.localStorage.setItem("godex:web:locale", "zh"));
  await mockBackend(page);
  await page.route("**/api/config*", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ effective_values: {} }) });
  });
  await page.route("**/api/control/nodes/node-new/overview", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(OVERVIEW) });
  });
  // Once a node is active EVERY desktop request carries the center proxy prefix, and
  // the desktop's list consumers call array methods on the payload
  // (`(resourcesQuery.data ?? []).filter(...)`), so the generic `{}` fallback would
  // crash the page instead of rendering an empty desktop — hiding the entry this
  // test is about behind an unrelated error boundary.
  await page.route("**/proxy/v1/work-contexts**", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: "[]" });
  });
  await page.route("**/proxy/v1/work-context-resources**", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: "[]" });
  });
  await page.route("**/proxy/v1/work-attention**", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ items: [], unread: {}, counts: {}, deferred: [] }) });
  });

  await page.goto("/nodes/node-new");
  const button = page.getByRole("button", { name: "打开工作桌面" });
  await expect(button).toBeVisible();
  await button.click();

  // The desktop has no route of its own — it is a VIEW of the chat's left rail — so
  // "open it" means landing on the chat with the rail switched there, plus the
  // remote banner naming the node (proving we did not silently fall back to local).
  await expect(page.locator(".chat-v2-left")).toHaveAttribute("data-left-view", "desktop");
  await expect(page.getByTestId("chat-v2-workdesk")).toBeVisible();
  await expect(page.locator(".chat-v2-topbar-remote-label")).toHaveText("新机器");
});
