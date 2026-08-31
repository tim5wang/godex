# 业务智能体管理台（Business Agents Console）设计

> 状态：Active / Implemented baseline｜ 关联：Agent Step Platform（A/B/C 已完成）
> 本文回答三件事：① 完善 ui_card 交互闭环 ② 业务智能体管理界面（含业务/工具/skill/package/工作目录/接入指南/嵌入预览）③ 现有「工作流」去留。

## 当前实现快照（2026-08-31）

- Business Agents 导航、Biz key CRUD、能力/预算/工作目录配置、接入示例与嵌入预览已在 `BusinessAgentsPage.tsx` 和 `/v1/biz/keys*` 落地。
- `POST /v1/agent-steps/{id}/reply`、SDK `replyStep`、`<godex-step>` 与 `ui_card` 续跑闭环已落地并有测试。
- Workflows 页面已在 `c9612c1` 删除；仅保留被聊天/嵌入链路复用的 `UiCardView`，不要再把它解释成 Workflows 产品仍存在。
- `template_id` 已加入 Biz key 并进入模板解析/迁移链；本文早期“skills/packages 仅展示”的描述是首版历史约束，当前运行时能力应以模板解析和 Agent Step 路由为准。

下文作为实现基线的设计记录保留；“新增/本期/实施顺序”均是历史交付措辞，不代表仍未实现。

## 1. 形态与目标

把 **biz API key** 升级成可视化的「业务智能体档案」：一个 key = 一个业务智能体配置块，
管理台集中管理其**认证、工具能力、知识召回、工作目录、预算**，并提供**接入指南 + URL 嵌入预览**，
让业务系统接入 godex 当 agent 大脑这件事从「看文档手写 curl」变成「点几下复制即用」。

目标用户：godex 管理员（配置每个业务智能体的能力边界）。

## 2. 范围与对标

### 2.1 范围（本次交付）

| 模块 | 内容 |
|---|---|
| 业务智能体 CRUD | 列表 + 创建/编辑/启停/删除（复用 `POST/GET/PATCH/DELETE /v1/biz/keys`） |
| 能力白名单 | 从全局池勾选绑定：MCP server、sandbox 工具、skill、package、recall provider、models |
| 工作目录 | 每个业务配置 `project_dir`（sandbox 默认工作目录） |
| 预算 | 预算额度 + 告警阈值（现有字段可视化） |
| 接入指南 | key 详情页内联「快速开始」：curl / TS SDK / 嵌入标签三形态代码片段一键复制 |
| URL 嵌入预览 | 基于该 key 实时渲染 `<godex-step>` 预览（真实可用，非截图） |
| ui_card 交互闭环 | 表单/按钮提交值 → 注入 step 会话续跑 → agent 继续 |

### 2.2 明确不做（本期）

- 不改 Agent Step 平台核心行为（同步单环节、超时降级、追踪端点保持现状）
- 不为「业务」引入独立实体（沿用 key = 业务智能体）
- 首版曾不消费 skill/package 白名单；当前已进入 template_id/AgentTemplate 解析链。若 key 未绑定模板，则仍需以实际 step/session 装配结果为准，不能只看表单字段推断运行时能力。

### 2.3 工作流去留评估（已与用户对齐）

结论：**移除 WorkflowsPage**。理由：它不是真正的工作流运行时（无 DAG/步骤状态机，只是把 playbook 当 prompt 发给普通 chat agent，"start 后不能中止"就是缺 step 级可中止运行时——Agent Step Platform 的 cancelStep 已补上）。

吸收去向：
| 资产 | 去留 | 去向 |
|---|---|---|
| `UiCardView.tsx` | 保留 | 已复用；ui_card 交互闭环本设计补齐 |
| Playbook（结构化剧本） | 保留内容 | 演化为业务智能体的「默认提示词/角色设定」字段 |
| Launch 流式展示（toolActivity/uiCards/pendingPermissions） | 吸收 | 嵌入预览复用其呈现思路（用原生 DOM 版） |
| KB 检索 | 吸收 | 业务智能体的「召回配置」 |
| 剧本编辑器 + 运行历史 + WorkflowsPage 壳 | 移除 | 无 |

## 3. 架构与数据模型

### 3.1 BizAPIKey 扩展（internal/services/usage/types.go）

```go
type BizAPIKey struct {
    ID               string        `json:"id"`
    Name             string        `json:"name"`
    Description      string        `json:"description,omitempty"`   // 新增：业务智能体描述
    DefaultPrompt    string        `json:"default_prompt,omitempty"` // 新增：默认提示词/角色设定（吸收 playbook）
    KeyHash          string        `json:"key_hash,omitempty"`
    KeyPrefix        string        `json:"key_prefix"`
    Enabled          bool          `json:"enabled"`
    MCPServers       []string      `json:"mcp_servers"`
    SandboxTools     []string      `json:"sandbox_tools"`
    Skills           []string      `json:"skills,omitempty"`        // 新增：白名单引用（本期仅配置/展示）
    Packages         []string      `json:"packages,omitempty"`      // 新增：同上
    Providers        []ProviderRef `json:"providers"`               // recall provider（含 godex://memory 内置）
    Models           []string      `json:"models"`
    ProjectDir       string        `json:"project_dir,omitempty"`   // 新增：sandbox 默认工作目录
    BudgetCredits    float64       `json:"budget_credits"`
    WarningThreshold float64       `json:"warning_threshold"`
    CreatedAt        time.Time     `json:"created_at"`
    UpdatedAt        time.Time     `json:"updated_at"`
}
```

