package mintui

import (
	"context"
	"fmt"
	"strings"
	"sync"

	rtbackend "github.com/tim5wang/godex/internal/services/backend"
	minitui "github.com/tim5wang/min-tui"
)

// longTaskUI is the per-session state that backs the Ctrl+B
// background-task popup.  It is kept on *Session (not on the
// Popup itself) because popups are recreated on every push and
// we want the cursor position, filter text, and last-loaded
// row set to survive the user closing and reopening the popup
// in the same session.
type longTaskUI struct {
	// mu guards every field. The popup's OnKey runs on the
	// min-tui input goroutine, while the async loader's
	// callback runs on a fresh goroutine; both can race.
	mu sync.Mutex

	// rows is the most recent ListLongTasks result.  When
	// non-nil the list popup renders from this slice; when
	// nil the popup shows a "loading…" or "no tasks" state.
	rows []rtbackend.LongTaskRow

	// detailOps holds the state for advanced operations
	// (rollback, lookup, GC) inside the detail popup.
	detailOps longTaskDetailOps

	// lastErr is the most recent backend error.  It wins over
	// rows when set so the user sees why a refresh failed.
	lastErr error

	// loading is true while an async ListLongTasks fetch is
	// in flight.  The list popup's Render closure reads this
	// flag and shows "Loading…" instead of rows.  Using an
	// in-band loading state (instead of a separate loading
	// popup) avoids a deadlock: OnKey callbacks run under
	// min-tui's t.mu lock, so PushPopup from within OnKey
	// would deadlock trying to re-acquire t.mu.
	loading bool

	// cursor is the index of the highlighted row in the
	// filtered view (NOT in `rows` directly).  Always clamped
	// to [0, len(filtered)-1] by the renderer.
	cursor int

	// filter is the current substring filter (empty = show
	// all).  Live-applied: each new rune re-renders the popup.
	filter string

	// filtering is true when the user has pressed `/` and
	// subsequent printable runes go into `filter`.  We model
	// it explicitly (rather than inferring from "filter is
	// non-empty") so Esc can exit filter mode without
	// clearing the filter text — matching how min-tui's own
	// slash dropdown behaves.
	filtering bool
}

// reset clears the cursor / filter state but keeps the last
// loaded rows so reopening the popup is instant.  Called when
// the user presses Esc inside the popup (we don't have a hook
// for that today, but it's the right place if we add one) or
// when the session is closed.
func (lt *longTaskUI) reset() {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	lt.cursor = 0
	lt.filter = ""
	lt.filtering = false
}

// filteredRows returns the rows that match the current filter.
// Locked snapshot so OnKey and the async loader do not race
// during render.
func (lt *longTaskUI) filteredRows() []rtbackend.LongTaskRow {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	if lt.filter == "" {
		out := make([]rtbackend.LongTaskRow, len(lt.rows))
		copy(out, lt.rows)
		return out
	}
	q := strings.ToLower(lt.filter)
	out := make([]rtbackend.LongTaskRow, 0, len(lt.rows))
	for _, r := range lt.rows {
		if strings.Contains(strings.ToLower(r.WorkflowID), q) ||
			strings.Contains(strings.ToLower(r.Description), q) ||
			strings.Contains(strings.ToLower(r.LastStoryTitle), q) {
			out = append(out, r)
		}
	}
	return out
}

// selectedRow returns the currently highlighted row, or
// LongTaskRow{} if the list is empty.
func (lt *longTaskUI) selectedRow() (rtbackend.LongTaskRow, bool) {
	rows := lt.filteredRows()
	lt.mu.Lock()
	cur := lt.cursor
	lt.mu.Unlock()
	if len(rows) == 0 {
		return rtbackend.LongTaskRow{}, false
	}
	if cur < 0 {
		cur = 0
	}
	if cur >= len(rows) {
		cur = len(rows) - 1
	}
	return rows[cur], true
}

// setRows updates the cache, clears the error and loading
// states, and clamps the cursor.  Called from the async loader.
func (lt *longTaskUI) setRows(rows []rtbackend.LongTaskRow) {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	lt.rows = rows
	lt.lastErr = nil
	lt.loading = false
	if lt.cursor >= len(rows) {
		lt.cursor = len(rows) - 1
	}
	if lt.cursor < 0 {
		lt.cursor = 0
	}
}

// setErr records a backend error and clears the rows so the
// popup renders an error message instead of stale data.
func (lt *longTaskUI) setErr(err error) {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	lt.lastErr = err
	lt.rows = nil
	lt.cursor = 0
}

// ── popup pushers ────────────────────────────────────────────
//
// These methods are the only entry points the Session uses to
// open the Ctrl+B popup surface.  They are intentionally
// broken into two phases (push loading → replace with real)
// because the OnKey callback is forbidden from calling back
// into min-tui; we always trigger the load from the Session's
// own goroutine.

