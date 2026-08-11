package agent

import (
	"context"
	"strings"

	"github.com/tim5wang/godex/internal/core/compress"
	"github.com/tim5wang/godex/internal/core/insights"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/tools"
)

// InspectContext summarizes the current prompt budget without mutating history.
func (a *Agent) InspectContext(ctx context.Context, sessionID string) (tools.ContextInspection, error) {
	system, err := a.buildRuntimeSystemPrompt(agentProfileFromContext(ctx))
	if err != nil {
		return tools.ContextInspection{}, err
	}
	history, _ := a.messageState()
	history = dedupeRepeatedLargeToolResultSummaries(history)
	memoryMessages, _, err := a.collectMemoryMessages(history)
	if err != nil {
		return tools.ContextInspection{}, err
	}
	agentProfile := agentProfileFromContext(ctx)
	memoryIndexTokens := 0
	if _, tokens, err := a.buildMemoryIndexPromptMessage(); err == nil {
		memoryIndexTokens = tokens
	}
	promptStateSections, err := a.buildDynamicRuntimePromptSections(agentProfile)
	if err != nil {
		return tools.ContextInspection{}, err
	}
	promptStateMessages := runtimePromptMessages(promptStateSections)
	runtimeMessages, _ := a.collectRuntimeMessages()
	volatileMessages := append(protocol.CloneMessages(memoryMessages), protocol.CloneMessages(runtimeMessages)...)

	toolSchemas := a.toolHandler.ActiveSchemas()
	triggerTokens := a.compactionTriggerTokens()
	estimate := estimateContextBudget(system, history, memoryMessages, promptStateMessages, runtimeMessages, memoryIndexTokens, toolSchemas, triggerTokens)
	pendingCount := 0
	if a.permissions != nil && strings.TrimSpace(sessionID) != "" {
		pendingCount = len(a.permissions.ListPending(sessionID))
	}
	return tools.ContextInspection{
		SessionID:                     strings.TrimSpace(sessionID),
		MessageCount:                  len(history),
		TokenEstimate:                 estimate.Breakdown.Total,
		HistoryTokenEstimate:          estimate.Breakdown.History,
		TotalTokenEstimate:            estimate.Breakdown.Total,
		TokenBreakdown:                estimate.Breakdown,
		PrefixCache:                   prefixCacheInspection(system, toolSchemas, history, promptStateSections, memoryIndexTokens, volatileMessages),
		CacheUsage:                    a.cacheUsageSnapshot(),
		CompressThreshold:             triggerTokens,
		SuggestCompact:                len(estimate.Reasons) > 0,
		CompressionReasons:            append([]string{}, estimate.Reasons...),
		PreCompactionTotal:            estimate.Breakdown.Total,
		PostCompactionTotal:           estimate.Breakdown.Total,
		CompactionMode:                normalizeAgentCompactionMode(a.cfg.Compaction.Mode),
		LargestContextSources:         largestContextSources(estimate.Breakdown),
		ActiveSkillCount:              len(a.ActiveSkillNames()),
		PendingPermissionCount:        pendingCount,
		LargeToolResultReferenceCount: estimate.LargeToolResultReferenceCount,
		ToolResultReferences:          append([]tools.ToolResultReference{}, estimate.ToolResultReferences...),
	}, nil
}

// CompactConversation manually compacts the persistent conversation history.
func (a *Agent) CompactConversation() (string, error) {
	return a.CompactConversationWithMode("fast")
}

