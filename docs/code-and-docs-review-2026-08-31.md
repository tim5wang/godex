# GoDex 全模块代码与文档审查（2026-08-31）

> 状态：Active / Completed review（问题清单保持 Active；本文不代替产品设计文档）
> 范围：当前 `go list` 可见 85 个 Go package（其中 81 个 `internal` package）、17 个 Web feature 目录、78 份 Markdown 文档。
> 基线：`8acd0f60bfbb9e52def400d21eb5c8e48eb4a598`；审查开始时仅有用户未跟踪文件 `docs/cache-hitrate-analysis.md`。

## 后续修复状态（2026-08-31）

审查后的确定性修复已继续落地：Agent Step allowlist 已收敛到共享 `core/toolfilter` 并以表驱动测试固定 deny-overrides-allow、通配符和空列表语义；两个空 LLM 响应测试夹具已补齐终态响应；latest persistent user message、best-effort directory size、三份语义完全相同的 rune 截断 helper 已共享。语义不同的 compress/memory 截断实现保留独立，避免错误 DRY。

新增 `internal/architecture` import gate，禁止新增 `domain→core/platform`、`platform→core`、`core→tools` 依赖。最初记录的 8 条生产依赖例外现已全部迁移并删除，`importExceptions` 为空；门禁继续阻止同类债务重新进入。

后续产品决策已冻结 P0-1：`always_on` 是 Agent 模板显式可选且会话内固定的 bundle，不是宿主隐式能力；模板基线精确等于 `Tools ∪ Bundles` 展开工具，clear/reset 恢复该基线，仅 `default` 空模板保留旧标准模式兼容。相关 `internal/toolruntime`/`internal/tools` 测试已全部通过，`internal/agent` 的 activation/capability 失败已清零；随后剩余的 2 项 loop-guard fixture 与 1 项 oversized tool-result fixture 也已按当前生产语义修复。

HTTP composition root 已完成五批增量拆分：共 138 条路由迁入 19 个按资源域划分的 registrar；`NewHandlerWithRuntime` 从约 1,883 行降到约 85 行，图复杂度从 266 降到 5、cognitive 从 293 降到 7。新增静态 route ownership 测试，禁止生产路由字面量被多个文件重复注册；构造边界也已由命名 `Dependencies` 承接，旧签名只保留为兼容入口。Usage 与 Anthropic gateway 已进一步分文件，避免协议转换继续挤入 usage registrar。

Web 源码默认预算已从 1000 收紧到 900 行。`ChatPage` 的提交状态机、`MemoryPage` 的卡片/表单 helper、`TaskBoardView` 的 dialogs/execution UI 均已按职责迁入同 feature 文件，三个主文件都低于默认预算；当前只保留 `SettingsConfigFields.tsx` 的 915 行精确迁移例外，不能继续增长，降到阈值后测试会要求删除。此前 Skills、Settings 主页面、共享 types/API/messages 与全局 stylesheet 的拆分继续由同一门禁保护。该门禁阻止债务扩大，不代替后续 feature vertical slice 拆分。

Go 生产函数新增复杂度 40 的增量门禁。本轮已拆分 TaskBoard dispatch、ACP prompt event stream、config doctor 和 conversation runner 四个核心状态机并删除其例外；当前只剩 browser、cron、LSP 三个工具构造器以精确历史分数受限。命令服务已按 skills/packages、memory/notes、runtime 三个命令域拆分，公共 dispatch/metadata 留在 `commands.go`。

前端 `typecheck` 原先对只有 project reference 的 solution `tsconfig.json` 执行 `tsc --noEmit`，实际没有检查 app source，因而漏过 Skills 拆分时遗失的 `Metric` 引用。脚本现改为 `tsc -b`，并让 `SkillsPage` 复用已迁移到 `PackagePanels` 的同一组件；真实 project-reference 类型检查和生产构建均已通过。

配置映射新增 schema/setter/stored/effective 契约测试，并修复 8 个 schema 路径的不完整映射：`security.screener.*` 5 项与 `tools.execution.scope_write` 原先无法通过 Web 配置写入，`heartbeat.default_watchdog_script` 与 `control.credential` 原先缺少 stored/effective view；credential 仍按 secret policy 掩码。setter 已按配置域拆分，doctor 检查也已按 config/model/channel/security/storage 等阶段拆开。

