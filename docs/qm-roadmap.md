# GoDex QM 借鉴路线图

> 状态：Superseded（已被 `docs/godex-optimization-roadmap.md` 合并，保留仅作追溯）

## 前言

这份文档不是一次性改造计划。它是一份**长期观察清单**，记录从 QM 项目中学到的设计模式，映射到 GoDex 当前架构，按阶段排列。每个阶段都是独立可交付的，不依赖后续阶段。宁可不做，也不要用"一次性大重写"的方式推进。

**QM 参考版本**：`temp/qm/`，约 76k 行 TypeScript，346 个源文件，45 个 src 模块。GoDex 是 Go 单二进制，约 150k+ 行 Go + 前端。

---

## 第一阶段：低风险基础改进（立即可以做的）

### 1.1 记忆策略模式（Memory Strategy）

**QM 做法**：`memory/strategy.ts` 定义 `MemoryStrategy` 接口，3 种实现（per-turn / scratch-promote / agent-only），外加 `consolidation` 叠加层。工厂函数 `createMemoryStrategy` 组合策略。

**GoDex 当前**：记忆行为固定——`layers.go` 的 `BuildContextLayers` 决定注入什么，`extract.go` 的 turn-end extractor 决定抽取什么。不可配置、不可组合。

**落地路径**：

1. 定义 `MemoryStrategy` 接口（类似 GoDex 的 `Tool` 接口风格）
2. 把 `layers.go` 的 `BuildContextLayers` 和 `extract.go` 的抽取逻辑提取为默认策略
3. 新增 `consolidation` 策略——用 LLM 自动合并/去重/删除候选记忆（参考 QM 的 `consolidation.ts`）
4. 配置化：`godex.json` 中允许用户选择策略组合

**QM 参考文件**：
- `temp/qm/src/memory/strategy.ts`（87 行，接口定义 + 工厂函数）
- `temp/qm/src/memory/strategies/per-turn.ts`（每轮抽取 prompt）
- `temp/qm/src/memory/strategies/consolidation.ts`（合并 prompt，UPDATE/DELETE/ADD 指令）
- `temp/qm/src/memory/strategies/scratch-promote.ts`（两级缓冲 + 定时 promote）

**收益**：减少人工审核负担，记忆系统可演进

### 1.2 幂等性存储（Idempotency Store）

**QM 做法**：`idempotency/idempotency-store.ts` 提供 `once(key, fn)` 和 `committed(key)` 接口。内存缓存 + 持久化 backing，14 天 retention，自动清理。处理 inflight 去重。

**GoDex 当前**：没有幂等性保证。cron/heartbeat/subagent 调度中，同一个任务可能被多个 worker 同时执行。

**落地路径**：

1. 在 `internal/core/` 下新增 `idempotency` 包
2. 实现 `IdempotencyStore` 接口，SQLite 作为 backing store
3. 在 `cron`、`heartbeat`、`subagent dispatch` 中集成
4. 默认 retention 7 天，可配置

**QM 参考文件**：
- `temp/qm/src/idempotency/idempotency-store.ts`（78 行，完整实现）

**收益**：调度可靠性大幅提升，避免重复执行

### 1.3 记忆 notebook 格式简化

**QM 做法**：`memory/notebook.ts` 定义极简记忆格式——纯 bullet list，无 frontmatter。配套函数：`isBullet`、`bulletText`、`bullets`、`captureDate`、`normalize`（去重用）、`capTail`（截断）。

**GoDex 当前**：`*.md` + YAML frontmatter + `index.json` + SQLite sidecar。结构完整但偏重。

**落地路径**：

1. 在 `internal/core/memory/` 下新增 `notebook.go`，实现 QM 的 `foldCapture` 去重逻辑
2. `foldCapture` 在写入新事实时做 `normalize` 去重，避免重复追加
3. 可选：在 `layers.go` 的 `trimMemoriesToTokenBudget` 中使用 `capTail` 策略（保留尾部最新内容）

**QM 参考文件**：
- `temp/qm/src/memory/notebook.ts`（37 行）
- `temp/qm/src/memory/memory-service.ts` 的 `foldCapture` 函数

**收益**：减少重复记忆，提升召回质量

---

## 第二阶段：架构基础设施（中期）

### 2.1 Turn Error 分层

**QM 做法**：`core/turn-error.ts` 定义 `NonRetryableTurnError`，与普通错误区分。Harness 层和 Orchestrator 层根据错误类型决定是否重试。

**GoDex 当前**：`internal/agent/` 中错误处理分散在各处，没有统一的 turn error 类型。部分错误被吞掉，部分被重复重试。

**落地路径**：

1. 在 `internal/core/` 下新增 `turnerror` 包
2. 定义 `TurnError` 接口，区分 `Retryable` / `NonRetryable` / `Transient`
3. 在 `context.go` 的 `Run` 循环中集成错误路由
4. 逐步迁移现有错误处理

**QM 参考文件**：
- `temp/qm/src/core/turn-error.ts`

