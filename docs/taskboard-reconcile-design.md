# taskboard 对账功能设计（reconcile / 执行一致性）

> 状态：Draft（本卡交付）｜ 关联：taskboard-plugin-design.md（§6 M3/可观测性）、taskboard-collaboration-design.md（闸门 3 动态观测）
> 日期：2026-08-30
> 起因：任务卡 `t-1788080801653-2` 指出「任务看板对账功能实现似乎不完整，请详细设计一下这块儿的功能」。
> 本设计基于对现有实现的实测盘点（详见 §2），不是从零发明，而是把已零散落地的观测/恢复能力**补全为闭环的对账语义**，并给出分阶段落地路径。

---

## 1. 形态与目标

### 1.1 对账 = 什么？

「对账」在 taskboard 语境下指：**让宿主账本（ledger）里记录的执行状态，与真实执行会话（durable subagent/执行会话）的客观状态保持一致**，并自动收敛二者不一致产生的"僵尸执行"。

核心主张是**不臆造完成**：对账只依据客观证据（会话终态、显式取消）收敛状态，绝不凭空把 running 翻成 completed。

### 1.2 现状问题（为何"不完整"）

现有代码已有一个 `Reconcile` 函数（`taskboard_executor_observability.go:72`）和 observe/recover/retry 工具 + HTTP 面 + UI 按钮，但存在以下**结构性缺口**：

| # | 缺口 | 现象 | 根因 |
|---|---|---|---|
| G1 | **无自动调度** | 僵尸 running 只能靠人主动点「对账」按钮触发；PJM 忘了点就一直卡在 running | Reconcile 无任何后台循环 / cron / heartbeat 挂钩 |
| G2 | **报告无明细** | `ReconcileReport{Scanned,Observed,Finalized}` 只有计数，PJM 无法知道"哪张卡被 finalize、哪些停滞" | report 仅聚合，不携带逐卡条目 |
| G3 | **无停滞(stall)检测** | 观测的 `stage=idle` 不区分"真在思考/长任务"与"死循环/卡死超时"；无"停滞多久算坏"的阈值语义 | 无 stall 判定逻辑，只有单次快照 |
| G4 | **无卡级一致性核对** | 只看 running execution，不看"in_progress 但无 running execution 且 holder 残留""in_review 但执行记录未收尾" | Reconcile 只扫 running，不核对卡级状态机一致性 |
| G5 | **报告不持久化** | 对账结果是瞬态，无历史，无法做"这次收敛了几次""连续 3 次都卡同一张卡"的趋势分析 | 无对账记录存储 |
| G6 | **无独立设计文档** | 对账语义散落在 plugin design 与 observability 代码，无一张权威文档 | 本文档目标 |

### 1.3 目标（可验证）

1. **自动触发**：对账可由后台定时器自动跑（无需人点），且与既有 cron/heartbeat 机制兼容或复用。
2. **明细可读**：每次收敛返回"每张卡 → 阶段/是否停滞/是否 finalize/收敛动作"，PJM 一眼看懂对账做了什么。
3. **停滞可辨**：对"running 但长时间无进展"的执行给出 stall 标记与阈值，与正常长任务区分。
4. **卡级一致**：除收敛 execution 外，也纠正在_progress/in_review 卡与执行记录的错位。
5. **可观测**：对账结果落账（历史），支持"连续停滞"可视化。
6. **回归无损**：既有 single-card、同卡并发、human 越锁、状态离开 in_progress 自动终结 running execution 全部不受影响。

---

## 2. 现状盘点（实测）

### 2.1 已落地的能力

| 能力 | 位置 | 说明 |
|---|---|---|
| `Observe` | `taskboard_executor_observability.go:30` | 单执行观测 → `observeFromSnapshot` 推导 stage/error/LastTool/live，并 `UpdateExecutionObservation` 写回 |
| `Reconcile` | `observability.go:72` | 遍历所有 running execution，`!live && ErrorType!=""` → `FinishExecutionWithObs` finalize；否则 `UpdateExecutionObservation` |
| `Recover` | `observability.go:125` | 追加重启执行会话 + 提交恢复消息（QueueModeFollowUp） |
| `Retry` | `observability.go:164` | 重放最后一个 `CanRetry` turn |
| `observeFromSnapshot` | `observability.go:200` | 由 `ActivePhase/PendingPermissions/Turns` 推导 |
| `ReconcileReport` | `types.go:132` | `{Scanned,Observed,Finalized}` 三计数 |
| tool actions | `tools.go:73` | `observe/reconcile/recover/retry/report_touched/merge_precheck` |
| HTTP routes | `plugin.go:122` | `POST .../observe|recover|retry` + `POST /v1/taskboard/reconcile` |
| UI | `TaskBoardPage.tsx:327` | reconcile 按钮 + observe/recover/retry mutation |

