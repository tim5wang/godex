# GoDex High-ROI Roadmap

> 状态：Superseded（已被 `docs/godex-optimization-roadmap.md` 合并，保留仅作追溯）

这份文档用于记录 GoDex 当前最值得继续投入的基础能力。它不是历史设计稿，也不是“大而全”的产品路线图；它只保留会明显提升长任务稳定性、跨入口一致性、agent 可治理性和生态扩展性的工作。

旧版 Milestone A/B/C/D 中的大部分 v1 能力已经落地，本文从当前实现状态重新整理后续方向。

## 当前基线

GoDex 现在已经不是单入口聊天 demo，而是一套本地优先的共享 agent runtime。当前稳定基线包括：

- 多入口共享后端：CLI、readline、TUI、Web、Feishu、Weixin、cron、heartbeat 使用同一套 session、tool、memory、timeline、attachment、approval 基础设施。
- Web UI 已是主力管理入口：Chat、Automation、Skills、Memory、Settings、pending approvals、Context & Recall、跨 channel session 列表。
- Memory 2.x 已有：显式记忆、候选 inbox、suppression、SQLite + FTS5 sidecar、scope-aware recall、project miner、history_search。
- 指令系统已落地：`AGENT.md`、本地规则、动态上下文、skills/package prompt 可组合注入。
- Skill/Package 体系已落地：native skill、第三方 normalizer、compatibility analyzer、package command dispatcher、role registry、role/package contract v1、quality diagnostics、smoke run、reinstall tracking。
- Tool runtime 已有：typed tools、参数 coercion、before/after interceptors、session-scoped permission policy、manual/review/yolo 审批模式。
- Runtime hardening 已有：tool exposure 按需加载、`tool_exchange` 短摘要、large tool result artifact cap、loop guard、replay eval fixture、模型辅助 session summarization fallback。
- Subagent/workflow 已有：durable subagent、subagent inspector/API、batch/wait、分层 job storage、per-job timeout、workflow handoff artifact、dependency injection、preview merge、dynamic append node、conditional append edge。
- Runner 韧性已升级：统一 phase checkpoint、active turn follow-up injection、请求前 context sanitization、空回复/length/provider error 恢复 handoff。
- Agent 治理已升级：main/subagent/workflow identity v1、role contract v1、capability summary/inheritance 边界和 UI/API 展示字段。
- Channel/API 接入治理已升级：`ChannelAdapter` v1 capability/routing、event fanout/replay、delivery retry/status、inbound access gate/security audit、最小 OpenAI-compatible chat endpoint 和 Web Settings 展示。
- Package/Skill 生态质量已升级：package manifest contract v1、capability/tool policy diagnostics、smoke quick check + explicit smoke run API、command dispatch diagnostics、reinstall tracking、Web Skills 展示与回归 fixtures。
- 跨项目配置与 provider 治理已升级：`GODEX_HOME` / `GODEX_PROJECT_DIR` 分层、home/project config 与 env overlay、OpenAI/Codex login/logout/provider list/test/fetch models、`openai_codex` Responses backend、Settings provider/model 选择与状态展示。
- 安全边界已升级：安全 profile、host privilege policy、WorkspaceFS 文件边界、workspace 外路径/symlink escape 拦截、package smoke working dir 检查、shell 风险分级和审计。
- Browser/Desktop/ACP 已有：browser handoff/resume、desktop bundle、OCR 基础能力、external ACP stdio agent bridge。
- 执行后端已有：本地和 Docker bind-mount workspace 模式；SSH 仍只保留配置与明确错误边界。

## 已完成里程碑归档

下面这些内容在旧 roadmap 中曾是主要目标，现在只作为状态归档，不再作为待办设计稿维护：

