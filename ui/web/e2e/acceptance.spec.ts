import { test, expect } from "@playwright/test";
import { mockBackend } from "./fixtures";

async function resetBrowserState(page: import("@playwright/test").Page) {
  await page.goto("/");
  await page.evaluate(() => {
    window.localStorage.clear();
    window.sessionStorage.clear();
  });
}

const DOCK_TAB_IDS: Record<string, string> = {
  Files: "files",
  Terminal: "terminal",
  "Work desktop": "workdesk",
  Preview: "preview",
  Status: "status",
  Browser: "browser",
};

/**
 * Open a right-dock tab and wait for its pane.
 *
 * Queries the ALWAYS-PRESENT `.chat-v2-right` to read state, rather than a locator scoped
 * to the target tab: `[data-active-tab="terminal"]` only matches once that tab is
 * already active, so waiting on it before clicking deadlocks (the element it needs is
 * the one the click would create).
 *
 * Reads state before clicking because the tab's click TOGGLES: clicking an
 * already-active tab collapses the dock, which would fail the assertion below for a
 * reason unrelated to the feature under test.
 */
async function openDock(page: import("@playwright/test").Page, label: keyof typeof DOCK_TAB_IDS) {
  const id = DOCK_TAB_IDS[label]!;
  const panel = page.locator(".chat-v2-right").first();
  const active = await panel.getAttribute("data-active-tab");
  const collapsed = await panel.getAttribute("data-collapsed");
  if (active !== id || collapsed !== "false") {
    await page.locator(`.chat-v2-topbar-tab[aria-label="${label}"]`).click();
  }
  await expect(panel).toHaveAttribute("data-active-tab", id);
  await expect(panel).toHaveAttribute("data-collapsed", "false");
  return panel;
}

/**
 * M0 acceptance checklist, rewritten against the CURRENT shell.
 *
 * The previous revision asserted against `CenterGrid` — the 2×2/3×3 grid that the
 * work-desktop fusion retired. That component is now an orphan (nothing imports it),
 * so those assertions could not pass no matter how healthy the app was: the failures
 * were a stale spec, not a broken page. This revision tests the shell that actually
 * ships: AppNav + chat centre + right dock + left rail (chat v2).
 *
 * It also covers a real defect found while doing it: the mobile nav Drawer defaulted
 * to OPEN (it read the PC inspector flag, which starts expanded), covering the app —
 * and because the mask swallows clicks, the dock strip underneath was untappable.
 */
