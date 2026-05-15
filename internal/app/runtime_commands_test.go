package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/services/sessionadmin"
	"github.com/tim5wang/godex/internal/tools"
)

type fakeModelRuntime struct {
	current *config.Config
	updated config.UpdateRequest
}

func (f *fakeModelRuntime) Current() *config.Config { return f.current }

func (f *fakeModelRuntime) Update(ctx context.Context, req config.UpdateRequest) (config.View, error) {
	_ = ctx
	f.updated = req
	if f.current == nil {
		f.current = &config.Config{}
	}
	if profile, _ := req.Values["api.default_model"].(string); profile != "" {
		f.current.DefaultProfileID = profile
		f.current.DefaultModelRef = profile
		return config.View{EffectiveValues: map[string]interface{}{"api.default_model": profile}}, nil
	}
	return config.View{EffectiveValues: map[string]interface{}{}}, nil
}

type fakeModelSessionRuntime struct {
	view         backend.ModelsView
	sessionID    string
	profileID    string
	setCallCount int
}

func (f *fakeModelSessionRuntime) Models(ctx context.Context, sessionID string) (backend.ModelsView, error) {
	_ = ctx
	f.sessionID = sessionID
	return f.view, nil
}

func (f *fakeModelSessionRuntime) SetSessionModelProfile(ctx context.Context, sessionID, profileID string) (backend.ModelsView, error) {
	_ = ctx
	f.sessionID = sessionID
	f.profileID = profileID
	f.setCallCount++
	f.view.SessionProfileID = profileID
	for idx := range f.view.Profiles {
		f.view.Profiles[idx].Selected = f.view.Profiles[idx].ID == profileID
	}
	return f.view, nil
}

type fakeSessionRuntime struct {
	sessions []backend.ListedSession
	opened   *backend.OpenedSession
}

func (f *fakeSessionRuntime) ListSessions(ctx context.Context, filter backend.SessionListFilter) ([]backend.ListedSession, error) {
	_ = ctx
	if strings.TrimSpace(filter.Channel) == "" {
		return f.sessions, nil
	}
	filtered := make([]backend.ListedSession, 0, len(f.sessions))
	for _, item := range f.sessions {
		if item.Locator.Channel == filter.Channel {
			filtered = append(filtered, item)
		}
	}
	return filtered, nil
}

func (f *fakeSessionRuntime) OpenSession(ctx context.Context, locator backend.SessionLocator) (*backend.OpenedSession, error) {
	_ = ctx
	if f.opened != nil {
		return f.opened, nil
	}
	return &backend.OpenedSession{
		SessionID: "session-new",
		Locator:   locator,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}, nil
}

type fakeSessionAdminBackend struct {
	summary          tools.SessionSummary
	context          tools.ContextInspection
	pending          []tools.PendingPermission
	approveResult    tools.PermissionResolution
	lastApproveID    string
	lastApproveScope tools.PermissionGrantScope
	lastDenyID       string
	lastDenyReason   string
	cleared          bool
}

func (f *fakeSessionAdminBackend) SessionSummary(ctx context.Context, sessionID string) (tools.SessionSummary, error) {
	_ = ctx
	summary := f.summary
	if summary.SessionID == "" {
		summary.SessionID = sessionID
	}
	return summary, nil
}

func (f *fakeSessionAdminBackend) PendingPermissions(ctx context.Context, sessionID string) ([]tools.PendingPermission, error) {
	_ = ctx
	_ = sessionID
	return append([]tools.PendingPermission{}, f.pending...), nil
}

func (f *fakeSessionAdminBackend) ApprovePermission(ctx context.Context, sessionID, requestID string, scope tools.PermissionGrantScope) (tools.PermissionResolution, error) {
	_ = ctx
	_ = sessionID
	f.lastApproveID = requestID
	f.lastApproveScope = scope
	if f.approveResult.RequestID != "" {
		result := f.approveResult
		result.RequestID = requestID
		result.Scope = scope
		return result, nil
	}
	return tools.PermissionResolution{
		RequestID: requestID,
		Scope:     scope,
		Request: tools.PermissionRequest{
			ToolName: "write_file",
			Action:   "write",
		},
	}, nil
}