> 消费说明：`project_dir` 由 routes_steps 通过 locator metadata 应用；skills/packages 的“仅展示”是首版历史语义。当前绑定 `template_id` 的 key 会走 AgentTemplate 解析/会话应用链，key 白名单字段作为覆盖层存在。

### 3.2 全局池数据源（前端已有端点，管理台直接复用）

| 池 | 端点 |
|---|---|
| MCP server | SettingsPage 配置的 godex.yaml mcp 段（经 /config 读取） |
| skill | `GET /skills/catalog` |
| package | `GET /packages` / `GET /packages/roles` |
| memory | `GET /memory`（知识库浏览） |

### 3.3 ui_card 交互闭环（后端）

新增端点（在 routes_step_track.go 注册，withBizKeyAuth 保护）：

```
POST /v1/agent-steps/{id}/reply
Body: {
  "value": string | object   // 用户提交的表单值或按钮值
  "turn_id": string          // 可选：回复的 ui_card 所属 turn
}
→ 200 { step_id, session_id, status: "queued"|"running", message }
```

实现：定位 step 会话（locator）→ `SubmitAsync(sessionID, envelope{role:user, text: 序列化的交互结果})`
→ 用结构化格式把用户交互值回注给 agent 续跑（同一条会话，不是新开）。

SDK（client.ts）新增 `replyStep(stepId, value, opts)`；`godex-step` 组件与 `UiCardView`
的提交回调改走 `replyStep`（替代现在「塞回输入框重跑」的 MVP）。

## 4. 前端页面结构

新导航项「业务智能体」（appRegistry，id: `business-agents`，navPath `/business-agents`）。

```
BusinessAgentsPage
├─ 左侧：业务智能体列表（名称/描述/状态/预算，可搜索）
├─ 右侧主区（选中一个 key 后）：
│   ├─ 概览 Tab：名称/描述/默认提示词/启停/预算（编辑表单）
│   ├─ 能力 Tab：MCP server 勾选 + sandbox 工具勾选 + skill + package（全局池多选）
│   ├─ 召回 Tab：recall provider 列表（含 godex://memory + 外部 URL）
│   ├─ 工作目录 Tab：project_dir 配置
│   └─ 接入 Tab：
│       ├─ 快速开始（curl / TS SDK / 嵌入标签 三段，复制按钮）
│       └─ 嵌入预览（真实 <godex-step> 组件，填 prompt 即跑）
└─ 新建按钮 → 抽屉表单（含初始 key 展示，仅创建时可见一次）
```

移除 WorkflowsPage：删除 appRegistry 的 `workflows` 项 + `loadWorkflowsPage` 懒加载引用；
保留 `UiCardView.tsx`（被 Workflows 及潜在页面引用，但实际已由 agent-step 复用）；
`workflows` 相关 i18n key 保留（避免大范围删键，后续清理）。

## 5. 验收标准（可验证）

1. **CRUD**：在管理台新建业务智能体 → 生成 `biz_` key（仅一次展示）；编辑能力白名单/工作目录/预算 → 保存后 `GET /v1/biz/keys/{id}` 字段正确落盘。
2. **能力白名单**：勾选 MCP server / sandbox 工具 / skill / package 后，key 详情回显一致。
3. **工作目录**：配置 `project_dir` 后，用该 key 调 `POST /v1/agent-steps` 的 step 会话工作目录落在该目录（单测断言 locator.ProjectDir）。
4. **接入指南**：key 详情页能一键复制 curl / TS SDK / `<godex-step>` 标签三形态片段，片段含该 key 的 base-url + api-key。
5. **嵌入预览**：`<godex-step base-url api-key>` 在管理台内真实可运行（输入 prompt → 返回结构化结果），后端同源无 CORS 问题。
6. **ui_card 闭环**：`POST /v1/agent-steps/{id}/reply` 把交互值注入会话并触发续跑（单测断言 session 收到 user 消息）；`UiCardView` 表单提交调用 `replyStep`。
7. **工作流移除**：导航不再出现「工作流」；`WorkflowsPage` 懒加载引用移除；`UiCardView` 保留且 UI 无回归。
8. **回归**：`go test ./internal/...` 无新增失败（既有失败清单外）；`tsc -b` / `vitest run`（SDK+新增）全绿；`vite build` 主应用构建通过。

## 6. 实施顺序（提交拆分）

1. **后端字段扩展**：BizAPIKey 加 description/default_prompt/skills/packages/project_dir + CRUD 透传 + 测试
2. **工作目录生效**：routes_steps 把 key.project_dir 应用到 step locator + 测试
3. **ui_card 回传端点**：POST /reply（SubmitAsync 注入）+ SDK replyStep + 测试
4. **前端管理台**：BusinessAgentsPage（CRUD + 能力/召回/工作目录/预算 + 接入指南 + 嵌入预览）+ appRegistry + i18n
5. **ui_card 交互闭环前端**：UiCardView/godex-step 提交走 replyStep
6. **移除工作流**：删 WorkflowsPage 注册与懒加载，保留 UiCardView
7. 全量验证 + 提交推送

## 7. 风险与注意

- `SubmitAsync` 注入续跑与「同步单环节」语义有张力：回复后 step 会继续在后台跑，调用方需轮询终态（SDK 已有 getStep）。本期保持简单：reply 返回 queued，不阻塞。
- 嵌入预览与主应用同源（同 godex 服务），无 CORS；但 `<godex-step>` bundle 需在管理台页面加载（vite 主构建不打包 embed 产物，预览时用运行时注入 script 或直接 import SDK 组件）。
- 移除 Workflows 时确认 `loadWorkflowsPage` 无其它引用点。