## 1. 工具选择与成本控制

主工具选用仓库已经接入的 **codebase-memory-mcp**，不再引入第二套长期索引。最终核验时索引精确指向本仓库，状态 `ready`，且 `parse_partial=0`、`skipped=0`；节点/关系数量不写入文档，避免后台增量索引造成无意义漂移。未索引内容是 `.git`、构建产物、截图、node_modules 和 wasm 二进制，不影响源代码结构结论。

成本策略：先用 `get_architecture`/`search_graph`/图查询做全局收敛，再对高复杂度、高 fan-in/fan-out、跨层依赖和相似实现调用 `trace_path` 与精确源码片段，最后用编译器、类型检查和测试验证。LSP 更适合单符号编辑；本任务要覆盖数百文件和跨模块关系，知识图谱单位上下文成本更低。图查询不支持的复杂 `SIMILAR_TO` 条件改为拉取边后在结果层筛选。

证据等级采用 Auditor（Tier 3）：结构范围全量，物质结论读取精确源码；索引覆盖见本文末尾。负面结论只限定在已列 scope，不把“索引无结果”当作绝对不存在。

## 2. 结论摘要

项目的功能覆盖和测试数量都很强。以下是审查时的主要结论；已完成项同步标注，避免把历史基线误读成当前状态：

1. 默认工具面、system prompt、README 和测试对“按需加载”的定义曾互相矛盾；现已冻结模板 `always_on` 与 activation baseline 语义并修复相关测试。
2. Go 全量测试审查时有 15 个失败；确定性 lifecycle/fixture、activation policy 和最后 3 个旧 fixture 均已修复。Web 原有 1 个 `writeScope` fixture 失败也已修正。
3. `httpapi`、配置映射和多个 Web 页面再次形成超大 composition root；先前的大文件拆分只解决了旧热点，没有建立持续阈值。
4. domain storage、shared protocol contract 与 teammate tool adapter 的 8 条反向依赖已完成迁移；architecture import gate 的例外清单为空。
5. 安全相关 step allowlist 已收敛到共享 evaluator；latest-user-message、目录大小与同语义截断 helper 也已共享，语义不同的实现继续独立。
6. 文档索引、5 个失效链接和 Agent Step/Responses/Workflows 状态已经修正，并加入 `make docs-check`。

## 3. 优先级问题清单

### P0 — 先恢复契约一致性

#### P0-1 默认工具面存在三套互相冲突的事实源（已冻结语义并修复）

- `internal/agent/tool_registration.go` 把 `web`、`mcp` bridge、`planning`、`lsp`、`core_code` 设为 `DefaultActive`，taskboard 存在时也默认激活。
- `internal/agent/system_prompt_dynamic.go` 的 coding prompt 仍要求“先用 tool_exchange 启用 web/MCP 等重能力”。
- `README.md` 和 `docs/user-guide.md` 也描述 web/MCP 按需开启。
- 同一个 `context_test.go` 既有“所有 query 预载 web”的测试，也有旧的精确 active-tool 集；全量测试实际得到额外的 `call_mcp_tool/list_mcp_tools/taskboard/ui_card`。

影响：模型收到的可调用 schema 与行为指引不一致；默认 token 成本上升；required bundle 校验被默认激活绕过；清空 session 后“重置 transient bundle”的语义失效。

决策与落地：canonical `ToolHandler.Catalog()` 保留 `always_on` 供模板编辑器选择；`tool_exchange` 使用过滤后的可变视图，显式开关 `always_on` 会返回 `template-pinned` 错误。`ApplyTemplate` 记录精确工具基线，`ClearMessages` 恢复该基线而非宿主全局默认。runtime prompt 从 canonical catalog 展示已激活的 `always_on [template-pinned]`，但不把它广告为可按需开启。required-capability 测试显式应用精简模板，不再偶然依赖会演进的注册默认集。

#### P0-2 全量测试不是绿色基线

