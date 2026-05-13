[简体中文](SPEC.md) | [English](SPEC.en.md)

# GoDex 2.0 架构 SPEC

## 摘要

GoDex 1.0 已经从本地优先的 Agent 工作台，成长为一套可以服务 CLI、TUI、Web、HTTP API、Feishu、Weixin、工具、记忆、子 agent、自动化和审批流程的共享运行时。GoDex 2.0 的下一步，是从单个大型进程内 Agent，演进为一个明确解耦身份、编排、Worker 执行、沙盒环境、Session 记忆和存储的 Agent Runtime 平台。

本 SPEC 定义长期架构方向和迁移路径。它不是一次性大重写。每个阶段都必须保留现有 1.0 行为，同时抽取更清晰的边界。

## 当前 1.0 基线

GoDex 1.0 已经提供：

- CLI、TUI、Web、HTTP API、Feishu、Weixin 共享的 session runtime。
- 带审批的工具执行，包括 shell、file、browser、web、memory、skill、package、MCP 等能力面。
- Durable memory、history recall、context compaction、transcript archive 和 context inspection。
- Durable subagent job、workflow/longtask 编排、review/merge/cancel/resume。
- Web 工作台，覆盖 chat、settings、memory、automation、nodes、notes、skills 和 context diagnostics。
- 本地优先部署，支持内嵌 Web UI 的单二进制和自管理 service install。

主要架构压力是 `internal/agent/agent.go` 仍然同时承担大型 composition root、session state holder、tool registry、skill/package facade、permission facade、subagent tool controller 和 compaction bridge。这个形态能支撑 1.0，但会让 sandbox 替换、worker runtime 替换、session branching 和 storage backend 替换变得困难。

## 2.0 原则

### 1. Agent Identity 必须和 Sandbox Execution 解耦

Agent 回答：我是谁，我能做什么，我会怎么工作？

Sandbox 回答：我在哪里工作，能访问哪些文件和工具，环境如何重建？

GoDex 2.0 将这些概念分开：

- **Agent Identity**：名称、profile、role、model policy、capability policy、prompt strategy、delegation strategy。
- **Sandbox**：workspace、filesystem view、process environment、command policy、tool runtime、artifacts、lifecycle。
- **Tool Runtime**：bash、file IO、browser、web、MCP、desktop 和外部系统的具体执行双手。

如果 sandbox 被污染或损坏，Agent identity 应该可以挂载到新的 sandbox，而不丢失高层 session、memory 或 orchestration state。

### 2. Orchestrator Agent 必须和 Worker Agent 解耦

主 Agent 应保持聪明和上下文干净。它负责规划、委派、监控、评审和合并。

Worker Agent 负责在边界明确的 sandbox 中执行重活和脏活。Worker 可以是：

- 同进程内运行的 GoDex role。
- 运行在另一个 sandbox、host 或 node 上的 GoDex worker。
- 通过兼容 worker protocol 暴露的其它 agent runtime。

Orchestrator 通过结构化 job 派发工作：prompt、role、required tools、required bundles、sandbox policy、write scope、expected output、timeout、merge policy。Worker 返回 progress、artifacts、diffs、summaries、errors 和 completion state。

### 3. Session Memory 必须成为可分支的树

当前 session 主要是线性 history，加上 compaction 和 transcript references。GoDex 2.0 需要 session memory tree：

- 从任何稳定点创建分支。
- 为 worker exploration 克隆 session。
- 回滚到早期 node。
- 把 worker 的结果、摘要、diff 和决策合并回主分支。
- 从 events、checkpoints、summaries、artifacts、memory references 重建上下文。

它应该更像版本化 context graph，而不是单个可变的 `messages` slice。

### 4. Session 必须和 Storage Backend 解耦

Session state 不应假设某一种存储介质。GoDex 2.0 必须定义可由不同后端实现的 storage interfaces：

- JSON files：适合简单本地部署。
- SQLite：适合可靠的单机索引和事务。
- Server database：适合多节点或团队部署。
- Cloud/object storage：适合大型 artifacts、transcripts 和长期 archive。

Runtime code 应依赖 repository/store 接口，而不是直接依赖文件布局。只有 storage backend implementation 可以直接了解具体存储路径。

## 概念边界与 ID

GoDex 2.0 必须显式建模 runtime objects。实现中不应再用同一个 struct 同时表示 agent、session、sandbox、worker 和 storage row。

