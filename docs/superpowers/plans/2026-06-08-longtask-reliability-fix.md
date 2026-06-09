# LongTask 修复方案 v2 — Reliability & Correctness

> 创建日期: 2026-06-08
> 状态: 草稿(待评审)
> 上一份: `2026-05-27-longtask-optimization.md` (P0~P3 优化已合并)

## 背景与愿景校准

上一轮 PRD 完成了代码组织拆分、storyCompiler 解耦、handoff 透传、validation timeout、worktree GC、async run 等优化。`go test ./internal/agent/ -run TestLongTask` 12 个用例全绿。

**愿景(用户确认)**:

- **北极星指标**:
  - **(A) 跑完成功率**:把 PRD 编译成"能跑完的故事链",断连 / 失败 / 重启不丢进度
  - **(C) 单 story 可审计可回滚**:每个 story 的 validation / merge / commit artifact 永久可查
- **session 关系**:**(B) 附加执行,跑完结果回流对话历史**。longtask 在 chat session 内是"附加执行",跑完最终状态 push 回 agent message history。
- **follow-up / steer**:本轮**不在范围**,挪到下一份 PRD(见文末 **T-future-1**)。

**深读 `run` 循环 / repair 流程 / 持久化路径后,发现这套实现"几乎不可用"**——根因是 longtask 的**串行故事语义**和底层 workflow 的**并行 DAG 语义**在 5 个关键点错位,叠加 sync `run` 完全无持久化、跑完结果从不回流到对话历史 2 个核心 gap。

本文档按 P0 → P3 列出**修复任务**。每条任务都带:

- **问题**:精确定位 + 复现条件 + 对应愿景指标
- **方案**:落地代码改动,精确到文件
- **验收**:可执行的测试用例或 e2e 脚本
- **不做**:显式排除的边界(避免 scope creep)

---

## 优先级矩阵

| 编号 | 标题 | 优先级 | 估时 | 依赖 | 服务愿景 |
|---|---|---|---|---|---|
| T1 | ~~run 循环状态机重写~~ ✅(2026-06-08) | **P0** | 2d | — | A |
| T2 | ~~run 状态落盘 + 重启 resume~~ ✅(2026-06-08) | **P0** | 1.5d | T1 | A |
| T3 | ~~Repair 重新接线覆盖已 running 的下游~~ ✅(2026-06-08) | P1 | 0.5d | T1 | A |
| T4 | ~~Repair 节点 handoff vs prompt 去重~~ ✅(2026-06-08) | P1 | 0.5d | — | A |
| T5 | ~~`longtask cancel <id> --all` 支持~~ ✅(2026-06-08) | P1 | 0.5d | T1 | A |
| T6 | async run 状态落盘(`runs/<id>.json`) | P1 | 1d | T2 | A |
| T7 | ~~validation 全局 timeout + ctx 取消传播~~ ✅(2026-06-08) | P1 | 0.5d | — | A |
| T8 | ~~worktree GC 条件修正(失败不清)~~ ✅(2026-06-08) | P1 | 0.5d | — | C |
| T9 | ~~stop_on_failure 字段真正生效 + 默认语义~~ ✅(2026-06-08) | P1 | 0.5d | T1 | A |
| T10 | 修复路径解析走 `safeJoinUnderRoot` | P2 | 0.5d | — | C |
| **T11** | ~~longtask 完成 / 中断时回流对话历史(B)~~ ✅(2026-06-08) | **P0** | 1.5d | T1 | B |
| **T12** | **artifact 永久可查 + commit 反查 + rollback 入口** | **P0** | 1.5d | T8 | C |
| T13-e2e | 10 个端到端 e2e(长链+repair+cancel+resume+回流+rollback+retention) | **P0** | 1d | T1~T12 | A+B+C |
| **T15** | **TUI + Web UI 完善(覆盖 T1~T12 所有用户可见能力,Web 拆 5 个组件)** | **P1** | **2.0d** | T1~T12 | A+B+C |
| T14 | docs 更新:validation 不在 subagent 沙箱里 | P3 | 0.2d | T15 之后 | — |

**总估时: ~13.2 工作日**

---

## P0 任务

### T1 — `run` 循环状态机重写

**状态:✅ 完成 (2026-06-08)**
- commit: TBD
- 6 个新验收测试全过 + 原 12 个 longtask 测试全过
- 唯一与 T1 相关的非 longtask 测试保持预存 fail 状态不变

**对应愿景**:A(跑完成功率)

**问题**
`internal/agent/longtask_run.go::runLongTaskSync` 是一次"扫描 → 操作 → 扫描"的轮询循环,导致:

1. **`progressed` 跳过 start**:`longtask_run.go:188` 的 `if progressed { continue }` 在 finalize 任一 story 之后跳过了本轮的 `startLongTask`。下一次迭代才补做 start,**在 5+ 故事上引入 "完成一个就卡 1 轮" 的延迟**,且 `startLongTask` 会拉起**所有 deps 都完成的下游 story**,破坏 longtask 串行语义。
2. **`startWorkflowReadyNodes` 的并行语义与 longtask 串行期待不匹配**:longtask 的故事按 `DependsOn` 串成链,但 `startLongTask` 调底层 `startWorkflowReadyNodes` 时**只要 deps 完成的全部一起 start**。当用户中途 `complete_story` 手工完成中间一个,后面所有 story 会被并发拉起。
3. **`validation_status == pending` 之外的状态被吞**:`longTaskBlockedStory` 只识别 4 类,缺 `running` 卡死、`verdict` 为空、worker 中断等场景。
4. **`args.StopOnFailure` 字段声明了但全文未读**,默认行为是"任一失败 = 停",文档没说。

**方案**

1. 把"每轮"改为真正的"故事状态机",核心循环:

   ```go
   for iter := 0; iter < maxIterations; iter++ {
       view, _ = a.longTaskStatus(workflowID)

       // 严格只做一步:
       action := pickNextAction(view, args)  // 新函数
       view, progressed, done = applyAction(ctx, action, view, args)
       if done { return view }
       if !progressed { return stalledView }
   }
   ```

2. 新增 `pickNextAction(view, args) -> nextAction`:
   - 优先看是否有 story 处于 `status=completed & verdict=pass & validation=pending` → `finalizeStory`
   - 否则看是否有 story 处于 `running` → `waitRunning`
   - 否则找 `deps 完成 & pending` 的 story,**按 priority 升序取第一个** → `startOne`
   - 否则看是否有 `blocked/error` 且 `auto_repair && RepairAttempts < max` → `repair`
   - 否则 `stalled` 或 `blocked`

3. **关键改动**:`startOne` 走新增的 `startLongTaskOneNode(ctx, workflowID, storyID)`,**不调用** `startWorkflowReadyNodes`。实现方式:

   ```go
   func (a *Agent) startLongTaskOneNode(ctx context.Context, workflowID, storyID string) (longTaskView, error) {
       state, _ := a.workflowState(workflowID)
       target := findNodeByID(state, storyID)
       if target == nil || target.Status != workflowStatusPending { return view, nil }
       if !workflowDepsCompleted(state.Nodes, target.DependsOn) { return view, nil }
       return a.startWorkflowNode(ctx, state, target)
   }
   ```

   提取 `startWorkflowNode(ctx, state, *workflowNode) error` 出来,`startWorkflowReadyNodes` 改成"对每个 ready 节点调 `startWorkflowNode`",longtask 走单点版本。

4. `args.StopOnFailure` 真正生效:
   - `true`(默认):任一 story 失败就停
   - `false`:继续跑后面 story,失败的标记为 `error` 但不阻塞依赖
   - 在 `pickNextAction` 里检查
   - CLI 文档里写清默认是 `true`

**验收**

- 新测试 `TestLongTaskRunSerializesStoryStart`:**3 个 story**且所有 deps 都已 ready 的人为 setup(通过 `append_node` 直接把 3 个 story 都设成 pending 且 deps 完成),跑 `run`,**断言 3 个 story 的 `started_at` 时间严格递增**。
- 新测试 `TestLongTaskRunFinalizeThenStartSameIteration`:在第一个 story running 时手工把它设成 completed-pass-pending,round 1 内必须看到 `finalize → start` 都发生(检查事件日志),**不能多耗一轮**。
- 现有 `TestLongTaskRunCompletesStories` / `TestLongTaskRunAutoRepairPassesAfterValidationRetry` 等**继续 pass**。
- 新测试 `TestLongTaskRunStopOnFailureFalse`:把 `StopOnFailure: false` 跑 2 故事,故意让 `US-001` fail,断言 `US-002` 仍然完成。

**不做**

- 不改 `startWorkflowReadyNodes` 的对外签名,只内部提取 `startWorkflowNode`。
- 不引入新的 `workflow` 工具 action。
- 不动 `Workflow` schema。

---

### T2 — run 状态落盘 + 重启 resume

**状态:✅ 完成 (2026-06-08)**
- commit: TBD
- 4 个新验收测试 + 18 个 longtask 测试 + 6 个 T1 + 2 个 T3 全过
- run 状态、Status、Started/Finalized 落盘到 `runs/<runID>.json`
- ctx cancel 写 interrupted 保留进度
- resume_run_id 跳过已 started 的 story
- `sweepStaleLongTaskRuns()` 在 godex 启动时扫 stale runs

**对应愿景**:A(跑完成功率)

**问题**

