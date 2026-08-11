package memory

import (
	"context"
	"strings"

	"github.com/tim5wang/godex/internal/core/protocol"
)

// StrategyKind selects a composable memory behavior. The default (per-turn)
// preserves today's behavior; consolidation layers LLM-driven candidate
// hygiene on top; agent-only disables automatic extraction entirely.
type StrategyKind string

const (
	// StrategyPerTurn is the default: capture durable-memory candidates from
	// finished turns exactly as before (Extractor.Capture).
	StrategyPerTurn StrategyKind = "per-turn"
	// StrategyAgentOnly disables automatic candidate extraction; memory is
	// written only through explicit remember/accept actions.
	StrategyAgentOnly StrategyKind = "agent-only"
	// StrategyConsolidated enables per-turn capture plus LLM-driven
	// consolidation (merge/dedup/delete) of pending candidates once the
	// inbox grows past the configured threshold.
	StrategyConsolidated StrategyKind = "consolidated"
)

// ParseStrategyKind normalizes an unknown/empty value to the default.
func ParseStrategyKind(value string) StrategyKind {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "agent-only", "agent_only", "agentonly":
		return StrategyAgentOnly
	case "consolidated", "consolidation":
		return StrategyConsolidated
	default:
		return StrategyPerTurn
	}
}

// Strategy abstracts how durable memory is captured and maintained. It is the
// Go-side counterpart of temp/qm/src/memory/strategy.ts (onTurnEnd/maintain).
type Strategy interface {
	// Kind reports the strategy identifier for diagnostics/config.
	Kind() StrategyKind
	// Capture extracts and stores new memory candidates from finished-turn
	// messages. It mirrors Extractor.Capture; strategies may wrap or skip it.
	Capture(messages []protocol.Message) ([]Candidate, error)
	// Maintain runs periodic background maintenance (consolidation). It is a
	// no-op for strategies without maintenance duties.
	Maintain(ctx context.Context) error
}

// perTurnStrategy is the default behavior: delegate directly to the extractor.
type perTurnStrategy struct {
	extractor *Extractor
}

func (s perTurnStrategy) Kind() StrategyKind            { return StrategyPerTurn }
func (s perTurnStrategy) Maintain(context.Context) error { return nil }
func (s perTurnStrategy) Capture(messages []protocol.Message) ([]Candidate, error) {
	return s.extractor.Capture(messages)
}

// agentOnlyStrategy disables automatic extraction entirely.
type agentOnlyStrategy struct{}

func (agentOnlyStrategy) Kind() StrategyKind            { return StrategyAgentOnly }
func (agentOnlyStrategy) Maintain(context.Context) error { return nil }
func (agentOnlyStrategy) Capture([]protocol.Message) ([]Candidate, error) {
	return nil, nil
}

// consolidatingStrategy wraps per-turn capture and triggers LLM consolidation
// of the candidate inbox after each capture (threshold-checked lazily).
type consolidatingStrategy struct {
	extractor   *Extractor
	consolidator *Consolidator
}

func (s consolidatingStrategy) Kind() StrategyKind { return StrategyConsolidated }
func (s consolidatingStrategy) Capture(messages []protocol.Message) ([]Candidate, error) {
	added, err := s.extractor.Capture(messages)
	if err != nil {
		return added, err
	}
	_ = s.consolidator.MaybeMaintain(context.Background())
	return added, nil
}
func (s consolidatingStrategy) Maintain(ctx context.Context) error {
	return s.consolidator.MaybeMaintain(ctx)
}

// StrategyOptions configures strategy construction.
type StrategyOptions struct {
	// Kind selects the strategy; empty defaults to per-turn.
	Kind StrategyKind
	// Extract is the underlying candidate extractor (required).
	Extract *Extractor
	// Consolidator is required for StrategyConsolidated.
	Consolidator *Consolidator
}

// NewStrategy builds a Strategy from options. Unknown kinds fall back to the
// default per-turn behavior so old configs keep working unchanged.
func NewStrategy(opts StrategyOptions) Strategy {
	extractor := opts.Extract
	switch opts.Kind {
	case StrategyAgentOnly:
		return agentOnlyStrategy{}
	case StrategyConsolidated:
		if extractor != nil && opts.Consolidator != nil {
			return consolidatingStrategy{extractor: extractor, consolidator: opts.Consolidator}
		}
		// Missing pieces degrade to per-turn rather than dropping capture.
	}
	if extractor == nil {
		return agentOnlyStrategy{}
	}
	return perTurnStrategy{extractor: extractor}
}
