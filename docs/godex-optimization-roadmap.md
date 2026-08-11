# GoDex 优化路线图（统一版）

> 合并自：`docs/qm-roadmap.md`、`docs/longtask-analysis.md`、`docs/roadmap-high-roi.md`、`docs/roadmap-runtime-hardening.md`、`docs/architecture-v2-spec.md`、`docs/agent-role-and-bundle-design.md`
> 创建日期：2026-08-10
> 已完成的记忆改造（work_method/work_fact 类型、笔记↔记忆联动）已纳入基线

---

## 当前基线（已完成）

以下能力已经落地，不再作为待办：

| 领域 | 已落地能力 |
|------|-----------|
| **多入口** | CLI、readline、TUI、Web、Feishu、Weixin、cron、heartbeat 共享同一后端 |
| **Web UI** | Chat、Settings、Memory、Notes、Automation（cron/triggers）、Skills、Nodes、Context & Recall |
| **Memory 2.x** | 显式记忆、候选 inbox、suppression、SQLite + FTS5 sidecar、scope-aware recall、project miner、history_search |
| **Memory 类型扩展** | ✅ 新增 `work_method` / `work_fact` 类型（2026-08-10） |
| **笔记↔记忆联动** | ✅ 双向打通：笔记展示相关记忆、记忆注入追加笔记引用、HTTP API + UI（2026-08-10） |
| **记忆去重（foldCapture）** | ✅ 同 title Remember 增量去重（normalize 对比，重复跳过）+ 上下文截断 capTail 保留尾部最新（2026-08-11） |
| **指令系统** | `AGENT.md`、本地规则、动态上下文、skills/package prompt 可组合注入 |
| **Skill/Package** | native skill、第三方 normalizer、compatibility analyzer、package command dispatcher、role registry |
| **Tool runtime** | typed tools、参数 coercion、before/after interceptors、审批模式、bundle 管理 |
| **Subagent/Workflow** | durable subagent、batch/wait、分层 job storage、per-job timeout、workflow handoff artifact、dependency injection、preview merge、dynamic append node |
| **Longtask 动态 DAG** | ✅ 动态并行 DAG 语义（depends_on 显式依赖、无依赖并行 fan-out，2026-08-10） |
| **Longtask 重启恢复** | ✅ run 记录持久化 + 启动 sweep/重建 + `--resume-run-id` 续跑（2026-08-11） |
| **上下文预算管理** | ✅ 子任务自动摘要化（token 预算截断）、依赖 handoff 共享截断路径、历史子任务不驻留活跃上下文（2026-08-11） |
| **上下文预算按角色分配** | ✅ 角色预算解析（orchestrator 200K / worker 100K / reviewer 100K / researcher 50K）、`ContextBudget` 持久化到 subagent job、subagent 运行循环超预算自动 rule-based 压缩（2026-08-11） |
| **AgentGraph 抽象** | ✅ `agent_graph` 工具 + `AgentGraph` 接口（Create/Get/AddNode/AddEdge/RemoveNode/Cancel/Run/Wait）、5 种节点类型、3 种边类型、动态增删、merge_point/user_input 语义（2026-08-11） |
| **Runner 韧性** | 统一 phase checkpoint、active turn follow-up injection、空回复恢复、length/provider error 恢复 |
| **Turn Error 分层** | ✅ `TurnError` 接口（Retryable/Transient/NonRetryable）+ `ClassifyTurnError` + Runner 内层有限重试路由 + `TurnFailureMessage`（2026-08-11） |
| **Harness 多引擎抽象** | ✅ `Harness` 接口（Profile/Models/Tools/RunTurn/ResetSession/Close）+ godexHarness 默认实现 + harnessRouter 按会话切换并重置引擎（2026-08-11） |
| **持久化 Map 抽象** | ✅ `DurableMap[V]` 接口 + MemoryMap + SQLiteMap（dir/<table>.db）；替换试点发现有序数组（candidates.json）不适用 key-value map，保留文件读写（2026-08-11） |
| **多引擎热切换** | ✅ `RunOptions.Harness` 每轮选引擎 + `RegisterHarness` + 惰性 harnessRouter + backend envelope metadata 透传；切换引擎自动 reset session（2026-08-12） |
| **安全边界** | 安全 profile、host privilege policy、WorkspaceFS 文件边界、shell 风险分级和审计 |
| **Browser/Desktop/ACP** | browser handoff/resume、desktop bundle、OCR、external ACP stdio bridge |
| **执行后端** | 本地和 Docker bind-mount workspace 模式 |

---

## 核心设计原则

1. **动态并行 DAG，不牺牲效率** — longtask 重构目标：动态、并行、可调整的 Agent 图，不改串行 chain
2. **每个阶段独立可交付** — 不依赖后续阶段，不搞一次性大重写
3. **先接口后实现** — 先定义接口，再逐步替换实现
4. **测试即契约** — 每个接口都有兼容性测试，确保替换不破坏既有行为
5. **长期主义，宁慢勿糙** — 借鉴但不照搬 QM/Codex，只落地适合 GoDex 的模式
6. **三层工具组织模型** — 工具是原子能力，bundle 是逻辑分组，角色是能力边界。角色决定 bundle 的默认激活集合和权限策略，但 agent 可动态调整
7. **角色能力边界 = 工具集合 × 权限策略 × 上下文预算** — 每个角色从三个维度定义能力边界，子 agent 创建时根据角色自动配置

---

## 工具组织模型：三层架构

### 三层模型

```
Layer 3: Agent 角色 (Role) — 身份声明 + 能力边界 + 默认 bundle 集 + 策略约束
Layer 2: Tool Bundle     — 逻辑分组 + 按需加载 (tool_exchange) + 可审计
Layer 1: 原子工具 (Tool) — schema + 权限声明 + bundle 归属
```

