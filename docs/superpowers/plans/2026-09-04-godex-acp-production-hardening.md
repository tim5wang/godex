# GoDex ACP 生产语义加固与缺口补齐实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (- [ ]) syntax for tracking.

**背景（依据调查报告）：** GoDex 已在 ACP 上完成双向闭环（Server 端约 95%、acp_agent 委派工具约 90%、63 个测试全绿）。本计划针对报告中识别的「完成度中等 / 已知缺口」逐项补齐：

1. Whole-turn Harness（阶段 C）约 75%：CLI/API 缺少正式 Harness 覆盖入口、只转发最后一条用户消息、外部进程不受内部 permission interceptor 完整约束、后代进程治理不足。Web 统一由新建会话时选择的 Agent 模板固定 engine。
2. 研究文档中「再封装为 PiHarness 接管完整 Turn」仍 ⏳ 未做。
3. 已知缺口：acp_agent 工具每次调用仍启新进程、客户端提议的 MCP servers 只记录审计不实际 spawn、生产 provider/streaming/credential 语义待加固。

**Goal:** 把 ACP 从「MVP 可用」提升为「生产语义闭环」：正式 Harness 选择器、完整多轮上下文转发、外部引擎权限纳入 GoDex 审批流、进程树治理、工具层会话复用、客户端 MCP 桥接策略化，并完成 PiHarness 再封装与文档闭环。

**Architecture:** 保持 ACP 为统一后端之上的适配器，不改变 GoDex Agent 主循环。所有改动集中在 internal/agent/acp_harness.go（Whole-turn Harness）、internal/tools/acp_agent.go 与 acp_bridge.go（client 侧）、internal/services/backend/turn.go 与 templates.go（选择器与路由）、internal/acp/server/handler.go（Server 侧 MCP 策略）、ui/web 与 cmd/godex（入口）。

**Tech Stack:** Go 1.26.4、github.com/coder/acp-go-sdk、既有 internal/platform/tooling 进程组助手、internal/core/mcp stdio client、既有后端/session/tooling 包、Go 单元测试与真实 wire smoke 测试。

---

## File Structure

- Modify internal/agent/acp_harness.go：历史/多轮上下文转发、权限桥接入审批流、进程树治理接线。
- Modify internal/agent/harness.go：HarnessTurnInput 增加历史转发所需字段（可选）。
- Modify internal/agent/session_state.go：PiHarness 注册、选择器数据源。
- Modify internal/services/backend/turn.go：harness 路由校验 + 错误反馈。
- Modify internal/services/backend/templates.go / backend.go：暴露会话级 harness 列表。
- Modify internal/services/backend/acp.go：新增客户端 MCP 桥接相关服务方法（若策略启用）。
- Modify internal/tools/acp_agent.go：runACPAgent/OpenACPSession/ACPSession 进程组治理、工具层会话复用。
- Modify internal/tools/acp_bridge.go：terminal 桥进程组治理。
- Modify internal/acp/server/handler.go：客户端提议 MCP servers 桥接策略（默认仅审计）。
- Modify internal/core/config：新增 forward_history、permission_mode、bridge_client_mcp_servers、工具层会话复用开关等配置。
- Modify cmd/godex/main.go：--harness 选择器 flag。
- Modify ui/web/src/features/chat/：会话引擎下拉选择器 + metadata 透传。
- Modify 文档：docs/extension-runtime-user-guide.md 4.3 节、docs/research_of_dsh_for_godex_optimize.md 阶段 C 状态、docs/feature-implementation-matrix.md、docs/vscode-acp.md。

---

## Task 1: 正式 Harness 路由（Agent 模板 + CLI/API metadata 闭环）✅

**现状（已核实）：** 后端 internal/services/backend/turn.go:688 读取 envelope.Metadata 的 harness 键，为空时回退 session.agent.TemplateEngine()；模板编辑器已有 engine 下拉（数据源 RegisteredHarnessIDs()，templates.go）。CLI 增加显式覆盖入口；Web 只使用会话创建时选定模板的 engine，避免对话中切换导致外部上下文重建。

**Files:**
- Modify: cmd/godex/main.go
- Modify: internal/services/backend/backend.go / templates.go
- Modify: ui/web/src/features/chat/ChatPage.tsx、ui/web/src/features/chat/chatSubmission.ts
- Modify: internal/services/backend/turn.go
- Modify: internal/agent/session_state.go（如需校验非法 harness id）

- [x] **Step 1: 后端暴露会话可用 harness 列表**

