package agent

import (
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/contracts/protocol"
)

func TestReplaceRuntimeFeedbackReplacesAndClearsMatchingEphemeralMessage(t *testing.T) {
	a := newTestAgent(t, 1024)
	const prefix = "current canvas:"
	a.AppendRuntimeFeedback(prefix + " old snapshot")
	a.AppendRuntimeFeedback("unrelated runtime feedback")

	a.ReplaceRuntimeFeedback(prefix, prefix+" new snapshot")
	messages := a.GetMessages()
	foundNew := false
	foundOld := false
	foundUnrelated := false
	for _, msg := range messages {
		text := protocol.MessageText(msg)
		foundOld = foundOld || strings.Contains(text, "old snapshot")
		foundNew = foundNew || strings.Contains(text, "new snapshot")
		foundUnrelated = foundUnrelated || strings.Contains(text, "unrelated runtime feedback")
	}
	if foundOld || !foundNew || !foundUnrelated {
		t.Fatalf("replacement did not preserve only the intended feedback: old=%v new=%v unrelated=%v", foundOld, foundNew, foundUnrelated)
	}

	a.ReplaceRuntimeFeedback(prefix, "")
	for _, msg := range a.GetMessages() {
		if strings.HasPrefix(strings.TrimSpace(protocol.MessageText(msg)), prefix) {
			t.Fatalf("matching runtime feedback should be cleared, found %q", protocol.MessageText(msg))
		}
	}
}
