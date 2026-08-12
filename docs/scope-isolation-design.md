# Scope 隔离模型设计（roadmap 6.2）

> 状态：已完成 ✅（2026-08-12，M1-M5 全部落地，见 roadmap 6.2）
> 日期：2026-08-12
> 关联：roadmap 6.2（依赖 3.3 ✅）、`docs/per-session-workspace-plan.md`、4.5 写 scope 与 bundle 联动
> 参考：`temp/qm/src/types.ts`（scopeId/parseScopeId）、`scope-storage-key.ts`、`scope-classifier.ts`、`scoped-event-sink.ts`、`memory-service.ts`

---

## 一、背景与问题

godex 是单进程多 session 架构：`backend.Service` 持有全局 `cfg`，每个 session 创建独立 `Agent`，但共享 `cfg.WorkspaceDir`、`cfg.Paths.MemoryDir`、`SharedDependencies`。当前隔离现状：

| 维度 | 现状 | 问题 |
|------|------|------|
| **memory** | 全局单一 `memory/` 目录（`MEMORY.md` + candidates + sidecar `memory.db`），所有 session 读写同一份 | session A 记住的内容会污染 session B 的 recall/consolidation；多项目共用一服务时记忆串味 |
| **files** | 子 agent 有 `write_scope`（4.5，bundle 联动收窄写工具）；主 agent 文件工具仍以 `cfg.WorkspaceDir` 为根全量读写 | 只有"子 agent 写权限收窄"，没有统一的"会话级路径边界"；per-session workspace 仅靠创建时注入，无统一模型 |
| **sandbox** | `Sandbox` 接口（3.3）有 `ID/WorkspaceDir/TempDir/ArtifactDir`，但**没有 scope 语义**；per-session workspace 是创建期注入 | 无法表达"同一沙箱在不同 scope 下的不同行为"，Rebuild 也不携带 scope |

**核心问题**：memory/files/sandbox 三处各自为政，缺少一个统一的 **ScopeId** 概念把"这个 agent 当前在哪个边界内工作"贯穿起来。QM 参考实现（`temp/qm/src/`）已有完整模型：`ScopeId = kind:ref`，memory-service 的所有方法（recall/capture/query/read/replace）第一参数都是 `scopeId`。

## 二、目标 / 非目标

### 目标
1. 定义 `ScopeId` 类型 + 构造/解析/存储键工具（对齐 QM `scopeId()`/`parseScopeId()`/`scopeStorageKey()`）。
2. **memory 按 scope 分区**：每个 scope 拥有独立的记忆目录（`memory/<scope-key>/`），recall/consolidation 默认只在该 scope 内进行；支持跨 scope 合并查询（session + org）。
3. **files 路径按 scope 限定**：写工具（bash/write_file/edit_file 等）的路径解析统一经过 scope root 检查；只读工具不限定。
4. **sandbox 携带 scope**：`Sandbox` 接口新增 `ScopeID()`，`LocalOptions` 增加 scope 字段，per-scope workspace/temp/artifact 目录由 scope 推导。
5. 审计/事件带 scope 标签（对齐 `scoped-event-sink`），便于按 scope 过滤。

### 非目标
- **不做成员关系目录**（channel/team 成员判定，QM `scope-membership.ts`）——godex 本地单机无 Slack 目录服务。
- **不做跨进程多租户**——仍是单进程多 session。
- **不改变 4.5 write scope 的 bundle 联动语义**——6.2 在其之上叠加统一的路径 scope 解析，不重写 `resolveSubagentWriteScope`。
- **不迁移既有 memory 数据**——存量目录视为默认 scope（见 §6 兼容性）。

## 三、ScopeId 设计

### 3.1 类型与构造（`internal/core/scope/scope.go` 新包）

```go
// ScopeKind 是 scope 的类别。对齐 QM（personal/channel/team/org/group），
// 但 godex 本地场景只启用子集。
type ScopeKind string

const (
    ScopeSession  ScopeKind = "session"   // 单个会话：默认隔离粒度
    ScopePersonal ScopeKind = "personal"  // 用户个人：跨 session 共享
    ScopeOrg      ScopeKind = "org"       // 工作区级：全服务共享（默认兼容层）
)

// ScopeId 形如 "session:<sessionID>" / "org:<workspaceName>" / "personal:<user>"。
// 用 string 便于 JSON 透传与日志，提供构造/解析函数保证格式正确。
type ScopeId string

func New(kind ScopeKind, ref string) ScopeId            // "kind:ref"
func Parse(id ScopeId) (ScopeKind, string, bool)         // 解析，非法返回 false
func Session(id string) ScopeId
func Personal(user string) ScopeId
func Org(name string) ScopeId
func (s ScopeId) IsShared() bool                         // 非 session 即共享
func (s ScopeId) String() string
```

