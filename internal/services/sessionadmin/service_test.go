package sessionadmin

import (
	"context"
	"testing"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/tools"
)

type fakeCurrentSession struct {
	messages []protocol.Message
	cleared  bool
}

func (f *fakeCurrentSession) GetMessages() []protocol.Message {
	return append([]protocol.Message{}, f.messages...)
}

func (f *fakeCurrentSession) ActiveSkillNames() []string {
	return []string{"code-guide"}
}

func (f *fakeCurrentSession) PendingPermissions(sessionID string) []tools.PendingPermission {
	_ = sessionID
	return []tools.PendingPermission{{ID: "perm-1"}}
}

func (f *fakeCurrentSession) ApprovePendingPermission(sessionID, requestID string, scope tools.PermissionGrantScope) (tools.PermissionResolution, error) {
	_ = sessionID
	return tools.PermissionResolution{RequestID: requestID, Scope: scope}, nil
}

func (f *fakeCurrentSession) DenyPendingPermission(sessionID, requestID, reason string) (tools.PermissionResolution, error) {
	_ = sessionID
	return tools.PermissionResolution{RequestID: requestID, Reason: reason}, nil
}

func (f *fakeCurrentSession) ClearMessages() {
	f.cleared = true
	f.messages = nil
}

func (f *fakeCurrentSession) InspectContext(ctx context.Context, sessionID string) (tools.ContextInspection, error) {
	_ = ctx
	return tools.ContextInspection{
		SessionID:              sessionID,
		MessageCount:           len(f.messages),
		TokenEstimate:          512,
		CompressThreshold:      12000,
		SuggestCompact:         false,
		ActiveSkillCount:       1,
		PendingPermissionCount: 1,
	}, nil
}

func TestBoundRuntimeClearMessagesUsesCurrentSession(t *testing.T) {
	current := &fakeCurrentSession{
		messages: []protocol.Message{
			protocol.NewTextMessage(protocol.RoleUser, "hello"),
			protocol.NewTextMessage(protocol.RoleAssistant, "world"),
		},
	}
	service := NewService(func() *config.Config { return &config.Config{} }, nil, nil, nil)
	bound := service.Bind(current)

	result, err := bound.ClearMessages(context.Background(), "session-1", automation.SessionContext{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("clear messages: %v", err)
	}
	if !current.cleared || result.ClearedMessages != 2 || result.Status != "cleared" {
		t.Fatalf("unexpected clear result: %+v", result)
	}
}

func TestBoundRuntimeTokenSummaryReturnsUnsupportedForFeishu(t *testing.T) {
	service := NewService(func() *config.Config { return &config.Config{} }, nil, nil, nil)
	bound := service.Bind(nil)

	view, err := bound.TokenSummary(context.Background(), "session-1", automation.SessionContext{
		SessionID:      "session-1",
		LocatorChannel: "feishu",
	}, false)
	if err != nil {
		t.Fatalf("token summary: %v", err)
	}
	if view.Supported || view.Message == "" {
		t.Fatalf("unexpected feishu token view: %+v", view)
	}
}
