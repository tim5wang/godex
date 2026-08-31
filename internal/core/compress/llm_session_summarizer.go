package compress

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/contracts/protocol"
)

const (
	defaultSummaryMaxTokens     = 2048
	llmSummaryAttempts          = 2
	llmSummaryFailureRecovery   = "model_summary_failed_fallback_rule_based"
	llmSummaryEmptyResponseHint = "model_summary_empty_fallback_rule_based"
	llmSummaryReserveTokens     = 16384
)

// LLMSessionSummarizer asks the configured model to write the compaction
// summary while keeping transcript persistence and recent-message retention in
// the same shape as the rule-based compressor.
type LLMSessionSummarizer struct {
	caller     conversation.Caller
	model      string
	maxTokens  int
	compressor *Compressor
	fallback   SessionSummarizer
	// pruneThresholdChars/PruneHeadChars/PruneTailChars configure the
	// model-free tool-result pruner applied to the summary region (Phase 4.1).
	pruneThresholdChars int
	pruneHeadChars      int
	pruneTailChars      int
}

func NewLLMSessionSummarizer(caller conversation.Caller, model string, maxTokens int, compressor *Compressor, fallback SessionSummarizer) *LLMSessionSummarizer {
	if maxTokens <= 0 {
		maxTokens = defaultSummaryMaxTokens
	}
	// Cap at a reasonable upper bound to prevent runaway generation.
	// llmSummaryReserveTokens (16384) is the default reserve budget;
	// allow up to 2x for users who explicitly configure larger.
	maxReserve := llmSummaryReserveTokens * 2
	if maxTokens > maxReserve {
		maxTokens = maxReserve
	}
	return &LLMSessionSummarizer{
		caller:     caller,
		model:      strings.TrimSpace(model),
		maxTokens:  maxTokens,
		compressor: compressor,
		fallback:   fallback,
	}
}

// SetPruneConfig configures the model-free tool-result pruner applied to the
// summary region (Phase 4.1). Non-positive thresholds disable pruning.
func (s *LLMSessionSummarizer) SetPruneConfig(thresholdChars, headChars, tailChars int) {
	s.pruneThresholdChars = thresholdChars
	s.pruneHeadChars = headChars
	s.pruneTailChars = tailChars
}

// reserveTokensFromContextWindow estimates a reasonable token budget for the
// summarization model based on the configured model's max tokens.
func reserveTokensFromContextWindow(modelMaxTokens int) int {
	if modelMaxTokens <= 0 {
		return defaultSummaryMaxTokens
	}
	reserve := modelMaxTokens / 10
	if reserve > llmSummaryReserveTokens {
		reserve = llmSummaryReserveTokens
	}
	if reserve < defaultSummaryMaxTokens {
		reserve = defaultSummaryMaxTokens
	}
	return reserve
}

