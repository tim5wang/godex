# TUI Scrollback Streaming Refactor

**Date:**2026-06-11
**Status:** Phase1+2 complete, commit `7d23d1b`

##目标

解决 godex TUI 长对话卡顿问题：bubbletea standardRenderer60fps 全屏 diff，CPU随历史线性增长。

借鉴现有 `internal/runtime/repl` 的 REPL模式：历史直接打印到 stdout、输入框自管、状态条 ANSI 行内刷新。

##关键观察

1. **现有 `repl` 包已经做了90%**：用 lineEditor读输入、用 `fmt.Fprintf(stdout, ...)` 直打事件流，零 bubbletea。
2. **现有 `tui` 包是8000行的全屏渲染器**：覆盖 workbench、permission modal、longtask详情、task center 等 UI。
3. **本次改造的本质**：把 `tui`拆成两层 ——

 - **streaming layer**（沿用 repl思路）：把事件直打 stdout、底部 lineEditor + status line。
 - **modal layer**（保留 bubbletea）：workbench / permission / longtask 等仍走全屏 UI，按需 suspend 流式层。

##架构

```
+-----------------------------------------------------------+
| Streaming Layer |
| -启动时打印 header、model、workspace |
| -订阅 backend events -> fmt.Fprintln(stdout, ...) |
| - 用户输入 -> lineEditor (复用 repl/editor.go) |
| -底部状态条：ANSI \r + \x1b[K 行内刷新 |
| - 流式期间禁用输入框 (与原 REPL 一致) |
+-----------------------------------------------------------+
| Modal Layer (legacy) |
| - 用 bubbletea 全屏 UI 处理 workbench/permission/longtask |
| -退出 modal 后回到 streaming layer |
| - 大部分时间不运行，不影响性能 |
+-----------------------------------------------------------+
```

##实施阶段

### Phase1:抽出 Streaming Layer — 完成

- 新建 `internal/tui/streaming/` 包 (streaming.go +17 个测试)
- 实现 `Session` 类型：直接订阅事件、fmt打印到底
-复用 `repl/editor.go` 的 `lineEditor`
- 实现 `StatusBar`：底部状态行 ANSI刷新
- 与 `Session.Run()`集成：移除 `tea.WithAltScreen()`，保留工作台/权限弹窗走旧路径

### Phase2:合并到 main入口 — 完成

- `cmd/godex/main.go`: 新增 `--tui-mode=scrollback`标志
- 默认走 legacy TUI（向后兼容）；streaming模式为 opt-in
- 帮助文本已更新
-8 个 flag parsing tests

### Phase3:优化 streaming模式（部分完成）

- ✅ 状态栏固定到输入框下方，ANSI行内刷新
- ✅ 复用 legacy TUI 的 baseStatusLabel / contextUsageText / permissionBlocker 等渲染逻辑
- ✅ 紧凑布局：`Ready · Input · MiniMax-M3 · 13k/256k 5% · msgs 18`
- ✅ 模型调用计数（model_request runner phase 去重）
- ✅ SIGWINCH 监听，resize 后状态栏自动 ellipsize
- ⏳ glamour风格的 markdown渲染（streaming暂以原始 markdown 输出）
- ⏳ 考虑把 streaming设为默认（需调研用户习惯）

### Phase4:移除 legacy bubbletea（暂缓）

- 等 Phase1-3稳定后再考虑完全废弃
-风险：长 task detail 页等复杂 UI短期无法复刻

## 当前状态（2026-06-11）

提交 `7d23d1b`实现了 Phase1+2。可以通过 `godex --tui-mode=scrollback`启动 streaming模式。

```
🤖 GoDex · streaming mode
 session local:default
 workspace /Users/taiwu.wang/Documents/leader_agent/godex
 model MiniMax-M3
```

- legacy TUI1625 行测试仍然通过
- streaming 包17 个新测试全部通过
- cmd/godex 帮助文本已更新

##收益（已实现）

- 流式输出延迟从60fps 节流降到即时
-终端 scrollback天然处理历史回滚
- 不依赖 alt-screen，与 ssh/web terminal兼容性更好
- 重渲染范围从整个屏幕缩到 status 行单行更新

##收益（未实现 - Phase3）

- 长对话 CPU占用从 O(N)降到 O(1)（legacy TUI仍存在）
- 完全移除 bubbletea依赖

##风险

-终端状态管理：lineEditor 与 statusBar共享屏幕底行，需谨慎处理 ANSI序列
- streaming模式下 markdown暂以原始文本渲染（用户能看懂但不是美化版）
-终端 resize 时状态行可能错位（Phase3 处理）
