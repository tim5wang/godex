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
	// DSH-style window-scaled policy defaults: trigger ≈ 0.8×128k, verbatim
	// retention tail ≈ 0.16×128k.
	defaultCompactionContextWindowTokens = 128000
	defaultCompactionTriggerRatio        = 0.8
	defaultCompactionRetainRatio         = 0.16
	defaultCompactionRetainTokens        = 20480
	// maxCompactionTriggerTokens caps the auto-compaction trigger so a
	// misconfigured value (e.g. 800000) cannot silently disable compaction.
	// With a trigger above the model's context window the check never fires,
	// context grows unbounded, and long turns degrade into loops / provider
	// overflow. 150k still fits current model windows (128k-200k) while firing
	// before overflow.
	maxCompactionTriggerTokens = 150000
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
	return compactionTriggerTokensFromConfig(a.cfg)
}

// compactionTriggerTokensFromConfig computes the auto-compaction trigger for
// the config's routed model: explicit trigger_tokens / compress_threshold
// wins, otherwise the DSH-style window-scaled value contextWindow ×
// triggerRatio, with per-model policy overrides (Phase 4.3).
func compactionTriggerTokensFromConfig(cfg *config.Config) int {
	provider, model := routedCompactionTarget(cfg)
	return compactionTriggerTokensForTarget(cfg, provider, model)
}

func compactionTriggerTokensForTarget(cfg *config.Config, provider, model string) int {
	if cfg == nil {
		return defaultCompactionTriggerTokens
	}
	trigger := 0
	if cfg.Compaction.TriggerTokens > 0 {
		trigger = cfg.Compaction.TriggerTokens
	} else if cfg.CompressThreshold > 0 {
		trigger = cfg.CompressThreshold
	}
	window := compactionContextWindowTokensFromConfig(cfg)
	ratio := compactionTriggerRatioFromConfig(cfg)
	if policy := compactionPolicyForTarget(cfg, provider, model); policy != nil {
		if policy.TriggerTokens > 0 {
			return clampCompactionTrigger(policy.TriggerTokens, window)
		}
		if policy.ContextWindowTokens > 0 {
			window = policy.ContextWindowTokens
		}
		if policy.TriggerRatio > 0 {
			ratio = policy.TriggerRatio
		}
	}
	if trigger <= 0 {
		trigger = int(float64(window) * ratio)
	}
	return clampCompactionTrigger(trigger, window)
}

// clampCompactionTrigger caps the auto-compaction trigger at the model's
// context window so a misconfigured value cannot silently disable compaction
// (a trigger above the window never fires and context grows unbounded into
// loops / provider overflow). When no window is known (<= 0), the historical
// conservative ceiling (maxCompactionTriggerTokens) applies as a fallback.
func clampCompactionTrigger(trigger, window int) int {
	if trigger <= 0 {
		return defaultCompactionTriggerTokens
	}
	if window > 0 {
		if trigger > window {
			return window
		}
		return trigger
	}
	if trigger > maxCompactionTriggerTokens {
		return maxCompactionTriggerTokens
	}
	return trigger
}

// compactionContextWindowTokensFromConfig resolves the model context window
// used to scale the auto-compaction trigger and the verbatim retention tail.
// Priority: the routed model profile's own context_window_tokens (per-model
// provider config), then the global agent.compaction.context_window_tokens,
// then the DSH-style default (128k).
func compactionContextWindowTokensFromConfig(cfg *config.Config) int {
	if cfg == nil {
		return defaultCompactionContextWindowTokens
	}
	if profile := cfg.DefaultModelProfile(); profile.ContextWindowTokens > 0 {
		return profile.ContextWindowTokens
	}
	if cfg.Compaction.ContextWindowTokens > 0 {
		return cfg.Compaction.ContextWindowTokens
	}
	return defaultCompactionContextWindowTokens
}

func compactionTriggerRatioFromConfig(cfg *config.Config) float64 {
	if cfg == nil || cfg.Compaction.TriggerRatio <= 0 {
		return defaultCompactionTriggerRatio
	}
	return cfg.Compaction.TriggerRatio
}

func compactionRetainRatioFromConfig(cfg *config.Config) float64 {
	if cfg == nil || cfg.Compaction.RetainRatio <= 0 {
		return defaultCompactionRetainRatio
	}
	return cfg.Compaction.RetainRatio
}

