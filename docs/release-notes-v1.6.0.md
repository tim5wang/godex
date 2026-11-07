# godex v1.6.0 — "Multi-Tenant Platform, Work Context Desktop & Wear OS"

> 状态：Historical（v1.6.0 release record）
> Release date: 2026-11-08 ｜ Base: v1.5.0 ｜ Commits: 157

🇬🇧 **English**

**v1.6.0 is the "Multi-Tenant Platform, Work Context Desktop & Wear OS" release.** It turns godex into a multi-tenant relay platform — tenants, users, memberships, node ownership, per-tenant node quotas, relay metering, delegation with consent, and a consolidated cross-tenant authorization matrix — and it adds a Work Context Desktop that keeps long-running work streams, their plans, attention inbox and embedded conversations in one place. A native Wear OS client turns the watch into a wrist remote control for any node, and a Xiaomi/Android mobile companion bridges the two. Every authenticated surface now resolves a `Principal` instead of a shared secret.

### 🔐 Multi-Tenant Platform (identity → isolation → metering)
- **Identity foundation (P0)**: `Tenant` / `User` / `Membership` / `Principal` entities, email+password registration and login (`/auth/*`), session-cookie auth, and a `protected` chain that resolves a real principal instead of the old shared web token. Registration is closed by default and gated by invitation codes (`gui_`) so nobody can self-provision a tenant.
- **Node ownership and visibility (P1)**: the node registry gains `tenant_id` / `owner_user_id` / visibility, written only by trusted paths — a registering node can no longer self-report its tenant. `private` is the default visibility, and a tenant-level sharing switch governs peer reach. Node id enumeration across tenants is closed on **six** surfaces (list masking, single node, overview, HTTP proxy, WS TCP forward, managed forward-tunnel CRUD).
- **NodeAccess seam**: `identity.Policy.Authorize` is the single authorization entry for every "reach this node" path. The managed forward-tunnel CRUD surface was the third instance of the same class of bug and is now covered, with tests asserting the denial happens **before** the side effect (no listen socket left behind).
- **`nk_` per-node credentials**: center-wide proxy credentials can now be issued per node, resolved per-node first with a fallback to the legacy global value (dual acceptance, zero interruption), plus node-side self-rotation (`godex node rotate-proxy-token`) and 401 self-healing.
- **Center execution narrowed (decision ⑨)**: only the platform operator account may run agents on the center; tenant users and tenant admins get 403 at every execution entry, and the check fails closed when no principal is present.
- **Observability projection strips titles**: session titles are user content, so the center-side observation projection omits them by default (`relay.observation.include_titles`).
- **Relay metering (basic tier)**: forwarded requests are recorded per tenant/user/node with byte counts and status, reconcilable via `GET /admin/usage/relay` (cross-tenant) and the tenant-scoped view. A tenant node quota (`max_nodes`) is enforced atomically at enrolment so over-quota registration cannot create ghost nodes.
- **Delegation with consent (§3.6)**: tenants grant time-boxed, read-only operator access; every grant is audited at the authorization seam, consent is recorded at registration, and the tenant sees a banner while delegation is active. Same-tenant peer reads are deliberately **not** audited as delegation.
- **Platform-admin-only config keys** (e.g. WeChat `allow_from`): the `PUT /config` gate refuses tenant users for keys marked platform-admin, closing the "tenant configures who may drive the center's agent" hole.
- **Docs**: `docs/multi-tenant-commercialization-design.md` (master), `docs/multi-tenant-authorization-matrix.md` (per-surface gates + the test that pins each), `docs/admin-console-guide.md`, `docs/p0-principal-migration-inventory.md`.

