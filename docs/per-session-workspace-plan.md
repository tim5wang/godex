# Per-Session 工作目录（新建对话可选设置工作目录）

> 状态：已实施（2026-07-31）
> 日期：2026-07-31
> 关联：chat-v2 Web UI、backend session 管理、agent 工具执行

## 1. 需求

在 Web UI 的 chat-v2 界面中，新建对话时支持**可选**地设置一个工作目录（working directory）：

- **不设置**：保持当前默认行为，以 godex service 的运行目录（`cfg.WorkspaceDir`）作为工作目录。
- **设置后**：该 session 工作期间，所有工具执行（bash、文件读写等）都落在指定目录下，而非服务运行目录。

这是一个多 project 场景的基础能力：用户可以在同一个 godex 服务上，让不同对话分别操作不同的代码仓库/目录。

## 2. 背景信息

### 2.1 当前架构

godex 采用单服务多 session 架构：

- `backend.Service` 持有全局唯一的 `s.cfg`（`*config.Config`），其中 `WorkspaceDir` 在进程启动时确定。
- 每个 session 通过 `agent.NewForSession(s.cfg, s.shared, sessionID)` 创建独立的 `Agent` 实例，但**所有 session 共享同一份 `s.cfg` 和 `s.shared`（SharedDependencies）**。
- session 的身份由 `SessionLocator{Channel, Key, Metadata}` 决定，`Metadata["project_dir"]` 已存在，但目前仅用于：
  - session ID 哈希（`stableSessionID`，使同一路径哈希到同一 session）
  - session 列表分组展示（`/resume` 命令按目录分组）

### 2.2 会话创建链路（现状）

```
chat-v2 前端点"新建对话"
  → ChatPage.createSession()：生成时间戳 key，navigate 到 /chat-v2/web/<key>
  → 页面加载时 POST /sessions {locator: {channel: "web", key: <key>}}
  → backend.OpenSession(ctx, locator)
      → withDefaultLocatorMetadata：注入 cfg.WorkspaceDir 到 metadata["project_dir"]
      → stableSessionID(normalized)：哈希出 session ID
      → loadSession(sessionID, locator)
          → agent.NewForSession(s.cfg, s.shared, sessionID)
          → a.RegisterTools()
```

注意：前端 `createSession` 只是生成 key 并导航，**没有传任何工作目录参数**；`POST /sessions` 的 `openSessionRequest{Locator}` 是当前唯一入口。

### 2.3 工作目录的固化点（核心问题）

工具执行目录在 **agent 构造 + 工具注册时**就固化了：

1. **sandbox 创建**（`internal/agent/sandbox_facade.go`）：
   `localSandboxFromConfig(cfg)` → `sandbox.NewLocal({WorkspaceDir: cfg.WorkspaceDir, ...})`

2. **工具注册**（`internal/agent/tool_registration.go` `registerToolsWith`）：
   ```go
   binding := a.SandboxBinding()
   workspaceDir := binding.WorkspaceDir   // ← 来自 sandbox，即 cfg.WorkspaceDir
   tools.NewBashToolWithExecution(workspaceDir, tempDir, execution)
   tools.NewGlobTool(workspaceDir, ...)
   tools.NewReadFileTool(workspaceDir)
   tools.NewWriteFileTool(workspaceDir)
   tools.NewEditFileTool(workspaceDir)
   tools.NewAttachFileTool(workspaceDir)
   tools.NewGrepTool(workspaceDir, ...)
   tools.NewFindTool(workspaceDir, ...)
   tools.NewLsTool(workspaceDir, ...)
   ```
   所有文件/命令工具在注册时把 `workspaceDir` 固化进闭包。

3. **执行层**（`internal/platform/tooling/tooling.go` `WorkspaceExecutor`）：
   - `cmd.Dir = e.WorkspaceDir`（bash 的 cwd）
   - `workspacefs.New(e.WorkspaceDir)`（文件工具的根目录，防目录逃逸）

4. **散落在 agent 内的直接引用**（`a.cfg.WorkspaceDir`）：
   - `repo_map.go`：coding profile 下的仓库地图
   - `tool_result_filter.go`：大结果 spill 到 `<workspace>/.godex`
   - `skill_facade.go`：skill 目录解析
   - `subagent_jobs.go`：subagent worktree / merge / baseline（强依赖，涉及 git 操作）
   - `system_prompt_dynamic.go`：environment 段展示给模型的 "Workspace root"

## 3. 调研结论

1. **session 级独立目录在技术上是可行的**：因为每个 session 有独立的 `Agent` 实例，工具是 per-agent 注册的。只要让某个 session 的 agent 用一份 `WorkspaceDir` 被覆盖的 config，其工具执行目录就自然正确。

2. **关键约束：不能改共享的 `s.cfg`**。`s.cfg` 和 `s.shared` 被所有 session 共享，直接改会串扰其它 session。正确做法是 **clone config 后覆盖 `WorkspaceDir`**（以及派生的 `TempDir` 等），让该 session 的 agent 持有独立 config。

3. **`SharedDependencies` 需要按目录隔离**。`s.shared` 中有 workspace 相关的服务：
   - `sandbox`：必须 per-session（决定工具执行目录）
   - `skillLoader`、`memoryMgr`、`historySearch` 等：第一版可以继续共享（全局能力），后续再按目录隔离
   - `subagent_jobs.go` 的 worktree/merge 逻辑强依赖 `a.cfg.WorkspaceDir`，只要 agent 的 cfg 被正确覆盖即可工作，但需要回归测试

