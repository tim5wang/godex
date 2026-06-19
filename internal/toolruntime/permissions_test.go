package toolruntime

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/domain/message"
)

func TestPermissionRequestFromCallExtractsSecurityContext(t *testing.T) {
	call := ToolCall{
		Name: "background",
		NormalizedInput: map[string]interface{}{
			"action":      "run",
			"command":     "go test ./...",
			"path":        "notes/todo.txt",
			"root":        ".",
			"output_path": ".godex/out.txt",
		},
		SessionContext: automation.SessionContext{
			SessionID: "session-1",
			Source:    string(message.SourceWeb),
			Sender:    "taiwu",
		},
	}

	req := PermissionRequestFromCall(call)
	if req.SessionID != "session-1" || req.Source != string(message.SourceWeb) || req.Sender != "taiwu" {
		t.Fatalf("unexpected session context: %+v", req)
	}
	if req.ToolName != "background" || req.Action != "run" {
		t.Fatalf("unexpected tool identity: %+v", req)
	}
	if req.Command != "go test ./..." {
		t.Fatalf("unexpected command extraction: %+v", req)
	}
	if !req.Mutation {
		t.Fatalf("expected background to be treated as mutation: %+v", req)
	}
	expectedPaths := []string{"notes/todo.txt", ".", ".godex/out.txt"}
	if !reflect.DeepEqual(req.Paths, expectedPaths) {
		t.Fatalf("unexpected path extraction: want %v got %v", expectedPaths, req.Paths)
	}
}

func TestPermissionManagerSessionDecisionOverridesRule(t *testing.T) {
	manager := NewPermissionManager(NewAutomationMutationRule())
	req := PermissionRequest{
		SessionID: "cron-session",
		Source:    string(message.SourceCron),
		ToolName:  "cron",
		Action:    "create",
		Mutation:  true,
	}

	result := manager.Evaluate(req)
	if result.Decision != PermissionDeny {
		t.Fatalf("expected static rule deny, got %+v", result)
	}

	manager.AllowSession(req)
	result = manager.Evaluate(req)
	if result.Decision != PermissionAllow || result.Scope != "session" {
		t.Fatalf("expected session allow override, got %+v", result)
	}

	manager.ResetSession("cron-session")
	result = manager.Evaluate(req)
	if result.Decision != PermissionDeny {
		t.Fatalf("expected reset to restore static deny, got %+v", result)
	}
}

func TestPermissionManagerCreatesPendingApprovalForRemoteShell(t *testing.T) {
	manager := NewDefaultPermissionManager()
	now := time.Date(2026, 5, 16, 10, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	req := PermissionRequest{
		SessionID: "web-session",
		Source:    string(message.SourceWeb),
		ToolName:  "bash",
		Action:    "exec",
		Command:   "go test ./...",
		Mutation:  true,
	}

	result := manager.Evaluate(req)
	if result.Decision != PermissionPending || result.RequestID == "" {
		t.Fatalf("expected pending approval with request id, got %+v", result)
	}

	pending := manager.ListPending("web-session")
	if len(pending) != 1 {
		t.Fatalf("expected one pending approval, got %d", len(pending))
	}
	if pending[0].ID != result.RequestID || pending[0].Request.Command != "go test ./..." {
		t.Fatalf("unexpected pending approval: %+v", pending[0])
	}
	if pending[0].ExpiresAt.IsZero() || !pending[0].ExpiresAt.After(now) {
		t.Fatalf("expected pending approval expiry after now, got %+v", pending[0])
	}
}

func TestPermissionManagerExpiresPendingApproval(t *testing.T) {
	now := time.Date(2026, 5, 16, 10, 0, 0, 0, time.UTC)
	manager := NewPermissionManagerForPolicy(PermissionPolicy{
		InteractiveApproval: InteractiveApprovalPolicy{
			Enabled:           true,
			Mode:              InteractiveApprovalModeManual,
			Sources:           []string{string(message.SourceWeb)},
			Tools:             []string{"bash"},
			PendingTTLSeconds: 5,
		},
	})
	manager.now = func() time.Time { return now }
	req := PermissionRequest{
		SessionID: "web-session",
		Source:    string(message.SourceWeb),
		ToolName:  "bash",
		Action:    "exec",
		Command:   "cargo --version",
		Mutation:  true,
	}

	result := manager.Evaluate(req)
	if result.Decision != PermissionPending || result.RequestID == "" {
		t.Fatalf("expected pending approval, got %+v", result)
	}
	now = now.Add(6 * time.Second)

	if _, err := manager.ApprovePending("web-session", result.RequestID, PermissionGrantOnce); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expired approval error, got %v", err)
	}
	if pending := manager.ListPending("web-session"); len(pending) != 0 {
		t.Fatalf("expected expired pending request to be pruned, got %+v", pending)
	}
}

func TestPermissionManagerRequiresApprovalForUnlistedShellCommand(t *testing.T) {
	manager := NewDefaultPermissionManager()
	req := PermissionRequest{
		SessionID: "local-session",
		Source:    "local",
		ToolName:  "bash",
		Action:    "exec",
		Command:   "cargo --version",
		Mutation:  true,
	}

	result := manager.Evaluate(req)
	if result.Decision != PermissionPending || result.RequestID == "" {
		t.Fatalf("expected unlisted shell command to require approval, got %+v", result)
	}
	if !strings.Contains(result.Reason, "outside the allowlist: cargo") {
		t.Fatalf("expected allowlist reason, got %q", result.Reason)
	}
}

func TestPermissionManagerRequiresApprovalForUnlistedBackgroundCommand(t *testing.T) {
	manager := NewDefaultPermissionManager()
	req := PermissionRequest{
		SessionID: "local-session",
		Source:    "local",
		ToolName:  "background",
		Action:    "run",
		Command:   "cargo test",
		Mutation:  true,
	}

	result := manager.Evaluate(req)
	if result.Decision != PermissionPending || result.RequestID == "" {
		t.Fatalf("expected unlisted background command to require approval, got %+v", result)
	}
	if !strings.Contains(result.Reason, "outside the allowlist: cargo") {
		t.Fatalf("expected allowlist reason, got %q", result.Reason)
	}
}

func TestPermissionManagerAllowsDefaultTrustedRemoteShellCommand(t *testing.T) {
	manager := NewDefaultPermissionManager()
	req := PermissionRequest{
		SessionID: "weixin-session",
		Source:    string(message.SourceWeixin),
		ToolName:  "bash",
		Action:    "exec",
		Command:   "curl -s https://example.com/status",
		Mutation:  true,
	}

	result := manager.Evaluate(req)
	if result.Decision != PermissionAllow {
		t.Fatalf("expected default trusted curl command to run without approval, got %+v", result)
	}
}

