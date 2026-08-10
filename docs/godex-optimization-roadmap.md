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
| **指令系统** | `AGENT.md`、本地规则、动态上下文、skills/package prompt 可组合注入 |
| **Skill/Package** | native skill、第三方 normalizer、compatibility analyzer、package command dispatcher、role registry |
| **Tool runtime** | typed tools、参数 coercion、before/after interceptors、审批模式、bundle 管理 |
| **Subagent/Workflow** | durable subagent、batch/wait、分层 job storage、per-job timeout、workflow handoff artifact、dependency injection、preview merge、dynamic append node |
| **Runner 韧性** | 统一 phase checkpoint、active turn follow-up injection、空回复恢复、length/provider error 恢复 |
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

#### 1.1 异步 turn/job runtime

**问题**：Web 消息提交绑定 HTTP 请求生命周期，长任务断开后丢失状态。

**方案**：
- `POST /sessions/{id}/messages` 返回 `202 + turn_id`
- 服务端用独立 context 运行 turn
- SSE 按 `turn_id` 推送状态
- 增加 cancel endpoint

**参考**：QM `core/orchestrator.ts`（单一入口编排）

#### 1.2 Durable event journal + checkpoint

**问题**：session 主要在 turn 完成后持久化，中途 crash 丢数小时工作。

**方案**：
- 每个 session/turn 写 append-only journal
- 在 user append、assistant append、tool start/finish、permission pending、compaction、turn completion 后落盘
- 启动时 replay journal，恢复 running/pending/error 状态

**参考**：QM `runs/worker.ts`（lease + heartbeat）、Codex `rollout/`（事件流持久化）

#### 1.3 幂等性存储

**问题**：cron/heartbeat/subagent 无幂等性保证，可能重复执行。

**方案**：
- 实现 `IdempotencyStore` 接口（`once(key, fn)` + `committed(key)`）
- SQLite backing store，14 天 retention
- 集成到 cron、heartbeat、subagent dispatch

**参考**：`temp/qm/src/idempotency/idempotency-store.ts`（78 行）

#### 1.4 Worker Lease + Heartbeat

**问题**：subagent/workflow 没有 lease 机制，进程重启后 running 状态丢失。

**方案**：
- 实现 `LeaseStore` 接口（SQLite 实现）
- 连续 N 次心跳丢失自动取消
- 进程重启时通过 lease 恢复 running 状态

**参考**：`temp/qm/src/runs/worker.ts`（188 行完整 worker 循环）

---

### 🟡 Phase 2：动态 Agent 图（P1，longtask 重构核心）

#### 2.1 明确 longtask 语义：动态并行 DAG

**决策**：不用串行 chain，用动态并行 DAG。效率和速度优先。

**当前问题**：
- longtask 的串行故事语义与 workflow 的并行 DAG 语义在 5 个关键点错位
- 重启后 `sync.Map` 清空 → 全部中断
- 上下文预算与长任务时长冲突

**方案**：
- 保留 `workflow.go` 的 DAG 基础设施（nodes + edges + dependencies）
- 删除 longtask 的"预编译串行 chain"模式
- 改为：agent 在运行时动态创建和管理子任务图
- 子任务之间用 `data_dependency` 和 `control_flow` 边表达依赖
- 无依赖的子任务自动并行执行

#### 2.2 重启后恢复运行中 longtask

**方案**：
- `longTaskAsyncRuns` 从 `sync.Map` 改为持久化存储
- 启动时恢复 interrupted 状态，提供 resume 入口
- 子 agent 的 lease 机制（复用 1.4）

#### 2.3 上下文预算管理

**方案**：
- 完成的子任务自动摘要化
- handoff 结果按 token 预算截断
- 历史子任务不保留在活跃上下文中

#### 2.4 AgentGraph 运行时抽象

**方案**：
- 定义 `AgentGraph` 接口（借鉴 Codex 的 spawn/send_input/wait 模型）
- 节点类型：`llm_task`、`subagent_task`、`tool_call`、`user_input`、`merge_point`
- 边类型：`data_dependency`、`control_flow`、`handoff`
- 运行时动态添加/删除节点、重连边
- 图可观测：每个节点 input/output/status/耗时可查

**参考**：
- Codex `spawn_agent`/`send_input`/`wait_agent`（`temp/codex/codex-rs/tools/src/agent_tool.rs`）
- Codex `AgentControl`（`temp/codex/codex-rs/core/src/agent/control.rs`）
- Graph Engineering 范式（DAG 作为一等公民，运行时可编辑）

---

### 🟠 Phase 3：Agent 身份与记忆系统增强（P2）

#### 3.1 记忆策略模式（Memory Strategy）

**问题**：记忆行为固定，不可配置、不可组合。

**方案**：
- 定义 `MemoryStrategy` 接口
- 默认策略 = 当前行为
- 新增 `consolidation` 策略：LLM 自动合并/去重/删除候选记忆
- 配置化：`godex.json` 允许选择策略组合

