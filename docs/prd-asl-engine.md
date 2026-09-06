# PRD: ASL（Agent 流程固化语言/引擎）

> 日期：2026-09-05 ｜ 状态：Draft（调研完成，待评审）
> 任务卡：t-1788526734340-1 ｜ 类型：调研 + 方案设计（不包含实现）
> 一句话：以 godex 为基座，深入融入业务场景，用声明式流程把高频执行路径固化下来，降低 LLM 参与度；LLM 只在意外分支接收流程并完善流程，实现自诊断。

---

## 1. 背景

### 1.1 问题

godex 目前是「LLM 全程参与」的执行模型：每个任务无论多固定，都会走一次完整的 agent turn（模型推理 + 工具调用）。带来的问题：

- **成本与延迟**：高频、可预知路径（如「解析单据 → 校验 → 调 MCP 更新 → 回执」）每次都要 LLM 推理，token 与耗时被浪费。
- **不确定性**：同一流程每次执行结果可能漂移（模型采样、上下文差异），难以满足业务场景对「同一输入 → 同一路径 → 同一结果」的固化要求。
- **不可审计/不可重放**：执行路径由模型自由探索决定，流程版本、输入输出 schema、失败重试策略没有声明式描述，审计与问题定位依赖翻会话记录。
- **意外分支处理无结构**：异常时模型自行「临场发挥」，修复逻辑没有沉淀回流程定义，同类错误反复消耗 LLM。

### 1.2 机会（godex 已有基础设施，均已核实）

| 设施 | 位置 | 对 ASL 的意义 |
|---|---|---|
| Durable Workflow Runtime | `internal/agent/workflow.go`（workflowStore / workflowNode / workflowEdge / handoff artifact / verdict / 重启恢复） | 已有「持久化 DAG + 条件分支（when{status,verdict}）+ 迭代上限 + 幂等恢复」骨架，ASL 引擎可复用其存储与恢复模式 |
| LongTask 层 | `internal/agent/longtask.go` + `internal/app/longtask.go` | stories→顺序节点、quality_checks 确定性验证、run 循环，是「固化执行」的雏形 |
| Harness 抽象 | `internal/agent/harness.go`（Phase 5.1/6.4 多引擎热切换，每轮选引擎） | ASL 引擎可作为独立 Harness（`flow harness`）注册，与默认 `godexHarness`（LLM 引擎）并行共存 |
| Agent Step Platform | `docs/agent-step-platform-design.md`（Phase A 已实现：`/v1/agent-steps` + `biz_` key + MCP 双轨） | 业务系统接入点；「单环节」升级为「多环节固化流程」是其自然演进 |
| MCP 工具注册 | `internal/core/mcp/` + `toolruntime.RegisterOwned` | 流程 task 步骤的动作执行器（业务侧执行） |
| 其他 | `internal/domain/automation/`（cron/heartbeat/DeliveryTarget）、taskboard 插件、TurnError 分层（5.2） | flow 触发源、执行会话绑定、错误分类经验 |

结论：godex 已有「把业务动作固化为可重放流程」的全部运行底座，缺的是一层**声明式流程语言（DSL）+ 确定性执行引擎 + LLM 意外分支接管/自诊断闭环**。

### 1.3 范围

- 本次交付：**调研 + PRD**（对标分析、ASL 适用性、集成架构、推荐架构、实现代价、风险、验收标准）。不含实现代码与流程编辑器 UI。
- 边界：以 godex 为基座、以业务场景（MCP 工具 + 沙箱工具可混合执行）为目标形态；不重新发明分布式工作流（Temporal 级别的跨机重试/续跑由 godex 常驻进程 + 既有 workflow runtime 承担）。

---

## 2. 目标

### 2.1 目标（Outcome）

| # | 目标 | 衡量 |
|---|---|---|
| G1 | 高频路径固化：声明式流程定义，确定性执行 | 同一流程同一输入执行路径 100% 一致；无 `llm` 状态的纯确定性流程 0 token |
| G2 | 降低 LLM 参与度 | 常规步骤不耗 LLM；LLM 只在「意外分支」与「流程完善」被唤起 |
| G3 | 意外分支接管 + 自诊断 | 未匹配分支/步骤失败 → LLM 接管诊断并给出修复；修复可回写为流程定义 patch |
| G4 | 业务融入 | 流程步骤可直接调用 MCP 业务工具与沙箱工具（双轨混合，与 Agent Step Platform 一致） |
| G5 | 可审计可重放 | 每次执行有结构化 trace（步骤/输入/输出/耗时/异常）；可按版本重放 |

