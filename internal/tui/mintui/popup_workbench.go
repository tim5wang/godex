package mintui

import (
	"context"
	"fmt"
	"strings"
	"sync"

	rtbackend "github.com/tim5wang/godex/internal/services/backend"
	minitui "github.com/tim5wang/min-tui"
)

// ── workbench state ─────────────────────────────────────────

type workbenchTab int

const (
	wbTabTask workbenchTab = iota
	wbTabWorkers
	wbTabCount
)

func (t workbenchTab) label() string {
	switch t {
	case wbTabTask:
		return "Tasks"
	case wbTabWorkers:
		return "Workers"
	}
	return ""
}

type wbOutcomeStatus int

const (
	wbStatusIdle wbOutcomeStatus = iota
	wbStatusRunning
	wbStatusBlocked
	wbStatusReadyForReview
	wbStatusMerged
	wbStatusFailed
)

func wbStatusLabel(s wbOutcomeStatus) string {
	switch s {
	case wbStatusRunning:
		return "Running"
	case wbStatusBlocked:
		return "Blocked"
	case wbStatusReadyForReview:
		return "Ready"
	case wbStatusMerged:
		return "Merged"
	case wbStatusFailed:
		return "Failed"
	default:
		return "Idle"
	}
}

type workbenchOutcome struct {
	Status      wbOutcomeStatus
	Title       string
	Detail      string
	LongTaskID  string
	WorkerID    string
	WorkerTitle string
	HasLongTask bool
	HasWorker   bool
}

// workbenchUI is the per-session state backing the Ctrl+W workbench
// popup.  It caches longtask + subagent rows and builds a merged
// outcome list for the Task tab.
type workbenchUI struct {
	mu sync.Mutex

	tab     workbenchTab
	loading bool
	lastErr error

	longTasks []rtbackend.LongTaskRow
	subagents []rtbackend.SubagentRow
	outcomes  []workbenchOutcome

	cursor int
}

func (wb *workbenchUI) reset() {
	wb.mu.Lock()
	defer wb.mu.Unlock()
	wb.cursor = 0
	wb.tab = wbTabTask
}

func (wb *workbenchUI) setLoading() {
	wb.mu.Lock()
	defer wb.mu.Unlock()
	wb.loading = true
	wb.lastErr = nil
}

func (wb *workbenchUI) setData(longTasks []rtbackend.LongTaskRow, subagents []rtbackend.SubagentRow) {
	wb.mu.Lock()
	defer wb.mu.Unlock()
	wb.longTasks = longTasks
	wb.subagents = subagents
	wb.outcomes = buildWorkbenchOutcomes(longTasks, subagents)
	wb.loading = false
	wb.lastErr = nil
	if wb.cursor >= wb.itemCount() {
		wb.cursor = maxInt(0, wb.itemCount()-1)
	}
}

func (wb *workbenchUI) setErr(err error) {
	wb.mu.Lock()
	defer wb.mu.Unlock()
	wb.lastErr = err
	wb.loading = false
	wb.cursor = 0
}

func (wb *workbenchUI) itemCount() int {
	switch wb.tab {
	case wbTabTask:
		return len(wb.outcomes)
	case wbTabWorkers:
		return len(wb.subagents)
	}
	return 0
}

// ── outcome builder ──────────────────────────────────────────

