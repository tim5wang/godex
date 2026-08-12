# GoDex 能力增强与 Agent 基座产品化规划（修订版 v2）

> 状态：Historical（产品化规划线，**非 P0-P6 分期定义来源**；P0-P6 以 `docs/godex-optimization-roadmap.md` 为准）

> 本计划基于 v1 版本（`capability-enhancement-v1.md`）的用户反馈和代码审核修订。
> 核心调整：砍掉过度设计的 Orchestrator、替换 RO I 低迷的 Study Assistant、补入 UX 打磨工作。

## 背景（不变）

GoDex 当前已经具备可用的单实例 Agent 能力：Web Chat、TUI、package / skill / command / role 生态、subagent、timeline、approval、安全 profile、服务化运行、workspace 隔离、存储治理等。后续目标是演进为一个可扩展的 **Agent 基座**。

这个基座需要支撑更多产品形态：

- 多个 GoDex 运行实例的统一管理和观测。
- 面向项目、任务、文档等业务对象的 Agent 工作台。
- 可安装、可诊断、可卸载的能力包和 UI 扩展。
- 与 Claude Code 等外部 Agent 生态兼容。

## 当前进展（2026-05-04）

本轮已在 `capability-enhancement-v1` 分支落地以下阶段性能力：

- **P0 App Shell**：已完成内置 `AppRegistry`，Chat、Automation、Nodes、Notes、Skills、Memory、Settings 统一以 builtin app 注册和路由。
- **P1 Node Registry**：已完成轻量 node identity、register/heartbeat API、只读 Nodes 页面、手动 nodes 配置和离线检测。
- **P2 Package 生态增强**：已完成 package app capability 的基础解析/展示和 doctor diagnostics；仍不支持第三方前端 JS 注入。
- **P3 UX 打磨**：已完成页面级 ErrorBoundary、部分空状态、aria-label/可访问性补齐、审批主路径可见性和 IM 审批详情增强。
- **P4 Claude Code Import**：已完成 `godex import claude`，支持 `.claude/skills`、`.claude/commands`、`.claude/agents` 的 deterministic parse/convert 和 dry-run diagnostics。
- **P5 Notes Demo App**：已完成 Notes builtin app、本地 Markdown 存储、搜索/标签、`/note` 命令、Chat 中保存 assistant 输出到 note 和 current note context。
- **Context 成本优化**：已完成候选 memory count cache、紧凑 memory 注入、动态 ledger runtime message、token 估算调整和默认简洁输出指令。

仍保持不做的范围：跨 node 任务派发、完整 Orchestrator、第三方 UI 注入、Study Assistant 完整学习系统。

## 设计原则

1. **单实例是主力形态，多实例是扩展能力。** 所有改动不能以牺牲单实例体验为代价。
2. **看得见的改进大于看不见的架构。** 用户体验打磨（首次运行、审批可见性、错误处理）的 ROI 高于分布式基础设施。
3. **用最简方案验证假设。** 不做超出当前已知需求的抽象。App Object 用一个小 demo 验证，而不是做一个完整的学习系统。
4. **安全不是事后补的。** 跨 Node 执行等涉及远程能力的特性，必须有完整的安全模型后再实现。

## 架构总览（调整后）

```text
+------------------------------------------------------+
|  GoDex Web UI (App Shell + 内置 Apps)                |
|  Chat | Automation | Skills | Memory | Settings |    |
|  Nodes (read-only) | Notes (demo)                     |
+------------------------------------------------------+
|  GoDex Control Service (轻量)                         |
|  Node Registry | Node Status (read-only)              |
+------------------------------------------------------+
|  Runtime Node A     |  Runtime Node B                |
|  (local project)    |  (local/remote)                |
+------------------------------------------------------+
|  Agent / Skills / Packages / Subagents / Approvals    |
+------------------------------------------------------+
```

关键变化 vs v1：
- **取消了 Orchestrator / Workflow Contract / Retry Queue / Reconciliation** 等 Symphony 完整实现
- **Node Dashboard 改为 read-only 观测面板**，不做远程执行
- **新增 UX 打磨阶段**
- **Study Assistant 替换为 Notes Demo App**

## 阶段性方案

### P0：App Shell 与导航扩展 （优先级：最高）