### 角色能力边界 = 三个维度

```
能力边界 = (工具集合 × 权限策略 × 上下文预算)
```

### 建议的角色分工

| 角色 | 核心工具 | 写权限 | 上下文 | 典型场景 |
|------|---------|--------|--------|---------|
| **orchestrator** | 全部 | 允许 | 200K | 规划、委派、评审、合并 |
| **worker** | core_code, lsp | 受限写 scope | 100K | 写代码、改文件 |
| **reviewer** | lsp, diff, grep | 禁止 | 100K | 代码审查、diff 分析 |
| **researcher** | web, browser | 禁止 | 50K | 搜索、调研 |
| **planner** | todo, read, lsp | 禁止 | 100K | 拆解任务、制定计划 |

### 一个长任务场景的执行流程

```
用户：重构这个模块
    │
    ▼
orchestrator (完整工具集)
    │
    ├─ 1. planner 角色 → 分析模块、制定计划
    │   bundle: [core_code, lsp, planning], write: false
    │
    ├─ 2. worker 角色 A → 实现模块 A (并行)
    │   bundle: [core_code, lsp], write: true (scope: "src/module_a/")
    │
    ├─ 3. worker 角色 B → 实现模块 B (并行)
    │   bundle: [core_code, lsp], write: true (scope: "src/module_b/")
    │
    ├─ 4. reviewer 角色 → review 两个 worker 的产出
    │   bundle: [lsp, diff], write: false
    │
    └─ 5. orchestrator → 合并结果、汇报
        bundle: [core_code, lsp, subagent], write: true
```

### 已有基础设施 vs 需要补齐的

**已有**：
- ✅ `ToolBundle` 机制完整（`toolruntime/base.go`）
- ✅ `tool_exchange` 运行时动态加载
- ✅ `Role` 结构体定义（`packages.go`）
- ✅ 写 scope 机制（`workflow.go`）

**需要补齐的**（对应 Phase 3 任务）：

| 机制 | 优先级 | 说明 |
|------|--------|------|
| 角色→bundle 运行时映射 | P0 | subagent 创建时根据角色自动激活对应 bundle |
| 角色 bundle 注册表 | P0 | 集中管理所有角色和 bundle 的映射关系 |
| 子 agent bundle 继承 | P1 | 子 agent 默认继承父 agent 的 bundle，可覆盖 |
| 写 scope 与 bundle 联动 | P1 | 激活 writing bundle 时自动应用写 scope |
| 上下文预算按角色分配 | P2 | 不同角色有不同 token 预算 |

---

## 路线图

### ✅ Phase 0：已完成（2026-08-10）

| 编号 | 项目 | 文件 |
|------|------|------|
| P0-1 | 记忆类型扩展：`work_method` / `work_fact` | `internal/core/memory/manager.go` |
| P0-2 | 笔记↔记忆双向联动 | `internal/services/backend/notes.go` 等 |
| P0-3 | 记忆注入时追加笔记引用 | `internal/agent/context.go` |
| P0-4 | HTTP API + UI 展示 | `httpapi.go`、`MemoryPage.tsx`、`NotesPage.tsx` |
| P0-5 | 设计文档更新 | `docs/memory-design-principles.md`、`memory/MEMORY.md` |

---

### 🔴 Phase 1：长任务运行时韧性（P0，基础底座）

#### 1.1 异步 turn/job runtime ✅（2026-08-10）

**问题**：Web 消息提交绑定 HTTP 请求生命周期，长任务断开后丢失状态。

**方案**：
- ✅ `POST /sessions/{id}/messages` 返回 `202 + turn_id`（已有 `SubmitAsync`，本次核验确认）
- ✅ 服务端用独立 context 运行 turn（已有，`context.WithCancelCause(context.Background())`）
- ✅ SSE 按 `turn_id` 推送状态（新增 `?turn_id=` 过滤，`EventReplayOptions.TurnID` 优先于 ActiveOnly）
- ✅ 增加 cancel/retry/resume endpoint（已有）+ 新增 `GET /sessions/{id}/turns/{turnID}` 单 turn 状态查询
- ✅ 测试覆盖：GetTurn 返回状态 + 未知 turn 404、replayEvents 按 turn_id 过滤及优先级

**参考**：QM `core/orchestrator.ts`（单一入口编排）

#### 1.2 Durable event journal + checkpoint ✅（2026-08-10）

**问题**：session 主要在 turn 完成后持久化，中途 crash 丢数小时工作。

**方案**：
- ✅ append-only journal（已有 `events.jsonl`，逐条追加 recordable 事件，本轮核验确认）
- ✅ 落盘点：user append、assistant append、tool 事件、turn completion（已有 checkpoint 回调，核验确认）
- ✅ **A. journal 轮转**（新增）：turn 达 terminal 后截断 `events.jsonl` + 同步 SQLite `EventJournal`，journal 只保留崩溃恢复增量，解决无界增长
- ✅ **B. compaction 落盘**（新增）：compaction 完成后立即触发 checkpoint，压缩上下文不丢
- ✅ 启动时恢复 running/pending/error 状态（已有 `recoverInterruptedTurn`，自动 resume + 防死循环，核验确认）
- ✅ 测试覆盖：turn 完成轮转 journal、best-effort 轮转、相关回放测试通过

**参考**：QM `runs/worker.ts`（lease + heartbeat）、Codex `rollout/`（事件流持久化）

#### 1.3 幂等性存储 ✅（2026-08-10）

**问题**：cron/heartbeat/subagent 无幂等性保证，可能重复执行。

