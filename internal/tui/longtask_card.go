package tui

import (
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/agent"
)

// longTaskCard renders a single longtask entry for the TUI task
// center. It is intentionally a pure function over the input view
// so it can be unit-tested without a Bubble Tea model.
//
// The card is one logical block with three rows: header (id +
// status), story progress (X/Y completed), and an inline hint
// about the active mode. The card is *not* an interactive widget;
// longtask_detail.go handles detail / rollback / lookup / gc
// interaction on top of this rendering helper.
func longTaskCard(view agent.LongTaskView, width int) string {
	if strings.TrimSpace(view.LongTaskID) == "" {
		return ""
	}
	width = normalizeLongTaskCardWidth(width)
	header := fmt.Sprintf("[LongTask] %s  %s", view.LongTaskID, shortLongTaskStatus(view))
	body := longTaskCardBody(view, width-len(header)-2)
	if body == "" {
		return header
	}
	return header + "\n" + indentBlock(body, "  ")
}

func normalizeLongTaskCardWidth(width int) int {
	if width < 40 {
		return 40
	}
	if width > 200 {
		return 200
	}
	return width
}

func shortLongTaskStatus(view agent.LongTaskView) string {
	if view.Run != nil && strings.TrimSpace(view.Run.Status) != "" {
		return view.Run.Status
	}
	return "unknown"
}

func longTaskCardBody(view agent.LongTaskView, width int) string {
	if width < 8 {
		return ""
	}
	completed, total := countLongTaskStories(view.Stories)
	if total == 0 {
		return ""
	}
	reverted := countLongTaskRevertedStories(view.Stories)
	out := fmt.Sprintf("stories %d/%d completed", completed, total)
	if reverted > 0 {
		out += fmt.Sprintf(" (%d reverted)", reverted)
	}
	if view.Run != nil && len(view.Run.Repaired) > 0 {
		out += fmt.Sprintf(", %d repaired", len(view.Run.Repaired))
	}
	if view.Run != nil && strings.TrimSpace(view.Run.Message) != "" {
		out += "\n  " + ellipsize(view.Run.Message, width-2)
	}
	return out
}

func countLongTaskStories(stories []agent.LongTaskStoryView) (completed, total int) {
	for _, s := range stories {
		total++
		if s.Status == "completed" {
			completed++
		}
	}
	return
}

func countLongTaskRevertedStories(stories []agent.LongTaskStoryView) int {
	n := 0
	for _, s := range stories {
		if s.Reverted {
			n++
		}
	}
	return n
}

func indentBlock(s, indent string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line == "" {
			continue
		}
		lines[i] = indent + line
	}
	return strings.Join(lines, "\n")
}


