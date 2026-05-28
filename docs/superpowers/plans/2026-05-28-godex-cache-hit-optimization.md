# Godex 缓存命中率优化方案

> 目标：将 DeepSeek API prefix cache 命中率从 ~84% 提升至 ~95%+
> 参考：DeepSeek-Reasonix 的 99.82% cache hit 设计
> 日期：2026-05-28

---

## 1. 现状分析

### Godex 当前 Context 组装流程 (`buildContext` in `context.go`)

每轮 API 调用时，Godex 组装如下结构发送给 DeepSeek：

```
┌─────────────────────────────────────────┐
│ System Message (rebuild every turn)     │ ← 每轮重建
│   base system prompt                    │   (稳定)
│   conciseDefaultResponsePrompt          │   (稳定)
│   instructionPrompt (AGENT.md etc.)     │   (稳定)
│   codingProfilePrompt                   │   (稳定)
│   capabilityCheckPrompt                 │   (稳定)
├─────────────────────────────────────────┤
│ Memory Messages                         │ ← 变化频率低
│   memory index, MEMORY.md content       │   memory 操作时变
├─────────────────────────────────────────┤
│ Runtime Prompt State Messages           │ ← 每轮变化！
│   - repo_map (coding profile)           │   文件变化时变
│   - active_skills                       │   skill 切换时变
│   - environment (timestamp!)            │   ⚠️ 每轮变化
│   - tool_availability (dynamic filter)  │   ⚠️ 每轮可能变化
├─────────────────────────────────────────┤
│ Runtime Messages                        │ ← 不可预测
│   - background notifications            │   通知产生时变
│   - project ledger                      │   项目状态变化时变
├─────────────────────────────────────────┤
│ Conversation History (append-only)      │ ← 只追加 ✅
│   [user₁][assistant₁][tool₁]...        │   保持前缀稳定
├─────────────────────────────────────────┤
│ Tool Schemas (dynamic filtered)         │ ← ⚠️ 每轮可能变化
│   activeToolSchemas(hints, profile)     │   query 分析后过滤
└─────────────────────────────────────────┘
```

### DeepSeek Prefix Cache 机制

DeepSeek API 的 prefix cache 是**字节级前缀匹配**：
- 当请求的前 N 个 token 与上一次请求完全一致时，这 N 个 token 按缓存价计费
- 只要**前缀的任何字节**发生变化，缓存从变化点开始全部失效
- 缓存命中率 = 缓存命中 token / (缓存命中 + 未命中) token

### Godex 当前的 cache-breaking 点

| 问题 | 位置 | 影响 | 严重程度 |
|------|------|------|----------|
| **环境时间戳每轮变化** | `buildEnvironmentPrompt()` | 整个 Runtime Prompt State 前缀失效 | 🔴 高 |
| **Tool Availability 动态过滤** | `buildToolAvailabilityPromptForProfile()` | 过滤结果基于 query 变化，导致前缀失效 | 🔴 高 |
| **Tool Schemas 动态过滤** | `activeToolSchemas(hints, profile)` | tools 参数变化导致 prefix 全部失效 | 🔴 高 |
| **Compaction 重写历史** | `maybeAutoCompact()` → `a.messages = compacted` | 压缩后历史完全不同，prefix 全部失效 | 🟡 中 |
| **Repo Map 变化** | `buildRepoMapPrompt()` | 文件增删时变化 | 🟡 中 |
| **Runtime Messages 不可预测** | `collectRuntimeMessages()` | 背景通知随机出现 | 🟡 中 |
| **Memory 索引变化** | `memoryMgr.BuildPromptSection()` | memory 操作后变化 | 🟢 低 |

---

## 2. Reasonix 的设计 vs Godex 的设计差异

| 维度 | Reasonix | Godex | 差异原因 |
|------|----------|-------|---------|
| **System Prompt** | 会话启动时冻结，整会话不变 | 每轮重建（含环境、工具可用性） | Godex 有 bundle 动态切换能力 |
| **Tool Specs** | 会话启动时冻结 | 每轮根据 query 动态过滤 | Godex 有 tool_exchange 机制 |
| **消息组织** | 单层数组，prefix + append-only | 多层拼接后合并 | Godex 需要 runtime 注入 |
| **Compaction** | 追加摘要消息，不重写 | 替换整个 messages 数组 | 设计选择不同 |
| **环境信息** | 不含时间戳 | 每轮注入时间戳 | Godex 需要时间感知 |
| **Reasoning** | VolatileScratch，不发送 | 作为 thinking_content 发送 | DeepSeek 原生支持 |

**关键差异**：Reasonix 为 DeepSeek 量身定制，Godex 支持多模型和 bundle 动态切换，设计约束更多。不能照搬 Reasonix，但可以借鉴其**分区+稳定性**的核心思路。

---

## 3. 优化方案

### 优化 1：消除环境时间戳变化 🔴 高优先级

**问题**：`buildEnvironmentPrompt()` 每轮生成包含当前日期时间的 Environment section，导致 Runtime Prompt State 前缀完全变化。

**方案**：将时间戳固定为**会话开始时的时间**，不在每轮更新。

