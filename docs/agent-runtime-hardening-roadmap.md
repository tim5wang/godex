# GoDex Agent Runtime Hardening Roadmap

这份文档沉淀一次面向“通用、强大、快速、能持续数小时完成复杂任务”的深度 review。它不是功能愿望清单，而是接下来逐个修复的执行路线。

## 目标画像

GoDex 的目标形态：

- 通用：能写代码、运维、处理文档，也能跨 Web、TUI、IM 入口工作。
- 强大：能规划、调用工具、处理权限、管理记忆，并能在复杂任务中保持问题解决能力。
- 快速：首字响应、工具反馈、历史检索、UI 操作都要尽量低延迟。
- 长任务可靠：浏览器断开、进程重启、审批暂停、大输出工具、上下文压缩都不应轻易毁掉几个小时的工作。

## 当前基础

已有能力已经超过 demo 阶段：

- typed tools、permission interceptor、工具 schema 暴露与 bundle 管理。
- session persistence、timeline、Web/TUI/IM 共享 backend。
- memory durable/candidate/suppression、history_search、context inspector。
- cron、heartbeat、attachments/media、skills、MCP、browser/web_fetch 等扩展能力。
- Ant Design Web UI 已经统一到较成熟的管理后台体验。

后续重点不是简单堆更多工具，而是补齐长任务 runtime 的可靠性、可恢复性、资源边界和可观测性。

## P0: 长任务运行时可靠性

### 1. 异步 turn/job runtime

问题：历史上 Web `POST /sessions/{id}/messages` 会等待 agent 整轮执行完成，任务生命周期绑定 HTTP 请求。当前已完成第一步：Web message endpoint 会返回 `202 + turn_id`，后端用 background context 继续执行。剩余工作是把 turn/job 状态、cancel 和 reconnect replay 做成完整 runtime。

修复方向：

- `POST` 快速返回 `202 Accepted` 和 `turn_id`。
- 服务端用独立 context 运行 turn。
- SSE 按 `turn_id` 推送状态、assistant delta、tool start/finish、warnings、completion。
- 增加 cancel endpoint。
- Web 发送消息后进入 running 状态，不再等待 submit 请求完成。

验收：

- 刷新页面后任务继续运行。
- 断开 SSE 后重连能继续看到状态。
- cancel 能停止模型请求和工具执行。

### 2. Durable event journal 和 checkpoint

问题：session 主要在 agent turn 完成后持久化。长任务中途 crash 可能丢失几个小时的工具结果和上下文。

修复方向：

- 为每个 session/turn 写 append-only journal。
- 在 user append、assistant append、tool start、tool finish、permission pending、compaction、turn completion 后落盘。
- 启动时 replay journal，恢复 running/pending/error 状态。
- timeline 从 journal 派生或与 journal 保持同源。

验收：

- 工具执行完成后 kill 进程，重启仍能看到已完成工具事件。
- permission pending 后重启仍可审批并 resume。
- turn completion 前的部分 assistant/tool 结果不会丢。

### 3. 明确 MaxTurns/loop 失败状态

问题：达到最大 turn 数时不能返回空错误，必须可诊断。

修复方向：

- 保留 `conversation.ErrMaxTurnsReached`。
- 错误消息包含 turn budget 和 session/turn 信息。
- UI 和 timeline 显示清晰失败原因。

验收：

- runaway loop 结束后不会出现空白错误。

## P1: 资源边界和速度

### 4. 工具输出与输入大小上限

问题：shell/background 输出、web fetch body、附件上传、媒体读取可能完整进入内存或上下文。

修复方向：

- shell/background stdout/stderr 使用 ring buffer 或 spill file。
- web_fetch 原始响应体设置硬上限。
- attachment upload 设置 HTTP 与 backend 双层硬上限。
- media 读取大文件前检查 size。
- tool result 进入模型前做预算和摘要。

验收：

