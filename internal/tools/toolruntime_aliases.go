package tools

import (
	"context"
	"time"

	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/platform/tooling"
	"github.com/tim5wang/godex/internal/toolruntime"
)

type ToolMeta = toolruntime.ToolMeta
type BundleCatalogItem = toolruntime.BundleCatalogItem
type ToolCatalog = toolruntime.ToolCatalog
type ToolHandler = toolruntime.ToolHandler

type ToolSpec = toolruntime.ToolSpec
type ToolCall = toolruntime.ToolCall
type ToolResult = toolruntime.ToolResult
type BeforeInterceptor = toolruntime.BeforeInterceptor
type AfterInterceptor = toolruntime.AfterInterceptor
type Tool = toolruntime.Tool

type PermissionDecision = toolruntime.PermissionDecision
type PermissionStatus = toolruntime.PermissionStatus
type PermissionGrantScope = toolruntime.PermissionGrantScope
type PermissionPolicy = toolruntime.PermissionPolicy
type InteractiveApprovalPolicy = toolruntime.InteractiveApprovalPolicy
type PermissionRequest = toolruntime.PermissionRequest
type PendingPermission = toolruntime.PendingPermission
type PermissionOverrideState = toolruntime.PermissionOverrideState
type PermissionSessionState = toolruntime.PermissionSessionState
type PermissionResolution = toolruntime.PermissionResolution
type PermissionResult = toolruntime.PermissionResult
type PermissionRule = toolruntime.PermissionRule
type PermissionReviewer = toolruntime.PermissionReviewer
type PermissionRuleFunc = toolruntime.PermissionRuleFunc
type PermissionManager = toolruntime.PermissionManager

type ErrToolNotFound = toolruntime.ErrToolNotFound
type ErrToolInactive = toolruntime.ErrToolInactive
type ErrPermissionDenied = toolruntime.ErrPermissionDenied
type ErrPermissionPending = toolruntime.ErrPermissionPending

const (
	PermissionAbstain = toolruntime.PermissionAbstain
	PermissionAllow   = toolruntime.PermissionAllow
	PermissionDeny    = toolruntime.PermissionDeny
	PermissionPending = toolruntime.PermissionPending

	PermissionStatusPending  = toolruntime.PermissionStatusPending
	PermissionStatusApproved = toolruntime.PermissionStatusApproved
	PermissionStatusDenied   = toolruntime.PermissionStatusDenied
	PermissionStatusExpired  = toolruntime.PermissionStatusExpired
	PermissionStatusResumed  = toolruntime.PermissionStatusResumed

	PermissionGrantOnce    = toolruntime.PermissionGrantOnce
	PermissionGrantTask    = toolruntime.PermissionGrantTask
	PermissionGrantSession = toolruntime.PermissionGrantSession
	PermissionGrantPattern = toolruntime.PermissionGrantPattern

	InteractiveApprovalModeManual = toolruntime.InteractiveApprovalModeManual
	InteractiveApprovalModeReview = toolruntime.InteractiveApprovalModeReview
	InteractiveApprovalModeYOLO   = toolruntime.InteractiveApprovalModeYOLO

	SecurityProfileTrustedLocal   = toolruntime.SecurityProfileTrustedLocal
	SecurityProfileGuardedLocal   = toolruntime.SecurityProfileGuardedLocal
	SecurityProfileSandboxed      = toolruntime.SecurityProfileSandboxed
	SecurityProfileStrict         = toolruntime.SecurityProfileStrict
	SecurityProfileHostPrivileged = toolruntime.SecurityProfileHostPrivileged
	SecurityProfileDevRepair      = toolruntime.SecurityProfileDevRepair
)

func NewToolHandler() *ToolHandler {
	return toolruntime.NewToolHandler()
}

func NewTypedTool[T any](spec ToolSpec, run func(context.Context, T) (ToolResult, error)) Tool {
	return toolruntime.NewTypedTool(spec, run)
}

func SpecFromDefinition(def tooling.Definition, aliases map[string]string) ToolSpec {
	return toolruntime.SpecFromDefinition(def, aliases)
}

func NewToolSpec(name, description string, inputSchema map[string]interface{}, aliases map[string]string) ToolSpec {
	return toolruntime.NewToolSpec(name, description, inputSchema, aliases)
}

func WithSessionID(ctx context.Context, sessionID string) context.Context {
	return toolruntime.WithSessionID(ctx, sessionID)
}

func SessionIDFromContext(ctx context.Context) string {
	return toolruntime.SessionIDFromContext(ctx)
}

func WithSandboxID(ctx context.Context, sandboxID string) context.Context {
	return toolruntime.WithSandboxID(ctx, sandboxID)
}

func SandboxIDFromContext(ctx context.Context) string {
	return toolruntime.SandboxIDFromContext(ctx)
}

func WithSessionContext(ctx context.Context, runtimeContext automation.SessionContext) context.Context {
	return toolruntime.WithSessionContext(ctx, runtimeContext)
}

func SessionContextFromContext(ctx context.Context) automation.SessionContext {
	return toolruntime.SessionContextFromContext(ctx)
}

func NewPermissionManager(rules ...PermissionRule) *PermissionManager {
	return toolruntime.NewPermissionManager(rules...)
}

func NewPermissionManagerForPolicy(policy PermissionPolicy) *PermissionManager {
	return toolruntime.NewPermissionManagerForPolicy(policy)
}

func NewDefaultPermissionManager() *PermissionManager {
	return toolruntime.NewDefaultPermissionManager()
}

func DefaultPermissionPolicy() PermissionPolicy {
	return toolruntime.DefaultPermissionPolicy()
}

func PermissionPolicyForSecurityProfile(profile, approvalMode string) PermissionPolicy {
	return toolruntime.PermissionPolicyForSecurityProfile(profile, approvalMode)
}

func PermissionIntentSummary(pending PendingPermission) string {
	return toolruntime.PermissionIntentSummary(pending)
}

func PermissionRiskSummary(req PermissionRequest) string {
	return toolruntime.PermissionRiskSummary(req)
}

func PermissionExpirySummary(pending PendingPermission, now time.Time) string {
	return toolruntime.PermissionExpirySummary(pending, now)
}

func NewPermissionInterceptor(manager *PermissionManager) BeforeInterceptor {
	return toolruntime.NewPermissionInterceptor(manager)
}

func NewPermissionInterceptorWithReview(manager *PermissionManager, reviewer PermissionReviewer) BeforeInterceptor {
	return toolruntime.NewPermissionInterceptorWithReview(manager, reviewer)
}

func PermissionRequestFromCall(call ToolCall) PermissionRequest {
	return toolruntime.PermissionRequestFromCall(call)
}

func NewAutomationMutationRule() PermissionRule {
	return toolruntime.NewAutomationMutationRule()
}

func NewInteractiveApprovalRule() PermissionRule {
	return toolruntime.NewInteractiveApprovalRule()
}
