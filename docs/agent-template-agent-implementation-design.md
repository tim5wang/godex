# Agent 模板指定外部 agent 内核：设计方案

> 状态：**M1 已实施（2026-09-02）；M2/M3 待做**
> 任务：t-1788324432299-1 · Agent模板设置agent实现
> 日期：2026-09-02
> 范围：设计「Agent 模板 → 外部 agent 内核」的接入方案；**M1 已按本方案落地**（模板 engine 字段 + 会话级默认引擎 + 前端内核下拉），M2/M3 见「五」。

---

## 一、背景与目标

Agent 模板（人才市场）目前可以把会话固定到一组**精确能力**（`Tools ∪ Bundles`、`memory` 作用域、`persona`/`base_prompt`），但**内核（引擎）始终是 godex 内建引擎**。目标：让模板还能指定「会话用哪个 agent 内核跑」，例如：

- 通过 **ACP（Agent Client Protocol）** 把整轮委托给外部 agent（codex / pi / dsh 等）；
- 复用 godex 已有的 **harness 抽象**（`Harness` 接口 + `harnessRouter` + `RegisterHarness`）接入其它内核。

本文档对比两条路线，给出推荐方案与分期落地计划。

---

## 二、现状盘点（源码实证）

### 2.1 已验证事实（trust，勿重复调研）

| 事实 | 位置 | 说明 |
|------|------|------|
| `Harness` 接口已存在 | `internal/agent/harness.go:79` | `Profile() / Models() / Tools() / RunTurn(ctx, HarnessTurnInput) / ResetSession(ctx, sessionID) / Close()` |
| `HarnessTurnInput` 稳定访问面 | `internal/agent/harness.go:35` | `Messages` 快照提供者、`WorkspaceDir`、`Scope`、`UsageContext`、`Harness` 字段（每轮请求引擎 id） |
| `HarnessTurnResult` | `internal/agent/harness.go:65` | `Reply / Completed / Stopped / HadInjections / RecoveryHint` |
| `godexHarness` 默认实现 | `internal/agent/harness.go:97` | 包装 `Agent.RunWithOptions`，`NewGodexHarness(agent)` 生成 |
| `harnessRouter` 动态注册 | `internal/agent/harness.go:198` | `Register(id, harness)` 并发安全；按 `sessionID` 记忆上次引擎，切换时对旧引擎 `ResetSession` |
| 请求解析器 | `internal/agent/harness.go:182` | `NewRequestedHarnessResolver("godex")`：`input.Harness` 非空用之，否则默认 godex |
| `Agent.RegisterHarness` | `internal/agent/session_state.go:201` | 动态注册额外引擎；router 已构建后仍生效 |
| `RegisterConfiguredACPHarnesses` | `internal/agent/session_state.go:222` | 为每个配置的 ACP agent 注册 `acp:<agent-id>` harness |
| 引擎路由消费点 | `internal/agent/runtime.go:664` | `RunWithOptions` 中 `opts.Harness` 非空且 ≠ `godex` 时走 `harnessRouter().RunTurn(...)`；宿主消费 `Reply` 追加 transcript + checkpoint |
| 每轮引擎请求来源 | `internal/services/backend/turn.go:685` | `requestedHarness := envelope.Metadata["harness"]` —— **由每轮调用方在 envelope metadata 里传**，空则默认 godex |
| ACP 配置结构 | `internal/core/config/config.go:78,240` | `ACPConfig.Agents map[string]ACPAgentConfig`；`ACPAgentConfig{ID, Command, Args, Env, TimeoutSeconds, Description}`，stdio 启动 |
| `ACPHarness` 已实现 | `internal/agent/acp_harness.go:32` | **ACP 已是 Harness 的一个实现**：`Profile() ID = "acp:<agent-id>"`；`RunTurn` 从稳定输入面取最后 user 文本 → `tools.StreamACPAgent` 整轮委托 → 宿主消费 `Reply`；首用绑定 scope、跨 scope 复用拒绝（P2 #5） |
| 事件映射 | `internal/agent/acp_harness.go` + `internal/agent/runtime.go` | text delta 流式推送、tool_call/plan 重放、error → `error_raised`（P2 #4 已落地） |
| `acp_agent` 工具 | `internal/agent/tool_registration.go:272` | bundle `external_agents`，stdio ACP 委托（工具面与 Harness 面并存） |
| `external_agents` bundle | `internal/tools/tool_exchange.go:273` | 已注册；prompt 关键字 acp/codex/claude/external agent |
| 模板结构体 | `internal/core/templates/template.go:62` | `ID/Name/…/Bundles/Tools/WriteEnabled/WriteScope/MCPServers/Skills/Packages/Persona/Profile/BasePrompt/ModelHint/BudgetHint/TrimHeavySections/Memory/ProjectDir` —— **当前无 engine/harness 内核字段** |
| `ApplyTemplate` 运行时链 | `internal/agent/session_template.go:29` | 设 persona/base_prompt/profile/trim_heavy/skills/memory_mode → `activation.Resolve` → `SetActiveToolsExact` / `ResetActiveToolsToDefaults` → 固定工具基线 |
| `activation.Resolve` | `internal/agent/activation/policy.go:28` | `Bundles + Tools → 精确工具名集合`（经 catalog 展开 bundle） |
| 模板应用时机 | `internal/services/backend/load.go:102`（会话加载，`locator.Metadata["template"]`）；`session.go:1076`（已打开会话 `ApplyTemplateToSession`）；`taskboard_executor.go:62`（任务看板起会话） | 三处入口都走 `ApplyTemplate` |
| 模板身份持久化 | `locator.Metadata["template"]` | 会话创建写入，reload 恢复同一预设 |

