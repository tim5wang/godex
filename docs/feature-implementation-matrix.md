# GoDex 功能—实现—文档矩阵

> 状态：Active（功能事实索引；最后核对：2026-08-31）
> 维护规则：功能状态以可执行入口、实现和测试共同确认；设计文档只描述边界与决策，不能单独把功能标成“已实现”。

## 状态定义

| 状态 | 含义 |
|---|---|
| Implemented | 入口、实现与基本测试均存在 |
| Partial | 主链存在，但验收项、UI 或可靠性仍缺 |
| Planned | 仅设计/路线图，不应出现在“已实现”能力清单 |
| Superseded | 入口已删除或被另一产品形态取代 |
| Contract drift | 实现、测试、help 或文档至少两者冲突 |

## 用户功能矩阵

| 功能域 | 状态 | 代码事实源 | 用户入口 / Help | 权威文档与备注 |
|---|---|---|---|---|
| 共享 Session Runtime | Implemented | `services/backend`, `domain/events`, `sessionstore` | CLI/TUI/Web/HTTP/IM | README、user-guide；turn/session API 有大量回归测试。 |
| CLI/TUI | Implemented | `internal/app`, `cmd/godex`, `tui/mintui` | `godex --help`, `/help` | root help 在代码中；TUI help 由 `buildHelpPages` 维护。 |
| Web 工作台 | Implemented | `app/appRegistry.tsx` | 12 个当前导航 app | README；当前 app 是 Chat、Files、Automation、Nodes、Notes、Skills、Agents、Memory、Settings、Business Agents、TaskBoard、Usage。 |
| HTTP/SSE session API | Implemented | `runtime/httpapi` resource registrars | `/sessions/*`, `/events` | user-guide；命名 `Dependencies`、route ownership gate 与按资源域 registrar 共同约束装配。 |
| Slash commands / 多入口命令 | Implemented | `services/commands.AvailableMetadata` | CLI `command`、Web `/commands`、TUI、ACP、IM | metadata 同时驱动 help 与入口注册并有测试；文档只解释复杂参数。 |
| OpenAI Chat Completions gateway | Implemented | `httpapi.go`, `routes_openai.go` | `POST /v1/chat/completions` | user-guide、release notes。 |
| OpenAI Responses gateway/provider | Implemented | `routes_responses.go`, conversation Responses clients | `POST /v1/responses` | `responses-protocol-plan.md` 已改为 Historical；v1.4 release notes 记录落地。 |
| Anthropic Messages gateway | Implemented | `routes_anthropic.go`/gateway converters | `POST /v1/messages` | user-guide；stream/non-stream tests 存在，与 Usage registrar 分离。 |
| Agent Step Phase A | Implemented | `routes_steps.go`, `routes_step_track.go` | `/v1/agent-steps*` | main/details 文档已改为 Active；biz-key auth、run/get/cancel/reply/events 均有测试。 |
| Agent Step TypeScript SDK | Implemented | `ui/web/src/lib/agent-step/client.ts` | `createStepClient` | `agent-step-sdk.md`；client tests 存在。 |
| `<godex-step>` Web Component | Implemented | `agent-step/godex-step.ts` | HTML custom element | Agent Step Phase C；支持流式、多轮与 ui_card reply。 |
| Business Agents | Implemented / evolving | `BusinessAgentsPage.tsx`, `routes_biz.go`, usage biz keys | `/business-agents`, `/v1/biz/keys*` | business-agents-console-design；template_id 收敛已落地，后续能力仍需按文档逐项标记。 |
| Workflows Web 板块 | Superseded | 仅剩 `features/workflows/components/UiCardView.tsx` | 无 `/workflows` route | `c9612c1` 删除页面并由 Business Agents 取代；旧三份 Workflows 文档已标 Superseded/Historical。 |
| ui_card | Implemented | `tools/ui_card`, MessageFeedV2, UiCardView, godex-step | Chat 与嵌入组件 | Agent Step/Business Agents 文档；交互 reply 已闭环。 |
| Files / code editor / diff | Implemented | `routes_files.go`, `features/files` | Web Files | README/user-guide。 |
| Attachments / media / artifacts | Implemented baseline | `core/media`, backend attachment/materialize, Web upload | Chat、Feishu、Weixin、tool `ArtifactPaths` | 图片/文档/OCR/音视频和显式 artifact 提升有测试；processor 是 955 行热点。 |
| Terminal | Implemented | `routes_terminal.go`, `TerminalPanel.tsx` | Web panel | README/user-guide；PTY 与 pipe fallback 有测试。 |
| Static/proxy Preview | Implemented | `routes_preview.go`, `PreviewPanel.tsx` | Web preview panel | workflow integration 旧文档不再是权威入口。 |
| Provider/model 配置 | Implemented | `core/config`, `core/providers`, Settings | `providers`, `login/logout`, Settings | README/user-guide；schema/get/set 契约测试覆盖字段映射，doctor 检查按域组织。 |
| Tool runtime 与 bundles | Implemented baseline | `toolruntime`, `agent/tool_registration.go`, `agent/session_template.go` | `tool_exchange`, runtime prompt | 模板基线为 `Tools ∪ Bundles`；`always_on` 由模板显式选择并在会话内固定；clear 恢复精确模板基线，`default` 空模板保留兼容语义。 |
| LSP 工具 | Implemented | `tools/lsp*.go` | `lsp` bundle/tool | README/user-guide；code graph 是本次审查工具，不是产品 LSP 的替代实现。 |
| MCP stdio/HTTP | Implemented | `core/mcp`, `routes_mcp.go`, MCP tools | Settings、`/v1/mcp/*` | extension-runtime-user-guide；动态 server tools 会让工具总数变化。 |
| Package / Skill | Implemented | `core/packages`, `core/skill`, `pluginrt`, SkillsPage | `/packages`, `/skills`, Web Skills | README、extension runtime guide；quality/smoke/reinstall 均存在。 |
| WASM plugin runtime | Implemented baseline | `wasmrt`, `pluginrt`, examples | package activation | plugin evolution/research docs；完整 marketplace/signing 仍 Planned。 |
| ACP external agents/server + whole-turn Harness | Implemented（PiHarness 专用封装除外） | `acp/server`, `agent/acp_harness.go`, `tools/acp_agent.go` | `acp-server`、Agent 模板 engine、CLI `--harness`、external_agents bundle | Web 会话由模板固定 engine；有界历史恢复、交互审批、进程组、工具 session 复用与 opt-in 客户端 MCP 临时桥接已覆盖；PiHarness 仍 Planned。 |
| Subagent jobs | Implemented | `agent/subagent_*`, backend surfaces | Chat/TUI/API/tool | workflow-runtime、roadmap；review/merge/cancel/resume/iterate 存在。 |
| Review / Merge Center | Implemented, test drift fixed | `reviewMergeCenter.ts`, panel, backend review/merge | Chat panel | superpowers plan 为历史实现记录；read-only job 应是 `no_changes`，review fixture 必须带 writeScope。 |
| Workflow / AgentGraph / LongTask | Implemented | `agent/workflow.go`, `agentgraph.go`, `longtask_*` | tools、CLI、Web task center | workflow-runtime、roadmap；不是已删除的 Workflows 页面。 |
| TaskBoard plugin | Implemented baseline / evolving | `plugins/taskboard`, backend executor, `TaskBoardView` | `/taskboard`, `/v1/taskboard*`, tool | 模板分派、PJM、research、路径冲突闸门与手动 reconcile P0 已落地；自动 reconcile/history/依赖拓扑仍 Planned。 |
| Agent templates / roles / bundles | Implemented baseline / evolving | `core/templates`, role registry, AgentTemplatesPage | `/agents`, template APIs、新建对话/TaskBoard/Biz key | M1–M3、M4 P1、M5 P1–P3 已落地；导入导出/NL 生成/预算硬限制仍 Planned。 |
| Durable Memory / recall | Implemented | `core/memory`, historysearch, MemoryPage | `/memory`, tools, slash commands | memory-design-principles、user-guide。 |
| Context compaction/inspection | Implemented baseline | `core/compress`, agent context/compaction, inspector panels | Chat Context & Recall | compaction plan；模板 activation baseline、runtime prompt 与 clear/reset 已有契约测试，后续继续演进 compaction 策略。 |
| Notes | Implemented | `core/notes`, backend notes, NotesPage | `/notes`, `/note` | README/user-guide。 |
| Session tree | Implemented baseline | `sessiongraph`, backend fork/rollback/merge | Chat/API | architecture-v2、roadmap；更完整 branch isolation 仍属 2.0 演进。 |
| Storage backend abstraction | Implemented baseline / evolving | `sessionstore`, `persistence.DurableMap`, domain Repository、`platform/localstore` | internal | task/todo/message 已通过 Repository 隔离本地 JSON adapter；跨后端事务与完整 2.0 storage 分离仍 Planned。 |
| Cron / Heartbeat | Implemented | `runtime/cron`, `runtime/heartbeat` | Web Automation、slash/API | README/user-guide。 |
| Feishu / Weixin | Implemented | `runtime/channels/*` | serve runtime、Weixin CLI | README/user-guide；channel core 文件过大。 |
| Node Registry / Relay / remote exec-forward | Implemented baseline | noderegistry, relay, nodeobs | Web Nodes、`godex node` | Node Mesh Phase 1–3、forward/exec/Web Push 已落地；PWA、Android node、跨节点编排、doctor/audit 仍 Planned。 |
| Web Push | Implemented baseline | `services/webpush`, `runtime/httpapi/push.go`, Web push client | `/push/public-key`, `/push/subscribe`, `/push/test` | subscribe/unsubscribe/auth 与前端 service worker 注册有测试；完整 PWA 仍 Planned。 |
| Security / approval / sandbox / scope | Implemented baseline | core/security, `core/toolfilter`, toolruntime/permissions, sandbox, scope | approval UI、profiles | Agent Step/template/biz key narrowing 已共享 allowlist evaluator；sandbox hardening 继续按架构路线演进。 |
| Usage / cache stats | Implemented | services/usage, routes_usage, UsagePage | Web Usage、`/usage/*` | codex-cache/cache analysis 是分析资料，不应覆盖运行时事实。 |
| Voice / TTS | Implemented baseline | routes_voice, VoiceBar, TTS playback | `/v1/voice`, `/v1/tts`, `/v1/tts/stream` | voice-engine WS/TTS 主链已落地；turn middleware、plugin config/UI、OpenAI REST/Realtime adapter 仍 Planned。 |
| Cache prompt stability / retention | Implemented baseline / evolving | agent context/system_prompt_dynamic、protocol/conversation clients | Usage/cache metrics | stable/dynamic 分拆和 session 24h retention 已落地；自适应 TTL/compaction 联动仍 Planned。 |
| Browser/Desktop | Implemented | tools/browser*, tools/desktop* | bundles | README/user-guide；Lightpanda 设计为历史/计划参考。 |
| Eval harness | Implemented baseline | services/evalharness, domain/eval | `godex eval` | root help；domain/eval 缺直接 contract test。 |
| Service install / self deploy / GC / doctor / repair | Implemented | app/service, storagegc, config doctor, sessionrepair | CLI | self-deploy、user-guide。 |

