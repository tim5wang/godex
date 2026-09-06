# PRD：godex 内嵌浏览器视图（Browser use inside）

> 状态：调研 PRD（需求池卡片 t-1788517998831-3）
> 日期：2026-09-05
> 目标读者：实现工程师 / 架构评审

## 1. 背景

Agent（LLM）执行网页任务时，用户目前只能通过文本/截图 artifact 间接了解「Agent 正在操作什么网页」。参考 Codex 的 Browser use——在对话界面内以小窗或右侧侧边栏实时展示 Agent 正在操作的浏览器画面——godex 需要评估同能力的内嵌浏览器视图（Browser use inside）可行性与实现代价。

godex 已有完整且较成熟的浏览器自动化底座（rod/CDP + 22 个 browser action + 分布式 CDP relay），缺的是「连续画面」的展示通道：目前 `browser screenshot / capture_page` 产出的是**离散**截图 artifact（工具结果附件），不是可实时观看的画面流。

## 2. 目标

- **G1**：Agent 使用 browser 工具时，用户能在 Chat 右侧侧边栏（Dock）实时看到 Agent 正在操作的页面画面（非仅截图 artifact）。
- **G2**：视图自动跟随 Agent 的导航/操作（URL、滚动、点击后的页面状态变化），延迟可接受（≤2s）。
- **G3**：不改变现有 browser 工具契约（22 个 action 行为零改动），浏览器控制仍由 Agent 通过工具完成。
- **G4**：会话隔离、鉴权复用、可手动开关；跨节点（远程 browser 运行时）场景可用。

非目标：双向交互（用户反向控制浏览器画面）不在本次范围；已有 `handoff` 动作覆盖「移交可见浏览器给用户协助」场景，本次只做「观察」通道。

## 3. 竞品对标

| 产品 | 形态 | 关键机制 | 可借鉴点 |
|---|---|---|---|
| **Codex Browser use**（OpenAI，2026 初） | 对话界面内嵌浏览器小窗/分屏视图，Agent 用自然语言驱动真实浏览器（QA 测试、登录流程、网页任务） | 浏览器 Agent（真实浏览器实例）+ 界面内嵌视图实时展示 Agent 操作 | 「内嵌视图跟随 Agent 操作」正是本 PRD 目标形态；配套 computer use（桌面控制）不在范围 |
| **Claude browser use tool**（Anthropic，官方文档） | 在你的应用运行的浏览器环境内导航/读取/交互网页 | 双通道感知：结构通道（accessibility tree / elements / forms / tabs）+ 像素通道（screenshots + viewport coordinates）；区别于 computer use tool（全屏截图+坐标点击） | 像素通道 = 截图流思路；结构通道 = godex 已有 `snapshot`（DOM 文本快照）+ `find`，感知侧已具备 |
| **OpenAI Operator** | 云端浏览器会话，用户在对话界面观看 Agent 操作并可 **Take control** 接管 | CUA（Computer-Using Agent）模型：看屏幕 → 移动光标 → 点击；云端浏览器实例 | 观察 + 可接管；godex 已有 `handoff/resume` 覆盖「接管」语义（交给用户可见浏览器） |
| **Playwright Trace Viewer / codegen**（工具参考） | 录制回放 UI 展示页面快照与操作轨迹 | 每一步生成页面快照 + 操作列表 | 快照回放适合事后审计，不适合实时直播 |

**结论**：三家中 Codex 的形态与本 PRD 完全一致（内嵌视图 + Agent 操作浏览器）；Claude 的「像素通道 + 结构通道」双感知与 godex 现有工具能力天然对应（`screenshot` + `snapshot`/`find`）。

## 4. 技术方案对比

