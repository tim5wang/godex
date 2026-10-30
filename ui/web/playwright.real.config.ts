import { defineConfig } from "@playwright/test";

/**
 * Config for specs that drive a REAL godex server serving the built SPA, rather than
 * the canned e2e/fixtures backend or the Vite dev server.
 *
 * Start the server first (see e2e/composer-draft.spec.ts), then:
 *   cd ui/web && npx playwright test composer-draft --config playwright.real.config.ts
 */
export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false,
  workers: 1,
  timeout: 90_000,
  reporter: [["list"]],
  use: {
    baseURL: process.env.GODEX_E2E_BASE_URL ?? "http://127.0.0.1:8080",
    viewport: { width: 1440, height: 900 },
    browserName: "chromium",
  },
});
