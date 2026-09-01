# godex Context 缓存命中率分析（长任务 ~96% vs pi/deepseek-harness ~99.5%）

> 状态：分析报告（任务 t-1788097103296-1）
> 日期：2026-08-31
> 目标：定位 godex 在长任务下，是哪些内容没有进入 context 缓存段，导致命中率只有 ~96%，而 temp/pi、temp/deepseek-harness 能到 ~99.5%。

---

## 1. godex 的缓存模型（先厘清前提）

默认模型 `deepseek.deepseek-v4-flash-vision-exp` 走 **`openai_responses`** provider（`base_url: https://api.deepseek.com`，见 `~/.godex/godex.yaml`）。该客户端（`internal/core/conversation/openai_responses_client.go`）使用 **自动前缀缓存**：`prompt_cache_retention` 下发、**不转发** `prompt_cache_key`（避免"整段全量匹配、一变全失效"的确定性缓存语义，注释明确说明）。

所以 godex 的缓存靠的是**请求体字节前缀的最长匹配**。要让缓存命中率高，必须保证**请求体靠前的字节跨 turn 完全稳定**，新的内容只允许**追加在尾部**。

请求构造顺序（`internal/agent/context.go` `buildContext` + `internal/agent/runtime.go` `BuildRequest`）：

```
[system]                    → buildRuntimeSystemPrompt（含模板 persona + 指令 + coding profile + capability check）
[quasiStable 运行时段]       → memory 索引 + skill_catalog/repo_map + active_skills + environment + tool_availability
[history]                   → 会话历史（append-only 增长）
[volatile 运行时段]          → memory 召回 + 项目 ledger + 后台通知 + 收件箱 + todo + 日期/星期 + repo 变更note
[tools]                     → req.Tools（激活集工具 schema，独立字段下发）
```

`quasiStable` 段放在 history **之前**（保证历史前缀缓存不受 per-turn 抖动影响）；`volatile` 段放在 history **之后**（其变化不破坏历史前缀，但它是**每次请求都重新发送、不缓存**的新鲜内容）。

---

## 2. 没有进入缓存段的内容（按影响排序）

### ① 每 turn 注入的 volatile 运行时段 —— **主要且持续**的 miss 来源

这是 godex 特有、pi/deepseek-harness 没有的设计。每个请求都往 history **之后**重发以下内容（`collectRuntimeMessages` + `context.go` `buildContext` line 87-104）：

- **记忆召回**：`memoryMgr.BuildContextLayers(query)`，随 query 变化（`latestPersistentUserText(history)`），每条 relevant 记忆含 content（截断 800 字符）+ related notes 引用。
- **项目 ledger 注入**：`formatProjectLedgerRuntimeMessage`（由 taskboard 写入，随执行状态变化）。
- **后台通知**：`formatBackgroundNotifications`（随 `PeekNotifications` 变化）。
- **收件箱预览**：`FormatInboxMessages`。
- **todo 状态**：`collectTodoStatus`（每 turn 调用 `todoMgr.Render()`）。
- **日期/星期**：`buildEnvironmentDatePrompt`（设计上刻意放尾段，避免 daily rollover 破坏历史前缀缓存——这是正确做法）。
- **repo map 变更 note + query focus**：`renderRepoMapChangeNote` / `renderRepoMapQueryFocus`（每 turn 计算当前工作区 vs 快照，随文件编辑变化）。

这些 token **从来不命中缓存**（因为在字节前缀中它们位于 history 之后、且每 turn 内容不同）。长任务里它们以稳定的比例拖低命中率。若 volatile 占输入平均 ~4%，就正好解释 96% vs 99.5% 的缺口。

> 补充：`quasiStable` 段（environment/tool_availability/skill_catalog/repo_map/active_skills）中，`environment` 段**不含日期**（日期已被移至尾部 volatile），因此是稳定的；`repo_map` 快照在会话起始与 compaction 时重建，跨 turn 字节稳定（文件编辑通过尾部的 change note 上报而不是改写快照）。这几点设计正确。

### ② 会话中途的隐式 bundle 激活 —— **间歇性整体断前缀**

`buildContext` 里 `activateImplicitBundlesForQuery(query)`（`implicitBundlesForQuery`）：当 query 命中 web/当前信息关键词时激活 `web` bundle。这会导致：

- `req.Tools` 激活集变化（新增 web_search/web_fetch/browser schema）；
- `tool_availability` quasi-stable 段变化；
- `capability_check` 系统提示变化（`buildCapabilityCheckPromptForProfile` 按 `catalogHasActiveTool` 是否注入 web/browser 提及）。

系统提示变化会**破坏从 system 开始的整段字节前缀**，把后续所有 history 的缓存一起打掉。虽然只在激活发生的那一轮一次，但在"长任务中偶发 query 触发 web"的场景会周期性打断缓存。

### ③ 早段历史被 `dedupeRepeatedLargeToolResultSummaries` 重写

