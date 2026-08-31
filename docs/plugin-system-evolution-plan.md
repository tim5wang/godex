# GoDex 插件系统演进方案（长期主义）

> 状态：Active（P-A/P-C/P-D 与 TaskBoard 原生插件已落地；P-B 通用前端插件槽仍 Partial） · 日期：2026-08-27，核对：2026-08-31

## 当前实现快照

| 能力 | 状态 | 当前事实 |
|---|---|---|
| P-A 插件 HTTP 路由 | Implemented | `pluginrt.Host.RegisterRoutes` 按 prefix 挂载；停用时通过 effect ledger 反向卸载，`TestPluginRoutesRegisterAndRevert` 覆盖。 |
| P-B 前端插件 UI 插槽 | Partial | TaskBoard 已有内建 React 页面、`ui_card` 有通用渲染，但尚无 manifest `ui:` 聚合协议、iframe 桥或可动态装卸的通用 Web 插件槽。 |
| P-C 运行时服务注入 | Implemented baseline | `Host.Services()` + `WithServices(Services)` 提供 workspace/state 等受控服务，`TestPluginServicesInjection` 覆盖；它不是无限制的 service locator。 |
| P-D 插件调度 | Implemented | `Host.RegisterSchedule` 支持 every/cron 并在停用时撤销，`TestPluginScheduleTicksAndReverts` 覆盖。 |
| TaskBoard 路径 A | Implemented baseline | `internal/plugins/taskboard` 已作为原生插件注册 routes/tools，并由 Web TaskBoard 消费；高级协作和 reconcile 分阶段演进。 |

本文保留原始差距分析以说明设计动机；后续路线与验收必须按上表区分已落地底座和剩余通用 UI 能力。
> 目标：长期主义地完善 godex 插件边界，使 godex 能低成本承接 dsh 生态等开源插件；
> 第一个验证案例：dsh-taskboard（跨 session 项目任务看板）适配。
> 关联：docs/voice-plugin-extensibility-design.md（turn 中间件 / UI 插槽 / 语音 L2，本方案是其广义化）

## 1. 背景与目标

用户确认方向（2026-08-27）：
- **Q1**：倾向把 dsh-taskboard 源码拿来，通过完善 godex 插件边界做适配插件；太难则 ACP 适配。
- **Q2**：花更多精力梳理插件系统优化方案——这次跑通，未来可迁移更多开源插件（长期主义）。
- **Q3**：minimind-o（0.1B 多模态）做 ASR/TTS 记为探索任务，其他工作完成后尝试，做进 voice-engine。

本方案回答：dsh-taskboard 需要哪些插件能力面 → godex 现有差距 → 补齐路线 → 适配路径决策。

## 2. dsh-taskboard 能力面（源码事实）

npm 0.5.1 描述：*Agent-first task board for the DSH web GUI: host-authoritative task ledger with `taskboard_*` agent tools, project (= workspace) claim boundaries, per-task model execution in fresh sessions, optional per-task git-worktree isolation (dedicated task branches, commit evidence, one-click merge), host-side cron scheduling, and a live SSE kanban view. Mounts via the official dsh plugin system — no DSH source changes.*

关键：**它不是"看板 UI 插件"，是"agent-first 任务执行框架"**。能力分两层：

### host 半（Node 进程，Cordis 插件）
| 文件 | 能力 |
|---|---|
| `lib/index.js` | Cordis 插件入口：`inject: ["tools","systemPrompt"]`，`ctx.effect` 可逆注册，`ctx.inject(["workspaceRegistry"])` + `llm.listProviders()` 服务注入，systemPrompt protocol section 注入 |
| `lib/host/store.js` | 任务账本（`dsh-taskboard.json`，host 权威） |
| `lib/host/scheduler.js` | host 侧 cron 调度（`DSH_TASKBOARD_MAX_CONCURRENT` 并发控制） |
| `lib/host/execution.js` | **每任务独立会话执行**（fresh session per task） |
| `lib/host/git.js` | 每任务 git-worktree 隔离（独立分支/commit 证据/一键 merge） |
| `lib/host/tools.js` | `taskboard_*` agent 工具注册 |
| `lib/host/routes.js` | SSE kanban 数据路由 |
| `lib/host/templates.js` | 任务模板 |
| `lib/host/protocol-text.js` | system prompt 段（教 agent 用 taskboard 协议） |