- `runLongTaskSync` 整个 `summary` 在栈上,**进程一退出,所有进度全丢**。
- CLI sync 路径的 `ctx` 直接来自 `r.runLongTaskRun(ctx, ...)`,**等于进程生命周期**——Ctrl+C 才会停,无法 graceful shutdown。
- `longTaskAsyncRuns` 是 `sync.Map`,godex 重启后**全部 async run 消失**。

**方案**

1. **新增 run 状态文件** `~/.godex/workflows/{workflowID}/runs/{runID}.json`:

   ```go
   type longTaskRunRecord struct {
       RunID         string    `json:"run_id"`
       WorkflowID    string    `json:"workflow_id"`
       SessionID     string    `json:"session_id"`  // 新增,回流对话历史需要
       StartedAt     time.Time `json:"started_at"`
       UpdatedAt     time.Time `json:"updated_at"`
       Status        string    `json:"status"`
       Iterations    int       `json:"iterations"`
       MaxIterations int       `json:"max_iterations"`
       Started       []string  `json:"started"`
       Finalized     []string  `json:"finalized"`
       Repaired      []longTaskRepairSummary `json:"repaired"`
       BlockedBy     string    `json:"blocked_by,omitempty"`
       Async         bool      `json:"async"`
   }
   ```

2. **存储 helper** `internal/agent/longtask_run_store.go`:
   - `workflowStore.writeLongTaskRun(record)` 原子写
   - `workflowStore.loadLongTaskRun(runID)` 读
   - `workflowStore.listLongTaskRuns(workflowID)` 列

3. **runLongTaskSync 改造**:
   - 开局生成 `runID`(=`longtask_id + "_run_" + unixnano`,或 `randomID`)
   - 写入 run record,每轮迭代**先 atomic write 再 return**:
     - start → 写 `Started`
     - finalize → 写 `Finalized`
     - repair → 写 `Repaired`
     - 结束态 → 写 `Status: completed|blocked|stalled|max_iterations|interrupted`
   - **ctx 取消时**(HTTP 断连、CLI Ctrl+C、godex shutdown):
     - 写 `Status: interrupted` 保留所有进度
     - 不丢任何已完成 finalize 结果

4. **resume 入口**:
   - `longTaskArgs` 新增 `ResumeRunID string` 字段
   - `RunLongTask` 检测传 `resume_run_id` 时,先 `loadLongTaskRun` 取出上次进度,接着跑——但**不能重复 start 已 started 的 story**(基于 `Started` 列表 + workflow state 的 `running` 状态去重)
   - `longTaskRunStatus` 走落盘,不再走 `sync.Map`

5. **godex 启动 hook**(`internal/services/backend/backend.go::openSession` 或 `setup.go`):
   - 扫所有 `workflows/*/runs/*.json`,把 `Status == running` 的标为 `interrupted` 并触发 resume 提示
   - HTTP API 加 `GET /sessions/{id}/longtasks/{workflowID}/runs` 列出历史 run

**验收**

- 新测试 `TestLongTaskRunWritesRunRecord`:跑 2-story 任务,跑到一半(用 `StopOnFailure` 触发提前 return)后 `loadLongTaskRun` 能读到 `Status: blocked` + `Started`/`Finalized` 列表。
- 新测试 `TestLongTaskRunResumeAfterInterrupt`:跑 2-story,**第一次 run 中断**(取消 ctx),第二次传 `resume_run_id` 接着跑,**断言 US-001 不被重新 start**(基于 `Started` 去重)。
- 新测试 `TestLongTaskRunStatusReadsFromDisk`(不再依赖 `sync.Map`):godex 重启后 `run_status` action 仍能查到 interrupted/历史 run。
- 现有 async 测试继续 pass,且 async run 进程退出后状态不丢。

**不做**

- 不引入分布式锁(单机).
- 不动 `sync.Map` 的移除(暂时保留作为 in-memory cache,落盘为权威).

---

### T11 — longtask 完成 / 中断时回流对话历史

**状态:✅ 完成 (2026-06-08)**
- commit: TBD
- 新增 `longtask_reflux.go` 构造 `protocol.Message{Role: assistant, Kind: longtask_reflux}`
- `runLongTaskSync` finalize helper 在每个结束态调 `appendLongTaskReflux`
- `LastRefluxKey = runID|Status|UpdatedAt.UnixNano()` 三元组 dedupe(同 status 内容变也回流)
- `agent.currentLongTaskArgs` 暂存让 `NoReflux` flag 在 finalize 时可见
- 3 个新验收测试(completion / same-status content change / order before follow-up) + 28 个 longtask 测试全过

**对应愿景**:B(附加执行,跑完结果回流对话历史)

**用户确认的关键决策**:
- **去重粒度**:允许同 `Status` 但内容变化也回流——**用 `runID + Status + UpdatedAt` 做 dedupe key**(`Status` 变化必回流,`Status` 相同但 `UpdatedAt` 推进也回流,完全相同才跳过)
- **角色**:用 `protocol.RoleAssistant` 的 `protocol.Message`,不走 `envelope` 路径(那是给"外部来源"用的)
- **顺序**:同轮里 longtask 跑完 + 用户发 follow-up 时,**先回流 longtask 再注入 follow-up**——follow-up 看到的"上下文"是"longtask 刚跑完"

**问题**

- `godex longtask run` 跑完,`longTaskView` 序列化返回给 CLI/HTTP,**没有**把结果 push 回 chat session 的 message history。
- 用户在 Web Chat 里看到的是"用户消息:跑 X / 工具调用:longtask run / 工具结果:View JSON",**没有"longtask 完成了,story 通过/失败的摘要"作为助手消息回流**。
- 这违背 README 写的 "长任务韧性 ... runner phase checkpoint、运行中 follow-up/steer"——**跑完的最终状态必须是 assistant message 的一部分**,不是被埋没在 tool result 里。
- **关键基础设施现成但未用**:
  - `internal/agent/runtime.go::AppendRuntimeFeedback` —— agent 自己的 protocol.Message 注入接口
  - `runtime.go::AppendInjectedMessages` —— 队列化注入接口
  - `events.EventRunnerPhaseChanged` / `EventMessageInjected` —— 事件流

**方案**

1. **新增回流消息构造器** `internal/agent/longtask_reflux.go`:
   ```go
   type longTaskRefluxPayload struct {
       LongTaskID    string                 `json:"longtask_id"`
       RunID         string                 `json:"run_id"`
       SessionID     string                 `json:"session_id"`
       Status        string                 `json:"status"`         // running | completed | blocked | interrupted | max_iterations | stalled
       UpdatedAt     time.Time              `json:"updated_at"`
       Iterations    int                    `json:"iterations"`
       Stories       []longTaskRefluxStory  `json:"stories"`
       Repaired      []longTaskRepairSummary `json:"repaired,omitempty"`
       BlockedBy     string                 `json:"blocked_by,omitempty"`
       SuggestedActions []string            `json:"suggested_actions,omitempty"`  // 提示用户下一步可选: ["wait", "fix and rerun", "cancel --all"]
   }

   type longTaskRefluxStory struct {
       StoryID         string `json:"story_id"`
       Status          string `json:"status"`           // pending | running | passed | failed | canceled | reverted
       Verdict         string `json:"verdict,omitempty"`
       ValidationRef   string `json:"validation_ref,omitempty"`
       CommitRef       string `json:"commit_ref,omitempty"`
       CommitHash      string `json:"commit_hash,omitempty"`
       ResultPreview   string `json:"result_preview,omitempty"`
       Error           string `json:"error,omitempty"`
   }
   ```

   构造出 `protocol.Message{Role: protocol.RoleAssistant, Content: <rendered text>}`。
   `Content` 格式(在 main session model 看到的人类可读文字):
   ```
   LongTask lt_checkout: completed (3 stories, 1 repair, 28s)

   ✓ US-001  Add backend API
   ✓ US-002  Add CLI command
   ✓ US-003  Add error log  (repaired 1x: validation failed → added log → passed)

   Artifacts:
     - validations/US-001/1.json
     - commits/US-001/1.json (commit abc123)
     - commits/US-003/2.json (commit def456, after repair)

   Suggested actions: fix and rerun, wait, status lt_checkout
   ```

2. **新增 longtask 专用的 assistant appender** `internal/agent/longtask_reflux.go`:
   ```go
   // longTaskRefluxApplier 在 agent 上提供，finalize/run 结束后调用
   func (a *Agent) AppendLongTaskReflux(view longTaskView, runID string) error {
       msg := buildLongTaskRefluxMessage(view, runID)  // protocol.Message{Role: assistant}
       a.appendMessage(msg)   // 复用 session_state.go 现有的 appendMessage
       return nil
   }
   ```
   **不走** `SubmitAsync(QueueModeSteering)`——那路径是给"外部 source"用的,T11 是 longtask 自己作为 agent 角色回滚。**不**走 `AppendRuntimeFeedback`——那路径是给"系统提醒"用的(比如 permission approve),T11 是有上下文的 longtask 报告。

3. **触发时机**:
   - **结束态**(completed / blocked / stalled / max_iterations / interrupted):final `runLongTaskSync` return 前调 `AppendLongTaskReflux`
   - **每个 phase 切换**:emit `events.EventRunnerPhaseChanged`,payload 加 `LongTaskID / RunID / StoryID / Phase`(T13-e2e 用)
   - **async run 期间**:**每隔 N 秒**(默认 30)emit 一次 progress 摘要,但**不注入**到 history(避免刷屏),只在 Web 长任务 tab 显示

