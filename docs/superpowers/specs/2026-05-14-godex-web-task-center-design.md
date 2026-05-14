# GoDex Web Task Center Design

## Summary

Web UI already has the raw pieces for GoDex 2.0 work visibility: chat feed, sessions, right-side inspector, LongTasks, Subagents, timeline, permissions, review, merge, cancel, and resume. The MVP gap is product framing: users must open several inspector tabs to understand whether one task is running, blocked, recovered, reviewable, or merged.

The Web Task Center adds a compact, frontend-only workbench band to the Chat page. It reuses existing APIs and mirrors the TUI outcome reconciliation model so `longtask error + recovered/merged worker` appears as one clear outcome instead of conflicting raw records.

## Product Shape

- Add a `Task Center` band directly below the session header and above the chat feed.
- Default desktop layout is a dense three-column workbench: `Outcomes`, `Active`, `Review`.
- Mobile layout becomes a single stacked panel with the same sections.
- The panel is collapsible, with state stored in local browser storage.
- The existing right inspector remains the deep-dive area for Approvals, Context, Turns, Subagents, LongTasks, and Timeline.

The first screen should answer:

- What is the current task outcome?
- What is running or blocked?
- What can be reviewed or merged?

## Data Model

The Web MVP should not add backend fields or routes. It derives `TaskOutcome` records from:

- `listSessionLongTasks`
- `listSessionSubagents`
- `getSnapshot` pending permissions, queued turns, active turn
- timeline events already streamed through SSE

Outcome matching follows the TUI rule:

- Prefer direct LongTask story `job_id == subagent.job_id`.
- Otherwise match only when LongTask and worker share a path-like token, such as `docs/superpowers/tmp/tui-mvp-demo.md` or `docs/superpowers/tmp`.
- Do not use generic text similarity.

Outcome statuses:

- `running`
- `blocked`
- `ready_for_review`
- `merged`
- `failed`
- `idle`

Merged or no-change workers override matched failed LongTasks and show `recovered from failed longtask`.

## UI Behavior

### Outcomes

Shows one line/card per reconciled outcome:

- status tag
- task title
- recovered marker when applicable
- LongTask ID and worker/job ID when available
- concise progress summary

Unmatched failed LongTasks stay visible as failed. Unmatched merged workers stay visible as completed worker outcomes.

### Active

Shows only active or blocked work:

- active main turn
- running LongTask or worker
- `pending_approval` worker
- pending permission count

Completed/merged outcomes do not occupy Active.

### Review

Shows reviewable and completed merge state:

- completed worker with pending/ready merge
- merged worker with applied status
- failed worker needing attention
- pending approval shortcuts via existing approval controls

Review, merge, cancel, and resume actions continue to call existing ChatPage mutations.

## Non-Goals

- No new backend API or storage schema.
- No graph canvas.
- No full diff viewer beyond existing subagent review drawer.
- No new task creation wizard.
- No change to existing chat feed, session list, inspector tabs, or provider setup flow.

## Success Criteria

- Chat page can show the demo scenario as `Merged · recovered from failed longtask` in the Task Center band.
- Numeric or text input behavior is unaffected.
- Existing inspector tabs continue to work.
- Existing Web build passes with `pnpm --dir ui/web build`.
- Backend tests remain unaffected.

## Review & Merge Center Extension

The Review & Merge Center is the Task Center's queue-first deep-dive surface for durable subagent outputs. It stays frontend-only for the MVP and uses existing session subagent endpoints to load reviews, show changed files and diffs, merge worker outputs, resume interrupted workers, and cancel running workers.

It does not add backend routes, persisted task state, batch merge, or a full code review editor. The first version focuses on reducing ambiguity around completed workers: ready, conflicted, merged, no changes, blocked, failed, and running.

The first product optimization pass adds three review ergonomics:

- Opening the center selects the first ready or conflicted worker and automatically loads its review.
- The detail panel shows a safety strip for diff completeness, conflicts, changed file count, and write scope before merge.
- The queue has `Ready`, `Conflicted`, `Merged`, `Failed`, and `All` filters, with the default `Ready` view including both ready and conflicted workers.

The second product optimization pass improves review depth without adding backend APIs:

- The changed-file list can focus the diff, copy individual paths, and expose a compact large-diff preview with a `Show full diff` toggle.
- The detail panel shows the task lineage from LongTask state to worker state, review state, and merge state, including recovered outcomes.
- The last merge result remains visible in the center with merge status, applied files, conflicts, and worktree path when available.