func (s *LLMSessionSummarizer) SummarizeSession(ctx context.Context, req SessionSummaryRequest) (SessionSummaryResult, error) {
	if len(req.History) == 0 {
		return SessionSummaryResult{}, nil
	}
	if s == nil || s.caller == nil || s.compressor == nil || strings.TrimSpace(s.model) == "" {
		return s.fallbackSummary(ctx, req, nil, "")
	}

	historyForLLM := req.History

	// Incremental update: if PreviousSummary is provided, filter history to only
	// include messages after the last compaction boundary.
	if strings.TrimSpace(req.PreviousSummary) != "" {
		historyForLLM = filterNewMessagesSinceLastCompaction(req.History)
		if len(historyForLLM) == 0 {
			cached := protocol.CloneMessages(req.History)
			return SessionSummaryResult{Messages: cached, TranscriptRefs: transcriptRefs(cached)}, nil
		}
	}

	// Hash-based dedup: skip LLM call when the input is unchanged.
	// Use the full history (not filtered) so all callers benefit from caching.
	hash, err := s.compressor.hashMessagesWithSnapshot(req.History, req.ContinuationSnapshot)
	if err != nil {
		return s.fallbackSummary(ctx, req, []string{"llm_summary_hash_failed: " + err.Error()}, llmSummaryFailureRecovery)
	}
	s.compressor.mu.Lock()
	if s.compressor.hasCached && hash == s.compressor.lastHash {
		cached := protocol.CloneMessages(s.compressor.lastResult)
		s.compressor.mu.Unlock()
		return SessionSummaryResult{Messages: cached, TranscriptRefs: transcriptRefs(cached)}, nil
	}
	s.compressor.mu.Unlock()

	transcript, err := s.compressor.saveTranscript(req.History)
	if err != nil {
		return s.fallbackSummary(ctx, req, []string{"llm_summary_transcript_failed: " + err.Error()}, llmSummaryFailureRecovery)
	}

	messagesForSummary := s.pruneRegionForSummary(historyForLLM, transcript)
	providerReq := s.buildPrefixAlignedRequest(req, transcript, messagesForSummary)

	diagnostics := make([]string, 0, llmSummaryAttempts)
	var lastErr error
	for attempt := 1; attempt <= llmSummaryAttempts; attempt++ {
		resp, err := s.caller.Call(ctx, providerReq)
		if err != nil {
			if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
				return SessionSummaryResult{}, err
			}
			lastErr = err
			diagnostics = append(diagnostics, fmt.Sprintf("llm_summary_attempt_%d_failed: %v", attempt, err))
			continue
		}
		text := strings.TrimSpace(responseText(resp))
		if text == "" {
			lastErr = fmt.Errorf("empty summary response")
			diagnostics = append(diagnostics, fmt.Sprintf("llm_summary_attempt_%d_empty", attempt))
			continue
		}

		result := s.compactWithModelSummary(req.History, text, transcript, req.ContinuationSnapshot)
		s.compressor.mu.Lock()
		s.compressor.lastHash = hash
		s.compressor.lastResult = protocol.CloneMessages(result)
		s.compressor.hasCached = true
		s.compressor.mu.Unlock()
		return SessionSummaryResult{
			Messages:       result,
			TranscriptRefs: transcriptRefs(result),
			Diagnostics:    diagnostics,
			FileOps:        extractFileOpsFromHistory(historyForLLM),
		}, nil
	}

	hint := llmSummaryFailureRecovery
	if lastErr != nil && strings.Contains(lastErr.Error(), "empty summary response") {
		hint = llmSummaryEmptyResponseHint
	}
	return s.fallbackSummary(ctx, req, diagnostics, hint)
}

// filterNewMessagesSinceLastCompaction returns messages that appeared after the
// last KindSummary (compaction boundary) in the history.
func filterNewMessagesSinceLastCompaction(messages []protocol.Message) []protocol.Message {
	lastSummaryIdx := -1
	for i, msg := range messages {
		if msg.Metadata != nil && msg.Metadata.Kind == protocol.KindSummary {
			lastSummaryIdx = i
		}
	}
	if lastSummaryIdx < 0 {
		return messages
	}
	out := messages[lastSummaryIdx+1:]
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *LLMSessionSummarizer) fallbackSummary(ctx context.Context, req SessionSummaryRequest, diagnostics []string, hint string) (SessionSummaryResult, error) {
	if s == nil || s.fallback == nil {
		result := SessionSummaryResult{Messages: protocol.CloneMessages(req.History), Diagnostics: diagnostics, RecoveryHint: hint}
		return result, nil
	}
	result, err := s.fallback.SummarizeSession(ctx, req)
	if err != nil {
		return result, err
	}
	result.Diagnostics = append(diagnostics, result.Diagnostics...)
	if result.RecoveryHint == "" {
		result.RecoveryHint = hint
	}
	return result, nil
}