### 2.2 关键缺口（本次要设计解决）

1. **模板没有「指定内核」的字段**：`AgentTemplate` 没有任何 engine/harness/kernel 字段，无法表达「本会话用 ACP codex / pi / dsh 跑」。
2. **没有「会话级默认引擎」机制**：每轮引擎目前只来自 `envelope.Metadata["harness"]`（调用方每轮显式传）。模板固定的引擎需要成为**会话级默认**，且优先级低于每轮显式请求。
3. **外部引擎不消费 godex 工具注册**：`ACPHarness.Tools()` 返回空、不转发工具注册（P2 默认）。模板的 `Bundles/Tools` 对外部引擎语义需要明确（工具面由外部引擎自带，模板工具字段对外部内核是否生效需定义）。
4. **memory 作用域与外部内核的关系**：模板 `memory` 字段目前驱动 godex 侧记忆注入/捕获；外部引擎的会话记忆由谁持有（godex transcript？外部引擎自身状态？）需定义。

### 2.3 开放点（executor/评审需验证）

- ACP agent（codex/pi/dsh）各自的 ACP 协议符合度：`initialize / session-new / session-prompt` 三方法是否齐备、`session-update` 事件形态（`text-delta / tool_call / plan`）是否一致——`internal/tools/acp_agent.go` 已按此解析，但真实 codex/pi/dsh 二进制未在仓库内端到端验证过（`acp_harness_test.go` 用 fake ACP server）。
- 外部引擎失败/超时后是否应回退 godex 引擎（failover）还是 fail-fast —— 现有 `RunWithOptions` harness 分支是 fail-fast（`return err`）。
- `ResetSession` 语义对外部 ACP 进程：当前 `ACPHarness.ResetSession` 仅解绑 scope，**不终止 stdio 子进程**（进程生命周期管理现状待确认）。

---

## 三、两条路线架构对比

### 路线 1：通过 ACP 调用外部 agent 作为内核

