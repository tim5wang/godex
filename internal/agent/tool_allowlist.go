package agent

import (
	"strings"
)

// ApplyToolAllowlist narrows the session's active tool set to the tools
// permitted by a business key (Agent Step Platform): only MCP tools whose
// server is in allowedServers, plus sandbox tools listed in allowedSandbox.
// A "*" entry in either list means "all". Always-active tools (memory, skill,
// manage_session, ...) are preserved by SetActiveTools regardless.
//
// Tool naming follows tools.mcpToolName: MCP tools are "<server>__<tool>";
// anything without a "__" separator is treated as a sandbox tool. This is the
// minimal-permission intersection applied after a step session is opened.
func (a *Agent) ApplyToolAllowlist(allowedServers []string, allowedSandbox []string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	allowServer := func(server string) bool {
		if containsWildcard(allowedServers) {
			return true
		}
		for _, s := range allowedServers {
			if s == server {
				return true
			}
		}
		return false
	}
	allowSandbox := func(name string) bool {
		if containsWildcard(allowedSandbox) {
			return true
		}
		for _, s := range allowedSandbox {
			if s == name {
				return true
			}
		}
		return false
	}

	var keep []string
	for _, name := range a.toolHandler.List() {
		// Always-active tools are re-added by SetActiveTools; no need to
		// filter them here (and we must not drop them from `keep` anyway).
		if server, tool, ok := splitMCPToolName(name); ok {
			if allowServer(server) {
				keep = append(keep, name)
			}
			_ = tool
			continue
		}
		if allowSandbox(name) {
			keep = append(keep, name)
		}
	}
	a.toolHandler.SetActiveTools(keep...)
}

// splitMCPToolName splits an MCP tool name "<server>__<tool>" back into its
// parts. Names without the double-underscore separator are sandbox tools.
func splitMCPToolName(name string) (server, tool string, ok bool) {
	idx := strings.Index(name, "__")
	if idx <= 0 || idx == len(name)-2 {
		return "", "", false
	}
	return name[:idx], name[idx+2:], true
}

func containsWildcard(list []string) bool {
	for _, item := range list {
		if item == "*" {
			return true
		}
	}
	return false
}
