package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tim5wang/godex/internal/domain/events"
)

func (m *model) handleEvent(event events.Event) []tea.Cmd {
	switch payload := event.Payload.(type) {
	case events.MessagePayload:
		if event.Type == events.EventUserMessageAccepted {
			title := "You"
			if payload.Sender != "" && payload.Sender != m.cfg.LeadName {
				title = payload.Sender
			}
			m.upsertOverlay(feedItem{
				ID:          "user:" + event.TurnID,
				Kind:        feedUser,
				Title:       title,
				Body:        payload.Text,
				Summary:     firstSummaryLine(payload.Text),
				TurnID:      event.TurnID,
				RuntimeOnly: true,
				CreatedAt:   event.Timestamp,
			})
			m.status = fmt.Sprintf("Accepted message at %s", formatClock(event.Timestamp))
		}
	case events.TextPayload:
		switch event.Type {
		case events.EventAssistantTextDelta:
			m.appendAssistantDelta(event.TurnID, payload.Text, event.Timestamp)
			m.status = "Writing response..."
		case events.EventAssistantMessageComplete:
			m.status = "Assistant replied"
		}
	case events.ToolCallPayload:
		switch event.Type {
		case events.EventToolCallStarted:
			if payload.Name == "todo_write" {
				break
			}
			item := newToolItem(toolRuntimeKey(event.TurnID, payload), event.TurnID, payload.Name, payload.Input, "", "", true, true)
			item.CreatedAt = event.Timestamp
			m.upsertOverlay(item)
			m.selectedItemID = item.ID
			m.status = "Running tool " + payload.Name
			if !m.working {
				return []tea.Cmd{m.startWorking("")}
			}
		case events.EventToolCallFinished:
			if payload.Name == "todo_write" && strings.TrimSpace(payload.Error) == "" {
				break
			}
			item := newToolItem(toolRuntimeKey(event.TurnID, payload), event.TurnID, payload.Name, payload.Input, payload.Output, payload.Error, false, true)
			item.CreatedAt = event.Timestamp
			m.upsertOverlay(item)
			m.selectedItemID = item.ID
			if payload.Error != "" {
				m.status = "Tool failed: " + payload.Name
			} else {
				m.status = "Finished tool " + payload.Name
			}
		}
	case events.TodoListPayload:
		if event.Type == events.EventTodoListUpdated {
			body := payload.RenderPlain()
			item := simpleFeedItem(feedTodo, "todo:"+event.TurnID, "Todo list", body, event.TurnID, true)
			item.CreatedAt = event.Timestamp
			m.upsertOverlay(item)
			m.selectedItemID = item.ID
			m.status = "Todo list " + payload.Summary()
		}
	case events.CommandPayload:
		if event.Type == events.EventCommandCompleted {
			kind := feedCommand
			title := "/" + payload.Name
			body := strings.TrimSpace(payload.Output)
			if body == "" {
				body = "Command completed."
			}
			if payload.Error != "" {
				kind = feedError
				title = "Command error"
				body = payload.Error
			}
			item := simpleFeedItem(kind, "command:"+event.TurnID+":"+payload.Name, title, body, event.TurnID, true)
			item.CreatedAt = event.Timestamp
			m.upsertOverlay(item)
			m.stopWorking()
			m.submitting = false
			m.status = title
		}
	case events.NoticePayload:
		switch event.Type {
		case events.EventWarningRaised:
			item := simpleFeedItem(feedWarning, "warning:"+event.TurnID+":"+payload.Message, "Warning", payload.Message, event.TurnID, true)
			item.CreatedAt = event.Timestamp
			m.upsertOverlay(item)
			m.status = "Warning received"
		case events.EventErrorRaised:
			item := simpleFeedItem(feedError, "error:"+event.TurnID+":"+payload.Message, "Error", payload.Message, event.TurnID, true)
			item.CreatedAt = event.Timestamp
			m.upsertOverlay(item)
			m.status = "Error received"
		}
	case events.SnapshotPayload:
		if event.Type == events.EventSnapshotReady {
			m.status = "Refreshing session snapshot..."
			return []tea.Cmd{m.fetchSnapshotCmd()}
		}
	case events.RunnerPhasePayload:
		if event.Type == events.EventRunnerPhaseChanged {
			m.activePhase = payload.Phase
			m.activeToolName = payload.ToolName
			m.recordModelCallEvent(event)
			if strings.TrimSpace(payload.Message) != "" {
				m.status = payload.Message
			}
		}
	case events.TurnPayload:
		if event.Type == events.EventTurnCompleted {
			m.submitting = false
			m.stopWorking()
			m.status = "Turn " + payload.Status
			m.refreshViewport(false)
			return []tea.Cmd{m.fetchContextSummaryCmd()}
		}
	}

	m.refreshViewport(false)
	return nil
}

func (m *model) startWorking(status string) tea.Cmd {
	if status != "" {
		m.status = status
	}
	if m.working {
		return nil
	}
	m.working = true
	m.workingSince = m.now()
	m.heartbeatFrame = 0
	return tickHeartbeat()
}

func (m *model) stopWorking() {
	m.working = false
	m.workingSince = time.Time{}
	m.heartbeatFrame = 0
}

func (m *model) toggleFocus() tea.Cmd {
	if m.focus == focusComposer {
		m.focus = focusFeed
		m.composer.Blur()
		return nil
	}
	m.focus = focusComposer
	return m.composer.Focus()
}

func (m *model) appendAssistantDelta(turnID, text string, createdAt time.Time) {
	if text == "" {
		return
	}
	id := "assistant:" + turnID
	idx := m.overlayIndex(id)
	if idx >= 0 {
		m.overlayItems[idx].Body += text
		m.overlayItems[idx].Summary = firstSummaryLine(m.overlayItems[idx].Body)
		if m.overlayItems[idx].CreatedAt.IsZero() {
			m.overlayItems[idx].CreatedAt = createdAt
		}
		return
	}
	m.overlayItems = append(m.overlayItems, feedItem{
		ID:          id,
		Kind:        feedAssistant,
		Title:       botName,
		Body:        text,
		Summary:     firstSummaryLine(text),
		TurnID:      turnID,
		RuntimeOnly: true,
		CreatedAt:   createdAt,
	})
}

func (m *model) upsertOverlay(item feedItem) {
	if item.ID == "" {
		m.overlayItems = append(m.overlayItems, item)
		return
	}
	if idx := m.overlayIndex(item.ID); idx >= 0 {
		item.Expanded = m.overlayItems[idx].Expanded
		m.overlayItems[idx] = item
		return
	}
	m.overlayItems = append(m.overlayItems, item)
}

func (m *model) appendOverlay(item feedItem) {
	m.overlayItems = append(m.overlayItems, item)
}

func (m *model) overlayIndex(id string) int {
	for i := range m.overlayItems {
		if m.overlayItems[i].ID == id {
			return i
		}
	}
	return -1
}

func (m *model) expansionState() map[string]bool {
	state := make(map[string]bool)
	for _, item := range m.allItems() {
		if item.Foldable {
			state[item.ID] = item.Expanded
		}
	}
	return state
}

func (m *model) refreshViewport(forceBottom bool) {
	oldOffset := m.viewport.YOffset
	content, spans := m.renderFeedContent()
	m.viewport.SetContent(content)
	m.itemSpans = spans

	if forceBottom || m.autoFollow {
		m.viewport.GotoBottom()
	} else {
		m.viewport.SetYOffset(clamp(oldOffset, 0, maxInt(0, m.viewport.TotalLineCount()-m.viewport.Height)))
	}
	m.reconcileSelectedItem()
}
