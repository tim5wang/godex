# Agent 模板板块设计方案

> 状态：**Superseded** —— 本草稿已吸收进 `docs/agent-role-and-bundle-design.md`（agent 模板板块主设计文档，含已对齐决策 Q1B/Q2A/Q3A、三个消费入口集成、验收指标与 M1-M4 分期），后续以该文档为准。
> 作者：基于 codebase_memory + godex 文档审查
> 日期：2026-08-28

---

## 一、现状与已有基础设施

通过代码审查，godex 已经具备了搭建 agent 模板的几乎全部零件，只是散落在不同包里，没有统一的模板抽象。

| 已有能力 | 位置 | 现状 |
|---------|------|------|
| **Role 结构体** | `internal/core/packages/packages.go:153` | 已有 `BasePrompt`、`DefaultBundles`、`Tools`、`WriteScope`、`ModelHint`、`BudgetHint` 等字段 |
| **Role 解析链路** | `internal/agent/subagent_policy.go` | `resolveSubagentRole` → `subagentToolNamesForRole` → `subagentBasePromptForRole` 已跑通，子 agent 创建时自动应用 |
| **Skill 系统** | `internal/core/skill/skill.go` | `Loader.Load(name)` 加载 skill 内容，`RecommendedBundles` 关联 bundle，已注入 system prompt |
| **Tool Bundle 机制** | `internal/toolruntime/base.go` | `ToolMeta` / `BundleCatalogItem` / `ToolHandler.ActivateBundles`，`tool_exchange` 工具暴露 enable/disable |
| **MCP 管理** | `internal/core/mcp/manage.go` | `UpsertServer` / `DeleteServer` / `ListServers`，支持 filesystem/stdio/streamable-http 三种类型 |
| **指令系统** | `internal/core/instructions/loader.go` | AGENT.md / 本地规则 / 项目指令，优先级分层 |
| **Agent Profile** | `internal/core/config/config.go` | `general` / `coding` 两种 profile，已驱动 system prompt 组装 |
| **上下文预算** | `internal/agent/context_budget.go` | orchestrator 200K / worker 100K / reviewer 100K / researcher 50K |
| **System Prompt 组装** | `internal/agent/system_prompt_dynamic.go` | 动态拼接 instructions → profile → capability_check → skill_catalog → repo_map → active_skills → environment → tool_availability |

**关键发现**：`Role` 结构体已经包含了模板需要的 80% 字段，缺的只是 **skill 列表、MCP 链、personality prompt（性格描述）** 这三个维度。

---

## 二、设计方案

### 2.1 核心模型：AgentTemplate

在 `Role` 基础上扩展，补齐缺失维度：

```
AgentTemplate = Role（身份/能力边界）
              + Skills（要加载的 skill）
              + MCPServers（MCP 工具链）
              + Persona（性格 prompt）
              + AgentProfile（general/coding）
```

**数据结构**（扩展 `Role`）：

```go
type Role struct {
    // --- 已有字段（保持不变）---
    ID, Name, Description, BasePrompt
    DefaultBundles, Tools, WriteEnabled, WriteScope
    Capabilities, ToolPolicy, ModelHint, BudgetHint
    Display

    // --- 新增：模板维度 ---
    Skills        []string `yaml:"skills" json:"skills,omitempty"`
    MCPServers    []string `yaml:"mcp_servers" json:"mcp_servers,omitempty"`
    Persona       string   `yaml:"persona" json:"persona,omitempty"`
    Profile       string   `yaml:"profile" json:"profile,omitempty"`
}
```

**字段语义**：

| 字段 | 来源 | 用途 |
|------|------|------|
| `DefaultBundles` | 已有 | 启动时默认激活的工具 bundle |
| `Tools` | 已有 | 额外工具白名单 |
| `Skills` | **新增** | 启动时自动加载的 skill（注入 skill_catalog + active_skills） |
| `MCPServers` | **新增** | 启动时自动启用的 MCP server（其工具进入 tool catalog） |
| `Persona` | **新增** | 角色扮演 prompt，追加到 system prompt 最前（高于 instructions） |
| `Profile` | **新增** | general/coding，决定 capability_check 段内容 |
| `BasePrompt` | 已有 | 角色指令，追加到子 agent system prompt |
| `WriteScope` | 已有 | 写文件路径白名单 |

