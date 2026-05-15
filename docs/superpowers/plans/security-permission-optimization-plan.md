# GoDex Security Permission Optimization Plan

## Summary

GoDex already has a useful permission foundation: security profiles, remote-source approval, trusted path/command prefixes, manual/review/yolo modes, persisted pending approvals, and pending turn resume. The poor UX comes from the current approval model being too coarse and too easy to miss: a protected tool call turns the whole turn into `pending_approval`, users only get `once/session`, and pending requests have no expiry or strong status signal.

This plan keeps the current security baseline but improves the approval workflow so tasks are less likely to become stuck. It deliberately avoids a large runner protocol rewrite in the first implementation slice.

## Industry Notes, 2026 H1

- Claude Code exposes explicit permission rules with allow/ask/deny behavior and several permission modes. The practical lesson for GoDex is to make low-risk reads easy, keep write/shell/desktop controlled, and let users encode narrow repeated approvals.
- OpenAI Codex separates sandbox policy from approval policy. The practical lesson for GoDex is to let sandbox/profile rules carry more of the safety burden, while approval is reserved for boundary crossings and high-risk operations.
- Anthropic Managed Agents model approvals as durable session state transitions. The practical lesson for GoDex is that pending approval should be a recoverable state, not an ordinary tool failure.
- GitHub Copilot cloud agent emphasizes network and egress controls. The practical lesson for GoDex is to treat network/shell/file/desktop risks separately instead of using one global "approve everything" knob.

## Current Implementation Review

- `internal/toolruntime/permissions.go` has `PermissionDecision` and only two grant scopes: `once` and `session`.
- `PermissionManager.Evaluate()` creates pending approvals and reuses the same pending request by fingerprint.
- `PermissionManager.ApprovePending()` stores an override with `remaining=1` for once or unlimited for session.
- `internal/services/backend` converts `ErrPermissionPending` into a `pending_approval` turn and stores pending resume state.
- TUI supports `a` for once, `s` for session, and `x` for deny.
- ACP asks for native approval if available, otherwise renders plain text instructions.

## Problems To Fix First

1. Pending approvals have no expiry, so missed prompts remain active indefinitely and can leave sessions in a confusing state.
2. `session` approvals are too broad for some tools. Browser and desktop session approvals currently cover whole tools, which is convenient but risky.
3. Approval UX lacks a user-oriented summary. It shows tool/action/command, but not a concise "Agent wants to..." statement or expiry.
4. Approval scopes lack middle ground. `once` is noisy; `session` is often too broad.
5. Permission decisions are not consistently audited at the approval boundary.

## Implementation Slice

### Phase 1: Durable Approval State And Better Scope

- Add pending approval expiry:
  - `PendingPermission.ExpiresAt`.
  - Policy config `tools.permissions.pending_ttl_seconds`, default `300`.
  - Expired pending requests are pruned from `Evaluate()`, `ListPending()`, `ExportSession()`, and approval attempts fail clearly.
- Add grant scopes:
  - `once`: exact fingerprint, one use.
  - `task`: exact fingerprint, current task/turn only.
  - `count:N`: exact fingerprint, N uses.
  - `timebox:duration`: exact fingerprint until expiry, for example `timebox:10m`.
  - `pattern`: command/path pattern level for bash/background/file actions.
  - `session`: keep for compatibility, but keep browser/desktop session approval action-scoped instead of tool-wide.
- Persist override expiry:
  - Extend `PermissionOverrideState` with `ExpiresAt`.
  - Expired overrides are ignored and removed.

### Phase 2: Approval UX

- Add permission summary helpers:
  - `PermissionIntentSummary(PendingPermission)`.
  - `PermissionRiskSummary(PermissionRequest)`.
  - Redacted input preview remains available for technical detail.
- TUI:
  - Pending approval items show intent, risk, and expiry.
  - Add shortcuts: `a` once, `p` pattern, `t` timebox, `s` session, `x` deny.
- ACP:
  - Fallback text shows intent, risk, expiry, and new slash-command options.
  - Native approval keeps once/session because ACP option kinds are limited; fallback text advertises richer slash commands.
- Slash commands:
  - `/approve` defaults to once.
  - `/approve task`, `/approve session`, `/approve pattern`, `/approve count:5`, `/approve timebox:10m`.
  - `/approve <id> task`, `/approve <id> pattern`, `/approve <id> count:5`, `/approve <id> timebox:10m`.

