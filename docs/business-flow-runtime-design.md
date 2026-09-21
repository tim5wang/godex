# 业务流程运行时（Business Flow Runtime）与 Flow Spec v1 设计

> 状态：Partial（F0 内核原语已落地并测试，见 §13；F1–F4 未实现）
> 关联：
>
> `business-agents-console-design.md`
>
> （业务智能体管理台）、
>
> `agent-step-platform-design.md`
>
> （单环节平台，Phase D+ 的流程复用在本文落地）、
>
> `workflow-runtime.md`
>
> （durable DAG 引擎现状）、
>
> `agent-role-and-bundle-design.md`
>
> （模板 / 能力边界）
> 日期：2026-09-21
> 决策记录：2026-09-21 已拍板 §12 中 5 个开放问题（providers 复用、阈值归属、64 上限、计量方式、灰度语义）。

本文定义 business-agents 板块的**在线流程编排层**：一套可被 FlowGram 画布编辑、版本化、绑定业务智能体（biz key/template）的声明式流程规范（Flow Spec v1），以及它如何**编译到现有 durable workflow 引擎**（`internal/agent/workflow.go` + `agentgraph.go`）执行，不重写调度内核。

## 0. 一句话与范围



* **一句话**：FlowGram 产出 Flow Spec（版本化 JSON）→ 后端编译为现有 `workflowNodeInput`/`workflowEdgeInput` 跑在 durable 引擎上；新增三类节点（`decision`/`human`/`branch`+`loop` 语义）、节点级 `RetryPolicy`、扩展条件（含 confidence）与流程版本化。

* **v1 范围**：Flow 定义 CRUD + 版本化（草稿 / 灰度 / 发布）、节点联合、边条件扩展、RetryPolicy、Flow Run 实例与 API、FlowGram JSON 双向编译契约。

* **非目标（v1 不做）**：不替换自研引擎为 Temporal 等外部编排器；不做跨节点分布式调度（Node Mesh 服务化后另议）；不做任意代码表达式条件（只允许针对已声明输出 schema 的结构化谓词）；不做流程挖掘 / 自动沉淀闭环（F4/WP6，另文）；不做按比例流量灰度（v1 仅白名单）。

## 1. 背景：现有引擎事实（代码核对，2026-09-21）



| 能力             | 现状                                                                                                                                    | 事实源                                                     |
| -------------- | ------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------- |
| durable DAG 调度 | 已有：依赖门控、并行 fan-out/fan-in、重启恢复、幂等 append                                                                                              | `workflow.go` `workflowStore`/`startWorkflowReadyNodes` |
| 节点 handoff     | 已有：不可变 artifact、verdict（pass/fail/blocked/needs_fix）、有界摘要、preview merge                                                              | `workflowNodeHandoff`、`workflow-runtime.md`             |
| 动态控制流边         | 已有：`control_flow` 边命中 `when` 后 append 节点，`iteration_key`+`max_iterations` 防重复循环                                                       | `workflowEdge`、`processWorkflowEdges`                   |
| 节点类型           | 已有 5 种：`llm_task` / `subagent_task`(默认) / `tool_call` / `user_input`（阻塞等 complete_node）/ `merge_point`                               | `agentgraph.go:21-25`                                   |
| 边类型            | 已有 3 种：`data_dependency` / `control_flow` / `handoff`                                                                                 | `agentgraph.go:30-32`                                   |
| 循环             | 部分：仅 "条件 append 新节点" 式动态循环（64 节点封顶），无静态回边 / Loop 容器                                                                                   | `workflowMaxNodes=64`                                   |
| 选择             | 受限：`when` 仅支持 `status`/`verdict` 字符串匹配，**明确拒绝任意表达式与模型评估条件**                                                                           | `workflowEdgeCondition`、`workflowEdgesFromInputs` 校验    |
| 重试             | 缺失：节点 error → 整个 workflow error；`Attempt` 仅为 handoff 文件计数。唯一 "重试" 是 LongTask `auto_repair`（带失败上下文新派 repair 子 agent，默认 2 次），属语义修复非工程重试 | `refreshWorkflowStatus`、`longtask_repair.go`            |
| 人工等待           | 雏形：`user_input` 节点阻塞至 `complete_node`，无任务队列 / 表单 / 超时升级                                                                               | `agentGraphNodeUserInput`                               |
| 业务暴露面          | 无：workflow 仅经内部工具 / CLI / 会话级 HTTP（`/api/sessions/{id}/longtasks`）暴露；`/v1/agent-steps` 冻结为单环节                                         | `routes_steps.go`、step 设计文档 §2                          |
| 版本化            | 无：流程定义即会话内工具调用，无草稿 / 发布 / 灰度                                                                                                          | —                                                       |

**结论**：引擎内核可复用约 70%，缺口集中在 "声明式流程定义 + 版本化 + 三类节点 / 原语 + 业务 API + 前端画布"。

## 2. 总体形态



```
FlowGram 画布（设计态：编辑/表单/变量）  ←→  画布 JSON adapter（TS）

FlowGram 画布（运行态：只读，SSE 高亮状态/置信度）

                  ↕ 双向编译

Flow Spec v1（flow.json，版本化：draft / gray / published）

                  ↓ compile（发布时一次性编译 + 校验，运行时只跑编译产物）

现有 durable workflow 引擎（workflowState：summary/nodes/edges/events/handoffs）

  ├ step/llm 节点 → subagent_task / llm_task / tool_call（现有）

  ├ human 节点   → user_input（现有 kind）+ 新增 human task store

  ├ decision 节点→ 新增 kind，调度器同步调用决策模型 provider（Jev/Laya）

  ├ branch       → 网关节点（不起 job）+ 扩展 when 的边

  └ loop         → 编译为现有 control_flow append 边（iteration_key/max_iterations）
```

关键原则：



1. **后端是唯一执行真源**。FlowGram Runtime（Node.js demo，官方不发布 SDK）不采用；画布只做编辑与监控。

2. **发布时编译**。flow.json 经编译 + 校验后生成不可变编译产物（digest），Run 绑定 digest；已发布流程的后续编辑不影响在途 Run。

3. **能映射到现有结构的绝不新造调度**。Loop 复用 append 边、human 复用 user_input、step 复用 agent-steps 的会话装配链。

## 3. Flow Spec v1 数据模型

### 3.1 流程定义与版本



```
// FlowDefinition 是一个流程的某个不可变版本（flow.json）。

type FlowDefinition struct {

    // --- 身份 ---

    FlowID      string \`json:"flow_id"\`                // 稳定 ID，如 fl_order_recovery

    Name        string \`json:"name"\`

    Description string \`json:"description,omitempty"\`

    Version     string \`json:"version"\`                // 单调版本号，如 "3"（也兼容 semver）

    Status      string \`json:"status"\`                 // draft | gray | published | deprecated | archived（candidate 预留给 F4 沉淀实验）

    // --- 归属与能力边界 ---

    BizKeyID   string \`json:"biz_key_id,omitempty"\`   // 绑定的业务智能体（可空=模板级共享流程）

    TemplateID string \`json:"template_id,omitempty"\`  // 能力基线（工具/MCP/写 scope），复用 AgentTemplate 解析链

    ProjectDir string \`json:"project_dir,omitempty"\`  // 缺省取 biz key/template 的 project_dir

    // --- 图 ---

    Nodes []FlowNode \`json:"nodes"\`

    Edges []FlowEdge \`json:"edges"\`

    // --- 全局策略 ---

    DefaultRetry *RetryPolicy \`json:"default_retry,omitempty"\`

    TimeoutSec   int          \`json:"timeout_seconds,omitempty"\`     // 整个 Run 的上限

    MaxLoopIters int          \`json:"max_loop_iterations,omitempty"\` // 单 loop 迭代上限的全局缺省；最终受引擎 64 节点上限约束（见 §3.2 编译期校验）

    // --- 输入输出契约 ---

    Inputs  *JSONSchema \`json:"inputs,omitempty"\`   // 启动 Run 时 inputs 的 schema

    Outputs *JSONSchema \`json:"outputs,omitempty"\`  // end 节点汇聚产出的 schema

    // --- 元数据 ---

    CreatedBy   string     \`json:"created_by,omitempty"\`

    CreatedAt   time.Time  \`json:"created_at"\`

    PublishedAt time.Time  \`json:"published_at,omitempty"\`

    Changelog   string     \`json:"changelog,omitempty"\`

    Digest      string     \`json:"digest"\`           // 编译产物 sha256，发布时计算

}
```

