# GoDex 用户指南

> 状态：Active（用户手册）
> 修订日志：
> - 2026-08-15 全量 review 后重写：修正 CLI 命令面（移除不存在的 `repl`）、补充完整命令行参考、配置参考、工具清单、Slash Command 清单、HTTP API 路由表、自动化（cron/heartbeat）、安全模型与故障排查；对齐 Agent Profile 真实语义（提示词策略而非工具过滤）。

本文档承接 README 中不适合放在产品总览里的细节，覆盖安装运行、配置、Provider、CLI、Web UI、工具、Channel、自动化、安全、Memory、Subagent/Workflow/Package、Slash Commands、HTTP API、Eval 和发布检查。

## 环境要求

- Go `1.26+`（`go.mod` 声明 `go 1.26.4`）
- Node.js + `pnpm`，仅在需要构建 Web 前端时使用
- 至少一个可用 LLM provider

## 安装与启动

安装依赖并构建：

```bash
go mod download
pnpm -C ui/web install
pnpm -C ui/web build   # Web dist 直接输出到 Go embed 目录
```

初始化一个工作区（`setup` 与 `init` 等价，首次启动无配置时会生成带注释的默认 `godex.yaml`）：

```bash
go run ./cmd/godex setup --dir /path/to/project
go run ./cmd/godex init --dir /path/to/project
```

启动 Web / HTTP / SSE / channel runtime：

```bash
go run ./cmd/godex serve --addr 127.0.0.1:8080
```

打开：

```text
http://127.0.0.1:8080
```

常用入口：

```bash
# 全屏 TUI（默认入口，min-tui）
go run ./cmd/godex
go run ./cmd/godex tui                 # 显式进入 TUI（同一实现）

# 单次提问。CLI/ACP/TUI 默认 coding profile
go run ./cmd/godex ask "总结一下当前仓库结构"
go run ./cmd/godex ask --stdin < prompt.txt
go run ./cmd/godex ask --session my-session "继续上次的话题"
go run ./cmd/godex ask --profile general "帮我做产品规划"

# 执行 slash command
go run ./cmd/godex command /doctor
go run ./cmd/godex command --session web:default "/history search weixin scope=session_archive"

# 诊断与清理
go run ./cmd/godex doctor               # 配置/运行时健康检查
go run ./cmd/godex doctor storage       # 存储目录诊断
go run ./cmd/godex doctor sessions      # session 持久化状态诊断
go run ./cmd/godex gc --dry-run

# 导入 Claude Code 生态资源
go run ./cmd/godex import claude --source .claude --dry-run
```

> 注意：`godex repl` 命令**不存在**（旧 readline REPL 已移除）。交互式终端统一使用 TUI（`godex` / `godex tui`）。

## 配置

### 配置位置与职责

GoDex 将跨项目稳定资产和项目状态分开管理：

- `GODEX_HOME` 默认是 `~/.godex`，保存全局配置（`godex.yaml`）、全局 `.env`、provider、skills、packages、logs、sessions、memory、transcripts、tasks、todos、tmp/cache、rules 和 MCP 默认配置。**home 配置是稳定的写入目标**，Web Settings 修改默认写到这里。
- `GODEX_PROJECT_DIR` 默认是当前工作目录，只作为代码/文件工具的 workspace 边界。项目目录中的 `godex.yaml` / `.env` 仍是**可读的兼容层**：项目配置会深合并覆盖 home 配置（项目优先）。
- `GODEX_CONFIG` 可指定一个显式配置文件（相对路径按 `GODEX_PROJECT_DIR` 解析）。

项目指令（instructions）加载优先级：workspace 根 `AGENT.md`（项目级）> `<state>/rules/*.md`（规则级）> `<state>/AGENT.local.md`（本地级），注入到系统提示词的 `# Instructions` 段。

需要把旧的项目级配置迁移到 `~/.godex` 时：

```bash
godex migrate home --dry-run   # 预览
godex migrate home             # 项目 godex.yaml/.env 复制到 home（仅当 home 中不存在时）
```

### 配置加载顺序

```text
代码默认值 < home godex.yaml < 项目 godex.yaml < home .env < 项目 .env < 进程环境变量 < 显式 flags
```

- `godex.yaml`：home 为基础，项目配置按 section 深合并覆盖。
- `.env`：home 与项目 `.env` 按 key 覆盖 YAML（项目优先）。
- 进程环境变量覆盖 `.env`（如 `GODEX_WEB_TOKEN=xxx godex serve`）。
- 每个字段会记录 origin（YAML / .env / env），`godex doctor` 和 Web Settings 的 `stored / effective` 对照会展示差异。

### 推荐配置方式

推荐用 Web `Settings` 修改配置。它会展示：

- home/project config 路径
- home/project env 路径
- stored config 与 effective runtime 的差异
- doctor warning 和 migration 建议
- provider 状态、模型列表和密钥是否存在

也可以使用交互式向导：`godex config`（命令行 provider 配置向导）。

### godex.yaml 顶层结构

首次启动生成的默认配置本身就是自描述设计文档（含每个字段的注释和环境变量覆盖名），完整参考见 `~/.godex/godex.yaml`。顶层 section：

| Section | 说明 |
|---------|------|
| `api` | provider 与模型路由（`providers`、`default_model`、`auto_fallback_enabled`、`timeout_seconds`、`model_strategy`） |
| `acp` | 外部 ACP agent 列表（`agents`，供 `acp_agent` 工具调用） |
| `agent` | 压缩阈值、compaction 参数、`max_turns`、`profile`、`default_profiles` |
| `logging` | `level`、`file_path`、`also_stderr` |
| `web` | `token`（HTTP API 共享 bearer token） |
| `cron` | 后台 cron 调度器 |
| `heartbeat` | 后台 heartbeat 循环 |
| `control` | 节点注册表 / control plane（`node_name`、`node_id`、`default_node`、`trust_level`、`center_url`、`heartbeat_seconds`、`offline_after_seconds`、`nodes`） |
| `runtime` | `recovery.auto_resume_interrupted_turns`、`recovery.auto_repair_sessions` |
| `security` | `profile` + `screener`（内容安全筛查器） |
| `storage` | TTL、browser cache、session checkpoint、session backend |
| `team` | lead/team 身份与 teammate 运行时参数 |
| `paths` | `state_dir`、`tasks_dir`、`memory_dir`、`skills_dir`、`temp_dir`、`sessions_dir` 等运行时路径（默认相对 `GODEX_HOME`） |
| `tools` | web_search / web_fetch / glob / subagent / execution / browser / lightpanda / history_search / permissions / loop_guard |
| `media` | moonshot / document / ocr / audio / video 附件预处理 |
| `channels` | `feishu`、`weixin` |
| `memory` | `strategy`（per-turn / agent-only / consolidated）、`consolidate_after`、`session_scope` |

### Provider 模型

Provider 类型（`api.providers.<id>.type`）：

- `anthropic_compatible`：Anthropic Messages API（`POST /v1/messages`）
- `openai_compatible`：OpenAI Chat Completions（`POST /v1/chat/completions`），兼容 DeepSeek / Ollama / OpenRouter / Groq / Moonshot / Zhipu / Qwen / 自建代理等
- `openai_codex`：OpenAI Codex OAuth（device-code + token exchange，`login codex --mode codex-oauth`）

