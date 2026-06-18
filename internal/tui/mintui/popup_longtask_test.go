package mintui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	rtbackend "github.com/tim5wang/godex/internal/services/backend"
	minitui "github.com/tim5wang/min-tui"
)

// ── fixtures ──────────────────────────────────────────────────

func newLongTaskFixture() ([]rtbackend.LongTaskRow, []rtbackend.LongTaskDetail) {
	now := time.Now()
	rows := []rtbackend.LongTaskRow{
		{
			WorkflowID:  "wf-001",
			Title:       "wf-001",
			Description: "Refactor mintui popup layering",
			Status:      "running",
			Total:       8,
			Running:     2,
			Completed:   5,
			Failed:      1,
			UpdatedAt:   now.Add(-2 * time.Minute),
		},
		{
			WorkflowID:  "wf-002",
			Title:       "wf-002",
			Description: "Migrate storage to sqlite",
			Status:      "completed",
			Total:       3,
			Running:     0,
			Completed:   3,
			Failed:      0,
			UpdatedAt:   now.Add(-2 * time.Hour),
		},
		{
			WorkflowID:  "wf-003",
			Title:       "wf-003",
			Description: "Build eval suite",
			Status:      "failed",
			Total:       4,
			Running:     0,
			Completed:   2,
			Failed:      2,
			UpdatedAt:   now.Add(-30 * time.Minute),
		},
	}
	details := []rtbackend.LongTaskDetail{
		{
			Row: rows[0],
			Stories: []rtbackend.LongTaskStoryRow{
				{ID: "n1", Title: "extract popup module", Status: "completed", Passes: true, CommitHash: "abc1234"},
				{ID: "n2", Title: "add filter support", Status: "running", Passes: false},
				{ID: "n3", Title: "wire cancel", Status: "failed", Passes: false, Error: "context deadline"},
			},
		},
	}
	return rows, details
}

// newLongTaskSession wires a Session with the longtask fixture
// rows pre-loaded and a capturing TUI attached, so individual
// tests can poke at the popup state without spinning up a real
// terminal.
func newLongTaskSession(t *testing.T) (*Session, *capturingTUI, *fakeBackend) {
	t.Helper()
	b := newFakeBackend()
	rows, details := newLongTaskFixture()
	b.longTaskRows = rows
	b.longTaskDetails = details

	s := &Session{
		backend: b,
		stdout:  &strings.Builder{},
		stderr:  &strings.Builder{},
		now:     time.Now,
	}
	tui := &capturingTUI{}
	s.tui = tui
	s.longTasks.setRows(rows)
	return s, tui, b
}

// ── rendering ─────────────────────────────────────────────────

// TestLongTaskListRenderIncludesAllRows verifies the list popup
// renders every fixture row when no filter is active and the
// header is present.
func TestLongTaskListRenderIncludesAllRows(t *testing.T) {
	s, _, _ := newLongTaskSession(t)
	lines := s.renderLongTaskList(80, 20)

	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"STATUS",
		"wf-001",
		"wf-002",
		"wf-003",
		"running",
		"done",
		"failed",
		"navigate",
		"refresh",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("list popup missing %q\n--rendered--\n%s\n--end--", want, joined)
		}
	}
}

// TestLongTaskListEmptyShowsHint verifies the empty-state
// message renders when there are no rows.
func TestLongTaskListEmptyShowsHint(t *testing.T) {
	s, _, b := newLongTaskSession(t)
	b.longTaskRows = nil
	s.longTasks.setRows(nil)
	lines := s.renderLongTaskList(80, 20)

	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "No background tasks") {
		t.Fatalf("empty state missing: %q", joined)
	}
}

// TestLongTaskListErrorSurfacesBackendFailure verifies a
// failed ListLongTasks shows the error message in the popup
// rather than a stale empty state.
func TestLongTaskListErrorSurfacesBackendFailure(t *testing.T) {
	s, _, b := newLongTaskSession(t)
	b.longTaskListErr = errors.New("upstream timeout")
	s.longTasks.setErr(b.longTaskListErr)

	lines := s.renderLongTaskList(80, 20)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "upstream timeout") {
		t.Fatalf("error popup missing message: %q", joined)
	}
}

