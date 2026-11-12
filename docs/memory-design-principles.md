# Memory Design Principles

> 状态：Active（当前 memory 模块权威设计）
> 修订日志：2026-08-10 新增 work_method/work_fact 类型、笔记↔记忆联动；2026-08-11 foldCapture 去重 + capTail 截断（roadmap 3.2）；2026-11-11 收紧注入资格，支持过期/替代与显式纠错；2026-11-12 按桌面与个人隔离、来源版本失效、Dream 增量整理

这份文档描述 GoDex 当前 `memory` 模块的目标、边界、组成和约束。它不是“把所有历史都记住”的系统，而是一套按归属、证据与有效性管理的记忆系统。

## 归属与工作上下文（2026-11-12）

工作桌面 `work_context` 是工作记忆的默认边界。宿主从认证身份和持久化会话绑定确定 `(tenant, owner, work_context, session)`；模型工具参数不能改写它。每轮与后台整理开始时冻结目标，运行中切换界面、修改另一桌面或解除关联，不会把本轮写入转投另一处。

| 层次 | 作用与存储 | 默认读写边界 |
|---|---|---|
| 会话工作区 | 尚未绑定桌面的临时候选、会话历史；session 分区 | 当前会话；不能自动读实例旧工作记忆 |
| 桌面工作状态 | SQLite 中目标、检查点、计划、资源和委派投影 | 同桌面的多个会话共享；检查点与事实分开 |
| 桌面工作知识 | SQLite `wc_facts`；候选、确认、过期、替代和拒绝 | 原始知识默认只在本桌面自动召回；关联桌面与同仓库不扩大范围 |
| 个人可复用知识与偏好 | 按 tenant + owner 哈希分区的 Markdown 与索引 | 同一所有者的会话读取；桌面对话默认写入桌面候选，个人层为只读叠加 |

跨桌面复用需要两步：审阅并确认桌面事实，再显式提交个人记忆候选，由用户采纳。提炼保留 `Origin`：桌面、事实 ID/版本、适用范围、来源版本、观测/核实时间和有效期。同一来源的新版本更新原记忆，避免把旧结论追加到新结论里；不同来源的同标题不误合并。源事实变为 stale/rejected/superseded、被修改或到期后，已提炼记录退出自动召回，历史仍可查。

旧实例目录里的工作记录不按标题或仓库名猜归属，不删除、不默认兜底召回。仅空 tenant 的 `local-operator` 明确 `user` 偏好做一次保留 ID 的复制迁移；其他用户不会继承。原记录保留，Web「历史记忆（只读）」与 `GET /memory/legacy` 可供本机操作者核对，确认归属后重新记录到对应桌面。

会话首次关联建立稳定主桌面，额外关联只是引用；只有显式解除主关联才选择剩余的最早引用。迁移遇到跨用户/租户歧义时拒绝猜测。认证身份与已保存会话所有者不匹配时不能打开或关联该会话。

## Dream 自动整理

Dream 整理桌面内部的工作记忆。手动、定时使用相同的受限后台入口，独立于聊天，不创建维护对话、工具调用链或执行权限。