credential 种类（`credential_kind`）：`api-key`（默认）、`codex-oauth`、`oauth-token`。

模型策略（`api.model_strategy`）：

- `primary`：始终用第一个候选
- `fallback`：失败时按候选顺序重试（默认）
- `round_robin`：轮换候选

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

CLI 管理 Provider：

```bash
godex login openai --mode platform-api-key   # 或 auto
godex login codex --mode codex-oauth          # PKCE OAuth 浏览器授权
godex login codex --mode platform-api-key
godex logout openai
godex logout codex
godex providers list                          # id / type / api_key_env / credential / present
godex providers test <provider-id>
godex config                                  # 交互式配置向导
```

密钥默认写入 home `.env`（`OPENAI_API_KEY` / `GODEX_OPENAI_CODEX_OAUTH_TOKEN`），不落 `godex.yaml`。

### Agent Profile（语义说明）

`agent.profile` 是**入口/任务提示词策略**，用于控制默认回复风格和能力使用引导；它**不替代** `security.profile`，权限审批仍由安全配置决定。

> 修正说明：coding/general 两个 profile 的**工具目录是相同的**（always-active / default-active 工具集合一致）。差异在于系统提示词内容：coding profile 注入精简的 coding 提示词（简洁回复、先读代码再改、优先 `lsp`、todo 用法），并在提示词中要求“仅在用户明确要求或任务确实需要时，通过 `tool_exchange` 启用 web/browser/subagent/background/package/skill/memory/MCP/external 等重能力”；同时用 `repo_map` 替代 `skill_catalog` 注入。general profile 保留完整工作台体验（含 skill catalog 注入）。

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

- `acp`、`cli`、`tui` 入口默认 `coding`。
- `web`、`weixin`、`feishu` 入口默认 `general`。
- `ask`、`tui`、`acp-server` 支持 `--profile general|coding` 临时覆盖；也可用 `GODEX_AGENT_PROFILE` 覆盖全局，或在 Web `Settings` 修改 `agent.default_profiles.*`。

### Agent 运行时

- **多引擎抽象（Harness）**：`agent` 通过 `Harness` 接口（Profile/Models/Tools/RunTurn/ResetSession/Close）运行 turn；默认 `godex` 引擎。可通过 envelope metadata `harness` 按轮切换引擎（如 `POST /sessions/{id}/messages` 携带 `metadata.harness`），切换时自动 reset 旧/新引擎 session。注册额外引擎使用 `Agent.RegisterHarness`。
- **Turn 错误分层**：模型调用错误分类为 Retryable / Transient / NonRetryable；Retryable/Transient 在同一 turn 内有限重试（`agent.max_turns` 之外的独立预算），NonRetryable 立即失败并透出明确 message。
- **Runner 韧性**：统一 phase checkpoint、active turn follow-up 注入、空回复/`finish_reason=length` 恢复、`runtime.recovery.auto_resume_interrupted_turns`（默认 false）与 `auto_repair_sessions`（默认 true）。
- **Loop guard**：见「安全模型 → Loop Guard」。

### 常见环境变量

```bash
# LLM 密钥
ANTHROPIC_API_KEY=
OPENAI_API_KEY=
GODEX_OPENAI_CODEX_OAUTH_TOKEN=

# 路径与进程覆盖
GODEX_HOME=                 # 默认 ~/.godex
GODEX_PROJECT_DIR=          # 默认当前工作目录
GODEX_CONFIG=               # 显式 godex.yaml 路径
GODEX_AGENT_PROFILE=        # 覆盖默认 agent profile

# Web / API 共享 token
GODEX_WEB_TOKEN=

# Feishu / Weixin channel
FEISHU_ENABLED= FEISHU_APP_ID= FEISHU_APP_SECRET= FEISHU_DOMAIN=lark
WEIXIN_ENABLED= WEIXIN_BASE_URL= WEIXIN_ACCOUNT_ID= WEIXIN_ALLOW_FROM=

# Web 工具
GODEX_WEB_SEARCH_ENABLED= GODEX_WEB_SEARCH_PROVIDER_ORDER=
GODEX_WEB_SEARCH_BRAVE_API_KEY= GODEX_WEB_SEARCH_EXA_API_KEY=
GODEX_WEB_SEARCH_TAVILY_API_KEY= GODEX_WEB_SEARCH_SERPAPI_API_KEY=
GODEX_WEB_FETCH_ENABLED= GODEX_WEB_FETCH_MAX_CHARS= GODEX_WEB_FETCH_POLICY=
GODEX_GLOB_DEFAULT_MAX_RESULTS=
GODEX_BROWSER_ENABLED= GODEX_BROWSER_HEADLESS= GODEX_BROWSER_CDP_URL=

# 日志
LOG_LEVEL= LOG_FILE= LOG_MIRROR_TO_STDERR=

# 执行后端
GODEX_TOOLS_EXECUTION_MODE=            # local | docker | ssh
GODEX_TOOLS_EXECUTION_DOCKER_IMAGE=
GODEX_TOOLS_EXECUTION_SSH_TARGET=

# 服务运行（service 命令设置）
GODEX_SERVICE_NAME= GODEX_SERVICE_SCOPE=

# 节点接入（node join 写入）
GODEX_CONTROL_CREDENTIAL= GODEX_CONTROL_CENTER_URL=
GODEX_CONTROL_NODE_ID= GODEX_CONTROL_TRUST_LEVEL= GODEX_CONTROL_DEFAULT_NODE=
```

每个配置字段的环境变量覆盖名都标注在生成的 `godex.yaml` 注释里（如 `timeout_seconds` → `GODEX_API_TIMEOUT_SECONDS`）；完整覆盖包括 `GODEX_CRON_*`、`GODEX_HEARTBEAT_*`、`GODEX_STORAGE_*`、`GODEX_CONTROL_*`、`GODEX_SUBAGENT_*`、`GODEX_LOOP_GUARD_*`、`GODEX_TOOLS_HISTORY_SEARCH_*`、`GODEX_TOOLS_PERMISSIONS_*`、`GODEX_MEDIA_*`、`WEIXIN_*` 等。所有 key 均有代码默认值，YAML 中每个 key 都可省略。

## 命令行参考

```
godex [全局 flags] <command> [flags]
godex                         启动全屏 TUI（min-tui，默认入口）
```

全局 flags（须在子命令之前，`--config`/`--session` 会从整个 argv 中提取）：

| Flag | 说明 |
|------|------|
| `--config <path>` | 使用显式 godex.yaml（等价 `GODEX_CONFIG`） |
| `--session <spec>` | 默认 session spec（`channel:key` 或裸 key） |
| `--pprof-addr=`, `--dump-dir=`, `--heap-dump=` | 调试 flags：pprof server、goroutine/heap dump 目录、heap dump 开关（等号形式） |

> 已知行为：全局 `--session` 解析器扫描整个 argv，会覆盖 `ask/command/tui/longtask/doctor sessions/repair sessions` 各自的 `--session` flag。需要精确指定 session 时使用 `--session` 的等号形式或直接把它放在最前。

