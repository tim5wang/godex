# GoDex Local Task Center TUI Design

## Goal

GoDex Local Agent Workbench first lands as a TUI-first Task Center for individual developers. The first screen should answer three questions without opening logs: what is the plan, what is running, and whether the result can be reviewed or merged.

## Product Scope

The MVP extends the existing Bubble Tea TUI. It does not create a new web app, does not replace the chat backend, and does not change CLI, HTTP, tool schemas, session IDs, or storage defaults.

The primary layout is:

- Plan: current plan-like work inferred from long tasks, task list, todos, queued turns, and session state.
- Active Execution: currently running turn, active phase, active long task stories, active subagent worker metadata, sandbox, worker branch, source branch, and latest progress.
- Review & Merge: completed or mergeable subagents, failed work, pending permissions, changed-file summaries where available, and commands the user can take next.

Secondary tabs are Task, Workers, Graph, Diff, and Logs. For the MVP, Task is the default workbench view and Logs maps to the existing conversation feed. Workers is a focused list of durable subagents. Graph and Diff are lightweight metadata views until deeper graph/diff browsing is implemented.

## Entry Model

The default path is chat-to-task. A user can keep typing normally in the TUI. When GoDex starts durable long tasks or subagents, the Task Center surfaces the execution state automatically.

Plan-file and explicit task commands remain future-compatible but are not required for the first slice.

## Data Sources

The TUI should reuse existing backend APIs:

- `Snapshot`: session identity, messages, tasks, todos, queued turns, timeline, running state, active phase, permissions.
- `ListLongTasks`: durable long task workflow summaries and story status.
- `ListSubagents`: durable worker jobs, progress, sandbox, worker ID, branch metadata, merge status.
- Existing runtime events: trigger refreshes after activity; no new event protocol is required for the MVP.

## Interaction Model

The TUI remains keyboard-first:

- `1`: Task Center
- `2`: Workers
- `3`: Graph
- `4`: Diff
- `5`: Logs
- Existing feed navigation, composer input, permission approval, and slash commands continue to work.

The composer remains available at the bottom in every tab. This keeps the workbench commandable and avoids splitting the product into separate chat and task modes.

## Rendering Rules

The Task Center is a three-column layout on wide terminals. On narrow terminals it degrades to stacked sections. Text must be clipped or wrapped; layout must not panic with small terminal dimensions.

The first version uses stable terminal text and existing lipgloss styles. It should favor dense, readable state over decorative UI.

## Non-Goals

- No new local web UI.
- No graph editing, visual graph canvas, or distributed graph coordination.
- No new storage backend behavior.
- No automatic task planning beyond existing longtask/subagent mechanisms.
- No forced subagent concurrency.
- No new commit/merge semantics beyond existing durable subagent review and merge APIs.

## Success Criteria

- Opening the TUI shows the Task Center by default.
- The Task Center renders useful state for empty sessions, active running sessions, long tasks, subagents, permissions, and queued turns.
- Users can switch to Logs to access the previous chat feed.
- Tests cover summary construction and the key tab rendering behavior.
- Existing TUI tests and `go test ./...` pass.