**规则**：
- ref 不允许含 `:`（构造时清洗或拒绝）；kind 必须是已知值。
- 空 scope（`""`）= 未指定，调用方决定默认值（session scope）。
- `session` scope 的 ref 用现有 session ID（与 6.3 Session 树的 `session_id` 一致）。

### 3.2 存储键（对齐 QM `scope-storage-key.ts`）

```go
// StorageKey 把 ScopeId 转成安全的文件系统路径片段：
// 合法字符保留，非法字符替换为 "__"；若 ref 含特殊字符，追加 hash 后缀防碰撞。
func StorageKey(id ScopeId) string
```

QM 参考：`legacy = scopeId.replace(/[^a-zA-Z0-9_.-]/g, "__")`，若 ref 非安全字符则追加 `--<hash12>`。Go 侧用 `sha256` 前 12 位 hex。

**示例**：
- `session:abc-123` → `session:abc-123`（`:` 在目录名中合法但建议替换为 `__` → `session__abc-123`）
- `org:godex` → `org__godex`
- 含 `/`、`..` 等路径穿越字符的 ref → 替换 + hash 后缀（**必须防目录穿越**）。

### 3.3 实体绑定：ScopeId ↔ org / user / session

**godex 现有实体盘点**（决定了 ScopeId 的 ref 来源）：

| 实体 | godex 现状 | ref 来源 |
|------|-----------|----------|
| **session** | ✅ 有。`SessionLocator{Channel, Key, UserID, Metadata}` → `stableSessionID()` 哈希出 `web-<hex>` | session ID（与 6.3 Session 树一致） |
| **user** | ⚠️ 半有。仅 `locator.UserID` 字符串（前端 `?user_id=` 传入），**无独立用户目录/principal 实体**，auth 仅 OAuth 取 key | `locator.UserID`；为空时 fallback 到 session scope |
| **org** | ❌ 无。config 无 `org_id`/tenant；最接近的是 `cfg.WorkspaceDir`（路径，非身份） | 混合方案（见下） |

**绑定解析**（backend `OpenSession` 时一次性解析，注入 Agent）：

```go
// scope_resolver.go（新增）
type SessionScopes struct {
    Session  scope.ScopeId // session:<stableSessionID>
    Personal scope.ScopeId // personal:<userID>，userID 为空时 = session scope
    Org      scope.ScopeId // org:<orgID>，混合方案解析
}

func ResolveSessionScopes(sessionID string, locator SessionLocator, cfg *config.Config) SessionScopes {
    personal := scope.Session(sessionID)
    if user := strings.TrimSpace(locator.UserID); user != "" {
        personal = scope.Personal(user)
    }
    return SessionScopes{
        Session:  scope.Session(sessionID),
        Personal: personal,
        Org:      scope.Org(resolveOrgID(cfg)),
    }
}

// org 混合方案：显式配置优先，workspace 身份兜底。
func resolveOrgID(cfg *config.Config) string {
    if id := strings.TrimSpace(cfg.OrgID); id != "" {
        return id // 显式配置：稳定、可跨项目共享（对齐 QM env ORG_ID）
    }
    return workspaceOrgID(cfg.WorkspaceDir) // 兜底：cleanProjectDir 规范化后的 basename
}
```

**混合方案权衡**（用户已确认）：

| 维度 | 新增 `org_id` 配置（A） | 复用 workspace 身份（B） | 混合（配置优先，workspace 兜底） |
|------|------------------------|--------------------------|----------------------------------|
| 语义 | 正确：org 是身份，与路径解耦 | 直觉：每项目目录 = 一 org | 默认直觉，显式稳定 |
| 稳定性 | 换目录/换机器不变 | 路径即身份，易变 | 显式配置时稳定 |
| 多项目共享 org 记忆 | ✅ 支持 | ❌ checkout 即碎片 | ✅ 配 org_id 即共享 |
| 配置成本 | 新增配置面（yaml/schema/template/env） | 零配置 | 仅新增 `org_id`（默认空，schema/template 各一行） |
| 对齐 QM | ✅ `scopeId("org", config.orgId)` | ❌ | ✅ 显式路径对齐，兜底路径兼容本地 |
| 默认行为 | `default-org` 多用户共用会串 | 单机单项目最简 | 单机直觉、多人可配 |

**贯穿方式**：
- `Agent` 新增 `scopes SessionScopes` 字段，暴露 `ScopeID()`（默认返回 session scope）+ `OrgScopeID()`/`PersonalScopeID()`。
- memory：`NewScopedManager(memoryDir, scope.StorageKey(scopes.Session))` 每 session 一个；合并查询（M2 可选）带 `OrgScopeID()`。
- sandbox：`LocalOptions.Scope = scopes.Session`（workspace/tmp/artifact 目录推导用）。
- 审计/timeline：事件打 `scope_label = scopes.Session`，跨 session 视图用 org 过滤。

