# godex v1.5.0 — "Agent Templates, Business Agents & Taskboard"

> 状态：Draft（发布草案，待 release 前核验）｜ 主题版本号可按实际发布调整（Makefile `VERSION` 现为 v1.4.0）
> Release date: 2026-09-04 ｜ Base: v1.4.0 ｜ Commits: 134

🇬🇧 **English**

**v1.5.0 is the "Agent Templates, Business Agents & Taskboard" release.** It turns one-off agent sessions into reusable, role-preset templates — a template marketplace ("人才市场") pins a session's capability boundary (exact tool set / skills / MCP / persona / write & memory scope) and can even select an external agent kernel via the Agent Client Protocol (ACP harness); business agents grow from raw `biz_` keys into a managed console backed by the Agent Step Platform (single-step API, TypeScript SDK, embeddable `<godex-step>` component and ui_card reply closure) with the Agent Template as the single source of truth for their capability boundary; and the task board becomes a multi-agent collaboration scheduler — cards open template-pinned execution sessions, PJM-style orchestration dispatches work with structured research handoff and four conflict gates, and a reconcile ledger keeps card state honest with real execution evidence.

### 🧩 Agent Templates & ACP Kernel (Marketplace)
- Template model `AgentTemplate`: a creation-time preset of the capability boundary — `Bundles/Tools` (exact activation via `SetActiveToolsExact`), `Skills`, `MCPServers`, `Packages`, `Persona`, `Profile`, `BasePrompt`, `WriteEnabled/WriteScope`, `Memory` (`none`/`shared`/`scoped`), `ModelHint/BudgetHint`, `TrimHeavySections`, `ProjectDir` (reserved), and `Engine` (run kernel, see below). Sources: builtin / user / package-derived, with builtin override + delete protection.
- Registry + REST: `/agent-templates` CRUD + validate + `/agent-templates/options` (options resolved against the real tool catalog & bundle registry — dropdown-backed forms instead of free text). Session identity persists `template` in locator metadata so reloads re-apply the same preset.
- Built-in templates (8): `default`, `minimal` (legacy mode parity), `general-assistant`, `coder`, `researcher`, `reviewer`, `planner`, `pjm`.
- Web: "Agent 模板" marketplace at `/agents` (avatar/color/scenarios cards, read-only detail for builtins), new-chat template selector replaces the old mode dropdown (backward compatible with `mode=minimal`), and the active template is surfaced in the chat top bar / assistant avatar during the session.
- Honest prompts: system prompts only advertise actually-active tools — exact `active_tools` restoration (session restore / hot reload), tool_availability lists the precise activation set, and `tool_exchange` guidance is omitted when it isn't in the active set (lean templates no longer mislead the model into claiming unavailable capabilities).
- ACP external engine harness: template `engine` field (`godex` | `acp:<agent-id>`) makes an external agent the session-level default run kernel — whole turns are delegated over ACP stdio; the per-turn explicit `harness` envelope request still overrides it; unknown ids fall back to `godex` without rejecting session creation. ACP harness now streams text deltas, persists thinking deltas across re-entry, maps session/update + tool_call/plan events, binds/rejects scopes, and hardens session lifecycle + tool-call event pairing. Web chat renders collapsible thinking bubbles and turn-level process collapse so the thinking↔tool interleave survives re-entry.
- Docs: `docs/agent-role-and-bundle-design.md` (master), `docs/agent-template-agent-implementation-design.md` (ACP kernel).

### 🤖 Business Agents (Agent Step Platform + Console)
- Agent Step Platform Phase A: `POST /v1/agent-steps` synchronous single-step calls authenticated by `biz_` business-system API keys; per-step tool allowlist (MCP ∪ sandbox minimal permission); recall providers (built-in `godex://memory` + external HTTP); step tracking endpoints (terminal state query + `cancelStep`).
- Phase B — TypeScript SDK: `createStep` / `getStep` / `cancelStep` / `streamEvents` (+ `replyStep`); Phase C — `<godex-step>` embeddable Web Component (runs for real, not a screenshot), plus `ui_card` interactive cards rendered in both chat and embed with a reply closure (`POST /v1/agent-steps/{id}/reply` resumes the same step session with the submitted value).
- Business Agents Console (`/business-agents`): biz-key CRUD with a per-agent profile — capabilities (MCP servers / sandbox tools / skills / packages / models), recall config, `project_dir`, budget + warning threshold, inline quick-start snippets (curl / TS SDK / embed tag) and live embed preview.
- M4 convergence: the Agent Template is the single source of truth for a business agent's boundary — `BizAPIKey.template_id` + template-baseline → key-override → request-narrowing chain, and `POST /v1/biz/keys/{id}/migrate-template` one-click migration from a key whitelist.
- The old Workflows page was removed (its playbook/UiCardView assets were absorbed); only `UiCardView` (reused by chat & embed) remains.