### 2.2 非目标

- 不做分布式/跨机工作流（godex 常驻进程内执行）。
- 不做可视化流程编辑器（本期）；DSL 文件 + 调试视图优先。
- 不强制所有任务走 ASL：LLM 自由模式保留，ASL 是「固化层」，两种模式可混用（每轮 harness 选择）。

### 2.3 术语

- **ASL**：本 PRD 指「Agent 流程固化语言」，对标 Amazon States Language 的声明式状态机思想，但为 godex 场景裁剪定制（见 §5/§6）。
- **Flow**：一份流程定义（`flow.yaml`，版本化）。
- **Step/State**：flow 内的执行单元（确定性 task / LLM 步骤 / choice 分支 / 子流程）。
- **Run**：flow 的一次实例化执行（有结构化 trace，可重放）。

---

## 3. godex 现状与差距

### 3.1 可复用设施

见 §1.2 表。核心结论：存储（workflowStore）、条件边、verdict、重启恢复、harness 路由、MCP 双轨工具、biz key 认证均可直接复用。

### 3.2 差距（ASL 需要补齐的）

1. **无声明式 DSL**：workflow 是 JSON 图结构（prompt 模板 + 条件边），不是可编译/可校验/可版本化的流程语言；条件仅限 `status/verdict`。
2. **无确定性步骤**：node 一律执行 subagent job（LLM 全程参与），不能执行纯确定性步骤（脚本 / MCP 调用 / HTTP / 数据变换）而零 LLM 消耗。
3. **无结构化数据流**：步骤间只有文本 handoff，没有类型化的输入/输出 schema 与状态传递（无法做数据驱动的分支）。
4. **无「LLM 接管 → 回写流程」闭环**：失败时模型临场发挥，修复不沉淀回流程定义 → 自诊断机制缺失。
5. **无流程版本管理/上线回滚**。
6. **无流程级 run trace 视图**（虽有 events.jsonl 与 handoff，但非按 run 组织的步骤级 trace）。

---

## 4. 对标分析（工作流/流程编排引擎）

> 本节基于独立联网调研（8 个引擎 + ASL 规范专题，42 个来源链接；完整报告见 `~/.godex/tmp/asl-research-report.md` 存档）。

### 4.1 各引擎要点

**Temporal（Workflow-as-Code，事件溯源）**
- 固化：流程用普通语言写 Workflow 函数（无独立 DSL）；代码必须确定性，执行靠 Event History 回放恢复；Continue-As-New 控制历史膨胀。
- 异常：Activity 默认声明式 RetryPolicy（指数退避 + 不重试错误白名单）；四类超时 + 心跳断点续传；Saga 逆序补偿（幂等）；Signal/Update 外部介入。
- LLM 协作：官方强制 LLM 调用放 Activity（非确定性操作隔离），获得重试/超时/心跳。
- 调试：Web UI 事件时间线、本地 replay 测试。
- 启示：**确定性编排 + 副作用隔离是可靠恢复的前提**；LLM 是"最不可靠的 Activity"，必须内置超时/有限重试/输出校验。

**n8n（可视化节点流 + workflow JSON）**
- 固化：画布节点流，workflow JSON 可导出/参数化加载；执行记录持久化（Executions）。
- 异常：Error Workflow（工作流级错误处理器，首节点 Error Trigger，含 execution id/错误栈/lastNodeExecuted）；Stop and Error 主动失败；条件节点分支 + continue-on-fail + 重试；无内建 Saga。
- LLM 协作：AI Agent/LLM 节点即插即用，走统一节点错误处理。
- 调试：Executions 逐步查看每节点输入输出、可加载历史数据回画布、日志流。
- 启示：**可观测 = 可恢复**；执行 ID + 逐步 I/O 审计是运维兜底。

