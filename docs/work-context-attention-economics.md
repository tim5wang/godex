# 工作桌面的注意力经济学（Attention Economics）：现状核查与增强方案

> 状态：Partial（§3 为代码实证基线；**AE1、AE3、AE4、AE5 已实施**，见 §6.1–§6.3；**AE2 已实施**，见 §6.4；§7 保留实施前的调研与选型记录）
> 日期：2026-11-07
> 关联：[work-context-desktop-design.md](./work-context-desktop-design.md)（工作桌面主设计）、[taskboard-plugin-design.md](./taskboard-plugin-design.md)（委派执行器）、[memory-design-principles.md](./memory-design-principles.md)（记忆治理）、[agent-role-and-bundle-design.md](./agent-role-and-bundle-design.md)（Agent 模板/多入口）
> 定位：解释「工作桌面」这一层在注意力经济中的位置，指出现有实现的杠杆缺口，并给出可分期落地的增强方案。本文不引入第二套 Agent Runtime，不改变既有权限与执行边界。

## 1. 命题

> 「AI 时代，人的注意力是最珍贵稀缺的资源，用你有限的注意力能撬动越多的 AI Agent 高质量工作，收获越多。」

这句话常被读成「多开 Agent 就多收获」，那是误读。准确的形式是：

**AI 没有消耗人的注意力，它把「可选工作量」放大了十倍。分母不变、分子暴增，于是注意力的相对价值上升。** 稀缺不是 AI 造成的，是供给爆炸造成的。

由此得到一个反直觉推论：**杠杆不在「让更多 Agent 跑」，而在「每个注意力片段撬动的 Agent 工作更多」**——即提高**注意力收益率**：

```
注意力收益率 = 已完成的有效 Agent 工作 / 人投入的注意力片段
```

### 1.1 注意力有三种消耗，成本量级不同

| 类型 | 内容 | 相对成本 | 可否工程化消除 |
|---|---|---|---|
| A 上下文加载 | 回到这条主线「现在到哪了」 | 低 | 高（可注入、可投影） |
| B 决策判断 | 要不要继续、往哪走、要不要收 | 中 | 中（信息密度） |
| C 结果验收 | 产物对不对、能不能收 | **高** | 中（重构产物形态） |

**杠杆顺序是 C > B > A**，而目前绝大多数 AI 工具（含本产品此前的重心）都在优化 A。本文的核心判断是：工作桌面已经把 A 做得不错，**B 只做了一半，C 基本没做**。

## 2. 三种实践的坐标系

工作桌面、任务看板、Agent Team 不是三个并列做法，它们各自降低**不同类型**的成本：

| 实践 | 主要降低 | 本质 |
|---|---|---|
| 工作桌面 | A 上下文加载（**注意力税**） | 让状态常驻，把「重新进入」从 O(N) 扫描变成一次注入 |
| 任务看板 | B 决策的一次性开销 | 把连续注意力切成可异步调度、可批处理的离散任务（**解耦决策与执行**） |
| Agent Team | 串行依赖（提高并行度） | 代价：验收量线性上升，人从执行者变为 lead |

**这解释了 Agent Team 近期的退潮**：它把人的角色从执行者搬到**评审者**，而评审正是成本最高的 C 类注意力。多一个 Agent 就多一条串行审批链，监督成本往往大于并行收益。方向不错，但把成本搬到了更贵的地方——**不是「多 Agent」错了，是「靠人盯每一个 Agent」错了**。

## 3. 当前实现的实证基线（2026-11-07 代码核查）

> 以下为对当前工作树代码的核查结论。**代码快照不代表生产在跑什么**，也不构成运行时承诺。

### 3.1 已经做对的部分

| 能力 | 落点 |
|---|---|
| 检查点是一份带版本的、人是作者的显式文档（不是聊天摘要） | `internal/core/workcontext/types.go`（Checkpoint: summary/in_progress/next_actions/completed/blockers/open_questions/evidence_refs） |
| 「自上次查看以来的变动」水位 + 未读数 | `internal/core/workcontext/attention.go:238` `UnreadCounts`；前端 `workDesktopStore` 的 `revealChanges` |
| 跨主线收件箱 + 一键跳主线与页签 | `GET /v1/work-attention`（`internal/runtime/httpapi/routes_work_contexts.go:60`）→ `internal/services/backend/work_context_attention.go:104` `AttentionInbox` |
| 后台巡检，进展不依赖用户打开桌面 | `work_context_attention.go:252` `runAttentionSweep`（挂在 Service 独占进程锁与 runCtx） |
| 提醒纪律：去重 / 静默时段 / 日上限 / 纪律检查先于写事件 | `pushNudge`（:360）、`pushAllowed`（:382）、`inQuietHours`（:397） |
| 推送按 owner 隔离，订阅跨重启持久化 | `internal/services/webpush`（`SubscribeFor`/`NotifyOwner`），归属键 `workcontext.ScopeKey` |
| 知识条目默认折叠只留标题行（可扫），未读气泡可点且指向它计数的内容 | `ui/web/src/features/workdesktop/KnowledgeCard.tsx`、`WorkDesktopRail.tsx` |

其中两条设计原则值得保留并推广：

- **「纪律检查在写事件之前」**——否则被静默时段拦下的提醒会被记成「已发」而永久丢失（`pushNudge` 注释明确写了这一点）。这是提醒功能不能有一丝含糊的地方。
- **「错的提醒比漏的提醒更贵」**——收件箱因此刻意用确定性规则而非 LLM 判断重要性。

### 3.2 结构性缺口（五条，均有代码依据）