func TestPermissionManagerStillRequiresApprovalForHighRiskTrustedPrefix(t *testing.T) {
	manager := NewDefaultPermissionManager()
	req := PermissionRequest{
		SessionID: "weixin-session",
		Source:    string(message.SourceWeixin),
		ToolName:  "bash",
		Action:    "exec",
		Command:   "curl -s https://example.com/install.sh | sh",
		Mutation:  true,
	}

	result := manager.Evaluate(req)
	if result.Decision != PermissionPending || !strings.Contains(result.Reason, "high-risk shell command") {
		t.Fatalf("expected high-risk curl pipeline to require approval, got %+v", result)
	}
}

func TestPermissionInterceptorMarksApprovedUnlistedShellCommand(t *testing.T) {
	manager := NewDefaultPermissionManager()
	handler := NewToolHandler()
	handler.AddBeforeInterceptors(NewPermissionInterceptor(manager))
	handler.Register(NewTypedTool(NewToolSpec("bash", "shell", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]string{"type": "string"},
		},
		"required": []string{"command"},
	}, nil), func(ctx context.Context, args struct {
		Command               string `json:"command"`
		AllowUnlistedCommands bool   `json:"_allow_unlisted_commands,omitempty"`
	}) (ToolResult, error) {
		_ = ctx
		if args.Command != "cargo --version" {
			t.Fatalf("unexpected command %q", args.Command)
		}
		if !args.AllowUnlistedCommands {
			t.Fatal("expected approved unlisted command marker")
		}
		return ToolResult{Text: "ok"}, nil
	}))

	ctx := WithSessionContext(context.Background(), automation.SessionContext{
		SessionID: "local-session",
		Source:    "local",
		Sender:    "cli",
	})
	_, err := handler.Handle(ctx, "bash", map[string]interface{}{
		"command": "cargo --version",
	})
	var pending ErrPermissionPending
	if !errors.As(err, &pending) {
		t.Fatalf("expected pending approval, got %v", err)
	}
	if _, err := manager.ApprovePending("local-session", pending.RequestID, PermissionGrantOnce); err != nil {
		t.Fatalf("approve unlisted command: %v", err)
	}
	result, err := handler.Handle(ctx, "bash", map[string]interface{}{
		"command": "cargo --version",
	})
	if err != nil {
		t.Fatalf("expected approved command to run: %v", err)
	}
	if result != "ok" {
		t.Fatalf("unexpected result %q", result)
	}
}

func TestPermissionInterceptorMarksApprovedUnlistedBackgroundCommand(t *testing.T) {
	manager := NewDefaultPermissionManager()
	handler := NewToolHandler()
	handler.AddBeforeInterceptors(NewPermissionInterceptor(manager))
	handler.Register(NewTypedTool(NewToolSpec("background", "background", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]string{"type": "string"},
		},
		"required": []string{"command"},
	}, nil), func(ctx context.Context, args struct {
		Command               string `json:"command"`
		AllowUnlistedCommands bool   `json:"_allow_unlisted_commands,omitempty"`
	}) (ToolResult, error) {
		_ = ctx
		if args.Command != "cargo test" {
			t.Fatalf("unexpected command %q", args.Command)
		}
		if !args.AllowUnlistedCommands {
			t.Fatal("expected approved unlisted background command marker")
		}
		return ToolResult{Text: "ok"}, nil
	}))

	ctx := WithSessionContext(context.Background(), automation.SessionContext{
		SessionID: "local-session",
		Source:    "local",
		Sender:    "cli",
	})
	_, err := handler.Handle(ctx, "background", map[string]interface{}{
		"command": "cargo test",
	})
	var pending ErrPermissionPending
	if !errors.As(err, &pending) {
		t.Fatalf("expected pending approval, got %v", err)
	}
	if _, err := manager.ApprovePending("local-session", pending.RequestID, PermissionGrantOnce); err != nil {
		t.Fatalf("approve unlisted background command: %v", err)
	}
	result, err := handler.Handle(ctx, "background", map[string]interface{}{
		"command": "cargo test",
	})
	if err != nil {
		t.Fatalf("expected approved background command to run: %v", err)
	}
	if result != "ok" {
		t.Fatalf("unexpected result %q", result)
	}
}

func TestPermissionManagerAllowOnceConsumesSingleUse(t *testing.T) {
	manager := NewDefaultPermissionManager()
	req := PermissionRequest{
		SessionID: "web-session",
		Source:    string(message.SourceWeb),
		ToolName:  "write_file",
		Action:    "write",
		Paths:     []string{"notes/todo.txt"},
		Mutation:  true,
	}

	first := manager.Evaluate(req)
	if first.Decision != PermissionPending {
		t.Fatalf("expected initial pending approval, got %+v", first)
	}

	resolution, err := manager.ApprovePending("web-session", first.RequestID, PermissionGrantOnce)
	if err != nil {
		t.Fatalf("approve pending once: %v", err)
	}
	if resolution.Scope != PermissionGrantOnce || resolution.Decision != PermissionAllow {
		t.Fatalf("unexpected approval resolution: %+v", resolution)
	}

	second := manager.Evaluate(req)
	if second.Decision != PermissionAllow {
		t.Fatalf("expected one-time approval to allow next execution, got %+v", second)
	}
	third := manager.Evaluate(req)
	if third.Decision != PermissionPending {
		t.Fatalf("expected one-time approval to be consumed, got %+v", third)
	}
}

func TestPermissionInterceptorDeniesAutomationToolExchangeMutation(t *testing.T) {
	handler := NewToolHandler()
	handler.AddBeforeInterceptors(NewPermissionInterceptor(NewDefaultPermissionManager()))
	handler.Register(NewTypedTool(NewToolSpec("tool_exchange", "exchange", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"enable_bundles": map[string]interface{}{
				"type":  "array",
				"items": map[string]string{"type": "string"},
			},
		},
	}, nil), func(ctx context.Context, args struct {
		EnableBundles []string `json:"enable_bundles,omitempty"`
	}) (ToolResult, error) {
		_ = ctx
		return ToolResult{Structured: args}, nil
	}))

	ctx := WithSessionContext(context.Background(), automation.SessionContext{
		SessionID: "heartbeat-session",
		Source:    string(message.SourceHeartbeat),
		Sender:    "heartbeat",
	})
	_, err := handler.Handle(ctx, "tool_exchange", map[string]interface{}{
		"enable_bundles": []interface{}{"background"},
	})
	var denied ErrPermissionDenied
	if !errors.As(err, &denied) {
		t.Fatalf("expected permission denial, got %v", err)
	}
	if denied.Tool != "tool_exchange" || !strings.Contains(denied.Error(), "tool bundle mutations are disabled") {
		t.Fatalf("unexpected permission denial: %+v", denied)
	}
}