在 AgentTemplateFormOptions 之外新增会话级数据源（或在 session snapshot 中附 harness_ids 字段），数据来自 probe.RegisteredHarnessIDs()，供 API 诊断和模板编辑器使用。保证 ACP harness 经 live-apply 注册后列表动态刷新。

示例（internal/services/backend/backend.go 的 SessionSnapshot 新增字段）：

    HarnessIDs []string  // json: "harness_ids,omitempty"

- [x] **Step 2: CLI 增加 --harness flag**

在 prompt/send 相关命令（cmd/godex/main.go）增加 --harness acp:pi（或 --engine 别名），透传到 envelope metadata 的 harness 键。非法值由后端返回明确错误，不允许静默回退到 godex（避免用户以为委派成功）。

- [x] **Step 3: Web 使用模板固定引擎**

Web 新建会话继续通过 Agent 模板选择 engine；Chat 状态栏不提供 Harness 下拉，也不随普通消息提交 metadata.harness。显式 per-turn 覆盖只保留给 CLI/API 高级调用方。

- [x] **Step 4: 路由校验**

turn.go 中 requestedHarness 若非空且不在 RegisteredHarnessIDs() 中，返回 NonRetryableTurnError，文案明确列出可用引擎。

- [x] **Step 5: 测试**

运行 go test ./internal/services/backend -run TestHarness -count=1 与 go test ./cmd/godex -run TestHarness -count=1。

新增后端单测：metadata 指定 harness=acp:test 时路由到 ACP harness；非法 harness 报错不静默回退。CLI 测试：--harness flag 写入 envelope metadata。

## Task 2: Whole-turn Harness 完整多轮上下文转发 ✅

**现状（已核实）：** ACPHarness.RunTurn 只取 lastUserMessage，经 ACPContentBlocksForMessage（text + 按能力带 image）转发。持久 ACPSession 跨 turn 保留外部会话上下文，但「resume 失败新建会话」与「首次委派」时外部引擎无 GoDex 侧历史，导致上下文断裂。

**Files:**
- Modify: internal/agent/acp_harness.go
- Modify: internal/core/config/config.go（ACPAgentConfig.ForwardHistoryTurns）
- Modify: internal/agent/acp_harness_test.go

- [x] **Step 1: 配置项**

ACPAgentConfig 增加 ForwardHistoryTurns int（0=仅最后一条，保持现状；N>0=转发最近 N 条用户/助手消息，默认建议 0 以保守起步，文档标注）。

- [x] **Step 2: 历史块构建**

新增 historyBlocks(messages []protocol.Message, includeImages bool, turns int) []acp.ContentBlock：从 input.Messages() 快照取最近 N 条消息（含助手回合，便于外部引擎理解已完成工作），text/image 按能力转块，并附一条明确的 previous-conversation-history 文本块分隔，防止外部引擎把历史当新指令。

- [x] **Step 3: 接入 RunTurn**

仅在「新建/重开外部会话」的场景（liveSession 返回 resumeFailed 为 true 或首次无 sessionID）注入历史块；正常续跑保持只发增量（外部会话已含上下文，避免重复膨胀）。历史块 + 最新用户消息一起经 sess.PromptBlocks 发送。

- [x] **Step 4: 失败测试先行**

新增单测：模拟 resume 失败 + ForwardHistoryTurns=2，断言发送的历史文本块顺序、数量、image 按能力过滤；ForwardHistoryTurns=0 时行为与现状一致（只发最后用户消息）。

运行 go test ./internal/agent -run TestACPHarnessForwardHistory -count=1。预期先 FAIL，实现后 PASS。

## Task 3: 外部引擎权限纳入 GoDex 审批流（不只 warning + deny）✅

**现状（已核实）：** permissionResolver 默认 DenyACPPermissionRequest + 发 warning 审计事件，PermissionPolicy 可插拔但未接 reviewPermissionRequest / pending approval 体系；外部引擎的工具调用不受 GoDex 内建 tool permission interceptor 约束。

**Files:**
- Modify: internal/agent/acp_harness.go
- Modify: internal/agent/agent.go / session_state.go（把 Agent 的权限管理器/审批回调注入 ACPHarness）
- Modify: internal/tools/acp_agent.go（ACPPermissionHandler 增加映射助手）
- Modify: internal/core/config/config.go（ACPAgentConfig.PermissionMode：deny | policy | interactive，默认 deny）
- Modify: internal/agent/acp_harness_test.go

- [x] **Step 1: 配置与注入**

ACPAgentConfig 增加 PermissionMode。RegisterConfiguredACPHarnesses 构造时把 a.permissions 与 a.reviewPermissionRequest（permission_facade.go）注入 harness（仅当模式非 deny）。

- [x] **Step 2: 权限请求映射**

