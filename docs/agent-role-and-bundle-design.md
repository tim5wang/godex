# Agent 角色能力边界与工具组织设计

> 核心问题：服务于长任务、多 Agent 协同的工具如何组织？不同角色 Agent 的能力边界是什么？

---

## 一、GoDex 已有的可插拔基础设施

### 1. Tool Bundle 机制（已成熟）

`internal/toolruntime/base.go` 定义了完整的可插拔工具体系：

```
ToolMeta:
  - Bundle: 工具所属的 bundle 名
  - Summary: 工具的简短描述
  - DefaultActive: 是否默认激活
  - AlwaysActive: 是否始终激活

BundleCatalogItem:
  - Name: bundle 名
  - Summary: 功能描述
  - Tools: 包含的工具列表
  - Active: 当前是否激活

ToolHandler:
  - ActivateBundles(names) → 激活指定 bundle
  - DeactivateBundles(names) → 停用指定 bundle
  - ResetActiveToolsToDefaults() → 重置为默认状态
  - Catalog() → 返回当前所有 bundle 和工具的状态
```

当前内置 bundle：
- `core_code`（shell、文件操作）
- `lsp`（代码智能）
- `planning`（todo 规划）
- `web`（网络搜索）
- `browser`（浏览器自动化）
- `subagent`（子 agent 调度）
- `mcp`（MCP 协议工具）
- `desktop`（桌面操作）
- 等等

### 2. Package/Role 机制（已定义但未与 bundle 深度集成）

`internal/core/packages/packages.go` 定义了：

```go
type Role struct {
    ID             string   // 角色 ID
    Name           string   // 显示名
    Description    string   // 描述
    BasePrompt     string   // 基础系统提示词
    DefaultBundles []string // 默认激活的 bundle 列表
    Tools          []string // 额外工具
    WriteEnabled   bool     // 是否允许写操作
    Capabilities   []string // 能力声明
    ToolPolicy     []string // 工具策略
    ModelHint      string   // 模型提示
    BudgetHint     string   // 预算提示
    Display        Display  // UI 展示配置
}
```

### 3. Manifest 中的角色声明

`godex.package.yaml` 的 `Resources` 通过 `Roles` 字段引用角色定义。

### 4. 当前 gap

**Role 和 bundle 之间缺少 "角色 → bundle 激活"的运行时映射**。当前：
- Role 定义 `DefaultBundles`（声明式），但运行时没有"激活角色 → 自动激活对应 bundle"的机制
- Agent 启动时 bundle 是固定的（由 `AlwaysActive` 和 `DefaultActive` 决定），不随角色变化
- subagent 创建时没有继承角色的 bundle 配置

---

## 二、工具组织模型：三层架构

### 核心原则

1. **工具是原子能力，bundle 是逻辑分组，角色是能力边界**
2. **bundle 按需加载，不浪费上下文预算**
3. **角色决定 bundle 的默认激活集合，但 agent 可动态调整**

### 三层模型

```
┌─────────────────────────────────────────────────────┐
│  Layer 3: Agent 角色 (Role)                         │
│  ├── 身份声明 (我是谁)                              │
│  ├── 能力边界 (我能做什么)                          │
│  ├── 默认 bundle 集 (我默认带什么工具)              │
│  └── 策略约束 (我怎么做)                            │
├─────────────────────────────────────────────────────┤
│  Layer 2: Tool Bundle                               │
│  ├── 逻辑分组 (一组相关工具)                        │
│  ├── 按需加载 (用 tool_exchange 激活)               │
│  └── 可审计 (加载/卸载有日志)                       │
├─────────────────────────────────────────────────────┤
│  Layer 1: 原子工具 (Tool)                           │
│  ├── schema 定义 (参数/返回值)                      │
│  ├── 权限声明 (需要什么权限)                        │
│  └── bundle 归属 (属于哪个 bundle)                  │
└─────────────────────────────────────────────────────┘
```

### 角色和 bundle 的映射关系