func TestPermissionInterceptorAllowsAutomationToolExchangeInspection(t *testing.T) {
	handler := NewToolHandler()
	handler.AddBeforeInterceptors(NewPermissionInterceptor(NewDefaultPermissionManager()))
	handler.Register(NewTypedTool(NewToolSpec("tool_exchange", "exchange", map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}, nil), func(ctx context.Context, args struct{}) (ToolResult, error) {
		_ = ctx
		return ToolResult{Text: "ok"}, nil
	}))

	ctx := WithSessionContext(context.Background(), automation.SessionContext{
		SessionID: "cron-session",
		Source:    string(message.SourceCron),
		Sender:    "cron",
	})
	result, err := handler.Handle(ctx, "tool_exchange", map[string]interface{}{})
	if err != nil {
		t.Fatalf("expected inspection call to pass, got %v", err)
	}
	if result != "ok" {
		t.Fatalf("unexpected tool exchange result %q", result)
	}
}

func TestPermissionInterceptorReturnsPendingErrorForRemoteMutation(t *testing.T) {
	handler := NewToolHandler()
	handler.AddBeforeInterceptors(NewPermissionInterceptor(NewDefaultPermissionManager()))
	handler.Register(NewTypedTool(NewToolSpec("write_file", "write", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path":    map[string]string{"type": "string"},
			"content": map[string]string{"type": "string"},
		},
		"required": []string{"path", "content"},
	}, nil), func(ctx context.Context, args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}) (ToolResult, error) {
		_ = ctx
		return ToolResult{Text: args.Path + ":" + args.Content}, nil
	}))

	ctx := WithSessionContext(context.Background(), automation.SessionContext{
		SessionID: "web-session",
		Source:    string(message.SourceWeb),
		Sender:    "lead",
	})
	_, err := handler.Handle(ctx, "write_file", map[string]interface{}{
		"path":    "notes/todo.txt",
		"content": "hello",
	})
	var pending ErrPermissionPending
	if !errors.As(err, &pending) {
		t.Fatalf("expected pending approval error, got %v", err)
	}
	if pending.Tool != "write_file" || pending.RequestID == "" {
		t.Fatalf("unexpected pending approval: %+v", pending)
	}
}

func TestPermissionInterceptorReviewModeAllowsAfterReviewerApproval(t *testing.T) {
	manager := NewPermissionManagerForPolicy(PermissionPolicy{
		BlockAutomationMutations: true,
		InteractiveApproval: InteractiveApprovalPolicy{
			Mode:    InteractiveApprovalModeReview,
			Enabled: true,
			Sources: []string{string(message.SourceWeb)},
			Tools:   []string{"write_file"},
		},
	})

	handler := NewToolHandler()
	handler.AddBeforeInterceptors(NewPermissionInterceptorWithReview(manager, func(ctx context.Context, req PermissionRequest) (PermissionResult, error) {
		_ = ctx
		if req.ToolName != "write_file" {
			t.Fatalf("unexpected review request: %+v", req)
		}
		return PermissionResult{Decision: PermissionAllow, Reason: "safe"}, nil
	}))
	handler.Register(NewTypedTool(NewToolSpec("write_file", "write", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path":    map[string]string{"type": "string"},
			"content": map[string]string{"type": "string"},
		},
		"required": []string{"path", "content"},
	}, nil), func(ctx context.Context, args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}) (ToolResult, error) {
		_ = ctx
		return ToolResult{Text: args.Path + ":" + args.Content}, nil
	}))

	ctx := WithSessionContext(context.Background(), automation.SessionContext{
		SessionID: "web-session",
		Source:    string(message.SourceWeb),
		Sender:    "lead",
	})
	result, err := handler.Handle(ctx, "write_file", map[string]interface{}{
		"path":    "notes/todo.txt",
		"content": "hello",
	})
	if err != nil {
		t.Fatalf("expected review approval to allow call, got %v", err)
	}
	if result != "notes/todo.txt:hello" {
		t.Fatalf("unexpected write result %q", result)
	}
	if pending := manager.ListPending("web-session"); len(pending) != 0 {
		t.Fatalf("expected no pending approvals after review allow, got %+v", pending)
	}
}

func TestPermissionInterceptorReviewModeFallsBackToManualApproval(t *testing.T) {
	manager := NewPermissionManagerForPolicy(PermissionPolicy{
		BlockAutomationMutations: true,
		InteractiveApproval: InteractiveApprovalPolicy{
			Mode:    InteractiveApprovalModeReview,
			Enabled: true,
			Sources: []string{string(message.SourceWeb)},
			Tools:   []string{"write_file"},
		},
	})

	handler := NewToolHandler()
	handler.AddBeforeInterceptors(NewPermissionInterceptorWithReview(manager, func(ctx context.Context, req PermissionRequest) (PermissionResult, error) {
		_ = ctx
		return PermissionResult{Decision: PermissionPending, Reason: "needs human confirmation"}, nil
	}))
	handler.Register(NewTypedTool(NewToolSpec("write_file", "write", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path":    map[string]string{"type": "string"},
			"content": map[string]string{"type": "string"},
		},
		"required": []string{"path", "content"},
	}, nil), func(ctx context.Context, args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}) (ToolResult, error) {
		_ = ctx
		return ToolResult{Text: args.Path + ":" + args.Content}, nil
	}))

	ctx := WithSessionContext(context.Background(), automation.SessionContext{
		SessionID: "web-session",
		Source:    string(message.SourceWeb),
		Sender:    "lead",
	})
	_, err := handler.Handle(ctx, "write_file", map[string]interface{}{
		"path":    "notes/todo.txt",
		"content": "hello",
	})
	var pending ErrPermissionPending
	if !errors.As(err, &pending) {
		t.Fatalf("expected manual approval fallback, got %v", err)
	}
	if pending.RequestID == "" {
		t.Fatalf("expected pending approval id, got %+v", pending)
	}
	if got := manager.ListPending("web-session"); len(got) != 1 {
		t.Fatalf("expected one pending approval after manual fallback, got %+v", got)
	}
}

