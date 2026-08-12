# UI 用户体验优化 · P0-P6 端到端测试用例

> 状态：Active（基于 `docs/godex-optimization-roadmap.md` 的 Phase 0-6 分期重写，2026-08-12）
> 目标：以「优化 Godex Web UI 用户体验」为任务载体，让一个新 session 的 agent 在执行 UI 优化任务时**自然触发** P0-P6 的核心特性，从而一次性观测这些特性在 UI 上的表现。
> 每个用例 = 一个可复制的提示词 + 预期观测点（对应 roadmap 分期）。
> 建议：3 个用例分别在 3 个新 session 中执行（或同一 session 依次输入），覆盖 Phase 0-6 全部核心特性。

## P0-P6 定义（唯一权威：docs/godex-optimization-roadmap.md）

| 分期 | Roadmap 对应 | 核心特性 | 主要落地位置 |
|------|-------------|---------|-------------|
| **P0** | Phase 0（已完成区） | 记忆类型扩展、笔记↔记忆联动、记忆注入笔记引用、API+UI 展示 | MemoryPage / NotesPage / context 注入 |
| **P1** | Phase 1（P0，基础底座） | 异步 turn/job runtime、Durable event journal、幂等性存储、Worker Lease | Timeline / runner phase / snapshot |
| **P2** | Phase 2（P1，longtask 核心） | 动态并行 DAG、重启恢复、上下文预算管理、AgentGraph 运行时抽象 | LongTasks DAG / Context & Recall |
| **P3** | Phase 3（P2） | 记忆策略模式、记忆 notebook 去重、Agent Identity 解耦 | MemoryPage / Subagents 身份 |
| **P4** | Phase 4（P3） | spawn/send_input/wait、双向通信 iterate、角色→bundle 映射、bundle 继承、写 scope 联动、上下文预算按角色 | Subagents / Subagent timeline / approvals |
| **P5** | Phase 5（P4） | Harness 多引擎抽象、Turn Error 分层、持久化 Map 抽象 | Settings Provider / 错误恢复 |
| **P6** | Phase 6（6.1-6.5） | 安全筛查器、Scope 隔离、Session 树、多引擎热切换、自然语言创建 longtask | Settings Security / Session 树 / fork / longtask plan |

---

## 准备工作（每个用例执行前）

```bash
cd /Users/taiwu.wang/Documents/leader_agent/godex
PATH="/usr/local/bin:$PATH" pnpm --dir ui/web build
/usr/local/go/bin/go run ./cmd/godex serve --addr 127.0.0.1:8088
```

打开 `http://127.0.0.1:8088`，新建一个 Web chat session。

---

## 用例 1：上下文与 Timeline 面板深度体检（覆盖 P0 + P1 + P2 + 6.5）

### 提示词（直接复制到新 session）

```
以优化 Godex Web UI 用户体验为目标，对 Chat 右侧 Inspector 的「Context & Recall」和「Timeline」两个面板做一次深度体验优化审查。

要求：
1. 用自然语言创建一个 longtask（描述你的审查计划即可，不要预先指定 story 细节），让系统把计划拆成 2-3 个并行 story，每个 story 审查一个文件：
   - ui/web/src/features/chat/panels/ContextPanels.tsx
   - ui/web/src/features/chat/panels/TimelinePanels.tsx
   - ui/web/src/lib/timelineUtils.ts
   以及 docs/p0-p4-visualization-design.md 作为设计参考。
2. 逐个列出这两个面板当前「可见的信号」与「缺失/可改进项」：
   - Context & Recall：token 分层堆叠条、阈值警戒色、压缩建议（suggest_compact）、prefix cache、按角色预算；
   - Timeline：类型过滤器、分页/游标、加载与空态降级、事件详情、runner phase 事件。
3. 为验证判断，运行至少 3 个只读检查（grep / glob / read_file），在最终回答中引用关键文件与行号。
4. 回答结构：先给出一段简短进度说明（我会观察 Timeline 的 phase 变化），再运行一次 /memory-digest 生成记忆候选，最后输出「优化建议清单（按优先级）+ 已确认的 2 个具体问题」。
5. 给出验证清单：如何用 tsc -b 验证你的结论（不要用 tsc --noEmit）。
```

