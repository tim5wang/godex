# GoDex 统一多入口交互与 Bubble Tea TUI 设计方案

> 状态：Plan（设计方案）

## 目标

GoDex 接下来不只是要把当前 `readline` REPL 升级成更好的 TUI，而是要一次性铺好这几类入口共用同一个后端：

- `readline` 交互终端
- 单次调用 `cli`
- 全屏 `tui`
- `web chat`
- `IM` 入口

目标不是“做 5 套聊天程序”，而是：

1. 只有一个 agent backend
2. 只有一套会话、上下文、任务、memory、teammate 运行时
3. 不同入口只是不同的 adapter 和 renderer
4. slash command、事件流、会话身份、工具执行语义保持一致

Bubble Tea TUI 在这个方案里依然重要，但它只是统一交互体系里的一个前端，而不是新的内核。

## 要解决的问题

当前系统已经从 `main.go` 中拆出了 `repl` 包，也已经有统一的 `Envelope` 雏形，但还远远没有达到“多入口共用后端”的状态。

现在的主要问题：

- `readline` 入口仍然直接读字符串，然后直接调用 `agent.Run`
- `agent.Run` 仍然直接往 stdout 打 assistant 文本和 tool 输出
- 没有统一的 session backend
- 没有统一的 runtime event stream
- 没有统一的 command dispatch
- `web / IM / tui / cli` 还没有真正意义上的 adapter 层

当前已有的基础：

- `repl/repl.go` 已经把交互层从 `main.go` 里抽出来
- `message.Envelope` 已经有 `SourceCLI / SourceWeb / SourceGateway` 等来源标识
- `agent / task / teammate / todo / insights / memory` 这些核心运行时已经存在

这意味着方向是对的，但还缺中间那一层“统一后端”。

## 设计目标

这次设计的核心目标有 6 个：

1. `gateway / IM / web / tui / readline / cli` 共享一套 backend API。
2. 会话身份统一，不因入口不同而切成多套历史。
3. 所有入口共享一套 slash command 和 runtime event 定义。
4. `Bubble Tea` TUI 成为一个正式前端，而不是绑死在 `agent` 输出上的终端皮肤。
5. 为后续附件、多模态、回放、协作和审计留接口。
6. 保留无头模式，保证测试、自动化和脚本调用不依赖 UI。

## 总体结构

推荐把系统拆成四层：

```text
+-------------------------------------------------------------+
| Frontends                                                   |
| readline | cli | Bubble Tea TUI | web chat | IM adapters    |
+--------------------------+----------------------------------+
                           |
                           v
+-------------------------------------------------------------+
| Unified Session Backend                                     |
| session manager | command service | runtime event bus       |
| request normalizer | attachment resolver | snapshot API     |
+--------------------------+----------------------------------+
                           |
                           v
+-------------------------------------------------------------+
| Agent Runtime                                               |
| agent | conversation | task | teammate | todo | memory      |
| skills | insights | tools | mcp                              |
+--------------------------+----------------------------------+
                           |
                           v
+-------------------------------------------------------------+
| Persistence / Delivery                                      |
| transcripts | todos | tasks | memory | inbox | websocket    |
| stdout sink | webhook replies | IM send APIs                |
+-------------------------------------------------------------+
```

这里真正要新增的是中间那层 `Unified Session Backend`。  
没有这层，前端越多，耦合越多。

## 统一后端的核心抽象

### 1. Session Backend

建议新增一个“会话后端”作为唯一入口。它不关心消息来自 TUI、网页还是 IM，只处理统一的请求。

建议接口形态：

```go
type Backend interface {
    OpenSession(ctx context.Context, req OpenSessionRequest) (*SessionHandle, error)
    Submit(ctx context.Context, sessionID string, input InboundEnvelope) error
    ExecuteCommand(ctx context.Context, sessionID string, cmd CommandRequest) (*CommandResult, error)
    Snapshot(ctx context.Context, sessionID string) (*SessionSnapshot, error)
    Subscribe(ctx context.Context, sessionID string, sink EventSink) error
}
```

这个 backend 负责：

- 会话创建与查找
- turn 串行化
- 调用 agent runtime
- 事件分发
- snapshot 查询
- command 统一处理

### 2. Session Identity

如果多个入口要共享后端，会话身份必须统一。

建议把 session identity 抽象成：

