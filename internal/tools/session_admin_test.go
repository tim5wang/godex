package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/domain/automation"
)

type fakeSessionAdminRuntime struct {
	context   ContextInspection
	tokens    SessionTokenView
	approved  PermissionResolution
	lastID    string
	lastScope PermissionGrantScope
}

func (f *fakeSessionAdminRuntime) CurrentSession(ctx context.Context, sessionID string, runtimeCtx automation.SessionContext) (SessionSummary, error) {
	_ = ctx
	_ = runtimeCtx
	return SessionSummary{SessionID: sessionID, Channel: "web", Key: "default"}, nil
}

func (f *fakeSessionAdminRuntime) ContextSummary(ctx context.Context, sessionID string, runtimeCtx automation.SessionContext) (ContextInspection, error) {
	_ = ctx
	_ = runtimeCtx
	summary := f.context
	summary.SessionID = sessionID
	return summary, nil
}

func (f *fakeSessionAdminRuntime) ClearMessages(ctx context.Context, sessionID string, runtimeCtx automation.SessionContext) (SessionActionResult, error) {
	_ = ctx
	_ = runtimeCtx
	return SessionActionResult{SessionID: sessionID, Action: "clear_messages", Status: "cleared"}, nil
}

func (f *fakeSessionAdminRuntime) ListPendingPermissions(ctx context.Context, sessionID string, runtimeCtx automation.SessionContext) ([]PendingPermission, error) {
	_ = ctx
	_ = sessionID
	_ = runtimeCtx
	return []PendingPermission{{ID: "perm-1"}}, nil
}

func (f *fakeSessionAdminRuntime) ApprovePermission(ctx context.Context, sessionID string, runtimeCtx automation.SessionContext, requestID string, scope PermissionGrantScope) (PermissionResolution, error) {
	_ = ctx
	_ = sessionID
	_ = runtimeCtx
	f.lastID = requestID
	f.lastScope = scope
	if f.approved.RequestID != "" {
		return f.approved, nil
	}
	return PermissionResolution{RequestID: requestID, Scope: scope}, nil
}

func (f *fakeSessionAdminRuntime) DenyPermission(ctx context.Context, sessionID string, runtimeCtx automation.SessionContext, requestID, reason string) (PermissionResolution, error) {
	_ = ctx
	_ = sessionID
	_ = runtimeCtx
	return PermissionResolution{RequestID: requestID, Reason: reason}, nil
}

func (f *fakeSessionAdminRuntime) TokenSummary(ctx context.Context, sessionID string, runtimeCtx automation.SessionContext, reveal bool) (SessionTokenView, error) {
	_ = ctx
	_ = sessionID
	_ = runtimeCtx
	view := f.tokens
	view.Reveal = reveal
	return view, nil
}

func (f *fakeSessionAdminRuntime) ChannelAuth(ctx context.Context, sessionID string, runtimeCtx automation.SessionContext, action string) (SessionChannelAuth, error) {
	_ = ctx
	_ = sessionID
	_ = runtimeCtx
	return SessionChannelAuth{Channel: "weixin", Action: action, Supported: true}, nil
}

func TestManageSessionToolReturnsStructuredContextSummary(t *testing.T) {
	runtime := &fakeSessionAdminRuntime{
		context: ContextInspection{
			MessageCount:           6,
			TokenEstimate:          2048,
			HistoryTokenEstimate:   1024,
			TotalTokenEstimate:     2048,
			TokenBreakdown:         ContextTokenBreakdown{System: 256, History: 1024, ToolSchemas: 768, Total: 2048},
			CompressThreshold:      12000,
			SuggestCompact:         false,
			CompressionReasons:     []string{"total_over_threshold"},
			ActiveSkillCount:       2,
			PendingPermissionCount: 1,
		},
	}
	tool := NewManageSessionTool(runtime)
	ctx := WithSessionContext(context.Background(), automation.SessionContext{
		SessionID: "session-1",
		Source:    "web",
	})

	result, err := tool.Execute(ctx, map[string]interface{}{"action": "inspect_context"})
	if err != nil {
		t.Fatalf("inspect context: %v", err)
	}
	if !strings.Contains(result, `"token_estimate":2048`) || !strings.Contains(result, `"history_token_estimate":1024`) || !strings.Contains(result, `"session_id":"session-1"`) {
		t.Fatalf("unexpected inspect context result: %s", result)
	}
}

func TestManageSessionToolApproveRequiresRequestID(t *testing.T) {
	tool := NewManageSessionTool(&fakeSessionAdminRuntime{})
	ctx := WithSessionContext(context.Background(), automation.SessionContext{SessionID: "session-1"})

	_, err := tool.Execute(ctx, map[string]interface{}{"action": "approve_permission"})
	if err == nil || !strings.Contains(err.Error(), "missing request_id") {
		t.Fatalf("expected missing request_id error, got %v", err)
	}
}

func TestManageSessionToolApproveSupportsTaskScope(t *testing.T) {
	runtime := &fakeSessionAdminRuntime{}
	tool := NewManageSessionTool(runtime)
	ctx := WithSessionContext(context.Background(), automation.SessionContext{SessionID: "session-1"})

	if _, err := tool.Execute(ctx, map[string]interface{}{"action": "approve_permission", "request_id": "perm-1", "scope": "task"}); err != nil {
		t.Fatalf("approve task scope: %v", err)
	}
	if runtime.lastID != "perm-1" || runtime.lastScope != PermissionGrantTask {
		t.Fatalf("expected task scope approval, id=%q scope=%q", runtime.lastID, runtime.lastScope)
	}
}

func TestManageSessionToolMutationDeniedDuringHeartbeatRuns(t *testing.T) {
	handler := NewToolHandler()
	handler.AddBeforeInterceptors(NewPermissionInterceptor(NewDefaultPermissionManager()))
	handler.Register(NewManageSessionTool(&fakeSessionAdminRuntime{}))

	ctx := WithSessionContext(context.Background(), automation.SessionContext{
		SessionID: "heartbeat-session",
		Source:    "heartbeat",
		Sender:    "heartbeat",
	})

	_, err := handler.Handle(ctx, "manage_session", map[string]interface{}{
		"action": "clear_messages",
	})
	var denied ErrPermissionDenied
	if !errors.As(err, &denied) {
		t.Fatalf("expected permission denial, got %v", err)
	}
	if denied.Tool != "manage_session" || !strings.Contains(denied.Reason, "session-management mutations are disabled") {
		t.Fatalf("unexpected permission denial: %+v", denied)
	}
}