// openLongTaskList is the entry point for the initial Ctrl+B
// press from normal (non-popup) mode.  It pushes the list
// popup, sets the in-band loading flag so the popup's Render
// shows "Loading…", then spawns a goroutine to fetch data.
//
// No separate loading popup is used: PushPopup from within an
// OnKey callback would deadlock because min-tui's ReadLine
// holds t.mu while dispatching popup keys (see the "Global key
// handler — before lock so PushPopup does not deadlock" comment
// in min-tui/tui.go).  Using a loading flag on longTaskUI
// avoids the deadlock entirely.
//
// For the refresh path (r key inside the list popup), callers
// use pushLongTaskListLoading directly since the list popup is
// already on the stack.
func (s *Session) openLongTaskList(ctx context.Context) {
	if s.tui == nil {
		return
	}
	s.tui.PushPopup(s.buildLongTaskListPopup())
	s.pushLongTaskListLoading(ctx)
}

// pushLongTaskListLoading marks the popup as loading and
// spawns a goroutine that fetches data from the backend.
// Once the data arrives, setRows clears the loading flag and
// a WriteString("") triggers reRenderPopups so the list
// reflects the fresh rows.
//
// This method does NOT push a separate loading popup — see
// openLongTaskList for why.  It is safe to call from OnKey
// callbacks because it does not call PushPopup.
func (s *Session) pushLongTaskListLoading(ctx context.Context) {
	if s.tui == nil {
		return
	}
	s.longTasks.mu.Lock()
	s.longTasks.loading = true
	s.longTasks.mu.Unlock()

	go s.refreshLongTaskList(ctx)
}

// refreshLongTaskList calls the backend and updates the cached
// rows.  Runs on a fresh goroutine.  Once the data (or error)
// arrives it calls WriteString("") to trigger min-tui's
// reRenderPopups; the list popup's Render closure reads
// s.longTasks.rows at draw time and picks up the fresh data.
//
// The empty WriteString is a no-op for the output screen
// (appendOutput drops empty payloads) but it still flows
// through renderAfterWrite → reRenderPopups.
func (s *Session) refreshLongTaskList(ctx context.Context) {
	if s.backend == nil {
		return
	}
	rows, err := s.backend.ListLongTasks(ctx, s.sessionID)
	if err != nil {
		s.longTasks.setErr(err)
	} else {
		s.longTasks.setRows(rows)
	}
	if s.tui != nil {
		_, _ = s.tui.WriteString("")
	}
}

// buildLongTaskListPopup constructs the long-task list popup
// with the current cached state.  Safe to call from any
// goroutine; the only side effect is allocating the closure.
func (s *Session) buildLongTaskListPopup() minitui.Popup {
	return minitui.Popup{
		Title: "Background Tasks (Ctrl+B)",
		Render: func(w, h int) []string {
			return s.renderLongTaskList(w, h)
		},
		OnKey: func(k minitui.KeyEvent) minitui.PopupAction {
			return s.handleLongTaskListKey(k)
		},
		OnClose: func() {
			// Coming back to the list popup (e.g. after the
			// detail popup closed) — clear any filter-mode
			// cursor highlight, but keep the loaded rows
			// and the filter text so the user does not
			// have to retype their query.
			s.longTasks.mu.Lock()
			s.longTasks.filtering = false
			s.longTasks.mu.Unlock()
		},
	}
}

// pushLongTaskDetail opens the detail popup for one workflow.
// The detail data is fetched on a goroutine; first we show a
// "loading detail" popup and replace it with the real one
// when GetLongTask returns.
//
// If the user dismisses the loading popup with ESC before the
// backend responds, the goroutine detects the cancellation and
// does NOT push the detail popup — preventing the "closed
// window reappears during streaming" bug.
func (s *Session) pushLongTaskDetail(ctx context.Context, workflowID string) {
	if s.tui == nil {
		return
	}
	detailCtx, detailCancel := context.WithCancel(ctx)
	s.tui.PushPopup(minitui.Popup{
		Title: "Task " + workflowID,
		Render: func(w, h int) []string {
			lines := []string{"", "  Loading…", ""}
			return padPopupLines(lines, w, h)
		},
		OnKey: noopPopupKey,
		OnClose: func() {
			detailCancel()
		},
	})

	go s.refreshLongTaskDetail(detailCtx, workflowID)
}

