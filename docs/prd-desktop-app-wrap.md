# PRD: godex 桌面版 + App 版（轻量级系统浏览器内核包装）

> 状态：草案（调研产出，待评审）
> 日期：2026-09-05
> 关联：任务卡 t-1788517972939-2；godex v1.4.0
> 调研方式：官方文档核实（tauri.app、web.dev、MDN、npm registry）+ 领域知识交叉确认；当日 web_search 通道故障（见 docs/tools_issues.md），部分数据点以官方文档/既有知识为准

---

## 1. 背景

godex 当前形态：

- **单二进制**：Go 实现，Web UI（React/Vite）内嵌于 `internal/uiassets/embedded_dist`，单一可执行文件分发（`docs/project-structure.md`）。
- **本地 HTTP 服务**：`internal/runtime/webui.NewHandler` 服务静态资源，`/api`、`/v1` 前缀转发至 API handler，已启用 token 认证（`web.token`，热生效）。
- **已有 TUI**（Bubble Tea）与 Web UI 双入口，但两者均为「终端/浏览器内」体验。

**缺口**：

1. 桌面用户期望**应用级体验**：托盘常驻、全局快捷键唤起、系统通知、开机自启、独立窗口。
2. 移动场景（手机查看会话、接收 agent 完成通知）无入口。
3. 现有 Web UI 与后端已完整可用，**不应重写前端**，只缺一层原生壳。

**约束（轻量级）**：不引入重型运行时（如捆绑 Chromium）；优先复用系统浏览器内核（macOS WKWebView / Windows WebView2 / Linux WebKitGTK / iOS WKWebView / Android WebView）。

---

## 2. 目标

- **G1 桌面版**：macOS / Windows / Linux 三平台，系统内核包装 Web UI，单窗口 + 托盘常驻。
- **G2 原生能力**：系统通知、托盘菜单、全局快捷键、剪贴板、文件/目录访问、开机自启。
- **G3 移动版**：iOS / Android WebView 包装现有 Web UI；本地通知必达，远程推送（APNs/FCM）可选。
- **G4 成本约束**：后端与前端零改动或极小改动；壳层代码薄、可维护；包体远小于 Electron 方案。
- **G5 非目标**：不重写 Web UI 为原生 UI；不做应用商店强制上架（分发可选）。

---

## 3. 方案对比表

| 维度 | Tauri 2.x | Electron | 系统 WebView 包装（WKWebView / WebView2 / WebKitGTK） | PWA |
|---|---|---|---|---|
| **运行时** | 系统 WebView + Rust core | 捆绑 Chromium + Node | 纯系统 WebView（无捆绑） | 浏览器本身 |
| **包体大小** | 极小，安装包通常 **3–10 MB**（官方：tiny binaries） | 大，安装包通常 **60–150 MB**，解压后 200 MB+ | 最小（数百 KB–2 MB，仅壳代码） | 0（无安装包） |
| **内存/启动** | 低（复用系统内核） | 高（Chromium 常驻，100–500 MB 常见） | 最低 | 中（浏览器进程） |
| **开发语言** | Rust + 前端（Rust 有学习成本） | JS/TS（Node 生态） | 每平台原生：Swift / C# / C | Web 技术栈 |
| **三平台覆盖** | macOS/Win/Linux **+ iOS/Android（2.x 起）**，一套代码 | 桌面三平台（移动需另做） | 每平台单独写壳，无跨平台复用 | 三平台 + 移动，浏览器内 |
| **原生能力** | 官方插件：tray、notification、global-shortcut、clipboard、fs（capability 白名单）、autostart | 内置 API 最全：Tray、Notification、globalShortcut、clipboard、shell、dialog、app.setLoginItemSettings | 全能力可调（系统 API 直通），但全部手写 | 受限：通知 ✅、剪贴板 ✅、文件系统（File System Access API，仅 Chromium）❌ 托盘/全局快捷键 |
| **移动支持** | ✅ Tauri Mobile（iOS/Android） | ❌（需 Electron→Capacitor/重写） | 每平台原生壳 | ✅ 可安装（无商店分发则弱） |
| **生态/维护** | 活跃，2024-10 起 2.x 稳定，移动仍较新 | 极成熟，但体积/内存/供应链争议大 | 无框架依赖，全自维护 | 浏览器厂商主导，能力随版本 |
| **适合场景** | **本地服务包装 + 轻量桌面壳 + 原生能力** | 重客户端、深度 Node 集成 | 极简壳、能力全手写 | 免安装、低频桌面集成 |
| **主要风险** | Rust 构建链；WebView 版本差异；移动端新 | 体积/内存/合规 | 三平台三套代码、维护贵 | 能力边界受限、无托盘 |