func (f *fakeSessionAdminBackend) DenyPermission(ctx context.Context, sessionID, requestID, reason string) (tools.PermissionResolution, error) {
	_ = ctx
	_ = sessionID
	f.lastDenyID = requestID
	f.lastDenyReason = reason
	return tools.PermissionResolution{
		RequestID: requestID,
		Request: tools.PermissionRequest{
			ToolName: "write_file",
			Action:   "write",
		},
		Reason: reason,
	}, nil
}

func (f *fakeSessionAdminBackend) ClearMessages(ctx context.Context, sessionID string) error {
	_ = ctx
	_ = sessionID
	f.cleared = true
	return nil
}

func (f *fakeSessionAdminBackend) ContextSummary(ctx context.Context, sessionID string) (tools.ContextInspection, error) {
	_ = ctx
	summary := f.context
	if summary.SessionID == "" {
		summary.SessionID = sessionID
	}
	return summary, nil
}

type fakeWeixinAuthProvider struct {
	lastAction string
	status     sessionadmin.WeixinAuthStatus
}

func (f *fakeWeixinAuthProvider) Status(ctx context.Context, accountID string) (sessionadmin.WeixinAuthStatus, error) {
	_ = ctx
	_ = accountID
	f.lastAction = "status"
	return f.status, nil
}

func (f *fakeWeixinAuthProvider) Start(ctx context.Context, accountID string) (sessionadmin.WeixinAuthStatus, error) {
	_ = ctx
	_ = accountID
	f.lastAction = "login"
	return f.status, nil
}

func (f *fakeWeixinAuthProvider) Logout(ctx context.Context, accountID string) (sessionadmin.WeixinAuthStatus, error) {
	_ = ctx
	_ = accountID
	f.lastAction = "logout"
	return f.status, nil
}

func newFakeSessionAdmin(t *testing.T, backendRuntime *fakeSessionAdminBackend) *sessionadmin.Service {
	t.Helper()
	auth := &fakeWeixinAuthProvider{
		status: sessionadmin.WeixinAuthStatus{
			AccountID:  "wx-account",
			Enabled:    true,
			Configured: true,
			Login: &sessionadmin.WeixinAuthLoginStatus{
				Active:       true,
				State:        "pending",
				Message:      "Scan QR code",
				QRCode:       "qr-token",
				QRCodeImgURL: "https://example.com/qr.png",
			},
		},
	}
	return sessionadmin.NewService(
		func() *config.Config {
			return &config.Config{
				StateDir: t.TempDir(),
				Weixin: config.WeixinConfig{
					AccountID: "wx-account",
				},
			}
		},
		backendRuntime,
		auth,
		func(stateDir, accountID, userID string, reveal bool) (sessionadmin.WeixinContextTokenInspection, error) {
			_ = stateDir
			return sessionadmin.WeixinContextTokenInspection{
				AccountID:   accountID,
				UserID:      userID,
				TokenCount:  1,
				UpdatedAt:   time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC),
				TokenMasked: "tok_****_mask",
				Token:       map[bool]string{true: "tok-revealed", false: ""}[reveal],
			}, nil
		},
	)
}

func TestModelCommandHandlerSetUpdatesConfig(t *testing.T) {
	runtime := &fakeModelRuntime{current: &config.Config{Model: "claude-sonnet"}}
	handler := NewModelCommandHandler(runtime)

	result, err := handler(context.Background(), commands.Command{Name: "model", Args: []string{"set", "kimi-k2.5"}})
	if err != nil {
		t.Fatalf("model set: %v", err)
	}
	if got := runtime.updated.Values["api.default_model"]; got != "kimi-k2.5" {
		t.Fatalf("expected update path api.default_model=kimi-k2.5, got %#v", runtime.updated.Values)
	}
	if !strings.Contains(result.Output, "kimi-k2.5") {
		t.Fatalf("unexpected output: %q", result.Output)
	}
}

