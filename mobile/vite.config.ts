import { defineConfig } from "vite";

// 壳层静态页构建：src/main.ts -> www/（Capacitor webDir）
export default defineConfig({
  root: ".",
  build: {
    outDir: "www",
    emptyOutDir: true,
    target: "es2022",
  },
  server: {
    port: 5174,
  },
});
