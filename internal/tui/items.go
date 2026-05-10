package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/platform/textutil"
	"github.com/tim5wang/godex/internal/tools"
)

func (m *model) allItems() []feedItem {
	items := make([]feedItem, 0, len(m.historyItems)+len(m.overlayItems))
	items = append(items, m.historyItems...)
	items = append(items, m.overlayItems...)
	sort.SliceStable(items, func(i, j int) bool {
		left := items[i].CreatedAt
		right := items[j].CreatedAt
		if left.IsZero() || right.IsZero() || left.Equal(right) {
			return false
		}
		return left.Before(right)
	})
	return items
}

func (m *model) syncAutoFollow() {
	m.autoFollow = m.viewport.AtBottom()
}

func (m *model) reconcileSelectedItem() {
	visibleStart := m.viewport.YOffset
	visibleEnd := visibleStart + maxInt(1, m.viewport.Height)

	if m.selectedItemID != "" && spanVisible(m.selectedItemID, m.itemSpans, visibleStart, visibleEnd) {
		return
	}

	for _, span := range m.itemSpans {
		if span.End >= visibleStart && span.Start < visibleEnd {
			m.selectedItemID = span.ID
			return
		}
	}

	for i := len(m.itemSpans) - 1; i >= 0; i-- {
		m.selectedItemID = m.itemSpans[i].ID
		return
	}
	m.selectedItemID = ""
}

func (m *model) toggleSelectedItem() {
	if m.selectedItemID == "" {
		return
	}

	for i := range m.overlayItems {
		if m.overlayItems[i].ID == m.selectedItemID && m.overlayItems[i].Foldable {
			m.overlayItems[i].Expanded = !m.overlayItems[i].Expanded
			m.refreshViewport(false)
			return
		}
	}
	for i := range m.historyItems {
		if m.historyItems[i].ID == m.selectedItemID && m.historyItems[i].Foldable {
			m.historyItems[i].Expanded = !m.historyItems[i].Expanded
			m.refreshViewport(false)
			return
		}
	}
}

func (m *model) selectAdjacentItem(delta int) bool {
	if len(m.itemSpans) == 0 {
		return false
	}
	current := -1
	for i, span := range m.itemSpans {
		if span.ID == m.selectedItemID {
			current = i
			break
		}
	}
	if current == -1 {
		m.reconcileSelectedItem()
		for i, span := range m.itemSpans {
			if span.ID == m.selectedItemID {
				current = i
				break
			}
		}
	}
	if current == -1 {
		return false
	}
	next := clamp(current+delta, 0, len(m.itemSpans)-1)
	m.selectedItemID = m.itemSpans[next].ID
	m.ensureSpanVisible(m.itemSpans[next])
	return true
}

func (m *model) ensureSpanVisible(span itemSpan) {
	visibleStart := m.viewport.YOffset
	visibleEnd := visibleStart + maxInt(1, m.viewport.Height) - 1
	switch {
	case span.Start < visibleStart:
		m.viewport.SetYOffset(span.Start)
	case span.End > visibleEnd:
		m.viewport.SetYOffset(clamp(span.End-maxInt(1, m.viewport.Height)+1, 0, maxInt(0, m.viewport.TotalLineCount()-m.viewport.Height)))
	}
}

func (m *model) approveSelectedPermissionCmd(scope tools.PermissionGrantScope) tea.Cmd {
	if m.resolvingPermission {
		return nil
	}
	pending, ok := m.selectedPendingPermission()
	if !ok {
		return nil
	}
	m.resolvingPermission = true
	m.status = "Resolving permission approval..."
	return func() tea.Msg {
		resolution, err := m.backend.ApprovePermission(m.ctx, m.sessionID, pending.ID, scope)
		return permissionFinishedMsg{Resolution: resolution, Err: err}
	}
}

func (m *model) denySelectedPermissionCmd() tea.Cmd {
	if m.resolvingPermission {
		return nil
	}
	pending, ok := m.selectedPendingPermission()
	if !ok {
		return nil
	}
	m.resolvingPermission = true
	m.status = "Denying permission..."
	return func() tea.Msg {
		resolution, err := m.backend.DenyPermission(m.ctx, m.sessionID, pending.ID, "Denied from TUI")
		return permissionFinishedMsg{Resolution: resolution, Err: err}
	}
}

func (m *model) selectedPendingPermission() (tools.PendingPermission, bool) {
	if m.selectedItemID == "" {
		return tools.PendingPermission{}, false
	}
	for _, item := range m.allItems() {
		if item.ID != m.selectedItemID || item.Permission == nil {
			continue
		}
		return *item.Permission, true
	}
	return tools.PendingPermission{}, false
}

func simpleFeedItem(kind feedItemKind, id, title, body, turnID string, runtimeOnly bool) feedItem {
	return feedItem{
		ID:          id,
		Kind:        kind,
		Title:       title,
		Body:        body,
		Summary:     firstSummaryLine(body),
		TurnID:      turnID,
		RuntimeOnly: runtimeOnly,
	}
}