### 命令一览

| 命令 | 子命令 / flags | 说明 |
|------|---------------|------|
| `tui` | `[--session spec] [--profile general\|coding]` | 启动全屏 TUI |
| `ask` | `[--session spec] [--stdin] [--profile general\|coding] <prompt...>` | 单次提问 |
| `command` | `[--session spec] <slash-command>` | 执行 slash command（缺省 `/` 前缀会自动补） |
| `doctor` | `[storage\|sessions [--session <id>]]` | 配置/存储/session 诊断 |
| `eval` | `run --suite <path> [--out <dir>] [--model-profile <id>]` / `list [--dir]` / `show --run <path>` | 运行/列出/查看 eval 套件 |
| `login` | `openai [--mode platform-api-key\|auto]` / `codex [--mode codex-oauth\|platform-api-key\|auto]` | 配置凭据 |
| `logout` | `openai\|codex` | 移除凭据 |
| `config` | — | 交互式 provider 配置向导 |
| `providers` | `list` / `test <id>` | 查看/测试 provider |
| `migrate` | `home [--dry-run]` | 项目配置迁移到 home |
| `repair` | `sessions [--dry-run] [--session <id>]` | 诊断/修复 session 持久化 |
| `gc` | `[--dry-run]` / `browser-cache` / `sessions [--older-than 168h]` / `artifacts [--older-than 168h]` / `subagents [--merged] [--older-than 24h]` | 空间治理 |
| `acp-server` | `[--profile general\|coding]` | 以 ACP stdio agent 运行 |
| `import` | `claude [--source .claude] [--package <name>] [--dry-run]` | 导入 Claude Code 生态 |
| `longtask` | 见下 | durable story-loop 任务 |
| `node` | `forward` / `exec` / `join` | 节点跳板：端口转发、远程执行、接入 |
| `setup` / `init` | `[--dir <path>] [--force]` | 初始化工作区（`--force` 用默认配置覆盖 godex.yaml）；在配置加载前执行 |
| `serve` | `[--addr 127.0.0.1:8080]` | 启动 Web/HTTP/SSE/channel runtime |
| `service` | `install / uninstall / start / stop / restart / status / logs [--scope user\|system] [--name <name>]` | system service 管理 |
| `weixin` | `setup` / `logout` | Weixin/iLink channel 登录管理 |
| `version` | `[--json]` | 版本信息 |

### longtask

```text
godex longtask list [--session key]
godex longtask create --file spec.json [--session key]
godex longtask run <id> [--session key] [--auto-repair] [--max-repair-attempts N]
                       [--max-iterations N] [--wait-timeout-ms N] [--async]
                       [--no-stop-on-failure] [--resume-run-id <id>]
godex longtask status <id> [--session key]
godex longtask cancel <id> (--node <node_id> | --all) [--session key]
godex longtask finalize <id> --node <node_id> [--session key]
godex longtask lookup --commit <hash> [--longtask <id>] [--session key]
godex longtask rollback <id> --node <node_id> [--reason <text>] [--session key]
godex longtask gc <id> [--older-than N] [--apply] [--session key]
```

默认 run 行为：在第一个阻塞 story 处停止；传 `--no-stop-on-failure` 继续。`--resume-run-id` 续跑中断的 run（如 Ctrl+C / HTTP 断开）。`lookup/rollback/gc` 是审计与撤销面。

### node（节点跳板）

```bash
# 接入中心（中心「节点」页可生成完整命令）
godex node join <center-url> --id <node-id> --credential ck_... [--trust trusted|guarded-remote] [--name <name>]

# 在节点网络内执行命令（在中心侧执行）
godex node exec --node <id> [--dir <path>] [--center <url>] [--token <token>] 'cmd...'

# 端口转发（等价 ssh -L）
godex node forward --node <id> --local 3306 --target 10.0.0.5:3306

# 默认节点（省略 --node）
GODEX_CONTROL_DEFAULT_NODE=<id> godex node exec 'echo hi'
# 或 center 侧 godex.yaml: control.default_node: <id>
```

详见 [node-onboarding.md](node-onboarding.md)。

### service

```bash
godex service install --scope system --name godex --addr 0.0.0.0:3801 \
  --gomemlimit 200MiB --gogc 50 --gomaxprocs 1 --memory-high 260M --memory-max 300M
godex service start|stop|restart|status [--scope user|system] [--name <name>]
godex service logs [--scope user|system] [--name <name>] [--follow]
godex service uninstall [--scope user|system] [--name <name>]
```

`install` 默认值：`--scope user --name godex --addr 127.0.0.1:8088 --gomemlimit 220MiB --gogc 50 --gomaxprocs 1 --godebug madvdontneed=1 --watchdog-sec 30`，另支持 `--memory-high <val>`、`--memory-max <val>`。`--follow` 仅 `logs` 子命令支持。详见 [self-deploy.md](self-deploy.md)。

## Web 工作台

Web UI 是当前最完整的产品入口。前端构建直接输出到 Go embed 目录：

```bash
pnpm -C ui/web build
```

### Chat

- 共享会话工作区（多面板网格 2×2 / 3×3，含 Terminal / Files 面板）
- 跨 channel session 列表与过滤
- 消息附件上传与回显（图片 / 文档 / PDF / 音视频预处理）
- 消息 Markdown 渲染和一键复制
- browser screenshot / artifact 回传
- composer 上方待审批 banner 与右侧审批面板
- IM channel 审批消息展示 tool、action、command/path、关键 input preview、reason 和 approve/deny 指令
- `Context & Recall` 解释面板（token 估算、压缩原因、compaction 历史、DAG）
- runner phase、identity、subagent 事件（含 scope label）
- 运行中 follow-up / steer
- session timeline、subagent 时序面板、LongTasks tab
- 将 Agent 输出保存到 Notes，或在当前 Note 上下文中继续对话
- 会话分支（fork）、回滚、合并（Session Tree）

### Files

- 文件树浏览、代码编辑器、diff 查看
- 文件创建/编辑/删除/重命名/搜索（经 `/files/*` API，受 workspace 边界约束）

### Settings

- 本地 token / 默认 session key
- 后端配置中心（`stored / effective` 对照、doctor 报告）
- provider 状态、模型发现和模型选择
- runtime channel 状态
- Weixin 登录 / 重登 / 登出
- service 运行状态与重启（`godex service` 启动的进程）

### Automation

- Cron jobs（创建/编辑/启停/手动 run/运行日志）
- Heartbeat 规则
- 运行日志

### Nodes

Nodes 是只读 Control Plane Dashboard，展示当前中心服务已知的 GoDex runtime：

- node id / name、workspace dir / godex home
- online / offline 状态、Relay connected 状态
- version / capabilities、last seen / heartbeat 时间
- 接入引导（生成 `godex node join` 命令）、节点删除
- 远程 Chat / Terminal / Files 面板（经 relay 代理）

当前阶段不做跨 node session 聚合；远程写操作按信任级别（trusted / guarded-remote）决定是否审批。

### Notes

