# 工作流集成方案：以 godex 为 agent 基建的定制化对话 UI

> 日期：2026-08-23 ｜ 状态：提案（调研完成）
> 目标：让其他 UI 快速以 godex 为 agent 基建，实现「知识库召回 + 特定工作环境问题解决定制化」，提供比纯发消息更易用的表单/按钮对话交互（Pi 在这方面做得不错，作为参考）

## 0. 核心洞察

**godex 已经是完整的 agent 基建，缺的不是能力而是「UI 表达层」。**

- godex 的 Web API（sessions/messages/events/permissions）已经是完整的 agent 会话面：工具调用、知识库召回、审批、fork/retry/resume 全都有 HTTP 端点。
- Pi 的「表单/按钮」交互本质 = **自定义消息类型（customType）+ 自定义渲染器（registerMessageRenderer）+ 工具调用模板渲染**。agent 发出结构化消息，UI 渲染成表单/按钮，而不是纯文本。
- godex 缺的正是这一层：`protocol.Block` 只有 text/image/tool_use/tool_result/thinking，**没有通用自定义 UI 块**；前端虽有卡片先例（ChangesCard/TodoCard/SubagentCard），但无「自定义类型 → 自定义渲染器」的注册机制。

## 1. 现状盘点

### 1.1 godex 已有对外集成面（全部可用）

| 面 | 端点/机制 | 能力 | 状态 |
|---|---|---|---|
| **Web API（完整 agent 会话面）** | `POST /sessions`、`POST /sessions/{id}/messages`（Envelope 支持附件/steering/queue_mode）、SSE `GET /sessions/{id}/events`、`POST /sessions/{id}/permissions/{rid}/approve\|deny`、fork/retry/resume/cancel/model | 完整 agent 循环：工具、记忆召回、审批、会话管理 | ✅ 现成 |
| **Usage Gateway** | `/v1/chat/completions`、`/v1/messages`、`/v1/responses` | LLM 协议转换 + 预算/模型映射（**无 agent 循环**） | ✅ 现成 |
| **Relay 远程** | `/control/nodes/{id}/proxy/{path...}` | 远程 node 透传同一套 Web API | ✅ 现成 |
| **ACP Harness** | `acp:<id>` whole-turn / `acp_agent` 委派 | godex 作宿主调外部 agent | ✅ 现成 |
| **知识库召回** | memory 分层（L0 identity / Core / Relevant recall）+ `/memory` API + 记忆文件预置 | 相关性召回注入上下文 | ✅ 现成 |
| **工作流定制** | bundle / skill / scope / slash commands / WASM 插件（`godex:plugin@0.1`：tools/prompts/policy ABI） | 能力包 + 提示词技能 + 领域工具 | ✅ 现成 |
| **工具审批** | `PendingPermission` + approve/deny 端点（交互式审批策略可配） | 「按钮」交互的基础 | ✅ 现成 |

### 1.2 Pi 的「表单/按钮」交互机制（参考实现 `temp/pi/`）

1. **RPC mode**（`modes/rpc/`）：jsonl stdin/stdout 无头协议 —— `prompt` / `steer` / `follow_up` / `abort` / `bash` / `get_commands` / `get_state` / `set_model` / `compact` / `export_html` / `switch_session` / `fork`。任何 UI 可对接，UI 完全自定义。
2. **export-html**（`core/export-html/template.js`）：`renderToolCall(call)` 按工具名 switch —— bash 显示 `$ command` + 可展开输出；read 显示 path + 行号 + 图片；write 显示行数；edit 显示 diff 高亮；pending/success/error 三态。
3. **extensions**（`core/extensions/loader.ts`）—— 这是「表单/按钮」的真正机制：
   - `registerMessageRenderer<T>(customType, renderer)`：扩展注册自定义消息渲染器
   - `appendEntry(customType, data)` / `sendMessage(message)`：扩展产出自定义类型消息
   - `registerCommand(name, options)` / `registerTool(tool)` / `registerShortcut` / `registerFlag`
   - 自定义消息（`harness/messages.ts` `CustomMessage`）：`role:"custom"` + `customType` + `content` + `display` + `details`

