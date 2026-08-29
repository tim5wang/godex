# taskboard 插件设计方案（需求池 #1）

> 状态：Approved（2026-08-27 用户拍板"全按默认"） · 关联：plugin-system-evolution-plan.md（§4 边界 P-A~P-D、§5 路径 A）
> 形态：godex 原生 Go 插件（能力面重写，非 JS 二进制迁移）；前端原生 React 页面（Q2=B）；
> 项目维度走轻量注册表（Q3=B）；MVP 按三期交付（Q1=A）。
>
> 进度：**M1（核心闭环）✅ → M2（看板）✅ → M2.5（执行进度跳转）✅ 已完成；M3（协作增强）未开始**。
> 本文档随之从"设计"同步为"设计与实现对照"；下述 `## ` 内如与代码冲突，以 `internal/plugins/taskboard/` 为准。

## 1. 已确认决策

| 分叉 | 拍板 | 说明 |
|---|---|---|
| 移植路径 | A：能力面 Go 重写 | 复用 godex 执行器；无 Node 依赖；长期主义 |
| MVP 切期 | 三期：M1 闭环 → M2 看板 → M3 增强 | 模板/导入导出/diff 查看器**不做** |
| UI 形态 | B：原生 React `TaskBoardPage` | 先质量后"UI 插件化"；iframe 桥留待后续用别的轻场景验证 |
| 项目维度 | B：轻量项目注册表 | name + root_dir；M1 内置"默认项目"指向 workspace |

## 2. 架构

```
internal/plugins/taskboard/        # host 半（Go，pluginrt 插件）
├── plugin.go        # 插件入口：RegisterEffect/RegisterRoutes 可逆注册
├── ledger.go        # 任务账本（host 权威 JSON，乐观并发 ifVersion；协议闸/状态机）
├── types.go         # Project/ChecklistItem/Execution/HostRef/Card 数据模型
├── tools.go         # taskboard 单工具（action 枚举分发；保留 NewTaskboardTools 兼容入口）
├── routes.go        # /v1/taskboard HTTP 面（人工 PATCH/complete/reject/checklist）
├── ledger_test.go / tools_test.go / plugin_test.go   # 账本/协议闸/工具/边界 测试

internal/services/backend/taskboard_executor.go   # 执行适配：hostSession + 提交卡片进入主会话（M1-d）

ui/web/src/features/taskboard/
└── TaskBoardPage.tsx  # 五列看板 + 详情抽屉 + 验收/退回操作（M2）
ui/web/src/lib/api.ts   # /v1/taskboard/* 前端请求封装
```

- 账本持久化：`<StateDir>/taskboard/ledger.json`（原子写 tmp+rename，与 mcp SaveConfig 同款套路）
- 协议闸（代码级，参考 dsh）：`move` 永远到不了 `done`；任务被持有时，非持有者不可抢、但 **human 作为 superuser 可越过持有者锁** 推进/清空（解决遗留会话占用卡片的死锁）；存在 running execution 时不可删
- 执行：`POST /v1/taskboard/cards/{id}/execute` → 找到最近活跃 host 会话，`StartExecution` 记 running + holder，再 `SubmitAsync` 把卡片指令拉进该会话主对话（agent 认领/执行全程可见，无子 agent 黑盒）；结束后由 `closeRunningExecutions` 在状态离开 `in_progress` 时收尾
- 工具身份：`taskboard` agent 工具移动卡片时用**当前会话 id** 作 actor（`SessionIDFromContext`），与 `StartExecution` 记录的 holder（=宿主会话 id）一致；无会话上下文回退 `agentActor="agent"`

## 3. 数据模型

