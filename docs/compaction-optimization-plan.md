# GoDex 压缩优化实施计划（对齐 DeepSeek Harness）

> 状态：已落地（Phase 1/2/3 与 UI 同步全部完成并测试；见各阶段 ✅ 与验收标准）
> 修订日志：2026-08-16 制定本计划并实施：对照 DSH `packages/compaction/compaction-basic/{index,region,config,summarizer}.ts` 与 `packages/core/session/surface.ts`，映射到 GoDex `internal/agent/{compaction_policy,context_facade}.go`、`internal/core/compress/{compress,llm_session_summarizer}.go` 与 `internal/core/config`；目标 = 修复"压缩过狠丢信息"与"压缩破坏前缀缓存"。

## 背景与目标

用户反馈：GoDex 上下文压缩太狠，压缩后丢失信息、需要重新扫描阅读代码。对照 DSH 后定位到四条结构性差异：

| # | GoDex 现状 | 问题 |
|---|---|---|
| T1 | 触发阈值固定（默认 60K/100K），不随模型窗口缩放 | 128K 模型过早压缩 |
| T2 | 压缩输出 = `[新摘要, 最近 20 条被 sanitize 的消息]`；`sanitizeRecentMessageForContext` 把 tool result 换成截断预览（`compress.go:184-210`） | **保留尾被改写**：信息丢失 + 前缀全变 |
| T3 | 整段 history 被替换（`CompactWithSnapshot`），无"头部区间"概念 | 与 T2 叠加 = 压缩后整个前缀都是新字节 |
| T4 | **自动压缩两条路径都硬编码 `"fast"` 规则摘要**（`context_facade.go:193`、`compaction_policy.go:302`）；配置 `mode` 只影响手动 /compact | 自动压缩永远用规则抽取（goals/constraints/decisions），质量低 → "压缩太狠"主因 |
| T5 | LLM 摘要用独立 prompt + 扁平化单条消息（`llm_session_summarizer.go:112-117`） | 摘要调用是冷请求、看不到真实对话结构 |
| T6 | `enforceTargetHistoryBudget` 把压缩后历史压到 12K（`compaction_policy.go:180`） | 与 T2 叠加进一步削信息 |
| T7 | 摘要文本内嵌 `Compressed at: <time.Now()>`（`compress.go:367-368`） | 同输入摘要不具确定性 |

DSH 的四条对应设计（`region.ts:98 selectCompactableRange`、`index.ts:258-332 compactIfNeeded`、`summarizer.ts:26-28,74-76`）：
1. 触发阈值 = `contextWindow × thresholdRatio`（默认 0.8），per-model 策略；
2. **头部锚定区间替换**：从末尾累计 ≥ `retainTokens`（默认 0.16×window）确定保留尾，边界用 tool-pair 对齐（`toolPairingBalancedBefore`），只替换 `[0, cutoff]` 为 1 个 summary 节点，保留尾**逐字节原样**；
3. **摘要调用前缀对齐**：复用会话自己的 system + tools + 前缀消息，尾部追加摘要指令 → 摘要调用命中 provider 前缀缓存，且模型看到真实对话；
4. 压缩后摘要只依赖输入内容（确定性）。

## 目标布局

```
压缩前: [system][quasiStable] [h0 h1 ... hk | tk tk+1 ... tn] [volatile]
                          └──── 旧区（被压缩）───┘└─ 保留尾（verbatim）─┘
压缩后: [system][quasiStable] [summary(旧区)] [tk tk+1 ... tn 原样] [volatile]
```

## Phase 1：阈值与保留预算（触发层）✅

- 新增配置（`internal/core/config`，向后兼容，显式值优先）：
  - `agent.compaction.context_window_tokens`（默认 128000）；
  - `agent.compaction.trigger_ratio`（默认 0.8）；
  - `agent.compaction.retain_ratio`（默认 0.16）；
  - `agent.compaction.retain_tokens`（默认 0 = 由 ratio 推导）；
  - 校验 `retain_tokens < threshold_tokens`。
- `compactionTriggerTokens()`：显式 `trigger_tokens`/`compress_threshold` 优先，否则 `window × trigger_ratio`（保留 150K 上限）。
- 新增 `compactionRetainTokens()`：显式 `retain_tokens` 优先，否则 `window × retain_ratio`。
- `agent_wiring.go`：`compressor.SetRetainTokens(n)`。