// ── filter ────────────────────────────────────────────────────

// TestLongTaskListFilterMatchesSubstring verifies that typing
// into the filter narrows the rendered rows.
func TestLongTaskListFilterMatchesSubstring(t *testing.T) {
	s, _, _ := newLongTaskSession(t)
	s.longTasks.mu.Lock()
	s.longTasks.filter = "sqlite"
	s.longTasks.mu.Unlock()

	rows := s.longTasks.filteredRows()
	if len(rows) != 1 {
		t.Fatalf("filter should keep exactly 1 row, got %d", len(rows))
	}
	if rows[0].WorkflowID != "wf-002" {
		t.Fatalf("filter should keep wf-002, got %q", rows[0].WorkflowID)
	}
}

// TestLongTaskListFilterIsCaseInsensitive mirrors the doc on
// filteredRows: matching must be case-insensitive.
func TestLongTaskListFilterIsCaseInsensitive(t *testing.T) {
	s, _, _ := newLongTaskSession(t)
	s.longTasks.mu.Lock()
	s.longTasks.filter = "REFACTOR"
	s.longTasks.mu.Unlock()
	rows := s.longTasks.filteredRows()
	if len(rows) != 1 || rows[0].WorkflowID != "wf-001" {
		t.Fatalf("case-insensitive filter broken: got %+v", rows)
	}
}

// TestLongTaskListFilterKeyAppendsAndResetsCursor verifies
// that pressing a printable rune in filter mode appends to the
// filter and re-anchors the cursor.
func TestLongTaskListFilterKeyAppendsAndResetsCursor(t *testing.T) {
	s, _, _ := newLongTaskSession(t)
	s.longTasks.mu.Lock()
	s.longTasks.cursor = 2
	s.longTasks.filtering = true
	s.longTasks.mu.Unlock()

	action := s.handleLongTaskFilterKey(minitui.KeyEvent{Rune: 'a'})
	if action != minitui.PopupUpdate {
		t.Fatalf("filter key should produce PopupUpdate, got %v", action)
	}
	s.longTasks.mu.Lock()
	got := s.longTasks.filter
	cur := s.longTasks.cursor
	s.longTasks.mu.Unlock()
	if got != "a" {
		t.Fatalf("filter should be %q, got %q", "a", got)
	}
	if cur != 0 {
		t.Fatalf("cursor should reset to 0 after filter change, got %d", cur)
	}
}

// TestLongTaskListFilterEscLeavesFilterText is a regression
// guard for the documented behavior: pressing `/` again (not
// the real Esc, which min-tui consumes before OnKey) leaves
// filter mode but keeps the typed filter text so the user can
// re-enter the same query.
func TestLongTaskListFilterEscLeavesFilterText(t *testing.T) {
	s, _, _ := newLongTaskSession(t)
	s.longTasks.mu.Lock()
	s.longTasks.filter = "abc"
	s.longTasks.filtering = true
	s.longTasks.mu.Unlock()

	s.handleLongTaskFilterKey(minitui.KeyEvent{Rune: '/'})
	s.longTasks.mu.Lock()
	filtering := s.longTasks.filtering
	filter := s.longTasks.filter
	s.longTasks.mu.Unlock()
	if filtering {
		t.Fatalf("filtering should be false after pressing /")
	}
	if filter != "abc" {
		t.Fatalf("filter text should be preserved, got %q", filter)
	}
}

// ── navigation ────────────────────────────────────────────────

