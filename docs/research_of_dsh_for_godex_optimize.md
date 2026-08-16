# DeepSeek Harness 对 GoDex 的改进启示

> 状态：Draft / Plan（设计分析与改进方案；**阶段 0、P1、阶段 A 内核、MCP 桥接（tools/prompts 面）、阶段 B MVP（wazero WASM Tool）、P2 #1/#2/#3、阶段 C 的 ACP Harness adapter 均已落地**，其余阶段 C 项待实施）
> 目标：提炼 `temp/deepseek-harness` 中值得 GoDex 吸收的架构能力，聚焦近期可落地优化；不追求复制 Cordis，也不把 WASM 等同于插件系统。
> 修订日志：2026-08-15 整合插件对照表、wazero 兼容性结论（协议层/桥接层）、MCP 跨运行时能力协议视角与更低起步点（阶段 0：package requires 依赖解析）。2026-08-16 阶段 0 落地：`godex.package.yaml` 支持 `requires`/`provides`、安装时依赖图校验（缺失/冲突/环）、卸载依赖保护、事务式重装与旧 digest 目录 GC（见 `internal/core/packages/{requires,deps}.go`）。同日 P1 与阶段 A 内核骨架落地：`internal/toolruntime` 注册返回可逆 `Registration`（owner/generation/draining，`RegisterOwned`/`UnregisterOwner`），新增 `internal/pluginrt` 轻量插件内核（manifest/graph/instance/effects/registry/manager，含事务式 prepare/commit/rollback 与 `NativeToolPlugin` 内建 Go 适配器）；MCP 桥接落地：`internal/core/mcp` 新增 stdio JSON-RPC client（`list_mcp_tools`/`call_mcp_tool`，任意语言 MCP server 即 GoDex 插件）。

## 1. 核心结论

GoDex 已有 ToolHandler、Harness、MCP、Package、Skill、Scope、Sandbox、配置热更新等扩展点，但它们彼此独立，尚缺统一的插件生命周期与能力注册模型。

DeepSeek Harness（DSH）最值得借鉴的不是 TypeScript 动态加载，而是以下语义：

1. 插件声明 `requires/provides`，按能力依赖激活；
2. 插件实例拥有明确生命周期；
3. 工具、服务、事件监听和后台资源均归属于实例，卸载时自动撤销；
4. 能力注册受 scope 隔离；
5. 配置先构建并验证候选树，成功后原子切换，失败保留旧树。

因此，GoDex 的优先方向应是先建立轻量 **Plugin Kernel**，再按需增加 WASM 执行器，而不是直接把现有工具包装成 WASM。

### 1.1 关于「wazero 兼容 DSH 插件」的明确结论

DSH 插件是 **TypeScript/JS 模块**，运行在 Node 的 Cordis Loader 里（`@deepseek-ai/cordis` + `cordis-plugin-loader/include/group/hmr`），**不是 WASM**。因此：

- **wazero 无法二进制级执行 DSH 插件**——这是运行时形态的根本差异，不是工程难度问题。wazero 能实现的是 GoDex 自己的、沙箱化的 WASM 插件内核（见第 5 节）。
- 想在 Go 内跑 JS 插件需走 goja/quickjs 嵌 JS 的路线，但 Cordis 依赖 Node 的模块解析、动态 import、HMR 与装饰器，在嵌入式 JS 引擎里复刻不现实。
- 「兼容 DSH 插件」应在**协议/清单层**或**桥接层**做，而不是二进制层（详见 5.2）。

## 2. DSH 值得吸收的设计

### 2.1 依赖驱动生命周期

DSH 的插件实例由 Fiber 管理：

```text
PENDING → LOADING → ACTIVE → UNLOADING → DISPOSED
                 ↘ FAILED
```

缺少依赖时保持 `PENDING`；provider 消失时 consumer 自动卸载，恢复后重新激活。这样 provider 替换不会留下失效引用。

GoDex 可采用更简单的状态机，但应保留三个关键性质：

