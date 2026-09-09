# 远程沙箱（RemoteSandbox）设计：A 会话 + B 工具执行

> 状态：Draft（待确认后实施）
> 背景：用户在 pod 等无 UI 环境运行 godex，希望「A（工作电脑）起对话，工具调用运行在 B（内网 pod）」，B 作为执行沙箱。痛点：pod 重启配置/session 丢失。
> 关联：node-center-bridge-design.md（中心桥/relay 基建）、node-onboarding.md（接入）

## 1. 形态目标

```
内网 A（工作电脑，桌面端）
  agent 会话在 A（session/日志/上下文归 A，pod 重启不丢）
  工具调用（bash/文件/终端）经 中心 C → relay → 内网 B 执行
内网 B（pod，无 UI）
  仅提供执行环境：WorkspaceDir/TempDir/ArtifactDir 都在 B
  B 重启 = 执行环境重建；会话不丢（在 A）
```

关键收益：**session 天然归 A** —— 用户痛点「pod 重启 session 丢失」在 B 路径下自动解决（会话历史、日志、上下文都在 A 的本地 session store）。

## 2. 已确认决策

| 项 | 决策 |
|---|---|
| 执行形式 | **RemoteSandbox（沙箱实现），非 MCP server** |
| 沙箱范围 | **完整沙箱**：bash 命令 + 文件读写 + 终端 PTY + workspace 布局全走远端 |
| 绑定粒度 | **会话级（已确认）**：A 的某个会话可选「执行在 B」，其余会话仍本地 |

### 为什么是 RemoteSandbox 而非 MCP server（代码依据）
1. **执行层已有远程执行先例**：`tooling.ExecutionConfig.Mode = local|docker|ssh`，`sshShellCommand` 已实现。新增 `ExecutionModeRelay` 是既有扩展点，不是新概念。
2. **agent 工具系统围绕 Sandbox 抽象**：工具 handler 从 `sandbox.ToolBinding()` 拿 WorkspaceDir/Execution 执行，agent 不感知执行地。实现 `RemoteSandbox` 后 A 侧工具列表/prompt/调用协议**零改动**。
3. **MCP 是 client 形态**：godex 的 MCP（`internal/tools/mcp.go`）是消费外部 MCP server 的 client。让 B 提供工具 = 新写 MCP server 端 + 工具语义重映射 + 流式/PTY 退化，工作量反而更大。

## 3. 复用现有基建（回答「工作量不大」的依据）

| 现有能力 | 复用方式 |
|---|---|
| relay 通道（B join C 出站 WSS） | 工具调用经 relay 到 B，**零新增网络层** |
| CenterBridge + BridgeProxyHandler（A→C→B HTTP 转发，e2e 已验证） | A 侧 RemoteSandbox 的所有操作 = HTTP 经桥转发到 B 的本地 httpapi |
| B 的 httpapi（已有 files/terminal 端点） | 新增一个统一「sandbox exec」端点（复用 tooling.WorkspaceExecutor + workspacefs） |
| `ExecutionConfig.Mode` 分派 | 新增 `ExecutionModeRelay`，WorkspaceExecutor 内一分支 |
| `sandbox.Sandbox` 接口 | 新增 `RemoteSandbox` 实现，装配处按会话选择 |

## 4. 架构与消息流

### 4.1 执行链路（bash 命令示例）

```
A 的 agent 工具 handler
  → WorkspaceExecutor.RunShell（Mode=relay）
    → RemoteExecClient：POST {center}/api/control/nodes/B/proxy/control/sandbox/exec
      （Authorization: Bearer nk_，经 CenterBridge.ServeProxy 转发）
      → 中心 C BridgeProxyHandler（isLocalNode=false → bridge.ServeProxy）
        → C 的 ProxyHandler → relay 通道 → B 的 agent handleTCPOpen → B 本地 httpapi
          → /control/sandbox/exec handler → B 的 WorkspaceExecutor.RunShell
          → {stdout, stderr, exit_code} 原路回传
```