**方案**：
- ✅ 实现 `IdempotencyStore` 接口（`once(key, fn)` + `committed(key)`）
- ✅ SQLite backing store，14 天 retention
- ✅ 集成到 cron、heartbeat（subagent 天然有 job ID 唯一性，暂不集成）
- ✅ 测试覆盖：首次执行、重复跳过、错误传播、并发防重、retention prune

**参考**：`temp/qm/src/idempotency/idempotency-store.ts`（78 行）

#### 1.4 Worker Lease + Heartbeat ✅（2026-08-10）

**问题**：subagent/workflow 没有 lease 机制，进程重启后 running 状态丢失。

**方案**：
- ✅ 实现 `LeaseStore` 接口（`Acquire/Heartbeat/Release/ReapExpired/IsLeased`）+ SQLite 实现（`stateDir/leases.db`）
- ✅ subagent 运行循环接入 lease：TTL/3 间隔心跳，连续 3 次丢失自动取消运行
- ✅ 方案 A 恢复语义：优雅释放 → job 保持终端状态；lease 过期（崩溃）→ 标记 `interrupted` 保留现场，不自动重跑
- ✅ 接线：`newSubagentJobStoreWithLease`（buildDependencies 默认启用）、`SetLeaseStore` 启动时 reap 孤儿 lease
- ✅ 测试覆盖：Acquire/冲突/Heartbeat 续租过期/Release/ReapExpired 等 13 项

**参考**：`temp/qm/src/runs/worker.ts`（188 行完整 worker 循环）

---

### 🟡 Phase 2：动态 Agent 图（P1，longtask 重构核心）

#### 2.1 明确 longtask 语义：动态并行 DAG ✅（2026-08-10）

**决策**：不用串行 chain，用动态并行 DAG。效率和速度优先。

**当前问题**：
- longtask 的串行故事语义与 workflow 的并行 DAG 语义在 5 个关键点错位
- 重启后 `sync.Map` 清空 → 全部中断（见 2.2）
- 上下文预算与长任务时长冲突（见 2.3）

**方案（已落地）**：
- ✅ 保留 `workflow.go` 的 DAG 基础设施（nodes + edges + dependencies）
- ✅ 删除 longtask 预编译串行 chain：`CompileStories` 不再强制 `deps=stories[i-1]`，只用显式依赖
- ✅ `longTaskStoryInput` / `LongTaskStoryView` 新增 `depends_on` 字段
- ✅ 无依赖子任务自动并行：`startLongTaskParallel` fan-out 所有 deps-complete 的 pending story（受 subagent 并发上限约束）
- ✅ `pickNextAction` 由 start-one 改为 start-ready；race/finalize 语义保留
- ✅ 测试：并行 DAG（无依赖并行 + 有依赖顺序 + depends_on 视图暴露）

**参考**：Graph Engineering、Codex collaboration-mode、QM orchestrator

#### 2.2 重启后恢复运行中 longtask ✅（2026-08-11）

**方案**：
- ✅ `longTaskAsyncRuns` 的持久化底座：`longTaskRunRecord` 每次迭代落盘（`writeLongTaskRun`），内存 `sync.Map` 仅作加速索引，磁盘是唯一事实源
- ✅ `workflowStore.walkLongTaskRuns`：跨全部 workflow 扫描 run 记录，供启动时重建索引
- ✅ 启动恢复：`SharedDependencies.ResumeLongTasksAfterRestart()`（`sync.Once` 幂等）+ `backend.NewService` 接线；`sweepStaleLongTaskRuns` 把崩溃遗留的 `running` 记录翻转为 `interrupted`，可被 `--resume-run-id` 续跑
- ✅ `rebuildInterruptedAsyncRuns`：重启后把 interrupted run 重新注册进内存索引（不自动重跑，安全默认）
- ✅ 子 agent lease 复用 1.4（TTL/3 心跳，崩溃自动标记 interrupted）
- ✅ 测试：`longtask_test.go` 重启恢复（sweep/重建/resume）覆盖

#### 2.3 上下文预算管理 ✅（2026-08-11）

**方案**：
- ✅ `internal/agent/context_budget.go`：token 预算工具（`truncateTextToTokenBudget` 头优先截断，`assembleTruncatedHandoffs` 共享组装，`compressCountTokensForText` 复用 `compress.CountTokens` 字符分类估算，CJK 2 字符/token）
- ✅ 完成的子任务自动摘要化：`workflowHandoffSummary` 改为 token 预算截断（2000 token 上限，不再只按字符数），merge_point 自动合成依赖 handoff 摘要
- ✅ handoff 结果按预算截断：依赖 handoff 注入与 merge_point 合成共用同一截断路径（字节上限 + token 预算）
- ✅ 历史子任务不保留在活跃上下文：子任务 prompt 只注入依赖 handoff 摘要，全量结果留在磁盘 handoff artifact
- ✅ 测试：`context_budget_test.go`（预算内截断/头尾保留/标记/CJK 上限/字节上限）

#### 2.4 AgentGraph 运行时抽象 ✅（2026-08-11）