func (s *LLMSessionSummarizer) compactWithModelSummary(history []protocol.Message, summaryText, transcript, continuationSnapshot string) []protocol.Message {
	summary := buildModelSummaryMessage(summaryText, transcript, continuationSnapshot)
	compact := []protocol.Message{summary}
	// Verbatim retention tail shared with the rule-based path: no truncation,
	// no rewriting, tool-pair aligned — post-compaction cache and working set
	// survive.
	compact = append(compact, s.compressor.RetentionTail(history)...)
	return compact
}

func buildModelSummaryMessage(summaryText, transcript, continuationSnapshot string) protocol.Message {
	var builder strings.Builder
	builder.WriteString("Model-assisted compaction summary\n")
	builder.WriteString("Transcript: ")
	builder.WriteString(transcript)
	builder.WriteString("\n\n")
	builder.WriteString("Use history_search with this transcript when exact older details are needed.\n\n")
	writePinnedContinuationSnapshot(&builder, continuationSnapshot)
	builder.WriteString(strings.TrimSpace(summaryText))
	return protocol.NewSummaryMessage(builder.String(), transcript)
}

// buildPrefixAlignedRequest assembles the summarization request as a prefix of
// the conversation's own model request: [conversation system][quasi-stable
// prefix][region verbatim] + one trailing summary-instruction user message.
// Reusing the conversation's system and message bytes lets the provider serve
// most of the call from its warm prefix cache (DSH-style, summarizer.ts), and
// the model reads the real conversation instead of a flattened blob.
func (s *LLMSessionSummarizer) buildPrefixAlignedRequest(req SessionSummaryRequest, transcript string, region []protocol.Message) protocol.Request {
	messages := make([]protocol.Message, 0, len(req.Prefix)+len(region)+1)
	messages = append(messages, protocol.CloneMessages(req.Prefix)...)
	messages = append(messages, protocol.CloneMessages(region)...)
	messages = append(messages, protocol.NewTextMessage(protocol.RoleUser, buildSummaryInstruction(req, transcript)))
	return conversation.NewRequest(s.model, s.maxTokens, "", req.System, messages, nil)
}