**LangGraph（代码即图 + checkpointer）**
- 固化：StateGraph 有向图（节点函数 + 普通边/条件边 + reducer 状态合并）；checkpointer 按 thread_id 持久化状态快照（故障恢复、时间旅行）；store 跨线程长期记忆。
- 异常：代码级 try/except + 条件边进恢复路径；`interrupt()` 任意节点暂停等待外部输入（HITL），checkpointer 落盘、跨进程恢复，resume 值成为 interrupt 返回值；恢复时整节点重放、前置副作用必须幂等。
- LLM 协作：原生 agent 图，LLM 不确定性用条件路由/校验节点/HITL 收敛。
- 调试：LangSmith/Studio 时间旅行重放、graph.stream_events。
- 启示：**人工介入是一等公民**——可暂停点（标记 → 落盘 → 外部输入 → 同 thread 恢复，前置副作用幂等）是 agent 审批/纠偏的最佳参照。

**Dify（可视化编排 + DSL + 错误三态）**
- 固化：画布拖拽节点（LLM/知识检索/工具/代码/HTTP/If-Else/迭代/Agent 等），应用以结构化 DSL 保存可导出导入；Workflow 与 Chatflow 两种形态。
- 异常：节点三态开箱即用——Fail（即停）/ Default Value（用备份默认值继续）/ Fail Branch（走错误分支）；错误变量 `error_type`/`error_message`；Iteration 三模式（terminated / continue-on-error / remove-abnormal-output）。
- LLM 协作：Question Classifier 用 LLM 意图路由、Parameter Extractor 抽参、Agent 节点作为工作流一步；LLM 失败用 default value 降级或 fail branch 改道——**这是它对模型不确定性的主要应答方式**。
- 调试：单节点 step run、运行日志、画布内看节点输出。
- 启示：**迭代/循环内的错误语义要显式**（terminated / continue-on-error / remove-abnormal-output 三模式值得 godex 采纳）。

**AWS Step Functions / ASL（声明式状态机）**
- 固化：ASL（JSON/YAML）定义状态机，服务端记录完整执行历史（可审计可回放）；8 类状态（Pass/Task/Choice/Wait/Succeed/Fail/Parallel/Map）；Standard（最长 1 年，精确一次）vs Express（最长 5 分钟，至少一次，需幂等）。
- 异常：内置错误名 States.*（Timeout/TaskFailed/Runtime/NoChoiceMatched/ExceedToleratedFailureThreshold 等）+ States.ALL 通配；**Retry retrier**（ErrorEquals + IntervalSeconds + MaxAttempts 默认 3 + BackoffRate 2.0 + JitterStrategy）先自动重试，**Catch catcher**（ErrorEquals + Next 降级分支 + ResultPath 错误写回）重试耗尽后走降级；未处理错误默认整执行失败；redrive 从失败点重启。
- LLM 协作：无内建 LLM，Task 调 Lambda/Bedrock/HTTP，模型调用视为普通 Task 用 Retry/Catch 收敛。
- 调试：控制台执行图、逐步 I/O、90 天历史、redrive。
- 启示：**异常分层三件套**——自动重试 → 降级分支 → 补偿，是 ASL 的核心资产。

**Airflow / Argo（批处理与 K8s 生态）**：Python DAG 动态生成 + retries/ExceptionRetryPolicy/Trigger Rules；YAML CRD + retryStrategy/onExit/failFast。适合批处理/CI/CD，与 LLM 协作弱，仅作参照。

**CrewAI Flows / ElizaOS / AutoGen（agent 框架）**：事件驱动 + 装饰器路由、state 持久化、无内建重试/补偿 DSL，编排能力弱于专职引擎；流程固化以「会话/事件 + 代码编排」为主，声明式程度低。仅列方向。

### 4.2 对比总结表

