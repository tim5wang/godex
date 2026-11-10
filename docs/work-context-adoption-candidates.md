# 工作桌面「借鉴候选」评估（7 条 + 安全面 2 条）

> 状态：Analysis（2026-11-11）。逐条以**代码实证**核对候选前提，给出建议（做 / 不做 / 单独立项）。**本文不改任何代码**，只交付评估结论供决策。
> 关联：[work-context-desktop-design.md](./work-context-desktop-design.md)（工作桌面主设计）、[work-context-attention-economics.md](./work-context-attention-economics.md)（注意力经济学）、[multi-tenant-commercialization-design.md](./multi-tenant-commercialization-design.md)（租户/隔离面）
> 方法：每条候选先核对"现状到底是什么"，再看差距与成本。凡与候选描述不符者单独点出（第 ④⑥ 条）。

## 0. 结论汇总

| # | 候选 | 实测结论 | 建议 |
|---|---|---|---|
| ① | 主线「常驻提示」（context 级 prompt） | 每轮已注入 title/goal/background/checkpoint，但**没有可编辑的 prompt 字段** | **做**（低成本，增量是一个字段 + 复用现成注入点） |
| ② | 知识「跨主线合成层」 | 跨主线**读**已有（recall scope=all）；跨主线**合成**无，且现有合成器只服务 memory，不碰 wc_facts | **先定边界再做**（否则与 memory 合成层职责重叠） |
| ③ | 引用分「单轮 / 常驻（pin）」 | 资源 ≤12 条、已确认事实 ≤8 条**每轮无条件注入** —— 全部是常驻，没有单轮概念 | **做**（低成本，注入点现成） |
| ④ | 审批作用域分档 + 超时作废 | **候选的前提有误**：无人值守超时作废**已存在**；作用域已有 4 档 | **只补 2 档**（限时/永久）+ 暴露到配置面，不要重做 |
| ⑤ | 凭据中介 + tainted egress | 无凭据代持、无污染继承；现有安全面只有内容筛查 | **单独立项**（跨系统：relay / tooling / 审批面） |
| ⑥ | 主线级成本/上下文开销可见 | **比候选描述的便宜**：会话级成本聚合与 HTTP 路由已存在，主线→会话映射也已存在 | **做**（数据链已通，只差 join + 呈现） |
| ⑦ | 多主线消息归属快判 | 回合与工作主线**无任何自动归属**；绑定是显式的 | **先做确定性规则**，轻量分类放后续（误判代价高） |

两条跨系统的（⑤ 及 ④ 若扩到"站点级"）不属于工作桌面单点改动，建议与安全面一起立项。

## ① 主线「常驻提示」

**现状（实证）**：`wc_contexts` 的列是 `tenant_id, id, owner_id, title, background, goal, status, version, checkpoint_json, created_at, updated_at, archived_at` —— **没有 prompt 字段**。但每轮已注入一段该主线的上下文：`work_context_feedback.go:50 renderWorkContextFeedback` 输出 Title / Goal / Background / Checkpoint（含 Next / Blocked / Unverified）。

**差距**：`background` + `goal` 事实上已在充当"常驻提示"，但它们是**叙事字段**（描述这条主线是什么），不是**指令字段**（每轮要模型遵守什么）。候选要的正是后者。

**建议做，成本低**：加一个 `prompt` 字段（跟随 `wc_contexts` 的常规迁移），注入位置复用 `renderWorkContextFeedback` 的现有段落——注入机制、预算（12k 上限）与"编辑下轮生效"的顺序都已经就位。风险点是它成为第二个"goal"而语义重叠，需在 UI 上明确区分。

## ② 知识「跨主线合成层」

**现状（实证）**：`wc_facts` 是**单主线**的（`context_id NOT NULL`），状态 candidate / confirmed / rejected / superseded，证据走 `source` + `source_excerpt`。跨主线**读取**已经存在（`facts.go:265` 支持 scope `all`/`global`）。但**合成**没有：`internal/core/memory/consolidation.go` 的 Consolidator 操作的是 memory 的 `Manager`/`Candidate`，全仓检索确认它**不读 wc_facts**。

**差距**：缺"把多条主线的 confirmed 事实汇总/去重/审计"这一层。

**建议先定边界再做**：真正的难点不是合并算法，而是**合成结果放在哪** —— `wc_facts` 的 owner 是单主线，跨主线结论没有归属列，直接落进去会与"每条事实属于一条主线"的既有模型冲突（也涉及隔离面）。且它与 memory 合成层会出现职责重叠：两者都做"把候选收敛成可信知识"。先决定二者关系（合并 / 分工 / 谁读谁），再动代码。

## ③ 引用分「单轮 / 常驻（pin）」

**现状（实证）**：`renderWorkContextFeedback` 里资源**上限 12 条**（`work_context_feedback.go:110-111`）、已确认/陈旧事实**上限 8 条**（`155-165`），并且这段是在 `turn.go:580 s.refreshWorkContextFeedback(session)` 于**每个用户回合边界**注入。也就是说：挂载到主线的资源与知识，**一律每轮常驻**。

**差距**：没有"只在本轮引用一次"的概念。注意力成本因此不透明 —— 挂 12 个资源与挂 1 个资源，每轮的注入代价不同，但界面上看不出来。