## 四、三处接入设计

### 4.1 memory 按 scope 分区（改动最大）

**现状**：`internal/core/memory/manager.go` 的 `Manager{dir}` 单目录，`MEMORY.md` 索引 + 文件记录 + sidecar `memory.db`；`Strategy` 接口（per-turn/agent-only/consolidated）无 scope 概念。

**设计**：目录布局改为 `dir/<scope-key>/`，每 scope 一套完整状态：

```
<memory_dir>/
├── (legacy 根目录：存量数据，视为 org scope 兼容层，见 §6)
└── session__abc-123/     ← session scope
│   ├── MEMORY.md
│   ├── candidates.json
│   ├── index.json
│   └── memory.db
└── org__godex/           ← org scope（跨 session 共享）
    └── ...
```

**接口改动**（最小侵入，保持既有方法签名向后兼容）：

```go
// 新增：scope 感知的 Manager 构造与访问
func NewScopedManager(dir string, scope ScopeId) *Manager   // dir 指向 scope 根（由 caller 拼 scope-key）
func (m *Manager) Scope() ScopeId

// 既有方法（Remember/Search/FindRelevant/CandidateCount/...）不变，
// 因为 Manager 实例已经绑定单一 scope —— 这是最省事的接入方式：
// backend 为每个 session 构造一个 scope 绑定的 Manager，替代现在的全局单例。
```

**recall 跨 scope 合并**（可选，M2 之后）：
`FindRelevant` 支持 `SearchOptions.Scopes []ScopeId`，默认 `[session, org]`——先查 session 记忆，再合并 org 共享记忆，session 优先去重。**第一版只做 session 隔离，不做合并**，避免语义膨胀。

**Agent 侧**：`Agent` 现在持有的 memory 引用改为按 session scope 构造的 Manager；`Agent` 暴露 `ScopeID()` 供工具/审计使用。

### 4.2 files 路径按 scope 限定

**现状**：主 agent 工具以 `cfg.WorkspaceDir` 为根；子 agent 有 4.5 `write_scope`（路径前缀收窄）+ `narrowSubagentWriteTools`。

**设计**：新增统一路径解析层 `internal/agent/scope_path.go`：

```go
// ResolveWritePath 把工具请求的路径解析为 scope 内的绝对路径。
// scope root = session 的 per-session workspace（若设置）否则 org workspace。
// 越界（.. 逃逸 / 绝对路径不在 root 下）→ error。
func ResolveWritePath(scope ScopeId, root string, requested string) (string, error)
```

**接入点**（写工具拦截，复用 6.1 的 hook 思路）：
- `bash` / `write_file` / `edit_file` / `mkdir` / `rm` 等写工具执行前，把 `path`/`cwd` 参数经 `ResolveWritePath` 校验；越界直接拒绝并返回明确错误。
- 只读工具（`read_file`/`lsp`/`web`/`grep`/`glob`）不做路径限定（与 4.5 语义一致：researcher 天然只读）。
- **与 4.5 的关系**：4.5 是"子 agent 有没有写权限 + 写哪些前缀"；6.2 是"会话级路径边界"——两者叠加：子 agent 最终可写 = scope root ∩ write_scope。

**per-session workspace 纳入**：`docs/per-session-workspace-plan.md` 已实现 `Metadata["project_dir"]`；6.2 把它正式化为 session scope 的 root（`scope_root(session) = project_dir || cfg.WorkspaceDir`）。

### 4.3 sandbox 携带 scope

**现状**（3.3 已解耦）：`internal/sandbox/sandbox.go` 的 `Sandbox` 接口（ID/Lifecycle/WorkspaceDir/TempDir/ArtifactDir/ToolBinding/Info/FileSystem/Rebuild）+ `LocalSandbox`。

**设计**：

```go
// Sandbox 接口新增（向后兼容：默认实现返回 "" 表示未指定 scope）
ScopeID() ScopeId

// LocalOptions 增加字段
type LocalOptions struct {
    ID           string
    Scope        ScopeId   // 新增
    WorkspaceDir string
    TempDir      string
    ArtifactDir  string
    Execution    tooling.ExecutionConfig
    Lifecycle    Lifecycle
}

// 目录推导规则（未显式设置时）：
//   WorkspaceDir = scope_root(session)   （session scope）
//   TempDir      = <workspace>/.godex/tmp/<scope-key>   （per-scope 临时区）
//   ArtifactDir  = <workspace>/.godex/artifacts/<scope-key>
```

`Rebuild()` 保留 scope（`LocalSandbox.Rebuild` 复制 Scope 字段）。`Agent.sandbox` 通过 `SandboxScope()` facade 暴露 scope，供 tools/audit 使用。