**结论**：Pi 的表单/按钮 = 自定义消息类型 + 自定义渲染器 + 工具模板。对话流中 agent（或扩展）发 `customType` 消息，UI 用注册的渲染器显示成表单/按钮。

### 1.3 godex 差距（要做的事）

| 维度 | godex 现状 | 需要补的 |
|---|---|---|
| 消息类型 | `protocol.Block`：text/image/tool_use/tool_result/thinking | **无通用自定义 UI 块**（新增 `BlockCustom`） |
| 事件流 | `assistant_text_delta` / `tool_call_*` / `permission` 等 | **无 custom 消息事件** |
| UI 渲染 | 前端固定处理 text/tool/todo/subagent/command 卡片 | **无「自定义类型 → 渲染器」注册机制**（第三方 UI 需要） |
| 表单/按钮 | 只有审批 approve/deny 按钮 | 需要 agent 能产出「表单」「按钮」「卡片」结构消息 |

## 2. 方案设计（三层）

```
第三方 UI（自定义表单/按钮渲染）
        │  Web API（web-token 认证）
        ▼
┌────────────────────────────────────────────────┐
│ godex（agent 基建，零改动或极小改动）            │
│  - sessions/messages/events/permissions        │
│  - 知识库召回（memory 分层 + 预置记忆文件）       │
│  - 工作流定制（bundle/skill/slash/WASM 插件）    │
│  - 工具审批（按钮动作 → approve/deny）           │
└────────────────────────────────────────────────┘
```

### 2.1 协议层（godex 改动，最小）：新增通用自定义 UI 块

给 `protocol.Block` 增加 `BlockCustom BlockType = "custom"`：

```go
// Block 新增字段
Type    BlockType `json:"type"`              // "custom"
// Custom 携带任意结构化 UI 数据（表单 schema / 按钮组 / 卡片）
Custom  *CustomBlock `json:"custom,omitempty"`
```

```go
type CustomBlock struct {
    Kind string                 `json:"kind"`           // "form" | "button_group" | "card" | ...
    Schema map[string]any        `json:"schema,omitempty"` // 表单 JSON Schema（渲染器驱动）
    Actions []CustomAction       `json:"actions,omitempty"` // 按钮组
    Data map[string]any          `json:"data,omitempty"`   // 卡片/回填数据
}

type CustomAction struct {
    ID    string `json:"id"`
    Label string `json:"label"`
    // action 语义：提交消息 / 执行命令 / 触发工具 / 审批 / 回调 URL
    Kind  string `json:"kind"`  // "message" | "command" | "tool" | "approve" | "url"
    Value string `json:"value"` // 消息文本 / 命令 / 工具名 / 审批 id / URL
}
```

- 事件流加 `EventCustomMessage`（`custom_message`），携带 `{kind, data}` 透传。
- Agent/工具/WASM 插件均可产出 `BlockCustom`（扩展 `godex:plugin@0.1` ABI 或新增 `ui_card` 工具）。

**关键设计**：godex 自身 UI 不必深入渲染每种 custom —— 提供 **JSON Schema → 表单** 的通用渲染器 + 按钮组渲染器即可；第三方 UI 按 `kind` 注册自己的渲染器。这样 godex 端改动极小，第三方获得最大自由度。

### 2.2 知识库召回（零改动，配置即可）