### 2.2 关键判定逻辑（`observeFromSnapshot`，对设计最关键）

```
obs.Stage:
  有 ActivePermissionBlocker+PendingPermissions → waiting_approval（最强 waiting 信号）
  ActivePhase 映射: model_request→thinking / awaiting_tools→tool_call / final_response→final_response / error→error / interrupted→interrupted
  (以上未命中时) 取最后一条 TurnRecord:
    LastToolName → LastTool
    last.Error → LastError
    status=="error" → 若无 Stage 则 error; classifyExecutionError 分 provider/tool
    status=="canceled" → cancelled + interrupted
live := snapshot.Running || snapshot.ActiveTurnID != ""
  !live && Stage=="" → idle
```

> **关键洞察**：`live` 只依据「当前在跑 turn」，因此**"执行会话已退出（agent 已写完 final_response 但没调用工具移卡）"在盘上是 idle 而非死**——这正是 G3 停滞检测的落点。

### 2.3 缺口证据

- `Reconcile` 定义在 backend 层，但**无任何 `time.NewTicker`/cron/heartbeat 调用它**（grep 全库仅工具/路由/测试引用）。
- `ReconcileReport` 仅 `Scanned/Observed/Finalized` 三个 int，**无逐卡条目、无 stage、无 stall 标记**。
- 无「in_progress 卡 + holder 残留 + 无 running execution」一致性修正逻辑。
- `closeRunningExecutions`（`ledger.go:928`）只在**状态离开 in_progress** 时收尾，不覆盖"永远停在 in_progress"的场景。

---

## 3. 方案设计

### 3.1 数据模型扩展（`types.go`）

#### 3.1.1 `ReconcileReport` 带明细（G2）

在保留三计数兼容字段的同时，加**逐卡/逐执行条目**：

```go
// ReconcileResult 是一次对账对单个 running execution 的判定结果。
type ReconcileResult struct {
    CardID      string `json:"card_id"`
    CardTitle   string `json:"card_title"`
    ExecutionID string `json:"execution_id"`
    Stage       string `json:"stage"`        // 观测 stage（含 idle/stall）
    ErrorType   string `json:"error_type,omitempty"`
    LastTool    string `json:"last_tool,omitempty"`
    LastError   string `json:"last_error,omitempty"`
    // Stall 标记：running 但长时间无进展。true 表示停滞（需 PJM 关注）。
    Stall       bool   `json:"stall"`
    StallReason string `json:"stall_reason,omitempty"` // 如 "no_turn_progress_30m"
    // Action 是本次对账采取或建议的动作。
    Action ReconcileAction `json:"action"`
}

type ReconcileAction string

const (
    ActionNone     ReconcileAction = "none"      // 正常 running，仅写观测
    ActionFinalized ReconcileAction = "finalized" // 依据终态证据已收敛为 failed/cancelled
    ActionStalled   ReconcileAction = "stalled"   // 停滞，已标记待 PJM 介入
    ActionRecovered ReconcileAction = "recovered" // 已自动触发 recover/retry 引导
)

type ReconcileReport struct {
    Scanned   int               `json:"scanned"`   // 兼容
    Observed  int               `json:"observed"`  // 兼容
    Finalized int               `json:"finalized"` // 兼容
    Stalled   int               `json:"stalled"`   // 新增
    StartedAt time.Time         `json:"started_at"`
    Duration  time.Duration     `json:"duration"`
    Results   []ReconcileResult `json:"results"`   // 新增：逐执行明细
}
```

#### 3.1.2 存活标记（G3，明细层面）