```go
type Project struct {
    ID      string `json:"id"`
    Name    string `json:"name"`
    RootDir string `json:"root_dir"`
    BuiltIn bool   `json:"built_in,omitempty"` // 默认项目（=workspace）不可删
}

type ChecklistItem struct {
    Text     string `json:"text"`
    Done     bool   `json:"done"`
    Evidence string `json:"evidence,omitempty"` // 勾选附证据
}

type Execution struct {
    ID        string    `json:"id"`
    SessionID string    `json:"session_id"`
    Status    string    `json:"status"` // running|completed|failed|cancelled
    StartedAt time.Time `json:"started_at"`
    EndedAt   time.Time `json:"ended_at,omitempty"`
    Summary   string    `json:"summary,omitempty"`
    // Host 是宿主执行会话的定位（UI 跳转进度用）；ProjectDir 参与身份重建，缺了会开成新会话
    Host *HostRef `json:"host,omitempty"`
    // JobSessionID 是执行会话自身 id（异步回填），为主跳转目标，Host 仅兜底
    JobSessionID string `json:"job_session_id,omitempty"`
}

type HostRef struct {   // 定位宿主执行会话，供 UI 跳到其实时进度
    SessionID  string `json:"session_id"`
    Channel    string `json:"channel,omitempty"`
    Key        string `json:"key,omitempty"`
    UserID     string `json:"user_id,omitempty"`
    ProjectDir string `json:"project_dir,omitempty"`
}

type Card struct {
    ID         string            `json:"id"`          // t-<ts>
    ProjectID  string            `json:"project_id"`
    Title      string            `json:"title"`
    Description string           `json:"description,omitempty"`
    Prompt     string            `json:"prompt,omitempty"`
    Urgency    string            `json:"urgency"`     // urgent|normal|low
    Status     string            `json:"status"`      // backlog|todo|in_progress|in_review|done
    // Holder 记录当前持有者（执行会话 id 或认领 agent）；被持有的卡不可被其它角色抢走
    Holder      string            `json:"holder,omitempty"`
    Blocked    bool              `json:"blocked,omitempty"`
    Checklist  []ChecklistItem   `json:"checklist,omitempty"`
    Comments   []Comment         `json:"comments,omitempty"`
    Executions []Execution       `json:"executions,omitempty"`
    Version    int               `json:"version"`     // 乐观并发
    CreatedBy  string            `json:"created_by"`
    UpdatedBy  string            `json:"updated_by"`
    CreatedAt  time.Time         `json:"created_at"`
    UpdatedAt  time.Time         `json:"updated_at"`
    Deleted    bool              `json:"deleted,omitempty"` // 软删除
}
```

状态机：`backlog→todo→in_progress→in_review→done`。`done` 仅人工（HTTP/UI）可置；
`taskboard` 工具 move 到 `in_review` 即终点；`in_review` 可被退回 `todo`（附原因评论）。

协议闸补充（deadlock 修复，2026-08-28）：
- **持有者身份 = 执行会话 id**：`StartExecution` 把 `Holder` 记为宿主会话 id，agent 工具 move 时用 `SessionIDFromContext(ctx)` 的当前会话 id 作 actor，二者一致才可推进自己持有的卡；
- **human 是 superuser**：`actor=="human"` 时可越过 holder 锁推进并清空持有者（解决遗留/已死会话占用导致无法手动验收）；
- **离开 in_progress 自动收尾**：move→in_review 或 complete→done 时 `closeRunningExecutions` 把所有 running execution 置为 completed，避免永久 running 导致无法再次执行/删除。

## 4. taskboard 工具协议（M1 核心集，方式 B 合并为单工具）

> 2026-08-28 起，原 8 个 `taskboard_*` 工具合并为**单个 `taskboard` 工具**，以 `action` 参数枚举分发
> （惯例与 background 工具、人工 PATCH 的 action 分发一致），协议闸仍在账本层。

| action | 作用 | 协议闸 |
|---|---|---|
| `list` | 查板（项目/状态/紧急度过滤，紧凑摘要） | 只读 |
| `get` | 读单卡全文（描述/prompt/评论/清单/执行记录） | 只读 |
| `create` | 建卡（project_id/title/urgency/description/prompt/checklist） | — |
| `update` | 改标题/描述/prompt/紧急度/清单 | model/execution 只读 |
| `move` | 移卡 backlog→todo→in_progress→in_review | **到不了 done**；被持有不可抢；human 可越锁 |
| `comment_add` | 追加评论（交接/风险/进展） | — |
| `delete` | 软删除 | 执行中不可删 |
| `checklist` | 清单 add / check（附证据）/ uncheck | — |

