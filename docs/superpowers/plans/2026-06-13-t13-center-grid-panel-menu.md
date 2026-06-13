# T13 Center Grid Panel Menu Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a visible CenterGrid titlebar menu that lets users move or swap panels between grid slots.

**Architecture:** Keep `CenterGrid` presentational and expose optional slot chrome callbacks. `ChatWorkspaceCanvas` owns store wiring and maps menu actions to `movePanelToGrid` or slot-level `swapGridSlots`. Drag/drop remains out of scope for this slice.

**Tech Stack:** React, Ant Design `Dropdown`/`Button`, Zustand layout store, Vitest pure helper tests.

---

### Task 1: Slot Menu Contract

**Files:**
- Modify: `ui/web/src/components/workspace/CenterGrid.tsx`
- Test: `ui/web/test/centerGrid.test.ts`

- [x] Add pure helpers for deriving movable grid slots and action labels.
- [x] Verify helpers handle 2x2 and 3x3 occupancy.

### Task 2: CenterGrid Slot Chrome

**Files:**
- Modify: `ui/web/src/components/workspace/CenterGrid.tsx`

- [x] Wrap non-empty slots with a compact titlebar.
- [x] Render an optional menu button when callbacks are provided.
- [x] Preserve full-row and split-cell layout behavior.

### Task 3: Store Wiring

**Files:**
- Modify: `ui/web/src/features/chat/ChatWorkspaceCanvas.tsx`

- [x] Pass current occupancy to the menu helpers.
- [x] Call `movePanelToGrid` for empty targets.
- [x] Call `swapPanelInGrid` for occupied targets.

### Task 4: Verification

**Commands:**
- `pnpm --dir ui/web test -- test/centerGrid.test.ts test/layoutStore.test.ts`
- `pnpm --dir ui/web build`

**Verified:**
- `pnpm --dir ui/web test -- test/centerGrid.test.ts test/layoutStore.test.ts`
- `pnpm --dir ui/web typecheck`
- `pnpm --dir ui/web test`
- `pnpm --dir ui/web build`
- `pnpm --dir ui/web test:e2e --project=chromium-mobile`
- `pnpm --dir ui/web test:e2e --project=chromium-desktop`

### Task 5: Phase 2 Interaction Feedback + Usability Fixes

**Files:**
- Modify: `ui/web/src/features/chat/ChatWorkspaceCanvas.tsx`
- Modify: `ui/web/src/features/files/FilesPanel.tsx`
- Modify: `ui/web/src/lib/terminalClient.ts`
- Modify: `ui/web/src/store/layout.ts`
- Test: `ui/web/test/layoutStore.test.ts`
- Test: `ui/web/test/terminalClient.test.ts`
- Test: `ui/web/e2e/acceptance.spec.ts`

- [x] Show transient feedback after a panel move/swap menu action.
- [x] Use slot-to-slot swap so “Swap with Chat” moves both visible panels instead of evicting/no-op.
- [x] Render grid-hosted files panels expanded and filling the cell, independent of dock collapsed state.
- [x] Adopt the backend terminal id returned by `/v1/terminal/create` for output/input/delete calls.
- [x] Add Playwright coverage for menu feedback and grid files expanded state.

### Task 6: Phase 3 Titlebar Drag/Drop

**Files:**
- Modify: `ui/web/src/components/workspace/CenterGrid.tsx`
- Modify: `ui/web/src/features/chat/ChatWorkspaceCanvas.tsx`
- Modify: `ui/web/src/styles.css`
- Test: `ui/web/test/centerGrid.test.ts`
- Test: `ui/web/e2e/acceptance.spec.ts`

- [x] Add a draggable titlebar handle for occupied grid panels.
- [x] Highlight valid drop targets while dragging.
- [x] Drop on occupied slots using slot-to-slot swap and on empty slots using move.
- [x] Keep the menu-button path working alongside drag/drop.
- [x] Add Playwright coverage for titlebar drag swapping panels.

### Task 7: Screenshot Usability Regression Fixes

**Files:**
- Modify: `ui/web/src/App.tsx`
- Modify: `ui/web/src/features/files/FilesPanel.tsx`
- Modify: `ui/web/src/features/terminal/TerminalPanel.tsx`
- Modify: `ui/web/src/components/Composer.tsx`
- Modify: `ui/web/src/features/chat/ChatPage.tsx`
- Modify: `ui/web/src/styles.css`
- Test: `ui/web/e2e/acceptance.spec.ts`

- [x] Replace the generic chat route subtitle with workspace/model/version context.
- [x] Make the grid files panel load and preview selected files.
- [x] Add a grid files action that attaches the selected file to the active chat composer.
- [x] Make the grid chat cell a real flex column so the composer stays at the bottom.
- [x] Add visible terminal connection status instead of a blank black panel.
- [x] Isolate Playwright localStorage state so layout tests are deterministic.