### 4.2 B 侧端点（新增，`internal/runtime/httpapi/routes_sandbox_exec.go`）

B 的 httpapi 新增（受 protected 保护——仅本节点自己的 relay trust 头可过，A 无法直连 B）：

| 端点 | 功能 | 复用 |
|---|---|---|
| `POST /control/sandbox/exec` | 执行 bash 命令（cwd=workspace） | `tooling.WorkspaceExecutor.RunShellBudgeted` |
| `POST /control/sandbox/fs` | 文件操作（read/write/readdir/stat/mkdir/rm/rename） | `workspacefs.FS` + `tooling.ReadFileLines` |
| `GET  /control/sandbox/terminal`（可选，SSE） | 交互式 PTY | 复用现有 `routes_terminal.go` 的本地实现 |

端点统一返回 `{ok, data, error}` JSON；exec 长输出沿用 B 已有的 budget/截断策略。

### 4.3 A 侧 RemoteSandbox（新增，`internal/sandbox/remote.go`）

实现 `sandbox.Sandbox` 接口：
- `WorkspaceDir/TempDir/ArtifactDir` → B 侧路径（B 的 exec 端点创建/返回，A 只存字符串）
- `ToolBinding().Execution` → `{Mode: ExecutionModeRelay, SSHTarget 语义复用为 relay 目标}`（新增 `RelayTarget` 字段：center_url + node_id + nk_）
- `FileSystem()` → `RemoteFS`（实现 `workspacefs.FS`，方法经 relay HTTP 转发）
- `Rebuild()` → 同配置新实例（对应 pod 重建后执行环境自动重建）

### 4.4 执行层扩展（`internal/platform/tooling/tooling.go`）

```go
const ExecutionModeRelay = "relay"
type ExecutionConfig struct {
    // ...existing
    RelayTarget  string // "center_url|node_id"（会话级 sandbox 配置解析而来）
    RelayToken   string // nk_ 受限凭证（不落 yaml，走 .env/内存）
}
```
`shellCommand`/`argvCommand` 增加 `case ExecutionModeRelay`：把命令封装为 JSON 经 `RemoteExecClient` 发出（复用 CenterBridge 的 HTTP 转发，不走本地 ssh 二进制）。

### 4.5 会话级绑定（A 侧装配）

- `cmd/godex/main.go`：agent 装配处按「会话 sandbox 配置」选择 Sandbox：
  - 未配置 → 现有 `LocalSandbox`（默认，行为不变）
  - 配置 `sandbox_node = B` → `RemoteSandbox`
- 配置来源：agent 模板字段（复用 templates 的 memory/scope 分区先例）或会话创建参数；默认本地，显式才远程。

## 5. 一键执行器（中心 UI + pod 命令）

**中心「节点」页**新增卡片（复用 JoinNodeCard 的生成/复制交互）：
「沙箱节点接入命令」→ 复制命令 → 在 pod 里执行一次：

```bash
godex node join 'https://center' --id pod-b --credential ck_xxx --trust guarded-remote \
  --sandbox-exec-on  # 新 flag：标记本节点为执行沙箱（启用 /control/sandbox/exec 端点）
```

**pod 重启恢复**（痛点 1 的解法）：
- 命令**幂等且自包含**：join + 写 .env（credential/nk_）+ 启用 sandbox exec 端点；重跑同一条命令即恢复全部配置。
- 可选用 `--data-dir <持久卷路径>`：若 pod 挂了持久卷，workspace/session 天然保留；未挂卷时，重跑命令 = 全新执行环境，A 的会话历史不受影响（在 A）。

## 6. 范围与验收

## 6. 范围与验收

### M1（核心链路）✅ Implemented（2026-09-09）

