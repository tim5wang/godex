# GoDex 用户指南

本文档承接 README 中不适合放在产品总览里的细节，覆盖安装运行、配置、Web UI、工具、Memory、命令、HTTP API 和发布检查。

## 环境要求

- Go `1.26+`
- Node.js + `pnpm`，用于构建 Web 前端
- 至少一个可用 LLM provider

## 安装与启动

安装依赖：

```bash
go mod download
pnpm -C ui/web install
```

初始化一个工作区：

```bash
go run ./cmd/godex setup --dir /path/to/project
```

启动 Web / HTTP / SSE / channel runtime：

```bash
pnpm -C ui/web build
go run ./cmd/godex serve --addr 127.0.0.1:8080
```

打开：

```text
http://127.0.0.1:8080
```

常用入口：

```bash
# 交互式 REPL
go run ./cmd/godex

# 单次提问
go run ./cmd/godex ask "总结一下当前仓库结构"
go run ./cmd/godex ask --stdin < prompt.txt
go run ./cmd/godex ask --profile general "帮我做产品规划"

# 执行 slash command
go run ./cmd/godex command /doctor
go run ./cmd/godex command "/history search weixin scope=session_archive"

# 全屏 TUI（默认入口）
go run ./cmd/godex

# 旧 readline REPL
go run ./cmd/godex repl

# 检查和清理 runtime 空间
go run ./cmd/godex doctor storage
go run ./cmd/godex gc --dry-run

# 导入 Claude Code 生态资源
go run ./cmd/godex import claude --source .claude --dry-run
```

## 配置与 Provider

GoDex 将跨项目稳定资产和项目状态分开管理：

- `GODEX_HOME` 默认是 `~/.godex`，保存全局配置、全局 `.env`、provider、skills、packages、logs、sessions、memory、transcripts、tasks、todos、tmp/cache、rules 和 MCP 默认配置。
- `GODEX_PROJECT_DIR` 默认是当前工作目录，只作为代码/文件工具的 workspace 边界。GoDex 不再默认在任意运行目录创建 workspace `.godex`；需要项目本地状态时请在配置中使用绝对路径显式指定。

配置加载顺序：

```text
defaults < home godex.yaml < project/legacy godex.yaml < home .env < project .env < process env < explicit flags
```

推荐用 Web `Settings` 修改配置。它会展示：

- home/project config 路径
- home/project env 路径
- stored config 与 effective runtime 的差异
- doctor warning 和 migration 建议
- provider 状态、模型列表和密钥是否存在

### Agent Profile

`agent.profile` 是入口/任务 profile，用来控制默认工具曝光和提示词权重；它不替代 `security.profile`，权限审批仍由安全配置决定。

默认策略：

```yaml
agent:
  profile: general
  default_profiles:
    acp: coding
    cli: coding
    tui: coding
    web: general
    weixin: general
    feishu: general
```

`coding` profile 面向 IDE/CLI/TUI 编码任务：默认暴露 `bash`、`glob`、`read_file`、`write_file`、`edit_file`、`attach_file`、todo 和 `tool_exchange`。需要 web/browser/subagent/skill/memory/package 等能力时，agent 会按需通过 `tool_exchange` 启用。可用 `GODEX_AGENT_PROFILE` 覆盖全局，也可用 `--profile general|coding` 覆盖 `ask`、`acp-server` 当前进程。

CLI Provider 命令：

```bash
godex login openai --mode platform-api-key
godex login codex --mode codex-oauth
godex logout openai
godex logout codex
godex providers list
godex providers test <provider-id>
godex migrate home --dry-run
godex migrate home
```

Control Plane 节点配置用于只读观测多个 GoDex runtime。中心服务由 `godex serve` 或 `godex service` 启动，其他节点可以通过 `control.center_url` 自动注册，也可以在中心配置中手动声明节点：

```yaml
control:
  node_name: local-project-a
  center_url: http://127.0.0.1:8088
  heartbeat_seconds: 15
  offline_after_seconds: 60
  nodes:
    - id: cloud-prod
      name: Cloud Prod
      endpoint: https://godex.example.com
      workspace_dir: /opt/godex
      godex_home: /opt/godex/.godex
      capabilities:
        - web
        - feishu
```

