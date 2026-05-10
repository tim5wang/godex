package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/domain/automation"
)

// ContextInspection describes the current session prompt budget and history shape
// without mutating the conversation state.
type ContextInspection struct {
	SessionID                     string                `json:"session_id,omitempty"`
	MessageCount                  int                   `json:"message_count"`
	TokenEstimate                 int                   `json:"token_estimate"`
	HistoryTokenEstimate          int                   `json:"history_token_estimate,omitempty"`
	TotalTokenEstimate            int                   `json:"total_token_estimate,omitempty"`
	TokenBreakdown                ContextTokenBreakdown `json:"token_breakdown,omitempty"`
	CompressThreshold             int                   `json:"compress_threshold"`
	SuggestCompact                bool                  `json:"suggest_compact"`
	CompressionReasons            []string              `json:"compression_reasons,omitempty"`
	ActiveSkillCount              int                   `json:"active_skill_count"`
	PendingPermissionCount        int                   `json:"pending_permission_count"`
	LargeToolResultReferenceCount int                   `json:"large_tool_result_reference_count,omitempty"`
	ToolResultReferences          []ToolResultReference `json:"tool_result_references,omitempty"`
}

// ContextTokenBreakdown describes prompt-budget pressure by source. The Total
// field is the value exposed through ContextInspection.TokenEstimate.
type ContextTokenBreakdown struct {
	System      int `json:"system"`
	History     int `json:"history"`
	Memory      int `json:"memory"`
	Runtime     int `json:"runtime"`
	ToolSchemas int `json:"tool_schemas"`
	Attachments int `json:"attachments"`
	ToolResults int `json:"tool_results"`
	Total       int `json:"total"`
}

// ToolResultReference summarizes a model-visible placeholder for a large tool
// result without reading the persisted artifact.
type ToolResultReference struct {
	ToolName     string `json:"tool_name,omitempty"`
	ToolUseID    string `json:"tool_use_id,omitempty"`
	Bytes        int    `json:"bytes,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
	ArtifactPath string `json:"artifact_path,omitempty"`
}

// SessionSummary is the lightweight overview of the current runtime session.
type SessionSummary struct {
	SessionID              string    `json:"session_id"`
	Channel                string    `json:"channel,omitempty"`
	Key                    string    `json:"key,omitempty"`
	UserID                 string    `json:"user_id,omitempty"`
	MessageCount           int       `json:"message_count"`
	ActiveSkillCount       int       `json:"active_skill_count"`
	PendingPermissionCount int       `json:"pending_permission_count"`
	Running                bool      `json:"running,omitempty"`
	UpdatedAt              time.Time `json:"updated_at,omitempty"`
}

// SessionActionResult describes one session-management mutation.
type SessionActionResult struct {
	SessionID       string `json:"session_id,omitempty"`
	Action          string `json:"action"`
	Status          string `json:"status"`
	Message         string `json:"message,omitempty"`
	ClearedMessages int    `json:"cleared_messages,omitempty"`
}

// SessionTokenView describes channel token/context state for the current session.
type SessionTokenView struct {
	SessionID   string    `json:"session_id,omitempty"`
	Channel     string    `json:"channel,omitempty"`
	Supported   bool      `json:"supported"`
	Reveal      bool      `json:"reveal,omitempty"`
	AccountID   string    `json:"account_id,omitempty"`
	UserID      string    `json:"user_id,omitempty"`
	TokenCount  int       `json:"token_count,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
	TokenMasked string    `json:"token_masked,omitempty"`
	Token       string    `json:"token,omitempty"`
	Message     string    `json:"message,omitempty"`
}

// SessionChannelAuth describes the channel-specific auth state for the current session.
type SessionChannelAuth struct {
	SessionID    string `json:"session_id,omitempty"`
	Channel      string `json:"channel,omitempty"`
	Supported    bool   `json:"supported"`
	Action       string `json:"action,omitempty"`
	AccountID    string `json:"account_id,omitempty"`
	Enabled      bool   `json:"enabled,omitempty"`
	Configured   bool   `json:"configured,omitempty"`
	State        string `json:"state,omitempty"`
	Message      string `json:"message,omitempty"`
	LoginActive  bool   `json:"login_active,omitempty"`
	LoginState   string `json:"login_state,omitempty"`
	LoginMessage string `json:"login_message,omitempty"`
	QRCode       string `json:"qr_code,omitempty"`
	QRCodeImgURL string `json:"qr_code_img_url,omitempty"`
}

// SessionAdminCurrent is the current-session runtime surface needed by session management.
type SessionAdminCurrent interface {
	GetMessages() []protocol.Message
	ActiveSkillNames() []string
	PendingPermissions(sessionID string) []PendingPermission
	ApprovePendingPermission(sessionID, requestID string, scope PermissionGrantScope) (PermissionResolution, error)
	DenyPendingPermission(sessionID, requestID, reason string) (PermissionResolution, error)
	ClearMessages()
	InspectContext(context.Context, string) (ContextInspection, error)
}