4. **去重机制**(用户确认细粒度):
   - 在 `longTaskRunRecord` 加 `LastRefluxKey string` 字段(不是 `LastRefluxStatus`)
   - dedupe key = `runID + "|" + Status + "|" + UpdatedAt.UnixNano()`
   - 每次回流前读 `runs/<runID>.json`,算 current key,跟 `LastRefluxKey` 比较,**完全相同才跳过**
   - 写回 `LastRefluxKey = current key` 跟状态一起 atomic write
   - **Status 变** → 必回流(例: running → blocked)
   - **Status 不变但内容变**(例: blocked by US-003 → blocked by US-005,因为 US-003 修好了) → UpdatedAt 推进 → 必回流
   - **完全不变** → 跳过

5. **同轮顺序保证**(用户确认):
   - 在 `Service.RunLongTask` 同步路径里,`session.agent.RunLongTask(...)` return 之前 **不**调 reflux,**之后**才调 `service.AppendLongTaskReflux`
   - 在 `Service.RunLongTask` async 路径里,goroutine 内 `agent.RunLongTask(...)` return 后才调 `service.AppendLongTaskReflux`
   - **同轮里**用户发 follow-up message → `SubmitAsync` 走 `enqueueTurn`(不是 `injectActiveTurn` 的 steering 路径)→ 进入 queue → **不**跟 longtask reflux 冲突(顺序由 backend 的 `startQueuedTurns` 控制,reflux append 走 agent.messageHistory,queue 走 turn 队列,**两者不抢占 message history slot**)
   - **重要**:reflux append 到 `a.messages` 后,**下一个 turn**(`startQueuedTurns` 拉起 follow-up turn)会把这些 message 当作上下文——这是"longtask 先回流,follow-up 看到结果"的正确顺序
   - 防止回流给正在运行的 turn 造成混乱:**reflux 只 append 到 message history,不调 `InjectActiveTurn`**(那路径是给"正在 active 的 turn 加 steering"用的)

6. **CLI / HTTP 行为**:
   - CLI 走 `godex longtask run` 后,stdout 仍输出 JSON,**同时**通过 backend 注入 assistant message
   - HTTP `POST /sessions/{id}/longtasks/{lt}/run` 返回 view + 同上
   - Web LongTasks tab 看到回流后的 assistant message 在 chat 列表里加高亮
   - **`--no-reflux` flag** / `args.NoReflux bool` 允许用户关掉这个行为(避免刷屏或 e2e 干扰)
   - **TUI / Web 的回流渲染增强留给 T15**(T11 只保证后端发出 message,UI 高亮是 T15 范围内)

**验收**

- 新测试 `TestLongTaskRefluxInjectsAssistantMessageOnCompletion`:
  - backend mock session
  - 跑 1-story longtask,status=completed
  - 断言 `agent.GetMessages()[-1].Role == "assistant"`(不是 user,不是 tool)
  - 断言 `agent.GetMessages()[-1].Content` 含 `[pass]` 或 `✓` 标记 + commit hash
- 新测试 `TestLongTaskRefluxAllowsSameStatusContentChange`:
  - 跑 2-story longtask,第 1 次回流 blocked-by-US-001
  - 修 US-001 再跑,**Status 仍是 blocked**(没变)但 `BlockedBy` 变 → 断言第 2 次回流发生,`LastRefluxKey` 变了
  - **再**跑一次同样的,内容不变 → 断言回流被跳过
- 新测试 `TestLongTaskRefluxRunsBeforeFollowUp`:
  - 模拟场景:longtask 跑完时同时收到 follow-up user message
  - 断言 `agent.GetMessages()` 里 assistant reflux message **在** follow-up user message 之前(按 `Index`/`Timestamp`)
- 新测试 `TestLongTaskRefluxOnInterrupt`:
  - 跑 longtask 中途 cancel ctx
  - 断言回流消息 `Status: "interrupted"` + 已 finalize 的 story 摘要
- 新测试 `TestLongTaskRefluxNoRefluxFlag`:
  - `args.NoReflux = true` → 跑完 `agent.GetMessages()` **不**含 reflux message
- 现有 backend 测试继续 pass

**不做**

- 不实现 follow-up / steer 注入(留给 T-future-1)
- 不动 `QueueModeSteering` / `SubmitAsync` 的现有协议
- 不改前端 TUI / Web 渲染(**留 T15**)
- 不让 reflux 走 `envelope` 路径(envelope 是给"外部 source"用的,T11 是 agent 自生成)

---

### T12 — artifact 永久可查 + commit 反查 + rollback 入口

**对应愿景**:C(可审计可回滚)

**用户确认的关键决策**:
- **retention 默认永久**(`ArtifactRetentionDays = 0`),不自动删
- **rollback 改 `passes → reverted`**(保留 passes 字段,加 `reverted: true` 区分)
- **rollback 保留 `revert_history`**(多次回滚记录)
- **rollback 冲突不解决**,写 `index.json` + 报错
- **rollback 支持备注 `revert_reason`**,为后续手动重做做准备

**问题**

- `validations/{nodeID}/{attempt}.json` / `commits/{nodeID}/{attempt}.json` **已经**落盘,但:
  - **没有反查入口**:"某次 commit 是哪个 story 改的"反向查不到
  - **没有 rollback 入口**:story 出问题时,用户得手工 `git revert <hash>` + 重跑 longtask,workflow / longtask 都不知道这次 revert
  - **worktree GC 在 finalize 末尾无条件清**(`gcLongTaskStoryWorktree`):失败 story 的 worktree 立刻清,**想看 diff 已不可能**(T8 关联问题)
  - **validations dir 在主 workspace 内**,git commit 时会被 `git add -- <changes>` 一起加进 commit——**`quality_checks` 跑出来的 `validations/*.json` 变成了 commit 的一部分**,污染 commit
  - **没有 `revert_reason` 备注**——后续手动重做时,会忘"为什么 revert"

**方案**

1. **反查索引** `~/.godex/workflows/{workflowID}/index.json`:
   ```json
   {
     "stories": {
       "US-001": {
         "node_id": "US-001",
         "title": "Add backend API",
         "repair_attempts": 0,
         "validation_refs": ["validations/US-001/1.json"],
         "commit_refs": ["commits/US-001/1.json"],
         "commit_hashes": ["abc1234"],
         "reverted": false,
         "revert_history": [
           {
             "commit": "fed9876",
             "reason": "API design conflicts with US-004 refactor",
             "at": "2026-06-08T12:34:56Z"
           }
         ]
       }
     },
     "by_commit": {
       "abc1234": {
         "story_id": "US-001",
         "title": "Add backend API",
         "subject": "longtask(lt_checkout): complete US-001 Add backend API"
       },
       "fed9876": {
         "story_id": "US-001",
         "kind": "revert",
         "reason": "API design conflicts with US-004 refactor"
       }
     }
   }
   ```
   - 每次 `finalizeLongTaskStory` 末尾 atomic write
   - 每次 `rollback_story` 末尾 atomic write(同时追加 `revert_history`)
   - 新 action `longtask action=lookup` + CLI `godex longtask lookup --commit <hash>` / `--story <id>`

2. **rollback 入口**:
   - 新 action `longtask action=rollback_story <story_id>` + 可选 `reason string`:
     - 读 `index.json` 找 `US-001.commit_hashes[0]` = H1(取最早一个 commit,避免多次 commit 都 revert)
     - **`git revert -n H1`**(主 workspace,不是 commit——留个干净 index)
     - 写新 commit "longtask(<lt>): revert <story_id> <title>\n\nReason: <user_reason>"
     - 更新 `index.json`:
       - `US-001.reverted = true`
       - `US-001.revert_history` 追加 `{commit: <new_hash>, reason: <user_reason>, at: <now>}`
       - `by_commit.<new_hash>` 加 entry,`kind: "revert"`
     - 写事件 `longtask_story_reverted`,payload `{commit_hash, revert_hash, story_id, reason}`
   - CLI `godex longtask rollback <lt> --story <id> --reason "<text>"`:
     - `--reason` 可选,推荐填("后续手动重做"要靠这个)
   - HTTP `POST /longtasks/{lt}/rollback` body `{"story_id": "US-001", "reason": "..."}`

   **reason 长度限制(用户确认)**:
   - **最大 1024 字节**(1KB)
   - 超出 → 返回 error "reason exceeds 1024 bytes, got <N>"
   - 0 字节 reason → OK,不强制填(只不发 commit message 里的 "Reason: ..." 段)
   - CLI 提前检查(不调 backend);tool action 在 backend 层检查
   - **不**只检查 byte 数(避免用户用多字节 UTF-8 创越界):用 `len([]byte(reason))` 不是 `utf8.RuneCountInString`

   **rollback 后回流(用户确认)**:
   - rollback **成功完成**后,走 T11 机制发一条 assistant message
     - 格式:
       ```
       LongTask lt_checkout: rolled back US-001
         - commit <H1> reverted → commit <H2> ("revert US-001 Add backend API")
         - reason: <user_reason or "(none provided)">
         - history: 2 reverts in record
       Suggested actions: status, lookup --commit <H2>, rerun US-001 (future T-future-2)
       ```
     - `LastRefluxKey` dedupe 仍然有效(同 commit 同 reason 重调 rollback 不重复回流)
   - rollback **冲突**时也发回流(T11 同路径):
     - 格式:
       ```
       LongTask lt_checkout: rollback conflict on US-001
         - git revert <H1> failed: <error_summary>
         - conflict files: <list>
         - status: conflict (recorded in index.json)
       Suggested actions: resolve conflicts, then rollback --continue (future T-future-3)
       ```
   - **不**发回流的场景:rollback 完全跳过(例如 story 不存在、未提交过 commit)→ 返回 error,不回流

   **rollback 细节**:
   - 冲突时(`git revert` exit non-zero):
     - **不**自动 resolve
     - 把 conflict 状态写到 `index.json.US-001.rollback_status: "conflict"` + `rollback_conflict_files: [...]`
     - 返回 error 给用户,用户手工 merge
     - 后续用户解决 conflict 后再调一次 `rollback --continue`(本轮**不**实现,留 T-future-3)
   - **不**重跑 story(用户决定)
   - **不**影响其他 story
   - **不**自动 push 到 remote
   - 每次 rollback **单独**写新 commit(不 squashing),保留完整 history
   - **多次 rollback 同一个 story**:`revert_history` 不断 append,`reverted` 保持 `true`

   **`passes` vs `reverted` 语义**:
   - `passes: true` 仍然保留(代码已合并进主 workspace 这个**事实**没变)
   - `reverted: true` 额外标记(代码后来被 revert 了)
   - 前端用 `reverted` 字段显示不同颜色,用户一眼看出"跑过但被回滚"