**方案（已落地 `internal/agent/agentgraph.go` + `agentgraph_test.go`）**：
- ✅ `AgentGraph` 接口：`Create/GetGraph/AddNode/AddEdge/RemoveNode/CancelNode/Run/Wait`（由 `agentGraph` 包装器实现，避免与 `Agent.Run` turn 循环重名）
- ✅ 节点类型：`llm_task`、`subagent_task`、`tool_call`、`user_input`、`merge_point`（存于 node Kind，视图暴露 `node_type`）；`merge_point` 自动完成并合成摘要，`user_input` 保持 pending 等待 `complete_node`
- ✅ 边类型：`data_dependency`（静态调度）、`control_flow`（动态 append，when=status/verdict）、`handoff`（摘要传递，隐含依赖）；视图暴露 `edge_type`
- ✅ 动态增删：`add_node`/`add_edge`（静态边编译进 DependsOn/HandoffFrom，动态边存为 durable workflow edge）、`remove_node`（依赖保护 + 取消运行中 job）、`cancel_node`
- ✅ 全部适配现有 `a.workflows *workflowStore`：复用 `create/load/appendWorkflowNodes/startWorkflowReadyNodes/cancelWorkflowNode/completeWorkflowNode/processWorkflowEdges`，图跨重启可恢复
- ✅ 工具注册：`agent_graph`（bundle `subagent`，`tool_registration.go`）
- ✅ 可观测：`agentGraphViewFromState` 复用 `workflowViewFromState` + 派生 typed edges
- ✅ 测试：图创建/node_type/edge_type 暴露、依赖调度（并行 fan-out）、merge_point 自动完成、user_input 阻塞、动态增删、control_flow append-on-outcome

**参考**：
- Codex `spawn_agent`/`send_input`/`wait_agent`（`temp/codex/codex-rs/tools/src/agent_tool.rs`）
- Codex `AgentControl`（`temp/codex/codex-rs/core/src/agent/control.rs`）
- Graph Engineering 范式（DAG 作为一等公民，运行时可编辑）

---

### 🟠 Phase 3：Agent 身份与记忆系统增强（P2）

#### 3.1 记忆策略模式（Memory Strategy） ✅（2026-08-11）

**问题**：记忆行为固定，不可配置、不可组合。

**方案（已落地）**：
- ✅ `strategy.go`：`Strategy` 接口（Kind/Capture/Maintain）+ 三种策略：`per-turn`（默认，= 当前行为）、`agent-only`（关闭自动提取）、`consolidated`（per-turn 捕获 + LLM 合并/去重/删除候选）；`ParseStrategyKind` 归一化变体
- ✅ `consolidation.go`：`Consolidator`（LLM 一次性 prompt → `UPDATE/DELETE/ADD` actions 解析 → 应用到候选 inbox）；阈值触发（`consolidate_after`，默认 10）、模型失败自动降级 capture-only、`MaybeMaintain`/`Maintain`
- ✅ 配置化：`godex.json` `memory.strategy` / `memory.consolidate_after`（types/config/resolve/defaults/values/schema 全链路 + `normalizeMemoryStrategyKind`）；agent 接线按配置构建 strategy，`agent-only` 关闭捕获、`consolidated` 注入 LLM one-shot
- ✅ 测试：strategy 单测（kind 归一化/默认/降级/agent-only 跳过/consolidated 捕获+维护）、consolidation 单测（parse/apply/阈值/落盘/降级/nil 安全）+ config 解析测试（11 个新测试），全量回归无新增失败

**参考**：`temp/qm/src/memory/strategy.ts` + `consolidation.ts`

#### 3.2 记忆 notebook 去重（foldCapture） ✅（2026-08-11）

**问题**：`Remember` 直接追加，无去重。

**方案（已落地）**：
- ✅ `fold.go`：`normalizeMemoryLine`（去 bullet/日期前缀、压空白、小写）+ `foldCapture`（normalize 后对比，重复行跳过、新行追加，返回合并 body 与新增行数）+ `truncateTextTailToTokenBudget`（capTail：token 预算下保留尾部最新内容）
- ✅ `Remember` 同 title 更新路径接入 foldCapture：重复 facts 不再累积，新 facts 追加；显式 `Update` 仍整体替换
- ✅ `layers.go` `fitMemoryToBudget` 的 content 截断改用 capTail（保留尾部最新），summary 保持头部截断
- ✅ 测试：normalize 单测、foldCapture 追加/全重复/空输入/空已有、capTail 保留尾部、Remember 同 title 去重集成（7 个新测试）

**参考**：`temp/qm/src/memory/notebook.ts` + `memory-service.ts` 的 `foldCapture`

#### 3.3 Agent Identity 解耦 ✅（2026-08-11）

**问题**：`internal/agent/agent.go` 同时承担 composition root、session state holder、tool registry、sandbox facade。

**方案（已落地）**：
- ✅ `internal/sandbox/sandbox.go`：定义 `Sandbox` 接口（ID/Lifecycle/WorkspaceDir/TempDir/ArtifactDir/ToolBinding/Info/FileSystem/Rebuild，Rebuild 返回接口），对齐参考实现接口面；具体结构体改名 `LocalSandbox` 并实现接口（`var _ Sandbox = (*LocalSandbox)(nil)` 编译期断言）
- ✅ `localSandboxFromConfig` / `ensureSandbox` 返回 `sandbox.Sandbox` 接口；`Agent.sandbox` 字段（Agent + dependencies）类型改为接口
- ✅ Agent 通过接口使用 sandbox（SandboxID/SandboxBinding/SandboxInfo/RebuildSandbox），不依赖具体本地实现
- ✅ 测试：接口契约测试（LocalSandbox 通过接口满足全部能力）+ fake Sandbox 注入验证 Agent 依赖接口而非具体类型（3 个新测试），全量回归无新增失败

**参考**：`temp/qm/src/sandbox/sandbox.ts`（接口定义）

---

### 🟢 Phase 4：多 Agent 通信 + 角色 Bundle 集成（P3）

#### 4.1 spawn/send_input/wait 工具 ✅（2026-08-11）

**方案（已落地）**：
- ✅ `send_input` / `followup_task`：subagent 工具新增两个 action，消息入 `subagentJob.PendingInputs` 持久化队列
- ✅ Runner 注入通道：`runSubagentJob` 接线 `DrainInjections`/`AppendInjectedMessages`，运行中的子 agent 在 turn 边界自动吸收排队输入
- ✅ spawn/wait 已有：`subagent` 工具的 `start`（≈spawn_agent）与 `wait`（≈wait_agent）
- ✅ 测试：队列/弹出/终态拒绝/工具接线/Runner 注入端到端（5 个新测试）