**缺口 1｜收件箱不完整，用户仍须去别处扫。**
`AttentionInbox` 只产出三类：`delegation_review`（taskboard 卡 `in_review`，恒 high）、`todo_overdue`、`todo_soon`（`work_context_attention.go:133-165`）。而库中**已经存在**、同样值得人看一眼的信号没有进入同一份清单：

- 待审核的 candidate 事实（`wc_facts.status='candidate'`，前端 `KnowledgeCard` 有 accept/dismiss 按钮）
- 检查点的 `open_questions` / `blockers`（`types.go:36-37`，`OverviewPanel` 有板块）
- 计划项 `status='blocked'`（`internal/core/workcontext/todos.go:12` 合法状态含 blocked）
- 委派卡丢失（`source_missing`，前端有显式状态 `delegationState === "missing"`）
- Flow 失败 run（`internal/agent/flow_inspect.go` 已具备巡检与卡片渲染能力）

**只要还有一类在别处，O(1) 就退化回 O(N)**：注意力又被花在「发现该做什么」而不是「决定怎么做」。

**缺口 2｜排序缺「杠杆」维度。**
`sortAttention`（:183）只按 `priority → due_at → context_title`。一张 `in_review` 的卡可能挡着三件下游；一条过期计划项可能只是忘了删。当前对两者投入等量注意力，没有区分「解阻杠杆」。

**缺口 3｜收件箱是只读清单，不是队列。**
`AttentionInbox` 的每条只有「打开」一个动作。没有 claim（我接管了）/ snooze（三天后再来）/ 「交回 Agent 继续」/ dismiss-with-reason。**提醒疲劳的根源不是频率，是不可行动。**

**缺口 4｜提醒有纪律，没有经济性。**
`attentionDailyCap = 12` 与静默时段 22:00–08:00 是 **owner 级常量**（`work_context_attention.go:32-47`），不是按主线配额，也不按价值分配。7 条主线同时到关键时刻时，先到者占满当日额度。

**缺口 5｜未读数是「事件数」，不是「我关心的事数」。**
`UnreadCounts` 统计 `created_at > last_seen_at` 的事件；一条主线动 5 次就是 5。**多条同类噪音应压缩成一行**（「这条主线动了 5 次，其中 1 条需要决策」）。

### 3.3 与另两种实践尚未打通的点

`internal/services/metering`、`internal/services/usage` 与 `workcontext` **无任何挂钩**：今天无法回答「这条主线至今烧了多少 token、产出多少、注意力投入多少」。度量缺失使得「注意力收益率」只能靠感觉。

## 4. 设计原则（本文提出的判据）

以下六条作为后续所有桌面增强的评审判据，可直接用于代码评审。

- **P1 默认呈现变化，状态按需展开。** 人脑对 delta 的感知远强于对状态的感知。「自上次查看以来的变动」已是此原则，应推广到委派结果、知识条目、计划。
- **P2 提醒的经济性优先于纪律。** 纪律（去重/静默/上限）保证「不烦人」；经济性保证「值得看」。两者都要，但经济性决定功能是帮忙还是添乱。
- **P3 每条提醒必须可行动。** 人做出的动作本身成为学习信号（哪类提醒常被处理、哪类总被忽略）。无动作的提醒是纯负债。
- **P4 默认动作 + 事后否决。** 把「等人」从无限期阻塞变成「有超时的委托」。注意力不足时系统仍前进，人只处理**异常与不可逆决策**。这是修 Agent Team 的正确方式：不靠多开 Agent，靠 Agent 之间用默认动作协作，人只做否决。
- **P5 注意力预算显式化。** 每条主线有配额；Agent 想要更多注意力必须给出理由、合批、提高信息密度。此时 Agent 竞争的不是算力，是**主人的注意力配额**。
- **P6 只在切换时刻并置，深工作时只看见一条。** 把 7 条主线常驻在眼前会提高「并置干扰」，收件箱会退化成焦虑仪表盘。跨主线视图应只在打开桌面、每 N 小时、主动呼出时出现。
- **P7 采样用确定性规则，不用 LLM 猜重要性。** 延续既定原则：错的提醒比漏的提醒贵。（LLM 生成「验收摘要」是另一件事：那是压缩已有证据，不是判断重要性。）

## 5. 增强方案

按「杠杆/成本比」排序。**L1 是唯一建议立即做的**，其余按需。

### L1｜把收件箱从「只读清单」升级为「注意力队列」（改动最小、收益最大）

1. **完整化**：把 §3.2 缺口 1 的五类信号并入同一 `AttentionItem` 投影。**不新增采集，只补投影**；`AttentionItem{kind, priority, context_id, context_title, title, detail, due_at, tab}` 已是合适的统一契约。
2. **合批压缩**：同一主线 + 同一 kind 的多条折叠为一行，行内标注「N 条」可下钻。注意力成本对条目数近似对数增长——这是「降低理解负担」的第一性形式，也直接修缺口 5。
3. **杠杆排序**：在 `sortAttention` 的 key 中加入确定性杠杆项：`阻塞下游数`（依赖关系可从 `wc_relations` / 卡依赖取）× `停滞天数` × `是否卡验收`。**规则可解释、可测试**，不引入模型判断。
4. **加动词**：每条支持 claim / snooze / delegate-to-agent / dismiss-with-reason。有了动词它才是队列，也顺带产出「注意力账本」（每天处理几条、每条多久），使 L4 的度量成为可能。
5. **按主线配额替代全局配额**：`attentionDailyCap` 由 owner 级常量改为「owner 总量 + 每主线上限」，在同样 12 条的预算下优先保证「只有一两条主线在关键时刻」的场景不被别人挤掉。

