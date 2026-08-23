# Workflows 板块设计文档（产品化）

> 日期：2026-08-24 ｜ 状态：Step 1 已实施（板块骨架落地）
> 目标：在 godex Web UI 里新增一个与 Usage 并列的 **Workflows** 板块，产品化地承载「知识库召回 + 特定环境问题解决定制化 + 表单/按钮式对话」——让第三方 UI / 普通用户无需写代码就能以 godex 为 agent 基建跑定制流程。

## 1. 板块定位

**Workflows = 工作流中心**，三个页签，全部复用现有后端能力（零后端改动）：

| 页签 | 产品名 | 承载能力 | 复用后端 |
|---|---|---|---|
| Playbooks | 剧本 | 可复用的问题解决流程（Markdown 剧本） | notes API（tag=`workflow`）|
| Knowledge | 知识库 | 知识库召回预览 + 记忆浏览 | memory API + /memory/context |
| Launch | 启动 | 表单驱动的对话启动（选剧本 → 填指令 → 跑 agent → 流式结果） | sessions + messages + SSE events |

## 2. 已实施内容（Step 1 骨架）

### 2.1 导航注册
- `ui/web/src/app/appRegistry.tsx`：新增 `builtinApps` entry
  ```ts
  { id: "workflows", navPath: "/workflows", routePaths: ["/workflows"],
    icon: <BookOutlined />, labelKey: "app.nav.workflows",
    component: pageComponent(loadWorkflowsPage, "WorkflowsPage"),
    isActive: (pathname) => pathname.startsWith("/workflows"),
    headerSubtitleKey: "workflows.pageSubtitle" }
  ```
- 侧边栏菜单自动出现（`App.tsx` 的 `builtinApps.map` 驱动），与 Usage 并列，懒加载 + hover 预取。

### 2.2 页面
- `ui/web/src/features/workflows/WorkflowsPage.tsx`（~620 行，三页签 Tabs）
- `ui/web/src/pages/WorkflowsPage.tsx`（re-export）

**Playbooks 页签**：
- 卡片网格（标题 / 摘要 / `workflow` 标签）+ 搜索
- 新建 / 编辑（Modal 表单：标题 + 摘要 + Markdown 流程）
- 删除（Popconfirm）
- 点卡片 ▶ 自动切到 Launch 页签并预选剧本

**Knowledge 页签**：
- 召回预览：`/memory/context?q=...` → L0 identity / Core / Relevant 三层渲染
- 记忆浏览：`/memory` 列表（标题 + 类型 tag + 摘要）

**Launch 页签**：
- 表单：选剧本（Select 搜索）+ 附加指令（TextArea）+ 预览（MarkdownContent）
- 运行：`openSession` → `submitMessage(steering)` → `streamEvents` 流式渲染 `assistant_text_delta` → `turn_completed` 完结
- 运行状态卡片（idle/running/completed/error 四态 tag），取消 abort，完成后「在聊天中打开」

### 2.3 i18n
- `ui/web/src/i18n/messages.ts`：`app.nav.workflows`（en: Workflows / zh: 工作流）+ 完整 `workflows.*` 段（en+zh，~50 键）

## 3. 验证

- `tsc -b` ✅（含 `npx tsc --noEmit`）
- `vite build` ✅
- `go build ./...` ✅

## 4. 分阶段路线图

### Step 1（✅ 已完成）：板块骨架
三页签 + 导航 + i18n，全部复用现有 API。

### Step 2（✅ 已完成）：产品化增强
- **剧本模板库**：内置 4 个预设模板（测试失败排查 / 发布前检查清单 / 新环境接入 / 问题复盘报告），工具栏「从模板新建」Dropdown 一键填充编辑器。
- **剧本运行历史**：localStorage 持久化最近 20 次运行（剧本 + 状态 + 时间 + 会话），Playbooks 页签展示最近 6 条 + 一键清空 + 打开对应聊天会话。
- **Launch 结构化结果**：运行中实时展示工具活动 chips（tool_call_started/finished 事件驱动，running/finished/failed 三态）；`snapshot_ready` 时刷新待审批列表，渲染允许/拒绝按钮（复用 `/permissions/{id}/approve|deny`）。

### Step 3（✅ 已完成）：第三方 UI 集成契约
- 产出 `docs/workflows-integration-guide.md`：认证、建会话、SSE 事件、ui_card 卡片契约、审批按钮、知识库召回、工作流定制、最小伪代码示例、集成检查清单。外部 UI 可直接复用同一套剧本/知识库数据，或仅把 godex 当 agent 引擎自建交互层。

### Step 4（✅ 已完成）：对话内表单/按钮卡片
- **后端**：新增 `ui_card` 工具（`internal/tools/ui_card.go`，注册到 core_code bundle，DefaultActive）：`kind=form|button_group|card`，结构化输出 JSON；3 个测试覆盖合法 kind / 非法 kind / 表单字段回显。
- **前端**：Launch 页签监听 `tool_call_finished` 中 `name=ui_card` 的事件，解析 `output` JSON 渲染 `UiCardView`：
  - `form` → JSON Schema 风格表单（text/textarea/select/number，必填校验）
  - `button_group` → 按钮行
  - `card` → Markdown 卡片
  - 提交 = 把表单 JSON / 按钮值作为 follow_up 消息发回当前会话（文本即协议，agent 零协议改动）
- **数据流**：`tool_call_finished` 事件已携带 `output`（工具输出字符串），前端直接消费，无需协议层新事件。

## 5. 数据模型约定

| 实体 | 存储 | 约定 |
|---|---|---|
| Playbook（剧本） | notes API | `tags: ["workflow"]`，content 为 Markdown 流程（即 launch prompt）|
| Knowledge（知识库） | memory API | 记忆文件 `~/.godex/memory/*.md`，召回走 `/memory/context` |
| Launch 会话 | sessions API | locator `{channel:"web", key: uuid}`，queue_mode `steering` |

## 6. 设计取舍

- **零后端改动**（Step 1-2）：剧本直接复用 notes（天然支持 markdown + 本地文件 + 标签），知识库直接复用 memory，启动直接复用既有 agent 会话面。这是最快的产品化路径。
- **Step 4 用工具而非协议层新事件**：ui_card 通过 `tool_call_finished` 事件的 `output` 字段传递结构化 JSON（工具输出本身就在事件里），避免改 `protocol.Block` 和事件枚举——**侵入面最小**，且 TUI/CLI 能看到同一 JSON 不丢信息。
- **提交走 follow_up 文本**：卡片表单值/按钮动作作为 follow-up 消息发回会话（文本即协议），agent 端零改动即可理解；后续若要「按钮回执」等富语义可再加结构化 envelope。
- **与 Automation 板块的区别**：Automation 是定时/事件触发的 cron 任务编排；Workflows 是**手动引导式的按需问题解决**（人点选剧本 → 填参 → 跑 agent → 卡片/审批交互）。
