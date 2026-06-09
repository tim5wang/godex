package tui

import (
	"strings"
)

// longTaskLookupMode selects what the lookup modal is searching
// for. The two modes share the same modal chrome so the user
// can switch without re-rendering the whole panel.
type longTaskLookupMode int

const (
	longTaskLookupByCommit longTaskLookupMode = iota
	longTaskLookupByStory
)

// longTaskLookupState is the local state for the commit / story
// lookup modal. It is intentionally minimal: the actual network
// call is made by the model layer, not by the view helper.
type longTaskLookupState struct {
	Visible bool
	Mode    longTaskLookupMode
	Query   string
	Err     string
}

// longTaskLookupApply updates the in-progress query string. The
// model layer is expected to call the backend whenever Query
// stops changing (debounced) and surface the result via the
// existing longTaskView.
func longTaskLookupApply(s longTaskLookupState, q string) longTaskLookupState {
	if !s.Visible {
		return s
	}
	s.Query = q
	s.Err = ""
	return s
}

// longTaskLookupView renders the modal chrome. Width-stable: at
// narrow widths we truncate the hint rather than wrapping so the
// input field is always visible.
func longTaskLookupView(s longTaskLookupState, width int) string {
	if !s.Visible {
		return ""
	}
	if width < 30 {
		width = 30
	}
	hint := lookupModeHint(s.Mode)
	var lines []string
	lines = append(lines, "Lookup longtask story")
	lines = append(lines, hint)
	if s.Err != "" {
		lines = append(lines, s.Err)
	}
	lines = append(lines, "> "+s.Query)
	return strings.Join(lines, "\n")
}

func lookupModeHint(mode longTaskLookupMode) string {
	switch mode {
	case longTaskLookupByCommit:
		return "type a commit hash (or prefix); Enter to search"
	case longTaskLookupByStory:
		return "type a story id (e.g. US-001); Enter to search"
	}
	return ""
}

// longTaskLookupResult renders a successful lookup result. The
// caller passes the matched stories (already filtered by the
// model layer) and the result is rendered with the same card
// conventions used elsewhere in the TUI.
func longTaskLookupResult(stories []longTaskStoryForList, width int) string {
	if len(stories) == 0 {
		return "(no matching stories)"
	}
	return longTaskStoryList(stories, width)
}


