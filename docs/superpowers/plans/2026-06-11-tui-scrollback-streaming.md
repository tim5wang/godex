# TUI Scrollback Streaming Refactor

**Date:**2026-06-11
**Status:** Implementation in progress

##目标

解决 godex TUI 长对话卡顿问题：bubbletea standardRenderer60fps 全屏 diff，CPU随历史线性增长。

借鉴现有 `internal/runtime/repl` 的 REPL模式：历史直接打印到 stdout、输入框自管、状态条 ANSI 行内刷新。

##关键观察

1. **现有 `repl` 包已经做了90%**：用 lineEditor读输入、用 `fmt.Fprintf(stdout, ...)` 直打事件流，零 bubbletea。
2. **现有 `tui` 包是8000行的全屏渲染器**：覆盖 workbench、permission modal、longtask详情、task center 等 UI。
3. **本次改造的本质**：把 `tui`拆成两层——
 - **streaming layer**（沿用 repl思路）：把事件直打 stdout、底部 lineEditor + status line。
 - **modal layer**（保留 bubbletea）：workbench / permission / longtask 等仍走全屏 UI，按需 suspend 流式层。

##架构

```
+-----------------------------------------------------------+
| Streaming Layer |
| -启动时打印 header、model、workspace |
| -订阅 backend events → fmt.Fprintln(stdout, ...) |
| - 用户输入 → lineEditor (复用 repl/editor.go) |
| -底部状态条：ANSI \r + \x1b[K 行内刷新 |
| - 流式期间禁用输入框 (与原 REPL 一致) |
+-----------------------------------------------------------+
| Modal Layer |
| - 用 bubbletea 全屏 UI 处理 workbench/permission/longtask |
| -退出 modal 后回到 streaming layer |
| - 大部分时间不运行，不影响性能 |
+-----------------------------------------------------------+
```

##实施阶段

### Phase1:抽出 Streaming Layer（本次提交）

- 新建 `internal/tui/streaming/` 包
- 实现 `Session` 类型：直接订阅事件、fmt打印到底
-复用 `repl/editor.go` 的 `lineEditor`
- 实现 `StatusBar`：底部状态行 ANSI刷新
- 与 `Session.Run()`集成：移除 `tea.WithAltScreen()`，保留工作台/权限弹窗走旧路径

### Phase2:合并到 main入口

- `cmd/godex/main.go`: `RunTUI`切换为 streaming layer +旧的 workbench入口作为 fallback
- 默认走 streaming；只有用户显式调用 `/workbench` 等命令才进入旧 UI

### Phase3:移除 bubbletea（暂缓）

- 等 Phase1/2稳定后再考虑完全废弃
-风险：长 task detail 页等复杂 UI短期无法复刻

##收益（Phase1+2 后）

- 长对话 CPU占用从 O(N)降到 O(1)
- 流式输出延迟从60fps 节流降到即时
-终端 scrollback天然处理历史回滚
- 不依赖 alt-screen，与 ssh/web terminal兼容性更好

##风险

-终端状态管理：lineEditor 与 statusBar共享屏幕底行，需谨慎处理 ANSI序列
-现有1625 行 `tui_test.go` 大多假设 `m.View()`/`tea.WindowSizeMsg`，迁移成本高
- 本次只做 Phase1+2增量，保留旧 `model.go`/`view.go`/`update.go` 代码作为 modal 层