- **预置知识库**：把领域知识写成 memory 文件（`~/.godex/memory/*.md`，每文件一个主题），agent 自动做 L0/Core/Relevant 分层召回注入上下文。
- **/memory API**：外部系统可程序化写入知识（`POST /memory`），构建「运营知识库」。
- **工作区 docs/**：项目内 `docs/` 由 agent 的 repo_map/读文件能力召回（适合代码库知识）。
- 召回效果可调：`memory.strategy`（per-turn/agent-only/consolidated）、`memory.consolidate_after`。

### 2.3 工作流定制（零改动，配置/插件即可）

| 机制 | 用途 | 配置位置 |
|---|---|---|
| **bundle** | 能力包（写 scope / 工具集 / 提示词） | `godex.yaml` api.bundles |
| **skill** | 领域提示词技能（问题解决流程模板） | `~/.godex/skills/*/SKILL.md` |
| **slash commands** | 常用操作一键触发（`/triage`、`/report`…） | `godex.yaml` + Web UI 直接可用 |
| **scope** | 环境隔离（session/personal/org） | `godex.yaml` control |
| **WASM 插件** | 领域工具（如 `todo_scan`、知识库查询） | packages 安装 |

### 2.4 UI 层（第三方 UI 的对接契约）

**对接面 = godex Web API + web-token**：

1. `POST /sessions` 建会话（或复用已有）
2. `POST /sessions/{id}/messages` 发消息（Envelope：text/attachments/steering/queue_mode）
3. SSE `GET /sessions/{id}/events?replay=active` 收事件（增量渲染：assistant_text_delta / tool_call_* / custom_message / snapshot_ready）
4. `POST /sessions/{id}/permissions/{rid}/approve|deny` —— 按钮「允许/拒绝」直接映射
5. 自定义渲染器：按 `kind` 注册 —— form → JSON Schema 表单（提交 = 发消息或执行命令）；button_group → 按钮行（动作映射到命令/消息/审批）

**Pi RPC mode 对照**：godex 的 Web API + SSE 事件流已覆盖 RPC 的 prompt/steer/follow_up/abort/bash/get_commands/get_state 语义（events 有 steering/queue_mode，permissions 有审批，commands 有 slash）。**不需要另建 RPC 协议** —— 第三方 UI 直接对接 REST + SSE 即可，比 jsonl 更适合 Web UI。

## 3. 工作量评估

| 项 | 工作量 | 说明 |
|---|---|---|
| `BlockCustom` + 事件 | S（协议层小改） | protocol.go + events.go + 前端透传 |
| JSON Schema 表单渲染器 + 按钮组渲染器（godex 自带 UI） | M | ChatPage 消息渲染 + 组件 |
| WASM 插件 ABI 扩展（可选，产出 custom 块） | M | `godex_ui_emit` host call 或工具 |
| 文档：第三方 UI 对接契约（Web API 指南） | S | 现有 Web API + custom 块说明 |
| 示例：最小第三方 UI（表单/按钮 + 知识库 + 工作流） | M | 参考 Pi export-html 的模板渲染 |

**总计**：核心（协议 + 渲染器 + 文档）≈ 1 个里程碑；示例 UI 可并行。

## 4. 与 Pi 的对比结论

| 能力 | Pi | godex（方案后） |
|---|---|---|
| 无头协议 | RPC jsonl | Web API + SSE（更适 Web UI）✅ |
| 自定义消息 → 自定义渲染器 | extensions `registerMessageRenderer` | `BlockCustom` + 前端按 kind 注册渲染器（等价）✅ |
| 工具调用模板渲染 | export-html `renderToolCall` | 前端已有 tool 卡片 + 可扩展 custom 渲染器 ✅ |
| 知识库召回 | —（Pi 无内置记忆分层） | memory 分层 + /memory API + 预置文件 ✅（godex 更强） |
| 工作流定制 | extensions（registerTool/Command） | bundle/skill/slash/WASM 插件（更系统）✅ |
| 审批按钮 | 无内置 | PendingPermission + approve/deny 端点 ✅ |

**结论**：godex 在 agent 基建（知识库、工具、审批、工作流）上已领先；唯一缺的是「UI 表达层」——`BlockCustom` + 渲染器注册机制，补齐后即可支撑第三方表单/按钮 UI，且改动集中在协议层 + 前端渲染，不影响 agent 主链路。

## 5. 建议落地顺序

1. **Step 1**：`protocol.Block` 加 `BlockCustom` + `CustomBlock` 结构 + 事件 `custom_message`（含测试）——纯增量。
2. **Step 2**：前端（Web UI）加通用渲染器：JSON Schema 表单 + 按钮组 + 卡片；`/v1/chat/completions` 网关对 custom 块的透传策略（可选：透传为文本摘要）。
3. **Step 3**：WASM 插件 ABI 加 `godex_ui_emit` host call（可选），让领域插件产出表单/按钮。
4. **Step 4**：写「第三方 UI 集成指南」（对接契约 + 最小示例）。
5. **Step 5**（可选）：参考 Pi RPC 提供极薄的无头适配层，给非 HTTP UI（Electron/CLI 壳）用。
