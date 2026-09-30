# Godex 业务编排画布与 Session Workflow 使用指南

> 状态：Active / Partial（2026-09-29 补充 Voice Agent 首版使用与验收流程）；用途：供用户和 Flow Designer Agent 查阅画布操作、Agent 保存边界及 Session 能力限制。

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

## Voice Agent 快速开始与浏览器验收

首版 Voice Agent 是桌面 localhost 上的引导式 Voice Flow：Flow Designer 负责设计与校验，运行时只开放 Godex `Explore` 只读工具。用户显式开启/停止麦克风；服务端 VAD 自动分句；每个非空识别结果以独立 durable `voice.asr_final` 事件提交。

1. 在 Godex 仓库运行 `make dev-voice`。默认从相邻的 `../voice-engine` 启动 Zipformer；`VOICE_ENGINE_DIR` 和 `VOICE_ASR_MODEL` 可覆盖。模型不会自动下载；缺失时按终端提示执行 `./scripts/fetch-models.sh --asr zipformer` 后重试。
2. 在 Settings 启用 Voice Chat，打开「业务编排」并选择一个 Flow，进入「Voice Agent」页。先确认状态为就绪；未启用、引擎未启动或默认 ASR/VAD/TTS 缺失时，按页面提示处理。
3. 输入业务目标并选择「让 Flow Designer 按需求创建」。设计要求会预填到自然语言面板；检查画布与校验结果后，再明确要求 Agent 保存草稿。也可以确认创建最小 Voice Agent 示例草稿直接体验。
4. 在 Voice Agent 页明确确认发布一个草稿版本，然后创建绑定该版本的 FlowSession。
5. 点击麦克风按钮并允许浏览器访问麦克风。说一句并等回复完成，再继续说第二句；每句都应独立进入 FlowSession 并得到语音回复。回复播放时再次说话，应立即停止旧播报并开始新 turn；点击停止后浏览器录音指示应消失，麦克风轨道和音频上下文会关闭。
6. 手工检查异常提示：拒绝麦克风权限后按提示在站点设置中放行；关闭 voice-engine 后页面应显示断连与重启提示；模型缺失时 `make dev-voice` 应给出下载命令，不应自行下载。

确定性工具调用、Flow 输出、TTS 与插话取消由模拟引擎 E2E 覆盖。可选真实模型测试要求本地 engine 地址和 16 kHz、单声道 PCM WAV 样本：

```sh
GODEX_REAL_VOICE_ENGINE_ADDR=127.0.0.1:17021 \
GODEX_REAL_VOICE_SAMPLE_WAV=/path/to/sample-16k-mono.wav \
go test ./internal/runtime/httpapi -run TestVoiceFlowSessionAdapterWithRealVoiceEngine -count=1
```

## 配置 Session Workflow

在 Flow 详情的「Session 配置」页选择 `request` 或 `session`。该页会用当前画布（包含未保存编辑）创建一个新的草稿版本。

选择 `session` 后至少配置一个触发器：

- `event_type`：稳定的业务事件名，例如 `voice.asr.final` 或 `game.snapshot`。
- `entry_node`：该事件触发的区域入口节点。
- `delivery=durable`：写入事件 journal、按 session 序号处理，适合不能合并的事件和 Agent step。
- `delivery=latest_wins`：同类状态信号可覆盖合并，适合可替代的最新状态；不能用来逐帧传媒体。
- `lane_id`：可选，将触发器绑定到某个执行 lane；不绑定时走默认路径。

Lane 可设置 `event`、`periodic` 或 `hybrid` cadence，执行期限、`fast`/`standard`/`slow` worker class 和过载策略。周期/混合 lane 必须使用 `latest_wins`；Agent step 需要 `durable`。周期值以毫秒配置。保存后创建的是草稿版本，发布状态不会自动改变。

### Branch 路由与汇合

Session branch 每次只选择一个 case 或 default 路径。不同路径可以连接到同一个下游节点形成**互斥汇合（OR-join）**：运行时只执行选中的路径和共享汇合节点，汇合节点只执行一次；其他路径视为跳过，不会产生 outputs。汇合节点应使用 session state 或所选路径已有的 outputs，不要读取未选路径的节点 outputs。一个 branch route 内继续嵌套 branch 目前不支持。

## 创建与观测 Session

在「Session 运行」页选择一个已保存的 Session 版本，填写 JSON inputs 并创建 Session。建议先发布后再把版本用于正式场景；草稿可用于受控测试（具体限制以服务端响应为准）。

选中 Session 后可以：

- 查看状态、事件序号、状态版本、最近一次执行及节点耗时、在途 lane、最近错误和状态快照。
- 暂停、恢复、正常结束或取消 Session。
- 按已配置触发器手动发送测试事件/信号；可填写 source、source sequence、correlation ID 和 JSON payload。
- durable 触发器发事件；latest-wins 触发器发可合并信号。

普通 Session Web 页是管理和语义事件测试面板；Business Flow 内的 Voice Agent 页另外提供实时语音控制。Voice Agent 支持服务端连续监听与自动分句，不支持通用的音频/视频帧编排。

## WebSocket、媒体 Adapter 与重连

后端 Session WebSocket 是**语义事件适配器**：支持 durable event、latest-wins signal、ping/pong、journal event 推送和 Session 快照。客户端断线不会暂停或结束 Session。重连时客户端携带自己的 `after_sequence` 拉取 journal；投递语义为 at-least-once，消费端需要通过 sequence、source sequence 或 output ID 去重。它没有服务端 consumer ACK。

Voice adapter 可把 PCM 经 voice-engine 热路径处理，并将 ASR final 作为语义事件交给绑定的 FlowSession。重连重发时应复用稳定的 `(source, source_sequence)`，避免“服务端已写入但客户端没收到回执”造成重复事件。原始 PCM 不经通用 Session journal 或通用 Session WebSocket 传输。

Web UI 当前没有 WebSocket 连接器/媒体控制台；实时客户端需使用相应服务端接口。面板里的手动发送按钮只用于语义事件联调。

## 当前支持边界

Session region 当前支持内联 JavaScript `function`、HTTP JSON `service`、纯 `llm`、durable Agent `step` 和确定性 `branch`。不能据此宣称 C/D 全链路已完成：

- 不支持在通用事件 journal 或 Session WebSocket 中传二进制音视频帧；没有 token/partial ASR/TTS 分帧输出。
- 不支持 Session loop、嵌套 branch、一般化的持久状态机迁移；branch 只支持互斥路由汇合，不支持等待多个并行分支的 join。
- Voice Agent 首版覆盖单一 durable 语音入口和只读 Explore Agent，带有 turn generation fencing、插话取消和 TTS 取消；实时游戏的多速率、多层端到端 Agent 仍未闭环。
- 运行时协调仍是单进程；durable event 的外部副作用按 at-least-once 处理，不是 exactly-once；输出也没有服务端消费确认。

因此，当前 Voice Agent 可用于本地连续语音对话、只读工具调用、FlowSession 状态保留、TTS 播报和插话取消；生产多进程部署、通用实时媒体编排和游戏 Agent 仍需依据运行时设计文档逐项实现与验收。