- 本地 Markdown 文件存储
- 笔记列表、编辑、搜索、标签过滤
- Chat 中保存 assistant 输出到笔记
- 笔记打开时 Chat turn 携带 note object context（`?note_id=`）
- 笔记详情页展示相关记忆（`GET /notes/{id}/related-memories`）

### Skills

- 已激活 skills / skills source 检索
- package quality diagnostics
- smoke run
- reinstall tracking
- 安装本地路径 / GitHub `owner/repo` 源

Skill 的 `SKILL.md` frontmatter 支持：`name`、`description`、`summary`、`when_to_use`（列表）、`argument_hint`、`paths`（列表）、`recommended_bundles`（列表）、`sections`（列表）。

如果要创建或维护 GoDex package，可以安装示例 skill：`examples/skills/package-developer`。

### Memory

- durable memory 管理（按类型：identity / user / workflow / project / warning / work_method / work_fact）
- candidate inbox（accept / dismiss / suppression）
- 分层 context preview（L0 identity / core / relevant）
- `Always include / core`
- 手动触发 `Mine project docs`、`Digest`
- audit log、before/after diff 与 restore/reapply

### Usage

- usage keys / models 管理（含 reset）
- 汇总、调用明细、缓存命中统计、时间序列、按 session 用量
- 记录 LLM token 用量（`usage` 服务，SQLite 存储）

### Context & Recall

- total / history token estimate 与 token breakdown 表格
- compression reasons、compaction 历史
- 大 tool result / artifact reference
- history recall 状态
- active phase 与 compact 建议
- AgentGraph DAG 图 + 上下文预算 stacked bar

### Subagents

- durable subagent 列表与详情
- identity / role / capability summary、bundle / write scope
- phase / last tool / progress summary
- review / merge / cancel / resume / iterate
- result preview、result bytes、result digest

### 静态资源

- 优先使用磁盘上的 `ui/web/dist`
- 若磁盘资源不存在，则回退到二进制内置资源（`internal/uiassets/embedded_dist`）

## 工具系统

当前内建工具按 bundle 分组（`internal/agent/tool_registration.go`）。always-active / default-active 的工具在所有入口默认可用；其余通过 `tool_exchange` 按需启用 bundle。

| Bundle | 工具 | 默认 |
|--------|------|------|
| `core_code` | `bash`、`glob`、`read_file`、`write_file`、`edit_file`、`attach_file`、`grep`（ripgrep 双后端）、`find`、`ls` | default active |
| `planning` | `todo_write`、`todo_list` | default active |
| `lsp` | `lsp`（definitions/references/hover/diagnostics/completions） | default active |
| `task_board` | `task` | bundle |
| `team` | `read_inbox`、`send_message`、`broadcast`、`shutdown_request`、`list_teammates`、`plan_approval` | bundle |
| `web` | `web_search`（provider 顺序可配：brave/exa/tavily/serpapi/browser/lightpanda/duckduckgo）、`web_fetch` | default active（启用时） |
| `browser` | `browser`（打开/搜索/点选/填表/上传下载/截图/handoff-resume） | bundle |
| `desktop` | `desktop`（截图、窗口标题、点击/输入/按键、剪贴板、OCR） | bundle |
| `background` | `background`（长命令执行与状态查询） | bundle |
| `subagent` | `subagent`（run/start/batch/wait/status/logs/list/cancel/resume/review/merge/send_input/followup_task/iterate）、`workflow`（create/status/start/wait/cancel/complete_node/append_node）、`agent_graph`（动态 DAG）、`longtask`（含 plan） | bundle |
| `external_agents` | `acp_agent`（调用配置的 ACP agent） | bundle |
| `mcp` | `list_mcp_resources`、`read_mcp_resource`（只读文件系统资源，无协议 client/server） | bundle（默认关闭） |
| `packages` | `list_packages`、`install_package`、`remove_package`、`list_prompts`、`list_package_commands`、`list_package_roles` | bundle |
| — | `memory`、`skill`、`compress`、`history_search`、`manage_session`、`tool_exchange`、`cron`、`heartbeat` | always active |

Browser tool 适合：

- 打开网页、搜索、点选、表单填写、上传/下载文件、截图
- 遇到登录、验证码、二次确认等用户门禁时切到可见浏览器，用户完成后 `resume`
- 简单多步自动化

Desktop tool v1 适合：

- macOS / Linux / Windows 桌面截图并作为 artifact 回传
- 查看应用窗口标题、点击屏幕坐标、输入文本、按常见按键
- 读取和设置系统剪贴板
- 使用 `tesseract` CLI 做轻量 OCR，查找并点击屏幕文字

### tool_exchange

`tool_exchange` 是 always-active 工具，用于查询/启用/停用 bundle：

- 输入短 query 或 `enable_bundles: [...]` / `disable_bundles: [...]`
- 只影响该会话的活跃 bundle 集合
- coding profile 提示词要求 agent 仅在用户明确要求时启用重能力 bundle

### Artifact 策略

- 不从普通 tool output 里启发式猜路径
- 只有工具显式返回 `ArtifactPaths` 时，才提升成 session attachment
- `attach_file` 是显式把本地文件作为附件发送的入口

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
FEISHU_DOMAIN=lark   # lark | feishu
```

启动 `godex serve` 后，channel 自动监听 `im.message.receive_v1` 事件。IM 消息触发工具审批时，回复会展示审批对象摘要：tool name、action、command/path/url 等关键参数（最多 6 个 key 的脱敏 preview）、reason、request id，以及 `/approve <id>`、`/approve <id> session`、`/deny <id>` 指令。

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

也可以在 Web `Settings` 中发起 Weixin 登录（QR code）。登出：

```bash
go run ./cmd/godex weixin logout
```

账号状态保存在 `<stateDir>/channels/weixin/<accountID>/`（`account.json`、`cursor.json`、`context_tokens.json`）。

## 自动化

### Cron

Cron job 本质是一次**agent turn**（携带消息 payload），不是 shell 命令。调度类型（`schedule.type`）：

- `at`：指定时间执行一次（执行后自动停用）
- `every`：每 N 秒执行（`every_seconds`）
- `cron`：5 段标准 cron 表达式（`cron_expr`，robfig/cron `ParseStandard`）

Job 字段还包括：`name`、`message`（必填）、`timezone`、`session_mode`（`shared` 复用 `jobID` session / `isolated` 每次 run 独立 session）、`model_profile_id`（可选固定模型）、`delivery_target`（`session` 或 `channel`，可选投递）、`enabled`。状态：`pending/running/completed/error/delivery_blocked`。

配置（默认值）：

```yaml
cron:
  enabled: true
  tick_seconds: 1
  default_timezone: Local
  max_concurrent_runs: 2
