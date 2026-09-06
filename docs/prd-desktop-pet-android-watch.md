# PRD: 桌宠（Desktop Pet）与 Android Wear OS 手表客户端

> 状态：草案（调研产出，待评审）
> 日期：2026-09-05
> 关联：任务卡 t-1788517951199-1；godex v1.4.0
> 调研方式：godex 本地代码盘点（internal/runtime/httpapi、internal/services/webpush、internal/plugins/taskboard）+ 官方文档核实（live2d.com、android-developers.googleblog.com）+ 领域知识交叉确认。当日 web_search 通道持续故障（见 docs/tools_issues.md 2026-09-05 条目），部分数据点以官方文档开头/既有知识为准并标注。

---

## 1. 背景

godex 是本地 AI 编程助手（Go 单二进制 + 内嵌 Web UI + 本地 HTTP 服务），带**任务看板（taskboard）**：卡片在独立执行会话中运行，状态流转 backlog→todo→in_progress→in_review→done，执行进度可观测（`/v1/taskboard/cards/{id}/executions/{executionID}/observe`）。

**缺口**：

1. 用户离开浏览器/终端时，**无法及时感知任务完成**——需要"看一眼就知道"的常驻提醒形态。
2. **输入与状态查看的移动入口缺失**——出门/离开工位时想给 godex 发指令、看任务状态、收完成通知。

**本 PRD 调研两个客户端方向**：

- **桌宠（Desktop Pet）**：桌面常驻的动画角色，任务完成时即时"跳出来"汇报（气泡/动画），是任务状态展示终端。
- **Android Wear OS 手表客户端**：输入入口（语音/文本）+ 状态查看终端 + 任务完成通知终端。

---

## 2. 目标

- **G1 桌宠**：任务完成事件到达时，桌宠立即用动画 + 气泡汇报"哪个任务完成了、结果如何"；空闲时静默悬浮（低干扰）。
- **G2 手表端输入**：在手表上向 godex 发起新任务/查询状态（语音为主、文本为辅）。
- **G3 手表端展示**：任务列表/状态查看（滚动列表，Wear OS 圆屏适配）。
- **G4 手表端通知**：任务完成推送通知（含卡片标题/结果摘要），点按直达详情。
- **G5 成本约束**：尽量复用 godex 现有通道（REST / Web Push / WebSocket / SSE），不改造 taskboard 核心账本。
- **G6 非目标**：不重写 godex Web UI；不做桌宠 AI 对话大脑（仅状态播报）；不做 iOS watchOS（另行评估）。

---

## 3. godex 现有对外通道盘点（本地核实）

| 通道 | 现状 | 对本项目价值 |
|---|---|---|
| **REST** `internal/runtime/httpapi/routes_*.go` | `/v1/taskboard/projects`、`/cards`、`/cards/{id}`、`/cards/{id}/executions/{id}/observe`、`/reconcile`、`/status`（`internal/plugins/taskboard/plugin.go`）；/sessions、/turns 等 | 桌宠/手表**拉取**任务状态、执行进度 |
| **Web Push** `internal/services/webpush` + `internal/runtime/httpapi/push.go` | `GET /push/public-key`、`POST /push/subscribe`、`POST /push/unsubscribe`、`POST /push/test`；VAPID 密钥，`Notify(ctx, title, body)` 全量广播；**订阅仅存内存**，未持久化；中心不落盘推送状态（node-mesh 设计） | 桌宠/手表**接收**任务完成通知的现成通道（浏览器/Android 均可订阅，Web Push 协议标准） |
| **WebSocket** `internal/runtime/httpapi/routes_voice.go` | `GET /v1/voice`（语音桥，query token 认证），另有 browser CDP 桥 | 桌宠/手表的**实时双向通道**（长连 + 低延迟），可承载任务事件流与输入上行 |
| **SSE** `internal/runtime/httpapi/routes_anthropic.go` | 现用于 LLM 协议转发（text/event-stream + Flusher） | 无独立任务事件 SSE 端点，**需新增**（或在 WS 上复用） |
| **认证** | `protected` handler + `web.token`（远程已启用，本地匿名） | 桌宠/手表远端接入需带 token；WS 走 query token（参照 voice） |

**关键结论**：
- godex 已具备 Web Push 能力，**桌宠/手表"任务完成通知"可直接订阅 `/push` 通道**，需补一个"任务完成 → `webpush.Notify`"的事件钩子（现 taskboard 无完成广播，需在插件层补：状态进入 done/in_review 时触发）。
- godex 已具备 WebSocket 桥模式（voice 先例），**实时任务事件流可按同样模式新增 `/v1/events`**（WS 或 SSE），桌宠/手表均可消费。
- REST 面完整，拉取型查询（列表/详情/执行进度）**零新增**。

