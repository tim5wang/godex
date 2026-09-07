import type { CapacitorConfig } from "@capacitor/cli";

const config: CapacitorConfig = {
  appId: "com.godex.mobile",
  appName: "godex Mobile",
  webDir: "www",

  // 壳层默认加载本地 www（设置页），用户配置 godex 服务地址后由原生层
  // 导航到目标 Web UI。不设置 server.url，避免 Capacitor 强制远程加载
  // 导致本地设置页不可用。
  server: {
    androidScheme: "https",
    cleartext: true, // 开发期允许 http 局域网地址；生产建议关闭并走 https
  },

  android: {
    allowMixedContent: true, // 开发期 http 服务；生产收紧
  },

  ios: {
    contentInset: "always",
  },
};

export default config;