### 概念边界表

| Concept | 回答的问题 | 拥有 | 不拥有 |
|---------|------------|------|--------|
| **Agent Identity** | 我是谁？遵循什么策略？ | profile、role、prompt strategy、model policy、capability policy、delegation policy | workspace files、process state、session messages、persistent storage layout |
| **Agent Instance** | 当前哪个 live agent process 正在行动？ | 绑定到 session、sandbox、model caller、tool exposure state 的 runtime state | durable identity definition、storage backend implementation |
| **Orchestrator** | 谁负责规划和派工？ | job planning、worker assignment、review/merge decisions、mainline context hygiene | worker filesystem、low-level tool execution |
| **Worker** | 谁执行被分配的 job？ | job-local model turns、progress、artifacts、diffs、result summaries | main session authority、global memory policy、unrelated branches |
| **Sandbox** | 工作在哪里发生？ | workspace view、environment variables、command policy、temp/artifact roots、lifecycle | agent identity、session graph、storage schema |
| **Tool Runtime** | 双手如何执行动作？ | concrete tool handlers、permission checks、tool bundle activation、shell/file/browser/web/MCP adapters | orchestration policy、session branching、long-term identity |
| **Session Graph** | 发生了什么？当前上下文在哪个 branch？ | session nodes、branches、messages、events、checkpoints、merge records | physical storage details、process lifecycle |
| **Storage Backend** | 状态保存在哪里？ | JSON/SQLite/DB/cloud implementation、transactions、indexes、migrations | agent behavior、sandbox policy、orchestration decisions |
| **Artifact** | 产生了什么具体输出？ | file/blob metadata、digest、producer reference、retention policy | session graph topology、worker lifecycle |

### 稳定 ID

ID 在 API 边界上必须是不透明字符串。推荐使用前缀便于调试和迁移安全，但除兼容 adapter 外，代码不应通过拆分前缀来解析 ID。

| ID | 示例 | 作用域 | 含义 |
|----|------|--------|------|
| `agent_id` | `agent:lead` | Durable config scope | Agent 或 role 的稳定身份定义。 |
| `agent_instance_id` | `agent-inst:01J...` | Runtime process scope | Agent identity 到 session/sandbox 的一次 live attachment。 |
| `session_id` | `session:web:42d...` | Durable storage scope | 逻辑用户或工作流 session。 |
| `branch_id` | `branch:main` | Session scope | 单个 session graph 内的命名或生成 branch。 |
| `node_id` | `node:01J...` | Session graph scope | Context graph 中不可变的点。 |
| `sandbox_id` | `sandbox:local:repo` | Runtime/storage scope | 可重建的执行环境。 |
| `tool_runtime_id` | `tools:local:default` | Sandbox scope | 绑定到 sandbox 的具体 tool handler set。 |
| `worker_id` | `worker:godex:01J...` | Orchestration scope | Worker runtime endpoint 或 live worker。 |
| `job_id` | `job:subagent:01J...` | Orchestration/session scope | 一个被委派的工作单元。 |
| `artifact_id` | `artifact:01J...` | Storage scope | 持久化 file/blob/diff/result reference。 |
| `store_id` | `store:sqlite:local` | Deployment scope | Storage backend instance。 |

### 所有权与引用规则

- 一个 `Agent Identity` 可以在时间上对应多个 `Agent Instance`。
- 一个 `Agent Instance` 绑定一个 `Agent Identity`、一个 active `Session Graph` branch，并且可选绑定一个 active `Sandbox`。
- `Orchestrator` 是具有创建 jobs 和把 worker output 合并进 branch 权限的 agent instance。
- `Worker` 可以执行一个或多个 jobs，但每个 job 必须声明 `session_id`、source `branch_id` 或 `node_id`，以及 assigned `sandbox_id`。
- `Sandbox` 只有在 lifecycle policy 允许时才能被多个 jobs 复用。Disposable sandbox 应按 job 或 branch 重建。
- `Tool Runtime` 绑定到 sandbox。Tool permissions 和 bundle activation 属于这个绑定，不属于 durable agent identity。
- `Session Graph` 通过 `artifact_id` 引用 artifacts，不内联保存大型 artifact payload。
- `Storage Backend` 持久化所有 durable concepts 的 records，但不定义 runtime behavior。Runtime behavior 属于 agent、worker、sandbox 和 tool policies。
- Storage 和 API 边界上的跨对象引用必须使用 ID，而不是内存指针。

