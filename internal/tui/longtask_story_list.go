package tui

import (
	"fmt"
	"sort"
	"strings"
)

// longTaskStoryList renders the expanded story list for a longtask
// detail view. T15 acceptance: every story appears with its
// status, commit hash (if any), and a [reverted] tag when the
// story was rolled back. The list is sorted so completed stories
// come last, which makes the active set easy to scan.
func longTaskStoryList(stories []longTaskStoryForList, width int) string {
	if len(stories) == 0 {
		return "(no stories yet)"
	}
	width = normalizeLongTaskCardWidth(width)
	sorted := append([]longTaskStoryForList(nil), stories...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return longTaskStoryOrderKey(sorted[i]) < longTaskStoryOrderKey(sorted[j])
	})
	var b strings.Builder
	for i, s := range sorted {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "  %s  %s", longTaskStoryStatusMarker(s.Status), s.StoryID)
		if s.CommitHash != "" {
			fmt.Fprintf(&b, "  (commit %s)", truncateCommit(s.CommitHash))
		}
		if s.Reverted {
			b.WriteString("  [reverted]")
		}
		if s.Error != "" {
			fmt.Fprintf(&b, "\n    err: %s", ellipsize(s.Error, width-8))
		}
	}
	return b.String()
}

type longTaskStoryForList struct {
	StoryID    string
	Status     string
	CommitHash string
	Reverted   bool
	Error      string
}

func longTaskStoryOrderKey(s longTaskStoryForList) string {
	// Active first, completed/reverted last, then by story id.
	switch strings.ToLower(s.Status) {
	case "pending", "":
		return "0" + s.StoryID
	case "running", "blocked":
		return "1" + s.StoryID
	case "completed":
		return "2" + s.StoryID
	case "canceled":
		return "3" + s.StoryID
	}
	return "9" + s.StoryID
}

func longTaskStoryStatusMarker(status string) string {
	switch strings.ToLower(status) {
	case "completed":
		return "[x]"
	case "running":
		return "[~]"
	case "blocked":
		return "[!]"
	case "error":
		return "[!]"
	case "canceled":
		return "[-]"
	default:
		return "[ ]"
	}
}

func truncateCommit(hash string) string {
	if len(hash) <= 8 {
		return hash
	}
	return hash[:8]
}