func TestModelCommandHandlerListsAndSetsSessionProfile(t *testing.T) {
	cfg := &config.Config{
		Model:            "claude-sonnet",
		DefaultProfileID: "sonnet",
		ModelProfiles: map[string]config.ModelProfileConfig{
			"sonnet": {ID: "sonnet", Name: "Claude Sonnet", Provider: "anthropic", Model: "claude-sonnet"},
			"mini":   {ID: "mini", Name: "GPT Mini", Provider: "codex", Model: "gpt-5.4-mini"},
		},
	}
	runtime := &fakeModelRuntime{current: cfg}
	sessions := &fakeModelSessionRuntime{view: backend.ModelsView{
		DefaultProfileID: "sonnet",
		Profiles: []backend.ModelProfile{
			{ID: "sonnet", Name: "Claude Sonnet", Provider: "anthropic", Model: "claude-sonnet", Default: true},
			{ID: "mini", Name: "GPT Mini", Provider: "codex", Model: "gpt-5.4-mini"},
		},
	}}
	handler := NewModelCommandHandlerWithSessions(runtime, sessions)
	ctx := commands.WithSessionContext(context.Background(), commands.SessionContext{SessionID: "session-1", Channel: "web", Key: "default"})

	listResult, err := handler(ctx, commands.Command{Name: "model", Args: []string{"list"}})
	if err != nil {
		t.Fatalf("model list: %v", err)
	}
	if sessions.sessionID != "session-1" {
		t.Fatalf("expected list to inspect session-1, got %q", sessions.sessionID)
	}
	if !strings.Contains(listResult.Output, "Available model profiles:") || !strings.Contains(listResult.Output, "*  sonnet") || !strings.Contains(listResult.Output, "mini") {
		t.Fatalf("unexpected model list output: %q", listResult.Output)
	}

	setResult, err := handler(ctx, commands.Command{Name: "model", Args: []string{"use", "mini"}})
	if err != nil {
		t.Fatalf("model use: %v", err)
	}
	if sessions.profileID != "mini" || sessions.setCallCount != 1 {
		t.Fatalf("expected session profile mini to be set once, got profile=%q calls=%d", sessions.profileID, sessions.setCallCount)
	}
	if !strings.Contains(setResult.Output, "Updated current session model profile to mini") {
		t.Fatalf("unexpected model use output: %q", setResult.Output)
	}
}

func TestModelCommandHandlerDefaultProfileUpdatesConfig(t *testing.T) {
	cfg := &config.Config{
		Model:            "claude-sonnet",
		DefaultProfileID: "sonnet",
		ModelProfiles: map[string]config.ModelProfileConfig{
			"sonnet": {ID: "sonnet", Provider: "anthropic", Model: "claude-sonnet"},
			"mini":   {ID: "mini", Provider: "codex", Model: "gpt-5.4-mini"},
		},
	}
	runtime := &fakeModelRuntime{current: cfg}
	handler := NewModelCommandHandlerWithSessions(runtime, nil)

	result, err := handler(context.Background(), commands.Command{Name: "model", Args: []string{"default", "mini"}})
	if err != nil {
		t.Fatalf("model default: %v", err)
	}
	if got := runtime.updated.Values["api.default_model"]; got != "mini" {
		t.Fatalf("expected default model update, got %#v", runtime.updated.Values)
	}
	if !strings.Contains(result.Output, "Updated default model profile to mini") {
		t.Fatalf("unexpected output: %q", result.Output)
	}
}

