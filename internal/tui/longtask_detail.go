package tui

import (
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/agent"
)

// longTaskDetailView is the TUI rendering of a single longtask's
// detail screen. The model layer holds the active longtask id and
// the matching longTaskView; the renderer is a pure function
// over those inputs so unit tests do not need a Bubble Tea
// runtime.
func longTaskDetailView(view agent.LongTaskView, rollback longTaskRollbackReasonState, lookup longTaskLookupState, width int) string {
	if strings.TrimSpace(view.LongTaskID) == "" {
		return "(no longtask selected)"
	}
	width = normalizeLongTaskCardWidth(width)
	var sections []string
	sections = append(sections, longTaskCard(view, width))
	// Inline hint about the keyboard shortcuts so the user can
	// discover them without leaving the detail view.
	sections = append(sections, longTaskDetailKeyHint(width))
	stories := longTaskViewStoriesToForList(view.Stories)
	if len(stories) > 0 {
		sections = append(sections, "Stories:")
		sections = append(sections, longTaskStoryList(stories, width-2))
	}
	if rollback.Visible {
		sections = append(sections, longTaskRollbackReasonView(rollback, width))
	}
	if lookup.Visible {
		sections = append(sections, longTaskLookupView(lookup, width))
	}
	return strings.Join(sections, "\n\n")
}

func longTaskDetailKeyHint(width int) string {
	hint := "keys: [r] run  [w] wait  [c] cancel  [f] finalize  [R] rollback  [l] lookup  [g] gc  [Esc] back"
	return ellipsize(hint, width)
}

func longTaskViewStoriesToForList(stories []agent.LongTaskStoryView) []longTaskStoryForList {
	out := make([]longTaskStoryForList, 0, len(stories))
	for _, s := range stories {
		out = append(out, longTaskStoryForList{
			StoryID:    s.ID,
			Status:     s.Status,
			CommitHash: s.CommitHash,
			Reverted:   s.Reverted,
			Error:      s.Error,
		})
	}
	return out
}

// longTaskDetailAction enumerates the keyboard actions the
// detail view can dispatch. The model layer is responsible for
// turning the action into a backend call; the renderer only
// reflects the resulting state.
type longTaskDetailAction int

const (
	longTaskActionNone longTaskDetailAction = iota
	longTaskActionRun
	longTaskActionWait
	longTaskActionCancel
	longTaskActionFinalize
	longTaskActionRollback
	longTaskActionLookup
	longTaskActionGC
	longTaskActionBack
)

// longTaskDetailKey maps a single ASCII keypress to a detail
// action. The mapping is intentionally trivial: there is no
// chord, no shift key, no prefix. Lowercase and uppercase are
// the only two cases the renderer cares about.
func longTaskDetailKey(key string) longTaskDetailAction {
	switch key {
	case "r":
		return longTaskActionRun
	case "w":
		return longTaskActionWait
	case "c":
		return longTaskActionCancel
	case "f":
		return longTaskActionFinalize
	case "R":
		return longTaskActionRollback
	case "l":
		return longTaskActionLookup
	case "g":
		return longTaskActionGC
	case "esc":
		return longTaskActionBack
	}
	return longTaskActionNone
}

// longTaskDetailActionLabel is the user-facing label for an
// action. Used by the test surface and any future help dialog.
func longTaskDetailActionLabel(a longTaskDetailAction) string {
	switch a {
	case longTaskActionRun:
		return "run"
	case longTaskActionWait:
		return "wait"
	case longTaskActionCancel:
		return "cancel"
	case longTaskActionFinalize:
		return "finalize"
	case longTaskActionRollback:
		return "rollback"
	case longTaskActionLookup:
		return "lookup"
	case longTaskActionGC:
		return "gc"
	case longTaskActionBack:
		return "back"
	}
	return "noop"
}

// longTaskDetailActionString is used in error messages / logs to
// make the action name greppable.
func longTaskDetailActionString(a longTaskDetailAction) string {
	return fmt.Sprintf("longtask-detail:%s", longTaskDetailActionLabel(a))
}