---

## 4. 方案对比

### 4.1 桌宠框架（>=2 种）

| 维度 | A. Electron 桌宠 | B. Live2D 原生桌宠 | C. Web/浏览器桌宠（PWA/独立窗口） | D. 系统原生（macOS Swift / Windows） |
|---|---|---|---|---|
| **代表作** | Electron + pixi.js/canvas 自绘；开源桌宠多为 Electron/Web 壳（如各类 GitHub 桌宠项目） | Live2D Cubism SDK（官方多平台，含 Web/Windows/macOS/Android/iOS；live2d.com/en/sdk/about/）；Cubism Editor 制作模型 | Shimeji-ee 类 Java 桌宠、BongoCat 等社区项目；浏览器扩展/PWA | 每平台手写（NSWindow/透明窗口 + SpriteKit 等） |
| **动画质量** | 中（pixi 2D 精灵/序列帧，Lottie 可加分） | **高**（实时变形 2D，模型生态大） | 低-中（受浏览器窗口限制，可做序列帧/Live2D Web SDK） | 高（原生动画能力，但每平台重做） |
| **与 godex 集成** | 本地 HTTP/WS 直连 localhost；Electron main 进程持 WS/SSE 长连，渲染进程显示 | 本地 HTTP/WS 直连；SDK 负责渲染，事件由宿主逻辑驱动 | 浏览器直接连 localhost（需 CORS/认证）；PWA 可订阅 Web Push | 本地 HTTP/WS 直连 + 系统通知 |
| **开发语言/成本** | TS/JS，中（Electron 打包 60-150MB） | 官方 SDK + 各平台宿主，中-高（模型制作另需美术） | Web 栈，低-中 | Swift / C#，高（三平台三套） |
| **常驻/置顶/托盘** | ✅ 成熟（tray、always-on-top） | 需各平台宿主实现 | ⚠️ PWA 无托盘/置顶受限 | ✅ 全能力但全手写 |
| **分发** | 安装包（体积大） | 各平台 SDK 构建 | 免安装/商店可选 | 每平台构建 |
| **适合场景** | **快速实现 + 跨平台 + 深集成** | 追求角色质感、模型团队已有 Live2D 资产 | 最低成本尝鲜、团队纯 Web 栈 | 已有原生桌面团队 |
| **风险** | 体积/内存（Electron 常驻 100-500MB）；供应链 | SDK 授权（Cubism 免费版有商用限制，需核对 license）；美术成本 | 置顶/托盘弱；浏览器策略限制 | 三平台维护成本最高 |

### 4.2 Wear OS 客户端

| 维度 | 方案 A：独立 Wear OS App（Kotlin + Compose for Wear OS） | 方案 B：手机 Companion App + Wear 转发 | 方案 C：PWA/浏览器在手表（不推荐） |
|---|---|---|---|
| **开发栈** | Kotlin + Jetpack Compose for Wear OS（`androidx.wear.compose:material`）；官方模板（android-developers.googleblog.com 2021-10 起稳定，developer.android.com/wear 文档站）；minSdk 视 Wear OS 版本（Wear OS 3+ 建议 API 30+，旧设备 API 28 兜底） | 同 A + 手机端 App（转发层） | 无原生栈 |
| **与 godex 通信** | ① WebSocket（OkHttp）直连 godex WS/events——实时状态+输入上行；② REST（Retrofit/Ktor）拉取列表/详情；③ FCM 推送任务完成通知（经 godex `/push` 或手机转发） | 手机 App 持 WS/REST，经 WearableListenerService/MessageClient 转发给手表；FCM 到手机再转发 | 手表浏览器受限（Wear OS 浏览器生态弱），❌ |
| **语音输入** | ✅ Wear OS 内置语音识别（SpeechRecognizer / GMS voice input），转文本发 REST/WS | 同 A（语音在手表面，转发给手机→godex） | ❌ |
| **文本输入** | ✅ Compose 键盘/手写（圆屏输入体验一般，作为语音补充） | 同 A | 弱 |
| **通知展示** | ✅ Notification API（NotificationCompat + Wear 风格），任务完成直接推；点按 deep link 进 App 详情 | ✅ 通知由手机转发的 Wear 通知 | ⚠️ Web Push 在手表的支持极弱 |
| **后台限制** | ⚠️ Wear OS Doze/后台限制：长连 WS 不可靠，**通知应走 FCM/系统推送**，WS 仅前台活跃时使用 | ✅ 手机常驻更可靠，手表依赖手机 | — |
| **复杂度** | 中（原生开发 + 通信 + 认证） | 高（三端：手表+手机+godex） | — |
| **分发** | Google Play（Wear OS 需商店或侧载；国内环境侧载） | Google Play（手机 + Wear） | 无需 |
| **风险** | 手表直连 godex 需 godex 可公网/内网可达 + token；FCM 需 Google Play Services（国内设备受限） | 多一跳、维护两 App | 不成立 |

