# GoDex 能力增强与 Agent 基座产品化规划

## 背景

GoDex 当前已经具备可用的单实例 Agent 能力：Web Chat、TUI、package / skill / command / role 生态、subagent、timeline、approval、安全 profile、服务化运行、workspace 隔离、存储治理等。但后续目标不只是把 GoDex 做成一个聊天 Agent，而是做成一个可扩展的 **Agent 基座**。

这个基座需要支撑更多产品形态：

- 多个 GoDex 运行实例的统一管理和观测。
- 面向项目、任务、文档、学习资料等业务对象的 Agent 工作台。
- 可安装、可诊断、可卸载的能力包和 UI 扩展。
- 与 Claude Code 等外部 Agent 生态兼容。

灵感来源之一是 OpenAI Symphony：将 issue tracker / task board 作为 Agent 的 control plane，而不是让用户在多个交互式 session 之间来回切换。

参考文章：

- https://openai.com/index/open-source-codex-orchestration-symphony/

## 当前进展

### 已完成能力

- Web Chat / TUI / CLI 共用 session 和 command 后端。
- `godex service` 支持服务化运行。
- Provider / model / settings 可在 Web UI 中配置。
- Package 体系支持：
  - skills
  - prompts
  - commands
  - roles
  - smoke tests
  - permissions / capabilities / tool policy
- Skill 体系支持：
  - `SKILL.md`
  - catalog
  - suite / child skill
  - normalize 状态
  - compatibility diagnostics
- Subagent 体系支持：
  - durable subagent job
  - progress / timeline
  - role / objective / display title
  - max turns 诊断
  - workspace isolation
  - git worktree / dirty overlay / readonly isolation
- 安全体系支持：
  - approval
  - security profile
  - shell risk classification
  - workspace boundary
- 存储治理支持：
  - `doctor storage`
  - `gc`
  - browser cache 清理
  - session checkpoint prune
  - artifact / web_fetch spill 治理

### 当前短板

- GoDex 仍以单实例、单 workspace、单 Web UI 为主要产品形态。
- Chat session 仍然是最核心的交互对象，业务对象还不是一等实体。
- 多个 GoDex runtime 无统一主控面板。
- Package 还不能声明一级 UI app。
- 外部生态导入能力还不完整，例如 Claude Code 的 skills / commands / agents。
- 学习辅助、知识库、任务工作台等非 coding 场景缺少专门 UI。

## 产品目标

GoDex 后续应演进为一个中心化、可扩展的 Agent 基座。

核心变化：

```text
从：

User -> Chat Session -> Agent -> Tools / Skills / Subagents

演进为：

App / Workspace / Task / Document / Knowledge Object
  -> Agent Session / Subagent Job / Automation
  -> Runtime Node
  -> Tools / Skills / Packages / Knowledge
```

Chat 不再是唯一入口。Chat 是业务工作台里随时可展开的 Agent 输入入口。

## 核心设计决策

### 主控板采用中心化服务

GoDex 的主控板采用中心化服务模式：

- 中心服务由 `godex service` 启动。
- 其它 GoDex runtime node 可以自动或手动注册到中心服务。
- 中心服务提供统一 Web UI，用来管理、观测和协调多个 node。
- 每个 node 仍然保留自己的 workspace、配置、权限、安全边界和本地状态。

中心服务不是简单反向代理，而是一个 control plane。

它负责：

- Runtime node registry
- node heartbeat
- capability report
- session / job / timeline 聚合
- approval 聚合
- package / skill 状态观测
- 跨 node 消息传递
- 共享文档和知识库入口

它不应该默认绕过 node 本地安全策略。跨 node 执行仍需要按目标 node 的 safety profile 和 approval 策略处理。

## 关键概念

### Runtime Node

Runtime Node 表示一个独立运行的 GoDex 实例。

字段建议：