版本语义（新增）：



* `draft`：可任意编辑，不可被业务 API 启动；

* `gray`：版本灰度，仅允许在灰度白名单内的 biz key（`GrayFlows`）启动，见 §8.2；

* `published`：默认版本，biz key 调 `POST /v1/flows/{flow_id}/runs` 命中；

* `candidate`（**F4 预留，v1 不实现**）：LLM 挖掘的候选流程，仅允许 shadow 实验运行；

* 同一 flow_id 的 published 版本唯一；发布新版本不删除旧版本，在途 Run 永远跑其启动时绑定的 digest（复用引擎重启恢复的前提）。

### 3.2 节点联合



```
type FlowNode struct {

    ID    string \`json:"id"\`

    Kind  string \`json:"kind"\`   // start | end | step | llm | decision | human | branch | loop

    Title string \`json:"title,omitempty"\`

    // --- 通用：能力与交接（直接映射现有 workflowNodeInput）---

    AgentType       string       \`json:"agent_type,omitempty"\`

    WriteScope      []string     \`json:"write_scope,omitempty"\`

    HandoffPolicy   string       \`json:"handoff_policy,omitempty"\`   // none|summary|summary_artifacts|selected

    HandoffFrom     []string     \`json:"handoff_from,omitempty"\`

    HandoffMaxBytes int          \`json:"handoff_max_bytes,omitempty"\`

    PreviewMerge    *bool        \`json:"preview_merge,omitempty"\`

    TimeoutSec      int          \`json:"timeout_seconds,omitempty"\`  // 节点级超时（新增调度语义）

    Retry           *RetryPolicy \`json:"retry,omitempty"\`            // 缺省取 Flow.DefaultRetry（新增）

    // RiskTier 不是阈值事实源：置信度阈值一律写在出边条件上（§3.3）。

    // RiskTier 仅用于 ① FlowGram decision 节点表单的阈值默认填表值；

    // ② high 档给出"建议 LLM 异步巡检/二次复核"的表单提示。运行时不据此改变路由。

    RiskTier string \`json:"risk_tier,omitempty"\` // low|medium|high

    // --- 数据流 ---

    Inputs  map[string]Expr \`json:"inputs,omitempty"\`  // 输入变量绑定，Expr 形如 {{nodes.x.outputs.y}}

    Outputs *JSONSchema     \`json:"outputs,omitempty"\` // 本节点产出 schema（branch/loop 除外）

    // --- 各 kind 专属配置（互斥，按 Kind 取一个）---

    Step     *StepSpec     \`json:"step,omitempty"\`

    LLM      *LLMSpec      \`json:"llm,omitempty"\`

    Decision *DecisionSpec \`json:"decision,omitempty"\`

    Human    *HumanSpec    \`json:"human,omitempty"\`

    Branch   *BranchSpec   \`json:"branch,omitempty"\`

    Loop     *LoopSpec     \`json:"loop,omitempty"\`

}
```

各节点 kind 说明：



| Kind            | 语义                                                             | 编译目标                                                            | 状态          |
| --------------- | -------------------------------------------------------------- | --------------------------------------------------------------- | ----------- |
| `start` / `end` | 入口 / 汇聚（FlowGram 约定，end 汇聚 outputs）                            | 不产生 job；start 无依赖，end 编译为 `merge_point`                         | 新增（轻）       |
| `step`          | 一个完整 agent 环节（可用工具 / MCP / 召回 / 结构化输出），等价今天的 `/v1/agent-steps` | `subagent_task`（默认）或 `tool_call`                                | 复用现有        |
| `llm`           | 纯推理兜底节点（无工具），低置信 fallback                                      | `llm_task`                                                      | 复用现有        |
| `decision`      | 调决策模型（Jev/Laya）返回 `{choice, confidence}`，不起子 agent             | **新增 kind **`decision`，调度器内同步调用 provider                   | 新增          |
| `human`         | 挂起等待人工处理，带表单 / 队列 / 超时升级                                       | `user_input`（现有 kind）+ 新增 human task 记录                         | 半新增         |
| `branch`        | 网关节点，N 个输出端口按条件选路，本身不起 job                                     | 无 job 节点 + 扩展 `when` 的边                                         | 新增（轻）       |
| `loop`          | 循环容器：body 子图 + 退出条件 + 迭代上限                                     | 编译为现有 `control_flow` append 边（`iteration_key`/`max_iterations`） | 新增编译规则，机制复用 |

**64 节点上限的编译期校验（已定：不放宽）**：编译器计算最坏情况展开节点数 =

静态节点数 + Σ_loop（`max_iterations × loop body 节点数`）（嵌套递归计算；branch 网关不展开占额），

超过 `workflowMaxNodes=64` 则拒绝发布并定位到具体超限的 loop。运行期超限时沿用现有

`edge_iteration_cap` 语义将 Run 置为 error。

**StepSpec（对齐 **`stepRequest`**，逐字段复用单环节契约）**：



```
type StepSpec struct {

    Prompt           string            \`json:"prompt"\`                       // → workflowNodeInput.Prompt

    Tools            *StepTools        \`json:"tools,omitempty"\`              // 同 stepRequest.Tools {mcp, sandbox}

    Recall           []string          \`json:"recall,omitempty"\`             // 同 stepRequest.Context.Recall

    Model            string            \`json:"model,omitempty"\`

    StructuredOutput *StructuredOutput \`json:"structured_output,omitempty"\`  // 同 stepRequest（schema）

    // template_id 取节点继承的 Flow.TemplateID（能力基线），不重复声明

}
```

**LLMSpec**：仅 `prompt` + 可选 `model`（编译 `llm_task`，无工具访问）。

**DecisionSpec（Jev/Laya 接入点，新增）**：



```
type DecisionSpec struct {

    Provider     string           \`json:"provider"\`       // 现有 providers 配置中的 provider id（如 "jev" / "laya-local"），见 §8.1

    Question     string           \`json:"question"\`       // 判断问题模板（可含 {{inputs.*}}）

    DecisionType string           \`json:"decision_type"\`  // choice | boolean | score

    Choices      []DecisionChoice \`json:"choices,omitempty"\` // choice 类型的封闭候选集（Schema 约束，零幻觉边界）

    ScoreBands   []ScoreBand      \`json:"score_bands,omitempty"\` // score 类型分档（如 >=0.8 auto / 0.5-0.8 review / <0.5 llm）

    TimeoutMS    int              \`json:"timeout_ms,omitempty"\`

    OnError      string           \`json:"on_error"\`       // fail_closed（转 llm 兜底，缺省）| fail_open（按 default_choice 放行）| fail

    DefaultChoice string          \`json:"default_choice,omitempty"\`

}

type DecisionChoice struct {

    ID    string \`json:"id"\`    // 稳定标识，边条件引用它

    Label string \`json:"label"\`

}

// decision 节点的标准输出（写入 outputs，供边条件与下游引用）：

// { "choice": "auto", "confidence": 0.93, "calibrated": true,

//   "raw": {...provider 原始返回...}, "model": "laya-v1", "latency_ms": 31 }
```