- 大输出命令不会撑爆进程内存。
- 超大网页、超大上传、超大媒体文件有明确错误。
- tool result 不会无上限塞进下一轮模型上下文。

### 5. 真正的 LLM streaming

问题：当前 UI 有事件流，但模型调用本身是非 streaming，assistant text delta 实际是完整文本一次性发出。

修复方向：

- provider client 支持 streaming。
- agent runtime 将 token delta、tool_use partial、心跳接入 event stream。
- 前端按 delta 增量渲染。

验收：

- 长回答能快速首字显示。
- 模型长时间生成时 SSE 有心跳，不容易被代理断开。

### 6. 可配置模型 timeout

问题：模型请求 timeout 不应硬编码。

修复方向：

- 增加 `api.timeout_seconds` 或 provider-specific timeout。
- streaming 模式区分 connect timeout 和 idle timeout。

验收：

- 配置文件、环境变量、Settings UI 均可修改。

## P2: 长上下文能力

### 7. 语义化 compaction

问题：当前 compaction 主要保留 transcript 引用和最近消息，缺少结构化任务状态。

修复方向：

- compact summary 包含目标、约束、决策、已改文件、测试结果、未解决问题、下一步。
- transcript ref 保留完整历史。
- history_search 可从 summary 自动推荐检索范围。

验收：

- 压缩后继续开发任务时，agent 不丢当前目标和文件状态。

### 8. history/memory 索引

问题：history search 按需扫描 session/transcript，规模变大后会慢。

修复方向：

- 引入本地 SQLite FTS 或等价索引。
- session persist、transcript 写入、memory 更新时增量索引。

验收：

- 上千 session/transcript 下搜索仍稳定低延迟。

### 9. 工具发现和 schema 暴露一致性

问题：prompt/catalog 说工具可用，但 schema 暴露依赖字符串启发式，容易和实际可调用工具不一致。

修复方向：

- 增加轻量 `tool_search` / `tool_info`。
- 将工具暴露从 raw substring 迁移为 planner/router 决策。
- catalog 中明确当前 turn 可调用与需激活工具。

验收：

- 中文和英文自然表达都能稳定触发相关工具。
- prompt 不暗示不可调用的工具。

## P3: 复杂任务协作

### 10. Durable subagents

问题：subagent 需要从同步子调用升级为可恢复、可审阅、可合并的并行 worker，避免长任务 worker 互相覆盖主工作区。

修复方向：

- subagent 变成 durable job。
- 支持隔离 snapshot worktree、明确 write scope、progress events、cancel/resume。
- 增加 review/merge 流程，merge 前检测主 workspace 与 baseline 是否冲突。

验收：

- 多 worker 并行探索、实现、验证，不互相覆盖。
- 主 agent 可中途查看 worker 状态并整合结果。

### 11. 评测与故障注入

修复方向：

- 增加 restart mid-turn、approval resume、huge output、browser disconnect、history scale 的测试。
- CI 中对核心包覆盖率设阈值。
- 建立多小时开发任务 smoke/eval。

验收：

- 长任务能力有回归保护，不靠手动体验判断。

## 当前修复进度