```yaml
id: local-project-a
name: Local Project A
endpoint: http://127.0.0.1:8088
workspace_dir: /Users/me/code/project-a
godex_home: /Users/me/.godex
status: online
trust_profile: guarded-local
capabilities:
  - chat
  - tools
  - subagent
  - packages
  - browser
active_sessions: 2
active_jobs: 4
pending_approvals: 1
last_seen_at: 2026-05-02T10:00:00Z
```

Node 类型：

- `local`：本机项目目录。
- `remote`：云服务器或远端机器。
- `project`：绑定具体项目 workspace。
- `app`：绑定某个应用数据目录，例如学习资料库。

### Control Plane

中心化主控服务。

职责：

- 管理 node 注册。
- 聚合 node 状态。
- 聚合 session、job、subagent、timeline。
- 提供跨 node 搜索和筛选。
- 统一展示 approvals。
- 发起远程任务或对话。
- 展示存储、健康、能力、版本差异。

非职责：

- 不替代 node 本地权限判断。
- 不直接读取 node 文件系统，除非目标 node 暴露受控 API。
- 不把所有 node 的 session history 合并成一个不可解释的大上下文。

### App

App 是 GoDex Web UI 的一级工作台。

内置 app 示例：

- Chat
- Control Plane
- Workspaces
- Agents
- Tasks
- Knowledge
- Skills
- Settings
- Study Assistant

Package 后续可以声明 app capability，但第三方 app 的 UI 运行需要分阶段开放。

### App Object

App Object 是业务对象，例如：

- task
- ticket
- document
- note
- question
- review plan
- learning material
- flashcard
- workspace

Agent turn 应该能绑定到 App Object。

这样可以表达：

```text
对这个学习资料做总结
针对这个疑点继续追问
根据这个复习计划生成今天的练习
对这个任务启动一个远程 agent
```

而不是所有事情都落到一个普通 chat session。

## 需求整理

### 1. 多 GoDex 实例统一管理

目标：

- 一个中心化 Web 主控板管理多个 GoDex runtime。
- runtime 可以在本地不同项目目录、云服务器、其它设备上运行。
- 中心服务可以观测每个 runtime 的健康、进展、timeline、approval、session、job。

核心能力：

- 手动注册 node。
- 自动注册 node。
- node heartbeat。
- node capability report。
- node token / trust 管理。
- node 离线检测。
- 多 node session / job / approval 聚合。
- 向指定 node 创建任务或打开对话。

### 2. Control Plane Orchestration

目标：

- 替代“打开多个终端 / 多个浏览器 tab 手动盯 Agent”的体验。
- 不只是展示 Agent 在做什么，而是把任务源、调度、workspace、runner、重试、handoff 和观测串起来。
- 让用户管理工作，而不是监督每个 Agent 的每一步。

Symphony SPEC 的核心不是 dashboard，而是一个长期运行的 scheduler / runner：

- 从 issue tracker 持续读取候选任务。
- 为每个 issue 创建确定性的隔离 workspace。
- 按并发、状态、依赖和优先级调度 Agent。
- 对 running issue 做 reconcile、stall detection、retry backoff。
- 用仓库内 `WORKFLOW.md` 保存 prompt、runtime config、hooks 和 tracker 配置。
- Dashboard 只是可选 status surface，用来呈现 orchestrator state。

GoDex 的 P2 因此不应只做“多 node 状态面板”，而应升级为 “Control Plane Dashboard + Orchestrator Surface”。

核心视图：

- Node 列表：在线状态、workspace、版本、capabilities、trust profile。
- Work queue：候选任务、blocked 任务、优先级、来源、目标 node。
- Running runs：当前运行中的 issue/task/app object、attempt、turn、workspace、agent。
- Retry queue：失败原因、backoff、下次重试时间、attempt。
- Reconciliation：因状态变化、terminal state、stall timeout 被停止的 run。
- Pending approvals：按 node/workspace/session/task 聚合。
- Recent failures：workflow/config、workspace、agent session、tool approval、tracker API。
- Timeline stream：跨 node、跨 task、跨 run 的事件流。
- Resource summary：token、runtime、concurrency、rate limit、storage warnings。