**HumanSpec（人工兜底，半新增）**：



```
type HumanSpec struct {

    Queue          string   \`json:"queue"\`                     // 人工队列（按 biz_key 隔离），如 "risk-review"

    AssigneePolicy string   \`json:"assignee_policy,omitempty"\` // any | role:\<id>（v1 仅 any/角色）

    Form           *UIBlock \`json:"form"\`                      // 复用 ui_card 的卡片/表单 JSON

    Prompt         string   \`json:"prompt,omitempty"\`          // 给人工的上下文说明

    TimeoutMS      int      \`json:"timeout_ms,omitempty"\`

    OnTimeout      string   \`json:"on_timeout,omitempty"\`      // escalate:\<queue> | llm（转兜底节点）| fail

    ResultVar      string   \`json:"result_var"\`                // 人工提交值写入的变量名

}
```

**BranchSpec / LoopSpec**：



```
type BranchSpec struct {

    Cases     []BranchCase \`json:"cases"\`      // 每个 case 对应一个输出端口；按序匹配，命中即选路

    DefaultTo string       \`json:"default_to"\` // 必选：无一命中时的目标节点（禁止隐式挂死）

}

type BranchCase struct {

    Name      string        \`json:"name"\`

    To        string        \`json:"to"\` // 目标节点 ID

    Condition FlowCondition \`json:"condition"\`

}

type LoopSpec struct {

    Body          []string      \`json:"body"\`             // 子图节点 ID 集合（编译期校验闭合）

    ExitWhen      FlowCondition \`json:"exit_when"\`        // 满足即退出（如 decision.choice=="done"）

    MaxIterations int           \`json:"max_iterations"\`   // 编译为边的 max_iterations；受 64 上限约束

    IterationKey  string        \`json:"iteration_key"\`    // 编译为边的 iteration_key（幂等）

}
```

### 3.3 边与条件（扩展现有 when）



```
type FlowEdge struct {

    ID       string         \`json:"id,omitempty"\`

    From     string         \`json:"from"\`

    To       string         \`json:"to"\`

    EdgeType string         \`json:"edge_type,omitempty"\` // data_dependency | handoff（control_flow 由 branch/loop 编译产生）

    When     *FlowCondition \`json:"when,omitempty"\`     // 有条件依赖时

}

// FlowCondition 是对"已声明输出"的结构化谓词，不允许任意代码表达式。

type FlowCondition struct {

    // 现有能力（保留）：

    Status  string \`json:"status,omitempty"\`  // pending|running|completed|canceled|error

    Verdict string \`json:"verdict,omitempty"\` // pass|fail|blocked|needs_fix

    // 新增：

    Node       string         \`json:"node,omitempty"\`       // 谓词针对的节点（缺省=边的 from）

    Choice     string         \`json:"choice,omitempty"\`     // decision 输出 choice 等于某候选 ID

    Confidence *NumCompare    \`json:"confidence,omitempty"\` // 如 {"op":"gte","value":0.9} —— 置信度阈值的唯一事实源

    Output     *FieldCompare  \`json:"output,omitempty"\`     // 对结构化输出字段的比较

    All        []FlowCondition \`json:"all,omitempty"\`       // AND 组合

    Any        []FlowCondition \`json:"any,omitempty"\`       // OR 组合

}

type NumCompare struct {

    Op    string  \`json:"op"\`    // gt|gte|lt|lte

    Value float64 \`json:"value"\`

}

type FieldCompare struct {

    Path  string \`json:"path"\`  // 输出 JSON 内的点路径，如 "outputs.risk_level"

    Op    string \`json:"op"\`    // eq|ne|in|not_in|contains|gt|gte|lt|lte

    Value any    \`json:"value"\`

}
```

> 谓词语义注记：`contains` 对字符串值为**子串匹配**（`strings.Contains`），对数组值为**成员包含**；`in`/`not_in` 恒为集合成员判断。两者对字符串行为不同，FlowGram 表单据此区分控件。

> 安全边界：条件只能读取节点声明过的 
>
> `outputs`
>
>  字段与标准 verdict/status/choice/confidence；编译期做路径与类型校验。这延续现有引擎 "不支持任意表达式" 的立场（见 
>
> `workflowEdgesFromInputs`
>
> ），只是把可判定的数据源从 2 个扩到 5 类。

### 3.4 变量与数据流（新增，最小集）



* 现状引擎只在节点间传递**有界 handoff 文本**，没有类型化变量。Flow Spec v1 引入最小变量系统：节点 `outputs` 按 schema 产出，下游用 `{{nodes.<id>.outputs.<field>}}`（decision 节点固定产出 `choice/confidence/...`，human 节点产出表单值）。

* 编译期：校验所有引用路径存在且类型兼容（FlowGram 的变量作用域链可直接消费同一份元数据）。

* 运行期：调度器启动节点时把变量渲染进 prompt/inputs；handoff 文本机制保留作为未声明变量时的兜底。

### 3.5 RetryPolicy（新增，语义对齐 Temporal RetryPolicy）



```
type RetryPolicy struct {

    MaxAttempts        int     \`json:"max_attempts"\`                    // 含首次，1=不重试；step 缺省 2，decision 缺省 2

    InitialIntervalMS  int     \`json:"initial_interval_ms,omitempty"\`  // 缺省 500

    BackoffCoefficient float64 \`json:"backoff_coefficient,omitempty"\`  // 缺省 2.0

    MaxIntervalMS      int     \`json:"max_interval_ms,omitempty"\`      // 缺省 30000

    Jitter             float64 \`json:"jitter,omitempty"\`               // 0..1，缺省 0.2

    // 关键区分：工程瞬时错误才自动重试；语义失败不重试（走 branch/repair 边）

    RetryOn      []string \`json:"retry_on,omitempty"\`      // provider_timeout|provider_error|tool_transient|decision_model_error

    NonRetryable []string \`json:"non_retryable,omitempty"\` // 如 tool_permission_denied、verdict_fail、schema_violation

}
```

与现有语义的边界：



* **工程重试（RetryPolicy，新增）**：provider 超时 / 5xx、工具瞬时错误、决策模型调用失败 → 同节点原地重试，backoff 计入 `Attempt` 与事件流；

* **语义修复（已有，保留）**：节点跑完但 verdict=fail/needs_fix → 不触发 RetryPolicy，由 branch/loop 边路由到修复 / 兜底节点（即今天 LongTask `auto_repair` 的泛化）；

* `workflowNode.Attempt` 字段已存在，重试计数直接复用，handoff artifact 已按 attempt 分文件（`handoffs/{nodeID}/{attempt}.json`）。

### 3.6 Run 状态机（扩展现有状态）

现有：`pending|running|completed|canceled|error`。新增：



* `waiting_human`：Run 中存在 human 节点挂起（节点级标记为主，Run 级聚合状态便于任务台筛选）；