| 引擎 | 流程固化方式 | 异常处理 | LLM 协作 | 可调试性 | 适用场景 |
| --- | --- | --- | --- | --- | --- |
| Temporal | 代码即定义（确定性 + Event History 回放） | 声明式 RetryPolicy 默认重试；超时/心跳；Saga 补偿；Signal/Update | LLM 调用必须放 Activity | Web UI 事件时间线、本地 replay 测试 | 长时强一致、需精确恢复的关键业务与 AI 编排 |
| n8n | 可视化节点流 + workflow JSON | Error Workflow + Stop/Error + 条件分支；无 Saga | AI/LLM 节点即插即用 | Executions 逐步 I/O、日志流 | SaaS 集成自动化、非开发者可视化流程 |
| LangGraph | 代码即图 + checkpointer 快照 | 代码级 try/except + 条件边；interrupt() HITL 暂停/恢复 | 原生 LLM agent 图 | LangSmith/Studio 时间旅行 | LLM agent/多智能体、细粒度路由与人工审批 |
| Dify | 可视化画布 + DSL 保存 | 节点三态（Fail/Default Value/Fail Branch）+ error_type/message；Iteration 三模式 | LLM 节点为核心，降级默认值/改道 | 单节点 step run、运行日志 | RAG/对话 AI、低代码 AI 工作流 |
| AWS Step Functions | 声明式 ASL（JSON/YAML 状态机，8 类状态） | Retry retrier + Catch catcher + States.ALL；嵌套子机捕获 | Task 调 Lambda/Bedrock，Retry/Catch 容错 | 控制台执行图、逐步 I/O、90 天历史、redrive | AWS 生态业务编排、高审计、无服务器 |
| Airflow | Python DAG（代码即定义） | retries + ExceptionRetryPolicy + Trigger Rules | PythonOperator 接模型 | Web UI Grid/Graph/日志 | 批处理、ETL、定时调度 |
| Argo | YAML CRD（K8s 原生） | retryStrategy + onExit + failFast | 容器模板跑模型 | Argo UI DAG/日志 | K8s CI/CD、ML 数据流水线 |
| CrewAI Flows | Python 装饰器事件流 + state | 代码级异常为主，无重试/补偿 DSL | agent/crew 作步骤 | plot() 图、状态快照 fork | 多 agent 协作原型 |

### 4.3 对 godex 的关键启示

1. **确定性编排 + 副作用隔离**（Temporal）：核心执行路径可重放，非确定性操作（LLM、工具副作用、IO）收口到带重试边界的步骤层，步骤输入输出 append-only 持久化。
2. **异常分层三件套**（ASL/Dify）：a) 自动重试（声明式 RetryPolicy：指数退避 + 最大次数 + 不重试错误清单）；b) 重试耗尽走降级分支（Catch/Fail Branch/Default Value）；c) 多步副作用用幂等 Saga 补偿。
3. **LLM 是"最不可靠的步骤"**：模型调用必须内置超时、有限重试、输出 schema 校验与「不合格输出重试一次」机制，暴露 `error_type/error_message` 给上层分支决策。
4. **人工介入是一等公民**（LangGraph）：可暂停点（标记 → 落盘 → 外部输入 → 同 thread 恢复，前置副作用幂等）。
5. **可观测 = 可恢复**（n8n/Step Functions）：统一 run/execution ID + 逐步 I/O 审计 + redrive/重试已失败执行。
6. **声明式 vs 代码式取决于受众**：面向 godex 开发者用户，宜「代码/DSL 一等公民 + 可导出 JSON/YAML 可审计形态」，流程可版本化、可 lint、可 code review。
7. **迭代/循环错误语义要显式**（Dify）：terminated / continue-on-error / remove-abnormal-output 三模式作为标准选项。

---

## 5. ASL 语言（Amazon States Language / 自定义 DSL）适用性分析

### 5.1 直接采用 Amazon States Language 的评估

| 维度 | 评估 |
|---|---|
| 表达力 | 8 类状态覆盖线性/分支/并行/迭代/等待/终止；**无循环原语、无任意编程逻辑**（复杂度靠 Lambda 外置）。对「业务固化流程」够用，对「agent 内省/自诊断」不足 |
| 数据流 | JSONPath（2024 起可选 JSONata）+ 五段式（InputPath/Parameters/ResultSelector/ResultPath/OutputPath）表达力强但**学习成本高、可读性差**（JSONPath 引用路径 + Intrinsic Functions 组合常写成"魔术字符串"） |
| 可调试性 | 规范公开（states-language.net）、有 asl-validator npm 校验器、Step Functions Local 本地运行；但无独立本地编译器，复杂数据整形难 debug |
| 学习成本 | 状态机思维 + JSONPath 语义 + 错误匹配矩阵；简单线性流程数分钟上手，复杂流程陡峭 |
| 开源实现 | 规范开源但 **AWS 执行引擎闭源**；同类开放规范 Serverless Workflow（CNCF Sandbox，JSON/YAML DSL + 事件驱动 + Conformance Test Kit + TypeScript SDK）可作参照 |