- 启动前验证依赖；
- 停止时阻止新调用并清理资源；
- 更新失败不影响旧实例。

### 2.2 可逆注册

DSH 把工具、服务、事件监听、timer、连接等都视为插件 effect。卸载插件即撤销该实例的全部 effect。

GoDex 当前 `ToolHandler.ReplaceWith()` 已有原子替换基础，但工具注册缺少 owner/disposer。建议注册统一返回清理函数，并记录：

```text
plugin_id + instance_id + generation + scope
```

这能解决动态停用、热更新、迟到异步结果和资源泄漏问题。

### 2.3 配置组成与 Scope

DSH 用 profile、bundle、patch 组成应用，并可把同一插件树挂到不同 agent scope。

GoDex 已有 org/personal/session scope，可直接复用。需要明确区分：

- **Tool Bundle**：控制本轮模型可见的工具；
- **Plugin Bundle**：安装和配置一组运行时能力。

两者不能共用同一语义，否则“加载 provider”和“向模型暴露 schema”会混在一起。

### 2.4 DSH 概念 → GoDex 现状 → 差距（对照）

| DSH/Cordis 概念 | GoDex 现状 | 差距 |
|---|---|---|
| **Context 作用域注入**（`ctx.get/provide`，能力可互相引用） | `scope` 隔离（org/personal/session）、bundle 会话级激活、`ToolHandler` 按 agent 注册 | 有「作用域」无「注入」：能力之间不能互相引用/协商，只有主 agent 单向调用 |
| **Loader 依赖图**（includes/groups/依赖解析、环检测） | `godex.package.yaml` 无 `requires`；安装是拷贝文件 | 无依赖解析、无环检测、无版本约束，装错顺序靠 smoke 兜底 |
| **可逆注册**（`ctx.on/ctx.provide` 返回 dispose，卸载即撤销全部 effect） | 工具注册是命令式的；`ToolHandler.ReplaceWith` 有原子替换基础但注册无 owner/disposer | **重装/卸载不干净**：reinstall 只是覆盖文件，旧注册可能残留 |
| **事务热更新**（Loader settle + Fiber 阶段 + HMR） | config live-apply + `config_reload_watcher` 已存在 | 无插件级热替换：无「验证 → 激活 → 提交/回滚」事务 |
| **统一能力协议**（service 注册 + typert 生成 Host/Client Remote） | `toolruntime.Tool` 是强类型，但 package 只能声明 command/role，**不能声明自己的工具** | 能力注册表缺失：56 个工具硬编码在 `tool_registration.go` |
| **生命周期钩子**（onActive/onDispose） | 无 | package 激活/停用无副作用钩子 |

一句话：GoDex 的 package/skill 是**声明式资源包**，DSH 的插件是**可编排的运行时单元**。指导价值在于把 GoDex 从「资源包」升级为「运行时插件」。

## 3. GoDex 当前基础与主要缺口

### 已有基础

| 能力 | 现状 |
|---|---|
| Tool registry | 支持 metadata、bundle、before/after interceptor、`ReplaceWith` |
| Agent Engine | `agent.Harness` 支持按轮路由和切换时 reset session |
| 外部 Agent | `acp_agent` 可通过 ACP stdio 委派任务 |
| Provider SPI | 已有 Caller、Sandbox、Memory Strategy、Worker Runtime 等 Go 接口 |
| Package | 已有 manifest、digest、trust、permissions、registry、smoke test |
| Scope | 已有 org/personal/session 隔离模型 |
| Reload | 配置变化可重建依赖和工具 registry |

### 主要缺口

1. 工具仍集中在 `Agent.registerToolsWith()` 中硬编码；
2. `Agent`/`dependencies` 持有大量具体 manager，装配耦合较高；
3. 没有统一 `PluginInstance`、依赖图和 effect ledger；
4. `events.Sink` 是 UI/Timeline 广播，不是内部 middleware event bus；
5. Harness router 由 `sync.Once` 构建，不支持运行期动态注册；
6. Package 当前是声明式资源包，不能承载可执行插件。

