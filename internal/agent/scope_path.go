package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/tim5wang/godex/internal/core/scope"
	"github.com/tim5wang/godex/internal/tools"
)

// ResolveWritePath resolves a tool-requested write path inside the scope root
// (roadmap 6.2 M4). It returns the clean absolute path when the request stays
// inside root, and an error when the path escapes the root via ".." or an
// absolute path outside the workspace. Requests are usually relative (e.g.
// "docs/plan.md"); absolute requests are allowed only when they live under
// root.
//
// The scope parameter is informational today (session scopes enforce against
// their own root; org/unspecified scopes use the workspace root) and kept in
// the signature so callers can later branch per scope kind without changing
// the call sites.
func ResolveWritePath(scopeID scope.Id, root, requested string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", fmt.Errorf("scope path: empty workspace root")
	}
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", fmt.Errorf("scope path: empty requested path")
	}
	cleanRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("scope path: resolve root %q: %w", root, err)
	}
	candidate := requested
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(cleanRoot, candidate)
	}
	cleanCandidate, err := filepath.Abs(filepath.Clean(candidate))
	if err != nil {
		return "", fmt.Errorf("scope path: resolve %q: %w", requested, err)
	}
	// filepath.Rel returns "" when the paths are equal, and a clean relative
	// path otherwise; escaping root yields a ".."-prefixed result.
	rel, err := filepath.Rel(cleanRoot, cleanCandidate)
	if err != nil {
		return "", fmt.Errorf("scope path: compare %q to root %q: %w", requested, cleanRoot, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("scope path: %q escapes workspace root %q", requested, cleanRoot)
	}
	return cleanCandidate, nil
}

// NewScopeWriteInterceptor returns a before-interceptor that rejects write
// tools (write_file/edit_file) whose path argument escapes the scope workspace
// root (roadmap 6.2 M4). attach_file is intentionally NOT intercepted: it does
// not write into the workspace — it reads a file through the already
// boundary-enforced workspacefs (workspace root + read allowlist, which covers
// GoDex state dirs and session attachments) and promotes it to a session
// attachment, so an additional write-guard would wrongly block attaching
// allowlisted external files. When the guard is disabled
// (tools.execution.scope_write=false) callers should not install it at all;
// this constructor assumes enforcement is wanted.
func NewScopeWriteInterceptor(workspaceDir string) tools.BeforeInterceptor {
	return func(ctx context.Context, call *tools.ToolCall) (*tools.ToolResult, error) {
		_ = ctx
		pathArg, ok := call.NormalizedInput["path"].(string)
		if !ok || strings.TrimSpace(pathArg) == "" {
			// No path argument (e.g. edit_file files[] batch) — leave it to the
			// executor's own path handling.
			return nil, nil
		}
		if _, err := ResolveWritePath(scope.Session(""), workspaceDir, pathArg); err != nil {
			return &tools.ToolResult{Text: fmt.Sprintf("blocked by scope write guard: %v", err)}, nil
		}
		return nil, nil
	}
}
