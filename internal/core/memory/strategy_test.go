package memory

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/tim5wang/godex/internal/core/protocol"
)

func TestParseStrategyKindNormalizesInputs(t *testing.T) {
	cases := []struct {
		in   string
		want StrategyKind
	}{
		{"", StrategyPerTurn},
		{"per-turn", StrategyPerTurn},
		{"PER_TURN", StrategyPerTurn},
		{"agent-only", StrategyAgentOnly},
		{"AgentOnly", StrategyAgentOnly},
		{"consolidated", StrategyConsolidated},
		{"CONSOLIDATION", StrategyConsolidated},
		{"bogus", StrategyPerTurn},
		{"  ", StrategyPerTurn},
	}
	for _, tc := range cases {
		if got := ParseStrategyKind(tc.in); got != tc.want {
			t.Errorf("ParseStrategyKind(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNewStrategyDefaultsToPerTurn(t *testing.T) {
	manager := NewManager(filepath.Join(t.TempDir(), "memory"))
	extractor := NewExtractor(manager, t.TempDir())
	strategy := NewStrategy(StrategyOptions{Kind: "", Extract: extractor})
	if strategy.Kind() != StrategyPerTurn {
		t.Fatalf("expected per-turn strategy, got %q", strategy.Kind())
	}
	if err := strategy.Maintain(context.Background()); err != nil {
		t.Fatalf("per-turn maintain should be a no-op: %v", err)
	}
}

func TestNewStrategyMissingExtractorDegradesToAgentOnly(t *testing.T) {
	strategy := NewStrategy(StrategyOptions{Kind: StrategyPerTurn, Extract: nil})
	if strategy.Kind() != StrategyAgentOnly {
		t.Fatalf("expected agent-only fallback, got %q", strategy.Kind())
	}
	added, err := strategy.Capture([]protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "请记住：用 Go 写运行时。"),
	})
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if len(added) != 0 {
		t.Fatalf("agent-only should not capture, got %d candidates", len(added))
	}
}

func TestAgentOnlyStrategySkipsCapture(t *testing.T) {
	strategy := NewStrategy(StrategyOptions{Kind: StrategyAgentOnly, Extract: nil})
	if strategy.Kind() != StrategyAgentOnly {
		t.Fatalf("expected agent-only strategy, got %q", strategy.Kind())
	}
	added, err := strategy.Capture([]protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "以后请用中文回复。"),
	})
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if len(added) != 0 {
		t.Fatalf("agent-only capture should return nothing, got %+v", added)
	}
}

func TestConsolidatedStrategyCapturesAndMaintains(t *testing.T) {
	manager := NewManager(filepath.Join(t.TempDir(), "memory"))
	extractor := NewExtractor(manager, t.TempDir())
	maintainCalls := 0
	consolidator := NewConsolidator(ConsolidatorOptions{
		Manager: manager,
		OneShot: func(ctx context.Context, prompt, input string) (string, error) {
			maintainCalls++
			return "NONE", nil
		},
		AfterN: 1, // any capture triggers a pass
	})
	strategy := NewStrategy(StrategyOptions{
		Kind:         StrategyConsolidated,
		Extract:      extractor,
		Consolidator: consolidator,
	})
	if strategy.Kind() != StrategyConsolidated {
		t.Fatalf("expected consolidated strategy, got %q", strategy.Kind())
	}
	added, err := strategy.Capture([]protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "以后请用中文回复。"),
	})
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if len(added) != 1 {
		t.Fatalf("expected 1 candidate captured, got %+v", added)
	}
	if maintainCalls != 1 {
		t.Fatalf("expected consolidation pass after capture, got %d calls", maintainCalls)
	}
}
