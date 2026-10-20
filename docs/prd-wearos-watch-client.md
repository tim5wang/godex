# PRD: GoDex Wear OS 手表客户端（腕上遥控器）

> 状态：Draft（调研 + 设计产出，待评审）
> 日期：2026-10-20
> 关联：`docs/prd-desktop-pet-android-watch.md`（前一版桌宠 + Wear OS 调研，本文件取代其手表部分并扩写）、`docs/node-mesh-design.md`、`docs/node-onboarding.md`、`docs/agent-step-platform-design.md`
> ⚠️ **平台差异（已核实）**：小米 Watch 5 等设备存在**双系统分区域版**（国行 HyperOS 3 / 海外 Wear OS 6），但**两者底层都是 Android** → **本文件的 Wear OS 实现栈（Kotlin + Compose for Wear OS）对两版均适用**，无需第二套实现。国行差异集中在三处：**① 无 GMS → FCM 不可用（推送必须走手机端桥接）② 分发靠侧载（无 Play）③ Tile / Complication / Ongoing Activity 属系统组件，需真机验证并可降级**。详见 `docs/prd-xiaomi-watch5-compat.md`。§7.7 手势结论（禁水平滑动业务语义）与后端集成面 W1–W8 与平台无关，仍然有效。
> UI 设计稿：`docs/public/mockups/wearos/`（15 张圆屏稿，双击 `index.html`）
> 调研方式：官方文档直连（developer.android.com、Android Developers Blog、Home Assistant companion 文档）+ 同类产品公开资料 + godex 本地代码盘点（`internal/runtime/httpapi`、`internal/services/webpush`、`internal/services/noderegistry`、`mobile/`）。引用来源见 §12。

---

## 1. 背景与定位

godex 已有：Web UI、**移动端**（`mobile/`，Capacitor 壳，Android 可本地跑 agent）、**节点互联**（中心 + 内网节点 relay，`godex node join`）、**taskboard**（卡片在独立执行会话运行）、**Business Flow**（含 `waiting_human` 人工卡点）、**Agent Step Platform**（业务系统把 agent 当环节调用）、**Web Push**。

**缺口**：离开工位时，「看一眼 / 点一下 / 说一句」的物理入口缺失。手机要掏出来解锁，通知容易被忽略；而手表是**永远在腕上、抬起即看**的形态。

**本文件的定位（用户需求原文四条）**：

| # | 需求 | 设计结论 |
|---|---|---|
| 1 | 原生组件开发，低功耗，少外部依赖 | Kotlin + **Compose for Wear OS**（`androidx.wear.compose`）；仅用官方 AndroidX/Wear 库，不引入第三方 UI 框架、不引入 WebView、不引 GraphQL/ORM。零本地持久化重担（只用 DataStore 存一个 token 和偏好） |
| 2 | 优先实现圆表盘 | 全部界面按 456×456 圆屏安全区设计（外圈 24px 不放可点元素），见 `docs/public/mockups/wearos/` |
| 3 | 手表**不跑 agent**，定位为遥控器（按钮 + 语音）+ 通知硬件 | 表上无 agent loop、无工具执行、无文件系统访问。手表只做三件事：**发指令**、**收通知**、**做决定（审批）** |
| 4 | 合理的绑定与互操作（类 godex 节点 / 米家 / 鸿蒙） | **账号 + 设备码绑定**（RFC 8628 式），同一账号下所有端共享同一份任务/审批队列；手表通过「当前遥控目标」在手机 / 云端 / PC 之间切换 |

**一句话**：手表是 godex 的**腕上遥控器与通知终端**，不是第二个 agent 宿主。

### 非目标

- 不在手表上运行 godex 二进制或任何 agent（明确排除；Android 手机端的本地 agent 实验仍留在 `mobile/`）。
- 不做 watchOS（另行评估；本文件只覆盖 Wear OS）。
- 不做手表端的完整 Web UI 或聊天式长文本阅读（圆屏 + 小电池不适合）。
- 不改 taskboard / Flow 的核心账本与状态机（新增事件与聚合读接口为旁路）。
- 不做手表端的文件浏览、终端、diff 审阅（这些留在手机 / PC）。

---

## 2. 调研：同类应用怎么做

### 2.1 Home Assistant（Wear OS）——最完整、最值得抄的样本

HA 的 Wear OS App 是「手机端 App 的腕上遥控器」的成熟范本：

| 能力 | 做法 |
|---|---|
| **独立运行** | 手表可直连自己的 HA 实例（WiFi / LTE），**不依赖手机在线**；但**首次登录必须借手机 App 完成**，登录后手机 App 不再参与（官方原文：需要一台装了 HA App 的配对手机来登录，登录后手机 App 不再被使用） |
| **首页** | 只暴露可「一键触发」的域（`light` / `switch` / `scene` / `script` / `lock` / `cover` / `fan` / `button`…），不是全量实体 |
| **Favorites（收藏）** | 腕上可设收藏，收藏项在其余实体加载前就渲染出来 → **启动即可点**；也可在手机 App 里拖动排序 |
| **Tile** | Shortcuts tile（最多 **7** 个快捷项）、Template tile（渲染文本，**不可滚动，必须一屏放得下**）、Camera tile、**Assist tile**（一键开口说话）、Thermostat tile |
| **Tile 刷新限制** | 官方明确：系统不保证刷新频率；列表中部/末端的 tile 更新更稀疏；可选「手动 / 进入视野时（Wear OS 6 起不支持）/ 每 x 分钟」。HA 建议先用「视图触发 + 手动刷新」思路设计，并提示可能看到 1–2 秒旧数据 |
| **Complications** | 实体状态（短文本/长文本）挂在表盘上，**亮屏时 + 约每 15 分钟**自动刷新，点一下可强制刷新；另有 **Assist complication** 直接从表盘发起语音 |
| **通知** | 默认走「手机通知桥接到手表」；HA 也支持**直接推给手表**（跳过手机）。Wear 侧只支持 `title` / `message` / `channel` / `icon` / `tag`（替换）/ `vibrationPattern`，**alarm 级通知在 Wear OS 不支持** |
| **通知命令** | 腕上可执行有限的通知命令（清通知、停 TTS、更新传感器等） |
| **传感器反向** | 手表把传感器（电量、心率、Doze/省电状态、网络、步数等）回传给 HA；**传感器上报必须挂一个常驻通知**，否则会被系统杀掉 |