## 4. 优先改进方案

### P0：统一插件内核，不引入第三方代码

新增 `internal/pluginrt`，先服务内建 Go 组件：

```text
internal/pluginrt/
  manifest.go    # 插件、依赖、贡献声明
  graph.go       # requires/provides 校验
  instance.go    # 生命周期和 generation
  effects.go     # 可逆注册账本
  registry.go    # scope-aware capability registry
  reload.go      # prepare/commit/rollback
  native.go      # 内建 Go adapter
```

最小接口可围绕以下对象设计：

```go
type Instance interface {
    ID() string
    Scope() scope.Id
    Start(context.Context, Host) error
    Stop(context.Context) error
}

type Effect func(context.Context) error
```

验收标准：

- 先迁移 2～3 个内建工具或 hook；
- 注册与卸载可重复执行且无泄漏；
- 坏配置不替换当前可用 registry；
- 不改变现有 Tool Bundle 行为。

### P1：改造 ToolHandler 注册所有权 ✅ 已落地（`internal/toolruntime`）

为工具和 interceptor 增加 owner/generation-aware 注册：

- 注册返回 disposer —— ✅ `Register`/`RegisterWithMeta` 返回 `*Registration`，`Dispose()` 逆序撤销；interceptor 侧 `AddBeforeInterceptorsOwned`/`AddAfterInterceptorsOwned` 返回 disposer
- 同名冲突明确报错或按优先级替换 —— ✅ `RegisterOwned(owner, ...)` 对不同非空 owner 报 `ErrToolConflict`
- 实例进入 draining 后拒绝新调用 —— ✅ `MarkDraining` + `ErrToolDraining`
- 迟到结果若 generation 已过期则丢弃 —— ✅ generation 计数 + `CurrentGeneration`；`ReplaceWith` 重映射 generation
- reload 采用 shadow registry + atomic swap —— ✅ 原有 `ReplaceWith` 保留

这是后续 WASM、动态 MCP provider、Package 执行能力的共同基础。

### P2：完善 Agent Engine 接入 ✅ 全部落地

`agent.Harness` 抽象已经存在，但生产环境只有内建 GoDex engine。建议补齐：

1. `HarnessTurnInput` 提供稳定的消息/会话访问面，而不是依赖 `*Agent` 内部状态 —— ✅ `HarnessTurnInput` 新增 `Messages func() []protocol.Message`（快照提供者）、`WorkspaceDir`、`UsageContext`；宿主在 `RunWithOptions` 填充，外部 engine 只消费这些输入（见 `internal/agent/{harness,runtime}.go`）；
2. 由宿主统一消费 `HarnessTurnResult.Reply`、写 transcript 并 checkpoint —— ✅ harness 分支在 `RunTurn` 后把 `Reply` 追加进 transcript、触发 checkpoint 并发出 `assistant_message_completed` 事件（`internal/agent/runtime.go`）；
3. 将 Harness registry 改为动态、generation-aware，移除 `sync.Once` 快照限制 —— ✅ `harnessRouter` 增加并发安全的 `Register`（`sync.RWMutex`），`Agent.RegisterHarness` 在 router 已构建后仍生效（见 `internal/agent/{harness,session_state}.go`）；
4. 统一 text delta、tool、usage、error、permission 等事件映射 —— ✅ 已落地：ACP 外部 engine 的 session/update 事件（message chunk → `assistant_text_delta`、tool_call → `tool_call_started/finished`）由 `ACPHarness.emitUpdateEvents` 重放为 GoDex 事件（`tools.ACPUpdate`/`ACPRunResult.UpdateEvents` 结构化解析）；error 映射已落地（失败 turn 发 `error_raised`，`emitErrorEvent`）；plan/permission 类更新映射为 `warning_raised`（`acp_external_update`，`emitUpdateEvents`），外部 engine 不消耗 GoDex token（usage 映射不适用）；
5. 明确外部 engine 的 workspace、scope 和工具权限 —— ✅ `HarnessTurnInput` 新增 `Scope`（宿主注入 `SandboxScope`）；`ACPHarness` 在首次使用时绑定 scope 并拒绝跨 scope 复用（`bindScope` + `ResetSession` 解绑，P2 #5 外部 engine scope 联动）；workspace 由宿主注入；工具权限边界以「不转发工具注册」为默认。

