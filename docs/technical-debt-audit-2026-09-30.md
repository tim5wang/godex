# GoDex 技术债审计与优化方案（2026-09-30）

> 状态：Active（按 2026-09-30 当前工作树复核；发布状态以提交和 CI 结果为准）
> 范围：GoDex 当前代码、顶层技术文档、架构/文档门禁、Web/Android 构建入口与 GitHub Actions 发布门禁。
> 基线：原始审计起点为 HEAD `175b25cc320eb8654f1ff6e9d3fe534ddbe0f425`；复核时以 HEAD `80e43f9` 加当时工作树为准，其中的 FlowSession 状态目录锁、故障注入测试和文档更新随后已提交到 `e30bc36`。外部 CI 未核验。

## 结论摘要

项目已有较好的结构治理基础：Go import boundary、复杂度预算、前端文件行数预算、`make verify` 和文档检查都已存在。本工作树已恢复架构与文档门禁，修正 release recipe 的缺失文件引用，并固定 Go/Web 验证工具与并发范围。限制 Vitest 为 4 个 worker 后，37 个 Web 测试文件、393 项通过；完整 `make verify`、正式 `make web` 与四平台 release 打包 smoke 均已通过。

FlowSession 现在明确支持一个 backend runtime 独占一个本地 `StateDir`，`Start` 通过 OS advisory lock 拒绝冲突实例；运行语义仍是单进程 at-least-once，不包含跨进程 lease、接管或 exactly-once。当前支持边界已明确；只有出现水平扩展需求时，才需要设计分布式契约。不要把“文件很大”直接等同于需要重写，也不要把单进程 at-least-once 边界泛化成所有部署模式下的缺陷。

本轮阶段 2/3 已补上 Web 路由 gzip 回归预算、Android 显式 native 准备、GitHub Actions 统一验证入口与 release archive 清单/smoke。剩余的性能工作是结合浏览器实际请求或生产遥测，判断 Chat 与 Orchestration 的冷路由预算中哪些资源确实会下载。

## 优先级与发现

### P0：恢复质量门禁和发布基线

| 发现 | 证据与影响 | 优化方案与验收 |
|---|---|---|
| 架构预算曾失败，当前工作树已恢复 | 原始基线中 12 个 Web 文件、7 个 Go 函数超过预算。当前重构拆分了 Chat、Flow、Settings、i18n、API、timeline、FlowSession 与工具构造代码；`go test ./internal/architecture -count=1` 已通过，前端例外表为空，Go 仍保留一个精确的 LSP 构造函数例外。 | 保持增量预算；新例外须说明原因、精确到符号且不得随意扩张。验收：架构测试持续通过，旧例外只减不增。 |
| 文档门禁曾失败，当前工作树已恢复 | 原始基线报告 16 项索引/状态错误；当前 `make docs-check` 已通过，文档索引、状态头和允许值已补齐。检查逻辑见 `scripts/check_docs.sh`。 | 保持 `docs-check` 在本地与 CI 统一入口中；新增顶层文档必须登记状态和索引。 |
| Release recipe 曾引用缺失文件，当前增加了文件 manifest 与归档校验 | 原始 recipe 要求 `README.en.md`，仓库并无该文件；当前只复制存在的 `README.md`。每个平台包现带二进制与 README 的大小/SHA-256；顶层清单汇总 archive hash，校验脚本验证内容并 smoke runner 同平台二进制。 | 隔离 `DIST_DIR` 下执行 `make release`，校验四个平台归档、文件 hash 与本机二进制 `--help`；GitHub tag workflow 上传包和清单。真实目标机签名/运行仍属于发布环境验收。 |

### P1：可靠性和契约收敛