#### 4.2 子 agent 双向通信 ✅（2026-08-11）

**方案（已落地）**：
- ✅ `ReopenForIteration`：store 新增方法，允许从 completed/error 状态重新打开 job（清 result/error，置 running，可携带反馈）
- ✅ `IterateDurableSubagentWithContext` + subagent 工具 `iterate` action：review→fix→re-review 循环——注入 review 反馈（复用 4.1 PendingInputs 通道）→ 重跑 → 完成后可再次 review
- ✅ 不是单向 handoff：父 agent 可对同一 job 多轮 iterate，直到 review 通过
- ✅ 测试：重开/拒绝 running/非终态拒绝/带反馈重跑注入（4 个新测试）

**参考**：Codex `multi_agents_v2.rs`（spawn/followup_task/send_message/wait 工具面）

#### 4.3 角色→bundle 运行时映射 ✅（2026-08-11）

**问题**：`Role` 结构体定义了 `DefaultBundles`，但运行时"激活角色→自动激活对应 bundle"的机制缺失。

**方案（已落地）**：
- ✅ `RoleBundleRegistry`（`internal/agent/role_bundles.go`）：集中管理「角色 → 默认 bundle」映射，`RegisterRole` 可覆盖内置或注册 package role，空集合删除条目
- ✅ 内置角色映射对齐路线图分工表：orchestrator `[core_code lsp planning subagent web]`，worker/reviewer `[core_code lsp]`，researcher `[web]`，planner `[core_code lsp planning]`
- ✅ `BundlesForRole`：显式 roleID 命中优先（package role `DefaultBundles` 或内置），否则按 agentType 关键词推断内置角色
- ✅ subagent 创建时自动解析：`startDurableSubagentWithContext` 合并 required_bundles + 角色 bundles + 4.4 继承 bundles，写入 `DefaultBundles` 并展开为可用工具（`appendRequiredSubagentTools`）
- ✅ 测试：内置映射/注册覆盖/未知角色空/优先 role.DefaultBundles/创建时解析（5 个新测试）

**参考**：`internal/core/packages/packages.go` 的 `Role` 结构体、`internal/agent/role_bundles.go`

#### 4.4 子 agent bundle 继承 ✅（2026-08-11）

**方案（已落地）**：
- ✅ 子 agent 默认继承父 agent 活跃 bundle 集合：`inheritedSubagentBundles` 读取 `toolHandler.Catalog().ActiveBundles`
- ✅ `bundle_overrides` 覆盖继承：非空时以 overrides 替换继承集合（`subagent` 工具新参数，持久化到 `subagentJob.BundleOverrides`）
- ✅ `deactivate_bundles` 移除不需要的 bundle：从继承集合过滤（`subagentJob.DeactivateBundles` 持久化）；general-purpose 默认工具面不受影响，仅收窄 bundle 继承层
- ✅ 贯穿全链路：`subagent` 工具 args/schema → `durableSubagentStartRequest` → `subagentStartOptions` → `subagentJob` 持久化 → `DurableSubagentJobView`/worker contract `CapabilitySet`
- ✅ 测试：默认继承父活跃/overrides 替换/deactivate 移除/创建时生效（4 个新测试）

#### 4.5 写 scope 与 bundle 联动 ✅（2026-08-11）

**方案（已落地）**：
- ✅ 新增虚拟能力 bundle `writing`（`internal/agent/tool_registration.go`）：不挂具体工具，仅声明"该子 agent 可写"；内置角色映射（orchestrator/worker/reviewer/planner）默认含 writing，researcher 天然只读
- ✅ `resolveSubagentWriteScope`（`subagent_policy.go`）统一解析链：显式 write_scope > role.WriteScope（package role 声明，`Role` 新增 `WriteScope` 字段）> nil；bundle 集合不含 writing/core_code 且角色默认工具面不含写工具时显式 scope 也被忽略（天然只读）
- ✅ `appendRequiredSubagentTools` writing 分支：仅当存在有效 scope 才展开 bash/write_file/edit_file，无 scope 则只读降级；core_code 视为隐式 writing（兼容旧调用）；general-purpose 默认工具面（硬编码含写工具）不受 bundle 影响
- ✅ 创建/恢复路径统一：`startDurableSubagentWithContext` 与 `localGoDexWorkerRuntime.Dispatch` 均走解析链 + `narrowSubagentWriteTools` 收窄，避免 Dispatch 重建时把 bundle 写工具重新加回
- ✅ 角色切换时写 scope 自动更新：`ReopenForIterationWithUpdate` + `IterateDurableSubagentWithUpdate`（`subagent` 工具 iterate action 透传 agent_type/write_scope/bundle_overrides/deactivate_bundles），重开时重新解析角色 bundles 与 scope 并更新 job 配置
- ✅ 测试：解析链优先级/可写判定/writing 分支/worker 有 scope 展开/worker 无 scope 只读/无 writing 忽略 scope/重开更新配置/iterate 切换角色（8 个新测试）
- ✅ 顺带修复 4.3/4.4 引入的 2 个回归（Dispatch 重建未收窄写工具、package role 测试未注册父工具）与 2 个 4.5 新回归（general-purpose 默认工具面写能力未计入、worktree 审批路径）

#### 4.6 上下文预算按角色分配 ✅（2026-08-11）