### 📋 Taskboard (Multi-Agent Collaboration Board)
- Native Go plugin (`internal/plugins/taskboard`): authoritative JSON ledger (`ledger.json`, atomic tmp+rename writes, optimistic `ifVersion` locking), a code-level protocol state machine (agents can never move cards to `done`; holder locks protect in-flight cards while humans act as superuser; cards with running executions cannot be deleted), a single `taskboard` tool with action dispatch (the 8 ad-hoc tools merged), an HTTP `/v1/taskboard` surface and a Web five-column kanban with detail drawer, checklist/DoD, approve/reject actions.
- Agent-executed cards: agents claim and execute cards in **template-pinned independent sessions** (`Card.TemplateID` → `channel=taskboard key=card-<id>`, `ActivateBundles(task_board)` fallback), with "view progress" jumping to the real execution session via complete locator/identity metadata (no more accidental new sessions or stale "running" states).
- Multi-agent collaboration: ① structured `research` handoff (PJM-verified facts + open questions injected into the executor prompt so coders verify instead of re-investigate); ② four conflict gates — `touched_paths` static declarations, dispatch-time overlap interception (blocks issuing a card that collides with in-flight ones), `observed_paths` dynamic reporting, and a path-overlap merge report attached when a card enters `in_review`; ③ execution observability — ledger consistency, failure insight, append-message recovery, and the in_review dispatch-block fix.
- Reconcile: safe terminal-state convergence (never fabricates `completed`) with per-card/per-execution detail, stall detection, holder/execution vs DoD signal consistency — exposed as `taskboard reconcile` tool action, `POST /v1/taskboard/reconcile` and a Web report.
- PJM orchestration (M5): built-in `pjm` template (planning bundles + persona), a "Talk to PJM" entry on the board (opens a `template=pjm` chat), `dispatch` action wired through the executor chain, and a scheduled `taskboard-pjm-sweep` cron to periodically review todos and dispatch.
- Project registry: lightweight per-board projects (`name` + `root_dir` + multiple work dirs; built-in default project = workspace) — taskboard project management is now available from the Web UI.
- Docs: `docs/taskboard-plugin-design.md`, `docs/taskboard-collaboration-design.md`, `docs/taskboard-reconcile-design.md`.

### 🧰 Other Experience & Engineering (within this range)
- Chat: sending goes through a queue with edit/cancel/delete APIs, draft persistence, session rename, message-level fork from a turn's completed state, "notify me when the turn finishes" + steer interaction polish.
- Voice (opt-in): push-to-talk mic input with segmented recognition echo, editable-then-send flow, TTS streaming playback (WS `/v1/tts/stream`), configurable `voice_enabled`/`voice_engine_addr` + status diagnostics.
- MCP management: server lifecycle management + Settings management panel + business-agent whitelist wiring.
- Automation: cron expression editor (`CronExprInput`) and heartbeat/cron `watchdog_script` pre-gates with definitions & run logs surfaced in the Automation page.
- Desktop & browser: distributed browser runtime over the relay channel (a11y snapshots, shadow/iframe piercing, tabs, persistent profile); desktop accessibility dump + OCR backend abstraction (RapidOCR) with scroll/activate/screenshot primitives.
- Engineering: architecture-boundary enforcement and extensive frontend layering refactors (types/API/locale/styles/HTTP-route splits), Windows dev scripts (`dev.cmd`) & user-scope service install fix, tool-chain hardening (command-substitution validation, `web_fetch` fallback hints, `edit_file` guidance, `ApprovalMode` passthrough), memory optimizations.

* * *

🇨🇳 **中文**

**v1.5.0 是「Agent 模板、业务智能体与任务看板」版本。** 一次性会话进化为可复用的角色预设——Agent 模板人才市场把会话的能力边界（精确工具集 / skills / MCP / persona / 读写与记忆范围）固化成模板，还能通过 ACP harness 把整轮委托给外部 agent 内核；业务智能体从裸 `biz_` key 升级为可视化管理台，底层是 Agent Step 平台（单步 API、TS SDK、可嵌入 `<godex-step>` 组件与 ui_card 交互闭环），并以 Agent 模板作为能力边界的单一事实源；任务看板进化为多智能体协作调度器——卡片按模板开独立执行会话、PJM 式编排带结构化调研传递与四道冲突闸门、对账账本用真实执行证据保持卡片状态诚实。