**结论价值**：HA 证明了三件事——(a)「收藏/快捷项 + Tile + Complication」是腕上遥控的标准三件套；(b) Tile 是**不可滚动、单页、刷新不可控**的载体，只能承载「一句话状态 + 一个动作」；(c) 通知的桥接路径（手机 App → 手表）是低功耗的默认通路，比手表自己长连可靠。

### 2.2 Tesla / 车控类（Drive Electric for Tesla）

- 6 个快捷 Tile、电量/充电 → Complication，NFC + 蓝牙 + 可选远程；
- **本地近场（蓝牙/NFC）优先，云端远程兜底**——遥控类应用的通用分层。

### 2.3 消息类（WhatsApp / Telegram Wear OS）

- 独立 App：聊天列表 → 消息历史 → **文本或语音回复**；
- 提供 Tile；未读 → Complication；
- 通知**直接带回复动作**（RemoteInput），腕上回完不用进 App。

### 2.4 语音助手类（Google Assistant → Gemini on Wear OS）

- 手表承担「入口」，计算在云端（**与我们要的架构完全一致**）；
- Gemini on Wear OS 支持语音提问、建任务，并可读取用户收藏的 Google 应用上下文；
- 表盘/侧键/Bubbles 三个开口，其中**快捷开口（长按表冠、Tile、Complication）比 App 图标更常用**。

### 2.5 编程 agent 遥控类（AgentApprove）——最贴近本需求

- 定位：**在 iPhone / Apple Watch 上批准 AI 编码 agent 的命令**；
- 结构：**账号 + 每台机器装一个 installer/integration**，本机侧 `~/.agentapprove/env` 缓存配置；支持「把 Claude Code 的问题发到手机而不是留在终端」；
- 核心动作只有两个：**批准 / 拒绝**，外加查看上下文。
- 启示：对「agent 遥控」而言，**审批是最高价值动作**，比「浏览任务列表」价值高一个量级。

### 2.6 AutoWear / Tasker 与智能家居生态（米家 / 鸿蒙）

- AutoWear：自定义屏幕、浮动图标、语音命令、通知、摇一摇触发——证明腕上「**可配置的按钮组**」模式成立，但配置复杂度是它的主要抱怨点 → 我们要**内置固定动作集**，不做通用配置器。
- **米家**：一台设备归属一个主账号，账号可把设备**共享**给家庭成员（各自账号），共享后每端都能控制同一批设备。
- **鸿蒙**：设备通过「运动健康 App 绑定到手机所登录的华为账号」，再借**超级终端 / 分布式软总线**做跨端流转；同一账号是互操作的前提。
- 启示：**账号是互操作的锚点，端点只是账号下的一个「能力出口」**。这正是需求 4 要的东西。

### 2.7 抽象出的腕上遥控设计模式（本设计直接采用）

| # | 模式 | 落地 |
|---|---|---|
| P1 | **三件套**：Complication（瞥一眼）→ Tile（一点用）→ App（做完事） | 见表/稿件 01、02、03 |
| P2 | **收藏 / 快捷动作先渲染**，不等全量数据 | 主屏三颗圆钮 + Tile 快捷项，本地缓存上次状态先渲染 |
| P3 | **Tile 不可滚动**：一句话状态 + 一个主操作 | 稿件 02 |
| P4 | **通知即界面**：把「批准 / 拒绝」放进通知动作 | 稿件 06、08 |
| P5 | **通知桥接优于手表长连**（HA 官方默认路径） | §5.3 通道分层 |
| P6 | **首次绑定借手机，之后独立** | §6 绑定流程 |
| P7 | **账号是锚点**，同账号多端共享同一数据面 | §7 互操作模型 |
| P8 | **近场优先、云端兜底** | §5.4 传输优先级 |
| P9 | **输入只做两件事**：语音 + 预置按钮 | 稿件 03、07 |
| P10 | **省电三律**：不在待机态跑动画、不常驻长连、不做频繁 Tile 刷新 | §8 |

---

## 3. godex 侧现有资产盘点（本地核实）