**收益**：长任务运行时韧性提升，避免"明明不可恢复还重试 3 次"的场景

### 2.2 Agent Identity 与 Sandbox 解耦

**QM 做法**：`core/orchestrator.ts` 编排 agent 执行，`sandbox/sandbox.ts` 定义沙箱接口。sandbox 独立于 agent identity，可以被替换。

**GoDex 当前**：`internal/agent/agent.go` 同时承担 composition root、session state holder、tool registry、sandbox facade。`2.0 SPEC` 已定义此方向但未落地。

**落地路径**：

1. 定义 `Sandbox` 接口（参考 `sandbox/sandbox.ts` 的 215 行接口）
2. 把当前 `localSandboxFromConfig` 改为 `Sandbox` 接口的实现
3. `Agent` 通过接口使用 sandbox，不直接操作文件系统

**QM 参考文件**：
- `temp/qm/src/sandbox/sandbox.ts`（接口定义）
- `temp/qm/src/sandbox/sandbox-routing.ts`（路由逻辑）
- `temp/qm/src/sandbox/local-sandbox.ts`（本地实现）
- `temp/qm/src/sandbox/aws-sandbox.ts`（AWS 实现）

**收益**：为未来替换 sandbox 后端做准备，同时让 agent 结构更清晰

### 2.3 Worker Lease + Heartbeat

**QM 做法**：`runs/worker.ts` 实现 worker 循环：lease 领取 → 心跳保活 → 连续 3 次心跳丢失则取消。`run-store.ts` 管理 lease。

**GoDex 当前**：subagent/workflow 没有 lease 机制，进程重启后 running 状态丢失。

**落地路径**：

1. 在 `internal/core/` 下新增 `lease` 包
2. 实现 `LeaseStore` 接口（SQLite 实现）
3. 在 `subagent` 和 `workflow` 运行时引入 lease
4. 进程重启时，通过 lease 恢复 running 状态

**QM 参考文件**：
- `temp/qm/src/runs/worker.ts`（188 行，完整 worker 循环）
- `temp/qm/src/runs/run-store.ts`（run 状态管理）
- `temp/qm/src/persistence/leader-lease.ts`（leader lease 实现）

**收益**：长任务可靠性从"不丢"提升到"可恢复"

---

## 第三阶段：高价值大工程（长期）

### 3.1 Harness 多引擎抽象

**QM 做法**：`harness/harness.ts` 定义 `Harness` 接口（~200 行），`harness-router.ts` 根据配置 + 作用域路由到对应引擎。Pi（2070 行）、Codex（942 行）、Claude Code（926 行）、OpenCode（1163 行）各一个实现。

**GoDex 当前**：只支持一个 LLM 后端，通过 `Tools` 接口与工具交互。没有抽象层来切换不同的 agent 引擎。

**落地路径**：

1. 定义 `Harness` 接口：`runTurn`、`resetSession`、`close`、`profile`、`models`、`tools`
2. 把当前 agent loop 提取为默认 harness 实现
3. 新增 `harnessRouter` 根据配置路由
4. 外部 harness（如 Claude Code / Codex）通过 stdio MCP 或 HTTP 接入

**QM 参考文件**：
- `temp/qm/src/harness/harness.ts`（接口定义，202 行）
- `temp/qm/src/harness/harness-router.ts`（路由逻辑，117 行）
- `temp/qm/src/harness/pi-harness.ts`（Pi 实现，2070 行）

**收益**：不绑定单一 LLM 提供商，可切换引擎做不同任务

### 3.2 安全筛查器（Security Screener）

**QM 做法**：`security/security-screener.ts` 实现内容级安全筛查：大文本分块（1600 字符 + 256 重叠）、多模型投票、shadow mode（主 + 影子并行）、重试退避。

**GoDex 当前**：有 `PermissionManager`（审批机制），但没有内容级安全筛查。无法检测 prompt injection、敏感内容泄露。

**落地路径**：

1. 定义 `SecurityScreener` 接口
2. 实现基于 LLM 的安全分类器（参考 QM 的 HTTP proxy 模式）
3. 集成到 tool execution 的 before/after interceptor
4. 可选：shadow mode 先做，不阻塞主流程

**QM 参考文件**：
- `temp/qm/src/security/security-screener.ts`（269 行，完整实现）
- `temp/qm/src/security/security-posture.ts`（安全 posture 配置）

**收益**：安全边界从"工具级审批"提升到"内容级筛查"

### 3.3 Scope 隔离模型

**QM 做法**：`scopeId` 贯穿所有模块，每个 scope（用户/频道/项目）有独立记忆、沙箱、文件、密钥、技能授权。`acl/` 和 `policy/` 管理权限。

**GoDex 当前**：单用户，无多租户需求。但 session 间隔离、project 间隔离已经开始需要。

**落地路径**：

1. 定义 `ScopeId` 类型（参考 QM 的 `parseScopeId` / `scopeId`）
2. 在 `memory`、`files`、`sandbox` 中引入 scope 参数
3. 当前单用户场景下，默认 scope = "default"
4. 为未来多 project 共享运行时做准备