- [x] 修复 `ErrMaxTurnsReached` 空错误，保留可诊断错误信息。
- [x] transcript 文件名增加高精度后缀，避免同秒 compaction 覆盖。
- [x] transcript 写入前确保目录存在。
- [x] `web_fetch` 原始响应体加硬上限和单测。
- [x] attachment upload 增加 backend 存储层硬上限和单测。
- [x] Web attachment endpoint 增加 request body 上限。
- [x] Web message endpoint 改为异步 accept：`202 + turn_id`，agent turn 使用服务端 background context 继续执行。
- [x] Web 前端提交消息后不再等待 agent 完整跑完，并主动刷新 snapshot/timeline/session。
- [x] Web async turn 增加 cancel endpoint 和 Chat 停止按钮。
- [x] SSE 支持 `replay=active`，重连后补发当前 active turn 的已记录事件。
- [x] session timeline 在运行中持续落盘，作为轻量 event journal 基础。
- [x] boot-time recovery 会将已开始但未完成的 turn 标记为 `interrupted`，避免重启后状态消失。
- [x] `turns.json` 持久化 turn 状态机，记录 running/canceling/completed/canceled/interrupted/error/pending_approval。
- [x] 最新 canceled/error/interrupted turn 支持基于持久化输入重试，并通过 Web API/Chat Inspector 暴露。
- [x] 完整 turn/job runtime：turn 执行中会 checkpoint transcript/turn 状态，interrupted turn 支持从持久化 checkpoint 继续执行，并通过 Web API/Chat Inspector 暴露 resume。
- [x] durable `events.jsonl` 追加式 journal；启动时优先从 journal 回放 timeline，旧缓存缺失时仍能恢复 interrupted turn。
- [x] shell/background 输出预算与 spill：命令输出只保留预览进上下文，完整保留内容默认落到 `~/.godex/tmp` spill 文件并设置硬上限。
- [x] 真正的 LLM streaming：Anthropic-compatible SSE 增量解析，runner 优先使用 streaming caller，assistant delta 与完成事件分离。
- [x] 可配置模型 timeout：新增 `api.timeout_seconds`，支持 `GODEX_API_TIMEOUT_SECONDS`/Settings UI/live apply，默认单次模型请求 600 秒。
- [x] semantic compaction：压缩摘要结构化保留目标、约束、决策、文件、验证命令、开放事项和最近 handoff，并保留 transcript ref 供 history_search 精确回查。
- [x] history/memory FTS：memory 已使用 `memory.db` SQLite/FTS sidecar；history_search 增加 `history_search.db`，按 session/transcript 变更增量同步，archive 搜索优先走索引，线性回退与索引路径复用 refs/metadata 收集逻辑。
- [x] durable subagents：已完成基础 durable job 化，`task` subagent 工具支持 `start/status/list/cancel/resume/review/merge`，job 落盘到 `subagents/`，运行中重启会标记 `interrupted` 并可 resume；subagent 在隔离 snapshot worktree 中执行，progress 会落盘并进入 session SSE/timeline，review 可查看 write_scope 内 diff，merge 会检测主 workspace 与 baseline 冲突后再写回。
- [x] 长任务 smoke 入口：新增 `scripts/longtask_smoke.sh`，集中覆盖 restart mid-turn、checkpoint resume、pending approval 重启后继续审批并恢复、SSE active replay、durable subagent resume/review/merge、端到端开发 loop（失败测试 -> 代码修复 -> 复跑通过 -> handoff）、大输出 spill、channel restart rollback、history_search sidecar/indexing；`scripts/release_check.sh` 支持 `GODEX_LONGTASK_SMOKE=1` 可选执行。
- [x] 发布前整洁性清理：Web JSON API 统一到 `/api/*`，移除旧裸 API、旧 env alias、旧 skill 单文件布局、旧 todo 字段 alias、前端 context schema fallback，并收敛 tool/error/subagent/history_search 的重复实现。
- [x] 项目结构重排：入口迁入 `cmd/godex`，Go 业务代码迁入 `internal/*` 分层，tool framework 抽到 `internal/toolruntime`，history domain 抽到 `internal/domain/history`，embedded Web UI 迁入 `internal/uiassets`，默认本地运行态收敛到 `~/.godex/`。
- [x] 四项高收益优化 v1：新增 model provider registry 和 OpenAI-compatible Chat Completions caller，Web Chat 支持按 session 切换模型；新增 session fork metadata、fork API 和 running message queue；新增 Security CIK summary/audit API 与 Settings 面板；新增 declaration-only package registry，支持安装/卸载 `godex.package.yaml` package 并发现 skills/prompts/commands 资源。
- [x] Browser assisted automation v1：`browser` 工具新增 `handoff/resume`，默认 headless 遇到登录、验证码、二次确认等用户门禁时可切到可见浏览器，用户完成后恢复 snapshot；新增 `browser-assist` 示例 skill。
- [x] Desktop/UI automation tool v1：新增 `desktop` bundle/tool，采用小原生后端且不引入 robotgo 依赖；macOS / Linux / Windows 支持桌面截图 artifact、窗口列表、坐标点击、文本输入、常见按键、剪贴板读写，远程入口的 mutating desktop 动作沿用 approval policy。
- [x] Desktop OCR/视觉定位 v1：`desktop` 工具新增 `ocr`、`find_text`、`click_text`，复用截图并调用系统 `tesseract` CLI 输出 TSV，支持按屏幕文字返回坐标或点击第一个匹配项。
- [x] Eval/benchmark harness v1：新增 `internal/domain/eval`、`internal/services/evalharness` 和 `godex eval run/list/show`，支持 `godex.eval.yaml`、文本/工具/失败数断言、JSON/JSONL 报告、示例 smoke suite 和 `GODEX_EVAL_SMOKE=1` release check 入口。
- [x] Skill/Package 质量面板 v1：新增 `/packages/quality`，后端聚合 package manifest/resource/permission/trust 健康和近期 tool health；Web Skills 展示 Quality & Security，Settings Security 展示 package risk 摘要。
- [x] Command execution backend v1：新增 `tools.execution`，`bash` / `background_run` / isolated subagent bash 统一走 workspace executor，可选 Docker bind-mount workspace 后端；SSH 后端先保留配置和显式未实现错误。
- [x] Tool exposure hardening v1：`web` bundle 从默认 active 改为按需启用；`tool_exchange` 增加自然语言 `query`、`include_tools`、`include_schemas` 和 `max_results`，默认只返回推荐 bundle 短摘要，避免长任务反复把完整 catalog/schema 写入上下文。
- [x] Model provider registry v2：`api.providers` 支持多个 LLM 供应商及每个供应商下多个模型，`api.default_model` 使用 `provider.model` id，`api.model_strategy` 支持 `primary` / `fallback` / `round_robin`，并统一投影到 runtime model profile 与调用策略。
- [x] Workspace setup v1：`godex setup --dir <path>` 初始化项目配置、`.env.example` 和 `AGENT.md`；运行态默认进入 `GODEX_HOME`。
- [x] ACP bridge v1：`external_agents` bundle 暴露 `acp_agent`，按 ACP stdio 基线流程调用配置在 `acp.agents` 中的外部 agent。

