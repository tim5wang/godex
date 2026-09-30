# GoDex 技术债审计与优化方案（2026-09-30）

> 状态：Active（审计基线；后续状态以代码、测试和功能矩阵复核）
> 范围：GoDex 当前代码、顶层技术文档、架构/文档门禁、Web 构建与 Android 构建入口。
> 基线：HEAD `2680288c0004b64ae4e57ce2eddd4c2703945e6b` 加当前工作树。工作树已有未提交的 Flow/Voice 改动；本文不把这些改动视为已发布，也未修改其源文件。

## 结论摘要

项目已有较好的结构治理基础：Go import boundary、复杂度预算、前端文件行数预算、`make verify` 和文档检查都已存在。但当前工作树下，架构预算与文档门禁均失败，release recipe 还引用缺失文件，因此质量门禁尚未形成可用的绿色基线。

建议先修复可复现的验证与发布阻断，再处理 FlowSession 的可靠性契约和文档状态，最后按测量结果收敛前端体量及跨平台构建。不要把“文件很大”直接等同于需要重写，也不要在未明确多进程部署需求时把 at-least-once 误判成所有部署模式下的缺陷。

## 优先级与发现

### P0：恢复质量门禁和发布基线

| 发现 | 证据与影响 | 优化方案与验收 |
|---|---|---|
| 架构预算测试失败 | `go test ./internal/architecture -count=1` 当前失败：12 个 Web 源文件超过各自行数预算，7 个 Go 函数超过复杂度预算。Web 文件：`MessageFeedV2.tsx` 927、`ChatPage.tsx` 1196、`FlowsPage.tsx` 2501、`SettingsConfigFields.tsx` 1112（预算 915）、`messagesEnCore.ts` 905、`messagesEnProduct.ts` 976、`messagesZhCore.ts` 902、`messagesZhProduct.ts` 976、`api.ts` 960、`timelineUtils.ts` 1207、`chat.ts` 1067、`stylesChatContent.css` 1061（其余预算 900）。Go 函数：`Agent.runFlowSessionRegion` 58、`buildFlowSessionRegion` 51、`validateSessionRegion` 86、`validateSessionWorkflow` 47、`Service.wireSlashCommandHandlers` 45（预算 40）；`NewBrowserTool` 43（预算 42）；`NewCronTool` 45（预算 44）。详见 `internal/architecture/frontend_size_test.go` 与 `go_complexity_test.go`。 | 先按职责拆分确实承载多个状态机/职责的文件与函数，保留行为测试；仅对有理由暂时不拆的项设置精确、不可增长的例外。验收：架构包测试通过，例外不会随文件/函数继续增长。 |
| 文档门禁失败 | `make docs-check` 当前报告 16 项。未进入索引：`desktop-shell.md`、`mobile-webview-compatibility.md`、`node-center-bridge-design.md`、`p3-foundations-design.md`、`prd-asl-engine.md`、`prd-browser-use-inside.md`、`prd-desktop-app-wrap.md`、`prd-desktop-pet-android-watch.md`、`release-notes-v1.5.0.md`、`remote-sandbox-design.md`。缺少状态头：`desktop-shell.md`、`mobile-webview-compatibility.md`、`p3-foundations-design.md`。状态值不受识别：`prd-browser-use-inside.md`、`prd-desktop-app-wrap.md`、`prd-desktop-pet-android-watch.md`。检查逻辑见 `scripts/check_docs.sh`。 | 补齐索引与状态元数据，并将检查加入本地及 CI 的统一验证。验收：`make docs-check` 通过，新增顶层文档不能绕过索引/状态检查。 |
| Release recipe 引用不存在的文件 | `Makefile` 的 `release` recipe 无条件复制 `README.md README.en.md`，但仓库没有 `README.en.md`；release 打包会在复制阶段失败。文档检查也因读取该文件而输出缺失文件诊断。 | 决定提供正式英文 README，或移除/改为可选复制，并为 release recipe 增加不依赖发布凭据的打包冒烟检查。验收：本地 release dry-run 能完成并检查产物清单。 |

### P1：可靠性和契约收敛