### 🧩 Agent 模板与 ACP 内核（人才市场）
- 模板模型 `AgentTemplate`：一次性能力边界预设——`Bundles/Tools`（`SetActiveToolsExact` 精确激活）、`Skills`、`MCPServers`、`Packages`、`Persona`、`Profile`、`BasePrompt`、`WriteEnabled/WriteScope`、`Memory`（`none`/`shared`/`scoped` 记忆范围）、`ModelHint/BudgetHint`、`TrimHeavySections`、`ProjectDir`（预留）、`Engine`（运行内核，见下）。来源：builtin / user / package 派生，内置模板防覆盖、防删除。
- 注册表 + REST：`/agent-templates` CRUD + validate + `/agent-templates/options`（选项按真实工具目录解析，表单全部下拉化）；会话身份把 `template` 写入 locator 元数据，reload 自动恢复同一预设。
- 内置模板 8 个：`default`、`minimal`（兼容旧 mode）、`general-assistant`、`coder`、`researcher`、`reviewer`、`planner`、`pjm`。
- Web：「Agent 模板」人才市场页（`/agents`，头像/配色/场景卡片，内置模板只读详情）；新建对话模板选择器取代原 mode 下拉（兼容 `mode=minimal`）；会话顶栏/助手头像透出当前模板身份。
- 诚实的提示词：system prompt 只宣传真正激活的工具——会话恢复 / 热重载按 `active_tools` 精确名单还原；tool_availability 展示精确激活集；`tool_exchange` 不在激活集时不注入「用它扩展」指引（lean 模板不再误导模型宣称不可用能力）。
- ACP 外部内核 harness：模板 `engine` 字段（`godex` | `acp:<agent-id>`）把外部 agent 设为会话级默认内核，整轮经 ACP stdio 委托；每轮显式 `harness` envelope 请求仍可覆盖；未知 id 回退 godex、不拒绝建会话。ACP harness 流式透出文本 delta、跨重入持久化 thinking delta、统一映射 session/update 与 tool_call/plan 事件、scope bind/reject、加固会话生命周期与 tool-call 事件配对；Web Chat 支持折叠 thinking 气泡与 turn 级 process 折叠，重入后可重建「思考↔工具」交错。
- 文档：`docs/agent-role-and-bundle-design.md`（主设计）、`docs/agent-template-agent-implementation-design.md`（ACP 内核）。

### 🤖 业务智能体（Agent Step 平台 + 管理台）
- Agent Step 平台 Phase A：`POST /v1/agent-steps` 同步单步调用，`biz_` 业务系统 API key 认证；按步工具白名单（MCP ∪ sandbox 最小权限）；召回 provider（内置 `godex://memory` + 外部 HTTP）；step 追踪端点（终态查询 + `cancelStep`）。
- Phase B — TypeScript SDK：`createStep` / `getStep` / `cancelStep` / `streamEvents`（+ `replyStep`）；Phase C — `<godex-step>` 可嵌入 Web Component（真实可运行，非截图）+ `ui_card` 交互卡片（聊天与嵌入双处渲染），`POST /v1/agent-steps/{id}/reply` 把交互值回注同一 step 会话续跑。
- 业务智能体管理台（`/business-agents`）：biz key CRUD + 能力（MCP / sandbox 工具 / skill / package / models）+ 召回 + `project_dir` 工作目录 + 预算与告警阈值 + 内联接入指南（curl / TS SDK / 嵌入标签一键复制）+ 实时嵌入预览。
- M4 收敛：Agent 模板作为业务智能体能力边界的单一事实源——`BizAPIKey.template_id` + 「模板基线 → key 覆盖层 → 请求收窄」三段链，`POST /v1/biz/keys/{id}/migrate-template` 从 key 白名单一键迁移生成模板。
- 移除旧 Workflows 页（playbook/UiCardView 资产被吸收），仅保留被聊天与嵌入复用的 `UiCardView`。