func (s *Session) refreshLongTaskDetail(ctx context.Context, workflowID string) {
	if s.backend == nil {
		return
	}
	detail, err := s.backend.GetLongTask(ctx, s.sessionID, workflowID)

	// If the loading popup was dismissed, don't push anything.
	if ctx.Err() != nil {
		return
	}

	if s.tui == nil {
		return
	}

	if err != nil {
		s.tui.PopPopup()
		// Only push error if still relevant (context not cancelled).
		if ctx.Err() == nil {
			s.tui.PushPopup(minitui.Popup{
				Title: "Task " + workflowID,
				Render: func(w, h int) []string {
					lines := []string{
						"",
						"  Failed to load task:",
						"  " + truncateForPopup(err.Error(), 60),
						"",
						"  Esc to close",
					}
					return padPopupLines(lines, w, h)
				},
				OnKey: noopPopupKey,
			})
		}
		return
	}
	s.tui.PopPopup()
	s.tui.PushPopup(s.buildLongTaskDetailPopup(detail))
}

// buildLongTaskDetailPopup constructs the detail popup for an
// already-loaded LongTaskDetail snapshot.
func (s *Session) buildLongTaskDetailPopup(detail rtbackend.LongTaskDetail) minitui.Popup {
	// Reset any stale rollback/lookup/GC state from a previous
	// detail view so the new popup starts clean.
	s.longTasks.detailOps.reset()
	return minitui.Popup{
		Title: "Task " + detail.Row.WorkflowID,
		Render: func(w, h int) []string {
			ops := &s.longTasks.detailOps
			ops.mu.Lock()
			rollback := struct {
				visible  bool
				nodeID   string
				reason   string
				result   string
				loading  bool
			}{
				visible: ops.rollbackVisible,
				nodeID:  ops.rollbackNodeID,
				reason:  ops.rollbackReason,
				result:  ops.rollbackResult,
				loading: ops.rollbackLoading,
			}
			lookup := struct {
				visible bool
				query   string
				result  string
				loading bool
			}{
				visible: ops.lookupVisible,
				query:   ops.lookupQuery,
				result:  ops.lookupResult,
				loading: ops.lookupLoading,
			}
			gc := struct {
				visible bool
				result  string
				loading bool
			}{
				visible: ops.gcVisible,
				result:  ops.gcResult,
				loading: ops.gcLoading,
			}
			ops.mu.Unlock()
			return renderLongTaskDetail(detail, rollback, lookup, gc, w, h)
		},
		OnKey: func(k minitui.KeyEvent) minitui.PopupAction {
			return s.handleLongTaskDetailKey(k, detail.Row.WorkflowID)
		},
	}
}

// pushLongTaskCancelConfirm opens a yes/no confirmation popup
// on top of the list.  Confirming fires off a cancel goroutine
// that replaces the popup with a result popup.
func (s *Session) pushLongTaskCancelConfirm(ctx context.Context, row rtbackend.LongTaskRow) {
	if s.tui == nil {
		return
	}
	s.tui.PushPopup(minitui.Popup{
		Title: "Cancel task?",
		Render: func(w, h int) []string {
			lines := []string{
				"",
				"  Cancel " + row.WorkflowID + " ?",
				"  " + longTaskRowToTitle(row),
				"",
				"  [y] yes   [n/Esc] no",
			}
			return padPopupLines(lines, w, h)
		},
		OnKey: func(k minitui.KeyEvent) minitui.PopupAction {
			switch {
			case k.Rune == 'y' || k.Rune == 'Y':
				// Returning PopupClose is enough — min-tui
				// will pop the confirm popup and reveal
				// the list popup underneath.  We kick off
				// the cancel goroutine AFTER the close so
				// its PushPopup lands on a stable stack.
				go s.runLongTaskCancel(s.runCtx, row)
				return minitui.PopupClose
			case k.Rune == 'n' || k.Rune == 'N':
				return minitui.PopupClose
			default:
				// Tab / ↑↓ etc. are not actionable here.
				return minitui.PopupPassthrough
			}
		},
	})
}

// runLongTaskCancel calls CancelLongTask on the backend and
// shows a transient result popup.  The list popup is
// refreshed afterwards so cancelled tasks disappear or
// change status without the user having to reopen Ctrl+B.
func (s *Session) runLongTaskCancel(ctx context.Context, row rtbackend.LongTaskRow) {
	if s.backend == nil {
		return
	}
	err := s.backend.CancelLongTask(ctx, s.sessionID, row.WorkflowID)
	if s.tui == nil {
		return
	}
	if err != nil {
		s.tui.PushPopup(minitui.Popup{
			Title: "Cancel failed",
			Render: func(w, h int) []string {
				lines := []string{
					"",
					"  Could not cancel " + row.WorkflowID + ":",
					"  " + truncateForPopup(err.Error(), 60),
					"",
					"  Esc to close",
				}
				return padPopupLines(lines, w, h)
			},
			OnKey: noopPopupKey,
		})
		return
	}
	s.tui.PushPopup(minitui.Popup{
		Title: "Cancel sent",
		Render: func(w, h int) []string {
			lines := []string{
				"",
				"  Cancellation sent for " + row.WorkflowID + ".",
				"  Status will refresh next time you open this view.",
				"",
				"  Esc to close",
			}
			return padPopupLines(lines, w, h)
		},
		OnKey: noopPopupKey,
	})
	// Refresh the list in the background so the next Ctrl+B
	// reflects the new state.
	go s.refreshLongTaskList(ctx)
}

