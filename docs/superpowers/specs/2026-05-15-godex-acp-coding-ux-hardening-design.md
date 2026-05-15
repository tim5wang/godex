# GoDex ACP Coding UX Hardening Design

## Summary

This design targets three VS Code ACP pain points reported during real coding use:

- Repeated task list refreshes can appear as duplicate plan blocks and push streaming assistant output out of view.
- Native approval popups are easy to miss; missed approval can leave the user unsure whether the task is still running, waiting, or recoverable.
- `read_file` is too weak for focused code inspection, so the model often falls back to shell commands such as `sed -n`.

The work stays inside the ACP adapter and existing file tooling. It does not change HTTP/Web/TUI entrypoints, persisted session schemas, or the ACP protocol itself.

## Goals

- Keep streaming output visible by reducing redundant ACP plan updates.
- Show pending approvals in the ACP conversation itself, even when the native VS Code permission popup is present.
- Avoid zombie-like user experience when approval is missed or the native approval request blocks too long.
- Make `read_file` good enough for line-range code inspection so coding sessions use fewer shell reads.
- Preserve existing backend approval, pending resume, and durable session behavior.

## Non-Goals

- No custom VS Code extension work.
- No ACP protocol fork.
- No backend rewrite of approval persistence or turn recovery.
- No removal of `bash`; shell remains available for search, tests, and commands where it is the right tool.
- No attempt to guarantee client-side task list replacement if a client ignores ACP plan semantics. GoDex will reduce duplicate updates and include stable metadata, but the client still controls rendering.

## Recommended Approach

Use a conservative three-part adapter hardening:

1. Add ACP plan de-duplication and stable plan metadata in `internal/acp/server`.
2. Add dual-channel approval visibility: native ACP permission request plus an in-chat pending approval notice, with a bounded native request wait.
3. Extend `read_file` with line-range ergonomics and update coding guidance so focused file reads use `read_file` instead of `sed`.

This approach keeps the fix close to the problematic surfaces and avoids pulling Web/TUI/session-store code into the ACP-specific problem.

## Component Design

### ACP Plan Update Reconciliation

Current behavior: `EventTodoListUpdated` is converted into ACP `UpdatePlan(...)`, and final turn completion can send another completed plan. If the client renders each update as a fresh block, the UI can show duplicate task lists.

Design:

- Add a small in-turn plan emitter helper in `internal/acp/server/handler.go`.
- Compute a deterministic signature from each `PlanEntry` content, priority, and status.
- Only send `UpdatePlan` when the signature changes.
- Add stable `_meta` fields per plan entry:
  - `godex.kind = "todo"`
  - `godex.index`
  - `godex.content_hash`
- Send the final all-completed plan only if it differs from the last sent plan.

This does not require a new API. It only changes how the ACP adapter emits existing `SessionUpdatePlan` messages.

### ACP Approval Visibility And Timeout Handling

Current behavior: GoDex requests ACP-native permission via `RequestPermission`. If the VS Code popup is missed, the user may not notice the task is waiting. If the request blocks or is canceled by the client, the visible state can feel like a dead task.

Design:

- When a turn enters `pending_approval`, fetch pending permissions and render a compact approval notice into the ACP conversation before or alongside native approval:
  - request id
  - tool/action
  - command or paths
  - reason
  - `/approve`, `/approve session`, `/deny` shortcuts
- Wrap native `RequestPermission` with a bounded timeout. If it times out or fails, return an end-turn message that clearly says the task is waiting for approval and can be resumed with slash commands.
- Keep using backend `ApprovePermission`, which already owns pending approval state and pending resume replay.
- When native approval succeeds and returns a `ResumeTurnID`, keep watching the resumed turn as current ACP code already does.
- Do not create a second approval system in ACP. ACP only displays and routes decisions to the existing backend.

Recovery policy:

- Missed native popup: conversation shows `/approve` instructions and pending request details.
- Approval after prompt ended: `/approve` executes through the existing command path and backend resume result is returned in chat.
- Process restart: backend pending approval and pending resume are already persisted; ACP docs should tell users to run `/approve list` or `/approve`.

### `read_file` Code Inspection Ergonomics

Current behavior: `read_file` supports `start_line` and byte `offset`, but lacks a natural `end_line`/`line_count` API. Large file messages even suggest shell tools such as `sed`, which encourages shell fallback.

Design:

- Extend `read_file` args with:
  - `end_line`: optional 1-based inclusive ending line.
  - `line_count`: optional number of lines to read from `start_line`.
  - `show_line_numbers`: optional boolean for focused code snippets.
- Validation:
  - `offset` remains mutually exclusive with line-based fields.
  - `end_line` and `line_count` are mutually exclusive.
  - `end_line` must be greater than or equal to `start_line` when both are provided.
  - `line_count` must be positive when provided.
- Preserve current behavior when only `path`, `limit`, `offset`, or `start_line` are used.
- Update tool description and coding profile guidance to prefer `read_file` ranges for code inspection.
- Update large-file guidance to recommend `read_file` with `start_line`/`line_count` or `rg` for locating symbols, not `sed`.

## Data Flow

### Plan Updates

1. Backend emits `EventTodoListUpdated`.
2. ACP adapter converts todos into `PlanEntry` items with stable metadata.
3. Plan emitter compares the new signature with the last sent signature.
4. Adapter sends `UpdatePlan` only when changed.
5. On turn completion, adapter emits completed entries only if the completed signature is new.

### Approval

1. Backend submit or turn event reports `pending_approval`.
2. ACP adapter fetches pending permissions.
3. Adapter sends a visible approval notice to chat.
4. Adapter starts native permission request with a timeout.
5. If native approval succeeds, adapter calls backend approve/deny and watches resumed turn if available.
6. If native approval times out/fails, adapter ends the ACP prompt with pending approval instructions; backend remains the source of truth.

### File Reads

1. Model calls `read_file` with `path` plus optional `start_line`/`line_count` or `end_line`.
2. Tool validates range arguments.
3. Workspace executor reads only the requested range under existing workspace boundary checks.
4. Tool returns text, optionally line-numbered.

## Error Handling

- ACP plan de-duplication must never drop changed status. Signature includes status and priority.
- Native approval timeout must not deny the request. It only falls back to visible pending instructions.
- Approval notice rendering must be best effort. If pending lookup fails, show the known request id and generic instructions.
- Invalid `read_file` ranges return actionable errors with the accepted combinations.
- Binary file and workspace escape behavior remain unchanged.

## Testing

- ACP handler tests:
  - duplicate todo updates emit one plan update.
  - changed todo status emits a new plan update.
  - final completed plan is not re-emitted when already complete.
  - pending approval sends a visible agent message before native approval resolution.
  - native approval timeout/failure returns pending approval instructions without losing backend pending state.
- File tooling tests:
  - `start_line + line_count` returns the expected range.
  - `start_line + end_line` returns the expected range.
  - `show_line_numbers` prefixes returned lines.
  - invalid combinations produce clear errors.
- ACP docs:
  - approval recovery instructions include `/approve list`.
  - file inspection guidance prefers `read_file` ranges.

## Compatibility

- Existing ACP sessions continue to work.
- Existing `read_file` calls keep the same output unless new range or line-number options are used.
- Existing approval slash commands remain valid.
- No Web/TUI/API behavior changes are required.