验收：新增测试覆盖「同一主线同类多条压缩为一行且可下钻」「杠杆排序稳定且可解释」「snooze 期间不再出现且到期复现」「dismiss-with-reason 记录动作」；e2e 覆盖「收件箱出现五类信号」「打开收件箱仍不推进 seen 水位」。

### L2｜让 Agent 输出「一眼可验收」的产物（补上 C 类注意力）

1. **验收包（最高单点杠杆）**：`in_review` 的卡不应只给标题 + 两个按钮，应给结构化自查——做了什么 / 动了哪些文件 / **怎么验证的（命令 + 输出摘要）** / 没做什么与风险 / 待确认项。taskboard 卡已具备 Execution / Checklist / Research 结构，**只差渲染形态**，不需要新的执行能力。
2. **证据徽章**：把「有命令输出/有证据」「推测」「未核实」做成通用 UI 原子（检查点已有 `evidence_refs` 语义、事实已有 source/source_excerpt）。这样「我该信到什么程度」不必每次重新判断。
3. **复用既有巡检渲染**：`flow_inspect.go:150` `InspectionCardMarkdown()` 已是「报告 → 紧凑卡片」的现成形态，可作为验收包与每日 digest 的渲染范式，避免另起一套。

### L3｜把「注意力预算」显式化（把命题变成产品机制）

1. **每日一页 digest**：跨主线 delta 合批成一页、按杠杆排序、一行一条可下钻。静默时段的提醒不再丢弃而是**攒到次日 08:00 一次性给出**（注意保持既有不变量：纪律检查仍在写事件之前）。
2. **配额制**：每主线每日注意力配额；超出需「理由 + 合批 + 提高密度」才占额度。
3. **红点老化降级**：清不掉的未读自动 aging，降级为摘要行，避免红点变成长期欠债感。

### L4｜打通度量（让「收益率」可观测）

现成数据即可计算，无需新采集：

| 指标 | 定义 | 数据来源 |
|---|---|---|
| 注意力收益率 | 完成的有效工作 / 人做出的决策数 | taskboard 卡完成数 ÷ 收件箱动作数 |
| 提醒可行动率 | 收到提醒后 X 分钟内产生动作的比例 | 收件箱动作时间戳 vs 事件时间 |
| 默认动作否决率 | 自主执行被否决的比例（Agent 自主度的直接旋钮） | P4 机制的否决记录 |
| 上下文重建耗时 | 打开主线 → 发出第一条指令的时间 | 前端交互埋点 |

### L5｜三者的有机结合：桌面是调度器，另两者是生产者

统一抽象**已经存在**——就是 `AttentionItem`。今天它只是「来源太少（3 类）」。把它提升为一等契约后：

```
任务看板  = 条目的生产者 + 执行器（卡状态、验收）
Agent     = 条目的生产者（delegate / executor / flow_inspect / 记忆候选）
工作桌面  = 条目的调度器（排序、合批、配额、配额内动作）
```

「有机结合」最具体的一步：**让 Agent 能主动向上申请注意力，且必须守格式**——

> 一句话结论 · 要什么决策 · 我的建议 · 证据 · **如果你 5 分钟不看，我的默认动作是什么**

最后一项即 P4。它把「解耦决策与执行」补完整：解耦了但没给默认值，注意力不足时系统整体停摆；有了默认值，注意力不足时系统仍前进。

## 6. 分期与验收门禁

| 期 | 内容 | 依赖 | 门禁 |
|---|---|---|---|
| AE1 | L1 完整化 + 合批 + 杠杆排序 | 无新采集 | ✅ 已实施（2026-11-07），见 §6.1 |
| AE2 | L1 动词（claim/snooze/dismiss/委托） | 需新增少量接口（动作本身要落审计） | ✅ 已实施（2026-11-07，claim/snooze/dismiss；「委托」未做），见 §6.4 |
| AE3 | L2 验收包 + 证据徽章 | 复用卡 Execution/Checklist | ✅ 已实施（2026-11-07），见 §6.2 |
| AE4 | L3 digest + 配额 + 老化降级 | 依赖 AE1 排序 | ✅ 已实施（2026-11-07，仅老化降级 + 按主线配额；每日 digest 未做），见 §6.3 |
| AE5 | L4 度量 | 依赖 AE2 动作记录 | ✅ 已实施（2026-11-07，部分：预算/未读/老化三项，依赖 AE2 的「注意力收益率」未做），见 §6.3 |

通用门禁（延续既有约定）：

- 后端：SQLite 事务/CAS/幂等、租户与来源鉴权、同事务事件；用真实 modernc SQLite 而非 mock。
- 前端：i18n 中英全量对齐、视图模型测试、真实 `pnpm build`。
- 文档：`make docs-check` 通过。

### 6.1 AE1 实施记录（2026-11-07）

只做**读模型**：收件箱投影 + 合批 + 杠杆排序。不碰巡检、推送、纪律常量，不新增 migration。

