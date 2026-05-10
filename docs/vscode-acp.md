# VS Code ACP Client 集成指南

本指南说明如何在 VS Code 的 ACP（Agent Client Protocol）client 中使用 GoDex。

## 快速开始

### 1. 构建 GoDex 二进制

```bash
go build -o ./godex ./cmd/godex
```

### 2. 在 VS Code 中配置 ACP agent

在 VS Code settings（`settings.json`）中添加：

```jsonc
{
  "acp.agents": {
    "godex": {
      "command": "/absolute/path/to/godex",
      "args": ["acp-server"],
      "cwd": "${workspaceFolder}"
    }
  }
}
```

ACP 默认使用 `coding` agent profile，适合 VS Code 内的代码阅读、修改和验证。如果希望临时恢复通用工作台行为，可以把启动参数改成：

```jsonc
"args": ["acp-server", "--profile", "general"]
```

或使用项目内脚本（推荐，自动构建）：

```jsonc
{
  "acp.agents": {
    "godex": {
      "command": "/bin/bash",
      "args": ["${workspaceFolder}/scripts/run_acp_server.sh"],
      "cwd": "${workspaceFolder}"
    }
  }
}
```

也可以使用 `go run`（启动较慢，适合开发调试）：

```jsonc
{
  "acp.agents": {
    "godex": {
      "command": "go",
      "args": ["run", "./cmd/godex", "acp-server"],
      "cwd": "${workspaceFolder}"
    }
  }
}
```

### 3. 确保配置就绪

GoDex ACP agent 启动时会读取 `~/.godex` 全局配置，并把当前工作目录作为 workspace 边界。如果没有初始化过项目说明文件：

```bash
./godex init --dir .
```

确认 LLM provider 已配置：

```bash
./godex providers list
```

## 常用命令

在 VS Code ACP client 的对话中，可以直接使用 slash commands：

| 命令 | 说明 |
|------|------|
| `/doctor` | 诊断配置和运行时问题 |
| `/doctor acp` | ACP 模式专用诊断 |
| `/providers list` | 查看已配置的 LLM provider |
| `/providers test <id>` | 测试 provider 连通性 |
| `/help` | 查看所有可用命令 |
| `/approve <id>` | 批准待审批的权限请求 |
| `/approve <id> session` | 本次会话始终批准 |
| `/deny <id>` | 拒绝待审批的权限请求 |
| `/model` | 查看/切换当前模型 |
| `/gc --dry-run` | 检查可清理的本地存储 |

## ACP 模式特性

GoDex 作为 ACP agent 提供以下能力：

- **完整后端能力**：工具调用、记忆、skills、子 agent、审批流
- **Coding profile 默认启用**：默认精简工具曝光，优先代码检查、编辑和验证；需要 web/browser/subagent/skill 时再按需启用
- **文件资源读取**：VS Code 中选中的代码片段会自动作为上下文传入
- **流式输出**：文本回复实时流式展示
- **会话管理**：支持 session load / restore
- **slash commands**：直接在对话中执行 `/doctor`、`/approve` 等命令

## 环境变量

在 `.env` 文件或系统环境中配置：

```bash
# Anthropic
ANTHROPIC_API_KEY=sk-ant-...

# OpenAI
OPENAI_API_KEY=sk-...

# 或使用 CLI 登录
./godex login openai
./godex login codex
```

## 日志

ACP 模式下，stdout 用于 ACP 协议通信，所有诊断日志写入文件：

- 默认日志路径：`~/.godex/log/godex.log`
- 可在 `godex.yaml` 的 `logging.file_path` 中自定义

查看实时日志：

```bash
tail -f ~/.godex/log/godex.log
```

## 故障排查

### ACP agent 无响应

1. 确认二进制可执行：
   ```bash
   ./godex doctor
   ```

2. 确认 ACP 模式诊断：
   ```bash
   ./godex doctor acp
   ```

3. 检查日志：
   ```bash
   tail -50 .godex/log/godex.log
   ```

### 模型调用失败

```bash
./godex providers list
./godex providers test <provider-id>
```

### 权限审批阻塞

当 agent 请求执行高风险操作时，会在对话中显示审批提示。在 VS Code ACP client 中回复：

```
/approve <request-id>
```

或使用 `/approve <request-id> session` 在本次会话中自动批准同类操作。

## 推荐项目结构

```
your-project/
├── godex.yaml               # 项目配置（可选，推荐 gitignore）
├── scripts/
│   └── run_acp_server.sh    # ACP server 启动脚本
└── AGENT.md                 # 项目级 agent 指令（可选）
```

## `AGENT.md` 项目指令

在项目根目录创建 `AGENT.md` 文件，GoDex 会自动加载为系统指令。示例：

```markdown
# 项目说明

这是一个 Go 微服务项目。

## 代码规范
- 使用 table-driven tests
- 错误处理使用 `fmt.Errorf` 包装
- 提交前运行 `go test ./...`

## 构建命令
- 测试：`go test ./...`
- 构建：`go build ./...`
- Lint：`golangci-lint run`
```
