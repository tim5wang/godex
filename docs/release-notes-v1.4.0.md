# godex v1.4.0 — "Protocols & Plugins"

> 状态：Historical（v1.4.0 release record）
> Release date: 2026-08-23 ｜ Base: v1.3.0 ｜ Commits: 64

🇬🇧 **English**

**v1.4.0 is the "Protocols & Plugins" release.** It opens the LLM layer to the modern OpenAI Responses wire protocol (new `openai_responses` provider type, a usage-gateway `POST /v1/responses` endpoint, and zero-cost remote passthrough), and it turns the agent runtime into a plugin host — a lightweight plugin kernel with wazero WASM tool execution, a stdio MCP bridge, an ACP external-engine harness, and a hardened package dependency graph. Context compaction gets a model-agnostic Phase-4 pruner and prefix-cache-friendly history; the relay plane gains gzip frames and managed TCP forward tunnels; and the Web UI grows a windowed three-lane timeline, grouped-by-provider model pickers, and a fully i18n settings page.

### 🔌 OpenAI Responses Protocol
- New `openai_responses` provider type (Phase A): a general-purpose OpenAI Responses client on the openai-go SDK, reusing the codex converters (input items, tools, event state machine, usage) with no codex-specific headers, `max_output_tokens` passthrough, and official automatic prefix caching via `prompt_cache_retention`.
- Usage-gateway `POST /v1/responses` endpoint (Phase B): requests/instructions → internal protocol, non-streaming `{id, object:"response", output[], usage}` JSON and streaming `response.output_text.delta` / `reasoning_summary_text.delta` / `output_item.added` / `function_call_arguments.delta` / `response.completed` SSE; gdx_ proxy-key and web-token dual auth.
- Remote passthrough (Phase C): confirmed the relay channel forwards arbitrary paths, so `/v1/responses` works on remote nodes with zero code changes.
- `docs/responses-protocol-plan.md` — full four-phase design (Phase D deliberately YAGNI'd).

### 🧩 Plugin Kernel & WASM Runtime
- Lightweight plugin kernel (`pluginrt`): lifecycle, effects, transactional reload, owner/generation-aware tool registration, dynamic harness router (engines registerable after first use).
- wazero WASM tool executor (`wasmrt`): controlled host calls (`godex_http_get`, `godex_kv` namespaced durable broker, `godex_credential_get` allowlisted broker, `godex_policy` before/after tool decisions, `godex_prompts_list` prompt/context contribution).
- Plugin SDKs: zero-dependency Rust cdylib and TinyGo WASM examples (`examples/wasm-plugin-todo`), ABI-equivalent `godex:plugin@0.1`.
- Package system: phase-0 dependency graph with requires/provides and transactional reinstall; Package→WASM runtime auto-activation with security hardening.
- MCP bridge: stdio JSON-RPC MCP client with dynamic per-server tool registration and `prompts/list` + `prompts/get`.

### 🤖 ACP External Engine
- Stable Harness input surface: the host consumes the engine's Reply (append + checkpoint + events); `HarnessTurnInput.Scope` binding with ACPHarness bind/reject.
- ACP streaming handle: live text deltas via readResponseWithCallback; unified session/update → GoDex event mapping; plan/permission updates surfaced as warning events; external-engine failures mapped onto `error_raised`.

### 📦 Context Compaction & Prefix Cache
- Compaction Phase 4: model-agnostic pruner + context-overflow recovery + per-model strategy table; DSH-aligned window scaling thresholds, verbatim tail preservation, prefix-aligned LLM summarization.
- Prefix-cache-friendly history: environment date moved into the tail, orphan tool_use sanitized, deterministic repo_map snapshot, codex reasoning-stream events.

### 🔄 Relay & Remote Plane
- gzip frame bodies negotiated via hello capabilities; managed TCP forward tunnels in the center process; `node join --llm-proxy` writes a center usage-gateway provider; SSE heartbeat with bounded connect/forward timeouts; remote path proxying for usage/notes/config/memory/skills/automation; node last-seen refreshed from relay pongs.
- `request_gzip` provider option + gateway gunzip.

### 🖥️ Web UI
- Timeline stage 2: grouped turn/step view + 3-lane overview + windowed virtualization; token totals; math (KaTeX) rendering; mobile layout fixes; stream connection hygiene.
- Model pickers grouped by provider: backend exposes `provider_name`, the Chat dropdown groups options by provider (active provider group first), and the TUI `/model` picker prefixes every option with the provider label — no more "which provider is this model from?".
- Settings: full i18n, collapsible LLM providers, compact model rows; usage-gateway page shows the new `/v1/responses` endpoint; per-session skill presets; loading veil on session switch.

### 🛠️ Tools & Engineering
- `glob` tool switched to a ctx-aware controlled traversal (fixes hangs on huge repos); hardened `openai_compatible` wire + raised output budget; min-tui bumped to v0.5.12.

* * *

🇨🇳 **中文**

**v1.4.0 是「协议与插件」版本。** LLM 层全面接入现代 OpenAI Responses 协议（新增 `openai_responses` provider 类型、usage gateway 的 `POST /v1/responses` 端点、远程零成本透传）；Agent 运行时进化为插件宿主——轻量插件内核 + wazero WASM 工具执行、stdio MCP 桥接、ACP 外部引擎 harness、加固的包依赖图。上下文压缩迎来模型无关的 Phase 4 pruner 与前缀缓存友好的历史构造；relay 平面新增 gzip 帧与受管 TCP 转发隧道；Web UI 新增虚拟化三泳道时间线、按 provider 分组的模型选择器、全量 i18n 的设置页。

### 🔌 OpenAI Responses 协议
- 新增 `openai_responses` provider 类型（Phase A）：基于 openai-go SDK 的通用 Responses 客户端，复用 codex 转换函数（input items / tools / 事件状态机 / usage），不带 codex 专属头、透传 `max_output_tokens`、经 `prompt_cache_retention` 开启官方自动前缀缓存。
- Usage gateway `POST /v1/responses` 端点（Phase B）：请求/instructions → 内部协议；非流式 `{id, object:"response", output[], usage}` JSON 与流式 `response.output_text.delta` / `reasoning_summary_text.delta` / `output_item.added` / `function_call_arguments.delta` / `response.completed` SSE；支持 gdx_ 代理 key 与 web-token 双认证。
- 远程透传（Phase C）：确认 relay 通道按任意 path 转发，`/v1/responses` 远程 node 零代码改动即可用。
- `docs/responses-protocol-plan.md`：完整四阶段设计（Phase D 明确 YAGNI 砍掉）。

### 🧩 插件内核与 WASM 运行时
- 轻量插件内核（`pluginrt`）：生命周期 / 副作用 / 事务式重载，owner/generation 感知的工具注册，动态 harness 路由（引擎首次使用后可注册）。
- wazero WASM 工具执行器（`wasmrt`）：受控 host 调用（`godex_http_get`、`godex_kv` 命名空间持久 KV、`godex_credential_get` 白名单凭证、`godex_policy` 工具前后策略、`godex_prompts_list` 提示词/上下文注入）。
- 插件 SDK：零依赖 Rust cdylib 与 TinyGo WASM 示例（`examples/wasm-plugin-todo`），ABI 等价 `godex:plugin@0.1`。
- 包系统：phase-0 依赖图（requires/provides）+ 事务式重装；Package→WASM 运行时自动激活与安全加固。
- MCP 桥接：stdio JSON-RPC MCP 客户端 + 动态按服务器工具注册 + `prompts/list` / `prompts/get`。

### 🤖 ACP 外部引擎
- 稳定 Harness 输入表面：宿主消费引擎 Reply（追加 + checkpoint + 事件）；`HarnessTurnInput.Scope` 绑定与 ACPHarness bind/reject。
- ACP 流式处理：经 readResponseWithCallback 透出实时文本 delta；session/update → GoDex 事件统一映射；plan/permission 更新以 warning 事件上浮；外部引擎失败映射到 `error_raised`。

### 📦 上下文压缩与前缀缓存
- 压缩 Phase 4：模型无关 pruner + context-overflow 恢复 + per-model 策略表；DSH 对齐的窗口缩放阈值、verbatim 保留尾、前缀对齐 LLM 摘要。
- 前缀缓存友好的历史：environment 日期移入尾部、孤儿 tool_use 清理、确定性 repo_map 快照、codex 推理流事件。

### 🔄 Relay 与远程平面
- hello 能力协商的 gzip 帧体；中心进程受管 TCP 转发隧道；`node join --llm-proxy` 写入中心 usage-gateway provider；SSE 心跳 + 连接/转发超时上限；usage/notes/config/memory/skills/automation 远程 path 代理；node last-seen 从 relay pong 刷新。
- `request_gzip` provider 选项 + 网关 gunzip。

### 🖥️ Web UI
- 时间线 stage 2：分组 turn/step 视图 + 三泳道总览 + 窗口虚拟化；token 合计；数学公式（KaTeX）渲染；移动端布局修复；SSE 连接卫生。
- 模型选择器按 provider 分组：后端暴露 `provider_name`，Chat 下拉按 provider 分组（当前 provider 组置顶），TUI `/model` 选择器每个选项带 provider 前缀——不再出现「看不出是哪个 provider 的模型」。
- 设置页：全量 i18n、可折叠 LLM providers、紧凑模型行；usage-gateway 页面展示新增 `/v1/responses` 端点；按会话技能预设；切换会话加载遮罩。

### 🛠️ 工具与工程
- `glob` 工具改为 ctx 感知的受控遍历（修复大仓库递归 glob 卡死）；加固 `openai_compatible` wire + 提高输出预算；min-tui 升级到 v0.5.12。

* * *

### 📦 Assets
- `godex-v1.4.0-linux-x86-64.tar.gz` — Linux amd64
- `godex-v1.4.0-mac-x86-64.tar.gz` — macOS amd64
- `godex-v1.4.0-mac-apple.tar.gz` — macOS arm64
- `godex-v1.4.0-win-x86-64.tar.gz` — Windows amd64
