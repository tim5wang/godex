# GoDex 扩展能力使用手册

> 适用范围：当前 `main` 分支中的 Package 依赖、MCP stdio、ACP 外部 Agent 与 WASM Package Runtime。
>
> 重要说明：这四类能力的信任边界不同。Package 会安装声明式资源并可激活 WASM runtime；MCP/ACP 会启动本机进程；WASM 只获得 manifest 明确授权且由宿主提供的 host callback。

## 1. 能力与成熟度

| 能力 | 当前可用性 | 典型用途 |
|---|---|---|
| Package `requires` / `provides` | 可用 | 声明资源包之间的依赖和能力契约 |
| MCP filesystem | 可用 | 向 Agent 提供只读目录资源 |
| MCP stdio tools/prompts | 可用 | 通过独立进程扩展工具和 Prompt |
| ACP `acp_agent` 委派 | 可用 | 把一个任务交给外部 ACP Agent |
| ACP whole-turn Harness | 后端已接线，暂无 CLI/Web 选择器 | 由集成方通过消息 metadata 指定外部引擎 |
| WASM Runtime / Plugin Kernel | 可用 | 安装 Package 后自动加载工具和 Prompt |
| 安装、重装、删除 WASM Package | 可用 | 自动激活、热重载和撤销运行时能力 |

## 2. Package 依赖

Package 根目录必须包含 `godex.package.yaml`。

### 2.1 Provider 示例

```yaml
name: base-kit
version: 1.2.0
description: Shared review resources

provides:
  - acme:review-rules@1

resources:
  prompts:
    - prompts/review.md
```

### 2.2 Consumer 示例

```yaml
name: review-app
version: 0.3.0

requires:
  - base-kit@^1.0.0
  - acme:review-rules@1

resources:
  skills:
    - skills/reviewer/SKILL.md
```

支持的 Package 约束：

- 精确版本：`base-kit@1.2.0`
- 主/次版本前缀：`base-kit@1`、`base-kit@1.2`
- 比较：`>=`、`>`、`<=`、`<`
- 范围：`^1.2.0`、`~1.2.0`
- 任意版本：`*` 或省略约束

能力依赖使用 `namespace:name[@major]`，例如 `acme:review-rules@1`。

### 2.3 安装、查看和删除

在 Chat 中启用 `packages` bundle，然后调用 `install_package`：

```json
{"source":"/absolute/path/to/review-app"}
```

对应工具为：

- `install_package`：安装本地目录、Git URL 或 `owner/repo`
- `list_packages`：查看已安装包、依赖和 runtime 声明
- `remove_package`：删除包
- `list_prompts`、`list_package_commands`、`list_package_roles`：查看声明式资源

也可以在 Web 的 Skills / Packages 页面安装和查看。

安装时会检查：

- 候选最终 registry 的完整依赖图，包括既有 Consumer；
- 缺少的 Package 或 capability；
- 版本冲突；
- 依赖环；
- 所有资源和 runtime module 的 Package 根边界；
- WASM runtime 声明的 kind、ABI 和 module 是否存在。

重装 Provider 如果会破坏现有 Consumer，会在替换 registry 前失败。删除后的完整依赖图也必须有效，因此 Package 名依赖和 capability 依赖都会阻止仍被引用的 Provider 被卸载。

### 2.4 当前限制

- 不要从不可信来源安装 Package；即使 WASM 受运行时隔离，Prompt、Skill 和工具输出仍属于不可信内容。
- 建议串行执行安装、重装和卸载；当前 registry 尚未实现跨进程文件锁。

## 3. MCP

默认配置文件为 `~/.godex/mcp.json`，可在 `godex.yaml` 中修改：

```yaml
paths:
  mcp_config_path: ~/.godex/mcp.json
```

### 3.1 Filesystem server

```json
{
  "servers": [
    {
      "name": "project-docs",
      "type": "filesystem",
      "root": "docs"
    }
  ]
}
```

启用 `mcp` bundle 后使用：

