# Agent 模板板块（人才市场）与角色能力边界设计

> 状态：设计定稿（决策已对齐，待实施）
> 本文档是 agent 模板板块的**主设计文档**，取代 2026-07 旧版「Agent 角色能力边界与工具组织设计」（旧版核心思想已由 roadmap Phase 3/4 落地），并吸收取代 `docs/agent-template-board-design.md` 草稿（该文件已标记 Superseded）。
> 关联文档：`docs/business-agents-console-design.md`（业务智能体，二期收敛对象）、`docs/taskboard-plugin-design.md`（M3 将基于本设计重规划）、`docs/godex-optimization-roadmap.md`（Phase 4 角色机制为技术底座）。

---

## 一、背景与核心诉求

给 godex 增加「agent 模板板块」：一个能预设 agent 能力边界的**人才市场**。模板预设以下维度：

- 加载的 package、skill
- 工具集合（bundle / 工具白名单）
- 性格 prompt（persona）
- MCP server 链
- （预留）写 scope、模型、上下文预算

### 三条核心诉求

1. **省 token / 提高上下文缓存命中率**（第一优先级）。当前会话默认加载全部默认激活 bundle 的 tool schema 与描述，以及 repo map、skill catalog 等重量级 prompt 段。模板让每个会话只挂载场景所需的工具与 prompt 段，且创建后固定不变（稳定 prefix → provider prefix-cache 命中）。这正是现有「标准/极简模式」想解决但粒度不够的问题。
2. **场景化习惯**。不同场景的 agent 应有不同行为边界：写代码的不应该改没规划没提到的代码；私人助理应主动为用户思考和提供建议；研究型 agent 禁写。这些差异 = persona prompt + 工具边界 + 写权限的组合，用模板表达。
3. **统一抽象**。模板能力与业务智能体板块（BizAPIKey 白名单）高度同构，且任务看板 M3 执行分派也需要同样的能力预设。做成**同一个公共模板**，一次建设服务三个消费入口。

---

## 二、决策记录（已与需求方对齐）

| # | 问题 | 决策 | 说明 |
|---|------|------|------|
| Q1 | 模板与三套既有机制（package Role / BizAPIKey / 会话模式）的收敛程度 | **B：先共存后收敛** | 新建 Template 实体，对话创建与任务看板先用模板；Role/BizAPIKey 暂不动，二期收敛。但**字段设计按 A（单一事实源）预留**，避免返工 |
| Q2 | 模板作用域 | **A：全局库起步** | 存 `~/.godex/state/agent-templates/`，字段预留 `project_dir` 绑定，项目级覆盖列入二期 |
| Q3 | 会话中途能否换模板 | **A：仅创建时选择** | 中途切换涉及 bundle 重置 + prompt 重建，缓存全部失效，与省 token 诉求冲突；列入路线图（若做，复用 6.4 harness 热切换的 reset 语义） |
| Q4 | 验收标准 | **可验证指标** | 见「十、验收指标」 |

---

## 三、现状盘点（源码实证）

模板所需的零件已存在 80%，散落在不同包中，缺统一抽象：