3. **worktree GC 条件修正**(对应 T8 任务):
   - validation pass / skipped 且 merge+commit 全 OK → 立即 GC
   - validation fail / merge fail / commit fail → **保留 worktree**,写 `retained_until: <now+7d>` 到 `index.json.US-001.worktree_retained_until`
   - 新 action `longtask action=gc_worktrees` + CLI `godex longtask gc-worktrees <lt>` 强制清

4. **validations / commits / runs 目录排除 git**:
   - `longTaskGitCommit` 前过滤:**显式跳过 `validations/` / `commits/` / `runs/` / `index.json` 开头的 path**
   - **不**用 `.gitignore`(那是 godex 仓库的 gitignore,会被用户的 longtask 仓库继承,可能跟用户自己的 gitignore 冲突)

5. **artifact 寿命(retention)**:
   - `Spec.ArtifactRetentionDays int`(默认 **0 = 永久保留**)
   - `0` → 永不删(用户手动 `godex longtask gc` 才动)
   - `> 0` → 懒 GC 入口 `godex longtask gc <lt>`(可加 `--dry-run` 必做)
   - GC 范围:`validations/*` / `commits/*` 文件(根据 `mtime > N days`)
   - **不**删:`index.json` / `runs/*.json` / `longtask.json` / `summary.json` / `nodes.json` / `edges.json`
   - **不**自动 GC(只在用户调 `godex longtask gc` 时才执行,且支持 `--dry-run`)

**验收**

- 新测试 `TestLongTaskIndexWrittenOnFinalize`:
  - finalize 1 story,断言 `index.json` 存在且 `by_commit.<hash>.story_id == "US-001"`
- 新测试 `TestLongTaskLookupByCommit`:
  - 调 `lookup --commit <hash>`,断言返回 story 元数据(含 title / subject / refs)
- 新测试 `TestLongTaskLookupByStory`:
  - 调 `lookup --story US-001`,断言返回完整 story 记录(含 `reverted` 字段)
- 新测试 `TestLongTaskRollbackCreatesRevertCommit`:
  - 跑 1 story(成功),commit hash = H1
  - 调 `rollback_story US-001 --reason "design conflict"`
  - 断言 git log 出现 "revert US-001" commit
  - 断言 `index.json.US-001.reverted == true`
  - 断言 `index.json.US-001.revert_history[0].reason == "design conflict"`
  - 断言 `index.json.US-001.passes == true`(保留)
- 新测试 `TestLongTaskRollbackMultiplePreservesHistory`:
  - 同一个 story rollback 2 次(reason 不同)
  - 断言 `revert_history` 长度 2,2 个不同 reason,2 个不同 commit
  - 断言 `by_commit` 多了 2 个 entry,`kind: "revert"`
- 新测试 `TestLongTaskRollbackConflictRecordsStatus`:
  - 手工 mock 冲突场景(主 workspace 上有 commit 改了 US-001 的同一个文件)
  - 调 `rollback_story`,断言 git exit non-zero,**不**回滚
  - 断言 `index.json.US-001.rollback_status == "conflict"`
  - 断言 `rollback_conflict_files` 列出冲突文件
- 新测试 `TestLongTaskGitCommitExcludesArtifactPaths`:
  - finalize 后,git diff HEAD~1 --name-only 断言**没有** `validations/...` / `commits/...` / `runs/...` / `index.json`
- 新测试 `TestLongTaskGCDryRun`:
  - 创建 1 个 100 天的 validation 文件
  - 设 `ArtifactRetentionDays = 90`
  - 调 `godex longtask gc --dry-run`,断言输出列出该文件,断言**不**真删
- 新测试 `TestLongTaskGCPermanent`:
  - `ArtifactRetentionDays = 0` (永久)
  - 调 `godex longtask gc`,断言**不**删任何文件
- 新测试 `TestLongTaskRetainsWorktreeOnFailure`:(T8 验收)
- 新测试 `TestLongTaskRollbackRejectsOversizedReason`:
  - reason = 1025 字节 → 调 `rollback_story`,断言返回 error "reason exceeds 1024 bytes"
  - reason = 1024 字节 → OK
  - reason = "" → OK(不填 reason 合法)
- 新测试 `TestLongTaskRollbackRefluxesOnSuccess`:
  - 跑 1 story,commit H1
  - 调 `rollback_story US-001 --reason "design conflict"`
  - 断言 `agent.GetMessages()[-1].Role == "assistant"`
  - 断言消息含 `"rolled back"` + H1 + H2 + reason
- 新测试 `TestLongTaskRollbackRefluxesOnConflict`:
  - mock 冲突场景
  - 调 `rollback_story`,断言返回 error
  - 断言 `agent.GetMessages()[-1]` 含 `"rollback conflict"` + conflict_files
- 新测试 `TestLongTaskRollbackRefluxDedupeSameCommitSameReason`:
  - rollback US-001 成功,回流发生
  - **不**改任何状态再调 `run_status` 多次 → 断言回流**不**重复
- 现有 finalization 测试继续 pass

**不做**

- 不实现 `rollback --continue`(冲突后用户手工 merge 完成,然后再 rollback——留给 T-future-3)
- 不实现 `Spec.ArtifactRetentionDays > 0` 的自动清理(只暴露字段 + 显式 GC 命令)
- 不实现"全量 longtask rollback"——只支持单 story
- 不动 `CleanupDurableSubagentWorkspace` 的现有行为
- 不让 rollback 改 longtask story 状态(`passes: true` 保留,只加 `reverted`)
- 不让 rollback 影响其他 story
- 不让 rollback 在“未填 reason 且 H1 已被 revert 过”的情况下不回流——reverted 发生必回流(同 commit 同 reason 才 dedupe)
- 不让 T11 / T12 改 TUI / Web 渲染(**留 T15**)

---

### T13-e2e — 端到端 e2e 测试(长链+repair+cancel+resume+回流+rollback)

**对应愿景**:A + B + C(综合验证)

**问题**

现有 12 个 longtask 测试都是**单 story 路径**或**fixture 极简**的 happy path。**没有覆盖**真实 longtask 场景:
- 5+ stories 串行,中间 1 个 validation fail → repair → 重跑
- 跳过的 priority(中间有空 priority slot)
- async run 跨 ctx cancel
- 取消整个 longtask
- **跑完结果回流对话历史**(T11 核心场景,assistant role)
- **rollback / commit 反查 / 带 reason 的 revert_history**(T12 核心场景)
- **reflux 同 status 不同内容多次回流**(T11 决策点)

**方案**

新增 `internal/agent/longtask_e2e_test.go` 和 `internal/services/backend/longtask_e2e_test.go`,测试用例:

1. `TestLongTaskE2E_5StoriesWithMidChainRepair`:
   - 5 stories,priority 1-5
   - subagent 客户端用 `sequenceCaller` 控制每个 story 的产出
   - US-003 故意产出 fail → 触发 repair → repair 产出 pass → 继续 US-004/005
   - 断言最终全部 `passes==true`,`Repaired[0].StoryID == "US-003"`
   - 断言 git log 有 5 条 commit(假设 write_scope 在 git repo 里)
   - 断言 `index.json` 5 个 story 都有 commit 记录
   - 断言 `agent.GetMessages()[-1].Role == "assistant"` 且含"completed"

2. `TestLongTaskE2E_CancelMidRunCascadesAll`:
   - 3 stories,running US-002 时调 `CancelLongTask(workflowID, "", true)`
   - 断言 US-002 的 subagent job 状态 `canceled`
   - 断言 US-003/004 状态 `canceled`
   - 断言 US-001 状态 `completed` 不动
   - 断言回流消息 body 含 `status: canceled` + 已完成 story 摘要

3. `TestLongTaskE2E_InterruptedAsyncResumesCleanly`:
   - 4 stories,async run,running US-002 时 cancel ctx
   - 重启进程,调 `run --resume-run-id <id>`
   - 断言 US-002 不重启,US-003/004 接着跑
   - 断言回流消息各阶段都触发(分 status 多次回流)