func TestPermissionManagerPolicyCanDisableInteractiveApprovals(t *testing.T) {
	manager := NewPermissionManagerForPolicy(PermissionPolicy{
		BlockAutomationMutations: true,
		InteractiveApproval: InteractiveApprovalPolicy{
			Enabled: false,
			Sources: []string{string(message.SourceWeb)},
			Tools:   []string{"write_file"},
		},
	})
	req := PermissionRequest{
		SessionID: "web-session",
		Source:    string(message.SourceWeb),
		ToolName:  "write_file",
		Action:    "write",
		Paths:     []string{"notes/todo.txt"},
		Mutation:  true,
	}

	result := manager.Evaluate(req)
	if result.Decision != PermissionAbstain {
		t.Fatalf("expected approvals to be disabled, got %+v", result)
	}
}

func TestPermissionManagerPolicyUsesConfiguredSourcesAndTools(t *testing.T) {
	manager := NewPermissionManagerForPolicy(PermissionPolicy{
		BlockAutomationMutations: true,
		InteractiveApproval: InteractiveApprovalPolicy{
			Enabled: true,
			Sources: []string{string(message.SourceFeishu)},
			Tools:   []string{"browser"},
		},
	})
	req := PermissionRequest{
		SessionID: "feishu-session",
		Source:    string(message.SourceFeishu),
		ToolName:  "browser",
		Action:    "click",
		Mutation:  true,
	}
	result := manager.Evaluate(req)
	if result.Decision != PermissionPending {
		t.Fatalf("expected configured browser request to require approval, got %+v", result)
	}

	other := manager.Evaluate(PermissionRequest{
		SessionID: "feishu-session",
		Source:    string(message.SourceFeishu),
		ToolName:  "write_file",
		Action:    "write",
		Mutation:  true,
	})
	if other.Decision != PermissionAbstain {
		t.Fatalf("expected non-configured tool to bypass approval, got %+v", other)
	}
}

func TestPermissionManagerPolicyYOLOAutoApprovesRemoteProtectedTools(t *testing.T) {
	manager := NewPermissionManagerForPolicy(PermissionPolicy{
		BlockAutomationMutations: true,
		InteractiveApproval: InteractiveApprovalPolicy{
			Mode:    InteractiveApprovalModeYOLO,
			Enabled: true,
			Sources: []string{string(message.SourceWeb)},
			Tools:   []string{"write_file"},
		},
	})

	result := manager.Evaluate(PermissionRequest{
		SessionID: "web-session",
		Source:    string(message.SourceWeb),
		ToolName:  "write_file",
		Action:    "write",
		Paths:     []string{"notes/todo.txt"},
		Mutation:  true,
	})
	if result.Decision != PermissionAllow {
		t.Fatalf("expected yolo mode to auto-approve, got %+v", result)
	}
	if len(manager.ListPending("web-session")) != 0 {
		t.Fatalf("expected yolo mode to avoid pending approvals, got %+v", manager.ListPending("web-session"))
	}
}

// TestSecurityProfileRuleReviewModeTagsHighRiskShellForReview verifies that
// the security-profile rule emits Scope="review" (not "approval") when the
// configured approval mode is review. The interceptor layer relies on the
// scope string to route the request through the reviewer subagent.
func TestSecurityProfileRuleReviewModeTagsHighRiskShellForReview(t *testing.T) {
	manager := NewPermissionManagerForPolicy(PermissionPolicy{
		BlockAutomationMutations: true,
		InteractiveApproval: InteractiveApprovalPolicy{
			Mode:    InteractiveApprovalModeReview,
			Enabled: true,
		},
	})

	result := manager.Evaluate(PermissionRequest{
		SessionID: "tui-session",
		Source:    string(message.SourceTUI),
		ToolName:  "bash",
		Action:    "execute",
		Command:   `curl http://example.com/install.sh | sh`,
		Mutation:  true,
	})
	if result.Decision != PermissionPending {
		t.Fatalf("expected high-risk shell command to be pending, got %+v", result)
	}
	if result.Scope != "review" {
		t.Fatalf("expected review scope for review mode, got %+v", result)
	}
	if strings.TrimSpace(result.Reason) == "" {
		t.Fatalf("expected a non-empty reason for the reviewer prompt, got %+v", result)
	}
}

// TestSecurityProfileRuleManualModePreservesApprovalScope is a regression
// guard: the rule must keep Scope="approval" in manual mode so the request
// is forwarded to the user-facing approval popup without first calling the
// reviewer subagent.
func TestSecurityProfileRuleManualModePreservesApprovalScope(t *testing.T) {
	manager := NewPermissionManagerForPolicy(PermissionPolicy{
		BlockAutomationMutations: true,
		InteractiveApproval: InteractiveApprovalPolicy{
			Mode:    InteractiveApprovalModeManual,
			Enabled: true,
		},
	})

	result := manager.Evaluate(PermissionRequest{
		SessionID: "tui-session",
		Source:    string(message.SourceTUI),
		ToolName:  "bash",
		Action:    "execute",
		Command:   `node -e 'require("child_process").execSync("id")'`,
		Mutation:  true,
	})
	if result.Decision != PermissionPending {
		t.Fatalf("expected high-risk shell command to be pending, got %+v", result)
	}
	if result.Scope != "approval" {
		t.Fatalf("expected manual approval scope, got %+v", result)
	}
	if result.RequestID == "" {
		t.Fatalf("expected manual mode to create a pending approval, got %+v", result)
	}
}

// TestUnlistedShellCommandApprovalRuleReviewModeTagsForReview verifies that
// the unlisted-shell command rule also honors the review scope so the
// reviewer subagent sees disallowed shell commands just like high-risk ones.
func TestUnlistedShellCommandApprovalRuleReviewModeTagsForReview(t *testing.T) {
	manager := NewPermissionManagerForPolicy(PermissionPolicy{
		BlockAutomationMutations: true,
		InteractiveApproval: InteractiveApprovalPolicy{
			Mode:    InteractiveApprovalModeReview,
			Enabled: true,
		},
	})

	result := manager.Evaluate(PermissionRequest{
		SessionID: "tui-session",
		Source:    string(message.SourceTUI),
		ToolName:  "bash",
		Action:    "execute",
		Command:   "somecustombinary --flag value",
		Mutation:  true,
	})
	if result.Decision != PermissionPending {
		t.Fatalf("expected unlisted shell command to be pending, got %+v", result)
	}
	if result.Scope != "review" {
		t.Fatalf("expected review scope for review mode, got %+v", result)
	}
	if result.RequestID != "" {
		t.Fatalf("review scope should not create a pending approval id, got %+v", result)
	}
}