- `AGENT.md` 分层指令系统
- Memory v1 显式写入、forget、query-time retrieval
- turn-end memory extraction
- Insights Lite
- Skill v2 native 基础层
- 第三方 skill normalizer
- compatibility analyzer
- skill 与 bundle 推荐联动
- MCP resource tools
- 统一消息 envelope
- `allowed-tools` / `context: fork` / hooks 的最小兼容
- subagent 可观测性 v1
- replay eval v1
- package command palette
- workflow handoff runtime v1
- channel/external access governance v1

## 当前判断

GoDex 的短板已经从“有没有基础能力”转向“长任务能否稳定工作数小时、多个 agent 能否被治理、自动记忆和工具执行能否可审计”。后续优先级应从新增花哨功能转向这几类底座：

- 长任务运行时韧性
- agent 身份与角色边界
- 分层 context 压缩与恢复
- 可审计 memory 整理
- 工具执行安全与可诊断性
- channel/API 插件边界
- package/skill 生态质量
- 跨项目 CLI/TUI 配置体验、OAuth provider 登录和分档安全模式

## 高 ROI 优先级

### P0 长任务交互与 Runner 韧性

状态：已完成 v1。当前实现包含统一 runner phase event、mid-turn follow-up/steering injection、context sanitization、空回复与 length 截断恢复、错误 handoff checkpoint。

目标：让 GoDex 面对“连续工作数小时、从零开发项目、期间用户多次补充要求”的任务时，不靠单个超长 turn 硬撑，而是有清晰的阶段、恢复点、注入机制和错误恢复策略。

需求点：

1. 统一 runner phase checkpoint，覆盖 `model_request / awaiting_tools / tools_completed / final_response / error / interrupted` 等阶段。
2. main agent、subagent、workflow node 复用同一套 phase event，UI 和 timeline 不再各自猜状态。
3. 支持 mid-turn follow-up injection：active turn 运行中用户追加消息时，优先注入当前 turn，而不是启动竞争性的第二个 turn。
4. injection 需要有数量上限、合并规则、timeline 事件、UI 提示，并能和 pending approval 共存。
5. 每次模型请求前执行最终 context sanitization：修复 orphan/missing tool result、压缩旧工具结果、裁剪历史、保证 provider 需要的合法 role/tool 边界。
6. 对空回复、`finish_reason=length`、provider timeout、可重试 provider error 增加显式恢复策略。
7. 长任务失败时返回可诊断 handoff，而不是只给用户一个 runner error。

参考来源：

- `temp/nanobot` 的 `AgentRunSpec / AgentRunResult`、checkpoint callback、mid-turn injection、provider recovery。
- 当前 GoDex conversation runner、session checkpoint、workflow handoff runtime。

验收标准：

- 用户在长任务运行中追加要求，不会产生并发抢占的重复 turn。
- provider 空回复或 length 截断不会直接导致任务失败。
- timeline 能看出任务卡在哪个 phase。
- runner 失败时能保留最后状态、已完成工具、可恢复提示。

### P1 Agent 身份与角色边界

状态：已完成 v1。当前实现包含 main/subagent/workflow identity、package role contract 扩展、capability summary 与子 agent 工具边界、UI/API 展示字段。

目标：把 GoDex 的 role/subagent/workflow node 从“prompt 片段 + job”升级成可治理的 agent identity/manifest，明确“谁在执行、凭什么执行、权限来自哪里、结果归属于谁”。

需求点：

1. subagent 启动时生成稳定 `identity/manifest`，包含 agent id、name、kind、role、parent、session、source、created_at。
2. role registry 输出不只是一段 prompt，而是 `role + tool/capability policy + model/budget hint + display metadata` 的角色契约。
3. 子 agent 权限不能超过父 agent/session policy，建立最小化 capability inheritance。
4. workflow node、durable subagent、automation job 都应能引用 agent identity。
5. UI/API 展示 agent identity、parent、role、capability summary、budget/turn limits、last activity。

参考来源：

- `temp/openfang` 的 AgentManifest、AgentMode、Capability、capability inheritance。
- GoDex 现有 package role registry、Security CIK、subagent job view。