4. `TestLongTaskE2E_NonSequentialPrioritySorting`:
   - 输入 stories priority = [3, 1, 5, 2]
   - 断言 view.Stories[i].ID == "US-001"(priority=1)/"US-002"/"US-004"/"US-003"
   - 断言 workflow 依赖链 1→2→4→3

5. `TestLongTaskE2E_RefluxInjectsAssistantMessage`:
   - 走完整 backend 层(不是直接调 agent)
   - 跑 1-story longtask,完成后 backend 注入 message
   - 断言 `agent.GetMessages()[-1].Role == "assistant"`
   - 断言消息 content 含 `[pass]` 或 `✓` 摘要 + commit hash

6. `TestLongTaskE2E_RefluxSameStatusDifferentContent`:
   - 跑 2-story,初次跑 US-001 fail → blocked
   - 修 US-001 重跑 → US-001 pass,但 US-002 fail → blocked
   - **Status 都是 blocked** → 断言第 2 次回流发生(`LastRefluxKey` 变)
   - 再跑同样场景什么都不变 → 断言第 3 次回流被跳过(dedupe 生效)

7. `TestLongTaskE2E_RefluxRunsBeforeFollowUp`:
   - 模拟场景:longtask 跑完时同时收到 follow-up user message
   - 断言 `agent.GetMessages()` 里 assistant reflux message **在** follow-up user message 之前

8. `TestLongTaskE2E_RollbackWithReasonPreservesHistory`:
   - 跑 1 story,commit H1
   - 调 `rollback_story US-001 --reason "design conflict with US-004"`
   - 断言 git log 出现 "revert US-001" commit 且 commit message 含 reason
   - 断言 `index.json.US-001.reverted == true`
   - 断言 `index.json.US-001.revert_history[0].reason == "design conflict with US-004"`
   - 断言 `index.json.US-001.passes == true`(保留)
   - **再**调一次 `rollback_story US-001 --reason "actually need a different fix"`
   - 断言 `revert_history` 长度 2,2 个不同 reason

9. `TestLongTaskE2E_LookupByCommitAndStory`:
   - 跑 1 story,commit H1
   - `lookup --commit H1` 断言返回 US-001 元数据
   - `lookup --story US-001` 断言返回完整 story 记录(含 `reverted` 字段)

10. `TestLongTaskE2E_RetentionDefaultPermanent`:
    - 跑 1 story,跑完
    - **不**设 `ArtifactRetentionDays`(默认 0)
    - 调 `godex longtask gc` → 断言**不**删任何文件
    - 手工 mtime 100d 前的文件再调 `gc` → 仍然不删

**验收**

- 全部 pass,单测总耗时 < 60s
- `go test ./internal/agent/ ./internal/services/backend/ -run "TestLongTaskE2E" -v` 绿
- 现有 12 个 longtask 单测继续 pass

**不做**

- 不引入真实 LLM 调用(全用 `sequenceCaller` / `repeatedTextCaller`).
- 不测 git push / remote 操作.

---

## P1 任务

### T15 — TUI + Web UI 完善(覆盖 T1~T12 所有用户可见能力)

**对应愿景**:A + B + C(被用户看见才算交付)

**用户确认**:TUI 和 Web 都要补齐,不是“后端完事、UI 不做”。

**现状(2026-06-08)**:

- **TUI** (`internal/tui/`):
  - 只有 workbench summary 里一段 `longtask <id> <status> -> worker <id>` 一行文本
  - 没有任何 longtask 详情视图、tab、交互
  - 不能 run / wait / cancel / finalize / rollback / lookup / resume / gc
  - `TestWorkbenchSummarySurfacesLongTasksSubagentsAndReviewState` 验证 12 个 mock longtask 能被解析,但 UI 上除了 summary 行什么也不出
- **Web** (`ui/web/src/features/chat/TaskCenterPanel.tsx`):
  - 有 TaskCenterPanel(332 行),能列 longtask + 调 run / cancel / finalize
  - **没有**:detail view(点进去看每个 story 的 validation/merge/commit/repair)
  - **没有**:rollback / lookup / gc / runs 列表 / resume
  - **没有**回流 assistant message 的高亮(chat 列表里出现时跟其他 message 一样)
  - **没有** index.json / by_commit 反查入口
  - **没有** artifact 引用(点击 validations/US-001/1.json 跳到 detail)

**问题**

仅后端能跑通不代表 longtask 可用。Rui 平台用户(PM、agent 编排工程师)90% 互动在 TUI / Web:

- 跑完 longtask 在 Web chat 里看到回流消息,但“跟 user message 长的没区别”，用户才不回头看
- 想 rollback US-001 但只有 CLI 有,Web 用户被迫 ssh 进服务器
- 想看 US-003 为啥 fail,Web 只有一行 status,看不到 validation / commit / diff
- 想看 `index.json.by_commit.H1` 查 commit 是哪个 story,TUI / Web 都没反查入口

**方案**

#### 15.1 TUI 完善(`internal/tui/`)

1. **新增 longtask detail tab** `internal/tui/longtask_detail.go`:
   - `longtask list` 视图:接 `m.backend.ListLongTasks`,展示 longtask id / status / progress
   - 按 Enter 进入 detail:展示每个 story + validation status + commit hash + repair attempts
   - 加快捷键:`r` 跑 / `w` wait / `c` cancel / `f` finalize / `R` rollback(弹 prompt 输 story id + reason) / `l` lookup / `g` gc / `?` 跳出 reason 输入框
   - `l` (lookup) 后输 commit hash,接 `m.backend.LookupLongTaskByCommit(sessionID, hash)` 返回 story 元数据
   - **跟现有 workbench summary 的 `longtask <id> <status>` 一行兼容**(只增加 detail 入口,不改 summary 格式)

2. **回流 message 高亮**:
   - `internal/tui/update.go` 跟踪 reflux message(messages[-1].Role == assistant 且 metadata 里有 `kind: longtask_reflux` 标记,见 15.3)
   - 渲染时加一个 `[LongTask]` 标签 + 点击跳 longtask detail
   - **不**用 emoji(原 README 避免 emoji 偏好)

3. **测试**:
   - `TestTUILongTaskDetailRendersStories`
   - `TestTUILongTaskRollbackPromptsReasonAndLength`:
     - 输 1025 字节 reason → 报错
     - 输 1024 字节 reason → OK
   - `TestTUIHighlightsRefluxMessages`

#### 15.2 Web 完善(`ui/web/src/`)

**用户确认**:Web 拆 5 个独立组件,TaskCenterPanel 变 “< 200 行” 调度器。`LongTaskRefluxBubble` **浮动** 渲染(跟现有 task center 风格一致)。

**拆出的组件**(`ui/web/src/features/chat/`):
- `LongTaskCard.tsx` (单个 longtask 展示,受控展开)
- `LongTaskStoryList.tsx` (story 展开列表,含 reverted 标签)
- `LongTaskRollbackModal.tsx` (rollback reason 输入 modal,带 1024 byte 计数器)
- `LongTaskLookupModal.tsx` (commit/story 反查 modal,switch tab 选输入方式)
- `LongTaskRefluxBubble.tsx` (浮动回流气泡,识别 `metadata.kind == "longtask_reflux"`)

**TaskCenterPanel.tsx**(重构后 < 200 行)只负责: 1) 列表调度(调 `ListLongTasks`); 2) 状态计算(active / review / unresolved); 3) 把每个 longtask 传给 `<LongTaskCard>`; 4) 跳 LongTaskRefluxBubble 状态。

1. **API 层补全** `ui/web/src/lib/api.ts`:
   ```ts
   rollbackSessionLongTask(token, sessionId, workflowId, storyId, reason): Promise<LongTaskView>
   lookupSessionLongTaskByCommit(token, sessionId, workflowId, commitHash): Promise<LongTaskLookup>
   lookupSessionLongTaskByStory(token, sessionId, workflowId, storyId): Promise<LongTaskLookup>
   listSessionLongTaskRuns(token, sessionId, workflowId): Promise<LongTaskRunRecord[]>
   getSessionLongTaskRun(token, sessionId, workflowId, runId): Promise<LongTaskRunRecord>
   resumeSessionLongTask(token, sessionId, workflowId, runId, options): Promise<LongTaskView>
   gcSessionLongTask(token, sessionId, workflowId, options: {dry_run?: boolean, retention_days?: number}): Promise<LongTaskGCReport>
   ```
   **这些 API 后端要在 T1/T2/T5/T12 实现**,本任务只补前端 binding

2. **LongTaskCard 增强**:
   - 默认折叠,点击展开
   - 展开后调 `LongTaskStoryList` 展示 story 列表
   - action buttons:Run / Cancel --all / Finalize / Resume / Rollback(弹 modal) / Lookup(弹 modal) / GC(弹 modal)
   - **不**改现有 run / cancel / finalize 调用方式(只加新 button)

3. **LongTaskRefluxBubble 浮动渲染**:
   - 识别 `metadata.kind == "longtask_reflux"` 的 assistant message
   - **浮动**(`position: fixed` 或 `absolute`)、标题 + story list + suggested actions
   - suggested actions:["rerun", "lookup", "rollback", "status"] 都是按钮点击直接调 API
   - 位置:与现有 task center 浮动面板同右侧,多个回流闾时堆叠