**实施内容**：
- **B 侧端点**（`internal/runtime/httpapi/routes_sandbox.go`）：`POST /control/sandbox/exec`（复用 `WorkspaceExecutor.RunShellBudgeted`，空命令 400、非零退出返回 200+输出）、`POST /control/sandbox/fs`（read/write/readdir/stat/mkdir/rm/rename，base64 载荷）。受 protected 保护（web token 或 relay trust）。
- **A 侧执行层**（`internal/platform/tooling/`）：`ExecutionModeRelay` + `ExecutionConfig` 加 `RelayCenter/RelayNode/RelayToken`；`RelayClient`（POST `{center}/api/control/nodes/{node}/proxy/control/sandbox/exec|fs`，Bearer nk_）；`RunShellBudgetedWithOptions` relay 分支 `runShellRelay`。
- **RemoteSandbox/RemoteFS**（`internal/sandbox/remote.go`）：实现 `sandbox.Sandbox` 接口，`ToolBinding()` 强制 relay mode，`FileSystem()` 返回经 relay 转发的 `workspacefs.FS` 实现；`Rebuild()` 保留身份与目标。
- **会话级装配**：`sandboxFromConfig`（原 `localSandboxFromConfig` 改名）——`tools.execution.mode=relay` 且 `relay_node` 非空时创建 RemoteSandbox（center/token 缺省取 control 段），否则保持 LocalSandbox 行为完全不变。config 全链路（types/config/resolve/schema/template/values/setters）加 relay 字段。
- **单测**：routes_sandbox_test（exec/fs 端点 4 例）、relay_client_test（转发/错误透传/relay 分派 4 例）、remote_test（RemoteFS 转发/绑定/Rebuild 5 例）全绿。

**验证**：`go build ./...`；tooling/sandbox/httpapi/config/cmd/app 六包测试全绿。注：agent 包 3 个失败（`godex_docs` pin 测试 + 2 个 TempDir cleanup 竞态）经 git stash 基线验证为既有问题，与 M1 无关。

**手动 e2e（未跑，待执行）**：A 配置 `tools.execution.mode=relay + relay_node=pod-b` 后重启 → A 发 `echo hello` → 输出来自 B；A 写文件 → 落在 B workspace。

### M2（一键 + 持久化，待做）
- B 侧 `/control/sandbox/exec` + `/control/sandbox/fs` 端点（复用 tooling/workspacefs）
- A 侧 `ExecutionModeRelay` + `RemoteSandbox` + `RemoteFS`
- 会话级装配：默认本地，显式 sandbox_node 才远程
- 单测：exec/fs 端点、RemoteFS 转发、relay mode 分派
- 手动 e2e：A 配置 sandbox_node=pod-b → A 发 `uname -a` → 输出来自 B；A 写文件 → 落在 B workspace

### M2（一键 + 持久化）
- 中心 UI「沙箱节点接入命令」卡片（复用生成/复制交互）
- `--sandbox-exec-on` + `--data-dir`（幂等命令，pod 重建重跑即恢复）
- 文档：node-onboarding.md 补「沙箱节点」章节

### 验收
1. `go build ./...` + relay/httpapi/config/cmd/app 测试全绿，`tsc -b` + `vite build` 通过
2. 三节点手动 e2e：A 会话工具调用在 B 执行（bash/文件/终端），A 会话历史完整
3. pod 重建后重跑一键命令，A 无需任何重配即可继续对话
4. 未配置 sandbox_node 的既有会话行为与现在完全一致（无回归）

## 7. 开放点（实施前确认）

1. ~~Q2 默认会话级~~ **Q2 已确认会话级（2026-09-09）**；节点级全局配置留作后续可选项，不影响本方案。
2. 终端 PTY 走 SSE（复用 B 的 terminal 端点）还是先只支持 exec/fs（M1 不做 PTY）？默认：M1 先 exec+fs，PTY 放 M2（完整沙箱语义逐步到位）。
3. `--trust guarded-remote` 对沙箱节点：exec 端点是「被 A 调用的执行端」，信任级别语义需要对齐（建议沙箱节点保持 trusted 或 guarded 均可，A 侧会话自己负责审批）。
