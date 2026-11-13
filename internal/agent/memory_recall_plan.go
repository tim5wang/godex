package agent

import (
	"strings"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/platform/retrieval"
)

// MemoryRecallPlan shares the exact user-only query policy with the host's
// desktop snapshot, including when the new envelope is not in history yet.
func (a *Agent) MemoryRecallPlan(current ...string) retrieval.RecallPlan {
	return memoryRecallPlan(a.GetMessages(), current...)
}

func memoryRecallPlan(history []protocol.Message, current ...string) retrieval.RecallPlan {
	texts := []string{}
	for _, msg := range history {
		if msg.Role != protocol.RoleUser || msg.Metadata != nil && (msg.Metadata.Ephemeral || msg.Metadata.Kind == protocol.KindSummary) {
			continue
		}
		if text := strings.TrimSpace(protocol.MessageText(msg)); text != "" {
			texts = append(texts, text)
		}
	}
	if len(current) > 0 {
		texts = append(texts, current[0])
	}
	return retrieval.PlanRecall(texts)
}