| 方案 | 原理 | 实现成本 | 可行性 | 优点 | 缺点 |
|---|---|---|---|---|---|
| **A. CDP 画面流（Screencast）** ⭐推荐 | 后端复用现有 rod/CDP，对当前页面订阅 `Page.startScreencast`（CDP 原生 JPEG 帧事件）或定时 `captureScreenshot`，降采样后经 WebSocket 推前端 `<img>` 逐帧渲染 | 中（后端 ~3-4 人日 + 前端 ~2-3 人日） | **高**：godex 已是 rod+CDP 架构，screencast 是 CDP 原生能力，无需新依赖 | 实时看到 Agent 真实操作的像素（滚动/hover/点击后状态）；控制仍走现有 browser 工具零改动；支持多页面切换跟随 | 帧流带宽/CPU 开销（可降采样到 1-2fps、宽 ≤640px 缓解）；headless 渲染与真实浏览器存在细节差异 |
| **B. iframe URL 跟随** | 前端把 Agent 当前页面 URL 塞进 iframe（可复用 Preview dock 的 iframe 容器） | 低（~1-2 人日） | 低-中：大量站点因 `X-Frame-Options`/CSP `frame-ancestors` 拒绝被嵌（Google/登录页等）；且 iframe 与 Agent 的浏览器会话 cookie 隔离，看到的是「同一个 URL 的独立页面」而非 Agent 实际操作的页面 | 成本极低、实现快 | 主流站点大面积嵌不了；看不到 Agent 的滚动/局部交互；登录态不共享——只能做 A 的兜底降级显示 |
| **C. Playwright 截图流** | 引入 Playwright 依赖 + 独立浏览器实例，定时截图推流 | 中高（需新增依赖、双浏览器栈并存） | 中：Playwright 生态有现成截图/录屏 API | 截图/录屏 API 顺手 | 与 godex 现有 rod 栈重复造轮子；双浏览器管理复杂度上升；收益与 A 相同但成本更高 |
| **D. 系统 WebView / 嵌入式 Chromium（CEF/electron 式）** | 桌面端嵌入真实浏览器控件展示页面 | 高 | 低：godex Web UI 本身跑在浏览器里（套娃），移动端不可行；需桌面壳改造 | 可展示真实渲染 + 允许用户交互 | 架构侵入大、跨端不可行、与「Web UI 即前端」的现状冲突 |
| **E. WebRTC 画面流** | 浏览器实例 → WebRTC 视频流 → 前端 `<video>` | 高（信令 + 授权 + 浏览器集成） | 低：headless 场景集成复杂，授权弹窗/信令链路重 | 低延迟、低带宽 | 复杂度远超收益；与现有 WS 基建不匹配 |

### 结论

**方案 A（CDP Screencast 流）为推荐方案**：godex 的浏览器栈（go-rod + CDP + launcher）已就位，screencast 是 Chrome/CDP 原生能力，无新依赖；后端仅需「帧订阅 + 降采样 + WS 推送」，前端仅需「新增 Browser dock 面板 + 逐帧渲染」。方案 B 作为 A 的兜底（站点无法截帧或帧流断时显示 URL 卡片/iframe 尝试）。

## 5. godex 现状盘点

### 5.1 浏览器自动化底座（已具备，本次零改动）

- `internal/tools/browser_*.go`（约 12 个文件）：`browser` 工具 22 个 action——`status / open / open_tab / navigate / snapshot / click / type / press / wait / screenshot / close / close_tab / switch_tab / list_pages / find / fill_form / upload_file / wait_network_idle / network_snapshot / download / capture_page / search_and_open / handoff / resume`。
- `BrowserService`（`internal/tools/browser_types.go:160`）：基于 **go-rod**（CDP Go 客户端）+ launcher（自动下载 Chromium），按 session 管理多页面状态、refs、持久化 profile（`stateDir`）。
- 截图能力现成：`Screenshot(ctx, sessionID, pageID, fullPage)` → PNG artifact，工具结果带 `ArtifactPaths` 前端可展示（离散截图）。
- **分布式浏览器**：`browser_cdp_relay.go` 已实现经 relay 通道连接远程 node CDP 端点（`tools.browser.cdp_relay_node` + `cdp_listen`），说明 gorilla/websocket + CDP 的通道基建已存在。
- `handoff`（`browser_handoff.go`）：把当前页面移交给可见浏览器供用户协助，`resume` 收回——「接管」语义已覆盖。
- `lightpanda.go`：轻量无头浏览器（抓取用，web_fetch/web_search 底座）；`desktop_ocr.go`：桌面 OCR（computer use 类似能力的前置）。

