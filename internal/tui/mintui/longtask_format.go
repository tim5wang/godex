package mintui

import (
	"fmt"
	rtbackend "github.com/tim5wang/godex/internal/services/backend"
	"strings"
	"time"
)

// longTaskTitle derives a human-readable title for the list popup.
// It prefers description and falls back to the workflow id. The
// length is bounded so a single row never overflows the popup.
func longTaskTitle(workflowID, description string) string {
	const max = 60
	desc := strings.TrimSpace(description)
	if desc == "" {
		return workflowID
	}
	if len(desc) > max {
		return desc[:max-1] + "…"
	}
	return desc
}

// relativeTime returns a short human string like "5s ago" or
// "2m ago".  We avoid pulling in a date library — the popup
// already trims lines to a small width and "5m ago" is plenty
// of context for picking a recent task to inspect.
func relativeTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	switch {
	case d < 0:
		return "now"
	case d < time.Minute:
		return formatAgo(d, "s")
	case d < time.Hour:
		return formatAgo(d, "m")
	case d < 24*time.Hour:
		return formatAgo(d, "h")
	default:
		return formatAgo(d, "d")
	}
}

func formatAgo(d time.Duration, unit string) string {
	var n int
	switch unit {
	case "s":
		n = int(d / time.Second)
	case "m":
		n = int(d / time.Minute)
	case "h":
		n = int(d / time.Hour)
	case "d":
		n = int(d / (24 * time.Hour))
	}
	if n < 1 {
		n = 1
	}
	if n > 999 {
		n = 999
	}
	return fmt.Sprintf("%d%s ago", n, unit)
}

// longTaskRowToTitle is a small adapter used by the popup
// renderer so the list and detail views stay in sync.
func longTaskRowToTitle(r rtbackend.LongTaskRow) string {
	return longTaskTitle(r.WorkflowID, r.Description)
}
