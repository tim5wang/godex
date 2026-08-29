package tools

import (
	"strings"

	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/platform/tooling"
)

func ShellCommandOptionsForContext(runtimeCtx automation.SessionContext, options tooling.ShellCommandOptions) tooling.ShellCommandOptions {
	if isDevRepairSecurityProfile(runtimeCtx.SecurityProfile) {
		options.AllowedCommands = append(options.AllowedCommands, "which", "command", "pgrep", "lsof", "ps", "stat")
	}
	// Relax the `` `$()` `` command-substitution gate for the agent bash path.
	// Normal mode inspects each substitution's inner command against the safety
	// chain (read-only commands pass; high-risk/nested/dangerous are rejected).
	// Yolo/trusted approval mode allows every substitution.
	options.RelaxCommandSubstitution = true
	if isYoloApprovalMode(runtimeCtx.ApprovalMode) {
		options.RelaxSubstitutionAll = true
	}
	return options
}

func isYoloApprovalMode(mode string) bool {
	return strings.EqualFold(strings.TrimSpace(mode), "yolo")
}

func shellCommandOptionsForContext(runtimeCtx automation.SessionContext, options tooling.ShellCommandOptions) tooling.ShellCommandOptions {
	return ShellCommandOptionsForContext(runtimeCtx, options)
}

func isDevRepairSecurityProfile(profile string) bool {
	switch strings.ToLower(strings.TrimSpace(profile)) {
	case SecurityProfileDevRepair, "dev-repair", "dev_repair", "repair":
		return true
	default:
		return false
	}
}
