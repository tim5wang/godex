# GoDex Local Task Center TUI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a TUI-first Task Center that makes GoDex 2.0 long-task, worker, sandbox, branch, and review state visible in the existing Bubble Tea frontend.

**Architecture:** Extend the existing `internal/tui` model with a workbench tab state and a derived task-center view model. Reuse backend `Snapshot`, `ListLongTasks`, and `ListSubagents`; do not add new persistence, protocols, or entrypoints.

**Tech Stack:** Go, Bubble Tea, Lipgloss, existing `internal/services/backend`, `internal/agent` longtask/subagent view types.

---

### Task 1: Backend Surface And View Model

**Files:**
- Modify: `internal/tui/model.go`
- Modify: `internal/tui/session.go`
- Modify: `internal/tui/tui_test.go`
- Create: `internal/tui/workbench.go`

- [x] Add `ListLongTasks` and `ListSubagents` to the TUI `Backend` interface.
- [x] Add model fields for the active workbench tab, long task views, subagent views, and latest load error.
- [x] Add a focused `workbenchSummary` builder in `internal/tui/workbench.go`.
- [x] Write tests that verify summary construction for empty sessions, long tasks, active subagents, pending permissions, and queued turns.

### Task 2: Async Loading

**Files:**
- Modify: `internal/tui/model.go`
- Modify: `internal/tui/session.go`
- Modify: `internal/tui/commands.go`
- Modify: `internal/tui/update.go`

- [x] Add `workbenchLoadedMsg`.
- [x] Fetch long tasks and subagents during TUI init and after snapshot refreshes.
- [x] Preserve existing snapshot, context summary, and event behavior.
- [x] On load failure, show a non-fatal workbench warning while keeping chat usable.

### Task 3: Task Center Rendering

**Files:**
- Modify: `internal/tui/view.go`
- Create or modify: `internal/tui/workbench_render.go`
- Modify: `internal/tui/tui_test.go`

- [x] Render Task Center as the default tab.
- [x] Keep the composer visible below the workbench.
- [x] Add tab labels: `1 Task`, `2 Workers`, `3 Graph`, `4 Diff`, `5 Logs`.
- [x] Render the existing conversation feed in Logs.
- [x] Add tests for default Task tab and Logs tab behavior.

### Task 4: Keyboard Navigation

**Files:**
- Modify: `internal/tui/update.go`
- Modify: `internal/tui/tui_test.go`

- [x] Map number keys `1` through `5` to workbench tabs.
- [x] Keep existing composer submission and feed navigation behavior intact.
- [x] In Logs tab, feed focus and item expansion continue to work as before.

### Task 5: Verification And Commit

**Files:**
- Modify: `.gitignore`

- [x] Ignore `.superpowers/` generated brainstorming companion files.
- [x] Run `go test ./internal/tui -count=1`.
- [x] Run `go test ./...`.
- [x] Run `git diff --check`.
- [x] Commit with `feat(tui): add local task center workbench`.
