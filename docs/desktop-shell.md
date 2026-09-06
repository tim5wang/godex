# godex 桌面壳（Tauri 2.x，自托管）

> 对应 PRD：`docs/prd-desktop-app-wrap.md` 第 6.1 节推荐方案（Tauri 2.x）
> 实现：M1 桌面壳 MVP + M2 原生能力（系统通知 / 全局快捷键 / 剪贴板）
> 自托管：Go 编译的 godex 二进制作为 sidecar 打进 app，双击启动即自动拉起 `godex serve`

桌面壳是 godex Web UI 的一层**原生壳**：不重写前端，不捆绑 Chromium，复用系统
WebView（macOS WKWebView / Windows WebView2 / Linux WebKitGTK）。**默认自托管**：
app 内嵌 Go 编译的 godex 可执行文件，双击 app 即自动 `godex serve` 到本地端口，
WebView 连接该端口——用户无需手动启动任何服务。后端仅新增一个受 web-token 保护的
任务完成事件 SSE 端点（见下文「后端事件桥」）。

---

## 1. 结构

```
desktop/
├── ui/                      # frontendDist 占位（壳加载远程 URL，不打包前端资源）
└── src-tauri/
    ├── Cargo.toml           # tauri 2 + 官方插件（notification/global-shortcut/clipboard/single-instance/shell）
    ├── build.rs
    ├── tauri.conf.json      # productName godex-desktop，bundle.icon，externalBin 声明 sidecar
    ├── capabilities/default.json  # 能力白名单（通知/快捷键/剪贴板）
    ├── icons/icon.png       # 占位图标（可用 `tauri icon` 生成全套）
    ├── binaries/            # Go 编译的 godex 可执行文件（构建产物，gitignore）
    └── src/
        ├── main.rs
        └── lib.rs           # 全部壳逻辑（自托管服务/窗口/托盘/单实例/事件桥/快捷键）
```

壳逻辑集中在 `desktop/src-tauri/src/lib.rs`，要点：

| 能力 | 实现 |
|---|---|
| **自托管 godex 服务** | 启动时 `tauri-plugin-shell` 的 `sidecar("godex")` spawn `godex serve --addr 127.0.0.1:PORT`；端口默认 17889（小众端口，避开 8080 撞车），被占用则自动选空闲端口（`GODEX_DESKTOP_PORT` 可指定）；**工作目录默认 `~/godex-desktop-workspace`（专用轻量目录，绝不用 $HOME，见下方「重要：工作目录」）**；轮询 `/meta` 就绪后 WebView 才连接；退出时 kill 子进程 |
| 加载本地 Web UI | `WebviewWindowBuilder` + `WebviewUrl::External`，URL 为自托管端口；设 `GODEX_DESKTOP_URL` 则切外部模式（连已运行的服务，不 spawn） |
| token 注入（R2 缓解） | 壳自动生成随机 web token 传给子进程环境变量 `GODEX_WEB_TOKEN`，并用初始化脚本写入 `localStorage['godex:web:token']`（与前端 `store/settings.ts` 的 tokenKey 一致）；`GODEX_WEB_TOKEN` 已设置时沿用 |
| 托盘常驻 | `TrayIconBuilder` + 菜单「打开主窗口 / 退出」；左键单击托盘也唤起窗口；**托盘初始化失败仅打日志，不影响启动**（与快捷键一致，非关键能力失败降级） |
| 关窗不退出（R6） | `on_window_event` 拦截 `CloseRequested` → `prevent_close` + `hide`；仅托盘「退出」结束进程（此时回收 godex 子进程） |
| 单实例 | `tauri-plugin-single-instance`：二次启动唤起既有窗口 |
| 系统通知 | 后台线程订阅后端 `/api/desktop/events`（SSE），收到 `task_completed` 事件后经 `tauri-plugin-notification` 弹系统通知 |
| 全局快捷键 | `tauri-plugin-global-shortcut`，默认 `CmdOrCtrl+Shift+G`（macOS Cmd / Win+Linux Ctrl），环境变量 `GODEX_DESKTOP_HOTKEY` 可配置；**注册失败仅降级为警告日志**（组合键被系统/其他 app 占用时，如 macOS Cmd+Shift+G 冲突），不会导致启动崩溃 |
| 剪贴板 | `tauri-plugin-clipboard-manager` + capability 白名单；WebView 内复制/粘贴走系统剪贴板 |

## 2. 环境依赖

- **Rust 工具链**：`rustup`（stable，≥1.77 即可），含 `cargo`
- **macOS**：Xcode Command Line Tools（`xcode-select --install`）；WebView 用系统 WKWebView，无需额外 SDK
- **Windows**：WebView2 Runtime（Win10/11 自带；老系统需安装 Evergreen 运行时）
- **Linux**：`webkit2gtk-4.1`、`libappindicator`/`libayatana-appindicator`、`librsvg` 等（Tauri 官方 prerequisites，安装命令见 https://tauri.app/start/prerequisites/）
- **后端**：本仓库 Go 工具链（构建 godex 服务本身 + 自托管 sidecar 二进制）
- **Rust 目标三元组**：sidecar 命名 `godex-<triple>`，`scripts/build-desktop.sh` 自动取 `rustc -vV` 的 host triple