### 最小关系模型

```text
Agent Identity
  -> Agent Instance
      -> active Session Graph branch
      -> optional Sandbox
          -> Tool Runtime

Orchestrator Agent Instance
  -> creates Job
      -> assigned Worker
      -> assigned Sandbox
      -> source Session node/branch
      -> output Artifacts
      -> merge record back into Session Graph

Storage Backend
  -> persists Agent Identity, Session Graph, Job, Sandbox metadata, Artifact metadata, Permission records
```

### 边界不变量

- 重建 sandbox 不得改变 `agent_id`、`session_id` 或 `branch_id`。
- 克隆 session branch 不得克隆 process state。它创建新的 graph nodes，并引用既有 artifacts，除非显式请求 copy-on-write。
- 从 JSON 迁移到 SQLite 不得改变 public IDs。
- Worker failure 不得污染 orchestrator branch。失败工作保留在其 job 和 branch 下，直到显式 merge 或 discard。
- Tool availability changes 是 runtime state。它们可以被 checkpoint 用于 replay，但不是 durable Agent Identity 的一部分。

## 身份、授权与能力策略

GoDex 2.0 必须区分 human/channel identity 和 agent identity。Approval 与 capability decision 应是显式 records，而不是 tool call 的隐藏副作用。

### 身份类型

- **UserIdentity**：发起或拥有请求的人类用户或 service account。应包含 stable user ID、display label、trust level，以及可选 organization/team scope。
- **ChannelIdentity**：传递请求的入口表面，例如 CLI、Web、TUI、Feishu、Weixin、HTTP API、ACP、cron、heartbeat。应包含 channel type、channel account、tenant/chat/user identifiers 和 auth state。
- **AgentIdentity**：代表用户行动的 durable agent role 或 profile。
- **AgentInstance**：agent identity 到某个 session branch 和可选 sandbox 的 live runtime binding。

### Approval Authority

Approval authority 是允许 protected action 的权利。它不等同于消息发送者身份。

Approval records 应记录：

- 谁请求动作：`agent_instance_id`、`session_id`、`branch_id`，如果是 delegated work 还要包含 `job_id`。
- 请求来自谁或什么：`user_identity` 和 `channel_identity`。
- 谁批准或拒绝：approving `user_identity`、channel、timestamp、scope、reason。
- 批准了什么：tool name、normalized input summary、affected paths/commands、sandbox、capability。
- approval scope：once、session、branch、sandbox、job 或 configured policy。

Orchestrator 可以为 worker 请求审批，但 approval record 必须声明将消费该权限的 worker job 和 sandbox。

### Capability Downgrade

当请求动作在当前 identity、channel、sandbox 或 policy 下无法安全或完整运行时，必须进行 capability downgrade。

例子：

- 远程 IM channel 请求 mutating shell command，因此 tool 降级为 approval-required。
- Worker 请求 `web`，但 orchestrator 尚未启用或批准 web tools，因此 job 以 capability-required error 阻塞。
- Sandbox 是 read-only，因此 write tools 被隐藏或拒绝。
- Model/provider 不支持 image understanding，因此 image analysis 降级为 attachment metadata only。

Capability downgrade results 必须对模型可见且机器可读：

- `status`：`allowed`、`downgraded`、`blocked` 或 `requires_approval`
- `missing_capabilities`
- `available_alternatives`
- `approval_hint`
- `retry_policy`

Downgrade 优先于静默失败或假装能力存在。

## 目标分层架构

### Entry Layer

CLI、TUI、Web、HTTP API、Feishu、Weixin、ACP 和未来集成只应把外部事件翻译成 runtime requests，不应拥有 agent internals。

职责：

- 认证并规范化 incoming requests。
- 附加 channel/session metadata。
- 把 runtime events stream 回用户。
- 展示 approvals、attachments 和 artifacts。

### Orchestration Layer

Orchestrator 拥有 planning、delegation、progress monitoring 和 result integration。它应能运行，而不需要直接知道 sandbox 如何执行 bash 或 worker 如何保存本地文件。

职责：

- 构建高层 plan。
- 决定何时直接调用 tools，何时 delegate。
- 创建 worker jobs。
- 监控 worker progress。
- Review、merge、reject 或 retry worker outputs。
- 保持主上下文紧凑。

### Agent Identity Layer

这一层定义稳定的 agent brain。

职责：