**结论**：直接采用 ASL 会带来 JSONPath 复杂度与闭源执行引擎依赖，且其「Task = 无差别工作单元」模型没有「LLM 步骤」的一等地位，与 godex 的「LLM 意外分支接管」目标不匹配。**采用 ASL 的状态机思想（State + Retry/Catch + 数据流）但定制为 godex 自己的 YAML DSL** 是更优解。

### 5.2 自定义 DSL 设计要点（本 PRD 推荐）

- **形态**：YAML 文件（人类可写、可注释、可 lint、可 code review、可版本化），单文件 = 单 flow。
- **状态类型（裁剪自 ASL 8 态 + 扩展）**：
  - `task`：确定性工作单元（MCP 工具 / 沙箱工具 / HTTP / 脚本 / 数据变换）——零 LLM
  - `llm`：显式 LLM 步骤（有输入 schema、输出 schema 校验、超时、有限重试）——LLM 参与点显式声明
  - `choice`：数据驱动条件路由（基于类型化 step 输出，无匹配走 `unexpected` 接管）
  - `wait`、`parallel`、`foreach`（Map）、`succeed`、`fail`、`subflow`（子流程复用）
  - 隐式终态：`unexpected`（意外分支接管点，见 §6.3）
- **数据流**：不用完整 JSONPath，用简单引用路径 `$.a.b` + 每步 JSON Schema 校验输入/输出；state 即类型化上下文（替代现有文本 handoff）。
- **错误处理**：每步可选 `retry`（ErrorEquals/Interval/BackoffRate/MaxAttempts/NonRetryable）+ `catch`（ErrorEquals → 降级分支 / `default` 值 / 走 `unexpected`），对标 ASL retrier/catcher。
- **学习成本**：状态机思维保留（这是固化流程的本质），但去掉 JSONPath 与五段式数据整形（用 schema 校验 + 简单 path），显著降低门槛。

---

## 6. 方案设计（固化流程 + LLM 意外分支接管 + 自诊断）

### 6.1 总体架构（三层）

```
┌─────────────────────────────────────────────────────────────┐
│ Layer 1  Flow DSL（flow.yaml）：状态 + 边 + retry/catch + schema │
├─────────────────────────────────────────────────────────────┤
│ Layer 2  Flow Harness（确定性执行引擎，注册进 harnessRouter）      │
│          ├─ 步骤执行器：task(MCP/沙箱/HTTP/脚本) / llm / subflow │
│          ├─ 状态机解释器：choice 路由、parallel/foreach、wait     │
│          ├─ 数据流：类型化 step I/O + JSON Schema 校验            │
│          ├─ 错误处理：retry(声明式) → catch(降级分支) → unexpected │
│          ├─ HITL 暂停点：标记 → 落盘 → 外部输入 → 同 run 恢复      │
│          └─ trace：append-only 步骤级记录（复用 workflowStore 模式）│
├─────────────────────────────────────────────────────────────┤
│ Layer 3  LLM 意外分支接管 + 自诊断（只在 unexpected 被唤起）        │
│          ├─ 接管协议：run trace + flow 定义 + 失败上下文 → LLM     │
│          ├─ 诊断输出：根因分类 + 修复建议 + 流程完善 patch(draft)   │
│          ├─ 自诊断沉淀：error_type 分类统计 → 同类错误固化为 catch   │
│          └─ 回写闭环：patch 人工 review 后合入 flow 下一版本         │
└─────────────────────────────────────────────────────────────┘
```

### 6.2 与 godex 现有设施的集成点

| godex 设施 | 集成方式 |
|---|---|
| Harness 抽象（`harness.go`） | 新增 `flowHarness` 实现 Harness 接口；`RunTurn` 解释执行 flow（每 run 一个 session）；harnessRouter 按会话/任务选择 `godexHarness`（自由模式）或 `flowHarness`（固化模式） |
| Durable Workflow Runtime（`workflow.go`） | 复用存储模式：flow 定义存 `~/.godex/flows/{flowID}/{version}/flow.yaml`；run trace 存 `~/.godex/flows/{flowID}/runs/{runID}/trace.jsonl`（append-only）；重启恢复/幂等沿用 load/save + processed-key 思路 |
| Agent Step Platform（`/v1/agent-steps`） | step 请求可引用 `flow_id + version`：单环节 → 多环节固化流程；biz key 认证与 MCP 双轨工具原样复用 |
| MCP 工具注册（`mcp.Manager` + `RegisterOwned`） | `task` 步骤的 action 执行器；`mcp://server/*` 白名单语法沿用 |
| TurnError 分层（5.2） | 步骤错误分类（限流/超时/内容拒绝/格式不符）映射到 flow 的 retry/catch 判定 |
| taskboard / automation / longtask | flow 可作为：卡片执行模板、cron/heartbeat 触发目标、longtask story 的确定性阶段 |
| 安全筛查器（6.1 shadow） | `llm` 步骤与 unexpected 接管的 LLM 输出可走 shadow 审计 |