| 资产 | 位置 | 对本项目的价值 |
|---|---|---|
| REST：会话 | `GET /sessions`、`GET /sessions/{id}`、`GET /sessions/{id}/events`、`GET /sessions/{id}/longtasks` | 任务列表 / 详情数据面（**零新增**） |
| REST：审批 | `GET /sessions/{id}/permissions`、`POST /sessions/{id}/permissions/{requestID}/approve`、`.../deny` | **审批动作已有**（遥控器最关键动作，零新增） |
| REST：taskboard | `/v1/taskboard/projects`、`/cards`、`/cards/{id}`、`/cards/{id}/execute`、PATCH 卡片 | 看板卡片列表与状态流转（零新增） |
| REST：Flow 人工卡点 | `GET /v1/human-tasks`、`POST /v1/flow-runs/{runID}/human/{nodeID}/reply`、`POST /v1/flow-runs/{runID}/step`、`.../cancel` | 流程人工卡点也能在腕上处理 |
| REST：Business Flow 只读 | `GET /v1/flows`、`GET /v1/flows/{id}/runs`、`GET /v1/flow-runs/{runID}/events`、`GET /v1/flow-inspection` | 运行态与巡检结论可作为「一段话状态」喂给 Tile |
| WS：实时事件 | `GET /v1/flows/{id}/sessions/{sessionID}/ws`（含 SSE 变体 `.../events`）、`GET /v1/voice`（query token 认证） | 前台实时刷新可复用；**已有 query token 认证先例** |
| 语音 | `GET /v1/voice`（PCM 上下行 + ASR/TTS 桥）、`POST /v1/tts`、`GET /v1/tts/stream` | 手表「说一句话」可直接接现有语音桥 |
| Web Push | `internal/services/webpush` + `/push/public-key`、`/push/subscribe`、`/push/unsubscribe`、`/push/test` | VAPID 标准实现，但**订阅仅内存**，且 **Wear OS 浏览器/Web Push 支持极弱** → 手表不能靠它 |
| 节点互联 | `internal/services/noderegistry`（注册/心跳/离线判定）、`internal/services/relay`、`GET /control/nodes`、`/control/nodes/{id}`、`/control/nodes/{id}/proxy/...`、`godex node join`（`ck_` 一次性凭证、`trusted` / `guarded-remote` 信任级别） | **一个可复用的「同一账号下的多端」模型**：端点注册 + 心跳 + 经中心代理访问。手表可直接复用这套心智 |
| 认证 | web token（`protected`）、节点代理 token（`control.node_proxy_token`，只读受限）、业务 key（`biz_`，`withBizKeyAuth`） | 「受限 token」已有先例 → 手表拿**最小权限 token** |
| Agent Step | `POST /v1/agent-steps`、`GET /v1/agent-steps/{id}`、`.../cancel`、`.../reply`（`biz_` 认证） | 业务侧调用面：手表发一条指令可落到同一条链路上 |
| 移动端壳与通知 | `mobile/`（Capacitor）、`mobile/public/godex-watcher.js`（轮询 + 本地通知）、`@capacitor/local-notifications` | 手机端已有「任务完成 → 本地通知」的现成逻辑，可升级为**桥接到手表** |
| 桌面/手机/云 | `docs/prd-desktop-app-wrap.md`、`mobile/README.md` | 「多端同一账号」的既有拼图 |

**关键结论**：

1. **「腕上遥控」所需的数据面与动作面，godex 现有 REST 已基本覆盖**（列表/详情/审批/取消/Flow 人工回复）。手表客户端的后端新增面很小。
2. **缺的是「到腕上的通路」**：Web Push 到不了 Wear OS；**FCM 在国行不可用（无 GMS）**，因此一期只能走**手机端桥接**。
3. **缺的是「腕上可读的摘要」**：现有接口返回的是给 PC 富界面用的结构，需要一层**为腕上收敛的摘要/精简读接口**。
4. **绑定与多端模型可以照抄节点模型**（注册 + 心跳 + 中心代理 + 受限 token + 信任级别）。

---

## 4. 目标（可验收）

| 编号 | 目标 | 验收方式 |
|---|---|---|
| G1 | **瞥一眼**：抬腕 1 秒内知道「有几个任务在跑 / 有没有要我做决定的事」 | Complication + Tile + 表盘胶囊显示运行中/待审批计数 |
| G2 | **点一下**：从 Tile 或主屏 2 次点按内到达任一「需我处理」的条目 | 待审批项在列表置顶，Tile 有 badge |
| G3 | **说一句**：按住说话 → godex 收到文本并落到指定目标（会话/新建任务）→ 腕上可见回执 | 语音转写 + 目标设备显式可见 + 提交成功回执 |
| G4 | **做决定**：审批请求可在腕上批准/拒绝，或从通知直接操作 | 批准后 PC 侧继续执行；拒绝后 agent 收到拒绝 |
| G5 | **多端互操作**：同一账号下手表能看/控手机端、云端、经云端可见的 PC 端 | 设备列表 + 切换「当前遥控目标」后数据面随之切换 |
| G6 | **低功耗**：无任务时手表不保持长连；24h 混合使用额外耗电 ≤ 5%/天（目标值，需真机标定） | 电量对比测试 + `dumpsys batterystats` |
| G7 | **少依赖**：仅官方 AndroidX / Wear 依赖，无第三方 UI、无 WebView | `./gradlew :app:dependencies` 审查 |
| G8 | **非目标守护**：手表端不含 agent 运行时、不访问文件系统、不执行工具 | 依赖与权限清单审查（无网络外的敏感权限） |
| G9 | **多设备切换**：可在账号下任一在线端点间切换「当前遥控目标」 | 设备列表 + 顶部目标 chip；切换 ≤2 次点按 |
| G10 | **会话切换**：可切换「当前会话」（置顶 + 最近），语音与指令默认落到它 | 会话选择器 + 主屏当前会话胶囊；发送后可见回执 |

---

## 5. 架构

### 5.1 角色

