[简体中文](README.md) | [English](README.en.md)

# GoDex

<p align="center">
  <img src="ui/web/public/brand/godex-icon.jpg" alt="GoDex icon" width="160" />
</p>

GoDex 是一个本地优先的 AI Agent 工作台。它把 CLI、TUI、Web、HTTP API、Feishu/Weixin 等入口接到同一套后端，让聊天、工具执行、文件附件、长期记忆、子 agent、审批和运行审计共享同一个 session runtime。

## 界面速览

### tui
![](./docs/_images/tui.png)

### web ui
![](./docs/_images/web_ui_chat_1.png)

### web ui in mobile
![](./docs/_images/web_ui_mobile_remote.jpg)
图中是在执行遥测

## 产品定位

GoDex 面向需要把 AI Agent 真正接入日常工程工作流的团队和个人：

- 在本地项目中运行，保留 workspace、配置和运行状态的可控性。
- 支持长任务、工具调用、子 agent 和工作流，而不是只做单轮聊天。
- 所有高风险动作都可被配置、审批、追踪和复盘。
- Web UI 是主力管理入口，CLI/TUI/IM/API 共享同一套能力。

适合的场景：

- 代码库理解、修改、测试、部署辅助
- 多入口团队机器人，包括 Web、TUI、Feishu、Weixin
- 需要长期记忆、历史召回和可审计工具执行的本地 agent runtime
- 需要将 package、skill、automation、subagent 纳入统一治理的 agent 平台原型

## 核心特性

- **共享 Session Runtime**：CLI、TUI、Web、HTTP API、IM channel 共用 session、timeline、attachment、permission 和 memory。
- **Web 工作台**：可拖拽多面板网格布局（2×2 / 3×3），Chat、Terminal、Files、Automation、Nodes、Notes、Skills、Memory、Usage、Settings、审批面板和 subagent 管理，支持移动端自适应。
- **多 Provider 管理**：支持 Anthropic-compatible、OpenAI-compatible、OpenAI Codex provider、模型策略（primary/fallback/round_robin）、Web Settings 动态配置与 OpenAI-compatible `/v1/*` API。
- **长任务韧性**：Ralph-style LongTask story loop（动态并行 DAG）、auto-repair、validation artifact、auto merge/commit、runner phase checkpoint、重启恢复（`--resume-run-id`）、运行中 follow-up/steer、上下文预算按角色分配。
- **Agent 图与多引擎**：`agent_graph` 动态 DAG 抽象、`workflow` durable workflow、Harness 引擎抽象与 per-turn 多引擎热切换。
- **Agent Identity / Sandbox 解耦**：`Sandbox` 接口 + `LocalSandbox`，scope 隔离（session/personal/org）与写路径限定。
- **Context 与 Memory**：带 pinned continuation snapshot 的模型辅助压缩、rule-based fallback、transcript archive、history_search、durable memory、candidate inbox、audit/restore、紧凑 memory 注入和 token 估算。
- **Agent Profile**：CLI/TUI/ACP 默认走精简 `coding` profile，Web/IM 默认保留 `general` profile；可按入口或命令覆盖工具曝光策略。
- **工具与安全**：merge、grep（ripgrep 双后端）、edit_file 多编辑、LSP 代码智能、WorkspaceFS 文件边界、shell guard、manual/review/yolo approval、安全 profile、内容安全筛查器、loop guard、security audit。
- **Subagent / Workflow**：durable subagent job、角色→bundle 映射与写 scope 联动、review/merge/cancel/resume/iterate、LongTask Web/CLI/API surface、能力边界、隔离 workspace 策略和 compact handoff。
- **Package / Skill 生态**：package manifest（resources/app/tool_policy/smoke_tests/recommended_bundles）、role/command contract、tool policy、quality diagnostics、smoke run、reinstall tracking、Claude Code import。
- **Automation 与 Channel**：Cron（at/every/cron 调度）、Heartbeat（HEARTBEAT.md checklist + OK token）、Feishu、Weixin、OpenAI-compatible chat completions API；IM 审批消息会展示 tool 和关键参数摘要。
- **Control Plane 基础**：轻量 Node Registry 和只读 Nodes Dashboard，用于观测多个 GoDex runtime；Relay 中继（WSS 出站接入）、`node exec/forward` 跳板、`guarded-remote` 审批头。
- **Notes 工作台**：本地 Markdown 笔记、搜索/标签、Chat 中保存 Agent 输出到笔记、笔记↔记忆双向联动。
- **Memory 2.x**：candidate inbox、suppression、SQLite + FTS5 sidecar、scope-aware recall、project miner、记忆策略（per-turn/agent-only/consolidated）、foldCapture 去重。
- **Session 树**：会话可分支（fork/rollback/merge）、session 图持久化。
- **空间治理**：storage doctor、browser cache/session checkpoint/artifact/subagent GC。
- **终端**：Go PTY 原生后端 + xterm.js 前端，提供真实 Shell 体验。
- **用量追踪**：LLM token 用量记录（SQLite），Web Usage 面板与 `/usage/*` API。
- **性能**：Anthropic 风格 cache_control 断点、prompt 缓存、compaction 优化。
- **单二进制 Web UI**：Web dist 嵌入 Go binary，全平台（Linux/macOS/Windows）单文件部署。

