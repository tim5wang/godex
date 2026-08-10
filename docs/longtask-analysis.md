# GoDex LongTask 全面分析

## 第一部分：现状分析

### 代码规模

| 维度 | 数值 |
|------|------|
| 源文件数 | 23 个 `.go` + 6 个前端组件 + 3 个设计文档 |
| 核心代码行 | ~3000 行 Go（workflow.go 单文件 1600 行） |
| 测试文件 | 4 个（longtask_test / e2e / eval / workflow_test） |
| 设计文档 | 3 份（优化 PRD + 修复方案 v2 + workflow runtime spec） |

### 架构图

```
longtask tool (agent tool handler)
    │
    ├── create →   longTaskSpec + stories → storyCompiler → workflow nodes
    │
    ├── run →      async goroutine loop
    │               ├── foreach story (in order):
    │               │   ├── wait dependency complete
    │               │   ├── spawn subagent → wait result
    │               │   ├── validate (quality_checks)
    │               │   ├── merge (auto/review)
    │               │   ├── commit (auto/none)
    │               │   └── reflux result to chat
    │               ├── on failure → repair (append repair node + rewire)
    │               └── on cancel → cascade cancel all
    │
    ├── status →   read workflow state
    ├── wait →     wait for async run
    ├── cancel →   cancel story/node/all
    ├── finalize → validate + merge + commit one story
    └── reflux →   push result back to chat history
```

### 核心数据类型

```
longTaskSpec:
  - stories[]: {title, description, acceptance_criteria, priority, agent_type, write_scope, handoff_policy}
  - quality_checks[], merge_policy, commit_policy

workflowNode:
  - id, kind(story|repair), status, verdict
  - depends_on[], handoff_from[], prompt, agent_type, write_scope
  - handoff_policy, handoff_max_bytes

longTaskRunRecord:
  - run_id, workflow_id, status, iterations
  - started[], finalized[], repaired[]
  - async: bool, last_reflux_key
```

---

## 第二部分：为什么"不完善，不闭环，完全不可用"

### 根因 1：串行故事语义与并行 DAG 语义的错位

| 错位点 | longtask 期望 | workflow 实际 |
|--------|---------------|---------------|
| 依赖关系 | 严格的线性 chain（story1→story2→story3） | 通用的并行 DAG |
| 顺序保证 | 前一个完成才开始下一个 | 依赖就绪即可启动 |
| 失败处理 | 失败故事修复后继续 | 失败节点可能阻塞下游 |
| 资源隔离 | 每个 story 独立 sandbox | 共享 workspace，会冲突 |
| 上下文传递 | 前一个 handoff 自动注入 | 需显式配置 handoff_from |

### 根因 2：缺少多 agent 编排的核心能力

对比 Codex 的多 agent 体系：

| 缺失能力 | 现状 | 后果 |
|----------|------|------|
| **子 agent 间通信** | 只有单向 handoff | 无法做 review→fix→re-review 循环 |
| **子 agent 并行** | 强串行 | 无法利用并行加速 |
| **子 agent 动态创建** | 仅 repair 场景 | 无法根据中间结果动态调整 |
| **子 agent 合并** | 只有 merge 到 workspace | 没有结构化 diff/merge 流程 |
| **子 agent 可见性** | 跑完后 reflux 一条消息 | 运行中不可见 |

### 根因 3：长期重启后完全不可恢复

```
GoDex 进程重启后：
  1. sync.Map (longTaskAsyncRuns) 清空 → 全部 running 标记为 interrupted
  2. 正在运行的 subagent 被孤儿化
  3. 用户无法 resume 或 cancel
  4. 只能看到"interrupted"，然后手动重新创建
```

### 根因 4：上下文预算与长任务时长冲突

- 长任务可能跑数小时，上下文预算有限（~200K tokens）
- `Pinned continuation state` 在 compaction 时保留 longtask 状态
- **没有压缩中间结果** → 10 个 story 的 chain 吃掉 100K tokens
- **没有自动截断历史** → 已完成 story 的细节仍占用上下文

### 根因 5：用户体验不闭环

| 场景 | 当前行为 | 理想行为 |
|------|----------|----------|
| 创建 | 需手动写 JSON 格式 stories | 对话中描述，agent 自动解析 |
| 进度 | 只有 reflux 气泡 | 进度条、ETA、当前任务描述 |
| 恢复 | 自动 repair 失败后标记 error | 提示用户问题，提供修复选项 |
| 结果 | 需打开 Web UI 的 TaskCenter | 直接在对话中看到结果摘要 |

---

## 第三部分：业界优秀做法

### 1. Codex 的 Multi-Agent 体系

**核心工具**：