| 已有能力 | 位置 | 现状与可复用点 |
|---|---|---|
| `Role` 结构体 | `internal/core/packages/packages.go:153` | 已有 BasePrompt / DefaultBundles / Tools / WriteEnabled / WriteScope / Capabilities / ToolPolicy / ModelHint / BudgetHint / Display；**缺 Skills、MCPServers、Persona、Profile 四个维度** |
| 角色→bundle 运行时映射（Phase 4.3） | `internal/agent/role_bundles.go` | `RoleBundleRegistry` + `BundlesForRole`，内置 orchestrator/worker/reviewer/researcher/planner 五角色默认 bundle；package role 可 RegisterRole 覆盖 |
| 子 agent 角色解析链 | `internal/agent/subagent_policy.go` | `resolveSubagentRole` → `subagentToolNamesForRole` → `subagentBasePromptForRole`，创建时自动应用；bundle 继承（4.4）与写 scope 联动（4.5）已闭环 |
| 会话模式（标准/极简） | `internal/agent/session_mode.go` | `ApplySessionMode` + `SetActiveTools`；mode 存 locator metadata，创建后固定（保护 prefix-cache）。**该机制即被本设计替换的对象，其「模式→初始工具集 + prompt 段裁剪」骨架直接复用** |
| Tool Bundle 机制 | `internal/toolruntime/base.go` | ToolMeta / BundleCatalogItem / ActivateBundles / DeactivateBundles / SetActiveTools，运行时动态挂卸成熟 |
| Skill 系统 | `internal/core/skill` | Loader.Load + RecommendedBundles，已注入 skill_catalog / active_skills prompt 段 |
| MCP 管理 | `internal/core/mcp/manage.go` | UpsertServer / DeleteServer / ListServers，filesystem/stdio/streamable-http 三类型 |
| Agent Profile | `internal/core/config/config.go` | general / coding 两种 profile，驱动 system prompt 组装（capability_check 等段） |
| System prompt 动态组装 | `internal/agent/system_prompt_dynamic.go` | instructions → profile → capability_check → skill_catalog → repo_map → active_skills → environment → tool_availability，模板各维度在此找到注入点 |
| 上下文预算 | `internal/agent/context_budget.go` | orchestrator 200K / worker 100K / reviewer 100K / researcher 50K，模板 BudgetHint 对接此分配 |
| 业务智能体白名单 | `internal/services/usage/types.go` BizAPIKey | MCPServers / SandboxTools / Skills / Packages / Models / ProjectDir / DefaultPrompt / AllowedModels——与模板字段几乎一一对应，是二期收敛的目标 |
| 任务看板执行器 | taskboard 插件 M1 执行器 | 卡片认领→起执行会话；M3 重规划后按模板分派（见 §7.3） |

**关键 gap**：没有一个统一的「模板」实体把上述维度打包；对话创建入口只有 default/minimal 两档；任务看板执行 agent 无能力预设。

---

## 四、核心模型：AgentTemplate

### 4.1 字段定义

```go
// internal/core/templates/template.go
type AgentTemplate struct {
    // --- 身份 ---
    ID          string  `json:"id" yaml:"id"`                        // 唯一 ID（builtin/ 前缀区分内置）
    Name        string  `json:"name" yaml:"name"`
    Description string  `json:"description,omitempty" yaml:"description,omitempty"`
    Avatar      string  `json:"avatar,omitempty" yaml:"avatar,omitempty"`       // 头像：emoji 或图片 URL；缺省回退到「首字母 + Color」生成的字母头像
    Color       string  `json:"color,omitempty" yaml:"color,omitempty"`        // 主题色（卡片描边/字母头像底色）
    Scenarios   []string `json:"scenarios,omitempty" yaml:"scenarios,omitempty"` // 标签：coding / writing / research / assistant ...

    // --- 能力边界 ---
    Bundles      []string `json:"bundles,omitempty" yaml:"bundles,omitempty"`           // 初始激活 bundle（复用 SetActiveTools 语义，always-active 工具自动保留）
    Tools        []string `json:"tools,omitempty" yaml:"tools,omitempty"`               // 细粒度工具白名单（可选，优先于 bundles 全集）
    WriteEnabled bool     `json:"write_enabled,omitempty" yaml:"write_enabled,omitempty"`
    WriteScope   []string `json:"write_scope,omitempty" yaml:"write_scope,omitempty"`   // 写路径白名单（对接 4.5 解析链）
    MCPServers   []string `json:"mcp_servers,omitempty" yaml:"mcp_servers,omitempty"`   // 启用的 MCP server 白名单
    Skills       []string `json:"skills,omitempty" yaml:"skills,omitempty"`             // 自动加载 skill
    Packages     []string `json:"packages,omitempty" yaml:"packages,omitempty"`         // 启用的 package（其 roles/tools 进入可用池）

    // --- 人格与习惯 ---
    Persona  string `json:"persona,omitempty" yaml:"persona,omitempty"`    // 性格 prompt，注入 system prompt 前部
    Profile  string `json:"profile,omitempty" yaml:"profile,omitempty"`    // general / coding，复用现有 Agent Profile
    BasePrompt string `json:"base_prompt,omitempty" yaml:"base_prompt,omitempty"` // 角色指令（行为边界规则）

    // --- 资源提示 ---
    ModelHint  string `json:"model_hint,omitempty" yaml:"model_hint,omitempty"`
    BudgetHint string `json:"budget_hint,omitempty" yaml:"budget_hint,omitempty"`

    // --- 二期收敛预留 ---
    ProjectDir  string `json:"project_dir,omitempty" yaml:"project_dir,omitempty"` // Q2 预留：项目绑定
    BizRefOnly  bool   `json:"-" yaml:"-"`                                          // 内部标记：由 BizAPIKey 派生的只读模板
}
```