test.describe("M0 Acceptance Checklist — PC (Desktop Chrome 1440×900)", () => {
  test.beforeEach(async ({ page }) => {
    // These tests require a desktop viewport. Skip on mobile.
    const viewport = page.viewportSize();
    if (!viewport || viewport.width < 1024) {
      test.skip();
    }
    await mockBackend(page);
    await resetBrowserState(page);
    await page.goto("/chat");
    // Wait for the main layout to render.
    await page.waitForSelector(".godex-sider", { timeout: 10000 });
  });

  test("Desktop Status panel does not add a redundant close button", async ({ page }) => {
    await page.getByRole("button", { name: "Status", exact: true }).click();
    const panel = page.locator('.chat-v2-right[data-active-tab="status"]');
    await expect(panel).toHaveAttribute("data-collapsed", "false");
    await expect(panel.getByRole("button", { name: "Close status panel" })).toHaveCount(0);
  });

  test("AppNav is visible and shows navigation menu items", async ({ page }) => {
    // Verify the app navigation sidebar exists.
    const sider = page.locator(".godex-sider");
    await expect(sider).toBeVisible();

    // Verify at least one menu item is visible (antd Menu.Item).
    const menuItems = page.locator(".ant-menu-item");
    const count = await menuItems.count();
    expect(count).toBeGreaterThan(0);
  });

  test("AppNav collapses to icon-only strip on toggle button click", async ({ page }) => {
    // Click the collapse/expand toggle button.
    const toggleBtn = page.locator('.godex-sider-top button[aria-label*="Collapse"]');
    await toggleBtn.click();
    await page.waitForTimeout(300);

    // After collapse, the sider should have collapsed class or reduced width.
    const sider = page.locator(".godex-sider");
    const box = await sider.boundingBox();
    expect(box).not.toBeNull();
    // Collapsed width should be around 48px.
    expect(box!.width).toBeLessThan(80);
  });

  test("Chat workspace area renders the main content", async ({ page }) => {
    // The chat is the centre column of the shell, so "did the page render" is
    // answered by the feed area plus a usable composer.
    const content = page.locator(".godex-content");
    await expect(content).toBeVisible({ timeout: 5000 });
    await expect(page.locator(".chat-composer")).toBeVisible({ timeout: 5000 });
    await page.waitForTimeout(1000);
    await expect(content).not.toContainText(/crashed|Maximum update depth exceeded|Element type is invalid/i);
  });

  test("Right dock opens the Files panel as a side-by-side tree and preview", async ({ page }) => {
    await openDock(page, "Files");
    const tree = page.locator('[data-testid="files-panel-tree"]');
    const preview = page.locator('[data-testid="files-panel-preview-pane"]');
    await expect(tree).toBeVisible({ timeout: 5000 });
    await expect(preview).toBeVisible({ timeout: 5000 });

    // Side by side, not stacked: the dock is the only home these panels have now.
    const treeBox = await tree.boundingBox();
    const previewBox = await preview.boundingBox();
    expect(treeBox).not.toBeNull();
    expect(previewBox).not.toBeNull();
    expect(treeBox!.x + treeBox!.width).toBeLessThanOrEqual(previewBox!.x + 4);
    expect(Math.abs(treeBox!.y - previewBox!.y)).toBeLessThan(48);
  });

  test("Files panel previews the selected file", async ({ page }) => {
    await openDock(page, "Files");
    await page.getByText("SPEC.md").first().click();
    await expect(page.locator('[data-testid="files-panel-preview-path"]')).toContainText("SPEC.md");
    await expect(page.locator('[data-testid="files-panel-preview"]')).toContainText("GoDex Web UI");
  });

  test("Files panel tree collapses to a compact rail and restores", async ({ page }) => {
    await openDock(page, "Files");
    const tree = page.locator('[data-testid="files-panel-tree"]');
    await expect(tree).toBeVisible({ timeout: 5000 });

    await page.locator('[data-testid="files-panel-tree-collapse"]').click();
    await expect(page.locator('[data-testid="files-panel-tree-collapsed"]')).toBeVisible({ timeout: 3000 });
    await expect(tree).toHaveCount(0);

    // The preview must NOT go with it: collapsing the tree is about reclaiming width,
    // and losing the file you are reading to do that would be a bug.
    await expect(page.locator('[data-testid="files-panel-preview-pane"]')).toBeVisible();

    await page.locator('[data-testid="files-panel-tree-collapsed"] button').click();
    await expect(tree).toBeVisible({ timeout: 3000 });
  });

  test("Terminal dock reports connection status", async ({ page }) => {
    await openDock(page, "Terminal");
    await expect(page.locator('[data-testid="terminal-status"]')).toContainText(/connected|ready/i, { timeout: 7000 });
  });

  test("Right dock collapses and restores", async ({ page }) => {
    const panel = await openDock(page, "Files");
    await page.getByRole("button", { name: "Toggle right panel" }).click();
    await expect(panel).toHaveAttribute("data-collapsed", "true");
    await page.getByRole("button", { name: "Toggle right panel" }).click();
    await expect(panel).toHaveAttribute("data-collapsed", "false");
  });

  test("Left rail collapses to a compact strip and expands", async ({ page }) => {
    const rail = page.locator(".chat-v2-left");
    await expect(rail).toHaveAttribute("data-collapsed", "false");

    const toggle = page.getByRole("button", { name: "Toggle sidebar" });
    await toggle.click();
    await expect(rail).toHaveAttribute("data-collapsed", "true");
    const collapsedBox = await rail.boundingBox();
    expect(collapsedBox).not.toBeNull();
    expect(collapsedBox!.width).toBeLessThan(80);

    await toggle.click();
    await expect(rail).toHaveAttribute("data-collapsed", "false");
    const expandedBox = await rail.boundingBox();
    expect(expandedBox).not.toBeNull();
    expect(expandedBox!.width).toBeGreaterThan(120);
  });

  test("Layout persists collapsed state across reload", async ({ page }) => {
    // Collapse AppNav.
    const toggleBtn = page.locator('.godex-sider-top button[aria-label*="Collapse"]');
    await toggleBtn.click();
    await page.waitForTimeout(300);

    // Reload the page.
    await page.reload();
    await page.waitForSelector(".godex-sider", { timeout: 10000 });

    // After reload, AppNav should still be collapsed.
    const sider = page.locator(".godex-sider");
    const box = await sider.boundingBox();
    expect(box!.width).toBeLessThan(80);
  });

  test("Chat rail and dock collapsed state persists across reload", async ({ page }) => {
    // Both rails are chat-v2 state with its own storage key; the AppNav assertion above
    // would not catch a regression in this one.
    await openDock(page, "Files");
    await page.getByRole("button", { name: "Toggle sidebar" }).click();
    await expect(page.locator(".chat-v2-left")).toHaveAttribute("data-collapsed", "true");
    await page.getByRole("button", { name: "Toggle right panel" }).click();
    await expect(page.locator(".chat-v2-right")).toHaveAttribute("data-collapsed", "true");

    await page.reload();
    await page.waitForSelector(".godex-sider", { timeout: 10000 });

    await expect(page.locator(".chat-v2-left")).toHaveAttribute("data-collapsed", "true");
    await expect(page.locator(".chat-v2-right")).toHaveAttribute("data-collapsed", "true");
  });
});