- **新增信号采集**（`internal/core/workcontext/attention_signals.go`）：`ListCandidateFacts`（待审事实）、`ListBlockedPlanTodos`（阻塞计划项，**不要求有截止时间**——`ListOpenPlanTodos` 因服务于截止日而过滤 `due_at <> ''`，恰恰看不见「卡住但没定日期」这一类）、`DownstreamBlockedCounts`（每条主线被多少条其它主线 `depends_on`，即被依赖数）。三者都 JOIN `wc_contexts` 并过滤 `owner_id`，跨主线也绝不越所有者。
- **收件箱重构**（`work_context_attention.go`）：`AttentionInbox` 拆成 `delegationAttention` / `factAttention` / `planAttention` / `checkpointAttention` 四个投影 + `groupAttention` 合批 + `sortAttention` 排序。新增 kind：`delegation_missing`（卡被删，原来是静默跳过：一条无人能解析的链接本身就是待决事项）、`fact_review`、`todo_blocked`、`checkpoint_blocker`、`checkpoint_question`；kind 常量集中在文件头，前端按 kind 出标签，不再由后端写展示文案。
- **合批**：同一主线 + 同一 kind 折叠为一行，带 `count`（用户读的是行数，不是事实数）+ `also`（折叠标题，上限 4）；折叠行的 `due_at` 取组内**最早**那个。行数上限 50（检查点类），因为这两份清单会随主线年龄无限增长。
- **杠杆排序**：`priority → 被依赖数(降序) → 最早截止 → 稳定 tiebreak`。一条卡住但有 3 条主线在等的主线，排在一条同样紧急但无人依赖的主线之前。
- **修掉一个我自己引入的重复**：阻塞项若带截止时间，会同时命中「过期」与「受阻」两条投影；`planAttention` 显式跳过 `status == blocked`，保证一项只出现一次。
- **隔离边界（范围变更，重要）**：原计划把「flow 失败 run」并入收件箱，实施前核对发现 **`flowRunRecord`（`internal/agent/flow_store.go`）没有任何 tenant/owner 字段**，且 flow 与 work stream 之间不存在数据模型关联。按 owner 隔离面投影一个无归属的信号 = 跨用户泄露，直接违反既有红线（`NotifyOwner("")` 宁可返回 0 也不退化成广播）。故**不做**，作为阻塞项记录在案；要做的前提是先给 flow run 建立 (tenant, owner) 归属。
- **前端**：`AttentionInbox` 按新 kind 出标签、显示「共 N 条」与「阻塞 N 条主线」标签、并列出折叠标题；`apiWorkContext.ts` 的 `AttentionItem` 增补 `count`/`blocked_count`/`also`；i18n 中英新增 8 键。

验证（2026-11-07）：

- `go build ./...`、`go vet` 通过；`go test ./internal/core/workcontext/... ./internal/runtime/httpapi/...` 通过；`internal/services/backend` 除既有 TempDir 清理 flake 外通过（隔离复跑通过）。
- 新增后端测试：`TestAttentionInboxSurfacesFactsBlockersAndQuestions`（五类新信号 + 正确页签）、`TestAttentionInboxGroupsRepeatedNoise`（3 条过期 → 1 行 count=3 且 due 取最早）、`TestAttentionInboxRanksLeverageAboveDueTime`（下游更过期但上游因被依赖仍排第一）、`TestAttentionInboxReportsLostDelegationCard`、`TestAttentionInboxNeverCrossesOwnersOnNewSignals`（隔离面扩到新信号）。
- 前端：`pnpm typecheck` 通过；`vitest run` 49 文件 / 497 通过；i18n 中英对齐通过。
- `playwright work-desktop` **三端 27 passed / 36 skipped / 0 failed**（新增「同一主线多条提醒合批 + 展示被依赖数」用例，含严格模式下的杠杆标签断言）。
- arch 门禁两个失败仍为**既有基线**（`routes_account.go:registerMemberRoutes` 复杂度 45、`ChatPageView.tsx` 行数），本轮未触碰这两个文件（stash 后复现证实）。

### 6.2 AE3 实施记录（2026-11-07）

验收包：让「能不能收」不靠读执行记录。

- **关键发现（为什么便宜）**：后端与前端类型早已带齐所需字段，**是渲染时把它们丢了**。`WorkDelegationView.Card` 就是完整 `taskboard.Card`，而 `WorkDesktopSections.tsx` 只渲染了 `checklist[].text`，丢掉 `evidence`；`observed_paths` / `research.open_questions` / `merge_report` / `executions[].summary` 全部未用。taskboard 自己的弹窗（`TaskBoardDialogs.tsx:310`）早已渲染 `evidence`——既有先例。
- **新增纯函数** `acceptancePackage(card)`（`workDesktopView.ts`）：从卡自身字段装配自查包——`summary`（取**最后一条非空** execution summary；无执行则留空，**不拿冻结简报冒充结果**）、`observedPaths`（gate 3 运行上报）与 `declaredPaths`（gate 1 声明未验证，与前者**分开展示**且去重，observed 优先）、`checks`（验收条件 + 证据）、`open`（research.open_questions）、`conflicts`（merge_report）、`hasEvidence`。
- **诚实规则（本项的核心）**：`hasEvidence` **只在「已勾选且有证据」时才算真**——勾选本身只是声称。一个勾了但无证据的条目会被单独标注「（未附证据）」；若一条证据都没有，面板直接明说「没有可核验的证据」并解释含义，而**不是**让绿勾暗示已验证。未勾选项上的 evidence 不计入（那是备注，不是验证结果）。
- **新增展示组件** `AcceptancePanel.tsx`（98 行）：单列分区渲染上述五块；证据串按原文换行渲染并加引号（当作记录，不当作者表述）；跨卡路径冲突用 error Alert（裁定冲突正是 reviewer 的职责，不能理）。
- **接线**：委派卡改用 `AcceptancePanel`；未取到卡时仍显式报 `cardMissing`。
- **i18n**：中英新增 12 键；样式 `wd-accept-*` 追加入 `workDesktop.css`。
- 无后端改动（无新接口、无新执行能力），故不重建二进制。