### 预期观测点（执行中打开右侧面板 + Memory 页面）

| P 项 | 观测点 | 具体信号 |
|------|--------|---------|
| **P1 韧性** | Timeline 页签 | 出现多个 `runner_phase_changed`（model_request / awaiting_tools / tools_completed / final_response）；longtask 异步执行不阻塞 UI（202 提交） |
| **P2 动态 DAG** | LongTasks 页签 | longtask DAG 图显示节点/边与状态着色；运行中每 3s 刷新；点击节点可看详情抽屉 |
| **P2 上下文预算** | Context & Recall 页签 | token 分层堆叠条 + 各层数值；预算 ≥65% 蓝色 / ≥85% 警戒色；有 suggest_compact 状态与原因 |
| **P0 记忆联动** | Memory 页面 | /memory-digest 创建可 review 的候选（不直接写 durable memory）；候选列表出现 pending 条目 |
| **6.5 自然语言 longtask** | LongTasks 页签 | 纯自然语言描述被拆解成带依赖关系的 stories（US-002 → US-001） |

---

## 用例 2：子 agent 泳道 + 多 Agent 通信 + 角色 bundle 并行审查（覆盖 P3 + P4）

### 提示词（直接复制到新 session）

```
以优化 Godex Web UI 用户体验为目标，使用 longtask 并行审查「Subagent timeline」「Subagents」「LongTasks」三个面板，并验证多 Agent 通信与角色能力边界。

要求：
1. 创建一个 longtask（自然语言描述），拆成 3 个并行的 story，每个 story 委派一个 durable subagent（指定不同 agent_type / 角色）：
   - story A（reviewer，只读）：审查 SubagentTimeline.tsx 的泳道图在 agent 运行中是否会出现「只有一行、图标重叠」的问题（重点看 lane 分组与同时间簇防重叠逻辑）；
   - story B（researcher，只读）：审查 TurnSubagentPanels.tsx 的 Subagents 列表是否完整展示 job identity / role / phase / status / last tool / capability；
   - story C（worker，只读审查 + 可写 scope 只输出建议补丁）：审查 AgentGraphDiagram.tsx 的 DAG 图节点状态着色、运行中刷新、节点点击详情。
2. 每个 subagent 只读检查相关文件，返回「发现的问题 + 改进建议」，不要实际修改代码。
3. 子 agent 完成后，主 agent 用 send_input / iterate 对其中 1 个 story 发起一轮 review 反馈（验证双向通信），再汇总输出按优先级排序的优化建议清单（附文件与行号）。
```

### 预期观测点（执行中打开右侧面板）

| P 项 | 观测点 | 具体信号 |
|------|--------|---------|
| **P3 身份治理** | Subagents 页签 + Timeline | 每个 subagent job 展示 identity、role/agent type、parent turn、phase、status、last tool；Timeline 出现 `subagent_job_updated` / `agent_identity_updated` |
| **P4 角色→bundle** | Subagents 页签 | reviewer/researcher 子 agent 只读（无写工具）；worker 有写 scope 但受限 |
| **P4 上下文预算按角色** | Subagents 页签 | 子 agent 卡片显示角色预算 Tag（reviewer 100K / researcher 50K 等） |
| **P4 双向通信** | Timeline 页签 | send_input 消息进入 PendingInputs 并在 turn 边界注入；iterate 触发 review→fix→re-review 循环事件 |
| **P4 写 scope** | approvals 页签 | worker 写操作若超 scope 触发审批或拒绝；只读角色写操作被拦截 |
| **C1 泳道图（P2 可视化）** | Subagent timeline 页签 | 泳道图多条 lane（每 subagent 一条），事件点分类着色（spawn/send_input/review/iterate），同时间簇不重叠 |
| **A1/A2 DAG（P2 可视化）** | LongTasks 页签 | DAG 图 3 个并行节点 + 状态着色；运行中 3s 刷新；点击节点看详情 |

---

## 用例 3：架构韧性 + 安全 + Session 树体检（覆盖 P5 + P6）