| 发现 | 证据与影响 | 优化方案与验收 |
|---|---|---|
| Relay 测试使用固定睡眠等待异步断言 | `internal/services/relay/compress_test.go` 的旧 hub 测试在 goroutine 中调用 `t.Errorf`，测试主体用 `time.Sleep(500ms)` 等待。全量测试曾观察到 goroutine 在测试结束后报告错误；该用例隔离重复运行未复现，故属于已观察到的稳定性风险，不是每次必现的失败。 | 用完成 channel 返回 goroutine 结果，由测试 goroutine 执行断言；用 deadline 控制超时并确保连接与 goroutine 收尾。验收：relay 包压力运行及 `go test ./... -count=1` 不再出现测试结束后的 `testing.T` 调用。 |
| FlowSession 当前是单进程、at-least-once 契约 | `docs/business-flow-runtime-design.md` 已说明协调器无跨进程 lease/锁，durable event 崩溃恢复可能重放，LLM 结果在 checkpoint 前崩溃可能重复费用/外部副作用；消费端目前自行用 sequence/output ID 去重，没有服务端 consumer ACK。单实例部署下这是明确限制，不自动等同于 bug；共享状态目录或多副本部署下会成为正确性风险。 | 先写清支持的部署拓扑并在启动时拒绝不受支持的多实例共享目录。需要水平扩展时再引入跨进程 lease/接管；对外部副作用传递稳定幂等键。只有产品需要服务端确认消费时才增加 ACK/outbox 契约。验收：崩溃重放、进程竞争、迟到结果和副作用去重均有故障注入测试，文档不宣称 exactly-once。 |
| Flow 相关文档状态和取消语义不同步 | `feature-implementation-matrix.md` 最后核对时间为 2026-08-31，仍把 Flow compiler/version store/FlowGram 标为 Planned；运行时设计的早期规划段落与后续实现记录并存。当前 Voice/Flow guide 写有插话取消，而 `business-flow-runtime-design.md` §22 仍写用户打断不会取消在途 Flow LLM/Agent 工作。当前工作树中的取消实现与相关 guide 均有未提交修改，不能据此宣称发布版本已具备该行为。 | 以功能矩阵维护当前实现状态；将设计文档中的 As-Is 快照标为历史，收敛重复状态描述。待 Flow/Voice 代码改动确定后再核对取消边界、测试与用户指南。验收：每项状态都有当前入口/实现/测试证据，冲突说法归零。 |
| Flow 校验与执行热点承担过多分支决策 | 当前复杂度门禁点名 `validateSessionRegion`（86）、`validateSessionWorkflow`（47）、`buildFlowSessionRegion`（51）和 `runFlowSessionRegion`（58）。校验、区域构建、branch 路由与执行编排交织，理解成本和回归范围偏大。 | 以现有 characterization/contract tests 固定边界，按纯校验阶段、区域/branch 分析、节点执行策略分离；避免只为压低指标引入通用框架。验收：核心分支、OR 汇合、generation fencing、取消和重放的行为测试通过，复杂度门禁恢复。 |

### P2：性能与开发环境治理

| 发现 | 证据与影响 | 优化方案与验收 |
|---|---|---|
| Web 生产构建存在大 chunk 警告 | Vite build 已通过，但报告了多个超过 500 KB 的 chunk；已观察到 Ant Design、FlowGram、CodeMirror 等较重依赖。当前没有路由级加载数据证明具体用户体验损失。 | 先生成 bundle 分析并测量首屏/各功能路由的加载，再决定 lazy boundary、依赖拆分或替代方案。为首屏与单路由设置体积预算。验收：构建无意外大包回归，关键路由实测数据改善；不以拆包数量代替体验指标。 |
| Android `preBuild` 隐式触发本机工具链和网络构建 | `mobile/android/app/build.gradle` 在缺失 native `.so` 时自动调用本地脚本；源码注释明确需要 bash、Go 工具链及网络。普通 Gradle 构建因此可能产生隐式副作用，CI/离线环境不易复现。 | 将 native runtime 准备改为显式 task/CI 前置步骤，记录工具链与产物来源；普通 `preBuild` 对缺失产物快速失败并给出明确指引。验收：CI 可在锁定依赖和声明的构建环境中重复产出 APK。 |
| Go 测试发现范围可能被本机 `temp/` 目录污染 | 当前全量 Go 测试会发现 `.gitignore` 忽略的本机 `temp/` 下 package，结果可能依赖未跟踪本地文件。 | 把临时实现移出 Go package 搜索树，或在 CI 明确、稳定地枚举测试模块。验收：清洁 checkout 与开发机运行同一测试命令时 package 集一致。 |
| Node 工具版本和验证入口不一致 | 本次环境的 pnpm 为 12.5.1，仓库锁定 10.11.0；pnpm wrapper 未能运行。Web typecheck、392 项单测和 Vite build 通过，但通过本地二进制执行。 | 使用 Corepack/CI 固定 package manager 版本，并保留可复现的单一验证入口。验收：干净环境运行 `make verify`（或其 CI 等价入口）不依赖全局工具版本。 |

## 分阶段实施方案

### 阶段 0：恢复可信基线

1. 处理三个 P0 项：架构预算、`docs-check`、release README 引用。
2. 把 relay 异步测试改成显式同步，排除偶发的全量测试失败。
3. 用同一干净 checkout 重跑 Go 全量测试、`go vet`、Web typecheck/unit/build、文档检查及 release dry-run。

**完成标准：** 所有可执行门禁通过；未通过项有明确 owner、原因和禁止发布的边界，不使用忽略错误的脚本掩盖失败。

### 阶段 1：固化 FlowSession 契约

