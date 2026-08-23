# 第三方 UI 集成指南（以 godex 为 agent 基建）

> 日期：2026-08-24 ｜ 状态：Step 3 完成
> 目标：让其他 UI（Web / Electron / IM 机器人 / 内部工具）快速以 godex 为 agent 基建，实现「知识库召回 + 特定环境问题解决定制化 + 表单/按钮对话交互」。

## 1. 对接总览

```
第三方 UI（自定义表单/按钮/卡片渲染）
        │  HTTP + SSE（web-token 认证）
        ▼
┌────────────────────────────────────────────────┐
│ godex backend                                  │
│  · sessions/messages/events/permissions        │  ← 完整 agent 会话面
│  · memory（知识库召回，L0/Core/Relevant）        │
│  · notes（剧本/工作流存储，tag=workflow）         │
│  · ui_card 工具（agent 产出表单/按钮/卡片）       │  ← Step 4
│  · slash commands / bundles / skills（定制化）   │
└────────────────────────────────────────────────┘
```

## 2. 认证

所有端点使用 `Authorization: Bearer <web_token>`（`godex.yaml` 的 `web.token`）。

```
GET /meta            → 检查 auth_required
POST /sessions       → 建会话（locator: {channel:"web", key:"<your-uuid>"}）
```

未配置 web.token 时无需认证（本地单用户场景）。

## 3. 核心流程（表单/按钮式对话）

### 3.1 建会话并发送引导消息

```http
POST /sessions
Content-Type: application/json
Authorization: Bearer <token>

{ "locator": { "channel": "web", "key": "wf-001" } }
```

```http
POST /sessions/{session_id}/messages
Content-Type: application/json

{
  "envelope": {
    "source": "third-party-ui",
    "text": "## 排查测试失败\n1. 复现失败...\n2. 读取源码...\n（剧本 Markdown）"
  },
  "queue_mode": "steering"
}
```

### 3.2 订阅事件流（SSE）

```http
GET /sessions/{session_id}/events?replay=active
Accept: text/event-stream
```

关键事件：

| 事件 | 用途 |
|---|---|
| `user_message_accepted` | 消息已入队 |
| `assistant_text_delta` | 流式文本输出（`payload.text`） |
| `tool_call_started` / `tool_call_finished` | 工具活动（`payload.name` / `payload.output` / `payload.error`） |
| `tool_call_finished` + name=`ui_card` | **表单/按钮卡片**（`payload.output` 是 JSON，见 §4） |
| `snapshot_ready` | 快照就绪（含 `pending_permissions`） |
| `turn_completed` | 本轮结束 |

### 3.3 表单/按钮交互（Step 4）

1. **agent 产出卡片**：agent 调用 `ui_card` 工具，`tool_call_finished` 事件的 `output` 是结构化 JSON：

```json
{
  "kind": "form",
  "title": "收集发布信息",
  "fields": [
    { "name": "version", "label": "版本号", "type": "text", "required": true },
    { "name": "target", "label": "发布目标", "type": "select",
      "options": [{ "label": "生产", "value": "prod" }, { "label": "预发", "value": "staging" }] }
  ]
}
```

2. **UI 渲染成表单**：按 `kind` 渲染——
   - `form`：JSON Schema 风格字段 → 输入框/下拉/多行文本
   - `button_group`：`actions[]` → 按钮行
   - `card`：`content` → Markdown 卡片

3. **提交回 agent**：把表单值/按钮动作作为 follow-up 消息发回：

```http
POST /sessions/{session_id}/messages
{
  "envelope": { "source": "third-party-ui", "text": "{\"version\":\"v1.5.0\",\"target\":\"prod\"}" },
  "queue_mode": "follow_up"
}
```

> 交互协议：**文本即协议**。卡片 JSON 和表单值都走 text envelope，agent 端零协议改动即可理解——`ui_card` 的输出 JSON 本身就是给 agent 看的结构化表示，UI 只需透传用户填写结果。

### 3.4 工具审批按钮（权限）