`go vet ./...` 与 Web typecheck 通过；首次 `go test ./...` 失败 15 项：

- `internal/agent`：默认 tool schema、tool availability prompt、required-tool validation、loop guard、large tool result、clear/reset、durable subagent capability 等 10 项。
- `internal/services/backend`：1 项模型 profile 持久化测试因 fake caller 连续空响应退出。
- `internal/tools`：tool_exchange catalog/推荐/未知 bundle 等 4 项。

这些失败不是同一种原因：多数是默认 bundle contract 漂移，少数是 fake LLM fixture/异步清理隔离。对三个失败 package 的隔离重跑还暴露了后台任务未结束导致 `t.TempDir` 清理失败的波动项。当时的处理原则是先固定 P0-1 policy 再修 fixture；该步骤已完成，剩余 3 项已明确与 activation 无关。

#### P0-3 文档实现状态倒置

- Agent Step 主设计/细节写“未实现”，但 Phase A routes、Phase B SDK、Phase C `<godex-step>` 与测试均存在。
- Responses plan 写“未实施”，但 `/v1/responses`、provider client 与 v1.4 release notes 已存在。
- Workflows board 写“已实施”，但 commit `c9612c1` 删除页面并由 Business Agents 取代，只保留 `UiCardView`。

本次已修正文档状态，详细映射见 [feature-implementation-matrix.md](./feature-implementation-matrix.md)。

继续逐项核对 Planned/Draft 文档后，又确认并修正了以下正文级漂移，而不只是改标题：

- AgentTemplate M1–M3、Biz key M4 P1、PJM M5 P1–P3 已实现；导入导出/NL 生成/预算硬限制仍待做。
- Node Mesh Phase 1–3 与 forward/exec/Web Push 已实现；删掉 Phase 2/3 重复的旧未勾选清单，保留 PWA/Android/跨节点编排等真实缺口。
- pluginrt P-A routes、P-C services、P-D schedule 已实现且可逆；P-B 通用动态 Web UI slot 仍 Partial。
- TaskBoard research、touched/observed paths、dispatch/merge path conflict、手动 reconcile P0 已实现；自动 reconcile/history/dry-run、depends_on/经验回流仍 Planned。
- Cache stable/dynamic prompt 分拆和 session 24h retention baseline 已实现；自适应 TTL 未实现。
- Bubble Tea 多入口文档已是历史实现记录；readline REPL 已被 TUI/`godex ask` 取代。
- Voice 的 `ui_card`、voice-engine WebSocket、TTS/stream baseline 已实现；turn middleware、plugin config/UI、OpenAI REST/Realtime adapter 仍 Planned。
- 旧四大文件和后续 Chat/Memory/TaskBoard 热点均已按职责拆分，并由 Go 复杂度与前端 900 行预算门禁防止回退。
- 可视化设计正文仍声称 LongTask API/前端类型没有 graph；实际 `LongTaskView.Graph`、前端类型、`AgentGraphDiagram` 和回归测试均已落地，现已改为“数据缺口已关闭”。
- Agent Step/MCP 文档只写 stdio，且把 `session_required` 描述成已维护 `Mcp-Session-Id`；实际已有 Streamable HTTP JSON/SSE baseline，但 session id 保持尚未实现。代码注释、设计与两份用户手册现已对齐这一边界。
- Node Mesh、Scope 与 AgentTemplate 文档的 As-Is 段落仍使用“当前”措辞；现已明确标成实施前历史快照，避免与文首完成态冲突。
- 根 `godex --help` 漏写实际支持的全局 `--session spec`；现已补入代码内 help 并加回归断言。Slash command 继续由 `AvailableMetadata()` 同时驱动 help、Web/TUI/ACP/IM 入口。

### P1 — 收紧架构边界

#### P1-1 HTTP API composition root 重新膨胀（registrar 拆分已完成）

`NewHandlerWithRuntime` 位于 `internal/runtime/httpapi/httpapi.go`，单函数约 1,883 行，图复杂度 266、cognitive 293、outbound call edge 192；文件共 2,339 行。虽然 Files/Usage/Voice/Steps 等已拆 route 文件，大量 config/node/provider/package/automation/memory/session handlers 仍以内联闭包集中在一个函数。