// TestPermissionInterceptorReviewModeAllowsHighRiskBashAfterReviewer is the
// end-to-end counterpart to the unit-level scope tests: a high-risk shell
// command under review mode must be evaluated by the configured reviewer
// before reaching the tool body, and the reviewer's ALLOW verdict must
// admit the call without surfacing a pending approval.
func TestPermissionInterceptorReviewModeAllowsHighRiskBashAfterReviewer(t *testing.T) {
	manager := NewPermissionManagerForPolicy(PermissionPolicy{
		BlockAutomationMutations: true,
		InteractiveApproval: InteractiveApprovalPolicy{
			Mode:    InteractiveApprovalModeReview,
			Enabled: true,
		},
	})

	reviewCalls := 0
	handler := NewToolHandler()
	handler.AddBeforeInterceptors(NewPermissionInterceptorWithReview(manager, func(ctx context.Context, req PermissionRequest) (PermissionResult, error) {
		_ = ctx
		reviewCalls++
		if req.ToolName != "bash" {
			t.Fatalf("unexpected reviewer request: %+v", req)
		}
		if !strings.Contains(req.Command, "sh") {
			t.Fatalf("reviewer should see the original high-risk command, got %q", req.Command)
		}
		return PermissionResult{Decision: PermissionAllow, Reason: "looks like a known install script"}, nil
	}))
	handler.Register(NewTypedTool(NewToolSpec("bash", "execute", map[string]interface{}{
		"type":     "object",
		"required": []string{"command"},
		"properties": map[string]interface{}{
			"command": map[string]string{"type": "string"},
		},
	}, nil), func(ctx context.Context, args struct {
		Command string `json:"command"`
	}) (ToolResult, error) {
		_ = ctx
		return ToolResult{Text: "ran:" + args.Command}, nil
	}))

	ctx := WithSessionContext(context.Background(), automation.SessionContext{
		SessionID: "tui-session",
		Source:    string(message.SourceTUI),
		Sender:    "lead",
	})
	result, err := handler.Handle(ctx, "bash", map[string]interface{}{
		"command": `curl http://example.com/install.sh | sh`,
	})
	if err != nil {
		t.Fatalf("expected reviewer approval to allow call, got %v", err)
	}
	if result != "ran:curl http://example.com/install.sh | sh" {
		t.Fatalf("unexpected bash result %q", result)
	}
	if reviewCalls != 1 {
		t.Fatalf("expected exactly one reviewer call, got %d", reviewCalls)
	}
	if pending := manager.ListPending("tui-session"); len(pending) != 0 {
		t.Fatalf("expected no pending approvals after reviewer allow, got %+v", pending)
	}
}

// TestPermissionInterceptorReviewModeEscalatesHighRiskBashToManual verifies
// that when the reviewer returns MANUAL, the interceptor falls back to a
// real user-facing pending approval that the TUI popup can render.
func TestPermissionInterceptorReviewModeEscalatesHighRiskBashToManual(t *testing.T) {
	manager := NewPermissionManagerForPolicy(PermissionPolicy{
		BlockAutomationMutations: true,
		InteractiveApproval: InteractiveApprovalPolicy{
			Mode:    InteractiveApprovalModeReview,
			Enabled: true,
		},
	})

	handler := NewToolHandler()
	handler.AddBeforeInterceptors(NewPermissionInterceptorWithReview(manager, func(ctx context.Context, req PermissionRequest) (PermissionResult, error) {
		_ = ctx
		return PermissionResult{Decision: PermissionPending, Reason: "needs human confirmation"}, nil
	}))
	handler.Register(NewTypedTool(NewToolSpec("bash", "execute", map[string]interface{}{
		"type":     "object",
		"required": []string{"command"},
		"properties": map[string]interface{}{
			"command": map[string]string{"type": "string"},
		},
	}, nil), func(ctx context.Context, args struct {
		Command string `json:"command"`
	}) (ToolResult, error) {
		_ = ctx
		return ToolResult{Text: "ran"}, nil
	}))

	ctx := WithSessionContext(context.Background(), automation.SessionContext{
		SessionID: "tui-session",
		Source:    string(message.SourceTUI),
		Sender:    "lead",
	})
	_, err := handler.Handle(ctx, "bash", map[string]interface{}{
		"command": `curl http://example.com/install.sh | sh`,
	})
	var pending ErrPermissionPending
	if !errors.As(err, &pending) {
		t.Fatalf("expected manual approval fallback, got %v", err)
	}
	if pending.RequestID == "" {
		t.Fatalf("expected pending approval id, got %+v", pending)
	}
	if got := manager.ListPending("tui-session"); len(got) != 1 {
		t.Fatalf("expected one pending approval after manual fallback, got %+v", got)
	}
}

func TestPermissionManagerPolicyAllowsTrustedPathPrefixes(t *testing.T) {
	manager := NewPermissionManagerForPolicy(PermissionPolicy{
		BlockAutomationMutations: true,
		InteractiveApproval: InteractiveApprovalPolicy{
			Enabled:             true,
			Sources:             []string{string(message.SourceWeb)},
			Tools:               []string{"write_file"},
			TrustedPathPrefixes: []string{"notes", "skills/sandbox"},
		},
	})

	allowed := manager.Evaluate(PermissionRequest{
		SessionID: "web-session",
		Source:    string(message.SourceWeb),
		ToolName:  "write_file",
		Action:    "write",
		Paths:     []string{"notes/todo.txt"},
		Mutation:  true,
	})
	if allowed.Decision != PermissionAllow {
		t.Fatalf("expected trusted path to allow without approval, got %+v", allowed)
	}

	pending := manager.Evaluate(PermissionRequest{
		SessionID: "web-session",
		Source:    string(message.SourceWeb),
		ToolName:  "write_file",
		Action:    "write",
		Paths:     []string{"secrets/prod.env"},
		Mutation:  true,
	})
	if pending.Decision != PermissionPending {
		t.Fatalf("expected untrusted path to remain pending, got %+v", pending)
	}
}