// TestLongTaskListNavigationUpDown verifies the ↑/↓ keys move
// the cursor and clamp at the ends.
func TestLongTaskListNavigationUpDown(t *testing.T) {
	s, _, _ := newLongTaskSession(t)

	// Down: 0 → 1 → 2 (last)
	if a := s.handleLongTaskListKey(minitui.KeyEvent{Special: minitui.KeyDown}); a != minitui.PopupUpdate {
		t.Fatalf("down should return PopupUpdate, got %v", a)
	}
	if a := s.handleLongTaskListKey(minitui.KeyEvent{Special: minitui.KeyDown}); a != minitui.PopupUpdate {
		t.Fatalf("down should return PopupUpdate, got %v", a)
	}
	s.longTasks.mu.Lock()
	cur := s.longTasks.cursor
	s.longTasks.mu.Unlock()
	if cur != 2 {
		t.Fatalf("cursor should be at last (2), got %d", cur)
	}

	// Down at the end should NOT advance.
	s.handleLongTaskListKey(minitui.KeyEvent{Special: minitui.KeyDown})
	s.longTasks.mu.Lock()
	cur = s.longTasks.cursor
	s.longTasks.mu.Unlock()
	if cur != 2 {
		t.Fatalf("cursor should still be at 2, got %d", cur)
	}

	// Up: 2 → 1 → 0 (clamp).
	s.handleLongTaskListKey(minitui.KeyEvent{Special: minitui.KeyUp})
	s.handleLongTaskListKey(minitui.KeyEvent{Special: minitui.KeyUp})
	s.handleLongTaskListKey(minitui.KeyEvent{Special: minitui.KeyUp})
	s.longTasks.mu.Lock()
	cur = s.longTasks.cursor
	s.longTasks.mu.Unlock()
	if cur != 0 {
		t.Fatalf("cursor should clamp to 0, got %d", cur)
	}
}

// ── Enter / detail ───────────────────────────────────────────

// TestLongTaskListEnterPushesDetail verifies that pressing
// Enter pushes a new popup (the detail popup).  We can't tell
// from the capturingTUI which popup type was pushed, but we
// can count and assert that two popups are stacked (loading
// detail + the one pushed by Enter).
func TestLongTaskListEnterPushesDetail(t *testing.T) {
	s, tui, _ := newLongTaskSession(t)
	s.runCtx = context.Background()
	before := len(tui.popups)

	action := s.handleLongTaskListKey(minitui.KeyEvent{Enter: true})
	if action != minitui.PopupUpdate {
		t.Fatalf("Enter should produce PopupUpdate, got %v", action)
	}
	// pushLongTaskDetail now runs on a goroutine to avoid the
	// PushPopup-from-OnKey deadlock.  Give it a moment.
	// The capturingTUI.PopPopup is a no-op, so both the loading
	// and the detail popup accumulate (before+2).
	time.Sleep(50 * time.Millisecond)
	if got := len(tui.popups); got != before+2 {
		t.Fatalf("Enter should push loading+detail popups (2), got %d (before=%d)", got, before)
	}
	// The pushed popup's title should mention the workflow id
	// of the highlighted row (wf-001, cursor 0).
	last := tui.popups[len(tui.popups)-1]
	if !strings.Contains(last.Title, "wf-001") {
		t.Fatalf("detail popup title should reference wf-001, got %q", last.Title)
	}
}

// ── cancel confirm ──────────────────────────────────────────

// TestLongTaskListCancelPushesConfirmPopup verifies that
// pressing `c` on a row pushes the yes/no confirm popup.
func TestLongTaskListCancelPushesConfirmPopup(t *testing.T) {
	s, tui, _ := newLongTaskSession(t)
	s.runCtx = context.Background()
	before := len(tui.popups)

	action := s.handleLongTaskListKey(minitui.KeyEvent{Rune: 'c'})
	if action != minitui.PopupUpdate {
		t.Fatalf("c should produce PopupUpdate, got %v", action)
	}
	if got := len(tui.popups); got != before+1 {
		t.Fatalf("c should push 1 popup, got %d (before=%d)", got, before)
	}
	last := tui.popups[len(tui.popups)-1]
	if !strings.Contains(last.Title, "Cancel") {
		t.Fatalf("confirm popup title should contain 'Cancel', got %q", last.Title)
	}
}