Pi 等外部 agent 的近期接入顺序建议是：

```text
先通过 ACP 作为任务委派 Agent ✅
          ↓
验证 session、事件和权限语义 ✅（宿主统一消费 Reply + checkpoint + 事件映射）
          ↓
再封装为 PiHarness 接管完整 Turn ⏳
```

这样可以复用现有 `acp_agent`，避免一开始改动 Agent 主循环。第一步已落地为 `ACPHarness`（见下「阶段 C」）。

### P3：扩展 Package 为可执行插件包

在现有 `godex.package.yaml` 上增加可选 runtime 声明，而不破坏声明式资源包：

```yaml
runtime:
  kind: wasm
  module: plugin.wasm
  abi: godex:plugin@0.1
requires:
  - godex:log@1
provides:
  - godex:tool-provider@1
permissions:
  - workspace.read
```

Package 层负责安装、摘要、来源和授权；Plugin Kernel 负责实例生命周期，二者职责不要混合。

### P4：WASM Tool MVP ✅ 工具面 + prompt 贡献面 + policy 面已落地（`internal/wasmrt`）

首版建议：

```text
wazero + Core Wasm + 版本化 JSON ABI
```

原因是 GoDex 当前保持 `CGO_ENABLED=0` 的单二进制发布方式，wazero 更匹配。首期仅支持：

- 工具声明和执行 —— ✅ `godex_tools_list` / `godex_invoke`（mailbox JSON ABI）
- prompt/context contributor —— ✅ `godex_prompts_list`：插件声明 prompt sections（key/kind/text），`pluginrt.Manager.PromptSections` 聚合活跃插件贡献，`Agent.SetPluginPromptProvider`/`pluginPromptSectionsFromManager` 注入 runtime prompt（key `plugin:<id>:<key>`）；无插件时 prompt 不变
- tool before/after policy —— ✅ `godex_policy`：插件返回显式决策 `{"action":"continue"|"deny"|"replace","error":{code,message},"result":...}`（研究文档 §4 的显式决策而非 waterfall next()）；`WasmToolPlugin` 把策略注册为 owner-aware before-interceptor（`AddBeforeInterceptorsOwned`，卸载时随 instance 逆序撤销），`toolruntime` 同时新增 `UnregisterOwnerInterceptors` 一键撤销某 owner 全部 interceptor
- 显式 KV、日志和受控 workspace read host calls —— ✅ `godex_host`：`godex_log` / `godex_kv_get` / `godex_kv_set` / `godex_workspace_read`

默认不开放完整 WASI、socket、环境变量、shell、进程或明文凭据。有效权限应为：

```text
插件申请 ∩ 安装授权 ∩ Profile 策略
∩ Session security profile ∩ 当前 scope/write scope
```

LLM streaming provider、Harness WASM 化和 UI 插件不应进入首期。

## 5. WASM 能力边界

| 能力 | 建议 |
|---|---|
| Tool、Prompt、Tool Policy | 首期适合 |
| Memory/Web provider | 后续通过受控 host API 接入 |
| LLM provider | 需要 streaming、取消和 backpressure ABI |
| Harness engine | 可做，但应晚于 ACP/原生 adapter |
| Shell/FS/Network | 只能通过宿主权限代理 |
| Sandbox provider | 保留 Go/进程级实现 |
| React UI 插件 | 后端 WASM 无法解决，需独立前端协议 |

跨 WASM 的 middleware 不建议直接复制 DSH 的 `waterfall next()`。首版使用显式决策更安全：