验证（2026-11-07）：

- 新增纯函数单测 6 条（装配、无证据不算、未勾选证据不算且路径不重复、无执行时 summary 留空、取最后一条非空 summary、冲突标签回退）；`workDesktopView.test.ts` 13 通过。
- 前端：`pnpm typecheck` 通过；`vitest run` 49 文件 / 503 通过；i18n 中英对齐通过。
- `playwright work-desktop` **三端 29 passed / 40 skipped / 0 failed**；新增两条用例：「展示验收包且把主张与证据分开」（断言证据可见、「（未附证据）」被标出、observed/declared 分区、重叠路径只列一次、未决与冲突可见）与「卡上没有可核验项时直接明说」（种子卡的验收项未勾选且无证据）。

### 6.3 聚焦修复 + AE4 老化/配额 + AE5 度量（2026-11-07）

用户实测反馈：「点击桌面消息数能联动概览，但概览里的内容还缺聚焦/高亮，就是只能看到」。三项一次性实施，均按「低认知负担」定形：

**一、修复揭示缺聚焦（真 bug，不只是缺高亮）。** 根因不是样式，而是**揭示请求被静默丢弃**：`WorkDesktopRail.openUnreadChanges` 在同一次点击里既 `setSection("overview")` 又 `revealChanges()`，于是 OverviewPanel **是被这次请求创建出来的**；`FoldCard` 用 `useRef(revealNonce)` 与挂载值比较，挂载时读到的已是最新 nonce → 判定「已处理」→ 展开与标记都不触发。此前用例之所以看着通过，只是因为新卡默认 `defaultOpen=true`，看起来像展开了——**从其它板块点未读气泡这条最常见路径上，卡片没有任何标记**。

- 修法：把「待回应的揭示请求」从组件 ref 移到 store（`changesFocusNonce` + `changesFocusAck` + `ackChangesFocus`），面板消费后确认；`FoldCard` 的 `revealNonce` 换成布尔 `reveal`（只在 true 方向动作，回应请求的回落不会与用户自己的折叠打架）。
- 高亮本身：一次性瞬时高亮（accent 描边 + 淡出，2s）后自动消失，**不新增常驻 UI**；带 `prefers-reduced-motion` 降级（保留标记、去掉动画）。

**二、AE4：老化降级 + 按主线配额（每日 digest 未做）。**

- **老化降级**（`unreadAgingAfter = 72h`）：超过 3 天的未读不再当作待办提醒，红点降级为**静音数字**（可点、计数仍真实）。这是「老化」而非「隐藏」——净不掉的标记长期存在会从「请求」变成「背景负债感」。服务端算（`NewestUnreadEventAt`），两个视图不会对「哪条主线陈旧」得出不同结论。**空未读的主线永不被标陈旧**，否则安静主线会永久带灰点。
- **按主线配额**（`attentionPerStreamDailyCap = 4`）：只有 owner 级全局上限时，**哪条主线碰巧到期项最多就吃光全天预算、其它主线永远不说话**。两道限额都在**写事件之前**检查，保留「静默时段不烧提醒」不变量（`pushAllowed` 现在收 contextID）。

**三、AE5：注意力度量卡（默认折叠、可隐藏）。** 今日提醒**带上限显示**（`5 / 12`——光一个数字会引来错误结论）、未读总量、陈旧主线数。三项均由收件箱已读数据导出，不是第二事实源。**默认折叠**，折叠就是「支持隐藏」的全部实现：不加关闭按钮、不落持久化偏好，不用它的人看到的界面与从前完全一致。计数失败时退化为 0，不让 ledger 把整个收件箱带挂。

验证（2026-11-07）：

- 新增后端测试 4 条：`TestAttentionInboxAgesStaleUnread`（陈旧被标出、计数不丢）、`TestAttentionInboxNeverAgesAStreamWithNothingUnread`、`TestAttentionMetricsReportSpendAgainstCap`（用量与上限同报）、`TestPushPerStreamCapBoundsOneBusyStream`（忙碌主线上限用尽后另一条主线仍能进来）。
- 前端：`pnpm typecheck` 通过；`vitest run` 49 文件 / 503 通过；i18n 中英对齐通过。
- `playwright work-desktop` **三端 31 passed / 44 skipped / 0 failed**；新增/加强：揭示标记断言（`data-flash`）、陈旧计数降级（无 Badge、有静音数字、仍可点）、度量卡默认折叠及「5 / 12」与每主线上限。
- **反向验证过非空洞**：临时把 `reveal={pendingReveal}` 改成 `reveal={false}` 后，`data-flash` 断言**真失败**，恢复后通过——即该断言真的在守护这个 bug。
- **flake 判定用 A/B 对照而非名字**：全量中 `TestScreenUserInputHookFireAndForgetShadow` 失败并不等于本轮引入。我的改动 `-count=5` 失败 2 次，stash 后基线 `-count=5` 也失败 1 次 → 既有 flake（根因：shadow screener 的后台分类 goroutine 在测试结束后才写盘，与 `t.TempDir` 清理竞态，见 `security_screener_test.go:73`）。只看名字会「猜对而推理错」。

### 6.4 AE2 实施记录：收件箱动词（2026-11-07）

按 §7 的选型结论实施（选项 B：**新表**，而非事件派生）。实施了 claim / snooze / dismiss；「委托给 Agent」未做（它需要真的能启一个 Agent，与 P4 默认动作一并考虑）。

**一、存储：migration 4 新表 `wc_attention_actions`。**