Provider 示例：

```yaml
api:
  default_model: anthropic.sonnet
  providers:
    anthropic:
      type: anthropic_compatible
      base_url: https://api.anthropic.com
      api_key_env: ANTHROPIC_API_KEY
      credential_kind: api-key
      models:
        sonnet:
          model: claude-sonnet-4-20250514
          max_tokens: 4096
    openai:
      type: openai_compatible
      base_url: https://api.openai.com/v1
      api_key_env: OPENAI_API_KEY
      credential_kind: api-key
      models:
        gpt:
          model: gpt-4.1
          max_tokens: 4096
  model_strategy:
    type: fallback
    candidates:
      - provider: anthropic
        model: sonnet
      - provider: openai
        model: gpt
```

常见环境变量：

```bash
ANTHROPIC_API_KEY=
OPENAI_API_KEY=
GODEX_OPENAI_CODEX_OAUTH_TOKEN=
GODEX_HOME=
GODEX_PROJECT_DIR=
GODEX_WEB_TOKEN=
FEISHU_ENABLED=
FEISHU_APP_ID=
FEISHU_APP_SECRET=
FEISHU_DOMAIN=lark
```

## Web UI

Web UI 是当前主力管理入口。

### Chat

- 共享会话工作区
- 跨 channel session 列表与过滤
- 消息附件上传与回显
- 消息 Markdown 渲染和一键复制
- browser screenshot / artifact 回传
- composer 上方待审批 banner 与右侧审批面板
- IM channel 审批消息展示 tool、action、command/path、关键 input preview、reason 和 approve/deny 指令
- `Context & Recall` 解释面板
- runner phase、identity、subagent 事件
- 运行中 follow-up / steer
- session timeline
- 将 Agent 输出保存到 Notes，或在当前 Note 上下文中继续对话

### Settings

- 本地 token / 默认 session key
- 后端配置中心
- `stored / effective` 对照
- doctor 报告
- provider 状态、模型发现和模型选择
- runtime channel 状态
- Weixin 登录 / 重登 / 登出

### Automation

- Cron jobs
- Heartbeat 规则
- 运行日志

### Nodes

Nodes 是只读 Control Plane Dashboard。它展示当前中心服务已知的 GoDex runtime：

- node id / name
- workspace dir / godex home
- online / offline 状态
- version / capabilities
- last seen / heartbeat 时间

当前阶段不做远程任务派发、跨 node approval 转发或跨 node session 聚合。

### Notes

Notes 是内置 Markdown 工作台，用于验证“工作台为主，Agent 为辅助入口”的产品形态：

- 本地 Markdown 文件存储
- 笔记列表、编辑、搜索、标签过滤
- Chat 中保存 assistant 输出到笔记
- 笔记页打开时，Chat turn 可携带当前 note object context

Slash command：

```bash
godex command '/note create Architecture --tags review -- Initial notes'
godex command '/note list --tag review'
godex command '/note search architecture'
godex command '/note append architecture -- More notes'
godex command '/note update architecture -- Replacement'
```

### Skills

- 已激活 skills
- skills source 检索
- package quality diagnostics
- smoke run
- reinstall tracking

如果要创建或维护 Godex package，可以先安装并加载示例 skill：

```text
examples/skills/package-developer
```

它覆盖 `godex.package.yaml`、prompts、commands、roles、smoke tests、安装、重装、卸载和常见诊断。

### Memory

- durable memory 管理
- candidate inbox
- suppression 列表
- layered context preview
- `Always include / core`
- 手动触发 `Mine project docs`
- 手动触发 `Digest`
- audit log、before/after diff 与 restore/reapply

### Context & Recall

- total / history token estimate
- token breakdown 表格
- compression reasons
- large tool result / artifact reference
- history recall 状态
- active phase 与 compact 建议

### Subagents

- durable subagent 列表与详情
- identity / role / capability summary
- phase / last tool / progress summary
- review / merge / cancel / resume
- result preview、result bytes、result digest