// TestLongTaskCancelConfirmYesFiresBackendCancel verifies that
// pressing `y` on the confirm popup returns PopupClose and
// queues a CancelLongTask call on the fake backend.
func TestLongTaskCancelConfirmYesFiresBackendCancel(t *testing.T) {
	s, _, b := newLongTaskSession(t)
	s.runCtx = context.Background()
	row, _ := s.longTasks.selectedRow()

	s.pushLongTaskCancelConfirm(s.runCtx, row)
	if len(b.longTaskCancelCalls) != 0 {
		t.Fatalf("CancelLongTask should not have been called yet, got %v", b.longTaskCancelCalls)
	}
	confirm := s.tui.(*capturingTUI).popups[len(s.tui.(*capturingTUI).popups)-1]
	action := confirm.OnKey(minitui.KeyEvent{Rune: 'y'})
	if action != minitui.PopupClose {
		t.Fatalf("y should produce PopupClose, got %v", action)
	}
	// CancelLongTask is invoked from a goroutine; give it a
	// moment to land.  In tests this is deterministic because
	// the fake backend is synchronous — the call happens
	// before the goroutine yields.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		s.longTasks.mu.Lock()
		_ = s.longTasks.rows
		s.longTasks.mu.Unlock()
		if len(b.longTaskCancelCalls) == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := b.longTaskCancelCalls; len(got) != 1 || got[0] != row.WorkflowID {
		t.Fatalf("expected one cancel call for %q, got %v", row.WorkflowID, got)
	}
}

// TestLongTaskCancelConfirmNoDoesNothing verifies the `n` key
// closes the confirm popup without firing the backend call.
func TestLongTaskCancelConfirmNoDoesNothing(t *testing.T) {
	s, _, b := newLongTaskSession(t)
	s.runCtx = context.Background()
	row, _ := s.longTasks.selectedRow()

	s.pushLongTaskCancelConfirm(s.runCtx, row)
	confirm := s.tui.(*capturingTUI).popups[len(s.tui.(*capturingTUI).popups)-1]
	action := confirm.OnKey(minitui.KeyEvent{Rune: 'n'})
	if action != minitui.PopupClose {
		t.Fatalf("n should produce PopupClose, got %v", action)
	}
	// Give the (un-fired) goroutine time to NOT run.
	time.Sleep(20 * time.Millisecond)
	if len(b.longTaskCancelCalls) != 0 {
		t.Fatalf("n should not have triggered CancelLongTask, got %v", b.longTaskCancelCalls)
	}
}

// ── refresh hotkey ──────────────────────────────────────────

// TestLongTaskListRefreshHotkeySetsLoadingFlag verifies that
// pressing `r` sets the in-band loading flag on longTaskUI
// instead of pushing a separate loading popup.  The old design
// pushed a loading popup which deadlocked because PushPopup
// is called from inside the OnKey callback while min-tui's
// ReadLine holds t.mu.
func TestLongTaskListRefreshHotkeySetsLoadingFlag(t *testing.T) {
	s, tui, _ := newLongTaskSession(t)
	s.runCtx = context.Background()
	before := len(tui.popups)

	action := s.handleLongTaskListKey(minitui.KeyEvent{Rune: 'r'})
	if action != minitui.PopupUpdate {
		t.Fatalf("r should produce PopupUpdate, got %v", action)
	}
	if got := len(tui.popups); got != before {
		t.Fatalf("r should NOT push a popup (no loading popup), got %d (before=%d)", got, before)
	}
	// The loading flag should be set.
	s.longTasks.mu.Lock()
	loading := s.longTasks.loading
	s.longTasks.mu.Unlock()
	if !loading {
		t.Fatalf("r should set the loading flag")
	}
	_ = tui // keep reference
}

// ── refresh hotkey stack shape ───────────────────────────

// stackTUI is a capturingTUI variant whose PushPopup / PopPopup
// actually model a real popup stack.  The shared
// capturingTUI.PopPopup is intentionally a no-op (other tests
// only need the list of "things pushed"), but the refresh
// regression test must observe the stack height to prove the
// panel is not double-stacked after a refresh.
type stackTUI struct {
	capturingTUI

	stack []int // 1 == list popup (only popup type now — loading state is in-band)
}