建议：保留同包拆分，把每个资源域收敛为 `registerConfigRoutes`、`registerControlRoutes`、`registerSessionRoutes` 等；构造参数改为窄 `Dependencies`，避免继续拉长函数签名。新增 route ownership 测试，禁止同一路径在多个注册器重复。

后续状态：138 条静态路由已迁入 19 个 registrar，route ownership 测试已落地；主函数现约 85 行、复杂度 5/cognitive 7。命名 `Dependencies` 已成为组合边界，旧长参数签名保留兼容；Anthropic gateway 也已从 `routes_usage.go` 迁入独立 registrar 文件。

#### P1-2 配置 schema 与读写映射不是单一事实源（增量契约门禁已完成）

`setStoredValue` 原先是 446 行 switch（复杂度 215、cognitive 446），同文件还有反向的 stored/effective value 映射。新增字段必须同时修改 schema、setter、getter、mask/clear-secret 多处。

建议：不是引入反射框架，而是先把字段按域拆成小 handler table；每个 schema field 绑定 get/set/secret policy，并加 round-trip property test，逐步消灭平行 switch。

后续状态：新增 AST 契约测试，要求 `baseSchema` 的每个字段都能被 `setStoredValue` 处理，并要求 stored/effective map 覆盖每个 schema path；round-trip 测试覆盖本轮修复的普通字段与 secret credential。由此发现并补齐上述 8 个不完整路径。setter 已按 17 个一级配置域拆分，tools 再拆为 10 个子域，并由 Go 复杂度增量门禁约束回退。

#### P1-3 Web 页面和共享文件再次超过可维护阈值（增量门禁已完成）

Chat、Memory、TaskBoard 的主页面现均低于 900 行默认预算：提交状态机、Memory 卡片/表单 helper、TaskBoard dialogs/execution UI 分别留在各自 feature 内独立维护。共享 styles/messages/API/types 以及 Skills、Settings 主页面也继续处于预算内；唯一例外是 `SettingsConfigFields.tsx`，以 915 行精确上限约束。

`big-file-split-plan.md` 的旧目标及后续热点拆分均已完成。architecture test 对 `ui/web/src` 执行 900 行默认预算，例外必须固定到当前大小且在文件回落后删除。拆分均保留在原 feature 或兼容 barrel 内，没有引入新的跨层状态容器；CSS 拆分前后 459 个顶层节点顺序及生产构建的 4 个 CSS 资产字节完全一致。

#### P1-4 Domain 与 infrastructure 边界不纯（已修复）

原问题是 `internal/domain/message`、`task`、`todo` 直接管理 JSON/文件路径和原子写；`domain/events`、`history`、`message` 与 `platform/tooling` 反向依赖 `core/protocol`，`core/teammate` 还依赖具体 `internal/tools`。

现已完成三部分迁移：task/todo/message 只依赖各自的 Repository 接口，JSON、路径和原子写实现迁入 `platform/localstore`；共享协议包物理迁入 `internal/contracts/protocol`；teammate loop 通过窄 `LoopToolContext`/`LoopToolFactory` 注入具体工具，默认 adapter 位于 `tools/teamtools`。原有文件布局与 JSON 格式保持兼容。

#### P1-5 权限/allowlist 算法重复（已修复）

`agent.stepListAllowsTool` 与 `httpapi.stepListAllows` 是逐字等价的 27 行实现：都处理 `*`、`!x`、`x/*`，但分别服务 runtime narrowing 与 Agent Step HTTP narrowing。图 trace 显示两条独立调用链。

现已新建无外部依赖的窄包 `core/toolfilter`，Agent 与 HTTP API 两侧共同调用，并以表驱动测试固定 deny-overrides-allow、通配符和空列表行为。

#### P1-6 代码级模块边界只有约定，没有 enforcement（已完成）

Go 编译器只防 import cycle，不防 `domain -> platform`、`platform -> core` 等方向错误。architecture test 现通过 `go list -json` 拒绝新增禁止层间边；8 条存量依赖完成实体迁移后，精确例外清单已经清空。

### P2 — 有选择地收敛重复与维护成本