核心操作：

- 手动 refresh tracker / task source。
- 暂停 / 恢复调度。
- 对单个 task 触发 retry / cancel / release。
- 打开 task workspace / session / timeline。
- 修改 workflow 后观察 hot reload 结果。
- 从中心向指定 node 派发一次手动任务。

### 3. Package 生态继续增强

目标：

- Package 不只是能力包，也可以声明业务 app 所需的资源。
- 但 v1 不允许第三方 package 直接注入任意前端 JS。

短期扩展：

```yaml
resources:
  skills:
    - skills/study/SKILL.md
  commands:
    - commands/summarize.yaml
  roles:
    - roles/tutor.yaml
  prompts:
    - prompts/review-plan.md
  docs:
    - docs/usage.md
  assets:
    - assets/template.md

app:
  kind: builtin
  id: study-assistant
  label: Study
  config:
    default_role: tutor
```

长期扩展：

- sandboxed UI extension
- app manifest
- app-specific schema
- app-specific storage
- package marketplace

### 4. Claude Code 生态兼容

目标：

- 支持导入 Claude Code 的 skills / commands / agents。
- 导入结果落到 GoDex package，而不是零散复制。

命令建议：

```bash
godex import claude --source .claude --dry-run
godex import claude --source .claude
godex import claude --source ~/.claude
```

映射关系：

```text
.claude/skills/*/SKILL.md
  -> GoDex skill

.claude/commands/**/*.md
  -> GoDex package command

.claude/agents/**/*.md
  -> GoDex package role

.claude/settings*.json
  -> diagnostics / compatibility warnings
```

原则：

- parse / convert 默认不调用 LLM。
- LLM normalize 必须手动触发或显式 opt-in。
- hooks 不自动执行。
- MCP 不自动启用。
- permissions / allowed-tools 转成 GoDex tool policy 和 warning。

### 5. 学习辅助网页应用

目标：

做一个以学习对象为中心的网页应用，而不是 Chat 套壳。

核心对象：

- 学习资料
- 笔记
- 疑点备忘
- 复习计划
- 知识点
- 练习题
- 记忆卡片

核心能力：

- Agent 整理学习资料。
- Agent 辅助整理学习笔记。
- Agent 记录和追踪疑点。
- Agent 根据艾宾浩斯曲线辅助复习。
- Agent 生成练习题和讲解。
- Agent 在任意对象上作为侧边栏 / 弹窗入口被唤起。

交互原则：

- 工作台主对象不是 Agent。
- Agent 是随时展开的辅助输入入口。
- Agent turn 应绑定具体学习对象和上下文。
- 学习记录和复习计划应结构化保存。

## 阶段性方案

### P0：App Shell 与导航扩展

目标：

- Web UI 从 Chat 单中心演进为 App Shell。
- Chat、Skills、Settings、Control Plane 都作为一级 app。

任务：

- 定义 `AppRegistry`。
- 抽象一级导航。
- 让 Chat 成为 `chat` app。
- Settings / Skills 迁移为 app entry。
- 预留 builtin app 插槽。

验收：

- 现有 Chat 功能不回退。
- 一级导航可以通过 registry 声明。
- 不引入第三方 UI 执行能力。

### P1：Runtime Node Registry

目标：

- 中心服务可以登记和观测多个 GoDex runtime。

任务：

- 增加 node identity。
- 增加 node registration API。
- 增加 node heartbeat API。
- 增加 node capability report。
- 增加手动注册配置。
- 增加中心服务 node registry 存储。

建议 API：

```text
POST /control/nodes/register
POST /control/nodes/{id}/heartbeat
GET  /control/nodes
GET  /control/nodes/{id}
DELETE /control/nodes/{id}
```

验收：

- 本地启动两个 GoDex service，可以注册到中心。
- 中心 UI 能看到 node 在线状态、workspace、版本、capabilities。