验收标准：

- 每个 subagent/workflow node 都能被稳定追踪到 identity。
- role 变更不会只影响 prompt，还能影响工具、路径、执行边界。
- 子 agent 请求超出父权限时被明确拒绝或要求审批。

### P2 Context 压缩与 Memory 可审计整理

状态：P2.4 Web/API 收尾已完成。当前 GoDex 已有分层 context token 估算、total/history 双维度 compact 提示、保守 auto compact 触发、large tool result 引用观测、summarizer v1 接口、模型辅助 session summarization、规则式 fallback、Context & Recall 展示、既有规则式会话压缩、memory digest/log/restore 命令和 HTTP API、durable memory append-only audit log、Web Memory audit before/after diff + restore/reapply、subagent 父上下文 compact handoff metadata，以及 active skill prompt 预算裁剪。完整 P2 仍未完成：Dream-style 慢速整理、真实 durable memory 版本链和 package/context 全量预算治理仍待实现。

目标：让 GoDex 的长任务上下文从“一刀切 compact”升级为“按重要性分层压缩、可恢复、可审计”的体系；同时让自动记忆从“候选提取”升级为“可审计、可恢复、慢速整理”的长期知识系统，避免把噪声直接写进 durable memory。

设计原则：

1. 工具结果、用户意图、agent 决策、subagent 中间过程和长期 memory 重要性不同，不能使用同一种压缩策略。
2. 用户输入和系统/开发指令优先保真；agent 普通输出优先语义摘要；工具大结果优先 artifact/offload + 可恢复引用；subagent 中间事件优先进 timeline/job archive，父上下文只保留最终可消费结果和追溯引用。
3. 会话压缩摘要只服务当前/未来 turn 续航，不等同于 durable memory；durable memory 必须有候选、diff、版本和 restore。
4. 压缩触发应基于接近真实模型请求体的分层 token 估算，而不是只看历史消息。

需求点：

1. 明确区分“会话压缩摘要”和“长期 durable memory”：前者偏 append-only 机器材料，后者偏人工可审查的稳定知识。
2. 将 context 估算改为分层统计并展示：`system / history / memory / runtime / tool_schemas / attachments / tool_results`，同时保留 total request estimate。
3. 自动压缩触发基于 total request estimate、history estimate 和模型 context window 三者共同判断；当前只看 history 的策略只能作为兼容兜底。
4. 引入模型辅助 session summarization：超过阈值时用独立 summarization prompt 生成续航摘要，保留 system/developer 边界和最近用户消息，摘要失败要有 retry/failover 或明确 handoff。当前 P2.1 已实现模型摘要器、no-tools 摘要请求、大工具结果引用化、一次重试和规则式 fallback。
5. 对工具结果使用独立 reduction/offload 策略：工具刚返回时对超大结果立即 artifact 化并保留 head/tail/sha/path；模型请求前清理旧工具结果，只留下可恢复引用，不把大工具结果混入语义摘要。
6. 对 agent/subagent 输出建立分层保留规则：subagent 中间事件进入 timeline/job archive；父 agent 上下文只保留 subagent final output、job id、identity、artifact 引用和必要的 failure/handoff 摘要。当前 P2.2 已让 subagent model-visible status/wait 结果包含 job、identity、role、result preview、bytes 和 digest，不暴露 raw messages/progress。
7. 为 skills/package/context 注入内容设置单项和总预算，保留最近/显式启用的高价值内容，超预算内容要有可诊断的裁剪说明。当前 P2.3 已对 active skill prompt 增加总预算、单段预算和 truncation notes；package/context 全量预算治理仍待补。
8. 增加 Dream-style 慢速整理任务：定期、小批量读取 session summary、transcript archive、project artifacts 和 memory candidates，提出或执行最小 memory 更新。
9. 给长期 memory 变更增加版本历史、diff、restore 能力。当前 P2.4 已实现 append-only audit log、audit snapshot restore、Web before/after diff 和 restore/reapply；真实版本链仍待补。
10. 给 memory 整理提供命令入口，例如 `/memory-digest`、`/memory-log`、`/memory-restore`，并让命令输出区分 session summary、memory candidate 和 durable memory change。当前 P2.4 已提供这三个命令和对应 HTTP API，`/memory-digest` 只写候选 inbox，不直接写 durable memory。
11. UI Memory/Context 页显示 pending candidates、已采纳变更、diff、回滚入口、压缩前后 token delta、最近 transcript refs、artifact/offload 引用和 history_search 召回入口。当前 P2.4 已覆盖 pending candidates、audit diff、restore/reapply、Context & Recall breakdown、transcript refs 和 artifact/offload 引用；history_search 召回入口仍待补。