func buildWorkbenchOutcomes(longTasks []rtbackend.LongTaskRow, subagents []rtbackend.SubagentRow) []workbenchOutcome {
	used := make(map[int]struct{})
	outcomes := make([]workbenchOutcome, 0, len(longTasks)+len(subagents))

	for _, lt := range longTasks {
		o := outcomeFromLongTaskRow(lt)
		if wi, ok := matchSubagentRow(lt, subagents, used); ok {
			used[wi] = struct{}{}
			sa := subagents[wi]
			o.HasWorker = true
			o.WorkerID = sa.JobID
			o.WorkerTitle = firstNonEmpty(sa.DisplayTitle, sa.Objective)
			// Merge status: worker status wins for running/blocked/ready
			ws := classifySubagentStatus(sa)
			if ws != wbStatusIdle {
				o.Status = ws
				o.Detail = firstNonEmpty(sa.LastMessage, sa.Result)
			}
		}
		outcomes = append(outcomes, o)
	}

	for i, sa := range subagents {
		if _, ok := used[i]; ok {
			continue
		}
		outcomes = append(outcomes, workbenchOutcome{
			Status:      classifySubagentStatus(sa),
			Title:       firstNonEmpty(sa.DisplayTitle, sa.Objective, sa.JobID),
			Detail:      firstNonEmpty(sa.LastMessage, sa.Result),
			WorkerID:    sa.JobID,
			WorkerTitle: firstNonEmpty(sa.DisplayTitle, sa.Objective),
			HasWorker:   true,
		})
	}
	return outcomes
}

func outcomeFromLongTaskRow(lt rtbackend.LongTaskRow) workbenchOutcome {
	status := classifyLongTaskStatus(lt)
	return workbenchOutcome{
		Status:      status,
		Title:       firstNonEmpty(lt.Project, lt.LastStoryTitle, lt.Description, lt.WorkflowID),
		Detail:      fmt.Sprintf("%d/%d done", lt.Completed, lt.Total),
		LongTaskID:  lt.WorkflowID,
		HasLongTask: true,
	}
}

func matchSubagentRow(lt rtbackend.LongTaskRow, subagents []rtbackend.SubagentRow, used map[int]struct{}) (int, bool) {
	for i, sa := range subagents {
		if _, ok := used[i]; ok {
			continue
		}
		// Match by checking if worker's Objective/DisplayTitle
		// contains the longtask's title/project.
		if matchText(lt.Project, sa.Objective) ||
			matchText(lt.Project, sa.DisplayTitle) ||
			matchText(lt.Description, sa.Objective) ||
			matchText(lt.Description, sa.DisplayTitle) {
			return i, true
		}
	}
	return 0, false
}

func matchText(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	return strings.Contains(strings.ToLower(a), strings.ToLower(b)) ||
		strings.Contains(strings.ToLower(b), strings.ToLower(a))
}

func classifyLongTaskStatus(lt rtbackend.LongTaskRow) wbOutcomeStatus {
	s := strings.ToLower(lt.Status)
	if lt.Running > 0 || s == "running" {
		return wbStatusRunning
	}
	if lt.Failed > 0 || s == "failed" || s == "error" {
		return wbStatusFailed
	}
	if s == "completed" || (lt.Completed > 0 && lt.Total > 0 && lt.Completed == lt.Total) {
		return wbStatusMerged
	}
	if s == "blocked" {
		return wbStatusBlocked
	}
	return wbStatusIdle
}