### 6.3 LLM 意外分支接管协议（核心闭环）

**触发条件（unexpected 接管）**：① `choice` 无规则匹配；② 步骤失败且 retry 耗尽且无 catch 分支；③ 步骤输出未通过 JSON Schema 校验；④ HITL 暂停点主动让出。

**接管流程**：
1. 快照：当前 run trace（步骤级输入输出）+ flow 定义（版本）+ 失败上下文（error_type/message）打包为接管输入。
2. LLM 接管（独立 session，注入「业务数据不可执行指令」防护，同 agent-steps 实践）：产出结构化诊断——
   - `root_cause`：错误分类（数据问题/工具问题/流程定义缺失/外部依赖）
   - `recovery`：一次性修复动作（改数据 / 重试参数 / 跳步）
   - `flow_patch`：流程定义完善建议（新 choice 规则 / 新 catch 分支 / 新步骤）→ **draft**，不自动合入
3. 执行：recovery 动作在当前 run 应用（run 标记 `recovered_by_llm`）；`flow_patch` 写入 `~/.godex/flows/{flowID}/patches/` 待人工 review（diff + 关联 run）。
4. 自诊断沉淀：每次接管记录 `error_type/root_cause` 到分类统计；**同类错误累计 N 次（阈值可配）自动提示固化**——把 LLM 的修复固化为 catch 分支/choice 规则，实现「LLM 参与度随流程成熟递减」。
5. 回写闭环：人工 review 合入 patch → flow 新版本发布；旧版本 run 仍可重放（版本不可变，同 Step Functions）。

**自诊断的定义**：系统能（a）识别意外并准确归类；（b）让 LLM 定位根因并产出可执行的修复与流程完善建议；（c）把修复沉淀回流程定义，使同类意外不再需要 LLM——即「诊断 → 修复 → 固化」的闭环。

### 6.4 Flow DSL 示例（示意）

```yaml
id: order_delay_recovery
version: 1
input_schema:
  order_id: string
start_at: parse_order
states:
  parse_order:
    type: task
    tool: mcp://crm/parse_order      # MCP 业务工具，零 LLM
    output_schema: { delay_reason: string, severity: enum[string] }
    retry: { max_attempts: 3, backoff_rate: 2.0, non_retryable: [InvalidOrderId] }
    catch: { InvalidOrderId: "notify_human" }   # 降级分支
    next: classify
  classify:
    type: choice                     # 数据驱动路由，无匹配 → unexpected
    rules:
      - when: $.severity == "critical" → llm_recovery
      - when: $.severity == "minor"   → auto_fix
    default: unexpected
  auto_fix:
    type: task
    tool: sandbox:bash               # 沙箱通用工具
    command: "apply_fix.sh {order_id}"
    next: succeed
  llm_recovery:
    type: llm                        # 显式 LLM 步骤（有 schema/超时/重试）
    prompt: "分析订单 {order_id} 延迟根因并给出恢复方案"
    output_schema: { plan: string, eta: string }
    retry: { max_attempts: 2, timeout: 120s }
    next: succeed
  unexpected:                        # 隐式终态：LLM 接管点（协议见 §6.3）
    type: takeover
  notify_human:
    type: task
    tool: mcp://notify/send
    next: fail
```

### 6.5 与既有 Workflow Runtime 的边界（防混淆）

| 维度 | 既有 workflow runtime / longtask | ASL flow harness |
|---|---|---|
| 定义 | JSON 图 + prompt 模板（LLM 子代理驱动节点） | YAML 声明式 DSL（确定性引擎解释） |
| 执行单元 | 每个 node = durable subagent job（LLM 全程） | task 确定性执行 / llm 显式步骤 / 意外分支才接管 LLM |
| 数据流 | 文本 handoff（摘要） | 类型化 step I/O + schema 校验 |
| 适用 | 探索型/研究型/不可预知任务（longtask、深研） | 高频可预知业务路径（固化层） |
| 关系 | 可互为边界：flow 的 `unexpected` 可升级为 longtask 子任务；longtask 的确定性阶段可下沉为 flow | |