**建议做，成本低**：注入点、上限、排序都已就位；增量是给引用加一个"单轮 / 常驻"标记，并让注入按标记取。与 ⑥ 天然互补（一个说清"花了多少"，一个说清"为什么花"）。

## ④ 审批作用域分档 + 无人值守超时作废

**这条候选的前提与实测不符，值得单独说明。**

候选描述："把现有 manual/review/yolo 粗粒度扩成作用域授权（一次/这条任务/这个站点/限时/永久），且无人值守时发起的审批约 10 分钟无人响应就自动作废（动作不执行）"。

实测：

1. **超时作废已经存在**。`pendingTTL` 默认 **5 分钟**（`toolruntime/permissions.go:217`），且**可配置**：`tools.permissions.pending_ttl_seconds` 已进配置面与 schema（`core/config/schema.go:371`，LiveApply）。到期语义正是候选要的"动作不执行"：`permissions.go:626-629` 对已过期的 pending 直接返回 `permission request expired`；`pruneExpiredLocked`（`813-836`）清理 pending 与已过期的 decisions。
2. **作用域已有 4 档**，不是"只有一次"：`PermissionGrantScope` = `once` / `task` / `session` / `pattern`（`permissions.go:43-46`）。映射到候选想要的粒度：`once`≈一次、`task`≈这条任务、`session`≈会话、`pattern`≈按规则模式（`pattern` 的语义已覆盖"这个站点/这类目标"的一部分）。

**差距**：只差候选列举里的**显式"限时"与"永久"**两档（现有 `session` 是会话生命周期，不等于"限时 N 小时"）。

**建议只补这两档**，并把作用域选择暴露到配置/审批 UI；**不要**按候选字面重做审批面。这是本次评估里"省掉一次重复建设"的主要发现。

## ⑤ 凭据中介 + tainted egress

**现状（实证）**：全仓检索无凭据代持/替身 token/凭据代理（无 `vault` / `broker` / `impersonat` / `delegated token`），也无"污染状态继承"（无 `tainted`/污染）。`internal/core/security/` 目前只有内容筛查（`screener.go` / `llm_screener.go`）。

**建议单独立项**：这是候选里最重的一条，也是唯一**跨系统**的一条 —— 它要同时动工具运行时（凭证注入面）、relay/egress 路径与审批语义。它与既有决策"暂不做自托管容器、隔离层选型仅留备查；可交付面回到 per-tenant egress + 凭据中介 + 转发审计"方向一致，因此适合作为那个方向的具体条目立项，而不是塞进工作桌面这一层。工作桌面这边最多能承接"污染标记在 UI 上可见"。

## ⑥ 主线级成本 / 上下文开销可见化

**这条比候选描述的便宜 —— 数据链已经通了。**

实测：

1. **会话级成本聚合已存在**：`usage.Store.GetSessionUsage`（`store.go:927`）与 `ListSessions`（`986`，按 `session_id` GROUP BY 汇总 input/output/cache/billable tokens 与 credits），并有 `SessionUsageSummary` 结构（`types.go:303`）。
2. **已暴露为 HTTP**：`GET /usage/sessions` 与 `GET /usage/sessions/{id}`（`routes_usage.go:235/248`）。
3. **主线→会话映射已存在**：`wc_context_sessions`（`context_id` + `session_id`），读取入口 `SessionBinding`（`sessions.go:18`）与链接 API（`POST /v1/work-contexts/{id}/sessions`）。

**差距**：缺"把一条主线的全部会话成本加起来并呈现"，以及"本轮上下文用量"的呈现。**数据齐全，只差一次 join + UI**。

**建议做，性价比最高**：它是候选里唯一"基建已完成大半"的一条，且直接对应行业最大的隐性伤口（每轮重读历史导致成本不透明）。注意口径：按主线聚合是**展示层**的事，不应新增独立的事实数，否则与 usage 面板重复计数。

## ⑦ 多主线消息归属快判

**现状（实证）**：回合与工作主线**没有任何自动归属**（全仓检索无分类/路由到主线的逻辑）。绑定是**显式**的：`wc_context_sessions` + `SessionBinding`；且 `SessionBinding` 在一条会话被绑到不同 owner 的主线时返回冲突（`sessions.go:52-58`），可见模型假设"一条会话归属明确"。

**建议先做确定性规则**：候选提的"轻量分类百毫秒判归属、不确定回退 lead"方向合理，但**误判代价不对称** —— 把消息落错主线，比让用户显式指定一次更贵（工作桌面的整个价值是"归属可信"）。建议顺序：先做显式且廉价的归属（如消息内点名主线 / 一个轻量选择器），把自动分类放在有真实误判数据之后。

## 建议的推进顺序

1. **⑥ 主线级成本**（数据链已通，性价比最高）
2. **③ 引用单轮/常驻** 与 **① 常驻提示**（同一注入面，可一起做；成本低）
3. **④ 只补两档**（限时/永久）
4. **⑦ 显式归属**（确定性优先）
5. **② 跨主线合成**（先定与 memory 合成层的边界）
6. **⑤ 凭据中介 + tainted egress**（**单独立项**，跨系统）
