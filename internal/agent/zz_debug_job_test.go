package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tim5wang/godex/internal/contracts/protocol"
)

// Load the failed job exactly as the store does (disk -> memory) and report
// the actual Messages count + any message whose content is empty. This tells
// us whether resume sent 28 messages (matching disk) or more.
func TestZZDebugStoreJobMessages(t *testing.T) {
	store := newSubagentJobStore("/Users/taiwu.wang/.godex/state/subagents")
	job, err := store.Get("subagent_1786547903106382000")
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	t.Logf("job status=%s merge=%s", job.Status, job.MergeStatus)
	t.Logf("job.Messages count: %d", len(job.Messages))
	for i, m := range job.Messages {
		if len(m.Content) == 0 {
			t.Logf("EMPTY content msg[%d] role=%s", i, m.Role)
		}
	}
	// Also verify messages.json on disk parses to the same count.
	msgs, err := loadJobMessagesFile(filepath.Join(store.dir, "subagent_1786547903106382000", "messages.json"))
	if err != nil {
		t.Fatalf("load messages file: %v", err)
	}
	t.Logf("disk messages.json count: %d", len(msgs))
}

func loadJobMessagesFile(path string) ([]protocol.Message, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var msgs []protocol.Message
	if err := json.Unmarshal(raw, &msgs); err != nil {
		return nil, err
	}
	return msgs, nil
}