4. **ChatPage 集成**:
   - 同一 chat 中如果有回流 message,顶部加一排 LongTask 状态行(只读概览)
   - **不**改变现有 chat 布局,只加一个 hook

5. **i18n** `taskCenter.i18n.ts`:
   - 加 key:`rollback`, `rollbackReason`, `rollbackReasonTooLong`, `lookup`, `lookupByCommit`, `lookupByStory`, `gc`, `gcDryRun`, `resume`, `reverted`, `refluxPrefix`

6. **测试** `ui/web/test/` (vitest):
   - `TestLongTaskCard` (snapshot: 折叠/展开 两状态)
   - `TestLongTaskRollbackModal` (1024 byte 边界)
   - `TestLongTaskRefluxBubbleFloats` (DOM 查询 position)
   - `TestTaskCenterLookupByCommitAndStory`
   - `TestTaskCenterGcDryRunOutput`

#### 15.3 后端回流 metadata 补一个 marker(T11 加一个低代价字段)

T11 现在没在 `protocol.Message.Metadata` 里写 `kind: "longtask_reflux"`,前端无法识别 message 是 reflux。T15 需要在 T11 产出 message 时加:

```go
if msg.Metadata == nil { msg.Metadata = &protocol.Metadata{} }
msg.Metadata.Kind = "longtask_reflux"
msg.Metadata.SetExtra("longtask_id", view.LongTaskID)
msg.Metadata.SetExtra("run_id", runID)
msg.Metadata.SetExtra("status", view.Status)
```

(不增 package,复用 `protocol.Metadata` 现有字段)

**验收**

- **TUI**:
  - `go test ./internal/tui/ -run "TestLongTask\|TestTUI" -v` 绿
  - 手动能进 longtask detail / rollback / lookup / gc
  - reason 1025 byte 报错,1024 byte OK
- **Web**:
  - `pnpm test` 绿
  - 手打开 TaskCenter 能展开每个 story / 看到 reverted 标签
  - rollback modal reason 实时计字节
  - chat 列表里回流 message 是独立气泡

**不做**

- 不重做 TaskCenterPanel 样式(只加展开、button、modal)
- 不加 LongTask 专有 tab 代替现有 workbench(不拆 UI 结构)
- 不改 longtask backend model(后端 model 6 月 8 日定下来不变)
- 不加 i18n 除中文 / 英文以外的语言

**依赖**

- **后端 T1/T2/T5/T11/T12 全部完成后**才开 T15
- T15 预计估时: **1.5d**(TUI 0.7d + Web 0.7d + 后端 marker 0.1d)

---

### T3 — Repair 重新接线覆盖已 running 的下游

**状态:✅ 完成 (2026-06-08)**
- commit: TBD
- `appendLongTaskRepair` 取消所有 Status=Running 下游并重置为 pending,同时 replace `DependsOn` + `HandoffFrom`
- 2 个新验收测试 + 18 个 longtask 测试 + 6 个 T1 pickNextAction 测试全过

**对应愿景**:A(跑完成功率)

**问题**
`internal/agent/longtask_repair.go::appendLongTaskRepair` 的 `rewired` 循环:

```go
for i := range state.Nodes {
    if state.Nodes[i].ID == repairID || state.Nodes[i].Status != workflowStatusPending {
        continue
    }
    if replaceWorkflowDep(state.Nodes[i].DependsOn, failedNodeID, repairID) { ... }
}
```

`Status != pending` 跳过**已经在跑**的下游。如果 `US-001` 失败 + `US-002` 已经被 auto start 起来(见 T1 修之前的并行 bug),re-wiring 对 `US-002` 无效。

**方案**

1. `replaceWorkflowDep` 改为对所有 deps 引用 `failedNodeID` 的节点,无脑替换,不论 `Status`。
2. 如果替换的下游节点已经 `running`,**追加一个 cancel + 重新入队的步骤**:
   - `subagentJobs.Cancel(jobID)`
   - 改完 `DependsOn` 之后,**重置该节点到 `pending`**(从 `running` 退回)
   - 写事件 `repair_cascade_cancel`
3. **但**:这个 case 在 T1 修好之后基本不会出现(longtask 严格串行)。所以这条更像是"修 T1 之前的代码留下的兜底"。

**验收**

- 新测试 `TestLongTaskRepairRewiresRunningDownstream`:人为把 US-002 设到 `running`,然后给 US-001 触发 repair,断言 US-002 被 cancel 并退回 pending。
- 不破坏 `TestLongTaskRunAutoRepairPassesAfterValidationRetry`。

**不做**

- 不去处理"已经 merge 完毕的下游"(那个故事已经 passes,repair 也不该回滚)。

---

### T4 — Repair 节点 handoff vs prompt 去重

**状态:✅ 完成 (2026-06-08)**
- commit: TBD
- repair 节点 HandoffPolicy=none, HandoffFrom=空
- 1 个新验收测试 + 19 个 longtask 测试全过

**对应愿景**:A

**问题**
`renderLongTaskRepairPrompt` 已经把 "Previous result preview / Validation artifact / Failure reason" 写进 prompt,但 `appendWorkflowNodes(...)` 又设了 `HandoffFrom: [failedNodeID]` + `HandoffPolicy: summary_artifacts`,导致 subagent 看到的 handoff 摘要**和 prompt 里的 preview 重复**(8000 byte budget 浪费)。

**方案**

1. 二选一:让 repair 节点走 `HandoffPolicy: workflowHandoffPolicyNone`,**只用 prompt 里的 preview**。
2. 改 `appendLongTaskRepair` 的 `workflowNodeInput.HandoffPolicy` 改成 `workflowHandoffPolicyNone`,同时 `HandoffFrom: nil`。

**验收**

- 新测试 `TestLongTaskRepairPromptDoesNotDuplicateHandoff`:mock 一个 subagent 截取它收到的 prompt,断言 "Previous result preview" 段出现在 prompt 中,且 `HandoffPolicy == "none"`。
- `TestLongTaskRunAutoRepairPassesAfterValidationRetry` 继续 pass。

**不做**

- 不改 `HandoffPolicy` 的可选值列表。

---

### T5 — `longtask cancel <id> --all` 支持

**状态:✅ 完成 (2026-06-08)**
- commit: TBD
- CancelAll 透传到 cancelLongTaskAll:取消所有 pending+running,保持 completed 不动
- workflow.Summary.Status 标 canceled(只要有 ≥1 canceled 节点)
- CLI `--all` flag + HTTP body `cancel_all: true` + Service.CancelLongTaskAll
- 1 个新验收测试 + 22 个 longtask 测试全过

**对应愿景**:A

**问题**
当前 `cancel` action 只能取消**单个** node(看 `cancelWorkflowNode`)。`godex longtask cancel lt_X --all` 不存在。Longtask 视角下用户想"停掉整个 longtask",但 CLI 不支持。

**方案**

1. `longTaskArgs` 新增 `CancelAll bool`.
2. `longtask.go::newLongTaskTool` 的 `case "cancel"` 走 `CancelAll ? cancelLongTaskAll : cancelWorkflowNode`.
3. `internal/agent/longtask_run.go` 新增 `cancelLongTaskAll(ctx, workflowID) (longTaskView, error)`:
   - 把 `running` 的 subagent 全部 `subagentJobs.Cancel`
   - 把 `pending` 的 story node 标 `canceled`
   - 把 longtask spec 标 `Status: canceled`(存到 `longtask.json` 里新增字段,或写到 `runs/{latestRunID}.json` 改 status)
4. CLI `internal/app/longtask.go::runLongTaskCancel`:
   - 加 `--all` flag
   - help 文本说明
5. HTTP `POST /sessions/{id}/longtasks/{workflowID}/cancel` 接受 `{"cancel_all": true}` body.

**验收**

- 新测试 `TestLongTaskCancelAllCascades`:`run` async,3 stories,running US-002 时调 `CancelLongTask(workflowID, "", true)`,断言:
  - US-002 的 subagent job 状态 `canceled`
  - US-003/004 状态 `canceled`
  - US-001 状态 `completed` 不动
  - longtask summary.status 含 `canceled`
- CLI smoke:`./godex longtask cancel lt_demo --all --session local:default` 退出码 0.

**不做**

- 不加 `--story <id>` 单个 cancel(已经有了 `--node`).

---

### T6 — async run 状态落盘

**对应愿景**:A

**问题**
见 T2 背景。`longTaskAsyncRuns` 内存 map,godex 重启即丢。修复办法是把它**完全替换成** `runs/{runID}.json`,`longTaskAsyncRuns` 仅作 in-memory 索引加速查询。

**方案**

1. T2 已经把 `longTaskRunRecord` 落盘,async 路径同样:
   - `startAsyncLongTask` 启动 goroutine,**先把 `RunRecord{Status: running}` 落盘**
   - goroutine 内 `runLongTaskSync` 的每次迭代**先更新落盘记录**再继续
2. `longTaskAsyncRuns` 退化为:
   ```go
   type asyncIndex struct {
       mu     sync.Mutex
       cancel context.CancelFunc
   }
   ```
   **只存 `cancel func`**,用于 HTTP 断连时立即 cancel async run。
3. `longTaskRunStatus` 优先看 `runs/{runID}.json`,找不到再 fallback 到 `sync.Map`(兼容老版本写的)。
4. CLI 启动时扫描所有 `runs/*.json` 的 `Status==running`,把 `cancel` 设为 `nil`(`asyncIndex` 不重建),并把 `Status` 改成 `interrupted`,触发**用户/上层主动 resume**。