// ── OnKey handlers ────────────────────────────────────────────

// handleLongTaskListKey is the OnKey closure for the list
// popup.  It is called on the min-tui input goroutine and
// must NOT call back into the frontend (only through
// s.tui.PushPopup / s.tui.PopPopup, which the OnKey contract
// does not forbid — we tested this empirically with the
// loading→real replacement in runLongTaskCancel above).
func (s *Session) handleLongTaskListKey(k minitui.KeyEvent) minitui.PopupAction {
	// Filter-mode dispatch first: once the user has pressed
	// `/`, every printable rune (and Backspace) goes to the
	// filter, not the cursor.  Esc to leave filter mode
	// returns navigation to the list.
	s.longTasks.mu.Lock()
	filtering := s.longTasks.filtering
	s.longTasks.mu.Unlock()

	if filtering {
		return s.handleLongTaskFilterKey(k)
	}

	// Not filtering — pure navigation / actions.
	switch {
	case k.Special == minitui.KeyUp:
		s.longTasks.mu.Lock()
		if s.longTasks.cursor > 0 {
			s.longTasks.cursor--
		}
		s.longTasks.mu.Unlock()
		return minitui.PopupUpdate

	case k.Special == minitui.KeyDown:
		s.longTasks.mu.Lock()
		rows := s.longTasks.rows
		cur := s.longTasks.cursor
		if cur < len(rows)-1 {
			s.longTasks.cursor = cur + 1
		}
		s.longTasks.mu.Unlock()
		return minitui.PopupUpdate

	case k.Enter:
		row, ok := s.longTasks.selectedRow()
		if !ok {
			return minitui.PopupPassthrough
		}
		s.pushLongTaskDetail(s.runCtx, row.WorkflowID)
		return minitui.PopupUpdate

	case k.Rune == '/':
		s.longTasks.mu.Lock()
		s.longTasks.filtering = true
		s.longTasks.mu.Unlock()
		return minitui.PopupUpdate

	case k.Rune == 'c' || k.Rune == 'C':
		row, ok := s.longTasks.selectedRow()
		if !ok {
			return minitui.PopupPassthrough
		}
		s.pushLongTaskCancelConfirm(s.runCtx, row)
		return minitui.PopupUpdate

	case k.Rune == 'r' || k.Rune == 'R':
		// Manual refresh — bypass the cache and re-fetch
		// from the backend.  Useful after a cancel when
		// the user does not want to close+reopen.
		s.pushLongTaskListLoading(s.runCtx)
		return minitui.PopupUpdate

	default:
		return minitui.PopupPassthrough
	}
}

// handleLongTaskFilterKey handles keys while the user is
// editing the filter.  Runes append to the filter, Backspace
// removes the last rune, ESC leaves filter mode while keeping
// the popup open.  As of min-tui v0.5.5, ESC is routed to
// OnKey first and only closes the popup when OnKey returns
// PopupPassthrough.
func (s *Session) handleLongTaskFilterKey(k minitui.KeyEvent) minitui.PopupAction {
	switch {
	case k.Rune == '/':
		// Pressing `/` again leaves filter mode and
		// keeps the existing filter text (matches how
		// min-tui's slash dropdown works: first `/`
		// opens, second `/` would otherwise close).
		s.longTasks.mu.Lock()
		s.longTasks.filtering = false
		s.longTasks.mu.Unlock()
		return minitui.PopupUpdate
	case k.Rune == 27:
		// ESC: exit filter mode but keep the popup open
		// (min-tui v0.5.5 routes ESC to OnKey first;
		// returning PopupUpdate prevents the default close).
		s.longTasks.mu.Lock()
		s.longTasks.filtering = false
		s.longTasks.mu.Unlock()
		return minitui.PopupUpdate
	case k.Special == minitui.KeyBackspace:
		// Backspace: remove last rune from filter.
		s.longTasks.mu.Lock()
		if len(s.longTasks.filter) > 0 {
			r := []rune(s.longTasks.filter)
			s.longTasks.filter = string(r[:len(r)-1])
		}
		s.longTasks.cursor = 0
		s.longTasks.mu.Unlock()
		return minitui.PopupUpdate
	case k.Special == minitui.KeyUp, k.Special == minitui.KeyDown:
		// Up/Down in filter mode exits filter mode and
		// navigates the list — this is the most
		// common pattern (e.g. fzf, VSCode command
		// palette).
		s.longTasks.mu.Lock()
		s.longTasks.filtering = false
		s.longTasks.mu.Unlock()
		return s.handleLongTaskListKey(k)
	case k.Enter:
		// Enter while filtering opens the detail for the
		// first filtered row.
		s.longTasks.mu.Lock()
		s.longTasks.filtering = false
		s.longTasks.mu.Unlock()
		return s.handleLongTaskListKey(k)
	case k.Rune == 0:
		// Other control keys (Tab, Delete, etc.) — passthrough.
		return minitui.PopupPassthrough
	default:
		s.longTasks.mu.Lock()
		s.longTasks.filter += string(k.Rune)
		// Reset cursor so the highlighted row stays
		// sensible after the filter shrinks the list.
		s.longTasks.cursor = 0
		s.longTasks.mu.Unlock()
		return minitui.PopupUpdate
	}
}

