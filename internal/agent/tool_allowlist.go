package agent

import (
	"strings"

	"github.com/tim5wang/godex/internal/core/toolfilter"
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

// ApplyToolOverlay merges a business key's override layer onto the current
// active tool set (the template baseline, M4 P1 convergence). Unlike
// ApplyToolAllowlist (which narrows to an intersection), this is additive
// with exclusions, matching the approved "可增可删可替换" semantics:
//
//   - "*" in either list activates every registered tool of that category;
//   - "!name" / "!server" removes that sandbox tool / MCP server;
//   - plain entries activate the tool even if the template baseline does not
//     include it (append);
//
// Always-active tools (memory, skill, manage_session, ...) are preserved by
// SetActiveTools regardless.
func (a *Agent) ApplyToolOverlay(allowedServers []string, allowedSandbox []string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	desired := make(map[string]struct{})
	for _, name := range a.toolHandler.ActiveToolNames() {
		desired[name] = struct{}{}
	}

	// Exclusion pass: "!x" / "!server" removes from the baseline.
	for _, s := range allowedSandbox {
		s = strings.TrimSpace(s)
		if strings.HasPrefix(s, "!") {
			delete(desired, strings.TrimPrefix(s, "!"))
		}
	}
	for _, s := range allowedServers {
		s = strings.TrimSpace(s)
		if strings.HasPrefix(s, "!") {
			server := strings.TrimPrefix(s, "!")
			for name := range desired {
				if srv, _, ok := splitMCPToolName(name); ok && srv == server {
					delete(desired, name)
				}
			}
		}
	}

	// Inclusion pass: plain entries and "*" activate registered tools.
	sandboxWildcard := containsWildcard(allowedSandbox)
	serverWildcard := containsWildcard(allowedServers)
	for _, name := range a.toolHandler.List() {
		if server, _, ok := splitMCPToolName(name); ok {
			if serverWildcard || allowServerEntry(allowedServers, server) {
				desired[name] = struct{}{}
			}
			continue
		}
		if sandboxWildcard || allowSandboxEntry(allowedSandbox, name) {
			desired[name] = struct{}{}
		}
	}

	names := make([]string, 0, len(desired))
	for n := range desired {
		names = append(names, n)
	}
	a.toolHandler.SetActiveTools(names...)
}

// allowServerEntry reports whether a server list (with "*" / "!x" entries)
// permits the given server in the inclusion pass ("!x" alone never adds).
func allowServerEntry(list []string, server string) bool {
	for _, s := range list {
		s = strings.TrimSpace(s)
		if s == server {
			return true
		}
	}
	return false
}

// allowSandboxEntry reports whether a sandbox list (with "*" / "!x" entries)
// permits the given tool in the inclusion pass ("!x" alone never adds).
func allowSandboxEntry(list []string, name string) bool {
	for _, s := range list {
		s = strings.TrimSpace(s)
		if s == name {
			return true
		}
	}
	return false
}

// ApplyStepListNarrow narrows the current active tool set (the template
// baseline plus the business key's override layer) to what the step request
// permits. The request can only narrow — entries with "*" / "!x" / "x/*"
// follow the same semantics as the step API's tool filters.
func (a *Agent) ApplyStepListNarrow(reqServers, reqSandbox []string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	var keep []string
	for _, name := range a.toolHandler.ActiveToolNames() {
		if server, _, ok := splitMCPToolName(name); ok {
			if toolfilter.Allows(reqServers, server) {
				keep = append(keep, name)
			}
			continue
		}
		if toolfilter.Allows(reqSandbox, name) {
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