在 `ExecutionObservation`（observability 字段集）上**不新增字段**，stall 由对账器在运行时结合**时间戳**计算，而非写死到观测里——这样观测保持"当前快照"语义，停滞是"跨次快照"语义，职责分开。

### 3.2 对账核心重写（`taskboard_executor_observability.go`）

把 `Reconcile` 升级为**两段式**：先扫描收敛（既有逻辑），再追加**卡级一致性核对**与**停滞判定**。

```go
func (e *TaskboardExecutor) Reconcile(ctx context.Context) (taskboard.ReconcileReport, error) {
    report := taskboard.ReconcileReport{StartedAt: e.service.now()}
    for _, card := range e.ledger.ListCards(taskboard.CardFilter{}) {
        // ---- 段 1：收敛 running execution（既有） ----
        for i := range card.Executions {
            ex := &card.Executions[i]
            if ex.Status != taskboard.ExecutionRunning { continue }
            report.Scanned++
            sessionID := executionSessionID(card, *ex)
            if sessionID == "" { continue }
            snapshot, err := e.service.Snapshot(ctx, sessionID)
            if err != nil { continue } // 磁盘不可读 → 留给修复步骤，不臆造
            obs, live := observeFromSnapshot(snapshot)
            result := taskboard.ReconcileResult{
                CardID: card.ID, CardTitle: card.Title, ExecutionID: ex.ID,
                Stage: obs.Stage, ErrorType: obs.ErrorType,
                LastTool: obs.LastTool, LastError: obs.LastError,
            }
            if !live {
                if obs.ErrorType != "" {
                    // 终态证据：finalize
                    status := taskboard.ExecutionFailed
                    if obs.ErrorType == taskboard.ErrTypeCancelled { status = taskboard.ExecutionCancelled }
                    summary := obs.LastError; if summary == "" { summary = "run errored (no detail)" }
                    if _, err := e.ledger.FinishExecutionWithObs(card.ID, ex.ID, status, summary, obs); err == nil {
                        report.Finalized++; result.Action = taskboard.ActionFinalized
                    }
                } else {
                    // 未死但无进展 → 走停滞判定
                    if e.isStalled(ex, snapshot) {
                        result.Stall = true; result.StallReason = "idle_no_progress"
                        result.Action = taskboard.ActionStalled
                        report.Stalled++
                    }
                }
            } else {
                result.Action = taskboard.ActionNone
            }
            if result.Action != taskboard.ActionFinalized {
                if _, err := e.ledger.UpdateExecutionObservation(card.ID, ex.ID, obs); err == nil {
                    report.Observed++
                }
            }
            report.Results = append(report.Results, result)
        }

        // ---- 段 2：卡级一致性核对（G4） ----
        if card.Status == taskboard.StatusInProgress {
            if !card.HasRunningExecution() && card.Holder != "" {
                // in_progress + holder 残留 但 无 running execution：
                // 说明执行已结束（agent 写完 final_response 但没走 move→in_review）。
                // 保守做法：只标记/上报，不自动改卡（把推进交给 PJM/human）。
                report.SignalCard = append(report.SignalCard, taskboard.CardConsistency{
                    CardID: card.ID, CardTitle: card.Title, Field: "holder/execution",
                    Problem: "in_progress but no running execution with holder residue",
                    Suggested: "recover to finalize, or human accept",
                })
            }
        }
    }
    report.Duration = e.service.now().Sub(report.StartedAt)
    return report, nil
}
```

（`Result` 优先级：`Finalized > Stalled > Recovered > None`，一个 execution 只落到一个 Action。）

#### 3.2.1 停滞判定 `isStalled`（G3）

```go
// StallThreshold 由配置注入（见 §3.3）。默认 30 分钟无 turn 进展即视为停滞。
func (e *TaskboardExecutor) isStalled(ex taskboard.Execution, snap Snapshot) bool {
    if snap.Running || snap.ActiveTurnID != "" { return false } // 真在跑
    // 有可重试 turn 且有活跃会话 → 认为是"等待恢复"而非停滞，交给 recover/retry
    if retryableTurnID(snap.Turns) != "" { return false }
    last := lastTurnAt(snap.Turns)
    threshold := e.reconcileStallThreshold()
    if last.IsZero() {
        return e.service.now().Sub(ex.StartedAt) > threshold // 从未有进展
    }
    return e.service.now().Sub(last) > threshold // 最后一次进展距今已超阈值
}
```

