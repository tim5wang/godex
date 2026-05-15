# GoDex ACP Coding UX Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Improve VS Code ACP coding usability by de-duplicating plan updates, making approval waits visible and recoverable, and extending `read_file` for focused line-range inspection.

**Architecture:** Keep changes scoped to `internal/acp/server` and file tooling. ACP remains an adapter over the unified backend; backend approval persistence and resume behavior stay unchanged.

**Tech Stack:** Go 1.22, `github.com/coder/acp-go-sdk`, existing GoDex backend/session/tooling packages, Go unit tests.

---

## File Structure

- Modify `internal/acp/server/handler.go`: add ACP plan emitter/de-dup helper, visible approval notice emission, native approval timeout wrapper.
- Modify `internal/acp/server/handler_test.go`: add ACP adapter regression tests.
- Modify `internal/platform/tooling/tooling.go`: extend `ReadFileDefinition` and `WorkspaceExecutor.ReadFileRange`.
- Modify `internal/platform/tooling/tooling_test.go`: add executor range and line-number tests.
- Modify `internal/tools/file.go`: extend typed `read_file` args.
- Modify `internal/tools/file_test.go`: add public tool wrapper tests.
- Modify `internal/agent/system_prompt_dynamic.go`: update coding guidance for focused `read_file` ranges.
- Modify `docs/vscode-acp.md`: document approval recovery and file-read guidance.

## Task 1: ACP Plan Update De-Duplication

**Files:**
- Modify: `internal/acp/server/handler.go`
- Modify: `internal/acp/server/handler_test.go`

- [ ] **Step 1: Add failing test for duplicate todo plan updates**

Add a test in `internal/acp/server/handler_test.go` that emits the same `EventTodoListUpdated` twice, completes the turn, then asserts only one non-final plan update is sent for the duplicate content.

Use the existing `fakeHandlerBackend`, `recordingUpdater`, and event emit pattern from `TestBackendPromptHandlerStreamsTodoListUpdateToToolCall`.

Expected assertion shape:

```go
planUpdates := collectPlanUpdates(updater.updates)
if len(planUpdates) != 2 {
	t.Fatalf("expected initial and final plan updates, got %d: %+v", len(planUpdates), planUpdates)
}
```

- [ ] **Step 2: Run failing test**

Run:

```bash
go test ./internal/acp/server -run TestBackendPromptHandlerDeduplicatesTodoPlanUpdates -count=1
```

Expected: FAIL because duplicate `UpdatePlan` messages are currently emitted.

- [ ] **Step 3: Implement plan emitter helper**

In `internal/acp/server/handler.go`, add a helper owned by one prompt turn:

```go
type acpPlanEmitter struct {
	updater       SessionUpdater
	lastSignature string
}

func (e *acpPlanEmitter) emit(ctx context.Context, entries []acp.PlanEntry) {
	if e == nil || e.updater == nil {
		return
	}
	signature := planSignature(entries)
	if signature == "" || signature == e.lastSignature {
		return
	}
	e.lastSignature = signature
	if err := e.updater.Update(ctx, acp.UpdatePlan(entries...)); err != nil {
		logger.Warnf("ACP plan update: %v", err)
	}
}
```

Add `planSignature(entries []acp.PlanEntry) string` using deterministic string building over content, priority, and status.

- [ ] **Step 4: Add stable plan metadata**

Update `todoPlanEntries` so each entry includes `_meta`:

```go
Meta: map[string]any{
	"godex.kind":         "todo",
	"godex.index":        idx,
	"godex.content_hash": shortStableHash(content),
},
```

Use a small helper based on `crypto/sha1` or `hash/fnv`. Keep the hash local to ACP adapter code.

- [ ] **Step 5: Route all plan updates through the emitter**

In `BackendPromptHandlerWithOptions`, replace direct calls to `turn.Updater.Update(ctx, acp.UpdatePlan(...))` with `planEmitter.emit(ctx, entries)`.

Also use the emitter for the final `completePlanEntries(lastTodoPlan)` call.

- [ ] **Step 6: Run ACP server tests**

Run:

```bash
go test ./internal/acp/server -count=1
```

Expected: PASS.

## Task 2: ACP Approval Visibility And Timeout