### 🗂️ Work Context Desktop
- A first-class work stream (主线) model persisted in per-user SQLite: goal, background, plans, linked conversations and knowledge entries survive restarts, and the desktop is reachable through a node's remote access (not only on the machine running the center).
- Plans become first-class entities with an explicit focus/ack/seen watermark, in-place editing for every field (no dialog round-trips), and a `work-desk` built-in template injected by entry point.
- **Attention inbox (AE1–AE5)**: progress no longer depends on the user opening the desktop — a background sweep writes attention events; the inbox supports `claim` / `snooze` / `dismiss` verbs, same-stream batching, leverage ordering, adaptive aging, and unread red dots that navigate to the content they count.
- **Push discipline**: proactive reminders are gated (decision-class only, de-duplicated, quiet hours, daily cap) and the discipline check runs **before** the event is written, so a suppressed reminder cannot burn the budget. Push subscriptions are persisted per owner, so a restart no longer silently unsubscribes every browser.
- Embedded conversations: the desktop is folded into the chat page (left-column view switch + right drawer) with a status bar, session double-open, and a layout height budget; a restart no longer leaves the embedded conversation unopenable.
- **Docs**: `docs/work-context-desktop-design.md`, `docs/work-context-attention-economics.md`.

### ⌚ Wear OS Watch Client
- A native Wear OS client (`wear/`, Compose, Wear Material) that treats the watch as a **remote control**, not an agent host: 12 navigation destinations across 11 round-screen surfaces (home, watch face, talk, sessions, detail, history, approvals, approval detail, devices, cross-device, settings, pairing). No GMS dependency is assumed — the target is a China-market watch — and every list screen has an explicit fix for the rotary behaviour that otherwise crashes.
- **22 `/watch/*` routes**: pairing and approval (`/watch/pair/*`), session list/detail/create/stop, multi-turn history, approvals, voice (`/watch/asr`, `/watch/tts`), push queue, devices, nodes, templates, overview, and cross-device listing.
- Wrist-shaped output: agent replies are condensed deterministically into a ~120-rune summary (markdown stripped, sentence-boundary cut, code-only replies replaced with a pointer to a bigger screen) rather than being a raw truncation.
- **Full message text is now readable on the wrist**: history messages carry an `index` into the session's message list, and `GET /watch/sessions/{id}/messages/{index}` returns the uncut message (capped at 20 000 runes as a memory guard). Previously "read full text" re-displayed the same 600-rune cut because the client had no way to ask for the rest.
- The app now ships a real launcher icon (adaptive, 5 densities + 2 anydpi variants). Before this release it had no `android:icon` at all and fell back to the Android default.
- **Multi-node remote control**: the watch can drive any online node through the center proxy — the same route the web/Android clients use — with background sync following the selected device.
- **Mobile companion**: option A pairing, where the phone joins the same godex node and the watch pairs by showing a code the phone approves; the phone's watcher also drains the push queue.

### 📱 Mobile (Android companion)
- Joins the same node as the desktop client so the watch has something to bridge to; fixes a blank-screen loop on "connect to this machine", an external-browser jump on "connect and enter", and a stale address bar.
- The message watcher no longer swallows 403 silently — a permission denial is surfaced instead of looking like "nothing happened".

### 🛡️ Security Hardening (beyond tenancy)
- Request-layer guard for the fail-open case the startup guard cannot see (anonymous reachability behind a reverse proxy).
- Channel binding restricted to the platform operator; the WeChat inbound gate defaults to admitting only the account that scanned the QR code (decision ⑫), instead of "empty = admit everyone".
- Feishu credentials and the legacy global `node_proxy_token` gained platform-role gates.
- The center's local workspace is gated uniformly: a tenant can no longer read, modify or delete the operator's sessions.

### 🧰 Other Engineering (same range)
- `history_search`: fixed the O(N²) FTS delete that could hang a search for an hour, plus cancellation and a limit cap.
- Node kind is declared (`pc` / `cloud` / `phone`) so clients can tell an agent host from a server or a phone bridge.
- Relay asserts the caller's identity across the hop, so per-owner data resolves on the far side instead of falling back to the center's own identity.
- Tooling: `if_version` is now per-action/per-entity actionable, and the ledger bundle became installable on demand; Web e2e acceptance leftovers were recomputed rather than assumed.

* * *

🇨🇳 **中文**

**v1.6.0 是「多租户平台、工作上下文桌面与 Wear OS」版本。** 本版把 godex 变成多租户中继平台——租户/用户/成员关系、节点归属、每租户节点配额、中继计量、带同意的代看，以及一份收敛的跨租户越权矩阵；同时新增工作上下文桌面，把长期工作主线、计划、注意力收件箱与内嵌会话收在一处。原生 Wear OS 客户端把手表变成任意节点的腕上遥控器，小米/Android 手机端负责桥接。所有已认证面现在都解析 `Principal`，而不再依赖共享密钥。