`buildContext` 开头对历史做去重：当发现两个相同 SHA/artifact 的**大 tool result** 时，把**较早的那一个**原地改写为 summary 块。这会让被改写块**之后的所有历史字节**相对上一次请求发生变化 → 前缀缓存断。由于它是确定性改写（每次对同一对重复结果改出相同摘要），每个新重复对首次触发时造成一次整段 miss，之后恢复。长任务里多次重复大 tool 输出会多次触发。

### ④ 压缩（compaction）边界

`maybeAutoCompact` 会把 history 重写为摘要，必然断前缀。这是预期行为（配置 `compaction.trigger_tokens: 230000`，目标 history 80k），处于阈值边界才发生，是长任务不可避免的一次性成本，不算"漏进缓存段"，但与 ①③ 叠加后拖低长任务的平均命中率。

### ⑤ 工具 schema 顺序确定性

`ActiveSchemas()` → `activeNames` → `sort.Strings`，且 `tool_availability`/`capability_check` 的 bundle/tool 列举都 `sort.Strings` 后输出。**在工具集不变的前提下顺序是确定的**。因此 tools 本身不是持续 miss，只在 ② 工具集变化时变为 miss。

---

## 3. 对照 pi / deepseek-harness（为什么它们能 ~99.5%）

- **pi**：`Context` 只有 `{ systemPrompt, messages, tools }`（`temp/pi/packages/ai/src/types.ts:344`）。**不注入**任何 per-turn 的 volatile 运行时内容（无通知/收件箱/todo/记忆召回/日期/ledger 注入）。它的请求实质就是 append-only 的 history + 静态 system/tools，故前缀缓存几乎全命中。
- **deepseek-harness**：提示由固定的 context 层（agent-instructions / time-context / session-reference 等）组装，同样没有 godex 那种随执行状态变化的运行时段注入。它把上下文做成受控、可分层的静态构建，追求字节稳定。

**结论：差距的核心不是"某段内容写错了"，而是架构差异——godex 主动把"每 turn 新鲜、不确定、不缓存"的运行时段注进请求；参考实现刻意不这么做。**

---

## 4. 可行的改进方向（按性价比/风险排序）

| 优先级 | 措施 | 位置 | 预期收益 | 风险 |
|---|---|---|---|---|
| P0 | 把 volatile 段中**可以缓存的部分**前移进 quasiStable：例如把"记忆召回"这一依赖 query、历史上不变/少变的层（identity/core）放进稳定段，仅 relevant 层留在尾段 | `context.go` volatile/quasiStable 组装 | 高：直接减少持续 miss | 中：记忆召回本随 query 变化，需分级 |
| P0 | 减少 volatile 内容量：date/todo/通知等小段合并成单条消息，避免每个请求多次字符串拼接与 Envelope 格式化；且通知/inbox 用"仅在有新内容时注入"，Ack 后即从后续请求移除 | `collectRuntimeMessages` | 中：降每 turn 尾段 token | 低 |
| P1 | 记录并观测 volatile 段 token 占比，作为命中率预算指标，把"volatile/input"纳入 `prefixCacheInspection` 的告警 | `prefixCacheInspection` | 中：可观测哪个段在拖 | 低 |
| P1 | 隐式 bundle 激活发生后，在**同一 turn** 内保持工具集恒定（已如此），但避免 query 触发导致后续 turn 工具集回退/变化；可在激活时纳入会话级 ActiveTools 持久化，减少来回切换 | `activateImplicitBundlesForQuery` | 中：减少 ② 的中断 | 中 |
| P2 | dedupe 重写时改用**仅追加**备注（不原地改写早块），避免改写导致前缀断 | `dedupeRepeatedLargeToolResultSummaries` | 中：减少 ③ 的一次性断 | 中：可能加长历史 |
| P2 | 在稳定段/system 上做**缓存锚点**（cache_control breakpoint 前移），把 system+tools+quasiStable 作为独立缓存单元，history 前再断一次，使变化只影响尾部 | 请求构建层 | 中：让 ②③ 的影响局限在尾部 | 中——取决于 provider 是否支持多 breakpoint（Responses/自动前缀缓存下用处有限，Anthropic/OpenRouter 下有效） |

> 说明：`docs/cache-optimization-plan.md` 已记录"系统提示 stable/dynamic 分拆"(7) 与"cache-ttl 保留策略"(8) 为未落地项，可合并进上述 P1/P2。

---

## 5. 一句话总结

godex 的 96% 主要亏在**每 turn 主动注入、刻意放在 history 之后的 volatile 运行时段（记忆召回/项目 ledger/后台通知/收件箱/todo/日期/repo 变更）——这些内容按设计不进缓存段**；叠加**隐式 bundle 激活与 dedupe 重写**导致的间歇性前缀断裂。pi/deepseek-harness 之所以达 ~99.5%，正是因为它**不注入**这类运行时内容，请求几乎纯 append-only。缩小差距的方向是**把可缓存的运行时内容前移进稳定段、并压缩/去抖 volatile 段**，而非改 provider 或缓存字段。