```
                    ┌──────────────────────────────────────────┐
                    │        godex 中心 / 云端实例（账号锚点）      │
                    │  · 账号 + 设备注册表（手表、手机、PC、云）    │
                    │  · 统一任务 & 审批队列（单一事实源）          │
                    │  · 推送网关（FCM / 桥接）                    │
                    │  · /control/nodes/{id}/proxy 转发到各节点     │
                    └───────┬───────────────┬──────────────────┘
                            │               │
        ┌───────────────────┘               └───────────────────┐
        │                                                        │
┌───────▼────────┐   BT / 本地网 / 互联网        ┌───────────────▼──────┐
│ 手机端 App      │ ◄── 通知桥接 / 配对引导 ──►  │ 内网 PC 节点          │
│ (已存在 mobile/)│                              │ (godex serve，跑 agent)│
└───────┬────────┘                              └──────────────────────┘
        │ 通知桥接（P5）/ 可选直连
┌───────▼────────────────────────────────┐
│ Wear OS 手表（本项目）                   │
│ · 无 agent、无文件访问、无工具执行        │
│ · 三件事：发指令 / 收通知 / 做审批        │
└────────────────────────────────────────┘
```

### 5.2 手表端只做四类动作

| 动作 | 载体 | 对应已有接口 |
|---|---|---|
| 看状态 | Complication / Tile / 列表 | `GET /sessions`、`GET /v1/taskboard/cards`、`GET /v1/flows/{id}/runs` |
| 发指令 | 按住说话 / 文本 / 预置按钮 | `GET /sessions/{id}/events`（或消息接口）、`POST /v1/agent-steps`、`POST /v1/taskboard/cards/{id}/execute` |
| 做决定 | 审批整屏 / 通知动作 | `POST /sessions/{id}/permissions/{requestID}/approve\|deny`、`POST /v1/flow-runs/{runID}/human/{nodeID}/reply` |
| 收通知 | 系统通知（本地或桥接） | 新增推送通道（§9） |

**手表不调用**：任何工具执行、文件读写、终端、exec、节点管理写操作。

### 5.3 传输分层（按功耗从低到高）

| 层 | 用途 | 说明 |
|---|---|---|
| **L1 推送（默认）** | 通知、badge 更新、「有活干了」的唤醒 | **手机端 App 桥接**（`NotificationManager` 桥接路径，HA 验证过的默认通路）——**国行必选**（无 GMS，FCM 不可用）；海外版可将 FCM 直推作增强。**手表无任务时不保持任何长连** |
| **L2 短连接 REST** | 用户主动打开 App / Tile 刷新时拉取 | HTTP/1.1 + 短超时 + 本地缓存先渲染（P2）；不在后台轮询 |
| **L3 近场（可选增强）** | 手机就在身边时 | 蓝牙/WiFi 直连手机端本地服务，或经 Data Layer（`MessageClient`）转发，省一次云端往返 |
| **L4 前台长连（仅当前台）** | 盯着进度看 | 复用现有 WS（`/v1/voice`、flow session ws）做法；**退到后台立即断开**，绝不做后台长连 |

> 官方立场也支持这一分层：Android 明确建议「Wear OS App 直接联网，不要用 Data Layer 连网络」（`training/wearables/data/network-communication`）；Data Layer 只用于设备间同步，不作为上网通道。但我们把它当作 **L3 近场增强**，不是主通路。

### 5.4 目标选择（「当前遥控目标」）

一个显式状态：**当前遥控目标 = 哪个端点**。所有「发指令 / 看状态」默认作用于该目标。

- 默认取「最近有活动的端点」（**自动跟随**），可一键切换并**手动锁定**（稿件 03 / 09，详 §7.5）；
- 数据面永远经过账号/中心，因此手表**不需要知道节点的内网地址**，与节点接入模型一致（节点主动出站注册，中心代理访问）；
- 切换目标只影响「发给谁」，不影响「能看到什么」——同一账号下所有**在线**端点的任务与审批被聚合进同一份视图（**实时聚合，非中心持久化**，详 §7.4）。

> **「当前设备」与「当前会话」是两个独立状态**（§7.4）。设备决定「指令发给哪台」，会话决定「落在哪条会话」；会话从属于设备。两者都必须在 UI 上常显，用户永不需要猜。

---

## 6. 绑定与配对

### 6.1 方案：设备授权码（RFC 8628 式）+ 二维码

手表作为**输入受限设备**，不适合在腕上敲账号密码。采用行业标准做法（TV / CLI / 手表通用）：

```
① 手表首次启动 → 请求配对
   显示：二维码（内含 verification_uri + user_code）+ 8 位配对码
   同时进入轮询（长间隔、退避），有效期 5 分钟

② 用户在「已信任的一端」（手机 godex App / Web UI）扫码或手输码
   → 显示请求详情：「GoDex Wear（Pixel Watch 3）」请求接入
   → 选择授权范围（scope）与信任级别 → 确认

③ 手表拿到 access token + refresh token（+ 设备 ID）
   显示「已绑定到 <账号>」，进入主屏

④ 撤销：在手机/Web「我的 → 设备」里移除该手表（与节点删除同构）
```

要点：

- **码要短、要显示得大**：8 位数字，圆屏居中 34px 字号（稿件 10）；
- **过期可见**：`二维码 4:58 后失效`；
- **必须显式声明这是遥控器**：绑定确认页写明「本设备不运行 agent，权限为：读写任务 · 审批 · 发送语音」；
- **scope 最小化**，与已有「受限 token」先例一致（`control.node_proxy_token` 只读受限 / `biz_` key 绑定工具集）：

| scope | 允许 |
|---|---|
| `watch:read` | 读任务/会话摘要、读审批队列、读设备列表 |
| `watch:command` | 发指令（语音/文本 → 会话或新建） |
| `watch:approve` | 批准/拒绝（可在绑定页单独勾选，可关闭） |
| `watch:admin` | 默认**不给**（节点增删、配置修改、token 管理一律拒绝） |

