# GoDex 插件能力扩展设计：turn 中间件 + UI 插槽 + 语音后端 L2

> 状态：Active / Partial（`ui_card` 与现有 voice-engine WebSocket/TTS baseline 已落地；turn middleware、插件 config/UI 声明、OpenAI REST plugin 与 Realtime adapter 仍 Planned） · 日期：2026-08-27 · 核对：2026-08-31
> 目标：以 plugin 方式提升 godex 灵活性，落地三块能力：
>   A. 插件可捕获的 **turn 级中间件**（用户输入 → LLM → 回复全链路钩子）
>   B. **UI 插槽**：插件声明 settings 配置项 + 注册聊天内 `ui_card`
>   C. **语音后端 L2**：OpenAI 兼容 REST（先）→ Realtime（后续）

## 当前实现快照

| 设计项 | 状态 | 当前事实与边界 |
|---|---|---|
| A turn middleware | Planned | 工具级 interceptor、runtime events 与 harness input 已有，但没有本文定义的可逆 `TurnMiddleware` contributor 链。 |
| B1 plugin settings schema | Planned | Settings 本身是 schema 驱动；plugin manifest `config:` 聚合、CredentialBroker 授权读取尚未落地。 |
| B2 `ui_card` | Implemented baseline | `tools.NewUICardTool`、MessageFeed 解析、`UiCardView`、Agent Step reply 闭环已有；但 manifest `ui:` / `/plugin-ui` / 动态 PluginCardSlot 未实现。 |
| C 现有语音主链 | Implemented baseline | `/v1/voice` WebSocket bridge、`/v1/tts`、`/v1/tts/stream`、VoiceBar/TTS playback 与 mock-engine tests 已落地。 |
| C OpenAI REST / Realtime plugin | Planned | 当前依赖 voice-engine 协议，不等于本文的 `internal/speech`、OpenAI audio REST adapter 或 OpenAI Realtime adapter。 |

因此，“基础语音和 ui_card 已有”与“插件化 L2 全部完成”必须分开陈述。下文草案只对 Planned 部分表达目标接口。

## 0. 调研结论（dsh / pi → godex 借鉴）

### 0.1 dsh（deepseek-harness）
- 口号 "Everything is a Plugin"，基于 Cordis（`@deepseek-ai/cordis`），插件 = **TS/JS 模块**跑在 Node Cordis Loader（plugin-loader/include/group/hmr），**不是 WASM**。
- 核心机制：`ctx.on`（事件订阅）/ `ctx.provide`（能力提供）/ **middleware（拦截链）** / `ctx.get`（依赖注入）；依赖驱动生命周期（缺依赖保持 PENDING）；可逆注册（卸载撤销全部 effect）；事务热更新。
- **关键结论**：dsh 官方能力边界表明确「React UI 插件 → 后端无法解决，需独立前端协议」——UI 插槽对所有后端插件系统都是独立课题。
- godex 已落地其大部分借鉴点：`pluginrt`（manifest/graph/instance/effects/registry/manager + NativeToolPlugin）、`toolruntime`（owner/generation/disposer + before/after interceptor）、`wasmrt`（tools/prompts/policy + 受控 host）、MCP stdio、ACPHarness。

### 0.2 pi（pi coding agent）
- 指向 `mariozechner/pi-coding-agent`，通过 **pi-acp**（ACP Agent Client Protocol，JSON-RPC 2.0 over stdio）供 Zed 等编辑器使用——走 **ACP 外部 agent 委派**路线，**无独立插件切面体系**可抄。
- godex 已有 `ACPHarness` 承接（本地 DSH 文档建议：先 ACP 委派 ✅ → 再封装 PiHarness 接管完整 Turn ⏳）。

### 0.3 godex 现状差距（对照表）