### P2：Control Plane Dashboard + Orchestrator Surface

目标：

- 主控板不只是统一展示多个 node 的 Agent 进展，而是成为任务级 Agent 编排的操作面。
- 对齐 Symphony 的核心抽象：issue/task source、workflow contract、workspace manager、orchestrator、agent runner、retry/reconcile、status surface。
- GoDex 版本需要保留自身优势：多 node、package/skill/role、approval、安全 profile、durable timeline、subagent。

任务：

- 新增 Task Source 抽象：
  - v1 支持手动任务和 GoDex 内部 task。
  - 后续支持 Linear / GitHub Issues / 飞书任务 / 本地 Markdown backlog。
  - 统一字段：`id`、`identifier`、`title`、`description`、`state`、`priority`、`labels`、`blocked_by`、`url`。
- 新增 Workflow Contract：
  - 借鉴 Symphony 的 `WORKFLOW.md`。
  - GoDex 可命名为 `GODEX_WORKFLOW.md` 或沿用 `WORKFLOW.md`。
  - 包含 tracker/task source、polling、workspace、hooks、agent、safety、prompt template。
  - 支持热加载，非法配置保留 last-known-good。
- 新增 Orchestrator State：
  - `unclaimed`
  - `claimed`
  - `running`
  - `retry_queued`
  - `released`
  - `completed_for_now`
  - 状态由中心 orchestrator 单点修改，避免重复派发。
- 新增 Workspace / Run Attempt 视图：
  - 每个 task/run 有 workspace path、isolation mode、attempt、started_at、last_event、last_error。
  - 对 Git repo 复用 GoDex 已有 worktree / dirty overlay / readonly isolation。
- 新增调度和 reconciliation：
  - 按 active/terminal state、priority、created_at、blocked_by、node capability、concurrency 派发。
  - running run 周期性检查 task state。
  - terminal/non-active 任务停止 active run。
  - stall timeout 触发 kill/retry。
  - 正常结束后，如果 task 仍 active，可以 continuation retry，而不是假定完成。
- 新增 Retry Queue：
  - 指数退避。
  - 明确失败分类：workflow/config、tracker、workspace、hook、agent session、tool approval、timeout/stall。
  - UI 显示下次重试时间和手动 retry/release 操作。
- 新增 Dashboard：
  - 聚合 sessions、durable subagent jobs、pending approvals、timeline 最近事件。
  - 支持按 node / workspace / task source / state / status / type 过滤。
  - 支持从中心向指定 node 创建 session、发送消息、派发手动 task。
- 新增 Operator API：
  - `GET /control/state`
  - `POST /control/refresh`
  - `GET /control/tasks`
  - `POST /control/tasks`
  - `POST /control/tasks/{id}/retry`
  - `POST /control/tasks/{id}/cancel`
  - `POST /control/tasks/{id}/release`
  - `GET /control/runs`
  - `GET /control/runs/{id}`
  - `GET /control/retry-queue`

验收：

- 一个页面能看见多个 node 的 running jobs、retry queue、pending approvals、recent failures。
- 可以手动创建一个 task，并派发到指定 node。
- Orchestrator 能避免同一个 task 被重复派发。
- workflow/config 错误不会让服务崩溃，会阻止新派发并在 UI 显示。
- task terminal/non-active 后，active run 会被停止或释放。
- retry/backoff/stall detection 在 UI 可见。
- pending approval 能显示来源 node、workspace、task、session。
- 不同 node / task / run 的 timeline 不混淆。

### P3：跨 Node 消息与任务分发

目标：

- 中心可以把任务投递给指定 node。

任务：

- 定义 remote session create / submit message 协议。
- 定义 remote task/job dispatch 协议。
- 支持任务状态回传。
- 支持失败重试和离线提示。

验收：

- 从中心 UI 选择 node，创建一个任务。
- 任务在目标 node 执行。
- 中心可以看到执行进展和结果。

