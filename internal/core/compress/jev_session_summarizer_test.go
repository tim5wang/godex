package compress

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/contracts/protocol"
)

type scriptedJevHistoryCaller struct {
	scores    []float64
	err       error
	state     string
	questions []string
}

func (c *scriptedJevHistoryCaller) DecideToolHistory(_ context.Context, state string, questions []string) ([]float64, error) {
	c.state = state
	c.questions = append(c.questions, questions...)
	if c.err != nil {
		return nil, c.err
	}
	return append([]float64(nil), c.scores...), nil
}

func TestJevSessionSummarizerKeepTrimDropAndTail(t *testing.T) {
	dir := t.TempDir()
	compressor := NewCompressor(dir)
	compressor.SetKeepRecent(1)
	caller := &scriptedJevHistoryCaller{scores: []float64{
		0.1, 0.9, // keep call and exact output
		0.9, 0.1, // keep call, shorten output
		0.1, 0.2, // drop call and output
	}}
	history := []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "Original request"),
		protocol.NewMessage(protocol.RoleAssistant, protocol.ToolUseBlock("keep", "read_file", map[string]interface{}{"path": "a.txt"})),
		protocol.NewMessage(protocol.RoleUser, protocol.ToolResultBlock("keep", "exact useful output")),
		protocol.NewMessage(protocol.RoleAssistant, protocol.ToolUseBlock("trim", "read_file", map[string]interface{}{"path": "b.txt"})),
		protocol.NewMessage(protocol.RoleUser, protocol.ToolResultBlock("trim", strings.Repeat("t", 500))),
		protocol.NewMessage(protocol.RoleAssistant, protocol.ToolUseBlock("drop", "bash", map[string]interface{}{"command": "pwd"})),
		protocol.NewMessage(protocol.RoleUser, protocol.ToolResultBlock("drop", "unneeded output")),
		protocol.NewTextMessage(protocol.RoleAssistant, "Keep this assistant narrative."),
		protocol.NewTextMessage(protocol.RoleUser, "Latest request"),
	}
	result, err := NewJevSessionSummarizer(caller, compressor).SummarizeSession(context.Background(), SessionSummaryRequest{
		History:              history,
		RecentUserMessages:   []string{"Latest request"},
		ContinuationSnapshot: "Pinned state must survive.",
	})
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if len(caller.questions) != 6 {
		t.Fatalf("expected decisions for six questions, got %d", len(caller.questions))
	}
	if !strings.Contains(caller.state, "Latest request") {
		t.Fatalf("Jev state omitted current request: %q", caller.state)
	}
	if !strings.Contains(caller.questions[3], strings.Repeat("t", 200)) {
		t.Fatalf("output relevance question should include a bounded result excerpt: %q", caller.questions[3])
	}
	if !strings.Contains(caller.questions[3], "untrusted data") {
		t.Fatalf("output question must mark tool text untrusted: %q", caller.questions[3])
	}

	if result.Messages[0].Metadata == nil || result.Messages[0].Metadata.Kind != protocol.KindSummary {
		t.Fatalf("expected leading summary, got %+v", result.Messages[0])
	}
	if !strings.Contains(protocol.MessageText(result.Messages[0]), "Pinned state must survive.") {
		t.Fatalf("summary omitted continuation snapshot: %q", protocol.MessageText(result.Messages[0]))
	}
	if result.TranscriptRefs == nil || len(result.TranscriptRefs) != 1 || result.TranscriptRefs[0] != result.Messages[0].Metadata.Transcript {
		t.Fatalf("expected transcript reference on result, got %+v", result.TranscriptRefs)
	}
	archived, err := os.ReadFile(filepath.Join(dir, result.Messages[0].Metadata.Transcript))
	if err != nil || !strings.Contains(string(archived), "unneeded output") {
		t.Fatalf("expected full original transcript archive, err=%v", err)
	}

	var keepCall, keepOutput, trimOutput, dropCall, dropOutput, narrative, latest bool
	for _, msg := range result.Messages[1:] {
		for _, block := range msg.Content {
			switch {
			case block.Type == protocol.BlockToolUse && block.ID == "keep":
				keepCall = true
			case block.Type == protocol.BlockToolResult && block.ToolUseID == "keep" && block.Content == "exact useful output":
				keepOutput = true
			case block.Type == protocol.BlockToolResult && block.ToolUseID == "trim":
				trimOutput = strings.Contains(block.Content, "shortened by Jev") && len([]rune(block.Content)) < 500
			case block.Type == protocol.BlockToolUse && block.ID == "drop":
				dropCall = true
			case block.Type == protocol.BlockToolResult && block.ToolUseID == "drop":
				dropOutput = true
			case block.Type == protocol.BlockText && strings.Contains(block.Text, "Keep this assistant narrative."):
				narrative = true
			case block.Type == protocol.BlockText && block.Text == "Latest request":
				latest = true
			}
		}
	}
	if !keepCall || !keepOutput || !trimOutput || dropCall || dropOutput || !narrative || !latest {
		t.Fatalf("unexpected keep/trim/drop or tail result: keepCall=%v keepOutput=%v trim=%v dropCall=%v dropOutput=%v narrative=%v latest=%v", keepCall, keepOutput, trimOutput, dropCall, dropOutput, narrative, latest)
	}
}

func TestJevSessionSummarizerDoesNotSplitToolPairAtBoundary(t *testing.T) {
	caller := &scriptedJevHistoryCaller{scores: []float64{0, 0}}
	compressor := NewCompressor(t.TempDir())
	compressor.SetKeepRecent(2)
	history := []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "start"),
		protocol.NewMessage(protocol.RoleAssistant, protocol.ToolUseBlock("pair", "read_file", map[string]interface{}{})),
		protocol.NewTextMessage(protocol.RoleAssistant, "middle"),
		protocol.NewTextMessage(protocol.RoleAssistant, "more"),
		protocol.NewMessage(protocol.RoleUser, protocol.ToolResultBlock("pair", "result"), protocol.TextBlock("latest combined")),
		protocol.NewTextMessage(protocol.RoleUser, "tail"),
	}
	result, err := NewJevSessionSummarizer(caller, compressor).SummarizeSession(context.Background(), SessionSummaryRequest{History: history})
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if len(caller.questions) != 0 {
		t.Fatalf("should not ask Jev to prune a pair crossing an unsafe boundary, got %v", caller.questions)
	}
	if len(result.Messages) != len(history) {
		t.Fatalf("expected original history to remain intact, got %d messages", len(result.Messages))
	}
	for i := range history {
		if protocol.MessageText(result.Messages[i]) != protocol.MessageText(history[i]) || len(result.Messages[i].Content) != len(history[i].Content) {
			t.Fatalf("message %d was changed across pair boundary", i)
		}
	}
}

func TestJevSessionSummarizerPropagatesCallerFailure(t *testing.T) {
	caller := &scriptedJevHistoryCaller{err: errors.New("jev unavailable")}
	compressor := NewCompressor(t.TempDir())
	compressor.SetKeepRecent(1)
	history := []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "start"),
		protocol.NewMessage(protocol.RoleAssistant, protocol.ToolUseBlock("x", "bash", map[string]interface{}{})),
		protocol.NewMessage(protocol.RoleUser, protocol.ToolResultBlock("x", "result")),
		protocol.NewTextMessage(protocol.RoleUser, "latest"),
	}
	if _, err := NewJevSessionSummarizer(caller, compressor).SummarizeSession(context.Background(), SessionSummaryRequest{History: history}); err == nil {
		t.Fatal("expected Jev failure to propagate to the Agent fallback")
	}
}