**验收**

- T2 的 resume 测试覆盖此场景。
- 新测试 `TestLongTaskAsyncRestartShowsInterrupted`:
  - 启动 async run,中途 cancel ctx(模拟 godex 关机)
  - 重新构造 Agent 实例(模拟 godex 重启)
  - 调 `run_status`,断言返回的 `Run.Status == interrupted`,且 View 的故事状态保持中断瞬间的快照。

**不做**

- 不改 async 启动 API 的对外签名(还是 `args.Async = true`).
- 不动 `subagentJobs` 层。

---

### T7 — validation 全局 timeout + ctx 取消传播

**对应愿景**:A

**问题**
`longTaskCommitBlocksStory` 的 `runLongTaskValidation` 内部 `for check { context.WithTimeout(ctx, timeoutMS) }`,但:
- 每个 check 60s 串行,总时长无上限
- `ctx`(longtask run 的 ctx)被 cancel 时,正在跑的 check 不会立刻收到信号

**方案**

1. `runLongTaskValidation` 累计已用时间,超过 `len(checks) * timeoutMS` 立即停。
2. 加整体 `runLongTaskValidationWithBudget`:
   ```go
   budget := time.Duration(len(checks)) * time.Duration(timeoutMS) * time.Millisecond
   validateCtx, cancelAll := context.WithTimeout(ctx, budget)
   defer cancelAll()
   for _, cmd := range checks {
       checkCtx, cancel := context.WithTimeout(validateCtx, time.Duration(timeoutMS)*time.Millisecond)
       ...
       cancel()
   }
   ```
3. 增加 `Spec.MaxValidationBudgetMS` 字段(默认 = `len(checks) * ValidationTimeoutMS`),CLI 透传 `--max-validation-budget-ms`.

**验收**

- 新测试 `TestLongTaskValidationOverallBudget`:5 个 quality check × 30s timeout,故意让每个 check 跑 10s sleep,断言总时长 ≤ 60s。
- 新测试 `TestLongTaskValidationCancelledOnParentCtx`:parent ctx 在第一个 check 跑 1s 后 cancel,断言 validation 立刻返回 `error`,总时长 < 2s。

**不做**

- 不改 `tooling.RunShellBudgeted` 的签名。
- 不引入并发跑多个 check(保持串行,避免污染 workspace)。

---

### T8 — worktree GC 条件修正(配合 T12)

**对应愿景**:C

**问题**
`internal/agent/longtask_finalize.go::finalizeLongTaskStory` 末尾无条件调 `a.gcLongTaskStoryWorktree(node)`,**validation fail / merge fail / commit fail**导致 story 状态变 `error` 时**也会清掉 worktree**。用户想看"这次失败到底改了啥"已经来不及。

**方案**

1. `gcLongTaskStoryWorktree` 增加 `keepOnError bool` 参数:
   - `gcLongTaskStoryWorktreeOnSuccess(node)` —— 现状的语义
   - `gcLongTaskStoryWorktreeOnError(node)` —— **不删 worktree**,只发事件 `worktree_retained_for_diagnosis` + 写 `retained_until: now+7d` 到 index.json
2. `finalizeLongTaskStory` 内:
   - validation pass / skipped 且 merge+commit 全 OK → `gcOnSuccess`
   - 否则 → `gcOnError`
3. `gcLongTaskWorktrees`(批量)增加 CLI 入口 `godex longtask gc-worktrees lt_X`,文档明示"清掉 error 状态的 worktree"。

**验收**

- 新测试 `TestLongTaskFinalizeRetainsWorktreeOnFailure`:故意让 merge 失败,断言 `os.Stat(node.WorktreeDir)` 仍存在,`index.json` 出现 `retained_until`。
- `TestLongTaskRunAutoMergesAndCommitsStoryChanges` 继续 pass(成功路径 worktree 仍被清)。

**不做**

- 不动 `CleanupDurableSubagentWorkspace` 的现有行为。
- 不加自动 7-day 清理(留给上层 cron 或 T12 的 retention 字段)。

---

### T9 — `stop_on_failure` 真正生效 + 默认语义

**状态:✅ 完成 (2026-06-08)**
- commit: TBD(合并到 T5 commit 里)
- StopOnFailure *bool 默认 true 实现(在 T1)
- 新增 `TestLongTaskRunStopOnFailureDefaultTrue` 验收测试
- CLI `--no-stop-on-failure` flag + help 文本更新
- 同时扩展了 run 接受 `--async`, `--resume-run-id`

**对应愿景**:A

**问题**
`longTaskArgs.StopOnFailure` 字段声明了但**全文未读**。`runLongTaskSync` 默认"任一失败 = 停"。

**方案**

1. `pickNextAction`(T1 新增)里:
   ```go
   case storyError || storyBlocked:
       if args.AutoRepair && attemptsLeft { return repair }
       if !args.StopOnFailure { continue }   // 不停,继续后面的 story
       return blocked
   ```
2. CLI `godex longtask run` 加 `--no-stop-on-failure` flag,默认 `true`(语义不变,但显式)。
3. **明确文档**:默认行为 = "任一失败 = 停,等待用户/repair 介入"。`--no-stop-on-failure` = "继续跑,失败 story 标 error 但不阻塞依赖"。

**验收**

- T1 的 `TestLongTaskRunStopOnFailureFalse` 已覆盖 happy path。
- 新测试 `TestLongTaskRunStopOnFailureDefaultTrue`:`StopOnFailure: true`(默认),US-001 fail,断言 US-002 仍 `pending`、run summary `Status: blocked`。
- CLI help 文本更新。

**不做**

- 不改 schema 里字段顺序。
- 不引入新 action。

---

## P2 任务

### T10 — 修复路径解析用 `safeJoinUnderRoot`

**对应愿景**:C(可审计)

