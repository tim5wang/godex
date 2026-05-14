# TUI Outcome Reconciliation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the TUI Task Center present one clear task outcome when a failed LongTask is recovered by a durable subagent review/merge path.

**Architecture:** Keep the change inside the TUI derived view model. Build high-confidence outcome records from existing `ListLongTasks`, `ListSubagents`, snapshot permissions, and queued turn data; do not change backend APIs or persisted schemas.

**Tech Stack:** Go, Bubble Tea, Lipgloss, existing `internal/tui`, `internal/agent` view types.

---

### Task 1: Outcome View Model

**Files:**
- Modify: `internal/tui/workbench.go`
- Modify: `internal/tui/tui_test.go`

- [x] Add failing tests for recovered LongTask, direct story JobID match, unmatched LongTask failures, ready-for-review workers, blocked workers, and small terminal rendering.
- [x] Add `workbenchOutcome`, `outcomeStatus`, and `outcomeSignal`.
- [x] Match LongTask and worker records by direct story `JobID` first, then by shared path-like tokens from descriptions, prompts, results, and write scopes.
- [x] Classify matched outcomes so merged/no-change workers override recovered LongTask failures.

### Task 2: Task Center Rendering

**Files:**
- Modify: `internal/tui/workbench.go`
- Modify: `internal/tui/workbench_render.go`

- [x] Route Plan, Active Execution, Review & Merge, Workers, Graph, and Diff tabs through outcome lines.
- [x] Keep Logs tab, composer behavior, backend interface, and async loading unchanged.
- [x] Show conservative fallback lines when records cannot be matched with high confidence.

### Task 3: Verification

**Files:**
- Modify: `internal/tui/tui_test.go`

- [x] Run `go test ./internal/tui -count=1`.
- [x] Run `go test ./...`.
- [x] Run `git diff --check`.
