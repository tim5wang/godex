package agent

import (
	"context"
	"strings"

	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/tools"
)

// sessionCacheStats accumulates provider-reported prompt-cache usage for the
// current session so the UI can show the real cache hit rate rather than only
// the static prefix estimate.
type sessionCacheStats struct {
	Calls            int
	InputTokens      int64
	CacheReadTokens  int64
	CacheWriteTokens int64
}

// recordUsage folds one provider usage event into the per-session cache
// aggregation. Events without usage data or without a real input are ignored
// so failed/estimated calls don't distort the hit rate.
func (s *sessionCacheStats) recordUsage(event conversation.UsageEvent) {
	if event.Response == nil || event.Response.Usage == nil {
		return
	}
	usage := event.Response.Usage
	input := int64(usage.InputTokens)
	cacheRead := int64(usage.CacheReadTokens)
	cacheWrite := int64(usage.CacheWriteTokens)
	if input == 0 && cacheRead == 0 && cacheWrite == 0 {
		return
	}
	s.Calls++
	s.InputTokens += input
	s.CacheReadTokens += cacheRead
	s.CacheWriteTokens += cacheWrite
}

// snapshot returns the aggregated metrics plus derived rates.
func (s sessionCacheStats) snapshot() tools.CacheUsageInspection {
	if s.Calls == 0 {
		return tools.CacheUsageInspection{}
	}
	// InputTokens is the uncached input (see protocol.Usage), so the
	// provider-visible total input is InputTokens + CacheReadTokens and the
	// hit rate matches the supplier dashboard:
	// cache_read / (uncached input + cache_read).
	denom := s.InputTokens + s.CacheReadTokens
	hitRate := 0.0
	if denom > 0 {
		hitRate = float64(s.CacheReadTokens) / float64(denom) * 100
	}
	return tools.CacheUsageInspection{
		Calls:            s.Calls,
		InputTokens:      s.InputTokens,
		CacheReadTokens:  s.CacheReadTokens,
		CacheWriteTokens: s.CacheWriteTokens,
		HitRatePercent:   hitRate,
	}
}

// registerUsageHook subscribes the agent to provider usage events for its
// session. The hook only filters by session id and aggregates in memory, so
// it stays cheap enough to run on every model call.
func (a *Agent) registerUsageHook(sessionID string) {
	a.cacheStatsMu.Lock()
	if a.unsubUsage != nil {
		a.cacheStatsMu.Unlock()
		return
	}
	a.cacheStatsMu.Unlock()

	targetSession := strings.TrimSpace(sessionID)
	unsub := conversation.AddUsageHook(func(_ context.Context, event conversation.UsageEvent) {
		if targetSession == "" || strings.TrimSpace(event.Context.SessionID) != targetSession {
			return
		}
		a.cacheStatsMu.Lock()
		a.cacheStats.recordUsage(event)
		a.cacheStatsMu.Unlock()
	})
	a.cacheStatsMu.Lock()
	a.unsubUsage = unsub
	a.cacheStatsMu.Unlock()
}

// cacheUsageSnapshot returns the current real cache usage for the session.
func (a *Agent) cacheUsageSnapshot() tools.CacheUsageInspection {
	a.cacheStatsMu.Lock()
	defer a.cacheStatsMu.Unlock()
	return a.cacheStats.snapshot()
}

// resetCacheStats clears the aggregation when the conversation is cleared so
// the hit rate reflects only the current context window.
func (a *Agent) resetCacheStats() {
	a.cacheStatsMu.Lock()
	a.cacheStats = sessionCacheStats{}
	a.cacheStatsMu.Unlock()
}
