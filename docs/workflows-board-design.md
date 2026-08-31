# Workflows 板块设计文档（产品化）

> 日期：2026-08-24 ｜ 状态：Superseded（Workflows 页面已在 `c9612c1` 删除并由 Business Agents 取代；仅保留 `UiCardView` 复用组件）
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

## 7. 与 Codex Harness 开源的对比（2026-08-24 参考）

> 参考文章：《震撼！OpenAI 全面开源 Codex Harness，这场「反套壳」的革命正式打响》（微信公众平台）
> 核心主张：OpenAI 把驱动 Codex 的底层 Agent 执行层（Harness）完整开源——线程/上下文/工具执行/MCP/权限审批一整套机制，让开发者把 AI 从「聊天框」里解放出来，嵌入自己的业务软件、看板、工作台（界面/数据/审批都在业务系统，AI 只在底层干活）。

### 7.1 理念同源（我们已对齐的部分）

| 文章主张 | Workflows 对应实现 |
|---|---|
| Harness =「大模型想，Harness 让它持续工作」：线程/上下文/工具/审批整套机制 | godex 的 sessions/messages/events/permissions 即这套 Harness，Workflows 建立其上 |
| 双向事件流驱动线程、接收过程更新（App Server） | Launch 的 `streamEvents`（SSE `?replay=active`）+ `assistant_text_delta` 流式渲染 |
| 高风险动作主动停下、人工审批 | Launch 的 `pending_permissions` + 允许/拒绝按钮（`/permissions/{id}/approve\|deny`） |
| 找一条重复、输入输出清楚的流程写下来 | 剧本（notes tag=workflow），Markdown 流程即启动提示词 |
| 知识库准确与否是真正难的部分 | Knowledge 页签的 `/memory/context` 召回预览（L0/Core/Relevant 三层） |
| Agent 要知道能调什么、不能随便访问系统 | godex 的 bundle/scope 工具权限 + 写路径拦截器 |
| 把执行过程实时告诉人 | 工具活动 chips（running/finished/failed）+ 流式文本 |

### 7.2 差异与缺口（后续 Step 5+ 方向）

1. **方向相反**：文章是让 Harness **嵌入第三方业务系统**（ERP/看板/CRM），Workflows 目前是 godex 内部板块（内向）；Step 3 集成指南是"外向"契约但只是文档 + 参考实现，未到"嵌入任意第三方系统"的产品形态。
2. **我们做了文章没讲的**：agent 主动产出表单/按钮（`ui_card` 工具）——文章只讲了审批暂停，交互停在"确认/拒绝"；`ui_card` 的 form/button_group/card + follow_up 回传是增量（正好对应「更易用的表单/按钮对话」诉求）。
3. **文章五件事里 godex 有但 Workflows 板块没暴露**：任务进度记忆（session/turn/compaction 持久化）、失败后继续/暂停/复盘（retry/resume/fork/timeline）godex 后端都有，但 Workflows 板块只提供「在聊天中打开」跳转，未内嵌展示。
4. **文章的「AI 工号卡」六问，剧本模板没覆盖**：服务谁 / 可读什么 / 可调什么 / 不能做什么 / 交付什么 / 谁验收——当前模板只有「目标/步骤/输出」，缺权限边界、不可做项、验收人显式字段。

## 8. 后续实施方向（Step 5+，待实施）

> 目标：把 Workflows 从「godex 内部板块」演进为「可嵌入业务工作台的 agent 交互层」，并补齐与 Codex Harness 对齐的产品能力。

### Step 5（✅ 已完成）：剧本 schema 补「AI 工号卡」六问
- 剧本（note）增加结构化元数据：`service_whom`（服务谁）/ `can_read`（可读什么）/ `can_call`（可调什么）/ `cannot_do`（不能做什么）/ `deliverable`（交付什么）/ `reviewer`（谁验收）。
- **存储**：六问序列化为 content 的 YAML front-matter（`---\n...\n---`），零后端改动；`yaml` 库（已在依赖中）负责序列化/解析。
- 编辑器 Modal 增加六个字段；「从模板新建」回填；保存时写入 front-matter，编辑时解析回填（`parseCardFrontMatter` 剥离 front-matter 只留正文）。
- 启动时把这些字段注入 prompt（`buildCardSixSection` → 「AI 工号卡（边界与验收）」段落）。
- 模板库 4 个模板均补六问示例。
- 工具权限联动（bundle/scope）留作后续：六问的 can_read/can_call/cannot_do 目前以 prompt 约束为主，未做硬权限映射。

### Step 6（✅ 已完成）：Workflows 内嵌过程复盘
- Launch 失败结果卡片提供 **retry / resume** 按钮（复用 `retrySessionTurn` / `resumeSessionTurn`）。
- 运行历史记录增加 `turnId` / `durationMs` / `tools`（工具活动快照）；卡片展示耗时（`formatDuration`）+ 工具摘要 chips（最多 8 个 + 计数）。
- 运行历史仍存 localStorage（`godex:workflows:run-history`，上限 20 条）。

### Step 7（✅ 已完成）：「嵌入第三方」产品化
- **组件化**：抽出 `UiCardView` 到 `ui/web/src/features/workflows/components/UiCardView.tsx`（纯展示组件，`onSubmitCard` 回调 + 可选 `labels` 默认英文，不绑定 godex i18n）；barrel export `index.ts` 导出 `UiCardData`/`UiCardField`/`UiCardAction`。
- **可运行嵌入模板**：`examples/embed-ui/README.md` —— 最小嵌入式 UI 骨架（建会话/发消息/SSE/卡片/审批接线），第三方 UI 可 drop 进自己的业务页面。
- **可选（未做）**：App Server 风格的结构化 envelope（替代"文本即协议"的 follow_up）。当前 ui_card 提交走 follow_up 文本已可用，富语义回执留作后续。

### 定位叙事
- 文章：ToB 商业叙事（"AI 员工"），强调"模型不值钱、业务上下文和交付标准值钱"。
- godex：开源 dev tool，Workflows 是工程化落地——不推"员工"概念，而是给开发者一套可复用的板块 + 集成契约；Step 5-7 对齐的是文章的技术主张，而非商业叙事。
