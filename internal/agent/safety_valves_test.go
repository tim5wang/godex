package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/tools"
)

// Safety valve: compaction trigger is clamped so a misconfigured value
// (e.g. 800000, above any model window) cannot silently disable auto-compaction
// and let a long turn's context grow unbounded into a loop / overflow.
func TestCompactionTriggerTokensClamped(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.cfg.Compaction.TriggerTokens = 800000
	if got := a.compactionTriggerTokens(); got != maxCompactionTriggerTokens {
		t.Fatalf("expected absurd trigger clamped to %d, got %d", maxCompactionTriggerTokens, got)
	}
	// Sane values below the ceiling pass through untouched.
	a.cfg.Compaction.TriggerTokens = 80
	if got := a.compactionTriggerTokens(); got != 80 {
		t.Fatalf("expected small trigger untouched, got %d", got)
	}
	// Default path also clamps.
	a.cfg.Compaction.TriggerTokens = 0
	a.cfg.CompressThreshold = 900000
	if got := a.compactionTriggerTokens(); got != maxCompactionTriggerTokens {
		t.Fatalf("expected compress_threshold clamped too, got %d", got)
	}
}

// Safety valve: a project ledger that has not been refreshed by a completed
// turn within the window is stale (older task phase / stalled marathon turn)
// and must NOT be injected into the active context.
func TestStaleProjectLedgerNotInjected(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	ctx := tools.WithSessionContext(context.Background(), automation.SessionContext{
		SessionID:             "session-stale-ledger",
		ProjectLedger:         "Goal: old task from six hours ago\nCurrent phase: blocked",
		ProjectLedgerUpdatedAt: time.Now().Add(-projectLedgerInjectionWindow - time.Minute),
	})

	build, err := a.buildContext(ctx)
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	for _, msg := range build.Messages {
		if msg.Metadata == nil || msg.Metadata.Kind != protocol.KindBackground {
			continue
		}
		if strings.Contains(protocol.MessageText(msg), "Long-task project ledger") {
			t.Fatalf("stale project ledger must not be injected, got %q", protocol.MessageText(msg))
		}
	}
}

// Freshness boundary: a ledger updated just now is injected.
func TestFreshProjectLedgerInjected(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	ctx := tools.WithSessionContext(context.Background(), automation.SessionContext{
		SessionID:             "session-fresh-ledger",
		ProjectLedger:         "Goal: current task\nCurrent phase: active",
		ProjectLedgerUpdatedAt: time.Now(),
	})

	build, err := a.buildContext(ctx)
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	found := false
	for _, msg := range build.Messages {
		if msg.Metadata == nil || msg.Metadata.Kind != protocol.KindBackground {
			continue
		}
		if strings.Contains(protocol.MessageText(msg), "Long-task project ledger") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected fresh project ledger to be injected")
	}
}
