import type { Page, Route } from "@playwright/test";

/**
 * Mock the GoDex backend API so the SPA loads without a real server.
 * Returns a handler that intercepts API calls and returns canned
 * responses covering the full acceptance checklist.
 */
export async function mockBackend(page: Page): Promise<void> {
  // Intercept every API call and return appropriate canned responses.
  await page.route("**/api/**", handleAPIRoute);
  await page.route("**/v1/**", handleV1Route);
}

async function handleAPIRoute(route: Route): Promise<void> {
  const url = new URL(route.request().url());
  const path = url.pathname.replace(/^\/api/, "");

  switch (true) {
    case path === "/meta" || path === "/meta/":
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          lead_name: "Playwright Test",
          model: "gpt-4",
          workspace_dir: "/tmp/godex-playwright",
          auth_required: false,
          version: "test",
        }),
      });
      return;

    case path === "/providers" || path === "/providers/":
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ providers: [] }),
      });
      return;

    case path === "/sessions" || path.startsWith("/sessions/"):
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify([]),
      });
      return;

    case path === "/memory" || path.startsWith("/memory/"):
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify([]),
      });
      return;

    case path === "/notes" || path.startsWith("/notes/"):
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify([]),
      });
      return;

    case path === "/config" || path.startsWith("/config/"):
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          lead_name: "Playwright Test",
          model: "gpt-4",
          workspace_dir: "/tmp/godex-playwright",
          auth_required: false,
        }),
      });
      return;

    case path === "/packages" || path.startsWith("/packages/"):
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify([]),
      });
      return;

    case path === "/skills/catalog" || path.startsWith("/skills/"):
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify([]),
      });
      return;

    case path === "/automation" || path.startsWith("/automation/"):
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({}),
      });
      return;

    case path === "/channels" || path.startsWith("/channels/"):
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({}),
      });
      return;

    case path === "/security" || path.startsWith("/security/"):
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({}),
      });
      return;

    case path === "/usage" || path.startsWith("/usage/"):
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({}),
      });
      return;

    default:
      // For unknown API calls, return empty 200 to avoid app crashes.
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({}),
      });
  }
}

async function handleV1Route(route: Route): Promise<void> {
  const url = new URL(route.request().url());
  const path = url.pathname.replace(/^\/v1/, "");

  if (path === "/models" || path === "/models/") {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ object: "list", data: [] }),
    });
    return;
  }

  // Terminal routes — return empty success.
  await route.fulfill({
    status: 200,
    contentType: "application/json",
    body: JSON.stringify({}),
  });
}