---

## 7. 推荐架构

### 7.1 分期落地（M1-M3）

**M1 核心闭环（确定性固化，约 2-3 人周）**
- Flow DSL 最小状态集：`task / choice / wait / succeed / fail / parallel / foreach`
- Flow Harness 注册进 harnessRouter；确定性步骤执行器（MCP + 沙箱 + HTTP + 脚本）
- 类型化 step I/O + JSON Schema 校验 + 声明式 retry/catch
- run trace（append-only JSONL）+ 重启恢复 + 幂等
- 支持从 `/v1/agent-steps` 引用 flow_id 执行（单步 → 多环节验证）
- **验收演示**：把一个真实业务路径（如订单延迟分析或 taskboard 卡片固化流程）写成 flow.yaml，纯确定性执行 0 LLM token

**M2 LLM 接管 + 自诊断（约 2 人周）**
- `unexpected` 接管协议（触发条件 + 快照打包 + LLM 诊断输出 schema）
- recovery 应用（run 恢复）+ flow_patch draft（人工 review 合入）
- 错误分类统计 + 同类错误固化提示（阈值触发）
- HITL 暂停点（标记 → 落盘 → 恢复，前置副作用幂等）
- **验收演示**：注入意外（choice 无匹配 / 步骤失败）→ LLM 接管诊断 → run 恢复或 patch draft；同类错误二次出现不再触发 LLM

**M3 工程化（约 2-3 人周）**
- flow 版本管理（发布/回滚，版本不可变）+ patches 管理
- 调试视图（Web UI：flow 图 + run trace 步骤级 I/O，复用 AgentGraphDiagram 组件）
- subflow 复用 + DSL lint/校验器 + i18n
- taskboard 卡片绑定 flow、cron/heartbeat 触发 flow

### 7.2 关键设计决策

| # | 决策 | 选择 | 理由 |
|---|---|---|---|
| D1 | DSL 形态 | 定制 YAML DSL（ASL 状态机思想，非 ASL 语法） | 避开 JSONPath 复杂度与闭源引擎；LLM 步骤一等公民 |
| D2 | 执行模型 | 独立 Harness（flowHarness）+ harnessRouter | 复用 6.4 多引擎机制，自由模式与固化模式可混用 |
| D3 | 意外分支处理 | 重试 → catch 降级 → unexpected(LLM 接管) 三层 | 对标 ASL retrier/catcher + Dify 错误三态 |
| D4 | LLM 修复回写 | patch = draft + 人工 review + 版本化合入 | 防 LLM 改流程定义的失控；流程即代码需 review |
| D5 | 存储 | 复用 workflowStore 模式（`~/.godex/flows/...`） | 一致性、重启恢复、幂等零新造 |
| D6 | 数据流 | 简单 path `$.a.b` + JSON Schema 校验 | 表达力够用且可读，拒绝 JSONPath 全套 |

### 7.3 架构收益

- **Token 成本**：高频路径从「每任务 LLM 全程」降到「仅意外分支」，成本可降一个数量级（视固化比例）。
- **确定性**：固化路径执行序列 100% 一致，审计/重放/测试成为可能。
- **自诊断**：意外不再临场发挥，而是「诊断 → 修复 → 固化」闭环，LLM 参与度随时间递减。
- **业务融入**：直接承接 Agent Step Platform 的 biz key + MCP 双轨，从单环节平滑升级到多环节流程。

---

## 8. 实现代价

| 项 | M1 | M2 | M3 | 合计 |
|---|---|---|---|---|
| 人力 | 2-3 人周 | 2 人周 | 2-3 人周 | **6-8 人周** |
| 复杂度 | 中（DSL 解释器 + 执行器 + 存储） | 中高（接管协议 + 回写闭环） | 中（版本管理 + UI） | 中（整体可控，无分布式依赖） |
| 新增代码面 | `internal/flow/`（dsl.go/executor.go/store.go） + harness 接入 + `routes_steps.go` 扩展 | `internal/flow/takeover.go` + 诊断 schema + patch 存储 | 版本管理 + `ui/web` 调试视图 | 集中在 `internal/flow/`，不侵入 agent 主循环 |
| 风险 | DSL 设计过度或与业务不匹配 | 接管安全性（LLM 改流程） | UI 范围膨胀 | 见 §9 |