```json
{"action":"continue","patch":{}}
{"action":"deny","error":{"code":"..."}}
{"action":"replace","result":{}}
```

### 5.1 wazero 的硬限制（设计约束）

选择 wazero（纯 Go、无 CGO、匹配 `CGO_ENABLED=0` 单二进制发布）要接受以下边界：

- **WASI preview1 无网络 socket、guest 内无 goroutine**：需要 socket 的插件只能通过宿主 broker 代理；
- **宿主 ↔ guest 每次调用有编解码开销**：大对象（大 tool 结果、transcript）不要跨边界传，走宿主引用/文件引用；
- **Go 侧不能直接传对象**：ABI 必须显式版本化（JSON/二进制），稳定协议先行；
- **JS/TS 插件无法执行**（见 1.1）：wazero 只服务 Go/Rust/TinyGo 编译的 WASM 插件；
- **UI 插件无解**：后端 WASM 不影响前端，React 类插件需要独立的前端协议。

### 5.2 「兼容 DSH 插件」的两条现实路径

DSH 插件是 TS/JS 模块（跑在 Node 的 Cordis Loader 里），wazero 无法直接执行。要获得与 DSH 插件的互操作，应走协议/桥接而不是二进制：

1. **协议/清单层兼容（推荐，长期）**：把 DSH 的插件清单与能力协议抽象成**跨语言规范**（plugin manifest + capability contract），GoDex 实现同一规范。未来的插件可以「同一份 manifest，DSH 和 GoDex 都能装」——类似 LSP/MCP 的协议级兼容，而不是二进制兼容。这与 GoDex 已有的 package manifest、ACP 桥接、MCP 方向天然一致。
2. **桥接层兼容（立即可做）✅ 已落地**：GoDex 已有 `acp_agent` 工具（调用外部 ACP agent）与 ACP server，也有 MCP 只读资源支持——把 DSH 插件作为**外部 ACP agent 或 MCP server** 接入，不需要 wazero 就能获得「运行时拓展」。wazero 内核则是为 GoDex 原生、沙箱化、强安全的插件准备的，两者可以并存。

**MCP 作为跨运行时能力协议 ✅ 已落地（tools/prompts 面 + 动态按 server 注册）**：MCP 已从「只读文件系统资源」升级为完整 stdio client（`internal/core/mcp/stdio.go`，JSON-RPC 2.0 over stdio：initialize/tools-list/tools-call/prompts-list/prompts-get），配置 `type: stdio` + `command/args/env` 即可接入任意语言的 MCP server；其工具通过 `list_mcp_tools` / `call_mcp_tool`、prompt 通过 `list_mcp_prompts` / `get_mcp_prompt` 暴露。除通用桥接外，**每个 stdio server 的工具还会按 server 动态注册为一等工具**（命名 `<server>__<tool>`，owner `mcp:<server>`），直接进入工具目录并可独立卸载，`tools.NewMCPServerTool`/`Agent.registerMCPServerTools` 实现；bridge 与 per-server 注册均挂在 `godex:builtin:mcp` owner 体系下。任何语言实现的 MCP server 都成为 GoDex 插件——这本身就是 DSH 插件生态的通用等价物，且与 wazero 内核正交（WASM 插件跑在进程内，MCP 插件跑在进程外，共享同一能力注册表与权限/scope/审计体系）。

## 6. 建议路线图

### 阶段 0：Package 依赖与可逆卸载（低成本，先行）✅ 已落地

不需要 `internal/pluginrt` 即可获得 DSH 依赖图与可逆注册的第一档收益：

- `godex.package.yaml` 增加可选 `requires`（`name@version` 或 `capability`），安装时解析依赖图 + 环检测 + 缺失/冲突报告；同时支持 `provides` 声明能力供给；
- 给 command/role/skill 的注册点返回 `Dispose func`，package 卸载时逆序撤销全部注册（卸载保护：仍被引用的包拒绝移除）；
- 把现有 `reinstall` 升级为事务式：unload 旧 → 装新 → 激活 → 失败回滚旧，并 GC 旧 digest 目录。