---

## 5. 推荐方案

### 5.1 桌宠：Electron + Web 渲染（方案 A，渐进增强 Live2D）

- **首选 Electron**：godex 团队为 Web/TS 栈（Web UI 是 React/Vite），Electron 可直接**复用现有 Web UI 组件/API 客户端**；托盘/置顶/开机自启成熟；WS 长连 + 任务完成动画成本最低。
- **渲染层渐进**：M1 用 pixi.js/Canvas 精灵 + 序列帧（零美术）；M2 若需要角色质感，引入 **Live2D Cubism Web SDK**（live2d.com 官方支持 Web 平台，模型生态大）——同为 Web 栈，迁移平滑。
- **集成方式**：Electron main 进程持 `WebSocket → /v1/events`（新增，见 §6）或轮询 REST `/v1/taskboard/cards`；收到"任务完成"事件 → 主进程 → 渲染进程播放完成动画 + 气泡汇报（标题/结果摘要）；托盘菜单提供"打开看板"。
- **备选路径（最低成本）**：先做 PWA/浏览器标签页桌宠（无 Electron 依赖），验证事件流与交互后再决定是否壳化。若产品强调"不常驻大进程"，可改系统原生托盘 + 通知（放弃动画，仅气泡/通知）。

### 5.2 手表：独立 Wear OS App（Kotlin + Compose for Wear OS，方案 A）

- **开发栈**：Kotlin + Compose for Wear OS（官方稳定）；Gradle/AGP 常规配置。
- **通信分层**：
  - **前台**：WebSocket（OkHttp）连 godex `/v1/events` 拿实时状态；REST 拉列表/详情/执行进度。
  - **后台/离线**：FCM 推送（godex `/push` 订阅，经 Google 发送）承载任务完成通知——规避 Wear OS Doze/长连失效。
  - **输入**：语音（内置 SpeechRecognizer）→ 文本 → REST `POST`（复用现有 session/turn 或 taskboard 创建链路）。
- **通知**：NotificationCompat 构建 Wear 通知，点按 deep link 到 App 内卡片详情。
- **网络与认证**：手表直连 godex 需可达地址（内网/公网隧道）+ `web.token`；godex 侧认证复用现有 `protected` + token 机制。

---

## 6. godex 侧需新增的集成面（最小改动）

| 编号 | 改动 | 说明 | 工作量 |
|---|---|---|---|
| E1 | **任务完成事件钩子** | taskboard 插件层：卡片状态离开 in_progress（进入 in_review/done）或执行结束（`closeRunningExecutions` 处）时触发回调 → 组装 `{card_id,title,result,execution_id}` | 小（插件层 ~50-100 行 + 测试） |
| E2 | **任务事件流端点 `/v1/events`** | 新增 WS（参照 voice 桥，query token 认证）或 SSE（参照 anthropic Flusher 模式），把 E1 事件广播给订阅者；桌宠/手表前台实时消费 | 中（httpapi 新路由 ~150-250 行 + 测试） |
| E3 | **任务完成 → Web Push 通知** | E1 事件同时调 `webpush.Notify`（现成 Service，`Notify(ctx,title,body)` 全量广播）；订阅持久化可选（现仅内存，重启丢失——若手表 FCM 接入则改用 FCM 直发，Web Push 仅服务桌宠/浏览器） | 小-中（E1 内几行 + 可选持久化） |
| E4 | **FCM 接入（可选，手表通知必达）** | godex 增加 FCM 发送（server key）或经手机转发；替代 Web Push 在手表侧的不足 | 中（需 Google 项目 + server 端 ~100 行） |
| E5 | **CORS/认证放宽（仅本地桌宠需要）** | 本地 Electron 连 localhost WS/REST：WS 用 query token（已有先例），REST 加 token header，一般无需 CORS 改动（Electron 非浏览器同源策略） | 小 |

> 原则：**taskboard 核心账本零改动**，事件为新增旁路（observer/订阅模式），不侵入状态机。

---

## 7. 实现代价

### 7.1 桌宠