**问题**
`internal/agent/longtask_commit.go::longTaskGitCommit` 调 `git add -- <paths>`,路径来自 `subagentFileChange.Path`(subagent 视角的相对路径),**没有走 `safeJoinUnderRoot`**。git 自身的 `outside repository` 保护能挡住绝对路径和 `..`,但:
- `subagentFileChange.Path` 可能含 `~`(home 展开后路径变了,git 报错,commander 输出不友好)
- 空字符串 / 特殊字符(换行)直接传给 git
- 跨平台路径分隔符(`\` 在 Windows)未被 normalize

**风险评级**:低(git 自身防护足够),但出错信息不友好,且代码没经过 `safeJoinUnderRoot` 审计。

**方案**

1. `longTaskGitCommit` 前加 sanitize:
   ```go
   for _, p := range paths {
       abs, err := safeJoinUnderRoot(a.cfg.WorkspaceDir, p)
       if err != nil { return "", fmt.Errorf("git path %q: %w", p, err) }
       rel, _ := filepath.Rel(a.cfg.WorkspaceDir, abs)
       paths[i] = filepath.ToSlash(rel)
   }
   ```
2. 复用 `safeJoinUnderRoot`(`internal/agent/subagent_jobs.go:3099`)。
3. 加测试覆盖 `..` / 绝对路径 / 空字符串 / 含 `~`。

**验收**

- 新测试 `TestLongTaskGitCommitRejectsEscapePaths`:
  - `subagentFileChange{Path: "../escape"}` → sanitize 拒绝
  - `subagentFileChange{Path: "/etc/passwd"}` → 拒绝
  - `subagentFileChange{Path: ""}` → 跳过
  - `subagentFileChange{Path: "notes/result.txt"}` → OK
- 现有 `TestLongTaskRunAutoMergesAndCommitsStoryChanges` 继续 pass。

**不做**

- 不去校验 `subagentFileChange.Binary`(commit binary 留给 git 自己).

---

## P3 任务

### T14 — docs 更新:validation 不在 subagent 沙箱里

**对应愿景**:— (文档)

**问题**
`docs/workflow-runtime.md:184-188` 暗示 validation 在 subagent workspace 跑、check 文件"isolated changes",但**实际跑 validation 的 executor 用的是主 agent 的 `a.cfg.Tools.Execution`**,不是 subagent 沙箱。用户预期错了。

**方案**

1. 在 `docs/workflow-runtime.md` "LongTask Repair, Merge, and Commit" 之前加一节 "Validation Sandbox Boundary",明确写:
   - quality check **在主 agent 的 execution config 下跑**,不在 subagent 沙箱里
   - worktree 隔离仍在(workspaceDir 是 subagent 的 worktree),但 shell 命令的执行环境是 host
2. 在 longtask spec JSON schema 里加注释(或新增 `validation_sandbox: "host" | "subagent"` 字段,默认 `"host"`)。
3. CLI help 也加一句。

**验收**

- 文档链接 / grep 检查。
- 用户读了 docs 不会误以为 validation 在 subagent 沙箱里。

**不做**

- 不实现 `validation_sandbox: "subagent"`(那是大改动,需要把 executor 推给 subagent 跑,需要单独 PRD).

---

## 未来 PRD(本轮不做)

### T-future-1 — follow-up / steer 注入(下一份 PRD)

README 承诺"运行中 follow-up/steer",代码里**完全没实现**。基础设施现成:
- `QueueModeSteering` / `QueueModeFollowUp` 在 backend 已存在
- `events.EventRunnerPhaseChanged` 已存在

**本轮不做**,留给单独 PRD。本轮 T11 留接口(回流 message),T13 e2e 不覆盖 follow-up。

### T-future-2 — longtask ↔ package / skill 集成

`architecture-v2-spec.md:533` 提到 package / skill / longtask 概念统一,**代码里完全没集成**。longtask 入口完全独立(JSON file → CLI),没有"在 chat 里说"把对话升级成 longtask"的入口。

**本轮不做**。

---

## 风险与回滚

- T1 改了 run 主循环,**所有现有 longtask 单测都会被重跑**。如果失败需要重写。
- T2 + T6 + T11 落盘,**`~/.godex/workflows/*/runs/*.json` 和 `index.json` 是新文件**,老数据无影响。godex 启动时找不到 `runs/` 不报错。
- T5 + T6 加新 CLI flag,`godex longtask cancel --all` 和 `godex longtask run --resume-run-id` 是新参数,旧调用方不受影响。
- T8 改了 GC 行为,error 状态的 worktree **不再被自动清**,磁盘占用可能微增。需要 `godex longtask gc-worktrees` 手动回收。
- T11 改了回流,**默认会注入 assistant message**。如果某个 e2e 期望"无回流",需要新加 `args.NoReflux: true`。

**回滚**:所有改动都按文件隔离,git revert 单 commit 即可。

---

## 实施顺序

```
Week 1:
  Day 1-2: T1 (run 状态机重写)        — 主干风险最大
  Day 3:   T3, T4, T8, T9             — 小改,跟 T1 一起验证
  Day 4-5: T2 + T6 (run 落盘)          — 依赖 T1

Week 2:
  Day 1-2: T11 (回流对话历史)          — 跟 backend 集成
  Day 2-3: T12 (artifact + rollback)   — 跟 T8 联动
  Day 3:   T5 (cancel --all)           — 独立
  Day 4:   T7 (validation budget)      — 独立
  Day 4:   T10 (path escape)           — 独立
  Day 5:   T13 (e2e 测试)              — 收尾验证
  Day 5:   T14 (docs)                  — 收尾
```

**关键里程碑**:
- Week 1 末:`go test ./internal/agent/ -run TestLongTask` 全部绿 + T1/T2/T3/T4/T6/T8/T9 单测绿
- Week 2 末:CLI smoke + HTTP API + e2e 全部绿,文档更新,准备合入

Week 3 (T15 收尾):
  Day 1:   T15 后端 marker (0.1d) + TUI 详细页 (0.7d) + Web 拆组件 (0.4d) = 1.2d
  Day 2:   T15 Web 业务逻辑 + modal + bubble (0.8d)
  Day 2-3: T14 docs (0.2d)
  Day 3-5: 全量回归 + 1d buffer

**Week 3 末**:TUI / Web / T11 / T12 / e2e 全部交付,准备发布

**T15 详细拆解**:
| 任务 | 估时 | 范围 |
|---|---|---|
| 后端 marker (`Metadata.Kind = "longtask_reflux"`) | 0.1d | T11 产出 message 时多写 1 个字段 + 1 个 test |
| TUI 详细页 (`internal/tui/longtask_detail.go`) | 0.7d | 列表 + 详情 + 7 个快捷键 + reason 长度检查 + 3 个 test |
| Web 拆组件 | 0.4d | 5 个新组件文件 + i18n key + 3 个 snapshot test |
| Web 业务逻辑 | 0.8d | TaskCenterPanel 调用 5 个组件 + 接 7 个新 API + 3 个 integration test |
| **总计** | **2.0d** | |

---

## 验收总览

| 任务 | 单元测试 | e2e | CLI | HTTP | Docs | 服务愿景 |
|---|---|---|---|---|---|---|
| T1 | ✅ | T13-e2e.1,4 | — | — | — | A |
| T2 | ✅ | T13-e2e.3 | ✅ | ✅ | — | A |
| T3 | ✅ | — | — | — | — | A |
| T4 | ✅ | — | — | — | — | A |
| T5 | ✅ | T13-e2e.2 | ✅ | ✅ | — | A |
| T6 | ✅ | T13-e2e.3 | — | — | — | A |
| T7 | ✅ | — | — | — | — | A |
| T8 | ✅ | T13-e2e.8 | — | — | — | C |
| T9 | ✅ | — | — | — | ✅ | A |
| T10 | ✅ | — | — | — | — | C |
| **T11** | ✅ | T13-e2e.5,6,7 | ✅ | ✅ | — | **B** |
| **T12** | ✅ | T13-e2e.8,9,10 | ✅ | ✅ | — | **C** |
| **T13-e2e** | (本任务) | (本任务) | ✅ | ✅ | — | A+B+C |
| **T15** | ✅ (TUI + Web 拆组件) | T13-e2e 补充 | ✅ | ✅ | — | A+B+C |
| T14 | — | — | — | — | ✅ | — |

---

## 已确认决策(本次会话)

- **T11 去重粒度**:`runID + Status + UpdatedAt.UnixNano()` 三元组,同 status 不同 UpdatedAt 必回流
- **T11 role**:`protocol.RoleAssistant`,不走 envelope 路径
- **T11 顺序**:agent 自调 `appendMessage` 走 message history,后于 longtask 完成发生的 follow-up 走 `startQueuedTurns` 队列——两者不抢占同一个 slot,next turn 看到 reflux 作为上下文
- **T12 retention**:默认 0 = 永久,不自动删,只 `godex longtask gc --dry-run` / `gc` 显式处理
- **T12 rollback passes → reverted**:`passes: true` 保留,加 `reverted: true` 字段区分
- **T12 rollback history**:`revert_history: [{commit, reason, at}, ...]`,多次回滚累积
- **T12 rollback 冲突**:`git revert` 失败时不自动 resolve,写 `rollback_status: "conflict"` + `rollback_conflict_files` 到 `index.json`
- **T12 rollback 触发**:**只**用户显式触发(`{action: rollback_story, reason: ...}` / `godex longtask rollback --reason` / `POST /rollback`),longtask 自己绝不自动 rollback
- **T12 rollback 不重跑 story**:rollback 只回滚代码,longtask 状态保持(`passes` 保留,加 `reverted`),重跑是另一 action(本轮不做)
- **T12 rollback reason 长度限制**:**最大 1024 字节**(1KB),超出返回 error;0 字节 reason 合法(不填可以)。**字节数**不是字符数(多字节 UTF-8 不被创越界)。CLI / backend 两层都检查
- **T12 rollback 成功后回流**:走 T11 机制发 assistant message,draftede "rolled back" + 原 commit + revert commit + reason。`LastRefluxKey` dedupe 仍生效(同 commit 同 reason 重调不重复)
- **T12 rollback 冲突后回流**:也发回流,文案 "rollback conflict" + error 摘要 + conflict_files
- **T15 TUI / Web 都要做**:不只后端。TUI 加 longtask detail tab + 快捷键 + reason 长度检查;Web 加展开 / rollback modal / lookup / GC / 回流独立气泡;T11 需在回流 message 的 `Metadata.Kind = "longtask_reflux"` 加一个 marker
- **T15 依赖 T1~T12 完成**:UI 是最后一步,不是同步走
- **T15 估时 1.5d**:TUI 0.7d + Web 0.7d + 后端 marker 0.1d

## 仍然开放(等用户决定)

1. ~~**T15 TUI 快捷键的具体映射**~~ ✅ 接受(`r` / `w` / `c` / `f` / `R` / `l` / `g`)
2. ~~**T15 Web LongTaskRefluxBubble 的位置**~~ ✅ 浮动(跟现有 task center 风格一致)
3. ~~**T15 跟 T14 docs 顺序**~~ ✅ 顺序(T15 完后 T14)
4. ~~**T15 是否拆 TaskCenterPanel**~~ ✅ 拆一个(需额外 0.5d)

**T15 实际估时 修正为 2.0d**(原 1.5d + 0.5d 拆组件):TUI 0.7d + Web 0.8d(原 0.7d + 拆 0.1d) + 后端 marker 0.1d + 拆 `<LongTaskCard>` / `<StoryList>` 0.4d。T15 实施顺序不变。

**T15 补充:Web 组件拆分**:
- 拆出新组件 `ui/web/src/features/chat/LongTaskCard.tsx` (单个 longtask 展示)
- 拆出 `ui/web/src/features/chat/LongTaskStoryList.tsx` (story 展开列表)
- 拆出 `ui/web/src/features/chat/LongTaskRollbackModal.tsx` (rollback reason 输入 modal)
- 拆出 `ui/web/src/features/chat/LongTaskLookupModal.tsx` (commit/story 反查 modal)
- 拆出 `ui/web/src/features/chat/LongTaskRefluxBubble.tsx` (浮动回流气泡)
- TaskCenterPanel 本身变 “< 200 行” (只负责列表 + 调度)

### T15 拆组件验证补充

- 新测试 `TestLongTaskCardComponent`:
  - snapshot 渲染单个 longtask(story 折叠状态)
  - 点开 → snapshot 展开状态(含 reverted 标签)
- 新测试 `TestLongTaskRollbackModalReasonLength`:
  - 输 1024 字节 → 提交按钮可点
  - 输 1025 字节 → 提交按钮 disabled + 错误提示
- 新测试 `TestLongTaskRefluxBubbleFloats`:
  - 在 chat 列表里验证回流 bubble 位置是 `position: fixed` / `absolute`(靠 DOM 查询确认)

**已不再有开放问题。确认后开 T1。**