### Phase 3: Audit And Diagnostics

- Emit security audit events for approval, denial, expiry, and pending creation/resolution.
- Include scope, tool, action, source, risk, request id, and fingerprint.
- Security summary should expose pending approval count and oldest pending age in a later UI pass.

### Phase 4: Pending Recovery Hardening

- Runner tool results distinguish approval waits from ordinary failures:
  - pending approval returns `status=permission_pending`.
  - payload includes the permission `request_id`.
  - sibling tool calls in the same assistant message still receive skipped tool results so OpenAI-compatible transcripts remain valid.
- Backend snapshots expose a first-class active permission blocker:
  - `active_permission_blocker` includes request id, status, turn id, intent, risk, expiry, tool/action, command/path, and source.
  - pending turns persist `blocked_by_permission_id` and `permission_status`.
  - permission status values are `pending`, `approved`, `denied`, `expired`, and `resumed`.
- Denial clears the blocked pending resume and appends model-visible runtime feedback:
  - the model is told not to retry the denied blocked tool call.
  - the next user "continue" turn can proceed from the current transcript instead of being tied to the old pending approval.
- Expiry is recoverable:
  - before a new user turn starts, stale pending resume state is reconciled against the pending queue.
  - if the approval expired, the original turn is marked `permission_status=expired`, pending resume is cleared, and runtime feedback tells the model not to retry the old blocked call automatically.
- Approval resume keeps its existing recovery feedback:
  - retry only the approved call if needed.
  - do not restart completed analysis or reread unchanged files unnecessarily.
- Client visibility:
  - TUI status line displays the active blocker as `Blocked by approval ...`.
  - ACP native approval title includes intent, risk, and expiry so the VSCode-side prompt is harder to miss.
  - `/approve status` is a read-only diagnostics alias for listing approval blockers with status, intent, risk, expiry, source, and exact approve/deny commands.
  - `/deny` without a request id denies the only pending approval when exactly one exists; with multiple pending approvals it lists blockers instead of guessing.
  - REPL pending approval fallback includes the same status, intent, risk, expiry, and points users to `/approve status`.
  - `/approve task` grants the exact pending request only for the originating turn. The backend injects `turn_id` into runtime permission requests so a later turn must request fresh approval.
  - TUI uses `u` for task-scope approval because `k` is already feed navigation.

## Deferred Work

- Non-blocking tool continuation inside a single model turn remains deferred. The runner now records structured `permission_pending` tool results and keeps the transcript valid, but it still stops the current turn for explicit user approval.
- Full policy-as-code, domain-level network allowlists, and Trust Budget are deferred until the approval state model is stable.
- Plan pre-approval is deferred until task planning and work scopes are more structured.

## Test Plan

- `PermissionManager`:
  - pending request includes `ExpiresAt`.
  - expired pending requests disappear and approval returns a clear error.
  - `count:2` allows exactly two matching executions.
  - `timebox:10m` allows until expiry and then requires approval again.
  - `pattern` covers matching bash command prefix/path prefix but not unrelated commands.
  - browser/desktop `session` approval is action-scoped.
- Commands:
  - approve parser accepts `pattern`, `count:N`, `timebox:10m`.
  - invalid scope errors include supported formats.
- TUI/ACP:
  - pending approval render includes intent, risk, expiry, and command options.
- Runner/backend:
  - pending approval tool result uses `permission_pending` and carries `request_id`.
  - denied pending approval appends recovery feedback and a later continue turn can proceed normally.
  - expired pending approval clears pending resume before the next user turn and marks the original turn expired.
  - Snapshot exposes `active_permission_blocker`; turn records expose `blocked_by_permission_id` and `permission_status`.
  - `/approve status` must not approve anything; it only lists current pending approval blockers and actionable commands.
  - `/deny` with no arguments denies only when there is exactly one pending approval; otherwise it must stay read-only.
  - `/approve task` allows a repeated matching request in the same turn, but not in a later turn.
- Regression:
  - `go test ./internal/core/conversation ./internal/toolruntime ./internal/app ./internal/tui ./internal/acp/server ./internal/services/backend -count=1`
  - `go test ./...`
  - `git diff --check`

## Acceptance Criteria

- A missed approval no longer lives forever without an expiry.
- Users can approve repeated safe operations without granting a whole session.
- Browser/desktop session approval is less broad.
- TUI/ACP approval text makes the risk and next action obvious.
- Existing `once/session` callers remain compatible.