```
spawn_agent:    创建子 agent（可选 fork 上下文）
send_input:     发送消息到子 agent（可 interrupt 或 queue）
wait_agent:     等待子 agent 完成
followup_task:  给子 agent 分配新任务（不中断，排队）
close_agent:    关闭子 agent
list_agents:    列出所有活跃子 agent
```

**关键设计**：

- 每个 agent 是一个**独立线程**（thread），有自己的 session、rollout、sandbox
- 父子 agent 通过 **spawn → send_input → wait** 双向通信
- 子 agent 上下文通过 **fork** 继承父 agent（`fork_mode: FullHistory | LastNTurns`）
- 子 agent 完成结果通过 **notification** 回流到父 agent
- **进程重启后，子 agent 继续运行**，父 agent 恢复后收到通知

**设计要点**（来自 `agent_tool.rs`）：
```
spawn_agent 描述:
  "Spawned agents inherit your current model by default."
  "Omit `model` to use that preferred default."
  "Reuse the agent by send_input if you believe your assigned task
   is highly dependent on the context of a previous task."
```

> `spawn_agent` 返回 id + nickname，`wait_agent` 等待完成。不是预编译的 stories，而是**动态创建**。

### 2. Codex 的 Rollout 持久化

**Rollout** 不是简单的日志，而是**可回放、可还原、可分析的事件流**：

```
rollout-trace/src/reducer/:
  conversation.rs  — 对话还原
  thread.rs        — 多线程关系
  tool/agents.rs   — 多 agent 交互图
  compaction.rs    — 压缩还原
  inference.rs     — 推理过程
```

### 3. Codex 的 Collaboration Mode

通过 `collaboration-mode-templates` 定义 agent 行为模式：**Default**（默认）、**Plan**（先规划再执行）、**Pair Programming**（结对编程）。

对 longtask 的启发：stories 本质上就是 Plan 模式的产物，但缺少"plan → execute → review → adjust"循环。

### 4. QM 的 Orchestrator 设计

`core/orchestrator.ts` 是**单一入口**，统一处理所有 agent 执行。关键设计：

- **单一入口**：所有入口（Slack、Web、cron、API）走同一个 orchestrator
- **Harness 抽象**：不关心具体引擎，只调用 `Harness` 接口
- **阶段化**：每个 turn 分阶段（user input → security screen → harness → post-process）
- **可观测**：每个阶段都有 gap time 记录

### 5. Cursor Agent 模式

核心机制：**Plan → Execute → Review** 循环，先规划步骤，逐个执行，自我修正，用户可随时干预。

### 6. Graph Engineering

2026 年兴起的一种 agent 任务编排范式：把复杂任务建模为**有向图**，节点是原子操作，边是数据依赖和控制流。

**关键特征**：
1. DAG 作为一等公民
2. 节点可观测（每个节点有输入/输出/状态/耗时）
3. 图可编辑（运行时动态增减节点、重连边）
4. 图可回放（从任何节点重新执行）
5. 图可组合（子图嵌套、复用、共享）

**与 GoDex workflow 的关系**：
- GoDex 的 workflow 已经实现了 DAG（nodes + edges + dependencies）
- 但 workflow 的 DAG 是**预编译的**（创建时生成），不是**动态的**
- 真正的 Graph Engineering 要求 DAG 在运行时**可编辑、可回放、可组合**

---

## 第四部分：重构建议

### 全新设计：从"预编译故事链"到"动态 Agent 图"

```
当前：用户描述 → LLM 编译 stories → 串行执行 → 完成
                         ↓
目标：用户描述 → 初始规划 → 动态执行图 → 每步结果反馈 → 调整图 → 继续
```

### 核心架构

```
AgentGraph (运行时 DAG)
  ├── Node: 原子任务单元
  │   ├── kind: "llm" | "tool" | "subagent" | "user_input" | "merge"
  │   ├── input: 结构化输入（来自上游节点）
  │   ├── output: 结构化输出（传递给下游节点）
  │   ├── config: model, agent_type, timeout, retry
  │   └── status: pending | running | success | failed | skipped
  ├── Edge: 依赖关系
  │   ├── type: "data" (数据传递) | "control" (控制流)
  │   └── transform: 可选的数据转换
  ├── Dynamic: 运行时操作
  │   ├── add_node / remove_node / rewire
  │   ├── pause / resume / cancel
  │   └── checkpoint / rollback
  └── Observability:
      ├── 每个节点的 input/output 可查
      ├── 每个节点的耗时/token 消耗可查
      └── 整个图的执行路径可回放
```

### 建议的 4 个阶段

#### Phase 1：修复当前 longtask 的致命缺陷（P0）

1. **重启后恢复运行中 longtask**
   - 把 `longTaskAsyncRuns` 从 `sync.Map` 改为持久化存储
   - 启动时恢复 interrupted 状态，提供 resume 入口
   - 子 agent 的 lease 机制（参考 QM 的 `worker.ts`）

