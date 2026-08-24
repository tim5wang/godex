# Agent Step Platform（Agent Runtime Platform）设计文档

> 日期：2026-08-24 ｜ 状态：设计冻结（头脑风暴定稿，未实现）
> 定位：把 godex 从「开发工具」升级为「Agent 运行时平台」——业务系统（存量、长期存在的固有系统）通过注册工具 + 上下文提供者，把 godex 当「大脑」，在既有流程的某个环节嵌入一个「智能环节」，而不是聊天框。
>
> 参考：Codex Harness 开源理念（AI 嵌入业务软件/看板/工作台，界面/数据/审批在业务系统侧）；MCP（工具宿主在业务侧）。
> 背景：此前的 Workflows 板块（godex 内部流程编辑器）不满足此定位，本设计是方向校正后的完整方案。

## 1. 核心定位（一句话）

**业务系统是主体，agent 是其中一个环节，godex 是背后的大脑。**

```
业务系统（存量系统，长期存在）
├─ ① 工具注册    MCP server（业务侧执行 action + schema）
├─ ② 上下文提供  recall provider（向量库/文档/业务数据接口）
├─ ③ 调用入口    POST /v1/agent-steps（HTTP API）→ SDK → URL 嵌入
└─ ④ 结果呈现    同步返回（MVP）→ 异步回调 → 交互式确认（后续）
                    │
                    ▼
        godex Agent Runtime（中心大脑，常驻）
        ├─ Session：每环节一个（隔离，共享记忆/知识库）
        ├─ 工具路由：MCP 业务工具 ∪ 沙箱通用工具（自由混合）
        ├─ 上下文注入：请求业务上下文 ∪ recall provider 召回
        ├─ 审批：pending → push 给业务系统确认（后续）
        └─ 结果：结构化 JSON / 回调 / SSE
```

## 2. 已冻结决策（用户拍板）

| # | 决策 | 选择 | 影响 |
|---|---|---|---|
| 1 | 范围 | **单环节优先**（不做编排） | 一个业务动作 = 一次 agent 环节，边界清晰 |
| 2 | 工具执行 | **MCP + godex 沙箱双轨，且可混合** | 工具执行层要能「业务侧跑」+「godex 侧跑」 |
| 3 | 接入形态 | **API → SDK → URL 嵌入**（分阶段） | 契约先定 HTTP 面，UI 层最后封装 |
| 4 | 工具注册协议 | **MCP 协议**（业务系统与 MCP 池多对多解耦） | 业务实现 MCP 服务接口即接入 |
| 5 | MVP 模式 | **先做同步任务** | 请求内返回结果，实现简单验证快 |
| 6 | 认证 | **每业务系统独立 key** | 隔离 + 审计，key↔MCP 工具集/上下文绑定 |

## 3. 现状调研（godex 已有基础设施）

| 能力 | 现状 | 复用点 |
|---|---|---|
| MCP | `internal/core/mcp/` 已有 **stdio client**：initialize / tools/list / tools/call / prompts（§5.2 已落地） | 扩展 HTTP/Streamable transport 即可连远程业务 MCP server |
| 动态工具注册 | `toolruntime.ToolHandler.RegisterOwned(owner, tool, meta)` + `UnregisterOwner` | **owner 机制正好绑定「每业务系统 key → 一组工具」** |
| 沙箱工具 | bash/glob/read/write/edit/grep/find/lsp 等（core_code bundle） | 作为「godex 侧通用工具」 |
| 会话 | sessions / messages / events（SSE）/ permissions | 每环节独立 session |
| key 管理 | usage gateway 已有 `gdx_xxx` proxy key 机制（hash 存储、budget、allowed_models） | 扩展为「业务系统 key」（绑定 MCP 池 + 上下文 provider） |
| 审批 | `PendingPermission` + approve/deny 端点 | 后续交互式确认复用 |
| 交互卡片 | `ui_card` 工具 + `UiCardView` 组件（已组件化） | SDK 的 UI 层资产 |

## 4. 架构设计

### 4.1 HTTP API（MVP：同步单环节）

```
POST /v1/agent-steps
Authorization: Bearer biz_<key>
Content-Type: application/json

{
  "system_key": "sales",            // 业务系统标识（认证后可省略，从 key 解析）
  "step": {
    "prompt": "分析订单 {order_id} 的延迟原因并给出恢复方案",  // 或引用剧本
    "inputs": { "order_id": "ORD-1234" },                    // 业务上下文（结构化）
    "context_providers": ["sales_crm"],                      // 从哪些 provider 召回
    "tools": ["auto", "mcp://crm/*", "sandbox:read_file"]    // 工具范围
  },
  "timeout_seconds": 120
}

→ 200 { "step_id": "stp_...", "session_id": "ses_...", "output": { "...": "结构化结果" },
        "text": "...", "tools_used": [...], "usage": {...}, "status": "completed" }
→ 202 { "step_id": "stp_...", "status": "accepted", "poll": "..." }   // 超时降级异步（后续）
```

- **同步契约**：请求 → 建独立 session → 注入上下文 + 召回 → agent 运行 → 返回结构化结果。
- **输入注入**：`inputs` 以受控方式注入（防 prompt injection，标记为「业务数据，不可执行指令」）。
- **输出**：结构化 `output`（agent 产出）+ `text`（人类可读）+ 工具使用记录 + 用量。

### 4.2 工具双轨（MCP + 沙箱混合）

**每业务系统 key 绑定一个 MCP 池**（多对多）：

