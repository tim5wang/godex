# taskboard 插件设计方案（需求池 #1）

> 状态：Approved（2026-08-27 用户拍板"全按默认"） · 关联：plugin-system-evolution-plan.md（§4 边界 P-A~P-D、§5 路径 A）
> 形态：godex 原生 Go 插件（能力面重写，非 JS 二进制迁移）；前端原生 React 页面（Q2=B）；
> 项目维度走轻量注册表（Q3=B）；MVP 按三期交付（Q1=A）。

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
├── plugin.go        # 插件入口：RegisterHost 回调（tools/routes/cron 可逆注册）
├── ledger.go        # 任务账本（host 权威 JSON，乐观并发 ifVersion）
├── projects.go      # 项目注册表（name+root_dir；内置默认项目=workspace）
├── tools.go         # taskboard_* agent 工具（owner "plugin:taskboard"）
├── execute.go       # 每任务独立会话执行（复用 durable subagent 基建）
└── manage_test.go   # 账本/协议闸/工具 测试

ui/web/src/features/taskboard/
└── TaskBoardPage.tsx  # 五列看板 + 详情抽屉 + 验收操作（M2）
```

- 账本持久化：`<StateDir>/taskboard/ledger.json`（原子写，与 mcp SaveConfig 同款套路）
- 协议闸（代码级，参考 dsh）：`move` 永远到不了 `done`；任务被持有时不可抢；`model`/`execution` 字段对 agent 工具只读
- 执行：`POST /v1/taskboard/cards/{id}/execute` → durable subagent（session 隔离、write_scope 限任务项目 root），开场两条消息同回合送达（插件上下文行 + 卡片内容）；结束回写执行记录

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
}

type Card struct {
    ID         string            `json:"id"`          // t-<ts>
    ProjectID  string            `json:"project_id"`
    Title      string            `json:"title"`
    Description string           `json:"description,omitempty"`
    Prompt     string            `json:"prompt,omitempty"`
    Urgency    string            `json:"urgency"`     // urgent|normal|low
    Status     string            `json:"status"`      // backlog|todo|in_progress|in_review|done
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
`taskboard_move` 到 `in_review` 即终点；`in_review` 可被退回 `todo`（附原因评论）。

## 4. taskboard_* 工具协议（M1 核心集）

| 工具 | 作用 | 协议闸 |
|---|---|---|
| `taskboard_list` | 查板（按项目/状态/紧急度过滤，紧凑摘要） | 只读 |
| `taskboard_get` | 读单卡全文（描述/prompt/评论/清单/执行记录） | 只读 |
| `taskboard_create` | 建卡（project_id/title/urgency/description/prompt/checklist） | — |
| `taskboard_update` | 改标题/描述/prompt/紧急度/清单 | model/execution 只读 |
| `taskboard_move` | 移卡 backlog→todo→in_progress→in_review | **到不了 done**；被持有不可抢 |
| `taskboard_comment_add` | 追加评论（交接/风险/进展） | — |
| `taskboard_delete` | 软删除 | 执行中不可删 |
| `taskboard_checklist` | 清单 add / check（附证据）/ uncheck | — |

（`taskboard_execution_report` 归 M3。）项目边界：agent 认领/移动前须项目匹配
（session workspace ∈ 项目 root_dir 视为同项目；M1 简化为默认项目全通过）。

## 5. HTTP 路由（P-A 首个消费者）

```
GET    /v1/taskboard/projects          # 项目列表
POST   /v1/taskboard/projects          # 建项目
GET    /v1/taskboard/cards             # 查板（?project=&status=&urgency=）
POST   /v1/taskboard/cards             # 建卡（人工）
GET    /v1/taskboard/cards/{id}        # 单卡
PATCH  /v1/taskboard/cards/{id}        # 改卡（含 move/验收 done/退回）
DELETE /v1/taskboard/cards/{id}        # 软删除
POST   /v1/taskboard/cards/{id}/execute   # 手动执行（M1）
GET    /v1/taskboard/events            # SSE 变更流（M2）
```

## 6. 里程碑与验收

**M1 核心闭环**（P-A/P-C/P-D 边界 + 账本 + 工具 + 手动执行）
- [ ] pluginrt：`RegisterRoutes`（可逆）/ 服务注入 getter / `RegisterSchedule`（可逆）+ 测试
- [ ] 账本 CRUD + 乐观并发 + 软删除 + 持久化 round-trip 测试
- [ ] `taskboard_*` 8 工具注册可用；协议闸测试（move 到不了 done / 持有不可抢 / 执行中不可删）
- [ ] 手动执行拉起独立会话，结束回写执行记录；插件卸载后工具/路由全撤销无残留

**M2 可视化**
- [ ] `TaskBoardPage` 五列看板（紧急度色条/筛选/搜索）+ 详情抽屉（评论流/执行记录/清单）
- [ ] 人工验收：✓ done（清单未全勾需二次确认）/ ✗ 退回附原因
- [ ] SSE 实时刷新；nav 注册新入口；i18n 中英文

**M3 协作增强**
- [ ] git worktree 隔离执行（非 git 项目自动降级）+ 一键合并
- [ ] host cron 定时执行（复用 P-D）+ 并发上限
- [ ] `taskboard_execution_report` 结构化报告 + DoD 未勾高亮

## 7. 全局验收（可验证）

1. `go test ./internal/plugins/taskboard/ ./internal/runtime/pluginrt/` 全绿（账本/协议闸/边界可逆性）
2. Web UI 看板可建卡→让 agent 用 `taskboard_*` 认领执行→人工验收 done 全流程走通
3. 插件卸载：路由/工具/调度/执行无残留；既有语音/聊天/设置链路无回归（tsc + web build + 相关包测试）