**方案（已落地）**：
- ✅ 不同角色有不同最大 token 预算：orchestrator 200K，worker 100K，reviewer 100K，researcher 50K
- ✅ `roleContextBudgetTokens()` 解析 + `subagentStartOptions`/`subagentJob` 新增 `ContextBudget`（持久化，显式值优先）
- ✅ 超出预算时自动触发 compaction：`runSubagentJob` 的 BuildRequest 前用 `estimateMessages` 估算，超预算则 rule-based summarizer 压缩并 checkpoint

---

### 🔵 Phase 5：架构基础设施（P4）

#### 5.1 Harness 多引擎抽象 ✅（2026-08-11）

**问题**：只支持一个 LLM 后端，没有抽象层切换不同 agent 引擎。

**方案（已落地）**：
- ✅ `harness.go`：`Harness` 接口（`Profile`/`Models`/`Tools`/`RunTurn`/`ResetSession`/`Close`，对应 roadmap 的 runTurn/resetSession/close/profile/models/tools）+ `HarnessProfile`/`HarnessTurnInput`/`HarnessTurnResult` 类型
- ✅ `godexHarness` 默认实现：包装现有 `Agent.RunWithOptions`（`NewGodexHarness`），引擎行为不变，抽象层先行
- ✅ `harnessRouter`：`adapters` map + `HarnessResolver` 按 turn 选择引擎；会话切换引擎时自动 `ResetSession` 旧+新（对齐 harness-router.ts 的 lastHarness 语义）；`NewDefaultHarnessResolver` 默认路由 godex
- ✅ `Agent.Harness()` 访问器：backend 统一入口，现有 `RunWithOptions` 路径不受影响
- ✅ 测试：接口契约（godex 满足 Harness）、路由选择、切换时旧/新引擎各 reset 一次、同引擎不重复 reset、Close 去重、unavailable adapter 报错（6 个新测试）

**参考**：`temp/qm/src/harness/harness.ts`（202 行接口）+ `harness-router.ts`

#### 5.2 Turn Error 分层 ✅（2026-08-11）

**方案（已落地）**：
- ✅ `turn_error.go`：`TurnError` 接口（`Class() TurnErrorClass`）+ 三类错误（`RetryableTurnError`/`TransientTurnError`/`NonRetryableTurnError`）+ `ClassifyTurnError`（显式分类优先，其次 context/网络/HTTP 状态推断，复用 shouldRetryError 信号）+ `TurnFailureMessage`（NonRetryable 透出 message，其余通用文案，对齐 turn-error.ts）
- ✅ Runner 集成：`callModel` 错误按分类路由——Retryable/Transient 在**同一 turn 内**有限重试（`MaxModelRetries` 默认 2，不消耗外层 maxTurns 预算），NonRetryable 立即失败；新增 `PhaseRecoveryAttempt` 阶段事件
- ✅ backend 消费：`turn.go` 的 errorText 对 NonRetryable 错误用 `TurnFailureMessage` 透出明确 message，其余保留原始错误可调试
- ✅ 测试：分类（显式/HTTP 状态码/传输错误/包装错误）、TurnFailureMessage、Runner 重试路由（transient 重试后成功/预算耗尽返回原错/non-retryable 不重试）共 13 项，conversation 全绿

**参考**：`temp/qm/src/core/turn-error.ts`

#### 5.3 持久化 Map 抽象 ✅（2026-08-11，接口 + SQLite 实现）

**方案（已落地）**：
- ✅ `internal/core/persistence/durable_map.go`：`DurableMap[V]` 接口（All/Entries/Get/Put/PutIfAbsent/InsertIfAbsent/Update/DeleteIf/Delete/Take，对齐 durable-map.ts）+ `MemoryMap[V]` 内存实现 + `SQLiteMap[V]` SQLite 实现（`dir/<table>.db`，表名白名单校验，JSON 序列化，`ON CONFLICT` upsert）
- ✅ 测试：MemoryMap/SQLiteMap 全接口契约、SQLite 重开持久化、非法表名拒绝、db 文件位置（5 个新测试），全绿
- ⚠️ **替换文件读写试点结论**：尝试用 SQLiteMap 替换 `candidates.json` 时发现顺序语义冲突——candidates 是**有序数组**，consolidation 用 `UPDATE <n>/DELETE <n>` 位置索引操作列表，而 DurableMap 按 key 排序，替换后 ListCandidates 顺序改变导致 consolidation 删错对象。**结论：有序数组文件（candidates.json/index.json 等）不适合直接用 key-value DurableMap 替换**，已回退该试点；DurableMap 适用于真正的 key-value 语义存储（如按 fingerprint/id 索引的映射），后续替换需保持数组顺序语义或改用带顺序键的存储。

**参考**：`temp/qm/src/persistence/durable-map.ts`

---

### ⚪ Phase 6：长期愿景（可规划暂不启动）

#### 6.1 安全筛查器（Security Screener） ✅（2026-08-12）

