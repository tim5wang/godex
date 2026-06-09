package tui

import (
	"strings"

	"github.com/tim5wang/godex/internal/agent"
)

// renderLongTaskDetail is the TUI entry point for the longtask
// detail screen. It looks up the longtask view by id and
// delegates to the pure longTaskDetailView renderer. T15
// acceptance: the user can drill into a longtask from the
// workbench task tab and see the same 5-component surface the
// test suite covers.
func (m *model) renderLongTaskDetail() string {
	if strings.TrimSpace(m.longTaskDetailID) == "" {
		return "(no longtask selected)"
	}
	view := m.findLongTaskView(m.longTaskDetailID)
	width := maxInt(20, m.viewport.Width)
	return longTaskDetailView(view, m.longTaskRollback, m.longTaskLookup, width)
}

// findLongTaskView locates the matching longtask view in the
// cached list. Returns an empty LongTaskView (with the requested
// id pre-set) if the cache has not been populated yet so the
// renderer can still show a useful header.
func (m *model) findLongTaskView(id string) agent.LongTaskView {
	view := agent.LongTaskView{LongTaskID: id}
	for _, v := range m.longTasks {
		if v.LongTaskID == id {
			return v
		}
	}
	return view
}