### 静态资源

静态资源加载策略：

- 优先使用磁盘上的 `ui/web/dist`
- 若磁盘资源不存在，则回退到二进制内置资源

更新前端后同步内置资源：

```bash
pnpm -C ui/web build
./scripts/sync_embedded_web.sh
```

## Channel 接入

### Feishu / Lark

```yaml
channels:
  feishu:
    enabled: true
```

```bash
FEISHU_APP_ID=
FEISHU_APP_SECRET=
FEISHU_DOMAIN=lark
```

启动：

```bash
go run ./cmd/godex serve
```

当 IM 消息触发工具审批时，GoDex 会在回复中显示审批对象摘要，包括 tool name、action、command/path/url 等关键参数、reason、request id，以及 `/approve <id>`、`/approve <id> session`、`/deny <id>` 指令。

### Weixin / iLink

```yaml
channels:
  weixin:
    enabled: true
```

首次登录：

```bash
go run ./cmd/godex weixin setup
```

也可以在 Web `Settings` 中发起 Weixin 登录。

登出：

```bash
go run ./cmd/godex weixin logout
```

## Tools / Browser / Attachments

当前内建工具覆盖：

- 文件读写 / 文本替换
- bash / background_run
- todo / task board
- `web_search`
- `web_fetch`
- `glob`
- `browser`
- `desktop`
- `history_search`
- `attach_file`

Browser tool 适合：

- 打开网页
- 搜索、点选、表单填写
- 上传文件 / 下载文件
- 截图
- 遇到登录、验证码、二次确认等用户门禁时切到可见浏览器，用户完成后 `resume`
- 简单多步自动化

Desktop tool v1 适合：

- macOS / Linux / Windows 桌面截图并作为 artifact 回传
- 查看应用窗口标题
- 点击屏幕坐标、输入文本、按常见按键
- 读取和设置系统剪贴板
- 使用 `tesseract` CLI 做轻量 OCR，查找并点击屏幕文字

Artifact 策略：

- 不从普通 tool output 里启发式猜路径
- 只有工具显式返回 `ArtifactPaths` 时，才提升成 session attachment
- `attach_file` 是显式把本地文件作为附件发送的入口

## Storage / GC

GoDex 将可重建 cache、session checkpoint、tool artifact 和 subagent workspace 纳入统一空间治理。默认不会自动删除 durable memory、安全审计和用户生成报告。

诊断：

```bash
godex doctor storage
```

清理：

```bash
godex gc --dry-run
godex gc browser-cache --dry-run
godex gc sessions --dry-run --older-than 168h
godex gc artifacts --dry-run --older-than 168h
godex gc subagents --dry-run --merged --older-than 24h
```

配置项：

```yaml
storage:
  tmp_ttl_hours: 72
  artifact_ttl_hours: 168
  browser_cache_auto_clean: true
  browser_cache_max_mb: 256
  session_checkpoint_keep_latest: 20
  session_checkpoint_ttl_hours: 168
  session_checkpoint_auto_prune: true
```

## 工具执行与安全

Command execution 默认在 workspace 内运行。`bash`、`background_run`、package smoke、isolated subagent bash 共用执行 guard：

- 危险命令 denylist
- `curl|sh`、`wget|sh`、process substitution、base64 decode 执行、`python -c`、`node -e` 等风险分级
- metadata/private URL 拦截
- workspace escape 检测
- 最小环境变量
- approval prompt
- output capture
- security audit

安全 profile：

- `trusted-local`
- `guarded-local`
- `sandboxed`
- `strict`
- `host-privileged`

需要更强隔离时，可以切到 Docker 后端：

```yaml
tools:
  execution:
    mode: docker
    docker_image: golang:1.26
    docker_network: none
```

Docker 模式会把当前 workspace 挂载到容器的 `/workspace`。

也可以用全局 shell policy 收窄命令面：

```yaml
tools:
  execution:
    shell_allow_patterns:
      - "go test*"
      - "rg *"
    shell_deny_patterns:
      - "curl http://169.254.*"
```

