[简体中文](README.md) | [English](README.en.md)

# GoDex

<p align="center">
  <img src="ui/web/public/brand/godex-icon.jpg" alt="GoDex icon" width="160" />
</p>

GoDex 是一个本地优先的 AI Agent 工作台。它把 CLI、TUI、Web、HTTP API、Feishu/Weixin 等入口接到同一套后端，让聊天、工具执行、文件附件、长期记忆、子 agent、审批和运行审计共享同一个 session runtime。

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
- **Web 工作台**：Chat、Automation、Nodes、Notes、Skills、Memory、Settings、Context & Recall、审批面板和 subagent 管理。
- **多 Provider 管理**：支持 Anthropic-compatible、OpenAI-compatible、OpenAI Codex provider、模型策略和 Web Settings 动态配置。
- **长任务韧性**：Ralph-style LongTask story loop、auto-repair、validation artifact、auto merge/commit、runner phase checkpoint、运行中 follow-up/steer。
- **Context 与 Memory**：带 pinned continuation snapshot 的模型辅助压缩、rule-based fallback、transcript archive、history_search、durable memory、candidate inbox、audit/restore、紧凑 memory 注入和 token 估算。
- **Agent Profile**：CLI/TUI/ACP 默认走精简 `coding` profile，Web/IM 默认保留 `general` profile；可按入口或命令覆盖工具曝光策略。
- **工具与安全**：WorkspaceFS 文件边界、shell guard、manual/review/yolo approval、安全 profile、security audit。
- **Subagent / Workflow**：durable subagent job、review/merge/cancel/resume、LongTask Web/CLI/API surface、能力边界、隔离 workspace 策略和 compact handoff。
- **Package / Skill 生态**：package manifest、role/command contract、tool policy、quality diagnostics、smoke run、reinstall tracking、Claude Code import。
- **Automation 与 Channel**：Cron、Heartbeat、Feishu、Weixin、OpenAI-compatible chat completions API；IM 审批消息会展示 tool 和关键参数摘要。
- **Control Plane 基础**：轻量 Node Registry 和只读 Nodes Dashboard，用于观测多个 GoDex runtime。
- **Notes 工作台**：本地 Markdown 笔记、搜索/标签、Chat 中保存 Agent 输出到笔记。
- **空间治理**：storage doctor、browser cache/session checkpoint/artifact/subagent GC。
- **单二进制 Web UI**：Web dist 可嵌入 Go binary，部署时无需单独托管前端资源。

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

全局配置和默认运行态都放在 `~/.godex`：provider、skills、sessions、memory、tmp/cache、logs 等不会默认写入当前项目目录。项目目录只保留显式创建的 `godex.yaml`、`.env.example`、`AGENT.md` 等项目文件。详细配置说明见 [docs/user-guide.md](docs/user-guide.md#配置与-provider)。

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

- **Chat**：多入口 session、附件、审批、模型切换、Context & Recall、timeline、subagent 进度、保存 Agent 输出到 Notes。
- **Settings**：全局/项目配置路径、provider/model、doctor、channel 状态、安全策略。
- **Nodes**：只读观测本机和手动/自动注册的 GoDex runtime。
- **Notes**：本地 Markdown 笔记、标签、搜索、编辑和 Chat 集成。
- **Memory**：durable memory、candidate inbox、suppression、audit diff、restore/reapply。
- **Skills**：package/skill 管理、质量诊断、smoke run、reinstall。
- **Automation**：Cron、Heartbeat、运行日志。

前端构建和内置资源同步：

```bash
pnpm -C ui/web build
./scripts/sync_embedded_web.sh
```

Package 开发流程可以使用示例 skill 辅助：

```text
examples/skills/package-developer
```

在 Web `Skills` 的安装入口或 Chat 中安装这个本地 skill 后加载 `package-developer`，它会指导创建 `godex.package.yaml`、测试 smoke、安装 GitHub package、重装和卸载。

## Agent Profile

`agent.profile` 控制默认 agent 行为，不替代 `security.profile`。默认入口策略是：

- `acp`、`cli`、`tui`：`coding`，默认只暴露核心编码、todo、`tool_exchange` 和必要的会话/压缩/历史工具。
- `web`、`weixin`、`feishu`：`general`，保留完整工作台体验。

当 coding profile 需要联网、浏览器、subagent、skill、memory、package 等能力时，agent 会通过 `tool_exchange` 按需启用对应 bundle。CLI/TUI/ACP 可用 `--profile general|coding` 临时覆盖；也可在 Web `Settings` 修改 `agent.default_profiles.*`。

## 文档

- [用户指南](docs/user-guide.md)：安装、配置、Provider、Web UI、工具、Memory、API、发布检查。
- [项目结构](docs/project-structure.md)：目录职责和重构边界。
- [Memory 设计原则](docs/memory-design-principles.md)：长期记忆、候选、召回和审计设计。
- [Workflow Runtime](docs/workflow-runtime.md)：workflow/subagent runtime 设计。
- [自部署指南](docs/self-deploy.md)：部署到服务器和自管理运行。
- [能力增强 v2](docs/capability-enhencement-v2.md)：App Shell、Node Registry、Notes、Claude import 等阶段性方案和进展。
- [P0-P6 端到端验证](docs/p0-p6-e2e-validation.md)：手工验收清单。
- [高 ROI Roadmap](docs/high-roi-roadmap.md)：当前能力基线和后续方向。

## 目录结构

```text
cmd/godex/        CLI binary 入口
internal/app/     CLI、serve、slash command 组装
internal/agent/   agent loop、context、turn runtime、subagent
internal/runtime/ HTTP/WebUI、IM channels、Cron、Heartbeat、REPL
internal/services/backend、commands、historysearch、noderegistry、sessionadmin
internal/tools/   bash/file/browser/web/memory/skill 等工具
internal/core/    config、conversation、compress、memory、notes、skill、media
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
./scripts/sync_embedded_web.sh
git diff --check
```