## Planned / Draft 功能不能混入“已实现”清单

| 规划 | 当前状态 | 文档 |
|---|---|---|
| Node Mesh v2 / 更完整远程开发 | Implemented baseline / Partial | `node-mesh-design.md`；Phase 1–3 已完成，Phase 4/5 有明确剩余项。 |
| Voice turn middleware、plugin UI/config、OpenAI adapter | Partial / Planned | `voice-plugin-extensibility-design.md`；`ui_card` 与基础 voice 已实现，插件化部分未实现。 |
| 插件 lifecycle/routes/services/schedule 与通用 UI slot | Implemented baseline / Partial | P-A/P-C/P-D 已落地；P-B 动态 Web UI slot 未落地。 |
| TaskBoard reconcile 与协作治理 | Partial | 手动 reconcile P0、research 和路径冲突 baseline 已落地；自动调度/history/depends_on/经验回流未落地。 |
| 架构 2.0 的完整 worker/sandbox/storage/session graph 分离 | Partial | `architecture-v2-spec.md` |
| 复杂 container/SSH sandbox hardening | Planned | roadmap/architecture docs |

## 文档内化到代码的边界

- CLI 命令名、参数与示例优先放在 `rootHelpText`/子命令 help，并由测试读取；文档链接 help，不复制完整语法。
- Slash command 列表以 `AvailableMetadata()` 为事实源；UI/API 应消费 metadata，不手写第二份列表。
- Web app 列表以 `builtinApps` 为事实源；README 只描述产品能力，不硬编码页面数量。
- Tool/bundle 列表以 runtime catalog 为事实源；文档避免写死动态 MCP 后的精确工具数。
- HTTP path 以 route registrar 和 contract tests 为事实源；设计文档描述 auth、lifecycle 与错误语义。
- 文档状态、索引和本地链接由 `make docs-check` 强制检查；本地与 release 的统一质量入口是 `make verify`。
