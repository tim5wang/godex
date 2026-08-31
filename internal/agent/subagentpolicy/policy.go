// Package subagentpolicy owns pure durable-subagent tool and write-scope rules.
package subagentpolicy

import (
	"path/filepath"
	"sort"
	"strings"
)

// DefaultToolNames returns the built-in surface for an unnamed subagent role.
func DefaultToolNames(agentType string) []string {
	if NormalizeType(agentType) == "general-purpose" {
		return []string{"bash", "read_file", "write_file", "edit_file"}
	}
	return []string{"read_file"}
}

// SupportedTool reports whether the durable runtime can provide a tool.
func SupportedTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "bash", "read_file", "write_file", "edit_file", "web_search", "web_fetch":
		return true
	default:
		return false
	}
}

// IsWriteTool reports whether a tool can directly mutate workspace files.
func IsWriteTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "write_file", "edit_file":
		return true
	default:
		return false
	}
}

// NarrowWriteTools removes shell/write tools when no write scope was granted.
func NarrowWriteTools(toolNames, writeScope []string) []string {
	hasWriteScope := len(NormalizeWriteScope(writeScope)) > 0
	out := make([]string, 0, len(toolNames))
	for _, name := range toolNames {
		name = strings.TrimSpace(name)
		if name == "" || (!hasWriteScope && (name == "bash" || IsWriteTool(name))) {
			continue
		}
		out = append(out, name)
	}
	if len(out) == 0 {
		return []string{"read_file"}
	}
	return unique(out)
}

// NormalizeWriteScope canonicalizes, sorts, and deduplicates relative scopes.
func NormalizeWriteScope(scope []string) []string {
	out := make([]string, 0, len(scope))
	seen := make(map[string]struct{}, len(scope))
	for _, item := range scope {
		item = strings.Trim(strings.TrimSpace(filepath.ToSlash(item)), "/")
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

// PathAllowed reports whether a relative path is within an allowed write scope.
func PathAllowed(path string, scope []string) bool {
	path = strings.Trim(strings.TrimSpace(filepath.ToSlash(path)), "/")
	if path == "" || strings.HasPrefix(path, "../") || path == ".." {
		return false
	}
	for _, item := range NormalizeWriteScope(scope) {
		if path == item || strings.HasPrefix(path, item+"/") {
			return true
		}
	}
	return false
}

// NormalizeType preserves the two built-in role spellings and trims named roles.
func NormalizeType(agentType string) string {
	agentType = strings.TrimSpace(agentType)
	if agentType == "" || strings.EqualFold(agentType, "explore") {
		return "Explore"
	}
	return agentType
}

func unique(items []string) []string {
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}