## 快速开始

### 环境要求

- Go `1.26+`
- Node.js + `pnpm`，仅在需要构建 Web 前端时使用
- 至少一个可用 LLM provider

### 从源码运行

```bash
go mod download
pnpm -C ui/web install
pnpm -C ui/web build
go run ./cmd/godex serve --addr 127.0.0.1:8080
```

打开：

```text
http://127.0.0.1:8080
```

### 初始化项目

```bash
go run ./cmd/godex setup --dir /path/to/project
```

首次启动时，如果配置文件不存在，GoDex 会生成带注释的默认配置。

### 配置模型

推荐使用 Web `Settings` 管理 provider、模型和密钥引用。也可以使用 CLI 登录：

```bash
godex login openai --mode platform-api-key
godex login codex --mode codex-oauth
godex providers list
godex providers test <provider-id>
```

全局配置和默认运行态都放在 `~/.godex`：provider、skills、sessions、memory、tmp/cache、logs 等不会默认写入当前项目目录。项目目录只保留显式创建的 `godex.yaml`、`.env.example`、`AGENT.md` 等项目文件。详细配置说明见 [docs/user-guide.md](docs/user-guide.md#配置)。

## 常用入口

```bash
# 交互式 CLI
go run ./cmd/godex

# 单次提问。CLI/TUI/ACP 默认使用 coding profile
go run ./cmd/godex ask "总结一下当前仓库结构"
go run ./cmd/godex ask --profile general "帮我规划一个产品方案"

# 执行 slash command
go run ./cmd/godex command /doctor

# 检查和清理本地 runtime 空间
go run ./cmd/godex doctor storage
go run ./cmd/godex gc --dry-run

# 全屏 TUI（默认入口）
go run ./cmd/godex

# Web / HTTP / SSE / channel runtime
go run ./cmd/godex serve --addr 127.0.0.1:8080

# 导入 Claude Code 生态资源
go run ./cmd/godex import claude --source .claude --dry-run
```

更多命令、slash commands 和 HTTP API 见 [docs/user-guide.md](docs/user-guide.md)。

## Web 工作台

Web UI 是当前最完整的产品入口：

- **Chat**：多入口 session、附件、审批、模型切换、Context & Recall、timeline、subagent 进度、保存 Agent 输出到 Notes、会话分支。
- **Files**：文件树、代码编辑器、diff 与搜索（workspace 边界内）。
- **Settings**：全局/项目配置路径、provider/model、doctor、channel 状态、安全策略、service 运行状态。
- **Nodes**：只读观测本机和手动/自动注册的 GoDex runtime，支持远程 Chat/Terminal/Files。
- **Notes**：本地 Markdown 笔记、标签、搜索、编辑和 Chat 集成。
- **Memory**：durable memory、candidate inbox、suppression、audit diff、restore/reapply。
- **Skills**：package/skill 管理、质量诊断、smoke run、reinstall。
- **Automation**：Cron、Heartbeat、运行日志。
- **Usage**：LLM 用量、模型/keys 管理、缓存命中统计。

前端构建（自动输出到 Go embed 目录）：

```bash
pnpm -C ui/web build
```

Package 开发流程可以使用示例 skill 辅助：

```text
examples/skills/package-developer
```

在 Web `Skills` 的安装入口或 Chat 中安装这个本地 skill 后加载 `package-developer`，它会指导创建 `godex.package.yaml`、测试 smoke、安装 GitHub package、重装和卸载。

## 遥测（Telemetry）

GoDex 本地优先：**默认不向任何外部服务上报数据**。遥测能力分两层，都只在你显式启用时工作：

- **Control Plane 节点遥测**：当节点配置了 `control.center_url` + `control.credential` 并接入中心时，节点会通过 relay 周期性把本地运行快照推送给中心——包括运行中的 session（id/title/running/updated_at）、longtask 进度（status/phase/turn/total）与待审批请求（tool/action/paths），以及节点版本与能力列表。中心「Nodes」页与 `GET /control/nodes/{id}/overview` 据此展示实时进度（如图中手机端所见的遥测面板）。**不接入中心（默认）则不会产生任何推送**；快照只含摘要级状态，完整会话历史始终保留在节点本地，中心只读观测、不存会话内容。
- **LLM 用量追踪**：每次模型调用的 token 用量与缓存命中会记录到本地 SQLite（`usage` 服务），通过 Web `Usage` 面板与 `/usage/*` API 查看，仅用于成本与用量统计，不上传。

隐私边界：所有遥测数据默认落在 `~/.godex` 本地；只有显式配置中心接入时，才向该中心推送上述摘要状态，且中心不持久化完整会话历史。

## Agent Profile

`agent.profile` 是入口/任务提示词策略（控制默认回复风格与能力使用引导），不替代 `security.profile`。默认入口策略是：

- `acp`、`cli`、`tui`：`coding`，提示词要求 agent 默认走精简编码工作流（简洁回复、先读代码再改、优先 `lsp`），并仅在用户明确要求时通过 `tool_exchange` 启用 web/browser/subagent 等重能力 bundle。
- `web`、`weixin`、`feishu`：`general`，保留完整工作台体验（含 skill catalog 注入）。

说明：coding/general 的工具目录相同（always-active / default-active 工具集合一致），差异在系统提示词与运行时注入内容（coding 用 `repo_map` 替换 `skill_catalog`）。CLI/TUI/ACP 可用 `--profile general|coding` 临时覆盖；也可用 `GODEX_AGENT_PROFILE` 或 Web `Settings` 的 `agent.default_profiles.*` 修改。

## 里程碑

### 当前基线（2026-08 已实现）

GoDex 1.x 已经是一个本地优先、可部署、可审计的 Agent 工作台：

**运行时与韧性**
- 多入口共享同一个 session runtime：CLI、TUI、Web、HTTP API、Feishu、Weixin、Cron、Heartbeat。
- 异步 turn runtime + durable event journal + checkpoint；幂等存储（cron/heartbeat）；worker lease（崩溃标记 interrupted 不自动重跑）；重启恢复。
- Turn Error 分层（Retryable/Transient/NonRetryable）、loop guard（no-mutation 螺旋检测）、runner phase checkpoint、空回复/`finish_reason=length` 恢复。
- Harness 多引擎抽象与 per-turn 引擎热切换。

**多 Agent 编排**
- durable subagent job：review / merge / cancel / resume / iterate、角色→bundle 映射、写 scope 联动、按角色上下文预算、compact handoff。
- `workflow` 与 `agent_graph`：动态并行 DAG（data_dependency / control_flow / handoff 边）、重启可恢复。
- LongTask story loop：按 PRD/user stories 编译动态并行 DAG、auto-repair、validation artifact、auto merge/commit、`--resume-run-id` 续跑。
- 会话分支（fork / rollback / merge）与 session 图持久化。

**Context 与 Memory**
- 带 pinned continuation snapshot 的模型辅助压缩、rule-based fallback、transcript archive、`history_search`。
- durable memory：candidate inbox、suppression、audit/restore、SQLite + FTS5 sidecar、scope-aware recall、project miner、记忆策略（per-turn / agent-only / consolidated）、foldCapture 去重。
- 笔记 ↔ 记忆双向联动、context inspector、token 估算。

**工具与安全**
- 56 个工具 / 14 个 bundle：shell/file/grep(ripgrep)/LSP/browser/desktop/web/memory/skill/package/subagent/workflow/MCP/teamtools 等，`tool_exchange` 按需启用。
- WorkspaceFS 文件边界、shell guard、manual/review/yolo 审批、安全 profile（trusted-local … dev/repair）、内容安全筛查器、loop guard、security audit。
- Scope 隔离（session / personal / org）与写路径限定。

**生态与治理**
- Package / Skill 生态：manifest（resources/app/tool_policy/smoke_tests/recommended_bundles）、quality 诊断、smoke run、reinstall、Claude Code import。
- Automation 与 Channel：Cron（at/every/cron）、Heartbeat（HEARTBEAT.md checklist + OK token）、Feishu、Weixin、OpenAI-compatible `/v1/*` API。
- Control Plane：Node Registry + Relay 中继（WSS 出站接入）、`node exec/forward` 跳板、`guarded-remote` 审批头。
- Storage doctor / GC、LLM 用量追踪、单二进制 Web UI、自部署 `service install`。

### 2.0 规划（进行中）

GoDex 2.0 的目标是从单个大 Agent 工作台升级为可承载重任务的 Agent Runtime 平台。当前进展：

| 方向 | 状态 | 说明 |
|------|------|------|
| **Agent 与 Sandbox 解耦** | ✅ 接口已落地 | `Sandbox` 接口 + `LocalSandbox` + scope 隔离已实现（roadmap 3.3/6.2）；后续方向是更多后端（WASM、远程） |
| **Orchestrator 与 Worker 解耦** | 🚧 进行中 | 已有 durable subagent / workflow / longtask runtime；目标是更清晰的 worker runtime 协议与能力边界 |
| **Session 记忆树** | 🚧 部分落地 | fork / rollback / merge 已实现（`sessiongraph`）；目标是更完整的版本化上下文（clone、rebuild、跨存储） |
| **Session 与存储介质解耦** | ✅ 双后端已实现 | JSON + SQLite 镜像（`sessionstore`）；后续支持数据库、云存储等后端 |
| **统一插件内核** | 📋 规划中 | Plugin Kernel + 可选 WASM 执行器 + MCP 完整 client；设计见 [DSH 研究笔记](docs/research_of_dsh_for_godex_optimize.md) |

详细架构方向见 [GoDex 2.0 架构 SPEC](docs/architecture-v2-spec.md)。

## 文档

- [GoDex 2.0 架构 SPEC](docs/architecture-v2-spec.md)：Agent/Sandbox、Orchestrator/Worker、Session Graph 和存储解耦路线。
- [用户指南](docs/user-guide.md)：安装、配置、Provider、CLI、Web UI、工具、Memory、命令、HTTP API、自动化、安全、故障排查。
- [代码与设计 Review](docs/code-review-2026-08-15.md)：文档↔实现一致性审查与代码侧发现。
- [DSH 研究笔记](docs/research_of_dsh_for_godex_optimize.md)：DeepSeek Harness 插件设计对 GoDex 的改进启示（插件内核、WASM 边界、路线图）。
- [项目结构](docs/project-structure.md)：目录职责和重构边界。
- [Memory 设计原则](docs/memory-design-principles.md)：长期记忆、候选、召回和审计设计。
- [Workflow Runtime](docs/workflow-runtime.md)：workflow/subagent runtime 设计。
- [TUI 设计](docs/tui-bubbletea-design.md)：MintUI 全屏前端与多入口交互设计。
- [自部署指南](docs/self-deploy.md)：部署到服务器和自管理运行。
- [能力增强 v2](docs/capability-enhancement-v2.md)：App Shell、Node Registry、Notes、Claude import 等阶段性方案和进展。
- [P0-P6 端到端验证](docs/p0-p6-e2e-validation.md)：手工验收清单。
- [高 ROI Roadmap](docs/roadmap-high-roi.md)：当前能力基线和后续方向。
- [运行时加固路线](docs/roadmap-runtime-hardening.md)：面向通用、强大、长任务的深度 review 路线。
- [能力增强 v1](docs/capability-enhancement-v1.md)：Agent 平台化的早期规划基线。

## 目录结构

```text
cmd/godex/        CLI binary 入口
internal/app/     CLI、serve、slash command 组装
internal/agent/   agent loop、context、turn runtime、subagent、harness 引擎、agent graph
internal/runtime/ HTTP/WebUI、IM channels、Cron、Heartbeat
internal/services/ backend、commands、historysearch、noderegistry、relay、sessionadmin、usage、eval
internal/tools/   bash/file/browser/web/memory/skill/package/subagent/teamtools 等工具
internal/toolruntime/  typed tool 框架、权限、拦截器、执行上下文
internal/sandbox/ Sandbox 接口与 LocalSandbox 实现（Agent Identity 解耦）
internal/core/    config、conversation、compress、memory、notes、skill、package、media、mcp、security、scope
internal/domain/  跨层共享领域类型（events、message、security、eval 等）
internal/sessiongraph/  可分支 session 图
internal/sessionstore/   session 存储后端（json / sqlite）
internal/platform/  fs、logger、workspace 路径、tooling、storagegc 等基础设施
internal/tui/     min-tui 全屏前端
internal/uiassets/ 嵌入的 Web dist
internal/acp/     ACP stdio server
ui/web/           React + Vite Web 前端
docs/             产品、架构、验证和部署文档
```

更完整说明见 [docs/project-structure.md](docs/project-structure.md)。

## Release Check

发布前建议运行：

```bash
./scripts/release_check.sh
```

它会执行 Go 测试、Go binary 构建和 Web build。本地也可以单独运行：

```bash
go test ./...
pnpm -C ui/web build
git diff --check
```
