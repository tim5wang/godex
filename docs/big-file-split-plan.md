# 大文件拆分改造方案（技术债方向二）

> 日期：2026-08-05
> 目标：把 4 个"改起来很危险"的大文件按职责拆分为多个中小文件，降低后续改动回归风险。
> 原则：**同包/同目录拆分，不改包结构、不改导出 API、不改组件对外接口**。每个文件不超过 ~1000 行。

## 1. 现状

| 文件 | 行数 | 主要风险 |
|---|---|---|
| `internal/services/backend/backend.go` | 7269 | 后端核心：session/turn/approval/timeline/持久化全混在一起 |
| `internal/agent/subagent_jobs.go` | 3909 | subagent 全逻辑（store/执行/git/merge/权限/事件）单文件 |
| `internal/core/config/manager.go` | 4082 | 配置加载/解析/更新/Doctor/工具函数单文件 |
| `ui/web/src/features/chat/ChatPage.tsx` | 3640 | 主组件 + 12+ 个辅助面板组件 + 40+ 个纯函数混在一起 |

## 2. 拆分原则

1. **Go 全部走同包拆分**：同包内函数/类型可互相引用，不需要改任何 import，风险最低，可逐步搬迁。
2. **前端按组件边界拆**：辅助组件都是 props 风格（`function ApprovalBanner({...})`），不捕获主组件闭包状态，可独立成文件；纯函数进 `lib/`。
3. **只搬代码，不改逻辑**：拆分期间禁止顺手重构/改 bug，保证 diff 可审查（纯 move）。
4. **每步可编译可测试**：每拆一个文件立刻 `go build ./...` + `go test`（后端）/ `tsc -b`（前端）。
5. **依赖顺序**：先拆"被依赖最少的"（纯工具函数/类型），最后拆"被依赖最多的"（主组件）。

## 3. 拆分方案

### 3.1 `internal/services/backend/backend.go`（7269 → ~8 个文件）

| 目标文件 | 内容（现状行号区间） | 预估行数 |
|---|---|---|
| `backend.go`（保留） | 类型定义（Service/SessionLocator/SessionManifest/OpenedSession/SubmitResult 等 ~71-430）+ `NewService` + `MaxAttachmentUploadBytes` | ~500 |
| `session.go` | session 生命周期：`OpenSession`/`CreateNewSession`/`ForkSession`/`withDefaultLocatorMetadata`/`cleanProjectDir`/`validateSessionProjectDir`/`stableSessionID`/`DeleteSession`/`ListSessions`（430-949 + 4445-4566） | ~700 |
| `turn.go` | turn 管理：`Submit`/`SubmitAsync`/`enqueueTurn`/`injectActiveTurn`/`startQueuedTurns`/`CancelTurn`/`RetryTurnAsync`/`ResumeTurnAsync`/`runUserTurnLocked`/`startUserTurnLocked`/`finishAgentTurnLocked`/`checkpointRunningTurn`（1058-2030） | ~900 |
| `persist.go` | 持久化：`loadSession`/`readSessionFiles`/`readSessionState`/`readSessionTimeline`/`persistSession`/`writeSessionCheckpoint`/`writeManifest`/`saveSessionToStore`（4740-5745） | ~900 |
| `sessionstate.go` | `sessionState` 方法族：`acquire`/`setActiveTurn`/`updateTurnStatus`/`recordTurnStarted`/`turnRecords`/`queuedTurns`/`enqueue`/`peekQueued` 等（6114-6611） | ~500 |
| `commands.go` | `ExecuteCommand`/`dispatchPackageSubagent`/`dispatchPackageAgentTurn`/`wireSlashCommandHandlers`/`formatSessionLine`（2030-2206 + 4294-4445） | ~600 |
| `permissions.go` | `PendingPermissions`/`ApprovePermission`/`DenyPermission`/`activePermissionBlocker`/`appendPermissionAuditEvent`（3788-3922 + 3028-3065） | ~300 |
| `skills.go` | skill 相关：`ListSessionSkills`/`InstallSessionSkill`/`ActivateSessionSkill`/`ExpandSessionSkill`/`UnloadSessionSkill` 等（3922-4296） | ~400 |
| `packages.go` | 包管理：`ListPackages`/`InstallPackage`/`RunPackageSmoke`/`packageSmoke*`/`PackageQuality`（2645-2998） | ~400 |
| `subagents.go` | subagent API：`ListSubagents`/`ReviewSubagent`/`MergeSubagent`/`CancelSubagent`/`ResumeSubagent`（3353-3435） | ~150 |
| `longtasks.go` | LongTask API：`ListLongTasks`/`CreateLongTask`/`RunLongTask`/`CancelLongTask`/`RollbackLongTaskStory`/`GCLongTaskArtifacts` + Mintui 变体（3435-3788） | ~400 |
| `timeline.go` | Timeline：`Timeline`/`TimelinePage`/`filterTimelineEvents`/`timelineSearchText`/`timelinePayloadString`（3191-3353） | ~250 |
| `attachment.go` | 附件：`StoreAttachment`/`ResolveAttachment`/`materializeArtifactPaths`/`storeArtifactPath`/`sanitizeAttachmentName`（2256-2411 + 7180） | ~200 |
| `helpers.go` | 纯工具函数：`cloneTurnRecord`/`normalizeTurnRecords`/`mergeTurnRecord`/`randomSuffix`/`firstNonEmpty`/`stateDigest` 等（6611-7269） | ~600 |