| 诉求 | godex 现状 | 差距 |
|---|---|---|
| turn 级中间件 | 仅**工具级** before/after 拦截器；`events.Sink` 是 UI/Timeline 广播，**非内部 middleware 总线** | 无 turn 级中间件链（用户输入→LLM→回复） |
| session 管理切面 | 无会话生命周期钩子 | 无 onSessionCreate/Switch/End（列为后续） |
| ctx 可访问内容 | `HarnessTurnInput`（messages/workspace/usage/scope）已定型 | 插件无统一 ctx 访问面（DSH 有 ctx.get/provide） |
| UI 插槽 | `ui_card` 通用渲染/回传已落地；settings 是 schema 驱动 | 无 manifest `ui:`/`config:` 聚合与动态插件槽 |
| 语音 L2 | voice-engine WebSocket + REST/streaming TTS baseline 已落地 | godex 侧仍无 `internal/speech` 与 OpenAI-compatible provider plugin |

## 1. 范围（已与用户对齐）

| 项 | 对齐结果 |
|---|---|
| A. 切面 | **turn 级中间件**优先（用户输入 → LLM → 回复全链路钩子）；session 钩子/消息级钩子列入路线图后续 |
| B. UI | **settings 配置扩展**（最便宜先做）+ **插件注册 ui_card**（聊天内卡片）；App 级面板（dock 级）列为后续 |
| C. 语音 | **先 OpenAI 兼容 REST**（可配 base_url：Whisper ASR + TTS）→ **Realtime 后续阶段** |

## 2. 设计 A：turn 级中间件（Planned）

### 2.1 目标
让插件在 **agent turn 执行链路**上获得拦截/修改能力，这是当前工具级拦截器覆盖不到的层级（工具拦截器只能看到单次工具调用，看不到「用户说了什么 → LLM 回了什么」的整体回合）。

### 2.2 中间件链（挂载点）

```text
用户输入(消息/附件) ──▶ [Before 中间件链] ──▶ LLM 调用 ──▶ [After 中间件链] ──▶ 回复入库/下发
                              │                                                        │
                         可 modify/deny/replace                                 可 modify/追加副作用
                              ▼                                                        ▼
                       工具调用（复用现有 toolruntime before/after 拦截器，不重复造）
```

- **挂载点**：`agent.RunTurn` 前后（`internal/agent/runtime.go` 的 harness 分支处插入）。
- **显式决策语义**：沿用 wasmrt policy 的 `{"action":"continue"|"deny"|"replace"|"modify"}`，不引入 DSH 的 waterfall next()（本地 DSH 文档 §4 明确首版用显式决策更安全）。
- **可逆注册**：走 pluginrt effects ledger（owner 可逆，卸载撤销），与 toolruntime 的 `AddBeforeInterceptorsOwned` 同构。

### 2.3 接口草案（新增 `internal/agent/turnmiddleware.go`）

```go
// TurnInput 是 turn 级中间件的输入面：复用 HarnessTurnInput 已定型的
// 消息/workspace/usage/scope，作为插件可访问的 ctx 内容（加强 ctx 可访问性）。
type TurnInput struct {
    Messages  func() []protocol.Message // 消息快照（提供者，不持有全量）
    Workspace string
    Scope     scope.Id
    Usage     *UsageContext
    UserText  string // 当前用户输入文本（中间件可改写）
}

// TurnReply 是 LLM 回复面。
type TurnReply struct {
    Text    string
    Updates []protocol.Update // 结构化更新（text delta / tool / usage 等）
}

// TurnMiddleware 是插件可实现的 turn 级钩子接口。
type TurnMiddleware interface {
    // Before 在用户输入进入 turn 前调用；可修改输入、拒绝或替换。
    Before(ctx context.Context, in *TurnInput) (*TurnAction, error)
    // After 在 LLM 回复生成后调用；可修改回复或触发副作用。
    After(ctx context.Context, in *TurnInput, reply *TurnReply) (*TurnAction, error)
}

// TurnAction 是显式决策结果（continue/deny/replace/modify）。
type TurnAction struct {
    Action string // "continue" | "deny" | "replace" | "modify"
    Input  *TurnInput
    Reply  *TurnReply
    Error  *ActionError
}
```