### 5.2 Web UI 承载（容器现成）

- Chat 右侧 **DockRail 五 tab**（`ui/web/src/features/chat/layout/DockRail.tsx`）：`files / terminal / tasks / preview / status`，面板懒加载挂载 + 保持存活（`ChatPageView.tsx` mountedDockTabs）。
- **PreviewPanel**（`ui/web/src/features/preview/PreviewPanel.tsx`）：已是 iframe 容器，支持静态文件 `/api/preview/static`、dev server proxy `/api/preview/proxy/{port}`、任意 https URL 三种模式——iframe 兜底方案可无缝复用；新增 Browser dock tab 或给 preview 加 browser 模式均可。
- 工具调用事件已在 MessageFeed/ToolCard 展示（Agent 操作可见文本化），SSE 流式通道 + 断线重连已存在。

### 5.3 关键缺口

1. **无连续画面通道**：截图是离散 artifact，不是可订阅的帧流。
2. **无 Browser 面板**：前端没有展示 Agent 浏览器画面的容器（Preview 面板目前是用户手动输入地址，不跟随 Agent 的 browser 工具）。
3. **无事件联动**：Agent 的 browser 工具调用没有「激活/切换视图」的后端事件（导航 → 画面自动跟随需联动）。

## 6. 推荐方案（详细）

### 6.1 架构

```
Agent ──browser 工具(22 action)──▶ BrowserService(rod, 每 session 页面)
                                       │ Page.startScreencast / 定时 captureScreenshot
                                       ▼
                              FrameSubscriber(会话级订阅, 降采样)
                                       │ JPEG 帧 (base64/二进制)
                                       ▼
                       WS 端点 /api/browser/frames?session=xxx   ◀── 复用 gorilla/websocket + web token 鉴权
                                       ▲
                                       │
Web UI Chat ── 右侧 Dock 新增 tab「Browser」──▶ <img> 逐帧渲染 + 状态条(URL/标题/操作指示)
```

### 6.2 后端改动（`internal/tools` + `internal/runtime/httpapi`）

1. `BrowserService` 增加帧订阅：`SubscribeFrames(sessionID, pageID) (<-chan Frame, cancel)`；帧源优先 `rod` 的 `Page.startScreencast`（CDP 原生，headless 支持），fallback 定时 `captureScreenshot`（1-2fps）。
2. 降采样/限流：宽度 ≤640px、JPEG quality ~70、目标 ≥1fps；空订阅自动断开（空闲 30s 停采）。
3. WS 端点：`/api/browser/frames`，鉴权复用现有 web token + 会话归属校验（防跨会话偷看）；帧消息带 `{pageID, url, title, jpeg}`。
4. 事件联动：browser 工具执行时向现有事件流推 `browser.view {sessionID, pageID, url}`（前端据此自动激活/切换面板），导航后推 URL 更新。

### 6.3 前端改动（`ui/web`）

1. DockRail 新增 `browser` tab（或 Preview 面板加 browser 模式，推荐新增独立 tab 语义更清晰）。
2. BrowserPanel：`<img>` 逐帧渲染 WS 帧流 + 顶栏（URL、标题、页面切换、全屏/固定开关、截图保存）。
3. 监听 `browser.view` 事件：Agent 首次操作 browser 工具时自动激活面板并跟随；无操作时用户可手动打开。
4. 降级：帧流不可用时显示当前 URL + 「打开 Preview iframe」按钮（方案 B 兜底）。