参考来源：

- `temp/nanobot` 的 `history.jsonl + Dream + GitStore`。
- `temp/eino` 的 summarization middleware、tool result reduction/offload、PreserveUserMessages、PreserveSkills、summary retry/failover/internal events。
- GoDex 现有 Memory 2.x、history_search、candidate inbox、project miner。
- GoDex 当前规则式 compressor、transcript archive、large tool result artifact、Context & Recall inspector、subagent job storage。

验收标准：

- Context inspector 能分层展示 token 估算，且 auto compact 不再只依赖 history estimate。
- 长任务压缩后仍能保留最近用户意图、关键系统/开发约束、已完成决策、当前工作状态和可恢复 transcript/artifact 引用。当前 P2.4 已覆盖 session summary 层、transcript/artifact 引用和 durable memory audit UI；真实版本链仍待补。
- 大工具结果不会长期常驻模型上下文；模型可见内容包含摘要、head/tail、sha/path 和读取方式。
- subagent 中间过程不会污染父上下文，但 timeline/job archive 可追溯；父上下文能看到 final output 和 job/identity 引用。
- 自动 memory 变更有 diff 和恢复路径。
- memory digest 只生成可审查候选；accept/dismiss/remember/update/forget/restore 都有可追溯 audit log。
- 长任务结束后能生成结构化 project memory，而不是散乱候选。
- 用户能明确知道哪些内容进入长期记忆，哪些只是历史摘要。

### P3 工具与执行安全

状态：P3 execution contract baseline 已完成。当前已给 `bash` / `background_run` 接入共享 shell guard：危险命令模式硬拒绝、私网/metadata URL 拦截、路径敏感命令的 workspace escape 检测、最小环境变量执行、unlisted background command 审批、全局和 role 级 shell allow/deny pattern、Docker/SSH backend 共用执行前 guard 与 output capture，以及 interrupted background task 的 rerun hint。后续更深的 sandbox/container hardening 属于暂缓项。

目标：把工具执行从“可用 + 白名单 + 审批”升级为“按 agent role/capability 可治理、输出可追踪、进程可恢复”的执行合同。

需求点：

1. `bash/background_run` 除命令名白名单和审批外，增加危险模式 denylist、工作区路径逃逸检测、私网/metadata URL 检测和最小环境变量策略。当前已覆盖基础 denylist、metadata/private URL、路径敏感命令 workspace escape 和本地/Docker minimal env。
2. 统一 shell 输出策略：默认保留 head/tail preview，完整输出进 artifact/log，并在模型可见结果里标明截断、exit code、输出路径。当前 `bash` 和 `check_background` 已覆盖。
3. 执行器支持按 role/capability 注入 allow/deny patterns，让不同 agent role 拥有不同 shell 权限边界。当前已支持全局 `tools.execution.shell_allow_patterns / shell_deny_patterns` 和 package role `tool_policy` 中的 `shell:allow:<pattern>`、`shell:deny:<pattern>`。
4. 将超时、取消、进程树清理、后台任务恢复作为执行器合同的一部分。当前已有 timeout/cancel、Unix process group kill、持久化 background task、restart 后 interrupted 标记和 `rerun_hint`。
5. Docker/未来 SSH backend 使用同一套权限、日志、artifact、approval 语义。当前 Docker 和 SSH command builder 都先经过同一 guard，并复用同一 output capture / artifact / approval 路径。