### 2.4 插件接入点
- **原生插件（首选）**：pluginrt 新增 `TurnMiddlewareContributor`（仿 `ToolContributor`），插件在 Start 时注册中间件，Stop 时 effects 撤销。
- **WASM 插件（L3 远期）**：wasmrt 扩展 `godex_turn_before` / `godex_turn_after` host calls（受控），复用显式决策 JSON。
- **ctx 增强**：`TurnInput` 即统一 ctx 访问面；后续可扩展（session 状态、记忆快照等按需增加字段）。

### 2.5 不做（列入路线图）
- session 生命周期钩子（onSessionCreate/Switch/End）——依赖 turn 中间件先立住，再补会话级钩子。
- 消息级钩子（每条消息收发）——turn 中间件的 Before/After 已覆盖主要诉求，消息级可后续按需补。

## 3. 设计 B：UI 插槽（settings 配置扩展 + ui_card）

### 3.1 结论
**前端独立协议**（借鉴 dsh 结论）：后端插件声明 UI 契约（JSON），前端渲染层拉取并渲染；不要求插件提供 React 组件代码（那需要前端沙箱，最重）。

### 3.2 B1：settings 配置扩展（Planned）
- **插件 manifest 声明配置项**：新增 `config:` 段，复用现有 `FieldSchema` 结构（path/label/description/type/options/secret）。
  ```yaml
  # godex.package.yaml / plugin manifest
  config:
    - path: plugins.my-voice.api_key
      label: API Key
      type: string
      secret: true
    - path: plugins.my-voice.base_url
      label: Base URL
      type: string
      default: https://api.openai.com/v1
  ```
- **后端聚合**：pluginrt 收集活跃插件的 config 声明，合并进 `/config/schema`（现有 schema 驱动渲染）。
- **前端零改动**：SettingsPage 的 ConfigSectionFields 自动渲染新 section（schema 驱动已具备）。
- **密钥**：secret 字段走 CredentialBroker（插件授权后读取），不进配置明文。

### 3.3 B2：ui_card（baseline 已实现；插件声明层 Planned）
- **插件注册卡片**：manifest 声明 `ui:` 段（card id + 渲染契约）。
  ```yaml
  ui:
    cards:
      - id: my-voice-status
        title: My Voice Status
        # 渲染契约：markdown | form | button_group（复用现有 ui_card 语义）
        type: markdown
        # 卡片数据来源：插件 tool 或专用 endpoint
        data_from: plugin_tool
  ```
- **前端 PluginCardSlot**：消息流新增渲染插槽，拉取 `/plugin-ui/cards` 契约，按 type 渲染（markdown/form/button_group 都有现成渲染组件）。
- **交互回传**：卡片按钮/表单提交 → 后端路由到插件（tool/invoke 或专用 endpoint）。
- **复用现有 ui_card JSON 契约**：交互卡片语义已存在于前端（`ui_card` 工具的 form/button_group），插件卡片沿用，前端渲染成本低。

### 3.4 落地步骤
1. 后端：pluginrt manifest 增加 `config:` / `ui:` 声明解析 + `/plugin-ui` 聚合 API（cards + config schema）。
2. 前端：SettingsPage 渲染插件配置（schema 驱动，基本零改）；消息流新增 PluginCardSlot。
3. App 级面板（dock/面板级插件）列为后续（最重，不先做）。

## 4. 设计 C：语音后端 L2（现有 voice-engine baseline 已实现；OpenAI adapter Planned）

### 4.1 能力契约（沿用既有语音插件设计）
```go
// pluginrt 能力注册表新增 voice 命名空间
Provides: []string{"voice:asr@1", "voice:tts@1"}
Requires: []string{"godex:config@1"} // 读 base_url / api_key（经 CredentialBroker）
```

