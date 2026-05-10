package compress

import (
	"context"
	"strings"

	"github.com/tim5wang/godex/internal/core/protocol"
)

// SessionSummaryRequest carries the context needed by a session summarizer.
// The default implementation is rule-based, but the shape is intentionally
// suitable for a future model-backed summarizer.
type SessionSummaryRequest struct {
	System               string
	History              []protocol.Message
	TokenBreakdown       map[string]int
	TranscriptPath       string
	RecentUserMessages   []string
	ContinuationSnapshot string
}

// SessionSummaryResult is the replacement conversation and diagnostics emitted
// by a session summarizer.
type SessionSummaryResult struct {
	Messages       []protocol.Message
	TranscriptRefs []string
	Diagnostics    []string
	RecoveryHint   string
}

// SessionSummarizer produces compacted session history.
type SessionSummarizer interface {
	SummarizeSession(context.Context, SessionSummaryRequest) (SessionSummaryResult, error)
}

// RuleBasedSessionSummarizer adapts the existing semantic compressor to the
// summarizer interface.
type RuleBasedSessionSummarizer struct {
	compressor *Compressor
}

func NewRuleBasedSessionSummarizer(compressor *Compressor) *RuleBasedSessionSummarizer {
	return &RuleBasedSessionSummarizer{compressor: compressor}
}

func (s *RuleBasedSessionSummarizer) SummarizeSession(_ context.Context, req SessionSummaryRequest) (SessionSummaryResult, error) {
	if s == nil || s.compressor == nil {
		return SessionSummaryResult{Messages: protocol.CloneMessages(req.History)}, nil
	}
	messages, err := s.compressor.CompactWithSnapshot(req.History, req.System, req.ContinuationSnapshot)
	if err != nil {
		return SessionSummaryResult{}, err
	}
	return SessionSummaryResult{
		Messages:       messages,
		TranscriptRefs: transcriptRefs(messages),
	}, nil
}

func transcriptRefs(messages []protocol.Message) []string {
	seen := make(map[string]struct{}, len(messages))
	refs := make([]string, 0, len(messages))
	for _, msg := range messages {
		if msg.Metadata == nil {
			continue
		}
		ref := strings.TrimSpace(msg.Metadata.Transcript)
		if ref == "" {
			continue
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	return refs
}
