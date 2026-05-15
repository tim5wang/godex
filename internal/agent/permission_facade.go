package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/tools"
)

func permissionPolicyFromConfig(cfg *config.Config) tools.PermissionPolicy {
	if cfg == nil {
		return tools.DefaultPermissionPolicy()
	}
	if strings.TrimSpace(cfg.Security.Profile) == "" &&
		!cfg.Tools.Permissions.BlockAutomationMutations &&
		!cfg.Tools.Permissions.InteractiveApprovalEnabled &&
		cfg.Tools.Permissions.InteractiveApprovalMode == "" &&
		cfg.Tools.Permissions.PendingTTLSeconds == 0 &&
		len(cfg.Tools.Permissions.InteractiveApprovalSources) == 0 &&
		len(cfg.Tools.Permissions.InteractiveApprovalTools) == 0 &&
		len(cfg.Tools.Permissions.TrustedPathPrefixes) == 0 &&
		len(cfg.Tools.Permissions.TrustedCommandPrefixes) == 0 {
		return tools.DefaultPermissionPolicy()
	}
	policy := tools.PermissionPolicyForSecurityProfile(cfg.Security.Profile, cfg.Tools.Permissions.InteractiveApprovalMode)
	policy.BlockAutomationMutations = cfg.Tools.Permissions.BlockAutomationMutations
	if cfg.Tools.Permissions.InteractiveApprovalMode != "" {
		policy.InteractiveApproval.Mode = cfg.Tools.Permissions.InteractiveApprovalMode
	}
	if cfg.Tools.Permissions.PendingTTLSeconds > 0 {
		policy.InteractiveApproval.PendingTTLSeconds = cfg.Tools.Permissions.PendingTTLSeconds
	}
	policy.InteractiveApproval.Enabled = cfg.Tools.Permissions.InteractiveApprovalEnabled
	if len(cfg.Tools.Permissions.InteractiveApprovalSources) > 0 {
		policy.InteractiveApproval.Sources = append([]string{}, cfg.Tools.Permissions.InteractiveApprovalSources...)
	}
	if len(cfg.Tools.Permissions.InteractiveApprovalTools) > 0 {
		policy.InteractiveApproval.Tools = append([]string{}, cfg.Tools.Permissions.InteractiveApprovalTools...)
	}
	if len(cfg.Tools.Permissions.TrustedPathPrefixes) > 0 {
		policy.InteractiveApproval.TrustedPathPrefixes = append([]string{}, cfg.Tools.Permissions.TrustedPathPrefixes...)
	}
	if len(cfg.Tools.Permissions.TrustedCommandPrefixes) > 0 {
		policy.InteractiveApproval.TrustedCommandPrefixes = append([]string{}, cfg.Tools.Permissions.TrustedCommandPrefixes...)
	}
	return policy
}

// PendingPermissions returns session-scoped pending permission approvals.
func (a *Agent) PendingPermissions(sessionID string) []tools.PendingPermission {
	if a.permissions == nil {
		return nil
	}
	return a.permissions.ListPending(sessionID)
}

// ApprovePendingPermission resolves a pending permission request.
func (a *Agent) ApprovePendingPermission(sessionID, requestID string, scope tools.PermissionGrantScope) (tools.PermissionResolution, error) {
	if a.permissions == nil {
		return tools.PermissionResolution{}, fmt.Errorf("permission manager unavailable")
	}
	return a.permissions.ApprovePending(sessionID, requestID, scope)
}

// DenyPendingPermission resolves a pending permission request with denial.
func (a *Agent) DenyPendingPermission(sessionID, requestID, reason string) (tools.PermissionResolution, error) {
	if a.permissions == nil {
		return tools.PermissionResolution{}, fmt.Errorf("permission manager unavailable")
	}
	return a.permissions.DenyPending(sessionID, requestID, reason)
}

func (a *Agent) reviewPermissionRequest(ctx context.Context, req tools.PermissionRequest) (tools.PermissionResult, error) {
	prompt := buildPermissionReviewPrompt(req)
	result, err := a.runScopedSubagent(ctx, prompt, "You are a security review subagent. Review one protected tool call from a remote session. You may use read_file when file context matters. Be conservative. Reply with exactly one line beginning with ALLOW:, DENY:, or MANUAL: followed by a short reason.", []string{"read_file"}, 8)
	if err != nil && !errors.Is(err, conversation.ErrMaxTurnsReached) {
		return tools.PermissionResult{}, err
	}
	if result == nil {
		return tools.PermissionResult{}, fmt.Errorf("permission review returned no result")
	}
	return parsePermissionReviewResult(result.LastAssistantText), nil
}

func buildPermissionReviewPrompt(req tools.PermissionRequest) string {
	lines := []string{
		"Review this protected tool call from a remote session.",
		"Decide whether it is safe enough to auto-approve right now.",
		"",
		fmt.Sprintf("Tool: %s", req.ToolName),
		fmt.Sprintf("Action: %s", req.Action),
		fmt.Sprintf("Source: %s", req.Source),
		fmt.Sprintf("Sender: %s", req.Sender),
		fmt.Sprintf("Mutation: %t", req.Mutation),
	}
	if command := strings.TrimSpace(req.Command); command != "" {
		lines = append(lines, "", "Command:", command)
	}
	if len(req.Paths) > 0 {
		lines = append(lines, "", "Paths:")
		lines = append(lines, req.Paths...)
	}
	if len(req.Input) > 0 {
		lines = append(lines, "", "Normalized input:")
		lines = append(lines, formatPermissionReviewInput(req.Input))
	}
	lines = append(lines,
		"",
		"Reply with exactly one line:",
		"ALLOW: <short reason>",
		"DENY: <short reason>",
		"MANUAL: <short reason>",
	)
	return strings.Join(lines, "\n")
}

func parsePermissionReviewResult(text string) tools.PermissionResult {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return tools.PermissionResult{Decision: tools.PermissionPending, Reason: "automatic review returned no decision"}
	}
	for _, line := range strings.Split(strings.ReplaceAll(trimmed, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(strings.Trim(line, "`"))
		switch {
		case strings.HasPrefix(strings.ToUpper(line), "ALLOW:"):
			return tools.PermissionResult{Decision: tools.PermissionAllow, Reason: strings.TrimSpace(line[len("ALLOW:"):]), Scope: "review"}
		case strings.HasPrefix(strings.ToUpper(line), "DENY:"):
			return tools.PermissionResult{Decision: tools.PermissionDeny, Reason: strings.TrimSpace(line[len("DENY:"):]), Scope: "review"}
		case strings.HasPrefix(strings.ToUpper(line), "MANUAL:"):
			return tools.PermissionResult{Decision: tools.PermissionPending, Reason: strings.TrimSpace(line[len("MANUAL:"):]), Scope: "review"}
		}
	}
	return tools.PermissionResult{Decision: tools.PermissionPending, Reason: "automatic review returned an unrecognized decision"}
}

func formatPermissionReviewInput(input map[string]interface{}) string {
	data, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return fmt.Sprint(input)
	}
	return string(data)
}
