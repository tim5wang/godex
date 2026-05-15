package tui

import (
	"strings"

	"github.com/tim5wang/godex/internal/tools"
)

func (m *model) renderFeedContent() (string, []itemSpan) {
	items := m.allItems()
	if len(items) == 0 {
		return mutedLineStyle.Render("No conversation yet. Type a message or /help to get started."), nil
	}

	segments := make([]string, 0, len(items)*2)
	spans := make([]itemSpan, 0, len(items))
	lineNo := 0
	bodyWidth := maxInt(10, m.viewport.Width)

	for i, item := range items {
		lines := m.renderItemLines(item, bodyWidth)
		if len(lines) == 0 {
			continue
		}
		segments = append(segments, strings.Join(lines, "\n"))
		spans = append(spans, itemSpan{
			ID:       item.ID,
			Start:    lineNo,
			End:      lineNo + len(lines) - 1,
			Foldable: item.Foldable,
		})
		lineNo += len(lines)
		if i < len(items)-1 {
			segments = append(segments, "")
			lineNo++
		}
	}

	return strings.Join(segments, "\n"), spans
}

func (m *model) renderItemLines(item feedItem, width int) []string {
	selected := m.focus == focusFeed && item.ID == m.selectedItemID
	switch item.Kind {
	case feedUser:
		style := userLineStyle
		if selected {
			style = selectedTextStyle
		}
		return renderPrefixedBlock(item.Body, "› ", "  ", width, style)
	case feedAssistant:
		style := assistantLineStyle
		if selected {
			style = selectedTextStyle
		}
		return renderPrefixedBlock(item.Body, "• ", "  ", width, style)
	case feedTool:
		return m.renderToolLines(item, width)
	case feedTodo:
		labelStyle := commandLineStyle
		if selected {
			labelStyle = selectedTextStyle
		}
		return renderLabeledBlock("☑ "+item.Title, item.Body, width, labelStyle, assistantLineStyle)
	case feedPermission:
		return m.renderPermissionLines(item, width)
	case feedCommand:
		labelStyle := commandLineStyle
		if selected {
			labelStyle = selectedTextStyle
		}
		return renderLabeledBlock("· "+item.Title, item.Body, width, labelStyle, assistantLineStyle)
	case feedWarning:
		labelStyle := warningLineStyle
		if selected {
			labelStyle = selectedTextStyle
		}
		return renderLabeledBlock("! "+item.Title, item.Body, width, labelStyle, assistantLineStyle)
	case feedError:
		labelStyle := errorLineStyle
		if selected {
			labelStyle = selectedTextStyle
		}
		return renderLabeledBlock("x "+item.Title, item.Body, width, labelStyle, assistantLineStyle)
	default:
		return renderPrefixedBlock(item.Body, "", "", width, assistantLineStyle)
	}
}

func (m *model) renderToolLines(item feedItem, width int) []string {
	arrow := "▸"
	if item.Expanded {
		arrow = "▾"
	}

	parts := []string{arrow, item.Title}
	if item.Status != "" {
		parts = append(parts, "·", item.Status)
	}
	if summary := strings.TrimSpace(item.Summary); summary != "" {
		parts = append(parts, "·", summary)
	}
	head := ellipsize(strings.Join(parts, " "), width)

	headStyle := toolLineStyle
	switch item.Status {
	case "running":
		headStyle = toolRunningStyle
	case "failed":
		headStyle = errorLineStyle
	}
	if m.focus == focusFeed && item.ID == m.selectedItemID {
		headStyle = toolSelectedStyle
	}

	lines := []string{headStyle.Render(head)}
	if !item.Expanded {
		if item.Permission != nil {
			summary := permissionCompactDetailText(*item.Permission)
			if summary != "" {
				lines = append(lines, mutedLineStyle.Render(ellipsize("  "+summary, width)))
			}
		}
		return lines
	}

	for _, line := range wrapWithIndent(toolDetailText(item), "  ", "  ", width) {
		lines = append(lines, mutedLineStyle.Render(line))
	}
	return lines
}

func (m *model) renderPermissionLines(item feedItem, width int) []string {
	arrow := "▸"
	if item.Expanded {
		arrow = "▾"
	}

	parts := []string{arrow, item.Title}
	if summary := strings.TrimSpace(permissionHeaderSummary(item)); summary != "" {
		parts = append(parts, "·", summary)
	}
	head := ellipsize(strings.Join(parts, " "), width)

	headStyle := permissionLineStyle
	if m.focus == focusFeed && item.ID == m.selectedItemID {
		headStyle = permissionSelectedStyle
	}

	lines := []string{headStyle.Render(head)}
	if !item.Expanded {
		if item.Permission != nil {
			if summary := strings.TrimSpace(permissionCompactDetailText(*item.Permission)); summary != "" {
				lines = append(lines, mutedLineStyle.Render(ellipsize("  "+summary, width)))
			}
			if intent := strings.TrimSpace(tools.PermissionIntentSummary(*item.Permission)); intent != "" {
				lines = append(lines, mutedLineStyle.Render(ellipsize("  "+intent, width)))
			}
		}
		return lines
	}

	for _, line := range wrapWithIndent(permissionDetailText(item), "  ", "  ", width) {
		lines = append(lines, mutedLineStyle.Render(line))
	}
	return lines
}