| 维度 | 现状/设计 |
|------|-----------|
| 接入点 | `config.acp.agents` 配置 → `RegisterConfiguredACPHarnesses` 注册 `acp:<id>`；模板指定该 id |
| 会话/状态管理 | transcript 由 godex 宿主持有（`RunWithOptions` 追加 Reply）；外部 agent 自身状态由其 stdio 进程维护；`ACPHarness` 绑定 scope、跨 scope 拒绝复用 |
| 工具与 bundle 映射 | 外部引擎自带工具，godex **不转发**工具注册（`Tools()` 空）；模板的 `Bundles/Tools` 对外部内核不生效（需文档化） |
| 错误与恢复 | 现有 fail-fast（`return err`）；error 已统一映射为 `error_raised`；`RecoveryHint` 字段存在但 ACP 分支未填充恢复建议 |
| 多引擎热切换兼容性 | 完全兼容：`acp:<id>` 与 `godex` 同属 harness 注册表，`harnessRouter` 切换时自动 `ResetSession` |
| 已落地程度 | **高**：`ACPHarness` + 事件映射 + scope 绑定 + 动态注册均已实现；只缺「模板字段 + 会话级默认」这一层 |

### 路线 2：复用 harness 抽象接入其它内核

| 维度 | 现状/设计 |
|------|-----------|
| 接入点 | `Harness` 接口 → `Agent.RegisterHarness(id, harness)` → `harnessRouter`；模板指定注册的 harness id |
| 会话/状态管理 | 由 harness 实现方自定；宿主只通过 `HarnessTurnInput` 提供稳定访问面（Messages/WorkspaceDir/Scope） |
| 工具与 bundle 映射 | 同样由 harness 决定（`Tools()`）；宿主不强制转发 |
| 错误与恢复 | `HarnessTurnResult.RecoveryHint` 供 harness 方填充；宿主统一消费 |
| 多引擎热切换兼容性 | 原生支持（route 6.4 设计目标） |
| 已落地程度 | **中**：接口/路由/动态注册全通，但目前生产只有 `godex` 与 `acp:*` 两类 harness（`RegisterConfiguredACPHarnesses` 是唯一外部来源） |

### 对比结论（重要）

**两条路线不是二选一，而是「接口层 + 实现层」的关系**：`ACPHarness` 已经是 `Harness` 接口的实现（`acp:<id>`），路线 1 正是路线 2 的第一个落地实例。真正的设计选择是：

- **统一以 harness 抽象为接入面（路线 2 为主）**：模板只声明一个 harness id，无论它是 `godex` 还是 `acp:codex` 还是未来注册的其它内核，都走同一 `RunWithOptions → harnessRouter → RunTurn` 链路。
- **ACP 作为首个外部内核实现（路线 1 为路线 2 的特例）**：因为 ACP 链路已完整落地，作为 M1 的第一个外部引擎验证整条通路，成本最低。

因此推荐路线见「五」。

---

## 四、与现有 Agent 模板体系如何衔接

### 4.1 新增模板字段

`AgentTemplate` 增加一个字段（沿用现有 yaml/json 命名风格）：

```yaml
# engine 指定本模板会话的运行内核（harness id）。
# 空 = godex 内建引擎（默认）。
# 可选值：godex | acp:<agent-id>（config.acp.agents 里配置的 agent）| 其它已注册 harness id
engine: acp:codex
```

- 存于 `internal/core/templates/template.go` 的 `AgentTemplate`（新增 `Engine string \`json:"engine,omitempty" yaml:"engine,omitempty"\``）。
- 前端人才市场表单/模板编辑页增加「内核」下拉（选项来自注册 harness 的 `Profile().ID`，可用现有 options 端点扩展）。

### 4.2 运行时链（ApplyTemplate 扩展）

在 `internal/agent/session_template.go` 的 `ApplyTemplate` 内新增一步（放在 `activation.Resolve` 之后）：

```go
a.mu.Lock()
a.templateEngine = normalizeEngineID(t.Engine) // "" → "godex"
a.mu.Unlock()
```

`Agent` 新增字段 `templateEngine string`（`internal/agent/agent.go`），默认 `"godex"`。校验规则：非 `godex` 的 id 必须已注册（`extraHarnesses` 中存在），否则记录警告并回退 `godex`（不因模板字段失效而拒绝会话创建）。

### 4.3 会话级默认引擎（turn 请求合并）

`internal/services/backend/turn.go:685` 现为：

```go
requestedHarness := strings.TrimSpace(envelope.Metadata["harness"])
```

