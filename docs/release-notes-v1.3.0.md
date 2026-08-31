🇬🇧 **English**

> Status: Historical (v1.3.0 release record)

**v1.3.0 is the "Agent Runtime" release.** It lands the full P0–P6 roadmap (Phases 1–6): the agent loop becomes a platform with a pluggable Harness engine abstraction and per-turn multi-engine switching; long tasks gain restart recovery, per-role context budgets and loop guards; memory grows strategy modes and dedup; scope isolation, a content security screener and a session tree harden the runtime; and the Web UI grows live DAG visualization, compaction history and a subagent timeline.

### 🚀 Agent Runtime Platform
- Harness engine abstraction (5.1): `Harness` interface (Profile/Models/Tools/RunTurn/ResetSession/Close); the default godex engine wraps the existing turn loop unchanged.
- Per-turn multi-engine switching (6.4): each turn can request a non-default engine (via envelope metadata), with automatic session reset on engine switch and a dynamic `RegisterHarness` registry.
- Turn Error layering (5.2): model-call errors classified as Retryable / Transient / NonRetryable with in-turn bounded retries and honest failure messages.
- DurableMap persistence abstraction (5.3): `MemoryMap` + `SQLiteMap` (dir/`<table>.db`) for key-value durable state.

### 🛡️ Agent Identity / Sandbox Decoupling & Scope Isolation
- `Sandbox` interface + `LocalSandbox` implementation (3.3); the agent consumes the sandbox purely through the interface.
- Scope isolation model (6.2, M1–M5): session / personal / org scopes, per-scope memory / todos / temp / artifact dirs, `ResolveWritePath` + scope write interceptor, scope labels on subagent and runner-phase events.

### 🧠 Memory System Upgrade
- Memory strategy modes (3.1): per-turn (default) / agent-only / consolidated (per-turn capture + LLM consolidation with UPDATE/DELETE/ADD), threshold-triggered via `consolidate_after`, with graceful capture-only fallback.
- Memory notebook dedup (3.2): `foldCapture` incremental dedup on same-title `Remember` + capTail tail-preserving truncation.

### ⚡ Long-Task & Runner Resilience
- LongTask restart recovery, context budgets and agent graph (Phase 2): run records persisted, interrupted runs sweep/rebuild on startup, `--resume-run-id` continuation.
- Per-role context budgets (4.6): orchestrator 200K / worker 100K / reviewer 100K / researcher 50K, with automatic rule-based compaction when over budget.
- Runner recovery: reasoning-budget overflow recovery (empty answer + `finish_reason=length`), bounded compaction, stale ledger guard, honest empty-response errors.
- Loop guard: no-mutation spiral detector (research-safe repeatable nudge with generous abort).

### 🤝 Multi-Agent Orchestration
- Role→bundle runtime mapping (4.3) and subagent bundle inheritance / overrides / deactivation (4.4).
- Write-scope ↔ bundle linkage (4.5): `writing` capability bundle, resolve chain (explicit `write_scope` > role > nil), read-only downgrade when no scope.
- `send_input` / `followup_task` queue (4.1) and the `iterate` re-review loop (4.2) for review→fix→re-review cycles.

### 🌳 Session Tree & Natural-Language LongTask
- Session tree (6.3): fork / rollback / merge with a persisted session graph and tree queries.
- Natural-language LongTask creation (6.5): `longtask` tool `plan` action — describe the task and the LLM decomposes it into stories JSON.

### 🔐 Content Security Screener
- Content screener (6.1, minimal shadow): user_input / tool_response content classification; shadow mode records verdicts into the security audit without blocking the pipeline, configurable via `security.screener.*`.

### 🖥️ Web UI — Visualization & Workspace
- AgentGraph DAG diagram + context budget stacked bar (P0).
- Live DAG refresh, compaction history, subagent timeline (P1).
- Task Center → Action Center with drill-down details.
- Subagent swimlane grouping, timeline overlap fix, compaction-history panel fixes, UI three-panel review P1–P2 optimizations.
- Slash commands are now usable directly from the Web UI.

### 🔌 Control Plane / Relay
- Relay phases 1–4: outbound WSS node→center hub with per-node credential auth, relay proxy + streaming, node telemetry snapshots (sessions / longtasks / approvals), and the `node join` / `node exec` / `node forward` CLI jump-host tooling.

### 🔧 Tools & Infrastructure
- Unified workspace filesystem + better compaction; read_file optimization; JSON argument repair; large-file split; improved message sending; atomic release publishing (`make release`).

### 📚 Docs & Engineering
- Optimization roadmap (P0–P6, Phases 1–6), full user-guide rewrite plus code-review and bug fixes, and DeepSeek Harness research notes (plugin-kernel direction).

* * *

🇨🇳 **中文**

**v1.3.0 是「Agent Runtime」版本。** P0–P6 路线图（Phase 1–6）全部落地：Agent 主循环进化为平台——可插拔的 Harness 引擎抽象与逐轮多引擎热切换；长任务获得重启恢复、按角色上下文预算与 loop guard；记忆系统升级策略模式与去重；Scope 隔离、内容安全筛查器与会话树进一步加固运行时；Web UI 新增实时 DAG 可视化、压缩历史与子 Agent 时间线。