1. `snapshot_ready` / 轮询 `GET /sessions/{id}/permissions` 拿 `pending_permissions`
2. 渲染审批卡片（工具名 + 命令/路径 + 原因 + 允许/拒绝按钮）
3. 按钮动作：

```http
POST /sessions/{id}/permissions/{request_id}/approve   { "scope": "once" | "session" }
POST /sessions/{id}/permissions/{request_id}/deny      { "reason": "..." }
```

## 4. ui_card 工具契约

| 字段 | 类型 | 说明 |
|---|---|---|
| `kind` | `form` \| `button_group` \| `card` | 卡片类型（必填） |
| `title` | string | 卡片标题 |
| `content` | string | Markdown 内容/说明 |
| `fields[]` | array | 表单字段（form 用） |
| `fields[].name/label/type/required/placeholder` | — | `type`: text \| textarea \| select \| number |
| `fields[].options[]` | array | select 选项 `{label, value}` |
| `actions[]` | array | 按钮（button_group 用） |
| `actions[].id/label/kind/value` | — | `kind`: message \| command \| approve \| url |

非 Web UI（TUI/CLI）看到的是同一 JSON 的原始输出，不会丢信息。

## 5. 知识库召回

| 方式 | 说明 |
|---|---|
| **预置记忆文件** | `~/.godex/memory/*.md`，每文件一个主题；agent 自动分层召回（L0 identity / Core / Relevant） |
| **程序化写入** | `POST /memory/remember` 写入知识条目 |
| **召回预览** | `GET /memory/context?q=<query>` 返回三层召回，UI 可做「搜索前预览」 |
| **项目内 docs/** | 工作区 `docs/` 由 agent 的读文件/检索能力召回 |

## 6. 工作流定制

| 机制 | 用途 |
|---|---|
| **剧本（notes, tag=workflow）** | 可复用问题解决流程，Markdown 即启动提示词 |
| **slash commands** | `GET /commands` 列出，`POST /sessions/{id}/messages` 发送 `/cmd args` |
| **bundles / skills** | 能力包与提示词技能（`godex.yaml` + `~/.godex/skills/`） |
| **WASM 插件** | 领域工具（`godex:plugin@0.1` ABI），可产出自定义逻辑 |

## 7. 最小示例（伪代码）

```ts
// 1. 建会话
const { session_id } = await api.post("/sessions", { locator: { channel: "web", key: uuid() } });

// 2. 发剧本消息
await api.post(`/sessions/${session_id}/messages`, {
  envelope: { source: "my-ui", text: playbookMarkdown },
  queue_mode: "steering",
});

// 3. SSE 流式渲染 + 卡片/审批
const stream = sse(`/sessions/${session_id}/events?replay=active`);
stream.on("tool_call_finished", (e) => {
  if (e.payload.name === "ui_card") renderCard(JSON.parse(e.payload.output));
});
stream.on("snapshot_ready", async () => {
  const perms = await api.get(`/sessions/${session_id}/permissions`);
  renderApprovalButtons(perms); // → approve/deny POST
});
stream.on("assistant_text_delta", (e) => appendText(e.payload.text));
stream.on("turn_completed", () => setDone());
```

## 8. 集成检查清单

- [ ] `GET /meta` 确认 auth_required，配置 web-token
- [ ] `POST /sessions` + `POST /messages` 冒烟
- [ ] SSE `?replay=active` 收到 `assistant_text_delta` / `turn_completed`
- [ ] 剧本里提示 agent 用 `ui_card` 产出表单（验证卡片渲染链路）
- [ ] 触发一个需审批工具（如写文件），验证审批按钮 approve/deny
- [ ] `GET /memory/context?q=...` 验证知识库召回

## 9. 与 Workflows 板块的关系

godex Web UI 的 **Workflows 板块**（`/workflows`）就是本契约的参考实现：
- Playbooks 页签 = 剧本 CRUD（notes, tag=workflow）
- Knowledge 页签 = 知识库召回预览（memory）
- Launch 页签 = 表单启动 + 流式结果 + ui_card 卡片 + 审批按钮 + 工具活动

第三方 UI 可以直接复用同一套数据（剧本/知识库），或仅把 godex 当 agent 引擎、自建交互层。