改为：**envelope 显式请求优先，空则回退模板固定的引擎**：

```go
requestedHarness := strings.TrimSpace(envelope.Metadata["harness"])
if requestedHarness == "" {
    requestedHarness = session.agent.TemplateEngine() // 模板固定引擎，默认 "godex"
}
```

- 优先级：**每轮显式请求 > 模板固定引擎 > godex 默认**。这与 roadmap 6.4 的每轮热切换不冲突：显式请求仍可临时切到其它引擎，`harnessRouter` 自动处理切换/Reset。
- 该改动使「模板固定内核」成为会话级默认，是最小、向后兼容的接入点。

### 4.4 与 memory 字段的关系（定义语义）

- **godex 引擎**：`memory` 字段行为不变（none/shared/scoped 照旧驱动注入与捕获）。
- **外部内核**（如 `acp:*`）：外部引擎不消费 godex memory index 注入（`ACPHarness.RunTurn` 只取最后 user 文本）。定义：**外部内核会话中，模板 `memory` 字段仅对 godex 侧 transcript 与恢复生效**；外部引擎自身的记忆由其进程状态负责。M1 不额外做记忆桥接（列入非目标），文档注明即可。

### 4.5 与工具/bundle 的关系（定义语义）

- **godex 引擎**：`Bundles/Tools` 精确激活照旧（`SetActiveToolsExact`）。
- **外部内核**：godex 不转发工具注册，模板 `Bundles/Tools` 对外部内核**不生效**（外部引擎自带工具面）。`WriteEnabled/WriteScope/MCPServers/Skills/Packages` 同理——M1 明确「仅 godex 引擎生效」，避免误导。

---

## 五、推荐路线 + 理由 + 分期

### 推荐：**路线 2 为主（harness 抽象为统一接入面），ACP 作为首个外部内核（路线 1 是它的实现实例）**

理由：

1. **零重构、低成本**：`Harness` 接口 + `harnessRouter` + `RegisterHarness` + `ACPHarness` 全部已实现并测试（P2/6.4 落地）。只需补「模板字段 + 会话级默认引擎 + turn 合并」三处小改动即可打通。
2. **语义最清晰**：模板声明的是「harness id」，与 roadmap 6.4 的引擎概念天然同构；未来接入 pi/dsh/其它内核不需要改模板模型，只需 `RegisterHarness` 一个新实现。
3. **热切换兼容性最好**：模板固定引擎是「会话级默认」，每轮仍可被 envelope 显式请求覆盖，不破坏既有 6.4 能力。
4. **ACP 作为首个实例验证通路**：`acp:*` 已完整（事件映射/scope/streaming），M1 用它端到端验证「模板选内核」整条链路，风险最小。

### 分期落地

| 分期 | 内容 | 验收标准 |
|------|------|----------|
| **M1：模板 engine 字段 + 会话级默认引擎** | ① `AgentTemplate.Engine` 字段 + 校验/归一化；② `Agent.templateEngine` + `ApplyTemplate` 写入；③ `turn.go` envelope 空时回退模板引擎；④ 前端模板表单/编辑加「内核」下拉（选项来自注册 harness） | ① 模板带 `engine: acp:codex` 建会话 → 每轮实际走 `acp:codex` harness（断言 `harnessRouter.last` 或日志）；② 不带 engine 的模板行为与现状完全一致（回归无变化）；③ envelope 显式 `harness: godex` 能覆盖模板引擎；④ 未注册 engine id 回退 godex 并记录警告、不拒绝会话创建；⑤ `go test ./internal/agent/... ./internal/services/backend/...` + `tsc -b` 全绿 |
| **M2：外部内核语义文档化 + 观测** | ① 模板编辑/人才市场卡片展示内核徽标；② 会话状态/Status 面板显示当前引擎；③ 文档（本文档「四.4/4.5」语义）落地为模板提示文案 | ① UI 可见模板内核；② 会话内可看到引擎标识；③ 模板表单对 `engine` 非 godex 时展示「外部内核自带工具面，模板工具字段不生效」提示 |
| **M3（可选）：外部内核失败回退与进程生命周期** | ① 定义并实现 failover 策略（外部内核失败→是否回退 godex 重试，需产品决策）；② `ACPHarness.ResetSession` 终止 stdio 子进程；③ `RecoveryHint` 填充 | ① 外部内核 down 时行为可预期（回退或明确报错）；② 会话销毁/引擎切换无残留子进程；③ error 事件带恢复建议 |