这同时解决「重装不干净」的存量问题，并为阶段 A 提供依赖与 effect 的语义基础。

### 阶段 A：插件内核（3～5 人周）🔄 内核骨架已落地

- Plugin manifest/instance/graph/effects —— ✅ `internal/pluginrt/{manifest,graph,instance,effects}.go`
- scope-aware registry —— ✅ `internal/pluginrt/registry.go`（scope → capability → providers）
- ToolHandler disposer 和 generation —— ✅ `internal/toolruntime`（`RegisterOwned`/`Registration.Dispose`/`MarkDraining`/`UnregisterOwner`）
- 事务式整树切换 —— ✅ `internal/pluginrt/manager.go`（`Activate`/`Deactivate`/`Prepare`/`Commit`/`Rollback`，坏配置不替换当前 registry）
- 迁移少量内建组件验证 —— ✅ MCP bundle 工具已迁移到 `godex:builtin:mcp` owner；`NativeToolPlugin`/`WasmToolPlugin` 均有集成测试验证注册-卸载闭环。

### 阶段 B：WASM Tool（3～4 人周）🔄 MVP 已落地（`internal/wasmrt`），含 Rust 示例 SDK

- wazero runtime 和 module cache —— ✅ `internal/wasmrt`（纯 Go、无 CGO，匹配 `CGO_ENABLED=0` 单二进制发布；`wasmrt.Config` 支持 host callbacks / 超时 / 内存页上限 / 并发上限）
- JSON ABI、超时、取消、内存及并发限制 —— ✅ 版本化 JSON ABI `godex:plugin@0.1`（mailbox 请求缓冲 + `godex_tools_list`/`godex_invoke`/`godex_abi_version`），单次调用默认 30s 超时与 context 取消，guest 内存默认上限 32 MiB，Go-wasm 单线程 guest 以 callMu 串行化执行；受控 host 调用仅 `godex_log`/`godex_kv_get`/`godex_kv_set`/`godex_workspace_read`，不开放完整 WASI/socket/env/shell
- Package runtime/digest/trust —— ✅ `godex.package.yaml` 新增 `runtime{kind,module,abi}`（仅 `wasm`），安装时校验 module 存在且为包内相对路径、ABI 匹配；digest/trust 复用 package 现有机制；`RuntimeModulePath` 暴露模块路径
- Rust/TinyGo 示例 SDK —— ✅ `examples/wasm-plugin-rust`：零依赖 Rust `cdylib` 实现 mailbox ABI（tools/prompts/policy/abi 四面），`wasm32-wasip1` 交叉编译，`internal/wasmrt` 与 `internal/pluginrt` 均有端到端集成测试（Rust 编译产物直接跑通工具/prompt/policy）；`rebuild-testdata.sh` 一键刷新 Go 测试夹具；TinyGo 示例留待后续（ABI 与 Rust 示例等价）
- pluginrt 接线 —— ✅ `internal/pluginrt/wasm.go` `WasmToolPlugin`：Start 时 wazero 加载 → tools_list 发现 → 按 owner 注册 `toolruntime.Tool`；Stop/卸载时逆序撤销并关闭 runtime（集成测试含真实 wasm guest 调用）

### 阶段 C：Provider 与外部 Engine（4～8 人周）🔄 HTTP/credential/KV broker + streaming handle + ACP Harness adapter 已落地