### 2.2 存储与注册

```
~/.godex/state/agent-templates/
  ├── builtin/           # 内置模板（只读）
  │   ├── coder.yaml
  │   ├── researcher.yaml
  │   ├── reviewer.yaml
  │   └── orchestrator.yaml
  └── user/              # 用户自定义模板（可修改/删除）
      ├── my-dev.yaml
      └── ...
```

新增 `TemplateManager`：

```go
type TemplateManager struct {
    templatesDir string
    registry     *TemplateRegistry
}

func (m *TemplateManager) List() ([]Role, error)
func (m *TemplateManager) Get(id string) (Role, error)
func (m *TemplateManager) Save(role Role) error
func (m *TemplateManager) Delete(id string) error
func (m *TemplateManager) Apply(id string, session) error
```

### 2.3 模板 → 运行时解析链

```
AgentTemplate (YAML)
    │
    ├─ Persona ──────────────► system prompt 最前面（角色扮演）
    │
    ├─ Profile ──────────────► buildDynamicSystemPrompt(profile)
    │                           └─ capability_check / coding_profile 段
    │
    ├─ Skills ───────────────► skill.Loader.Load_each()
    │                           └─ 注入 active_skills + skill_catalog 段
    │
    ├─ DefaultBundles ───────► tool_handler.ActivateBundles()
    │                           └─ 工具进入 tool_availability 段
    │
    ├─ MCPServers ───────────► mcp.Manager 建立连接
    │                           └─ 工具进入 tool catalog
    │
    ├─ Tools + WriteScope ───► 子 agent 创建时透传
    │
    └─ ModelHint/BudgetHint ─► context_budget 按角色分配
```

### 2.4 API 层

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/agent-templates` | 列出所有模板（含 builtin/user 标记） |
| `GET` | `/agent-templates/{id}` | 获取模板详情 |
| `POST` | `/agent-templates` | 创建自定义模板 |
| `PUT` | `/agent-templates/{id}` | 更新模板 |
| `DELETE` | `/agent-templates/{id}` | 删除模板（仅 user） |
| `POST` | `/sessions/{id}/apply-template` | 应用模板到指定会话 |

### 2.5 Web UI

在 SettingsPage 新增 **"Agent 模板"** 标签页，三段式布局：模板列表 + 模板编辑器 + 应用到会话按钮。复用现有 MCPSettingsPanel、SkillsPage、tool_exchange 的组件。

### 2.6 内置模板（初始版本）

| 模板 ID | Persona 核心 | Bundles | Skills | 写权限 | 预算 |
|---------|------------|---------|--------|--------|------|
| `coder` | senior Go engineer | core_code, lsp | code-review, testing | 受限 | 100K |
| `researcher` | thorough researcher | web, browser | — | 禁止 | 50K |
| `reviewer` | strict code reviewer | lsp | code-review | 禁止 | 100K |
| `orchestrator` | project orchestrator | 全部 | planning | 允许 | 200K |
| `writer` | technical writer | core_code | docs | 受限 | 80K |

---

## 三、分阶段落地路径

### Phase A（MVP — 1 周）：模板 CRUD + 应用到会话
1. 扩展 `Role` 结构体，新增 `Skills` / `MCPServers` / `Persona` / `Profile` 四个字段
2. 新增 `internal/core/templates/template_manager.go`（存储 + CRUD）
3. 新增 `internal/agent/template_apply.go`（模板 → runtime 解析）
4. 新增 backend REST 端点（`/agent-templates` + `/sessions/{id}/apply-template`）
5. Web UI：SettingsPage 新增 "Agent 模板" 标签页
6. 内置 3 个模板：`coder` / `researcher` / `reviewer`

### Phase B（完善 — 1 周）：模板嵌套 + 继承 + 子 agent 模板透传
1. 模板支持 `extends` 字段（模板继承）
2. 子 agent 创建时自动继承父 agent 模板（可覆盖）
3. 新增 `writer` / `orchestrator` 内置模板

### Phase C（长期）：模板市场 + LLM 辅助创建
1. 模板导出/导入（yaml 文件交换）
2. "自然语言创建模板"：用户描述 → LLM 生成模板 YAML
3. 模板评分/共享