---

## 六、非目标与边界（明确不臆造、不破坏）

1. **本期不实现**：只产出设计文档，不改代码（任务边界）。
2. **不引入 ACP → godex 反向调用**：模板指定外部内核是「godex 委托外部」，不是「外部 agent 反向调 godex 工具」；`acp_agent` 工具（external_agents bundle）与 `ACPHarness` 两条委托路径在 M1 保持独立，不合并、不互斥。
3. **不破坏既有 harness 路由**：`harnessRouter` 逻辑、`RunWithOptions` harness 分支、6.4 每轮热切换均不动；M1 只在 `turn.go` 加「空则回退模板引擎」这一层默认，显式请求优先级不变。
4. **不做记忆桥接**：外部内核与 godex durable memory 之间 M1 不做双向同步（见四.4），仅文档化语义。
5. **不转发工具注册给外部内核**：保持 P2「不转发」默认；不把 godex 工具集塞进外部进程。
6. **不臆造未验证内核**：codex/pi/dsh 的真实 ACP 二进制未在本仓库端到端验证前，M1 验收用 fake ACP server（复用 `acp_harness_test.go` 模式）；真实内核接入列为开放点。

---

## 七、风险与开放问题

| # | 风险/开放点 | 影响 | 处置 |
|---|------------|------|------|
| 1 | 真实 codex/pi/dsh 的 ACP 协议差异 | M1 后真实内核可能事件解析不完整 | M1 用 fake 验证通路；真实内核接入单独验收 |
| 2 | 外部内核失败即 fail-fast，无回退 | 外部内核 down 时整轮失败 | M3 做 failover 策略决策（需产品/需求方对齐） |
| 3 | `ACPHarness.ResetSession` 不终止子进程 | 会话销毁可能残留 stdio 进程 | M3 补进程生命周期管理 |
| 4 | 模板 `memory`/`Bundles` 对外部内核语义易被误解 | 用户以为模板工具字段对外部内核生效 | M2 模板表单提示 + 文档化 |
| 5 | 会话级默认引擎是否要持久化到 session 元数据 | 重启恢复后引擎保持一致 | M1 通过 `locator.Metadata["template"]` 已隐式恢复（reload 重新 ApplyTemplate）；如需独立持久化列入 M2 评估 |

---

## 八、参考代码位置索引

| 关注点 | 位置 |
|--------|------|
| Harness 接口 / TurnInput / Result / godexHarness | `internal/agent/harness.go:35,65,79,97` |
| harnessRouter / 解析器 | `internal/agent/harness.go:170,182,198` |
| RegisterHarness / 装配 ACP harness | `internal/agent/session_state.go:196,222,238` |
| RunWithOptions harness 分支 | `internal/agent/runtime.go:664` |
| 每轮 harness 请求（envelope metadata） | `internal/services/backend/turn.go:685` |
| ACPHarness 实现 | `internal/agent/acp_harness.go:32` |
| ACP stdio client / StreamACPAgent | `internal/tools/acp_agent.go:193` |
| ACP 配置结构 | `internal/core/config/config.go:78,240` |
| AgentTemplate 结构体 | `internal/core/templates/template.go:62` |
| ApplyTemplate 运行时链 | `internal/agent/session_template.go:29` |
| activation.Resolve（bundle→tools） | `internal/agent/activation/policy.go:28` |
| 模板应用三入口 | `internal/services/backend/load.go:102`、`session.go:1076`、`taskboard_executor.go:62` |
| ACP harness 集成测试（fake server） | `internal/agent/acp_harness_test.go:193,302` |
| 既有 DSH/外部引擎调研 | `docs/research_of_dsh_for_godex_optimize.md`（P2/阶段 C） |