**目标：** Web UI 从 Chat 单中心演进为 App Shell，Chat、Skills、Settings、Automation、Memory 都作为一级 app。

**任务：**

- 定义 `AppRegistry`（内置，不开放给第三方）
- 抽象一级导航（当前 App.tsx 已使用 lazy-loaded routes + sidebar，迁移到 registry）
- Chat 作为 `chat` app
- Settings / Skills / Memory / Automation 迁移为 app entry
- 预留 builtin app 插槽（仅向后兼容，不引入第三方 UI 执行能力）
- 前置懒加载保持现有性能

**验收：**

- 现有 Chat 功能不回退
- 一级导航可以通过 registry 声明
- 不引入第三方 UI 执行能力
- 侧边栏宽度可调整（已有 `useResizableWidth` hook，保留）

**估算：** ~2-3 天

---

### P1：轻量 Node Registry + Read-Only Dashboard

**目标：** 中心服务可以登记和观测多个 GoDex runtime。**不做远程执行，不做编排。**

**设计选择：**

- 这是观测面板不是控制面板。不涉及：任务派发、远程 session、跨 node 执行、审批转发。
- 用最简方案：`~/.godex/nodes.yaml` 或 `config.yaml` 声明 + HTTP POST heartbeat。
- 不做 service discovery / consensus / 分布式锁——这些都是不需要的复杂化。

**任务：**

- 增加 node identity（启动时生成 UUID，持久化在 state dir）
- 增加 node registration API：`POST /control/nodes/register`
- 增加 node heartbeat API：`POST /control/nodes/{id}/heartbeat`
- Node 信息包括：id、name、workspace_dir、godex_home、status (online/offline)、version、capabilities（列举可用特性）
- 中心服务 node registry 存储（JSON 文件即可）
- 中心 UI 只读 Nodes 页面：表格展示 name / workspace / status / version / last_seen
- 提供 `GET /control/nodes` 和 `GET /control/nodes/{id}` API
- Node 自动注册（service 启动时调用 register + 定时 heartbeat）
- 离线检测：heartbeat 超时时间（默认 60s）后标记 offline
- 手动注册配置（`nodes` 在 config.yaml 中声明）

**明确不做：**

- ❌ 不向 node 派发任务
- ❌ 不跨 node 执行 Agent
- ❌ 不聚合 session / approval / timeline
- ❌ 不做 node token / trust 管理（推迟到真正需要远程执行时）

**验收：**

- 本地启动两个 GoDex service，可以注册到中心
- 中心 UI 能看到 node 在线状态、workspace、版本、capabilities
- 一个 node 停止后，在心跳超时后标记为 offline
- 不影响现有单实例功能

**估算：** ~5-7 天

---

### P2：Package 生态增强 （拆分自 v1 P4，优先级提高）

**目标：** Package 不只是能力包，也可以声明业务 app 所需的资源。v1 不允许第三方 package 注入任意前端 JS。

**短期扩展：**

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
  id: notes
  label: Notes
  config:
    default_role: assistant
