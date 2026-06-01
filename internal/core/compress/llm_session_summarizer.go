package compress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/core/modelcontext"
	"github.com/tim5wang/godex/internal/core/protocol"
)

const (
	defaultSummaryMaxTokens     = 2048
	llmSummaryAttempts          = 2
	llmSummaryMaxRenderedRunes  = 90000
	llmSummaryMaxBlockRunes     = 8000
	llmSummaryMaxJSONRunes      = 6000
	llmSummaryRecentKeep        = 10
	llmSummaryPromptSystem      = "You are GoDex's session compaction engine. Produce a dense continuation summary for the next assistant turn. Do not call tools."
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

	messagesForSummary := sanitizeMessagesForSummaryModel(historyForLLM, transcript)
	prompt := buildLLMSummaryPrompt(req, transcript, messagesForSummary)
	messages := []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, prompt)}
	providerReq := conversation.NewRequest(s.model, s.maxTokens, "", llmSummaryPromptSystem, messages, nil)

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
	recent := history
	if len(history) > llmSummaryRecentKeep {
		recent = history[len(history)-llmSummaryRecentKeep:]
	}
	for _, msg := range recent {
		compact = append(compact, sanitizeRecentMessageForContext(msg, transcript))
	}
	return compact
}

func buildModelSummaryMessage(summaryText, transcript, continuationSnapshot string) protocol.Message {
	var builder strings.Builder
	builder.WriteString("Model-assisted compaction summary\n")
	builder.WriteString("Compressed at: ")
	builder.WriteString(time.Now().Format("2006-01-02 15:04"))
	builder.WriteString("\nTranscript: ")
	builder.WriteString(transcript)
	builder.WriteString("\n\n")
	builder.WriteString("Use history_search with this transcript when exact older details are needed.\n\n")
	writePinnedContinuationSnapshot(&builder, continuationSnapshot)
	builder.WriteString(strings.TrimSpace(summaryText))
	return protocol.NewSummaryMessage(builder.String(), transcript)
}