**方案（已落地，最小 shadow 版）**：
- ✅ `internal/core/security/screener.go`：核心类型（`ScreenHook` user_input/tool_response、`ScreenVerdict` auto/strict、`Screener` 接口）、分块（≤1600 字符/块 + 256 重叠 + CJK 安全截断、总上限 16000）、多模型投票聚合（strict 优先、同 decision 取高分、任一 unscreened 则整体 unscreened）、降级（`[NOT security-screened ...]` 语义）、`NoopScreener`
- ✅ `llm_screener.go`：LLM 分类器（复用 `conversation.Caller`），系统提示词声明内容是未信任数据，解析 `{score, threshold, primary_outcome}` JSON（容错 markdown fence），任何失败降级为 unscreened 不阻断
- ✅ shadow 语义：`Shadow()` 为 true 时分类 fire-and-forget（后台 goroutine + 10s 超时），hook 立即返回 auto，**永不 gate 或延迟主链路**；非 shadow（未来权威模式）同步返回真实 verdict
- ✅ config：`security.screener.{enabled,shadow,provider,timeout_ms,max_tokens}`（含 env 覆盖 `GODEX_SECURITY_SCREENER_*`），默认 disabled + shadow=true
- ✅ Agent 挂载：`buildScreener`（disabled/无 client → noop）、`SetScreener`/`SetScreenAudit` 注入、`ScreenUserInput`/`screenToolResult` 两个 hook
- ✅ hook 接入：user_input → backend `startUserTurnLocked` 中 AddEnvelope 前（shadow 不阻塞 turn）；tool_response → `filterModelToolResult` 开头（压缩前）
- ✅ 审计：backend `wireScreenAudit` 把 verdict 写入 security audit（`screen_<hook>` action、malicious → warning 严重级、metadata 带 score/threshold/outcome/unscreened）
- ✅ 测试：核心 13 个（分块/投票/降级/LLM 解析容错）+ agent 8 个（shadow 不阻塞、非 shadow 同步+审计、noop 默认）+ backend 3 个（审计落盘、hook 不阻塞 turn、shadow fire-and-forget），全绿

**参考**：`temp/qm/src/security/security-screener.ts`（269 行）+ `security-posture.ts`

#### 6.2 Scope 隔离模型

**方案**：定义 `ScopeId` 类型，在 memory/files/sandbox 中引入 scope 参数。

#### 6.3 Session 树（可分支） ✅（2026-08-12）

**方案（已落地）**：
- ✅ backend `SessionTree(ctx, sessionID)`：按 `parent_session_id` 从磁盘发现全部 fork 关联（无需打开 session），返回树节点（session/title/parent/branch_title/forked_from_turn_id + graph 摘要），children 按 updated_at 降序
- ✅ backend `RollbackSession(ctx, sessionID, nodeID)`：`RollbackBranch(main, nodeID)` 回滚主分支 head 到早期 graph node + 持久化 + security event
- ✅ backend `MergeSessionBranch(ctx, sessionID, branchID, summary)`：`MergeBranch(main, branchID, nodeID, record)` 把 worker 分支合并回主分支（merge record 记录 source branch/summary）+ 持久化 + security event
- ✅ 复用既有基础：`ForkSession`（分支创建）+ `sessiongraph.SessionGraph`（EnsureMainBranch/CloneBranch/RollbackBranch/MergeBranch + JSON 持久化）+ `appendSessionGraphCheckpoint`（checkpoint 节点）
- ✅ 测试：fork 层级树查询、未知根报错、回滚移动 main head（持久化校验）、未知节点报错、合并记录 source branch、空 branch 报错（6 个新测试），全绿

**参考**：`docs/architecture-v2-spec.md` §3（Session Memory 必须成为可分支的树）

#### 6.4 多引擎热切换 ✅（2026-08-12）

**方案（已落地）**：
- ✅ `RunOptions.Harness` 字段：每轮 turn 可请求非默认引擎（空或 `godex` 保持默认循环）
- ✅ `Agent.RegisterHarness(id, harness)`：注册额外引擎；`Agent.harnessRouter()` 惰性构建 router（godex + 已注册引擎），resolver 用 `NewRequestedHarnessResolver`（按 input.Harness 路由，空则 godex）
- ✅ `RunWithOptions` 开头路由：`opts.Harness` 非空且非 godex 时经 `harnessRouter.RunTurn` 执行——会话切换引擎时自动 `ResetSession` 旧+新引擎（复用 5.1 语义），同引擎不重复 reset
- ✅ backend 透传：`turn.go` 从 `envelope.Metadata["harness"]` 读取引擎请求传给 `RunOptions.Harness`
- ✅ `HarnessTurnInput.Harness` 字段：携带 per-turn 引擎请求
- ✅ 测试：resolver 按请求/默认路由、注册引擎路由执行、unavailable 报错、切换时旧/新引擎各 reset 一次、同引擎不重复 reset、默认路径不触达注册引擎（6 个新测试），全绿

**参考**：`temp/qm/src/harness/harness-router.ts`（resolveRuntimeChoice + createHarnessRouter）

#### 6.5 自然语言创建 longtask ✅（2026-08-12）

**方案（已落地）**：
- ✅ `longtask` 工具新增 `plan` action：入参 `description`（自然语言任务描述），Agent 内部调用 LLM（`a.client.Call`，复用 `GenerateTitle` 的调用模式）把描述拆解成 stories JSON
- ✅ `parseLongTaskPlanStories`：容忍 markdown code fence 与 prose 包裹，提取 stories JSON 数组（字段对齐 `longTaskStoryInput`：id/title/description/acceptance_criteria/priority/agent_type/write_scope/depends_on）
- ✅ 拆解结果走既有 `createLongTask` 流程：`longTaskDefaultStoryCompiler` 编译 + `workflows.create` + `writeLongTaskSpec`，零重复逻辑
- ✅ 测试：描述→2 story 创建（依赖关系 US-002→US-001）、fence 容错、缺 description 报错、空 stories 报错（4 个新测试），全绿

**参考**：`internal/agent/longtask_plan.go`（roadmap 6.5 入口）

---

## 优先级矩阵

