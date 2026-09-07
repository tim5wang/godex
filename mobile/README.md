# godex Mobile —— Capacitor 壳（M3：WebView + 本地通知；M5：Android 本地运行 agent）

> 基于 `docs/prd-desktop-app-wrap.md` 第 5、6.2 节（Capacitor 首选方案）实现移动包装 MVP。
> 远程推送（APNs/FCM，M4）不在本期：需要 Apple Developer 账号 / Google Play 服务，见「已知限制」。

## 是什么

现有 godex Web UI（`ui/web`，React/Vite，后端 `internal/runtime/webui` 提供 `/api/*` 与 `Authorization: Bearer` 认证）**零改动**打包进 iOS/Android 原生壳，双模式：

**A. 本地运行 agent（Android，默认）** —— 仿桌面版 Tauri sidecar：

- APK 内嵌 Go 交叉编译的 godex 二进制（`jniLibs/arm64-v8a/libgodex.so`，由 `scripts/build-godex-android.sh` 产出，不入库）；
- 启动时解压到私有目录 → spawn `godex serve --addr 127.0.0.1:PORT` → 随机生成 web token → 等 `/meta` 就绪 → WebView 加载本地服务。agent 在手机本地执行，无需外部服务器；
- 原生层注入 `localStorage["godex:web:token"]`（Web UI 唯一读取的 token 键）+ 注入式 watcher（`public/godex-watcher.js`）轮询本地 `/api/sessions` 与 `/api/sessions/{id}/longtasks`，任务完成触发**本地通知**（零账号、零后端改动，PRD 5.2 Phase 1 策略）。

**B. 远程连接（iOS / 备选）** —— WebView 加载用户配置的远端 godex 服务地址，token 注入与通知机制同上；首次启动为设置页：填服务地址 + 访问令牌（web.token / GODEX_WEB_TOKEN）+ 轮询间隔。

## 目录结构

```
mobile/
├── capacitor.config.ts      # Capacitor 配置（webDir: www，开发期允许 cleartext）
├── index.html               # 壳层设置页（本地模式只显示启动状态）
├── src/
│   └── main.ts              # 设置页逻辑（存配置、请求通知权限、导航；本地模式不抢先跳转）
├── public/
│   └── godex-watcher.js     # 注入式 watcher 模板（唯一事实源，原生层读取后替换占位符）
├── scripts/
│   ├── build-godex-android.sh    # 交叉编译 godex -> jniLibs/arm64-v8a/libgodex.so（本地模式必需）
│   └── build-android-runtime.sh  # 拉取 busybox + 交叉编译静态 git -> jniLibs/arm64-v8a/（agent shell/git 工具）
├── ios/                     # cap add ios 生成 + 注入补丁（SceneDelegate.swift）
└── android/                 # cap add android 生成 + 注入补丁（MainActivity.java + network_security_config.xml）
```

## 环境要求

- Node.js ≥ 20 + npm（壳层构建）
- Go ≥ 1.22（交叉编译 godex 二进制，本地模式必需）
- Android：Android Studio + SDK（platform 36 + build-tools 35/36；JDK 21 —— capacitor-local-notifications 的 Gradle toolchain 要求）
- iOS：macOS + Xcode ≥ 15（构建/真机运行需要；**当前开发机未装 Xcode，iOS 只交付工程骨架**）
- 远程模式目标 godex 服务：移动端网络可达（局域网地址或公网 https）

## 构建与运行

```bash
cd mobile
npm install

# 1) 构建壳层静态页 -> www/
npm run build

# 2) 交叉编译 godex 二进制（Android 本地运行模式）-> android/app/src/main/jniLibs/arm64-v8a/libgodex.so
bash scripts/build-godex-android.sh

# 3) 拉取 busybox + 交叉编译静态 git -> android/app/src/main/jniLibs/arm64-v8a/
bash scripts/build-android-runtime.sh

# 4) 同步 www + 插件到原生工程
npx cap sync

# 5) 打开原生 IDE 构建运行
npm run open:android   # 需要 Android Studio；产物 APK 位于 android/app/build/outputs/apk/
npm run open:ios       # 需要 Xcode
```

> 步骤 2/3 的产物（`jniLibs/arm64-v8a/*.so`）**不入库**：`android/app/build.gradle` 的 `ensureNativeLibs` 任务会在 so 缺失时**自动执行**上述两个脚本，直接 `./gradlew assembleDebug` 即可；手动跑脚本可跳过自动构建。