test.describe("M0 Acceptance Checklist — Mobile (Pixel 7 375×812)", () => {
  test.beforeEach(async ({ page }) => {
    // These tests require a mobile viewport. Skip on desktop.
    const viewport = page.viewportSize();
    if (!viewport || viewport.width >= 1024) {
      test.skip();
    }
    await mockBackend(page);
    await resetBrowserState(page);
    await page.goto("/chat");
    // Wait for the mobile layout to render.
    await page.waitForSelector(".godex-header", { timeout: 10000 });
  });

  test("Mobile hamburger menu button is visible", async ({ page }) => {
    // On mobile, the hamburger button should be present in the header.
    const hamburger = page.locator('.godex-header button[aria-label*="Open navigation"]');
    await expect(hamburger).toBeVisible({ timeout: 5000 });
  });

  test("Mobile nav drawer starts closed so the app is usable on load", async ({ page }) => {
    // Regression guard for the real defect: the mobile Drawer used to read the PC
    // inspector flag (which defaults to OPEN), so the app loaded behind a full-screen
    // nav overlay whose mask swallowed every tap on the content underneath.
    await expect(page.locator(".ant-drawer").first()).toBeHidden({ timeout: 3000 });
    await expect(page.locator(".chat-v2-center-wrap")).toBeVisible();

    // And it still opens on demand.
    await page.locator('.godex-header button[aria-label*="Open navigation"]').click();
    await expect(page.locator('[data-testid="mobile-appnav-drawer"]')).toBeVisible({ timeout: 3000 });
  });

  test("Every mobile dock provides a reachable Back to chat action", async ({ page }) => {
    // "Tasks" is not in this list because that dock is no longer reachable from the
    // icon strip (it duplicated the desktop's delegation board and was removed).
    for (const dock of ["Files", "Terminal", "Preview", "Status"]) {
      const dockTab = page.getByRole("button", { name: dock, exact: true });
      await expect(dockTab, `${dock} button should not be clipped`).toBeVisible({ timeout: 5000 });
      await expect(dockTab).toBeInViewport();
      await dockTab.click();

      const overlay = page.locator(`.chat-v2-right[data-active-tab="${dock.toLowerCase()}"]`);
      await expect(overlay).toHaveAttribute("data-collapsed", "false");
      const headerBox = await page.locator(".godex-header").boundingBox();
      const overlayBox = await overlay.boundingBox();
      expect(headerBox).not.toBeNull();
      expect(overlayBox).not.toBeNull();
      expect(overlayBox!.y).toBeGreaterThanOrEqual(headerBox!.y + headerBox!.height - 1);
      const activePane = overlay.locator('.chat-v2-dock-tab-pane[data-active="true"]');
      const activePaneBox = await activePane.boundingBox();
      expect(activePaneBox).not.toBeNull();
      expect(activePaneBox!.y).toBeGreaterThanOrEqual(headerBox!.y + headerBox!.height - 1);

      const topControl = dock === "Files"
        ? activePane.locator('input[placeholder]').first()
        : dock === "Preview"
          ? activePane.getByRole("textbox", { name: "Preview address" })
          : null;
      if (topControl) {
        await expect(topControl, `${dock} top control should be fully visible`).toBeVisible();
        await expect(topControl).toBeInViewport();
        const controlBox = await topControl.boundingBox();
        expect(controlBox).not.toBeNull();
        expect(controlBox!.y).toBeGreaterThanOrEqual(headerBox!.y + headerBox!.height - 1);
      }

      const back = page.locator(".godex-header").getByRole("button", { name: "Back to chat" });
      await expect(back).toBeVisible();
      await expect(back).toBeInViewport();
      await expect(back.locator(".godex-header-dock-name")).toHaveText(dock);
      await back.click();

      await expect(overlay).toHaveAttribute("data-collapsed", "true");
      await expect(page.locator(".chat-v2-center-wrap")).toBeVisible();
    }
  });

  test("Mobile hamburger opens Drawer with navigation menu", async ({ page }) => {
    const hamburger = page.locator('.godex-header button[aria-label*="Open navigation"]');
    await hamburger.click();
    await page.waitForTimeout(500);

    // The Drawer should now be open.
    const drawer = page.locator('[data-testid="mobile-appnav-drawer"]');
    await expect(drawer).toBeVisible({ timeout: 3000 });
  });
});