| 发现 | 证据与影响 | 优化方案与验收 |
|---|---|---|
| Relay 异步测试曾在测试 goroutine 结束后断言，当前工作树已修复 | `internal/services/relay/compress_test.go` 的旧 Hub 测试在 goroutine 中调用 `t.Errorf` 并固定睡眠等待；现在用结果 channel 把错误交回测试 goroutine，并设置 deadline。 | 保持 goroutine 生命周期与测试同步；Relay 包压力测试及当前统一 Go 门禁通过。 |
| FlowSession 仍是单进程、at-least-once 契约，当前已拒绝同一状态目录的冲突 runtime | backend `Start` 对 `{StateDir}/.flow-session-runtime.lock` 获取 OS advisory lock；第二个 runtime 启动时报错。Stop 超时期间保持锁直到 worker 退出；进程退出后 OS 释放锁。此保护只适用于兑现 advisory lock 语义的本地文件系统，不是跨主机 lease/接管。durable event 崩溃恢复可能重放，纯 LLM 在 checkpoint 前崩溃可能重复费用/外部副作用；消费端仍自行用 sequence/output ID 去重，没有服务端 consumer ACK。 | 已固定支持拓扑为一个 backend runtime 独占一个本地 `StateDir`，并补充进程竞争、异常退出释放、Stop 超时、checkpoint 前重放和副作用幂等故障测试。若需要水平扩展，再设计跨进程 lease/接管；外部副作用继续传递稳定幂等键。只有产品需要服务端确认消费时才增加 ACK/outbox 契约。文档不宣称 exactly-once。 |
| Flow 文档矩阵与历史状态漂移，当前工作树已校正 | 原矩阵仍把 compiler/version store/FlowGram 标为 Planned；设计 §15 也保留了 F2b/F3 未完成的旧快照。当前矩阵已更新到 2026-09-30，区分 FlowRun 功能与仍属单进程 at-least-once 的 FlowSession；§15 标明快照并同步 human/loop/FlowGram 状态，§25.9 记录语音新轮次取消旧 durable work 的实现和测试。 | 功能状态继续以入口、实现、测试三者核对；C/D 跨进程租约、consumer ACK、exactly-once 和媒体编排仍作为明确后续边界，不宣称已实现。 |
| Flow 校验/执行热点曾超复杂度预算，当前架构门禁已通过 | 原始基线中的验证、区域构建和节点构造函数超过复杂度预算；当前职责拆分后架构测试通过。仍需避免为了降指标引入通用框架，并用行为测试守住 branch、loop、human、generation fencing、取消与重放语义。 | 复杂度测试继续作为回归信号；若复杂度增长，再按纯校验阶段、区域分析和执行策略拆分。 |

### P2：性能与开发环境治理

| 发现 | 证据与影响 | 优化方案与验收 |
|---|---|---|
| Web 生产构建存在大 chunk 警告 | `make web-bundle-check` 从 Vite manifest 汇总 HTML、JS、CSS 与声明资源，按入口静态依赖闭包去重并用 gzip level 6 估算冷加载。当前基线：shell 834.2 KiB；Chat 2,043.3 KiB；Orchestration 1,134.4 KiB；其他路由 840.6–884.9 KiB。`vendor-antd` 原始 minified chunk 仍约 1,266 KB。估算包含部分 CSS 声明的备用字体资源，并排除二级动态导入，因此不是浏览器真实请求量或 RUM。 | `make verify` 已纳入 shell 900 KiB、普通路由 1,024 KiB、Chat 2,304 KiB、Orchestration 1,280 KiB 的回归预算；阈值是当前基线的 guardrail，不是性能目标。后续优先用浏览器网络记录或生产遥测验证 Chat/Orchestration 的实际加载，再评估延迟 Markdown、FlowGram 或编辑器子功能；不为消除构建警告而盲目拆包。 |
| Android `preBuild` 曾隐式触发本机工具链和网络构建，当前已改为显式准备 | `mobile/android/app/build.gradle` 的 `prepareGodexNativeRuntime` 仅在显式调用时运行 runtime 下载/编译脚本；普通 `preBuild` 依赖 `verifyGodexNativeRuntime`，缺失 `.so` 时快速失败并提示命令。`build-and-install-android.sh` 显式准备 runtime，并强制重编内嵌当前 Web UI 的 `libgodex.so`。 | `./gradlew prepareGodexNativeRuntime` 是缺件准备入口；更新 UI 后用 `-PforceGodexBuild` 强制重编 Go 二进制。一键脚本与 `mobile/README.md` 已同步。已通过本机 Android `assembleDebug`；CI 尚未配置 Android SDK 构建 job，Android 验收目前仍是本机门禁。 |
| Go 测试发现范围可能被本机 `temp/` 目录污染，当前入口已显式限定 | `make verify` 现在使用 `./cmd/... ./examples/... ./internal/...`，排除被忽略的本机 `temp/` package。 | 本地与 CI 继续调用同一 package 集；清洁 checkout 与开发机的发现范围保持一致。 |
| Node 工具版本与 Web worker 并发曾造成验证不稳定，当前入口已固定 | Web 命令改用 Corepack，读取仓库的 pnpm 10.11.0 pin；Vitest 默认 fork 并发在本机首次运行有两个 worker 启动超时。设置 `WEB_TEST_MAX_WORKERS=4` 后 37 个文件、393 项通过，随后完整 `make verify` 已通过。 | 保持 Corepack 与可配置 worker 上限；本机与 CI 继续使用统一入口。 |