* `waiting_decision` 不引入（decision 同步执行，不持久挂起）；

* 终态语义不变：任一节点 error 且无 Retry / 无分支承接 → Run error；全部完成 → completed。

## 4. 运行实例（FlowRun）



```
type FlowRun struct {

    RunID      string         \`json:"run_id"\`

    FlowID     string         \`json:"flow_id"\`

    Version    string         \`json:"version"\`

    Digest     string         \`json:"digest"\`     // 启动时绑定的编译产物，在途不可变

    BizKeyID   string         \`json:"biz_key_id"\`

    SessionID  string         \`json:"session_id"\` // 编译后对应的 workflow 会话

    WorkflowID string         \`json:"workflow_id"\` // 现有 workflowStore 中的 ID

    Status     string         \`json:"status"\`

    Inputs     map[string]any \`json:"inputs"\`

    Outputs    map[string]any \`json:"outputs,omitempty"\`

    StartedAt  time.Time      \`json:"started_at"\`

    UpdatedAt  time.Time      \`json:"updated_at"\`

    FinishedAt time.Time      \`json:"finished_at,omitempty"\`

    Error      string         \`json:"error,omitempty"\`

}
```



* 编译产物：Flow Spec → `[]workflowNodeInput` + `[]workflowEdgeInput`（现有 `workflows.create` 的入参），decision/human/branch 的运行态扩展以节点 `Kind` + 新字段承载（见 §5）。

* 可观测：复用 `events.jsonl` 与 SSE；新增事件类型 `node_retry`（attempt / 原因 / 下次间隔）、`decision_made`（choice/confidence/provider）、`human_waiting`/`human_resumed`。

## 5. 逐字段映射表（Flow Spec → 现有引擎）



| Flow Spec                                     | 现有承载                                                            | 复用 / 新增                                             |
| --------------------------------------------- | --------------------------------------------------------------- | --------------------------------------------------- |
| FlowNode.ID/Title                             | `workflowNodeInput.ID/Title`                                    | 复用                                                  |
| step/llm 节点 Kind                              | `subagent_task`/`tool_call`/`llm_task`                          | 复用                                                  |
| human 节点 Kind                                 | `user_input` + 新增 human task store（队列 / 表单 / 超时）                | kind 复用，配套新增                                        |
| decision 节点 Kind                              | 无                                                               | **新增 kind **`decision`** + provider 调用链** |
| branch                                        | 无 job 网关节点                                                      | 新增（调度器直接求值，不起子 agent）                               |
| loop.body/exit/max_iterations/iteration_key | `workflowEdge{Append,When,MaxIterations,IterationKey}`          | 机制复用，编译规则新增                                         |
| Prompt/AgentType/WriteScope                   | `workflowNodeInput` 同名字段                                        | 复用                                                  |
| HandoffPolicy/From/MaxBytes/PreviewMerge      | 同名                                                              | 复用                                                  |
| DependsOn /handoff 边                          | `DependsOn`/`HandoffFrom`，`edge_type=data_dependency`/`handoff` | 复用                                                  |
| edge.when.status/verdict                      | `workflowEdgeCondition.Status/Verdict`                          | 复用                                                  |
| edge.when.choice/confidence/output            | 无                                                               | **新增字段 + 编译期校验 + 调度器求值**                            |
| RetryPolicy                                   | 无（`Attempt` 仅计数）                                                | **新增节点字段 + 调度器重试循环 + 事件**                           |
| 节点 TimeoutSec                                 | per-job timeout（job 层已有，需透传到节点定义）                               | 半新增                                                 |
| 变量 inputs/outputs                             | 仅 handoff 文本                                                    | **新增（schema + 渲染 + 编译期校验）**                         |
| FlowRun → workflowState                       | `workflowStore.create(sessionID, workflowID, nodes, edges)`     | 复用                                                  |
| 重启恢复 / 取消 / 等待                                | 现有 start/cancel/wait/refresh                                    | 复用                                                  |
| Flow 定义与版本存储                                  | 无                                                               | **新增 flows store（§6）**                              |
| human 回注                                      | `complete_node` / step `reply`（SubmitAsync）                     | 机制复用，API 新增                                         |

## 6. 存储布局（沿用 {StateDir} 文件约定）



```
{StateDir}/flows/{flowID}/

  versions/{version}/flow.json        # 不可变定义

  versions/{version}/compiled.json    # 编译产物（节点/边 + digest）

  current.json                        # 指向 draft/gray/published 各版本号

  runs/{runID}.json                   # FlowRun 记录（运行态主体仍在 workflows/）

{StateDir}/workflows/{workflowID}/... # 现有布局不变（summary/nodes/edges/events/handoffs）

{StateDir}/human-tasks/{bizKeyID}/{runID}/{nodeID}.json   # 新增：人工任务队列
```

## 7. API 面（biz-key auth 保护，风格对齐现有 `/v1/agent-steps*`）



```
# 定义与版本（管理台用，建议本机管理态鉴权）

GET    /v1/flows?biz_key_id=

POST   /v1/flows                       # 创建 draft（body=Flow Spec，或 FlowGram JSON 经 adapter 转换）

GET    /v1/flows/{id}

PUT    /v1/flows/{id}/versions/{ver}   # 更新 draft

POST   /v1/flows/{id}/versions/{ver}/validate   # 编译 + schema/路径/环/64 上限检测

POST   /v1/flows/{id}/versions/{ver}/publish    # draft→gray/published

POST   /v1/flows/{id}/versions/{ver}/deprecate

# 运行（业务系统调用，biz auth）

POST   /v1/flows/{id}/runs             # body: {inputs, version?(缺省=published), gray?(bool)}

GET    /v1/flow-runs/{runId}

POST   /v1/flow-runs/{runId}/cancel

GET    /v1/flow-runs/{runId}/events    # SSE，复用现有事件 fanout

POST   /v1/flow-runs/{runId}/human/{nodeId}/reply   # 人工回注（对齐 steps/{id}/reply）

GET    /v1/human-tasks?queue=\&status=  # 人工任务台
```

## 8. 与 biz key / AgentTemplate /providers 的关系

### 8.1 决策模型复用 providers 配置与 Settings 界面（已定）

事实基础：providers 现统一配置在 godex.yaml（`llm.ProviderConfig`：type/base_url/credential_kind 等），由 Settings 界面管理（list/test/fetch models，见 `internal/core/providers/providers.go` 与 `config/schema.go`），密钥以 env 引用不明文落盘；已有 "辅助模型按 provider 名复用已配置 provider" 的先例 ——`security.screener.provider` 按名取 `conversation.Caller`。

决策模型按同一模式接入：



1. **不新增独立配置段**。Jev（云 API）与 Laya（自托管）都注册为 provider（优先复用 `openai_compatible` + 自定义 base_url；若决策端点协议不兼容再新增 provider kind `decision`）。`DecisionSpec.Provider` 填 provider id，连接配置 / 密钥 / 健康检查全部复用 providers。

2. **调用适配独立**。决策调用不是 chat completions（结构化 choice/score 端点），在 decision 执行器内按 provider kind 做请求与返回解析，输出统一为 `{choice, confidence, calibrated, raw, model, latency_ms}`。

3. **Settings 适配点**：

* provider 增加能力标记 `capabilities`（chat /decision，可兼有），Settings 列表与业务智能体表单按能力过滤；

* decision 类 provider 的 "测试" 按钮调专用 ping / 一次样例决策，而不是 `/models` 拉取（Jev/Laya 不一定实现 models 端点）；

