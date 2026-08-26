# GoDex 插件系统演进方案（长期主义）

> 状态：Draft / Plan（未实施） · 日期：2026-08-27
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
| host 侧 cron/调度 | godex cron ✅（但插件侧**无注册 API**） | 需暴露（P-D） |
| **HTTP/SSE 路由注册**（kanban 视图） | httpapi ✅（但插件侧**无路由注册**） | ❌ 补 P-A |
| **client 半（浏览器 UI 插件）** | 前端**零插件 UI 插槽** | ❌ 补 P-B |
| **运行时服务注入**（`ctx.inject` workspace/llm/provider） | pluginrt `Host` 仅 RegisterEffect/Provide/Logger | ❌ 补 P-C |
| 跨进程/独立会话执行 | longtask（session 内）/ durable subagent / ACP harness ✅ | 执行器可复用 |

## 4. godex 插件边界补齐路线（长期主义）

### P-A：插件注册 HTTP 路由（host 服务面）
- pluginrt `Host` 增加 `RegisterRoutes(prefix string, handler func(*http.ServeMux))`（可逆注册）。
- 场景：taskboard 的 SSE kanban、任意插件面板数据、webhook。
- 成本：1 天（httpapi 组装处加 manager，按 prefix 分发到插件 mux）。

### P-B：前端插件 UI 插槽（client 面）
- 借鉴 dsh：插件 manifest 声明 `ui:` 段（现有设计稿 B 已有雏形），后端 `/plugin-ui` 聚合契约，前端渲染。
- 场景：kanban 视图（可先做 iframe 桥：插件 host 路由 serve 自己的 HTML → 前端 `<iframe>` 加载，**最省**且不要求 React 组件协议）；进阶再按 ui_card/markdown/form 渲染。
- 成本：iframe 桥 1–2 天；完整组件插槽 2–3 天（与 design B 合并推进）。

### P-C：插件运行时服务注入（ctx.inject 等价物）
- pluginrt `Host` 扩展服务面：`Workspace()`, `LLMProviders()`, `Cron()`, `Config()` 等 getter（按 manifest requires 授权）。
- 场景：taskboard 需要 workspace 边界 + llm provider 列表（`llm.listProviders`）。
- 成本：1–2 天（定义 service 接口 + 装配处注入）。

### P-D：插件侧 cron/调度注册
- pluginrt `Host` 增加 `RegisterSchedule(cronExpr, fn)`（可逆注册，复用 godex cron 基建）。
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

**结论**：路径 A（插件适配）为长期主义正解；ACP 作为"外部执行器"补充（taskboard 执行层委托外部 agent）。建议先补 P-A~P-D 边界（2–4 天），再实现 taskboard 核心能力面（3–5 天），前端 kanban 先 iframe 桥（1–2 天）。

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
| P3 | 插件边界补齐 P-A~P-D | 基建 | 2–4 天 | 无（长期主义底座） |
| P3 | **taskboard 插件（路径 A）** | 大 | 3–5 天 + 边界 2–4 天 | P-A~P-D |
| P4 | minimind-o ASR/TTS 探索 | 探索 | 待评估 | 其他工作完成后，做进 voice-engine |

## 8. 验收标准（可验证）

1. P-A~P-D 落地后：任意 godex 插件可注册 HTTP 路由、声明 ui: 段（iframe 桥可用）、经 Host 获取 workspace/llm/cron 服务、注册调度。
2. taskboard 插件：跨 session 任务账本可持久化、`taskboard_*` 工具可被 agent 调用、每任务独立会话执行（复用 longtask/ACP）、kanban 视图在 Web UI 渲染（iframe 桥首版即可）。
3. 插件卸载后：路由/工具/调度/UI 全部可逆撤销，无残留。
4. 无回归：现有语音链路、设置页、聊天交互不变。