- 每个桌面默认启用每 6 小时检查，可关闭或调整间隔；服务每分钟廉价扫描到期且有增量的桌面，每轮最多启动 2 个桌面任务。新建/迁移桌面的首次自动检查在 6 小时以后。
- `source_seq` 只随事实、计划、检查点语义、资源引用和委派状态等输入变化推进。读取、检索、seen/ack、会话完成通知、候选关联标记、相同检查点重存及 Dream 派生结果均不推进。
- 无变化时不创建 job、不创建会话、零 LLM 调用。普通手动整理也跳过；只有显式「重新整理全部」允许对未变化的输入重算。
- 每次最多读取 16 条变更及其当前事实/计划/链接状态，并从最近 64 条知识中选最多 4 条相关旧事实用于比较，截断模型输入；不发送完整历史或工具 schema。默认单次请求上限 90 秒，模型输出上限为配置值与 4096 tokens 的较小者（推理与 JSON 共用预算），截断/空响应不会应用结果；桌面 lease 为 2 分钟。全量重整通过持久化游标连续处理有界批次；普通增量按钮处理一批，剩余增量在已启用的后续定时检查处理。
- 产物是参考摘要、疑似重复/冲突建议和最多 4 条有来源的候选，不能确认事实、改目标/计划、自动删除原证据或跨桌面提炼。候选保留精确来源版本，不获得比来源更宽的范围或更晚的有效期；确认 Dream 候选必须提供审阅过的原始证据，不能把 Dream 摘要当核实来源。
- 应用结果使用运行 ID 与源事实版本校验。整理期间新写入保留为下一批；旧 lease 的迟到结果不能覆盖新任务。失败不消费水位，定时重试带退避，重启可恢复；手动可重试。
- 最近 20 次整理记录可查看。只能撤销最近完成的派生结果；有后续证据、运行中的任务或生成候选已被审阅时拒绝撤销，保留人工工作。

桌面「知识」面板显示整理状态、定时设置、手动/全量按钮、参考摘要及历史。摘要明确标为派生参考，不作为事实来源反复注入对话。默认事实注入按当前请求匹配并限量，候选/过期观察标为未核实；已注入的快照足够时不要求额外 get/recall。

| HTTP 接口 | 用途 |
|---|---|
| `POST /v1/work-contexts/{id}/organize` | 普通增量；`{"force":true}` 为全量重整 |
| `GET /v1/work-contexts/{id}/memory` | 源/处理水位、摘要、lease、状态、定时设置 |
| `PATCH /v1/work-contexts/{id}/memory` | `enabled`、`interval_seconds`（60 秒至 30 天） |
| `GET /v1/work-contexts/{id}/memory/runs` | 最近整理历史与建议 |
| `POST /v1/work-contexts/{id}/memory/runs/{runID}/revert` | 安全撤销最新派生结果 |

SQLite migration 7 增加稳定会话绑定、变更水位/日志、整理历史与事务 guard，以及事实 `valid_until`。迁移不改旧 memory ID 或删除原 Markdown；无需新增基础设施。候选 inbox 的旧 consolidated 策略也记录内容水位，NONE 结果在重启后仍不重复调模型；并发新候选会阻止旧快照回写。

## 目标

- 为未来 session 保留真正长期有价值的项目知识
- 在自动化和可控性之间保持平衡
- 避免把长期记忆、当前上下文和原始对话历史混成一锅
- 在多入口场景下保持一致：
  - Web
  - TUI
  - IM channels

## 非目标

- 不做“保存所有聊天记录”的全量归档替代
- 不把 `history_search` 的命中结果自动写入 durable memory
- 不做全自动、无审阅的长期记忆写入
- 不依赖 embedding / KG / diary 这类更重的认知底座

## 四个层次

当前系统里，至少要区分下面 4 种东西：

1. `当前会话历史`
- 当前 session 里的 user / assistant 消息
- 会受到 `/clear`、`/compact` 等操作影响

2. `当前 prompt 上下文`
- 每轮真正注入模型的临时上下文
- 包括：
  - L0 identity
  - Core memory
  - Relevant recall
- 这是“本轮可见”的，不等于全部历史

3. `durable memory`
- 跨 session 存活的长期记忆
- 个人可复用知识使用 owner 分区内的 Markdown；桌面工作知识以 WorkContext SQLite 为真源

4. `history recall`
- 对原始会话痕迹的回查能力
- 由 `history_search` 承担
- 只做回查，不自动沉淀到 durable memory

## 设计原则

### 1. Durable memory 只保存跨 session 仍然重要的内容

适合进入 durable memory 的通常是：
- 用户长期偏好
- 项目长期事实
- 重复工作流
- 持续性 warning / friction

不适合直接进入 durable memory 的通常是：
- 一次性任务进度
- 临时网页内容
- 某次单轮工具输出
- 只在当前会话里有意义的细节

### 2. 自动信号先进入 inbox，而不是直接落盘

