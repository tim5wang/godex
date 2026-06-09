package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m *model) renderActiveViewportContent() (string, []itemSpan) {
	if m.activeWorkbenchTab == workbenchTabLogs {
		return m.renderFeedContent()
	}
	return m.renderWorkbenchContent(), nil
}

func (m *model) renderWorkbenchContent() string {
	parts := []string{m.renderWorkbenchTabs()}
	if m.workbenchErr != nil {
		parts = append(parts, warningLineStyle.Render(ellipsize("Task Center refresh failed: "+m.workbenchErr.Error(), m.viewport.Width)))
	}
	switch m.activeWorkbenchTab {
	case workbenchTabTask:
		parts = append(parts, m.renderTaskCenter())
	case workbenchTabWorkers:
		parts = append(parts, m.renderWorkersTab())
	case workbenchTabGraph:
		parts = append(parts, m.renderGraphTab())
	case workbenchTabDiff:
		parts = append(parts, m.renderDiffTab())
	default:
		parts = append(parts, m.renderTaskCenter())
	}
	return strings.Join(parts, "\n")
}

func (m *model) renderWorkbenchTabs() string {
	labels := []struct {
		tab   workbenchTab
		label string
	}{
		{workbenchTabTask, "1 Task"},
		{workbenchTabWorkers, "2 Workers"},
		{workbenchTabGraph, "3 Graph"},
		{workbenchTabDiff, "4 Diff"},
		{workbenchTabLogs, "5 Logs"},
	}
	out := make([]string, 0, len(labels))
	for _, item := range labels {
		label := item.label
		if m.activeWorkbenchTab == item.tab {
			label = selectedTextStyle.Render(label)
		} else {
			label = mutedLineStyle.Render(label)
		}
		out = append(out, label)
	}
	return strings.Join(out, "  ")
}

func (m *model) renderTaskCenter() string {
	// T15: when the user has drilled into a longtask, render the
	// detail view (5 components: card + story list + rollback
	// modal + lookup modal + reflux bubble hint) instead of the
	// 3-column workbench summary.
	if m.longTaskDetailVisible {
		return m.renderLongTaskDetail()
	}
	summary := m.buildWorkbenchSummary()
	width := maxInt(20, m.viewport.Width)
	if width < 88 {
		return strings.Join([]string{
			m.renderWorkbenchSection("Plan", summary.Plan, width),
			m.renderWorkbenchSection("Active Execution", summary.Active, width),
			m.renderWorkbenchSection("Review & Merge", summary.Review, width),
		}, "\n")
	}

	gap := 2
	panelWidth := maxInt(20, (width-(gap*2))/3)
	return lipgloss.JoinHorizontal(lipgloss.Top,
		m.renderWorkbenchSection("Plan", summary.Plan, panelWidth),
		strings.Repeat(" ", gap),
		m.renderWorkbenchSection("Active Execution", summary.Active, panelWidth),
		strings.Repeat(" ", gap),
		m.renderWorkbenchSection("Review & Merge", summary.Review, panelWidth),
	)
}

func (m *model) renderWorkersTab() string {
	width := maxInt(20, m.viewport.Width)
	return m.renderWorkbenchSection("Workers", m.workbenchWorkerLines(), width)
}

func (m *model) renderGraphTab() string {
	width := maxInt(20, m.viewport.Width)
	return m.renderWorkbenchSection("Graph", m.workbenchGraphLines(), width)
}

func (m *model) renderDiffTab() string {
	width := maxInt(20, m.viewport.Width)
	lines := m.workbenchReviewLines()
	if len(lines) == 1 && lines[0] == "Nothing waiting for review" {
		lines = []string{"No reviewable worker diff summary yet"}
	}
	return m.renderWorkbenchSection("Diff", lines, width)
}

func (m *model) renderWorkbenchSection(title string, lines []string, width int) string {
	width = maxInt(12, width)
	body := make([]string, 0, len(lines)+2)
	body = append(body, titleStyle.Render(ellipsize(title, width-2)))
	body = append(body, ruleStyle.Render(strings.Repeat("─", maxInt(1, width-2))))
	if len(lines) == 0 {
		lines = []string{"n/a"}
	}
	for _, line := range lines {
		for _, wrapped := range wrapWithIndent(line, "", "  ", maxInt(8, width-2)) {
			body = append(body, ellipsize(wrapped, maxInt(8, width-2)))
		}
	}
	return lipgloss.NewStyle().
		Width(width).
		Border(lipgloss.NormalBorder()).
		Padding(0, 1).
		Render(strings.Join(body, "\n"))
}