// CompactConversationWithMode manually compacts persistent conversation history.
func (a *Agent) CompactConversationWithMode(mode string) (string, error) {
	system, err := a.buildRuntimeSystemPrompt()
	if err != nil {
		return "", err
	}
	history, _ := a.messageState()
	if len(history) == 0 {
		return "No messages to compress", nil
	}

	result, err := a.runCompaction(context.Background(), mode, compress.SessionSummaryRequest{
		System:               system,
		History:              protocol.CloneMessages(history),
		RecentUserMessages:   recentPersistentUserMessages(history, 6),
		ContinuationSnapshot: a.continuationSnapshot("", history),
	})
	if err != nil {
		return "", err
	}
	compacted := result.Messages

	a.storeCompactedMessages(compacted)

	summary := "Conversation compressed."
	if len(compacted) > 0 {
		if text := protocol.MessageText(compacted[0]); text != "" {
			summary = text
		}
	}
	return summary, nil
}

func (a *Agent) captureMemoryCandidates() error {
	if a.memoryStrategy != nil {
		messages, _ := a.messageState()
		_, err := a.memoryStrategy.Capture(messages)
		return err
	}
	if a.memoryExt == nil {
		return nil
	}
	messages, _ := a.messageState()
	_, err := a.memoryExt.Capture(messages)
	return err
}

// CaptureInsightMemoryCandidates stores durable memory suggestions derived from an insights report.
func (a *Agent) CaptureInsightMemoryCandidates(report *insights.Report) error {
	if a.memoryExt == nil {
		return nil
	}
	_, err := a.memoryExt.CaptureInsightsReport(report)
	return err
}

// CaptureTimelineMemoryCandidates stores durable memory suggestions derived from runtime timeline events.
func (a *Agent) CaptureTimelineMemoryCandidates(items []events.Event) error {
	if a.memoryExt == nil {
		return nil
	}
	_, err := a.memoryExt.CaptureTimeline(items)
	return err
}

func (a *Agent) storeCompactedMessages(messages []protocol.Message) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.messages = protocol.CloneMessages(messages)
	a.transcriptRefs = mergeTranscriptRefs(a.transcriptRefs, extractTranscriptRefs(messages))
	a.historyVersion++
	a.lastCompactedVersion = a.historyVersion
}

func (a *Agent) maybeAutoCompact(ctx context.Context, history []protocol.Message, version int64, system string, estimate contextBudgetEstimate) ([]protocol.Message, bool, compactionRunResult, error) {
	if !a.shouldAutoCompact(estimate) {
		return history, false, compactionRunResult{}, nil
	}

	a.mu.Lock()
	if a.lastCompactedVersion == version {
		current := protocol.CloneMessages(a.messages)
		a.mu.Unlock()
		return current, false, compactionRunResult{}, nil
	}
	a.mu.Unlock()

	if candidate := a.takeCompactionCandidate(version); candidate != nil {
		compacted := candidate.Result.Messages
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.historyVersion != version {
			return protocol.CloneMessages(a.messages), false, compactionRunResult{}, nil
		}
		a.messages = protocol.CloneMessages(compacted)
		a.transcriptRefs = mergeTranscriptRefs(a.transcriptRefs, extractTranscriptRefs(compacted))
		a.historyVersion++
		a.lastCompactedVersion = a.historyVersion
		return protocol.CloneMessages(a.messages), true, candidate.Result, nil
	}

	result, err := a.runCompaction(ctx, "fast", compress.SessionSummaryRequest{
		System:               system,
		History:              protocol.CloneMessages(history),
		TokenBreakdown:       tokenBreakdownMap(estimate.Breakdown),
		RecentUserMessages:   recentPersistentUserMessages(history, 6),
		ContinuationSnapshot: a.continuationSnapshot(tools.SessionContextFromContext(ctx).SessionID, history),
	})
	if err != nil {
		return nil, false, compactionRunResult{}, err
	}
	compacted := result.Messages

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.historyVersion != version {
		return protocol.CloneMessages(a.messages), false, compactionRunResult{}, nil
	}
	a.messages = protocol.CloneMessages(compacted)
	a.transcriptRefs = mergeTranscriptRefs(a.transcriptRefs, extractTranscriptRefs(compacted))
	a.historyVersion++
	a.lastCompactedVersion = a.historyVersion
	return protocol.CloneMessages(a.messages), true, result, nil
}
