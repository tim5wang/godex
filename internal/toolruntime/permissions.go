package toolruntime

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/platform/tooling"
)

// PermissionDecision represents the outcome of one permission check.
type PermissionDecision string

const (
	PermissionAbstain PermissionDecision = "abstain"
	PermissionAllow   PermissionDecision = "allow"
	PermissionDeny    PermissionDecision = "deny"
	PermissionPending PermissionDecision = "pending"
)

// PermissionGrantScope controls how long an approval should remain effective.
type PermissionGrantScope string

const (
	PermissionGrantOnce    PermissionGrantScope = "once"
	PermissionGrantSession PermissionGrantScope = "session"
	PermissionGrantPattern PermissionGrantScope = "pattern"
)

const (
	InteractiveApprovalModeManual = "manual"
	InteractiveApprovalModeReview = "review"
	InteractiveApprovalModeYOLO   = "yolo"
)

const (
	SecurityProfileTrustedLocal   = "trusted-local"
	SecurityProfileGuardedLocal   = "guarded-local"
	SecurityProfileSandboxed      = "sandboxed"
	SecurityProfileStrict         = "strict"
	SecurityProfileHostPrivileged = "host-privileged"
	SecurityProfileDevRepair      = "dev/repair"
)

var defaultTrustedCommandPrefixes = []string{
	"cat ",
	"curl ",
	"date",
	"diff ",
	"echo ",
	"find ",
	"git diff",
	"git log",
	"git show",
	"git status",
	"grep ",
	"head ",
	"jq ",
	"ls",
	"pwd",
	"rg ",
	"sed -n",
	"tail ",
	"uname",
	"wc ",
	"wget ",
	"whoami",
}

// PermissionPolicy controls workspace-wide permission behavior.
type PermissionPolicy struct {
	BlockAutomationMutations bool                      `json:"block_automation_mutations"`
	InteractiveApproval      InteractiveApprovalPolicy `json:"interactive_approval"`
	SecurityProfile          string                    `json:"security_profile,omitempty"`
}

// InteractiveApprovalPolicy configures which remote tool calls require approval.
type InteractiveApprovalPolicy struct {
	Mode                   string   `json:"mode,omitempty"`
	Enabled                bool     `json:"enabled"`
	Sources                []string `json:"sources,omitempty"`
	Tools                  []string `json:"tools,omitempty"`
	TrustedPathPrefixes    []string `json:"trusted_path_prefixes,omitempty"`
	TrustedCommandPrefixes []string `json:"trusted_command_prefixes,omitempty"`
	PendingTTLSeconds      int      `json:"pending_ttl_seconds,omitempty"`
}

// PermissionRequest is the normalized security context for one tool call.
type PermissionRequest struct {
	SessionID string                 `json:"session_id,omitempty"`
	Source    string                 `json:"source,omitempty"`
	Sender    string                 `json:"sender,omitempty"`
	ToolName  string                 `json:"tool_name"`
	Action    string                 `json:"action,omitempty"`
	Paths     []string               `json:"paths,omitempty"`
	Command   string                 `json:"command,omitempty"`
	Mutation  bool                   `json:"mutation,omitempty"`
	Input     map[string]interface{} `json:"input,omitempty"`
}