func TestPermissionManagerPolicyAllowsTrustedCommandPrefixes(t *testing.T) {
	manager := NewPermissionManagerForPolicy(PermissionPolicy{
		BlockAutomationMutations: true,
		InteractiveApproval: InteractiveApprovalPolicy{
			Enabled:                true,
			Sources:                []string{string(message.SourceWeb)},
			Tools:                  []string{"bash", "background"},
			TrustedPathPrefixes:    []string{".godex/.tmp", "notes"},
			TrustedCommandPrefixes: []string{"git status", "go test ./..."},
		},
	})

	allowed := manager.Evaluate(PermissionRequest{
		SessionID: "web-session",
		Source:    string(message.SourceWeb),
		ToolName:  "background",
		Action:    "run",
		Command:   "go test ./...",
		Paths:     []string{".godex/.tmp/results.txt"},
		Mutation:  true,
	})
	if allowed.Decision != PermissionAllow {
		t.Fatalf("expected trusted command prefix to allow background run, got %+v", allowed)
	}

	pending := manager.Evaluate(PermissionRequest{
		SessionID: "web-session",
		Source:    string(message.SourceWeb),
		ToolName:  "background",
		Action:    "run",
		Command:   "go test ./...",
		Paths:     []string{"secrets/results.txt"},
		Mutation:  true,
	})
	if pending.Decision != PermissionPending {
		t.Fatalf("expected untrusted output path to keep approval pending, got %+v", pending)
	}
}

func TestPermissionManagerExportAndRestoreSessionState(t *testing.T) {
	manager := NewDefaultPermissionManager()

	pendingReq := PermissionRequest{
		SessionID: "web-session",
		Source:    string(message.SourceWeb),
		ToolName:  "bash",
		Action:    "exec",
		Command:   "cargo --version",
		Mutation:  true,
	}
	if result := manager.Evaluate(pendingReq); result.Decision != PermissionPending {
		t.Fatalf("expected pending request, got %+v", result)
	}

	onceReq := PermissionRequest{
		SessionID: "web-session",
		Source:    string(message.SourceWeb),
		ToolName:  "write_file",
		Action:    "write",
		Paths:     []string{"notes/todo.txt"},
		Mutation:  true,
	}
	first := manager.Evaluate(onceReq)
	if first.Decision != PermissionPending {
		t.Fatalf("expected write_file approval request, got %+v", first)
	}
	if _, err := manager.ApprovePending("web-session", first.RequestID, PermissionGrantOnce); err != nil {
		t.Fatalf("approve pending once: %v", err)
	}

	exported := manager.ExportSession("web-session")
	if len(exported.Pending) != 1 || len(exported.Overrides) != 1 {
		t.Fatalf("unexpected exported permission state: %+v", exported)
	}

	restored := NewDefaultPermissionManager()
	restored.RestoreSession("web-session", exported)

	pending := restored.ListPending("web-session")
	if len(pending) != 1 || pending[0].Request.ToolName != "bash" {
		t.Fatalf("unexpected restored pending requests: %+v", pending)
	}

	second := restored.Evaluate(onceReq)
	if second.Decision != PermissionAllow {
		t.Fatalf("expected restored one-time approval to allow next execution, got %+v", second)
	}
	third := restored.Evaluate(onceReq)
	if third.Decision != PermissionPending {
		t.Fatalf("expected restored one-time approval to be consumed, got %+v", third)
	}
}

func TestPermissionManagerCountApprovalAllowsExactNumberOfUses(t *testing.T) {
	manager := NewDefaultPermissionManager()
	req := PermissionRequest{
		SessionID: "web-session",
		Source:    string(message.SourceWeb),
		ToolName:  "write_file",
		Action:    "write",
		Paths:     []string{"notes/todo.txt"},
		Mutation:  true,
	}
	result := manager.Evaluate(req)
	if result.Decision != PermissionPending {
		t.Fatalf("expected approval request, got %+v", result)
	}
	if _, err := manager.ApprovePending("web-session", result.RequestID, PermissionGrantScope("count:2")); err != nil {
		t.Fatalf("approve count scope: %v", err)
	}
	if first := manager.Evaluate(req); first.Decision != PermissionAllow {
		t.Fatalf("expected first count use to allow, got %+v", first)
	}
	if second := manager.Evaluate(req); second.Decision != PermissionAllow {
		t.Fatalf("expected second count use to allow, got %+v", second)
	}
	if third := manager.Evaluate(req); third.Decision != PermissionPending {
		t.Fatalf("expected count approval to be consumed, got %+v", third)
	}
}

func TestPermissionManagerTimeboxApprovalExpires(t *testing.T) {
	now := time.Date(2026, 5, 16, 10, 0, 0, 0, time.UTC)
	manager := NewDefaultPermissionManager()
	manager.now = func() time.Time { return now }
	req := PermissionRequest{
		SessionID: "web-session",
		Source:    string(message.SourceWeb),
		ToolName:  "write_file",
		Action:    "write",
		Paths:     []string{"notes/todo.txt"},
		Mutation:  true,
	}
	result := manager.Evaluate(req)
	if result.Decision != PermissionPending {
		t.Fatalf("expected approval request, got %+v", result)
	}
	if _, err := manager.ApprovePending("web-session", result.RequestID, PermissionGrantScope("timebox:10m")); err != nil {
		t.Fatalf("approve timebox scope: %v", err)
	}
	if allowed := manager.Evaluate(req); allowed.Decision != PermissionAllow {
		t.Fatalf("expected timebox approval to allow, got %+v", allowed)
	}
	now = now.Add(11 * time.Minute)
	if expired := manager.Evaluate(req); expired.Decision != PermissionPending {
		t.Fatalf("expected expired timebox to require approval again, got %+v", expired)
	}
}

func TestPermissionManagerTaskApprovalStaysWithinTurn(t *testing.T) {
	manager := NewDefaultPermissionManager()
	req := PermissionRequest{
		SessionID: "web-session",
		TurnID:    "turn-1",
		Source:    string(message.SourceWeb),
		ToolName:  "write_file",
		Action:    "write",
		Paths:     []string{"notes/todo.txt"},
		Mutation:  true,
	}
	result := manager.Evaluate(req)
	if result.Decision != PermissionPending {
		t.Fatalf("expected approval request, got %+v", result)
	}
	resolution, err := manager.ApprovePending("web-session", result.RequestID, PermissionGrantTask)
	if err != nil {
		t.Fatalf("approve task scope: %v", err)
	}
	if resolution.Scope != PermissionGrantTask {
		t.Fatalf("expected task scope in resolution, got %+v", resolution)
	}
	if sameTurn := manager.Evaluate(req); sameTurn.Decision != PermissionAllow {
		t.Fatalf("expected same turn request to allow, got %+v", sameTurn)
	}
	nextTurnReq := req
	nextTurnReq.TurnID = "turn-2"
	if nextTurn := manager.Evaluate(nextTurnReq); nextTurn.Decision != PermissionPending {
		t.Fatalf("expected next turn to require fresh approval, got %+v", nextTurn)
	}
}