### 6.2 与现有模型的对齐

| 现有 | 手表侧对应 |
|---|---|
| `ck_` 一次性凭证 + `godex node join` | 配对码 + 设备授权（一次性、短有效期） |
| `trusted` / `guarded-remote` 信任级别 | `watch:approve` 开 / 关（关 = 只能看和说，不能批） |
| 中心 `GET /control/nodes` 注册表 + 心跳 + 离线判定 | 账号下的设备列表（手表/手机/PC/云）+ 心跳 |
| 中心代理 `/control/nodes/{id}/proxy/...` | 手表请求 → 中心 → 目标端点（手表不知内网地址） |
| 节点删除闭环 | 手表解绑（立即吊销 token，推送停止） |

---

## 7. 多端互操作模型

### 7.1 锚点与共享

- **账号是唯一锚点**（同鸿蒙/米家）：手表、手机、PC、云端实例都是账号下的**端点**，不是独立个体。
- **共享同一份数据面**：任务列表、审批队列、设备可见性都是账号级的「一份」，而不是每端一份。手表上批掉的审批，手机和 PC 上立即消失（同一条队列）。
- **互操作是双向的**：
  - 手表 → 手机：切换目标到手机端，看手机端跑的任务 / 发指令给手机上的 agent；
  - 手表 → 云：云端常驻任务（cron / 巡检 / Flow）在腕上可见可控；
  - 手表 → PC：经云端可见的 PC 节点（走 relay，不需公网 IP，与 `docs/node-onboarding.md` 一致）。

### 7.2 冲突与一致性

| 场景 | 处理 |
|---|---|
| 手表和手机同时操作同一任务 | 服务端为单一事实源；腕上操作后本地乐观更新 + 下一次拉取纠偏；冲突时以服务端为准并提示「已在其它设备处理」 |
| 审批已被其它端处理 | 腕上点按返回 409/已处理 → 立刻换成「已在 PC-01 处理」并移除该条（幂等） |
| 目标端点离线 | 列表置灰 + 显示「离线 · 2 小时前」；发指令时提示将排队（沿用节点离线心智） |
| 手表长时间离线 | 通知由推送通道重投；重连后拉一次增量 |

### 7.3 权限与安全

- 手表 token **只读+受限动作**，永远不含 `watch:admin`；
- 高危审批：可开启「长按 1 秒才生效」（稿件 06 注释、稿件 11 设置）；
- 手表丢失：从任一端「设备」页一键吊销（与节点删除同构）；
- 表上不落盘敏感内容：只存 token + 偏好；任务详情按需拉取，不留历史。

### 7.4 两个正交维度：设备 与 会话

腕上遥控有两个**互相独立**的「当前」，必须分开建模（回答「怎么切设备 / 怎么切 session」）：

| 维度 | 含义 | 存在哪 | 切换入口 |
|---|---|---|---|
| **当前目标设备** | 指令发给哪台 godex（PC / 手机 / 云端） | 账号级，**手表本地记** | 主屏顶部设备 chip → 设备列表（稿件 03 / 09） |
| **当前会话** | 指令落在哪条会话 | **从属于某台设备**（节点本地是唯一事实源） | 主屏底部会话胶囊 → 会话选择器（稿件 13） |

**关键约束（对 §7.1 的精确化）**：godex 的既有决策是「节点本地是会话历史的唯一事实源，中心不持久化」（`docs/node-mesh-design.md`）。所以：

- **会话从属于设备**：切设备 → 会话列表整体换成该设备的，并自动选中它最近活跃的会话；切会话 → 不改设备。
- **「一份队列」= 对在线端点的实时聚合**，不是中心侧持久化的一份。→ 离线端点的待审批**看不见**（我们不在中心造第二份事实源）。这是必须对用户明说的边界，不做假数据。
- **待审批穿越两个维度**：无论当前目标是谁，所有在线端点上「等你决定」的事都要涌到腕上——这正是遥控器的价值。

### 7.5 设备切换（多设备遥控）

- **来源**：复用 `GET /control/nodes`（已有 `status` / `capabilities` / `trust_level` / `last_seen`）。
- **操作**：主屏顶部设备 chip 点按 → 设备列表（稿件 09）→ 选中生效，**2 次点按内完成**。
- **角标**：每个设备带该设备下的「待审批数 / 运行中数」，一眼看出**哪台需要我**。
- **自动跟随 vs 手动锁定**：默认跟随最近活跃的端点；手动锁定后顶部 chip 显示锁图标，不再被自动切换。
- **离线端点**：置灰 + `离线 · 2 小时前`；可选中但为空态并说明原因（稿件 14 的 `office-db 离线，读不到它的会话`），不允许发指令。
- **guarded-remote 端点**（`docs/node-onboarding.md` 信任级别；`internal/services/relay/proxy.go` 实证：该级别下经中心代理的**写操作**需带审批头才放行）：即「给不受信节点发指令」**本身会先生成一个待审批项**。手表必须在下发前提示，否则用户误以为指令已执行。
- **云端实例视作一个端点**（`kind=cloud`），可见性/可操作性一致。

### 7.6 会话切换

- **来源**：当前目标设备的 `GET /sessions`（经 `/control/nodes/{id}/proxy/sessions`），按 `last_activity_at` 降序；字段收敛为 `{title, running, 相对时间, 分支/父会话标记}`。
- **三段结构**（稿件 13）：
  1. **置顶**——手表本地 DataStore（跨设备可用）；长按置顶/取消；
  2. **最近**——服务端排序的列表，每条带**设备徽标**；
  3. **不可用项**置底（离线设备的会话）。