// routedCompactionTarget resolves the provider/model used for per-model policy
// matching: the effective default model profile. When no real profile is
// configured (synthetic fallback), the provider is left empty so policies with
// an empty provider match any upstream.
func routedCompactionTarget(cfg *config.Config) (string, string) {
	if cfg == nil {
		return "", ""
	}
	profile := cfg.DefaultModelProfile()
	model := strings.TrimSpace(profile.Model)
	if model == "" {
		model = strings.TrimSpace(cfg.Model)
	}
	provider := strings.TrimSpace(profile.Provider)
	if _, ok := cfg.ModelProfileByID(profile.ID); !ok && len(cfg.ModelProfiles) == 0 {
		provider = ""
	}
	return provider, model
}

// compactionPolicyForTarget finds the best per-model policy override (Phase
// 4.3): the provider must match exactly (when specified) and the model pattern
// must match by exact/prefix/wildcard; the longest matching model pattern wins.
func compactionPolicyForTarget(cfg *config.Config, provider, model string) *config.CompactionModelPolicy {
	if cfg == nil {
		return nil
	}
	var best *config.CompactionModelPolicy
	bestLen := -1
	for i := range cfg.Compaction.ModelPolicies {
		policy := &cfg.Compaction.ModelPolicies[i]
		if strings.TrimSpace(policy.Provider) != "" && !strings.EqualFold(strings.TrimSpace(policy.Provider), strings.TrimSpace(provider)) {
			continue
		}
		pattern := strings.TrimSpace(policy.Model)
		if pattern != "" && !compactionModelMatches(pattern, model) {
			continue
		}
		if len(pattern) > bestLen {
			best = policy
			bestLen = len(pattern)
		}
	}
	return best
}

// compactionModelMatches matches a policy model pattern: exact, bare prefix
// ("gpt-5" matches "gpt-5-codex"), or wildcard prefix ("gpt-5*").
func compactionModelMatches(pattern, model string) bool {
	if pattern == "" || model == "" {
		return false
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(model, strings.TrimSuffix(pattern, "*"))
	}
	return pattern == model || strings.HasPrefix(model, pattern)
}

// compactionRetainTokens returns the verbatim retention tail budget: explicit
// retain_tokens wins; otherwise contextWindow × retainRatio. It is clamped
// below the trigger so a misconfiguration cannot ask for a tail larger than
// the compaction threshold.
func (a *Agent) compactionRetainTokens() int {
	if a == nil || a.cfg == nil {
		return defaultCompactionRetainTokens
	}
	return compactionRetainTokensFromConfig(a.cfg)
}

// compactionRetainTokensFromConfig computes the verbatim retention tail budget
// shared by the agent and the compressor wiring.
func compactionRetainTokensFromConfig(cfg *config.Config) int {
	provider, model := routedCompactionTarget(cfg)
	return compactionRetainTokensForTarget(cfg, provider, model)
}