> 说明：`models.go`（Models/SetSessionModelProfile 2411-2537）、`security.go`（SecuritySummary/SecurityAudit 2547-2645）体量较小，可先并入 helpers 或独立，按实际搬迁时定。`sessiongraph` 相关（4929-5060）可并入 persist.go。

### 3.2 `internal/agent/subagent_jobs.go`（3909 → ~7 个文件）

| 目标文件 | 内容（现状行号区间） | 预估行数 |
|---|---|---|
| `subagent_jobs.go`（保留） | 类型/常量：`subagentJob`/`subagentJobStatus`/`subagentReview`/`subagentMergeResult`/view 类型/`ErrDurableSubagentNotFound`（34-344） | ~350 |
| `subagent_store.go` | `subagentJobStore` 全部方法：`loadAll`/`Start`/`StartWithOptions`/`RegisterTarget`/`Watch`/`StartNextPending`/`SetWorkspace`/`UpdateMessages`/`AppendProgress`/`Finish`/`saveLocked`（344-1193） | ~850 |
| `subagent_run.go` | 执行：`StartDurableSubagent*`/`startPendingSubagents`/`runSubagentJob`/`runSubagentJobAsync`/`ResumeDurableSubagent*`/`prepareSubagentWorkspace`/`ensureSubagentWorkspace`（1193-2012） | ~850 |
| `subagent_git.go` | git worktree：`cleanGitRepository`/`createSubagentGitWorktree`/`applyGitDirtyOverlay`/`removeSubagentGitWorktree`/`applyPreviewJobsToSubagentWorkspace`（1810-2012） | ~250 |
| `subagent_merge.go` | Review/Merge/GC：`ReviewDurableSubagent*`/`MergeDurableSubagent*`/`CleanupDurableSubagentWorkspace`/`CleanupSubagentWorkspaces`/`collectSubagentChanges`/`detectSubagentMergeConflicts`/`applySubagentChanges`/`buildSubagentDiffPreview`（2012-2272 + 2857-3128） | ~700 |
| `subagent_tools.go` | 工具执行/授权：`executeSubagentToolForJob`/`authorizeSubagentTool`/`executeSubagentToolWithHandlers`/`enforceSubagentWriteScope`/`workspaceSubagentToolHandlers`（2497-2870） | ~400 |
| `subagent_policy.go` | role/tool 解析：`resolveSubagentRole`/`subagentToolNamesForRole`/`validateSubagentToolInheritance`/`subagentCapabilityAllowed`/`durableSubagentPromptForRole`/`normalizeWriteScope`（3139-3570） | ~450 |
| `subagent_views.go` | view 转换：`durableSubagentJobView`/`durableSubagentReviewView`/`durableSubagentMergeView`/`durableSubagentProgressViews`（2272-2497） | ~250 |
| `subagent_events.go` | 事件/进度：`recordSubagentProgress`/`appendSubagentProgress`/`subagentEventTarget.emit*`/`appendBoundedSubagentProgress`（3570-3909） | ~350 |

