# Memory Design Principles

> 状态：Active（当前 memory 模块权威设计）
> 修订日志：2026-08-10 新增 work_method/work_fact 类型、笔记↔记忆联动；2026-08-11 foldCapture 去重 + capTail 截断（roadmap 3.2）

这份文档描述 GoDex 当前 `memory` 模块的目标、边界、组成和约束。它不是“把所有历史都记住”的系统，而是一套 `可治理的长期项目记忆系统`。

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
- 当前真源默认是 `~/.godex/memory/` 下的 markdown 文件

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

- 目录：`~/.godex/memory/`
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
- `Identity` 单独预算，优先级最高
- `Core` 用于稳定长期记忆
- `Relevant` 会先按 scope 优先召回，再用全局高分项补齐

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
- suppression
- Always include / core
- Web 管理页

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
- 记忆 = 结构化、短小、自动抽取、常驻上下文
- 笔记 = 人工整理、长篇、按需加载
- 一张笔记的 tags 天然构成一个"场景（scene）"索引，无需额外构建 L2 场景聚类

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
- 事实失效时间模型
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