**Android 本地运行模式（默认）**：APK 内置 godex 二进制（`jniLibs/arm64-v8a/libgodex.so`），启动即自动拉起本地 `godex serve`（127.0.0.1:17889 或空闲端口），无需任何配置即可使用。同时内置 busybox（sh/grep/sed/awk 等）与静态 git（`jniLibs/arm64-v8a/libbusybox.so` / `libgit.so` / `libgitremotehttps.so`），由 `build-android-runtime.sh` 生成——agent 的 bash 工具（`sh -c`）与 git 工具（`git`，含 https 远程经 `git-remote-https`）在 Android 本地可用。

**远程连接模式**：从设置页填入服务地址（assets 中无二进制时才显示设置页）：

| 字段 | 说明 |
|---|---|
| godex 服务地址 | 如 `http://192.168.1.10:9000` 或 `https://godex.example.com` |
| 访问令牌 | 服务端 `web.token` / `GODEX_WEB_TOKEN`；未启用认证可留空 |
| 轮询间隔（秒） | 默认 30s，最小 15s |

## 签名 / 渠道说明（R4 / R3）

### iOS

- 本地模拟器运行无需签名；真机需要 Apple 开发者账号（免费个人账号也可，7 天有效）。
- 分发（TestFlight / App Store）需 Apple Developer Program（$99/年）+ 证书签名 + notarization。
- 工程已含 `ios/App/App/Info.plist`；开发期 HTTP 内网地址需在 `Info.plist` 的 ATS 例外（`NSAppTransportSecurity/NSAllowsArbitraryLoads` 或按域名例外）——生产建议走 https 关闭例外。

### Android

- 调试运行：Android Studio 直接 run（自动用 debug keystore）。
- 分发：`npx cap open android` 后在 Android Studio 生成签名 APK/AAB：
  - 生成 release keystore：`keytool -genkey -v -keystore godex-release.keystore -alias godex -keyalg RSA -keysize 2048 -validity 10000`
  - 在 `android/app/build.gradle` 配置 `signingConfigs.release`；
  - AAB（Play 商店）或 APK（侧载/国内渠道）。
- 国内渠道（R3）：FCM 不可用时本地通知已覆盖主要场景；如需厂商通道（小米/华为/OPPO/vivo）属于 M4 后续。

## 已知限制（务必阅读）

1. **后台通知**：本地通知依赖 JS watcher 轮询，App 退到后台后系统会挂起 JS 定时器。iOS 需 BGTask 或远程推送（M4）才能保证后台必达；Android 可后续用 WorkManager 轮询增强。当前 MVP 保证「App 打开/回前台时」的任务完成提醒。
2. **只读 MVP**：移动端定位「随时查看 + 接收完成通知」。Web UI 中桌面交互（拖拽、快捷键、右键、大文件上传）在移动 WebView 有兼容损耗，见 `docs/mobile-webview-compatibility.md`。
3. **Android 本地运行的 agent 工具链**：已内置 busybox（sh/ash/bash + 常用命令）与静态 git（含 git-remote-https）。bash 工具默认调用 `sh`（busybox ash）；如需完整 bash 特性（`[[ ]]`、`source` 等），后续可静态编译 bash 一并打包。git 为 musl 静态构建，https 远程可用；未打包 `git-lfs` 等可选组件。
4. **token 变更**：远程模式修改 token 后重新连接即可；原生注入读取的是 Preferences 中的最新值。
5. **cleartext**：`capacitor.config.ts` 开发期开放 `cleartext`，生产环境应收紧（https）；`network_security_config.xml` 仅放行 127.0.0.1/localhost。
6. **原生代码编译状态**：Android 补丁（`MainActivity.java`、`network_security_config.xml`、`build.gradle`）已在本机编译验证通过（JDK 21 + Android SDK 36，APK 产出正常）；iOS 补丁（`SceneDelegate.swift`）因缺 Xcode 未编译，需在 Xcode 环境验证。
7. **godex 二进制体积**：本地模式 APK 约 67MB（godex-arm64 86MB + busybox 2.7MB + git-arm64 33MB + git-remote-https-arm64 31MB，assets zip 压缩后）。
8. **runtime 二进制下载/编译源**：busybox 取自 EXALAB/Busybox-static（GitHub），git 用 zig 0.16 交叉编译（musl 静态，见 `build-android-runtime.sh`）；两者均由脚本生成、不入库。

## 验收对照（PRD 第 9 节第 6 条）

- ✅ WebView 加载同一 Web UI（通过配置服务地址 + token 注入）
- ✅ 登录态可用（token 注入 `localStorage["godex:web:token"]`，与浏览器认证同机制）
- ✅ 任务完成触发本地通知（watcher 轮询 + `@capacitor/local-notifications`，零账号零后端改动）
- ⚠️ 远程推送（APNs/FCM）不在本期（M4，需开发者账号）
