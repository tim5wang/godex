# Notes ↔ Memory 双向联动实现 (2026-08-10)

## 背景

借鉴 TencentDB-Agent-Memory 的分层设计思想（MemoryCore 与 MemoryKnowledge 分离 + 打通），在 GoDex 的笔记 app（`internal/core/notes`）和记忆系统（`internal/core/memory`）之间建立双向联动。

## 设计原则

- 笔记和记忆保持独立的子系统，不合并
- 笔记 = 人工整理、长篇、按需加载
- 记忆 = 结构化、短小、自动抽取、常驻上下文
- 一张笔记的 tags 天然构成"场景（scene）"索引，无需额外构建 L2 场景聚类

## 两个方向

### 方向一：笔记 → 记忆（笔记打开时检索相关记忆）

- `GET /notes/{id}/related-memories` HTTP 端点
- 用笔记的 tags 作为搜索关键词，tags 不足时退化为 title + summary
- NotesPage 详情页底部展示相关记忆卡片（最多 6 条，含 type 标签和标题）
- 前端 API：`getNoteRelatedMemories(token, noteID)`

### 方向二：记忆 → 笔记（记忆注入时追加相关笔记引用）

- Agent 的 `collectMemoryMessages` 在注入记忆分层后，用召回查询关键词搜索笔记
- 最多追加 3 条相关笔记引用（含摘要）
- 更多提示："… and more (use `note list` or open the notes app)"

## 相关文件

| 文件 | 改动 |
|------|------|
| `internal/services/backend/notes.go` | 新增 `GetRelatedMemories`，`envelopeWithNoteContext` 注入相关记忆 |
| `internal/agent/agent.go` | Agent 结构体新增 `notesMgr` |
| `internal/agent/agent_wiring.go` | 新增 `notesDirForConfig`，wiring 注入 notesMgr |
| `internal/agent/context.go` | `collectMemoryMessages` 追加笔记引用 |
| `internal/runtime/httpapi/httpapi.go` | 新增 `GET /notes/{id}/related-memories` |
| `ui/web/src/lib/api.ts` | 新增 `getNoteRelatedMemories` |
| `ui/web/src/features/notes/NotesPage.tsx` | 底部"相关记忆"卡片 |

## 参考

- [Memory Design Principles](../docs/memory-design-principles.md)
- TencentDB-Agent-Memory: `temp/TencentDB-Agent-Memory/`