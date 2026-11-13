package backend

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/memory"
	"github.com/tim5wang/godex/internal/core/workcontext"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/platform/retrieval"
	"github.com/tim5wang/godex/internal/platform/textutil"
	"github.com/tim5wang/godex/internal/tools"
)

// HistoryRecallDecisionSummary is the latest history-search gating decision that
// affected the current session.
type HistoryRecallDecisionSummary struct {
	AllowTool        bool      `json:"allow_tool"`
	Automatic        bool      `json:"automatic"`
	ExplicitRequest  bool      `json:"explicit_request"`
	RecommendedScope string    `json:"recommended_scope,omitempty"`
	Score            int       `json:"score"`
	Reasons          []string  `json:"reasons,omitempty"`
	Timestamp        time.Time `json:"timestamp,omitempty"`
}

// MemoryContextPreview mirrors the layered durable-memory context that would be
// injected for the latest recalled user query.
type MemoryContextPreview struct {
	Identity []memory.RelevantMemory `json:"identity"`
	Core     []memory.RelevantMemory `json:"core"`
	Relevant []memory.RelevantMemory `json:"relevant"`
}

// SessionContextInspector aggregates context-budget, history-archive, memory,
// and history-recall signals for Chat UI inspection.
type SessionContextInspector struct {
	Context            tools.ContextInspection       `json:"context"`
	TranscriptRefCount int                           `json:"transcript_ref_count"`
	TranscriptRefs     []string                      `json:"transcript_refs,omitempty"`
	RecallQuery        string                        `json:"recall_query,omitempty"`
	RecallMode         string                        `json:"recall_mode,omitempty"`
	RecallDiagnostics  []*retrieval.Diagnostics      `json:"recall_diagnostics,omitempty"`
	MemoryPreview      MemoryContextPreview          `json:"memory_preview"`
	HistoryRecall      *HistoryRecallDecisionSummary `json:"history_recall,omitempty"`
}

// ContextInspector returns a read-only aggregate of the current context budget,
// transcript archive state, latest recall query, memory preview, and most
// recent history-search policy decision for one session.
func (s *Service) ContextInspector(ctx context.Context, sessionID string) (SessionContextInspector, error) {
	session, err := s.requireSessionFor(ctx, sessionID)
	if err != nil {
		return SessionContextInspector{}, err
	}

	details, err := session.agent.InspectContextDetails(ctx, sessionID)
	if err != nil {
		return SessionContextInspector{}, err
	}

	transcriptRefs := uniqueTranscriptRefs(session.agent.TranscriptRefs())
	result := SessionContextInspector{
		Context:            details.Context,
		TranscriptRefCount: len(transcriptRefs),
		TranscriptRefs:     recentTranscriptRefs(transcriptRefs, 3),
		RecallQuery:        summarizeInspectorText(details.RecallQuery, 160),
		RecallMode:         details.RecallMode,
		RecallDiagnostics:  details.MemoryLayers.Diagnostics,
		MemoryPreview: MemoryContextPreview{
			Identity: append([]memory.RelevantMemory{}, details.MemoryLayers.Identity...),
			Core:     append([]memory.RelevantMemory{}, details.MemoryLayers.Core...),
			Relevant: append([]memory.RelevantMemory{}, details.MemoryLayers.Relevant...),
		},
	}
	if session.agent.AutomaticMemoryEnabled() {
		if diagnostic := s.desktopRecallDiagnostic(ctx, session, details.RecallQuery); diagnostic != nil {
			result.RecallDiagnostics = append(result.RecallDiagnostics, diagnostic)
		}
	}
	if session.timeline != nil {
		result.HistoryRecall = latestHistoryRecallDecision(session.timeline.Entries(snapshotTimelineLimit))
	}

	return result, nil
}