func TestSessionCommandHandlerCurrentAndNew(t *testing.T) {
	runtime := &fakeSessionRuntime{}
	admin := newFakeSessionAdmin(t, &fakeSessionAdminBackend{
		summary: tools.SessionSummary{
			SessionID:        "session-1",
			Channel:          "web",
			Key:              "default",
			MessageCount:     0,
			ActiveSkillCount: 1,
			UpdatedAt:        time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC),
		},
	})
	handler := NewSessionCommandHandler(runtime, admin)

	ctx := commands.WithSessionContext(context.Background(), commands.SessionContext{
		SessionID: "session-1",
		Channel:   "web",
		Key:       "default",
	})
	currentResult, err := handler(ctx, nil, commands.Command{Name: "session", Args: []string{"current"}})
	if err != nil {
		t.Fatalf("session current: %v", err)
	}
	if !strings.Contains(currentResult.Output, "session-1") || !strings.Contains(currentResult.Output, "web:default") {
		t.Fatalf("unexpected current output: %q", currentResult.Output)
	}

	newResult, err := handler(context.Background(), nil, commands.Command{Name: "session", Args: []string{"new", "local:test"}})
	if err != nil {
		t.Fatalf("session new: %v", err)
	}
	if !strings.Contains(newResult.Output, "local:test") {
		t.Fatalf("unexpected new output: %q", newResult.Output)
	}
}

func TestSessionCommandHandlerListFiltersByChannel(t *testing.T) {
	runtime := &fakeSessionRuntime{
		sessions: []backend.ListedSession{
			{SessionID: "web-1", Locator: backend.SessionLocator{Channel: "web", Key: "one"}},
			{SessionID: "local-1", Locator: backend.SessionLocator{Channel: "local", Key: "default"}},
		},
	}
	handler := NewSessionCommandHandler(runtime, newFakeSessionAdmin(t, &fakeSessionAdminBackend{}))

	result, err := handler(context.Background(), nil, commands.Command{Name: "session", Args: []string{"list", "web"}})
	if err != nil {
		t.Fatalf("session list: %v", err)
	}
	if !strings.Contains(result.Output, "web-1") || strings.Contains(result.Output, "local-1") {
		t.Fatalf("unexpected list output: %q", result.Output)
	}
}

func TestSessionCommandHandlerContextTokensAndAuth(t *testing.T) {
	adminBackend := &fakeSessionAdminBackend{
		context: tools.ContextInspection{
			SessionID:              "session-1",
			MessageCount:           7,
			TokenEstimate:          1536,
			CompressThreshold:      12000,
			SuggestCompact:         false,
			ActiveSkillCount:       2,
			PendingPermissionCount: 1,
		},
	}
	handler := NewSessionCommandHandler(&fakeSessionRuntime{}, newFakeSessionAdmin(t, adminBackend))
	ctx := commands.WithSessionContext(context.Background(), commands.SessionContext{
		SessionID: "session-1",
		Channel:   "weixin",
		Key:       "chat-1",
		UserID:    "user-1",
		Metadata: map[string]string{
			"account_id":   "wx-account",
			"from_user_id": "user-1",
		},
	})

	contextResult, err := handler(ctx, nil, commands.Command{Name: "session", Args: []string{"context"}})
	if err != nil {
		t.Fatalf("session context: %v", err)
	}
	if !strings.Contains(contextResult.Output, "Estimated tokens: 1536") {
		t.Fatalf("unexpected context output: %q", contextResult.Output)
	}

	tokensResult, err := handler(ctx, nil, commands.Command{Name: "session", Args: []string{"tokens"}})
	if err != nil {
		t.Fatalf("session tokens: %v", err)
	}
	if !strings.Contains(tokensResult.Output, "tok_****_mask") || strings.Contains(tokensResult.Output, "tok-revealed") {
		t.Fatalf("unexpected masked tokens output: %q", tokensResult.Output)
	}

	revealResult, err := handler(ctx, nil, commands.Command{Name: "session", Args: []string{"tokens", "reveal"}})
	if err != nil {
		t.Fatalf("session tokens reveal: %v", err)
	}
	if !strings.Contains(revealResult.Output, "tok-revealed") {
		t.Fatalf("unexpected revealed tokens output: %q", revealResult.Output)
	}

	authResult, err := handler(ctx, nil, commands.Command{Name: "session", Args: []string{"auth", "status"}})
	if err != nil {
		t.Fatalf("session auth status: %v", err)
	}
	if !strings.Contains(authResult.Output, "Channel: weixin") || !strings.Contains(authResult.Output, "Scan QR code") {
		t.Fatalf("unexpected auth output: %q", authResult.Output)
	}
}

