package evalharness

import (
	"encoding/json"
	"fmt"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	evaldomain "github.com/tim5wang/godex/internal/domain/eval"
	"github.com/tim5wang/godex/internal/domain/events"
)

func extractTurnMetrics(timeline []events.Event, turnID string) *protocol.TurnMetrics {
	for i := len(timeline) - 1; i >= 0; i-- {
		event := timeline[i]
		if event.Type != events.EventTurnMetrics || (turnID != "" && event.TurnID != turnID) {
			continue
		}
		data, err := json.Marshal(event.Payload)
		if err != nil {
			continue
		}
		var metrics protocol.TurnMetrics
		if json.Unmarshal(data, &metrics) == nil {
			return &metrics
		}
	}
	return nil
}

func evaluatePerformanceExpectations(expected evaldomain.Expectation, metrics *protocol.TurnMetrics) []string {
	if expected.MaxModelCalls == nil && expected.MaxDurationMS == nil && expected.MaxOutputTokens == nil {
		return nil
	}
	if metrics == nil {
		return []string{"performance metrics unavailable"}
	}
	var failures []string
	if expected.MaxModelCalls != nil && metrics.ModelCalls > *expected.MaxModelCalls {
		failures = append(failures, fmt.Sprintf("model calls %d exceed max %d", metrics.ModelCalls, *expected.MaxModelCalls))
	}
	if expected.MaxDurationMS != nil && metrics.DurationMS > *expected.MaxDurationMS {
		failures = append(failures, fmt.Sprintf("duration %dms exceeds max %dms", metrics.DurationMS, *expected.MaxDurationMS))
	}
	if expected.MaxOutputTokens != nil && metrics.OutputTokens > *expected.MaxOutputTokens {
		failures = append(failures, fmt.Sprintf("output tokens %d exceed max %d", metrics.OutputTokens, *expected.MaxOutputTokens))
	}
	return failures
}