// longTaskDetailOps holds the state for advanced operations
// (rollback, lookup, GC) inside the detail popup.
type longTaskDetailOps struct {
	mu sync.Mutex

	// rollback
	rollbackVisible bool
	rollbackNodeID  string
	rollbackReason  string
	rollbackResult  string
	rollbackLoading bool

	// lookup
	lookupVisible  bool
	lookupQuery    string
	lookupResult   string
	lookupLoading  bool

	// gc
	gcVisible  bool
	gcResult   string
	gcLoading  bool
}

func (o *longTaskDetailOps) reset() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.rollbackVisible = false
	o.rollbackReason = ""
	o.rollbackResult = ""
	o.rollbackLoading = false
	o.lookupVisible = false
	o.lookupQuery = ""
	o.lookupResult = ""
	o.lookupLoading = false
	o.gcVisible = false
	o.gcResult = ""
	o.gcLoading = false
}

// handleLongTaskDetailKey handles keys inside the detail
// popup.  In addition to Esc (handled by min-tui), it supports:
//
//	R       start rollback — prompts for nodeID then reason
//	l       lookup by commit hash — prompts for commit hash
//	g       GC artifacts — prompts for confirm
func (s *Session) handleLongTaskDetailKey(k minitui.KeyEvent, workflowID string) minitui.PopupAction {
	ops := &s.longTasks.detailOps

	ops.mu.Lock()
	rollbackVisible := ops.rollbackVisible
	lookupVisible := ops.lookupVisible
	gcVisible := ops.gcVisible
	ops.mu.Unlock()

	// Rollback flow
	if rollbackVisible {
		return s.handleRollbackKey(k, workflowID)
	}
	// Lookup flow
	if lookupVisible {
		return s.handleLookupKey(k, workflowID)
	}
	// GC confirm flow
	if gcVisible {
		return s.handleGCKey(k, workflowID)
	}

	switch {
	case k.Rune == 'R':
		ops.mu.Lock()
		ops.rollbackVisible = true
		ops.rollbackNodeID = ""
		ops.rollbackReason = ""
		ops.rollbackResult = ""
		ops.rollbackLoading = false
		ops.mu.Unlock()
		s.tui.SetStatus("Rollback: enter story nodeID (e.g. n1)", minitui.StatusWarning)
		return minitui.PopupUpdate
	case k.Rune == 'l':
		ops.mu.Lock()
		ops.lookupVisible = true
		ops.lookupQuery = ""
		ops.lookupResult = ""
		ops.lookupLoading = false
		ops.mu.Unlock()
		s.tui.SetStatus("Lookup: type commit hash and press Enter", minitui.StatusWarning)
		return minitui.PopupUpdate
	case k.Rune == 'g':
		ops.mu.Lock()
		ops.gcVisible = true
		ops.gcResult = ""
		ops.gcLoading = false
		ops.mu.Unlock()
		s.tui.SetStatus("GC: press y to confirm, n to cancel", minitui.StatusWarning)
		return minitui.PopupUpdate
	default:
		return minitui.PopupPassthrough
	}
}