### 4.4 审计/事件带 scope 标签（对齐 `scoped-event-sink`）

- security audit 事件、timeline 事件（`subagent_job_updated`/`runner_phase_changed` 等）payload 增加 `scope_label`（可选字段，向后兼容）。
- 前端 timeline 可按 scope 过滤（复用 `timelineEventTypeOptions` 模式）。

## 五、与既有基础设施的关系

| 既有能力 | 关系 |
|----------|------|
| 3.3 Sandbox 接口 | 在其上加 `ScopeID()`，不破坏接口契约测试 |
| 4.5 写 scope 与 bundle 联动 | 保留；6.2 在其上叠加会话级路径边界（交集） |
| per-session workspace（已实施） | 正式化为 session scope root 的来源 |
| 6.3 Session 树 | fork 出的分支 session 拥有**独立 session scope**（新 session_id）；org scope 继承共享 |
| 6.1 安全筛查器 | 无直接关系；screener hook 在 scope 校验前/后均可（建议 scope 校验先于 screener，越界路径根本不执行） |

## 六、影响面与兼容性

**兼容策略（关键）**：默认行为 = **org scope**（全服务共享，等价现状）。只有显式配置会话隔离（如新建会话时勾选"隔离记忆/工作区"）才启用 session scope。这样：
- 存量 `memory/` 根目录数据视为 org scope，无需迁移。
- 既有 API/工具调用不传 scope → 默认 org → 行为不变。

**变更文件预估**：
- 新增：`internal/core/scope/scope.go` + `scope_test.go`（类型/解析/存储键）
- 改：`internal/core/memory/manager.go`（scoped 构造）、`internal/agent/sandbox_facade.go`（scope 透传）、`internal/sandbox/sandbox.go`（ScopeID + LocalOptions）、写工具拦截（`internal/tools/`）、backend session 创建（构造 scoped Manager/sandbox）
- 审计/timeline payload 加字段（向后兼容，不加不改旧事件）

**风险**：
- 写工具路径校验可能误伤合法用法（如跨目录引用共享依赖）→ 第一版只拦"越出 scope root"的绝对逃逸，不做精细白名单；用宽松模式可关（config 开关，默认开）。
- memory 分区后 consolidation 范围变小 → 属预期（隔离语义），文档中明示。

## 七、验收标准

1. **ScopeId 工具单测**：构造/解析/非法拒绝/存储键安全（含 `..`/`/` 穿越字符 + hash 后缀），对齐 QM 行为。
2. **memory 分区**：两个 session 各自 remember 一条，互相 `FindRelevant` 不可见；org scope 写入对两个 session 都可见（合并查询时）。
3. **sandbox scope**：`SandboxScope()` 返回正确值；per-scope tmp/artifact 目录各归各；Rebuild 保留 scope。
4. **写路径限定**：session scope 下写 `../` 逃逸被拒；写 scope 内路径正常；只读工具不受限。
5. **默认兼容**：不配置隔离时，全量回归（agent/backend/core）无新增失败；既有 memory 数据可读。
6. **审计标签**：新事件带 `scope_label`，旧事件不解析失败。

## 八、里程碑（建议拆分，避免大爆炸）

| 里程碑 | 内容 | 预估 |
|--------|------|------|
| M1 | `internal/core/scope` 类型 + 存储键 + 单测 | 0.5d |
| M2 | memory 按 scope 分区（scoped Manager 构造 + backend 接线 + 隔离测试） | 1.5d |
| M3 | sandbox `ScopeID` + per-scope 目录推导 + Rebuild 保留 | 1d |
| M4 | 写工具路径限定（ResolveWritePath + 拦截 + 宽松开关） | 1d |
| M5 | 审计/timeline scope_label + 全量回归 + roadmap 标记 ✅ | 0.5d |

每个里程碑独立可交付、可回滚；M2 开始前先提交 M1。

## 九、参考

- `temp/qm/src/types.ts`：`ScopeId`/`scopeId()`/`parseScopeId()`/`personalScope()`/`isSharedScope()`
- `temp/qm/src/util/scope-storage-key.ts`：`scopeStorageKey()`（清洗 + hash）
- `temp/qm/src/classify/scope-classifier.ts`：`classifyScopeLabel()`（soul→org、tool_result→source、其他→session）
- `temp/qm/src/admin/scoped-event-sink.ts`：事件按 scope 过滤
- `temp/qm/src/memory/memory-service.ts`：所有方法第一参数为 `scopeId`
- godex：`internal/core/memory/manager.go`、`internal/sandbox/sandbox.go`、`internal/agent/sandbox_facade.go`、`docs/per-session-workspace-plan.md`、roadmap 4.5/3.3