**结论要点**：对「Go 后端 + 现有 Web UI + 本地 HTTP 服务」架构，**壳层只需加载本地 URL**——四方案后端均零改动。差异集中在壳层成本与原生能力覆盖。

---

## 4. 原生能力增强清单

| 能力 | Tauri 2.x | Electron | 系统 WebView 壳 | PWA | 备注/实现难度 |
|---|---|---|---|---|---|
| 系统通知 | ✅ 官方插件（含 macOS 权限弹窗处理） | ✅ Notification 内置 | ✅ 手写（UNUserNotificationCenter / Win toast） | ✅ Web Notifications（须 HTTPS/本地回环） | 低 |
| 托盘（Tray） | ✅ 官方插件，跨平台 | ✅ Tray 内置 | ✅ 手写（NSStatusItem / Shell_NotifyIcon） | ❌ 浏览器不提供 | 低–中 |
| 全局快捷键 | ✅ 官方插件 | ✅ globalShortcut 内置 | ✅ 手写（Carbon/RegisterHotKey） | ❌ | 中 |
| 文件系统访问 | ✅ fs 插件 + capability 白名单（路径受限，安全） | ✅ fs 全权限（Node） | ✅ 手写（NSOpenPanel / CommonOpenFileDialog） | ⚠️ File System Access API 仅 Chromium，需用户授权 | 中 |
| 剪贴板 | ✅ 官方插件 | ✅ clipboard 内置 | ✅ 手写（NSPasteboard / Clipboard API） | ✅ async Clipboard API | 低 |
| 开机自启 | ✅ autostart 插件 | ✅ app.setLoginItemSettings | ✅ 手写（LoginItems / 注册表） | ❌ | 低–中 |
| 深链/单实例 | ✅ 可做 | ✅ | 手写 | ⚠️ 有限 | 中 |

> 对 godex 实际价值排序：**托盘 + 系统通知 + 全局快捷键（唤起输入框）+ 剪贴板**为 P0；文件系统访问与开机自启为 P1（Web UI 已有文件浏览能力，桌面壳的文件访问主要用于「打开工作目录」）。

---

## 5. 移动端 App 包装路径与推送

### 5.1 包装路径对比

| 方案 | 做法 | 成本 | 说明 |
|---|---|---|---|
| **Capacitor**（推荐候选） | 将现有 Web UI 以 WebView（iOS WKWebView / Android WebView）加载进原生壳，插件桥接原生能力 | 低 | 现网事实：npm 包 `@capacitor/push-notifications` 已迭代到 v8（2026 活跃）；Web UI 零改动，只需处理本地 HTTP 地址加载与 token 注入 |
| **Tauri Mobile** | Tauri 2.x 同一套壳代码编译 iOS/Android | 中 | 若桌面已选 Tauri 可复用 Rust 壳；移动端仍较新 |
| **React Native / Flutter 重写** | 重写前端为原生 UI | 高（数人月） | 违背「不重写前端」约束，仅当 WebView 体验不可接受时考虑 |

### 5.2 推送方案（APNs / FCM）