### 📋 任务看板（多智能体协作）
- 原生 Go 插件（`internal/plugins/taskboard`）：权威 JSON 账本（`ledger.json`，原子 tmp+rename 写、乐观锁 `ifVersion`）、代码级协议状态机（agent 永远不能把卡移到 `done`；holder 锁保护在飞卡、human 以 superuser 越锁；有 running execution 的卡不可删）、8 个 `taskboard_*` 工具收敛为单 `taskboard` 工具（action 分发）、HTTP `/v1/taskboard` 面与 Web 五列看板（详情抽屉、checklist/DoD、验收/退回）。
- Agent 执行卡片：agent 认领 + 在**模板钉住的独立执行会话**执行（`Card.TemplateID` → `channel=taskboard key=card-<id>` + `ActivateBundles(task_board)` 兜底）；「查看进度」凭完整 locator/身份 metadata 跳到真实执行会话（不再误开新会话 / 不再永远显示 running）。
- 多智能体协作：① 结构化 `research` 调研传递（PJM 已验证事实 + 待验证开放点分区注入执行提示词，coder 只验证不重查）；② 四道冲突闸门——`touched_paths` 静态声明、派工重叠拦截（与在跑卡冲突的卡派不出去）、`observed_paths` 动态上报、进 `in_review` 附 path-overlap 合并报告；③ 执行可观测性——账本一致性、失败洞察、追加消息恢复、修复 in_review 卡阻塞 dispatch。
- 对账 reconcile：安全终态收敛（绝不臆造 `completed`）+ 逐卡/逐执行明细 + 停滞检测 + holder/execution 与 DoD 信号卡级一致性核对；暴露为 `taskboard reconcile` 工具 action、`POST /v1/taskboard/reconcile` 与 Web 报告。
- PJM 编排者（M5）：内置 `pjm` 模板（planning bundles + persona）；看板「与 PJM 对话」入口（跳 `template=pjm` 会话）；`dispatch` action 注入执行链；`taskboard-pjm-sweep` 定时 cron 巡检待办并派卡。
- 项目注册表：轻量项目维度（`name` + `root_dir` + 多 workdir，内置默认项目 = workspace）；Web UI 支持任务看板项目管理。
- 文档：`docs/taskboard-plugin-design.md`、`docs/taskboard-collaboration-design.md`、`docs/taskboard-reconcile-design.md`。

### 🧰 其它体验与工程（同区间交付）
- Chat：发送统一进队列 + 排队消息编辑/取消/删除 API、草稿持久化、会话重命名、消息级 fork（从该 turn 完成态 fork）、turn 完成通知 + Steer 交互优化。
- 语音（可选）：push-to-talk 麦克风输入 + 分段识别回显、识别结果编辑后发送、TTS 边生成边流式播放（WS `/v1/tts/stream`）、`voice_enabled`/`voice_engine_addr` 配置化 + 状态诊断。
- MCP 管理：服务生命周期管理 + 设置页管理面板 + 业务智能体白名单接线。
- 自动化：cron 表达式编辑器（`CronExprInput`）；heartbeat/cron `watchdog_script` 前置门控，定义与执行日志在自动化页展示。
- 桌面与浏览器：relay 通道分布式浏览器运行时（a11y 快照、shadow/iframe 穿透、多 tab、持久 profile）；桌面无障碍 dump + OCR 后端抽象（RapidOCR）+ scroll/activate/screenshot 原语。
- 工程：架构边界强制 + 前端分层重构（types/API/locale/styles/HTTP 路由拆分）；Windows 开发脚本（`dev.cmd`）与用户级 service 安装修复；工具链加固（命令替换校验放宽、`web_fetch` fallback_hint、`edit_file` 引导提示、`ApprovalMode` 透传）；memory 优化。

* * *

几点说明：
- 内容基于 `v1.4.0..HEAD` 的 134 个 commit 与设计文档（agent-role-and-bundle-design / business-agents-console-design / taskboard-plugin-design / taskboard-collaboration-design / taskboard-reconcile-design / agent-template-agent-implementation-design）整理，未夸大；各小节实现状态以上述文档中的「当前实现快照」与源码为准。
- 本版本号与标题为草案：v1.4.0 之后尚未打 tag，发布前请确认最终版本号并更新 Makefile `VERSION`。
- 发布前请先跑 `./scripts/release_check.sh` 与 `make release`。

### 📦 Assets
- `godex-v1.5.0-linux-x86-64.tar.gz` — Linux amd64
- `godex-v1.5.0-mac-x86-64.tar.gz` — macOS amd64
- `godex-v1.5.0-mac-apple.tar.gz` — macOS arm64
- `godex-v1.5.0-win-x86-64.tar.gz` — Windows amd64