### 3.3 `internal/core/config/manager.go`（4082 → ~6 个文件）

| 目标文件 | 内容（现状行号区间） | 预估行数 |
|---|---|---|
| `manager.go`（保留） | `Manager` struct + `NewManager` + 核心方法（`Current`/`Meta`/`Schema`/`View`/`Reveal`/`Update`/`ReloadFromDisk`/`SetApplier` 等 29-409） | ~400 |
| `doctor.go` | `Doctor()` 方法（409-1025，约 600 行独立） | ~600 |
| `resolve.go` | 加载解析：`reload`/`resolve`/`resolveConfigFile`/`effectiveConfigFile`/`mergeConfigFileLayer`/`parseConfigFile`（1025-1707 + 2280-2450） | ~900 |
| `profiles.go` | 模型/provider 转换：`modelProfilesFromConfigFile`/`llmProvidersFromConfigFile`/`maskProfileSecrets`/`asLLMProviders`/`asLLMStrategy`（1707-2280 + 2866-3230） | ~700 |
| `values.go` | 值管理：`applyStoredValues`/`setStoredValue`/`storedValues`/`effectiveValues`/`fieldConfigured`/`maskLLMProviders`（2450-2866 + 3230-3468） | ~700 |
| `env.go` | 环境/路径：`readDotEnvFile`/`mergeEnvMaps`/`updateEnvVar`/`lookupEnvValue`/`lookupBool`/`resolvePath`/`expandHomePath`（3468-3695） | ~300 |
| `helpers.go` | 纯工具：`asString`/`asInt`/`asBool`/`asStringList`/`validateDomainPattern`/`cloneSectionSchema`/`defaultApply*`/browser engine 工具（3695-4082） | ~400 |

### 3.4 `ui/web/src/features/chat/ChatPage.tsx`（3640 → 主组件 + 8 个文件）

拆分落点：`ui/web/src/features/chat/` 下新建 `panels/` 子目录放面板组件，`lib/` 目录放纯函数。

| 目标文件 | 内容（现状行号区间） | 预估行数 |
|---|---|---|
| `ChatPage.tsx`（保留） | 主组件 `ChatPage`：路由/状态/store 绑定/主 JSX 布局（156-1543） | ~1400 |
| `panels/InspectorTabs.tsx` | `InspectorTabs`（1543-1724） | ~180 |
| `panels/NoteContextBanner.tsx` | `NoteContextBanner`/`noteContextMetadata`/`compactWorkspaceName`（1724-1787） | ~80 |
| `panels/ApprovalPanels.tsx` | `ApprovalBanner`/`ApprovalList`/`permissionRequestTitle`/`permissionRequestSummary`（1787-1906 + 3622-3640） | ~160 |
| `panels/ContextPanels.tsx` | `ContextStatusInline`/`ContextRecallPanel`/`MemoryPreviewSection`/`buildContextStatusSummary`（1906-2324 + 3233-3275） | ~450 |
| `panels/TurnSubagentPanels.tsx` | `TurnList`/`SubagentList`/`LongTaskList`/`SubagentOverview`/`SubagentQuickMeta`/`SubagentActions`/`SubagentReviewPanel`/`AvailableSubagentRoles`/`fileChangeColor`（2324-2823） | ~500 |
| `panels/TimelinePanels.tsx` | `TimelineList`/`TimelineFilters`/`timelineEventTypeLabel`/`turnStatusColor`/`formatTurnError`/`shortTurnId`（2823-3071） | ~250 |
| `lib/timelineUtils.ts` | 纯函数：`appendTimelineEvent`/`timelineEventLabel`/`timelineEventSummary`/`timelineEventFullText`/`subagentTimelineSummary`/`collectSubagentJobs`/`subagentJobToFeedItem`/`pendingSendToFeedItem`/`mergeChronologicalFeedItems`/`mergeSubagentItems`/`subagentStatusSortRank`/`mergeSubagentProgress`/payload helpers/`previewText`/`formatCompactNumber`/`formatTimelineTime`（3071-3640） | ~570 |
| `panels/ContextPopover.tsx` | ctx popover 相关 JSX（若独立） | 视搬迁情况 |