### client 半（浏览器，216KB client.js + SSE kanban）
- 通过 `dsh.client` 声明 + `cordis.patch.yml` 注册到 web GUI（`/plugins/<id>/client.js`）
- 提供看板列视图（board.png / modal.png 截图）

## 3. dsh-taskboard 所需 Cordis 能力面 → godex 现状差距

| Cordis 能力面（taskboard 用到） | godex 现状 | 差距 |
|---|---|---|
| `ctx.effect`（可逆注册/卸载撤销） | pluginrt `effects.Ledger` ✅ | 无 |
| tools 注册（`taskboard_*`） | toolruntime `RegisterOwned`（owner/disposer）✅ | 无 |
| systemPrompt section 注入 | pluginrt `PromptSections` → agent 注入 ✅ | 无 |
| host 侧 cron/调度 | `Host.RegisterSchedule` + 可逆撤销 ✅ | 已落地（P-D） |
| **HTTP/SSE 路由注册**（kanban 视图） | `Host.RegisterRoutes` + prefix mount/unmount ✅ | 已落地（P-A） |
| **client 半（浏览器 UI 插件）** | TaskBoard 内建页与 `ui_card` 已有；动态 manifest UI 插槽仍无 | 🟡 P-B Partial |
| **运行时服务注入**（`ctx.inject` workspace/llm/provider） | `Host.Services()` + `WithServices` ✅ | P-C baseline 已落地；按能力授权仍可增强 |
| 跨进程/独立会话执行 | longtask（session 内）/ durable subagent / ACP harness ✅ | 执行器可复用 |

## 4. godex 插件边界补齐路线（长期主义）

### P-A：插件注册 HTTP 路由（✅ 已落地）
- pluginrt `Host` 已增加 `RegisterRoutes(prefix string, handler func(*http.ServeMux))`（可逆注册）。
- 场景：taskboard 的 SSE kanban、任意插件面板数据、webhook。
- 成本：1 天（httpapi 组装处加 manager，按 prefix 分发到插件 mux）。

### P-B：前端插件 UI 插槽（🟡 Partial / Planned）
- 借鉴 dsh：插件 manifest 声明 `ui:` 段（现有设计稿 B 已有雏形），后端 `/plugin-ui` 聚合契约，前端渲染。
- 场景：kanban 视图（可先做 iframe 桥：插件 host 路由 serve 自己的 HTML → 前端 `<iframe>` 加载，**最省**且不要求 React 组件协议）；进阶再按 ui_card/markdown/form 渲染。
- 成本：iframe 桥 1–2 天；完整组件插槽 2–3 天（与 design B 合并推进）。

### P-C：插件运行时服务注入（✅ baseline 已落地）
- pluginrt `Host` 已通过 `Services()` 暴露受控服务集合，并由 `WithServices` 在装配处注入 workspace/state 等依赖；更细的 provider/config 能力和 manifest requires 授权仍可扩展。
- 场景：taskboard 需要 workspace 边界 + llm provider 列表（`llm.listProviders`）。
- 成本：1–2 天（定义 service 接口 + 装配处注入）。

### P-D：插件侧 cron/调度注册（✅ 已落地）
- pluginrt `Host` 已增加 `RegisterSchedule(name, ScheduleSpec, fn)`（可逆注册，支持 interval/cron）。
- 场景：taskboard 的 host-side scheduling。
- 成本：0.5–1 天。

### 与现有设计稿衔接
- 前述 design（turn 中间件 / settings+ui_card / 语音 L2）是 P-A~P-D 的具体消费者；本方案是统一底座。

## 5. taskboard 适配路径决策