- **选中即成为当前会话**：语音 / 文本 / 预置按钮都默认落到它（顶部目标行与底部胶囊始终显示）。
- **与发送的关系**（对齐既有 Steer 语义「发送一律排队」）：当前会话正在 `running` 时，腕上发送 = **入队**，腕上显示「已排队」并提供「引导 / 取消」（`POST /sessions/{id}/queued/{queueID}/steer|cancel`）；**不静默丢弃**。
- **从通知/审批进入时自动定位**到对应「设备 + 会话」（deep link 同时带 device_id + session_id），不让用户先手动切两次。
- **不在腕上新建会话**：圆屏键盘 + 无 prompt/模板选择器场景不合理；腕上只允许「进已有会话」或「起一个 agent-step」（§9 W8）。

### 7.7 手势与冲突（重要修正）

Wear OS 3+ 的系统手势是 **右滑 = 退出 / 返回**；小米 HyperOS 表端右滑是**负一屏**、左滑是**应用卡片**（详见 `docs/prd-xiaomi-watch5-compat.md` §4）。两平台的共同点是**水平滑动均被系统占用**。因此：

- **禁止用水平滑动承载业务语义**——原稿件 05 的「左右滑 = 上一个/下一个任务」**作废**，改为「列表 + 旋表冠 + 底部圆钮」；
- 滚动一律用**旋表冠 / 表圈**（`ScalingLazyColumn` + `PositionIndicator`），点按作降级路径；
- 「切设备 / 切会话」用**显式 chip 入口**，不用手势；
- **返回用显式顶部控件**，不依赖系统右滑（小米右滑是负一屏，不是返回）；
- **长按只承担次要动作**（置顶、长按批准），不承担导航。

---

## 8. 低功耗与「少依赖」的具体约束

| 约束 | 做法 | 依据 |
|---|---|---|
| 不做后台长连 | 推送唤醒 + 前台短连；退后台立即断开 WS | Wear OS Doze 下长连不可靠 |
| 不做后台轮询 | 手机端 watcher 的轮询模式**不搬到手表**；改用推送 | 轮询是腕上耗电主因 |
| Tile 刷新不贪心 | 默认「进入视野刷新 + 手动刷新」，最多 30 分钟后台刷新一次；**接受 1–2 秒旧数据** | 官方明确不保证刷新频率 |
| Complication 只给极简值 | 短文本 + `RANGED_VALUE`（进度）；亮屏时 + ~15 分钟刷新，点按强制刷新 | HA 实测行为 |
| 常亮态最简 | AOD 只显示「运行中 N / 待审批 M」纯文本，无动画、低亮 | 官方 always-on 指南 |
| 通知不用 alarm 级 | 用普通 channel + 可选震动模式 | Wear 不支持 alarm stream |
| 传感器上报（若做）需挂通知 | 一期**不做**传感器回传，避免常驻通知 | HA 的代价说明 |
| 依赖最小化 | Compose for Wear OS + Wear Tiles + Wear Complications + DataStore + OkHttp/官方推荐栈；**不引** WebView、不引第三方 UI 库、不引大体积 DI 框架 | 需求 1 |
| 启动即用 | 本地缓存上次状态先渲染（P2），网络结果后到覆盖 | HA favorites 经验 |

---

## 9. godex 侧需新增的集成面（最小改动）

| 编号 | 改动 | 说明 | 量级 |
|---|---|---|---|
| **W1** | **设备注册表扩展：端点类型** | 现有 `noderegistry` 加 `kind`（`watch` / `phone` / `pc` / `cloud`）+ 手表 scope；`GET /control/nodes` 返回值带类型 | 小（字段 + 校验 + 测试） |
| **W2** | **配对码端点** | `POST /watch/pair/start`（手表出码，返回 `user_code` + `verification_uri` + 过期）、`POST /watch/pair/approve`（信任端确认）、`POST /watch/pair/token`（手表轮询换 token）；签发受限 token（scope 内） | 中（~200–300 行 + 测试；与 node join 同构） |
| **W3** | **腕上摘要读接口** | `GET /watch/overview`：一次返回 {运行中数、待审批数、节点在线数、当前目标、最近 1 条事件一句话}；字段为此**收敛设计**（给 Tile / Complication / 表盘用） | 小（聚合已有服务） |
| **W4** | **审批聚合读** | `GET /watch/approvals`：把 `sessions/{id}/permissions` 与 `/v1/human-tasks` 合并成**一张待决队列**（含来源、命令/问题、风险提示、超时） | 小-中 |
| **W5** | **推送通道到腕** | **一期即走手机端桥接**（`mobile/` 已有本地通知逻辑 → 改造为「转发给配对手表」）。**FCM 直推不可用**：国行无 GMS（海外版可用，作为增强）；国内替代可评估厂商推送通道 | 中（一期桥接） |
| **W6** | **事件 → 推送钩子** | taskboard 卡片状态离开 `in_progress`、Flow 进入 `waiting_human`、审批请求创建时 → 触发推送（现 taskboard 无完成广播，需旁路钩子，**不动账本**） | 小 |
| **W7** | **手表 token 的鉴权中间件** | 类似 `withBizKeyAuth` / `nodeProxyRead`：`withWatchTokenAuth(scopes...)`，越权 403 | 小 |
| **W8** | **语音落到目标** | 手表语音文本 → 二选一：追加到指定会话，或 `POST /v1/agent-steps` 起一个环节；前者复用现有会话链路 | 小（若复用则接近零新增） |