2. **修复串行 → 并行 DAG 的语义错位**
   - 明确 longtask 的语义：是串行 chain 还是 DAG
   - 如果保持串行，去掉 DAG 的复杂度
   - 如果改为 DAG，重新设计 stories 的依赖表达

3. **上下文预算管理**
   - 完成的 story 自动摘要化
   - longtask 的 handoff 结果按 token 预算截断
   - 历史 story 不保留在活跃上下文中

#### Phase 2：引入 Agent 图（P1）

1. **定义 `AgentGraph` 作为运行时核心抽象**
   - 借鉴 Codex 的 `spawn_agent` / `send_input` / `wait_agent` 模型
   - 节点类型：`llm_task`、`subagent_task`、`tool_call`、`user_input`、`merge_point`
   - 边类型：`data_dependency`、`control_flow`、`handoff`

2. **实现动态图编辑**
   - 运行时添加/删除节点
   - 运行时重连边
   - 运行时暂停/恢复/取消

3. **实现图可观测性**
   - 每个节点的输入输出可查
   - 执行路径可回放
   - 失败节点可重试

#### Phase 3：多 Agent 通信（P2）

1. **借鉴 Codex 的 spawn/send_input/wait 模型**
   - `spawn_agent`：创建子 agent，可选 fork 上下文
   - `send_input`：发送消息到子 agent，可中断或排队
   - `wait_agent`：等待子 agent 完成
   - `followup_task`：给子 agent 分配新任务

2. **实现子 agent 间的双向通信**
   - 不仅仅是单向 handoff
   - 支持 review → fix → re-review 循环

#### Phase 4：集成到 Agent 核心循环（P3）

1. **把 longtask 从独立工具整合到 Orchestrator**
   - 创建一个统一的 `Orchestrator` 负责调度所有 agent 执行
   - 主 agent 和子 agent 共享同一个执行框架
   - longtask 只是 Orchestrator 的一个调度策略

2. **自然语言创建 longtask**
   - 用户在对话中描述任务
   - agent 自动拆解成 stories/graph
   - 用户确认后开始执行

### 优先级矩阵

| 编号 | 项目 | 难度 | 收益 | 依赖 | 建议顺序 |
|------|------|------|------|------|----------|
| P1-1 | 重启后恢复运行中 longtask | 中 | 高 | 无 | 1 |
| P1-2 | 串行/并行语义明确化 | 中 | 高 | 无 | 2 |
| P1-3 | 上下文预算管理 | 低 | 中 | 无 | 3 |
| P2-1 | AgentGraph 核心抽象 | 高 | 高 | P1-2 | 4 |
| P2-2 | 动态图编辑 | 高 | 高 | P2-1 | 5 |
| P2-3 | 图可观测性 | 中 | 中 | P2-1 | 6 |
| P3-1 | 多 agent 双向通信 | 高 | 高 | P2-1 | 7 |
| P3-2 | spawn/send_input/wait | 中 | 高 | P3-1 | 8 |
| P4-1 | Orchestrator 整合 | 高 | 高 | P2+P3 | 9 |
| P4-2 | 自然语言创建 | 中 | 中 | P4-1 | 10 |

### 关键参考文件

| 参考 | 文件 | 核心亮点 |
|------|------|----------|
| Codex spawn_agent | `temp/codex/codex-rs/tools/src/agent_tool.rs` | 工具定义，双向通信模型 |
| Codex spawn handler | `temp/codex/codex-rs/core/src/tools/handlers/multi_agents/spawn.rs` | spawn 实现，fork 上下文 |
| Codex agent control | `temp/codex/codex-rs/core/src/agent/control.rs` | AgentControl 注册表，spawn 内部逻辑 |
| Codex multi-agent v2 | `temp/codex/codex-rs/core/src/tools/handlers/multi_agents_v2.rs` | 新版多 agent 工具表面 |
| Codex rollout trace | `temp/codex/codex-rs/rollout-trace/src/reducer/tool/agents.rs` | 多 agent 交互图追踪 |
| Codex collaboration mode | `temp/codex/codex-rs/collaboration-mode-templates/templates/` | Plan/Default/Pair 模式 |
| QM orchestrator | `temp/qm/src/core/orchestrator.ts` | 单一入口编排 |
| QM worker lease | `temp/qm/src/runs/worker.ts` | 心跳保活 + lease 恢复 |
| QM idempotency | `temp/qm/src/idempotency/idempotency-store.ts` | 幂等性保证 |
| GoDex workflow | `internal/agent/workflow.go` | 已有的 DAG 基础设施 |
| GoDex longtask run | `internal/agent/longtask_run.go` | 当前的 run 循环 |