- `list_mcp_resources`
- `read_mcp_resource`

`root` 的相对路径以 GoDex workspace 为基准。`uri` 必须是 root 内相对路径；读取会同时检查词法路径和符号链接解析后的真实路径，`..`、绝对路径以及指向 root 外的 symlink 都会被拒绝。

### 3.2 Stdio server

```json
{
  "servers": [
    {
      "name": "demo",
      "type": "stdio",
      "command": "npx",
      "args": ["-y", "my-mcp-server"],
      "env": {
        "DEMO_API_KEY": "replace-me"
      }
    }
  ]
}
```

协议要求：

- JSON-RPC 2.0 over stdio；
- 当前客户端使用 MCP protocol version `2024-11-05`；
- stdout 每行只能输出一条协议 JSON；
- 服务日志应写入 stderr。

可用工具：

- `list_mcp_tools` / `call_mcp_tool`
- `list_mcp_prompts` / `get_mcp_prompt`
- 启动 Agent 时发现的 MCP 工具还会注册为 `<server>__<tool>`，owner 为 `mcp:<server>`。

修改 `mcp.json` 后应重建 Agent/session 或重启 GoDex。当前直接工具目录不会热刷新。

### 3.3 安全注意事项

MCP stdio 是本机原生进程，不是沙箱：

- 当前 MCP 子进程只收到 `mcp.json` 中显式配置的 `env`，不会继承 GoDex 的完整环境；需要 `PATH`、HOME 或代理变量时必须显式传入；
- MCP 程序仍是本机原生进程，可拥有操作系统用户本身具备的文件、网络和进程权限；
- 只配置可信可执行文件，敏感 token 尽量放到专用低权限环境；
- 不要把秘密打印到 stdout。

## 4. ACP 外部 Agent

在 `godex.yaml` 中配置：

```yaml
acp:
  agents:
    pi:
      command: pi-acp
      args: []
      env: {}
      timeout_seconds: 600
      description: Pi coding agent
```

`command` 必须启动一个 ACP stdio server；普通 CLI 只有在提供 ACP adapter 时才能直接使用。

### 4.1 推荐方式：任务委派

启用 `external_agents` bundle 后调用 `acp_agent`：

```json
{
  "action": "list"
}
```

```json
{
  "action": "run",
  "agent": "pi",
  "prompt": "检查当前仓库的测试失败并给出修复建议",
  "timeout_seconds": 600
}
```

执行流程为：GoDex 启动外部进程 → `initialize` → `session/new` → `session/prompt` → 收集更新和最终文本 → 关闭进程。

### 4.2 Whole-turn Harness

配置中的每个 ACP Agent 会注册为 `acp:<id>`，例如 `acp:pi`。后端集成可以在入站 `message.Envelope.Metadata` 中加入：

```json
{"harness":"acp:pi"}
```

当前 CLI 和 Web UI 没有正式的 Harness 选择器，因此普通用户优先使用 `acp_agent` 委派。

### 4.3 当前语义和限制

- 每次调用都会启动新进程并创建新 ACP session；目前不会跨 Turn 复用远端 session。
- Whole-turn Harness 当前只发送最新一条用户文本，不会完整转发历史、附件和结构化内容。
- ACP 进程继承宿主环境，并在 workspace 目录启动；它不受 GoDex 内部 Tool permission interceptor 的完整约束。
- timeout 会取消直接子进程，但复杂 wrapper 的后代进程治理仍需加强。

## 5. WASM Package Runtime

### 5.1 ABI

当前 ABI 为 `godex:plugin@0.1`，guest 需要导出：

- `godex_abi_version`
- `godex_tools_list`
- `godex_request_buffer`
- `godex_invoke`

可选导出：

- `godex_prompts_list`
- `godex_policy`

Runtime 默认限制：

- guest memory：32 MiB；
- 单次调用：30 秒；
- mailbox：64 KiB；
- response：4 MiB；
- 单 Runtime 最大并发配置默认 4，但 guest 调用由实例锁串行执行。