```go
// 当前（每轮变化）
func buildEnvironmentPrompt(input EnvironmentPromptInput) string {
    // ...
    fmt.Sprintf("- Local date: %s", input.Now.Format("2006-01-02")),
    fmt.Sprintf("- Weekday: %s", input.Now.Weekday()),
    fmt.Sprintf("- Timezone: %s", input.Timezone),
}

// 优化后（会话内固定）
type EnvironmentPromptInput struct {
    // ...
    SessionStartTime time.Time  // 会话启动时冻结
    Now              time.Time  // 仅用于非 system prompt 场景
}

func buildEnvironmentPrompt(input EnvironmentPromptInput) string {
    // ...
    fmt.Sprintf("- Session started: %s", input.SessionStartTime.Format("2006-01-02 Mon")),
    fmt.Sprintf("- Timezone: %s", input.Timezone),
    // 不再每轮更新日期和星期
}
```

**预期收益**：消除每轮约 50-100 token 的前缀变化，提升 ~3-5% 命中率。

**风险**：模型不再知道当前精确日期。可通过 conversation history 中的 user message 时间戳间接获知。

---

### 优化 2：分离稳定 System Prompt 和动态 Runtime Section 🔴 高优先级

**问题**：Godex 将所有内容拼接为一个 system message，任何部分变化都导致整个 system message 前缀失效。

**方案**：将 system message 拆分为**稳定部分**和**动态部分**，稳定部分放在前面保证 cache 命中。

```
消息顺序（优化后）:
┌─────────────────────────────────────────┐
│ [system] 稳定 System Prompt             │ ← 冻结，不变化
│   base + conciseDefault + instruction   │
│   + codingProfile + capabilityCheck     │
├─────────────────────────────────────────┤
│ [system] Memory Section                 │ ← 低频变化
├─────────────────────────────────────────┤
│ [system] Dynamic Runtime Sections       │ ← 高频变化，放最后
│   environment + tool_availability       │
│   + active_skills + repo_map            │
├─────────────────────────────────────────┤
│ [background] Runtime Messages           │ ← 不可预测
├─────────────────────────────────────────┤
│ Conversation History                    │ ← append-only
└─────────────────────────────────────────┘
```

实现方式：在 `buildContext()` 中，将 `buildRuntimeSystemPrompt()` 返回的 system prompt 拆分为稳定和动态两部分，分别作为独立的 system message。DeepSeek API 支持多个 system message。

**预期收益**：稳定 system prompt 约占 2000-4000 token，拆分后可独立缓存。提升 ~5-10% 命中率。

---

### 优化 3：稳定 Tool Schemas（Immutable Tool List）🔴 高优先级

**问题**：`activeToolSchemas()` 每轮根据 `deriveToolExposureHints(query)` 动态过滤 tools。tools 参数变化会导致 DeepSeek 将整个 tools 数组视为新内容，prefix cache 失效。

**Reasonix 的做法**：tool specs 在会话启动时冻结，整会话不变。

**Godex 的约束**：Godex 有 `tool_exchange` 机制允许用户动态启用/禁用 bundle，不能完全冻结 tools。

**方案 A：会话内稳定 + 只在 bundle 变化时更新**

```go
// 当前：每轮根据 query hints 过滤
func (a *Agent) activeToolSchemas(hints toolExposureHints, agentProfile string) []protocol.ToolSchema {
    // ... 基于 query 动态过滤
}

// 优化后：缓存 tool schemas，只在 bundle 变化时重新计算
type toolSchemaCache struct {
    schemas     []protocol.ToolSchema
    fingerprint string
    bundleState string // 序列化的 bundle 状态
}

func (a *Agent) activeToolSchemas(bundleState string, agentProfile string) []protocol.ToolSchema {
    if a.toolSchemaCache != nil && a.toolSchemaCache.bundleState == bundleState {
        return a.toolSchemaCache.schemas
    }
    // 重新计算...
    schemas := a.computeBaseToolSchemas(agentProfile)
    a.toolSchemaCache = &toolSchemaCache{
        schemas:     schemas,
        bundleState: bundleState,
    }
    return schemas
}
```

**方案 B：移除 query-based hints 过滤，改用 tool system prompt 引导**

将 `deriveToolExposureHints()` 的逻辑从"过滤 tools"改为"在 system prompt 中添加使用指导"。tools 始终保持完整列表，通过 prompt 引导模型按需使用。

**推荐方案 A**：改动最小，保留 bundle 动态切换能力，同时消除 query-based 变化。

**预期收益**：消除 tools 参数变化，这是最大的 cache-breaking 点之一。提升 ~5-10% 命中率。

---

### 优化 4：Append-Only Compaction（追加式压缩）🟡 中优先级

**问题**：`maybeAutoCompact()` 将历史替换为压缩后的版本（`a.messages = compacted`），整个 messages 数组变化，prefix 全部失效。

**Reasonix 的做法**：压缩时将旧消息折叠为一个摘要消息，**追加**到 log 末尾，前缀不变。