* decision 节点的 provider / 模型下拉直接消费现有 providers 接口，不手写第二份列表；

* 可新增全局缺省 `agents.decision.default_provider`（与 `security.screener.provider` 同构，含 env 覆盖），节点级 Provider 可覆盖。

### 8.2 biz key 字段与三种 "灰度 / 实验" 的区分（已定）

`BizAPIKey` 新增字段：



```
AllowedFlows []string     \`json:"allowed_flows,omitempty"\` // 该 key 允许启动的 flow_id 白名单，空=不允许跑流程

GrayFlows    []string     \`json:"gray_flows,omitempty"\`    // 允许启动的 gray 版本（flow_id 或 flow_id@version）

HumanQueues  []string     \`json:"human_queues,omitempty"\`  // 该 key 的人工任务可进入的队列

FlowRoutes   []FlowRoute  \`json:"flow_routes,omitempty"\`   // 旧 step 流量迁移到 flow 的显式路由（v1）

type FlowRoute struct {

    Action  string \`json:"action"\`   // 业务动作标识（由业务系统在 step 请求中携带）

    FlowID  string \`json:"flow_id"\`  // 命中后改跑的流程

    Enabled bool   \`json:"enabled"\`  // 显式开关，默认 false

}
```

三个容易混淆的概念明确分开：



| 概念                                    | 含义                                | v1 处理                                                                                                                         |
| ------------------------------------- | --------------------------------- | ----------------------------------------------------------------------------------------------------------------------------- |
| **版本灰度**（GrayFlows）                   | 同一个 flow 的**新版本 vs 旧版本**小范围上线     | gray 状态 + key 白名单（`GrayFlows`），启动时带 `gray=true`/ 显式 `version`；**不做百分比**                                                       |
| **迁移灰度**（新流程 vs 旧 business-agent 单环节） | 业务动作原来走 `/v1/agent-steps`，要切到编排流程 | 两种方式：业务方直接改调 `/v1/flows/{id}/runs`；或在 key 上配 `FlowRoutes` 按 action 显式映射（Enabled 开关，默认关，step 流量零影响）。**v1 不做透明按比例劫持**           |
| **沉淀实验**（LLM 挖掘新流程，F4/WP6）            | LLM 从轨迹挖掘的候选流程，离线验证能否替代线上判断       | **不属于 GrayFlows**：用独立的 `candidate` 状态 + shadow run（回放历史真实输入、只读无副作用、对比生产决策的一致率 / 置信度 / 成本），达标后才进 draft → 评审 → gray → published |

### 8.3 能力边界与计量（已定）



* 能力基线仍由 `AgentTemplate`（M4 P1 已落地的 "模板 + 覆盖层"）统一表达：Flow 挂 `template_id`，节点不重复声明工具集；step 节点的工具 / MCP / 写 scope 在编译期经同一条 Resolve 链收窄。

* **decision 节点独立计量**：它不起子 agent、不写会话 transcript、不占 `context_budget`；按调用次数与输入 token 以 `kind=decision` 独立记账，在 Run 用量汇总中单列（预算 / 配额仍复用现有 budget/credit 设施，按 Run 聚合）。

* 其余节点（step/llm/human 触发的 LLM 处理）按现有 token 计量累加。

## 9. FlowGram 对接契约（前端）



* **adapter 双向**：FlowGram 画布 JSON ⇄ Flow Spec。节点物料与 kind 一一对应：固定环节 = step、判断 = decision（表单内含候选集 / 阈值 /on_error，阈值默认值由 RiskTier 预填但可改）、AI 兜底 = llm、人工 = human、条件 = branch、循环 = loop；画布端口连线 ⇄ edges。

* **变量面板**：消费 Flow Spec 的 inputs/outputs schema，渲染 FlowGram 变量作用域链。

* **provider 选择**：decision 物料的 provider 下拉复用 Settings 的 providers 数据源（按 decision 能力过滤）。

* **运行态**：画布只读，SSE 事件映射为节点颜色（running/completed/error/waiting_human）与 decision 节点上的 confidence 标注；不做画布内编辑。

* FlowGram 是重框架（Inversify DI、Rush monorepo），集成工作量主要在 adapter 与自定义物料，估 8–12 人日；可先以 YAML/JSON 直接定义流程跑通后端，再接画布。

## 10. 分期交付（对齐差距评估 WP1–WP5）



| 期               | 内容                                                                                                      | 出口判据                                                                                     |
| --------------- | ------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------- |
| F0（内核原语）        | RetryPolicy + 扩展 when（choice/confidence/output）+ decision kind；复用 providers 注册一家决策模型并完成 Settings 能力标记适配 | 引擎级测试：瞬时错误自动重试到上限、verdict fail 不重试、confidence 路由正确、decision provider 可在 Settings 配置并通过测试 |
| F1（Flow 编译面）    | Flow Spec store + 版本化 + `compile`（含 loop→append 边、branch→网关、64 上限校验）+ `/v1/flows` CRUD/validate/publish | YAML 定义一条 "step→decision→(branch)→llm/human" 流程可发布启动                                     |
| F2（Run 与人工）     | `/v1/flows/{id}/runs` + SSE + human task store/reply + 超时升级 + FlowRoutes 迁移开关                           | 端到端切片：高置信自动执行、低置信 LLM 兜底、高危转人工并回注续跑                                                      |
| F3（FlowGram 画布） | adapter + 六类物料 + 设计态 / 运行态两模式                                                                           | 画布里新建 / 编辑 / 发布流程，运行态状态与 confidence 高亮                                                   |
| F4（沉淀闭环，WP6 另文） | candidate 状态 + shadow run + Run 轨迹标准化 → 挖掘 → 候选 Flow/decision schema 提案 → 评审发布                          | 依赖 F1–F3 真实运行数据，本期不做                                                                     |

## 11. 验收标准（可验证）



1. 一份 flow.json 经 `validate` 编译：环、重复 ID、未知节点引用、条件路径不存在、choice 不在候选集、loop 无退出条件均被拒绝并给出定位。

2. **64 上限**：最坏情况展开节点数（静态节点 + 各 loop `max_iterations × body 节点数`，含嵌套；branch 不占额）超过 64 时拒绝发布并定位到具体 loop。

3. step 节点编译产物的工具 / MCP / 写 scope 与同 template 下直接调 `/v1/agent-steps` 完全一致（同一 Resolve 链测试断言）。

4. decision 节点返回 schema 约束内的 choice + [0,1] confidence；provider 故障按 on_error 路由（默认 fail_closed 转 llm），不阻断 Run。

5. decision 节点在 Settings 中可选择 / 测试 decision-capable provider；调用以 `kind=decision` 独立计量，且不向会话 transcript 写消息、不占 context_budget（测试断言）。

6. RetryPolicy：注入 provider 超时故障，节点按 backoff 重试至 MaxAttempts，事件流记录每次 attempt；verdict=fail 时不产生重试而是走分支。

7. 发布流程 v2 后，v1 在途 Run 仍按 v1 digest 跑完（重启恢复测试）。

8. human 节点超时按 on_timeout 升级 / 转 llm；人工 reply 后 Run 续跑，提交值进入下游变量。

9. loop 编译出的 append 边具备幂等性：重启不产生重复迭代，超 max_iterations 进 error（沿用现有 edge_iteration_cap 语义）。

10. 回归：现有 workflow/longtask/agent-steps 测试全绿，新字段均为 omitempty 向后兼容。

## 12. 已拍板决策（2026-09-21）