新增 ACPToGodexPermission(req ACPPermissionRequest) tools.PermissionRequest：把外部引擎的 session/request_permission（tool title、输入、选项）映射为 GoDex PermissionRequest（tool name 前缀 acp:<agent-id>:<title>），保留原始 ACP 选项用于回写。

- [x] **Step 3: 决策回写**

根据 GoDex 审批结果（allow_once/task/session/pattern/deny）调用 SelectACPPermissionOption 或 DenyACPPermissionRequest 回写 ACP 响应；interactive 模式下经 pending approval（ApprovePendingPermission/DenyPendingPermission）挂起并支持 ContinueTurnID 续跑，行为对齐内建工具审批。每次请求仍发 acp_permission_request 审计事件。

- [x] **Step 4: 测试**

运行 go test ./internal/agent -run TestACPHarnessPermission -count=1 与 go test ./internal/tools -run TestACPPermission -count=1。

覆盖：默认 deny、policy 自动 allow、interactive 挂起 + 续跑、审计事件必然发出。

## Task 4: 后代进程治理（进程组 kill）✅

**现状（已核实）：** acp_agent.go 的 runACPAgent / OpenACPSession / ACPSession.Close / killProcess 以及 acp_bridge.go terminal 桥全部只调 cmd.Process.Kill()（仅杀直接子进程）。仓库已有现成助手：internal/platform/tooling/tooling_unix.go 的 configureCommandProcessGroup / killCommandProcessGroup（及 Windows no-op 版）。

**Files:**
- Modify: internal/platform/tooling/tooling.go（导出助手，供 tools 包复用）
- Modify: internal/tools/acp_agent.go
- Modify: internal/tools/acp_bridge.go
- Modify: internal/tools/acp_agent_test.go / acp_bridge_test.go

- [x] **Step 1: 导出进程组助手**

在 internal/platform/tooling 增加导出包装 ConfigureCommandProcessGroup / KillCommandProcessGroup（unix 用 Setpgid + syscall.Kill(-pid, SIGKILL)，windows 保持直接子进程 + CREATE_NEW_PROCESS_GROUP）。

- [x] **Step 2: 接入 ACP client 进程生命周期**

runACPAgent、OpenACPSession、ACPSession.Close、killProcess、acp_bridge.go 的 CreateTerminal / kill 路径：创建命令时 ConfigureCommandProcessGroup(cmd)，取消/超时/关闭时 KillCommandProcessGroup(cmd)（替代裸 Process.Kill()）。

- [x] **Step 3: 测试**

unix 下新增测试：spawn 一个会再派生子进程的 wrapper 脚本（如 sh -c 内 sleep 与 wait），取消后断言整棵树退出（syscall.Kill(pid, 0) 返回 ESRCH）。

运行 go test ./internal/tools -run TestACPProcessTree -count=1。

## Task 5: acp_agent 工具层跨 turn 会话复用 ✅

**现状（已核实）：** NewACPAgentTool 的 action=run 走 runACPAgent（每次新进程、新 session）；跨 turn 复用只存在于 harness 层（ACPSession）。工具每次调用启动外部进程开销大，且无法延续外部会话上下文。

**Files:**
- Modify: internal/tools/acp_agent.go
- Modify: internal/core/config/config.go（ACPAgentConfig.ReuseToolSessions，默认 false 保守起步）
- Modify: internal/tools/acp_agent_test.go

- [x] **Step 1: 会话池**

新增按 (agentID, workspace) 键控的轻量 ACPSession 复用池（对齐 harness 的 idle watchdog：空闲超时而非总时限），action=run 优先复用池中会话，进程死后沿用 session/load 到 session/resume 到 session/new 三级回退。

- [x] **Step 2: 工具语义保持**

action=run 结果保留现有 Text/StopReason/Usage/Updates 结构，新增可选 session_id 输出（复用会话时非空）。默认 ReuseToolSessions=false 保持现状，显式开启才复用，避免改变既有行为。

- [x] **Step 3: 测试**

运行 go test ./internal/tools -run TestACPAgentToolSessionReuse -count=1。

覆盖：连续两次 run 复用同一 session_id；进程死亡后自动重连新会话；空闲超时后池中会话释放。

## Task 6: 客户端提议 MCP servers 桥接策略化 ✅

**现状（已核实）：** internal/acp/server/handler.go:80 把客户端提议的 MCP servers 记入 locator.Metadata 的 acp_mcp_servers 键仅审计，注释明确「godex 不代 spawn」。internal/core/mcp 已有 stdio client 能力（list_mcp_tools/call_mcp_tool）。

