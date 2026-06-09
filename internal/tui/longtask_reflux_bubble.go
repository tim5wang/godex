package tui

import (
	"fmt"
	"strings"
)

// longTaskRefluxBubble is the TUI-side renderer for an assistant
// message that was emitted by the T11 longtask reflux path. The
// bubble is a simple "header + body" string suitable for being
// dropped into the chat list with a [LongTask] prefix so the
// user can see at a glance that this is a longtask status update
// rather than a regular assistant message.
//
// The TUI side is intentionally narrow: it just renders text. The
// rich floating layout that the Web uses is delegated to the Web
// frontend in T15.2. Bubble Tea runs in a terminal where floating
// is not a real concept, so the equivalent is a recognisable
// header that the message stream itself makes easy to find.
func longTaskRefluxBubble(content, longTaskID, status string) string {
	header := longTaskRefluxHeader(longTaskID, status)
	body := strings.TrimSpace(content)
	if body == "" {
		return header
	}
	return header + "\n" + indentBlock(body, "  ")
}

func longTaskRefluxHeader(longTaskID, status string) string {
	id := strings.TrimSpace(longTaskID)
	if id == "" {
		return "[LongTask]"
	}
	if strings.TrimSpace(status) == "" {
		return fmt.Sprintf("[LongTask] %s", id)
	}
	return fmt.Sprintf("[LongTask] %s  %s", id, status)
}

// isLongTaskRefluxMessage is the TUI's view-layer sniff test for
// "this is a longtask reflux message". The real authority is the
// Metadata.Kind field set by the agent; we keep the test loose
// here so the chat list does not silently drop a message if the
// marker is missing for any reason. Web side does the strict
// check.
func isLongTaskRefluxMessage(content string) bool {
	trimmed := strings.TrimSpace(content)
	return strings.HasPrefix(trimmed, "LongTask ")
}