宿主可按配置提供 log、KV、workspace read、受控 HTTP GET 和 allowlist credential host calls。

### 5.2 构建示例

Rust：

```bash
rustup target add wasm32-wasip1
cd examples/wasm-plugin-rust
cargo build --release --target wasm32-wasip1
```

TinyGo：

```bash
cd examples/wasm-plugin-tinygo
tinygo build -o plugin.wasm -target=wasip1 -buildmode=c-shared .
```

验证 Runtime：

```bash
go test ./internal/wasmrt ./internal/pluginrt ./internal/tools
```

### 5.3 Package 声明

```yaml
name: rust-hello
version: 0.1.0
permissions:
  - network             # 允许 godex_http_get，经 WebFetch policy
  - filesystem          # 允许 godex_workspace_read，仅工作区相对路径
  - memory              # 允许持久化 namespaced KV
  - credential:API_KEY  # 只允许读取指定环境变量
runtime:
  kind: wasm
  module: plugin.wasm
  abi: godex:plugin@0.1
provides:
  - acme:rust-hello@1
```

安装后，当前 Agent 会立即把 Package 构造成 `WasmToolPlugin` 并激活；新建或重启后的 Session 会在工具注册阶段枚举 registry 并恢复全部 runtime。重装以 Package digest 作为运行时 generation 标识：即使 manifest version 未变化，只要内容 digest 变化也会停用旧实例并加载新实例。删除 Package 会停用实例、撤销其工具、Prompt、Policy interceptor，并关闭 wazero runtime。

通过 Backend API 安装、重装或删除时，所有当前打开 Session 都会执行同一套 reconcile；后续打开的 Session 从 registry 自动加载。

### 5.4 安全边界

WASM 不等于无权限：实际权限由宿主导入决定。Runtime 会实例化 WASI Preview 1 以兼容编译产物，但未预打开 workspace 目录，也没有直接开放 socket、shell 或宿主环境。

Package 安装边界：

- 所有 runtime/resource 路径必须是 Package 内相对路径；拒绝 `..` 逃逸、绝对路径和整个 Package 树中的符号链接；
- `network` 才启用 `godex_http_get`，且请求继续受 WebFetch allow/deny、超时和大小策略约束；
- `filesystem` 或 `read_file` 才启用 `godex_workspace_read`，读取由 `workspacefs` 限定在当前 Session workspace；
- `memory` 才启用持久化 KV，key 按 plugin id 隔离；
- `credential:NAME` 逐项授权环境变量；未声明的名称不可读取；
- 不提供 shell、进程、任意文件系统、原始 socket 或环境变量枚举。

## 6. 故障排查

### MCP 工具没有出现

1. 检查 `paths.mcp_config_path`；
2. 确认 server stdout 没有普通日志；
3. 调用 `list_mcp_tools` 查看发现结果；
4. 重启 GoDex 或重建 session，使一等工具重新注册；
5. 检查 `mcp` bundle 是否启用。

### ACP Agent 无法启动

1. 先在终端确认 `command` 可执行；
2. 用 `acp_agent` 的 `action=list` 检查配置是否生效；
3. 确认程序实现 ACP stdio，而不只是普通交互式 CLI；
4. 检查 timeout 和 stderr；
5. 如果使用 shell wrapper，检查是否残留子进程。

### Package 安装失败

查看错误中的 `missing dependency`、`dependency conflict` 或 `dependency cycle`；先安装 Provider，再安装 Consumer。安装远端 Package 前审查 manifest、符号链接、runtime module 和 smoke command。

### WASM Package 安装成功但工具不存在

1. 检查 Package `runtime.kind` 是否为 `wasm`，module/ABI 是否有效；
2. 检查 GoDex stderr 中的 `runtime activation failed`；
3. 确认 guest 正确导出 ABI 和 `godex_tools_list`；
4. 如果工具需要 HTTP、workspace、KV 或 credential host call，检查相应 manifest permission；
5. 重启或重建 Session 会从 Package registry 重新激活 runtime。
