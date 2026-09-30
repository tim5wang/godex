# GoDex 技术债审计与优化方案（2026-09-30）

> 状态：Active（按 2026-09-30 当前工作树复核；发布状态以提交和 CI 结果为准）
> 范围：GoDex 当前代码、顶层技术文档、架构/文档门禁、Web 构建与 Android 构建入口。
> 基线：审计开始时的 HEAD `175b25cc320eb8654f1ff6e9d3fe534ddbe0f425` 加 2026-09-30 检查的工作树；本次实现与审计现已提交，本地验证通过，外部 CI 未核验。

## 结论摘要

项目已有较好的结构治理基础：Go import boundary、复杂度预算、前端文件行数预算、`make verify` 和文档检查都已存在。本工作树已恢复架构与文档门禁，修正 release recipe 的缺失文件引用，并固定 Go/Web 验证工具与并发范围。限制 Vitest 为 4 个 worker 后，37 个 Web 测试文件、393 项通过；完整 `make verify`、正式 `make web` 与四平台 release 打包 smoke 均已通过。

剩余重点是按真实路由数据测量 Web 大 chunk、显式化 Android native 构建依赖，并决定 FlowSession 在多进程部署下所需的 lease、消费确认与幂等契约。不要把“文件很大”直接等同于需要重写，也不要把单进程 at-least-once 边界泛化成所有部署模式下的缺陷。

## 优先级与发现

### P0：恢复质量门禁和发布基线

| 发现 | 证据与影响 | 优化方案与验收 |
|---|---|---|
| 架构预算曾失败，当前工作树已恢复 | 原始基线中 12 个 Web 文件、7 个 Go 函数超过预算。当前重构拆分了 Chat、Flow、Settings、i18n、API、timeline、FlowSession 与工具构造代码；`go test ./internal/architecture -count=1` 已通过，前端例外表为空，Go 仍保留一个精确的 LSP 构造函数例外。 | 保持增量预算；新例外须说明原因、精确到符号且不得随意扩张。验收：架构测试持续通过，旧例外只减不增。 |
| 文档门禁曾失败，当前工作树已恢复 | 原始基线报告 16 项索引/状态错误；当前 `make docs-check` 已通过，文档索引、状态头和允许值已补齐。检查逻辑见 `scripts/check_docs.sh`。 | 保持 `docs-check` 在本地与 CI 统一入口中；新增顶层文档必须登记状态和索引。 |
| Release recipe 曾引用缺失文件，源码与打包 smoke 均已修复/验证 | 原始 recipe 要求 `README.en.md`，仓库并无该文件；当前只复制存在的 `README.md`。隔离 `DIST_DIR` 下 Windows、macOS x86_64、macOS arm64、Linux x86_64 四个归档均包含平台二进制和 README。 | 已通过当前 `make release` recipe 验证；发布 CI 应继续核对版本、签名与真实目标机启动。 |

### P1：可靠性和契约收敛

| 发现 | 证据与影响 | 优化方案与验收 |
|---|---|---|
| Relay 异步测试曾在测试 goroutine 结束后断言，当前工作树已修复 | `internal/services/relay/compress_test.go` 的旧 Hub 测试在 goroutine 中调用 `t.Errorf` 并固定睡眠等待；现在用结果 channel 把错误交回测试 goroutine，并设置 deadline。 | 保持 goroutine 生命周期与测试同步；Relay 包压力测试及当前统一 Go 门禁通过。 |
| FlowSession 当前是单进程、at-least-once 契约 | `docs/business-flow-runtime-design.md` 已说明协调器无跨进程 lease/锁，durable event 崩溃恢复可能重放，LLM 结果在 checkpoint 前崩溃可能重复费用/外部副作用；消费端目前自行用 sequence/output ID 去重，没有服务端 consumer ACK。单实例部署下这是明确限制，不自动等同于 bug；共享状态目录或多副本部署下会成为正确性风险。 | 先写清支持的部署拓扑并在启动时拒绝不受支持的多实例共享目录。需要水平扩展时再引入跨进程 lease/接管；对外部副作用传递稳定幂等键。只有产品需要服务端确认消费时才增加 ACK/outbox 契约。验收：崩溃重放、进程竞争、迟到结果和副作用去重均有故障注入测试，文档不宣称 exactly-once。 |
| Flow 文档矩阵与历史状态漂移，当前工作树已校正 | 原矩阵仍把 compiler/version store/FlowGram 标为 Planned；设计 §15 也保留了 F2b/F3 未完成的旧快照。当前矩阵已更新到 2026-09-30，区分 FlowRun 功能与仍属单进程 at-least-once 的 FlowSession；§15 标明快照并同步 human/loop/FlowGram 状态，§25.9 记录语音新轮次取消旧 durable work 的实现和测试。 | 功能状态继续以入口、实现、测试三者核对；C/D 跨进程租约、consumer ACK、exactly-once 和媒体编排仍作为明确后续边界，不宣称已实现。 |
| Flow 校验/执行热点曾超复杂度预算，当前架构门禁已通过 | 原始基线中的验证、区域构建和节点构造函数超过复杂度预算；当前职责拆分后架构测试通过。仍需避免为了降指标引入通用框架，并用行为测试守住 branch、loop、human、generation fencing、取消与重放语义。 | 复杂度测试继续作为回归信号；若复杂度增长，再按纯校验阶段、区域分析和执行策略拆分。 |