> 原则：**taskboard / Flow 账本零改动**；新增均为旁路读取与受限写入，全部走已有的「受限 token + 信任级别」范式。

---

## 10. 界面与交互设计

设计稿在 `docs/public/mockups/wearos/`（15 张圆屏稿 + 画廊）：

| 稿 | 界面 | 关键设计决策 |
|---|---|---|
| 01 | 表盘 + Complications | 四角挂载：运行中 / 待审批 / 节点在线 / 当前设备；外圈进度环为总体进度 |
| 02 | **Tile** | 不可滚动 → 一句话状态 + 单主操作 + 外圈环 + 「1 待审批」badge |
| 03 | **主屏（遥控器）** | 中央超大「按住说话」（HOLD TO TALK，带虚线 hold 环）；下排三颗 62dp 圆钮 = 任务 / 审批 / 设备，各带角标 |
| 04 | 任务列表 | 状态点 + 一句话副标题（设备/耗时/当前动作）+ 细进度条；待审批项红色置顶 |
| 05 | 任务详情 | 进度环 + 三行事实（设备/已用时/当前工具）+ 最近动作日志；底部只留暂停·停止（**不用左右滑切任务**，与系统右滑退出手势冲突，见 §7.7） |
| 06 | **审批** | 整屏一个决定：原命令 + 风险解释 + 批准/拒绝；高危可要求长按 1 秒 |
| 07 | **语音输入** | 波形 + 实时转写回显 + 显式目标设备；松开发送、上滑取消、转写在本机完成 |
| 08 | 通知 | 完成通知（看摘要 / 让它提交）与审批通知（批准·拒绝**直接放腕上**） |
| 09 | 设备 / 节点切换 | 同一账号下的手机/云/PC 一张列表；点按切换「当前遥控目标」 |
| 10 | 配对绑定 | 二维码 + 8 位码 + 三步进度条；明确写出「本表是遥控器，不跑 agent」 |
| 11 | 设置 | 通知 / 省电 / 安全三组；审批请求标为「不可关闭」；显式写出本表权限范围 |
| 12 | 常驻活动 / Live Update | 长任务在腕上有持续入口（通知栏常驻项 + 表盘底部胶囊） |
| 13 | **会话切换** | 置顶（本地）+ 最近（跨设备带设备徽标）；点按即成为「当前会话」 |
| 14 | **跨设备统一列表** | 设备筛选 chips 是**视图**不是模式；离线端点显式说明读不到 |
| 15 | **待审批队列** | 跨设备统一、**不可筛选**；按「高危（需长按）/ 常规」分组 |

**交互约束（全稿统一）**：

- 圆屏安全区：外圈 24px 不放可点元素；正文左右 22–24px；三段式（顶部状态 / 中部滚动 / 底部操作）。
- 触控 ≥ 48dp：圆钮 62dp、主按钮 78dp。
- 列表支持旋转表冠/表圈（rotary input），并保留点按降级路径。
- 输入优先「说一句 + 点一下」，文本输入仅备选。
- 语义色全稿一致（运行中蓝 / 待审批红 / 完成绿 / 离线灰），且**颜色旁必有文案或图标**。
- **不使用水平滑动承载业务语义**（系统右滑 = 退出/返回）；滚动用旋表冠，进出用显式按钮/chip。
- 「当前设备」与「当前会话」是两个独立可见的状态，始终显示在顶部或底部，用户永不需要猜「我这句话发给谁了」。

**Compose for Wear OS 组件映射**（实现参考，不在本稿范围）：

| 设计元素 | Wear Compose |
|---|---|
| 顶部时间/状态行 | `TimeText` |
| 圆屏居中主内容 | `ScalingLazyColumn` + `PositionIndicator`（自动实现旋转表冠滚动） |
| 圆钮 / 主按钮 | `Button`（`IconButton`）以 `Modifier.size(62.dp)` / `78.dp` |
| 列表项 | `Chip` / 自定义 `Card` |
| 审批二选一 | `ConfirmationDialog` 或全屏 `ScreenScaffold` + 双 `Button` |
| 进度环 | `CircularProgressIndicator`（弧形变体用于 Tile 外圈） |
| 语音 | `SpeechRecognizer`（或 `RemoteInput`），波形自绘 |
| Tile | `androidx.wear.tiles` `TileService` + `PrimaryLayout` + `Clickable` |
| Complication | `androidx.wear.complications` 的 `SHORT_TEXT` / `RANGED_VALUE` data source |
| 常驻活动 | Ongoing Activity API（可复用同一 `Notification`） |

---

## 11. 分期与验收

| 阶段 | 范围 | 后端新增 | 验收 |
|---|---|---|---|
| **M1 只读遥控** | 主屏 + 任务列表 + **设备切换（§7.5）** + **会话切换（§7.6）** + 任务详情；配对绑定（W2）+ 摘要读（W3）+ 腕 token 鉴权（W7） | W2/W3/W7 | 绑一次即用；切换设备后会话列表跟随刷新；切换会话后语音落点正确；无后台长连 |
| **M2 通知与审批** | 审批整屏 + 通知动作 + Complication/badge（W4/W6/W5-手机桥接） | W4/W6/W5（手机桥接） | 审批创建 ≤5s 到腕（**经手机桥接，无 FCM**）；腕上批准后 PC 侧继续；已在别处处理时腕上正确纠偏 |
| **M3 指令与 Tile** | 语音输入 + Tile + 常驻活动 + 设置（W8 + W1） | W8/W1 | 语音文本落到正确目标并可见回执；Tile 不滚动且单动作可用；**Tile/常驻活动在国行真机验证通过或已降级** |
| **M4 打磨（可选）** | 侧载分发流程固化、近场增强（L3）、（海外）FCM 直推 | — | 无 GMS 环境可用；耗电达标 |