1. 确认发布版与当前未提交 Flow/Voice 改动的差异，更新 feature matrix 和 runtime/user guide。
2. 决定单进程支持边界，明确共享存储目录、多实例和进程接管策略。
3. 增补崩溃重放、重复副作用、LLM checkpoint 前崩溃、取消迟到结果和客户端重连去重测试。
4. 再拆分高复杂度 validator/runner；按职责抽取纯函数或小型策略对象，每步用行为测试守住语义。

**完成标准：** 运行语义、部署限制、取消行为与测试一致；不承诺未实现的 exactly-once 或服务端消费确认。

### 阶段 2：按数据治理体量与构建

1. 对 Web 生产 chunk 做路由级体积与加载分析，优先处理首屏实际加载的大依赖。
2. 将 Android native 产物准备从隐式构建移到可审计、可缓存的显式步骤。
3. 固定 Node/pnpm 版本与 Go 测试发现范围，建立 clean-checkout 验证。

**完成标准：** 有可重复的体积/加载基线和跨平台构建说明；优化以实测指标验收。

### 阶段 3：防止债务回流

1. CI 必须调用与本地相同的验证入口；release 流程包含产物清单和打包 smoke test。
2. 继续采用增量架构预算：新增长文件/复杂函数先被阻断，旧例外不得扩大。
3. 功能合并时同步 feature matrix 和权威文档；`docs-check` 保持必过。
4. 每个技术债条目记录证据、影响面、风险触发条件、验收方式和复核日期，避免只保留“未来重构”口号。

## 审计方法与工具

- **技能：** `codebase-memory` 用于先图谱后源码的结构查询；`karpathy-guidelines` 用于控制审计后的改动范围和验收标准。若审计 DOCX/PDF 或真实 UI 流程，再启用对应文档/浏览器工具；本次 Markdown 审计不需要额外安装通用插件。
- **插件/MCP：** 项目已配置 `codebase-memory-mcp`，本次使用其项目索引、架构热点、符号查询和路径覆盖检查。它是定位工具，不是完整性证明；关键结论回到源码、测试和命令输出。
- **流程：** 先记录 HEAD/未提交变更及验证范围；再盘点 package、路由、依赖边界、热点；对高风险符号追踪调用关系并读精确源码；运行质量门禁；最后将实现、测试、help 与文档相互对照。将发现分为确定缺陷、明确能力边界、需要测量的风险、一般维护成本。
- **排序：** 先看安全/数据正确性/发布阻断和影响范围，再看发生概率、检测能力与修复成本。文件行数、重复度、fan-in 和复杂度只作候选信号，不单独作为重构理由。

本次 Codebase Memory 索引 generation 为 2026-09-29，coverage 为 best-effort；13 个证据路径均已检查，其中 Android `build.gradle` 有一处解析不完整，相关构建逻辑已直接阅读。`docs/superpowers/tmp`、文档图片和 Android 构建/二进制资源不在图索引覆盖范围，不影响本文涉及的源文件结论。

## 常见 AI Coding 技术债观察清单

以下是通用检查项，不代表本次都在 GoDex 中发现：

- 实现、测试、prompt/tool schema、CLI help 和文档形成多套事实源，功能已变而说明未更新。
- 一次性需求被包装成过度通用的框架、配置层或抽象；反过来，AI 生成的相似逻辑又散落复制。
- 页面、handler、service、agent runner 变成大型编排中心，职责多、状态隐式、失败路径难测。
- 只补 happy-path/mock 测试，缺少取消、超时、并发、重试、崩溃恢复、权限拒绝和资源清理测试。
- 通过固定 sleep、宽松断言、吞错 fallback 或跳过测试制造“绿色”结果；异步 goroutine 在测试生命周期结束后继续工作。
- 生成代码/配置没有来源与生命周期，旧兼容层、死 feature flag 和重复 adapter 长期保留。
- Agent/工具边界缺少最小权限、输入验证、审计与副作用幂等；prompt 说法与实际可用工具不匹配。
- 新增依赖或复制代码很快，但依赖版本、漏洞面、许可证、升级策略和 owner 没有同步治理。
- 用大文件拆分或微优化作为目标，却没有用户影响指标、性能基线和明确验收条件。

## 验证记录与限制

- 失败：`go test ./internal/architecture -count=1`，原因见 P0。
- 失败：`make docs-check`，报告 16 项索引/状态错误。
- 全量 `go test ./... -count=1` 在本轮审计中失败：包含上述架构门禁失败；Relay 曾出现异步测试 goroutine 在测试结束后报告错误，定向重复运行未稳定复现。
- 通过：`go vet ./...`、Web TypeScript build-mode typecheck、Web 37 个测试文件共 392 项单测、Vite production build（有大 chunk warning）。
- 未验证：Android Gradle 构建、完整 release 打包；release recipe 的缺失 README 问题由源码静态确认。
- `make verify` 未能形成通过基线。pnpm wrapper 受本机版本与仓库 pin 不一致影响；对应 Web 检查通过本地二进制运行。
