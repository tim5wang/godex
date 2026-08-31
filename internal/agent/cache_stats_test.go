package agent

import (
	"context"
	"testing"

	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/contracts/protocol"
)

func TestSessionCacheStatsRecordUsage(t *testing.T) {
	var stats sessionCacheStats

	// No usage data → ignored.
	stats.recordUsage(conversation.UsageEvent{})
	stats.recordUsage(conversation.UsageEvent{Response: &protocol.Response{}})
	if stats.Calls != 0 {
		t.Fatalf("expected empty events ignored, got %+v", stats)
	}

	stats.recordUsage(conversation.UsageEvent{Response: &protocol.Response{Usage: &protocol.Usage{
		InputTokens:     1000,
		OutputTokens:    50,
		CacheReadTokens: 4000,
	}}})
	stats.recordUsage(conversation.UsageEvent{Response: &protocol.Response{Usage: &protocol.Usage{
		InputTokens:      2000,
		OutputTokens:     80,
		CacheReadTokens:  6000,
		CacheWriteTokens: 500,
	}}})

	snap := stats.snapshot()
	if snap.Calls != 2 {
		t.Fatalf("expected 2 calls, got %d", snap.Calls)
	}
	if snap.InputTokens != 3000 || snap.CacheReadTokens != 10000 || snap.CacheWriteTokens != 500 {
		t.Fatalf("unexpected aggregates: %+v", snap)
	}
	// hit rate = cache_read / (input + cache_read) = 10000 / 13000 ≈ 76.9%
	want := float64(10000) / float64(13000) * 100
	if snap.HitRatePercent < want-0.01 || snap.HitRatePercent > want+0.01 {
		t.Fatalf("expected hit rate ~%.2f, got %.2f", want, snap.HitRatePercent)
	}
}

func TestAgentUsageHookFiltersBySession(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.registerUsageHook("session-a")
	defer func() {
		a.cacheStatsMu.Lock()
		if a.unsubUsage != nil {
			a.unsubUsage()
			a.unsubUsage = nil
		}
		a.cacheStatsMu.Unlock()
	}()

	event := func(sessionID string, input, cacheRead int) conversation.UsageEvent {
		return conversation.UsageEvent{
			Context: conversation.UsageContext{SessionID: sessionID},
			Response: &protocol.Response{Usage: &protocol.Usage{
				InputTokens:     input,
				CacheReadTokens: cacheRead,
			}},
		}
	}

	// Simulate provider callbacks through the package-level hook dispatcher.
	conversation.NotifyUsageHooksForTest(context.Background(), event("session-a", 100, 900))
	conversation.NotifyUsageHooksForTest(context.Background(), event("session-b", 500, 500))

	snap := a.cacheUsageSnapshot()
	if snap.Calls != 1 || snap.InputTokens != 100 || snap.CacheReadTokens != 900 {
		t.Fatalf("expected only session-a usage recorded, got %+v", snap)
	}

	a.resetCacheStats()
	if got := a.cacheUsageSnapshot(); got.Calls != 0 {
		t.Fatalf("expected reset to clear stats, got %+v", got)
	}
}

func TestCumulativeUsageSurvivesCacheReset(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.registerUsageHook("session-a")
	defer func() {
		a.cacheStatsMu.Lock()
		if a.unsubUsage != nil {
			a.unsubUsage()
			a.unsubUsage = nil
		}
		a.cacheStatsMu.Unlock()
	}()

	event := conversation.UsageEvent{
		Context: conversation.UsageContext{SessionID: "session-a"},
		Response: &protocol.Response{Usage: &protocol.Usage{
			InputTokens:      1000,
			OutputTokens:     200,
			CacheReadTokens:  9000,
			CacheWriteTokens: 100,
		}},
	}
	conversation.NotifyUsageHooksForTest(context.Background(), event)
	conversation.NotifyUsageHooksForTest(context.Background(), event)

	// Total input = uncached input + cache read + cache write per call.
	input, output := a.cumulativeTokenUsage()
	if input != 2*(1000+9000+100) || output != 2*200 {
		t.Fatalf("unexpected cumulative usage: input=%d output=%d", input, output)
	}

	// Clearing the conversation resets cache stats (hit-rate scope) but must
	// keep the session cumulative total intact.
	a.resetCacheStats()
	input, output = a.cumulativeTokenUsage()
	if input != 2*(1000+9000+100) || output != 2*200 {
		t.Fatalf("cumulative usage should survive reset: input=%d output=%d", input, output)
	}

	inspection, err := a.InspectContext(context.Background(), "session-a")
	if err != nil {
		t.Fatalf("inspect context: %v", err)
	}
	if inspection.CumulativeTokens != int(input+output) ||
		inspection.CumulativeInputTokens != int(input) ||
		inspection.CumulativeOutputTokens != int(output) {
		t.Fatalf("expected cumulative usage surfaced in inspection, got %+v", inspection)
	}
}

func TestInspectContextIncludesCacheUsage(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.cacheStatsMu.Lock()
	a.cacheStats = sessionCacheStats{Calls: 3, InputTokens: 1000, CacheReadTokens: 9000, CacheWriteTokens: 100}
	a.cacheStatsMu.Unlock()

	inspection, err := a.InspectContext(context.Background(), "session-cache")
	if err != nil {
		t.Fatalf("inspect context: %v", err)
	}
	if inspection.CacheUsage.Calls != 3 || inspection.CacheUsage.CacheReadTokens != 9000 {
		t.Fatalf("expected cache usage surfaced in inspection, got %+v", inspection.CacheUsage)
	}
	if inspection.CacheUsage.HitRatePercent != 90 {
		t.Fatalf("expected 90%% hit rate, got %.2f", inspection.CacheUsage.HitRatePercent)
	}
}