- `latestPersistentUserText` 已归入 protocol message query helper。
- `dirSize` 已收敛为 `fsutil.DirSizeBestEffort`，名称和测试明确“忽略遍历/读取错误”的语义。
- agent、channel、Feishu renderer 三份等价 `truncateRunes` 已下沉 `textutil`；compress 与 memory 的非正 limit/trim 语义不同，继续保留独立实现。
- `unique/clean string list` 和 `clone map` 分散在多个包。对三行 clone 不必强行跨层复用；只合并带规范化/去重语义的版本，避免为了 DRY 反而制造依赖。
- ACP/TUI 的 `numFromAny` 仍是低优先级相似 helper；Relay/CDP/forward 的随机 ID 已统一调用带 prefix/entropy 参数的 `platform/idgen.New`，并修复重复拼接 `cdp-` 前缀的问题。

## 4. 模块审查矩阵

标记：🔴 需近期处理；🟡 有明确技术债；🟢 边界相对清晰；⚪ 示例/资源模块。

### Go 后端

| 模块 | 结论 | 主要观察 |
|---|---|---|
| `cmd/godex` | 🟡 | 仍包含 relay/CDP adapter；连接 ID 已统一到 `platform/idgen`，入口后续仍应只做装配。 |
| `internal/acp/server` | 🟡 | prompt event stream 已从 handler 拆为独立状态对象并进入复杂度预算；协议转换职责仍宽，`numFromAny` 与 TUI 的相似实现优先级较低。 |
| `internal/agent` | 🔴 | 57 个源码文件仍承载 composition、context、tool registry、workflow/longtask/subagent；默认 bundle 与后续 3 个旧 fixture 失败均已修复，剩余问题是职责面过宽。 |
| `internal/app` | 🟡 | CLI lifecycle 合理，但 `run.go` 1,479 行；root help 是较好的代码内事实源，应继续由 metadata 生成。 |
| `core/auth`, `idempotency`, `lease`, `modelcontext`, `notes`, `persistence`, `scope`, `templates` | 🟢 | 小而内聚，测试覆盖基本匹配职责；保持窄接口。 |
| `core/background`, `claudeimport`, `insights`, `llm`, `mcp`, `providers`, `security` | 🟢/🟡 | 结构可接受；`instructions` 无直接测试，`claudeimport` 单文件 666 行但职责仍单一。 |
| `core/compress`, `conversation` | 🟡 | runner 的请求、模型恢复、终态与工具循环已拆出 execution slice 并消除复杂度例外；client/compaction 仍是后续状态机拆分候选。 |
| `core/config` | 🟡 | schema/value 契约门禁与 8 个映射缺口已修复，setter 和 doctor 已按检查域拆分；resolve 与 secret policy 仍需关注增长。 |
| `core/media` | 🟡 | processor 955 行同时处理格式、转录与 provider 路由，适合拆 pipeline stage。 |
| `core/memory` | 🟡 | manager 1,245 行但已拆 sidecar/extract/layers；下一步应收窄 manager facade，而非再加新入口。 |
| `core/packages`, `core/skill` | 🟡 | package 1,282 行、skill 多个 600–1,043 行；manifest/安装/quality/runtime activation 边界仍交叠。 |
| `contracts/protocol` | 🟡 | 共享 wire contract 已从 core 下沉到中立层，fan-in 很高；应稳定，避免继续吸收业务 helper。 |
| `core/teammate` | 🟡 | 具体工具已通过窄 factory adapter 注入；文件仍约 700 行，同时处理 team state 与运行循环。 |
| `domain/automation`, `eval`, `security` | 🟡 | 纯类型但无直接测试；需要 serialization/compatibility contract test。 |
| `domain/events`, `history` | 🟢 | 共享 wire 类型统一依赖中立 `contracts/protocol`。 |
| `domain/message`, `task`, `todo` | 🟢/🟡 | manager 已只依赖 Repository 接口；本地 JSON adapter 位于 `platform/localstore`。 |
| `platform/browserutil`, `fsutil`, `logger`, `servicecontrol`, `storagegc`, `stringutil`, `textutil`, `workspacefs`, `workspacepath` | 🟢/🟡 | 多数窄而稳定；目录大小已共享为 `fsutil.DirSizeBestEffort` 并有直接测试，stringutil/workspacepath 仍缺直接测试。 |
| `platform/tooling` | 🟡 | 共享 wire 类型已改依赖 `contracts/protocol`，反向依赖消除；2,544 行中的 shell guard、file IO、execution config 仍适合继续拆。 |
| `pluginrt`, `plugins/taskboard`, `wasmrt` | 🟡 | 插件 ownership/lifecycle 测试较强；TaskBoard action dispatch 已按命令组拆分并消除复杂度例外，自动 reconcile/history/依赖拓扑与通用 UI slot 仍未实现。 |
| `runtime/channels`, `feishu`, `weixin` | 🟡 | adapter 边界合理；channels.go 1,695 行，reply planning/identity/routing 应进一步分离。 |
| `runtime/cron`, `heartbeat`, `webui` | 🟢/🟡 | cron/heartbeat 服务各约 800 行但测试充分；webui 很薄。 |
| `runtime/httpapi` | 🟡 | 138 条路由已迁入按资源域划分的 registrar，主注册函数约 85 行并有 ownership gate；命名 `Dependencies` 已承接构造边界，Anthropic gateway 与 Usage 也已分文件。协议 gateway 和 UI API 仍在同包，依赖面仍宽。 |
| `sandbox` | 🟢 | 接口边界明确，后续 hardening 可在实现内演进。 |
| `services/backend` | 🟡/🔴 | 已从旧 7K 单文件拆成 31 个文件，测试已恢复通过；仍有 30 个 internal imports，是高耦合 facade。 |
| `services/commands` | 🟢/🟡 | metadata 继续驱动 help；执行逻辑已按 skills/packages、memory/notes、runtime 三个命令域拆分，公共 dispatch 与 metadata 保持集中。测试文件仍大，但不参与生产复杂度预算。 |
| `services/evalharness`, `historysearch`, `localbash`, `nodeobs`, `noderegistry`, `sessionadmin`, `sessionrepair`, `usage`, `webpush` | 🟢/🟡 | 职责基本清晰；usage store 1,046 行、history sidecar 725 行需关注增长。 |
| `services/relay` | 🟢/🟡 | 14 源文件、20 测试文件，拆分和测试最好；协议/forward/hub 边界清楚。 |
| `sessiongraph`, `sessionstore`, `workerruntime` | 🟢 | 接口小、测试存在，是 2.0 抽象的可复用基础。 |
| `toolruntime` | 🟡 | permissions 1,889 行仍是大文件；模板 activation policy 已冻结，catalog/tool_exchange 契约测试通过。 |
| `tools`, `tools/teamtools` | 🟡 | 47 个实现文件，具体工具拆分较好；toolruntime_aliases 有高 fan-in，别继续把所有兼容 alias 堆在单文件。 |
| `tui/mintui` | 🟡 | session 2,289 行、popup_longtask 1,157 行；UI state/update/render 仍集中。 |
| `uiassets`, `version` | ⚪ | 嵌入资源与版本小模块，无实质架构问题。 |

