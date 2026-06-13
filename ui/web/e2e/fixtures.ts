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
        body: JSON.stringify({
          providers: [
            {
              id: "test-provider",
              name: "Test Provider",
              has_credential: true,
              token_present: false,
            },
          ],
        }),
      });
      return;

    case path === "/models" || path.startsWith("/models?"):
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ default_profile_id: "", profiles: [] }),
      });
      return;

    case path === "/sessions" || path === "/sessions/":
      if (route.request().method() === "POST") {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            session_id: "playwright-session",
            locator: { channel: "web", key: "playwright-session" },
          }),
        });
        return;
      }
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify([]),
      });
      return;

    case path === "/sessions/playwright-session" || path === "/sessions/playwright-session/":
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          session_id: "playwright-session",
          locator: { channel: "web", key: "playwright-session" },
          messages: [],
          display_messages: [],
          timeline: [],
          turns: [],
          queued_turns: [],
          pending_permissions: [],
          running: false,
          updated_at: "2026-06-13T00:00:00Z",
        }),
      });
      return;

    case path.startsWith("/sessions/playwright-session/timeline/page"):
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ items: [], has_more: false, total: 0 }),
      });
      return;

    case path.startsWith("/sessions/playwright-session/timeline"):
    case path.startsWith("/sessions/playwright-session/subagents"):
    case path.startsWith("/sessions/playwright-session/longtasks"):
    case path.startsWith("/sessions/playwright-session/skills"):
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify([]),
      });
      return;

    case path === "/sessions/playwright-session/context-inspector":
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          message_count: 0,
          token_estimate: 0,
          compress_threshold: 0,
          suggest_compact: false,
          active_skill_count: 0,
          pending_permission_count: 0,
        }),
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

    case path === "/config/meta" || path === "/config/meta/":
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ source: "test", warnings: [], errors: [] }),
      });
      return;

    case path === "/config/schema" || path === "/config/schema/":
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify([]),
      });
      return;

    case path === "/config/doctor" || path === "/config/doctor/":
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          generated_at: "2026-06-13T00:00:00Z",
          errors: 0,
          warnings: 0,
          infos: 0,
          checks: [],
        }),
      });
      return;

    case path === "/config" || path === "/config/":
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          file_path: "/tmp/godex-playwright/config.yaml",
          env_file: "/tmp/godex-playwright/.env",
          revision: 1,
          stored_values: {},
          effective_values: {},
          fields: {},
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

    case path.startsWith("/files/list"):
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          items: [
            { name: "SPEC.md", path: "SPEC.md", isDir: false, size: 12000, modTime: "2026-06-13T00:00:00Z" },
            { name: "Makefile", path: "Makefile", isDir: false, size: 800, modTime: "2026-06-13T00:00:00Z" },
            { name: "ui", path: "ui", isDir: true, size: 0, modTime: "2026-06-13T00:00:00Z" },
          ],
        }),
      });
      return;

    case path.startsWith("/files/read"):
      {
        const filePath = url.searchParams.get("path") || "SPEC.md";
        const content = filePath === "Makefile"
          ? "ui-web:\n\tpnpm --dir ui/web run dev\n"
          : "# GoDex Web UI\n\nMock SPEC content for Playwright.\n";
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            path: filePath,
            content,
            size: content.length,
          }),
        });
      }
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

    case path === "/usage/keys" || path === "/usage/keys/":
    case path === "/usage/models" || path === "/usage/models/":
    case path.startsWith("/usage/summary"):
    case path.startsWith("/usage/calls"):
    case path.startsWith("/usage/time-series"):
    case path.startsWith("/usage/sessions"):
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify([]),
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

  if (path === "/v1/terminal/create" || path === "/v1/terminal/create/") {
    await route.fulfill({
      status: 201,
      contentType: "application/json",
      body: JSON.stringify({ terminalId: "term-playwright", initialCursor: 0 }),
    });
    return;
  }

  // Match any /v1/terminal/:id/output requests (id is random client-side).
  if (/^\/v1\/terminal\/[^/]+\/output$/.test(path)) {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        terminalId: "term-playwright",
        cursor: 18,
        data: "GoDex terminal ready\n",
        exited: false,
      }),
    });
    return;
  }

  if (/^\/v1\/terminal\/[^/]+\/input$/.test(path)) {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ terminalId: "term-playwright", accepted: true }),
    });
    return;
  }

  if (/^\/v1\/terminal\/[^/]+$/.test(path)) {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ terminalId: "term-playwright", exited: true }),
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