### 🚀 Agent Runtime 平台化
- Harness 引擎抽象（5.1）：`Harness` 接口（Profile/Models/Tools/RunTurn/ResetSession/Close）；默认 godex 引擎包装现有 turn 循环，行为不变。
- 逐轮多引擎热切换（6.4）：每轮 turn 可通过 envelope metadata 请求非默认引擎，切换时自动 reset 旧/新引擎；支持动态 `RegisterHarness`。
- Turn Error 分层（5.2）：模型调用错误分类为 Retryable / Transient / NonRetryable，同 turn 内有限重试，失败透出诚实信息。
- DurableMap 持久化抽象（5.3）：`MemoryMap` + `SQLiteMap`（dir/`<table>.db`），统一 key-value 持久状态。

### 🛡️ Agent Identity / Sandbox 解耦与 Scope 隔离
- `Sandbox` 接口 + `LocalSandbox` 实现（3.3），Agent 完全通过接口使用沙箱。
- Scope 隔离模型（6.2，M1–M5）：session / personal / org 作用域，按 scope 划分 memory / todos / temp / artifact 目录，`ResolveWritePath` + 写路径拦截器，subagent 与 runner phase 事件携带 scope label。

### 🧠 记忆系统升级
- 记忆策略模式（3.1）：per-turn（默认）/ agent-only / consolidated（per-turn 捕获 + LLM 合并 UPDATE/DELETE/ADD），`consolidate_after` 阈值触发，模型失败自动降级 capture-only。
- 记忆 notebook 去重（3.2）：同 title `Remember` 增量去重（foldCapture）+ capTail 尾部保留截断。

### ⚡ 长任务与 Runner 韧性
- LongTask 重启恢复 + 上下文预算 + agent graph（Phase 2）：run 记录持久化，启动 sweep/重建 interrupted run，`--resume-run-id` 续跑。
- 按角色上下文预算（4.6）：orchestrator 200K / worker 100K / reviewer 100K / researcher 50K，超预算自动 rule-based 压缩。
- Runner 恢复：reasoning 预算溢出恢复（空回答 + `finish_reason=length`）、bounded compaction、stale ledger guard、诚实空回复报错。
- Loop guard：no-mutation 螺旋检测（可重复提示 + 宽松中止）。

### 🤝 多 Agent 编排
- 角色→bundle 运行时映射（4.3）与子 Agent bundle 继承 / 覆盖 / 停用（4.4）。
- 写 scope 与 bundle 联动（4.5）：`writing` 能力 bundle，解析链（显式 `write_scope` > role > 无），无 scope 自动只读降级。
- `send_input` / `followup_task` 队列（4.1）与 `iterate` re-review 循环（4.2），支持 review→fix→re-review 多轮迭代。

### 🌳 Session 树与自然语言 LongTask
- Session 树（6.3）：fork / rollback / merge + 持久化 session 图与树查询。
- 自然语言创建 LongTask（6.5）：`longtask` 工具新增 `plan` action——描述任务，LLM 自动拆解为 stories JSON。

### 🔐 内容安全筛查器
- 内容筛查器（6.1，最小 shadow 版）：user_input / tool_response 内容分级；shadow 模式只把 verdict 写入 security audit、不阻断主链路；`security.screener.*` 可配置。

### 🖥️ Web UI — 可视化与工作区
- AgentGraph DAG 图 + 上下文预算 stacked bar（P0）。
- 实时 DAG 刷新、压缩历史、子 Agent 时间线（P1）。
- Task Center → Action Center 下钻详情。
- 子 Agent 泳道分组、时间线重叠修复、压缩历史面板缺陷修复、UI 三面板审查 P1–P2 优化。
- Web UI 内可直接执行 Slash Command。

### 🔌 Control Plane / Relay
- Relay phase 1–4：节点出站 WSS 接入中心（按节点凭证认证）、relay 代理 + 流式透传、节点遥测快照（sessions / longtasks / approvals）、`node join` / `node exec` / `node forward` 跳板 CLI。

### 🔧 工具与基础设施
- 统一工作区文件系统 + 更好的压缩；read_file 优化；JSON 参数修复；大文件拆分；消息发送优化；发布脚本原子替换（`make release`）。

### 📚 文档与工程
- 优化路线图（P0–P6，Phase 1–6）、用户指南全量重写 + 代码 review 与 bug 修复、DeepSeek Harness 研究笔记（插件内核方向）。

* * *

### 📦 Assets

构建产物（`make release` 生成，上传后替换为实际链接）：

[godex-v1.3.0-linux-x86-64.tar.gz](https://github.com/tim5wang/godex/releases/download/v1.3.0/godex-v1.3.0-linux-x86-64.tar.gz)
[godex-v1.3.0-mac-apple.tar.gz](https://github.com/tim5wang/godex/releases/download/v1.3.0/godex-v1.3.0-mac-apple.tar.gz)
[godex-v1.3.0-mac-x86-64.tar.gz](https://github.com/tim5wang/godex/releases/download/v1.3.0/godex-v1.3.0-mac-x86-64.tar.gz)
[godex-v1.3.0-win-x86-64.tar.gz](https://github.com/tim5wang/godex/releases/download/v1.3.0/godex-v1.3.0-win-x86-64.tar.gz)

* * *

几点说明：
- 内容基于 `v1.2.0..HEAD` 的 51 个 commit 逐项核对源码后归纳，未夸大。
- 对应 `docs/godex-optimization-roadmap.md` 的 Phase 1–6（P0–P6）全部落地；commit 编号见该文档。
- 发布前请先跑 `./scripts/release_check.sh` 与 `make release`（Makefile 已预设 `VERSION=v1.3.0`）。