当前自动来源包括：
- turn-end extractor
- insights bridge
- timeline bridge
- project miner

这些自动信号统一先进入 `candidate inbox`，由用户：
- accept
- dismiss

之后才决定是否进入 durable memory。

### 3. 抑制比“反复提醒用户”更重要

对低价值或重复候选，`dismiss` 不是简单移除，而是生成 suppression。

这意味着：
- 相同或语义等价的自动候选不会立刻重新出现
- inbox 不会因为相同噪音持续膨胀

### 4. Core 是稀缺资源，Identity 更稀缺

当前注入层次为：
- `L0 Identity`
- `Core`
- `Relevant`

语义上：
- `Identity`：项目身份、长期角色、固定协作约束
- `Core`：稳定重要但不等于身份的长期知识
- `Relevant`：当前查询临时需要的召回结果

Identity 和 Core 都有独立预算，不应该无限增长。

### 5. History recall 与 durable memory 严格分离

`history_search` 解决的是：
- “之前说过什么？”
- “上次定的是什么？”
- “被 clear / compact 之后还能不能找回？”

它解决的是 `episodic recall`，不是 durable memory 的写入入口。

默认规则：
- `history_search` 不自动写 durable memory
- `history_search` 不自动生成 candidate

### 6. `/clear` 清的是会话负担，不是长期记忆

当前推荐语义：
- `/clear`：清空当前 session 消息上下文、归档引用、待处理队列，并把临时激活工具恢复到默认集合
- durable memory：不受影响
- `history_search`：可在需要时回查历史

所以 `/clear` 不是“遗忘项目知识”，而是“重置当前会话负担”。

### 7. `/compact` 是上下文治理，不是长期记忆治理

`/compact` 的目的：
- 缩短当前上下文
- 保留一定的历史连续性

它不是 durable memory 的替代品，也不应该被视作 durable memory 的真源。

## 当前实现（Memory 2.1）

### Durable memory 真源

- 根目录：`~/.godex/memory/`；新会话/个人记录位于各自分区，根目录旧工作记录为显式历史
- 真源：markdown 文件
- 索引：`index.json`
- sidecar：`memory.db`（SQLite + FTS5）

### 类型

当前 durable memory 类型：
- `identity`
- `user`
- `workflow`
- `project`
- `warning`
- `work_method`（工作方法：如何做一件事，如 recipe/tutorial/cookbook）
- `work_fact`（事实：如 faq/reference/cheatsheet，结构化的参考信息）

### 分层注入

当前 prompt 注入层：
- `Identity`
- `Core`
- `Relevant`

其中：
- `Identity` 与 `Core` 放在历史之前的稳定前缀，每次请求可见；仅相关召回进入动态尾部。
- `Identity` 单独预算，优先级最高
- `Core` 默认仅包含 `user` 长期偏好；其他类型必须显式标记 `core` 才能常驻。`project`、`work_fact`、`work_method` 不因类型自动常驻。
- `Relevant` 必须先有查询匹配，类型与 FTS 加分不能制造命中。纯数字不因来源 URI 的数字命中，来源中的随机长十六进制 ID / UUID 不作为相关性证据；推断出 scope 时仅保留匹配项，不用全局结果补齐，允许零召回。
- `MEMORY.md` 保留为可浏览、可恢复的目录，不再整份注入 prompt。静态 Memory 段仅解释用途与按需查询方式。
- 归档、过期、已被替代的记忆在默认检索和所有注入层统一排除；仍可按 ID 或 `status=all` 查看。

### 召回开销与观测