参考来源：

- `temp/nanobot` 的 shell guard、minimal env、bwrap backend、output truncate。
- GoDex 现有 permission runtime、command execution backend、background task storage、tool result artifact cap。

验收标准：

- 白名单外命令会走审批，危险命令即使审批前也能给出清晰风险提示。
- 背景命令重启后可查历史、日志、状态和 rerun hint。
- role/capability 能实际收窄 shell 权限。

### P4 Channel 与外部接入

状态：P4 v1 completed。已完成 `ChannelAdapter` v1 capability/routing、event fanout/replay、多入口 delivery retry/status、统一 inbound access gate/security audit、最小 `/v1/chat/completions` 和 Web Settings 展示。未做项：复杂 marketplace、完整 channel registration owner flow、完整 OpenAI API 复刻、复杂 A2A/remote peer protocol。

目标：让 Web、IM、API、插件 channel 都通过同一套 session identity、approval、timeline、artifact、event stream 和 delivery 规则接入，同时保持接入接口足够轻。GoDex backend/runner/timeline 是唯一 runtime 真源；channel adapter 只做外部平台 inbound/outbound adapter，不直接驱动 agent runtime。

设计原则：

1. backend/runner 拥有 session、turn、approval、checkpoint、timeline 和 artifact。
2. channel adapter 只负责平台协议、鉴权、媒体转换、投递和平台状态。
3. runtime event stream 是唯一可观测源；Web/SSE/IM/timeline/log 作为不同消费者，需要 fanout/replay，不能互相抢事件。
4. `channel / session / thread / sender` 是一等 routing 概念，不能只靠字符串拼接的 session key。

需求点：

1. 定义 `ChannelAdapter` v1：`start / stop / deliver / handle inbound`，并暴露 capabilities、auth/login、media、streaming、typing/status、`allow_from`。
2. 引入 channel routing model：规范 `channel_id / platform_id / thread_id / sender_id / session_mode`，支持 `shared / per-thread / agent-shared` 这类模式的 v1 等价表达。
3. 把 inbound 接入统一落到 backend session/turn：不绕过 P0/P1/P2/P3 已有的 identity、injection、approval、capability、context、artifact 规则。
4. 增加 outbound delivery baseline：`ReplyPlan` / stream delta 可进入可重试投递层，记录 `delivered / failed / last_error`，避免平台 API 失败后静默丢失。
5. 增加 sender/channel access gate：支持 `allow_from`、未知 sender/channel 审批、拒绝审计，并与现有 permission/security audit 对齐。
6. 提供轻量 OpenAI-compatible API 边界：session 隔离、SSE streaming、文件上传、显式 model profile，不承诺完整复刻 OpenAI API。
7. Channel plugin 先支持进程内注册或 package manifest 声明，不做复杂 marketplace。

参考来源：

- `temp/nanobot` 的 `BaseChannel`、`MessageBus`、entry point plugin、stream delta coalescing、OpenAI-compatible API。
- `temp/nanoclaw` 的 `ChannelAdapter`、messaging group/session mode、router、SQLite inbox/outbox、delivery retry、approval gate。
- `temp/eino` 的统一 `AgentEvent`、iterator、stream fanout 思路。
- GoDex 现有 `runtime/channels`、`httpapi`、session list、events、attachments、approval。

验收标准：

- 新增一个简单 channel 不需要改 agent core 或 runner。
- 同一 session 在 Web 和 IM 中看到一致 timeline、artifact、approval、phase。
- channel adapter 无权绕过 backend permission/capability/context rules。
- inbound 支持 thread/session routing；未知 sender/channel 可被审计、拒绝或审批。
- outbound 失败有 retry/failed 状态，不静默丢失。
- SSE/Web 和 IM 消费 runtime event 不互相影响。
- OpenAI-compatible API 能服务基础 IDE/脚本集成，但不承诺完整复刻 OpenAI API。

