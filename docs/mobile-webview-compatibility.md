# 移动 WebView 兼容性验证与已知问题清单（PRD R5）

> 对应 `docs/prd-desktop-app-wrap.md` 第 8 节风险 R5：移动 WebView 对现有 Web UI（拖拽、快捷键、文件上传、登录态）的兼容损耗。
> 现状：本开发机缺 Xcode/Android SDK，无法实机验证；本文档为**基于代码事实 + 平台已知限制**的静态清单，后续在有 SDK 环境按此表逐项实机验证并回填。

## 验证矩阵（待实机回填：✅ 通过 / ❌ 损耗 / ⚠️ 部分）

| # | 能力 | 相关代码 | iOS WKWebView | Android WebView | 说明 |
|---|---|---|---|---|---|
| 1 | 登录态（token） | `ui/web/src/store/settings.ts`（`godex:web:token`）+ `apiClient.ts`（Bearer 头） | ⚠️ | ⚠️ | 机制等价：原生注入 localStorage，Web UI 零改动。需实机验证注入时序（document-start 注入 vs 页面 JS 读取） |
| 2 | 拖拽（布局/排序） | `ui/web/src/components/workspace/CenterGrid.tsx` 等 | ❌ | ⚠️ | iOS WKWebView 不支持 HTML5 Drag-and-Drop；Android 支持有限。移动端应禁用/降级拖拽布局 |
| 3 | 快捷键 | `ui/web/src/hooks/useGlobalKey.ts`、`CodeEditor.tsx`、`App.tsx` | ❌ | ❌ | 移动端无物理键盘（外接键盘时部分生效）；Cmd/Ctrl 组合快捷键基本不可用 |
| 4 | 文件上传 | `Composer.tsx`、`FilesPanel.tsx`、`FileTree.tsx` | ⚠️ | ⚠️ | `input[type=file]` 在 WebView 可用（iOS 照片/文件、Android 文件选择器）；**拖拽上传不可用**，大文件上传体验差 |
| 5 | 右键菜单 | Web UI contextmenu 依赖 | ⚠️ | ⚠️ | 移动端长按触发系统菜单，非浏览器 contextmenu；需实机验证 |
| 6 | hover 交互 | 大量 CSS :hover 样式 | ❌ | ❌ | 触摸无 hover 态，悬停面板/按钮需点击替代 |
| 7 | SSE 实时流 | `ui/web/src/lib/sse.ts` | ⚠️ | ⚠️ | App 退后台 WebView 挂起，SSE 断线；回前台重连（代码已有重连逻辑）。移动端定位「随时查看」，可接受 |
| 8 | 剪贴板 | 浏览器 Clipboard API | ⚠️ | ⚠️ | iOS WKWebView 需用户手势 + 权限；Android 部分版本受限 |
| 9 | 桌面多栏布局 | Chat/Files/Workspace 网格 | ⚠️ | ⚠️ | Web UI 已有响应式断点（≤900px 布局），需实机验证窄屏可用性 |
| 10 | 复制粘贴（Composer） | 编辑器键盘 | ⚠️ | ⚠️ | 软键盘遮挡、输入法兼容需实机验证 |

## 已知损耗与本期对策（R5 缓解，对应 PRD 5.2「移动端先做只读+通知 MVP」）

1. **拖拽布局**：移动端不提供拖拽排序/调整大小交互；Web UI 若在窄屏自动降级为单栏则无影响。验证重点：`CenterGrid.tsx` 在窄屏是否自动折叠。
2. **快捷键**：依赖全局快捷键的功能（如唤起输入框）在移动端不可用；移动端通过页面内按钮操作。
3. **文件上传**：只读 MVP 阶段不承诺上传体验；`input[type=file]` 可用但建议文档标注限制。
4. **登录态**：token 注入与浏览器同机制（`Authorization: Bearer`），WebView 与系统浏览器 cookie/localStorage **相互隔离**（PRD R2），无泄漏面；token 变更需在设置页重新连接。

## 移动端 MVP 边界（本期交付）

- 定位：**随时查看 + 接收完成通知** 的伴生 App（PRD 5.2 结论），非全功能远程控制。
- Web UI 完整保留：Chat 查看、会话列表、任务状态、文件浏览等只读场景优先验证。
- 写操作（发消息、编辑文件）在 WebView 可用但非优化目标。

## 实机验证步骤（待 SDK 环境执行）

1. iOS：Xcode 打开 `mobile/ios/App/App.xcodeproj` → 真机/模拟器运行 → 设置页填服务地址+token → 验证登录态、会话查看、任务完成通知。
2. Android：Android Studio 打开 `mobile/android` → 运行 → 同上验证。
3. 按上表逐项回填 ✅/❌/⚠️，更新本文档。
