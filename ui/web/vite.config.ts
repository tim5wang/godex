import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: '../../internal/uiassets/embedded_dist',
    emptyOutDir: true,
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (!id.includes("node_modules")) {
            return;
          }
          if (id.includes("react-router") || id.includes("@remix-run")) {
            return "vendor-router";
          }
          if (id.includes("@tanstack/react-query")) {
            return "vendor-query";
          }
          if (id.includes("react-markdown") || id.includes("remark-gfm") || id.includes("remark-breaks") || id.includes("unified") || id.includes("mdast") || id.includes("micromark")) {
            return "vendor-markdown";
          }
          if (id.includes("@ant-design/x")) {
            return "vendor-antd-x";
          }
          if (id.includes("antd") || id.includes("@ant-design") || id.includes("@rc-component") || id.includes("rc-")) {
            return "vendor-antd";
          }
          if (id.includes("zustand")) {
            return "vendor-state";
          }
          if (id.includes("react") || id.includes("scheduler")) {
            return "vendor-react";
          }
          if (id.includes("@codemirror") || id.includes("@lezer")) {
            return "vendor-codemirror";
          }
        },
      },
    },
  },
  server: {
    port: 5173,
    proxy: {
      "/api": "http://127.0.0.1:8080"
    }
  }
});