**每期共同验收**：G1–G10（§4）；手表侧依赖清单仅官方库；无后台长连/轮询（用 `dumpsys` 验证）；无水平滑动业务手势（§7.7）。

---

## 12. 风险

| 风险 | 影响 | 缓解 |
|---|---|---|
| **FCM 依赖 Google 环境**（国行无 GMS，**已确认不可用**） | 通知必达打折 | **一期即走手机端桥接**（HA 同款默认路径）；海外版保留 FCM 作增强；国内可再评估厂商推送通道 |
| Wear OS 碎片化（Wear OS 3/4/5/6/7、圆/方屏、有无 Google 服务） | 兼容工作量 | 最小支持 API 30+；测试矩阵锁定 1–2 款真机；圆屏优先（需求 2） |
| Tile / Complication 刷新不可控 | 状态陈旧 | 设计上接受旧数据（显示相对时间），提供手动刷新；关键信息走通知 |
| 手机不在身边（无 WiFi/LTE 的手表） | 无法联网 | 明确降级：显示缓存 + 「未连接」；不做假数据 |
| 审批在腕上误触 | 错误批准高危命令 | 默认开「长按确认」；高危命令（如 `rm -rf`）强制长按；可关闭腕上审批（`watch:approve` 不给） |
| token 泄露（手表丢失） | 未授权操作 | 受限 scope + 一键吊销 + 短 refresh 有效期；不落盘敏感内容 |
| 「多端互操作」被理解成多端各自跑 agent | 范围失控 | 手表端硬边界：无 agent、无工具、无文件；写在 UI（稿件 10/11）与权限里 |
| 与已有 `prd-desktop-pet-android-watch.md` 手表章节重复 | 文档漂移 | 本文件取代其手表部分；该文件保留桌宠内容并在索引标注 |

---

## 13. 参考来源

- Wear OS 应用质量与原则：https://developer.android.com/docs/quality-guidelines/wear-app-quality ；https://developer.android.com/training/wearables/principles
- Standalone vs non-standalone（推荐独立运行）：https://developer.android.com/training/wearables/apps/standalone-apps
- Wear OS 网络访问（**不要用 Data Layer 当上网通道**）：https://developer.android.com/training/wearables/data/network-communication
- Tile 总览（不可滚动、系统不保证刷新）：https://developer.android.com/training/wearables/tiles
- 通知与常驻活动：https://developer.android.com/training/wearables/notifications ；https://developer.android.com/training/wearables/notifications/ongoing-activity
- Live Updates（Wear OS 7 起）：https://developer.android.com/training/wearables/notifications/live-updates
- 省电与常亮：https://developer.android.com/training/wearables/apps/power ；https://developer.android.com/training/wearables/always-on
- Material 3 Expressive for Wear OS（2025-08）：https://android-developers.googleblog.com/2025/08/introducing-material-3-expressive-for-wear-os.html
- Complication data sources（2025-08）：https://android-developers.googleblog.com/2025/08/building-complication-data-sources-wear-os.html
- Home Assistant Wear OS 文档（功能、Tile 清单、刷新限制、通知桥接、传感器）：https://companion.home-assistant.io/docs/wear-os/ ；https://github.com/home-assistant/companion.home-assistant/blob/master/docs/wear-os/wear-os.md ；传感器：`docs/wear-os/sensors.md`
- 智能家居多端账号模型：米家设备共享（主账号 + 家庭成员共享）、鸿蒙穿戴经运动健康 App 绑定华为账号 + 超级终端/分布式软总线（厂商公开资料）
- 编程 agent 腕上审批：https://www.agentapprove.com/docs
- godex 代码事实：`internal/services/noderegistry/`、`internal/services/relay/`、`internal/runtime/httpapi/{routes_control.go,centerbridge.go,push.go,routes_voice.go,routes_flows.go,routes_step_track.go,httpapi.go}`、`internal/services/webpush/`、`mobile/README.md`、`docs/node-onboarding.md`

---

## 附：待确认问题

1. **推送路线**：**已由设备事实定案**——国行无 GMS，FCM 不可用，一期走「手机端桥接」（海外版再用 FCM 作增强）。待确认的降为：国内是否额外接厂商推送通道。
2. **`watch:approve` 默认值**：默认允许腕上审批，还是默认关闭（更保守）？
3. **手表是否需要"新建任务"能力**（语音 → 建卡片），还是只允许「对现有任务追加指令」？后者更安全。
4. **是否复用现有 Web Push 代码**：手表不走 Web Push，`/push/*` 是否仍作为 Web/桌宠通道保留即可（不动）。
5. **真机标定**：G6 的「额外耗电 ≤5%/天」需要至少一款真机基线（Pixel Watch 3 / Galaxy Watch 7）。
6. **设备跟随策略**：默认「自动跟随最近活跃端点」还是「手动锁定后才切换」？（前者更省事，后者更可预测）
7. **置顶会话的归属**：置顶（稿件 13）是仅本表本地，还是账号级共享（手机/PC 上也同步置顶）？
8. **guarded-remote 的腕上体验**：给该级别节点发指令会先生成待审批项（§7.5），腕上是「直接拦下并提示」还是「允许下发但标注待审批」？