> **为什么用"时间戳"而非只看 stage=idle**：因为执行会话的 turn 记录带 `UpdatedAt`，而持续观察只能看到"当前是否在跑"。用 `now - lastTurnUpdatedAt > threshold` 判定"长时间无新动作"，能**同时覆盖**"agent 写完 final 但没移卡"与"真死循环不出新 turn"两种停滞，且不会把"仍在思考的慢模型"（有 active turn）误判。

### 3.3 配置化（G3 阈值 + G1 自动调度）

在 taskboard 插件配置（或复用已有 config 结构）增加：

```yaml
taskboard:
  reconcile:
    enabled: true          # 是否开启后台自动对账
    interval: 60s          # 自动对账周期（0 = 仅手动）
    stall_threshold: 30m   # 停滞判定阈值
    auto_recover: true     # 停滞时是否自动触发 recover 引导（false = 仅标记上报）
```

### 3.4 自动调度接线（G1 的核心）

**设计原则**：任务看板不是全局会话能感知的实时引擎，因此不放在 turn 循环里，而是**挂到一个独立的周期性循环**。候选与结论：

| 候选 | 结论 |
|---|---|
| `internal/runtime/cron` | 复用 cron 先例最简单，但语义是"用户配置的调度"，把系统级对账塞进去会让用户误以为是用户任务。**不首选** |
| `internal/runtime/heartbeat` | 心跳已每 tick 跑，适合挂系统级后台体检。但 heartbeat 职责是"服务活性"，塞对账会耦合语义。**可用但要解耦** |
| **独立 goroutine（推荐）** | 在 taskboard 插件 `RegisterEffect` 阶段（或 backend Assembly 装配一个 `reconcileLoop`）启动一个受 `reconcile.enabled/interval` 控制的 `time.NewTicker` 循环，`defer` 停止；随插件卸载可逆关闭。**语义最干净，且符合插件边界 P-A/C/D** |

```go
// plugin.go / 或 backend 装配处
func startReconcileLoop(ctx context.Context, exec ObservedExecutor, cfg ReconcileConfig) {
    if !cfg.Enabled { return }
    t := time.NewTicker(cfg.Interval)
    defer t.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-t.C:
            report, err := exec.Reconcile(ctx)
            if err != nil { log.Warn("taskboard reconcile", "err", err); continue }
            log.Info("taskboard reconcile done", "scanned", report.Scanned,
                "stalled", report.Stalled, "finalized", report.Finalized,
                "results", len(report.Results))
            // 若 report.SignalCard 非空 → 作为告警信号上报（可对接通知/后台）
        }
    }
}
```

**可逆性**：该循环随插件 `Deactivate` 取消，符合 pluginrt 可逆注册语义；卸载后无 goroutine 泄漏。

### 3.5 报告持久化 + 历史（G5）

- 对账报告不阻塞主流程：**fire-and-forget** 追加进一个新存储（或复用既有 job store/ledger 侧文件）。
- 持久化结构：`<StateDir>/taskboard/reconcile_history/<RFC3339>.json`，记录每次 `StartedAt/Duration/Results`。
- 前端可据此画"连续停滞"趋势（哪张卡在多少轮对账里反复 marking），为 G3/G5 服务。
- 轻量实现：仅保留最近 N 条（默认 50），写入 tmp+rename 原子写。

> **P0 快赢可先不做持久化**，仅把 `Results` 返回给 UI 即可获得明细；持久化/趋势归为 P1（见 §4）。

### 3.6 工具 / HTTP / UI 增强

- **tool action=reconcile**：返回结构从三计数升级为带 `results` + `signals` 的完整 report（同时保留计数字段兼容既有 parsing）。
- **HTTP `/v1/taskboard/reconcile`**：返回体同样升级，POST body 可带 `{dry_run: bool}`，dry-run 只对账不落账（便于 PJM 预演）。
- **UI**：TaskBoardPage 的 reconcile 按钮在 result toast 中展示"finalized N / stalled M / checked T"；stalled 执行在卡片/详情中以醒目状态（如"⚠ 停滞 30m+"）呈现，可一键跳 observe/recover/retry。i18n 补齐 `taskboard.reconcileDetail` 等 key。