```

幂等性：每次执行使用 `cron:run:<jobID>:<unix>` 幂等 key，重复触发不会重复执行。HTTP API 见下文 Automation 分组；Web `Automation` 页可管理。

### Heartbeat

Heartbeat 是周期性的 agent turn，按 checklist 巡检工作区：

- checklist 来源：`heartbeat.checklist_path` 指定的文件 → 否则 workspace 根目录 `HEARTBEAT.md` → 否则 state 目录
- 输出包含 OK token（默认 `HEARTBEAT_OK`，配置 `heartbeat.ok_token`，env `GODEX_HEARTBEAT_OK_TOKEN`）时标记 `suppressed` 且不投递
- 规则为单条 `id="default"`，支持 `interval_seconds`、`timezone`、`active_hours_start/end`（`HH:MM`，可跨午夜）、`session_mode`、`delivery_target`、`prompt_override`（完全替换默认提示词）
- 可选 `BusyChecker`：另一 turn 运行中时抑制本次巡检

配置（默认值）：

```yaml
heartbeat:
  enabled: false
  tick_seconds: 30
  checklist_path: HEARTBEAT.md   # 默认相对 workspace 根目录
  ok_token: HEARTBEAT_OK
  default_interval_seconds: 1800
  default_timezone: Local
```

状态：`pending/running/completed/suppressed/delivery_blocked/error`。

## 安全模型

### 安全 profile

`security.profile`（默认 `guarded-local`）：

| Profile | 语义 |
|---------|------|
| `trusted-local` | 信任本机：宽审批策略 |
| `guarded-local` | 默认：本机工具执行带风险分级与审批 |
| `sandboxed` | 更严格：强制 `manual` 审批、清空 trusted path 前缀 |
| `strict` | 最严格：强制 `manual` 审批、最窄工具面 |
| `host-privileged` | 面向宿主机特权操作；把 `yolo` 降级为 `manual` |
| `dev/repair` | 开发/修复用特殊 profile |

```yaml
security:
  profile: guarded-local
  screener:
    enabled: false      # 内容安全筛查器（roadmap 6.1）
    shadow: true        # shadow 模式：只记录 verdict 不阻断
    provider: llm
    timeout_ms: 10000   # 默认 0，注释建议 10000
    max_tokens: 256     # 默认 0，注释建议 256
```

### 工具审批

审批模式（`tools.permissions.interactive_approval_mode`）：

- `manual`：保持 pending approval 队列，等待人工 approve/deny（默认）
- `review`：先派一个只读 reviewer subagent 给出 ALLOW/DENY/MANUAL 建议
- `yolo`：对匹配的受保护远程工具调用自动批准

相关配置（`tools.permissions.*`）：

- `interactive_approval_enabled`、`interactive_approval_sources`（默认 web/gateway/feishu/weixin）、`interactive_approval_tools`（config 默认：bash、background、write_file、edit_file、skill、tool_exchange、cron、heartbeat、browser、desktop；运行时默认策略还会门控 attach_file、memory、task、install_package、remove_package）
- `trusted_path_prefixes`、`trusted_command_prefixes`（匹配时绕过审批；默认自带常用只读命令前缀如 git diff/log/show/status、grep、rg、tail、wget 等）
- `pending_ttl_seconds`（config 默认 900；运行时策略常量 300）、`block_automation_mutations`（默认 true：cron/heartbeat 触发的 turn 禁止变更 cron/heartbeat/tool bundle）

审批对象通过 `/approve`、`/deny` slash command、HTTP API（`POST /sessions/{id}/permissions/{requestID}/approve|deny`）或 agent 侧 `manage_session` 工具（`approve_permission` / `deny_permission` action）处理。`review` 模式下 reviewer subagent 的 ALLOW 会缓存为 allow pattern（bash 取前 ≤2 个 token，写路径取目录）。

### 命令执行守卫

`bash`、`background`、package smoke、isolated subagent bash 共用执行 guard（`internal/platform/tooling`）：

- 危险命令 denylist
- `curl|sh`、`wget|sh`、process substitution、base64 decode 执行、`python -c`、`node -e` 等风险分级
- metadata/private URL 拦截
- workspace escape 检测（`..` 逃逸 / 绝对路径越界）
- 最小环境变量
- approval prompt
- output capture
- security audit

执行后端（`tools.execution.mode`）：`local`（默认）、`docker`（bind-mount workspace 到 `/workspace`）、`ssh`（转发到远程 target）。示例：

```yaml
tools:
  execution:
    mode: docker
    docker_image: golang:1.26
    docker_network: none
    tool_timeout_seconds: 1800
```

也可以用 shell policy 收窄命令面：

```yaml
tools:
  execution:
    shell_allow_patterns:
      - "go test*"
      - "rg *"
    shell_deny_patterns:
      - "curl http://169.254.*"
```

### 文件边界与 Scope 隔离

- WorkspaceFS 文件边界：写工具只能在 workspace 内（`os.Root` 约束 + 只读外部 allowlist 用于读取 GoDex 自身状态目录）
- Scope 隔离（roadmap 6.2）：`tools.execution.scope_write`（默认 true）让写工具拒绝逃逸 scope workspace 根目录的路径；memory 可按 session 分区（`memory.session_scope`）
- subagent 写 scope：角色/bundle 联动决定子 agent 可写的目录范围

### Loop Guard（循环防护）

`tools.loop_guard`：检测 no-mutation 螺旋（连续相同 tool+input+output）、重复轮询、停滞 task 轮询；`mode` 为 `strict`（达到恢复上限后中止）/ `balanced`（无限恢复）/ `warn`（只告警不追踪）。

### 内容安全筛查器（Security Screener）

`security.screener`（roadmap 6.1，默认关闭，shadow 模式）：对 user_input 与 tool_response 做内容分级，shadow 模式只把 verdict 写入 security audit（`screen_<hook>` action），不阻断主链路。

### 安全审计

- 所有高危动作（shell、文件写、审批、scope 事件、memory restore、session fork/rollback/merge、node 操作等）写入 security audit
- HTTP：`GET /security/summary`、`GET /security/audit`

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
  session_backend: json        # json | sqlite
  sqlite_path: ""              # 默认 <state_dir>/session-store.sqlite
```

Session 存储：JSON（`~/.godex/sessions/`）始终作为主存储写入；`storage.session_backend: sqlite` 会额外维护 SQLite 镜像（`session-store.sqlite`，`ListSessions` 合并两者）。`godex doctor storage` 会报告当前 backend 与健康状态。

## Memory / History

系统区分三件事：

1. 当前会话历史（受 `/clear`、`/compact` 影响）
2. durable memory（跨 session 长期记忆）
3. history recall（`history_search` 回查原始会话痕迹）

### Durable memory

- 真源：`~/.godex/memory/` 下的 markdown 文件；索引 `index.json` + SQLite + FTS5 sidecar（`memory.db`）
- 类型：`identity`、`user`、`workflow`、`project`、`warning`、`work_method`（工作方法）、`work_fact`（结构化事实）
- 分层注入：`L0 identity`（单独预算）> `core` > `relevant recall`（scope 优先 + 全局高分补齐）
- 自动来源（turn-end extractor、insights bridge、timeline bridge、project miner）不直接写 durable memory，先进入 candidate inbox，等待 accept / dismiss；dismiss 会生成 suppression，防止同类候选反复出现
- 同 title `Remember` 走 foldCapture 增量去重（重复跳过、新内容追加），上下文截断保留尾部最新（capTail）

### Memory 策略

```yaml
memory:
  strategy: per-turn        # per-turn（默认）| agent-only | consolidated
  consolidate_after: 10     # consolidated 模式下候选数阈值，触发 LLM 合并
  session_scope: false      # true 时每 session 一套 memory 目录（scope 隔离）
```

