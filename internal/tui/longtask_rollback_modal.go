package tui

import (
	"fmt"
	"strings"
)

// longTaskRollbackModalReasonMaxBytes is the hard cap on the
// rollback reason. T15 mirrors the agent-side 1024-byte limit
// (see agent.longTaskRollbackReasonMaxBytes) so the TUI never
// lets the user type more than the agent will accept. The cap is
// a byte count, not a rune count, to match what the agent (and
// the CLI / HTTP boundary) actually enforce.
const longTaskRollbackModalReasonMaxBytes = 1024

// longTaskRollbackReasonState is the local state for the rollback
// reason prompt that pops up when the user presses R on a
// longtask detail row. It tracks both the in-progress text and a
// pre-computed byte count so the modal can render a live
// "n / 1024" indicator without re-counting on every keystroke.
type longTaskRollbackReasonState struct {
	Visible  bool
	NodeID   string
	Text     string
	ByteSize int
	Err      string
}

// longTaskRollbackReasonAppend appends a chunk to the reason
// state, refusing the append if the resulting byte count would
// exceed the cap. Returns the new state. The chunk is taken as
// raw bytes (no UTF-8 normalization) so multi-byte characters
// count for what they actually cost on the wire.
func longTaskRollbackReasonAppend(s longTaskRollbackReasonState, chunk string) longTaskRollbackReasonState {
	if !s.Visible {
		return s
	}
	if chunk == "" {
		return s
	}
	next := s.Text + chunk
	if len(next) > longTaskRollbackModalReasonMaxBytes {
		s.Err = fmt.Sprintf("reason exceeds %d bytes (got %d)", longTaskRollbackModalReasonMaxBytes, len(next))
		return s
	}
	s.Text = next
	s.ByteSize = len(next)
	s.Err = ""
	return s
}

// longTaskRollbackReasonBackspace removes the trailing byte. The
// TUI key handling layer calls this on a backspace key and
// re-renders; we keep the size bookkeeping in sync.
func longTaskRollbackReasonBackspace(s longTaskRollbackReasonState) longTaskRollbackReasonState {
	if !s.Visible || s.Text == "" {
		return s
	}
	s.Text = s.Text[:len(s.Text)-1]
	s.ByteSize = len(s.Text)
	s.Err = ""
	return s
}

// longTaskRollbackReasonView renders the rollback reason prompt
// (including the live byte counter and any error hint). The
// caller is expected to add a header / footer around this
// string. The result is multi-line and width-stable.
func longTaskRollbackReasonView(s longTaskRollbackReasonState, width int) string {
	if !s.Visible {
		return ""
	}
	if width < 30 {
		width = 30
	}
	counter := fmt.Sprintf("%d / %d bytes", s.ByteSize, longTaskRollbackModalReasonMaxBytes)
	var lines []string
	lines = append(lines, fmt.Sprintf("Rollback reason for story %s", s.NodeID))
	lines = append(lines, counter)
	if s.Err != "" {
		lines = append(lines, s.Err)
	}
	lines = append(lines, "> "+s.Text)
	return strings.Join(lines, "\n")
}