---

## 4. 落地分级

| 级别 | 内容 | 改动面 | 验收点 |
|---|---|---|---|
| **P0（对账语义闭环）** | ReconcileReport 带 `Results/Stalled/Signals`；`Reconcile` 加停滞判定 + 卡级一致性核对；tool/HTTP 返回升级；UI toast 展示 stalled/finalized | types.go + observability.go + routes.go + tools.go + TaskBoardPage + api.ts + i18n | 单卡 running → 对账能报"停滞"或"finalized"，结果含逐卡明细 |
| **P1（自动调度 + 配置）** | 独立 reconcileLoop + `reconcile.enabled/interval/stall_threshold` 配置 + 可逆关闭 | 装配处 + config | 后台定时自动跑，插件卸载无泄漏 |
| **P2（历史/趋势）** | reconcile 历史持久化 + 前端"连续停滞"趋势视图 | 存储 + UI | 能看到某卡连续多轮停滞 |
| **P3（自动恢复策略）** | `auto_recover`（停滞时自动 recover/retry）+ 逐步升级策略 | observability.go | 停滞执行被自动引导恢复，次数受限防风暴 |

---

## 5. 验收（可验证）

> 对齐 §1.3 目标，逐条给可执行验证。

1. `go test ./internal/plugins/taskboard/` 全绿（含新增 reconcile/停滞/一致性测试）。
2. `go test ./internal/services/backend/ -run Taskboard` 全绿（observability 层测试）。
3. **停滞检测**：构造一个 running 但 `UpdatedAt` 距今 > threshold 的 execution → `Reconcile` 返回 `Stalled=1` 且 `Results[].Action=="stalled"`；有 active turn 的同一执行 → 不判停滞。
4. **终态收敛**：构造 `!live && ErrorType!=""` 的 running execution → `Reconcile` 将其 `Finalized` 为 failed（`completed` 永不凭空生成）。
5. **无回归**：single-card 执行、同卡并发写、human 越锁、离开 in_progress 自动终结 running execution 均不受影响（沿袭 `conflicts_test.go` / `ledger_test.go` 既有回归）。
6. **自动调度**：`reconcile.enabled=true` 且 `interval` 短 → 观测到周期触发；插件卸载后 goroutine 停止（goroutine 泄漏检测 / 可逆回归测试）。
7. **UI**：`tsc -b` + web build 全绿；reconcile toast 展示 `finalized M / stalled N`。
8. **dry_run**：`POST /v1/taskboard/reconcile {"dry_run":true}` 不落账，仅返回计算结果。

---

## 6. 边界与非目标

- **不臆造完成**：对账永不把 running 翻成 completed；只有在终态证据（error turn / cancelled）下才 finalize。完成必须由 agent 走 move→in_review → human 验收。
- **不改会话语义**：`observeFromSnapshot` 的 live/stage 判定保持现状，只加"跨次时间戳"的停滞层，职责分离。
- **不破坏人工越锁**：human 作为 superuser 越 holder 锁的既有行为不变。
- **停滞非自动杀**：P0/P1 只标记上报；自动 recover/retry 是 P3 的受控升级，且次数受限防风暴。
- **不引入重实体**：不新增 ledger 外的权威状态源；对账仍是"账本为权威，观测为事实来源"的映射。

---

## 7. 待定项（设计开放问题）

1. **停滞判定时间基准**：用「最后一条 turn 的 `UpdatedAt`」还是「执行会话最后一次写入任何消息的时间」？前者更严（turn 粒度），后者更稳（会话活性）。倾向**两者取更宽**（任一超阈值才算停滞），减少误报。
2. **自动调度归属**：独立 goroutine（推荐）vs 复用 cron vs 复用 heartbeat 的最终取舍需在实现时按装配便捷性定。
3. **`auto_recover` 的升级策略**：停滞 N 次后自动 recover 还是仅提示？需与 loop_guard（防风暴）协调。
4. **报告持久化位置**：taskboard 插件自有存储 vs 复用 job store——需评审既有存储抽象再定。
