# LongTask 优化 PRD

> 创建日期: 2026-05-27
> 状态: 进行中

## 背景

LongTask 是 workflow runtime 之上的 story 编排层，将用户故事编译为 workflow 节点并驱动执行。
当前实现功能完整但存在代码组织、健壮性和功能缺失问题。

## 优化任务列表

### Phase 1 — 代码质量 (P3)

- [x] **T1: 拆分 longtask.go 大文件**
  - 将类型定义、create、run、finalize、merge 逻辑拆分为独立文件
  - 目标: 每个文件 < 300 行，职责单一

- [x] **T2: 抽取 storyCompiler 接口解耦 workflow**
  - story 编译逻辑从 longtask tool handler 中抽出为独立接口
  - 便于测试和后续扩展

### Phase 2 — 可靠性 (P2)

- [x] **T3: 支持 per-story handoff_policy 透传**
  - story spec 中新增可选 handoff_policy/handoff_max_bytes 字段
  - 编译 workflow node 时透传到 node.HandoffPolicy/HandoffMaxBytes

- [x] **T4: quality_check 加 timeout_ms 防 hang**
  - longtask spec 新增 validation_timeout_ms 字段（默认 60000）
  - finalize 时每条 quality_check 带超时执行

### Phase 3 — 可靠性 (P1)

- [x] **T5: finalize 后 worktree GC 防磁盘泄漏**
  - story finalize（pass 或 error）后标记 subagent worktree 可回收
  - 新增 gcLongTaskWorktrees 批量清理接口

### Phase 4 — 功能增强 (P0)

- [x] **T6: run action 支持异步模式**
  - run 新增 async 参数，async=true 时立即返回 run_id
  - 新增 run_status action 查询异步 run 进度
  - CLI 新增 --async flag

## 验收标准

- 每个任务独立可测试、可 commit
- 所有现有测试继续通过
- 新增功能有对应测试覆盖