**QM 参考文件**：
- `temp/qm/src/types.ts` 的 `ScopeId`
- `temp/qm/src/acl/acl-store.ts`（ACL 存储，207 行）
- `temp/qm/src/policy/command-policy.ts`（命令策略，816 行）

**收益**：架构清晰，为多项目/多用户场景铺路

### 3.4 持久化 Map 抽象

**QM 做法**：`persistence/durable-map.ts` 定义 `DurableMap` 接口（类似 `Map`，但持久化）。`idempotency-store`、`leader-lease`、`advisory-lock` 都依赖它。

**GoDex 当前**：`internal/core/store/` 有 `sqlite` 包，但接口不统一。部分模块直接读写文件。

**落地路径**：

1. 定义 `DurableMap[K, V]` 接口
2. SQLite 实现
3. 逐步替换 `index.json`、`candidates.json` 等文件读写
4. 为后续存储后端替换（如 PostgreSQL）做准备

**QM 参考文件**：
- `temp/qm/src/persistence/durable-map.ts`（229 行）
- `temp/qm/src/persistence/pg-pool.ts`（PostgreSQL 连接池）
- `temp/qm/src/persistence/advisory-lock.ts`（分布式锁，97 行）

**收益**：存储抽象统一，为分布式部署做准备

---

## 第四阶段：长期愿景（可规划但暂不启动）

### 4.1 分布式 Worker 运行时

**QM 做法**：`runs/worker.ts` + `runs/run-store.ts` + `runs/run-signal-store.ts` 构成完整 worker 运行时，支持跨进程/跨节点的 run 调度。

**GoDex 当前**：所有 agent 在同一进程内运行。subagent 是同一进程内的 goroutine。

**条件**：需要先完成 2.1（Agent Identity 解耦）、2.2（Worker Lease）、2.3（持久化 Map 抽象）。

### 4.2 Session 树（可分支）

**QM 做法**：没有直接对应。但 QM 的 `sessions/` 模块（`session-store.ts` + `memory-session-store.ts` + `postgres-session-store.ts`）提供了 session 持久化接口。

**GoDex 当前**：`2.0 SPEC` 已定义 session 树方向。当前 session 是线性 history + compaction + transcript references。

**条件**：需要先完成 2.1（Agent Identity 解耦）和 3.3（Scope 隔离模型）。

### 4.3 多引擎热切换

**QM 做法**：`harness-router.ts` 的 `resolveRuntimeChoice` 支持每轮对话切换引擎，切换时自动 reset session。

**GoDex 当前**：单引擎。

**条件**：需要先完成 3.1（Harness 多引擎抽象）。

---

## 优先级矩阵

| 阶段 | 项目 | 难度 | 收益 | 依赖 | 推荐顺序 |
|------|------|------|------|------|----------|
| P1 | 记忆策略模式 | 中 | 高 | 无 | 1 |
| P1 | 幂等性存储 | 低 | 中 | 无 | 2 |
| P1 | 记忆 notebook 简化 | 低 | 中 | 无 | 3 |
| P2 | Turn Error 分层 | 低 | 中 | 无 | 4 |
| P2 | Agent Identity 解耦 | 高 | 高 | 已有 SPEC | 5 |
| P2 | Worker Lease | 中 | 高 | P1 幂等性 | 6 |
| P3 | Harness 抽象 | 高 | 高 | P2 Identity | 7 |
| P3 | Security Screener | 中 | 中 | 无 | 8 |
| P3 | Scope 隔离 | 中 | 中 | 无 | 9 |
| P3 | 持久化 Map 抽象 | 中 | 中 | 无 | 10 |
| P4 | 分布式 Worker | 高 | 高 | P2+P3 多项 | 11 |
| P4 | Session 树 | 高 | 高 | P2 Identity | 12 |
| P4 | 多引擎切换 | 高 | 中 | P3 Harness | 13 |

## 与现有 roadmap 的关系

这份路线图与 `docs/roadmap-high-roi.md` 和 `docs/roadmap-runtime-hardening.md` 的关系：

- **补充而非替代**：现有 roadmap 聚焦"长任务可靠性和运行时韧性"，这份路线图聚焦"从 QM 借鉴的设计模式"
- **重叠区域**：P2 的 Agent Identity 解耦与现有 roadmap 的 P1 方向一致
- **新增区域**：P1 的记忆策略模式、幂等性存储、notebook 简化是现有 roadmap 没有覆盖的

## 落地原则

1. **每个阶段独立可交付**——不依赖后续阶段。
2. **宁可不做，不要一次性大重写**——每个改动都应保留现有行为。
3. **先接口后实现**——先定义接口，再逐步替换实现。
4. **测试即契约**——每个接口都有对应的兼容性测试，确保替换不会破坏既有行为。
5. **不追求 100% 覆盖**——QM 有 76k 行代码，GoDex 只需要借鉴适合的模式，不是全盘照搬。