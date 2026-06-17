# Cache & Tool 优化计划

## 背景

默认 coding session 下 godex 每次请求暴露约 34 个 tools，配合 per-turn dynamic runtime messages 和 tool schema 变化，LLM 缓存命中率仅 ~90%。pi/goclaw 通过 stable/dynamic 分拆和精简 tool 集达到 ~99%。

## 优化项总览

| 序号 | 优化项 | 改动量 | 预期收益 | 风险 | 状态 |
|------|--------|--------|----------|------|------|
| 1 | 合并 Memory 8→1 | 中 | 减 ~7 tool schema | 低 | ✅ 已实现 |
| 2 | 合并 Skills 6→1 | 中 | 减 ~5 tool schema | 低 | ✅ 已实现 |
| 3 | 合并 Task Board 5→1 | 中 | 减 ~4 tool schema | 低 | ✅ 已实现 |
| 4 | 合并 Background 2→1 | 小 | 减 ~1 tool schema | 低 | ✅ 已实现 |
| 5 | cache_control breakpoint 前移 | 小 | 最大（history 参与跨 turn 缓存） | 低 | ✅ 已实现 |
| 6 | 移除 per-turn tool 过滤 | 小 | 消除 tool 缓存每 turn 失效 | 中 | ✅ 已实现 |
| 7 | system prompt stable/dynamic 分拆 | 中 | 保护系统前缀不被动态内容污染 | 中 | ❌ 未实现 |
| 8 | cache-ttl 保留策略 | 中 | 减少不必要 compaction 破坏缓存前缀 | 中高 | ❌ 未实现 |

---

## 阶段一：Tool 合并（降低总量 + 减少变化源）✅ 全部完成

### 1.1 Memory 合并：8 tools → 1 ✅

`list_memory / get_memory / search_memory / list_memory_candidates / accept_memory_candidate / dismiss_memory_candidate / remember_memory / forget_memory`

→ 合并为 `memory` tool，通过 `action` 参数调度：

```
action: list | get | search | candidates | accept | dismiss | remember | forget
```

**实现文件**：`internal/tools/memory.go` — `NewMemoryTool`

### 1.2 Skills 合并：6 tools → 1 ✅

`list_skills / list_skill_sources / install_skill / load_skill / expand_skill / unload_skill`

→ 合并为 `skill` tool：

```
action: list | sources | install | load | expand | unload
```

**实现文件**：`internal/tools/skill.go` — `NewSkillTool`

### 1.3 Task Board 合并：5 tools → 1 ✅

`task_create / task_get / task_list / task_update / claim_task`

→ 合并为 `task` tool：

```
action: create | get | list | update | claim
```

**实现文件**：`internal/tools/task.go` — `NewTaskTool`

### 1.4 Background 合并：2 tools → 1 ✅

`background_run / check_background`

→ 合并为 `background` tool：

```
action: run | check
```

**实现文件**：`internal/tools/background.go` — `NewBackgroundTool`

### 合并后效果

默认 session 工具数：34 → 20（-14 个 tool schema）。注册见 `internal/agent/tool_registration.go`。

---

## 阶段二：Cache 优化

### 2.1 cache_control breakpoint 前移到 history 末尾 ✅

**改造**：`internal/agent/context.go` — `buildContext` 中消息组装顺序从 `[history..., memory..., promptState..., runtime...]` 改为 `[promptState..., runtime..., memory..., history...]`。runtime/ephemeral 消息现在排在 history 之前，`marshalAnthropicBody` 的 breakpoint 自然落在最后一条 history 消息上。

**效果**：

```
改造前：[system BP] [tools BP] [history] [runtime_v1 BP]   ← BP 每 turn 漂移
改造后：[system BP] [tools BP] [promptState, runtime, memory, history BP] ← BP 稳定在 history 尾
```

### 2.2 移除 per-turn tool schema 过滤 ✅

**改造**：`internal/agent/context.go` — `activeToolSchemas` 简化为直接返回 `a.toolHandler.ActiveSchemas()`，删除了 `deriveToolExposureHints`、`toolExposureHints`、`applyCodingProfileToolFilter`、`hasActiveSkills`、`hasMemoryCandidates` 等所有 per-turn 过滤逻辑。

### 2.3 system prompt stable/dynamic 分拆 ❌ 未实现

**原因**：当前 system prompt 内容在 session 内基本全稳定，独立做此优化收益有限。需要配合把部分 runtime content 移入 system prompt 的 dynamic 段才生效。优先级低，可在缓存命中率仍不达标时再排入。

### 2.4 cache-ttl 保留策略 ❌ 未实现

**原因**：改动量和风险较高（需要 compaction 决策层配合、TTL 状态管理、loop detection 安全阀）。当前阶段一的 tool 合并 + 阶段二的 breakpoint 前移和去过滤已预期能覆盖主要收益。优先级低。

---

## 同步更新清单

所有引用老 tool name 的非测试代码已同步更新：

| 文件 | 更新内容 |
|------|----------|
| `internal/agent/tool_registration.go` | 注册新的 4 个合并工具，移除 21 个旧工具 |
| `internal/agent/context.go` | 消息重排序 + 移除 per-turn 过滤 |
| `internal/core/conversation/runner.go` | polling guard: `check_background` → `background` |
| `internal/core/memory/manager.go` | prompt 文本：`remember_memory/forget_memory` → `memory` |
| `internal/agent/system_prompt_dynamic.go` | skill 使用说明文本 |
| `internal/tools/skill.go` | 返回给 LLM 的 hint 字符串更新 |
| `internal/tools/background.go` | rerun hint 字符串更新 |
| `internal/core/insights/analyzer.go` | tool name 引用更新 |
| `internal/core/compress/session_summarizer.go` | tool name 映射更新 |
| `internal/toolruntime/permissions.go` | `DefaultPermissionPolicy().Tools`、`inferPermissionAction`、`inferPermissionMutation` 等全部适配 |
| 各 `*_test.go` 文件 | tool name + `handleTool` 调用适配为带 `action` 参数的新格式 |

---

## 实施顺序（更新后）

```
第一步：1.1 Memory 合并                                   ✅
第二步：1.2 Skills 合并                                   ✅
第三步：1.3 Task Board 合并 + 1.4 Background 合并          ✅
          ↓
第四步：2.1 breakpoint 前移                                ✅
第五步：2.2 移除 per-turn tool 过滤                        ✅
          ↓
     观察缓存命中率（预期 ~95-98%）
          ↓
     不达标 → 第六步：2.3 system prompt 分拆
             第七步：2.4 cache-ttl
```

## 预期最终效果

- 工具数：34 → 20
- 缓存命中率：~90% → ~95-98%
- 每 turn token 开销显著降低