func TestClearApproveAndDenyHandlers(t *testing.T) {
	adminBackend := &fakeSessionAdminBackend{
		pending: []tools.PendingPermission{
			{
				ID: "perm-1",
				Request: tools.PermissionRequest{
					ToolName: "write_file",
					Action:   "write",
					Paths:    []string{"notes/todo.txt"},
				},
				Reason: "Needs approval",
			},
		},
	}
	admin := newFakeSessionAdmin(t, adminBackend)
	ctx := commands.WithSessionContext(context.Background(), commands.SessionContext{
		SessionID: "session-1",
		Channel:   "weixin",
		Key:       "chat-1",
	})

	clearResult, err := NewClearCommandHandler(admin)(ctx, nil, commands.Command{Name: "clear"})
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if !adminBackend.cleared || !strings.Contains(clearResult.Output, "clear_messages: cleared") {
		t.Fatalf("unexpected clear output: %q", clearResult.Output)
	}

	listResult, err := NewApproveCommandHandler(admin)(ctx, nil, commands.Command{Name: "approve", Args: []string{"list"}})
	if err != nil {
		t.Fatalf("approve list: %v", err)
	}
	if !strings.Contains(listResult.Output, "perm-1") {
		t.Fatalf("unexpected approve list output: %q", listResult.Output)
	}

	approveResult, err := NewApproveCommandHandler(admin)(ctx, nil, commands.Command{Name: "approve", Args: []string{"session"}})
	if err != nil {
		t.Fatalf("approve session: %v", err)
	}
	if adminBackend.lastApproveID != "perm-1" || adminBackend.lastApproveScope != tools.PermissionGrantSession || !strings.Contains(approveResult.Output, "Permission approved") {
		t.Fatalf("unexpected approve output: %q", approveResult.Output)
	}

	denyResult, err := NewDenyCommandHandler(admin)(ctx, nil, commands.Command{Name: "deny", Args: []string{"perm-1", "not", "safe"}})
	if err != nil {
		t.Fatalf("deny: %v", err)
	}
	if adminBackend.lastDenyID != "perm-1" || adminBackend.lastDenyReason != "not safe" || !strings.Contains(denyResult.Output, "Permission denied") {
		t.Fatalf("unexpected deny output: %q", denyResult.Output)
	}
}

func TestApproveHandlerApprovesSinglePendingWithoutRequestID(t *testing.T) {
	adminBackend := &fakeSessionAdminBackend{
		pending: []tools.PendingPermission{{
			ID:      "perm-1",
			Request: tools.PermissionRequest{ToolName: "bash", Action: "execute"},
		}},
	}
	admin := newFakeSessionAdmin(t, adminBackend)
	ctx := commands.WithSessionContext(context.Background(), commands.SessionContext{SessionID: "session-1"})

	result, err := NewApproveCommandHandler(admin)(ctx, nil, commands.Command{Name: "approve"})
	if err != nil {
		t.Fatalf("approve shortcut: %v", err)
	}
	if adminBackend.lastApproveID != "perm-1" || adminBackend.lastApproveScope != tools.PermissionGrantOnce {
		t.Fatalf("expected shortcut to approve only pending request once, id=%q scope=%q output=%q", adminBackend.lastApproveID, adminBackend.lastApproveScope, result.Output)
	}
	if !strings.Contains(result.Output, "Permission approved") {
		t.Fatalf("unexpected approve shortcut output: %q", result.Output)
	}
}