- `per-turn`：默认行为，每轮自动提取候选
- `agent-only`：关闭自动提取
- `consolidated`：per-turn 捕获 + 候选达到阈值后 LLM 一次性 UPDATE/DELETE/ADD 合并

### 常用命令

```bash
go run ./cmd/godex command /memory-digest
go run ./cmd/godex command "/memory-log 20"
go run ./cmd/godex command "/memory-restore <audit-id> before"
go run ./cmd/godex command "/memory list|search <query>|candidates|accept <id>|dismiss <id>"
```

`history_search` 只负责“查以前说过什么”，不自动写 durable memory，也不自动生成 candidate。`/clear` 清的是会话负担，不是长期记忆；`/compact` 是上下文治理，不是长期记忆治理（压缩 summary 会前置 `Pinned continuation state` 快照）。

更完整设计见 [memory-design-principles.md](memory-design-principles.md)。

## Subagent / Workflow / LongTask

### Subagent

- durable subagent job storage（`subagentJob` 持久化，含 role、bundle、write scope、context budget、pending inputs、handoff artifact）
- `subagent` 工具 actions：`start`（spawn）、`wait`、`send_input` / `followup_task`（运行中注入消息队列）、`iterate`（review→fix→re-review 循环）、`cancel`、`review`、`merge`
- 角色能力边界 = 工具集合 × 权限策略 × 上下文预算（orchestrator 200K / worker 100K / reviewer 100K / researcher 50K）
- 角色→bundle 运行时映射（`RoleBundleRegistry`）、子 agent bundle 继承与覆盖（`bundle_overrides` / `deactivate_bundles`）
- 写 scope 与 `writing` bundle 联动：显式 `write_scope` > role `WriteScope` > 无（只读降级）
- lease 机制：TTL/3 心跳续租，崩溃自动标记 `interrupted` 保留现场
- 父上下文只暴露 compact handoff metadata，不暴露 subagent raw messages/progress

### Workflow / AgentGraph

- `workflow` 工具：创建/运行 durable workflow DAG（nodes + edges + dependencies），handoff artifact、preview merge、动态 append node、条件 append 边（when=status/verdict）
- `agent_graph` 工具：动态 AgentGraph 抽象（Create/Get/AddNode/AddEdge/RemoveNode/Cancel/Run/Wait），节点类型 `llm_task` / `subagent_task` / `tool_call` / `user_input` / `merge_point`，边类型 `data_dependency` / `control_flow` / `handoff`，图跨重启可恢复
- `longtask` 工具（含 `plan` action）：Ralph-style story loop，支持自然语言描述→stories JSON 拆解

### LongTask

- story loop：按 PRD/user stories 编译 workflow node（显式 `depends_on`，无依赖并行 fan-out），validation 通过后自动 merge/commit，失败时可显式开启 auto-repair（`max-repair-attempts`）
- 动态并行 DAG：`longTaskStoryInput` 支持 `depends_on`，无依赖子任务自动并行（受 subagent 并发上限约束）
- 重启恢复：run 记录持久化 + 启动 sweep/重建 + `--resume-run-id` 续跑；崩溃遗留的 running 记录翻转为 interrupted
- 上下文预算：子任务结果按 token 预算截断（默认 2000 token），handoff 摘要共享截断路径
- merge/commit 策略：`merge_policy: auto_merge|review_only`，`commit_policy: auto_commit|none`；非 Git 仓库跳过 commit（`skipped_non_git`）
- 验证：`quality_checks`（如 `go test ./...`）在 story 子 agent 的 worktree（或 host workspace）内通过运行时 `Tools.Execution` sandbox 执行
- 详细设计见 [workflow-runtime.md](workflow-runtime.md)

LongTask CLI 见上文「命令行参考 → longtask」。Web Chat inspector 的 `LongTasks` tab 可查看 story progress、validation、merge、commit 和 repair attempts，并提供 run/finalize/cancel 控制。

## Skills / Packages / Claude Import

### Package manifest（godex.package.yaml）

```yaml
name: my-package
version: 1.0.0
description: ...
resources:
  skills: [skills/foo/SKILL.md]
  prompts: [prompts/bar.md]
  commands: [commands/*.md]
  roles: [roles/*.md]
  docs: [docs/*.md]
  assets: [assets/*]
app:
  kind: builtin
  id: notes
permissions: []
capabilities: []
provides:
  - "godex:my-capability@1"
requires:
  - "base-kit@>=0.2.0"
  - "godex:log@1"
tool_policy:
  - "shell:allow:go test*"
  - "shell:deny:curl *"
smoke_tests:
  - name: unit
    command: "go test ./..."
    working_dir: "."
    timeout_seconds: 120
    required_permissions: []
recommended_bundles: [core_code, lsp]
```

字段：`name`、`version`、`description`、`resources{skills,prompts,commands,roles,docs,assets}`、`app{kind,id,label,config}`、`permissions`、`capabilities`、`provides`、`requires`、`tool_policy`、`smoke_tests[]`（name/command/working_dir/timeout_seconds/required_permissions/expected_exit_code）、`recommended_bundles`。

**依赖声明（`requires` / `provides`）**：

- `requires` 支持两种形式：
  - 包依赖 `name@constraint`（如 `base-kit@>=0.2.0`、`toolkit@1`），约束支持精确版本、主/次版本前缀（`1`、`1.2`）、`>=`/`>`/`<=`/`<`、`^`（同主版本内）、`~`（同次版本内）、`*`（任意）；
  - 能力依赖 `namespace:name[@major]`（如 `godex:log@1`、`tool:read_file`），由平台内建能力或已安装包的 `provides` 提供。
- 安装 / 重装时解析依赖图：缺失依赖、版本冲突、依赖环都会阻止安装并给出报告；`/packages` quality 报告同样展示依赖问题。
- 卸载保护：仍被其他已安装包 `requires` 引用的包无法被直接移除，需先移除引用方。
- 重装是事务式的：新内容先落盘并校验，成功后原子切换 registry 并清理旧 digest 目录；失败时旧版本及其目录保持不变。

Package command declaration 支持：

- `prompt_only`
- enqueue `agent_turn`
- start durable `subagent_job`
- named package roles（含 `write_scope`）
- capability / tool policy
- explicit smoke tests

Smoke execution 绑定 backend session，并复用普通 shell permission、approval 和 security audit 路径。

### 安装源

- 本地路径：Web `Skills` 安装入口或 Chat 中 `skill` 工具（`action=install`）
- GitHub：Web `Skills -> Packages` 填写 `owner/repo` 或 `https://github.com/owner/repo.git`
- 调试顺序：先看 package quality → Commands/Roles/Prompts → 显式运行 smoke

### Claude Code 生态导入

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

导入过程默认不调用 LLM，不自动执行 hooks，不自动启用 MCP；不兼容项以 diagnostics/warnings 展示。

## Slash Commands

以下命令既可以在本地 `command` 模式使用，也会通过 Web / IM channel 复用同一套后端执行。