**Files:**
- Modify: `internal/acp/server/handler.go`
- Modify: `internal/acp/server/handler_test.go`
- Modify: `docs/vscode-acp.md`

- [ ] **Step 1: Add failing test for visible pending approval notice**

Add a test where submit returns `pending_approval`, pending permissions include a bash request, and the permission requester returns an error or no selection.

Assert that the ACP updater receives an agent text message containing:

```text
Pending approval required.
/approve
```

- [ ] **Step 2: Add failing test for native approval timeout/failure fallback**

Use `recordingPermissionRequester{err: errors.New("popup closed")}` and assert:

- handler returns no error.
- final text contains pending approval instructions.
- backend `approveCalled` and `denyCalled` remain false.

- [ ] **Step 3: Emit visible approval notice before native request**

Add helper:

```go
func emitPendingApprovalNotice(ctx context.Context, updater SessionUpdater, requestID string, items []tools.PendingPermission) {
	if updater == nil {
		return
	}
	text := renderPendingApproval(requestID, items)
	if strings.TrimSpace(text) == "" {
		return
	}
	if err := updater.Update(ctx, acp.UpdateAgentMessageText(text)); err != nil {
		logger.Warnf("ACP approval notice update: %v", err)
	}
}
```

Call it immediately before `resolveNativeApproval(...)` in both pending approval paths.

- [ ] **Step 4: Add bounded native approval wait**

Wrap `requestNativeApproval` internals with a timeout context:

```go
const nativeApprovalTimeout = 45 * time.Second

approvalCtx, cancel := context.WithTimeout(ctx, nativeApprovalTimeout)
defer cancel()
resp, err := requester.RequestPermission(approvalCtx, ...)
```

If timeout or error occurs, log the warning and return `(nativeApprovalSelection{}, false)`. The caller should return `renderPendingApproval(...)` as final text.

- [ ] **Step 5: Update ACP guide**

In `docs/vscode-acp.md`, update the approval section to state:

- GoDex shows approval details in chat even when VS Code also opens a popup.
- If the popup is missed, use `/approve list`, `/approve`, or `/approve session`.
- Pending approval state survives backend restart when session state is available.

- [ ] **Step 6: Run ACP server tests**

Run:

```bash
go test ./internal/acp/server -count=1
```

Expected: PASS.

## Task 3: `read_file` Line Range Ergonomics

**Files:**
- Modify: `internal/platform/tooling/tooling.go`
- Modify: `internal/platform/tooling/tooling_test.go`
- Modify: `internal/tools/file.go`
- Modify: `internal/tools/file_test.go`
- Modify: `internal/agent/system_prompt_dynamic.go`

- [ ] **Step 1: Add failing executor tests**

Add tests in `internal/platform/tooling/tooling_test.go` for:

```go
content, err := executor.ReadFileRangeWithOptions("notes.txt", 0, 0, tooling.ReadFileRangeOptions{
	StartLine: 2,
	LineCount: 2,
})
```

and:

```go
content, err := executor.ReadFileRangeWithOptions("notes.txt", 0, 0, tooling.ReadFileRangeOptions{
	StartLine: 2,
	EndLine: 3,
	ShowLineNumbers: true,
})
```

Expected outputs should include only requested lines; line-numbered output should prefix lines with their original 1-based line number.

- [ ] **Step 2: Add failing tool wrapper tests**

In `internal/tools/file_test.go`, call `NewReadFileTool` with `start_line`, `line_count`, and `show_line_numbers`.

Assert the wrapper forwards range args correctly and preserves workspace safety behavior.

- [ ] **Step 3: Introduce range options without breaking old calls**

In `internal/platform/tooling/tooling.go`, add:

```go
type ReadFileRangeOptions struct {
	StartLine       int
	EndLine         int
	LineCount       int
	ShowLineNumbers bool
}
```

Keep `ReadFile(path, limit int)` unchanged.

Add `ReadFileRangeWithOptions(path string, limit, offset int, opts ReadFileRangeOptions)` and keep old `ReadFileRange(path string, limit, offset, startLine int)` as a wrapper:

```go
func (e *WorkspaceExecutor) ReadFileRange(path string, limit, offset, startLine int) (string, error) {
	return e.ReadFileRangeWithOptions(path, limit, offset, ReadFileRangeOptions{StartLine: startLine})
}
```

Use the wrapper approach to reduce blast radius and preserve existing call sites.

- [ ] **Step 4: Implement validation**

Validation rules:

- `offset > 0` cannot be combined with any line range field.
- `end_line` and `line_count` cannot both be set.
- `start_line` is required when `end_line` or `line_count` is set.
- `end_line >= start_line`.
- `line_count > 0`.

Return exact errors that mention the conflicting fields.

- [ ] **Step 5: Implement line-range reading**

When line fields are used:

- Skip to `start_line`.
- Stop after `line_count` lines or after `end_line`.
- Apply `limit` as a byte cap after range selection.
- If `show_line_numbers` is true, prefix each returned line with `"<line>: "` preserving original line numbers.

- [ ] **Step 6: Extend tool schema and args**

In `internal/platform/tooling/tooling.go`, add schema properties:

```go
"end_line": map[string]interface{}{"type": "integer", "description": "Optional 1-based inclusive ending line. Use with start_line."},
"line_count": map[string]interface{}{"type": "integer", "description": "Optional number of lines to read from start_line."},
"show_line_numbers": map[string]interface{}{"type": "boolean", "description": "Prefix returned lines with original line numbers for code inspection."},
```

In `internal/tools/file.go`, extend `readFileArgs` with matching fields.

- [ ] **Step 7: Update coding guidance**

In `internal/agent/system_prompt_dynamic.go`, update file-reading guidance to say:

- For focused code inspection, prefer `read_file` with `start_line` and `line_count` or `end_line`.
- Use `rg` to locate symbols; use shell `sed` only when `read_file` cannot express the needed operation.

- [ ] **Step 8: Run file tooling tests**

Run:

```bash
go test ./internal/platform/tooling ./internal/tools ./internal/agent -run 'ReadFile|SystemPrompt' -count=1
```

Expected: PASS.

## Task 4: Integration Verification And Documentation

**Files:**
- Modify: `docs/vscode-acp.md`
- Optionally modify: `docs/issues.md` if the project tracks resolved ACP pain points there.

- [ ] **Step 1: Update VS Code ACP guide**

Ensure `docs/vscode-acp.md` includes:

- task list duplicate mitigation note
- approval popup plus chat fallback behavior
- `/approve list` recovery path
- `read_file` range examples:

```text
Read ui/web/src/i18n/messages.ts lines 12-18 with line numbers.
```

- [ ] **Step 2: Run focused tests**

Run:

```bash
go test ./internal/acp/server ./internal/platform/tooling ./internal/tools -count=1
```

Expected: PASS.

- [ ] **Step 3: Run full verification**

Run:

```bash
go test ./...
git diff --check
```

Expected: PASS.

- [ ] **Step 4: Manual VS Code ACP smoke**

Build:

```bash
go build -o ./godex ./cmd/godex
```

In VS Code ACP client:

- Ask for a small coding task that triggers `todo_write`; verify task list no longer duplicates on repeated todo events.
- Ask for a command likely to require approval; verify chat shows approval details even if the popup is missed.
- Ask to inspect a line range; verify model uses `read_file` range rather than `sed` for simple reads.

- [ ] **Step 5: Commit**

Commit after verification:

```bash
git add internal/acp/server/handler.go internal/acp/server/handler_test.go \
  internal/platform/tooling/tooling.go internal/platform/tooling/tooling_test.go \
  internal/tools/file.go internal/tools/file_test.go \
  internal/agent/system_prompt_dynamic.go docs/vscode-acp.md
git commit -m "fix(acp): harden coding workflow UX"
```

## Self-Review

- Spec coverage: plan update de-duplication is covered by Task 1; approval visibility and missed-popup fallback by Task 2; `read_file` ergonomics by Task 3; docs and verification by Task 4.
- Scope: no Web/TUI/API changes are required.
- Compatibility: old `read_file` call shape remains supported through a wrapper, and ACP backend approval behavior is reused.
- Risks: native approval timeout duration may need tuning after VS Code testing; the plan uses 45 seconds as an initial conservative value.