1. **decision provider 形态 → 复用 providers**。Jev/Laya 作为 provider 注册，密钥 /base_url/ 健康检查 / Settings 界面全部复用，仅新增 decision 能力标记与专用测试动作；不建独立配置段。落地细节见 §8.1。

2. **置信度阈值归属 → 边条件为唯一事实源**。阈值显式写在出边 `when.confidence` 上（画布可见、可审计）；RiskTier 只决定表单默认预填值与 high 档巡检提示，运行时不参与路由。

3. **64 节点上限 → 不放宽**。编译期按最坏展开（含 loop 迭代）校验，超限拒绝发布；branch 网关不占节点名额。

4. **decision 计量 → 独立小额计量**。不起子 agent、不进 transcript、不占 context_budget，`kind=decision` 单列。

5. **灰度语义 → 三分（见 §8.2）**：GrayFlows 仅指 "同一流程的新版本 vs 旧版本" 的版本灰度（key 白名单，不做百分比）；"新流程 vs 旧 business-agent 单环节" 是迁移灰度，用显式 FlowRoutes 开关、默认不影响 step 流量；"LLM 沉淀新流程的实验" 是 F4 的 candidate + shadow run，与线上灰度完全分离。

## 13. F0 落地状态（2026-09-21）

F0 内核原语已落地并通过引擎级测试（`internal/agent/workflow_flow_test.go`、`internal/core/decision/llm_caller_test.go`）。

**已落地**

1. 节点级 `RetryPolicy`（Temporal 语义：max_attempts / initial_interval_ms / backoff_coefficient / max_interval_ms / jitter / retry_on / non_retryable）：subagent job 终态失败先经 `classifyWorkflowFailure` 分类，瞬时错误（provider_timeout / provider_error / tool_transient / decision_model_error）按指数退避在原节点重试，pending 节点由 `next_retry_at` 门控；语义失败（verdict=fail、权限拒绝、schema 错误）不重试，直接走边。每次重试写 `node_retry` 事件。
2. 边条件 `when` 扩展：新增 `choice` / `confidence`（数值比较）/ `output`（点路径字段谓词 eq/ne/in/not_in/contains/gt/gte/lt/lte）/ `all` / `any`，以及 `node` 跨节点引用；旧 status/verdict 语义不变。
3. 新节点 kind `decision`：调度器内同步执行，不起子 agent job、不写 transcript、不占 context_budget；产出标准 `{choice, confidence, calibrated, score}` 并写入节点 outputs；支持 choice/boolean/score 三类决策与 on_error 三态（fail_closed 默认，产出保留 choice `__decision_error__`、verdict=blocked 路由到 llm 兜底；fail_open 用 default_choice 放行；fail 置 error）。
4. 决策模型接入复用 `api.providers`：配置段 `agent.decision.{enabled,provider,timeout_ms,max_tokens}`（默认关闭，provider=llm，10s，64 tokens），Settings schema、stored/effective values、环境变量 `GODEX_AGENT_DECISION_*` 全链路接通；核心包 `internal/core/decision` 定义 Caller 接口与严格 JSON chat 适配（封闭候选集校验、容忍 markdown 围栏）。

**F0 明确不做（留给 F1+）**

- 节点级 provider 选择：DecisionSpec.provider 仅存储与审计，F0 统一走默认 client（与 security screener 同构）。
- Jev/Laya 原生结构化端点：当前为 chat JSON 适配，原生端点在 Caller 接口后扩展。
- providers 的 decision capability 标记与 Settings 专用 ping 测试动作。
- `kind=decision` 的独立用量记账（预算/配额聚合）；当前已天然不进 transcript、不占 context_budget。
- Flow Spec 编译器、版本 store、`/v1/flows`、FlowGram adapter（F1/F3）。

### 13.1 F0 Review 结论（2026-09-21 未提交变更审查）

**结论**：F0 与 §13 声明严格对齐，工程质量良好，无阻塞问题；核心设计取舍正确（保守重试分类器——非瞬时错误不自动重试防死循环、decision 同步执行不占资源不入 transcript、fail_closed 默认安全路由到 LLM 兜底）。

**验证**：`go test ./internal/agent/ -run 'TestWorkflow|TestClassify|TestNormalize'` 与 `go vet ./internal/core/decision/` 通过；47 个未提交变更中 longtask/subagent/agentgraph 等大量改动经 `git diff -w` 核验为 gofmt 对齐（longtask_types.go 0 行实质改动，subagent_* 各仅 2 行）。

**发现的关注点（F1 前顺手处理）**

1. **kind 单一事实源缺失**：`decision` kind 仅以 `workflowNodeKindDecision = "decision"`（`workflow_flow.go:25`）存在，`agentgraph.go` NodeKind 常量区未同步（该文件 F0 改动仅注释对齐）。两处 kind 体系（agentgraph vs workflow）不一致，F1 FlowGram 编译易踩坑。→ 收尾：`agentgraph.go` 补 `agentGraphNodeDecision = "decision"`。
2. **retry 节点失败信息不保留在节点上**：`scheduleWorkflowNodeRetry` 清空 `Error/Verdict/HandoffRef`，仅事件流留 `node_retry`。属可接受权衡，排查时依赖事件流（文档可注明）。
3. **`contains` 谓词对字符串为子串匹配**（`strings.Contains`，`workflow_flow.go:552`），与 `in`（成员）区分明确，但文档 §3.3 谓词表建议注明字符串子串语义。
4. **config→caller 接线无测试**：`buildDecisionCaller`（`decision_caller.go`）走 `ApplyConfig`/`newAgentWithDependencies` 的路径未被测试覆盖。→ 收尾：补 enabled/disabled → caller nil/非 nil 单元测试。
5. **F1 编译语义待定**：Flow Spec 的条件边是 `{From, To, When}`（To 为**已有节点**），而现有引擎 control_flow 边是 `{From, When, Append}`（内联**新节点**）。F1a 编译器需明确展开策略（见 §14）。

### 13.2 F0 收尾项（随 F1a 提交）

- [x] `agentgraph.go` 补 `agentGraphNodeDecision` 常量（单一事实源）
- [x] `buildDecisionCaller` 接线测试（config enabled/disabled → caller nil/非 nil）
- [x] 文档 §3.3 谓词表注明 `contains` 字符串子串语义
- [x] §13.1 关注点 #5 的编译语义决策（见 §14）

## 14. F1a 编译语义决策记录（2026-09-21 落地）

F1a 落地了 Flow Spec 的**模型 + 校验 + 编译 + 引擎网关**，是 F1（Flow 编译面）的第一块：YAML/JSON 定义的流程可以编译并跑在 durable 引擎上（端到端测试 `TestFlowCompileBranchRoutesEndToEnd` / `TestFlowCompileBranchDefaultRoutes` 验证）。

**落地内容**

1. `internal/core/flow` 包：Flow Spec v1 模型（`model.go`）、校验（`validate.go`，§11.1-11.2 的环/重复 ID/未知引用/缺失 prompt/候选集/64 上限全部实现）、编译（`compile.go`，`flow.Definition → flow.Compiled{Digest}`）。
2. `internal/agent/workflow_flow_compile.go`：`flow.Compiled → workflowNodeInput/workflowEdgeInput` 映射（kind 映射 step→subagent_task、llm→llm_task，decision/branch 透传；条件边 `when` 全字段映射含 all/any 嵌套）。
3. `internal/agent/workflow_flow_branch.go`：**branch 同步网关节点** `executeWorkflowBranch` —— 对 Source 节点 outputs 顺序求值 cases，命中 route 写入 `outputs.choice`，完成节点（不起 subagent job、不入 transcript、不占 context_budget，与 decision 同构）。
4. 引擎修复：`startWorkflowReadyNodes` 改为**重扫直到无可推进节点**——同步节点（decision/branch）链在一个 tick 内跑完，branch 依赖 decision 不再卡 pending。
5. F0 收尾：`agentGraphNodeDecision` 常量、`buildDecisionCaller` 接线测试、`contains` 谓词语义注记（见 §13.2）。

