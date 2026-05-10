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
	return options
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
