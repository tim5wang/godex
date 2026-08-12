# TUI 卡死问题排查与修复 (2026-05-27)

> 状态：Historical（历史修复记录，问题已解决，保留仅作参考）

## 问题描述
GoDex TUI 下经常出现卡死：执行 bash / approve 权限 / 上下文压缩后，
无法发送消息、日志不变化，只能 Ctrl+C 重开。

## 修复的 5 个 Bug

### 1. listenBashStream 返回 nil → 永久卡死
- commands.go: channel 关闭但无 final 事件时返回 nil，bubbletea 静默丢弃
- 修复: 返回合成 bashFinishedMsg

### 2. permissionFinishedMsg 竞态 → 永久卡死
- update.go: ApprovePermission 同步执行 resume turn，EventTurnCompleted 先到达清 working，
  permissionFinishedMsg 后到达又设 working=true → 永远无法清除
- 修复: 不再在 permissionFinishedMsg 中调 startWorking，由 snapshotLoadedMsg 决定

### 3. submitFinishedMsg 成功路径不清除 working
- update.go: 成功时依赖 EventTurnCompleted，事件丢失则 working 永真
- 修复: 成功路径也调 stopWorking()

### 4. commandFinishedMsg 同上
- 修复: 无条件调 stopWorking()

### 5. snapshotLoadedMsg 不处理 Running=false 竞态
- update.go: 压缩触发的 snapshot 在 turn 完成后到达，startWorking 被调用但无人清除
- 修复: Running=false 时调 stopWorking()

## 修复文件
- internal/tui/commands.go: listenBashStream 修复
- internal/tui/update.go: submitFinishedMsg, commandFinishedMsg, permissionFinishedMsg, snapshotLoadedMsg 修复

## 验证
- go build ./... 通过
- go test ./internal/tui/... 全部 11 个测试通过