func newToolItem(id, turnID, name string, input map[string]interface{}, output, err string, running, runtimeOnly bool) feedItem {
	status := "finished"
	if running {
		status = "running"
	}
	if err != "" {
		status = "failed"
	}
	return feedItem{
		ID:          id,
		Kind:        feedTool,
		Title:       name,
		Summary:     summarizeTool(input, output, err, running),
		Status:      status,
		TurnID:      turnID,
		Input:       cloneToolInput(input),
		Output:      strings.TrimSpace(output),
		Error:       strings.TrimSpace(err),
		Foldable:    true,
		RuntimeOnly: runtimeOnly,
	}
}

func newPermissionItem(pending tools.PendingPermission, runtimeOnly bool, sessionID string) feedItem {
	title := "Permission approval"
	if toolName := strings.TrimSpace(pending.Request.ToolName); toolName != "" {
		title = "Permission · " + toolName
	}
	pendingCopy := pending
	return feedItem{
		ID:          "permission:" + pending.ID,
		Kind:        feedPermission,
		Title:       title,
		Summary:     summarizePendingPermission(pending),
		Status:      "pending approval",
		Foldable:    true,
		RuntimeOnly: runtimeOnly,
		SessionID:   strings.TrimSpace(sessionID),
		Permission:  &pendingCopy,
	}
}

func snapshotToItems(messages []protocol.Message, pending []tools.PendingPermission, expanded map[string]bool, sessionID ...string) []feedItem {
	items := make([]feedItem, 0, len(messages)+len(pending))
	toolIndexes := make(map[string]int)
	currentSessionID := ""
	if len(sessionID) > 0 {
		currentSessionID = strings.TrimSpace(sessionID[0])
	}

	for msgIndex, msg := range messages {
		if text := strings.TrimSpace(protocol.MessageText(msg)); text != "" {
			item := snapshotTextItem(msgIndex, msg, text)
			if expanded != nil {
				item.Expanded = expanded[item.ID]
			}
			items = append(items, item)
		}

		for blockIndex, block := range msg.Content {
			switch block.Type {
			case protocol.BlockToolUse:
				id := toolSnapshotID(msgIndex, blockIndex, block)
				item := newToolItem(id, "", block.Name, block.Input, "", "", true, false)
				item.CreatedAt = messageTimestamp(msg)
				if expanded != nil {
					item.Expanded = expanded[item.ID]
				}
				items = append(items, item)
				if block.ID != "" {
					toolIndexes[block.ID] = len(items) - 1
				}
			case protocol.BlockToolResult:
				if idx, ok := toolIndexes[block.ToolUseID]; ok {
					items[idx] = newToolItem(items[idx].ID, items[idx].TurnID, items[idx].Title, items[idx].Input, block.Content, "", false, false)
					items[idx].CreatedAt = messageTimestamp(msg)
					if expanded != nil {
						items[idx].Expanded = expanded[items[idx].ID]
					}
					continue
				}

				item := newToolItem(
					fmt.Sprintf("tool-result:%d:%d", msgIndex, blockIndex),
					"",
					"tool result",
					nil,
					block.Content,
					"",
					false,
					false,
				)
				item.CreatedAt = messageTimestamp(msg)
				if expanded != nil {
					item.Expanded = expanded[item.ID]
				}
				items = append(items, item)
			}
		}
	}

	for _, entry := range pending {
		item := newPermissionItem(entry, false, currentSessionID)
		if expanded != nil {
			item.Expanded = expanded[item.ID]
		}
		items = append(items, item)
	}

	return items
}

func snapshotTextItem(index int, msg protocol.Message, text string) feedItem {
	kind := feedUser
	title := "You"

	if msg.Role == protocol.RoleAssistant {
		kind = feedAssistant
		title = botName
	} else if msg.Metadata != nil && msg.Metadata.Kind != "" {
		kind = feedCommand
		title = textutil.Title(strings.ReplaceAll(string(msg.Metadata.Kind), "_", " "))
	}

	return feedItem{
		ID:        fmt.Sprintf("message:%d:%s", index, kind),
		Kind:      kind,
		Title:     title,
		Body:      text,
		Summary:   firstSummaryLine(text),
		CreatedAt: messageTimestamp(msg),
	}
}

func messageTimestamp(msg protocol.Message) time.Time {
	if msg.Metadata == nil {
		return time.Time{}
	}
	raw := strings.TrimSpace(msg.Metadata.Timestamp)
	if raw == "" {
		return time.Time{}
	}
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return parsed
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed
	}
	return time.Time{}
}

func assistantMessageCount(messages []protocol.Message) int {
	count := 0
	for _, msg := range messages {
		if msg.Role == protocol.RoleAssistant && strings.TrimSpace(protocol.MessageText(msg)) != "" {
			count++
		}
	}
	return count
}