// buildSummaryInstruction renders the trailing user instruction for the
// prefix-aligned summarization call. The conversation's own system prompt is
// already in place, so the instruction is self-contained about the role.
func buildSummaryInstruction(req SessionSummaryRequest, transcript string) string {
	var builder strings.Builder
	builder.WriteString("You are GoDex's session compaction engine. Produce a dense continuation summary for the next assistant turn. Do not call tools.\n")
	previousSummary := strings.TrimSpace(req.PreviousSummary)
	isIncremental := previousSummary != ""
	if isIncremental {
		builder.WriteString("Update the existing session summary with the new conversation messages above.\n")
		builder.WriteString("RULES:\n")
		builder.WriteString("- PRESERVE all existing information from the previous summary\n")
		builder.WriteString("- ADD new progress, decisions, and context from the new messages\n")
		builder.WriteString("- UPDATE the Progress section: move items to Done when completed\n")
		builder.WriteString("- UPDATE Next Steps based on what was accomplished\n")
		builder.WriteString("- PRESERVE exact file paths, function names, and error messages\n")
		builder.WriteString("- If something is no longer relevant, remove it\n\n")
	} else {
		builder.WriteString("Write a compact continuation summary of the conversation above.\n")
		builder.WriteString("The full transcript has been saved as: ")
		builder.WriteString(transcript)
		builder.WriteString("\n\n")
		builder.WriteString("Preserve, in this order:\n")
		builder.WriteString("- User goals and latest instructions\n")
		builder.WriteString("- Constraints, policies, and non-goals\n")
		builder.WriteString("- Decisions already made and rationale when important\n")
		builder.WriteString("- Files, commands, APIs, tests, and verification state\n")
		builder.WriteString("- Tool and subagent outcomes (summarize large results by reference)\n")
		builder.WriteString("- Open questions, blockers, and next actions\n\n")
	}
	builder.WriteString("Use this EXACT format:\n\n")
	builder.WriteString("## Goal\n")
	builder.WriteString("- [What is the user trying to accomplish?]\n\n")
	builder.WriteString("## Constraints & Preferences\n")
	builder.WriteString("- [Any constraints or preferences]\n\n")
	builder.WriteString("## Progress\n")
	builder.WriteString("### Done\n")
	builder.WriteString("- [x] [Completed items]\n")
	builder.WriteString("### In Progress\n")
	builder.WriteString("- [ ] [Current work]\n")
	builder.WriteString("### Blocked\n")
	builder.WriteString("- [Blockers, if any]\n\n")
	builder.WriteString("## Key Decisions\n")
	builder.WriteString("- **[Decision]**: [Rationale]\n\n")
	builder.WriteString("## Next Steps\n")
	builder.WriteString("1. [Ordered list]\n\n")
	builder.WriteString("## Critical Context\n")
	builder.WriteString("- [File paths, error messages, data needed to continue]\n\n")

	if isIncremental {
		builder.WriteString("<previous-summary>\n")
		builder.WriteString(limitRunes(previousSummary, 5000))
		builder.WriteString("\n</previous-summary>\n\n")
	}
	if snapshot := strings.TrimSpace(req.ContinuationSnapshot); snapshot != "" {
		builder.WriteString("<continuation-state>\n")
		builder.WriteString(limitRunes(snapshot, 5000))
		builder.WriteString("\n</continuation-state>\n")
	}
	if len(req.RecentUserMessages) > 0 {
		builder.WriteString("\n<recent-user-messages>\n")
		for i, msg := range limitRecentUserMessages(req.RecentUserMessages) {
			if strings.TrimSpace(msg) == "" {
				continue
			}
			builder.WriteString(fmt.Sprintf("%d. ```text\n%s\n```\n", i+1, limitRunes(strings.TrimSpace(msg), maxRecentUserRunes)))
		}
		builder.WriteString("</recent-user-messages>\n")
	}
	return strings.TrimSpace(builder.String())
}

func limitRecentUserMessages(messages []string) []string {
	if len(messages) == 0 {
		return nil
	}
	out := make([]string, 0, min(len(messages), maxRecentUserMessages))
	for _, msg := range messages {
		msg = strings.TrimSpace(msg)
		if msg == "" {
			continue
		}
		out = append(out, limitRunes(msg, maxRecentUserRunes))
		if len(out) > maxRecentUserMessages {
			out = out[len(out)-maxRecentUserMessages:]
		}
	}
	total := 0
	start := 0
	for i := len(out) - 1; i >= 0; i-- {
		total += len([]rune(out[i]))
		if total > maxRecentUserTotal {
			start = i + 1
			break
		}
	}
	if start > 0 {
		out = out[start:]
	}
	return out
}

// pruneRegionForSummary prepares the region for the prefix-aligned
// summarization call: message bytes stay verbatim (matching the conversation's
// own request so the provider prefix cache stays warm), and oversized tool
// results are pruned to head + marker + tail by the model-free pruner instead
// of being fully stubbed away.
func (s *LLMSessionSummarizer) pruneRegionForSummary(messages []protocol.Message, transcript string) []protocol.Message {
	return PruneOversizedToolResults(messages, s.pruneThresholdChars, s.pruneHeadChars, s.pruneTailChars, transcript)
}

func responseText(resp *protocol.Response) string {
	if resp == nil {
		return ""
	}
	var builder strings.Builder
	for _, block := range resp.Content {
		if block.Type != protocol.BlockText {
			continue
		}
		if strings.TrimSpace(block.Text) == "" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString(block.Text)
	}
	return builder.String()
}

func limitRunes(text string, maxRunes int) string {
	if maxRunes <= 0 || len([]rune(text)) <= maxRunes {
		return text
	}
	runes := []rune(text)
	return string(runes[:maxRunes]) + "\n...[truncated]..."
}