## 分阶段实施方案

### 阶段 0：恢复可信基线（已完成）

1. 架构预算、`docs-check` 与 release README 引用已在当前工作树修复。
2. Relay 异步断言已改为 channel 同步；Go 测试包范围和并发已固定。
3. 完整 `make verify`、正式 `make web` 与隔离 `DIST_DIR` 的四平台 release smoke 均已通过。

**完成标准：** 所有可执行门禁通过；未通过项有明确 owner、原因和禁止发布的边界，不使用忽略错误的脚本掩盖失败。

### 阶段 1：固化 FlowSession 契约（实现和完整本地门禁已完成）

1. 已复核 HEAD `80e43f9` 及当时工作树相对阶段基线的 Flow/Voice 状态；相关代码与文档随后已提交到 `e30bc36`，feature matrix 与 runtime design 已同步部署限制。
2. 已固定单 runtime/本地 `StateDir` 的支持边界；`backend.Start` 获取非阻塞 OS advisory lock，锁冲突时拒绝启动。共享盘/跨主机部署仍不支持，且不提供 lease 或自动接管。
3. 已验证服务重启后的 durable event 重放、纯 LLM checkpoint 前中断后重调、外部副作用按稳定幂等键去重、worker 迟到结果 fencing、客户端 WebSocket 重连去重，以及 Stop 超时期间保持锁。
4. `FlowSession` region runner 已在此前变更中拆到独立文件；本轮架构预算通过，没有为指标再引入通用框架。

**完成标准：** 运行语义、部署限制、取消行为与测试一致；不承诺未实现的 exactly-once 或服务端消费确认。FlowSession、Agent、HTTP API 定向测试及架构/文档门禁均通过；最终完整 `make verify` 已通过。

### 阶段 2：按数据治理体量与构建（实现和本地门禁已完成）

1. `make web-bundle-check` 构建嵌入版 Web 并输出 Vite manifest，按入口静态依赖闭包计算 cold gzip；检查后删除临时 manifest，不把分析文件带进产品。
2. 当前 gzip level 6 基线如下。`route delta` 是相对首屏 shell 新增的静态资源，不包括二级动态导入。

| Entry | Cold gzip | Route delta |
|---|---:|---:|
| Shell | 834.2 KiB | — |
| Chat | 2,043.3 KiB | 1,209.1 KiB |
| Files | 842.5 KiB | 8.3 KiB |
| Automation | 877.2 KiB | 43.0 KiB |
| Nodes | 840.9 KiB | 6.7 KiB |
| Notes | 869.8 KiB | 35.6 KiB |
| Skills | 846.1 KiB | 11.9 KiB |
| Agent Templates | 840.6 KiB | 6.4 KiB |
| Memory | 841.6 KiB | 7.4 KiB |
| TaskBoard | 884.9 KiB | 50.7 KiB |
| Orchestration | 1,134.4 KiB | 300.2 KiB |
| Settings | 868.2 KiB | 34.0 KiB |
| Usage | 845.1 KiB | 10.9 KiB |

3. `make verify` 强制 shell 900 KiB、普通路由 1,024 KiB、Chat 2,304 KiB、Orchestration 1,280 KiB 上限，作为回归阈值；它们不是目标性能值，也不能替代浏览器请求量或生产遥测。
4. Android native 构建已拆成 `prepareGodexNativeRuntime` 显式准备和 `verifyGodexNativeRuntime` 普通构建前检查；更新 Web 后可传 `-PforceGodexBuild` 重编内嵌 UI。`mobile/README.md` 与一键构建脚本已同步。
5. Node/pnpm pin 与 Go 测试发现范围沿用阶段 0 已完成的统一入口。

**完成标准：** 已有可重复的入口体积基线和回归预算；Android 缺件时普通构建不联网、不启动工具链，显式准备任务可构建完整 APK。

### 阶段 3：防止债务回流（实现完成；GitHub 执行结果待首个 CI run）