test.describe("M0 Acceptance Checklist — Cross-cutting", () => {
  test.beforeEach(async ({ page }) => {
    await mockBackend(page);
    await resetBrowserState(page);
  });

  async function expectNoRouteCrash(page: import("@playwright/test").Page) {
    await expect(page.locator(".godex-content")).toBeVisible();
    await page.waitForTimeout(1000);
    await expect(page.locator(".godex-content")).not.toContainText(/crashed|Maximum update depth exceeded|Element type is invalid/i);
  }

  test("Files route renders without crashing", async ({ page }) => {
    await page.goto("/files");
    await page.waitForSelector(".godex-content", { timeout: 10000 });
    await expectNoRouteCrash(page);
  });

  test("Settings route renders without crashing", async ({ page }) => {
    await page.goto("/settings");
    await page.waitForSelector(".godex-content", { timeout: 10000 });
    await expectNoRouteCrash(page);
  });

  test("Memory route renders without crashing", async ({ page }) => {
    await page.goto("/memory");
    await page.waitForSelector(".godex-content", { timeout: 10000 });
    await expectNoRouteCrash(page);
  });

  test("All builtin app routes render without crash", async ({ page }) => {
    const routes = ["/", "/chat", "/files", "/notes", "/skills", "/memory", "/settings", "/usage"];
    for (const route of routes) {
      await page.goto(route);
      await page.waitForSelector(".godex-content", { timeout: 10000 });
      await expectNoRouteCrash(page);
    }
  });
});
