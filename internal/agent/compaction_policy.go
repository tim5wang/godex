package agent

import (
	"context"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/compress"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/tools"
)

const (
	defaultCompactionTriggerTokens       = 60000
	defaultCompactionTargetHistoryTokens = 12000
	defaultCompactionMaxLatencyMS        = 3000
)

type compactionRunResult struct {
	Messages  []protocol.Message
	Mode      string
	LatencyMS int64
}

type compactionCandidate struct {
	HistoryVersion int64
	Result         compactionRunResult
}

func (a *Agent) compactionTriggerTokens() int {
	if a == nil || a.cfg == nil {
		return defaultCompactionTriggerTokens
	}
	if a.cfg.Compaction.TriggerTokens > 0 {
		return a.cfg.Compaction.TriggerTokens
	}
	if a.cfg.CompressThreshold > 0 {
		return a.cfg.CompressThreshold
	}
	return defaultCompactionTriggerTokens
}

func (a *Agent) compactionTargetHistoryTokens() int {
	if a == nil || a.cfg == nil || a.cfg.Compaction.TargetHistoryTokens <= 0 {
		return defaultCompactionTargetHistoryTokens
	}
	return a.cfg.Compaction.TargetHistoryTokens
}

func (a *Agent) compactionMaxLatencyMS() int {
	if a == nil || a.cfg == nil || a.cfg.Compaction.MaxLatencyMS <= 0 {
		return defaultCompactionMaxLatencyMS
	}
	return a.cfg.Compaction.MaxLatencyMS
}

func (a *Agent) autoCompactionEnabled() bool {
	if a == nil || a.cfg == nil {
		return true
	}
	if a.cfg.Compaction.TriggerTokens == 0 &&
		a.cfg.Compaction.TargetHistoryTokens == 0 &&
		a.cfg.Compaction.Mode == "" &&
		a.cfg.Compaction.MaxLatencyMS == 0 {
		return true
	}
	return a.cfg.Compaction.AutoEnabled
}

func normalizeAgentCompactionMode(mode string) string {
	return config.NormalizeCompactionMode(mode)
}

func (a *Agent) compactionSummarizer(mode string) (compress.SessionSummarizer, string) {
	normalized := normalizeAgentCompactionMode(mode)
	if normalized != "model" && normalized != "hybrid" {
		return compress.NewRuleBasedSessionSummarizer(a.compressor), "fast"
	}
	// model/hybrid compaction must use an LLM-backed summarizer bound to the
	// session's currently active client/model. An explicitly configured
	// summarizer (e.g. tests) is honored first; otherwise build one lazily so
	// web UI sessions whose credentials arrive via ApplyModelProfile (not the
	// startup cfg.APIKey) still get real model compression instead of silently
	// degrading to the rule-based default wired at startup.
	if s := a.summarizer; s != nil {
		if _, ruleBased := s.(*compress.RuleBasedSessionSummarizer); !ruleBased {
			return s, "model"
		}
	}
	if llm := a.buildLLMSummarizerFromSession(); llm != nil {
		return llm, "model"
	}
	return compress.NewRuleBasedSessionSummarizer(a.compressor), "fast"
}

// buildLLMSummarizerFromSession constructs an LLM-backed session summarizer
// from the agent's current client/model. It returns nil when no usable model
// caller is configured so callers fall back to rule-based compaction.
func (a *Agent) buildLLMSummarizerFromSession() compress.SessionSummarizer {
	a.mu.Lock()
	defer a.mu.Unlock()
	client := a.client
	model := ""
	maxTokens := 0
	if a.cfg != nil {
		model = strings.TrimSpace(a.cfg.Model)
		maxTokens = a.cfg.MaxTokens
	}
	if client == nil || model == "" {
		return nil
	}
	rule := compress.NewRuleBasedSessionSummarizer(a.compressor)
	return compress.NewLLMSessionSummarizer(client, model, min(maxTokens, 2048), a.compressor, rule)
}

// DefaultCompactionMode returns the configured default compaction mode
// (fast/model/hybrid) for manual /compact invocations.
func (a *Agent) DefaultCompactionMode() string {
	if a == nil || a.cfg == nil {
		return "fast"
	}
	return normalizeAgentCompactionMode(a.cfg.Compaction.Mode)
}

// extractPreviousSummary scans history for the last KindSummary and returns its text.
func extractPreviousSummary(history []protocol.Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		msg := history[i]
		if msg.Metadata != nil && msg.Metadata.Kind == protocol.KindSummary {
			return strings.TrimSpace(protocol.MessageText(msg))
		}
	}
	return ""
}

func (a *Agent) runCompaction(ctx context.Context, mode string, req compress.SessionSummaryRequest) (compactionRunResult, error) {
	// Extract previous summary from history for incremental updates.
	if strings.TrimSpace(req.PreviousSummary) == "" {
		req.PreviousSummary = extractPreviousSummary(req.History)
	}

	summarizer, effectiveMode := a.compactionSummarizer(mode)
	if summarizer == nil {
		summarizer = compress.NewRuleBasedSessionSummarizer(a.compressor)
		effectiveMode = "fast"
	}
	start := time.Now()
	result, err := summarizer.SummarizeSession(ctx, req)
	latency := time.Since(start).Milliseconds()
	if err != nil && effectiveMode != "fast" && normalizeAgentCompactionMode(mode) == "hybrid" {
		summarizer = compress.NewRuleBasedSessionSummarizer(a.compressor)
		start = time.Now()
		result, err = summarizer.SummarizeSession(ctx, req)
		latency = time.Since(start).Milliseconds()
		effectiveMode = "fast"
	}
	if err != nil {
		return compactionRunResult{}, err
	}
	messages := a.enforceTargetHistoryBudget(result.Messages)
	return compactionRunResult{
		Messages:  messages,
		Mode:      effectiveMode,
		LatencyMS: latency,
	}, nil
}