**参考**：`temp/qm/src/memory/strategy.ts` + `consolidation.ts`

#### 3.2 记忆 notebook 去重（foldCapture）

**问题**：`Remember` 直接追加，无去重。

**方案**：
- 实现 `foldCapture` 去重逻辑（normalize 后对比）
- 在 `layers.go` 的 `trimMemoriesToTokenBudget` 中改用 `capTail` 策略（保留尾部最新内容）

**参考**：`temp/qm/src/memory/notebook.ts` + `memory-service.ts` 的 `foldCapture`

#### 3.3 Agent Identity 解耦

**问题**：`internal/agent/agent.go` 同时承担 composition root、session state holder、tool registry、sandbox facade。

**方案**：
- 定义 `Sandbox` 接口
- 当前 `localSandboxFromConfig` 改为 `Sandbox` 接口实现
- `Agent` 通过接口使用 sandbox，不直接操作文件系统

**参考**：`temp/qm/src/sandbox/sandbox.ts`（接口定义）

---

### 🟢 Phase 4：多 Agent 通信 + 角色 Bundle 集成（P3）

#### 4.1 spawn/send_input/wait 工具

**方案**：
- 实现 `spawn_agent` 工具：创建子 agent，可选 fork 上下文
- 实现 `send_input` 工具：发送消息到子 agent（可 interrupt 或 queue）
- 实现 `wait_agent` 工具：等待子 agent 完成
- 实现 `followup_task` 工具：给子 agent 分配新任务

#### 4.2 子 agent 双向通信

**方案**：
- 支持 review → fix → re-review 循环
- 不仅仅是单向 handoff

**参考**：Codex `multi_agents_v2.rs`、`multi_agents/spawn.rs`

#### 4.3 角色→bundle 运行时映射

**问题**：`Role` 结构体定义了 `DefaultBundles`，但运行时"激活角色→自动激活对应 bundle"的机制缺失。

**方案**：
- 实现 `RoleBundleRegistry`：集中管理角色和 bundle 的映射关系
- subagent 创建时根据角色自动激活对应 bundle
- 角色切换时自动更新 bundle 激活状态

**参考**：`internal/core/packages/packages.go` 的 `Role` 结构体

#### 4.4 子 agent bundle 继承

**方案**：
- 子 agent 默认继承父 agent 的 bundle 集合
- 允许通过 `bundle_overrides` 覆盖继承的 bundle
- 允许通过 `deactivate_bundles` 移除不需要的 bundle

#### 4.5 写 scope 与 bundle 联动

**方案**：
- 激活 `writing` bundle 时自动应用写 scope 限制
- 角色切换时写 scope 自动更新
- 写 scope 在 bundle 层面做统一管理，不依赖单个工具

#### 4.6 上下文预算按角色分配

**方案**：
- 不同角色有不同最大 token 预算
- orchestrator 200K，worker 100K，reviewer 100K，researcher 50K
- 超出预算时自动触发 compaction

---

### 🔵 Phase 5：架构基础设施（P4）

#### 5.1 Harness 多引擎抽象

**问题**：只支持一个 LLM 后端，没有抽象层切换不同 agent 引擎。

**方案**：
- 定义 `Harness` 接口：`runTurn`、`resetSession`、`close`、`profile`、`models`、`tools`
- 当前 agent loop 提取为默认 harness 实现
- 新增 `harnessRouter` 根据配置路由

**参考**：`temp/qm/src/harness/harness.ts`（202 行接口）+ `harness-router.ts`

#### 5.2 Turn Error 分层

**方案**：
- 定义 `TurnError` 接口，区分 Retryable / NonRetryable / Transient
- 在 `context.go` 的 `Run` 循环中集成错误路由

**参考**：`temp/qm/src/core/turn-error.ts`

#### 5.3 持久化 Map 抽象

**方案**：
- 定义 `DurableMap[K, V]` 接口
- SQLite 实现
- 逐步替换 `index.json`、`candidates.json` 等文件读写

**参考**：`temp/qm/src/persistence/durable-map.ts`

---

### ⚪ Phase 6：长期愿景（可规划暂不启动）

#### 6.1 安全筛查器（Security Screener）

**方案**：内容级安全筛查，分块 + 多模型投票 + shadow mode。

**参考**：`temp/qm/src/security/security-screener.ts`（269 行）

#### 6.2 Scope 隔离模型

**方案**：定义 `ScopeId` 类型，在 memory/files/sandbox 中引入 scope 参数。

#### 6.3 Session 树（可分支）

**方案**：来自 2.0 SPEC，session 从线性 history 变为可分支的树。

#### 6.4 多引擎热切换

**方案**：每轮对话可切换引擎，切换时自动 reset session。

#### 6.5 自然语言创建 longtask

**方案**：用户在对话中描述任务，agent 自动拆解成 AgentGraph。

---

## 优先级矩阵