### Web feature

| Feature | 结论 | 主要观察 |
|---|---|---|
| `agent-templates` | 🟡 | 页面 519 行；与 roles/biz key/template 三套模型仍处收敛期。 |
| `automation` | 🟡 | 页面 606 行，Cron/Heartbeat 同页可接受，后续触发器增长时拆 tab controller。 |
| `business-agents` | 🟡 | 页面 796 行，实际已替代 Workflows；文档状态此前未同步。 |
| `chat` | 🟡 | 主控制器已低于 900 行，提交/附件/乐观消息状态机迁入 `chatSubmission.ts`；session、timeline、review、task center 的组合职责仍较多。 |
| Chat layout/chrome | 🟢/🟡 | 活跃实现已归入 `features/chat/layout`；`/chat-v2`、持久化 key 与 CSS class 仅作为外部兼容协议保留。 |
| `files` | 🟡 | panel 619 + page 421 + tree 376，编辑/浏览/diff 状态分层尚可。 |
| `memory` | 🟡 | 主页面已低于 600 行，viewer/digest/audit/context/metric 与 card/form helper 均已独立；inbox/suppression/restore/search 状态仍可继续按 tab 拆。 |
| `nodes` | 🟢/🟡 | list/detail/forward/join 已拆，结构相对健康。 |
| `notes` | 🟢 | 368 行，边界清楚。 |
| `preview` | 🟢 | 159 行，窄适配器。 |
| `settings` | 🟡 | 主页面已从 2,280 行降到 507 行；配置字段编辑器、纯配置转换模型、provider/security/channel/service 状态面板与 MCP panel 均已独立。字段编辑器是当前唯一 900 行预算例外，以 915 行精确上限约束。 |
| `skills` | 🟡 | 主页面已从 1,439 行降到 868 行；工具健康分析和 package/quality/prompt/command/role 表格已拆入同 feature 组件，安装与 source/session 编排仍集中在主页面。 |
| `taskboard` | 🟡 | 主 view 已低于 300 行，dialogs、execution row 和 action buttons 迁入同 feature 组件；ledger/reconcile/agent template 交互仍需由后端能力边界约束。 |
| `tasks` | 🟢 | selector/chip 小模块。 |
| `terminal` | 🟢/🟡 | 279 行，生命周期和 xterm adapter 基本清楚。 |
| `usage` | 🟡 | 主页面 799 行但 charts/session/cache 已拆，是可复制的拆分方向。 |
| `workflows` | ⚪ | 页面与空壳 feature 已删除，通用 `UiCardView` 已迁入 `components`。 |