### 4.2 internal/speech 接口（新增包）
```go
package speech

// Backend 是语音后端统一接口。第三方插件实现并注册到 pluginrt。
type Backend interface {
    ASR() ASR
    TTS() TTS
}

// ASR：流式音频输入 → 识别文本事件（复用 voice-engine 事件语义 asr_partial/final/end）
type ASR interface {
    Transcribe(ctx context.Context, audio []float32, sampleRate int) (string, error) // 整段
    // 流式版本后续：Start(ctx, sr) (ASRSession, error)；Accept/End/Close
}

type TTS interface {
    Synthesize(ctx context.Context, text string) ([]byte, error)             // 一次性 WAV
    SynthesizeStream(ctx context.Context, text string) (<-chan []byte, error) // 流式 PCM（后续）
}
```

### 4.3 OpenAI 兼容 REST 适配器（先做）
- **ASR**：`POST {base_url}/audio/transcriptions`（Whisper 兼容；base_url 可配 → Azure / 本地 OpenAI 兼容服务 / 第三方代理）。
- **TTS**：`POST {base_url}/audio/speech`（可配 voice/model；模型默认 `gpt-4o-mini-tts` 或用户指定）。
- **音频格式**：ASR 上行 16k s16 WAV（复用现有 VoiceBar 上行）；TTS 下行 24k PCM（复用现有下行播放语义）。
- **voiceBridge 改造**：注册了语音后端插件 → 用插件（适配器内部 HTTP 调 OpenAI 兼容端点）；未注册 → 回退现有 voice-engine 桥（**默认行为不变**）。

### 4.4 Realtime（后续阶段）
- OpenAI Realtime API（WS 双向流式实时对话）作为第二阶段。
- 前置：wasmrt 扩展 `godex_http_post` / `godex_ws_connect` 受控 host calls（走 policy allow/deny + 超时），或继续原生 Go 插件（推荐，无沙箱限制）。
- 插件只调供应商 API、不加载模型（与你的诉求一致）。

## 5. 路线图与工作量（估算）

| 阶段 | 内容 | 工作量 |
|---|---|---|
| P1 ⬜ | turn 级中间件（原生）：turnmiddleware 包 + agent 链路插入 + pluginrt TurnMiddlewareContributor | 2–3 人日 |
| P2 ⬜ | settings 配置扩展：manifest `config:` 解析 + /config/schema 聚合 | 1 人日 |
| P3 🟡 | `ui_card` 渲染/回传 baseline ✅；manifest `ui:` + /plugin-ui + PluginCardSlot ⬜ | 2–3 人日 |
| P4 🟡 | voice-engine bridge、TTS/stream baseline ✅；`internal/speech` + voice capability plugin ⬜ | 1–2 人日 |
| P5 ⬜ | OpenAI REST 适配器（Whisper ASR + TTS）+ voiceBridge provider selection | 1–2 人日 |
| P6 ⬜ | OpenAI Realtime adapter（不能用现有 voice-engine WS 代替验收） | 2–3 人日 |
| P7 | （可选远期）wasmrt 网络 host 扩展 godex_http_post/ws + WASM 适配器 demo | 2–3 人日 |

## 6. 验收标准（可验证）

1. **turn 中间件**：注册插件后，用户输入/LLM 回复可被拦截修改（modify/deny/replace 均验证）；插件卸载后效果可逆、行为复原。
2. **settings**：插件声明的配置项自动出现在设置页对应 section；secret 字段经 CredentialBroker 授权读取，不出明文。
3. **ui_card**：插件注册的卡片在消息流渲染（markdown/form/button_group），按钮/表单交互回传插件。
4. **语音**：注册 OpenAI 兼容后端插件后 VoiceBar 可用（Whisper ASR + TTS，base_url 可配）；未注册时回退 voice-engine 桥，行为与现状完全一致。
5. **无回归**：现有语音链路（voice-engine 桥）、设置页渲染、聊天交互均不变。
6. **安全性**：新增 host/API 面受 manifest 授权约束（config 声明 + CredentialBroker + policy）。

## 7. 关联文档
- 前置调研：`research_of_dsh_for_godex_optimize.md`（DSH 架构、pluginrt/wasmrt 落地对照）
- 语音链路现状：`docs/voice-plugin-extensibility-design.md` 历史版本（语音 L1-L3 可行性）
- 既有能力：`extension-runtime-user-guide.md`（Package/MCP/ACP/WASM 扩展）