## Phase 2：头部区间替换 + verbatim 保留尾（核心）✅

- `compress.go`：
  - 新增 `retentionBoundary(messages, retainTokens, fallbackKeep)`：从末尾累计 token ≥ retainTokens 定边界（无 retainTokens 时回退 `keep_recent_messages` 条数）；边界回调直到**不拆分 assistant tool_use / tool_result 对**（`toolResultNeedsEarlierUse`，pair 整体落入保留尾）；至少压缩最旧 1 条消息；
  - `CompactWithSnapshot` 改为 `[summary, 保留尾 verbatim]`：保留尾逐字节克隆，**删除** `sanitizeRecentMessageForContext` 对 tool result 的截断/改写（T2 修复）；`Compressor.RetentionTail` 供规则与 LLM 两条路径共用；
  - 删除 `Compressed at:` 时间戳（T7，规则摘要与模型摘要两处）；
  - 实现说明：规则摘要仍扫描整段 history（recent user/assistant verbatim 段落冗余保留），输出结构为 `[摘要, verbatim 保留尾]`。
- `compaction_policy.go`：`runCompaction` 不再调用 `enforceTargetHistoryBudget`（12K 截断删除，T6 修复）；删除该函数。
- 超大 tool result 保留在保留尾中逐字节可用；被压缩旧区经 `history_search` 仍可取全文。

## Phase 3：前缀对齐 LLM 摘要 + 默认 hybrid（质量 + 缓存）✅

- `SessionSummaryRequest` 新增 `Prefix []protocol.Message`（quasiStable 消息，供前缀对齐）。
- `llm_session_summarizer.go`：`buildPrefixAlignedRequest` 请求 = `[会话自己的 system][Prefix verbatim][区域真实消息 verbatim] + 尾部摘要指令`（`buildSummaryInstruction`）；删除扁平化 `buildLLMSummaryPrompt`/`renderMessagesForSummaryPrompt`；区域只对超大 tool result 做与主请求一致的 stub（`sanitizeMessagesForSummaryModel`）；保留哈希去重 + fast 兜底。
- 自动压缩（同步 `maybeAutoCompact` + 后台 `maybeStartBackgroundCompaction`）使用配置的 `mode`（T4 修复，默认 **hybrid**）；同步路径的 LLM 摘要以 `MaxLatencyMS` 为超时，超时/失败经 hybrid 回退 fast。
- 配置默认 `Mode: hybrid`（`defaults.go`）；`DefaultCompactionMode`/`autoCompactionMode` 对齐。

## UI 同步：右侧 Status 面板「上下文与召回」✅

- 后端 `internal/tools/session_admin.go` `ContextInspection` 新增 `retain_tokens`、`context_window_tokens`（`InspectContext` 填充）。
- `ui/web/src/features/chat/panels/ContextPanels.tsx`：Context 弹层新增「保留尾（原样保留）」行（`retain_tokens`）；阈值/占用率继续由 `compress_threshold` 驱动（值随窗口比例变化）；compaction 诊断行显示 mode（现在会是 hybrid）。
- `ui/web/src/i18n/messages.ts` 补充 `ctxPopoverRetention`（中/英）；`lib/types.ts` `ContextInspection` 补字段。

## 迁移与兼容

- 旧会话已有的 `KindSummary` + transcript 引用：`extractPreviousSummary` 不变，新语义可再压缩旧压缩历史。
- `keep_recent_messages` 配置保留但不再作为保留尾主控制（保留尾按 token 预算）；`target_history_tokens` 仅作摘要节点/旧区预算参考。
- 不触碰 volatile 尾部、transcript/history_search、checkpoint 语义。

## 验收标准

1. 长会话压缩后保留尾逐字节不变（测试断言），工具输出不再被截断 ✅（`TestCompactKeepsLargeRecentToolResultsVerbatim`/`TestCompactRetentionTailVerbatim`）；
2. 压缩频率随窗口比例缩放（默认 128K → 阈值 ≈ 102K）✅（`TestCompactionWindowScaledPolicy`）；
3. 自动压缩默认走 LLM（hybrid），摘要调用为前缀对齐（`TestLLMSessionSummarizerPrefixAlignedWithQuasiStablePrefix`）✅；
4. 同输入两次压缩输出一致（无时间戳）✅（`TestCompactSummaryHasNoTimestamp`）；
5. agent 全量测试与 clean HEAD 基线一致（无新失败）✅。