```
角色 → 默认 bundle 集合 (激活时自动加载)
    ├── 核心 bundles (始终激活)
    │   ├── core_code    (bash, read_file, write_file, edit_file, glob, grep, ls, find)
    │   ├── lsp          (代码智能)
    │   └── planning     (todo 规划)
    │
    ├── 按角色激活的 bundles
    │   ├── orchestrator 角色 → web, browser, subagent, mcp
    │   ├── worker 角色   → core_code, lsp (仅子集)
    │   ├── reviewer 角色 → lsp, diff, 无 write
    │   └── researcher 角色 → web, browser, 无 write
    │
    └── 按需激活的 bundles (tool_exchange)
        ├── desktop
        ├── background
        ├── external_agents
        ├── mcp (特定 MCP 服务)
        └── ... (future: 长任务专用 bundle)
```

---

## 三、角色能力边界模型

### 三个维度

```
能力边界 = (工具集合 × 权限策略 × 上下文预算)
```

### 1. 工具集合 (Tools)

决定 agent 能调用什么工具：

| 角色 | 核心工具 | 可选工具 | 不允许 |
|------|---------|---------|--------|
| **orchestrator** | 所有工具 | 按需加载 | 无限制 |
| **worker** | bash, file, lsp, planning | web, browser | 系统管理、审批 |
| **reviewer** | lsp, diff, read, grep, glob | web | write, bash, delete |
| **researcher** | web_search, web_fetch, browser | read_file | write, bash, delete |
| **planner** | todo, read, grep, lsp | web | write, bash, delete |

### 2. 权限策略 (Policy)

决定 agent 在什么条件下可以做什么：

| 角色 | 写操作 | shell 执行 | 网络访问 | 审批要求 |
|------|--------|-----------|---------|---------|
| **orchestrator** | 允许 | 允许 | 允许 | 高风险操作需审批 |
| **worker** | 允许（写 scope 内） | 允许 | 受限 | 所有写操作需审批 |
| **reviewer** | 禁止 | 禁止 | 允许 | 不适用 |
| **researcher** | 禁止 | 禁止 | 允许 | 不适用 |
| **planner** | 禁止 | 禁止 | 允许 | 不适用 |

### 3. 上下文预算 (Context Budget)

决定 agent 可用多少上下文：

| 角色 | 最大 tokens | 记忆注入 | 历史保留 |
|------|------------|---------|---------|
| **orchestrator** | 高（200K） | 完整 | 完整 |
| **worker** | 中（100K） | 相关 | 精简 |
| **reviewer** | 中（100K） | 仅 diff | 仅当前 |
| **researcher** | 低（50K） | 仅搜索 | 仅当前 |
| **planner** | 中（100K） | 项目 | 精简 |

---

## 四、长任务场景下的工具组织

### 场景：一个 longtask 的执行流程

```
用户：重构这个模块
    │
    ▼
orchestrator (完整工具集)
    │
    ├─ 1. planner 角色 → 分析模块、制定计划
    │   bundle: [core_code, lsp, planning]
    │   write: false
    │
    ├─ 2. worker 角色 A → 实现模块 A
    │   bundle: [core_code, lsp] (写 scope: "src/module_a/")
    │   write: true (scope 受限)
    │
    ├─ 3. worker 角色 B → 实现模块 B (并行)
    │   bundle: [core_code, lsp] (写 scope: "src/module_b/")
    │   write: true (scope 受限)
    │
    ├─ 4. reviewer 角色 → review 两个 worker 的产出
    │   bundle: [lsp, diff] (无 write 权限)
    │   write: false
    │
    └─ 5. orchestrator → 合并结果、汇报
        bundle: [core_code, lsp, subagent]
        write: true
```

### 关键设计点

1. **每个子 agent 携带不同的 bundle 集合**，不共用主 agent 的上下文预算
2. **写 scope 机制**：worker 的写权限限定在特定目录，无法越界
3. **角色转换**：同一个子 agent 可以在不同阶段切换角色（如 worker → reviewer）
4. **bundle 继承**：子 agent 默认继承父 agent 的 bundle，但可覆盖

### 代码实现路径