**字段设计原则（对齐 Q1B）**：字段命名与 BizAPIKey 白名单字段保持同构（Bundles↔SandboxTools、MCPServers↔MCPServers、Skills/Packages 同名、Persona↔DefaultPrompt），二期收敛时 BizAPIKey 增加 `template_id` 引用即可平滑迁移，Role 可由模板派生为兼容视图。

### 4.2 与既有三套机制的字段映射（收敛对照）

| AgentTemplate | package Role | BizAPIKey | 会话模式 |
|---|---|---|---|
| Bundles | DefaultBundles | SandboxTools | minimalModeTools（被替换） |
| Persona + BasePrompt | BasePrompt | DefaultPrompt | — |
| Skills | （缺，新增） | Skills | — |
| MCPServers | （缺，新增） | MCPServers | — |
| Packages | PackageName | Packages | — |
| WriteEnabled + WriteScope | 同名字段 | （缺，二期补） | — |
| ModelHint / BudgetHint | 同名字段 | AllowedModels（近似） | — |
| Profile | —（config 全局） | — | default/minimal（被替换） |

### 4.3 与 Role 的关系（共存期，Q1B）

- package 内 `Role` 保持不变，安装后自动**派生**一个只读模板（ID 形如 `pkg:<package>/<role>`），出现在人才市场但带「随包安装」标记；
- Role 的 `Display`（Label/Color/Icon）映射到派生模板的 Avatar/Color：`Display.Icon` 为 emoji 时直接作为 Avatar；否则回退首字母头像 + `Display.Color`。后续 package 作者可直接在 Role `Display` 中增加 `Avatar` 字段（string，emoji 或 URL）获得更好呈现；
- 反向不成立：用户自定义模板不回写 Role；
- 子 agent 分派时既有解析链（BundlesForRole 等）继续生效；模板解析链与其合并规则见 §6。

---

## 五、存储与注册（Q2A）

```
~/.godex/state/agent-templates/
  ├── builtin/            # 内置模板（版本升级时覆盖，只读）
  │   ├── general-assistant.yaml
  │   ├── coder.yaml
  │   ├── researcher.yaml
  │   ├── reviewer.yaml
  │   └── minimal.yaml            # 兼容旧「极简模式」的等价模板
  └── user/               # 用户自定义（CRUD）
      └── my-dev.yaml
```

新增 `internal/core/templates`：

```go
type Manager struct{ dir string }

func (m *Manager) List() ([]AgentTemplate, error)            // builtin + user + package 派生
func (m *Manager) Get(id string) (AgentTemplate, error)
func (m *Manager) Save(t AgentTemplate) error                // 仅 user/
func (m *Manager) Delete(id string) error                    // 仅 user/
func (m *Manager) Resolve(id string) (ResolvedTemplate, error) // extends 解继承 + 引用校验（bundle/skill/mcp/package 存在性）
```