### P5 Package/Skill 生态质量

状态：P5 v1 completed。已完成 package manifest contract v1、package/command capability 与 tool policy 声明、quality diagnostics 扩展、smoke declaration quick check、显式 `POST /packages/{name}/smoke/{smoke_name}`、session/permission 绑定 smoke run、command dispatch diagnostics、`POST /packages/{name}/reinstall`、Web Skills 质量/安全展示、compatibility regression tests。未做项：复杂 marketplace、签名信任链、远程 update polling、自动执行第三方 smoke、完整 eval matrix。

目标：继续把 GoDex package/skill 从“能安装能加载”推进到“可诊断、可升级、可信任、可复用”。

需求点：

1. package manifest 增加更明确的 role/capability/tool policy 声明。当前已扩展 package-level 与 command-level `capabilities / tool_policy`，role contract 继续展示 `capabilities / tool_policy / model_hint / budget_hint / display`。
2. package quality panel 展示安装健康、风险、常见失败、兼容降级原因。当前 `/packages/quality` 已返回 capability/tool policy summary、contract diagnostics、smoke quick-check、last smoke run、upgrade/reinstall hint 和 install health。
3. package command 的运行结果继续结构化，明确 `dispatch.mode / turn_id / job_id / output / error`。当前已增加 `dispatch_status / dispatch_error / diagnostics`，missing role、unsupported mode、queue/permission error 都能诊断。
4. 第三方 skill/package 增加 smoke test 或 eval hook。当前 manifest 支持 `smoke_tests`，安装只做静态 quick check；显式 smoke run 走 backend session、现有 `bash` permission 与 security audit，不自动执行第三方代码。
5. 为常用第三方生态建立 compatibility fixtures，避免 normalizer 回归。当前已覆盖 package contract、smoke declaration、role/command diagnostics、reinstall metadata 和 HTTP/API 回归；更完整 marketplace/eval fixture 仍待补。

验收标准：

- 安装 package 后能知道它要什么权限、提供什么角色、可能缺什么能力。
- 常见 package/skill 回归能被测试捕获。
- package command 失败时有明确诊断，而不是只显示普通 command failed。
- smoke run 成功、失败或等待审批都会写入最近结果、quality report 和 security audit。
- reinstall 只从已记录 source 重装，失败时不删除现有安装。

### P6 跨项目配置、OAuth Provider Login 与分档安全模式

状态：已完成 v1。当前实现已拆分 `GODEX_HOME=~/.godex` 与 `GODEX_PROJECT_DIR=<cwd>`，支持 home/project `godex.yaml` 与 `.env` 分层加载、旧 workspace 配置兼容读取、global skills/packages/log/provider 配置、`godex login/logout openai|codex`、provider list/test/model discovery、Web Settings provider/model 保存、`openai_codex` 专用 Responses backend、Codex OAuth token home `.env` 保存、`godex migrate home` dry-run/copy、WorkspaceFS 文件边界、安全 profile 与 shell 风险分级。仍未完成的是更深的容器/SSH sandbox hardening、复杂 marketplace 和远程自动更新。

目标：让 GoDex 在 CLI/TUI 模式下能像本地开发工具一样跨项目使用：全局账号、全局 skills、全局 provider 配置和默认运行态都在 `~/.godex`；项目目录默认只作为 workspace 边界，不再隐式创建 `.godex` 状态目录。同时引入 OpenAI/Codex OAuth 登录和分档 sandbox 策略，在不削弱通用强大 agent 能力的前提下，让高风险能力显式升级、可 review、可 approve、可审计。

设计原则：

