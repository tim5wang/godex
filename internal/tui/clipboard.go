package tui

import (
	"fmt"
	"strings"
)

func (m *model) copySelectedItem() {
	item, ok := m.selectedFeedItem()
	if !ok {
		m.status = "No feed item selected to copy"
		return
	}
	text := feedItemCopyText(item)
	if strings.TrimSpace(text) == "" {
		m.status = "Selected feed item has no text to copy"
		return
	}
	if m.clipboardWrite == nil {
		m.status = "Clipboard is unavailable"
		return
	}
	if err := m.clipboardWrite(text); err != nil {
		m.status = "Copy failed: " + err.Error()
		return
	}
	m.status = fmt.Sprintf("Copied %s to clipboard", item.Title)
}

func (m *model) selectedFeedItem() (feedItem, bool) {
	if strings.TrimSpace(m.selectedItemID) == "" {
		return feedItem{}, false
	}
	for _, item := range m.allItems() {
		if item.ID == m.selectedItemID {
			return item, true
		}
	}
	return feedItem{}, false
}

func feedItemCopyText(item feedItem) string {
	sections := make([]string, 0, 6)
	if title := strings.TrimSpace(item.Title); title != "" {
		sections = append(sections, title)
	}
	if status := strings.TrimSpace(item.Status); status != "" {
		sections = append(sections, "Status: "+status)
	}
	if body := strings.TrimSpace(item.Body); body != "" {
		sections = append(sections, body)
	}
	if input := strings.TrimSpace(formatToolInput(item.Input, true)); input != "" {
		sections = append(sections, "Input:\n"+input)
	}
	if output := strings.TrimSpace(item.Output); output != "" {
		sections = append(sections, "Output:\n"+output)
	}
	if errText := strings.TrimSpace(item.Error); errText != "" {
		sections = append(sections, "Error:\n"+errText)
	}
	if item.Permission != nil {
		if detail := strings.TrimSpace(permissionDetailText(item)); detail != "" {
			sections = append(sections, detail)
		}
	}
	return strings.TrimSpace(strings.Join(sections, "\n\n"))
}