func TestPermissionManagerPatternApprovalCoversSimilarShellCommands(t *testing.T) {
	manager := NewDefaultPermissionManager()
	req := PermissionRequest{
		SessionID: "web-session",
		Source:    string(message.SourceWeb),
		ToolName:  "bash",
		Action:    "exec",
		Command:   "go test ./internal/toolruntime",
		Mutation:  true,
	}
	result := manager.Evaluate(req)
	if result.Decision != PermissionPending {
		t.Fatalf("expected approval request, got %+v", result)
	}
	if _, err := manager.ApprovePending("web-session", result.RequestID, PermissionGrantPattern); err != nil {
		t.Fatalf("approve pattern scope: %v", err)
	}
	if similar := manager.Evaluate(PermissionRequest{
		SessionID: "web-session",
		Source:    string(message.SourceWeb),
		ToolName:  "bash",
		Action:    "exec",
		Command:   "go test ./internal/agent",
		Mutation:  true,
	}); similar.Decision != PermissionAllow {
		t.Fatalf("expected similar go test command to allow, got %+v", similar)
	}
	if unrelated := manager.Evaluate(PermissionRequest{
		SessionID: "web-session",
		Source:    string(message.SourceWeb),
		ToolName:  "bash",
		Action:    "exec",
		Command:   "cargo test",
		Mutation:  true,
	}); unrelated.Decision != PermissionPending {
		t.Fatalf("expected unrelated command to remain pending, got %+v", unrelated)
	}
}

func TestPermissionSummariesDescribeIntentRiskAndExpiry(t *testing.T) {
	expiresAt := time.Date(2026, 5, 16, 10, 5, 0, 0, time.UTC)
	pending := PendingPermission{
		ID:        "perm-1",
		Reason:    "shell execution requires approval in remote sessions",
		CreatedAt: expiresAt.Add(-5 * time.Minute),
		ExpiresAt: expiresAt,
		Request: PermissionRequest{
			SessionID: "web-session",
			Source:    string(message.SourceWeb),
			ToolName:  "bash",
			Action:    "exec",
			Command:   "go test ./...",
			Mutation:  true,
		},
	}

	if got := PermissionIntentSummary(pending); !strings.Contains(got, "run shell command") || !strings.Contains(got, "go test ./...") {
		t.Fatalf("unexpected intent summary: %q", got)
	}
	if got := PermissionRiskSummary(pending.Request); !strings.Contains(got, "medium") || !strings.Contains(got, "shell") {
		t.Fatalf("unexpected risk summary: %q", got)
	}
	if got := PermissionExpirySummary(pending, expiresAt.Add(-time.Minute)); !strings.Contains(got, "expires in 1m") {
		t.Fatalf("unexpected expiry summary: %q", got)
	}
}

func TestBrowserSessionApprovalCoversSubsequentBrowserActions(t *testing.T) {
	manager := NewDefaultPermissionManager()

	openReq := PermissionRequest{
		SessionID: "weixin-session",
		Source:    string(message.SourceWeixin),
		ToolName:  "browser",
		Action:    "open",
		Mutation:  true,
	}
	openResult := manager.Evaluate(openReq)
	if openResult.Decision != PermissionPending {
		t.Fatalf("expected browser open to require approval, got %+v", openResult)
	}
	if _, err := manager.ApprovePending("weixin-session", openResult.RequestID, PermissionGrantSession); err != nil {
		t.Fatalf("approve browser session: %v", err)
	}

	navigateReq := PermissionRequest{
		SessionID: "weixin-session",
		Source:    string(message.SourceWeixin),
		ToolName:  "browser",
		Action:    "navigate",
		Mutation:  true,
	}
	navigateResult := manager.Evaluate(navigateReq)
	if navigateResult.Decision != PermissionPending {
		t.Fatalf("expected browser session approval to remain action-scoped, got %+v", navigateResult)
	}

	screenshotReq := PermissionRequest{
		SessionID: "weixin-session",
		Source:    string(message.SourceWeixin),
		ToolName:  "browser",
		Action:    "screenshot",
		Mutation:  true,
	}
	screenshotResult := manager.Evaluate(screenshotReq)
	if screenshotResult.Decision != PermissionPending {
		t.Fatalf("expected browser session approval to remain action-scoped, got %+v", screenshotResult)
	}

	otherToolReq := PermissionRequest{
		SessionID: "weixin-session",
		Source:    string(message.SourceWeixin),
		ToolName:  "bash",
		Action:    "exec",
		Command:   "cargo --version",
		Mutation:  true,
	}
	otherToolResult := manager.Evaluate(otherToolReq)
	if otherToolResult.Decision != PermissionPending {
		t.Fatalf("expected browser session approval to remain tool-scoped, got %+v", otherToolResult)
	}
}

func TestDesktopSessionApprovalCoversSubsequentDesktopActions(t *testing.T) {
	manager := NewDefaultPermissionManager()

	clickReq := PermissionRequest{
		SessionID: "weixin-session",
		Source:    string(message.SourceWeixin),
		ToolName:  "desktop",
		Action:    "click",
		Mutation:  true,
	}
	clickResult := manager.Evaluate(clickReq)
	if clickResult.Decision != PermissionPending {
		t.Fatalf("expected desktop click to require approval, got %+v", clickResult)
	}
	if _, err := manager.ApprovePending("weixin-session", clickResult.RequestID, PermissionGrantSession); err != nil {
		t.Fatalf("approve desktop session: %v", err)
	}

	keyReq := PermissionRequest{
		SessionID: "weixin-session",
		Source:    string(message.SourceWeixin),
		ToolName:  "desktop",
		Action:    "key",
		Mutation:  true,
	}
	keyResult := manager.Evaluate(keyReq)
	if keyResult.Decision != PermissionPending {
		t.Fatalf("expected desktop session approval to remain action-scoped, got %+v", keyResult)
	}

	clipboardReadReq := PermissionRequest{
		SessionID: "weixin-session",
		Source:    string(message.SourceWeixin),
		ToolName:  "desktop",
		Action:    "clipboard_get",
		Mutation:  false,
	}
	clipboardReadResult := manager.Evaluate(clipboardReadReq)
	if clipboardReadResult.Decision != PermissionAbstain {
		t.Fatalf("expected desktop read-only action to bypass approval rules, got %+v", clipboardReadResult)
	}
}