1. 全局配置和默认运行态集中：`~/.godex` 保存跨项目稳定资产和默认 sessions/memory/tmp/tasks/todos；项目本地状态必须显式配置绝对路径。
2. OpenAI login 指 OAuth/SSO flow，不是手填 OpenAI Platform API key；参考 `temp/pi-go` 的 `/login codex`：PKCE、本地 callback server、浏览器授权、TLS preflight、token 保存到 home `.env`。
3. OAuth token 与普通 `sk-...` API key 要区分治理；Codex/ChatGPT OAuth token 需要专用 provider/backend，不应误投到普通 `https://api.openai.com/v1` platform endpoint。
4. 安全模式控制“在哪里执行、能访问什么”；approval 控制“这次高危动作是否允许”。两者叠加，不能互相替代。
5. 默认更安全，但保留 `host-privileged` 显式升级路径，用于用户确认后的云主机运维、宿主机维护、部署等强能力场景。

需求点：

1. 增加 CLI/TUI 登录命令：`godex login openai` 或 `godex login codex` 启动 OpenAI/Codex OAuth PKCE flow，启动本地 callback server，打开浏览器授权，支持取消、超时、错误诊断和 TLS preflight。
2. OAuth 成功后把 token 写入全局 `~/.godex/.env`，文件权限使用 `0600`；配置中只保存 env var 引用和 provider 元数据，不把 token 明文写进 `godex.yaml`。当前已完成。
3. 登录完成后创建或更新一个稳定 provider，例如 `openai_codex`：`type=openai_codex` 或等价专用类型，使用 Codex/ChatGPT backend base URL、支持模型白名单、账号 id 诊断和 token 失效提示；普通 OpenAI Platform API key provider 仍保留为 `openai_compatible`。当前已完成，并使用 `openai-go/v3` Responses streaming 调用 Codex backend。
4. 增加 `godex logout openai/codex`、`godex providers list`、`godex providers test <id>`，Web Settings 可展示 OAuth provider 登录状态、token 是否存在、过期/失败原因，但不展示 secret。当前已完成，Settings 还支持 fetch models 与模型选择保存。
5. 默认运行根拆分为 `GODEX_HOME=~/.godex` 和 `GODEX_PROJECT_DIR=<cwd>`：全局 `godex.yaml`、`.env`、skills、packages、channel login/cache、memory、sessions、transcripts、tasks、todos、tmp、workflow/background、rules 和 MCP 默认配置放到 home；项目目录不再默认创建 `.godex`。当前已完成，日志默认迁到 `~/.godex/log`。
6. 配置加载顺序升级为：defaults `< ~/.godex/godex.yaml < workspace project config/legacy godex.yaml < ~/.godex/.env < project env < process env < explicit flags`；旧 workspace `godex.yaml/.env/.godex/skills` 继续兼容读取，并由 `doctor` 给迁移提示。当前已完成。
7. 新增 `godex migrate home` 或等价命令，将通用配置、secrets、skills/packages 从旧 workspace 迁到 `~/.godex`，项目 memory/session/task 默认不迁移。当前已完成 dry-run/copy baseline。
8. 引入 `WorkspaceFS` 窄接口：生产默认用 Go `os.Root` 作为文件安全边界，测试/preview/dry-run 可用替代实现；`workspacepath.Resolve` 只保留做人性化路径纠错，不再作为最终安全边界。当前已完成。
9. 文件工具覆盖 `read_file/write_file/edit_file/attach_file/glob/browser upload/package smoke working_dir`，workspace 内 symlink 指向外部必须失败；需要 workspace 外访问时走 host privilege elevation，而不是静默放宽文件工具。当前已完成核心覆盖。
10. 增加安全 profile：`trusted-local / guarded-local / sandboxed / strict / host-privileged`。`guarded-local` 适合作为 CLI/TUI 默认；remote channel、第三方 skill、package smoke、automation 默认使用 `sandboxed` 或 `strict`；`host-privileged` 只允许显式升级。当前已完成 profile-to-policy baseline。
11. 安全 profile 与现有 approval mode 联动：`manual` 需要人工批准高危动作；`review` 先走 reviewer，必要时人工；`yolo` 只能在 sandbox/strict 内自动放行，不允许静默 host-privileged elevation。当前已完成 baseline。
12. 增强 shell guard：继续保留命令 allowlist、danger denylist、metadata/private URL、workspace escape、minimal env，并补充 `curl|sh`、`wget|sh`、`bash <(...)`、base64 decode 执行、`python -c/node -e` 等高风险模式的风险分级、审批提示和 audit。当前已完成 baseline。

