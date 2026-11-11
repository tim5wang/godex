package evalharness

import (
	"testing"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	evaldomain "github.com/tim5wang/godex/internal/domain/eval"
	"github.com/tim5wang/godex/internal/domain/events"
)

func TestPerformanceGatesRequireMetricsAndEnforceTaskBudget(t *testing.T) {
	calls, tokens, ms := 4, 100, int64(1000)
	expected := evaldomain.Expectation{MaxModelCalls: &calls, MaxDurationMS: &ms, MaxOutputTokens: &tokens}
	if len(evaluatePerformanceExpectations(expected, nil)) == 0 {
		t.Fatal("missing data must not pass a performance gate")
	}
	metrics := protocol.TurnMetrics{ModelCalls: 5, DurationMS: 1200, OutputTokens: 101}
	if got := evaluatePerformanceExpectations(expected, &metrics); len(got) != 3 {
		t.Fatalf("expected all exceeded budgets to fail: %v", got)
	}
	metrics = protocol.TurnMetrics{ModelCalls: 4, DurationMS: 1000, OutputTokens: 100}
	if got := evaluatePerformanceExpectations(expected, &metrics); len(got) != 0 {
		t.Fatalf("inclusive limits should pass: %v", got)
	}
	got := extractTurnMetrics([]events.Event{
		{Type: events.EventTurnMetrics, TurnID: "wanted", Payload: metrics},
		{Type: events.EventTurnMetrics, TurnID: "other", Payload: protocol.TurnMetrics{ModelCalls: 99}},
	}, "wanted")
	if got == nil || got.ModelCalls != 4 {
		t.Fatalf("metrics from a different turn were included: %+v", got)
	}
}
