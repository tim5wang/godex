# P0-P4 可视化设计文档（UI Visualization Design）

> 状态：Active（P0/P1 已落地，P2 待办）
> 日期：2026-08-11
> 修订日志：2026-08-11 P0（A1+B1）完成、P1（A2+B2+C1）完成；P2（A3+C2）待办

> 目标：把 Phase 0-4 已落地的运行时能力（longtask 动态 DAG、上下文预算、子 agent 通信/迭代、记忆策略）在 Web UI 中变成「看得见、可诊断」的视图。
> 原则：优先复用已有后端 view 结构与前端基建（DiagramBlock/mermaid、ContextPanels、TimelineList），新增 API 尽量小。

---

## 1. 背景与实现盘点

### 1.1 已完成的能力（数据已存在）

| 能力 | 落地位置 | 现有 view / API |
|------|----------|----------------|
| longtask 动态并行 DAG | Phase 2.4 `internal/agent/agentgraph.go` | `LongTaskView.Graph` 已随 longtask 详情暴露 `Nodes`/`Edges`，前后端类型与回归测试均存在 |
| 上下文预算管理 | Phase 2.3 + 4.6 `context_inspector.go` | `GET /sessions/{id}/context-inspector`（token 分层/prefix cache/压缩诊断） |
| 子 agent 双向通信（spawn/send_input/wait/iterate） | Phase 4.1/4.2 | `TurnRecord`、timeline 事件、`SubagentReviewPanel` |
| 角色→bundle 映射 + 子 agent bundle 继承 | Phase 4.3/4.4 | 角色配置 UI（已有部分） |
| 记忆策略模式 + 去重（foldCapture/capTail） | Phase 3.1/3.2 | `MemoryPage`（已有列表） |

### 1.2 已关闭的数据缺口（历史）

- 后端 `longTaskView.Graph *agentGraphView` 已随 `GET /sessions/{id}/longtasks/{workflowID}` 返回；`TestLongTaskViewGraphFieldExposesNodesAndEdges` 固定 nodes/edges 契约。
- 前端 `LongTaskView` 已有 `graph` 字段，`AgentGraphDiagram` 已接入 LongTask 视图。
- 因而 A1 所需 API 扩展已经完成；当前剩余项是 A3 的失败路径过滤、handoff 预览与导出，不是数据缺口。

### 1.3 前端可复用基建

- `DiagramBlock`：mermaid 懒加载渲染 → DAG 图的零依赖方案（也可评估 cytoscape，但优先 mermaid 避免新依赖）。
- `ContextPanels`：已有 token 分层/prefix cache/压缩诊断展示 → 视图 B 在其上增强。
- `TimelineList` + `TurnRecord` → 视图 C 的数据骨架。

---

## 2. 高价值视图选择

从 P0-P4 能力中挑 **3 个**高价值视图（按 ROI 排序）：

| # | 视图 | 解决的问题 | 数据来源 | 新增工作量 |
|---|------|-----------|----------|-----------|
| A | **AgentGraph 动态 DAG 图** | longtask 运行过程黑盒：节点依赖/状态/失败点/手写 scope 不可见 | 后端 `LongTaskView.Graph` + mermaid | A1/A2 已完成；A3 为交互增强 |
| B | **上下文预算仪表盘** | token 分层/压缩历史/按角色预算对比不可见，难以诊断上下文膨胀 | `context-inspector`（已有） | 小（增强 ContextPanels） |
| C | **子 agent 通信/迭代时序视图** | spawn→send_input→wait→iterate review 循环不可见，子 agent 生命周期黑盒 | timeline + `TurnRecord`（已有） | 小-中（1 组件） |

---

## 3. 视图 A：AgentGraph 动态 DAG 图（最高价值）

### 3.1 数据设计（后端）

**方案**：扩展 `GET /sessions/{id}/longtasks/{workflowID}` 响应，在现有 `LongTaskView` 上附加：

```ts
// 前端 LongTaskView 新增字段
graph?: {
  nodes: Array<{
    id: string;
    node_type: string;      // llm_task | subagent_task | tool_call | user_input | merge_point
    title?: string;
    status: string;         // pending | running | completed | failed | blocked
    agent_type?: string;
    write_scope?: string[];
    job_id?: string;
    attempt?: number;
    verdict?: string;
    handoff_ref?: string;
  }>;
  edges: Array<{
    id: string;
    edge_type: string;      // data_dependency | control_flow | handoff
    from: string;
    to: string;
    status?: string;
    verdict?: string;
  }>;
};
```

后端改动极小：`longTaskViewForState` 组装时把已有的 `workflowNodeView` / `agentGraphEdgeView` 直接透出（数据已存在，只是序列化字段）。

### 3.2 渲染方案（前端）

- **首选 mermaid flowchart**：新增 `AgentGraphDiagram` 组件，把 nodes/edges 编译为 mermaid 源码，复用 `DiagramBlock` 的懒加载渲染管线。零新依赖。
- **降级**：mermaid 加载失败时显示结构化表格（节点 id/类型/状态/verdict），保证诊断可用。
- **节点着色约定**（映射 status → mermaid classDef）：
  - `pending` 灰 / `running` 蓝 / `completed` 绿 / `failed` 红 / `blocked` 橙
  - 边用实线 `data_dependency`、虚线 `control_flow`、带标签 `handoff`