func (s *Session) handleRollbackKey(k minitui.KeyEvent, workflowID string) minitui.PopupAction {
	ops := &s.longTasks.detailOps
	ops.mu.Lock()
	defer ops.mu.Unlock()

	if ops.rollbackLoading {
		// Loading — only Esc to cancel
		return minitui.PopupPassthrough
	}

	if ops.rollbackNodeID == "" {
		// Step 1: entering nodeID
		if k.Rune == 0 {
			return minitui.PopupPassthrough
		}
		if k.Enter {
			if ops.rollbackReason == "" {
				ops.rollbackNodeID = strings.TrimSpace(ops.rollbackReason)
				ops.rollbackReason = ""
				if s.tui != nil {
					s.tui.SetStatus("Rollback: enter reason (optional, Enter to submit)", minitui.StatusWarning)
				}
				return minitui.PopupUpdate
			}
			// Submit rollback
			go s.runRollback(s.runCtx, workflowID, ops.rollbackNodeID, ops.rollbackReason)
			ops.rollbackLoading = true
			return minitui.PopupUpdate
		}
		if k.Special == minitui.KeyUp || k.Special == minitui.KeyDown {
			return minitui.PopupPassthrough
		}
		// Accumulate nodeID in rollbackReason temporarily
		ops.rollbackReason += string(k.Rune)
		return minitui.PopupUpdate
	}

	// Step 2: entering reason
	if k.Enter {
		go s.runRollback(s.runCtx, workflowID, ops.rollbackNodeID, ops.rollbackReason)
		ops.rollbackLoading = true
		return minitui.PopupUpdate
	}
	if k.Rune != 0 {
		ops.rollbackReason += string(k.Rune)
		return minitui.PopupUpdate
	}
	return minitui.PopupPassthrough
}

func (s *Session) handleLookupKey(k minitui.KeyEvent, workflowID string) minitui.PopupAction {
	ops := &s.longTasks.detailOps
	ops.mu.Lock()
	defer ops.mu.Unlock()

	if ops.lookupLoading {
		return minitui.PopupPassthrough
	}

	if k.Enter {
		go s.runLookup(s.runCtx, workflowID, ops.lookupQuery)
		ops.lookupLoading = true
		return minitui.PopupUpdate
	}
	if k.Rune != 0 && k.Special != minitui.KeyUp && k.Special != minitui.KeyDown {
		ops.lookupQuery += string(k.Rune)
		return minitui.PopupUpdate
	}
	return minitui.PopupPassthrough
}

func (s *Session) handleGCKey(k minitui.KeyEvent, workflowID string) minitui.PopupAction {
	ops := &s.longTasks.detailOps
	ops.mu.Lock()
	defer ops.mu.Unlock()

	if ops.gcLoading {
		return minitui.PopupPassthrough
	}

	switch {
	case k.Rune == 'y' || k.Rune == 'Y':
		go s.runGC(s.runCtx, workflowID)
		ops.gcLoading = true
		return minitui.PopupUpdate
	case k.Rune == 'n' || k.Rune == 'N':
		ops.gcVisible = false
		ops.gcResult = ""
		return minitui.PopupUpdate
	}
	return minitui.PopupPassthrough
}

// ── operation runners ──────────────────────────────────────

func (s *Session) runRollback(ctx context.Context, workflowID, nodeID, reason string) {
	if s.backend == nil {
		return
	}
	result, err := s.backend.RollbackLongTaskStory(ctx, s.sessionID, workflowID, nodeID, reason)
	ops := &s.longTasks.detailOps
	ops.mu.Lock()
	defer ops.mu.Unlock()
	ops.rollbackLoading = false
	if err != nil {
		ops.rollbackResult = "Rollback failed: " + err.Error()
	} else if result.Success {
		ops.rollbackResult = "Rollback succeeded — story " + result.StoryID
		ops.rollbackVisible = false
	} else {
		ops.rollbackResult = "Rollback conflict: " + result.Error
	}
	if s.tui != nil {
		_, _ = s.tui.WriteString("")
	}
}

func (s *Session) runLookup(ctx context.Context, workflowID, query string) {
	if s.backend == nil {
		return
	}
	result, err := s.backend.LookupLongTask(ctx, s.sessionID, query, workflowID)
	ops := &s.longTasks.detailOps
	ops.mu.Lock()
	defer ops.mu.Unlock()
	ops.lookupLoading = false
	if err != nil {
		ops.lookupResult = "Lookup failed: " + err.Error()
	} else if result.Error != "" {
		ops.lookupResult = result.Error
	} else if len(result.Entries) == 0 {
		ops.lookupResult = "No matches found for \"" + query + "\""
	} else {
		var lines []string
		for _, e := range result.Entries {
			lines = append(lines, fmt.Sprintf("  %s  story=%s", e.LongTaskID, e.StoryID))
		}
		ops.lookupResult = strings.Join(lines, "\n")
	}
	if s.tui != nil {
		_, _ = s.tui.WriteString("")
	}
}

func (s *Session) runGC(ctx context.Context, workflowID string) {
	if s.backend == nil {
		return
	}
	result, err := s.backend.GCLongTaskArtifacts(ctx, s.sessionID, workflowID, 0, true)
	ops := &s.longTasks.detailOps
	ops.mu.Lock()
	defer ops.mu.Unlock()
	ops.gcLoading = false
	if err != nil {
		ops.gcResult = "GC failed: " + err.Error()
	} else if result.Error != "" {
		ops.gcResult = "GC error: " + result.Error
	} else if result.DryRun {
		ops.gcResult = fmt.Sprintf("Dry run: would remove %d, keep %d", result.RemovedCount, result.KeptCount)
	} else {
		ops.gcResult = fmt.Sprintf("GC done: removed %d artifacts, kept %d", result.RemovedCount, result.KeptCount)
		ops.gcVisible = false
	}
	if s.tui != nil {
		_, _ = s.tui.WriteString("")
	}
}