- HTTP/credential/KV broker —— ✅ 全部：`internal/pluginrt/kv_broker.go` `PluginKVBroker`（namespaced、scope-scoped、durable SQLite 或 memory 后端，key 含 plugin id 隔离，禁止 `|` 防别名）；`WasmToolPlugin.KV` 接线到 wasmrt `godex_kv_get/set` host 调用；wasmrt 新增 `godex_http_get` host 调用（受宿主 web fetch 策略控制：allow/deny 域、超时、max chars，`Agent.pluginHTTPGet` 走 `WebFetchService.Fetch`）；`internal/pluginrt/credential_broker.go` `CredentialBroker`（按插件 allowlist 授权读取命名 secret，`godex_credential_get` host 调用，未授权/未设置返回不同错误码）；Rust 示例新增 `rust_counter`（KV）、`rust_http`（HTTP）、`rust_credential`（credential）工具，均端到端测试；
- streaming handle —— ✅ `tools.StreamACPAgent`：ACP client 的 `readResponseWithCallback` 在每个 session/update 到达时即时回调（text chunk 实时推送），`ACPHarness` 用它在流式过程中发 `assistant_text_delta`，tool_call/plan 事件仍聚合重放；返回结果保留完整回复文本与结构化更新列表（真实 wire 协议集成测试）；
- Pi/其他 ACP agent 的 Harness adapter —— ✅ `internal/agent/acp_harness.go` `ACPHarness`：包装一个配置的 ACP agent 为 `Harness`（id `acp:<agent-id>`），`RunTurn` 从稳定输入面取最后 user 文本、经 stdio ACP（initialize/session-new/session-prompt）委托整轮，回复经 `HarnessTurnResult.Reply` 由宿主写入 transcript + checkpoint；`RegisterConfiguredACPHarnesses` 在 agent 装配时注册所有配置的 ACP agent（真实 wire 协议集成测试）；
- 动态 Harness registry 和统一事件映射 —— ✅ 动态 registry（P2 #3）；事件映射部分落地（host 消费 Reply 发 `assistant_message_completed`），统一 text/tool/usage/error 映射待做。

### 暂缓

- 任意动态 Service；
- 最小子树 HMR；
- 完整 WASI；
- WASM Sandbox provider；
- 第三方 React 代码进入主页面 realm。

## 7. 决策建议

GoDex 不需要复制 Cordis 的动态对象 Context。更适合 Go/WASM 的模型是：

```text
版本化 Capability Contract
+ Scope-aware Registry
+ Plugin Instance Lifecycle
+ Reversible Effects
+ Transactional Reload
+ 可选 WASM Executor
```

近期最有价值的工作不是“支持任意 WASM”，而是先统一现有 Tool、Harness、Package 和 reload 的生命周期与注册所有权。完成这一层后：

- 内建 Go 组件更容易替换和测试；
- Pi 等外部 Agent Engine 更容易稳定接入；
- WASM、MCP 和未来 provider 可以共享同一权限、scope 和审计体系；
- 配置更新失败不会破坏正在工作的 Agent。

落地顺序建议：**阶段 0（package requires + 可逆卸载）→ 阶段 A（pluginrt 内核）→ 阶段 B（wazero WASM Tool）→ 阶段 C（Provider/外部 Engine）**，期间并行把 MCP 升级为完整 client 以承接进程外插件；「DSH 插件兼容」按 5.2 的协议/桥接层推进，不追求二进制互操作。

## 参考代码

- DSH：`temp/deepseek-harness/docs/architecture.md`
- Cordis：`temp/deepseek-harness/vendor/cordis/src/{context,fiber,service,registry,events}.ts`
- DSH 启动/加载：`temp/deepseek-harness/packages/boot/app-boot/src/index.ts`（Cordis Loader + include/group/HMR 装配）
- DSH 插件清单与生命周期投影：`temp/deepseek-harness/packages/host/plugin-inventory/src/`（Fiber 阶段 pending/loading/active/failed/unloading）
- DSH Tool/LLM：`temp/deepseek-harness/packages/core/tools`、`packages/llm/llm`
- GoDex Tool：`internal/toolruntime`、`internal/agent/tool_registration.go`
- GoDex Harness：`internal/agent/harness.go`、`internal/agent/session_state.go`
- GoDex Package/Scope：`internal/core/packages`、`internal/core/scope`
- GoDex ACP：`internal/tools/acp_agent.go`
- GoDex MCP（进程外能力协议基础）：`internal/tools/mcp.go`、`internal/core/mcp`