- Agent profile 和 role。
- System prompt strategy。
- Capability policy。
- Tool exposure strategy。
- Delegation policy。
- Model selection 和 fallback strategy。

这一层应可跨 sandbox 移植。

### Session Graph Layer

这一层拥有 conversation 和 memory topology。

职责：

- Persistent messages 和 runtime events。
- Ephemeral context layers。
- Branch、clone、rollback 和 merge。
- Compaction checkpoints。
- Transcript 和 artifact references。
- 为 model requests 重建 context。

### Worker Runtime Layer

这一层运行具体 delegated jobs。

职责：

- 接收 structured job requests。
- 将 identity/role 绑定到 sandbox。
- 执行 model turns 和 tools。
- 发送 progress events。
- 返回 result summaries、artifacts、diffs 和 errors。
- 支持 cancellation、resume、timeout、review 和 merge hooks。

### Sandbox And Tool Runtime Layer

这一层拥有 execution environments 和 hands。

职责：

- Workspace 和 filesystem isolation。
- Command execution policy。
- Tool bundle availability。
- Artifact capture。
- Environment reset/rebuild。
- Remote 或 local placement。

### Storage Layer

这一层通过接口持久化 sessions、memory、artifacts、jobs、permissions、timelines 和 runtime state。

职责：

- 兼容 1.0 的 JSON backend。
- 提供本地 2.0 可靠性的 SQLite backend。
- 支持未来 DB/cloud backend。
- Migration 和 export/import paths。

## 运行时协议

Runtime communication 应 event-driven 且可 replay。Events、artifacts、changesets、failures 和 retries 必须有稳定 ID，使 sessions 可以重建，worker outputs 可以审计。

### Event Model

每个重要 runtime transition 都应发出 event：

- user message received
- agent turn started/completed
- tool call started/completed/failed
- approval requested/resolved
- worker job created/started/progress/completed/failed
- sandbox created/reused/reset/destroyed
- artifact created/attached
- branch created/merged/rolled back
- compaction checkpoint created

Events 应包含：

- `event_id`、`event_type`、`created_at`
- 适用时包含 `session_id`、`branch_id`、`node_id`
- 适用时包含 `agent_instance_id`、`worker_id`、`job_id`、`sandbox_id`
- 因果字段：`parent_event_id`、`request_id`、`idempotency_key`
- structured payload，以及 redacted model-visible summary

### Artifact Model

Artifacts 是 tools、workers、sandboxes 或 model turns 产生的 durable outputs。

Artifact metadata 应包含：

- `artifact_id`、`kind`、`name`、`mime_type`、`size_bytes`、`sha256`
- producer references：`session_id`、`branch_id`、`job_id`、`tool_call_id`、`sandbox_id`
- storage reference：path、object key 或 external URI
- retention class 和 expiration policy
- visibility：model-visible summary、user-visible attachment、internal-only 或 secret

大型 artifacts 应在 session nodes 中通过 ID 引用，而不是嵌入 messages。

### Changeset Model

Changesets 表示对 workspace 或 session graph 的 proposed 或 applied modifications。

Changeset metadata 应包含：

- `changeset_id`、producer job/worker/sandbox
- base reference：git commit、file snapshot、session node 或 artifact digest
- changed files 或 graph nodes
- summary 和 risk notes
- validation results
- merge status：proposed、applied、rejected、conflicted、reverted

Worker writes 应通过 changesets 回流，而不是不经 review 直接修改 orchestrator 的 trusted branch。

### Failure Model

Failures 必须显式记录，并在可能时支持 resume。

Failure records 应分类：

- provider failure
- tool failure
- permission denial
- capability missing
- sandbox failure
- storage failure
- validation failure
- timeout/cancellation
- merge conflict

每个 failure 应包含：

- retryability：no、retry-same、retry-new-sandbox、retry-after-approval、retry-after-capability-change
- user/action hint
- affected IDs
- 仍然有效的 partial artifacts 或 progress

### Idempotency

可 retry 的 runtime requests 必须接受 `idempotency_key`。

Idempotency 适用于：

- creating jobs
- approving permissions
- creating artifacts
- applying changesets
- creating branches
- starting migrations

相同 key 的重复请求应返回原始结果或安全的 conflict response。不得创建重复 jobs、重复 approvals 或重复 applied changes。

## 工程落地

### Testing Strategy

每个 refactor phase 都必须先有 behavior-preserving tests，再做结构变更。