func (a *Agent) enforceTargetHistoryBudget(messages []protocol.Message) []protocol.Message {
	target := a.compactionTargetHistoryTokens()
	if target <= 0 || estimateMessages(messages) <= target {
		return protocol.CloneMessages(messages)
	}
	if len(messages) <= 2 {
		return protocol.CloneMessages(messages)
	}

	out := make([]protocol.Message, 0, len(messages))
	if messages[0].Metadata != nil && messages[0].Metadata.Kind == protocol.KindSummary {
		out = append(out, messages[0].Clone())
	}
	for i := len(messages) - 1; i >= 0 && len(out) < len(messages); i-- {
		msg := messages[i]
		if msg.Metadata != nil && msg.Metadata.Kind == protocol.KindSummary {
			continue
		}
		next := append([]protocol.Message{msg.Clone()}, out...)
		if estimateMessages(next) > target && len(out) > 0 {
			break
		}
		out = next
		if estimateMessages(out) >= target {
			break
		}
	}
	if len(out) == 0 {
		return protocol.CloneMessages(messages[len(messages)-1:])
	}
	return out
}

func (a *Agent) shouldAutoCompact(estimate contextBudgetEstimate) bool {
	if !a.autoCompactionEnabled() {
		return false
	}
	return shouldAutoCompact(estimate, a.compactionTriggerTokens())
}

func (a *Agent) backgroundCompactionPressure(estimate contextBudgetEstimate) bool {
	if !a.autoCompactionEnabled() {
		return false
	}
	trigger := a.compactionTriggerTokens()
	if trigger <= 0 {
		return false
	}
	preTrigger := int(float64(trigger) * 0.8)
	if preTrigger < 1 {
		preTrigger = trigger
	}
	if estimate.Breakdown.History >= preTrigger {
		return true
	}
	return estimate.Breakdown.Total >= preTrigger && historyHasCompactableContent(estimate.Breakdown.History, trigger)
}

func (a *Agent) takeCompactionCandidate(version int64) *compactionCandidate {
	a.compactionMu.Lock()
	defer a.compactionMu.Unlock()
	candidate := a.compactionCandidate
	if candidate == nil {
		return nil
	}
	a.compactionCandidate = nil
	if candidate.HistoryVersion != version {
		return nil
	}
	return candidate
}

func (a *Agent) storeCompactionCandidate(candidate compactionCandidate) {
	a.compactionMu.Lock()
	defer a.compactionMu.Unlock()
	a.compactionCandidate = &candidate
}

func (a *Agent) maybeStartBackgroundCompaction(ctx context.Context) {
	if !a.autoCompactionEnabled() {
		return
	}
	history, version := a.messageState()
	if len(history) == 0 {
		return
	}
	a.compactionMu.Lock()
	if a.compactionRunning || (a.compactionCandidate != nil && a.compactionCandidate.HistoryVersion == version) {
		a.compactionMu.Unlock()
		return
	}
	a.compactionRunning = true
	a.compactionMu.Unlock()

	go func() {
		defer func() {
			a.compactionMu.Lock()
			a.compactionRunning = false
			a.compactionMu.Unlock()
		}()
		system, err := a.buildRuntimeSystemPrompt(agentProfileFromContext(ctx))
		if err != nil {
			return
		}
		memoryMessages, _, err := a.collectMemoryMessages(history)
		if err != nil {
			return
		}
		promptStateSections, err := a.buildDynamicRuntimePromptSections(agentProfileFromContext(ctx))
		if err != nil {
			return
		}
		promptStateMessages := runtimePromptMessages(promptStateSections)
		runtimeMessages, _ := a.collectRuntimeMessages()
		memoryIndexTokens := 0
		if _, tokens, memErr := a.buildMemoryIndexPromptMessage(); memErr == nil {
			memoryIndexTokens = tokens
		}
		estimate := estimateContextBudget(system, history, memoryMessages, promptStateMessages, runtimeMessages, memoryIndexTokens, a.toolHandler.ActiveSchemas(), a.compactionTriggerTokens())
		if !a.backgroundCompactionPressure(estimate) {
			return
		}
		result, err := a.runCompaction(context.Background(), "fast", compress.SessionSummaryRequest{
			System:               system,
			History:              protocol.CloneMessages(history),
			TokenBreakdown:       tokenBreakdownMap(estimate.Breakdown),
			RecentUserMessages:   recentPersistentUserMessages(history, 6),
			ContinuationSnapshot: a.continuationSnapshot(tools.SessionContextFromContext(ctx).SessionID, history),
		})
		if err != nil {
			return
		}
		_, currentVersion := a.messageState()
		if currentVersion != version {
			return
		}
		a.storeCompactionCandidate(compactionCandidate{HistoryVersion: version, Result: result})
	}()
}

func largestContextSources(breakdown tools.ContextTokenBreakdown) []tools.ContextSourcePressure {
	items := []tools.ContextSourcePressure{
		{Source: "history", Tokens: breakdown.History},
		{Source: "tool_results", Tokens: breakdown.ToolResults},
		{Source: "tool_schemas", Tokens: breakdown.ToolSchemas},
		{Source: "runtime", Tokens: breakdown.Runtime},
		{Source: "memory", Tokens: breakdown.Memory},
		{Source: "system", Tokens: breakdown.System},
		{Source: "attachments", Tokens: breakdown.Attachments},
	}
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].Tokens > items[i].Tokens {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	out := make([]tools.ContextSourcePressure, 0, 3)
	for _, item := range items {
		if item.Tokens <= 0 {
			continue
		}
		out = append(out, item)
		if len(out) == 3 {
			break
		}
	}
	return out
}