- 内置 `default` 模板 = 现行为（全量默认激活），保证升级零感知；内置 `minimal` 模板承接旧极简模式语义（仅 4 核心工具 + 重量级 prompt 段裁剪）。
- Q2 预留：`ProjectDir` 字段本期不参与解析；二期引入「全局库 + 项目覆盖」时，同名项目模板优先。

---

## 六、模板 → 运行时解析链

```
AgentTemplate (YAML)
  │
  ├─ Persona ───────────► system prompt 最前部（角色扮演，高于 instructions）
  ├─ Profile ───────────► buildDynamicSystemPrompt(profile)（capability_check 等段）
  ├─ BasePrompt ────────► 角色指令段（行为边界规则，如"不改未规划代码"）
  ├─ Skills ────────────► skill.Loader.Load → skill_catalog + active_skills 段
  ├─ Bundles/Tools ─────► toolHandler.SetActiveTools（复用 session_mode 骨架）
  │                        └─ 仅激活场景所需 schema → tool_availability 段收窄
  ├─ MCPServers ────────► 仅白名单内 MCP server 建连，其工具进 catalog
  ├─ Packages ──────────► package roles/tools 进入可用池
  ├─ WriteEnabled/Scope ► 对接 Phase 4.5 写 scope 解析链
  ├─ ModelHint ─────────► 模型选择提示
  └─ BudgetHint ────────► context_budget 按模板分配
```

**缓存稳定性约束（对应诉求 ①）**：

- 模板在会话创建时一次性应用并写入 locator metadata（沿用现有 `mode` 字段的做法，新增 `template` 字段），reload 恢复同一预设；
- 会话生命周期内模板**固定不变**（Q3A），prompt prefix 稳定 → prefix-cache 命中；
- prompt 段裁剪复用 `sessionModeIsMinimal` 的分支思路，推广为「模板声明裁剪哪些重量级段」（repo_map / skill_catalog / active_skills / environment）。

**与子 agent 解析链的合并规则**：orchestrator 会话有模板时，spawn 子 agent 的 bundle 集合 = `BundlesForRole(role)` ∩ 模板允许池（模板 MCPServers/Bundles 是**上限**），子 agent 可显式 required_bundles 在上限内追加；无模板时行为完全不变（向后兼容）。

---

## 七、三个消费入口

### 7.1 对话创建（替换标准/极简模式选择）

- Web UI 新建对话弹窗：模式选择器（标准/极简）替换为**模板选择器**（卡片式人才市场入口，支持搜索/场景标签过滤，默认选中 `default`）；
- 后端：会话创建请求的 `mode` 字段升级为 `template`（兼容期：传 `mode=minimal` 自动映射到内置 `minimal` 模板）；
- `ApplySessionMode` 泛化为 `ApplyTemplate(t ResolvedTemplate)`，保留 mode 常量作兼容别名。

### 7.2 业务智能体（二期收敛，本期只留接口）

- 本期：BizAPIKey 不动；管理台展示「可选模板」只读列表，管理员可参考模板配置 key 白名单；
- 二期：BizAPIKey 增加 `template_id`，key 的白名单字段变为「模板 + 覆盖」两层；step 创建时走同一 Resolve 链；
- 收敛完成的判据：模板与 key 白名单字段不再出现同义双份维护。

### 7.3 任务看板 M3（基于本设计重规划）

原 M3「协作增强」作废，重规划为**模板驱动的智能执行**：

| 项 | 设计 |
|---|---|
| 卡片 → 模板分派 | 卡片新增 `template_id` 字段（创建/编辑时从人才市场选择，缺省用看板项目默认模板）；执行器认领卡片起执行会话时，按该模板初始化 agent |
| 执行会话预算 | 模板 BudgetHint 作为执行会话上下文预算与 token 预算上限，超额可配置自动压缩或中止 |
| 执行者画像 | 看板卡片详情与执行进度页展示执行 agent 的模板（persona/工具边界），让「谁在干、凭什么干」可解释 |
| 规划-执行分离习惯 | 典型用法：规划类卡片用 `planner` 模板（禁写），实现类卡片用 `coder` 模板（写 scope 限定在卡片涉及路径），复核类卡片用 `reviewer` 模板（禁写）——把诉求 ② 的场景习惯落到看板工作流 |
| 原 M3 候选项去向 | SSE 变更流 / execution_report 归 M4；模板/导入导出由本设计承接 |