必需测试层：

- Extracted components 的 unit tests。
- Store、worker runtime、sandbox runtime、tool runtime 的 contract tests。
- 当前 tool schemas 和重要 model-visible outputs 的 golden compatibility tests。
- 从现有 JSON/file sessions 迁移的 migration tests。
- CLI、Web、IM channel runtime、subagent、approval、service install 的 end-to-end smoke tests。

### Compatibility Matrix

每个 phase 都应更新 compatibility matrix，覆盖：

- Entry surfaces：CLI、TUI、Web、HTTP API、Feishu、Weixin、ACP、cron、heartbeat。
- Storage backends：current JSON/files、future SQLite、future DB/cloud。
- Sandbox modes：current local workspace、future disposable local、future remote。
- Worker runtimes：current durable subagent、future local worker、future remote/external worker。
- Tool bundles：core code、web、browser、MCP、desktop、background、package、skill、memory、automation。

Matrix 应把每个组合标为 supported、degraded、unsupported 或 planned。

### Refactor Guardrails

- 除非 migration SPEC 明确改变，否则保持 public tool names 稳定。
- 保持当前 config defaults 可用。
- 没有旧 state reader 时不得迁移 storage formats。
- 文件拆分不得改变 user-visible behavior，除非测试名称明确描述该行为变化。
- 跨 old/new 边界时优先使用 adapters，而不是重写。
- Phase 1 期间保持 `agent.go` 作为 facade，方便 downstream code 渐进迁移。
- 新 interface 至少应有一个当前实现，再添加 future-only abstraction layers。

### API Stability

稳定 API surfaces 包括：

- CLI command behavior 和 flags。
- HTTP API routes 和 SSE event semantics。
- Tool names、required input fields、structured result field names。
- Session IDs 和 channel session addressing。
- Service install/start/status/logs commands。

Breaking changes 必须包含：

- migration note
- compatibility adapter 或 versioned route
- old/new behavior 的 test coverage

## 运维与产品体验

### Doctor

`godex doctor` 应演进为多层诊断入口：

- identity 和 provider readiness
- channel auth
- storage backend health
- sandbox health 和 cleanup
- tool runtime capability checks
- worker queue/job health
- session graph integrity
- artifact retention pressure
- prefix cache/context diagnostics

Doctor output 应同时适合人类和 automation 使用。

### Migration UX

Migrations 应显式，并在可行时可回滚。

预期 UX：

- `godex migrate plan` 展示 source backend、target backend、affected records、risk、estimated size。
- `godex migrate run --dry-run` 只验证，不修改 durable state。
- `godex migrate run` 写入 migration checkpoint。
- `godex migrate rollback` 在 migration 标记为 rollback-safe 时可用。
- Web Settings 展示 migration status 和 last checkpoint。

### Observability

GoDex 应暴露以下运行可见性：

- active sessions、branches、workers、sandboxes、jobs
- event stream lag
- model request count 和 token estimates
- tool call count、latency、failure rate
- memory 和 storage pressure
- restart count 和 watchdog status
- approval queue age

本地部署应提供 logs 和 doctor summaries。更大部署应能导出 metrics/events。

### Retention

Retention 应由 policy 驱动：

- session event retention
- transcript archive retention
- artifact retention by kind and visibility
- sandbox workspace retention
- worker job retention
- approval/audit retention

删除必须尊重 references。保留的 session node 可以保留 artifact metadata，同时在 payload 不再需要时清理大型 artifact payload。

### Performance Targets

初始目标：

- 本地服务在保守 GC 配置下，应能在 300 MiB memory budget 内保持可用。
- Context build 应保持 stable prompt prefixes 对 prefix cache 友好。
- Active branch context 的 session graph lookup 应足够快，支持交互式 chat。
- Worker job status 和 logs inspection 应低成本，不需要加载完整 transcripts。
- Storage backends 应支持长期运行安装的 bounded cleanup 和 indexing。

## 产品表面

GoDex 2.0 概念需要可见产品表面，而不只是内部 APIs。