**编译语义决策（对应 §13.1 关注点 #5，已拍板）**

| 问题 | 决策 | 理由 |
| --- | --- | --- |
| Flow Spec 条件边 `{From, To, When}` vs 引擎 `{From, When, Append}` | **To 编译为 Append 模板**，节点不静态声明；若 To 同时有静态入边则拒绝（F1a 不支持混合使用） | 避免双创建；condition 边本来就是"命中才创建" |
| branch 网关形态 | **同步节点**（与 decision 同构）：对 Source 求值写 `outputs.choice`，路由靠 `when.choice` 条件边 | §2/§5 明确"调度器直接求值，不起子 agent"；choice 单值天然互斥，无需 NOT 条件、不侵入调度循环 |
| branch default | 保留字 route `"default"`，编译为 `{when: {choice: "default"}}` 边 | 显式路由、可审计；禁止隐式挂死 |
| loop | F1a **校验但拒绝编译**（需 exit_when 的 NOT 编译，F1b） | 收敛范围，避免半成品 |
| 64 上限 | 静态节点 + loop 最坏展开校验（branch 不占额），与 §12-3 一致 | — |
| `workflowMaxNodes=64` | flow 包 `MaxNodes=64` 与引擎常量一致 | — |

**验证**：`go test ./internal/core/flow/`（12 测试）+ `go test ./internal/agent/ -run TestFlowCompileBranch*`（2 端到端）全绿；`go build ./...`、`go vet`（flow/decision/agent）通过；agent 全量仅 1 个既有环境失败（`TestBuildContextExposesOnlyActiveToolSchemas`，godex_docs 为 always-on 宿主工具不在测试 pin 列表，与 F1a 无关）。

**F1a 明确不做（留给 F1b/F2）**：Flow 版本 store（`{StateDir}/flows/`）、`/v1/flows` CRUD/validate/publish、`FlowRun` 记录与 `/v1/flows/{id}/runs`、loop 编译、human task store。

## 15. F1b–F3 完整实施状态（2026-09-21 落地）

F1a（模型/校验/编译/网关）之后，本方案**前后端已完整实现**：Flow Spec v1 从「JSON 定义」到「HTTP API」到「Web 页面运行」全链路打通。

### 已落地

**后端（F1b + F2 核心）**

1. **Flow 版本 store**（`internal/agent/flow_store.go`）：`{StateDir}/flows/{flowID}/versions/{v}/{flow,compiled,meta}.json` + `current.json`（draft/gray/published 三车道）+ `runs/{runID}.json`，原子写、跨进程可见。
2. **公开操作**（`internal/agent/flow_ops.go`）：`CreateFlow`（保存时即 validate+compile）、`Get/List/ListVersions/Validate/Publish`、`CreateFlowRun`（解析版本→编译→建 durable workflow→写 run 记录，每次 run 独立 workflow_id）、`Start/Wait/Refresh/CancelFlowRun`、`ListFlowRuns`。
3. **backend Service 转发**（`internal/services/backend/flows.go`）：workspace 级共享 flowAgent，无会话依赖。
4. **HTTP API**（`internal/runtime/httpapi/routes_flows.go`，web-token protected）：
   - `GET/POST /v1/flows`（列表 / 创建 draft）
   - `GET /v1/flows/{id}`（版本列表）
   - `POST /v1/flows/{id}/versions/{ver}/validate|publish`
   - `POST /v1/flows/{id}/runs`（创建+启动+可选 wait）
   - `GET /v1/flows/{id}/runs`、`GET /v1/flow-runs/{runID}`、`POST /v1/flow-runs/{runID}/cancel`

**前端（F3）**

5. `ui/web/src/lib/apiFlow.ts`：Flow 类型 + 9 个 API 函数（复用 request/apiURL）。
6. `ui/web/src/features/flows/FlowsPage.tsx`：列表（三车道状态列）、新建 Drawer（JSON 编辑器）、详情 Drawer（版本 Tab + 运行记录 Tab + 定义 Tab）、发布/运行/取消操作。
7. 路由注册（`appRegistry.tsx` `/flows` 入口）+ i18n（en/zh core nav + product 页面键）。

### 验证

| 层 | 命令 | 结果 |
| --- | --- | --- |
| flow 包 | `go test ./internal/core/flow/` | ✅ 12 测试 |
| decision 包 | `go test ./internal/core/decision/` | ✅ 7 测试 |
| agent flow | `go test ./internal/agent/ -run TestFlow*` | ✅ 5 测试（CRUD/publish/run 生命周期/跨 agent 持久化） |
| HTTP | `go test ./internal/runtime/httpapi/ -run TestFlows*` + 全量 | ✅ 2 测试 + 全量 27s 通过 |
| backend | `go test ./internal/services/backend/` | ✅ 13s 通过 |
| 前端 | tsc -b + vite build | ✅ build 54s |
| 全量 | `go build ./...` + vet（5 包） | ✅ |

**agent 全量仅 1 个既有环境失败**（`TestBuildContextExposesOnlyActiveToolSchemas`，godex_docs 为 always-on 宿主工具不在测试 pin 列表），与 Flow 实施无关。

### 分期状态

| 期 | 内容 | 状态 |
| --- | --- | --- |
| F0 | RetryPolicy + 扩展 when + decision kind + providers 接入 | ✅ |
| F1a | Flow 模型 + 校验 + 编译 + branch 网关 | ✅ |
| F1b | Flow store + 版本化 + `/v1/flows` CRUD/validate/publish | ✅ |
| F2 | runs 端点 + FlowRun 记录 + cancel | ✅（events SSE 留 F2b） |
| F2b | human task store / reply / 超时升级 / loop 编译 | ⏳ 未做 |
| F3 | FlowGram 画布 adapter + 六类物料 + 运行态高亮 | ⏳ 部分（管理页面已交付，画布未接） |

### 明确不做（后续项）

- `GET /v1/flow-runs/{runId}/events` SSE 事件流（复用现有事件 fanout，F2b）。
- loop 编译（`exit_when` 需 NOT 条件编译）。
- human 节点与 human task store（F2b，设计文档 §3.2/§6）。
- FlowGram 画布双向编译（F3 剩余，Flow Spec 定义经 JSON 编辑器已可发布运行）。

## 16. 产品化决策（2026-09-21 拍板）

> 决策 6–7 由用户按推荐方向确认，进入产品定义基线；决策 8 为输入/输出接入面设计基线。

6. **入口形态 → 一个「业务编排」入口，智能体库 / 流程库并列分组**。不拆两个 app，也不把 Flow 塞进 Agent 详情页（Flow 跨 Agent，挂单个 Agent 下会破坏心智）。导航一个入口，左侧分组：
   - **智能体库**：Agent 档案 CRUD + 能力/预算/接入（现有 BusinessAgentsPage 平移）。
   - **流程库**：Flow 列表/画布/版本/运行（现有 FlowsPage 平移 + 后续 FlowGram）。
   双向引用跳转：Agent 档案页显示「被 N 个 Flow 引用」可跳转；Flow 编辑页显示「使用了哪些 Agent」；改 Agent 能力/停用时提示影响面。