| 项 | APNs（iOS） | FCM（Android / 跨端） |
|---|---|---|
| 前置要求 | Apple Developer Program（**$99/年**）+ App ID 开启 Push + APNs Auth Key（.p8）或证书 | Google 账号 + Firebase 项目 + `google-services.json`；Android 端依赖 Google Play 服务 |
| 推送流程 | 服务端持 APNs 密钥，向 device token 发请求 | 服务端持 FCM 密钥（HTTP v1 API），向 registration token 发送；FCM 可转发 APNs |
| 国内可用性 | ✅ 正常 | ⚠️ 中国大陆无 Google Play 服务，FCM 不可靠；需厂商通道（小米/华为/OPPO/vivo）或自建长连接 |
| 本地通知（Local Notification） | ✅ 无需后端与账号，godex 场景（agent 任务完成提醒）够用 | ✅ 同左 |

**推荐策略**：
- **Phase 1：本地通知**（本地 agent 完成后触发）——零账号、零后端改动，先达 80% 价值。
- **Phase 2：远程推送**——iOS 走 APNs（$99/年开发者账号 + 服务端 APNs 密钥）；Android 先 FCM，国内分发再评估厂商通道（人力成本高，建议按需）。
- godex 的移动端定位为「**随时查看 + 接收完成通知**」的伴生 App，而非全功能远程控制，故推送价值高于实时交互。

---

## 6. 推荐方案

### 6.1 桌面端：**Tauri 2.x**（首选）

理由：

1. **贴合架构**：godex 后端/前端不动，壳层加载本地 HTTP URL（`http://127.0.0.1:PORT`，token 注入或壳层自动带 cookie/header）即可。
2. **体积**：安装包个位数 MB（vs Electron 100 MB+），符合「轻量级」硬约束。
3. **原生能力齐备**：tray / notification / global-shortcut / clipboard / autostart 均有官方插件，能力白名单（capability 系统）比 Electron 更安全。
4. **移动复用潜力**：2.x 同套壳可编译 iOS/Android，桌面成果向移动延伸成本低。
5. **维护风险可控**：Tauri 2024-10 起 2.x 稳定、社区活跃；Rust 构建链是主要新依赖。

**备选（若团队不接受 Rust）**：系统 WebView 原生壳（Swift + WKWebView / C# + WebView2 / GTK），体积最小但三平台三套代码、托盘/快捷键全手写，维护成本约 2–3 倍，仅推荐「极简壳、能力少」场景。

**不建议**：Electron（违背轻量约束）；PWA 作为桌面壳（无托盘/全局快捷键，核心体验缺失）。

### 6.2 移动端：**Capacitor**（首选）+ 本地通知先行

理由：现有 Web UI 以 WebView 直接包装，改动最小；`@capacitor/push-notifications` 生态成熟；不引入重写成本。若桌面已用 Tauri 且希望一套壳代码，可改用 Tauri Mobile（成本略高）。

---

## 7. 实现代价

### 7.1 人力估算（1 名全栈，兼职后端）

| 阶段 | 范围 | 工作量 | 复杂度 |
|---|---|---|---|
| M1 桌面壳 MVP | Tauri 工程 + 加载本地 URL + 窗口/托盘/单实例 | 3–5 人日 | 低（Tauri 脚手架成熟） |
| M2 原生能力 | 通知、全局快捷键、剪贴板、autostart、设置持久化 | 3–5 人日 | 中（各平台通知权限/快捷键注册细节） |
| M3 移动包装 | Capacitor 工程 + WebView 加载 + token 会话注入 + 本地通知 | 5–8 人日 | 中（iOS 签名/Android 渠道、WebView 与 Web UI 兼容性） |
| M4 远程推送 | APNs 接入（服务端密钥 + token 管理）+ FCM；国内厂商通道 | 5–10 人日（不含厂商通道则减半） | 高（证书、token 生命周期、可靠性） |
| 合计 | | **约 3–5 人周**（不含国内厂商通道与商店上架合规） | |

