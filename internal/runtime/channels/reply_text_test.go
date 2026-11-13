package channels

import "testing"

func TestReplyTextAssemblerDeliversAnswerNotNarration(t *testing.T) {
	var a ReplyTextAssembler
	a.Streamed("Let me check the board. ")
	a.Streamed("Now running the tests. ")
	a.Completed("No changes since 05:40.")

	if got := a.Text(); got != "No changes since 05:40." {
		t.Fatalf("expected only the answer, got %q", got)
	}
}

// A recorded checkpoint between messages makes the short following line a closing
// note. It stays visible below the report instead of replacing it.
func TestReplyTextAssemblerKeepsReportWhenWrapUpFollowsRecording(t *testing.T) {
	report := "Board sweep finished: 2 cards in review, 1 blocked on credentials, no new failures."
	var a ReplyTextAssembler
	a.Completed(report)
	a.RecordingToolCalled()
	a.Completed("已记录，无新请求。")

	got := a.Text()
	if len(got) < len(report) || got[:len(report)] != report {
		t.Fatalf("expected the report to lead the delivery, got %q", got)
	}
	if want := report + "\n\n已记录，无新请求。"; got != want {
		t.Fatalf("expected wrap-up kept below the report, got %q", got)
	}
}

// A short line that follows a recording call but is nearly as long as the previous
// answer is a real second answer, not a closing note.
func TestReplyTextAssemblerTreatsComparableTextAsNewAnswer(t *testing.T) {
	first := "Sweep finished with no blocking issues."
	second := "Next run will recheck the same cards."
	var a ReplyTextAssembler
	a.Completed(first)
	a.RecordingToolCalled()
	a.Completed(second)

	if got := a.Text(); got != second {
		t.Fatalf("expected the newer comparable answer, got %q", got)
	}
}

// Streaming-only backends never complete a message; the stream stays the fallback.
func TestReplyTextAssemblerFallsBackToStream(t *testing.T) {
	var a ReplyTextAssembler
	a.Streamed("streamed ")
	a.Streamed("reply")

	if got := a.Text(); got != "streamed reply" {
		t.Fatalf("expected delta fallback, got %q", got)
	}
}

func TestReplyTextAssemblerEmptyWhenNothingArrived(t *testing.T) {
	var a ReplyTextAssembler
	a.Completed("   ")
	if got := a.Text(); got != "" {
		t.Fatalf("expected empty text, got %q", got)
	}
}

func TestIsLedgerRecordingTool(t *testing.T) {
	cases := []struct {
		name  string
		input map[string]interface{}
		want  bool
	}{
		{"work_context", map[string]interface{}{"action": "checkpoint"}, true},
		{"work_context", map[string]interface{}{"action": "ack"}, true},
		{"work_context", map[string]interface{}{"action": "focus"}, false},
		{"bash", map[string]interface{}{"action": "ack"}, false},
		{"work_context", nil, false},
	}
	for _, c := range cases {
		if got := IsLedgerRecordingTool(c.name, c.input); got != c.want {
			t.Errorf("IsLedgerRecordingTool(%q, %v) = %v, want %v", c.name, c.input, got, c.want)
		}
	}
}