不需要安装 Tauri CLI 也能编译（`cargo build`）；打包分发推荐 `tauri build`（需 `@tauri-apps/cli`，见第 4 节）。

## 3. 构建与运行

### 3.1 一键构建（自托管 sidecar）

```bash
# 构建嵌入式 godex 二进制 + 编译壳（debug）
scripts/build-desktop.sh

# release 壳二进制
scripts/build-desktop.sh --release
```

脚本做的事：`go build` 出 godex 可执行文件放到 `desktop/src-tauri/binaries/godex-<triple>`
（Tauri externalBin sidecar 命名约定），再 `cargo build` 壳。首次构建会下载并编译
约 400 个 crate，耗时数分钟（视网络）。

### 3.2 手动分步构建

```bash
# 1) Go 二进制 → sidecar（triple 用 rustc -vV 的 host）
TRIPLE=$(rustc -vV | sed -n 's/^host: //p')
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o desktop/src-tauri/binaries/godex-$TRIPLE ./cmd/godex

# 2) 编译壳
cd desktop/src-tauri && cargo check && cargo build
```

### 3.3 运行（双击即用 / 命令行）

```bash
# 默认自托管：双击 target/debug/godex-desktop（或打包后的 godex.app）即可，
# 壳自动拉起 godex serve（端口 17889 被占用则自动换空闲端口）并注入随机 web token。
./target/debug/godex-desktop
```

可选环境变量：

```bash
export GODEX_DESKTOP_URL=http://127.0.0.1:8080   # 设了则切外部模式（连已运行的服务，不 spawn）
export GODEX_DESKTOP_PORT=17889                   # 自托管固定端口（默认 17889，占用则选空闲）
export GODEX_DESKTOP_WORKSPACE=$HOME              # godex serve 工作目录（默认 $HOME）
export GODEX_WEB_TOKEN=my-secret                  # 沿用已有 token；不设则壳自动生成随机 token
```

验证清单（对照 PRD 第 9 节验收）：

1. **双击 app 无需任何手动步骤**：窗口加载 godex Web UI，可登录并使用 Chat 主流程
   （`godex serve` 由壳自动拉起）
2. 托盘图标常驻；右键菜单含「打开主窗口 / 退出」；点窗口关闭按钮 → 窗口隐藏、进程不退
3. 二次启动二进制 → 不产生第二个窗口，唤起既有窗口；仅一个 godex serve 子进程
4. 在 Web UI 发起一个 longtask/对话，任务完成时收到系统通知
5. 按 `Cmd/Ctrl+Shift+G` 从任意应用唤起主窗口
6. Web UI 中复制/粘贴文本可用
7. 从托盘「退出」后确认无残留 godex 子进程（壳在 RunEvent::Exit 时 kill sidecar）

## 4. 打包与签名（R4）

```bash
# 推荐：一键打包（自动构建 sidecar + 编译壳 + 打包 .app/.dmg）
scripts/build-desktop.sh --bundle

# 若 CLI 未安装会自动经 npx 下载 @tauri-apps/cli（无需全局安装）；
# 也可手动安装 Tauri CLI（Node 环境）：
npm install -g @tauri-apps/cli

# 生成全套平台图标（从 1024 占位 PNG 生成 .icns/.ico/.png 到 icons/；
# 仓库已含全套图标，换品牌图后重跑即可）
tauri icon desktop/src-tauri/icons/icon.png

# 手动打包当前平台安装包（macOS .app/.dmg，Windows .msi，Linux .deb/.AppImage）
cd desktop/src-tauri && tauri build --target aarch64-apple-darwin
```

> **重要**：`tauri build` 必须带 `--target <triple>`（与本机 `rustc -vV` 的 host
> 一致），且 sidecar 命名 `binaries/godex-<triple>` 必须与 target 相同。否则在
> Rosetta x64 Node 环境（npx CLI 为 darwin-x64）下，CLI 会按自身架构去找
> `godex-x86_64-apple-darwin` 而 sidecar 实际是 `aarch64`，导致打包失败
> （`Failed to copy external binaries`）。`scripts/build-desktop.sh` 已自动处理
> 这一一致性（sidecar 与 `--target` 共用 `$TRIPLE`，可用 `TARGET_TRIPLE` 覆盖）。

签名说明（R4 缓解）：
- **macOS**：默认输出未签名/未公证（ad-hoc）。正式分发需 Apple Developer 账号：
  `codesign` + `notarytool` 公证，或在 `tauri.conf.json` 配置 `bundle.macOS.signingIdentity`
  与公证凭据后由 `tauri build` 自动完成。
- **Windows**：默认未签名。需代码签名证书（EV 或 OV），在 CI 中对产物执行
  `signtool sign`，或配置 `bundle.windows.certificateThumbprint`。
- **Linux**：无强制签名要求；如需 AppImage 可配置 GPG 签名。