4. **session ID 哈希已包含 `project_dir`**（`stableSessionID`），所以同一 key + 不同目录会得到不同 session，天然避免了"同名 session 跨目录"的冲突。但这也意味着：**session 创建后目录不可变**（改了就是另一个 session）。

5. **持久化**：`sessionState.locator.Metadata["project_dir"]` 会随 manifest 持久化（`loadSession` 时从 manifest 恢复），所以重启后目录不丢失。

6. **校验责任在 API 边界**：路径合法性（存在、是目录、转绝对路径、`filepath.Clean`）应在 `POST /sessions` 处理层完成，复用现有 `cleanProjectDir`。

## 4. 方案

### 4.1 核心思路

session 创建时把可选的工作目录写入 locator metadata（`project_dir`）；`loadSession` 时若该 session 指定了独立目录，则为其 **clone 一份 agent config（仅覆盖 `WorkspaceDir` 及派生路径）**，使该 session 的 agent 拥有独立的 sandbox 和工具绑定。不设置则沿用现有全局行为，完全向后兼容。

### 4.2 改动点

#### 后端

1. **API 层**（`internal/runtime/httpapi/httpapi.go` `POST /sessions`）：
   - `openSessionRequest` 支持可选 `workspace_dir` 字段（或约定放入 `locator.metadata["project_dir"]`）
   - 校验：非空时检查路径存在、是目录、转绝对路径；失败返回 400

2. **backend**（`internal/services/backend/backend.go`）：
   - `OpenSession`：把校验后的目录写入 `locator.Metadata[sessionProjectDirMetadataKey]`（已有机制，只需允许外部指定值覆盖默认值）
   - `loadSession`：读取 session 的 `project_dir`，若与全局 `s.cfg.WorkspaceDir` 不同，则 clone cfg：
     ```go
     sessionCfg := s.cfg
     if dir := session.locator.Metadata[sessionProjectDirMetadataKey]; dir != "" && dir != s.cfg.WorkspaceDir {
         cloned := *s.cfg
         cloned.WorkspaceDir = dir
         cloned.TempDir = <派生，如 dir/.godex/.tmp>
         sessionCfg = &cloned
     }
     a := agent.NewForSession(sessionCfg, s.shared, sessionID)
     ```
   - 注意 `ApplyConfig`/`ApplyModelProfile` 链路不能把 session 的 cfg 换回全局

3. **agent**（`internal/agent/`）：
   - `NewForSession` / `NewWithSharedDependencies`：接受传入的 cfg（可能已是 override 后的）
   - `ApplyConfig`：确认 override session 的 `WorkspaceDir` 不被全局刷新覆盖（需要保留 per-session 的 workspace 字段或在 ApplyConfig 时跳过 WorkspaceDir）
   - `system_prompt_dynamic.go` 的 environment 段自动展示 session 的 workspace（读 `a.cfg.WorkspaceDir` 即可，无需改）

#### 前端

4. **chat-v2 新建对话**（`ui/web/src/features/chat-v2/SessionsRail.tsx` + `ChatPage.tsx`）：
   - 新建按钮旁加轻量的"工作目录"可填项（建议：点击新建时展开一个输入框，默认为空=服务运行目录；不强制弹窗）
   - 提交时在 `POST /sessions` 的 locator metadata 中带上 `project_dir`
   - i18n 词条（中/英）

### 4.3 作用域（第一版）

**纳入**：工具执行目录（bash cwd）+ 文件工具根目录（read/write/edit/grep/glob/ls）+ environment 提示 + repo map。

**不纳入（后续版本再考虑）**：
- skill 加载目录、memory 目录、subagent 的 worktree 隔离策略（仍走全局；subagent merge 依赖 `a.cfg.WorkspaceDir`，cfg override 后自然指向新目录，但需要专门回归测试）
- session 创建后修改目录（等于换 session，不支持）

### 4.4 风险与工作量

| 风险 | 说明 | 缓解 |
|---|---|---|
| SharedDependencies 串扰 | `s.shared` 内部分服务按 workspace 构建 | 第一版只覆盖 cfg，逐个核对 deps 使用点；sandbox 必须 per-session |
| ApplyConfig 覆盖 | 全局配置刷新时把 session 的目录冲掉 | Agent 内保存 `workspaceOverride`，ApplyConfig 时保留 |
| subagent merge | worktree/merge 强依赖 workspace | cfg override 后路径自然正确，但需 e2e 回归 |
| 路径安全 | 用户传入任意路径 | API 边界校验 + `filepath.Clean` + `workspacefs` 已有防逃逸 |
| 环境提示不准 | 模型看到的 Workspace root 与实际执行目录不一致 | environment 段读 `a.cfg.WorkspaceDir`，override 后自动一致 |

### 4.5 待确认问题

1. **工作目录作用域**：第一版只覆盖"工具执行目录 + 文件工具根目录"，skill/memory/subagent-worktree 仍走全局——是否接受？
2. **UI 形态**：新建对话旁的可选输入框（轻量，默认空）vs 新建时弹目录选择对话框——倾向哪种？

## 5. 验证计划

- 单测：`POST /sessions` 带/不带 `workspace_dir`；`loadSession` cfg clone 逻辑；ApplyConfig 不覆盖 per-session workspace
- 集成：创建指定目录的 session → 让 agent 执行 `pwd` / 写文件 → 断言落在指定目录
- 回归：默认 session（不设置目录）行为与现状一致；subagent/longtask 在指定目录下正常
