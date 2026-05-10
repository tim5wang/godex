package sessionadmin

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/tools"
)

type backendRuntime interface {
	SessionSummary(context.Context, string) (tools.SessionSummary, error)
	PendingPermissions(context.Context, string) ([]tools.PendingPermission, error)
	ApprovePermission(context.Context, string, string, tools.PermissionGrantScope) (tools.PermissionResolution, error)
	DenyPermission(context.Context, string, string, string) (tools.PermissionResolution, error)
	ClearMessages(context.Context, string) error
	ContextSummary(context.Context, string) (tools.ContextInspection, error)
}

type WeixinAuthProvider interface {
	Status(context.Context, string) (WeixinAuthStatus, error)
	Start(context.Context, string) (WeixinAuthStatus, error)
	Logout(context.Context, string) (WeixinAuthStatus, error)
}

type InspectContextTokensFunc func(stateDir, accountID, userID string, reveal bool) (WeixinContextTokenInspection, error)

type WeixinAuthStatus struct {
	AccountID  string
	Enabled    bool
	Configured bool
	Login      *WeixinAuthLoginStatus
}

type WeixinAuthLoginStatus struct {
	Active       bool
	State        string
	Message      string
	QRCode       string
	QRCodeImgURL string
}

type WeixinContextTokenInspection struct {
	AccountID   string
	UserID      string
	TokenCount  int
	UpdatedAt   time.Time
	TokenMasked string
	Token       string
}

// Service coordinates current-session inspection and management across commands and tools.
type Service struct {
	cfgProvider func() *config.Config
	backend     backendRuntime
	weixinAuth  WeixinAuthProvider
	inspectFunc InspectContextTokensFunc
}

type boundRuntime struct {
	service *Service
	current tools.SessionAdminCurrent
}

// NewService creates a session-admin service that can be bound to one current session runtime.
func NewService(cfgProvider func() *config.Config, backend backendRuntime, weixinAuth WeixinAuthProvider, inspect InspectContextTokensFunc) *Service {
	return &Service{
		cfgProvider: cfgProvider,
		backend:     backend,
		weixinAuth:  weixinAuth,
		inspectFunc: inspect,
	}
}

// Bind returns a current-session runtime view backed by the shared service.
func (s *Service) Bind(current tools.SessionAdminCurrent) tools.SessionAdminRuntime {
	return &boundRuntime{service: s, current: current}
}

func (r *boundRuntime) CurrentSession(ctx context.Context, sessionID string, runtimeCtx automation.SessionContext) (tools.SessionSummary, error) {
	return r.service.currentSession(ctx, r.current, sessionID, runtimeCtx)
}

func (r *boundRuntime) ContextSummary(ctx context.Context, sessionID string, runtimeCtx automation.SessionContext) (tools.ContextInspection, error) {
	return r.service.contextSummary(ctx, r.current, sessionID, runtimeCtx)
}

func (r *boundRuntime) ClearMessages(ctx context.Context, sessionID string, runtimeCtx automation.SessionContext) (tools.SessionActionResult, error) {
	return r.service.clearMessages(ctx, r.current, sessionID, runtimeCtx)
}

func (r *boundRuntime) ListPendingPermissions(ctx context.Context, sessionID string, runtimeCtx automation.SessionContext) ([]tools.PendingPermission, error) {
	return r.service.listPendingPermissions(ctx, r.current, sessionID, runtimeCtx)
}

func (r *boundRuntime) ApprovePermission(ctx context.Context, sessionID string, runtimeCtx automation.SessionContext, requestID string, scope tools.PermissionGrantScope) (tools.PermissionResolution, error) {
	return r.service.approvePermission(ctx, r.current, sessionID, runtimeCtx, requestID, scope)
}

func (r *boundRuntime) DenyPermission(ctx context.Context, sessionID string, runtimeCtx automation.SessionContext, requestID, reason string) (tools.PermissionResolution, error) {
	return r.service.denyPermission(ctx, r.current, sessionID, runtimeCtx, requestID, reason)
}

func (r *boundRuntime) TokenSummary(ctx context.Context, sessionID string, runtimeCtx automation.SessionContext, reveal bool) (tools.SessionTokenView, error) {
	return r.service.tokenSummary(ctx, r.current, sessionID, runtimeCtx, reveal)
}

func (r *boundRuntime) ChannelAuth(ctx context.Context, sessionID string, runtimeCtx automation.SessionContext, action string) (tools.SessionChannelAuth, error) {
	return r.service.channelAuth(ctx, r.current, sessionID, runtimeCtx, action)
}