func buildLLMSummaryPrompt(req SessionSummaryRequest, transcript string, messages []protocol.Message) string {
	var builder strings.Builder

	// Check if this is an incremental update (previous summary exists)
	previousSummary := strings.TrimSpace(req.PreviousSummary)
	isIncremental := previousSummary != ""

	if isIncremental {
		builder.WriteString("Update the existing session summary with new conversation messages.\n")
		builder.WriteString("RULES:\n")
		builder.WriteString("- PRESERVE all existing information from the previous summary\n")
		builder.WriteString("- ADD new progress, decisions, and context from the new messages\n")
		builder.WriteString("- UPDATE the Progress section: move items to Done when completed\n")
		builder.WriteString("- UPDATE Next Steps based on what was accomplished\n")
		builder.WriteString("- PRESERVE exact file paths, function names, and error messages\n")
		builder.WriteString("- If something is no longer relevant, remove it\n\n")
	} else {
		builder.WriteString("Write a compact continuation summary for this GoDex session.\n")
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

	if strings.TrimSpace(req.System) != "" && !isIncremental {
		builder.WriteString("\n<system-context>\n")
		builder.WriteString(limitRunes(req.System, llmSummaryMaxBlockRunes))
		builder.WriteString("\n</system-context>\n")
	}
	if len(req.TokenBreakdown) > 0 && !isIncremental {
		builder.WriteString("\n<token-pressure>\n")
		for _, key := range sortedMapKeys(req.TokenBreakdown) {
			builder.WriteString(fmt.Sprintf("- %s: %d\n", key, req.TokenBreakdown[key]))
		}
		builder.WriteString("</token-pressure>\n")
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

	if isIncremental {
		builder.WriteString("\n<new-conversation>\n")
		builder.WriteString(renderMessagesForSummaryPrompt(messages, llmSummaryMaxRenderedRunes))
		builder.WriteString("\n</new-conversation>\n")
	} else {
		builder.WriteString("\n<conversation>\n")
		builder.WriteString(renderMessagesForSummaryPrompt(messages, llmSummaryMaxRenderedRunes))
		builder.WriteString("\n</conversation>\n")
	}

	return builder.String()
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

func sanitizeMessagesForSummaryModel(messages []protocol.Message, transcript string) []protocol.Message {
	cloned := protocol.CloneMessages(messages)
	for msgIdx := range cloned {
		for blockIdx, block := range cloned[msgIdx].Content {
			switch block.Type {
			case protocol.BlockText:
				cloned[msgIdx].Content[blockIdx].Text = limitRunes(block.Text, llmSummaryMaxBlockRunes)
			case protocol.BlockToolResult:
				if modelcontext.TooLargeForModel(block.Content) {
					cloned[msgIdx].Content[blockIdx].Content = modelcontext.SummaryJSON(modelcontext.LargeToolResultSummary{
						ToolUseID:  block.ToolUseID,
						Bytes:      len([]byte(block.Content)),
						SHA256:     modelcontext.SHA256Hex(block.Content),
						Transcript: transcript,
						Note:       "Large tool result omitted from model summary input; use the saved transcript for full output.",
					})
					continue
				}
				cloned[msgIdx].Content[blockIdx].Content = limitRunes(block.Content, llmSummaryMaxBlockRunes)
			case protocol.BlockToolUse:
				cloned[msgIdx].Content[blockIdx].Input = limitMapForSummary(block.Input)
			}
		}
	}
	return cloned
}

func limitMapForSummary(input map[string]interface{}) map[string]interface{} {
	if input == nil {
		return nil
	}
	data, err := json.Marshal(input)
	if err != nil || len([]rune(string(data))) <= llmSummaryMaxJSONRunes {
		return cloneStringMap(input)
	}
	return map[string]interface{}{
		"summary": limitRunes(string(data), llmSummaryMaxJSONRunes),
	}
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

func renderMessagesForSummaryPrompt(messages []protocol.Message, maxRunes int) string {
	var builder strings.Builder
	for idx, msg := range messages {
		if maxRunes > 0 && len([]rune(builder.String())) >= maxRunes {
			builder.WriteString("\n...[transcript excerpt truncated]...\n")
			break
		}
		builder.WriteString("\nMessage ")
		builder.WriteString(fmt.Sprintf("%d", idx+1))
		builder.WriteString(" role=")
		builder.WriteString(msg.Role)
		if msg.Metadata != nil && msg.Metadata.Kind != "" {
			builder.WriteString(" kind=")
			builder.WriteString(string(msg.Metadata.Kind))
		}
		builder.WriteString("\n")
		for _, block := range msg.Content {
			switch block.Type {
			case protocol.BlockText:
				builder.WriteString("text: ")
				builder.WriteString(limitRunes(block.Text, llmSummaryMaxBlockRunes))
				builder.WriteString("\n")
			case protocol.BlockToolUse:
				builder.WriteString("tool_use ")
				builder.WriteString(block.Name)
				if block.ID != "" {
					builder.WriteString(" id=")
					builder.WriteString(block.ID)
				}
				if block.Input != nil {
					data, err := json.Marshal(block.Input)
					if err == nil {
						builder.WriteString(" input=")
						builder.WriteString(limitRunes(string(data), llmSummaryMaxJSONRunes))
					}
				}
				builder.WriteString("\n")
			case protocol.BlockToolResult:
				builder.WriteString("tool_result")
				if block.ToolUseID != "" {
					builder.WriteString(" for=")
					builder.WriteString(block.ToolUseID)
				}
				builder.WriteString(": ")
				builder.WriteString(limitRunes(block.Content, llmSummaryMaxBlockRunes))
				builder.WriteString("\n")
			case protocol.BlockImage:
				builder.WriteString("image: omitted; metadata only\n")
			}
		}
	}
	return limitRunes(builder.String(), maxRunes)
}

func sortedMapKeys(values map[string]int) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func limitRunes(text string, maxRunes int) string {
	if maxRunes <= 0 || len([]rune(text)) <= maxRunes {
		return text
	}
	runes := []rune(text)
	return string(runes[:maxRunes]) + "\n...[truncated]..."
}

func cloneStringMap(input map[string]interface{}) map[string]interface{} {
	if input == nil {
		return nil
	}
	data, err := json.Marshal(input)
	if err != nil {
		return map[string]interface{}{"summary": fmt.Sprintf("%v", input)}
	}
	var cloned map[string]interface{}
	if err := json.Unmarshal(data, &cloned); err != nil {
		return map[string]interface{}{"summary": string(data)}
	}
	return cloned
}