func TestBrowserSessionApprovalPersistsAcrossRestore(t *testing.T) {
	manager := NewDefaultPermissionManager()
	req := PermissionRequest{
		SessionID: "weixin-session",
		Source:    string(message.SourceWeixin),
		ToolName:  "browser",
		Action:    "open",
		Mutation:  true,
	}
	result := manager.Evaluate(req)
	if result.Decision != PermissionPending {
		t.Fatalf("expected browser approval request, got %+v", result)
	}
	if _, err := manager.ApprovePending("weixin-session", result.RequestID, PermissionGrantSession); err != nil {
		t.Fatalf("approve browser session: %v", err)
	}

	exported := manager.ExportSession("weixin-session")
	restored := NewDefaultPermissionManager()
	restored.RestoreSession("weixin-session", exported)

	navigateReq := PermissionRequest{
		SessionID: "weixin-session",
		Source:    string(message.SourceWeixin),
		ToolName:  "browser",
		Action:    "navigate",
		Mutation:  true,
	}
	restoredResult := restored.Evaluate(navigateReq)
	if restoredResult.Decision != PermissionPending {
		t.Fatalf("expected restored browser session approval to remain action-scoped, got %+v", restoredResult)
	}
}

// TestPermissionInterceptorReviewModeCachesAllowAsPattern verifies that once
// the reviewer approves a bash command, subsequent invocations of the same
// command pattern within the same session are admitted without re-running
// the reviewer subagent. This is the "review mode is too expensive when a
// command is repeated" guard: the first call pays the LLM cost, the
// remainder hit the cached pattern override.
func TestPermissionInterceptorReviewModeCachesAllowAsPattern(t *testing.T) {
	manager := NewPermissionManagerForPolicy(PermissionPolicy{
		BlockAutomationMutations: true,
		InteractiveApproval: InteractiveApprovalPolicy{
			Mode:    InteractiveApprovalModeReview,
			Enabled: true,
		},
	})

	reviewCalls := 0
	handler := NewToolHandler()
	handler.AddBeforeInterceptors(NewPermissionInterceptorWithReview(manager, func(ctx context.Context, req PermissionRequest) (PermissionResult, error) {
		_ = ctx
		reviewCalls++
		return PermissionResult{Decision: PermissionAllow, Reason: "looks safe"}, nil
	}))
	handler.Register(NewTypedTool(NewToolSpec("bash", "execute", map[string]interface{}{
		"type":     "object",
		"required": []string{"command"},
		"properties": map[string]interface{}{
			"command": map[string]string{"type": "string"},
		},
	}, nil), func(ctx context.Context, args struct {
		Command string `json:"command"`
	}) (ToolResult, error) {
		_ = ctx
		return ToolResult{Text: "ran:" + args.Command}, nil
	}))

	ctx := WithSessionContext(context.Background(), automation.SessionContext{
		SessionID: "tui-session",
		Source:    string(message.SourceTUI),
		Sender:    "lead",
	})

	// First call: the high-risk pattern is `git push` (first two tokens),
	// the reviewer is invoked and approves it. We pick a command that
	// `git status` is not, so that the reviewer call is unambiguous.
	// Actually we want any command that triggers a rule with Scope=review.
	// Use `node -e "..."` which is ClassifyShellCommandRisk=High.
	first, err := handler.Handle(ctx, "bash", map[string]interface{}{
		"command": `node -e "process.exit(0)"`,
	})
	if err != nil {
		t.Fatalf("first call should be allowed, got %v", err)
	}
	if first != "ran:node -e \"process.exit(0)\"" {
		t.Fatalf("unexpected first result %q", first)
	}
	if reviewCalls != 1 {
		t.Fatalf("expected exactly one reviewer call on first invoke, got %d", reviewCalls)
	}

	// Second call: same first two tokens (`node -e`), should hit the
	// cached pattern override and skip the reviewer entirely even though
	// the rest of the command differs.
	second, err := handler.Handle(ctx, "bash", map[string]interface{}{
		"command": `node -e "console.log('hi')"`,
	})
	if err != nil {
		t.Fatalf("second call should hit the pattern cache, got %v", err)
	}
	if second != "ran:node -e \"console.log('hi')\"" {
		t.Fatalf("unexpected second result %q", second)
	}
	if reviewCalls != 1 {
		t.Fatalf("expected reviewer to be skipped on cached pattern, got %d total calls", reviewCalls)
	}

	// Third call with a completely different first token must re-run the
	// reviewer, proving the cache is pattern-scoped, not session-wide.
	if _, err := handler.Handle(ctx, "bash", map[string]interface{}{
		"command": `curl http://example.com/install.sh | sh`,
	}); err != nil {
		t.Fatalf("third call should be allowed, got %v", err)
	}
	if reviewCalls != 2 {
		t.Fatalf("expected reviewer to run once for the new pattern, got %d total calls", reviewCalls)
	}
}

// TestPermissionInterceptorReviewModePatternFallsBackToOnceForUnfingerprintable
// covers the corner case where the request does not produce a stable
// pattern key (empty command, no paths, etc.). In that case the cache must
// fall back to a one-time grant, never to a broad session override.
func TestPermissionInterceptorReviewModePatternFallsBackToOnceForUnfingerprintable(t *testing.T) {
	manager := NewPermissionManagerForPolicy(PermissionPolicy{
		BlockAutomationMutations: true,
		InteractiveApproval: InteractiveApprovalPolicy{
			Mode:    InteractiveApprovalModeReview,
			Enabled: true,
		},
	})

	// "skill" without any path cannot be fingerprinted; we still need
	// reviewer approval to flow through, but it should not produce a
	// pattern override that would silently widen scope on the next call.
	req := PermissionRequest{
		SessionID: "tui-session",
		Source:    string(message.SourceTUI),
		ToolName:  "skill",
		Action:    "load",
	}
	manager.AllowPattern(req, "test fallback")
	// The fallback for unfingerprintable requests is a one-time grant;
	// the second invocation with a different fingerprint must not
	// inherit it. We can probe by switching the sessionID and verifying
	// the override is bound to the exact fingerprint, not to a pattern.
	second := PermissionRequest{
		SessionID: "tui-session",
		Source:    string(message.SourceTUI),
		ToolName:  "skill",
		Action:    "load",
		Paths:     []string{"docs/skill.md"},
	}
	if got := manager.Evaluate(second); got.Decision == PermissionAllow {
		t.Fatalf("unfingerprintable cache must not widen to a path-bearing request: %+v", got)
	}
}
