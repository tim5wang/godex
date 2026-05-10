package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/domain/message"
)

type fakeHeartbeatManager struct {
	ruleResult automation.HeartbeatRule
	runResult  automation.HeartbeatRunLog
	logsResult []automation.HeartbeatRunLog

	setInput     automation.HeartbeatSetInput
	toggleCalled bool
	toggleState  bool
}

func (m *fakeHeartbeatManager) GetRule() (automation.HeartbeatRule, error) {
	rule := m.ruleResult
	if rule.ID == "" {
		rule.ID = "default"
	}
	return rule, nil
}

func (m *fakeHeartbeatManager) SetRule(input automation.HeartbeatSetInput) (automation.HeartbeatRule, error) {
	m.setInput = input
	return automation.HeartbeatRule{
		ID:              "default",
		Enabled:         input.Enabled != nil && *input.Enabled,
		IntervalSeconds: derefInt(input.IntervalSeconds),
		Timezone:        derefString(input.Timezone),
		DeliveryTarget:  cloneDelivery(input.DeliveryTarget),
	}, nil
}

func (m *fakeHeartbeatManager) Toggle(enabled bool) (automation.HeartbeatRule, error) {
	m.toggleCalled = true
	m.toggleState = enabled
	return automation.HeartbeatRule{ID: "default", Enabled: enabled}, nil
}

func (m *fakeHeartbeatManager) TestNow(ctx context.Context) (automation.HeartbeatRunLog, error) {
	_ = ctx
	run := m.runResult
	if run.ID == "" {
		run.ID = "run-1"
	}
	return run, nil
}

func (m *fakeHeartbeatManager) ListRunLogs(limit int) ([]automation.HeartbeatRunLog, error) {
	_ = limit
	return append([]automation.HeartbeatRunLog{}, m.logsResult...), nil
}

func TestHeartbeatToolSetInheritsDefaultDelivery(t *testing.T) {
	manager := &fakeHeartbeatManager{}
	tool := NewHeartbeatTool(manager)
	ctx := WithSessionContext(context.Background(), automation.SessionContext{
		SessionID: "web-session",
		Source:    "web",
		Sender:    "taiwu",
		DefaultDelivery: automation.DeliveryTarget{
			Kind:      automation.DeliveryKindSession,
			SessionID: "web-session",
		},
	})

	result, err := tool.Execute(ctx, map[string]interface{}{
		"action":           "set",
		"enabled":          true,
		"interval_seconds": float64(1800),
	})
	if err != nil {
		t.Fatalf("execute set: %v", err)
	}
	if manager.setInput.CreatedBy != "taiwu" || manager.setInput.CreatedFromSession != "web-session" {
		t.Fatalf("unexpected heartbeat creator fields: %+v", manager.setInput)
	}
	if manager.setInput.DeliveryTarget == nil || manager.setInput.DeliveryTarget.SessionID != "web-session" {
		t.Fatalf("expected default delivery target inheritance, got %+v", manager.setInput.DeliveryTarget)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if parsed["action"] != "set" {
		t.Fatalf("unexpected set result: %+v", parsed)
	}
}

func TestHeartbeatToolToggleAndLogs(t *testing.T) {
	manager := &fakeHeartbeatManager{
		logsResult: []automation.HeartbeatRunLog{{ID: "run-1", Status: "suppressed"}},
		runResult:  automation.HeartbeatRunLog{ID: "run-test", Status: "completed"},
	}
	tool := NewHeartbeatTool(manager)

	if _, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":  "toggle",
		"enabled": false,
	}); err != nil {
		t.Fatalf("execute toggle: %v", err)
	}
	if !manager.toggleCalled || manager.toggleState {
		t.Fatalf("unexpected toggle state: called=%v state=%v", manager.toggleCalled, manager.toggleState)
	}

	if _, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "logs",
		"limit":  float64(5),
	}); err != nil {
		t.Fatalf("execute logs: %v", err)
	}

	if _, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "test",
	}); err != nil {
		t.Fatalf("execute test: %v", err)
	}
}

func TestHeartbeatToolSetDoesNotOverwriteExistingDeliveryTargetImplicitly(t *testing.T) {
	manager := &fakeHeartbeatManager{
		ruleResult: automation.HeartbeatRule{
			ID: "default",
			DeliveryTarget: automation.DeliveryTarget{
				Kind: automation.DeliveryKindChannel, Channel: "feishu", Recipient: "chat-1",
			},
		},
	}
	tool := NewHeartbeatTool(manager)
	ctx := WithSessionContext(context.Background(), automation.SessionContext{
		SessionID: "web-session",
		Source:    "web",
		Sender:    "taiwu",
		DefaultDelivery: automation.DeliveryTarget{
			Kind:      automation.DeliveryKindSession,
			SessionID: "web-session",
		},
	})

	if _, err := tool.Execute(ctx, map[string]interface{}{
		"action":           "set",
		"interval_seconds": float64(900),
	}); err != nil {
		t.Fatalf("execute set: %v", err)
	}
	if manager.setInput.DeliveryTarget != nil {
		t.Fatalf("expected existing delivery target to remain unchanged unless explicitly provided, got %+v", manager.setInput.DeliveryTarget)
	}
}

func TestHeartbeatToolSetRejectedDuringAutomationRun(t *testing.T) {
	manager := &fakeHeartbeatManager{}
	handler := NewToolHandler()
	handler.AddBeforeInterceptors(NewPermissionInterceptor(NewPermissionManager(NewAutomationMutationRule())))
	handler.Register(NewHeartbeatTool(manager))
	ctx := WithSessionContext(context.Background(), automation.SessionContext{
		SessionID: "heartbeat-session",
		Source:    string(message.SourceHeartbeat),
		Sender:    "heartbeat",
	})

	_, err := handler.Handle(ctx, "heartbeat", map[string]interface{}{
		"action":           "set",
		"interval_seconds": float64(900),
	})
	if err == nil || !strings.Contains(err.Error(), "disabled during active automation runs") {
		t.Fatalf("expected automation mutation error, got %v", err)
	}
	if manager.setInput.IntervalSeconds != nil {
		t.Fatalf("expected manager not to receive set input, got %+v", manager.setInput)
	}
}

func derefInt(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func cloneDelivery(target *automation.DeliveryTarget) automation.DeliveryTarget {
	if target == nil {
		return automation.DeliveryTarget{}
	}
	return target.Clone()
}
