package backend

import (
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/workcontext"
)

// TestFeedbackTellsTheModelToRecordBeforeReporting pins the ORDERING instruction, because
// getting it wrong is a user-visible bug that has now happened twice.
//
// The failure mode: the feed shows a turn's LAST text as its answer. The guidance used to say
// "after you have reported the focus, call ack", so a normal turn ended
//
//	…report → work_context checkpoint → work_context ack → one-line status
//
// and that closing status line took the answer slot, folding the actual report into the
// collapsed process summary. The turn the user was reading appeared to collapse. The rendered
// side has a guard for it (splitTurnSegments), but the guard is a repair: the durable fix is
// to stop producing the shape, which is what this instruction does.
//
// Asserted as a substring of the rendered text so it fails if the ordering is dropped or the
// clock is turned back, rather than silently regressing to a shape the feed has to fix up.
func TestFeedbackTellsTheModelToRecordBeforeReporting(t *testing.T) {
	text := renderWorkContextFeedback(workcontext.Detail{
		Context: workcontext.Context{Title: "语音服务", Goal: "可追溯的发布与回滚"},
	}, workcontext.Focus{})

	// The instruction must be present at all.
	if !strings.Contains(text, "BEFORE") {
		t.Fatalf("feedback must tell the model to finish work_context calls before the final text, got:\n%s", text)
	}
	// And it must say WHY, because the reason is what makes the rule survive a rewrite: a bare
	// rule invites "this seems unnecessary" edits.
	if !strings.Contains(strings.ToLower(text), "answer") {
		t.Fatalf("the ordering rule must explain the consequence (the last text is the turn's answer), got:\n%s", text)
	}
	// The old, wrong instruction must be gone: it is the thing that produced the bug.
	if strings.Contains(text, "After you have reported the focus") {
		t.Fatalf("the reversed instruction (ack AFTER reporting) must not be present, got:\n%s", text)
	}
}

// TestWorkContextToolDescribesTheSameOrdering keeps the tool description and the injected
// feedback saying the same thing. The two are written in different files and drift easily, but
// a model reads whichever it happens to attend to — so a disagreement means the bug returns
// for that model regardless of the other file being right.
func TestWorkContextToolDescribesTheSameOrdering(t *testing.T) {
	// A bare Service is enough: the constructor only builds the spec, and the store is reached
	// inside the run closure (at Execute time), never during construction.
	tool := (&Service{}).newWorkContextTool("session-1")
	desc := tool.Description()
	if !strings.Contains(desc, "ORDERING") {
		t.Fatalf("the work_context tool description must state the ordering rule, got:\n%s", desc)
	}
	if strings.Contains(desc, "ack marks the stream seen after reporting the focus") {
		t.Fatalf("the tool description still carries the reversed instruction, got:\n%s", desc)
	}
}