// PendingPermission is one request waiting for explicit approval.
type PendingPermission struct {
	ID        string            `json:"id"`
	Request   PermissionRequest `json:"request"`
	Reason    string            `json:"reason,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	ExpiresAt time.Time         `json:"expires_at,omitempty"`
}

// PermissionOverrideState is one persisted session-scoped override.
type PermissionOverrideState struct {
	Request   PermissionRequest `json:"request"`
	Result    PermissionResult  `json:"result"`
	Remaining int               `json:"remaining"`
	ExpiresAt time.Time         `json:"expires_at,omitempty"`
}

// PermissionSessionState contains all persisted permission data for one session.
type PermissionSessionState struct {
	Overrides []PermissionOverrideState `json:"overrides,omitempty"`
	Pending   []PendingPermission       `json:"pending,omitempty"`
}

// PermissionResolution describes one completed approval action.
type PermissionResolution struct {
	RequestID              string               `json:"request_id"`
	Decision               PermissionDecision   `json:"decision"`
	Scope                  PermissionGrantScope `json:"scope,omitempty"`
	Reason                 string               `json:"reason,omitempty"`
	Request                PermissionRequest    `json:"request"`
	ResolvedAt             time.Time            `json:"resolved_at"`
	Resumed                bool                 `json:"resumed,omitempty"`
	ResumeTurnID           string               `json:"resume_turn_id,omitempty"`
	ResumeStatus           string               `json:"resume_status,omitempty"`
	ResumeOutput           string               `json:"resume_output,omitempty"`
	ResumePendingRequestID string               `json:"resume_pending_request_id,omitempty"`
	ResumeError            string               `json:"resume_error,omitempty"`
}

// PermissionResult is the evaluated policy decision.
type PermissionResult struct {
	Decision  PermissionDecision `json:"decision"`
	Reason    string             `json:"reason,omitempty"`
	Scope     string             `json:"scope,omitempty"`
	RequestID string             `json:"request_id,omitempty"`
}

// PermissionRule evaluates a request and optionally returns a policy decision.
type PermissionRule interface {
	Evaluate(PermissionRequest) (PermissionResult, bool)
}

// PermissionReviewer performs an automatic secondary review before deciding
// whether a protected tool call should be allowed or escalated to manual approval.
type PermissionReviewer func(context.Context, PermissionRequest) (PermissionResult, error)

// PermissionRuleFunc adapts a function into a permission rule.
type PermissionRuleFunc func(PermissionRequest) (PermissionResult, bool)

// Evaluate implements PermissionRule.
func (f PermissionRuleFunc) Evaluate(req PermissionRequest) (PermissionResult, bool) {
	return f(req)
}

type permissionOverride struct {
	request   PermissionRequest
	result    PermissionResult
	remaining int
	expiresAt time.Time
}

type parsedPermissionGrant struct {
	kind     PermissionGrantScope
	count    int
	duration time.Duration
}

// PermissionManager stores rules, pending requests, and session-scoped overrides.
type PermissionManager struct {
	mu         sync.RWMutex
	rules      []PermissionRule
	decisions  map[string]permissionOverride
	pending    map[string]PendingPermission
	pendingKey map[string]string
	pendingTTL time.Duration
	now        func() time.Time
}

// NewPermissionManager creates a permission manager with the provided rules.
func NewPermissionManager(rules ...PermissionRule) *PermissionManager {
	manager := &PermissionManager{
		decisions:  make(map[string]permissionOverride),
		pending:    make(map[string]PendingPermission),
		pendingKey: make(map[string]string),
		pendingTTL: 5 * time.Minute,
		now:        time.Now,
	}
	manager.AddRules(rules...)
	return manager
}

// NewPermissionManagerForPolicy creates a permission manager from one policy.
func NewPermissionManagerForPolicy(policy PermissionPolicy) *PermissionManager {
	manager := NewPermissionManager()
	manager.ApplyPolicy(policy)
	return manager
}

// NewDefaultPermissionManager creates the default workspace permission policy.
func NewDefaultPermissionManager() *PermissionManager {
	return NewPermissionManagerForPolicy(DefaultPermissionPolicy())
}

// DefaultPermissionPolicy returns the default workspace permission policy.
func DefaultPermissionPolicy() PermissionPolicy {
	return PermissionPolicy{
		BlockAutomationMutations: true,
		InteractiveApproval: InteractiveApprovalPolicy{
			Mode:              InteractiveApprovalModeManual,
			Enabled:           true,
			PendingTTLSeconds: 300,
			Sources: []string{
				string(message.SourceWeb),
				string(message.SourceGateway),
				string(message.SourceFeishu),
				string(message.SourceWeixin),
			},
			Tools: []string{
				"bash",
				"background_run",
				"write_file",
				"edit_file",
				"attach_file",
				"install_skill",
				"install_package",
				"remove_package",
				"tool_exchange",
				"cron",
				"heartbeat",
				"browser",
				"desktop",
			},
			TrustedPathPrefixes:    []string{},
			TrustedCommandPrefixes: append([]string{}, defaultTrustedCommandPrefixes...),
		},
	}
}

func PermissionPolicyForSecurityProfile(profile, approvalMode string) PermissionPolicy {
	policy := DefaultPermissionPolicy()
	policy.SecurityProfile = normalizeSecurityProfile(profile)
	policy.InteractiveApproval.Mode = normalizeInteractiveApprovalMode(approvalMode)
	switch policy.SecurityProfile {
	case SecurityProfileTrustedLocal:
		policy.InteractiveApproval.Sources = []string{
			string(message.SourceWeb),
			string(message.SourceGateway),
			string(message.SourceFeishu),
			string(message.SourceWeixin),
		}
	case SecurityProfileGuardedLocal:
		// Default posture: local CLI/TUI can work normally; remote sources still
		// require approval for protected tools.
	case SecurityProfileSandboxed:
		policy.InteractiveApproval.Mode = InteractiveApprovalModeManual
		policy.InteractiveApproval.TrustedCommandPrefixes = nil
		policy.InteractiveApproval.TrustedPathPrefixes = nil
	case SecurityProfileStrict:
		policy.InteractiveApproval.Mode = InteractiveApprovalModeManual
		policy.InteractiveApproval.TrustedCommandPrefixes = nil
		policy.InteractiveApproval.TrustedPathPrefixes = nil
	case SecurityProfileHostPrivileged:
		if policy.InteractiveApproval.Mode == InteractiveApprovalModeYOLO {
			policy.InteractiveApproval.Mode = InteractiveApprovalModeManual
		}
	case SecurityProfileDevRepair:
		policy.InteractiveApproval.Mode = InteractiveApprovalModeManual
		policy.InteractiveApproval.TrustedCommandPrefixes = nil
		policy.InteractiveApproval.TrustedPathPrefixes = nil
	}
	return policy
}

// ApplyPolicy replaces the active rule set while preserving pending approvals
// and session-scoped allow/deny overrides.
func (m *PermissionManager) ApplyPolicy(policy PermissionPolicy) {
	if m == nil {
		return
	}
	policy = normalizePermissionPolicy(policy)
	m.setPendingTTL(policy.InteractiveApproval.PendingTTLSeconds)
	m.ReplaceRules(permissionRulesForPolicy(policy)...)
}

func (m *PermissionManager) setPendingTTL(seconds int) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if seconds <= 0 {
		m.pendingTTL = 5 * time.Minute
		return
	}
	m.pendingTTL = time.Duration(seconds) * time.Second
}

// AddRules appends ordered permission rules.
func (m *PermissionManager) AddRules(rules ...PermissionRule) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, rule := range rules {
		if rule == nil {
			continue
		}
		m.rules = append(m.rules, rule)
	}
}

// ReplaceRules swaps the active ordered rule set.
func (m *PermissionManager) ReplaceRules(rules ...PermissionRule) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rules = nil
	for _, rule := range rules {
		if rule == nil {
			continue
		}
		m.rules = append(m.rules, rule)
	}
}

// Evaluate resolves session overrides first, then static rules, and creates
// pending approval requests when a rule requires user approval.
func (m *PermissionManager) Evaluate(req PermissionRequest) PermissionResult {
	if m == nil {
		return PermissionResult{Decision: PermissionAbstain}
	}
	key := permissionDecisionKey(req)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneExpiredLocked()

	for _, candidate := range permissionDecisionKeys(req) {
		if result, ok := m.consumeOverrideLocked(candidate); ok {
			return result
		}
	}
	if pendingID, ok := m.pendingKey[key]; ok {
		pending := m.pending[pendingID]
		return PermissionResult{
			Decision:  PermissionPending,
			Reason:    pending.Reason,
			Scope:     "approval",
			RequestID: pending.ID,
		}
	}

	for _, rule := range m.rules {
		result, handled := rule.Evaluate(req)
		if !handled {
			continue
		}
		if result.Decision == "" {
			result.Decision = PermissionAbstain
		}
		if result.Decision == PermissionPending && strings.TrimSpace(result.Scope) != "review" {
			pending := m.ensurePendingLocked(req, result)
			result.RequestID = pending.ID
			if strings.TrimSpace(result.Reason) == "" {
				result.Reason = pending.Reason
			}
		}
		return result
	}
	return PermissionResult{Decision: PermissionAbstain}
}

// AllowSession records a session-scoped allow decision for the exact request fingerprint.
func (m *PermissionManager) AllowSession(req PermissionRequest) {
	if m == nil {
		return
	}
	m.setSessionDecision(req, PermissionResult{Decision: PermissionAllow, Scope: string(PermissionGrantSession)}, PermissionGrantSession)
}

// AllowOnce records a one-time allow decision for the exact request fingerprint.
func (m *PermissionManager) AllowOnce(req PermissionRequest) {
	if m == nil {
		return
	}
	m.setSessionDecision(req, PermissionResult{Decision: PermissionAllow, Scope: string(PermissionGrantOnce)}, PermissionGrantOnce)
}

// DenySession records a session-scoped deny decision for the exact request fingerprint.
func (m *PermissionManager) DenySession(req PermissionRequest, reason string) {
	if m == nil {
		return
	}
	m.setSessionDecision(req, PermissionResult{
		Decision: PermissionDeny,
		Reason:   strings.TrimSpace(reason),
		Scope:    string(PermissionGrantSession),
	}, PermissionGrantSession)
}

// ListPending returns pending permission requests for one session.
func (m *PermissionManager) ListPending(sessionID string) []PendingPermission {
	if m == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneExpiredLocked()

	items := make([]PendingPermission, 0, len(m.pending))
	for _, item := range m.pending {
		if sessionID != "" && item.Request.SessionID != sessionID {
			continue
		}
		items = append(items, clonePendingPermission(item))
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
	return items
}

// ExportSession returns the persisted permission state for one session.
func (m *PermissionManager) ExportSession(sessionID string) PermissionSessionState {
	if m == nil {
		return PermissionSessionState{}
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return PermissionSessionState{}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneExpiredLocked()

	state := PermissionSessionState{}
	for _, override := range m.decisions {
		if strings.TrimSpace(override.request.SessionID) != sessionID {
			continue
		}
		state.Overrides = append(state.Overrides, PermissionOverrideState{
			Request:   clonePermissionRequest(override.request),
			Result:    override.result,
			Remaining: override.remaining,
			ExpiresAt: override.expiresAt,
		})
	}
	sort.Slice(state.Overrides, func(i, j int) bool {
		return permissionDecisionKey(state.Overrides[i].Request) < permissionDecisionKey(state.Overrides[j].Request)
	})

	for _, pending := range m.pending {
		if strings.TrimSpace(pending.Request.SessionID) != sessionID {
			continue
		}
		state.Pending = append(state.Pending, clonePendingPermission(pending))
	}
	sort.Slice(state.Pending, func(i, j int) bool {
		if state.Pending[i].CreatedAt.Equal(state.Pending[j].CreatedAt) {
			return state.Pending[i].ID < state.Pending[j].ID
		}
		return state.Pending[i].CreatedAt.Before(state.Pending[j].CreatedAt)
	})
	return state
}

// RestoreSession replaces the persisted permission state for one session.
func (m *PermissionManager) RestoreSession(sessionID string, state PermissionSessionState) {
	if m == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}

	m.ResetSession(sessionID)

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, item := range state.Overrides {
		req := clonePermissionRequest(item.Request)
		if strings.TrimSpace(req.SessionID) == "" {
			req.SessionID = sessionID
		}
		if strings.TrimSpace(req.SessionID) != sessionID {
			continue
		}
		m.decisions[permissionOverrideKey(req, storedPermissionGrantScope(item))] = permissionOverride{
			request:   req,
			result:    item.Result,
			remaining: item.Remaining,
			expiresAt: item.ExpiresAt,
		}
	}

	for _, item := range state.Pending {
		pending := clonePendingPermission(item)
		if strings.TrimSpace(pending.Request.SessionID) == "" {
			pending.Request.SessionID = sessionID
		}
		if strings.TrimSpace(pending.Request.SessionID) != sessionID {
			continue
		}
		if pending.ID == "" {
			pending.ID = pendingPermissionID(permissionDecisionKey(pending.Request))
		}
		if pending.CreatedAt.IsZero() {
			pending.CreatedAt = m.nowUTCLocked()
		}
		if pending.ExpiresAt.IsZero() {
			pending.ExpiresAt = pending.CreatedAt.Add(m.pendingTTL)
		}
		m.pending[pending.ID] = pending
		m.pendingKey[permissionDecisionKey(pending.Request)] = pending.ID
	}
}

// ApprovePending resolves a pending request and records either a one-time or
// session-scoped allow decision.
func (m *PermissionManager) ApprovePending(sessionID, requestID string, scope PermissionGrantScope) (PermissionResolution, error) {
	if m == nil {
		return PermissionResolution{}, fmt.Errorf("permission manager unavailable")
	}
	if scope == "" {
		scope = PermissionGrantSession
	}
	parsed, err := parsePermissionGrantScope(scope)
	if err != nil {
		return PermissionResolution{}, err
	}
	if parsed.kind == "" {
		parsed.kind = PermissionGrantSession
	}
	if parsed.kind != PermissionGrantOnce && parsed.kind != PermissionGrantSession && parsed.kind != PermissionGrantPattern && parsed.kind != "count" && parsed.kind != "timebox" {
		return PermissionResolution{}, fmt.Errorf("unsupported permission grant scope %q", scope)
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	pending, ok := m.pending[requestID]
	if !ok || !sameSession(pending.Request.SessionID, sessionID) {
		return PermissionResolution{}, fmt.Errorf("permission request not found")
	}
	if m.pendingExpiredLocked(pending) {
		delete(m.pending, requestID)
		delete(m.pendingKey, permissionDecisionKey(pending.Request))
		return PermissionResolution{}, fmt.Errorf("permission request expired")
	}
	delete(m.pending, requestID)
	delete(m.pendingKey, permissionDecisionKey(pending.Request))
	m.setSessionDecisionLocked(pending.Request, PermissionResult{
		Decision: PermissionAllow,
		Scope:    string(scope),
	}, scope)
	return PermissionResolution{
		RequestID:  requestID,
		Decision:   PermissionAllow,
		Scope:      scope,
		Request:    clonePermissionRequest(pending.Request),
		ResolvedAt: time.Now().UTC(),
	}, nil
}

// DenyPending resolves a pending request and records a session-scoped deny decision.
func (m *PermissionManager) DenyPending(sessionID, requestID, reason string) (PermissionResolution, error) {
	if m == nil {
		return PermissionResolution{}, fmt.Errorf("permission manager unavailable")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	pending, ok := m.pending[requestID]
	if !ok || !sameSession(pending.Request.SessionID, sessionID) {
		return PermissionResolution{}, fmt.Errorf("permission request not found")
	}
	delete(m.pending, requestID)
	delete(m.pendingKey, permissionDecisionKey(pending.Request))
	trimmedReason := strings.TrimSpace(reason)
	if trimmedReason == "" {
		trimmedReason = "permission request denied"
	}
	m.setSessionDecisionLocked(pending.Request, PermissionResult{
		Decision: PermissionDeny,
		Reason:   trimmedReason,
		Scope:    string(PermissionGrantSession),
	}, PermissionGrantSession)
	return PermissionResolution{
		RequestID:  requestID,
		Decision:   PermissionDeny,
		Scope:      PermissionGrantSession,
		Reason:     trimmedReason,
		Request:    clonePermissionRequest(pending.Request),
		ResolvedAt: time.Now().UTC(),
	}, nil
}

// ResetSession clears all session-scoped decisions and pending requests for one session.
func (m *PermissionManager) ResetSession(sessionID string) {
	if m == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := sessionID + "|"
	for key := range m.decisions {
		if strings.HasPrefix(key, prefix) {
			delete(m.decisions, key)
		}
	}
	for id, pending := range m.pending {
		if pending.Request.SessionID != sessionID {
			continue
		}
		delete(m.pendingKey, permissionDecisionKey(pending.Request))
		delete(m.pending, id)
	}
}

func (m *PermissionManager) setSessionDecision(req PermissionRequest, result PermissionResult, scope PermissionGrantScope) {
	req.SessionID = strings.TrimSpace(req.SessionID)
	if req.SessionID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setSessionDecisionLocked(req, result, scope)
}

func (m *PermissionManager) setSessionDecisionLocked(req PermissionRequest, result PermissionResult, scope PermissionGrantScope) {
	if req.SessionID == "" {
		return
	}
	parsed, err := parsePermissionGrantScope(scope)
	if err != nil {
		parsed = parsedPermissionGrant{kind: PermissionGrantSession}
	}
	override := permissionOverride{request: clonePermissionRequest(req), result: result, remaining: -1}
	switch parsed.kind {
	case PermissionGrantOnce:
		override.remaining = 1
	case "count":
		override.remaining = parsed.count
	case "timebox":
		override.expiresAt = m.nowUTCLocked().Add(parsed.duration)
	}
	key := permissionOverrideKey(req, scope)
	m.decisions[key] = override
	exactKey := permissionDecisionKey(req)
	if pendingID, ok := m.pendingKey[exactKey]; ok {
		delete(m.pendingKey, exactKey)
		delete(m.pending, pendingID)
	}
}

func (m *PermissionManager) consumeOverrideLocked(key string) (PermissionResult, bool) {
	override, ok := m.decisions[key]
	if !ok {
		return PermissionResult{}, false
	}
	if !override.expiresAt.IsZero() && !m.nowUTCLocked().Before(override.expiresAt) {
		delete(m.decisions, key)
		return PermissionResult{}, false
	}
	if override.remaining == 1 {
		delete(m.decisions, key)
	} else if override.remaining > 1 {
		override.remaining--
		m.decisions[key] = override
	}
	return override.result, true
}

func (m *PermissionManager) ensurePendingLocked(req PermissionRequest, result PermissionResult) PendingPermission {
	key := permissionDecisionKey(req)
	if pendingID, ok := m.pendingKey[key]; ok {
		return m.pending[pendingID]
	}
	reason := strings.TrimSpace(result.Reason)
	if reason == "" {
		reason = "approval required before running this tool"
	}
	pending := PendingPermission{
		ID:        pendingPermissionID(key),
		Request:   clonePermissionRequest(req),
		Reason:    reason,
		CreatedAt: m.nowUTCLocked(),
	}
	pending.ExpiresAt = pending.CreatedAt.Add(m.pendingTTL)
	m.pending[pending.ID] = pending
	m.pendingKey[key] = pending.ID
	return pending
}

func (m *PermissionManager) nowUTCLocked() time.Time {
	if m == nil || m.now == nil {
		return time.Now().UTC()
	}
	return m.now().UTC()
}

func (m *PermissionManager) pendingExpiredLocked(pending PendingPermission) bool {
	if pending.ExpiresAt.IsZero() {
		return false
	}
	return !m.nowUTCLocked().Before(pending.ExpiresAt)
}

func (m *PermissionManager) pruneExpiredLocked() {
	if m == nil {
		return
	}
	now := m.nowUTCLocked()
	for key, override := range m.decisions {
		if !override.expiresAt.IsZero() && !now.Before(override.expiresAt) {
			delete(m.decisions, key)
		}
	}
	for id, pending := range m.pending {
		if pending.ExpiresAt.IsZero() || now.Before(pending.ExpiresAt) {
			continue
		}
		delete(m.pending, id)
		delete(m.pendingKey, permissionDecisionKey(pending.Request))
	}
}

// RequestApproval creates or reuses a pending approval request for one tool call.
func (m *PermissionManager) RequestApproval(req PermissionRequest, reason string) PermissionResult {
	if m == nil {
		return PermissionResult{Decision: PermissionPending, Reason: strings.TrimSpace(reason), Scope: "approval"}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	pending := m.ensurePendingLocked(req, PermissionResult{
		Decision: PermissionPending,
		Reason:   strings.TrimSpace(reason),
		Scope:    "approval",
	})
	return PermissionResult{
		Decision:  PermissionPending,
		Reason:    pending.Reason,
		Scope:     "approval",
		RequestID: pending.ID,
	}
}

// NewPermissionInterceptor applies permission checks before tool execution.
func NewPermissionInterceptor(manager *PermissionManager) BeforeInterceptor {
	return NewPermissionInterceptorWithReview(manager, nil)
}

// NewPermissionInterceptorWithReview applies permission checks and optionally
// runs an automatic review pass before falling back to manual approval.
func NewPermissionInterceptorWithReview(manager *PermissionManager, reviewer PermissionReviewer) BeforeInterceptor {
	return func(ctx context.Context, call *ToolCall) (*ToolResult, error) {
		_ = ctx
		req := PermissionRequestFromCall(*call)
		result := manager.Evaluate(req)
		switch result.Decision {
		case PermissionDeny:
			reason := strings.TrimSpace(result.Reason)
			if reason == "" {
				reason = "tool call denied by permission policy"
			}
			return nil, ErrPermissionDenied{
				Tool:   req.ToolName,
				Action: req.Action,
				Reason: reason,
			}
		case PermissionPending:
			if strings.TrimSpace(result.Scope) == "review" {
				result = resolveReviewedPermission(ctx, manager, reviewer, req, result)
				switch result.Decision {
				case PermissionAllow:
					markApprovedShellCommand(call, req)
					return nil, nil
				case PermissionDeny:
					reason := strings.TrimSpace(result.Reason)
					if reason == "" {
						reason = "tool call denied by automatic review"
					}
					return nil, ErrPermissionDenied{
						Tool:   req.ToolName,
						Action: req.Action,
						Reason: reason,
					}
				}
			}
			reason := strings.TrimSpace(result.Reason)
			if reason == "" {
				reason = "tool call requires approval"
			}
			return nil, ErrPermissionPending{
				Tool:      req.ToolName,
				Action:    req.Action,
				RequestID: result.RequestID,
				Reason:    reason,
			}
		case PermissionAllow:
			markApprovedShellCommand(call, req)
			return nil, nil
		default:
			return nil, nil
		}
	}
}

func resolveReviewedPermission(ctx context.Context, manager *PermissionManager, reviewer PermissionReviewer, req PermissionRequest, initial PermissionResult) PermissionResult {
	fallback := func(reason string) PermissionResult {
		reason = strings.TrimSpace(reason)
		if reason == "" {
			reason = initial.Reason
		}
		if strings.TrimSpace(reason) == "" {
			reason = "automatic review requested manual approval"
		}
		return manager.RequestApproval(req, reason)
	}
	if reviewer == nil {
		return fallback("automatic review is unavailable; manual approval required")
	}
	reviewed, err := reviewer(ctx, req)
	if err != nil {
		return fallback(fmt.Sprintf("automatic review failed: %v", err))
	}
	switch reviewed.Decision {
	case PermissionAllow, PermissionDeny:
		return reviewed
	case PermissionPending:
		if strings.TrimSpace(reviewed.RequestID) != "" {
			return reviewed
		}
		return fallback(reviewed.Reason)
	default:
		return fallback(reviewed.Reason)
	}
}

func markApprovedShellCommand(call *ToolCall, req PermissionRequest) {
	toolName := strings.ToLower(strings.TrimSpace(req.ToolName))
	if call == nil || (toolName != "bash" && toolName != "background_run") {
		return
	}
	command := strings.TrimSpace(req.Command)
	if command == "" {
		return
	}
	names, err := tooling.DisallowedShellCommands(command)
	if err != nil || len(names) == 0 {
		return
	}
	if call.NormalizedInput == nil {
		call.NormalizedInput = map[string]interface{}{}
	}
	call.NormalizedInput["_allow_unlisted_commands"] = true
}

// PermissionRequestFromCall converts a runtime tool call into a permission request.
func PermissionRequestFromCall(call ToolCall) PermissionRequest {
	req := PermissionRequest{
		SessionID: strings.TrimSpace(call.SessionContext.SessionID),
		Source:    strings.TrimSpace(call.SessionContext.Source),
		Sender:    strings.TrimSpace(call.SessionContext.Sender),
		ToolName:  strings.TrimSpace(call.Name),
		Action:    inferPermissionAction(strings.TrimSpace(call.Name), call.NormalizedInput),
		Paths:     extractPermissionPaths(call.NormalizedInput),
		Command:   strings.TrimSpace(asString(call.NormalizedInput["command"])),
		Mutation:  inferPermissionMutation(strings.TrimSpace(call.Name), call.NormalizedInput),
		Input:     cloneStringAnyMap(call.NormalizedInput),
	}
	return req
}

// NewAutomationMutationRule blocks capability mutations during active automation runs.
func NewAutomationMutationRule() PermissionRule {
	return newAutomationMutationRule(true)
}

func newAutomationMutationRule(enabled bool) PermissionRule {
	return PermissionRuleFunc(func(req PermissionRequest) (PermissionResult, bool) {
		if !enabled {
			return PermissionResult{}, false
		}
		switch req.Source {
		case string(message.SourceCron), string(message.SourceHeartbeat):
		default:
			return PermissionResult{}, false
		}

		switch req.ToolName {
		case "cron":
			switch req.Action {
			case "create", "update", "toggle", "delete":
				return PermissionResult{
					Decision: PermissionDeny,
					Reason:   "cron schedule mutations are disabled during active automation runs; execute the scheduled work instead",
					Scope:    "policy",
				}, true
			}
		case "heartbeat":
			switch req.Action {
			case "set", "toggle":
				return PermissionResult{
					Decision: PermissionDeny,
					Reason:   "heartbeat configuration changes are disabled during active automation runs; execute the scheduled work instead",
					Scope:    "policy",
				}, true
			}
		case "tool_exchange":
			if req.Mutation {
				return PermissionResult{
					Decision: PermissionDeny,
					Reason:   "tool bundle mutations are disabled during active automation runs; execute the scheduled work instead",
					Scope:    "policy",
				}, true
			}
		case "manage_session":
			if req.Mutation {
				return PermissionResult{
					Decision: PermissionDeny,
					Reason:   "session-management mutations are disabled during active automation runs; execute the scheduled work instead",
					Scope:    "policy",
				}, true
			}
		}
		return PermissionResult{}, false
	})
}

// NewInteractiveApprovalRule marks high-risk remote tool actions as pending,
// review-gated, or auto-approved depending on configured mode.
func NewInteractiveApprovalRule() PermissionRule {
	return newInteractiveApprovalRule(DefaultPermissionPolicy().InteractiveApproval)
}

func newInteractiveApprovalRule(policy InteractiveApprovalPolicy) PermissionRule {
	policy = normalizeInteractiveApprovalPolicy(policy)
	if !policy.Enabled {
		return nil
	}
	allowedSources := make(map[string]struct{}, len(policy.Sources))
	for _, source := range policy.Sources {
		source = strings.ToLower(strings.TrimSpace(source))
		if source == "" {
			continue
		}
		allowedSources[source] = struct{}{}
	}
	allowedTools := make(map[string]struct{}, len(policy.Tools))
	for _, tool := range policy.Tools {
		tool = strings.ToLower(strings.TrimSpace(tool))
		if tool == "" {
			continue
		}
		allowedTools[tool] = struct{}{}
	}
	return PermissionRuleFunc(func(req PermissionRequest) (PermissionResult, bool) {
		if _, ok := allowedSources[strings.ToLower(strings.TrimSpace(req.Source))]; !ok {
			return PermissionResult{}, false
		}
		if _, ok := allowedTools[strings.ToLower(strings.TrimSpace(req.ToolName))]; !ok {
			return PermissionResult{}, false
		}
		if !requiresInteractiveApproval(req) {
			return PermissionResult{}, false
		}
		if matchesTrustedInteractiveApproval(req, policy) {
			return PermissionResult{
				Decision: PermissionAllow,
				Reason:   "request matched a trusted interactive approval bypass",
				Scope:    "policy",
			}, true
		}
		if policy.Mode == InteractiveApprovalModeReview {
			return PermissionResult{
				Decision: PermissionPending,
				Reason:   approvalReason(req),
				Scope:    "review",
			}, true
		}
		if policy.Mode == InteractiveApprovalModeYOLO {
			return PermissionResult{
				Decision: PermissionAllow,
				Reason:   "request auto-approved by yolo mode",
				Scope:    "policy",
			}, true
		}
		return PermissionResult{
			Decision: PermissionPending,
			Reason:   approvalReason(req),
			Scope:    "approval",
		}, true
	})
}

func NewUnlistedShellCommandApprovalRule() PermissionRule {
	return newUnlistedShellCommandApprovalRule(SecurityProfileGuardedLocal)
}

func newUnlistedShellCommandApprovalRule(profile string) PermissionRule {
	profile = normalizeSecurityProfile(profile)
	return PermissionRuleFunc(func(req PermissionRequest) (PermissionResult, bool) {
		toolName := strings.ToLower(strings.TrimSpace(req.ToolName))
		if toolName != "bash" && toolName != "background_run" {
			return PermissionResult{}, false
		}
		command := strings.TrimSpace(req.Command)
		if command == "" {
			return PermissionResult{}, false
		}
		names, err := tooling.DisallowedShellCommandsWithOptions(command, tooling.ShellCommandOptions{AllowedCommands: repairDiagnosticCommands(profile)})
		if err != nil || len(names) == 0 {
			return PermissionResult{}, false
		}
		return PermissionResult{
			Decision: PermissionPending,
			Reason:   fmt.Sprintf("shell command uses command(s) outside the allowlist: %s", strings.Join(names, ", ")),
			Scope:    "approval",
		}, true
	})
}

func NewSecurityProfileRule(profile string) PermissionRule {
	profile = normalizeSecurityProfile(profile)
	return PermissionRuleFunc(func(req PermissionRequest) (PermissionResult, bool) {
		toolName := strings.ToLower(strings.TrimSpace(req.ToolName))
		source := strings.ToLower(strings.TrimSpace(req.Source))
		if profile == SecurityProfileStrict && (toolName == "bash" || toolName == "background_run" || toolName == "desktop") {
			if source == string(message.SourceWeb) || source == string(message.SourceGateway) || source == string(message.SourceFeishu) || source == string(message.SourceWeixin) || source == string(message.SourceCron) || source == string(message.SourceHeartbeat) {
				return PermissionResult{
					Decision: PermissionDeny,
					Reason:   "strict security profile denies host execution tools for remote or automated sources",
					Scope:    "policy",
				}, true
			}
		}
		if toolName == "bash" || toolName == "background_run" {
			if risk := tooling.ClassifyShellCommandRisk(req.Command); risk.Level == tooling.ShellRiskHigh {
				if profile == SecurityProfileStrict {
					return PermissionResult{Decision: PermissionDeny, Reason: "strict security profile denies high-risk shell command: " + risk.Reason, Scope: "policy"}, true
				}
				if profile == SecurityProfileDevRepair {
					return PermissionResult{Decision: PermissionPending, Reason: "dev/repair profile requires approval for high-risk repair shell command: " + risk.Reason, Scope: "approval"}, true
				}
				return PermissionResult{Decision: PermissionPending, Reason: "high-risk shell command requires approval: " + risk.Reason, Scope: "approval"}, true
			}
		}
		return PermissionResult{}, false
	})
}

func permissionRulesForPolicy(policy PermissionPolicy) []PermissionRule {
	policy = normalizePermissionPolicy(policy)
	rules := make([]PermissionRule, 0, 4)
	if policy.BlockAutomationMutations {
		rules = append(rules, newAutomationMutationRule(true))
	}
	rules = append(rules, NewSecurityProfileRule(policy.SecurityProfile))
	if policy.InteractiveApproval.Enabled {
		rules = append(rules, newUnlistedShellCommandApprovalRule(policy.SecurityProfile))
	}
	if rule := newInteractiveApprovalRule(policy.InteractiveApproval); rule != nil {
		rules = append(rules, rule)
	}
	return rules
}

func normalizePermissionPolicy(policy PermissionPolicy) PermissionPolicy {
	policy.InteractiveApproval = normalizeInteractiveApprovalPolicy(policy.InteractiveApproval)
	policy.SecurityProfile = normalizeSecurityProfile(policy.SecurityProfile)
	return policy
}

func normalizeSecurityProfile(profile string) string {
	switch strings.ToLower(strings.TrimSpace(profile)) {
	case "", SecurityProfileGuardedLocal:
		return SecurityProfileGuardedLocal
	case SecurityProfileTrustedLocal, SecurityProfileSandboxed, SecurityProfileStrict, SecurityProfileHostPrivileged, SecurityProfileDevRepair:
		return strings.ToLower(strings.TrimSpace(profile))
	case "dev-repair", "dev_repair", "repair":
		return SecurityProfileDevRepair
	default:
		return SecurityProfileGuardedLocal
	}
}

func repairDiagnosticCommands(profile string) []string {
	if normalizeSecurityProfile(profile) != SecurityProfileDevRepair {
		return nil
	}
	return []string{"which", "command", "pgrep", "lsof", "ps", "stat"}
}

func normalizeInteractiveApprovalPolicy(policy InteractiveApprovalPolicy) InteractiveApprovalPolicy {
	policy.Mode = normalizeInteractiveApprovalMode(policy.Mode)
	if policy.PendingTTLSeconds <= 0 {
		policy.PendingTTLSeconds = 300
	}
	policy.Sources = normalizeStringList(policy.Sources)
	policy.Tools = normalizeStringList(policy.Tools)
	policy.TrustedPathPrefixes = normalizePathPrefixes(policy.TrustedPathPrefixes)
	policy.TrustedCommandPrefixes = normalizeCommandPrefixes(policy.TrustedCommandPrefixes)
	return policy
}

func normalizeInteractiveApprovalMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", InteractiveApprovalModeManual:
		return InteractiveApprovalModeManual
	case InteractiveApprovalModeReview:
		return InteractiveApprovalModeReview
	case InteractiveApprovalModeYOLO:
		return InteractiveApprovalModeYOLO
	default:
		return InteractiveApprovalModeManual
	}
}

func permissionDecisionKey(req PermissionRequest) string {
	parts := []string{
		strings.TrimSpace(req.SessionID),
		strings.TrimSpace(req.ToolName),
		strings.TrimSpace(req.Action),
	}
	for _, path := range req.Paths {
		parts = append(parts, strings.TrimSpace(path))
	}
	parts = append(parts, strings.TrimSpace(req.Command))
	return strings.Join(parts, "|")
}

func permissionDecisionKeys(req PermissionRequest) []string {
	keys := []string{permissionDecisionKey(req)}
	if broad := permissionSessionToolKey(req); broad != "" && broad != keys[0] {
		keys = append(keys, broad)
	}
	if pattern := permissionPatternKey(req); pattern != "" && pattern != keys[0] {
		keys = append(keys, pattern)
	}
	return keys
}

func permissionOverrideKey(req PermissionRequest, scope PermissionGrantScope) string {
	parsed, err := parsePermissionGrantScope(scope)
	if err == nil && parsed.kind == PermissionGrantPattern {
		if pattern := permissionPatternKey(req); pattern != "" {
			return pattern
		}
	}
	if scope == PermissionGrantSession {
		if broad := permissionSessionToolKey(req); broad != "" {
			return broad
		}
	}
	return permissionDecisionKey(req)
}

func permissionSessionToolKey(req PermissionRequest) string {
	sessionID := strings.TrimSpace(req.SessionID)
	toolName := strings.TrimSpace(req.ToolName)
	if sessionID == "" || toolName == "" {
		return ""
	}
	switch toolName {
	case "browser":
		action := strings.TrimSpace(req.Action)
		if action == "" {
			action = "*"
		}
		return strings.Join([]string{sessionID, toolName, action}, "|")
	case "desktop":
		action := strings.TrimSpace(req.Action)
		if action == "" {
			action = "*"
		}
		return strings.Join([]string{sessionID, toolName, action}, "|")
	default:
		return ""
	}
}

func permissionPatternKey(req PermissionRequest) string {
	sessionID := strings.TrimSpace(req.SessionID)
	toolName := strings.TrimSpace(req.ToolName)
	if sessionID == "" || toolName == "" {
		return ""
	}
	parts := []string{sessionID, toolName, "pattern", strings.TrimSpace(req.Action)}
	switch toolName {
	case "bash", "background_run":
		pattern := commandApprovalPattern(req.Command)
		if pattern == "" {
			return ""
		}
		parts = append(parts, pattern)
	case "write_file", "edit_file", "attach_file", "install_skill", "install_package", "remove_package":
		pattern := pathApprovalPattern(req.Paths)
		if pattern == "" {
			return ""
		}
		parts = append(parts, pattern)
	default:
		return ""
	}
	return strings.Join(parts, "|")
}

func commandApprovalPattern(command string) string {
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) == 0 {
		return ""
	}
	limit := 1
	if len(fields) > 1 {
		limit = 2
	}
	return strings.Join(fields[:limit], " ")
}

func pathApprovalPattern(paths []string) string {
	for _, path := range paths {
		path = normalizePermissionPath(path)
		if path == "" {
			continue
		}
		dir := filepath.Dir(path)
		if dir == "." || dir == "" {
			return path
		}
		return dir
	}
	return ""
}

func storedPermissionGrantScope(state PermissionOverrideState) PermissionGrantScope {
	scope := PermissionGrantScope(strings.TrimSpace(state.Result.Scope))
	if parsed, err := parsePermissionGrantScope(scope); err == nil {
		switch parsed.kind {
		case PermissionGrantOnce, PermissionGrantSession, PermissionGrantPattern, "count", "timebox":
			return scope
		}
	}
	if state.Remaining == 1 {
		return PermissionGrantOnce
	}
	return PermissionGrantSession
}

func parsePermissionGrantScope(scope PermissionGrantScope) (parsedPermissionGrant, error) {
	raw := strings.ToLower(strings.TrimSpace(string(scope)))
	switch raw {
	case "", string(PermissionGrantOnce):
		return parsedPermissionGrant{kind: PermissionGrantOnce}, nil
	case string(PermissionGrantSession):
		return parsedPermissionGrant{kind: PermissionGrantSession}, nil
	case string(PermissionGrantPattern):
		return parsedPermissionGrant{kind: PermissionGrantPattern}, nil
	}
	if strings.HasPrefix(raw, "count:") {
		countText := strings.TrimSpace(strings.TrimPrefix(raw, "count:"))
		count, err := parsePositiveInt(countText)
		if err != nil {
			return parsedPermissionGrant{}, fmt.Errorf("unsupported permission grant scope %q", scope)
		}
		return parsedPermissionGrant{kind: "count", count: count}, nil
	}
	if strings.HasPrefix(raw, "timebox:") {
		durationText := strings.TrimSpace(strings.TrimPrefix(raw, "timebox:"))
		duration, err := time.ParseDuration(durationText)
		if err != nil || duration <= 0 {
			return parsedPermissionGrant{}, fmt.Errorf("unsupported permission grant scope %q", scope)
		}
		return parsedPermissionGrant{kind: "timebox", duration: duration}, nil
	}
	return parsedPermissionGrant{}, fmt.Errorf("unsupported permission grant scope %q", scope)
}

func parsePositiveInt(text string) (int, error) {
	if text == "" {
		return 0, fmt.Errorf("missing count")
	}
	value := 0
	for _, ch := range text {
		if ch < '0' || ch > '9' {
			return 0, fmt.Errorf("invalid count")
		}
		value = value*10 + int(ch-'0')
	}
	if value <= 0 {
		return 0, fmt.Errorf("invalid count")
	}
	return value, nil
}

func pendingPermissionID(key string) string {
	sum := sha1.Sum([]byte(key))
	return hex.EncodeToString(sum[:8])
}

func inferPermissionAction(toolName string, input map[string]interface{}) string {
	if action := strings.ToLower(strings.TrimSpace(asString(input["action"]))); action != "" {
		return action
	}
	switch toolName {
	case "tool_exchange":
		if len(asStringSlice(input["enable_bundles"])) > 0 || len(asStringSlice(input["disable_bundles"])) > 0 {
			return "mutate"
		}
		return "inspect"
	case "load_skill":
		return "load"
	case "expand_skill":
		return "expand"
	case "unload_skill":
		return "unload"
	case "install_skill", "install_package":
		return "install"
	case "remove_package":
		return "remove"
	case "write_file":
		return "write"
	case "edit_file":
		return "edit"
	case "attach_file":
		return "attach"
	case "read_file":
		return "read"
	case "background_run":
		return "run"
	case "bash":
		return "exec"
	case "manage_session":
		return strings.ToLower(strings.TrimSpace(asString(input["action"])))
	default:
		return ""
	}
}

func inferPermissionMutation(toolName string, input map[string]interface{}) bool {
	switch toolName {
	case "bash", "background_run", "write_file", "edit_file", "attach_file", "tool_exchange", "install_skill", "install_package", "remove_package", "load_skill", "expand_skill", "unload_skill",
		"remember_memory", "forget_memory", "accept_memory_candidate", "dismiss_memory_candidate", "task_create", "task_update", "claim_task",
		"send_message", "broadcast", "shutdown_request", "plan_approval", "todo_write":
		if toolName == "tool_exchange" {
			return len(asStringSlice(input["enable_bundles"])) > 0 || len(asStringSlice(input["disable_bundles"])) > 0
		}
		return true
	case "manage_session":
		switch strings.ToLower(strings.TrimSpace(asString(input["action"]))) {
		case "clear_messages", "approve_permission", "deny_permission", "auth_login", "auth_logout":
			return true
		}
	case "browser":
		switch strings.ToLower(strings.TrimSpace(asString(input["action"]))) {
		case "open", "navigate", "click", "type", "press", "screenshot", "fill_form", "upload_file", "download", "capture_page", "search_and_open", "handoff", "resume":
			return true
		}
	case "desktop":
		switch strings.ToLower(strings.TrimSpace(asString(input["action"]))) {
		case "screenshot", "click", "type_text", "key", "clipboard_set":
			return true
		}
	case "cron":
		switch strings.ToLower(strings.TrimSpace(asString(input["action"]))) {
		case "create", "update", "toggle", "delete":
			return true
		}
	case "heartbeat":
		switch strings.ToLower(strings.TrimSpace(asString(input["action"]))) {
		case "set", "toggle":
			return true
		}
	}
	return false
}

func extractPermissionPaths(input map[string]interface{}) []string {
	keys := []string{
		"path",
		"root",
		"file",
		"source",
		"output_path",
		"download_path",
		"screenshot_path",
	}
	paths := make([]string, 0, len(keys))
	seen := make(map[string]struct{})
	for _, key := range keys {
		value := strings.TrimSpace(asString(input[key]))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		paths = append(paths, value)
	}
	for _, key := range []string{"paths"} {
		for _, value := range asStringSlice(input[key]) {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			paths = append(paths, value)
		}
	}
	return paths
}

func approvalReason(req PermissionRequest) string {
	switch req.ToolName {
	case "bash", "background_run":
		return "shell execution requires approval in remote sessions"
	case "attach_file":
		return "sending local files requires approval in remote sessions"
	case "write_file", "edit_file":
		return "workspace file mutations require approval in remote sessions"
	case "browser":
		return "interactive browser actions require approval in remote sessions"
	case "desktop":
		return "local desktop control requires approval in remote sessions"
	case "tool_exchange":
		return "tool bundle mutations require approval in remote sessions"
	case "cron", "heartbeat":
		return "automation configuration changes require approval in remote sessions"
	case "install_skill":
		return "skill installation requires approval in remote sessions"
	case "install_package", "remove_package":
		return "package changes require approval in remote sessions"
	default:
		return "approval required before running this tool"
	}
}

func PermissionIntentSummary(pending PendingPermission) string {
	req := pending.Request
	switch req.ToolName {
	case "bash", "background_run":
		if command := strings.TrimSpace(req.Command); command != "" {
			return "Agent wants to run shell command: " + command
		}
		return "Agent wants to run a shell command"
	case "write_file":
		if len(req.Paths) > 0 {
			return "Agent wants to write file: " + strings.TrimSpace(req.Paths[0])
		}
		return "Agent wants to write a workspace file"
	case "edit_file":
		if len(req.Paths) > 0 {
			return "Agent wants to edit file: " + strings.TrimSpace(req.Paths[0])
		}
		return "Agent wants to edit a workspace file"
	case "attach_file":
		if len(req.Paths) > 0 {
			return "Agent wants to attach local file: " + strings.TrimSpace(req.Paths[0])
		}
		return "Agent wants to attach a local file"
	case "browser":
		action := strings.TrimSpace(req.Action)
		if action == "" {
			action = "control browser"
		}
		return "Agent wants to " + action + " in the browser"
	case "desktop":
		action := strings.TrimSpace(req.Action)
		if action == "" {
			action = "control desktop"
		}
		return "Agent wants to " + action + " on the desktop"
	case "install_skill", "install_package", "remove_package":
		return "Agent wants to change installed capabilities with " + req.ToolName
	case "tool_exchange":
		return "Agent wants to change active tool bundles"
	case "cron", "heartbeat":
		return "Agent wants to change automation settings"
	default:
		if action := strings.TrimSpace(req.Action); action != "" {
			return "Agent wants to run " + req.ToolName + " " + action
		}
		if req.ToolName != "" {
			return "Agent wants to run " + req.ToolName
		}
		return "Agent wants to run a protected action"
	}
}

func PermissionRiskSummary(req PermissionRequest) string {
	switch req.ToolName {
	case "desktop":
		return "high risk: desktop control can interact with local apps and clipboard"
	case "browser":
		return "medium risk: browser control may navigate, click, type, or access web accounts"
	case "bash", "background_run":
		if strings.Contains(strings.ToLower(req.Command), "rm -rf") {
			return "high risk: recursive deletion command"
		}
		if risk := tooling.ClassifyShellCommandRisk(req.Command); risk.Level == tooling.ShellRiskHigh {
			return "high risk: " + risk.Reason
		}
		return "medium risk: shell execution can read, write, or run local programs"
	case "write_file", "edit_file":
		return "medium risk: workspace file mutation"
	case "attach_file":
		return "high risk: local file content may leave the machine"
	case "install_skill", "install_package", "remove_package", "tool_exchange":
		return "high risk: capability set changes can affect future tool behavior"
	case "cron", "heartbeat":
		return "medium risk: automation settings can run future work"
	default:
		if req.Mutation {
			return "medium risk: protected mutation"
		}
		return "low risk: protected read or inspection"
	}
}

func PermissionExpirySummary(pending PendingPermission, now time.Time) string {
	if pending.ExpiresAt.IsZero() {
		return ""
	}
	remaining := pending.ExpiresAt.Sub(now)
	if remaining <= 0 {
		return "expired"
	}
	if remaining >= time.Hour {
		return fmt.Sprintf("expires in %dh", int(remaining/time.Hour))
	}
	if remaining >= time.Minute {
		return fmt.Sprintf("expires in %dm", int(remaining/time.Minute))
	}
	return fmt.Sprintf("expires in %ds", int(remaining/time.Second))
}

func requiresInteractiveApproval(req PermissionRequest) bool {
	switch req.ToolName {
	case "bash", "background_run", "write_file", "edit_file", "attach_file", "install_skill", "install_package", "remove_package":
		return true
	case "tool_exchange", "cron", "heartbeat":
		return req.Mutation
	case "browser":
		return req.Mutation
	case "desktop":
		return req.Mutation
	default:
		return false
	}
}

func matchesTrustedInteractiveApproval(req PermissionRequest, policy InteractiveApprovalPolicy) bool {
	switch req.ToolName {
	case "write_file", "edit_file", "attach_file", "install_skill", "install_package", "remove_package":
		return len(req.Paths) > 0 && allPathsTrusted(req.Paths, policy.TrustedPathPrefixes)
	case "bash", "background_run":
		if !commandMatchesTrustedPrefix(req.Command, policy.TrustedCommandPrefixes) {
			return false
		}
		return len(req.Paths) == 0 || allPathsTrusted(req.Paths, policy.TrustedPathPrefixes)
	default:
		return false
	}
}

func allPathsTrusted(paths []string, prefixes []string) bool {
	if len(paths) == 0 || len(prefixes) == 0 {
		return false
	}
	for _, path := range paths {
		if !pathMatchesTrustedPrefix(path, prefixes) {
			return false
		}
	}
	return true
}

func pathMatchesTrustedPrefix(path string, prefixes []string) bool {
	path = normalizePermissionPath(path)
	if path == "" {
		return false
	}
	for _, prefix := range prefixes {
		if permissionPathHasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func permissionPathHasPrefix(path, prefix string) bool {
	if path == prefix {
		return true
	}
	if prefix == "." {
		return !strings.HasPrefix(path, "/")
	}
	if prefix == "/" {
		return strings.HasPrefix(path, "/")
	}
	if strings.HasSuffix(prefix, "/") {
		return strings.HasPrefix(path, prefix)
	}
	return strings.HasPrefix(path, prefix+"/")
}

func commandMatchesTrustedPrefix(command string, prefixes []string) bool {
	command = strings.TrimSpace(command)
	if command == "" || len(prefixes) == 0 {
		return false
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(command, prefix) {
			return true
		}
	}
	return false
}

func normalizeStringList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func normalizePathPrefixes(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = normalizePermissionPath(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func normalizeCommandPrefixes(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func normalizePermissionPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(value))
}

func clonePendingPermission(input PendingPermission) PendingPermission {
	return PendingPermission{
		ID:        input.ID,
		Request:   clonePermissionRequest(input.Request),
		Reason:    input.Reason,
		CreatedAt: input.CreatedAt,
		ExpiresAt: input.ExpiresAt,
	}
}

func clonePermissionRequest(input PermissionRequest) PermissionRequest {
	return PermissionRequest{
		SessionID: input.SessionID,
		Source:    input.Source,
		Sender:    input.Sender,
		ToolName:  input.ToolName,
		Action:    input.Action,
		Paths:     append([]string{}, input.Paths...),
		Command:   input.Command,
		Mutation:  input.Mutation,
		Input:     cloneStringAnyMap(input.Input),
	}
}

func sameSession(requestSessionID, sessionID string) bool {
	return strings.TrimSpace(requestSessionID) == strings.TrimSpace(sessionID)
}

func asString(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return ""
	}
}

func asStringSlice(value interface{}) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string{}, typed...)
	case []interface{}:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}