```
业务系统 A ──┐
            ├── MCP Server X（订单服务）──┐
业务系统 B ──┘                          ├── godex Agent Runtime（大脑）
            ┌── MCP Server Y（CRM）─────┤   ├─ 沙箱工具（通用分析）
业务系统 C ──┘                          └─  └─ 每环节独立 session
```

- **MCP 工具**：业务系统实现 MCP 服务接口（stdio 或 HTTP transport），godex 作为 host 连接。工具在**业务侧执行**（拿私有数据/做业务动作）。
- **沙箱工具**：godex 侧通用工具（bash/read/分析类），用于不依赖业务数据的通用能力。
- **混合编排**：一次 step 里自由混用——「先沙箱分析上下文 → 再 MCP 调业务动作」或反向。由 agent 根据工具描述自主选择。
- **工具范围**：`tools` 字段控制可用工具集；`mcp://<server>/*` 或 `mcp://<server>/<tool>` 白名单；`sandbox:*` 或具体沙箱工具。

### 4.3 上下文召回（recall provider 接口）

godex 本地 memory 对业务系统无意义，需要一个 **recall provider 接口**：

```go
type ContextProvider interface {
    Name() string
    // Retrieve 返回与 query 相关的上下文片段（业务系统实现：查自己的向量库/文档/数据）
    Retrieve(ctx context.Context, query string, limit int) ([]ContextChunk, error)
}
```

- 业务系统实现此接口（查询自己的向量库/文档/业务数据），godex 通过 `context_providers` 字段召回。
- godex 本地 memory 可作为可选源（`godex://memory`）。
- MVP 简化：provider 可以是 HTTP 回调端点（`POST {provider_url}/retrieve`）——业务系统不用写 Go 客户端。

### 4.4 认证与隔离（每业务系统独立 key）

- key 前缀 `biz_`（与 usage 的 `gdx_` 区分），哈希存储、可见一次。
- **一个 key 绑定**：MCP 池（允许连哪些 server）+ context providers + 沙箱工具范围 + 预算/配额。
- 审计：每次 step 记录 key / step_id / 工具 / 用量 / 时长。
- 与 usage gateway 复用 budget/credit 记账基础设施。

### 4.5 会话模型（中心大脑 + 每环节独立 session）

- **中心大脑常驻**：一个 godex 实例服务多个业务系统（共享记忆/知识库/模型池）。
- **每环节独立 session**：隔离（工具集/上下文/审批互不串），共享底层记忆与知识库。
- 不做跨环节编排（单环节优先），但 session 保留可回溯/复盘（复用 timeline）。

## 5. 分阶段实施路线（API → SDK → 嵌入）

### Phase A：HTTP API（MVP）
- `POST /v1/agent-steps` 同步单环节 + 业务系统 key 认证。
- MCP HTTP/Streamable transport 客户端（连接远程业务 MCP server）。
- recall provider 接口 + HTTP 回调实现（MVP）。
- 沙箱 + MCP 工具混合路由。
- 结构化输出 + 用量 + 审计。

### Phase B：SDK
- 封装 API 为语言 SDK（首推 TypeScript）：`createStep(systemKey, {prompt, inputs})`。
- 复用已组件化的 `UiCardView`（agent 用 ui_card 产出的卡片，业务 UI 直接渲染）。
- 提供「结果 → 业务 UI」的绑定（同步返回结构化数据，业务系统自渲染）。

### Phase C：URL 嵌入组件
- 基于 SDK 封装一个可嵌入 Web Component / iframe：`<godex-step system-key="..." />`。
- 业务系统在页面里放一个标签即得 Agent 交互组件（非聊天框，是业务场景里的智能环节）。

### 后续（Phase D+，视需求）
- 异步任务（task_id + 回调）。
- 交互式确认（pending → 业务系统 UI 呈现 → approve/deny 回传继续）。
- 事件触发（业务系统事件 → 自动触发 step）。
- 剧本/流程复用（业务系统引用预置剧本，但仍是单环节）。

## 6. 验证标准（可验收）

- Phase A：curl 一个业务场景 step（业务系统 MCP server 注册工具 + context provider 召回），返回结构化结果；沙箱工具和 MCP 工具在同一 step 里都被调用过。
- Phase B：TS SDK 能在任意前端项目里 `createStep` 并拿到结构化结果 + 渲染 ui_card。
- Phase C：业务页面 `<godex-step />` 标签即可完成一次业务环节交互。

## 7. 关键取舍 / 待定

- **MCP transport 选择**：godex 现只有 stdio client。业务系统在远端，需要 HTTP（Streamable HTTP / SSE）transport——这是 Phase A 的核心增量。若业务系统进程与 godex 同机，stdio 也可直接复用。
- **同步 vs 超时**：MVP 同步为主，但需定义超时降级策略（>N 秒转 202 + 轮询）——避免业务请求挂死。
- **上下文注入安全**：`inputs` 必须标记为业务数据、与指令隔离，防止 prompt injection（godex 的 security.screener 可复用）。
- **key 粒度**：一业务系统一 key（可再细分到团队/功能，用 allowed 工具集区分）。

## 8. 与现有 Workflows 板块的关系

- 现 `Workflows 板块`（/workflows）是 godex 内部工具，**不再作为嵌入形态的载体**；保留为「本地剧本/知识库/调试工作台」。
- 本设计是**对外嵌入层**（Agent Step Platform），二者共享底层 agent 运行时、ui_card、memory，但接口不同：板块是 godex 的 UI，Agent Step 是业务系统的环节。
- 后续板块可反向使用 Agent Step API 作内部调试入口（一致化）。