| 路径 | 人力 | 复杂度 | 风险 |
|---|---|---|---|
| **P1 轻量（推荐先做）**：Electron 壳 + Canvas 精灵动画 + WS 事件 + 气泡/托盘 | **2-3 人周**（含 E1+E2） | 中 | 低-中：Electron 体积/内存；动画效果依赖美术资源 |
| P2 完整：P1 + Live2D 模型 + 状态机（空闲/工作/完成）+ 开机自启 | +2-3 人周（需模型资产或采购） | 中-高 | 中：Live2D 授权条款需核对；美术成本 |
| P3 备选：PWA 桌宠（无 Electron） | 1-1.5 人周 | 低 | 中：托盘/置顶受限，交互降级 |

### 7.2 Wear OS 手表

| 路径 | 人力 | 复杂度 | 风险 |
|---|---|---|---|
| **W1 轻量（推荐先做）**：Compose for Wear OS + 列表/详情 + WS/REST + 语音输入 + 本地通知 | **4-6 人周**（含 E1/E2/E3） | 中-高 | 中：Wear OS 设备碎片化（Wear OS 2/3/4/5）；手表直连需网络可达 + token；无 Google Play 设备需侧载 |
| W2 完整：W1 + FCM 必达通知（E4） | +1-2 人周（Google 项目 + server 端） | 中 | 中：FCM 依赖 Google Play Services（国内设备受限，需评估是否用厂商推送/手机转发兜底） |
| W3 完整 + 手机 Companion 转发 | +4-6 人周（三端） | 高 | 高：多一跳、两 App 维护，仅当手表无法直连时才值得 |

**合计（桌宠 P1 + 手表 W1）**：约 **6-9 人周** + 1 人周联调/验收；若含 Live2D（P2）与 FCM（W2）再加 3-5 人周。

---

## 8. 风险与缓解

| 风险 | 影响 | 缓解 |
|---|---|---|
| **web 通道不稳定影响本次调研**（lightpanda/duckduckgo 故障，见 tools_issues.md） | 调研完整性 | 已本地盘点为主 + 官方文档直连 + 领域知识交叉；收口不无限重试 |
| **taskboard 无完成事件**（现无广播） | 桌宠/手表拿不到"完成"信号 | E1 旁路事件钩子，不动状态机 |
| **Web Push 订阅仅内存**（重启丢失） | 通知可达性 | 轻量期可接受；W2 转 FCM 或做订阅持久化 |
| **Wear OS 后台限制/Doze** | WS 长连失效、通知延迟 | 通知走 FCM/系统推送，WS 仅前台使用 |
| **手表直连 godex 的网络与认证** | 不可达/401 | 内网/隧道（如已有的远程部署 godex.claw.carc.top）+ web.token；token 管理 UI 后续做 |
| **FCM 国内设备受限** | 通知必达打折 | 手机转发兜底（W3）或厂商通道；评估后定 |
| **Live2D 授权**（Cubism 免费版商用限制） | 法律/成本 | 立项前核对 license；备选 pixi/Lottie |
| **设备碎片化**（Wear OS 2-5 + 圆屏） | UI/兼容工作量 | 官方模板 + 最小支持 API 28/30；测试矩阵限定 1-2 款设备 |

---

## 9. 验收标准（草案）

1. **桌宠**：godex 上任意 taskboard 卡片完成（进入 done/in_review）时，桌宠在 ≤3s 内播放完成动画并弹气泡，气泡含卡片标题与结果摘要；可开关静默模式；托盘可一键打开 Web 看板。
2. **事件流**：`/v1/events`（WS/SSE）能收到任务完成事件（含 card_id/title/execution_id）；断线重连自动恢复（参照 Web UI SSE 断线重连既有能力）。
3. **通知**：桌宠订阅 Web Push（或 W2 的 FCM）后，卡片完成时收到系统通知；点按可达对应卡片。
4. **手表**：可列出任务（含状态徽标）、查看卡片详情与执行状态；语音输入能创建/追加任务到 godex；任务完成时手表收到通知。
5. **认证**：远端接入使用 web.token；无 token 请求被拒（401）。
6. **回归**：taskboard 插件与 Web UI 全量测试无新增失败（事件为旁路，不影响账本）。

---

## 10. 参考来源

- Live2D Cubism SDK 官方页：https://www.live2d.com/en/sdk/about/ （官方多平台 SDK，含 Web）
- Compose for Wear OS 官方公告：https://android-developers.googleblog.com/2021/10/compose-for-wear-os-now-in-developer.html
- Wear OS 开发文档站：https://developer.android.com/wear （当日 JS 渲染受限，正文未全量抓取）
- godex 本地代码：internal/runtime/httpapi/push.go、routes_voice.go、routes_anthropic.go；internal/services/webpush/；internal/plugins/taskboard/plugin.go
- 并行任务产出：docs/prd-desktop-app-wrap.md（桌面壳调研，t-1788517972939-2）