| 命令 | 语法 | 说明 |
|------|------|------|
| `/help` | — | 显示帮助 |
| `/bash`、`/sh` | `<shell command>` | 不经 LLM 直接在工作区执行 shell |
| `/compact` | `[--model\|--deep\|--hybrid]` | 压缩当前 session 上下文（默认 `compaction.mode`；写入 pinned continuation 快照） |
| `/clear` | — | 清空当前 session 消息上下文并重置临时工具 |
| `/tasks` | — | 查看任务板 |
| `/todos` | `list\|clear` | 查看/清空 todo 列表 |
| `/team` | — | 查看 teammates 状态 |
| `/inbox` | — | 读取 lead inbox |
| `/insights` | — | 生成 workspace insights 报告 |
| `/doctor` | — | 诊断配置与运行时 |
| `/channels` | — | 查看 channel 状态 |
| `/skills` | `list\|sources [query]\|active\|get <name>\|install <source> [name]\|load <name>\|expand <name> <section...>\|unload <name>` | 会话 skill 管理 |
| `/packages` | `list\|commands\|roles\|prompts` | 查看 package 声明 |
| `/memory` | `list\|get <id-or-title>\|search <q>\|candidates\|accept <fingerprint>\|dismiss <fingerprint>`（别名：`digest`、`log [limit]`、`restore <audit-id> [before\|after]`） | durable memory 浏览与候选审核 |
| `/memory-digest` | — | 分析会话信号并生成候选 |
| `/memory-log` | `[limit]` | 显示 memory audit 历史 |
| `/memory-restore` | `<audit-id> [before\|after]` | 恢复 audit 快照 |
| `/note` | `list\|search [q] [--tag <tag>]\|get <id>\|create\|new <title> [--tags a,b] -- <md>\|append [id] -- <md>\|update\|edit [id] -- <md>`（裸 `/note <title> ...` 也创建） | Markdown 笔记 |
| `/ledger` | — | 显示当前 long-task 项目 ledger |
| `/model` | `list\|get\|use\|session <profile-id>\|default\|set <profile-or-model>\|help`（裸 `/model <profile-id>` 等价 use） | 模型管理 |
| `/approve` | `[status\|list\|request-id] [once\|task\|session\|pattern\|count:N\|timebox:10m]` | 审批待处理权限请求 |
| `/deny` | `[request-id] [reason...]` | 拒绝权限请求 |
| `/session` | `current\|list [channel]\|new [key\|channel:key]\|context\|tokens\|auth` | session 管理 |
| `/new` | — | 创建新的空 session |
| `/resume` | `[session-id\|session-name]` | 列出并恢复之前的 session |
| `/history` | `show\|tail [count]\|search <query> [scope=...] [limit=N] [role=...]` | 会话历史查看/搜索 |
| `/cron` | `list\|get <job-id>\|run <job-id>\|logs <job-id>\|enable <job-id>\|disable <job-id>\|delete <job-id>` | 查看/运行/切换 cron job（创建/编辑走 HTTP API） |
| `/heartbeat` | `get\|test\|logs\|enable\|disable` | heartbeat 管理 |

`/history search` 参数：

- `scope=current_session|session_archive|all_archives`（默认 current_session）
- `limit=N`（默认 5）
- `role=user|assistant|any`

IM channel 中审批消息会直接给出 `/approve <id>`、`/approve <id> session`、`/deny <id>` 指令。

## HTTP API

基础路径前缀 `/api`：webui handler 会剥掉 `/api` 前缀后转发到 httpapi（所以下面的 `GET /config` 实际是 `GET /api/config`）；OpenAI-compat 路由 `/v1/*` 既可按原样访问（`/v1/...`），也可带前缀（`/api/v1/...`）。鉴权方式：

```http
Authorization: Bearer <token>
```

- `protected` 路由：读取 `web.token`（`GODEX_WEB_TOKEN`）；token 为空时全部放行，否则必须携带匹配的 Bearer token
- 公开路由：`GET /meta`、terminal 路由（`/v1/terminal/*`，设计上不鉴权）、`POST /v1/chat/completions` 在未配置 web token 时的回退路径
- `gdx_` proxy-key 路由（usage 网关）：`POST /v1/chat/completions`（Bearer `gdx_...`）、`POST /v1/messages`（Bearer **或** `x-api-key`）、`GET /v1/models`、`GET /v1/usage/cache-stats`（仅 Bearer）——**`/v1/messages` 始终要求 key，即使未配置 web token 也会 401**
- 节点 relay 请求可通过 `X-Godex-Relay-Trusted: <hex>` 头（HMAC-SHA256(nodeID, control.credential)）绕过 web token（仅节点侧 relay agent 能伪造，center 无 credential 时永不信任）
- `/preview/*` 支持 Bearer 或 `?token=` query 两种鉴权

### Meta / Config / Runtime

- `GET /meta`
- `GET /config/meta`、`GET /config/schema`、`GET /config`、`PUT /config`
- `POST /config/reload`、`POST /config/reveal`
- `GET /config/doctor`
- `GET /runtime/service`、`POST /runtime/service/restart`

### Providers

- `GET /providers`
- `POST /providers/{id}/test`
- `POST /providers/{id}/models`
- `GET /providers/import/codex`、`POST /providers/import/codex`

### OpenAI / Anthropic 兼容 API

- `POST /v1/chat/completions`（OpenAI-compatible chat completions；Bearer `gdx_...` 走 usage 网关，否则回退 web token；`stream:true` 时 SSE 分块）
- `POST /v1/messages`（Anthropic Messages 兼容；接受 `gdx_` key 或 web token，无 key 时 401）
- `POST /v1/exec`（在节点上执行 shell 命令并 SSE 流式输出 `{output, final, exit_code}`）
- `GET /v1/models`（gdx_ key 专属）、`GET /v1/usage/cache-stats`（gdx_ key 专属，绑定该 key 的缓存统计）
- `GET /models`（`?session_id=` 查询模型 profiles）
- Terminal（设计上不鉴权）：`POST /v1/terminal/create`、`GET /v1/terminal/{id}/output`、`POST /v1/terminal/{id}/input`、`POST /v1/terminal/{id}/resize`、`DELETE /v1/terminal/{id}`

### Channels

- `GET /channels`
- `GET /channels/weixin/auth`
- `POST /channels/weixin/auth/start`、`POST /channels/weixin/auth/logout`

### Control Plane / Relay

- `GET /control/nodes`、`GET /control/nodes/{id}`、`DELETE /control/nodes/{id}`
- `POST /control/nodes/register`、`POST /control/nodes/{id}/heartbeat`
- `POST /control/nodes/{id}/credential`
- `GET /control/nodes/{id}/overview`（聚合观测快照）
- `GET /control/nodes/{id}/proxy/...`（中心代理访问节点，经 relay；guarded-remote 写操作需 `X-Godex-Trust-Approved` 审批头）
- `GET /control/nodes/{id}/forward`（WebSocket 端口转发）
- `GET /relay`（WSS hub 升级端点）
- `GET /push/public-key`（也挂载在裸 `/push`）、`POST /push/subscribe`、`POST /push/unsubscribe`、`POST /push/test`（Web Push：VAPID 公钥/订阅/测试通知）

### Automation