### P2：性能与开发环境治理

| 发现 | 证据与影响 | 优化方案与验收 |
|---|---|---|
| Web 生产构建存在大 chunk 警告 | 当前嵌入构建报告 `vendor-antd` 约 1,266 KB、FlowGram/CodeMirror/Ant Design X 各约 705–757 KB，另有约 548 KB 的 ChatPage；构建通过，但尚无路由级首屏加载数据证明具体用户影响。 | 先生成 bundle 分析并测量首屏/各功能路由的加载，再决定 lazy boundary、依赖拆分或替代方案。为首屏与单路由设置体积预算。验收：构建无意外大包回归，关键路由实测数据改善；不以拆包数量代替体验指标。 |
| Android `preBuild` 隐式触发本机工具链和网络构建 | `mobile/android/app/build.gradle` 在缺失 native `.so` 时自动调用本地脚本；源码注释明确需要 bash、Go 工具链及网络。普通 Gradle 构建因此可能产生隐式副作用，CI/离线环境不易复现。 | 将 native runtime 准备改为显式 task/CI 前置步骤，记录工具链与产物来源；普通 `preBuild` 对缺失产物快速失败并给出明确指引。验收：CI 可在锁定依赖和声明的构建环境中重复产出 APK。 |
| Go 测试发现范围可能被本机 `temp/` 目录污染，当前入口已显式限定 | `make verify` 现在使用 `./cmd/... ./examples/... ./internal/...`，排除被忽略的本机 `temp/` package。 | 本地与 CI 继续调用同一 package 集；清洁 checkout 与开发机的发现范围保持一致。 |
| Node 工具版本与 Web worker 并发曾造成验证不稳定，当前入口已固定 | Web 命令改用 Corepack，读取仓库的 pnpm 10.11.0 pin；Vitest 默认 fork 并发在本机首次运行有两个 worker 启动超时。设置 `WEB_TEST_MAX_WORKERS=4` 后 37 个文件、393 项通过，随后完整 `make verify` 已通过。 | 保持 Corepack 与可配置 worker 上限；本机与 CI 继续使用统一入口。 |

## 分阶段实施方案

### 阶段 0：恢复可信基线（已完成）

1. 架构预算、`docs-check` 与 release README 引用已在当前工作树修复。
2. Relay 异步断言已改为 channel 同步；Go 测试包范围和并发已固定。
3. 完整 `make verify`、正式 `make web` 与隔离 `DIST_DIR` 的四平台 release smoke 均已通过。

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

本次 Codebase Memory 索引 generation 为 `2026-09-30T04:23:05Z`，coverage 为 best-effort；所有引用的代码证据路径均做过 coverage 检查。Android `build.gradle:22` 与 `gradlew:180` 存在解析不完整，相关逻辑按文件直接读取；索引结果只用于定位，不作为完整性证明。

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

- 通过：完整 `make verify`，包含 Go 包测试（`./cmd/... ./examples/... ./internal/...`、并发 4）、`go vet`、`docs-check`、Web typecheck、37 个 Vitest 文件共 393 项测试（最多 4 个 worker）及 Vite 嵌入构建。
- 通过：取消进程组回归用例连续运行 10 次；Relay 测试由 channel 返回结果，避免测试 goroutine 生命周期外调用 `testing.T`。
- 通过：`make web` 正式 TypeScript + Vite production build；隔离 `/tmp/godex-release-smoke-20260930` 下四个平台归档均包含预期二进制和 README。
- 通过但有警告：构建仍有多个大于 500 KB 的 chunk，最大约 1.27 MB；它们是待测量/治理项，不是构建失败。
- 未验证：Android Gradle 构建；跨平台 release 归档只确认成功交叉编译和清单，未在各目标操作系统运行二进制。
- 初次无并发限制的 Vitest 运行中，两个 fork worker 启动超时；统一入口已加 `WEB_TEST_MAX_WORKERS=4`，限制后完整门禁通过。