### 脚本、示例与文档

| 模块 | 结论 | 主要观察 |
|---|---|---|
| `scripts/` | 🟢/🟡 | `scripts/smoke.sh` 提供统一发现/执行入口与 `--json` 结果；各场景仍保留独立脚本。 |
| `examples/wasm-*` | ⚪ | 示例与 testdata 存在可接受重复，不应和生产重复项一并抽象。 |
| `examples/evals` | 🟡 | 多个独立 Python entry point，适合保留；结果目录已排除索引。 |
| `docs/` | 🔴→🟡 | 56 份顶层、78 份总文档；顶层均有受控状态并进入索引，22 份 `docs/superpowers` 执行计划按 Historical 目录管理。 |

## 5. 完成性审计

本节不是“抽样覆盖”，而是按用户可见入口和模块清单做闭环核对：

| 检查面 | 全量基数 | 核对结果 |
|---|---:|---|
| Go package | 82（其中 78 个 `internal`） | §4 已逐包或按同职责窄包组给出结论；高风险包单独列项，没有未归类 package。 |
| Web feature 目录 | 17 | §4 全部覆盖；12 个 `builtinApps` 导航入口逐项映射，`preview/terminal/tasks` 为嵌入子功能，`chat-v2/workflows` 的遗留命名单独指出。 |
| 根 CLI 用户命令 | 22（不含 help/flag alias） | `Runner.Run` 与 `rootHelpText` 逐项核对；命令域全部落入功能矩阵，唯一发现的 `--session` help 缺口已修复。 |
| Slash command | 29 个 metadata 项 | `AvailableMetadata()` 是 help、Web、ACP、TUI/IM 注册的共享事实源，并有 `TestAvailableMetadataDrivesHelpText`；用户指南只保留语法说明。 |
| HTTP/插件路由域 | session/events、OpenAI/Anthropic、Agent Step/Biz、files/git/preview/terminal、config/security、memory/notes、packages/skills/MCP、automation/channels/push、nodes/relay、usage/voice/TaskBoard | 每个域均已映射到功能矩阵；具体 path 以 registrar 与 contract tests 为准，不在设计文档维护第二份“精确总数”。 |
| Markdown 文档 | 78（56 顶层 + 22 个 `docs/superpowers` 历史执行计划） | 顶层逐篇核对状态、正文实现断言与本地链接；嵌套计划按 Historical 证据扫描，不作为当前产品契约。用户原有未跟踪分析文档保持未修改。 |

