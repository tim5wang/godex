# Godex 业务编排画布与 Session Workflow 使用指南

> 状态：Active / Partial（2026-09-27 按代码核对）；用途：供用户和 Flow Designer Agent 查阅画布操作、Agent 保存边界及 Session 能力限制。

本指南同时供用户和 Flow Designer Agent 使用。它说明 Web 画布的操作方式、Agent 的设计/保存边界，以及当前 Session Workflow 能做和不能做的事。完整字段定义与运行时约束见 [业务流程运行时设计](business-flow-runtime-design.md)。

## 先选对执行模式

- `request`（默认，`FlowRun`）：一次独立请求执行一个有限流程。适用于 AIGC 调度和 B 类在线 HTTP/JSON 服务编排。音频/视频作为一次请求输入即可，不要把普通媒体请求拆成 64 ms 帧。
- `session`（`FlowSession`）：长时间存在、由事件持续驱动并保留状态的流程。适用于 C/D 的状态与事件处理骨架，例如语音会话状态更新或游戏状态快照决策。Session 创建后固定绑定一个 Flow 版本。

`session` 不是“无限运行的 FlowRun”。连接（例如 WebSocket）是可断开、可重连的输入/输出适配器；FlowSession 本身拥有版本、状态、事件序号和生命周期。

## Web 画布基本操作

1. 打开「业务编排」，从左侧选择 Flow；若还没有 Flow，先创建一个基本 Flow。
2. 在画布工具栏添加节点。选择节点后在属性面板填写节点参数；从一个节点的输出端口拖到另一个节点的输入端口创建依赖边。可用自动布局整理拓扑。
3. 通过画布编辑节点和连线；「变量」页查看输入、输出和节点输出引用关系；「定义」页检查或编辑完整 JSON。画布保存会生成新的草稿版本，不覆盖既有版本。
4. 用「调试」选择版本和测试输入，可启动或单步运行；「运行记录」查看输入、输出、事件和失败诊断；「接入生产」用于配置发布版本和业务 Key。运行或上线前应先校验、保存并按需要发布。

节点大致分工：`step`/`llm` 处理模型任务，`decision` 产出决策，`branch` 根据决策路由，`function` 执行本地 JS/WASM 函数，`service` 发起一个 HTTP(S) JSON 请求，`human` 等待人工处理。具体字段、条件边和变量引用规则以 Flow Spec 与运行时设计文档为准。

## 使用 Flow Designer Agent

「自然语言」页是绑定当前 Flow 的持久设计会话。它会把当前画布定义（含尚未保存的改动）作为隐藏的请求上下文传给 Agent，不会把整份 JSON 展开成用户聊天消息。快照只用于当前轮，下一轮会替换或清除旧快照；最大 64 KiB。

推荐闭环：

1. 描述目标、输入输出以及运行模式；如果是长会话，再说明事件类型、状态和不同频率的处理需求。
2. Agent 使用 `flow_design` 生成或修改完整定义，并校验节点、连线、触发器和 lane；优先以当前画布快照为基准。
3. Agent 先总结改动并等待明确确认。确认前不应调用 `create_flow`；确认后才保存为新的草稿版本。
4. 用户可在画布、JSON 或变量页复查新版本，再进行发布或测试。

Agent 可通过 `godex_docs get flow-spec` 查询能力索引和文档路径，再用只读 `read_file` 阅读本指南及完整运行时设计。若某项能力未列入已支持范围，Agent 应说明限制，而不是虚构节点或配置字段。

## 配置 Session Workflow

在 Flow 详情的「Session 配置」页选择 `request` 或 `session`。该页会用当前画布（包含未保存编辑）创建一个新的草稿版本。

选择 `session` 后至少配置一个触发器：

- `event_type`：稳定的业务事件名，例如 `voice.asr.final` 或 `game.snapshot`。
- `entry_node`：该事件触发的区域入口节点。
- `delivery=durable`：写入事件 journal、按 session 序号处理，适合不能合并的事件和 Agent step。
- `delivery=latest_wins`：同类状态信号可覆盖合并，适合可替代的最新状态；不能用来逐帧传媒体。
- `lane_id`：可选，将触发器绑定到某个执行 lane；不绑定时走默认路径。

Lane 可设置 `event`、`periodic` 或 `hybrid` cadence，执行期限、`fast`/`standard`/`slow` worker class 和过载策略。周期/混合 lane 必须使用 `latest_wins`；Agent step 需要 `durable`。周期值以毫秒配置。保存后创建的是草稿版本，发布状态不会自动改变。

## 创建与观测 Session

在「Session 运行」页选择一个已保存的 Session 版本，填写 JSON inputs 并创建 Session。建议先发布后再把版本用于正式场景；草稿可用于受控测试（具体限制以服务端响应为准）。

选中 Session 后可以：

- 查看状态、事件序号、状态版本、最近一次执行及节点耗时、在途 lane、最近错误和状态快照。
- 暂停、恢复、正常结束或取消 Session。
- 按已配置触发器手动发送测试事件/信号；可填写 source、source sequence、correlation ID 和 JSON payload。
- durable 触发器发事件；latest-wins 触发器发可合并信号。

Web 页目前是管理和语义事件测试面板，不是实时音频/视频客户端。详情与事件列表通过轮询刷新。

## WebSocket、媒体 Adapter 与重连

后端 Session WebSocket 是**语义事件适配器**：支持 durable event、latest-wins signal、ping/pong、journal event 推送和 Session 快照。客户端断线不会暂停或结束 Session。重连时客户端携带自己的 `after_sequence` 拉取 journal；投递语义为 at-least-once，消费端需要通过 sequence、source sequence 或 output ID 去重。它没有服务端 consumer ACK。

Voice adapter 可把 PCM 经 voice-engine 热路径处理，并将 ASR final 作为语义事件交给绑定的 FlowSession。重连重发时应复用稳定的 `(source, source_sequence)`，避免“服务端已写入但客户端没收到回执”造成重复事件。原始 PCM 不经通用 Session journal 或通用 Session WebSocket 传输。

Web UI 当前没有 WebSocket 连接器/媒体控制台；实时客户端需使用相应服务端接口。面板里的手动发送按钮只用于语义事件联调。

## 当前支持边界

Session region 当前支持内联 JavaScript `function`、HTTP JSON `service`、纯 `llm`、durable Agent `step` 和确定性 `branch`。不能据此宣称 C/D 全链路已完成：

- 不支持在通用事件 journal 或 Session WebSocket 中传二进制音视频帧；没有 token/partial ASR/TTS 分帧输出。
- 不支持 Session loop、嵌套或汇合 branch、一般化的持久状态机迁移。
- Voice adapter 能把 ASR final 接入 FlowSession，但不自动实现完整语音 Agent 决策、打断和 TTS 闭环；实时游戏的多层端到端 Agent 也未闭环。
- 运行时协调仍是单进程；durable event 的外部副作用按 at-least-once 处理，不是 exactly-once；输出也没有服务端消费确认。

因此，当前可用于构建、测试和观测 Session 语义流程切片；正式接入实时媒体前，应由专用 adapter 负责媒体热路径、背压、取消和客户端重连，并依据运行时设计文档逐项验收。