- **Branch Inspector**：查看 session branches、current head、checkpoints、rollback points、merge records。
- **Worker Inspector**：查看 worker jobs、assigned role、sandbox、progress、failures、artifacts、merge status。
- **Sandbox Inspector**：查看 sandbox lifecycle、workspace path、command policy、tool runtime、resource usage、cleanup action。
- **Artifact Inspector**：查看 artifact metadata、preview、producer、retention policy、references。
- **Context Inspector**：展示 current prompt breakdown、prefix-cache hashes、dynamic runtime sections、memory layers、branch source、tool schema exposure。
- **Approval Surface**：展示 requester、channel identity、agent/worker/job/sandbox IDs、normalized action、scope、downgrade reason。

这些表面应优先在 Web 中存在，并暴露足够的 API shape 给 CLI inspection commands。

## 编排 DSL、Run Model 与生态接口

GoDex 2.0 不应只提供内部 runtime objects，还应提供可导入、可导出、可审计的描述格式。Coze 的 agent/bot 产品化、n8n 的 workflow/run 模型、Dify 的 app/workflow DSL 都说明：平台能力需要能被声明、复用、发布和运行追踪。

### Orchestration DSL

Orchestration DSL 用于描述可复用的 agent 编排单元。它可以先作为 package/skill/workflow manifest 的扩展，后续再稳定为独立 schema。

DSL 应描述：

- metadata：name、version、description、author、trust、compatibility。
- agents：orchestrator role、worker roles、model policy、prompt strategy。
- inputs：用户输入、文件、参数、secret references、channel constraints。
- jobs：worker task template、required bundles、required tools、sandbox policy、write scope、timeout。
- control flow：sequence、parallel、fan-out/fan-in、approval gate、retry、conditional branch。
- memory/knowledge：需要注入的 memory layer、knowledge source、RAG policy。
- outputs：artifacts、changesets、summary、delivery target、merge policy。
- validation：smoke command、test command、quality gate、manual review requirement。

第一阶段不要求完整图形化 DSL。最低目标是让现有 package command、workflow、longtask、subagent job 能逐步映射到同一组概念。

### Run Model

Run Model 描述一次编排执行如何被追踪。它补齐 `session_id`、`branch_id`、`job_id` 之外的运行维度。

核心 ID：

- `run_id`：一次 orchestrated execution，可能包含多个 jobs。
- `step_id`：run 内的一个 deterministic step。
- `attempt_id`：某个 step/job 的一次尝试。
- `tool_call_id`：一次 tool invocation。
- `changeset_id`：一次 proposed/applied modification。
- `artifact_id`：一次 durable output。

关系规则：

- 一个 `run_id` 属于一个 `session_id` 和一个 source `branch_id`。
- 一个 run 可以创建多个 jobs，每个 job 可以有多个 attempts。
- Retry 必须创建新的 `attempt_id`，但保留原 `job_id`。
- Tool calls、artifacts、failures、changesets 都必须能追溯到 `run_id` 和 `attempt_id`。
- Run completion 不等于 changeset merge。Merge 是单独的 review/apply 结果。

### Knowledge vs Memory

GoDex 2.0 应明确区分 knowledge 和 memory。

- **Knowledge**：外部或项目资料库，通常来自文档、代码、网页、数据库、上传文件。它强调可检索、可引用、可更新。
- **Memory**：运行过程中沉淀的长期偏好、事实、工作流、风险、经验和用户约定。它强调跨 session 的行为改进和上下文压缩。
- **Session Context**：当前 branch 上为了完成任务构建的短期上下文，包含 messages、runtime state、selected memory、selected knowledge snippets。
- **Artifact**：工具或 worker 产生的具体输出，不自动变成 knowledge 或 memory。

规则：

- Knowledge 可以被 RAG pipeline 检索，但不应自动写入 durable memory。
- Memory candidate 必须经过提取、去重、审计或用户确认策略。
- Worker 可以读取 knowledge，但写入 memory 应由 orchestrator 或 memory policy 决定。
- Context Inspector 应分别展示 memory layers 和 knowledge snippets，避免混淆来源。

### Template And Package Gallery

GoDex 2.0 应把 role、workflow、sandbox policy、tool policy、knowledge binding 和 validation gate 作为可复用资产。

Package/Gallery 资产可以包括：

- Agent role template。
- Worker role template。
- Orchestration DSL template。
- Sandbox policy template。
- Tool policy template。
- Knowledge connector template。
- Validation/smoke template。
- Web surface extension metadata。

安装 package 不应默认授予高风险能力。Package 应声明 requested capabilities，由 doctor/security review 展示风险，由用户或 policy 决定启用范围。

### MCP Inbound And Outbound

MCP 应同时支持 inbound 和 outbound。