1. 新增 `.github/workflows/ci.yml`：PR、main push、版本 tag 共用 `make verify`；版本 tag 在验证通过后构建 release。
2. 每个 release archive 带 `manifest.json`（文件大小与 SHA-256）；`release-manifest.json` 汇总平台包 checksum，`SHA256SUMS` 可供外部校验。发布前验证归档内容与每个文件 hash，并对 runner 同平台二进制执行 `--help` smoke；CI 保留生成归档和清单 30 天。
3. `make verify` 仍包含架构预算、文档同步、Go/Web 测试，并增加 Web 冷路由预算；功能事实继续由 feature matrix 与权威文档约束。
4. 此前仓库未发现 CI workflow；新 workflow 尚未由 GitHub runner 实际执行。Android SDK/APK 构建目前未纳入该 workflow。

**完成标准：** 本地统一门禁与隔离 release 归档检查通过；首个 GitHub Actions run 成功后再把外部 CI 标记为已验证。

## 审计方法与工具

- **技能：** `codebase-memory` 用于先图谱后源码的结构查询；`karpathy-guidelines` 用于控制审计后的改动范围和验收标准。若审计 DOCX/PDF 或真实 UI 流程，再启用对应文档/浏览器工具；本次 Markdown 审计不需要额外安装通用插件。
- **插件/MCP：** 项目已配置 `codebase-memory-mcp`，本次使用其项目索引、架构热点、符号查询和路径覆盖检查。它是定位工具，不是完整性证明；关键结论回到源码、测试和命令输出。
- **流程：** 先记录 HEAD/未提交变更及验证范围；再盘点 package、路由、依赖边界、热点；对高风险符号追踪调用关系并读精确源码；运行质量门禁；最后将实现、测试、help 与文档相互对照。将发现分为确定缺陷、明确能力边界、需要测量的风险、一般维护成本。
- **排序：** 先看安全/数据正确性/发布阻断和影响范围，再看发生概率、检测能力与修复成本。文件行数、重复度、fan-in 和复杂度只作候选信号，不单独作为重构理由。

本次 Codebase Memory 索引 generation 为 `2026-09-30T06:37:14Z`，coverage 为 best-effort；本轮引用的 backend、FlowSession、processlock 与文档路径均重新核验了 coverage。Android `build.gradle:22` 与 `gradlew:180` 存在解析不完整，相关逻辑按文件直接读取；索引结果只用于定位，不作为完整性证明。

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

- 历史基线通过：阶段 0 的完整 `make verify`、`make web` 和四平台 release smoke 曾在 `d25bb4f` 工作树验证通过。本轮新增变更后的首次 `make verify` 曾有三个时序用例失败，隔离复跑均通过；最终完整 `make verify` 复跑已通过。
- 通过：本轮 FlowSession backend、Agent、HTTP API 定向测试；`internal/platform/processlock` 跨进程竞争及持锁进程异常退出测试；`internal/architecture` 和 `scripts/check_docs.sh`。
- 通过：processlock 测试包已交叉编译为 Windows amd64、macOS arm64 与 Linux amd64；尚未在 Windows 主机上执行其运行时测试。
- 通过：取消进程组回归用例连续运行 10 次；Relay 测试由 channel 返回结果，避免测试 goroutine 生命周期外调用 `testing.T`。
- 通过：最终 `make verify` 的 Go tests/vet、docs-check、TypeScript、37 个 Vitest 文件（393 项）和 `make web-bundle-check`；shell 834.2 KiB、全部 12 个路由均在预算内。
- 通过：`make web` 正式 TypeScript + Vite production build；隔离 `/tmp/godex-release-stage3-20260930` 下四平台归档通过文件清单、SHA-256、归档成员检查，并对 macOS arm64 包运行 `--help` smoke。
- 通过：Android `verifyGodexNativeRuntime`、`prepareGodexNativeRuntime` 与 `assembleDebug`；现有 native `.so` 齐全，因此缺件时的失败提示路径未通过删改本机产物做破坏性测试。
- 通过但有警告：构建仍有多个大于 500 KB 的 chunk，最大约 1.27 MB；路由预算是静态依赖闭包估算，不是浏览器网络请求实测。
- 未验证：GitHub Actions workflow 尚未由远端 runner 实际执行；跨平台 release 包只在 macOS arm64 本机运行 smoke，其他目标平台仍需各自 runner/设备启动验证。
- 初次无并发限制的 Vitest 运行中，两个 fork worker 启动超时；统一入口已加 `WEB_TEST_MAX_WORKERS=4`，限制后完整门禁通过。