// SessionAdminRuntime exposes current-session inspection and management actions.
type SessionAdminRuntime interface {
	CurrentSession(context.Context, string, automation.SessionContext) (SessionSummary, error)
	ContextSummary(context.Context, string, automation.SessionContext) (ContextInspection, error)
	ClearMessages(context.Context, string, automation.SessionContext) (SessionActionResult, error)
	ListPendingPermissions(context.Context, string, automation.SessionContext) ([]PendingPermission, error)
	ApprovePermission(context.Context, string, automation.SessionContext, string, PermissionGrantScope) (PermissionResolution, error)
	DenyPermission(context.Context, string, automation.SessionContext, string, string) (PermissionResolution, error)
	TokenSummary(context.Context, string, automation.SessionContext, bool) (SessionTokenView, error)
	ChannelAuth(context.Context, string, automation.SessionContext, string) (SessionChannelAuth, error)
}

type manageSessionArgs struct {
	Action    string `json:"action"`
	RequestID string `json:"request_id,omitempty"`
	Scope     string `json:"scope,omitempty"`
	Reveal    bool   `json:"reveal,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// NewManageSessionTool creates a session-management tool for natural-language workflows.
func NewManageSessionTool(runtime SessionAdminRuntime) Tool {
	return NewTypedTool(NewToolSpec("manage_session", "Inspect or manage the current session: view context budget, inspect tokens, clear messages, review pending permissions, or manage channel auth.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type": "string",
				"enum": []string{
					"inspect",
					"inspect_context",
					"inspect_tokens",
					"clear_messages",
					"list_permissions",
					"approve_permission",
					"deny_permission",
					"auth_status",
					"auth_login",
					"auth_logout",
				},
			},
			"request_id": map[string]string{"type": "string"},
			"scope": map[string]interface{}{
				"type": "string",
				"enum": []string{string(PermissionGrantOnce), string(PermissionGrantSession)},
			},
			"reveal": map[string]string{"type": "boolean"},
			"reason": map[string]string{"type": "string"},
		},
		"required": []string{"action"},
	}, nil), func(ctx context.Context, args manageSessionArgs) (ToolResult, error) {
		if runtime == nil {
			return ToolResult{}, fmt.Errorf("session management runtime unavailable")
		}
		sessionID := strings.TrimSpace(SessionIDFromContext(ctx))
		runtimeCtx := SessionContextFromContext(ctx)
		if sessionID == "" {
			sessionID = strings.TrimSpace(runtimeCtx.SessionID)
		}
		if sessionID == "" {
			return ToolResult{}, fmt.Errorf("missing current session context")
		}

		action := strings.ToLower(strings.TrimSpace(args.Action))
		switch action {
		case "inspect":
			summary, err := runtime.CurrentSession(ctx, sessionID, runtimeCtx)
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: summary}, nil
		case "inspect_context":
			summary, err := runtime.ContextSummary(ctx, sessionID, runtimeCtx)
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: summary}, nil
		case "inspect_tokens":
			summary, err := runtime.TokenSummary(ctx, sessionID, runtimeCtx, args.Reveal)
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: summary}, nil
		case "clear_messages":
			result, err := runtime.ClearMessages(ctx, sessionID, runtimeCtx)
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: result}, nil
		case "list_permissions":
			items, err := runtime.ListPendingPermissions(ctx, sessionID, runtimeCtx)
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: map[string]interface{}{"pending_permissions": items}}, nil
		case "approve_permission":
			requestID := strings.TrimSpace(args.RequestID)
			if requestID == "" {
				return ToolResult{}, fmt.Errorf("missing request_id for approve_permission")
			}
			scope := PermissionGrantOnce
			if strings.EqualFold(strings.TrimSpace(args.Scope), string(PermissionGrantSession)) {
				scope = PermissionGrantSession
			}
			result, err := runtime.ApprovePermission(ctx, sessionID, runtimeCtx, requestID, scope)
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: result}, nil
		case "deny_permission":
			requestID := strings.TrimSpace(args.RequestID)
			if requestID == "" {
				return ToolResult{}, fmt.Errorf("missing request_id for deny_permission")
			}
			result, err := runtime.DenyPermission(ctx, sessionID, runtimeCtx, requestID, strings.TrimSpace(args.Reason))
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: result}, nil
		case "auth_status":
			status, err := runtime.ChannelAuth(ctx, sessionID, runtimeCtx, "status")
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: status}, nil
		case "auth_login":
			status, err := runtime.ChannelAuth(ctx, sessionID, runtimeCtx, "login")
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: status}, nil
		case "auth_logout":
			status, err := runtime.ChannelAuth(ctx, sessionID, runtimeCtx, "logout")
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: status}, nil
		default:
			return ToolResult{}, fmt.Errorf("unknown manage_session action %q", args.Action)
		}
	})
}