// ── renderers ────────────────────────────────────────────────

// renderLongTaskList produces the content lines for the list
// popup.  It honors the filter / cursor / error state held on
// s.longTasks.
func (s *Session) renderLongTaskList(w, h int) []string {
	s.longTasks.mu.Lock()
	loading := s.longTasks.loading
	lastErr := s.longTasks.lastErr
	filter := s.longTasks.filter
	filtering := s.longTasks.filtering
	s.longTasks.mu.Unlock()

	if loading {
		lines := []string{"", "  Loading…", ""}
		return padPopupLines(lines, w, h)
	}

	if lastErr != nil {
		lines := []string{
			"",
			"  Failed to load tasks:",
			"  " + truncateForPopup(lastErr.Error(), w-4),
			"",
			"  [r] refresh · Esc close",
		}
		return padPopupLines(lines, w, h)
	}

	rows := s.longTasks.filteredRows()
	s.longTasks.mu.Lock()
	cur := s.longTasks.cursor
	if cur < 0 {
		cur = 0
	}
	if cur >= len(rows) && len(rows) > 0 {
		cur = len(rows) - 1
	}
	s.longTasks.cursor = cur
	s.longTasks.mu.Unlock()

	if len(rows) == 0 {
		msg := "  No background tasks."
		if filter != "" {
			msg = "  No tasks match filter \"" + filter + "\"."
		}
		lines := []string{
			"",
			msg,
			"",
			"  [r] refresh · Esc close",
		}
		return padPopupLines(lines, w, h)
	}

	// Header + rows + footer.
	header := []string{
		"  STATUS    ID                              PROGRESS      UPDATED",
		"  ──────    ────────────────────────────   ──────────    ────────",
	}
	body := make([]string, 0, len(header)+len(rows)+4)
	body = append(body, header...)
	for i, r := range rows {
		line := formatLongTaskRow(r, w-4)
		if i == cur {
			line = "▶ " + line
		} else {
			line = "  " + line
		}
		body = append(body, line)
	}
	// Footer.
	if filtering {
		body = append(body, "")
		body = append(body, "  filter: "+filter+"_")
		body = append(body, "  [/] leave filter · type to filter")
	} else {
		body = append(body, "")
		body = append(body, "  [↑↓] navigate · [Enter] details · [/] filter · [c] cancel · [r] refresh · Esc close")
	}
	return padPopupLines(body, w, h)
}

// formatLongTaskRow renders one LongTaskRow as a single
// width-bounded line.  We avoid pulling in tabwriter here so
// the popup renders even when the user shrinks the terminal
// to 60 columns.
func formatLongTaskRow(r rtbackend.LongTaskRow, width int) string {
	status := statusBadge(r.Status)
	title := longTaskRowToTitle(r)
	if len(title) > 30 {
		title = title[:29] + "…"
	}
	id := r.WorkflowID
	if len(id) > 14 {
		id = id[:14]
	}
	progress := fmt.Sprintf("%d/%d", r.Completed, r.Total)
	if r.Running > 0 {
		progress = progress + fmt.Sprintf(" (%d run)", r.Running)
	}
	if r.Failed > 0 {
		progress = progress + fmt.Sprintf(" (%d fail)", r.Failed)
	}
	if len(progress) > 12 {
		progress = progress[:12]
	}
	updated := relativeTime(r.UpdatedAt)
	if len(updated) > 8 {
		updated = updated[:8]
	}
	return fmt.Sprintf("%-8s  %-14s  %-30s  %-12s  %s",
		status, id, title, progress, updated)
}

// statusBadge returns a short colored badge string for a
// task's status.  We keep this minimal so the popup stays
// scannable; color is ANSI so users on dumb terminals see
// the raw text.
func statusBadge(status string) string {
	switch status {
	case "running":
		return "\x1b[36mrunning\x1b[0m"
	case "completed":
		return "\x1b[32mdone   \x1b[0m"
	case "failed":
		return "\x1b[31mfailed \x1b[0m"
	case "pending":
		return "\x1b[2mpending\x1b[0m"
	case "blocked":
		return "\x1b[33mblocked\x1b[0m"
	case "cancelling", "canceling":
		return "\x1b[33mcancel \x1b[0m"
	default:
		if status == "" {
			return "unknown"
		}
		// Pad to 8 chars so columns line up.
		if len(status) > 8 {
			return status[:8]
		}
		return status + strings.Repeat(" ", 8-len(status))
	}
}

