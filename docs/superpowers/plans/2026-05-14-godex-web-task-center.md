# GoDex Web Task Center Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Web Chat Task Center band that reconciles LongTasks, workers, approvals, and review/merge state into a clear task outcome summary.

**Architecture:** Frontend-only MVP. Add a pure outcome builder under `ui/web/src/features/chat`, render it through a new Task Center component, and integrate it into `ChatPage` above the message feed. Reuse existing queries, mutations, inspector tabs, review drawer, and SSE invalidation.

**Tech Stack:** React 19, TypeScript, Ant Design, TanStack Query, existing GoDex Web API client.

---

### Task 1: Frontend Outcome Model

**Files:**
- Create: `ui/web/src/features/chat/taskCenterOutcome.ts`
- Modify: `ui/web/src/features/chat/ChatPage.tsx`

- [x] Define `TaskOutcomeStatus`, `TaskOutcome`, and small signal types.
- [x] Add `buildTaskOutcomes({ longTasks, subagents, pendingPermissions, queuedTurns, running, activeTurnId, activePhase })`.
- [x] Match LongTask to worker by story `job_id` first.
- [x] Add conservative path-token matching for fallback recovery.
- [x] Classify statuses so merged/no-change workers override matched failed LongTasks.
- [x] Export helpers only needed by Web components; keep backend types unchanged.

### Task 2: Task Center Component

**Files:**
- Create: `ui/web/src/features/chat/TaskCenterPanel.tsx`
- Modify: `ui/web/src/styles.css`

- [x] Render three sections: `Outcomes`, `Active`, `Review`.
- [x] Use AntD `Card`, `Tag`, `Progress`, `Badge`, `Space`, `Button`, and icons already used in the Web app.
- [x] Keep cards compact: no nested cards, no marketing hero, no decorative gradients.
- [x] Add collapse/expand affordance with browser-local state owned by `ChatPage`.
- [x] Ensure mobile layout stacks sections and desktop layout uses three columns.

### Task 3: ChatPage Integration

**Files:**
- Modify: `ui/web/src/features/chat/ChatPage.tsx`
- Modify: `ui/web/src/i18n/messages.ts` if user-facing labels need localization.

- [x] Build outcomes from existing `longTasksQuery`, `subagentJobs`, `pendingPermissions`, `queuedTurns`, and snapshot running state.
- [x] Insert `TaskCenterPanel` below the session header and above `.chat-feed`.
- [x] Wire existing actions into review controls:
  - review subagent
  - merge subagent
  - resume subagent
  - cancel subagent
  - run/finalize/cancel LongTask where applicable
- [x] Preserve existing inspector tabs and chat feed behavior.
- [x] Keep the panel visible by default on desktop and collapsed-by-user only after explicit toggle.

### Task 4: Styling And Responsive QA

**Files:**
- Modify: `ui/web/src/styles.css`

- [x] Add `.task-center-band`, `.task-center-grid`, `.task-outcome-row`, `.task-center-section`, and mobile rules.
- [x] Ensure long IDs and paths wrap or truncate without horizontal overflow.
- [x] Keep palette aligned with existing GoDex Web variables.
- [x] Confirm the chat feed still has usable height when the band is expanded.

### Task 5: Verification

**Files:**
- Modify only touched files if formatting or TypeScript catches issues.

- [x] Run `pnpm --dir ui/web build`.
- [x] Run `go test ./internal/tui -count=1`.
- [x] Run `go test ./...`.
- [x] Run `git diff --check`.
- [ ] Manually inspect a demo session in Web: recovered LongTask should appear as one merged outcome, while raw details remain available in inspector tabs.

## Acceptance Criteria

- Web Chat first screen shows a concise Task Center outcome summary.
- `longtask error + merged fallback subagent` is presented as recovered/merged when matched by job ID or path token.
- Generic text-only similarity does not merge unrelated tasks.
- No backend route, schema, or persisted state changes.
- Existing review/merge/cancel/resume behavior remains routed through current API functions.