func (s *Service) currentSession(ctx context.Context, current tools.SessionAdminCurrent, sessionID string, runtimeCtx automation.SessionContext) (tools.SessionSummary, error) {
	summary := tools.SessionSummary{
		SessionID: sessionID,
		Channel:   strings.TrimSpace(runtimeCtx.LocatorChannel),
		Key:       strings.TrimSpace(runtimeCtx.LocatorKey),
		UserID:    firstNonEmpty(strings.TrimSpace(runtimeCtx.LocatorUserID), strings.TrimSpace(runtimeCtx.Sender), strings.TrimSpace(runtimeCtx.Metadata["from_user_id"])),
	}
	if s.backend != nil && strings.TrimSpace(sessionID) != "" {
		snapshot, err := s.backend.SessionSummary(ctx, sessionID)
		if err == nil {
			summary.Channel = firstNonEmpty(snapshot.Channel, summary.Channel)
			summary.Key = firstNonEmpty(snapshot.Key, summary.Key)
			summary.UserID = firstNonEmpty(snapshot.UserID, summary.UserID)
			summary.MessageCount = snapshot.MessageCount
			summary.ActiveSkillCount = snapshot.ActiveSkillCount
			summary.PendingPermissionCount = snapshot.PendingPermissionCount
			summary.Running = snapshot.Running
			summary.UpdatedAt = snapshot.UpdatedAt
			return summary, nil
		}
	}
	if current == nil {
		return summary, fmt.Errorf("session runtime unavailable")
	}
	summary.MessageCount = len(current.GetMessages())
	summary.ActiveSkillCount = len(current.ActiveSkillNames())
	summary.PendingPermissionCount = len(current.PendingPermissions(sessionID))
	return summary, nil
}

func (s *Service) contextSummary(ctx context.Context, current tools.SessionAdminCurrent, sessionID string, _ automation.SessionContext) (tools.ContextInspection, error) {
	if current != nil {
		return current.InspectContext(ctx, sessionID)
	}
	if s.backend == nil {
		return tools.ContextInspection{}, fmt.Errorf("session runtime unavailable")
	}
	return s.backend.ContextSummary(ctx, sessionID)
}

func (s *Service) clearMessages(ctx context.Context, current tools.SessionAdminCurrent, sessionID string, _ automation.SessionContext) (tools.SessionActionResult, error) {
	cleared := 0
	if current != nil {
		cleared = len(current.GetMessages())
		current.ClearMessages()
		return tools.SessionActionResult{
			SessionID:       sessionID,
			Action:          "clear_messages",
			Status:          "cleared",
			Message:         "Cleared current session messages and reset transient context.",
			ClearedMessages: cleared,
		}, nil
	}
	if s.backend == nil {
		return tools.SessionActionResult{}, fmt.Errorf("session runtime unavailable")
	}
	snapshot, err := s.backend.SessionSummary(ctx, sessionID)
	if err == nil {
		cleared = snapshot.MessageCount
	}
	if err := s.backend.ClearMessages(ctx, sessionID); err != nil {
		return tools.SessionActionResult{}, err
	}
	return tools.SessionActionResult{
		SessionID:       sessionID,
		Action:          "clear_messages",
		Status:          "cleared",
		Message:         "Cleared current session messages and reset transient context.",
		ClearedMessages: cleared,
	}, nil
}

func (s *Service) listPendingPermissions(ctx context.Context, current tools.SessionAdminCurrent, sessionID string, _ automation.SessionContext) ([]tools.PendingPermission, error) {
	if current != nil {
		return current.PendingPermissions(sessionID), nil
	}
	if s.backend == nil {
		return nil, fmt.Errorf("session runtime unavailable")
	}
	return s.backend.PendingPermissions(ctx, sessionID)
}

func (s *Service) approvePermission(ctx context.Context, current tools.SessionAdminCurrent, sessionID string, _ automation.SessionContext, requestID string, scope tools.PermissionGrantScope) (tools.PermissionResolution, error) {
	if strings.TrimSpace(requestID) == "" {
		return tools.PermissionResolution{}, fmt.Errorf("missing request id")
	}
	if s.backend != nil && strings.TrimSpace(sessionID) != "" {
		return s.backend.ApprovePermission(ctx, sessionID, requestID, scope)
	}
	if current != nil {
		return current.ApprovePendingPermission(sessionID, requestID, scope)
	}
	if s.backend == nil {
		return tools.PermissionResolution{}, fmt.Errorf("session runtime unavailable")
	}
	return s.backend.ApprovePermission(ctx, sessionID, requestID, scope)
}

func (s *Service) denyPermission(ctx context.Context, current tools.SessionAdminCurrent, sessionID string, _ automation.SessionContext, requestID, reason string) (tools.PermissionResolution, error) {
	if strings.TrimSpace(requestID) == "" {
		return tools.PermissionResolution{}, fmt.Errorf("missing request id")
	}
	if s.backend != nil && strings.TrimSpace(sessionID) != "" {
		return s.backend.DenyPermission(ctx, sessionID, requestID, reason)
	}
	if current != nil {
		return current.DenyPendingPermission(sessionID, requestID, reason)
	}
	if s.backend == nil {
		return tools.PermissionResolution{}, fmt.Errorf("session runtime unavailable")
	}
	return s.backend.DenyPermission(ctx, sessionID, requestID, reason)
}