### 6.4 不做的事

- 双向控制（用户在画面里点击反向驱动浏览器）——后续可基于 CDP `Input.dispatchMouseEvent` 扩展，本期不做。
- 录屏/回放、多帧缓存审计。

## 7. 实现代价

| 项 | 工作量 | 复杂度 | 说明 |
|---|---|---|---|
| 后端帧订阅 + 降采样 + WS 端点 + 鉴权 | 3-4 人日 | 中 | 复用 gorilla/websocket（CDP relay 已验证）与 rod；screencast 原生支持，主要工作在订阅管理与事件联动 |
| 前端 Browser dock 面板 + 帧渲染 + 事件跟随 | 2-3 人日 | 中 | 复用 DockRail 机制与 Preview iframe；WS 客户端基建已有 |
| 联调 + 测试 + 远程节点验证 | 1-2 人日 | 低-中 | headless 帧率/带宽调优；relay 场景降帧率验证 |
| **合计** | **6-9 人日（约 1.5-2 周 × 1 人）** | 中 | 无新依赖、无架构变更 |

对比：方案 B（iframe 兜底）约 1-2 人日可先行落地作为 P0 最低可用形态；A 是主投入。

## 8. 风险

| 风险 | 等级 | 缓解 |
|---|---|---|
| 帧流带宽/CPU（headless screencast 默认高帧率） | 中 | 降采样 ≤640px / 1-2fps / JPEG70；空闲自动停采；远程 relay 场景限 1fps |
| 鉴权/跨会话泄露（帧流端点被未授权访问） | 高（安全） | 复用 web token + 会话归属强校验 + 帧流端点仅限 WS 且按 session 隔离；验收含安全用例 |
| headless 渲染差异（视频/WebGL/canvas/部分登录页） | 低-中 | 作为已知限制写入文档；必要时 handoff 到可见浏览器（已有能力） |
| iframe 兜底受 X-Frame-Options/CSP 限制 | 中 | 兜底仅「尝试打开」，失败显示 URL 卡片 + 引导用户用 handoff |
| 多会话并发帧订阅资源占用 | 低 | 帧订阅按需开启（agent 操作 browser 工具时激活），空闲 30s 断开 |
| 方案 A 依赖 CDP screencast 在现有 Chromium 版本行为 | 低 | fallback 定时 captureScreenshot；两者 API 均稳定 |

## 9. 验收标准

1. Agent 调用 `browser open/navigate` 后，Chat 右侧自动出现并激活「Browser」面板，实时显示当前页面画面（≥1fps，画面延迟 ≤2s）。
2. Agent 滚动/点击/输入后，画面反映实际页面状态变化（非仅 URL 变化）。
3. 页面切换（`switch_tab`/`navigate`）后视图自动跟随当前页面。
4. 面板可手动开关/固定；不同会话画面互不串扰（会话隔离用例通过）。
5. 现有 browser 工具 22 个 action 行为零改动，`screenshot`/`capture_page` 离散截图 artifact 仍正常。
6. `handoff` 场景面板显示「已移交可见浏览器」状态提示。
7. 远程节点（`cdp_relay_node`）场景帧流可用（允许降帧率至 1fps）。
8. 帧流端点未授权访问被拒绝（安全用例通过）。
9. 帧流不可用时降级路径（URL 卡片 / Preview iframe 尝试）可用。

## 10. 里程碑建议

- **P0（1-2 人日）**：方案 B 兜底——Preview 面板增加「跟随 Agent 当前 URL」模式 + browser 工具事件联动（URL 显示）。
- **P1（4-7 人日）**：方案 A——后端 screencast 帧订阅 + WS 端点；前端 Browser dock 面板 + 帧渲染 + 自动跟随。
- **P2（可选）**：画面内反向交互（CDP Input 转发）、帧流录制/回放。