## 高收益优化后续 Backlog

本轮先落地四项收益最大的本地 runtime 能力。以下调研项暂不进入当前实现：

- OAuth/subscription provider 登录。
- Native desktop app。
- Desktop/UI automation tool 后续增强：文件选择器专用 helper、Wayland/Windows 更深的原生 backend。
- A2A/remote agent interoperability：ACP stdio client bridge 已有 v1，后续补完整 client capabilities、权限回调、长任务 streaming/job 化，以及 A2A remote discovery。
- Voice/canvas/mobile node。
- Public session sharing/eval dataset。
- 第三方代码插件扩展。
- Container sandbox / SSH sandbox execution 后续增强：容器资源限制、网络策略、镜像预拉取/缓存、远程 SSH workspace 同步和审计。

## 执行顺序

第一批低风险修复：

- 修复 `ErrMaxTurnsReached` 空错误。
- transcript 文件名避免秒级冲突。
- web_fetch 原始响应体加硬上限。
- attachment upload 加硬上限。

第二批 runtime 改造：

- 异步 turn/job 状态机。
- Web submit 改为 fire-and-follow。
- cancel endpoint 和 SSE reconnect 状态补齐。

第三批持久化改造：

- turn journal。
- boot-time replay。
- permission pending/restart/resume 测试。

第四批能力增强：

- streaming LLM。
- semantic compaction。
- history/memory FTS。
- durable subagents。