func TestApproveHandlerAcceptsRicherApprovalScopes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		scope tools.PermissionGrantScope
	}{
		{name: "pattern implicit id", args: []string{"pattern"}, scope: tools.PermissionGrantPattern},
		{name: "count implicit id", args: []string{"count:5"}, scope: tools.PermissionGrantScope("count:5")},
		{name: "timebox implicit id", args: []string{"timebox:10m"}, scope: tools.PermissionGrantScope("timebox:10m")},
		{name: "pattern explicit id", args: []string{"perm-1", "pattern"}, scope: tools.PermissionGrantPattern},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adminBackend := &fakeSessionAdminBackend{
				pending: []tools.PendingPermission{{
					ID:      "perm-1",
					Request: tools.PermissionRequest{ToolName: "bash", Action: "execute"},
				}},
			}
			admin := newFakeSessionAdmin(t, adminBackend)
			ctx := commands.WithSessionContext(context.Background(), commands.SessionContext{SessionID: "session-1"})

			result, err := NewApproveCommandHandler(admin)(ctx, nil, commands.Command{Name: "approve", Args: tc.args})
			if err != nil {
				t.Fatalf("approve %v: %v", tc.args, err)
			}
			if adminBackend.lastApproveID != "perm-1" || adminBackend.lastApproveScope != tc.scope {
				t.Fatalf("unexpected approval id/scope: id=%q scope=%q output=%q", adminBackend.lastApproveID, adminBackend.lastApproveScope, result.Output)
			}
		})
	}
}

func TestApproveHandlerListsWhenMultiplePendingAndNoRequestID(t *testing.T) {
	adminBackend := &fakeSessionAdminBackend{
		pending: []tools.PendingPermission{
			{ID: "perm-1", Request: tools.PermissionRequest{ToolName: "bash"}},
			{ID: "perm-2", Request: tools.PermissionRequest{ToolName: "write_file"}},
		},
	}
	admin := newFakeSessionAdmin(t, adminBackend)
	ctx := commands.WithSessionContext(context.Background(), commands.SessionContext{SessionID: "session-1"})

	result, err := NewApproveCommandHandler(admin)(ctx, nil, commands.Command{Name: "approve"})
	if err != nil {
		t.Fatalf("approve list fallback: %v", err)
	}
	if adminBackend.lastApproveID != "" {
		t.Fatalf("did not expect approval when multiple requests are pending, got %q", adminBackend.lastApproveID)
	}
	if !strings.Contains(result.Output, "perm-1") || !strings.Contains(result.Output, "perm-2") {
		t.Fatalf("expected multiple pending requests in output, got %q", result.Output)
	}
}

func TestApproveHandlerIncludesResumedOutput(t *testing.T) {
	admin := newFakeSessionAdmin(t, &fakeSessionAdminBackend{
		approveResult: tools.PermissionResolution{
			RequestID:    "perm-1",
			Resumed:      true,
			ResumeStatus: "completed",
			ResumeOutput: "百度首页已打开，并已保存截图。",
			Request: tools.PermissionRequest{
				ToolName: "browser",
				Action:   "open",
			},
		},
	})
	ctx := commands.WithSessionContext(context.Background(), commands.SessionContext{
		SessionID: "session-1",
		Channel:   "weixin",
		Key:       "chat-1",
	})

	result, err := NewApproveCommandHandler(admin)(ctx, nil, commands.Command{Name: "approve", Args: []string{"perm-1"}})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if !strings.Contains(result.Output, "Resume: completed") || !strings.Contains(result.Output, "百度首页已打开") {
		t.Fatalf("unexpected resumed approve output: %q", result.Output)
	}
}