功能矩阵在最终交叉核对中补上了三个此前隐含在其他行里的独立域：Slash commands、多模态附件/ArtifactPaths、Web Push。这样 12 个导航 app、22 个根命令、主要 route registrar 与所有顶层产品文档都能找到明确归属。

## 6. 建议执行顺序

1. ~~冻结默认 tool activation policy，修复 activation/capability contract tests。~~ 已完成；3 个 `internal/agent` 旧 fixture 失败也已修复：loop-guard 测试显式固定阈值，oversized-result 测试改用不可被重复行压缩的大结果。
2. ~~抽取共享 allowlist evaluator，并让 Agent Step/template/biz key 三层 narrowing 共用一套测试。~~ 已完成共享 evaluator；两条实际 narrowing 调用链已共用。
3. `NewHandlerWithRuntime` 已由命名 `Dependencies` 组合边界承接（旧签名保留兼容），`setStoredValue` 已按配置域拆分；HTTP registrar 已完成五批，累计迁移 138 条路由，配置 schema/value 契约门禁与 8 个映射缺口已修复。
4. ~~完成 architecture import test 并迁移 8 条既有违规依赖。~~ 已完成，精确例外清单为空；Go 函数复杂度 40 的门禁也已落地，四个核心例外已删除，只保留三个工具构造器历史上限。
5. ~~拆分前端 Chat/Memory/TaskBoard 热点并收紧预算。~~ 已完成；默认预算为 900 行，仅 Settings 字段编辑器保留 915 行精确例外。
6. 每次功能合并同时更新 [feature-implementation-matrix.md](./feature-implementation-matrix.md)；本地与 release 统一执行 `make verify`，其中包含 docs check。

## 7. 验证与限制

- 通过：统一 `make verify`（`go test ./... -count=1`、`go vet ./...`、docs-check、Web typecheck、325 项 Web unit tests 与生产构建）；`git diff --check` 与 release-check shell syntax 也单独通过。
- 通过：`go test ./internal/app ./internal/core/mcp`，以及 LongTask graph 契约定向测试；新增 root help 和 MCP 注释/文档收口没有引入回归。
- 通过：templates、pluginrt、TaskBoard plugin、usage、httpapi、backend TaskBoard/reconcile、agent template/dynamic prompt/package runtime、conversation prompt-cache/retention 定向测试。
- Web 测试原有 1 个失败已定位为 fixture 漏写 `writeScope` 并修正；最终重跑 32 个 test files、325 个 tests 全部通过。
- 初始 `go test ./...` 有 15 个失败；确定性 fixture/lifecycle 与 activation policy 修复后曾只剩 3 个 `internal/agent` 失败。它们均为旧 fixture 与当前生产语义脱节：两个 loop-guard 测试隐式依赖旧阈值 3，而当前默认值为 8；oversized-result 测试的 4000 行重复文本会先压缩为单行。现已分别显式固定测试阈值并改用不可高度压缩的大结果，三项定向测试通过。
- 8 条架构依赖完成迁移后，`go test ./... -count=1` 全量通过；`go vet ./...`、`make docs-check` 与 `git diff --check` 再次通过。
- clear/reset、tool schema/prompt fixture、required tool/web capability 与 `tool_exchange` catalog/count 契约测试均已通过；没有通过删除新默认工具来迎合旧 fixture。
- 最终 `check_index_coverage` generation 与当前 metadata 匹配；本轮 agent fixture、domain repository、localstore、contracts/protocol、teammate adapter、architecture gate 与文档路径均为 `no_recorded_issue`，相关 domain/tooling/teammate/contracts/architecture scope 无记录缺口。报告不固化 generation 时间戳，避免后台增量索引后形成伪过期结论。
- `internal`、`docs`、`examples` 的已知缺口仅为 embedded dist、图片、wasm 二进制、eval 结果和 `docs/superpowers/tmp`；代码/文档结论没有依赖这些二进制资产，`superpowers` 计划/spec 已用源码 heading/内容检索补查。
- 覆盖信号仍是 best-effort；“无记录缺口”不等于数学上的完整性证明。
- `SIMILAR_TO` 是候选证据，不等于所有重复都应抽象；示例/test helper 和三行 clone 明确排除在强制 DRY 之外。
