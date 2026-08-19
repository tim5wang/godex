package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/tools"
)

// Safety valve: compaction trigger is clamped to the model context window so a
// misconfigured value (e.g. 800000, above any model window) cannot silently
// disable auto-compaction and let a long turn's context grow unbounded into a
// loop / overflow.  The ceiling follows the model window: a per-model or
// global context_window_tokens raises it, so a user-configured 230k trigger
// is respected when the model actually supports a 230k window.
func TestCompactionTriggerTokensClamped(t *testing.T) {
	a := newTestAgent(t, 4096)
	// Absurd triggers clamp to the default window (128k).
	a.cfg.Compaction.TriggerTokens = 800000
	if got := a.compactionTriggerTokens(); got != defaultCompactionContextWindowTokens {
		t.Fatalf("expected absurd trigger clamped to default window %d, got %d", defaultCompactionContextWindowTokens, got)
	}
	// Sane values below the ceiling pass through untouched.
	a.cfg.Compaction.TriggerTokens = 80
	if got := a.compactionTriggerTokens(); got != 80 {
		t.Fatalf("expected small trigger untouched, got %d", got)
	}
	// Default path also clamps.
	a.cfg.Compaction.TriggerTokens = 0
	a.cfg.CompressThreshold = 900000
	if got := a.compactionTriggerTokens(); got != defaultCompactionContextWindowTokens {
		t.Fatalf("expected compress_threshold clamped to default window too, got %d", got)
	}
	// A 230k global window raises the ceiling: a 230k trigger is honored
	// instead of being pinned to the historical 150k cap.
	a.cfg.Compaction.TriggerTokens = 230000
	a.cfg.Compaction.ContextWindowTokens = 230000
	if got := a.compactionTriggerTokens(); got != 230000 {
		t.Fatalf("expected 230k trigger honored with 230k window, got %d", got)
	}
	// Trigger above the configured window is still clamped to the window.
	a.cfg.Compaction.TriggerTokens = 400000
	if got := a.compactionTriggerTokens(); got != 230000 {
		t.Fatalf("expected 400k trigger clamped to 230k window, got %d", got)
	}
}