func (s *Service) tokenSummary(_ context.Context, _ tools.SessionAdminCurrent, sessionID string, runtimeCtx automation.SessionContext, reveal bool) (tools.SessionTokenView, error) {
	channel := strings.TrimSpace(runtimeCtx.LocatorChannel)
	view := tools.SessionTokenView{
		SessionID: sessionID,
		Channel:   channel,
		Supported: false,
		Reveal:    reveal,
	}
	if !strings.EqualFold(channel, "weixin") {
		view.Message = "Current channel does not expose revealable context tokens."
		return view, nil
	}
	cfg := s.currentConfig()
	accountID := firstNonEmpty(strings.TrimSpace(runtimeCtx.Metadata["account_id"]), strings.TrimSpace(runtimeCtx.Metadata["accountID"]))
	if cfg != nil && accountID == "" {
		accountID = strings.TrimSpace(cfg.Weixin.AccountID)
	}
	userID := firstNonEmpty(strings.TrimSpace(runtimeCtx.Metadata["from_user_id"]), strings.TrimSpace(runtimeCtx.LocatorUserID), strings.TrimSpace(runtimeCtx.Sender))
	if s.inspectFunc == nil {
		view.Message = "Weixin token inspection is unavailable."
		return view, nil
	}
	inspection, err := s.inspectFunc(cfg.StateDir, accountID, userID, reveal)
	if err != nil {
		return view, err
	}
	view.Supported = true
	view.AccountID = inspection.AccountID
	view.UserID = inspection.UserID
	view.TokenCount = inspection.TokenCount
	view.UpdatedAt = inspection.UpdatedAt
	view.TokenMasked = inspection.TokenMasked
	view.Token = inspection.Token
	if view.TokenMasked == "" {
		fallback := strings.TrimSpace(runtimeCtx.Metadata["context_token"])
		if fallback != "" {
			view.TokenMasked = maskToken(fallback)
			if reveal {
				view.Token = fallback
			}
			if view.TokenCount == 0 {
				view.TokenCount = 1
			}
		}
	}
	if view.TokenMasked == "" {
		view.Message = "No current Weixin context token is available for this session."
	} else if reveal {
		view.Message = "Revealed the current Weixin sender context token."
	} else {
		view.Message = "Showing the masked current Weixin sender context token."
	}
	return view, nil
}

func (s *Service) channelAuth(ctx context.Context, _ tools.SessionAdminCurrent, sessionID string, runtimeCtx automation.SessionContext, action string) (tools.SessionChannelAuth, error) {
	channel := strings.TrimSpace(runtimeCtx.LocatorChannel)
	view := tools.SessionChannelAuth{
		SessionID: sessionID,
		Channel:   channel,
		Action:    strings.ToLower(strings.TrimSpace(action)),
	}
	if !strings.EqualFold(channel, "weixin") {
		view.Message = "Current channel does not support interactive auth management."
		return view, nil
	}
	if s.weixinAuth == nil {
		view.Message = "Weixin auth runtime is unavailable."
		return view, nil
	}
	accountID := firstNonEmpty(strings.TrimSpace(runtimeCtx.Metadata["account_id"]), s.currentConfig().Weixin.AccountID)
	var (
		status WeixinAuthStatus
		err    error
	)
	switch view.Action {
	case "", "status":
		view.Action = "status"
		status, err = s.weixinAuth.Status(ctx, accountID)
	case "login":
		status, err = s.weixinAuth.Start(ctx, accountID)
	case "logout":
		status, err = s.weixinAuth.Logout(ctx, accountID)
	default:
		return view, fmt.Errorf("unknown channel auth action %q", action)
	}
	if err != nil {
		return view, err
	}
	return mapWeixinAuthStatus(sessionID, view.Action, status), nil
}

func mapWeixinAuthStatus(sessionID, action string, status WeixinAuthStatus) tools.SessionChannelAuth {
	view := tools.SessionChannelAuth{
		SessionID:  sessionID,
		Channel:    "weixin",
		Supported:  true,
		Action:     action,
		AccountID:  status.AccountID,
		Enabled:    status.Enabled,
		Configured: status.Configured,
	}
	switch {
	case status.Login != nil:
		view.State = strings.TrimSpace(status.Login.State)
		view.Message = strings.TrimSpace(status.Login.Message)
		view.LoginActive = status.Login.Active
		view.LoginState = strings.TrimSpace(status.Login.State)
		view.LoginMessage = strings.TrimSpace(status.Login.Message)
		view.QRCode = strings.TrimSpace(status.Login.QRCode)
		view.QRCodeImgURL = strings.TrimSpace(status.Login.QRCodeImgURL)
	case status.Configured:
		view.State = "configured"
		view.Message = "Weixin account is configured."
	default:
		view.State = "unconfigured"
		view.Message = "Weixin account is not configured."
	}
	if view.Message == "" {
		if view.State == "" {
			view.State = "unknown"
		}
		view.Message = "Weixin auth state checked."
	}
	return view
}

func (s *Service) currentConfig() *config.Config {
	if s == nil || s.cfgProvider == nil {
		return &config.Config{}
	}
	cfg := s.cfgProvider()
	if cfg == nil {
		return &config.Config{}
	}
	return cfg
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func maskToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) <= 8 {
		return strings.Repeat("*", len(value))
	}
	return value[:4] + strings.Repeat("*", len(value)-8) + value[len(value)-4:]
}