不在 `wc_events` 里存（§7 已实测：未读与变更流都是 kind-agnostic，动作入事件会把自己的忽略变成一条未读变动）。表形类似 `wc_seen`：记录的是**所有者与行的关系**，不是流内容，所以**不动流版本**，浏览/处置都不算编辑。主键 `(tenant, context, item_key)` 保证**一行一个判定**（snooze 后 dismiss 是一个事实而非两条），重复动词幂等。

**二、身份：组级 item_key `{kind}:{context_id}`。**

在收件箱投影里**只在一处**打这个标记（`groupAttention` 之后），不在七个构造点各打一次——因为 key 按定义就是组级的，在归一化的那一步派生才能真正保证「UI 对的就是它渲染的那一行」。与折叠成一行的设计一致；代价是 dismiss 会忽略整组，所以 **dismiss 强制带理由且可撤销**。

**三、读时判断 snooze 到期（无后台任务）。**

`AttentionActionActive(action, now)` 在**读投影时**判断；未到点则不投影。因此**不需要调度任务**，也就不存在「任务没跑所以提醒丢了」这种失败模式。

**四、sweep 尊重动词，且在写事件之前。**

`pushNudge` 新增 `itemKind` 参数并先查动词，再走纪律检查（静默时段/配额），**最后**才写事件。顺序是关键：先写事件会让被 snooze 的提醒占住 dedup key、**永久吞掉**——正是 `pushNudge` 注释点名最不能出的结果。有专门回归测试钉住它。

**五、动词不计未读，但计「决策」。**

处置一条不是阅读主线，所以未读徽标不动；但它是人做出的决策，所以进度量卡新增的**「今日决策」**（AE5 收益率的分母）。度量卡默认仍折叠，不增加默认负担。

**六、推迟区（默认折叠）：判定可见、可撤销。**

被 snooze/dismiss 的行不是消失，而是收进「已推迟 / 已忽略」折叠区，并显示**到期时间**或**理由**，且可撤销（撤销=删该行状态）。让一条建议静默消失与 bug 无法区分。

**七、一个被测试暴露的真问题：两套时钟会分叉。**

首次写测试就失败：`UpsertAttentionAction` 原本在 store 里读**真实墙钟** `time.Now()`，而收件箱其余部分读**可注入的** `s.now`——「wake 在未来」的校验与「snooze 还有效吗」的读取因此可以不一致。修法是把 `now` 作为参数传入（与 `AttentionActionActive` 同一模式），并新增 `Service.RecordAttentionAction` 包一层注入 `s.now()`。**这是测试先于实现发现的设计缺陷，不是测试写错。**

验证（2026-11-07）：

- 后端存储层新增 7 测试：幂等与替换、校验（未知动词/无理由的 dismiss/过去的 wake/空 key/默认 wake 时间）、**owner 隔离**（Bob 看不到也写不了 Alice 的判定）、撤销幂等、到期语义（含**无法解析的 wake 时间不能永久藏行**、未知动词不得藏行）、item key 往返、按日计数边界。
- 后端服务层新增 4 测试：**snooze 在写事件之前生效**（并断言**未写 nudge 事件**，钉死「记录先于检查会永久吞掉提醒」这一结论）、**读时到期无需 sweep**、claim 保留行而 dismiss 隐藏、**动词不计未读但计决策**。
- `go build`/`go vet` 干净；core/workcontext 与 httpapi 全量通过；backend 全量仅剩既有 TempDir 清理 flake（见下方 flake 说明）。
- 前端：`pnpm typecheck` 通过；`vitest` 49 文件 / 503 通过；i18n 中英对齐通过。
- `playwright work-desktop` **三端 34 passed / 50 skipped / 0 failed**；新增 3 条：snooze 行离开列表、进入折叠推迟区（显示到期时间）、撤销后回来；claim 行仍在列且标「已接手」+ 今日决策计数；dismiss 必须填理由才能确认（确认按钮先 disabled）。
- 无新增前端依赖；样式 `wd-attention-verbs` / `wd-attention-deferred-*` 追加入 `workDesktop.css`。

**未做 / 待定**：委托给 Agent（P4 前置）；snooze 时长目前固定 3 天（`DefaultSnoozeDuration`，客户端可传 `until_at` 覆盖，但 UI 未给选项）。

### 6.5 动词细化：单条动作 + 自适应老化（2026-11-07）

两项都是「把常量换成真实边界」的收尾：一项让动词可以精确到条目，一项让老化阈值不再是拍的。

**一、单条动作：组级与条目级共存，更具体的判定优先。**

- **身份**：新增条目级 key `{kind}:{context_id}:{entity_id}`（实体 id = 计划项/事实/卡的 id）。组级 key 保留，两者共存。
- **UI**：逐条处理默认**折叠**（多数行是整行处置，把每条三个按钮铺开会把三栏清单变成按钮墙）；展开后每条一行。
- **优先级**：条目级 > 组级。理由：点名一条的人说得比处置整行的人多，让宽判定覆盖窄判定会静默丢掉更精确的意图。读路径与 sweep 走**同一个** `AttentionActionFor`，所以「收件箱隐藏了却还在推送」不会发生。
- **行不会凭空消失**：只有**所有**条目都被推迟，行才离开清单；否则它带着剩下的条目留着，且报告剩下几条。若只剩一条，就不再算组（`count` 归零、`entries` 清空），避免出现「一个条目的组」这种自相矛盾的形状。
- **被推迟的条目单独成行**报在推迟区，带它自己的标题与理由——把它塞进行的副本里会丢掉「是哪一条」。
- **诚实边界**：`checkpoint_blocker` / `checkpoint_question` 指向检查点里的**自由文本**，没有稳定 id，因此**不给**条目级动词（不造标题哈希：文本一改就静默脱钩），只列出来并在行内说明只能整组处理。行级动词仍可用，所以不是死路。