// Safety valve: a project ledger that has not been refreshed by a completed
// turn within the window is stale (older task phase / stalled marathon turn)
// and must NOT be injected into the active context.
func TestStaleProjectLedgerNotInjected(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	ctx := tools.WithSessionContext(context.Background(), automation.SessionContext{
		SessionID:              "session-stale-ledger",
		ProjectLedger:          "Goal: old task from six hours ago\nCurrent phase: blocked",
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
		SessionID:              "session-fresh-ledger",
		ProjectLedger:          "Goal: current task\nCurrent phase: active",
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

// Window-scaled policy (DSH-style): with no explicit trigger/retain tokens,
// the threshold and the verbatim retention tail derive from the model context
// window × ratio, and retain is clamped below the trigger.
func TestCompactionWindowScaledPolicy(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.cfg.Compaction.TriggerTokens = 0
	a.cfg.CompressThreshold = 0
	a.cfg.Compaction.ContextWindowTokens = 128000
	a.cfg.Compaction.TriggerRatio = 0.8
	a.cfg.Compaction.RetainRatio = 0.16
	a.cfg.Compaction.RetainTokens = 0

	if got := a.compactionTriggerTokens(); got != 102400 {
		t.Fatalf("expected 0.8×128k trigger, got %d", got)
	}
	if got := a.compactionRetainTokens(); got != 20480 {
		t.Fatalf("expected 0.16×128k retain, got %d", got)
	}

	// Explicit retain_tokens wins over the ratio, clamped below the trigger.
	a.cfg.Compaction.RetainTokens = 200000
	if got := a.compactionRetainTokens(); got != 102399 {
		t.Fatalf("expected retain clamped below trigger, got %d", got)
	}
}

// Per-model policy table (Phase 4.3): exact provider + longest model prefix
// match wins; explicit policy fields override the global window/ratio.
func TestCompactionModelPolicyOverride(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.cfg.Compaction.TriggerTokens = 0
	a.cfg.CompressThreshold = 0
	a.cfg.Compaction.ContextWindowTokens = 128000
	a.cfg.Compaction.TriggerRatio = 0.8
	a.cfg.Compaction.RetainRatio = 0.16
	a.cfg.Compaction.ModelPolicies = []config.CompactionModelPolicy{
		{Provider: "deepseek", Model: "deepseek-chat", ContextWindowTokens: 64000, TriggerRatio: 0.5, RetainRatio: 0.25},
		{Provider: "deepseek", Model: "gpt", RetainTokens: 9000},
	}
	// A real profile makes the routed provider/model resolvable for matching.
	a.cfg.DefaultProfileID = "main"
	a.cfg.ModelProfiles = map[string]config.ModelProfileConfig{
		"main": {ID: "main", Provider: "deepseek", Model: "deepseek-chat"},
	}

	// The most specific policy (deepseek-chat, window 64k) wins: trigger
	// 64k×0.5, retain 64k×0.25.
	if got := a.compactionTriggerTokens(); got != 32000 {
		t.Fatalf("expected 64k×0.5 trigger from policy, got %d", got)
	}
	if got := a.compactionRetainTokens(); got != 16000 {
		t.Fatalf("expected 64k×0.25 retain from policy, got %d", got)
	}

	// Model prefix match: "gpt" matches "gpt-5" by prefix; its explicit
	// retain_tokens applies while trigger falls back to the global window.
	a.cfg.DefaultProfileID = "gpt"
	a.cfg.ModelProfiles = map[string]config.ModelProfileConfig{
		"main": {ID: "main", Provider: "deepseek", Model: "deepseek-chat"},
		"gpt":  {ID: "gpt", Provider: "deepseek", Model: "gpt-5"},
	}
	if got := a.compactionRetainTokens(); got != 9000 {
		t.Fatalf("expected prefix-policy retain 9000, got %d", got)
	}
	if got := a.compactionTriggerTokens(); got != 102400 {
		t.Fatalf("expected global 128k×0.8 trigger, got %d", got)
	}

	// No matching policy: global window/ratio apply.
	a.cfg.DefaultProfileID = "none"
	a.cfg.ModelProfiles = map[string]config.ModelProfileConfig{
		"main": {ID: "main", Provider: "deepseek", Model: "deepseek-chat"},
		"gpt":  {ID: "gpt", Provider: "deepseek", Model: "gpt-5"},
		"none": {ID: "none", Provider: "openai", Model: "claude-x"},
	}
	if got := a.compactionTriggerTokens(); got != 102400 {
		t.Fatalf("expected global 128k×0.8 trigger, got %d", got)
	}
}

// compactForOverflow (Phase 4.2) force-compacts history regardless of pressure
// and reports whether the history was rewritten.
func TestCompactForOverflowRewritesHistory(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.cfg.Compaction.Mode = "fast"
	big := strings.Repeat("tool output line\n", 3000)
	for i := 0; i < 8; i++ {
		a.AddMessage("user step " + fmt.Sprint(i))
		a.AddMessage("assistant step " + fmt.Sprint(i))
	}
	a.appendMessage(protocol.NewMessage(protocol.RoleUser, protocol.ToolResultBlock("tool-1", big)))

	before, beforeVersion := a.messageState()
	if !a.compactForOverflow(context.Background()) {
		t.Fatal("expected overflow compaction to rewrite history")
	}
	after, _ := a.messageState()
	if estimateMessages(after) >= estimateMessages(before) {
		t.Fatalf("expected smaller history after overflow compaction: before=%d after=%d", estimateMessages(before), estimateMessages(after))
	}
	if a.historyVersion <= beforeVersion {
		t.Fatal("expected history version to advance")
	}
}