### P4：Package App Capability

目标：

- Package 可以声明 app 所需的配置和资源。

任务：

- 扩展 `godex.package.yaml`。
- 支持 builtin app config。
- Package quality report 增加 app diagnostics。
- Skills 页面展示 package app capability。

验收：

- package 可以声明自己为某个 builtin app 提供能力。
- app 能读取 package 提供的 prompts / roles / skills / docs。

### P5：Claude Code Import

目标：

- 实现 `godex import claude`。

任务：

- 扫描 `.claude/skills`。
- 扫描 `.claude/commands`。
- 扫描 `.claude/agents`。
- 解析 settings 并生成 diagnostics。
- 生成 GoDex package。
- 支持 `--dry-run`。

验收：

- 导入后能在 Skills / Packages / Commands / Roles 页面看到资源。
- 不自动执行 hooks。
- 不自动启用 MCP。
- 不自动触发 LLM normalize。

### P6：Study Assistant App

目标：

- 做第一个非 coding app，验证“工作台为主，Agent 为辅助入口”的产品形态。

任务：

- 学习资料管理。
- 笔记管理。
- 疑点备忘。
- 复习计划。
- 艾宾浩斯复习调度。
- Agent 侧边栏。
- 与知识库 / memory 分层集成。

验收：

- 用户可以导入资料。
- 用户可以让 Agent 总结资料。
- 用户可以保存疑点。
- 系统可以生成复习计划。
- Agent 可以基于当前资料 / 笔记 / 疑点回答。

## 架构草图

```text
                      +--------------------------+
                      |  GoDex Control Service   |
                      |  godex service --control |
                      +------------+-------------+
                                   |
          +------------------------+------------------------+
          |                        |                        |
          v                        v                        v
+------------------+    +------------------+    +------------------+
| Runtime Node A   |    | Runtime Node B   |    | Runtime Node C   |
| local project A  |    | local project B  |    | cloud server     |
+--------+---------+    +--------+---------+    +--------+---------+
         |                       |                       |
         v                       v                       v
  sessions/jobs           sessions/jobs           sessions/jobs
  timeline                timeline                timeline
  approvals               approvals               approvals
  packages                packages                packages
```

## 开发计划建议

优先级建议：

1. P0 App Shell
2. P1 Runtime Node Registry
3. P2 Control Plane Dashboard
4. P5 Claude Code Import
5. P4 Package App Capability
6. P6 Study Assistant App
7. P3 跨 Node 任务分发

原因：

- App Shell 是所有新 UI 的入口基础。
- Node Registry 是中心化主控的基础。
- Dashboard 可以尽快验证多实例管理价值。
- Claude Import 是生态兼容的独立增量。
- Study Assistant 需要 App Shell 和 app context injection 作为前置。
- 跨 Node 任务分发涉及权限和安全，应在观测能力稳定后再做。

## 风险与待确认问题

### 安全风险

- 中心服务不能绕过 node 本地审批。
- 远程 node 注册需要 token / trust 机制。
- 云端 node 和本地 node 的权限差异必须显式展示。

### 数据边界

- 哪些知识可以跨 node 共享？
- 哪些 memory 只属于本地 workspace？
- 学习资料是否属于用户级私有数据？

### UI 扩展边界

- v1 是否只支持 builtin app？
- 第三方 package 是否允许声明 UI？
- 如果允许，是否使用 iframe / sandbox？

### 控制面协议

- node 是主动 push 状态，还是中心 pull？
- 离线 node 的任务是否排队？
- node 版本不一致时如何处理？

## 下一步

建议下一步先写 P0 + P1 的详细实施计划：

- Web App Shell registry。
- Runtime node identity。
- Node registration / heartbeat API。
- Control service 配置。
- 中心 UI 的 Nodes 页面。

这一步完成后，GoDex 就具备从单实例 Agent 工具演进为多实例 Agent 基座的最小产品骨架。