**主要工作量分布**：确定性执行引擎与存储（~40%）、DSL 定义与校验（~15%）、接管协议与自诊断（~25%）、测试与演示流程（~20%）。

---

## 9. 风险与对策

| # | 风险 | 影响 | 对策 |
|---|---|---|---|
| R1 | DSL 设计过度/不匹配真实业务 | 返工 | 先接 1-2 个真实业务流程验证再扩状态集；最小状态集起步 |
| R2 | 确定性步骤与 LLM 步骤边界模糊 | 成本目标落空 | `llm` 状态显式声明，其余一律确定性；trace 标记每步是否耗 LLM |
| R3 | LLM 接管后 flow_patch 失控（改坏流程） | 流程质量下降 | patch 一律 draft + 人工 review + 版本化合入；patch 带关联 run 可回滚 |
| R4 | 与既有 workflow runtime 混淆/重复建设 | 维护成本 | 明确边界（§6.5），两者通过 unexpected 升级 / 确定性阶段下沉互操作 |
| R5 | 状态持久化与重启恢复遗漏 | run 丢失 | 复用 workflowStore 的 load/save/processed-key 模式；trace append-only |
| R6 | HITL 暂停点副作用重复执行 | 数据错误 | 恢复时整步骤重放 + 前置副作用幂等（LangGraph 约束） |
| R7 | 意外分支接管拖慢 run（LLM 延迟） | 体验下降 | 接管设置超时；非关键路径可先降级失败再异步诊断 |
| R8 | JSONPath 替代（简单 path）表达力不足 | 部分流程写不出 | 预留 `expr` 逃生舱（步骤内嵌脚本），复杂逻辑外置 |

---

## 10. 验收标准

### 10.1 调研交付验收（本任务）

- [x] 对标调研 >=3 个引擎（实际 8 个：Temporal/n8n/LangGraph/Dify/AWS Step Functions + Airflow/Argo/CrewAI），回答「如何固化流程、如何处理异常分支」（§4，来源见调研存档）
- [x] ASL 语言适用性分析（表达力/可调试性/学习成本/开源实现，§5）
- [x] 与 godex 集成架构（固化流程 + LLM 意外分支接管 + 自诊断，§6，含 godex 现状/差距核实）
- [x] 推荐架构 + 实现代价（§7/§8，含风险 §9）
- [x] PRD 格式完整（背景/目标/对标分析/方案设计/推荐架构/实现代价/风险/验收标准）

### 10.2 后续实现验收（供实施阶段使用）

1. **确定性**：同一 flow 同一输入（含版本）执行序列 100% 一致；无 `llm` 状态的流程 run 的 LLM token 消耗为 0。
2. **固化**：一个真实业务路径可用 flow.yaml 定义并经 `/v1/agent-steps`（引用 flow_id）或对话触发执行。
3. **接管**：构造意外（choice 无匹配 / 步骤失败重试耗尽）→ LLM 接管产出结构化诊断（root_cause/recovery/flow_patch）；run 可恢复或生成 patch draft。
4. **自诊断**：同类错误被 LLM 修复并固化（patch 合入）后，二次出现走确定性 catch 分支，不再消耗 LLM。
5. **审计重放**：run trace 完整（每步 I/O/耗时/异常/是否耗 LLM）；旧版本 run 可重放；重启后未完成 run 可恢复。
6. **回归**：`go test ./...`、`tsc -b`、`vite build` 全绿，无新增失败。

---

## 附：参考来源（调研存档）

- 完整调研报告（42 来源）：`~/.godex/tmp/asl-research-report.md`（subagent worktree 内，正文 §4 已浓缩）
- 关键来源：Temporal docs（workflow-definition / retry-policies / saga-pattern）、n8n docs（handle-errors-gracefully / execute-workflow）、LangGraph docs（graph-api / persistence / human-in-the-loop）、Dify docs（predefined-error-handling-logic / error-type）、AWS Step Functions（concepts-error-handling / standard-vs-express）、[ASL 规范 states-language.net](https://states-language.net/spec.html)、[asl-validator](https://www.npmjs.com/package/asl-validator)、[Serverless Workflow 规范](https://github.com/serverlessworkflow/specification)、Airflow/Argo/CrewAI 官方文档