func classifySubagentStatus(sa rtbackend.SubagentRow) wbOutcomeStatus {
	merge := strings.ToLower(sa.MergeStatus)
	if merge == "merged" || merge == "no_changes" {
		return wbStatusMerged
	}
	s := strings.ToLower(sa.Status)
	switch s {
	case "pending_approval":
		return wbStatusBlocked
	case "running", "pending", "resuming":
		return wbStatusRunning
	case "completed":
		return wbStatusReadyForReview
	case "failed", "cancelled", "canceled", "error", "timeout", "interrupted":
		return wbStatusFailed
	}
	if merge != "" {
		return wbStatusReadyForReview
	}
	return wbStatusIdle
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ── popup pushers ───────────────────────────────────────────

// openWorkbench is the entry point for Ctrl+W.  It pushes the
// workbench popup in loading state and fetches data.
func (s *Session) openWorkbench(ctx context.Context) {
	if s.tui == nil || s.backend == nil {
		return
	}
	s.workbench.reset()
	s.workbench.setLoading()
	s.tui.PushPopup(s.buildWorkbenchPopup())
	go s.refreshWorkbench(ctx)
}

func (s *Session) refreshWorkbench(ctx context.Context) {
	ltRows, err := s.backend.ListLongTasks(ctx, s.sessionID)
	if err != nil {
		ltRows = nil
	}
	saRows, err := s.backend.ListSubagents(ctx, s.sessionID)
	if err != nil {
		saRows = nil
	}

	if s.backend != nil && ltRows != nil && saRows != nil {
		s.workbench.setData(ltRows, saRows)
	} else if err != nil {
		s.workbench.setErr(err)
	} else {
		s.workbench.setData(ltRows, saRows)
	}

	if s.tui != nil {
		_, _ = s.tui.WriteString("")
	}
}

// ── popup builder ───────────────────────────────────────────

func (s *Session) buildWorkbenchPopup() minitui.Popup {
	return minitui.Popup{
		Title: "Workbench (Ctrl+W)",
		Width: 70, Height: 22,
		Render: func(w, h int) []string {
			return s.renderWorkbench(w, h)
		},
		OnKey: func(k minitui.KeyEvent) minitui.PopupAction {
			return s.handleWorkbenchKey(k)
		},
	}
}

// ── renderer ────────────────────────────────────────────────

func (s *Session) renderWorkbench(w, h int) []string {
	s.workbench.mu.Lock()
	loading := s.workbench.loading
	lastErr := s.workbench.lastErr
	tab := s.workbench.tab
	cursor := s.workbench.cursor
	outcomes := s.workbench.outcomes
	subagents := s.workbench.subagents
	s.workbench.mu.Unlock()

	contentH := h - 4 // tabs bar + header + footer
	if contentH < 1 {
		contentH = 1
	}

	if loading {
		lines := []string{"", "  Loading…", ""}
		return padPopupLines(lines, w, h)
	}
	if lastErr != nil {
		lines := []string{
			"",
			"  Failed to load:",
			"  " + truncateForPopup(lastErr.Error(), w-4),
			"",
			"  [r] refresh · Esc close",
		}
		return padPopupLines(lines, w, h)
	}

	// Tabs bar
	tabs := make([]string, wbTabCount)
	for i := workbenchTab(0); i < wbTabCount; i++ {
		if i == tab {
			tabs[i] = fmt.Sprintf("[%s]", i.label())
		} else {
			tabs[i] = fmt.Sprintf(" %s ", i.label())
		}
	}
	tabBar := "  " + strings.Join(tabs, "  ")
	lines := []string{tabBar, ""}

	switch tab {
	case wbTabTask:
		lines = append(lines, s.renderWorkbenchTaskTab(outcomes, cursor, w)...)
	case wbTabWorkers:
		lines = append(lines, s.renderWorkbenchWorkersTab(subagents, cursor, w)...)
	}

	// Footer
	if s.workbench.itemCount() > 0 {
		lines = append(lines, "")
		lines = append(lines, "  [1/2] tabs · [↑↓] navigate · [r] refresh · Esc close")
	} else {
		lines = append(lines, "")
		lines = append(lines, "  [1/2] tabs · [r] refresh · Esc close")
	}

	return padPopupLines(lines, w, h)
}

func (s *Session) renderWorkbenchTaskTab(outcomes []workbenchOutcome, cursor, w int) []string {
	if len(outcomes) == 0 {
		return []string{"  No active longtasks or subagents.", "  Start a longtask to see it here."}
	}

	// Group by status
	plan := make([]int, 0)
	active := make([]int, 0)
	review := make([]int, 0)
	for i, o := range outcomes {
		switch o.Status {
		case wbStatusRunning, wbStatusBlocked:
			active = append(active, i)
		case wbStatusReadyForReview, wbStatusMerged, wbStatusFailed:
			review = append(review, i)
		default:
			plan = append(plan, i)
		}
	}

	renderGroup := func(label string, indices []int) []string {
		if len(indices) == 0 {
			return nil
		}
		lines := []string{fmt.Sprintf("  ── %s ──", label)}
		for _, idx := range indices {
			prefix := "  "
			if idx == cursor {
				prefix = "▶ "
			}
			o := outcomes[idx]
			line := fmt.Sprintf("%s%-8s %s", prefix, wbStatusLabel(o.Status), truncateForPopup(o.Title, w-14))
			if o.Detail != "" {
				line += "  " + truncateForPopup(o.Detail, w-20)
			}
			lines = append(lines, line)
		}
		return lines
	}

	var lines []string
	lines = append(lines, renderGroup("Active", active)...)
	if len(lines) > 0 && len(plan) > 0 {
		lines = append(lines, "")
	}
	lines = append(lines, renderGroup("Plan", plan)...)
	if len(lines) > 0 && len(review) > 0 {
		lines = append(lines, "")
	}
	lines = append(lines, renderGroup("Review", review)...)
	return lines
}

func (s *Session) renderWorkbenchWorkersTab(subagents []rtbackend.SubagentRow, cursor, w int) []string {
	if len(subagents) == 0 {
		return []string{"  No durable workers in this session."}
	}
	lines := make([]string, 0, len(subagents)+1)
	for i, sa := range subagents {
		prefix := "  "
		if i == cursor {
			prefix = "▶ "
		}
		status := strings.TrimSpace(sa.Status)
		if sa.MergeStatus != "" {
			status = sa.MergeStatus
		}
		title := firstNonEmpty(sa.DisplayTitle, sa.Objective, sa.JobID)
		line := fmt.Sprintf("%s%-12s %s", prefix, truncateForPopup(status, 11), truncateForPopup(title, w-18))
		lines = append(lines, line)
	}
	return lines
}

// ── key handler ─────────────────────────────────────────────

func (s *Session) handleWorkbenchKey(k minitui.KeyEvent) minitui.PopupAction {
	switch {
	case k.Rune == '1':
		s.workbench.mu.Lock()
		s.workbench.tab = wbTabTask
		s.workbench.cursor = 0
		s.workbench.mu.Unlock()
		return minitui.PopupUpdate
	case k.Rune == '2':
		s.workbench.mu.Lock()
		s.workbench.tab = wbTabWorkers
		s.workbench.cursor = 0
		s.workbench.mu.Unlock()
		return minitui.PopupUpdate
	case k.Special == minitui.KeyUp:
		s.workbench.mu.Lock()
		if s.workbench.cursor > 0 {
			s.workbench.cursor--
		}
		s.workbench.mu.Unlock()
		return minitui.PopupUpdate
	case k.Special == minitui.KeyDown:
		s.workbench.mu.Lock()
		n := s.workbench.itemCount()
		if s.workbench.cursor < n-1 {
			s.workbench.cursor++
		}
		s.workbench.mu.Unlock()
		return minitui.PopupUpdate
	case k.Rune == 'r' || k.Rune == 'R':
		s.workbench.setLoading()
		go s.refreshWorkbench(s.runCtx)
		return minitui.PopupUpdate
	case k.Enter:
		// Open longtask detail for selected outcome — must use
		// goroutine to avoid PushPopup-from-OnKey deadlock
		// (same as the longtask list Enter handler).
		s.workbench.mu.Lock()
		tab := s.workbench.tab
		cursor := s.workbench.cursor
		s.workbench.mu.Unlock()
		if tab == wbTabTask {
			s.workbench.mu.Lock()
			if cursor >= 0 && cursor < len(s.workbench.outcomes) {
				o := s.workbench.outcomes[cursor]
				if o.HasLongTask && o.LongTaskID != "" {
					s.workbench.mu.Unlock()
					go s.pushLongTaskDetail(s.runCtx, o.LongTaskID)
					return minitui.PopupUpdate
				}
			}
			s.workbench.mu.Unlock()
		}
		return minitui.PopupPassthrough
	default:
		return minitui.PopupPassthrough
	}
}