（`taskboard_execution_report` 归 M3。）项目边界：agent 认领/移动前须项目匹配。
备注：写操作强制 `version`（乐观并发）；`action=move` 的 actor 用当前会话 id（见上）。

## 5. HTTP 路由（P-A 首个消费者）

```
GET    /v1/taskboard/projects          # 项目列表
POST   /v1/taskboard/projects          # 建项目
GET    /v1/taskboard/cards             # 查板（?project=&status=&urgency=）
POST   /v1/taskboard/cards             # 建卡（人工）
GET    /v1/taskboard/cards/{id}        # 单卡
PATCH  /v1/taskboard/cards/{id}        # 改卡（action=update/move/complete/reject/checklist；actor 默认 human）
DELETE /v1/taskboard/cards/{id}        # 软删除
POST   /v1/taskboard/cards/{id}/execute   # 手动执行（M1）
```

> 说明：`GET /v1/taskboard/events`（SSE）在 M2 未落地为后端路由，前端 `TaskBoardPage`
> 改用 `refetchInterval: 15s` 轮询 + 变更后 `invalidateQueries` 刷新（见 `ui/web/src/features/taskboard/TaskBoardPage.tsx`）；
> SSE 变更流归入 M3 候选。

## 6. 里程碑与验收

**M1 核心闭环**（P-A/P-C/P-D 边界 + 账本 + 工具 + 手动执行）✅ 完成
- [x] pluginrt：`RegisterEffect`/`RegisterRoutes`（可逆）+ 服务注入 getter + 测试
- [x] 账本 CRUD + 乐观并发 + 软删除 + 持久化 round-trip 测试
- [x] `taskboard` 单工具注册可用；协议闸测试（move 到不了 done / 持有不可抢 / 执行中不可删）
- [x] 手动执行拉起宿主会话，投递卡片指令进主对话；插件卸载后工具/路由全撤销无残留

**M2 可视化** ✅ 完成
- [x] `TaskBoardPage` 五列看板（紧急度色条/筛选/搜索）+ 详情抽屉（评论流/执行记录/清单）
- [x] 人工验收：✓ done（清单未全勾需二次确认）/ ✗ 退回附原因（PATCH complete/reject）
- [x] 刷新：前端 15s 轮询 + 变更后 invalidateQueries；nav 注册新入口；i18n 中英文

**M2.5 进度跳转** ✅ 完成
- [x] 执行记录「查看进度」按 session_id 解析完整 locator → `buildChatRouteForSession` 跳到宿主会话（`ad9b722` 起多轮修复：完整身份编码、HostRef 补 project_dir、跳执行会话本体）

**M3 协作增强**（未开始）
- [ ] git worktree 隔离执行（非 git 项目自动降级）+ 一键合并
- [ ] host cron 定时执行（复用 P-D）+ 并发上限
- [ ] `taskboard_execution_report` 结构化报告 + DoD 未勾高亮
- [ ] SSE 变更流（`GET /v1/taskboard/events`）替代前端轮询
- [x] **多智能体协作优化设计已落盘** → 见 `docs/taskboard-collaboration-design.md`（M3.5 前置：上下文传递 research + 并行冲突治理四道闸门 + 经验回流）

## 7. 全局验收（可验证）

1. `go test ./internal/plugins/taskboard/ ./internal/runtime/pluginrt/` 全绿（账本/协议闸/边界可逆性）
2. Web UI 看板可建卡→让 agent 用 `taskboard` 认领执行→人工验收 done 全流程走通
3. 插件卸载：路由/工具/调度/执行无残留；既有语音/聊天/设置链路无回归（tsc + web build + 相关包测试）
4. 卡死锁修复：被执行会话持有的 `in_progress` 卡，human 可越锁推进；离开 `in_progress` 自动终结 running execution（回归：`TestHolderCanAdvanceOwnCardBySessionID` / `TestHumanCanUnstickHeldCard`）