### 🔐 多租户平台（身份 → 隔离 → 计量）
- **身份底座（P0）**：`Tenant` / `User` / `Membership` / `Principal` 实体、邮箱+密码注册登录（`/auth/*`）、session 鉴权，`protected` 链解析真实主体以取代旧的共享 web token。注册默认关闭，需邀请码（`gui_`），任何人都不能自建租户。
- **节点归属与可见性（P1）**：注册表增加 `tenant_id` / `owner_user_id` / 可见性，只由可信路径写入——注册节点不能再自报租户。默认可见性为 `private`，租户级共享开关管辖对等可达。跨租户节点 id 枚举在**六个**面收敛（列表遮蔽、单节点、overview、HTTP 代理、WS TCP 转发、托管隧道 CRUD）。
- **NodeAccess seam**：`identity.Policy.Authorize` 是所有「能否到达某节点」路径的唯一授权入口。托管隧道 CRUD 是该类漏洞的第三个面，现已覆盖，并且测试断言拒绝发生在副作用**之前**（不会留下已绑定的监听）。
- **`nk_` per-node 凭据**：中心级代理凭据可按节点签发，解析时先查 per-node、未命中回退全局（双认，零中断），并支持节点侧自助换发（`godex node rotate-proxy-token`）与 401 自愈。
- **中心执行收窄（决策⑨）**：只有平台操作员账号能在中心跑 agent；租户用户与租户管理员在每个执行入口均 403，且无 principal 时 fail closed。
- **观测投影去标题**：会话标题属用户内容，中心侧观测投影默认不含（`relay.observation.include_titles`）。
- **中继计量（基础档）**：转发请求按租户/用户/节点记录字节数与状态，可经 `GET /admin/usage/relay`（跨租户）与租户作用域视图对账。每租户节点配额（`max_nodes`）在入网处原子强制，超限不会产生幽灵节点。
- **带同意的代看（§3.6）**：租户授予限时、只读的运营访问；每次授权在授权缝落审计，注册即记录同意，代看期间租户侧有横幅提示。同租户对等读**不**计入代看审计。
- **平台专属配置键**（如微信 `allow_from`）：`PUT /config` 对标记为平台专属的键拒绝租户用户，堵住「租户决定谁能驱动中心 agent」的口子。
- **文档**：`docs/multi-tenant-commercialization-design.md`（主设计）、`docs/multi-tenant-authorization-matrix.md`（逐面闸门 + 对应测试）、`docs/admin-console-guide.md`、`docs/p0-principal-migration-inventory.md`。

### 🗂️ 工作上下文桌面
- 一等实体「工作主线」持久化在个人 SQLite：目标、背景、计划、关联会话与知识条目跨重启保留，并且可经节点的远程访问进入（不限于跑着中心的那台机器）。
- 计划成为一等实体，带显式 focus/ack/seen 水位；所有字段就地编辑（不再弹窗往返）；`work-desk` 内置模板按入口注入。
- **注意力收件箱（AE1–AE5）**：进展不再依赖用户主动打开桌面——后台巡检写入注意力事件；收件箱支持 `claim` / `snooze` / `dismiss` 动词、同主线同类合批、杠杆排序、自适应老化，未读红点会跳到它计数的内容。
- **推送纪律**：主动提醒受门控（只推决策类、去重、静默时段、日上限），且纪律检查在**写事件之前**执行，被抑制的提醒不会烧掉配额。推送订阅按 owner 持久化，重启不再静默退订所有浏览器。
- 内嵌会话：桌面并入聊天页（左栏视图切换 + 右栏抽屉），带状态栏、会话双开与布局高度预算；重启后内嵌对话不再打不开。
- **文档**：`docs/work-context-desktop-design.md`、`docs/work-context-attention-economics.md`。