**Godex 的约束**：Godex 的 compaction 支持 fast 和 model 两种模式，model 模式使用 LLM 生成摘要。压缩结果是一个全新的 messages 数组。

**方案**：

```go
// 当前：替换整个 messages 数组
compacted := result.Messages
a.messages = protocol.CloneMessages(compacted)

// 优化后：保留前缀，只替换历史部分
func (a *Agent) compactAppendOnly(ctx context.Context, ...) {
    // 1. 找到压缩分界点（最近 N 条消息保留原样）
    splitPoint := len(history) - keepRecentCount
    
    // 2. 只压缩 splitPoint 之前的消息
    oldMessages := history[:splitPoint]
    summary := summarize(oldMessages)
    
    // 3. 构造新 messages = [summary message] + [recent messages]
    // 4. summary message 的格式稳定（固定模板），不包含变量
}
```

关键：summary message 需要使用**固定模板**，不包含时间戳等变量内容，确保后续请求的 prefix 仍然匹配。

**预期收益**：compaction 后 prefix 仍然稳定。提升 ~3-5% 命中率。

**风险**：改动较大，需要修改 compaction 策略和 summary 模板。建议作为后续优化。

---

### 优化 5：Stable Runtime Section 顺序 🟡 中优先级

**问题**：`buildDynamicRuntimePromptSections()` 的 section 顺序可能因某些 section 为空而变化。

**方案**：确保 section 顺序稳定，空 section 用占位符或保持固定顺序。

```go
// 当前：动态拼接，空 section 被跳过
func (a *Agent) buildDynamicRuntimePromptSections(...) ([]runtimePromptSection, error) {
    sections := make([]runtimePromptSection, 0, 5)
    // 某些 section 为空时不追加 → 顺序可能变化
}

// 优化后：固定 section 槽位，空 section 用占位符
func (a *Agent) buildDynamicRuntimePromptSections(...) ([]runtimePromptSection, error) {
    // 固定顺序：memory_index → repo_map/skill_catalog → active_skills → environment → tool_availability
    // 空 section 用最小占位符
}
```

**预期收益**：消除因 section 顺序变化导致的前缀偏移。提升 ~1-2% 命中率。

---

## 4. 实施优先级和预期收益

| 优化项 | 优先级 | 预期命中率提升 | 实施难度 | 风险 |
|--------|--------|---------------|---------|------|
| 1. 消除环境时间戳 | P0 | +3-5% | 低 | 低 |
| 2. 分离稳定/动态 System Prompt | P0 | +5-10% | 中 | 低 |
| 3. 稳定 Tool Schemas | P0 | +5-10% | 中 | 中 |
| 4. Append-Only Compaction | P1 | +3-5% | 高 | 中 |
| 5. 稳定 Runtime Section 顺序 | P1 | +1-2% | 低 | 低 |

**P0 优化预计总提升**：+13-25%，从 84% → 93-97%
**P1 优化预计总提升**：再 +4-7%，可达 95-99%

---

## 5. 与 Godex 设计的兼容性考量

### Godex vs Reasonix 的关键差异

1. **Bundle 动态切换**：Godex 支持 `tool_exchange` 动态启用/禁用 bundle，Reasonix 没有这个机制。优化 3 需要保留这个能力。

2. **多模型支持**：Godex 支持多种模型 profile，Reasonix 只支持 DeepSeek。缓存优化只对 DeepSeek 有效。

3. **Agent Profile**：Godex 有 coding/general 等 profile，会改变 system prompt 和 tool 过滤逻辑。优化 2 需要考虑 profile 稳定性。

4. **Runtime Messages**：Godex 有 background notifications、project ledger 等动态注入，Reasonix 没有。这部分的 cache-breaking 难以完全消除。

5. **Memory 系统**：Godex 的 memory 系统比 Reasonix 复杂，memory section 变化频率更高。

### 不建议照搬的部分

- **VolatileScratch**：Reasonix 用 VolatileScratch 存储 reasoning content，不发送给 API。但 Godex 使用 DeepSeek 的 thinking_content 原生支持，这是正确的做法。

- **完全冻结 Tool Specs**：Reasonix 整会话冻结 tool specs。Godex 的 bundle 机制需要动态能力，不能完全冻结。

- **单一系统 prompt 内联**：Reasonix 所有内容拼接为一个 system message。Godex 拆分为多个 system message 更灵活，也更有助于缓存。

---

## 6. 实施计划

### Phase 1：快速收益（1-2 天）

1. 优化 1：环境时间戳固定为会话启动时间
2. 优化 5：固定 Runtime Section 顺序

### Phase 2：核心优化（3-5 天）

3. 优化 2：拆分 System Prompt 为稳定/动态两部分
4. 优化 3：缓存 Tool Schemas，只在 bundle 变化时更新

### Phase 3：深度优化（1 周）

5. 优化 4：实现 Append-Only Compaction

### 验证方式

- 在 DeepSeek dashboard 上观察 cache hit rate 变化
- 编写 benchmark 对比优化前后的 cache hit token 数
- 参考 `temp/deepseek-reasonix/benchmarks/real-world-cache/` 的验证方法
