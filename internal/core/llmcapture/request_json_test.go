package llmcapture

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/contracts/protocol"
)

func TestCaptureJSONIncludesRuntimeContextWithoutLeakingWireFields(t *testing.T) {
	req := protocol.Request{Model: "test", RuntimeTail: "Memory recall\nSession ledger", IncludeReasoningContent: true, ContextSources: []protocol.ContextSource{{Name: "memory_core", MemoryIDs: []string{"mem-1"}}}}
	wire, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "runtime_tail") || strings.Contains(string(wire), "context_sources") || strings.Contains(string(wire), "include_reasoning_content") {
		t.Fatalf("capture metadata leaked to wire: %s", wire)
	}
	encoded, err := json.Marshal(Record{ID: "capture-1", Request: req})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"runtime_tail", "context_sources", "memory_core", "mem-1", "include_reasoning_content", "godex_context_v2"} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("missing %s in %s", want, encoded)
		}
	}
	var decoded Record
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Request.RuntimeTail != req.RuntimeTail || !decoded.Request.IncludeReasoningContent || len(decoded.Request.ContextSources) != 1 {
		t.Fatalf("capture round trip lost context: %+v", decoded)
	}
	var legacy Record
	if err := json.Unmarshal([]byte(`{"id":"old","request":{"model":"legacy","messages":[]}}`), &legacy); err != nil || legacy.Request.Model != "legacy" {
		t.Fatalf("legacy capture failed: %+v %v", legacy, err)
	}
}
