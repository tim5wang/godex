# Memory Index

- [TDD 开发流程规范](tdd_开发流程规范.md) - workflow - TDD 开发流程规范：设计 → 写测试 → 实现 → 测试 → 修复循环，直到全部测试通过
- [TUI 卡死 Bug 修复 (2026-05-27)](tui_卡死_bug_修复_2026_05_27.md) - workflow - TUI 卡死问题排查与修复 - 修复了 listenBashStream nil return、permissionFinishedMsg 竞态、submitFinishedMsg/commandFinishedMsg 不清 working、snapshotLoadedMsg Running=false 竞态等 5 个 bug
- [Use subagent for web research tasks](use_subagent_for_web_research_tasks.md) - workflow - For web-heavy research tasks, delegate to an independent subagent and have it return a sourced report to keep main context clean.
- [Git Revert vs Reset for Public Branches](git_revert_vs_reset.md) - workflow - 对已 push 的 commit 用 git revert 而不是 git reset --hard 来撤销，避免 force push 风险
- [TUI Permission Blocker 修复 (2026-05-29)](tui_permission_blocker_fix_2026_05_29.md) - workflow - approve permission 后状态栏仍显示 "Blocked by approval"，在 permissionFinishedMsg 中立即清除 ActivePermissionBlocker