| 阶段 | 编号 | 项目 | 难度 | 收益 | 依赖 | 估时 |
|------|------|------|------|------|------|------|
| ✅ | P0-1 | 记忆类型扩展 | 低 | 中 | 无 | 已完 |
| ✅ | P0-2 | 笔记↔记忆联动 | 中 | 高 | 无 | 已完 |
| ✅ | P0-3 | 记忆注入笔记引用 | 低 | 中 | P0-2 | 已完 |
| ✅ | P0-4 | API + UI 联动 | 低 | 中 | P0-2 | 已完 |
| ✅ | P0-5 | 设计文档更新 | 低 | 低 | 全部 | 已完 |
| ✅ | 1.1 | 异步 turn/job runtime | 高 | 高 | 无 | 已完（2026-08-10） |
| ✅ | 1.2 | Durable event journal | 高 | 高 | 1.1 | 已完（2026-08-10） |
| ✅ | 1.3 | 幂等性存储 | 低 | 中 | 无 | 已完（2026-08-10） |
| ✅ | 1.4 | Worker Lease + Heartbeat | 中 | 高 | 1.3 | 已完（2026-08-10） |
| ✅ | 2.1 | 动态并行 DAG 语义明确 | 中 | 高 | 无 | 已完（2026-08-10） |
| ✅ | 2.2 | 重启后恢复 longtask | 中 | 高 | 1.4 | 已完（2026-08-11） |
| ✅ | 2.3 | 上下文预算管理 | 低 | 中 | 无 | 已完（2026-08-11） |
| ✅ | 2.4 | AgentGraph 运行时抽象 | 高 | 高 | 2.1 | 已完（2026-08-11） |
| ✅ | 3.1 | 记忆策略模式 | 中 | 高 | 无 | 已完（2026-08-11） |
| ✅ | 3.2 | 记忆 notebook 去重 | 低 | 中 | 无 | 已完（2026-08-11） |
| ✅ | 3.3 | Agent Identity 解耦 | 高 | 高 | 无 | 已完（2026-08-11） |
| ✅ | 4.1 | spawn/send_input/wait | 中 | 高 | 2.4 | 已完（2026-08-11） |
| ✅ | 4.2 | 子 agent 双向通信 | 中 | 高 | 4.1 | 已完（2026-08-11） |
| ✅ | 4.3 | 角色→bundle 运行时映射 | 中 | 高 | 2.4 | 已完（2026-08-11） |
| ✅ | 4.4 | 子 agent bundle 继承 | 中 | 高 | 4.3 | 已完（2026-08-11） |
| ✅ | 4.5 | 写 scope 与 bundle 联动 | 中 | 中 | 4.3 | 已完（2026-08-11） |
| ✅ | 4.6 | 上下文预算按角色分配 | 中 | 中 | 2.3 | 已完（2026-08-11） |
| ✅ | 5.1 | Harness 多引擎抽象 | 高 | 高 | 3.3 | 已完（2026-08-11） |
| ✅ | 5.2 | Turn Error 分层 | 低 | 中 | 无 | 已完（2026-08-11） |
| ✅ | 5.3 | 持久化 Map 抽象 | 中 | 中 | 无 | 已完（2026-08-11，接口+SQLite；替换试点发现有序数组语义冲突） |
| ⚪ | 6.1 | 安全筛查器 | 中 | 中 | 无 | 1w |
| ⚪ | 6.2 | Scope 隔离模型 | 中 | 中 | 3.3 | 1w |
| ⚪ | 6.3 | Session 树 | 高 | 高 | 3.3+5.3 | 3w |
| ✅ | 6.4 | 多引擎热切换 | 高 | 中 | 5.1 | 已完（2026-08-12） |
| ⚪ | 6.5 | 自然语言创建 longtask | 中 | 中 | 2.4+4.1 | 1w |

---

## 推荐执行顺序

```
🔴 Phase 1（当前聚焦，长任务基底）：
  ✅ 1.1 异步 turn runtime    → 已完成（2026-08-10）
  ✅ 1.2 Durable event journal → 已完成（2026-08-10）
  ✅ 1.3 幂等性存储          → 已完成（2026-08-10）
  ✅ 1.4 Worker Lease        → 已完成（2026-08-10）
  ✅ 2.1 动态并行 DAG 语义   → 已完成（2026-08-10）
  ✅ 2.2 重启后恢复          → 已完成（2026-08-11）
  ✅ 2.3 上下文预算管理       → 已完成（2026-08-11）

🟡 Phase 2（下一个，longtask 重构核心）：
  ✅ 2.4 AgentGraph 抽象     → 已完成（2026-08-11）
  ✅ 3.1 记忆策略模式         → 已完成（2026-08-11）
  ✅ 3.2 记忆 notebook 去重   → 已完成（2026-08-11）
  ✅ 3.3 Agent Identity 解耦  → 已完成（2026-08-11）

🟢 Phase 4（多 agent 通信 + 角色 bundle 集成）：
  ✅ 4.1 spawn/send_input/wait → 已完成（2026-08-11）
  ✅ 4.2 子 agent 双向通信     → 已完成（2026-08-11）
  ✅ 4.3 角色→bundle 运行时映射 → 已完成（2026-08-11）
  ✅ 4.4 子 agent bundle 继承   → 已完成（2026-08-11）
  ✅ 4.5 写 scope 与 bundle 联动 → 已完成（2026-08-11）
  ✅ 4.6 上下文预算按角色分配   → 已完成（2026-08-11）

🔵 Phase 4（架构基础设施）：
  ✅ 5.1 Harness 多引擎抽象    → 已完成（2026-08-11）
  ✅ 5.2 Turn Error 分层       → 已完成（2026-08-11）
  ✅ 5.3 持久化 Map 抽象       → 已完成（2026-08-11，接口+SQLite；有序数组文件不适用，见 5.3 结论）
```

## 文档维护规则

1. 已完成的 Phase 移入"当前基线"，不长期留在待办区
2. 新需求先进入对应 Phase 小节，不急着拆实现细节
3. 一旦进入实施计划，单独写具体 plan 或 issue
4. 每次完成一个阶段后，更新"当前基线"和对应验收状态