Package role 的 `tool_policy` 支持同样的 role 级约束，例如 `shell:allow:go test*`、`shell:deny:curl *`。

## Memory / History

系统区分三件事：

1. 当前会话历史
2. durable memory
3. history recall

Durable memory 用于保存跨 session 仍然重要的内容：

- 用户偏好
- 稳定 workflow
- 项目事实
- 长期 warning

自动来源不会直接写 durable memory，而是先进入 candidate inbox，等待人工 accept / dismiss。

常用命令：

```bash
go run ./cmd/godex command /memory-digest
go run ./cmd/godex command "/memory-log 20"
go run ./cmd/godex command "/memory-restore <audit-id> before"
```

`history_search` 只负责“查以前说过什么”，不自动写 durable memory，也不自动生成 candidate。

当前分层：

- `L0 identity`
- `core`
- `relevant recall`

为了降低 token 成本，identity/core memory 默认只注入 title/type/file/summary；relevant recall 可注入截断后的内容片段。动态 session ledger 会作为 ephemeral runtime message 注入，而不是拼进 system prompt 中间，以提高稳定 prompt 前缀的缓存命中率。

更完整设计见 [memory-design-principles.md](memory-design-principles.md)。

## Subagent / Workflow / Package

Subagent / Workflow 支持：

- durable subagent job storage
- batch / wait / resume / cancel / review / merge
- per-job timeout
- workflow handoff artifact
- LongTask story loop：按 PRD/user stories 编译 workflow node，validation 通过后自动 merge/commit，失败时可显式开启 auto-repair
- 父上下文只暴露 compact handoff metadata，不暴露 subagent raw messages/progress

LongTask CLI：

```bash
godex longtask list --session local:default
godex longtask create --file prd.json --session local:default
godex longtask run lt_checkout --auto-repair --max-repair-attempts 2
godex longtask status lt_checkout
```

Web Chat inspector 的 `LongTasks` tab 可查看 story progress、validation、merge、commit 和 repair attempts，并提供 run/finalize/cancel 控制。详细设计见 [workflow-runtime.md](workflow-runtime.md)。

Package command declaration 支持：

- `prompt_only`
- enqueue `agent_turn`
- start durable `subagent_job`
- named package roles
- capability / tool policy
- explicit smoke tests

Smoke execution 绑定 backend session，并复用普通 shell permission、approval 和 security audit 路径。

Package 开发辅助：

- 示例 skill：`examples/skills/package-developer`
- 本地安装源：使用 Web `Skills` 的安装入口或 Chat 中的 `install_skill` 工具安装该路径。
- GitHub package 安装源：Web `Skills -> Packages` 可填写 `owner/repo` 或 `https://github.com/owner/repo.git`。
- 调试顺序：先看 package quality，再看 Commands/Roles/Prompts，最后显式运行 smoke。

Claude Code 生态导入：

```bash
godex import claude --source .claude --dry-run
godex import claude --source .claude
godex import claude --source ~/.claude --package claude-user
```

映射关系：

```text
.claude/skills/*/SKILL.md  -> GoDex skill
.claude/commands/**/*.md   -> GoDex package command
.claude/agents/**/*.md     -> GoDex package role
.claude/settings*.json     -> diagnostics only
```

导入过程默认不调用 LLM，不自动执行 hooks，不自动启用 MCP；不兼容项会以 diagnostics/warnings 形式展示。

## 常用 Slash Commands

- `/doctor`
- `/channels`
- `/compact`
- `/clear`
- `/session`
- `/approve`
- `/deny`
- `/memory list|get|search|candidates|accept|dismiss`
- `/memory-digest`
- `/memory-log [limit]`
- `/memory-restore <audit-id> [before|after]`
- `/packages list|commands|roles|prompts`
- `/history search <query> [scope=...] [limit=N] [role=...]`
- `/note list|search|get|create|append|update`

这些命令既可以在本地 `command` 模式使用，也会通过 Web / IM channel 复用同一套后端执行。

## HTTP API

鉴权方式：

```http
Authorization: Bearer <token>
```

主要 API 分组：

### Meta / Config / Channels