func compactionRetainTokensForTarget(cfg *config.Config, provider, model string) int {
	if cfg == nil {
		return defaultCompactionRetainTokens
	}
	window := compactionContextWindowTokensFromConfig(cfg)
	ratio := compactionRetainRatioFromConfig(cfg)
	retain := cfg.Compaction.RetainTokens
	if policy := compactionPolicyForTarget(cfg, provider, model); policy != nil {
		if policy.RetainTokens > 0 {
			retain = policy.RetainTokens
		}
		if policy.ContextWindowTokens > 0 {
			window = policy.ContextWindowTokens
		}
		if policy.RetainRatio > 0 {
			ratio = policy.RetainRatio
		}
	}
	if retain <= 0 {
		retain = int(float64(window) * ratio)
	}
	trigger := compactionTriggerTokensForTarget(cfg, provider, model)
	if trigger > 0 && retain >= trigger {
		return trigger - 1
	}
	if retain <= 0 {
		return defaultCompactionRetainTokens
	}
	return retain
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

// applyCompactionPruneConfig wires the model-free tool-result pruner config
// (Phase 4.1) onto an LLM-backed summarizer.
func applyCompactionPruneConfig(summarizer *compress.LLMSessionSummarizer, cfg *config.Config) {
	if summarizer == nil || cfg == nil {
		return
	}
	summarizer.SetPruneConfig(
		cfg.Compaction.PruneThresholdChars,
		cfg.Compaction.PruneHeadChars,
		cfg.Compaction.PruneTailChars,
	)
}

// compactForOverflow force-compacts the session history so a provider
// context-overflow retry starts from a smaller prefix (Phase 4.2). Unlike
// maybeAutoCompact it bypasses the pressure threshold entirely and uses
// budget-constrained compaction: oversized tool results are stubbed to
// transcript references and only a small recent tail stays verbatim, so the
// emergency actually shrinks the request (the normal verbatim tail would keep
// small histories unchanged and the retry would overflow again). Returns true
// when the history was actually rewritten.
func (a *Agent) compactForOverflow(ctx context.Context) bool {
	if a == nil || a.compressor == nil {
		return false
	}
	history, version := a.messageState()
	if len(history) == 0 {
		return false
	}
	budget := a.compactionTriggerTokens()
	if budget <= 0 {
		budget = defaultCompactionTriggerTokens
	}
	compacted, err := a.compressor.CompactForBudget(history, budget)
	if err != nil || len(compacted) == 0 {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.historyVersion != version {
		return false
	}
	a.messages = protocol.CloneMessages(compacted)
	a.transcriptRefs = mergeTranscriptRefs(a.transcriptRefs, extractTranscriptRefs(compacted))
	a.historyVersion++
	a.lastCompactedVersion = a.historyVersion
	return true
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

// autoCompactionMode returns the compaction mode used by automatic compaction
// (sync + background), honoring the configured mode with the "fast" default so
// an empty config never surprises.
func (a *Agent) autoCompactionMode() string {
	if a == nil || a.cfg == nil {
		return "fast"
	}
	return normalizeAgentCompactionMode(a.cfg.Compaction.Mode)
}

func normalizeAgentCompactionMode(mode string) string {
	return config.NormalizeCompactionMode(mode)
}

func (a *Agent) compactionSummarizer(mode string) (compress.SessionSummarizer, string) {
	return a.compactionSummarizerFor(mode, a.compressor)
}

// compactionSummarizerFor builds the summarizer for the mode bound to the given
// compressor (an emergency path may pass a scoped compressor with a smaller
// retention tail).
func (a *Agent) compactionSummarizerFor(mode string, compressor *compress.Compressor) (compress.SessionSummarizer, string) {
	normalized := normalizeAgentCompactionMode(mode)
	if normalized != "model" && normalized != "hybrid" {
		return compress.NewRuleBasedSessionSummarizer(compressor), "fast"
	}
	// model/hybrid compaction must use an LLM-backed summarizer bound to the
	// session's currently active client/model. An explicitly configured
	// summarizer (e.g. tests) is honored first; otherwise build one lazily so
	// web UI sessions whose credentials arrive via ApplyModelProfile (not the
	// startup cfg.APIKey) still get real model compression instead of silently
	// degrading to the rule-based default wired at startup.
	if compressor == a.compressor {
		if s := a.summarizer; s != nil {
			if _, ruleBased := s.(*compress.RuleBasedSessionSummarizer); !ruleBased {
				return s, "model"
			}
		}
	}
	if llm := a.buildLLMSummarizerFor(compressor); llm != nil {
		return llm, "model"
	}
	return compress.NewRuleBasedSessionSummarizer(compressor), "fast"
}

// buildLLMSummarizerFromSession constructs an LLM-backed session summarizer
// from the agent's current client/model. It returns nil when no usable model
// caller is configured so callers fall back to rule-based compaction.
func (a *Agent) buildLLMSummarizerFromSession() compress.SessionSummarizer {
	return a.buildLLMSummarizerFor(a.compressor)
}

func (a *Agent) buildLLMSummarizerFor(compressor *compress.Compressor) compress.SessionSummarizer {
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
	rule := compress.NewRuleBasedSessionSummarizer(compressor)
	llmSummarizer := compress.NewLLMSessionSummarizer(client, model, min(maxTokens, 2048), compressor, rule)
	applyCompactionPruneConfig(llmSummarizer, a.cfg)
	return llmSummarizer
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
	// Retention (verbatim tail + summary) is handled inside the compressor;
	// target_history_tokens no longer crunches the retained tail down.
	return compactionRunResult{
		Messages:  result.Messages,
		Mode:      effectiveMode,
		LatencyMS: latency,
	}, nil
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
		result, err := a.runCompaction(context.Background(), a.autoCompactionMode(), compress.SessionSummaryRequest{
			System:               system,
			Prefix:               protocol.CloneMessages(promptStateMessages),
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