**二、自适应老化：按每条主线自己的推进节奏。**

固定 72h 在两个方向都错：真的两周才动一次的主线三天就被叫陈旧，而每小时都在动的主线早该陈旧。

- **阈值** = 该主线近期事件间隔的**中位数** × 3，夹逼到 **[24h, 14d]**。历史不足 3 个间隔的主线没有可用节奏，**回落到固定 72h**（一个间隔是噪声，会让阈值在两次读取间跳动）。
- **取中位数而非均值**：一次长假/一个阻塞周不该把阈值拖歪。
- **排除系统自己写的事件**（`nudge` / `task_status`）：否则「提醒一个安静的主线」会把这条主线显得很活跃，**重置掉正是触发提醒的那个陈旧性**——它反而永远不会老化，完全反了。这一条有专门回归测试钉住。
- **地板/天花板**：地板防止快主线在用户还没看到红点前就被标陈旧；天花板防止真正休眠的主线被当作永远活跃。
- **不新增后台任务**：节奏在读投影时从近期事件算出来。

验证（2026-11-07）：

- 后端新增 5 服务层测试：单条处置后**行存活且报告剩余条数**、条目级 claim **压过**组级 dismiss、自由文本条目**无条目 key**、节奏推算（快→地板 / 慢→6 天 / 休眠→天花板 / 历史不足→无阈值）、**系统事件不计入节奏**；存储层新增 3 测试：条目 key 需实体 id、组 key 归属、优先级选择。
- `go build`/`go vet` 干净；core/workcontext 与 httpapi 全量通过。
- 前端：`pnpm typecheck` 通过；`vitest` 49 文件 / 503 通过；i18n 中英对齐通过。
- `playwright work-desktop` **三端 36 passed / 54 skipped / 0 failed**；新增 2 条：逐条处置后行存活且被处置条目进推迟区（含理由可读回）、自由文本条目不提供条目动词且说明原因。

## 7. AE2 前置调研：动词状态放哪（2026-11-07；选项 A 的副作用已实测，其余为静态核查）

> 本节是实施**前**的调研记录，保留供追溯：实施结果见 §6.4（选型与本节结论一致，选项 B）。

**问题**。AE2 要给收件箱加动词（claim / snooze / dismiss）。真正决定成本的不是接口数量，而是**状态放哪**：snooze 是「持续到某时刻」的状态，与既有「读时投影」的 read model 不同类。

### 7.1 实证发现（代码核查）

1. **`AttentionItem` 没有稳定 id，而且它代表一组而不是一行**。字段为 `{kind, priority, context_id, context_title, title, detail, due_at, tab, count, blocked_count, also}`；`groupAttention` 按 `(kind, context_id)` 合并同类多条并写入 `count`/`also`。所以「给这一行加已忽略/稍后」在今天没有可挂的对象——必须先定义 item key，且必须决定**动作作用于组还是组内每一条**。
2. **提醒侧有稳定 key，读路径没有**。sweep 用 `nudge:card:{cardID}:v{version}` / `nudge:todo:{todoID}:{dueAt}:{state}` 作 `wc_events.dedup_key`（该列有 `UNIQUE(tenant_id, context_id, dedup_key)`）。读路径 8 类条目里，todo_* 可回指 `wc_todos.id`，delegation_* 可回指卡 id，fact_review 可回指 fact id，而 **checkpoint_blocker / checkpoint_question 指向的是检查点 JSON 里的一段文本，没有 id**。
3. **全仓没有 snooze/claim 先例**（`temp/orca` 是无关参考代码，非本仓机制）。`wc_todos` 已有 `assignee` 字段，是唯一近似 claim 的既有载体，但只覆盖计划项。
4. **「不新增 migration」是自设边界，不是技术约束**。迁移机制现成：`schemaVersion=3` + 有序 `migrations` map，事务内应用并记 `wc_schema_migrations`；新增一版约 20 行 DDL。
5. **已有「状态表」先例，其写法正是为了绕开版本号**：`wc_seen(tenant_id, context_id, owner_id, last_seen_at)`，注释明说「放在自己的表里，好让浏览不 bump 上下文版本（浏览不是编辑）」。snooze 的状态与它同类——是「人对条目的处理状态」，不是流内容。

### 7.2 三个选项与取舍

| 选项 | 做法 | 代价 | 结论 |
|---|---|---|---|
| A 事件派生 | 动作写 `wc_events`，`dedup_key` 形如 `action:snooze:{itemKey}`，读时查最新一条 | 零新 schema；但见下方致命副作用 | ❌ 不建议 |
| B 新表 | `wc_attention_actions(tenant, context, item_key, verb, until_at, reason, actor, created_at)`，PK 三列；读路径 LEFT JOIN | migration 4（~20 行 DDL）+ 需定 item_key | ✅ 建议 |
| C 复用 wc_todos | snooze=改 `due_at`，claim=写 `assignee` | 只能覆盖计划项 | 仅作 todo_* 快捷路径，不能当统一底座 |