- **交互**：
  - 点击节点 → 右侧/抽屉显示详情（write_scope、attempt、verdict、handoff_ref、job_id）
  - 失败节点自动带红圈标记，支持「只看失败路径」过滤开关

### 3.3 实施分期

- **A1（P0）✅**：后端 `longTaskView` 附加 `Graph *agentGraphView`（复用 `agentGraphViewFromState`，含 nodes + 运行时 edges with to/status/verdict）+ 前端 `AgentGraphDiagram` 组件（mermaid 渲染、节点状态着色、失败降级表格），已接入 `LongTaskList` Collapse
- **A2（P1）✅**：运行中轮询（`longTasksQuery` 有 running/pending 时每 3s `refetchInterval`）+ 点击节点详情抽屉（SVG 事件委托解析 mermaid g 节点 id + 兜底节点标签列表，Drawer 展示 write_scope/attempt/verdict/handoff_ref/job_id）
- **A3（P2）**：失败路径过滤、handoff 内容预览、导出 mermaid 源码

---

## 4. 视图 B：上下文预算仪表盘

### 4.1 数据来源（完全复用）

`GET /sessions/{id}/context-inspector` 已返回：
- token 分层（系统/记忆/工具/历史/当前…）
- prefix cache 命中率
- 压缩诊断（suggest_compact、pre/post compaction、latency、mode）

### 4.2 可视化增强（增强现有 ContextPanels）

- **分层堆叠条**：各层 token 占比横向堆叠条 + 数值 + 阈值警戒色（接近预算上限变橙/红）
- **压缩历史时间线**：在 InspectorTabs 内新增「Compaction 历史」页签，按时间列出每次压缩（mode/latency/前后 token），复用 timeline 数据
- **按角色预算对比**（Phase 4.6 能力）：若有子 agent 参与，展示各角色 token 预算使用率对比条

### 4.3 实施分期

- **B1（P0）✅**：ContextPanels 新增 `TokenBreakdownBar` 分层堆叠条（各层占比 + 颜色图例，零 token 层隐藏）+ 阈值警戒色 Tag（budget ≥85% 橙 / ≥100% 红）
- **B2（P1）✅**：InspectorTabs 新增「Compaction 历史」页签（`CompactionHistoryPanel` 从 timeline `snapshot_ready` compacted 事件过滤，零新 API）；按角色预算对比——后端 `DurableSubagentJobView` 透出 `ContextBudget`，前端 `FeedItem`/`DurableSubagentJob` 加 `context_budget`，SubagentQuickMeta 显示 budget Tag
- **B3（P2，可选）**：按角色预算使用率对比条（需后端记录各角色实际 token 用量）

---

## 5. 视图 C：子 agent 通信/迭代时序视图

### 5.1 数据来源（复用现有）

- `TimelineList` 事件流（spawn / send_input / wait / review / iterate 事件）
- `TurnRecord`（sender/status/phase/recovery_hint）
- `SubagentReviewPanel`（已展示 review 循环结果）

### 5.2 可视化设计

- **时序泳道图**：横轴时间、纵轴 agent 角色（orchestrator / worker / reviewer…），事件点带类型图标（spawn、send_input、review 通过/驳回、iterate 重试）。
- **交互**：点击事件点 → 展开该事件的详情（工具调用、handoff、review 结果）；驳回事件红色标记，可筛选「只看驳回/重试」。
- **实现**：轻量自绘（绝对定位 div + CSS）即可，不引入图表库；数据来自已有 timeline。

### 5.3 实施分期

- **C1（P1）✅**：`SubagentTimelinePanel` 泳道时序图（每条 lane 一个角色，x 轴时间，事件点 spawn/send_input/review/iterate 分类着色，点击 Popover 详情，issues/retries 筛选），接入 InspectorTabs「Subagent timeline」页签
- **C2（P2）**：驳回/重试筛选强化 + 事件详情展开
- **C2（P2）**：事件详情展开 + 驳回/重试筛选

---

## 6. 总分期与验收

| 阶段 | 内容 | 验收标准 | 状态 |
|------|------|----------|------|
| P0 | A1 + B1 | longtask 详情页出现 DAG 图（节点/边/状态着色正确）；ContextPanels 出现分层堆叠条与警戒色 | ✅ 完成（2026-08-11）|
| P1 | A2 + B2 + C1 | DAG 实时刷新+节点详情；Compaction 历史页签；子 agent 泳道图可读 | ✅ 完成（2026-08-11）|
| P2 | A3 + C2 | 失败路径过滤、mermaid 导出；驳回/重试筛选 | ⏳ |

**通用验收**：
1. 全部新组件过 `tsc -b`（真实构建命令，非 `--noEmit`）
2. 无新增运行时依赖（mermaid 已存在；不引 cytoscape/d3）
3. 后端改动过 `go test ./internal/...` 相关包 + 新增 graph 字段序列化测试
4. 空数据/加载失败均有降级 UI（表格/Empty）

---

## 7. 风险与备注

- **API 变更**：视图 A 需要给 `LongTaskView` 加 `graph` 字段——向后兼容（omitempty），旧前端不受影响。
- **mermaid 性能**：节点数 > 50 时考虑分页/折叠子图（workflow 节点数通常 < 30，风险低）。
- **与 TUI 的关系**：本设计仅覆盖 Web UI；TUI 后续可复用同一后端 graph 字段做文本化 DAG 渲染（不在本期范围）。
