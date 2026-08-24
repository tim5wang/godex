import { defineConfig } from "vite";

/**
 * Standalone build for the <godex-step> embeddable Web Component (Phase C).
 *
 * Produces a single self-contained IIFE bundle (godex-step.js) with no
 * external dependencies — `client.ts` and `godex-step.ts` are zero-dependency
 * pure TS, so the bundle is a few KB and can be dropped into any business page
 * with one <script> tag. Kept separate from the main app build (vite.config.ts
 * → internal/uiassets/embedded_dist), which it must never disturb.
 *
 *   npm run build:embed   →   dist/embed/godex-step.js
 */
export default defineConfig({
  build: {
    lib: {
      entry: "src/lib/agent-step/godex-step.ts",
      name: "GodexStep",
      formats: ["iife"],
      fileName: () => "godex-step.js",
    },
    outDir: "dist/embed",
    emptyOutDir: true,
    minify: false, // readable demo bundle; minify when shipping
  },
});
