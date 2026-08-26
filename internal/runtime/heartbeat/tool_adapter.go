package heartbeat

import (
	"context"

	"github.com/tim5wang/godex/internal/domain/automation"
)

// ToolAdapter exposes heartbeat service operations through tool-friendly neutral models.
type ToolAdapter struct {
	service *Service
}

func NewToolAdapter(service *Service) *ToolAdapter {
	return &ToolAdapter{service: service}
}

func (a *ToolAdapter) GetRule() (automation.HeartbeatRule, error) {
	rule, err := a.service.GetRule()
	if err != nil {
		return automation.HeartbeatRule{}, err
	}
	return toAutomationRule(rule), nil
}

func (a *ToolAdapter) SetRule(input automation.HeartbeatSetInput) (automation.HeartbeatRule, error) {
	setInput := SetRuleInput{
		CreatedBy:          input.CreatedBy,
		CreatedFromSession: input.CreatedFromSession,
	}
	if input.Enabled != nil {
		setInput.Enabled = input.Enabled
	}
	if input.IntervalSeconds != nil {
		setInput.IntervalSeconds = input.IntervalSeconds
	}
	if input.Timezone != nil {
		setInput.Timezone = input.Timezone
	}
	if input.ActiveHoursStart != nil {
		setInput.ActiveHoursStart = input.ActiveHoursStart
	}
	if input.ActiveHoursEnd != nil {
		setInput.ActiveHoursEnd = input.ActiveHoursEnd
	}
	if input.SessionMode != nil {
		mode := SessionMode(*input.SessionMode)
		setInput.SessionMode = &mode
	}
	if input.DeliveryTarget != nil {
		target := input.DeliveryTarget.Clone()
		setInput.DeliveryTarget = &target
	}
	if input.PromptOverride != nil {
		setInput.PromptOverride = input.PromptOverride
	}
	if input.WatchdogScript != nil {
		setInput.WatchdogScript = input.WatchdogScript
	}
	rule, err := a.service.SetRule(setInput)
	if err != nil {
		return automation.HeartbeatRule{}, err
	}
	return toAutomationRule(rule), nil
}

func (a *ToolAdapter) Toggle(enabled bool) (automation.HeartbeatRule, error) {
	rule, err := a.service.Toggle(enabled)
	if err != nil {
		return automation.HeartbeatRule{}, err
	}
	return toAutomationRule(rule), nil
}

func (a *ToolAdapter) TestNow(ctx context.Context) (automation.HeartbeatRunLog, error) {
	run, err := a.service.TestNow(ctx)
	if err != nil {
		return automation.HeartbeatRunLog{}, err
	}
	return toAutomationRunLog(run), nil
}

func (a *ToolAdapter) ListRunLogs(limit int) ([]automation.HeartbeatRunLog, error) {
	runs, err := a.service.ListRunLogs(limit)
	if err != nil {
		return nil, err
	}
	out := make([]automation.HeartbeatRunLog, 0, len(runs))
	for _, run := range runs {
		out = append(out, toAutomationRunLog(run))
	}
	return out, nil
}

func toAutomationRule(rule Rule) automation.HeartbeatRule {
	return automation.HeartbeatRule{
		ID:                 rule.ID,
		Enabled:            rule.Enabled,
		IntervalSeconds:    rule.IntervalSeconds,
		Timezone:           rule.Timezone,
		ActiveHoursStart:   rule.ActiveHoursStart,
		ActiveHoursEnd:     rule.ActiveHoursEnd,
		SessionMode:        string(rule.SessionMode),
		DeliveryTarget:     rule.DeliveryTarget.Clone(),
		PromptOverride:     rule.PromptOverride,
		WatchdogScript:     rule.WatchdogScript,
		CreatedBy:          rule.CreatedBy,
		CreatedFromSession: rule.CreatedFromSession,
		CreatedAt:          rule.CreatedAt,
		UpdatedAt:          rule.UpdatedAt,
		LastRunAt:          rule.LastRunAt,
		NextRunAt:          rule.NextRunAt,
		LastStatus:         string(rule.LastStatus),
		LastError:          rule.LastError,
	}
}

func toAutomationRunLog(run RunLog) automation.HeartbeatRunLog {
	return automation.HeartbeatRunLog{
		ID:             run.ID,
		RuleID:         run.RuleID,
		SessionID:      run.SessionID,
		TurnID:         run.TurnID,
		Status:         string(run.Status),
		Error:          run.Error,
		Suppressed:     run.Suppressed,
		DeliveryTarget: run.DeliveryTarget.Clone(),
		StartedAt:      run.StartedAt,
		FinishedAt:     run.FinishedAt,
	}
}
