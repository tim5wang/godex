package channels

import (
	"strings"
	"unicode/utf8"
)

// ReplyTextAssembler chooses the text a scheduled or heartbeat delivery carries.
//
// Two naive options are both wrong:
//   - concatenating every streamed delta ships the whole "let me check…" narration
//     as the delivered message (the repeated-progress noise users complained about);
//   - taking only the last message loses the report when a turn ends with a ledger
//     recording call plus a one-line wrap-up ("已记录").
//
// So the substantive answer wins, the wrap-up is kept below it, and the raw stream
// remains the fallback for backends that never complete a message.
type ReplyTextAssembler struct {
	streamed strings.Builder
	answer   string
	wrapUp   string
	// recording is set when a recording call lands between messages, which is
	// what makes a following short line a closing note rather than the answer.
	recording bool
}

// Streamed records one assistant text delta (fallback only).
func (a *ReplyTextAssembler) Streamed(text string) { a.streamed.WriteString(text) }

// RecordingToolCalled marks that a bookkeeping call completed successfully.
func (a *ReplyTextAssembler) RecordingToolCalled() { a.recording = true }

// Completed records one complete assistant message.
//
// After a recording call, a line less than half the length of the report is a
// closing note: it is appended below the report instead of replacing it. The
// comparison counts characters, not bytes — byte length made Chinese wrap-ups look
// long enough to win the answer slot.
func (a *ReplyTextAssembler) Completed(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if a.recording && a.answer != "" && utf8.RuneCountInString(text)*2 <= utf8.RuneCountInString(a.answer) {
		a.wrapUp = text
		a.recording = false
		return
	}
	a.answer, a.wrapUp, a.recording = text, "", false
}

// Text returns what should be delivered.
func (a *ReplyTextAssembler) Text() string {
	if a.answer == "" {
		return strings.TrimSpace(a.streamed.String())
	}
	if a.wrapUp == "" {
		return a.answer
	}
	return a.answer + "\n\n" + a.wrapUp
}

// IsLedgerRecordingTool reports whether a tool call only records work already
// reported (checkpoint / ack / propose_fact / todo_patch on work_context).
func IsLedgerRecordingTool(name string, input map[string]interface{}) bool {
	if name != "work_context" {
		return false
	}
	action, _ := input["action"].(string)
	switch action {
	case "checkpoint", "ack", "propose_fact", "todo_patch":
		return true
	}
	return false
}