- `GET /meta`
- `GET /config/meta`
- `GET /config/schema`
- `GET /config`
- `PUT /config`
- `POST /config/reveal`
- `GET /config/doctor`
- `GET /channels`
- `GET /channels/weixin/auth`
- `POST /channels/weixin/auth/start`
- `POST /channels/weixin/auth/logout`

### Control Plane

- `GET /control/nodes`
- `GET /control/nodes/{id}`
- `POST /control/nodes/register`
- `POST /control/nodes/{id}/heartbeat`

### Automation

- `GET /automation/cron/jobs`
- `POST /automation/cron/jobs`
- `GET /automation/cron/jobs/{id}`
- `PATCH /automation/cron/jobs/{id}`
- `DELETE /automation/cron/jobs/{id}`
- `POST /automation/cron/jobs/{id}/run`
- `GET /automation/cron/jobs/{id}/runs`
- `GET /automation/heartbeat`
- `PUT /automation/heartbeat`
- `POST /automation/heartbeat/test`
- `GET /automation/heartbeat/logs`

### Memory

- `GET /memory`
- `GET /memory/candidates`
- `GET /memory/suppressions`
- `GET /memory/context`
- `GET /memory/audit`
- `POST /memory/remember`
- `POST /memory/update`
- `POST /memory/forget`
- `POST /memory/digest`
- `POST /memory/mine/project`
- `POST /memory/audit/{auditID}/restore`
- `POST /memory/candidates/{fingerprint}/accept`
- `POST /memory/candidates/{fingerprint}/dismiss`

### Notes

- `GET /notes`
- `GET /notes/{id}`
- `POST /notes`
- `DELETE /notes/{id}`

### Sessions

- `POST /sessions`
- `GET /sessions`
- `DELETE /sessions/{id}`
- `GET /sessions/{id}`
- `GET /sessions/{id}/context-inspector`
- `GET /sessions/{id}/timeline`
- `GET /sessions/{id}/subagents`
- `GET /sessions/{id}/subagents/{jobID}`
- `GET /sessions/{id}/permissions`
- `POST /sessions/{id}/permissions/{requestID}/approve`
- `POST /sessions/{id}/permissions/{requestID}/deny`
- `POST /sessions/{id}/messages`
- `POST /sessions/{id}/turns/{turnID}/cancel`
- `POST /sessions/{id}/attachments`
- `GET /sessions/{id}/attachments/{attachmentID}`
- `POST /sessions/{id}/commands`
- `GET /sessions/{id}/events`

### Skills / Packages

- `GET /packages`
- `GET /packages/quality`
- `GET /packages/commands`
- `GET /packages/roles`
- `POST /packages/install`
- `POST /packages/remove`
- `POST /packages/{name}/reinstall`
- `POST /packages/{name}/smoke/{smoke_name}`
- `GET /prompts`
- `GET /sessions/{id}/skills/catalog`
- `GET /sessions/{id}/skills/sources`
- `GET /sessions/{id}/skills/active`
- `GET /sessions/{id}/skills/{name}`
- `POST /sessions/{id}/skills/install`
- `POST /sessions/{id}/skills/load`
- `POST /sessions/{id}/skills/expand`
- `POST /sessions/{id}/skills/unload`

## Eval / Benchmark

Eval harness 用于做可重复行为回归：

```bash
godex eval run --suite examples/evals/smoke.yaml --out ~/.godex/evals/runs
godex eval list --dir ~/.godex/evals/runs
godex eval show --run ~/.godex/evals/runs/<run_id>
```

suite 使用 `godex.eval.yaml` 格式，支持断言回答包含/不包含文本、必须/禁止使用的工具、工具失败数上限。

## Release Check

发布前建议至少跑一次：

```bash
./scripts/release_check.sh
```

它会执行：

- `go test ./...`
- `go build -ldflags "-s -w" -o ~/.godex/tmp/release/godex ./cmd/godex`
- `pnpm -C ui/web build`，如果本机已安装 `pnpm`

可选浏览器 smoke：

```bash
GODEX_BROWSER_SMOKE=1 ./scripts/release_check.sh
```