参考来源：

- `temp/pi-go/internal/auth` 的 OAuth PKCE/device-code/manual-code 抽象、Codex OAuth provider 配置、TLS preflight、home `.env` 保存逻辑。
- `temp/pi-go/internal/provider/openai_codex.go` 的 Codex/ChatGPT backend provider 边界：OAuth token 不等同于普通 OpenAI Platform API key。
- `temp/pi-go/internal/tools/sandbox.go` 的 Go `os.Root` 文件边界思路。
- `spf13/afero` 的文件系统抽象与内存 FS 测试价值；Afero 只做接口/测试/组合，不作为安全边界。
- GoDex 现有 config manager、provider registry、tool permission runtime、Docker execution backend、Settings/doctor。

验收标准：

- 用户在任意项目目录执行 `godex login openai/codex` 后，可通过全局 provider 直接启动 CLI/TUI/Web session，不需要每个项目重复配置 token。
- OAuth token、普通 OpenAI API key、Anthropic key 的来源、provider 类型和模型 profile 在 Settings/doctor 中清晰可见且 secret 被 mask。
- 旧项目的 `godex.yaml/.env/.godex` 不迁移也能继续运行，迁移命令不会把项目 memory/session 错迁到全局。
- `read_file/write_file/edit_file/attach_file` 对 workspace 外路径和 symlink escape 有回归测试。
- remote channel、package smoke、第三方 skill 默认不拿宿主 shell 权限；`yolo` 不会让远程入口自动获得 host privilege。
- 云服务运维这类需要完全访问权限的场景仍可通过 `host-privileged` + `review/manual approve` 显式开启，并在 timeline/security audit 里记录原因、scope、命令和路径。
当前 v1 验收已通过；后续工作主要是把 sandbox/container/SSH 的隔离强度继续加深，并补更完整的 provider health diagnostics。

## 暂缓项

这些能力有价值，但当前不是最高 ROI，除非有明确用户场景再启动：

- Native desktop app
- voice/canvas/mobile
- public sharing/eval dataset
- 完整第三方代码插件市场
- 完整 container/SSH sandbox hardening
- 复杂 A2A/remote agent peer protocol
- 子 agent 直接长期通信/mailbox
- workflow DAG priority queue / distributed scheduler

## 推荐实施顺序

短期建议按下面顺序推进：

1. P0 runner phase checkpoint + mid-turn injection
2. P1 agent identity/manifest v1
3. P3 shell/background execution contract hardening
4. P2 context compression baseline + model-backed session summarization
5. P4 channel contract + stream delta coalescing
6. P5 package/skill fixtures and smoke tests
7. P6 global home + OAuth provider login + safety profiles

这个顺序的理由是：先让长任务“不容易摔倒”，再让 agent “身份和权限可治理”，然后补执行安全和 memory 审计，最后再扩大外部接入和生态面。

## 文档维护规则

- 已完成的 v1 设计不要长期留在主体 roadmap 中，移动到“已完成里程碑归档”。
- 新需求先进入对应 P0-P6 小节，不急着拆实现细节。
- 一旦进入实施计划，单独写具体 plan 或 issue，避免把 roadmap 变成超长设计文档。
- 每次完成一个阶段后，更新“当前基线”和对应验收状态。