```go
// 角色 → bundle 映射注册表
type RoleBundleRegistry struct {
    roles map[string]RoleBundleConfig
}

type RoleBundleConfig struct {
    DefaultBundles  []string       // 默认激活的 bundle
    OptionalBundles []string       // 可选的 bundle
    DefaultActive   bool           // 默认是否激活
    WriteScope      []string       // 写权限范围
    ToolPolicy      []string       // 工具策略
    ContextBudget   ContextBudget  // 上下文预算
}

// 子 agent 创建时应用角色配置
func (a *Agent) spawnAgentWithRole(role string, task string) {
    config := roleRegistry.Get(role)
    // 1. 创建新的 agent 实例
    // 2. 激活 role 对应的 bundle 集合
    // 3. 设置写 scope
    // 4. 设置上下文预算
    // 5. 提交任务
}
```

---

## 五、当前与目标的差距

### 已有（可以直接用）

| 机制 | 状态 | 说明 |
|------|------|------|
| `ToolBundle` | ✅ 成熟 | `toolruntime/base.go` 完整实现 |
| `tool_exchange` | ✅ 成熟 | 运行时动态加载/卸载 bundle |
| `Role` 定义 | ✅ 已定义 | `packages.go` 的 `Role` 结构体 |
| 写 scope | ✅ 已有 | `workflow.go` 的 `WriteScope` 字段 |
| `ActivateBundles` | ✅ 已有 | 批量激活 bundle |

### 缺的（需要实现）

| 机制 | 优先级 | 说明 |
|------|--------|------|
| **角色 → bundle 运行时映射** | P0 | subagent 创建时根据角色激活对应 bundle |
| **角色 bundle 注册表** | P0 | 集中管理所有角色和 bundle 的映射关系 |
| **子 agent bundle 继承** | P1 | 子 agent 默认继承父 agent 的 bundle，可覆盖 |
| **写 scope 与 bundle 联动** | P1 | 激活 writing bundle 时自动应用写 scope |
| **上下文预算按角色分配** | P2 | 不同角色有不同 token 预算 |
| **角色 bundle 的 UI 管理** | P2 | Web 界面查看/编辑角色 bundle 配置 |

---

## 六、一个具体的例子

### 当前 longtask 创建子 agent 的方式

```go
// 当前：所有子 agent 共享同一 bundle 集合
subagent, err := a.spawnSubagent(sessionID, prompt, agentType, writeScope)
// agentType 只是字符串，不改变 bundle 配置
```

### 理想方式

```go
// 理想：根据角色激活不同 bundle
subagent, err := a.spawnSubagentWithRole(sessionID, RoleConfig{
    Role:            "worker",
    Task:            prompt,
    WriteScope:      []string{"src/module_a/"},
    ActivateBundles: []string{"core_code", "lsp"},
    DeactivateBundles: []string{"web", "browser", "subagent"},
    ContextBudget:   ContextBudget{MaxTokens: 100000},
})
```

### 对应的 bundle 定义

```yaml
# godex.package.yaml 中的角色定义
roles:
  - id: code-reviewer
    name: Code Reviewer
    description: Reviews code changes and provides feedback
    base_prompt: "你是一个代码审查者..."
    default_bundles: [core_code, lsp]
    tools: [diff, preview]
    write_enabled: false
    capabilities: [code_review, diff_analysis]
    tool_policy: [read_only]
    budget_hint: 100k
```

---

## 七、与其他系统的关系

### 与 QM 的 Scope 模型

QM 的 `scopeId` 贯穿所有模块，每个 scope 有独立记忆、沙箱、文件、密钥。GoDex 的角色模型可以借鉴：

```
QM: scope → 独立的记忆/沙箱/文件/密钥
GoDex: 角色 → 独立的 bundle/权限/写 scope/上下文预算
```

### 与 Codex 的 spawn_agent

Codex 的 `spawn_agent` 支持 `agent_type`（角色名）和 `fork_context`（上下文继承）。GoDex 可以复用：

```
Codex: spawn_agent(agent_type, message, fork_context)
GoDex: spawn_agent_with_role(role, task, write_scope, bundle_overrides)
```

### 与现有 roadmap 的关系

- Phase 1 的幂等性存储、Worker Lease 为基础底座
- Phase 2 的 AgentGraph 重构为角色编排提供执行框架
- 本设计为 Phase 3 的 spawn/send_input/wait 提供角色能力边界