7. **概念分层 → Agent = 能力单元，Flow = 能力组合**。
   - Flow 的 step 节点加 `agent_ref`（引用 template_id 或 biz_key_id），运行时走现有 template 解析链，继承该 Agent 的能力白名单/工作目录/预算；未指定时用 Flow 级默认。不新造装配链。
   - 计量贯通：Flow 运行中每个 step 节点的额度记到被引用 Agent 的预算；decision 节点记 Flow 自己的账（kind=decision 独立计量）。
   - 快捷接入：Flow 发布后绑定 biz key（`flow_id` 进 BizAPIKey），获得与 `/v1/agent-steps` 对齐的调用端点。Agent 和 Flow 都能被业务系统调——单环节调 Agent，多环节调 Flow，认证模型统一。

8. **输入/输出与网关 → Flow = 服务契约，Run = 一次请求，Gateway = API 网关**。
   - 输入触发：① 同步 API（已有 `POST /v1/flows/{id}/runs`）② 事件/Webhook 入（网关按 event_type/route 分发建 Run）③ cron 定时触发（复用 automation 基建）④ `<godex-flow>` 嵌入组件（类比 `<godex-step>`，复用 ui_card 交互闭环）⑤ Flow 嵌套（一个 Flow 的 outputs 作为子 Flow inputs）⑥ 人工/IM 触发。
   - 输出获取：① 同步等待（短流程 wait_ms）② 轮询（已有）③ SSE 事件流（`GET /v1/flow-runs/{runId}/events`，运行态高亮数据源，F2b）④ 完成回调 Webhook（Flow 声明 `on_complete` URL，Run 结束时 POST 结果，**新增到设计**）⑤ 落盘审计（已有）。
   - 网关层：**Flow Gateway 与 agent-step 共用同一前缀** `POST /v1/gateway/{route}`——按目标分发到单环节（step）或多环节（flow）。职责：路由分发、biz key 鉴权、限流、幂等键去重、input schema 校验（对照发布版 schema）、统一响应封装（`{run_id, status, outputs|callback}`）。对外业务心智统一：「我调一个入口，它可能一步完成，也可能跑一个流程后回调我」。

## 17. P1 实施状态（2026-09-21 落地）

P1 = §10 分期中的第一块运行时能力补齐（human 回注 + 事件流 + 完成回调 + step 能力继承 + loop 编译 + decision 计量），
在 F0/F1a（§13/§14）与 F1b–F3 基础设施（§15）之上补齐「Flow 作为服务」的交互闭环。P1.1–P1.6 全部完成。

**P1.1 human 节点 + 人工任务回注（10 测试全绿）**
- `internal/core/flow`：`HumanSpec` 模型（queue/assignee_policy/form/timeout_ms/on_timeout/result_var）+ 校验（缺失 spec/queue 拒绝、on_timeout 合法值、escalate 需 target）。
- `internal/agent/human_task_store.go`：`{StateDir}/human-tasks/` 任务账本（run/queue/status/node_id/assignee/form/due_at/timeout/on_timeout），幂等注册 + 查询过滤器（queue/status）。
- `internal/agent/flow_human_ops.go`：节点编译为 `user_input` → 注册任务 → 状态 `waiting_human`；`ReplyFlowRunHuman` 回注（结果写 `result_var` 输出、任务置 replied、节点完成、run 继续）；`CheckHumanTaskTimeouts` 超时扫描：`on_timeout=escalate|llm|fail` 三种升级路径（llm 升级重排节点为 `llm_task`）。
- API：`POST /v1/flows/{flowId}/runs/{runId}/human/{nodeId}/reply` + `GET /v1/flows/{flowId}/runs/{runId}/human/tasks` + `POST .../timeout-check`。

**P1.2 SSE 事件流（2 测试全绿）**
- `GET /v1/flow-runs/{runId}/events`：基于 workflow events 日志（created/node_started/node_completed/decision_made/human_task 等）的 SSE 流式输出，`text/event-stream` + `X-Accel-Buffering: no`，支持 `since` 参数补发历史事件。前端运行态高亮数据源（F2b）。

**P1.3 on_complete Webhook（1 测试全绿）**
- Flow 定义新增 `on_complete` URL 声明；Run 进入终态（completed/error/canceled）时 `internal/agent/flow_webhook.go` POST 结果 payload（run_id/status/outputs/summary），非 2xx 记事件不阻塞终态；幂等键防重发。

**P1.4 step 节点 agent_ref 能力继承（4 测试全绿）**
- `flow.Node/CompiledNode` 新增 `AgentRef`；编译链贯通（compile.go → workflowNodeInput → workflowNode）。
- 运行时 `startWorkflowNode` 解析 `agent_ref`（templates.Manager.Resolve），把模板 Bundles/Tools/WriteScope 注入 subagent 启动请求（`RequiredBundles`/`RequiredTools`/WriteScope 合并）；模板缺失 fail-fast 节点 error（不静默降级）。
- agent 层依赖注入 `templateMgr`（nil 安全）。

**P1.5 loop 编译（5 测试全绿）**
- `flow.Condition`/`workflowEdgeCondition` 新增 `Not` 全量否定（模型 + 校验递归 + 引擎求值 + 编译映射贯通）。
- `compile.go` 不再拒绝 `KindLoop`：loop 编译为一条 control_flow append 边——`When = Not(exit_when)`（继续迭代条件）、`Append = body 首节点模板`、`MaxIterations/IterationKey` 透传（引擎既有幂等/上限机制复用）；loop 节点自身不产生静态 job，其 data_dependency 入边在折叠阶段跳过。
- 端到端验证：flow 编译产物含 `Not` 条件边 + 引擎层映射保留 `Not` 与迭代边界。

**P1.6 decision 独立计量（3 新测试全绿）**
- `conversation.UsageContext`/`usage.UsageCall` 新增 `Kind` 字段；`RecordLLMUsage` 透传。
- `executeWorkflowDecision` 调用前注入 `kind=decision` 的 UsageContext（保留调用方 session/attribution）；决策调用仍不起 subagent job、不进 transcript、不占 context_budget，但产生独立 usage 记录（Run 用量汇总可按 kind=decision 单列，§16 决策 7 计量贯通）。

**验证**：`go test ./internal/core/flow/ ./internal/core/decision/ ./internal/core/conversation/ ./internal/services/usage/ ./internal/runtime/httpapi/` 全绿；
`go test ./internal/agent/ -run 'TestFlow|TestWorkflow|TestCompileLoop|TestHuman'` 断言全部通过（含 P1.1–P1.6 全部新测试）；
`go test ./internal/agent/` 全量中 P1 相关测试偶发报 `t.TempDir RemoveAll: directory not empty`（durable subagent 后台写盘与清理竞态，测试断言本身通过，非逻辑回归），另有需本地 LLM 服务（`127.0.0.1:80`）的既有环境失败；
`go build ./...`、`go vet`（flow/decision/agent/usage/conversation/httpapi）通过。

**P1 明确不做（留给 P2）**：FlowGram 画布（F2a）、Flow Gateway 统一前缀 `/v1/gateway/{route}`、biz key 绑定 flow_id 快捷接入、Flow 嵌套、cron/IM 触发、变量 schema 校验、嵌套 loop/loop 与 branch 混合展开的 64 上限最坏情况校验细化。