- 相同请求的工具循环复用召回分层；每8轮重选一次。索引版本变化、记忆工具写入、新一轮运行或查询变化立即刷新。缓存中的截止时间仍逐请求检查。
- 构造上下文和 Inspector 预览不更新引用时间；只有成功请求中实际包含的条目在 runner 确认后更新 `last_referenced_at`。引用仍不代表事实正确或对任务有用。
- 捕获格式 `godex_context_v2` 在 request 中保存完整 `runtime_tail`、`context_sources` 和 reasoning 回传标志；provider 请求序列化继续排除这些诊断字段。该格式记录完整 harness 上下文，不冒充最终 HTTP 请求体。
- 注入来源提供构造上下文时的估算 token、实际注入的记忆 ID 及选择原因；runner 追加的指导可在最终 runtime_tail 中查看，分项不是最终 HTTP 请求的精确 token 账单。排除计数区分归档、失效、替代、仅手动，以及相关性/scope/预算未入选项，不用入选证明有用。

### 有效性与纠错

- `recall_policy` 与内容类型独立：`auto` 为按规则自动召回，`manual` 只供显式检索。新建的已完成项目里程碑默认 `manual`；显式 core 或 auto 选择可保留常驻/自动行为。
- 归档和替代保留原始文件与 audit；历史查询用 `memory action=search status=all` 或按 ID 获取。
- `expires_at` 是明确的有效截止时间，未设置代表没有声明截止时间，不代表内容经过验证。
- `superseded_by` 指向替代该条记录的有效记忆 ID。设置时验证目标存在、有效且不构成循环；旧条目保留供回查。
- 以上元数据保存在 markdown 和 `index.json`，FTS 查询使用索引中的有效性元数据；重开和索引重建保留这些条件。
- 修改旧事实用 `memory action=update`：正文替换，不追加旧事实。仅更改有效性可提供 `id_or_title` 与 `expires_at` / `superseded_by`；空字符串清除对应条件。
- `remember` 同名写入仍是增量补充，不用于纠错；普通更新和重复 remember 均保留归档、有效期与替代关系，不会自动恢复无效记录。
- 当前指令和本轮验证优先于历史记忆。旧版本的语义冲突仍需明确纠正或标记替代，不按更新时间猜测真伪。

### 检索

当前检索能力：
- markdown 真源
- SQLite + FTS5 sidecar
- 增量同步
- scope-aware recall

轻量 scope 当前支持：
- `browser`
- `weixin`
- `feishu`
- `memory`
- `runtime`
- `config`

### 自动来源

当前自动桥接来源：
- turn-end extractor
- insights bridge
- timeline bridge
- project miner

### 治理能力

当前治理能力包括：
- candidate inbox
- accept / dismiss
- suppression（含解除抑制，`RemoveSuppression`）
- Always include / core
- 生命周期状态 `status`（`active` / `archived`）：
  - `archived` 记忆排除默认召回与上下文注入，但保留可恢复
  - 归档/恢复走 audit 链（`archive` / `restore_status`），可在 Web 上一键批量归档
  - 里程碑识别：标题含「完成/落地/收官/phase/阶段」的 `project`/`workflow` 记忆可一键归档（`/memory/milestones`、`/memory/milestones/archive`）
- 时效信号 `last_referenced_at`：
  - `BuildContextLayers` 注入时（节流、best-effort）回写最近一次被引用时间
  - Web UI 据此展示「从未使用 / 使用于 X / 已陈旧」标记，辅助清理
- Web 管理页（三栏看板：候选收件箱 / 长期记忆 / 屏蔽与归档，多选批量流转）

### 笔记 ↔ 记忆联动

笔记（Notes）与记忆（Memory）是独立的子系统，但在以下两个方向已打通：

**笔记 → 记忆（笔记打开时检索相关记忆）**
- `GET /notes/{id}/related-memories` 端点暴露
- NotesPage 详情页底部展示相关记忆列表（最多 6 条，含 type 标签）
- 笔记的 tags 优先作为搜索关键词，tags 不足时退化为 title + summary

**记忆 → 笔记（记忆注入时追加相关笔记引用）**
- Agent 的 `collectMemoryMessages` 在注入记忆分层后，用召回查询关键词搜索笔记
- 最多追加 3 条相关笔记引用（含摘要），更多提示可通过 `note list` 或笔记 app 查看
- 笔记引用的篇幅远小于笔记全文，不会影响上下文预算