func newStackTUI() *stackTUI {
	return &stackTUI{}
}

func (s *stackTUI) PushPopup(p minitui.Popup) {
	// Record the push on the parent so the regular assertions
	// (pushup count, last title, ...) still work, and also
	// append a stack marker.
	s.capturingTUI.PushPopup(p)
	s.stack = append(s.stack, 1)
}

func (s *stackTUI) PopPopup() {
	if len(s.stack) == 0 {
		return
	}
	s.stack = s.stack[:len(s.stack)-1]
	// Mirror capturingTUI.PopPopup which is a no-op: we
	// intentionally do not pop from s.popups because the
	// refresh test only cares about the stack height.
}

// TestLongTaskRefreshDoesNotDoubleStackList guards a
// regression that left the panel stuck after refresh.  The
// current design uses an in-band loading flag on longTaskUI
// instead of a separate loading popup, so the stack height
// never changes during a refresh — it stays at [list]
// throughout.
func TestLongTaskRefreshDoesNotDoubleStackList(t *testing.T) {
	s, _, _ := newLongTaskSession(t)
	s.runCtx = context.Background()
	st := newStackTUI()
	s.tui = st

	// Simulate the user having already opened the panel: stack
	// starts at [list].
	s.tui.PushPopup(s.buildLongTaskListPopup())
	if got := len(st.stack); got != 1 {
		t.Fatalf("setup: stack should start at [list] (1), got %d", got)
	}

	// Simulate pressing r: sets loading flag, spawns goroutine.
	// No popup is pushed — the loading state is in-band.
	s.pushLongTaskListLoading(s.runCtx)
	if got := len(st.stack); got != 1 {
		t.Fatalf("after r the stack should still be [list] (1), got %d", got)
	}
	s.longTasks.mu.Lock()
	if !s.longTasks.loading {
		t.Fatalf("loading flag should be set after r")
	}
	s.longTasks.mu.Unlock()

	// Run the refresh body synchronously.
	s.refreshLongTaskList(s.runCtx)
	if got := len(st.stack); got != 1 {
		t.Fatalf("after refresh the stack should stay [list] (1), got %d", got)
	}
	s.longTasks.mu.Lock()
	if s.longTasks.loading {
		t.Fatalf("loading flag should be cleared after refresh")
	}
	s.longTasks.mu.Unlock()
}

// ── detail render ──────────────────────────────────────────

// TestLongTaskDetailRendersStoriesAndCommitHashes verifies the
// detail popup surfaces the story table including the commit
// hash for completed stories.
func TestLongTaskDetailRendersStoriesAndCommitHashes(t *testing.T) {
	_, details := newLongTaskFixture()
	lines := renderLongTaskDetail(details[0],
		struct{visible bool; nodeID string; reason string; result string; loading bool}{},
		struct{visible bool; query string; result string; loading bool}{},
		struct{visible bool; result string; loading bool}{},
		80, 30)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"wf-001",      // workflow id
		"running",     // status badge
		"stories:",    // section header
		"extract popup module",
		"add filter support",
		"abc1234",     // commit hash
		"context deadline", // error
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("detail popup missing %q\n--rendered--\n%s\n--end--", want, joined)
		}
	}
}

// TestLongTaskDetailErrorStoryShowsInlineError verifies that a
// story with a non-empty Error field surfaces it indented
// below the row, prefixed with `!`.
func TestLongTaskDetailErrorStoryShowsInlineError(t *testing.T) {
	_, details := newLongTaskFixture()
	lines := renderLongTaskDetail(details[0],
		struct{visible bool; nodeID string; reason string; result string; loading bool}{},
		struct{visible bool; query string; result string; loading bool}{},
		struct{visible bool; result string; loading bool}{},
		80, 30)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "! context deadline") {
		t.Fatalf("error story should render with '! ' prefix, got: %q", joined)
	}
}

// ── global hotkey ──────────────────────────────────────────