### 7.2 关键依赖

- Rust 工具链（桌面选 Tauri 时）；Xcode/Android SDK（移动）。
- Apple Developer Program $99/年（iOS 真机 + APNs）；Google Play 账号 $25 一次性（Android 分发）。
- 后端需暴露最小原生桥接口：本地服务端口发现、token 注入、任务完成事件（供本地通知触发）。

### 7.3 复杂度与风险总评

- **整体风险：低–中**。核心思路（WebView 包装本地 HTTP 服务）已被广泛验证；风险集中在平台差异与移动推送通道，而非架构。

---

## 8. 风险

| # | 风险 | 等级 | 缓解 |
|---|---|---|---|
| R1 | WebView 版本差异（macOS 旧版 WKWebView、Win7 无 WebView2、Linux WebKitGTK 陈旧）导致 UI 兼容问题 | 中 | 设定最低系统版本；Linux 提供 WebKitGTK 依赖安装指引；必要时 Electron 兜底（仅 Linux） |
| R2 | token 会话管理：WebView 与浏览器共享/隔离 cookie，刷新机制需适配 | 中 | 壳层注入 token 到 localStorage 或专用 scheme；后端保持现有认证不变 |
| R3 | FCM 在中国大陆不可用 | 中（仅国内分发） | 移动端先本地通知；远程推送按市场决定是否接厂商通道 |
| R4 | Rust 构建链 / 签名与公证（macOS notarization、Windows 代码签名） | 低–中 | CI 固化构建；签名证书预算 |
| R5 | 移动 WebView 对现有 Web UI（拖拽、快捷键、文件上传）的兼容损耗 | 中 | 移动端先做只读+通知 MVP 验证兼容性再扩展 |
| R6 | 桌面壳与本地服务进程生命周期绑定（退出托盘是否关服务） | 低 | 明确策略：托盘退出仅关窗，服务常驻（与现有 TUI/server 共存） |

---

## 9. 验收标准

1. **桌面 MVP**：macOS（+Windows/Linux）安装包 ≤ 15 MB；启动即加载本地 godex Web UI，可登录/使用 Chat 主流程。
2. **托盘**：托盘图标常驻；右键菜单含「打开主窗口 / 退出」；关闭窗口不退出进程（常驻后台）。
3. **系统通知**：agent 任务完成（或配置事件）触发系统通知，macOS 权限弹窗正常处理。
4. **全局快捷键**：可配置全局快捷键唤起主窗口/输入框（默认如 `Cmd/Ctrl+Shift+G`）。
5. **剪贴板**：从 Web UI 复制/粘贴可用。
6. **移动 MVP（iOS/Android）**：WebView 加载同一 Web UI，登录态可用；任务完成触发本地通知。
7. **推送（可选验收）**：iOS APNs 消息可达（真实设备 + 开发者账号）；Android FCM 消息可达（有 Google 服务环境）。
8. **回归**：桌面/移动壳不改动 `internal/` 后端行为；现有 `go test` 与 Web UI 构建保持全绿。

---

## 附：调研来源

- Tauri 官方：https://tauri.app/start/ （tiny, fast binaries; desktop + mobile）
- Tauri Mobile：https://v2.tauri.app/ （2.x 移动支持；页面 404 未直接抓取，以官方首页描述为准）
- MDN PWA 安装指南：https://developer.mozilla.org/en-US/docs/Web/Progressive_web_apps/Guides/Installing
- web.dev PWA 安装/能力：https://web.dev/learn/pwa/installation/
- Capacitor Push Notifications 插件：https://registry.npmjs.org/@capacitor/push-notifications （v8.1.2 活跃）
- 项目内部：docs/project-structure.md、internal/runtime/webui/webui.go
- 当日 web_search 通道故障记录：docs/tools_issues.md（2026-09-05 条目）