包体说明（重要 trade-off）：

- **自托管模式**（默认，含 Go 编译的 godex 二进制）安装包约 **45–75 MB**（godex
  静态二进制 ~70MB，压缩后略小），**突破 PRD 的 ≤15MB 约束**——这是「双击即用、
  无需手动起服务」与「包体最小」的取舍；用户需求优先选择自托管。
- **外部模式**（设 `GODEX_DESKTOP_URL` 连已运行的服务）壳本身约 **5–12 MB**，
  满足 ≤15MB；代价是用户需自行启动 godex serve。
- 若需两者兼得：可将 godex 二进制压缩（UPX）或按需惰性拉取，见「已知限制」。

## 4.5 重要：工作目录（避免全盘扫描卡死）

自托管模式下壳以**专用轻量目录** `~/godex-desktop-workspace` 作为 `godex serve` 的
工作目录（首次运行自动创建），**默认绝不用 `$HOME`**。原因：godex 会从
`~/.godex/mcp.json` 加载 MCP server，其中 codebase-memory-mcp 的 `auto_index=true`
会对**工作目录**全量建索引（索引库可到数 GB）；若工作目录是数百 GB 的家目录，
serve 启动即触发全盘扫描，CPU 打满、系统卡死（实测复现：godex.log 膨胀到 1.7GB、
"godex serve" CPU 持续 50-80%）。

- 需要指向真实项目目录时设置 `GODEX_DESKTOP_WORKSPACE=/path/to/project`
- 或避免在该目录启用 MCP auto_index（`codebase-memory-mcp config set auto_index false`）

## 5. 后端事件桥（系统通知数据源）

壳层不轮询业务 API，而是订阅后端新增的轻量 SSE 端点：

```
GET /api/desktop/events      # 外部路径（webui 剥掉 /api 前缀）
```

- **实现**：`internal/runtime/httpapi/routes_desktop.go`（`registerDesktopRoutes`），
  受 `withBearerAuthProvider`（web token）保护。
- **语义**：每 2s 轮询一次 `backend.Service.ListSessions`，比较会话 `Running`
  状态；检测到 running→idle 转变即推送一条 SSE 事件：

  ```
  data: {"type":"task_completed","session_id":"...","title":"..."}
  ```

- **选择轮询而非事件总线的原因**：任务完成事件（`EventTurnCompleted` /
  `EventCommandCompleted`）是 per-session 广播（`sessionState.events`），没有全局
  聚合流；ListSessions 已带 `Running` 字段，用 2s 轮询做状态转变检测是**后端改动
  最小**（新增一个文件 + 一行注册）且行为确定的方案。若未来需要毫秒级推送，可改为
  在 backend 增加全局 Broadcaster 订阅点，端点内部逻辑不变。
- **测试**：`internal/runtime/httpapi/routes_desktop_test.go` 覆盖握手（200 +
  text/event-stream）与 web-token 鉴权（无 token 401）。

## 6. 配置汇总

| 环境变量 | 默认 | 说明 |
|---|---|---|
| `GODEX_DESKTOP_URL` | 空（自托管） | 设置后切外部模式：连接已运行的 godex 服务，不 spawn |
| `GODEX_DESKTOP_PORT` | 17889（占用则空闲） | 自托管模式下 godex serve 监听端口（小众默认，避开 8080） |
| `GODEX_DESKTOP_WORKSPACE` | `$HOME` | 自托管模式下 godex serve 的工作目录 |
| `GODEX_WEB_TOKEN` | 空（壳自动生成） | web token；自托管时传给子进程并注入 WebView localStorage |
| `GODEX_DESKTOP_HOTKEY` | `CmdOrCtrl+Shift+G` | 全局快捷键（Tauri Shortcut 语法） |

## 7. 已知限制 / 后续

- **图标**：`desktop/src-tauri/icons/` 全套图标由原始品牌图 `ui/web/public/brand/godex-icon.jpg`
  转换的 RGBA PNG（`icons/source-icon.png`，1024×1024）经 `tauri icon` 生成（含 icns/ico/
  各尺寸 png 及 ios/android 资源）；换品牌图后重跑 `tauri icon icons/source-icon.png` 即可。
- **包体优化**：自托管模式包体由 godex 二进制主导（~70MB）。候选优化：UPX 压缩
  sidecar；或首次运行从远端拉取二进制并校验 hash；或拆分「壳 + 按需下载」双发行。
- 全局快捷键配置目前走环境变量；如需 GUI 设置持久化，可扩展 `tauri-plugin-store`
  或复用 godex 配置中心。
- 自托管模式的工作目录默认 `$HOME`，配置（godex.yaml）与状态（~/.godex）都落在
  用户主目录；如需隔离可设 `GODEX_DESKTOP_WORKSPACE`。
- 移动端（iOS/Android）同壳复用是 Tauri 2 卖点，但不在本卡范围（M3）。
- WebView 版本差异风险（R1）：macOS 最低建议 Big Sur 11+；Linux 需 WebKitGTK 4.1。
