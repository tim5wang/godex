package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/memory"
)

func TestThrottledRecallReusesLayersWithoutReadingMemoryBodies(t *testing.T) {
	a := newTestAgent(t, 100000)
	entry, err := a.memoryMgr.Remember(memory.SaveInput{Title: "Chinese preference", Summary: "Reply in Chinese", Content: "Reply in Chinese", Type: memory.TypeUser})
	if err != nil {
		t.Fatal(err)
	}
	a.AddMessage("hello")
	_, layers, err := a.collectMemoryMessagesThrottled(a.GetMessages())
	if err != nil || len(layers.Core) != 1 {
		t.Fatalf("first recall: %+v %v", layers, err)
	}
	path := filepath.Join(a.cfg.MemoryDir, entry.File)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile(path, data, 0644) })
	msgs, cached, err := a.collectMemoryMessagesThrottled(a.GetMessages())
	if err != nil || len(msgs) != 0 || len(cached.Core) != 1 || cached.Core[0].ID != entry.ID {
		t.Fatalf("throttled recall reread disk: %+v %v", cached, err)
	}
}

func TestOnlyIncludedMemoryGetsReferencedAndStablePreferencePersists(t *testing.T) {
	a := newTestAgent(t, 100000)
	core, err := a.memoryMgr.Remember(memory.SaveInput{Title: "Chinese preference", Summary: "Reply in Chinese", Content: "Reply in Chinese", Type: memory.TypeUser})
	if err != nil {
		t.Fatal(err)
	}
	relevant, err := a.memoryMgr.Remember(memory.SaveInput{Title: "OCR workflow", Summary: "Use Vision for OCR", Content: "Use Vision for OCR", Type: memory.TypeWorkflow})
	if err != nil {
		t.Fatal(err)
	}
	a.AddMessage("OCR")
	first, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.RuntimeTail, "OCR workflow") {
		t.Fatal("missing initial recall")
	}
	// The unacknowledged first build is a preview, not a recorded injection.
	second, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(second.RuntimeTail, "OCR workflow") {
		t.Fatal("repeated relevant recall")
	}
	prefix := ""
	for _, msg := range second.Messages {
		prefix += protocol.MessageText(msg)
	}
	if !strings.Contains(prefix, "Reply in Chinese") {
		t.Fatal("stable preference disappeared during throttle")
	}
	second.AckRuntime()
	c, err := a.memoryMgr.Get(core.ID)
	if err != nil || c.LastReferencedAt.IsZero() {
		t.Fatalf("included core not recorded: %+v %v", c, err)
	}
	r, err := a.memoryMgr.Get(relevant.ID)
	if err != nil || !r.LastReferencedAt.IsZero() {
		t.Fatalf("throttled relevant falsely recorded: %+v %v", r, err)
	}
}

func TestFailedModelRequestDoesNotReferenceMemory(t *testing.T) {
	a := newTestAgent(t, 100000)
	entry, err := a.memoryMgr.Remember(memory.SaveInput{Title: "Chinese preference", Summary: "Reply in Chinese", Content: "Reply in Chinese", Type: memory.TypeUser})
	if err != nil {
		t.Fatal(err)
	}
	a.AddMessage("hello")
	a.client = fakeCaller{err: errors.New("model unavailable")}
	if err := a.Run(context.Background()); err == nil {
		t.Fatal("expected model failure")
	}
	record, err := a.memoryMgr.Get(entry.ID)
	if err != nil || !record.LastReferencedAt.IsZero() {
		t.Fatalf("failed request counted as use: %+v %v", record, err)
	}
}

func TestRecallCacheInvalidatesOnStoreUpdate(t *testing.T) {
	a := newTestAgent(t, 100000)
	entry, err := a.memoryMgr.Remember(memory.SaveInput{Title: "OCR workflow", Summary: "obsolete OCR", Content: "old command", Type: memory.TypeWorkflow})
	if err != nil {
		t.Fatal(err)
	}
	a.AddMessage("OCR")
	first, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first.AckRuntime()
	_, err = a.memoryMgr.Update(memory.UpdateInput{Match: memory.ForgetInput{File: entry.File}, Title: entry.Title, Summary: "current OCR with Vision", Content: "Use Vision", Type: entry.Type})
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.buildContext(context.Background())
	if err != nil || !strings.Contains(second.RuntimeTail, "Use Vision") || strings.Contains(second.RuntimeTail, "obsolete OCR") {
		t.Fatalf("stale cache: %+v %v", second, err)
	}
}