```go
type SessionLocator struct {
    WorkspaceID   string
    Channel       ChannelKind
    ExternalUser  string
    ExternalChat  string
    ExternalThread string
    SessionName   string
}
```

不同入口的映射建议：

- `readline`：默认本地 session，例如 `local/default`
- `cli`：默认短生命周期 session，也允许显式指定 `--session`
- `tui`：本地长会话，和 `readline` 可共享同一个 session
- `web`：`workspace + user + conversation_id`
- `IM`：`workspace + platform + chat_id + thread_ts`

关键原则：

- “同一条对话”在不同入口重连后仍能定位到同一 session
- session key 的生成必须由 backend 决定，而不是前端各自拼装

### 3. Inbound Envelope

现在的 `message.Envelope` 只有 `Content string`，这足够支撑文本，但对多入口统一仍然偏薄。

建议把它升级为统一入口协议：

```go
type InboundEnvelope struct {
    Source      EnvelopeSource
    SessionID   string
    Sender      string
    Timestamp   time.Time
    Text        string
    Parts       []ContentPart
    Attachments []AttachmentRef
    Metadata    map[string]string
}
```

其中：

- `Text` 保留，兼容当前纯文本路径
- `Parts` 为后续多模态/富文本预留
- `Attachments` 为 web / IM / TUI 上传文件预留
- `Metadata` 记录 channel 特有字段，例如 thread id、message id、mentions 等

这样 `readline` 和 `cli` 可以继续只填 `Text`，而 web / IM 则可以逐步带上附件。

### 4. Runtime Event

多入口共享后端的关键不在“输入统一”，而在“输出统一”。

当前最大阻碍是 `agent.Run` 直接打印 stdout。  
推荐把输出改成统一事件流。

建议事件模型：

```go
type RuntimeEvent struct {
    SessionID  string
    TurnID     string
    Type       EventType
    Timestamp  time.Time
    Payload    any
}
```

第一批事件建议包括：

- `UserMessageAccepted`
- `AssistantTextDelta`
- `AssistantMessageCompleted`
- `ToolCallStarted`
- `ToolCallFinished`
- `ToolOutputAvailable`
- `CommandCompleted`
- `TaskBoardChanged`
- `TodoListChanged`
- `TeamStateChanged`
- `InboxChanged`
- `InsightsReady`
- `WarningRaised`
- `ErrorRaised`
- `TurnCompleted`

每个前端只需要订阅这些事件，然后按自己的方式渲染：

- `readline`：打印文本
- `cli`：聚合后打印
- `tui`：增量渲染 viewport / tool cards
- `web`：转成 SSE / WebSocket event
- `IM`：转成平台消息回复、thread update 或卡片消息

### 5. Command Service

slash command 不应该散落在各前端各自 `switch`。

建议抽成统一命令服务：

```go
type CommandRequest struct {
    Name   string
    Args   map[string]string
    Raw    string
    Source ChannelKind
}
```

第一批共享命令：

- `/compact`
- `/tasks`
- `/team`
- `/inbox`
- `/todos`
- `/insights`
- `/clear`：reset current prompt state and transient tools
- `/focus`
- `/theme`
- `/help`

不同前端只做两件事：

1. 把用户输入解析成 `CommandRequest`
2. 把 `CommandResult` 渲染成自己平台适合的形式

## 建议的后端分层

### 1. Gateway / Adapter Layer

这一层是各种入口的适配器，不包含 agent 逻辑。

推荐职责：

- 读取用户输入
- 识别 channel identity
- 把输入转换成 `InboundEnvelope`
- 调 backend 的 `Submit / ExecuteCommand / Snapshot / Subscribe`
- 把后端事件转换成终端/WebSocket/IM 平台消息

建议按入口拆包：

- `gateway/readline`
- `gateway/cli`
- `gateway/tui`
- `gateway/web`
- `gateway/im`

注意：`tui` 也是 gateway，不是 runtime。

### 2. Session Orchestrator

这层是统一后端的核心。

建议职责：

- 保存 session registry
- 对每个 session 加锁，确保一次只跑一个 active turn
- 负责 turn lifecycle
- 管理 event sink 订阅者
- 提供 snapshot
- 统一 command dispatch

可理解为：

- frontend 是“进出站适配”
- orchestrator 是“会话控制器”
- agent 是“真正工作的人”

