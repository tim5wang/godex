package conversation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tim5wang/godex/internal/contracts/protocol"
)

// Simulate a full resume first-round: load messages exactly as the store does,
// clone, sanitize, and count. If this yields 28 valid messages while the API
// complained about messages[33], the runtime send array differs from disk.
func TestZZDebugResumeFirstRound(t *testing.T) {
	jobDir := "/Users/taiwu.wang/.godex/state/subagents/subagent_1786547903106382000"
	raw, err := os.ReadFile(filepath.Join(jobDir, "messages.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var msgs []protocol.Message
	if err := json.Unmarshal(raw, &msgs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	cloned := protocol.CloneMessages(msgs)
	t.Logf("cloned messages: %d", len(cloned))
	api := protocol.ToAPIMessages(cloned)
	san := SanitizeMessagesForProvider(api)
	t.Logf("after ToAPI + Sanitize: %d", len(san))
	if len(san) != len(msgs) {
		t.Logf("ARRAY CHANGED: disk=%d sent=%d", len(msgs), len(san))
	}
	// Report any message that would serialize with empty/missing content.
	for i, m := range san {
		if len(m.Content) == 0 {
			t.Logf("EMPTY CONTENT wire msg[%d] role=%s", i, m.Role)
		}
	}
}
