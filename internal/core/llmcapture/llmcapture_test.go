package llmcapture

import (
	"context"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/conversation"
)

func TestCaptureAppendsJSONLAndServesRecords(t *testing.T) {
	dir := t.TempDir()
	c := New(Options{DumpDir: dir, MaxMemRecords: 10})
	defer c.Close()

	if c.Enabled() {
		t.Fatal("capture should start disabled")
	}
	if err := c.SetEnabled(true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !c.Enabled() {
		t.Fatal("capture should be enabled after SetEnabled(true)")
	}

	ctx := conversation.WithUsageContext(context.Background(), conversation.UsageContext{
		SessionID: "sess-1",
		TurnID:    "turn-1",
	})
	// NotifyUsageHooksForTest skips the ctx→event.Context plumbing that the real
	// notifyUsage does, so mirror it here (production always runs through
	// notifyUsage which sets event.Context before dispatching hooks).
	conversation.NotifyUsageHooksForTest(ctx, conversation.UsageEvent{
		Context: conversation.UsageContext{SessionID: "sess-1", TurnID: "turn-1"},
		Request: protocol.Request{Model: "gpt-4o", Messages: []protocol.APIMessage{{Role: "user"}}},
		Response: &protocol.Response{
			Content:    []protocol.Block{{Type: protocol.BlockText, Text: "hello"}},
			StopReason: "end_turn",
			Usage:      &protocol.Usage{InputTokens: 10, OutputTokens: 5},
		},
		Latency: 3 * time.Millisecond,
	})

	list := c.List(10)
	if len(list) != 1 {
		t.Fatalf("expected 1 record, got %d", len(list))
	}
	s := list[0]
	if s.Model != "gpt-4o" {
		t.Errorf("model = %q, want gpt-4o", s.Model)
	}
	if s.InputTokens != 15 {
		t.Errorf("input_tokens = %d, want 15", s.InputTokens)
	}
	if s.SessionID != "sess-1" || s.TurnID != "turn-1" {
		t.Errorf("session/turn mismatch: %q/%q", s.SessionID, s.TurnID)
	}

	rec := c.Get(s.ID)
	if rec == nil {
		t.Fatal("Get returned nil for captured record")
	}
	if rec.Response == nil || len(rec.Response.Content) != 1 {
		t.Fatalf("record response lost: %+v", rec.Response)
	}

	// jsonl file should contain exactly one line matching the record.
	persisted, err := ReadAllFromFile(c.DumpPath())
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	if len(persisted) != 1 {
		t.Fatalf("expected 1 persisted line, got %d", len(persisted))
	}
	if persisted[0].ID != rec.ID {
		t.Errorf("persisted id %q != record id %q", persisted[0].ID, rec.ID)
	}
}

func TestCaptureDisabledDoesNotCapture(t *testing.T) {
	dir := t.TempDir()
	c := New(Options{DumpDir: dir})
	defer c.Close()

	ctx := conversation.WithUsageContext(context.Background(), conversation.UsageContext{SessionID: "s"})
	conversation.NotifyUsageHooksForTest(ctx, conversation.UsageEvent{
		Request: protocol.Request{Model: "m"},
	})
	if got := c.List(10); len(got) != 0 {
		t.Fatalf("captured %d records while disabled", len(got))
	}
}

func TestCaptureRingBufferCapsMemory(t *testing.T) {
	dir := t.TempDir()
	c := New(Options{DumpDir: dir, MaxMemRecords: 3})
	defer c.Close()
	_ = c.SetEnabled(true)

	ctx := conversation.WithUsageContext(context.Background(), conversation.UsageContext{SessionID: "s"})
	for i := 0; i < 10; i++ {
		conversation.NotifyUsageHooksForTest(ctx, conversation.UsageEvent{
			Request: protocol.Request{Model: "m"},
		})
	}

	list := c.List(100)
	if len(list) != 3 {
		t.Fatalf("expected 3 in-memory records, got %d", len(list))
	}
	// Newest first.
	if list[0].ID == list[2].ID {
		t.Fatal("list should be newest-first")
	}
}