> 注意：`ChatPage.tsx` 主组件从 ~1543 行起还有一部分辅助渲染（1592 起的多个 `return`），搬迁时需逐个确认是否属于上述面板组件。

## 4. 实施顺序（风险从低到高）

每步独立可验证，建议按序执行：

1. **后端 helpers 类**（`backend/helpers.go`、`config/helpers.go`、`config/env.go`）：纯函数无依赖，先搬。验证 `go build ./...`。
2. **config 包**（`doctor.go` → `resolve.go` → `profiles.go` → `values.go`）：单包内移动，无跨包影响。验证 `go test ./internal/core/config/...`。
3. **agent 包**（`subagent_views.go` → `subagent_events.go` → `subagent_policy.go` → `subagent_git.go` → `subagent_tools.go` → `subagent_store.go` → `subagent_run.go` → `subagent_merge.go`）：按"依赖最少→最多"搬。验证 `go test ./internal/agent/...`。
4. **backend 包**（`helpers.go` → `sessionstate.go` → `permissions.go` → `timeline.go` → `attachment.go` → `skills.go` → `packages.go` → `subagents.go` → `longtasks.go` → `commands.go` → `session.go` → `turn.go` → `persist.go`）：核心包最后拆，因为被 httpapi 大量引用。验证 `go test ./internal/services/backend/... ./internal/runtime/httpapi/...`。
5. **前端**（`lib/timelineUtils.ts` → `panels/` 各组件 → 主组件改 import）：先搬纯函数，再搬面板组件，最后主组件瘦身。验证 `npx tsc -b`。

## 5. 验证计划

每次搬迁一个文件后执行：

- 后端：`cd /Users/taiwu.wang/Documents/leader_agent/godex && go build ./... && go test <受影响包>`（镜像真实构建，不跳过测试）
- 前端：`cd ui/web && npx tsc -b`（**不是** `tsc --noEmit`，按项目记忆要求用真实构建命令）
- 全量收尾：`go build ./...` + 相关包 `go test` + `pnpm -C ui/web build`

拆完后抽查：

- `go vet ./...` 无新告警
- `git diff` 统计：改动应为"纯移动"（同函数在旧文件删除、新文件新增，内容逐字一致）
- 用 `git diff --stat` 确认 4 个大文件行数下降、无意外增删

## 6. 风险与规避

| 风险 | 规避 |
|---|---|
| 搬迁时误改逻辑 | 只允许 copy-paste 移动；diff 必须逐字一致；拆完对比 `git show HEAD:oldfile` 校验行数守恒 |
| 同包内符号重名/遗漏 | Go 编译器兜底（编译错即发现）；前端 tsc 兜底 |
| ChatPage 组件有隐藏闭包依赖 | 拆分前先用 grep 确认每个待拆函数只引用 props/import/自包含工具；发现闭包依赖就留在主组件 |
| `sessionState` 与 `Service` 方法分散后难找 | 拆分命名前缀化（如 `session.go` 只放 session 生命周期），配合 lsp document_symbols 导航 |
| 搬迁顺序错误导致编译失败时间变长 | 严格按"依赖最少→最多"；每步先 `go build` 再继续 |
| 测试文件引用旧文件内私有符号 | 同包拆分不影响测试（同包可见性不变）；测试文件不动 |

## 7. 验收标准

- 4 个大文件行数分别降到 ~1500 / ~350 / ~400 / ~1400 以下（主文件只保留核心类型+入口）。
- `go build ./...`、受影响包 `go test`、`npx tsc -b` 全绿。
- 拆分后行为零变化：现有测试全部通过即视为行为等价（纯 move，无重构）。
- 每个新文件职责单一、命名能自解释。

## 8. 不做的事（防止范围蔓延）

- 不改变任何导出 API / 组件 props / 路由 / store 结构。
- 不顺手重构逻辑、不优化性能、不重命名公共符号（除非同文件内部小写符号顺手清理，且确认无引用）。
- 不合并其他小文件进来（只拆这 4 个）。
- 不新增依赖。