### 3. Agent Runtime

`agent` 继续是核心执行器，但要逐步从“终端程序”改造成“可嵌入 runtime”。

这意味着它需要：

- 不直接依赖 stdout/stderr
- 支持注入 `EventSink`
- 把 assistant 文本、tool 输出、warning、memory capture 都变成事件

最关键的改动点就在这里。

### 4. Persistence / Replay

为了让 web / IM / TUI 真正共享会话，还需要统一 snapshot 和回放能力。

建议每个 session 至少能拿到：

- transcript
- current tasks
- todos
- team state
- tool catalog
- active skills
- latest warnings/errors
- last active timestamp

建议抽象：

```go
type SessionSnapshot struct {
    SessionID     string
    Messages      []protocol.Message
    Tasks         []*task.FileTask
    Todos         []todo.Item
    Team          []*teammate.Teammate
    ActiveSkills  []string
    ToolCatalog   tools.ToolCatalog
    UpdatedAt     time.Time
}
```

## 各入口如何接统一后端

### 1. `readline`

定位：

- 兼容现有本地开发体验
- 最轻的交互壳
- 调试入口

行为：

- 读一行用户输入
- 统一送到 backend
- 订阅事件并按文本格式输出

它不再直接调用 `agent.AddMessage` 和 `agent.Run`，而是调用 backend。

### 2. `cli`

这里的 `cli` 指单次执行模式，而不是当前长对话 `readline`。

典型形态：

```bash
godex ask "请审查这段代码"
godex ask --session workbench "继续上次任务"
cat prompt.txt | godex ask --stdin
godex command /insights
```

定位：

- 脚本化
- CI
- automation
- 与 shell 管道集成

行为：

- 提交一条输入
- 阻塞等待本轮完成
- 打印聚合结果

### 3. `tui`

Bubble Tea TUI 是最复杂但最“像产品”的前端。  
它不拥有任何特殊后端逻辑，只是最丰富的 renderer。

TUI 通过 backend 拿到：

- transcript 增量事件
- tool 执行事件
- snapshot
- command 结果

推荐的 TUI 组件：

- `viewport`：对话区
- `textarea`：输入区
- `help`：快捷键条
- `list`：右侧状态面板和 command palette
- `lipgloss`：布局和样式

推荐布局：

- 顶部状态栏
- 左侧 transcript timeline
- 右侧 `Tasks / Todos / Team / Tools / Context` 面板
- 底部 composer

这和当前文档最初的 TUI 设想保持一致，但它现在被放进统一后端框架里了。

### 4. `web`

推荐采用：

- `HTTP` 提交消息
- `SSE` 或 `WebSocket` 订阅事件

接口建议：

- `POST /sessions`
- `GET /sessions/:id`
- `POST /sessions/:id/messages`：Web 入口异步接受消息并通过事件流跟进 turn 状态。
- `POST /sessions/:id/commands`
- `GET /sessions/:id/events`

如果优先简单实现：

- 先做 `HTTP + SSE`
- WebSocket 放到第二阶段

Web 前端关注：

- 会话列表
- transcript 回放
- token/tool/team/todo 状态面板
- reconnect 后继续订阅同一 session

### 5. `IM`

IM 不适合照搬 TUI，而适合“统一后端 + 轻量 renderer”。

推荐支持的 IM 基础语义：

- 文本消息 -> `InboundEnvelope`
- 线程回复 -> 同一 session
- slash command 或 bot command -> `CommandRequest`
- 文件上传 -> `AttachmentRef`

IM 渲染建议：

- assistant 回复用 thread reply
- 长工具输出做摘要 + “查看详情链接”
- 任务板 / insights 用卡片或摘要消息
- 事件流不做逐 token 刷屏，按 chunk 或阶段性消息聚合

因为 IM 平台各自限制很多，所以统一点不在 UI，而在：

- 输入归一化
- session 映射
- 命令语义
- 输出事件聚合

## 会话并发与一致性

多入口共享后端后，必须明确并发策略。

建议规则：

1. 一个 session 同时只允许一个 active turn。
2. 新输入进来时，如果当前 turn 正在运行：
   - 可以排队
   - 或者返回“busy / retry / interrupt”策略
3. Web、IM、TUI、readline 都遵守同一策略。

推荐 v1 先做：

- 每个 session 一个 mutex
- 新请求进入队列
- `interrupt` 留给后续高级模式