- **Outbound MCP**：GoDex 作为 MCP client，调用外部 MCP resources/tools，把它们纳入 tool runtime。
- **Inbound MCP**：GoDex 把自身能力暴露为 MCP server，让其它 agent/runtime 调用 GoDex 的 jobs、workflows、knowledge、artifacts 或 context inspection。

Inbound MCP 初始应优先暴露低风险、可审计能力：

- list available workflows/packages/roles
- start job with idempotency key
- inspect job/run status
- read artifact metadata or approved artifact payload
- inspect context summary

高风险能力，如 apply changeset、shell execution、memory mutation、approval resolution，必须经过 capability policy 和 approval authority。

## 迁移计划

### Phase 1：行为保持的 Agent 重构

目标：在不改变用户可见行为的前提下降低 `agent.go` 耦合。

抽取清晰模块：

- Agent construction 和 dependency wiring。
- Tool registry 和 bundle registration。
- Session transcript state。
- Skill/session activation facade。
- Package registry facade。
- Permission review facade。
- Subagent tool controller。
- Context inspection 和 compaction bridge。

验收标准：

- `go test ./...` 通过。
- 现有 CLI/Web/IM/subagent flows 保持可用。
- Public tool names 和 tool schemas 保持兼容。
- `agent.go` 变成薄 composition root 和 facade。

### Phase 2：Sandbox Boundary

目标：让 workspace/tools execution 可 attach、可替换。

引入显式 sandbox abstractions：

- Sandbox identity 和 lifecycle。
- Workspace filesystem view。
- Tool runtime binding。
- Artifact 和 temp storage policy。
- 匹配当前行为的 local sandbox implementation。

验收标准：

- 当前 local workspace behavior 仍是默认行为。
- Worker jobs 可以通过 ID 引用 sandbox。
- Sandbox 可以重建而不改变 Agent identity。

### Phase 3：Worker Runtime Protocol

目标：让 workers 成为一等 runtime，而不是只作为 tool implementation detail。

引入 structured worker interfaces：

- Job request。
- Progress event。
- Result 和 artifact contract。
- Capability inheritance。
- Review/merge contract。

验收标准：

- 当前 durable subagent jobs 通过 worker runtime interface 实现。
- Orchestrator 可以通过同一 contract 派发到 local GoDex workers，未来 remote workers 也使用该 contract。

### Phase 4：Session Graph

目标：用 branchable context state 替代线性 session mutation。

引入：

- Session node IDs。
- Branch heads。
- Clone/rollback/merge operations。
- 作为 graph nodes 的 compaction checkpoints。
- Worker branch handoff 和 merge。

验收标准：

- 现有 sessions 可读取和迁移。
- Mainline chat 仍像普通线性 conversation 一样工作。
- Worker explorations 可以在 cloned branches 上发生。

### Phase 5：Storage Backend Abstraction

目标：让 session state 独立于存储介质。

引入 repository interfaces 和 backend implementations：

- JSON backend for compatibility。
- SQLite backend for local reliability。
- Export/import tools。
- Storage capability diagnostics。

验收标准：

- 当前 JSON/file state 继续支持。
- 新代码使用 store interfaces，而不是直接路径。
- SQLite 可以启用而不改变 entry/channel code。

## 兼容规则

- 迁移期间保持当前 1.0 commands、HTTP APIs、Web flows、tool names 和 default local storage 可用。
- 不要求本地用户部署 distributed infrastructure。
- 不一次性移动全部 state；使用 adapters 和 migration paths。
- Worker 和 sandbox features 在 local implementation 稳定前保持 optional。
- 除非 SPEC 明确变更，否则保持当前 approval 和 security behavior。

## 首次重构的非目标

- 不立即实现 distributed cluster。
- 不立即要求 cloud storage。
- 不替换全部当前 tools。
- 不强制迁移到 SQLite。
- 除非修复明确 compatibility bug，否则不改变 normal chat、Web UI、IM channels 或 CLI 行为。

## 第一个 SPEC 目标

第一个 implementation SPEC 应只聚焦 Phase 1：保持行为不变地拆解 `internal/agent/agent.go`。

推荐交付物：

- `agent.go` 继续作为 public facade 和 composition root。
- 新文件按 subsystem 拆分职责。
- 只有在能改善 ownership clarity 时才移动现有 tests。
- 新 tests 断言 behavior compatibility，而不是内部文件布局。
