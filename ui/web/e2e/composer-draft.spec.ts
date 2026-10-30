import { test, expect } from "@playwright/test";

/**
 * Regression: an unsent composer draft must survive leaving the chat view.
 *
 * THE BUG — the draft-load and draft-save effects run in the SAME commit. The load
 * effect marks "restoring" and reads the stored draft; the save effect then ran in
 * that same pass with the pre-update state, saw an empty input, and DELETED the
 * stored draft. The restored text still appeared in the box, so the loss was
 * invisible until the NEXT visit — "I typed something, left the chat, came back and
 * it was empty".
 *
 * HARNESS — this spec drives a REAL godex process serving the built SPA, and only
 * stubs `/api/providers` (a fresh store has no provider, which gates the whole chat
 * UI behind "Configure a model provider"). It deliberately does NOT use e2e/fixtures:
 * that canned backend leaves /chat on an error boundary, and Vite dev mode re-renders
 * the pane constantly, which Playwright reports as "element detached from the DOM".
 *
 * Run with:
 *   make web                                   # build the SPA into the binary
 *   go build -o /tmp/godex ./cmd/godex
 *   GODEX_HOME=$(mktemp -d) /tmp/godex serve --addr 127.0.0.1:8080 &
 *   cd ui/web && npx playwright test composer-draft --config playwright.real.config.ts
 */
const BASE = process.env.GODEX_E2E_BASE_URL ?? "http://127.0.0.1:8080";

test.use({ baseURL: BASE, viewport: { width: 1440, height: 900 } });

test.describe("composer draft persistence", () => {
  test.beforeEach(async ({ page }) => {
    // Only the provider gate is stubbed; everything else is the real server.
    await page.route("**/api/providers**", (r) =>
      r.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          providers: [{ id: "e2e", name: "E2E", has_credential: true, token_present: false }],
        }),
      }),
    );

    await page.goto("/chat");
    await page.evaluate(() => window.localStorage.clear());
    await page.goto("/chat");
    await page.waitForSelector(".godex-sider", { timeout: 20000 });
    await page.waitForSelector(".chat-composer", { timeout: 20000 });
    await page.waitForTimeout(2000); // let the workspace panes settle
  });

  const input = (page: import("@playwright/test").Page) => page.locator(".chat-composer textarea").first();

  const leaveChat = async (page: import("@playwright/test").Page) => {
    await page.locator(".godex-sider .ant-menu-item").nth(2).click();
    await expect(page.locator(".chat-composer")).toHaveCount(0);
  };

  const backToChat = async (page: import("@playwright/test").Page) => {
    await page.locator(".godex-sider .ant-menu-item").nth(0).click();
    await page.waitForSelector(".chat-composer", { timeout: 20000 });
  };

  test("survives leaving the chat view and returning twice", async ({ page }) => {
    await input(page).fill("draft-that-must-survive");
    await page.waitForTimeout(800); // past the 400ms debounce

    // Two cycles on purpose. The stored draft was being deleted while the FIRST
    // return mounted, but that same return still re-populated the box from a value
    // it had already read — so one cycle hides the bug and only the second visit
    // shows it. One cycle would therefore be a test that cannot fail.
    await leaveChat(page);
    await backToChat(page);
    await leaveChat(page);
    await backToChat(page);

    await expect(input(page)).toHaveValue("draft-that-must-survive", { timeout: 15000 });
  });

  test("survives leaving the chat view inside the debounce window", async ({ page }) => {
    await input(page).fill("typed-then-left");

    // No settle: the save timer is still pending when the composer unmounts.
    await leaveChat(page);
    await backToChat(page);

    await expect(input(page)).toHaveValue("typed-then-left", { timeout: 15000 });
  });

  test("survives page reloads", async ({ page }) => {
    await input(page).fill("survives-reload");
    await page.waitForTimeout(800);

    // Twice, for the reason given in the two-cycle case above.
    await page.reload();
    await page.waitForSelector(".chat-composer", { timeout: 20000 });
    await page.reload();
    await page.waitForSelector(".chat-composer", { timeout: 20000 });

    await expect(input(page)).toHaveValue("survives-reload", { timeout: 15000 });
  });
});