// This is an explicitly labeled live preview, not a replay of an earlier LLM
// request. It uses the same binding and source eligibility as automatic recall.
func (s *Service) desktopRecallDiagnostic(ctx context.Context, session *sessionState, query string) *retrieval.Diagnostics {
	unavailable := &retrieval.Diagnostics{Query: query, Layer: "desktop", Cache: "unavailable", Rejected: map[string]int{"binding_or_source_unavailable": 1}}
	store, err := s.WorkContexts()
	if err != nil {
		return unavailable
	}
	scope, id, err := store.SessionBinding(ctx, session.id)
	if errors.Is(err, workcontext.ErrNotFound) || err == nil && id == "" {
		return nil
	}
	if err != nil {
		return unavailable
	}
	ownerCtx := memory.WithOwner(ctx, memory.Owner{TenantID: scope.TenantID, UserID: scope.OwnerID})
	if !sessionMemoryOwnerMatches(ownerCtx, session) {
		return unavailable
	}
	facts, trace, err := store.RecallCurrentWithDiagnostics(ctx, scope, id, query)
	if err != nil {
		return unavailable
	}
	ids, tokens := []string{}, 0
	for i, f := range facts {
		if i >= 4 {
			break
		}
		ids = append(ids, f.ID)
		tokens += len([]rune(f.Title+textutil.TruncateRunes(f.Content, 220))) / 2
	}
	retrieval.FinalizeSelection(&trace, ids, tokens)
	return &trace
}

// ContextUsage returns live provider usage counters without constructing the
// larger context/memory Inspector snapshot.
func (s *Service) ContextUsage(ctx context.Context, sessionID string) (tools.ContextUsageInspection, error) {
	session, err := s.requireSessionFor(ctx, sessionID)
	if err != nil {
		return tools.ContextUsageInspection{}, err
	}
	return session.agent.InspectContextUsage(sessionID), nil
}

func uniqueTranscriptRefs(refs []string) []string {
	if len(refs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(refs))
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		out = append(out, ref)
	}
	return out
}

func recentTranscriptRefs(refs []string, limit int) []string {
	if len(refs) == 0 {
		return nil
	}
	if limit <= 0 || len(refs) <= limit {
		return append([]string{}, refs...)
	}
	return append([]string{}, refs[len(refs)-limit:]...)
}

func summarizeInspectorText(value string, limit int) string {
	value = strings.TrimSpace(strings.Join(strings.Fields(value), " "))
	if value == "" || limit <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return strings.TrimSpace(string(runes[:limit-1])) + "…"
}

func latestHistoryRecallDecision(items []events.Event) *HistoryRecallDecisionSummary {
	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		if item.Type != events.EventHistoryRecallDecision {
			continue
		}
		payload, ok := decodeHistoryRecallPayload(item.Payload)
		if !ok {
			continue
		}
		return &HistoryRecallDecisionSummary{
			AllowTool:        payload.AllowTool,
			Automatic:        payload.Automatic,
			ExplicitRequest:  payload.ExplicitRequest,
			RecommendedScope: strings.TrimSpace(payload.RecommendedScope),
			Score:            payload.Score,
			Reasons:          append([]string{}, payload.Reasons...),
			Timestamp:        item.Timestamp,
		}
	}
	return nil
}

func decodeHistoryRecallPayload(value any) (events.HistoryRecallPayload, bool) {
	switch payload := value.(type) {
	case events.HistoryRecallPayload:
		return payload, true
	case *events.HistoryRecallPayload:
		if payload == nil {
			return events.HistoryRecallPayload{}, false
		}
		return *payload, true
	case map[string]any:
		return decodeHistoryRecallPayloadFromMap(payload)
	default:
		data, err := json.Marshal(value)
		if err != nil {
			return events.HistoryRecallPayload{}, false
		}
		var decoded events.HistoryRecallPayload
		if err := json.Unmarshal(data, &decoded); err != nil {
			return events.HistoryRecallPayload{}, false
		}
		return decoded, true
	}
}

func decodeHistoryRecallPayloadFromMap(value map[string]any) (events.HistoryRecallPayload, bool) {
	var payload events.HistoryRecallPayload
	if raw, ok := value["allow_tool"].(bool); ok {
		payload.AllowTool = raw
	}
	if raw, ok := value["automatic"].(bool); ok {
		payload.Automatic = raw
	}
	if raw, ok := value["explicit_request"].(bool); ok {
		payload.ExplicitRequest = raw
	}
	if raw, ok := value["recommended_scope"].(string); ok {
		payload.RecommendedScope = raw
	}
	switch raw := value["score"].(type) {
	case float64:
		payload.Score = int(raw)
	case int:
		payload.Score = raw
	}
	if items, ok := value["reasons"].([]any); ok {
		payload.Reasons = make([]string, 0, len(items))
		for _, item := range items {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				payload.Reasons = append(payload.Reasons, text)
			}
		}
	}
	return payload, true
}