**Files:**
- Modify: internal/acp/server/handler.go
- Modify: internal/core/config/config.go（ACP.BridgeClientMCPServers，默认 false）
- Modify: internal/acp/server/handler_test.go

- [x] **Step 1: 配置与策略**

ACPConfig 增加 BridgeClientMCPServers bool（默认 false=仅审计，保持现状与安全默认）。

- [x] **Step 2: 桥接实现**

策略开启时：用 internal/core/mcp stdio client spawn 客户端提议的 stdio MCP server，把工具注册进该 ACP 会话的工具面（owner 前缀 acp-mcp:<session>:<server>），走既有 MCP 权限映射（workspace/network/credential）；关闭时维持现有审计记录。Spawn 失败降级为审计 + warning，不让会话崩溃。

- [x] **Step 3: 测试**

运行 go test ./internal/acp/server -run TestMcpBridge -count=1。

覆盖：默认仅记录 metadata；开启后工具可被列出/调用；spawn 失败降级审计。

## Task 7: PiHarness 再封装（研究文档 ⏳ 项）

**背景：** docs/research_of_dsh_for_godex_optimize.md 与 acp_harness.go 头注释均标注「再封装为 PiHarness 接管完整 Turn ⏳」。当前 ACPHarness 是通用 ACP 适配器，已完成「先 ACP 委派 → 验证语义」两步。

**Files:**
- Modify: internal/agent/session_state.go
- New: internal/agent/pi_harness.go
- Modify: internal/agent/pi_harness_test.go
- Modify: docs/research_of_dsh_for_godex_optimize.md

- [ ] **Step 1: 设计探针**

先写一页设计说明（可并入研究文档）：PiHarness 相对 ACPHarness 的增量——固定 acp:pi 引擎 + Pi 特定 model profile 默认值、provider 语义（streaming/credential 策略）、把「完整 Turn 接管」的宿主侧事件面（todo/usage/权限）统一到 GoDex 事件。不复制 ACP 协议逻辑，只做策略封装。

- [ ] **Step 2: PiHarness 落地**

PiHarness 包装/继承 ACPHarness，注册 id pi:<agent-id>；RegisterConfiguredACPHarnesses 之外按配置为 Pi 类 agent 额外注册。复用 Task 1–5 的成果（选择器、历史转发、权限审批、进程树、会话复用）。

- [ ] **Step 3: 测试与文档**

运行 go test ./internal/agent -run TestPiHarness -count=1。

研究文档把 ⏳ 改为 ✅（标注 PiHarness 再封装已落地），阶段 C 状态从 Partial 更新为「MVP + 生产语义加固完成」。

## Task 8: 生产 provider/streaming/credential 语义与文档闭环 ✅

**背景：** 报告标注阶段 C 的「生产 provider 语义、streaming 与 credential 策略」为长期加固项；docs/extension-runtime-user-guide.md 4.3 节限制列表需随本计划逐条更新。

**Files:**
- Modify: docs/extension-runtime-user-guide.md 4.3 节
- Modify: docs/research_of_dsh_for_godex_optimize.md 阶段 C 与状态行
- Modify: docs/feature-implementation-matrix.md（ACP 行补 Harness 选择器/PiHarness）
- Modify: docs/vscode-acp.md（如涉及客户端 MCP 桥接）

- [x] **Step 1: 文档逐条对齐**

4.3 节四条限制分别对应 Task 2（历史转发）、Task 4（进程组）、Task 5（工具会话复用）、Task 3（权限约束），逐条标注已解决与剩余项。

- [x] **Step 2: 状态更新**

研究文档状态行、feature matrix 更新为最终实现状态。

- [x] **Step 3: 全量回归**

运行 go vet ./...、go test ./internal/acp/... ./internal/tools ./internal/agent ./internal/services/backend -count=1、go test ./internal/acp/server -run TestACPSmokeEndToEnd -count=1。

---

## 验证清单

- [x] go vet ./... 无告警。
- [ ] ACP 相关包全量测试通过（server/tools/agent/backend）。
- [x] 真实 wire smoke 测试（TestACPSmokeEndToEnd）通过。
- [x] Task 1–6、8 各新增测试全绿；Task 7 按本轮要求跳过。
- [x] 4.3 节限制列表与实现状态一致。

## 完成顺序（建议）

1. Task 4（进程组，独立且低风险）→ 2. Task 1（选择器，纯增量）→ 3. Task 5（工具会话复用）→ 4. Task 2（历史转发）→ 5. Task 3（权限审批流，交互面大）→ 6. Task 6（MCP 桥接，默认关闭）→ 7. Task 7（PiHarness）→ 8. Task 8（文档闭环）。