否则会出现：

- transcript 乱序
- task/todo 状态互相覆盖
- IM 和 TUI 同时发消息把同一 session 打乱

## 命令和能力的一致性

多入口统一后端还有一个好处：  
命令能力终于可以做到“一处定义，多处复用”。

建议把每个命令都定义成 metadata：

```go
type CommandSpec struct {
    Name        string
    Summary     string
    Args        []CommandArg
    VisibleIn   []ChannelKind
    Interactive bool
}
```

示例：

- `readline`：通过 `/` 文本触发
- `tui`：通过 command palette 或快捷键触发
- `web`：通过按钮或 slash command 输入框触发
- `IM`：通过 bot slash command 或命令消息触发
- `cli`：通过子命令触发

语义仍然是同一个 command backend。

## 统一后端对多模态的意义

虽然当前仓库还不支持原生多模态输入，但这次统一设计应该顺手把接口留出来。

原因很简单：

- `web` 很快会需要文件上传
- `IM` 天然会有图片、附件、转发消息
- `tui` 以后也可能有 file picker / attachment panel

所以这次不一定要立刻把多模态做完，但至少要把输入协议改成“能带附件”的结构，而不是继续把所有入口都绑定在 `Content string` 上。

## 代码落点建议

建议新增这些包或目录：

- `internal/services/backend`
- `internal/services/sessionadmin`
- `internal/domain/events`
- `internal/services/commands`
- `gateway/readline`
- `gateway/cli`
- `gateway/tui`
- `gateway/web`
- `gateway/im`

已有代码建议演进方向：

- `message/envelope.go`
  - 从纯文本 envelope 升级成通用 inbound envelope
- `internal/agent/agent.go`
  - 从 stdout 驱动改成 event sink 驱动
- `repl/repl.go`
  - 退化成 `gateway/readline` 的一个实现
- `main.go`
  - 只负责选入口和启动 backend

## 与 Bubble Tea TUI 的关系

这份设计不是把 TUI 降级，而是把它放回正确的位置。

在新的架构里：

- Bubble Tea TUI 仍然是高优先级入口
- 它会拥有最完整的交互体验
- 但它不再是后端耦合点

换句话说：

- 以前是“先有 agent，再有 readline，再想办法长 TUI”
- 现在应该是“先有统一 backend，再让 readline、cli、tui、web、IM 都接上来”

## 分阶段实施建议

### Phase 0：统一输入输出协议

先做：

- `Envelope` 升级
- `EventSink` 抽象
- `agent.Run` 去 stdout 化
- `CommandService` 基础骨架

这是后面所有入口的底座。

### Phase 1：统一 session backend

先做：

- session registry
- session lock / queue
- snapshot API
- subscribe API

这一步完成后，`readline` 和 `cli` 可以先接入。

### Phase 2：迁移现有 `readline`

目标：

- 现有 REPL 改成 backend client
- slash command 走统一 command service
- 输出改成消费 runtime events

### Phase 3：补 `cli`

目标：

- `godex ask`
- `godex command`
- `--session`
- `--stdin`

### Phase 4：实现 Bubble Tea TUI

目标：

- alt screen
- transcript viewport
- textarea composer
- sidebar
- command palette

### Phase 5：实现 `web chat`

目标：

- HTTP + SSE
- session list
- transcript replay
- reconnect

### Phase 6：实现 `IM`

目标：

- 一个平台先跑通
- thread -> session 映射
- command 映射
- 文件附件基础能力

## 验收标准

这套设计完成后，应该满足：

1. `readline / cli / tui / web / IM` 共享同一套 session backend。
2. 同一 session 可以在不同入口继续。
3. assistant/tool/runtime 输出都来自统一事件流。
4. slash command 只定义一套语义。
5. `agent` 不直接依赖任何一个具体 UI。
6. TUI 只是最丰富的 frontend，而不是新的 backend。

## 结论

现在最该做的不是直接并行开 `web / IM / tui` 三条线，而是先补上统一后端。

真正的优先级应该是：

1. `agent -> event sink`
2. `session backend`
3. `command service`
4. `readline / cli` 迁移
5. `Bubble Tea TUI`
6. `web`
7. `IM`

只要这条主线走对，后面新增入口时，成本会从“再做一个聊天程序”变成“再写一个 adapter”。