### ⌚ Wear OS 手表客户端
- 原生 Wear OS 客户端（`wear/`，Compose + Wear Material），定位是**遥控器**而非 agent 宿主：12 个导航目的地、11 个圆屏界面（首页、表盘、说话、会话列表、详情、历史、审批、审批详情、设备、跨设备、设置、配对）。不假设有 GMS——目标是中国市场手表——并且每个列表页都针对无 GMS 时会导致崩溃的旋转行为给了明确修法。
- **22 条 `/watch/*` 路由**：配对与批准（`/watch/pair/*`）、会话列举/详情/新建/停止、多轮历史、审批、语音（`/watch/asr`、`/watch/tts`）、推送队列、设备、节点、模板、overview、跨设备列表。
- 腕上形态输出：agent 回复被确定性地收敛成约 120 字的摘要（去 markdown、按句边界切、纯代码回复换成"请在电脑端查看"），而不是粗暴截断。
- **腕上现在能读完整消息**：历史消息携带指向会话消息列表的 `index`，`GET /watch/sessions/{id}/messages/{index}` 返回未截断原文（上限 20000 字符，仅作内存保护）。此前「查看全文」只是把同一段 600 字截断再显示一遍，因为客户端没有任何通道能取回剩余部分。
- 应用现在有真正的启动图标（自适应，5 个密度 + 2 个 anydpi 变体）。本版之前它根本没有 `android:icon`，一直回退到安卓默认图标。
- **多节点遥控**：手表可经中心代理驱动任意在线节点——与 web/Android 客户端同一条路径——后台同步会跟随所选设备。
- **手机端桥接**：方案 A 配对，手机接入同一 godex 节点、手表出码由手机批准；手机 watcher 同时负责排空推送队列。

### 📱 移动端（Android 伴侣）
- 与桌面客户端接入同一节点，使手边有可桥接的对象；修掉「连本机」闪黑白屏（recreate 无限递归）、「连接并进入」跳外部浏览器、状态栏残留旧地址。
- 消息 watcher 不再静默吞掉 403——权限被拒会给出可见提示，而不是看起来「什么都没发生」。

### 🛡️ 安全加固（租户之外）
- 为启动守卫看不见的 fail-open 情形补请求层守卫（反代后匿名可达）。
- 渠道绑定收紧到平台操作员；微信入站闸门默认只放行扫码绑定者（决策⑫），不再是「空 = 放行全部」。
- 飞书凭据与 legacy 全局 `node_proxy_token` 补平台角色闸门。
- 中心本地工作区统一加闸：租户不再能读/改/删操作员的会话。

### 🧰 其它工程（同区间）
- `history_search`：修掉 FTS 删除的 O(N²)（曾导致搜索挂死 1 小时），并支持取消与 limit 上限。
- 节点声明自身 kind（`pc` / `cloud` / `phone`），客户端可区分 agent 宿主、服务器与手机桥。
- relay 跨跳断言调用者身份，使 per-owner 数据在对端正确解析，而不是退回中心自身身份。
- 工具链：`if_version` 按 action 分实体可行动化，台账 bundle 改为按需装卸；Web e2e 的验收残留被重新核算而非假定通过。

* * *

几点说明：
- 内容按 `v1.5.0..HEAD` 的 157 个 commit 与对应设计文档整理；功能边界以运行时设计文档和代码实现为准。
- 多租户部分按设计 §8 分期，P0–P3 主体已落地；`nk_` per-node 的 P4（确认无节点再用全局值后清空 `control.node_proxy_token`）是**未完成的收尾项**，属不可逆操作，需单独确认。
- 手表端已验证的是仿真器闭环与实机安装/启动；动作级实机验收清单（PRD 门禁 0.4–0.6）仍待补。
- 发布前运行 `./scripts/release_check.sh`；打 `v1.6.0` tag 后，CI 会构建并校验四个平台的 release archives。

### 📦 Assets
- `godex-v1.6.0-linux-x86-64.tar.gz` — Linux amd64
- `godex-v1.6.0-mac-x86-64.tar.gz` — macOS amd64
- `godex-v1.6.0-mac-apple.tar.gz` — macOS arm64
- `godex-v1.6.0-win-x86-64.tar.gz` — Windows amd64