---

## 八、API 与 Web UI

### 8.1 API

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/v1/agent-templates` | 列表（含 builtin/user/pkg 来源标记 + Resolve 后的告警） |
| GET | `/v1/agent-templates/{id}` | 详情 |
| POST | `/v1/agent-templates` | 创建（仅 user） |
| PUT | `/v1/agent-templates/{id}` | 更新（仅 user） |
| DELETE | `/v1/agent-templates/{id}` | 删除（仅 user） |
| POST | `/v1/agent-templates/{id}/validate` | Resolve 校验（引用存在性 + token 估算，见 §10） |

会话创建请求新增 `template` 字段（兼容 `mode`），存入 locator metadata。

### 8.2 Web UI：人才市场页

**主入口：顶级导航新增「Agent 模板」页**（`AgentTemplatesPage`，`navPath: /agents`）。

放置位置：插在 **Skills 之后、Memory 之前**（`appRegistry.tsx` builtinApps 数组顺序）——模板与 skill 同属「给 agent 装能力」的资源类页面，且都含「全局池 + agent 消费」的关系；业务智能体（M4 收敛后引用模板）与任务看板（M3 引用模板）是下游消费者，不与它们混排。图标用 `TeamOutlined`（人才市场隐喻），i18n key `app.nav.agentTemplates`。

```
Chat │ Files │ Automation │ Nodes │ Notes │ Skills │ ★Agent 模板 │ Memory │ Settings │ Business Agents │ Taskboard
```

页面布局（卡片网格）：
- 卡片：Avatar 头像（emoji 直渲染 / 图片 URL / 缺省首字母+Color 字母头像）+ 名称 + 一句话描述 + 场景标签 + 资源规模徽章（工具数 / skill 数 / token 估算）+ 来源标记（内置/自定义/随包）；Avatar 同时用于模板选择器、看板执行者画像、会话列表的 agent 标识，形成一致的视觉身份；
- 详情/编辑抽屉：分区表单（身份 / 工具与权限 / Skill 与 Package / MCP / 人格 prompt / 资源提示），bundle、skill、MCP 均为从全局池**勾选**（复用业务智能体管理台的多选交互）；
- 「用此模板开聊」按钮：一键携带 template 跳转新建对话；
- 来源分区：内置 / 自定义 / 随包（`pkg:<pkg>/<role>` 派生只读卡片，不单独做 Role 管理页）三组展示，随包卡片标注来源 package 并链接到 Skills 页的包详情；
- 复用组件：MCPSettingsPanel、SkillsPage 选择器、业务智能体表单控件。

**辅助入口（消费侧，不做管理）**：

| 入口 | 形态 |
|---|---|
| 新建对话弹窗 | 模板选择器（卡片式，替换标准/极简切换），右下角「管理模板」链接跳 `/agents` |
| 任务看板（M3） | 卡片编辑抽屉内 template_id 下拉（含项目默认模板选项），「去人才市场」链接 |
| 业务智能体（M4） | key 编辑表单内 template_id 选择器（模板 + 覆盖两层） |

---

## 九、内置模板（初始集）

| ID | Persona 核心 | Bundles | Skills | 写权限 | BudgetHint | 典型场景 |
|---|---|---|---|---|---|---|
| `default` | 通用助手 | 现默认全集 | — | 允许 | 模型 window | 升级零感知兜底 |
| `minimal` | 精简执行 | core_code(4 工具) | — | 允许 | 小 | 承接旧极简模式 |
| `general-assistant` | 主动思考、主动建议的私人助理 | core_code, web, mcp | — | 受限 | 中 | 日常助理（诉求 ② 正例） |
| `coder` | 严谨 senior 工程师，不改未规划代码 | core_code, lsp | code-review, testing | 受限（write_scope） | 100K | 看板实现卡片 |
| `researcher` | 彻底的研究员，只读 | web, browser | — | **禁止** | 50K | 资料调研 |
| `reviewer` | 严格审查者，只读 | core_code, lsp | code-review | **禁止** | 100K | 看板复核卡片 |
| `planner` | 规划者，产出计划不动手 | core_code, lsp, planning | — | **禁止** | 100K | 看板规划卡片 |

---

## 十、验收指标（可验证，Q4）

1. **Token 收口**：同一任务（如「阅读某仓库并总结」）分别用 `default` 与 `researcher` 模板各跑一轮，记录首轮 system prompt + tool schema token 数；`researcher` 相对 `default` 下降 ≥ 40%（预期主要来自工具 schema 与 repo_map/skill_catalog 裁剪）。
2. **缓存命中**：同模板连续两轮对话，第二轮 prefix-cache 命中率不低于现有 default 模式基线；模板会话中途不发生 prompt prefix 失效（日志无 prefix-reset 记录）。
3. **边界生效**：`reviewer`/`researcher` 模板会话中调用写工具被拒（协议闸拦截，有审计记录）；`coder` 模板 write_scope 外写入被拒。
4. **兼容零回归**：不传 template 的既有会话行为与升级前完全一致（`default` 模板等价旧标准模式）；`go test ./internal/...` 无新增失败；`tsc -b` + `vitest run` + `vite build` 通过。
5. **看板分派**（M3 后）：带 `template_id` 的卡片执行会话的 system prompt 与该模板 Resolve 结果一致（集成测试断言）。

---

## 十一、分期路线

| 期 | 内容 | 出口判据 |
|---|---|---|
| **M1：模板核心** | AgentTemplate 结构 + Manager（CRUD/Resolve）+ ApplyTemplate 运行时链（persona/profile/skills/bundles/mcp 注入 system_prompt_dynamic 与 SetActiveTools）+ 会话创建传 template + 7 个内置模板 | §10 验收 1-4 通过 |
| **M2：人才市场 UI + 对话入口切换** | AgentTemplatesPage（卡片网格 + 编辑抽屉 + 校验）+ 新建对话模板选择器替换模式选择 + 兼容 mode 映射 | 用户可全程 UI 完成建模板→选模板开聊；验收 1-4 复测 |
| **M3：任务看板模板分派（原 M3 重规划）** | 卡片 template_id + 执行器按模板起会话 + 执行者画像展示 + 预算联动 | §10 验收 5 通过；taskboard 文档同步更新 |
| **M4：收敛与增强** | BizAPIKey template_id 收敛（§7.2）；项目级模板覆盖（Q2 预留启用）；模板导入导出/分享；会话中热切换模板（Q3B，复用 6.4 reset 语义，需明确缓存代价提示）；自然语言生成模板 | 模板与 key 白名单无双份维护 |

---

## 十二、风险与注意

- **兼容窗口**：`mode` 与 `template` 双字段并存期，两处都传时 template 优先并在响应中告警；`mode=minimal` 映射内置模板的行为要写进变更日志。
- **MCP 白名单语义**：模板 MCPServers 是「会话启用子集」，全局 MCP 配置仍归设置页管理；模板引用了已删除的 MCP server 时 Resolve 必须告警而非静默失败。
- **Persona 注入位置**：persona 进 stable prefix（创建时固定），不能进每轮动态段，否则破坏缓存命中——实现时需评审 system_prompt_dynamic 的段序。
- **看板既有卡片**：无 template_id 的存量卡片按项目默认模板（可配，缺省 `default`）执行，不强制迁移。
- **预算字段语义**：BudgetHint 目前是提示性字段（context_budget 分配），不等于硬 token 上限；硬上限与超限策略归 M3 看板联动时定义。
