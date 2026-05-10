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
			m.appendOverlay(simpleFeedItem(feedError, "snapshot-error", "Snapshot error", msg.Err.Error(), "", true))
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
		return m, m.fetchContextSummaryCmd()
	case contextSummaryLoadedMsg:
		if msg.Err != nil {
			return m, nil
		}
		m.contextSummary = msg.Summary
		m.refreshViewport(false)
		return m, nil
	case submitFinishedMsg:
		m.submitting = false
		if msg.Err != nil {
			m.stopWorking()
			m.appendOverlay(simpleFeedItem(feedError, "submit-error", "Turn error", msg.Err.Error(), "", true))
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
		return m, tea.Batch(m.fetchSnapshotCmd(), m.fetchContextSummaryCmd())
	case permissionFinishedMsg:
		m.resolvingPermission = false
		if msg.Err != nil {
			m.appendOverlay(simpleFeedItem(feedError, "permission-error", "Permission error", msg.Err.Error(), "", true))
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
		return m, tea.Quit
	case "ctrl+l":
		m.status = "Refreshing session snapshot..."
		return m, m.fetchSnapshotCmd()
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
		switch msg.String() {
		case "enter", " ":
			m.toggleSelectedItem()
			return m, nil
		case "y":
			m.copySelectedItem()
			return m, nil
		case "a":
			return m, m.approveSelectedPermissionCmd(tools.PermissionGrantOnce)
		case "s":
			return m, m.approveSelectedPermissionCmd(tools.PermissionGrantSession)
		case "x":
			return m, m.denySelectedPermissionCmd()
		case "j":
			if !m.selectAdjacentItem(1) {
				m.viewport.LineDown(1)
			}
			m.syncAutoFollow()
			m.reconcileSelectedItem()
			return m, nil
		case "k":
			if !m.selectAdjacentItem(-1) {
				m.viewport.LineUp(1)
			}
			m.syncAutoFollow()
			m.reconcileSelectedItem()
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
	m.reconcileSelectedItem()
	return true
}
