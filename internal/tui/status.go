package tui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/domain/events"
)

func (m *model) renderRuntimeStatus() string {
	parts := []string{m.baseRuntimeStatus()}
	if focus := m.focusStatusText(); focus != "" {
		parts = append(parts, focus)
	}
	if blocker := m.permissionBlockerStatusText(); blocker != "" {
		parts = append(parts, blocker)
	}
	if ctx := m.contextUsageText(); ctx != "" {
		parts = append(parts, ctx)
	}
	if m.modelCallCount > 0 {
		parts = append(parts, fmt.Sprintf("calls %d", m.modelCallCount))
	}
	if m.contextSummary.MessageCount > 0 {
		parts = append(parts, fmt.Sprintf("msgs %d", m.contextSummary.MessageCount))
	}
	return strings.Join(parts, " · ")
}

func (m *model) permissionBlockerStatusText() string {
	blocker := m.snapshot.ActivePermissionBlocker
	if blocker == nil {
		return ""
	}
	parts := []string{"Blocked by approval"}
	if requestID := strings.TrimSpace(blocker.RequestID); requestID != "" {
		parts = append(parts, requestID)
	}
	action := strings.Join(strings.Fields(strings.TrimSpace(blocker.ToolName)+" "+strings.TrimSpace(blocker.Action)), " ")
	if action != "" {
		parts = append(parts, action)
	}
	if expiry := strings.TrimSpace(blocker.Expiry); expiry != "" {
		parts = append(parts, expiry)
	}
	return strings.Join(parts, " ")
}

func (m *model) focusStatusText() string {
	if m.focus == focusFeed {
		return "Focus: Workbench · 1-5 tabs · Tab input"
	}
	return "Focus: Input · Tab workbench"
}

func (m *model) baseRuntimeStatus() string {
	if !m.working {
		return "Ready"
	}
	frame := heartbeatFrames[m.heartbeatFrame%len(heartbeatFrames)]
	elapsed := formatElapsed(m.now().Sub(m.workingSince))
	status := fmt.Sprintf("%s Working for %s", frame, elapsed)
	if phase := strings.TrimSpace(m.activePhase); phase != "" {
		status += " · " + phase
	}
	if tool := strings.TrimSpace(m.activeToolName); tool != "" {
		status += " · " + tool
	}
	return status
}

func (m *model) contextUsageText() string {
	total := m.contextSummary.TotalTokenEstimate
	if total <= 0 {
		total = m.contextSummary.TokenEstimate
	}
	if total <= 0 {
		total = m.contextSummary.TokenBreakdown.Total
	}
	if total <= 0 {
		return ""
	}
	threshold := m.contextSummary.CompressThreshold
	if threshold <= 0 && m.cfg != nil {
		threshold = m.cfg.CompressThreshold
	}
	if threshold <= 0 {
		return "ctx " + formatCompactNumber(total)
	}
	pct := int(math.Round(float64(total) / float64(threshold) * 100))
	text := fmt.Sprintf("ctx %s/%s %d%%", formatCompactNumber(total), formatCompactNumber(threshold), pct)
	if len(m.contextSummary.LargestContextSources) > 0 {
		source := m.contextSummary.LargestContextSources[0]
		text += fmt.Sprintf(" · top %s %s", source.Source, formatCompactNumber(source.Tokens))
	}
	if mode := strings.TrimSpace(m.contextSummary.CompactionMode); mode != "" {
		text += " · compact " + mode
	}
	return text
}

func (m *model) rebuildModelCallStats() {
	m.seenModelCallEvent = make(map[string]struct{})
	m.modelCallCount = 0
	for _, event := range m.snapshot.Timeline {
		m.recordModelCallEvent(event)
	}
}

func (m *model) recordModelCallEvent(event events.Event) {
	if !isModelRequestEvent(event) {
		return
	}
	if m.seenModelCallEvent == nil {
		m.seenModelCallEvent = make(map[string]struct{})
	}
	key := modelCallEventKey(event)
	if _, ok := m.seenModelCallEvent[key]; ok {
		return
	}
	m.seenModelCallEvent[key] = struct{}{}
	m.modelCallCount++
}

func isModelRequestEvent(event events.Event) bool {
	if event.Type != events.EventRunnerPhaseChanged {
		return false
	}
	switch payload := event.Payload.(type) {
	case events.RunnerPhasePayload:
		return payload.Phase == "model_request"
	case map[string]interface{}:
		phase, _ := payload["phase"].(string)
		return phase == "model_request"
	default:
		return false
	}
}

func modelCallEventKey(event events.Event) string {
	return fmt.Sprintf("%s|%s|%s|%s|%d", event.Type, event.TurnID, event.Timestamp.UTC().Format(time.RFC3339Nano), modelCallEventPhase(event), modelCallEventIteration(event))
}

func modelCallEventPhase(event events.Event) string {
	switch payload := event.Payload.(type) {
	case events.RunnerPhasePayload:
		return payload.Phase
	case map[string]interface{}:
		phase, _ := payload["phase"].(string)
		return phase
	default:
		return ""
	}
}

func modelCallEventIteration(event events.Event) int {
	switch payload := event.Payload.(type) {
	case events.RunnerPhasePayload:
		return payload.Iteration
	case map[string]interface{}:
		switch value := payload["iteration"].(type) {
		case int:
			return value
		case float64:
			return int(value)
		default:
			return 0
		}
	default:
		return 0
	}
}

func formatCompactNumber(value int) string {
	switch {
	case value >= 1000000:
		return fmt.Sprintf("%.1fm", float64(value)/1000000)
	case value >= 1000:
		return fmt.Sprintf("%.1fk", float64(value)/1000)
	default:
		return fmt.Sprintf("%d", value)
	}
}
