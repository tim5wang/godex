# Memory Design Principles

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

3. 提升 project miner
- 保持 inbox 审核前提下，扩展更丰富的高信号来源

4. 提升 explainability
- 更明确地告诉用户：这条记忆为什么被注入、为什么被召回
