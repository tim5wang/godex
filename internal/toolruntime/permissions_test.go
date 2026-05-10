package toolruntime

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/domain/message"
)

func TestPermissionRequestFromCallExtractsSecurityContext(t *testing.T) {
	call := ToolCall{
		Name: "background_run",
		NormalizedInput: map[string]interface{}{
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
	if req.ToolName != "background_run" || req.Action != "run" {
		t.Fatalf("unexpected tool identity: %+v", req)
	}
	if req.Command != "go test ./..." {
		t.Fatalf("unexpected command extraction: %+v", req)
	}
	if !req.Mutation {
		t.Fatalf("expected background_run to be treated as mutation: %+v", req)
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
		ToolName:  "background_run",
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
	handler.Register(NewTypedTool(NewToolSpec("background_run", "background", map[string]interface{}{
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
	_, err := handler.Handle(ctx, "background_run", map[string]interface{}{
		"command": "cargo test",
	})
	var pending ErrPermissionPending
	if !errors.As(err, &pending) {
		t.Fatalf("expected pending approval, got %v", err)
	}
	if _, err := manager.ApprovePending("local-session", pending.RequestID, PermissionGrantOnce); err != nil {
		t.Fatalf("approve unlisted background command: %v", err)
	}
	result, err := handler.Handle(ctx, "background_run", map[string]interface{}{
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
			Tools:                  []string{"bash", "background_run"},
			TrustedPathPrefixes:    []string{".godex/.tmp", "notes"},
			TrustedCommandPrefixes: []string{"git status", "go test ./..."},
		},
	})

	allowed := manager.Evaluate(PermissionRequest{
		SessionID: "web-session",
		Source:    string(message.SourceWeb),
		ToolName:  "background_run",
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
		ToolName:  "background_run",
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
		Command:   "pwd",
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
	if navigateResult.Decision != PermissionAllow {
		t.Fatalf("expected browser navigate to inherit session approval, got %+v", navigateResult)
	}

	screenshotReq := PermissionRequest{
		SessionID: "weixin-session",
		Source:    string(message.SourceWeixin),
		ToolName:  "browser",
		Action:    "screenshot",
		Mutation:  true,
	}
	screenshotResult := manager.Evaluate(screenshotReq)
	if screenshotResult.Decision != PermissionAllow {
		t.Fatalf("expected browser screenshot to inherit session approval, got %+v", screenshotResult)
	}

	otherToolReq := PermissionRequest{
		SessionID: "weixin-session",
		Source:    string(message.SourceWeixin),
		ToolName:  "bash",
		Action:    "exec",
		Command:   "pwd",
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
	if keyResult.Decision != PermissionAllow {
		t.Fatalf("expected desktop key to inherit session approval, got %+v", keyResult)
	}

	clipboardReadReq := PermissionRequest{
		SessionID: "weixin-session",
		Source:    string(message.SourceWeixin),
		ToolName:  "desktop",
		Action:    "clipboard_get",
		Mutation:  false,
	}
	clipboardReadResult := manager.Evaluate(clipboardReadReq)
	if clipboardReadResult.Decision != PermissionAllow {
		t.Fatalf("expected desktop session approval to cover read after approval, got %+v", clipboardReadResult)
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
	if restoredResult.Decision != PermissionAllow {
		t.Fatalf("expected restored browser session approval to allow navigate, got %+v", restoredResult)
	}
}