**envelopeWithNoteContext（笔记语境注入）**
- 在 Chat 中打开笔记时，参数 `?note_id=` 触发笔记内容注入
- 注入内容包括笔记标题、标签、摘要、全文，以及相关记忆的摘要链接
- 相关记忆部分只展示 type 标签 + 标题 + 摘要（最多 4 条），末尾提示工具可搜索更多

打通后，记忆和笔记在语义上形成了互补关系：
- 记忆 = 结构化、短小、自动信号先成为候选、按资格和相关性有限注入
- 笔记 = 人工整理、长篇、按需加载
- 一张笔记的 tags 天然构成一个"场景（scene）"索引，无需额外构建 L2 场景聚类

## 模板级记忆范围（Agent Template memory 字段）

agent 模板板块（`docs/agent-role-and-bundle-design.md`）为每个会话预设记忆范围，
字段 `AgentTemplate.Memory` 取值 `none | shared | scoped`（空 = shared）：

| 值 | 注入（读取侧） | 捕获（写入侧） | 含义 |
|---|---|---|---|
| `none` | 不注入记忆索引消息（`buildMemoryIndexPromptMessage` 短路） | `captureMemoryCandidates` / `CaptureInsightMemoryCandidates` 短路 | 无记忆上下文，也不落盘 |
| `shared`（默认） | 按宿主确定的桌面/会话边界读取，并叠加本人已审核知识 | 桌面候选或会话分区 | shared 不代表可读全部实例工作记忆 |
| `scoped` | 无宿主绑定时使用会话分区 | 与读取边界一致 | 已建立的宿主桌面/owner 边界优先，模板与配置重载不能覆盖它 |

要点：
- 读侧与写侧**同步控制**：`none` 是彻底的“无记忆会话”（既不注入也不落盘），而不是只读；
- `scoped` 让模板可以**不依赖全局配置**强制每个会话独立记忆分区，避免跨会话泄漏；
- 记忆策略（per-turn / agent-only / consolidated）与记忆范围正交：范围管“哪一层记忆、是否参与”，策略管“候选如何积累”。

## Project Miner 原则

`project miner` 当前是保守模式：

- 只扫高信号文档：
  - `README*`
  - 根 `AGENTS.md`
  - `docs/**/*.md`
- 每个文档最多提炼 1 条 candidate
- 结果只进 inbox
- 不直接写 durable memory

这样做的目标是：
- 先补项目长期事实来源
- 不让代码库文档直接污染长期记忆

## 为什么这样设计

这套设计优先保证：

- `可审阅`
- `可控`
- `长期稳定`
- `上下文不会轻易爆炸`
- `不同入口语义一致`

它不是当前最“智能”的 memory 系统，但它足够稳，也更适合真实的多入口 agent runtime。

## 当前边界与已知限制

当前还没有：
- embedding-native semantic search
- KG / diary / richer long-term graph
- 自动的事实验证和按类型过期策略（已有 `status`、明确 `expires_at` / `superseded_by` 与里程碑归档；引用时间不代表有效性）
- 自动把历史回查结果沉淀成长期记忆
- 大规模项目代码级 mining

所以目前更适合把它理解成：

`一个可治理的长期项目记忆系统，而不是完整的本地认知数据库。`

## 后续演化方向

更适合继续做的方向：

1. 增强时间语义
- 区分长期恒定、阶段性有效、易过期 warning

2. 增强 scope 模型
- 从轻量规则走向更稳定的项目域召回
- 复用笔记 tags 建立更丰富的场景索引

3. 提升 project miner
- 保持 inbox 审核前提下，扩展更丰富的高信号来源
- 已支持按文件名推断 `work_method` / `work_fact` 类型（recipe/howto → work_method，faq/reference → work_fact）

4. 提升 explainability
- 更明确地告诉用户：这条记忆为什么被注入、为什么被召回

5. 加深笔记 ↔ 记忆联动
- 从"相关记忆摘要 + 笔记引用"走向双向自动沉淀
- 笔记保存时可选自动提炼全新记忆候选
- 记忆命中 `work_*` 时反向列出可参考的笔记
