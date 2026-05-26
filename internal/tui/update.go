package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tim5wang/godex/internal/tools"
)

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resize()
		m.refreshViewport(false)
		return m, nil
	case runtimeEventMsg:
		return m, tea.Batch(m.handleEvent(msg.Event)...)
	case snapshotLoadedMsg:
		if msg.Err != nil {
			m.appendOverlay(feedItem{
			ID:          "snapshot-error",
			Kind:        feedError,
			Title:       "Snapshot error",
			Body:        msg.Err.Error(),
			Summary:     firstSummaryLine(msg.Err.Error()),
			RuntimeOnly: true,
			CreatedAt:   m.now(),
		})
			m.status = "Snapshot refresh failed"
			m.refreshViewport(false)
			return m, nil
		}
		expanded := m.expansionState()
		dropCommandOverlays := assistantMessageCount(msg.Snapshot.Messages) > assistantMessageCount(m.snapshot.Messages)
		m.snapshot = msg.Snapshot
		m.locator = msg.Snapshot.Locator
		m.historyItems = snapshotToItems(msg.Snapshot.Messages, msg.Snapshot.PendingPermissions, expanded, m.sessionID)
		m.overlayItems = persistentOverlayItems(m.overlayItems, dropCommandOverlays)
		m.activePhase = msg.Snapshot.ActivePhase
		m.rebuildModelCallStats()
		if msg.Snapshot.Running {
			if cmd := m.startWorking(""); cmd != nil {
				m.refreshViewport(false)
				return m, tea.Batch(cmd, m.fetchContextSummaryCmd())
			}
		}
		m.status = fmt.Sprintf("Snapshot refreshed at %s", formatClock(msg.Snapshot.UpdatedAt))
		m.refreshViewport(false)
		return m, tea.Batch(m.fetchContextSummaryCmd(), m.fetchWorkbenchCmd())
	case contextSummaryLoadedMsg:
		if msg.Err != nil {
			return m, nil
		}
		m.contextSummary = msg.Summary
		m.refreshViewport(false)
		return m, nil
	case workbenchLoadedMsg:
		if msg.Err != nil {
			m.workbenchErr = msg.Err
			m.status = "Task Center refresh failed"
			m.refreshViewport(false)
			return m, nil
		}
		m.longTasks = msg.LongTasks
		m.subagents = msg.Subagents
		m.workbenchErr = nil
		m.refreshViewport(false)
		return m, nil
	case submitFinishedMsg:
		m.submitting = false
		if msg.Err != nil {
			m.stopWorking()
			m.appendOverlay(feedItem{
			ID:          "submit-error",
			Kind:        feedError,
			Title:       "Turn error",
			Body:        msg.Err.Error(),
			Summary:     firstSummaryLine(msg.Err.Error()),
			RuntimeOnly: true,
			CreatedAt:   m.now(),
		})
			m.status = "Turn failed"
			m.refreshViewport(false)
		}
		return m, nil
	case commandFinishedMsg:
		m.submitting = false
		if msg.Err != nil {
			m.stopWorking()
			m.status = "Command failed"
			m.refreshViewport(false)
			return m, nil
		}
		m.status = "Command completed"
		return m, tea.Batch(m.fetchSnapshotCmd(), m.fetchContextSummaryCmd(), m.fetchWorkbenchCmd())
	case bashChunkMsg:
		// streaming update: upsert the running feed item with latest output
		m.upsertOverlay(feedItem{
			ID:          "bash:" + msg.command,
			Kind:        feedCommand,
			Title:       "/bash (running)",
			Body:        msg.chunk,
			Summary:     firstSummaryLine(msg.chunk),
			RuntimeOnly: true,
			CreatedAt:   m.now(),
		})
		m.refreshViewport(false)
		// continue listening for more chunks
		if m.bashCh != nil {
			return m, listenBashStream(m.bashCh)
		}
		return m, nil
	case bashFinishedMsg:
		m.submitting = false
		m.stopWorking()
		m.bashCancel = nil
		m.bashCh = nil
		kind := feedCommand
		title := "/bash"
		body := msg.output
		if msg.exitCode != 0 || msg.err != nil {
			kind = feedError
			title = "/bash (exit " + fmt.Sprintf("%d", msg.exitCode) + ")"
		}
		if body == "" {
			body = "(no output)"
		}
		m.upsertOverlay(feedItem{
			ID:          "bash:" + msg.command,
			Kind:        kind,
			Title:       title,
			Body:        body,
			Summary:     firstSummaryLine(body),
			RuntimeOnly: true,
			CreatedAt:   m.now(),
		})
		m.status = title
		m.refreshViewport(false)
		return m, nil
	case permissionFinishedMsg:
		m.resolvingPermission = false
		if msg.Err != nil {
			m.appendOverlay(feedItem{
			ID:          "permission-error",
			Kind:        feedError,
			Title:       "Permission error",
			Body:        msg.Err.Error(),
			Summary:     firstSummaryLine(msg.Err.Error()),
			RuntimeOnly: true,
			CreatedAt:   m.now(),
		})
			m.status = "Permission action failed"
			m.refreshViewport(false)
			return m, nil
		}
		title, body := formatPermissionResolution(msg.Resolution)
		m.appendOverlay(simpleFeedItem(feedCommand, "permission:"+msg.Resolution.RequestID, title, body, "", true))
		m.status = title
		cmds := []tea.Cmd{m.fetchSnapshotCmd()}
		if cmd := m.startWorking(title); cmd != nil {
			cmds = append(cmds, cmd)
		}
		m.refreshViewport(false)
		return m, tea.Batch(cmds...)
	case heartbeatTickMsg:
		if !m.working {
			return m, nil
		}
		m.heartbeatFrame = (m.heartbeatFrame + 1) % len(heartbeatFrames)
		return m, tickHeartbeat()
	case tea.MouseMsg:
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		m.syncAutoFollow()
		m.reconcileSelectedItem()
		return m, cmd
	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	if m.focus == focusComposer {
		var cmd tea.Cmd
		m.composer, cmd = m.composer.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		if m.bashCancel != nil {
			m.bashCancel()
			m.bashCancel = nil
			m.status = "Bash cancelled"
			m.stopWorking()
			m.submitting = false
			m.refreshViewport(false)
			return m, nil
		}
		return m, tea.Quit
	case "ctrl+l":
		m.status = "Refreshing session snapshot..."
		return m, tea.Batch(m.fetchSnapshotCmd(), m.fetchWorkbenchCmd())
	case "tab":
		return m, m.toggleFocus()
	}

	if m.focus == focusComposer && m.handleInputHistory(msg.String()) {
		return m, nil
	}

	if handled := m.handleFeedNavigation(msg); handled {
		return m, nil
	}

	if m.focus == focusFeed {
		if tab, ok := parseWorkbenchTabKey(msg.String()); ok {
			m.setWorkbenchTab(tab)
			return m, nil
		}
	}

	if m.focus == focusFeed {
		switch msg.String() {
		case "enter", " ":
			m.toggleSelectedItem()
			return m, nil
		case "y":
			m.copySelectedItem()
			return m, nil
		case "a":
			return m, m.approveSelectedPermissionCmd(tools.PermissionGrantOnce)
		case "u":
			return m, m.approveSelectedPermissionCmd(tools.PermissionGrantTask)
		case "p":
			return m, m.approveSelectedPermissionCmd(tools.PermissionGrantPattern)
		case "t":
			return m, m.approveSelectedPermissionCmd(tools.PermissionGrantScope("timebox:10m"))
		case "s":
			return m, m.approveSelectedPermissionCmd(tools.PermissionGrantSession)
		case "x":
			return m, m.denySelectedPermissionCmd()
		case "j":
			if !m.selectAdjacentItem(1) {
				m.viewport.LineDown(1)
			}
			m.autoFollow = false
			m.refreshViewport(false)
			return m, nil
		case "k":
			if !m.selectAdjacentItem(-1) {
				m.viewport.LineUp(1)
			}
			m.autoFollow = false
			m.refreshViewport(false)
			return m, nil
		}
		return m, nil
	}

	switch msg.String() {
	case "alt+enter":
		m.composer.InsertRune('\n')
		m.resetInputHistoryNavigation()
		return m, nil
	case "enter":
		cmd := m.submitComposer()
		if cmd != nil {
			return m, cmd
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.composer, cmd = m.composer.Update(msg)
	m.resetInputHistoryNavigation()
	return m, cmd
}

func parseWorkbenchTabKey(key string) (workbenchTab, bool) {
	switch key {
	case "1":
		return workbenchTabTask, true
	case "2":
		return workbenchTabWorkers, true
	case "3":
		return workbenchTabGraph, true
	case "4":
		return workbenchTabDiff, true
	case "5":
		return workbenchTabLogs, true
	default:
		return workbenchTabTask, false
	}
}

func (m *model) setWorkbenchTab(tab workbenchTab) {
	if m.activeWorkbenchTab == tab {
		return
	}
	m.activeWorkbenchTab = tab
	m.autoFollow = true
	m.refreshViewport(true)
}

func (m *model) handleFeedNavigation(msg tea.KeyMsg) bool {
	allowWhileComposing := strings.TrimSpace(m.composer.Value()) == ""
	if m.focus != focusFeed && !allowWhileComposing {
		return false
	}

	switch msg.String() {
	case "up":
		m.viewport.LineUp(1)
	case "down":
		m.viewport.LineDown(1)
	case "pgup":
		m.viewport.PageUp()
	case "pgdown":
		m.viewport.PageDown()
	case "ctrl+u":
		m.viewport.HalfPageUp()
	case "ctrl+d":
		m.viewport.HalfPageDown()
	case "home":
		m.viewport.GotoTop()
	case "end":
		m.viewport.GotoBottom()
	default:
		return false
	}

	m.syncAutoFollow()
	m.refreshViewport(false)
	return true
}