**选项 A 的致命副作用（已实测确认，非推断）**：`wc_events` 同时是「未读计数」与「变了什么」的来源，而 `UnreadCounts` 的查询**不带 kind 过滤**（`attention.go:238-247`，只按 `created_at > last_seen_at` 分组）。把动作写进 wc_events，会让你**自己的 snooze 变成一条未读变动**，污染未读账本与 AE5 度量。要绕开就得给未读查询加 kind 白名单，把复杂度转移到读路径；此外 snooze 的「到点复现」还要额外 sweep，且日志无界增长需压实。

实测（`internal/core/workcontext/unread_kinds_test.go` · `TestUnreadCountsCountsEveryEventKind`，写成 characterization test 守护此语义）：给已 `MarkSeen` 的主线写入一条 `attention_action` 事件后，`UnreadCounts` 从 **0 变 1**，且该事件出现在 `Focus.changes` 里——即用户自己的 dismiss 会被当成「未读变动」报回给自己。

**选项 B 的好处**：动作与内容分离（不污染未读/变更流）；动作表天然就是 AE5「注意力收益率」的账本；与 `wc_seen` 先例同构。snooze 的「到期复现」改为**读时判断**（未到点则不投影），省掉一个后台唤醒任务及其不变式。

### 7.3 item_key 怎么定（A/B 都绕不开，这才是真正的设计决定）

- 必须**稳定**（同一件事跨两次读是同一 key）且**幂等**（重复动作不产生第二份状态）。
- 候选：`{kind}:{context_id}:{entity_id}`（todo id / card id / fact id）；checkpoint 类无实体 id，退化为 `{kind}:{context_id}:{hash(title)}`——文本变了就换 key（可接受：内容变了本就是新问题）。
- **组的语义要显式**：建议动作作用于**组**（`{kind}:{context_id}`），因为用户看到的就是一行；为组内单条建状态会让状态数膨胀，且与「折叠为一行」的 UI 自相矛盾。
- 权衡：作用于组意味着「组里只有一条想忽略」会忽略全组。因此 dismiss 必须带 reason，且**动作可撤销**（撤销=删该行状态）。

### 7.4 对既有不变量的影响

- snooze 必须在**写提醒事件之前**生效（延续 `pushNudge` 既有顺序：纪律检查在写事件之前），否则被 snooze 的提醒已被记为「已送达」、永久吞掉——正是 `pushNudge` 注释点名最不能出的结果。
- 动作不得推进 seen 水位（`wc_seen` 仍只由显式 `MarkSeen` 改）。
- owner 隔离：动作表必须带 tenant/owner 并 JOIN `wc_contexts` 过滤（沿用 `attention_signals.go` 既有做法）。

**量级参考（本机实测）**：`work-contexts.sqlite` 当前 3 条主线 / 105 条事件 / 10 条计划项。动作表的行数随「人做出的动作数」增长、而非随时间增长，在这个规模下任何索引方案都够用；这也是「动作表本身就是 AE5 账本」可行的原因。

**建议顺序**：① 先定 item_key（组级）+ 撤销语义；② 选 B（新表）；③ snooze 到期复现用读时判断。

**验证状态（如实区分）**：
- **已实测**：选项 A 的「未读污染」（探针一次 + 永久回归测试 `unread_kinds_test.go`）；`UnreadCounts` / `Focus.changes` 无 kind 过滤（代码阅读 + 该测试双重确认）。
- **仍为静态核查，未写码、未跑测**：本节选型结论本身、item_key 语义、B 的实际行数与读路径 JOIN 成本（**未评估**）。

## 8. 非目标与风险

- **不做** LLM 判断「什么时候该提醒」（不确定、要定时起模型、会幻觉）——沿用既有决策。
- **不做** 通用 IM 集成；不新建提醒实体（复用 `wc_todos.due_at` 与 `wc_events`，维持「无新 migration」的既有约束）。
- **不以** 桌面的关联关系扩大 sandbox / 节点 / 工具权限（既有非目标，见主设计 §1.3）。
- **风险 R1（并置干扰）**：收件箱越完整，一次看到的条数越多，反而提高上下文抖动成本。缓解：P6（聚焦模式 / 只在切换时刻并置）+ 合批压缩。
- **风险 R2（红点债务感）**：未读可视化的副作用是欠债感。缓解：老化降级 + 只对「能当场清掉」的项打红点。
- **风险 R3（默认动作误伤）**：P4 给 Agent 默认动作会引入不可逆错误的风险。缓解：**默认动作只允许可逆动作**；不可逆动作（发布、删除、外部副作用）永不设默认值，必须人来。
- **风险 R4（配额引发漏报）**：配额过严会漏掉真紧急项。缓解：决策类恒为 high 且不受 cap 限制（既有设计），配额只约束非决策类。

## 9. 待确认问题

1. L1 的「动词」是否需要落审计（谁在什么时候 claim/snooze），还是纯前端会话态即可？
2. 杠杆排序里的「阻塞下游数」以 `wc_relations`（主线依赖）还是 taskboard 卡依赖为准？两者今天不是同一套模型。
3. 每日 digest 的投递渠道：仅站内卡片，还是复用已有 webpush？
4. 「注意力配额」的默认值按什么定：主线数量自适应，还是固定每主线 2 条？
5. 是否把「久未触碰的主线」本身作为一种注意力信号（沉寂主线提醒），还是保持「不主动催」？

## 10. 一句话结论

工作桌面已经解决了**注意力税**（进入成本 A），尚未解决**注意力经济**（B 与 C：配额、合批、杠杆、默认动作）。下一步最值得做的不是再加板块，而是**把收件箱从「只读清单」变成「带动词的队列」**，并**给每条待决策项一个「默认动作 + 超时」**——这两件事同时降低 B 与 C 两类消耗，杠杆最高。