| 路径 | 做法 | 优点 | 缺点 |
|---|---|---|---|
| **A. 插件适配（能力面 Go 重写）** ✅ 推荐 | 按 §3 补齐 P-A~P-D 边界后，用 Go 实现 taskboard 能力面（账本 + 工具 + 调度 + worktree + SSE kanban），注册为 godex 原生插件 | 长期主义（未来 dsh 生态插件都受益）；无 Node 依赖；UI 可做 iframe 桥；执行器复用 longtask/ACP | 工作量大（账本/工具/UI 约 3–5 天，不含边界补齐） |
| B. ACP 适配（子进程桥） | Node 跑 dsh-taskboard 原插件，通过 ACP/MCP 桥到 godex | 复用原 JS 逻辑、快速验证能力 | **SSE kanban UI 无法经 ACP 表达**（ACP 是 agent 协议非 UI 协议），UI 仍要 godex 前端承载；引入 Node 运行时依赖；桥接层调试成本 |

**关键判断**：
1. dsh-taskboard 是 **JS 插件，不能二进制迁移**——只能迁移"能力面设计"（Go 重写）或"进程桥接"（ACP）。
2. ACP 桥接救不了 UI（kanban 视图必须 godex 前端/iframe 承载），所以 ACP 只解决"host 逻辑"不解决"UI"。
3. **执行器可复用**：taskboard 的"每任务独立会话执行"正是 godex longtask/ACP harness 的强项，Go 重写时直接复用，不重造。

**当前结论**：路径 A 已落地为 Go 原生 TaskBoard 插件，ACP 仍可作为外部执行器补充。P-A/P-C/P-D 不再是前置缺口；剩余平台级缺口主要是 P-B 通用动态 UI 插槽，TaskBoard 当前使用内建 React 页面而非 iframe/plugin manifest UI。

## 6. 与 longtask 的关系（用户 Q1 追问）

- longtask 是 **session 内**长迭代任务（workflowStore 按 session_id 绑定），交互性弱。
- 增加 project 维度只能覆盖"跨 session 看板"的**数据聚合**部分，覆盖不了 dsh-taskboard 的核心价值：**每任务独立会话执行 + git-worktree 隔离 + host cron 调度 + taskboard_* agent 工具面**。
- 结论：taskboard 不应从 longtask 扩展，应作为独立能力面实现（执行器可复用 longtask/ACP），project 维度仅用于聚合展示。

## 7. 优先级排序（更新版，bug 优先 → 简单独立 → 大任务）

| 优先级 | 项 | 形态 | 工作量 | 依赖 |
|---|---|---|---|---|
| P0 | #7 笔记标题丢失 | bug | 0.5 天 | 无（根因已定位） |
| P1 | #4 fork session（按 turn） | 小功能 | 0.5–1 天 | 后端 API 已有 |
| P1 | #8 turn 后通知我 | 小功能 | 1 天 | 无 |
| P1 | #3 心跳 watchdog | 小功能 | 1–1.5 天 | 无 |
| P2 | #5 Steer 交互 | 前端中 | 2–3 天 | 无 |
| P2 | #6 MCP 服务管理 | 中 | 2–3 天 | 无 |
| Done | 插件边界 P-A/P-C/P-D | 基建 | 已落地 | P-B 仍 Partial |
| Done / evolving | **taskboard 插件（路径 A）** | 大 | baseline 已落地 | 协作/reconcile 按独立文档演进 |
| P4 | minimind-o ASR/TTS 探索 | 探索 | 待评估 | 其他工作完成后，做进 voice-engine |

## 8. 验收标准（可验证）

1. P-A/P-C/P-D 已有可执行测试；P-B 的完成判据仍是：任意插件可声明 `ui:` 段并被 Web 动态挂载/卸载，而不是仅存在一个内建 TaskBoard 页面。
2. taskboard 插件：跨 session 任务账本可持久化、`taskboard_*` 工具可被 agent 调用、每任务独立会话执行（复用 longtask/ACP）、kanban 视图在 Web UI 渲染（iframe 桥首版即可）。
3. 插件卸载后：路由/工具/调度/UI 全部可逆撤销，无残留。
4. 无回归：现有语音链路、设置页、聊天交互不变。