| 阶段 | 编号 | 项目 | 难度 | 收益 | 依赖 | 估时 |
|------|------|------|------|------|------|------|
| ✅ | P0-1 | 记忆类型扩展 | 低 | 中 | 无 | 已完 |
| ✅ | P0-2 | 笔记↔记忆联动 | 中 | 高 | 无 | 已完 |
| ✅ | P0-3 | 记忆注入笔记引用 | 低 | 中 | P0-2 | 已完 |
| ✅ | P0-4 | API + UI 联动 | 低 | 中 | P0-2 | 已完 |
| ✅ | P0-5 | 设计文档更新 | 低 | 低 | 全部 | 已完 |
| 🔴 | 1.1 | 异步 turn/job runtime | 高 | 高 | 无 | 2w |
| 🔴 | 1.2 | Durable event journal | 高 | 高 | 1.1 | 2w |
| 🔴 | 1.3 | 幂等性存储 | 低 | 中 | 无 | 3d |
| 🔴 | 1.4 | Worker Lease + Heartbeat | 中 | 高 | 1.3 | 1w |
| 🟡 | 2.1 | 动态并行 DAG 语义明确 | 中 | 高 | 无 | 1w |
| 🟡 | 2.2 | 重启后恢复 longtask | 中 | 高 | 1.4 | 1w |
| 🟡 | 2.3 | 上下文预算管理 | 低 | 中 | 无 | 3d |
| 🟡 | 2.4 | AgentGraph 运行时抽象 | 高 | 高 | 2.1 | 2w |
| 🟠 | 3.1 | 记忆策略模式 | 中 | 高 | 无 | 1w |
| 🟠 | 3.2 | 记忆 notebook 去重 | 低 | 中 | 无 | 2d |
| 🟠 | 3.3 | Agent Identity 解耦 | 高 | 高 | 无 | 2w |
| 🟢 | 4.1 | spawn/send_input/wait | 中 | 高 | 2.4 | 1w |
| 🟢 | 4.2 | 子 agent 双向通信 | 中 | 高 | 4.1 | 1w |
| 🟢 | 4.3 | 角色→bundle 运行时映射 | 中 | 高 | 2.4 | 1w |
| 🟢 | 4.4 | 子 agent bundle 继承 | 中 | 高 | 4.3 | 3d |
| 🟢 | 4.5 | 写 scope 与 bundle 联动 | 中 | 中 | 4.3 | 3d |
| 🟢 | 4.6 | 上下文预算按角色分配 | 中 | 中 | 2.3 | 3d |
| 🔵 | 5.1 | Harness 多引擎抽象 | 高 | 高 | 3.3 | 2w |
| 🔵 | 5.2 | Turn Error 分层 | 低 | 中 | 无 | 2d |
| 🔵 | 5.3 | 持久化 Map 抽象 | 中 | 中 | 无 | 1w |
| ⚪ | 6.1 | 安全筛查器 | 中 | 中 | 无 | 1w |
| ⚪ | 6.2 | Scope 隔离模型 | 中 | 中 | 3.3 | 1w |
| ⚪ | 6.3 | Session 树 | 高 | 高 | 3.3+5.3 | 3w |
| ⚪ | 6.4 | 多引擎热切换 | 高 | 中 | 5.1 | 2w |
| ⚪ | 6.5 | 自然语言创建 longtask | 中 | 中 | 2.4+4.1 | 1w |

---

## 推荐执行顺序

```
🔴 Phase 1（当前聚焦，长任务基底）：
  1.3 幂等性存储          → 3d
  1.4 Worker Lease        → 1w
  2.1 动态并行 DAG 语义   → 1w
  2.2 重启后恢复          → 1w
  2.3 上下文预算管理       → 3d

🟡 Phase 2（下一个，longtask 重构核心）：
  2.4 AgentGraph 抽象     → 2w
  3.1 记忆策略模式         → 1w
  3.2 记忆 notebook 去重   → 2d
  3.3 Agent Identity 解耦  → 2w

🟢 Phase 3（多 agent 通信 + 角色 bundle 集成）：
  4.1 spawn/send_input/wait → 1w
  4.2 子 agent 双向通信     → 1w
  4.3 角色→bundle 运行时映射 → 1w
  4.4 子 agent bundle 继承   → 3d
  4.5 写 scope 与 bundle 联动 → 3d
  4.6 上下文预算按角色分配   → 3d

🔵 Phase 4（架构基础设施）：
  5.1 Harness 多引擎抽象    → 2w
  5.2 Turn Error 分层       → 2d
  5.3 持久化 Map 抽象       → 1w
```

## 文档维护规则

1. 已完成的 Phase 移入"当前基线"，不长期留在待办区
2. 新需求先进入对应 Phase 小节，不急着拆实现细节
3. 一旦进入实施计划，单独写具体 plan 或 issue
4. 每次完成一个阶段后，更新"当前基线"和对应验收状态