- `GET /automation/cron/jobs`、`POST /automation/cron/jobs`
- `GET /automation/cron/jobs/{id}`、`PATCH /automation/cron/jobs/{id}`、`DELETE /automation/cron/jobs/{id}`
- `POST /automation/cron/jobs/{id}/run`、`GET /automation/cron/jobs/{id}/runs`
- `GET /automation/heartbeat`、`PUT /automation/heartbeat`、`POST /automation/heartbeat/test`、`GET /automation/heartbeat/logs`

### Security

- `GET /security/summary`、`GET /security/audit`

### Memory

- `GET /memory`（搜索：`?q=&memory_type=&tag=&source=&limit=`）、`GET /memory/candidates`、`GET /memory/suppressions`、`GET /memory/context`（`?q=` 预览分层注入）、`GET /memory/audit`（`?limit=`）
- `POST /memory/remember`、`POST /memory/update`、`POST /memory/forget`
- `POST /memory/digest`、`POST /memory/mine/project`
- `POST /memory/audit/{id}/restore`
- `POST /memory/candidates/{fingerprint}/accept`、`POST /memory/candidates/{fingerprint}/dismiss`

### Notes

- `GET /notes`、`GET /notes/{id}`、`GET /notes/{id}/related-memories`
- `POST /notes`、`DELETE /notes/{id}`

### Sessions

核心：

- `POST /sessions`、`GET /sessions`、`DELETE /sessions/{id}`
- `GET /sessions/{id}`、`POST /sessions/{id}/fork`（session 分支）、`POST /sessions/{id}/model`
- `GET /sessions/{id}/context-inspector`
- `GET /sessions/{id}/transcript/{ref}`、`GET /sessions/{id}/ledger`、`POST /sessions/{id}/ledger`
- `GET /sessions/{id}/timeline`、`GET /sessions/{id}/timeline/page`
- `GET /sessions/{id}/compactions`
- `GET /sessions/{id}/permissions`
- `POST /sessions/{id}/permissions/{requestID}/approve`、`POST /sessions/{id}/permissions/{requestID}/deny`

消息与事件：

- `POST /sessions/{id}/messages`（202 + turn_id 异步提交；envelope metadata 可携带 `harness` 引擎切换、`agent_profile`、`note_id`）
- `GET /sessions/{id}/turns/{turnID}`、`POST /sessions/{id}/turns/{turnID}/cancel|retry|resume`
- `POST /sessions/{id}/attachments`、`GET /sessions/{id}/attachments/{attachmentID}`
- `POST /sessions/{id}/commands`
- `GET /sessions/{id}/events`（SSE 事件流，支持 `?replay=active` / `?turn_id=`；事件形如 `{session_id, turn_id, type, timestamp, payload}`，类型包括 `user_message_accepted`、`assistant_text_delta`、`assistant_message_completed`、`tool_call_started`、`tool_call_finished`、`todo_list_updated`、`warning_raised`、`error_raised`、`command_completed`、`skill_state_changed`、`history_recall_decision`、`subagent_job_updated`、`runner_phase_changed`、`message_injected`、`agent_identity_updated`、`snapshot_ready`、`turn_completed` 等）

Subagents：

- `GET /sessions/{id}/subagents`、`GET /sessions/{id}/subagents/{jobID}`
- `GET /sessions/{id}/subagents/{jobID}/review`
- `POST /sessions/{id}/subagents/{jobID}/cancel|resume|merge`

LongTasks：

- `GET /sessions/{id}/longtasks`、`POST /sessions/{id}/longtasks`
- `GET /sessions/{id}/longtasks/{workflowID}`
- `POST /sessions/{id}/longtasks/{workflowID}/run|cancel|finalize|lookup|rollback|gc`

Skills：

- `GET /sessions/{id}/skills/catalog`、`GET /sessions/{id}/skills/sources`、`GET /sessions/{id}/skills/active`
- `GET /sessions/{id}/skills/{name}`、`DELETE /sessions/{id}/skills/{name}`
- `POST /sessions/{id}/skills/install|normalize|load|expand|unload`

### Files / Git / Preview / Usage

- Files：`GET /files/list`、`GET /files/read`、`PUT /files/write`、`DELETE /files`、`POST /files/mkdir`、`POST /files/rename`、`GET /files/search`
- Git：`GET /git/diff`
- Preview：`GET /preview/static`、`GET /preview/static/{path...}`、`GET /preview/proxy/{port}`、`GET /preview/proxy/{port}/{path...}`（浏览器预览代理）
- Usage：`GET/POST /usage/keys`、`PATCH /usage/keys/{id}`、`POST /usage/keys/{id}/reset`、`GET/POST /usage/models`、`PATCH /usage/models/{id}`、`GET /usage/summary`、`GET /usage/calls`、`GET /usage/cache-stats`、`GET /usage/time-series`、`GET /usage/sessions`、`GET /usage/sessions/{id}`

### Packages / Prompts / Commands

- `GET /packages`、`GET /packages/quality`
- `POST /packages/install`、`POST /packages/remove`、`POST /packages/{name}/reinstall`
- `POST /packages/{name}/smoke/{smoke}`
- `GET /packages/commands`、`GET /packages/roles`、`GET /prompts`、`GET /commands`

## Eval / Benchmark

Eval harness 用于做可重复行为回归（CLI-only，无 HTTP endpoint）：

```bash
godex eval run --suite examples/evals/smoke.yaml --out ~/.godex/evals/runs
godex eval run --suite examples/evals/smoke.yaml --model-profile anthropic.sonnet
godex eval list --dir ~/.godex/evals/runs
godex eval show --run ~/.godex/evals/runs/<run_id>
```

suite 使用 `godex.eval.yaml` 格式：

```yaml
name: smoke
cases:
  - id: case-1
    title: ...
    prompt: "..."
    replay_fixture: ""        # 可选：离线回放 fixture
    model_profile_id: ""      # 可选：覆盖模型
    timeout_seconds: 120
    expected:
      required_substrings: ["..."]
      forbidden_substrings: []
      required_tools: [read_file, edit_file]
      forbidden_tools: [bash]
      max_tool_failures: 0
      max_repeated_assistant_messages: 3
      max_repeated_tool_calls: 5
      forbidden_tool_exchange_queries: []
      max_empty_tool_exchange_recommendations: 2
      expected_instability_signals: []
```

断言支持：回答包含/不包含文本、必须/禁止使用的工具、工具失败数上限、重复消息/重复工具调用上限、tool_exchange 相关检查与稳定性信号。

## 故障排查

| 症状 | 检查 |
|------|------|
| 启动/配置异常 | `godex doctor`（配置、channel、cron、heartbeat、package app 声明诊断） |
| session 状态异常 | `godex doctor sessions`、`godex repair sessions --dry-run`、`godex repair sessions` |
| 存储占用异常 | `godex doctor storage`、`godex gc --dry-run` |
| Provider 不通 | `godex providers test <id>` |
| 节点接入异常 | 见 [node-onboarding.md](node-onboarding.md) FAQ |
| 服务部署异常 | 见 [self-deploy.md](self-deploy.md) 故障排查表 |

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

本地也可以单独运行：

```bash
go test ./...
pnpm -C ui/web build
git diff --check
```
