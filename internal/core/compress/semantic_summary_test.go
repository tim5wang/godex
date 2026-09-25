package compress

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/contracts/protocol"
)

func TestRuleSummaryRetainsStructuredStateAcrossRepeatedCompactions(t *testing.T) {
	messages := []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "Implement archive pagination while keeping the earliest messages reachable."),
		protocol.NewTextMessage(protocol.RoleUser, "Must preserve archive ordering and avoid duplicate history messages."),
		protocol.NewTextMessage(protocol.RoleAssistant, "Implemented recursive transcript traversal and verified it."),
		protocol.NewTextMessage(protocol.RoleAssistant, "Next: verify the oldest archive remains reachable after repeated compression."),
	}

	for round := 1; round <= 3; round++ {
		summary := buildSemanticSummary(messages, fmt.Sprintf("transcript-%d.json", round), "")
		messages = []protocol.Message{
			protocol.NewSummaryMessage(summary, fmt.Sprintf("transcript-%d.json", round)),
			protocol.NewTextMessage(protocol.RoleUser, fmt.Sprintf("Continue the archive validation round %d.", round)),
			protocol.NewTextMessage(protocol.RoleAssistant, fmt.Sprintf("Validation round %d passed.", round)),
		}
	}

	summary := protocol.MessageText(messages[0])
	for _, want := range []string{
		"Implement archive pagination",
		"Must preserve archive ordering",
		"Implemented recursive transcript traversal",
		"verify the oldest archive remains reachable",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("repeated compaction lost %q:\n%s", want, summary)
		}
	}
}
