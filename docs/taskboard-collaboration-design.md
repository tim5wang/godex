# taskboard 多智能体协作优化设计（M3.5 前置）

> 状态：Draft（待评审）｜ 关联：taskboard-plugin-design.md（M3 协作增强）、docs/tools_issues.md（改进建议来源）
> 日期：2026-08-29
> 起因：PJM 会话实测暴露两个协作缺陷——① PJM 花精力调研的成果没有有效传给 coder，coder 重新定位；② PJM 调度未感知并发任务的文件/语义冲突（本次 edit_file 卡与 bash 卡同碰 `internal/platform/tooling/tooling.go`）。

## 1. 形态与目标

把 taskboard 从「任务分发器 + 状态机」升级为「多智能体协作调度器」，解决两个根因：

| 缺陷 | 现象 | 目标 |
|---|---|---|
| 上下文传递断层 | PJM 调研结论是"结论文本"，coder 领卡后重新 grep/read 复核，重复劳动 | coder 只验证开放点，不重复全量定位 |
| 无卡间冲突感知 | 现有 Holder/版本锁只保护"同一张卡"，不同卡之间文件/语义冲突后知后觉 | 冲突在派工前被拦截，或执行中被观测 |

不引入新实体；全部在现有 Card 模型 + ledger + executor 上扩展。

## 2. 方案 A：上下文传递（解决 coder 重新定位）

### 2.1 卡片新增 `research` 结构化字段
- PJM 调研时填：**已验证事实清单 + 关键落点（文件:行号）+ 排除路径**。
- executor 注入 prompt 时明确分区：
  ```
  ### 已由 PJM 验证（不必重复排查）
  <research.facts>
  ### 执行时需自行验证的开放点
  <research.open_questions>
  ```

### 2.2 角色边界：调研显式化、只做一次
- planner 模板卡承担「调研+方案+影响面声明」，产出物 = research + touched_paths；
- PJM 据此派 coder 卡（coder 卡直接引用 planner 卡的 research），调研落成资产、再分发，不重复。

## 3. 方案 B：并行冲突治理（本设计重点）

### 3.1 现状事实
- Card 已有：`ProjectID / TemplateID / Holder / Blocked / Checklist / Executions / Version` —— **全部只保护同卡并发**。
- ledger 已有：`ErrVersionConflict` 乐观锁、holder 锁、human 越锁。
- **缺口：无任何"不同卡之间"冲突感知。**

### 3.2 冲突类型谱系

| 类型 | 例子 | 现有机制 |
|---|---|---|
| 文件/代码重叠 | 两卡都改 `tooling.go` | ❌ 无 |
| 包级耦合 | 一卡加新工具、一卡重构同包文件 | ❌ 无 |
| API 契约冲突 | A 给 Card 加字段、B 重排字段/改 JSON tag | ❌ 无 |
| 资源冲突 | 同端口 / 同 MCP server / 同 DB 迁移 | ❌ 无 |
| 依赖时序 | B 依赖 A 新加的函数，A 未合 | ❌ 无（依赖仅"阻塞"，无自动排序） |
| 语义冲突 | 一卡删 WorkflowsPage、一卡还在其上加功能 | ❌ 无 |

### 3.3 四道闸门

**闸门 1 · 静态声明（建卡时）** —— 卡片加 `touched_paths: string[]`
- 粒度**包级**起步（比文件级稳、比目录级精确）。
- PJM 建卡时声明影响面（如 `["internal/platform/tooling", "internal/tools"]`）。

**闸门 2 · 派工拦截（dispatch 时）** —— 比对在跑卡占用
- ledger 维护"执行中卡的 TouchedPaths 占用表"；dispatch 新卡查交集。
- 命中 → 阻塞派工 + 提示："与 exec-xxx（title）重叠 `internal/platform/tooling`，建议串行或拆分"。
- 本次 edit_file/bash 场景若生效：第二张卡根本派不出去。

**闸门 3 · 动态观测（执行中）** —— 实际改动文件上报（兜底）
- 声明会漏/会错；执行会话定期上报真实 diff（git status / 写操作记录）。
- 发现动态重叠 → 实时告警 + 可暂停后进卡。

**闸门 4 · 合并闸门（进 in_review 前）** —— 结构性冲突兜底
- coder 提交前跑 merge 预检（目标分支 diff 交集）；有冲突 → 卡在 in_review 附"合并冲突报告"，交 reviewer/PJM 裁决。

### 3.4 配套处置策略
- **串行**：同包卡排队，前卡进 in_review 才派后卡。
- **分区**：按包 ownership 分派，每执行会话只管自己包（prompt 硬约束写权限）。
- **依赖拓扑**：`depends_on` 字段 + 自动排序，A 完成才派 B（不靠 PJM 记性）。
- **互斥标记**：`conflicts_with` 显式声明不兼容卡（即使文件不重叠，语义也要互斥）。

### 3.5 落地分级

| 级别 | 内容 | 改动面 |
|---|---|---|
| P0 快赢 | 闸门 1+2：Card.touched_paths + dispatch 前比对告警 | types + ledger + executor + 前端建卡表单多选（小） |
| P1 | 闸门 3 动态观测 + conflicts_with 互斥 | ledger 上报 + 告警（中） |
| P2 | depends_on 依赖拓扑 + 自动串行排队 | ledger 调度（中） |
| P3 | 闸门 4 合并预检 + git 冲突报告 | executor + 报告 schema（中） |

## 4. 方案 C：经验回流（长期复利）

- coder 收尾报告强制填结构化"经验收获"（踩坑/边界/可复用套路）。
- dispatch 新卡时扫描**同项目/同 touched_paths/同标签的历史已完成卡**，把执行报告/评论作为"参考经验"注入新卡上下文。
- 可复用经验由 PJM 一键转存 memory（work_fact/work_method）。

## 5. 与既有设计的关系

- 挂靠 taskboard-plugin-design.md **M3 协作增强**（未开始），作为其第一个落地子项（可命名为 M3.5）。
- 复用已有：`executionPrompt` 注入链（扩 research 分区）、ledger 乐观锁（扩展占用表）、Holder 锁语义（扩展到卡间）。
- 不改变：Card 基本字段语义、人工验收流程、模板分派机制。

## 6. 验收（可验证）

1. 建卡可填 `research` 与 `touched_paths`；executor 注入 prompt 含"已验证/待验证"分区。
2. dispatch 时对在跑卡 touched_paths 交集命中 → 返回阻塞错误 + 提示重叠卡。
3. 回归：单卡执行、同卡并发写、human 越锁、离开 in_progress 自动终结 running execution 均不受影响。
4. `go test ./internal/plugins/taskboard/` + `tsc -b` + web build 全绿。

## 7. 待定

- touched_paths 粒度确认：包级（默认） vs 文件级。
- 动态观测（闸门 3）的上报通道：复用 SSE 变更流（M3 已有计划）还是独立轮询。
- 依赖拓扑（P2）是否需要前端可视化。