// renderLongTaskDetail produces the content lines for the
// detail popup, including any active operation state
// (rollback, lookup, GC).
func renderLongTaskDetail(d rtbackend.LongTaskDetail, rollback struct{visible bool; nodeID string; reason string; result string; loading bool}, lookup struct{visible bool; query string; result string; loading bool}, gc struct{visible bool; result string; loading bool}, w, h int) []string {
	row := d.Row
	lines := []string{
		"",
		"  " + row.WorkflowID + "  " + statusBadge(row.Status),
		"  " + longTaskRowToTitle(row),
		"",
		fmt.Sprintf("  progress: %d/%d (running %d, failed %d)",
			row.Completed, row.Total, row.Running, row.Failed),
	}
	if row.Project != "" {
		lines = append(lines, "  project: "+row.Project)
	}
	if row.BranchName != "" {
		lines = append(lines, "  branch:  "+row.BranchName)
	}
	if !row.UpdatedAt.IsZero() {
		lines = append(lines, "  updated: "+relativeTime(row.UpdatedAt))
	}

	// Stories.
	if len(d.Stories) > 0 {
		lines = append(lines, "")
		lines = append(lines, "  stories:")
		for _, s := range d.Stories {
			badge := "  ☐"
			if s.Passes {
				badge = "  ☑"
			}
			line := fmt.Sprintf("  %s %-10s %s", badge, truncateForPopup(s.Status, 10), truncateForPopup(s.Title, w-20))
			if s.CommitHash != "" {
				line = line + "  " + s.CommitHash
			}
			lines = append(lines, line)
			if s.Error != "" {
				lines = append(lines, "      ! "+truncateForPopup(s.Error, w-8))
			}
		}
	}

	// Operation state
	if rollback.visible || lookup.visible || gc.visible {
		lines = append(lines, "")
		lines = append(lines, "  ── operations ──")
	}
	if rollback.visible {
		if rollback.loading {
			lines = append(lines, "  Rollback: loading…")
		} else if rollback.result != "" {
			lines = append(lines, "  "+rollback.result)
		} else if rollback.nodeID == "" {
			lines = append(lines, "  Rollback: enter story nodeID")
			if rollback.reason != "" {
				lines = append(lines, "  nodeID: "+rollback.reason+"_")
			}
		} else {
			lines = append(lines, "  Rollback: node="+rollback.nodeID)
			lines = append(lines, "  reason: "+rollback.reason+"_")
			lines = append(lines, "  Enter to submit")
		}
	}
	if lookup.visible {
		if lookup.loading {
			lines = append(lines, "  Lookup: loading…")
		} else if lookup.result != "" {
			for _, l := range strings.Split(lookup.result, "\n") {
				lines = append(lines, "  "+l)
			}
		} else {
			lines = append(lines, "  Lookup: "+lookup.query+"_")
			lines = append(lines, "  Enter to search")
		}
	}
	if gc.visible {
		if gc.loading {
			lines = append(lines, "  GC: running…")
		} else if gc.result != "" {
			lines = append(lines, "  "+gc.result)
		} else {
			lines = append(lines, "  GC artifacts? [y] yes  [n] no")
		}
	}

	lines = append(lines, "")
	lines = append(lines, "  Esc close · [R] rollback · [l] lookup · [g] gc")

	return padPopupLines(lines, w, h)
}

// ── helpers ──────────────────────────────────────────────────

// padPopupLines truncates / pads the rendered lines to the
// popup's content area (h-2).  We intentionally do not strip
// ANSI escapes before measuring width; min-tui's renderer
// already handles that for the writeRow calls.
func padPopupLines(lines []string, w, h int) []string {
	max := h - 2
	if max < 1 {
		max = 1
	}
	if len(lines) > max {
		lines = lines[:max]
	}
	for len(lines) < max {
		lines = append(lines, "")
	}
	_ = w // width is currently a soft hint; min-tui clips to
	// the actual border at render time.  We keep the
	// parameter in the signature so future renderers can
	// use it for column alignment.
	return lines
}

// noopPopupKey is a default OnKey that ignores every key.
// Used by transient / loading popups where Esc/Ctrl+C are
// sufficient to dismiss.
func noopPopupKey(minitui.KeyEvent) minitui.PopupAction {
	return minitui.PopupPassthrough
}

// truncateForPopup returns s clipped to max runes.  Used by
// the detail / error renderers to keep long messages from
// overflowing the popup width.
func truncateForPopup(s string, max int) string {
	if max <= 0 {
		return ""
	}
	// Count runes so a CJK title doesn't get cut mid-codepoint.
	count := 0
	for i := range s {
		if count == max {
			return s[:i] + "…"
		}
		count++
	}
	return s
}