// TestLongTaskCtrlBRegisteredAsGlobalHotkey verifies the Run
// path installs Ctrl+B on the TUI.  We don't call Run()
// (it acquires a real terminal); instead we assert on the
// captured SetGlobalKeyHandler.
func TestLongTaskCtrlBRegisteredAsGlobalHotkey(t *testing.T) {
	_, tui, _ := newLongTaskSession(t)
	s := &Session{
		backend: newFakeBackend(),
		stdout:  &strings.Builder{},
		stderr:  &strings.Builder{},
		now:     time.Now,
		tui:     tui,
	}
	s.runCtx = context.Background()
	s.registerGlobalHotkeys()

	if tui.globalKeyFn == nil {
		t.Fatalf("SetGlobalKeyHandler was not called")
	}
	// A non-Ctrl-B key must NOT consume the event.
	if consumed := tui.globalKeyFn(minitui.KeyEvent{Rune: 'b'}); consumed {
		t.Fatalf("bare 'b' must not be consumed by the global hotkey")
	}
	// Ctrl+B must consume the event AND push the list popup.
	// No separate loading popup — the loading state is in-band
	// on longTaskUI to avoid a PushPopup-from-OnKey deadlock.
	before := len(tui.popups)
	consumed := tui.globalKeyFn(minitui.KeyEvent{Ctrl: true, Rune: 'b'})
	if !consumed {
		t.Fatalf("Ctrl+B should be consumed")
	}
	if got := len(tui.popups); got != before+1 {
		t.Fatalf("Ctrl+B should push the list popup (1), got %d new", got-before)
	}
	// Verify the loading flag was set.
	s.longTasks.mu.Lock()
	loading := s.longTasks.loading
	s.longTasks.mu.Unlock()
	if !loading {
		t.Fatalf("Ctrl+B should set the loading flag")
	}
}

// ── status badge ────────────────────────────────────────────

// TestStatusBadgeReturnsColoredStatusForKnownStates is a
// regression guard: every LongTask status the backend emits
// must map to a renderable badge (no empty string).
func TestStatusBadgeReturnsColoredStatusForKnownStates(t *testing.T) {
	for _, st := range []string{"running", "completed", "failed", "pending", "blocked", "cancelling", "canceling", "", "weird-status"} {
		got := statusBadge(st)
		if got == "" {
			t.Fatalf("statusBadge(%q) returned empty string", st)
		}
	}
}

// ── title helper ────────────────────────────────────────────

// TestLongTaskTitlePrefersDescriptionOverID locks the title
// derivation: description wins when non-empty, falling back
// to the workflow id; long descriptions are truncated with
// an ellipsis.
func TestLongTaskTitlePrefersDescriptionOverID(t *testing.T) {
	if got := longTaskTitle("wf-x", ""); got != "wf-x" {
		t.Fatalf("empty description should fall back to id, got %q", got)
	}
	if got := longTaskTitle("wf-x", "  hello world  "); got != "hello world" {
		t.Fatalf("description should be trimmed, got %q", got)
	}
	long := strings.Repeat("a", 80)
	if got := longTaskTitle("wf-x", long); !strings.HasSuffix(got, "…") {
		t.Fatalf("long description should be truncated with ellipsis, got tail %q", got[len(got)-3:])
	}
}

// ── relative time helper ───────────────────────────────────

// TestRelativeTimeFormatsAndClamps verifies the relative time
// helper produces a "Nunit ago" string for a non-zero time
// and "—" for a zero time.
func TestRelativeTimeFormatsAndClamps(t *testing.T) {
	if got := relativeTime(time.Time{}); got != "—" {
		t.Fatalf("zero time should render as em-dash, got %q", got)
	}
	if got := relativeTime(time.Now().Add(-5 * time.Second)); !strings.HasSuffix(got, "s ago") {
		t.Fatalf("recent time should end in 's ago', got %q", got)
	}
	if got := relativeTime(time.Now().Add(-3 * time.Minute)); !strings.HasSuffix(got, "m ago") {
		t.Fatalf("minutes should end in 'm ago', got %q", got)
	}
}