```

**任务：**

- 扩展 `godex.package.yaml` schema 支持 `app` 字段
- 支持 builtin app config 解析
- Skills 页面展示 package app capability
- `godex doctor` 增加 package app diagnostics

**明确不做：**

- ❌ 第三方 UI sandbox / iframe
- ❌ App manifest registry（推迟到有第三方 app 需求时）

**估算：** ~2-3 天

---

### P3：UX 打磨（新增阶段，来自用户反馈和代码审核）

**目标：** 解决已知的首次运行体验、审批可见性、错误处理等问题。这是全计划中 ROI 最高的阶段。

**任务：**

- **首次运行向导**：首次打开 Web UI 时，检测是否有已配置的 active provider。如果没有，显示一个简单的引导页：配置 provider → 测试连接 → 开始使用。参考 VSCode 的"开始"页面设计。
- **审批可见性优化**：当前审批埋没在 Chat Inspector 中。改为在 composer 上方显示持久 banner，"有 X 个审批待处理，点击查看"。
- **React Error Boundaries**：为 ChatPage、SettingsPage、AutomationPage 添加 ErrorBoundary 包裹。当前如果某个页面崩溃，整个 App 白屏。
- **完善空状态和加载状态**：
  - Automation 页面：无 cron job 时显示"创建第一个定时任务"引导
  - Skills 页面：无 package 时显示"安装第一个技能包"引导
  - Memory 页面：无 memory 时显示"用 chat 开始积累"引导
- **UI 可访问性**：在所有图标按钮上添加 aria-label 属性（前次 review 指出的 accessibility 问题）。

**验收：**

- 首次启动 Web UI，无 provider 时看到引导页面
- 配置 provider 后可以立即测试连通性
- 有审批挂起时 composer 上方显示可见的 banner
- 单个页面崩溃不会导致整个应用白屏
- 所有图标按钮有 aria-label
- 空页面有引导 CTA

**估算：** ~5-7 天

---

### P4：Claude Code 生态兼容 （原 v1 P5）

**目标：** 支持导入 Claude Code 的 skills / commands / agents。

**任务：**

```bash
godex import claude --source .claude --dry-run
godex import claude --source .claude
godex import claude --source ~/.claude
```

**映射关系：**

```text
.claude/skills/*/SKILL.md  -> GoDex skill
.claude/commands/**/*.md   -> GoDex package command
.claude/agents/**/*.md     -> GoDex package role
.claude/settings*.json     -> diagnostics / compatibility warnings
```

**原则：**

- parse / convert 默认不调用 LLM
- LLM normalize 必须手动触发或显式 opt-in
- hooks 不自动执行
- MCP 不自动启用
- permissions / allowed-tools 转成 GoDex tool policy 和 warning

**估算：** ~3-5 天

---

### P5：Notes Demo App（替代 v1 P6 Study Assistant）

**目标：** 用最轻量的方式验证"工作台为主，Agent 为辅助入口"的产品形态。不做学习系统，不做艾宾浩斯，不做知识卡片。

**为什么选 Notes：**

- 概念简单：Markdown + title + tags，用户理解零门槛
- 数据模型轻量：不需要 DB schema，`~/.godex/notes/` 下直接存 `.md` 文件即可
- Agent 交互自然：Agent 可以辅助整理、分类、搜索、总结笔记
- 如果后续发现 Notes 使用率低，删除成本极低（一组 flat file + 一个页面）

**任务：**

- 新增 `notes` builtin app：
  - 笔记列表页：标题、摘要、标签、更新时间
  - 笔记编辑页：Markdown 编辑器（可复用已有的 MarkdownRenderer）
  - 标签筛选和全文搜索（`grep -r` 级别即可，不需要 Elasticsearch）
- Agent 侧边栏集成：
  - 在笔记页打开时，Agent 侧边栏显示"当前笔记"上下文
  - Agent 可以响应"总结这篇笔记"、"帮我补充笔记"、"查找相关笔记"
  - Agent turn 绑定到当前笔记 ID（验证 App Object 概念）
- 在 Chat 中使用 `/note` 命令快速创建笔记

**明确不做：**

- ❌ 不实现间隔重复 / 艾宾浩斯
- ❌ 不做知识图谱
- ❌ 不做学习计划
- ❌ 不做卡片盒
- ❌ 不引入数据库依赖

**验收：**

- 用户可以在 Notes app 中创建、编辑、搜索笔记
- 用户可以在 Chat 中提到"总结我当前的笔记"等上下文操作
- Agent turn 能绑定到具体笔记对象并在 timeline 中追踪
- 笔记文件以兼容格式存储在文件系统上（Markdown，用户可直接用其他工具编辑）

**估算：** ~5-7 天

---

### P6（推迟）：跨 Node 任务分发（原 v1 P3）

**状态：推迟到 Phase 3**

**原因：**

- 安全模型不成熟（token / trust / 传输加密 / 离线排队都未定义）
- GoDex 目前没有 RPC 基础设施
- 用户场景尚未验证（谁需要、做什么、为什么不能用 SSH/Webhook）
- P1 上线后根据实际使用 pattern 再评估

**如果未来做：**

- 先从"URL 方式打开远程 GoDex session"开始（SSH tunnel + HTTP 转发）
- 再用简单的 request/response 协议支持远程消息收发
- 最后才考虑完整的任务分发和状态回传

---

### P7（推迟）：完整 Orchestrator（原 v1 P2 的编排器部分）

**状态：推迟到 Phase 4+**

**原因：**

- 这是构建分布式任务调度引擎的工作量（对标 Temporal/Airflow/Symphony）
- GoDex 当前没有已知用户需要这个能力
- 单实例体验尚未完善
- 如果未来需要，应该**拆分为独立项目**"GoDex Orchestrator"，而不是塞入现有的单实例代码库

---

## 开发计划建议

### Phase 2 实施顺序

```text
第 1 周：P0 App Shell + 导航扩展
第 2 周：P1 轻量 Node Registry（简化版）
第 3 周：P2 Package 生态增强
第 4 周：P3 UX 打磨（首次运行向导 + 审批可见性 + 错误处理）
第 5 周：P3 UX 打磨（空状态 + a11y）+ P4 Claude Code Import
第 6 周：P5 Notes Demo App
```

### 调整前后的对比

| 原始优先级 (v1) | 修订后 (v2) | 变化 |
|---|---|---|
| P0 App Shell | **P0** App Shell | ✅ 不变 |
| P1 Node Registry | **P1** 轻量 Node Registry | 🔶 简化，砍掉 trust/security |
| P2 Control Plane Dashboard | **P1** (read-only dashboard，合并到 P1) | 🔴 大幅缩减——砍掉 orchestrator |
| P5 Claude Code Import | **P4** Claude Code Import | 🔶 推迟一位 |
| P4 Package App Capability | **P2** Package 生态增强 | 🔶 提前两位 |
| — | **P3** UX 打磨 | 🟢 **新增** |
| P6 Study Assistant App | **P5** Notes Demo App | 🟢 **替换，大幅缩小范围** |
| P3 跨 Node 任务分发 | **P6** 推迟到 Phase 3 | 🔴 推迟 |
| — | **P7** 完整 Orchestrator | 🔴 推迟到 Phase 4+ |

---

## 风险跟踪

| 风险 | 可能性 | 影响 | 缓解措施 |
|------|--------|------|----------|
| P0 App Shell 引入 regression | 中 | 高 | 回归测试覆盖 Chat 核心流程 |
| P1 多 node 场景实际使用率低 | 高 | 中 | 限制 P1 为 read-only，投入最小 |
| P3 UX 打磨被优先级挤压 | 中 | 中 | 放在 P0-P2 之后立即做，不给"以后再做"的机会 |
| P5 Notes 验证不足 | 中 | 中 | 接受"验证失败"也是有效结果——确认"工作台为主"不是好方向 |
| P4 Claude Import 兼容性差异 | 低 | 中 | `--dry-run` + diagnostics |

---

## 验收总体标准

- ✅ 所有现有 test 通过（`go test ./...`）
- ✅ Web build 通过（`pnpm -C ui/web build`）
- ✅ 单实例 Chat 功能不回退
- ✅ 不引入外部运行时依赖（不依赖外部数据库、外部服务）
- ✅ 每个阶段交付时有明确的前后对比

---

## 附录：被砍掉的功能与原因

| 功能 | 原始阶段 | 砍掉原因 |
|------|---------|---------|
| Orchestrator / Workflow Contract / Scheduler | P2 | 构建分布式调度引擎的复杂度远超已知需求。GoDex 是本地优先工具，单实例用户不需要跨机器编排 |
| Retry Queue / Backoff / Stall Detection | P2 | 这些是分布式系统的基础设施组件。在一个没有跨 node 任务的系统中，它们没有作用目标 |
| Reconciliation State Machine | P2 | 状态机需要 5+ 状态、竞态检测、幂等控制。在当前阶段这是过度工程 |
| Task Source 抽象（Linear / GitHub Issues 集成） | P2 | 第三方 tracker 集成应该在单实例内部先做，而不是作为多实例编排的一部分 |
| Study Assistant（艾宾浩斯/知识卡片/复习计划） | P6 | 完整的学习系统是独立产品。用 Notes Demo App 验证 App Object 概念更轻量、更低风险 |
| 跨 Node 任务分发 | P3 | 安全模型不成熟，场景未验证，RPC 基础设施为空 |
| 第三方 UI 注入 | P4（长期） | iframe sandbox 等机制增加了攻击面和维护成本。v1 仅支持 builtin app |

---

*版本：v2 | 修订日期：2026-05-03 | 基于 v1 审核反馈修订*