### 提示词（直接复制到新 session）

```
以优化 Godex Web UI 用户体验为目标，对 Settings、审批流、安全审计与 Session 树相关体验做一次体检。

要求：
1. 检查 Settings 页面（ui/web/src/features/settings/SettingsPage.tsx）：
   - Provider / 模型选择区域是否展示模型列表与选择入口（Fetch models）；Chat header 与 Settings 的 provider/model 是否一致（P5 多引擎/模型治理）。
   - Security 区域（security-summary / security-audit）是否展示风险分级、审批记录与审计事件（P6 安全筛查器审计）。
2. 触发一次需要审批的高风险命令（例如 shell 管道执行），观察审批 banner 与安全审计记录（P6 shell 风险分级）。
3. 在当前 session 执行一次 fork（ChatPage 的 forkSession），然后用 /memory 或 Memory 页面确认新 session 与旧 session 的记忆相互隔离（P6 Scope 隔离），再尝试 rollback/merge（P6 Session 树，可只验证 API/UI 入口是否存在）。
4. 检查 Context & Recall 的压缩历史（Compaction 历史页签）是否展示历史压缩记录（P2 上下文预算 + P6 压缩诊断联动）。
5. 输出：已确认的问题（按严重度排序）+ 优化建议清单 + 验证命令（tsc -b / go test）。
```

### 预期观测点（执行中打开对应页面）

| P 项 | 观测点 | 具体信号 |
|------|--------|---------|
| **P5 架构基础设施** | Settings Provider | 模型列表可 fetch 并选择；Chat header 与 Settings 一致；provider test 失败时展示可读错误（不泄露 secret） |
| **P6 安全筛查器** | Settings Security | 安全审计展示 screen_<hook> 事件（score/threshold/outcome）；高风险命令触发审批而非静默执行 |
| **P6 shell 风险分级** | approvals 页签 + Settings Security | 管道/下载执行等 shortcut 被识别为高风险；审批 prompt 展示风险等级与命中原因 |
| **P6 Scope 隔离** | Memory 页面 + fork 后 session | fork 出的 session 记忆与主 session 隔离（互不可见）；Timeline 出现 scope_label |
| **P6 Session 树** | fork 操作 + Session 树入口 | fork 成功（message.success）；rollback/merge 入口存在且可调用（可只验证 API/UI 可用性） |
| **P2 压缩历史** | Compaction 历史页签 | 展示历史压缩记录（mode/latency/前后 token）；双源合并（snapshot_ready + summary） |

---

## 覆盖矩阵

| 用例 | P0 记忆 | P1 韧性 | P2 DAG/预算 | P3 身份/记忆策略 | P4 多 Agent/角色 | P5 架构 | P6 安全/Scope/树 |
|------|:---:|:---:|:---:|:---:|:---:|:---:|:---:|
| 用例 1 | ✅ | ✅ | ✅ | | | | |
| 用例 2 | | | ✅（可视化） | ✅ | ✅ | | |
| 用例 3 | | | ✅（压缩历史） | | | ✅ | ✅ |

## 使用说明

1. 三个用例按 1 → 2 → 3 顺序执行：单 agent 深潜 → longtask 并行多 Agent → 跨页面/架构/安全体检，覆盖 Phase 0-6 全部核心特性。
2. 执行中**不要打断** agent：用例 1 要求先输出进度说明（便于观察 Timeline phase 与 longtask DAG）；用例 2 要求子 agent 只读不改（便于观察泳道/身份/角色能力边界）；用例 3 有真实审批与 fork 操作（便于观察安全审计与 Session 树）。
3. 观测优先级：用例 1 重点看右侧 Inspector（Timeline / Context / LongTasks / Compaction）+ Memory 页面；用例 2 重点看 Subagents / Subagent timeline / LongTasks 三个页签；用例 3 重点看 Settings（Provider / Security）+ approvals + fork 后行为。
4. 若某信号未出现：先区分「特性确实未实现」（如某面板缺空态 CTA，正是本次要暴露的优化点）与「观测方式问题」，再决定是否修复——这正是本套用例的价值：让 P0-P6 的落地程度在 UI 上一目了然。
