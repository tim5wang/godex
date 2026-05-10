package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/core/modelcontext"
	"github.com/tim5wang/godex/internal/tools"
)

type storedModelToolResult struct {
	SessionID string    `json:"session_id,omitempty"`
	ToolUseID string    `json:"tool_use_id,omitempty"`
	ToolName  string    `json:"tool_name,omitempty"`
	Bytes     int       `json:"bytes"`
	SHA256    string    `json:"sha256"`
	Output    string    `json:"output"`
	CreatedAt time.Time `json:"created_at"`
}

func (a *Agent) filterModelToolResult(ctx context.Context, tool conversation.ExecutedTool) conversation.ExecutedTool {
	if !modelcontext.TooLargeForModel(tool.Output) {
		return tool
	}

	bytes := len([]byte(tool.Output))
	sha := modelcontext.SHA256Hex(tool.Output)
	artifactPath, artifactErr := a.storeModelToolResult(ctx, tool, bytes, sha)
	refPath := a.modelToolResultReferencePath(artifactPath)
	if artifactPath != "" {
		tool.ArtifactPaths = appendUniqueString(tool.ArtifactPaths, artifactPath)
	}

	note := "Large tool result was removed from model-visible context; use the referenced artifact for full output."
	if artifactErr != nil {
		note = "Large tool result was removed from model-visible context, but writing the artifact failed: " + artifactErr.Error()
	}
	tool.Output = modelcontext.SummaryJSON(modelcontext.LargeToolResultSummary{
		ToolName:     tool.Name,
		ToolUseID:    tool.ID,
		Bytes:        bytes,
		SHA256:       sha,
		ArtifactPath: refPath,
		Preview:      modelcontext.TruncatedPreview(tool.Output),
		Note:         note,
	})
	return tool
}

func (a *Agent) storeModelToolResult(ctx context.Context, tool conversation.ExecutedTool, bytes int, sha string) (string, error) {
	if a == nil || a.cfg == nil {
		return "", nil
	}
	sessionID := strings.TrimSpace(tools.SessionIDFromContext(ctx))
	if sessionID == "" {
		sessionID = "unknown-session"
	}
	toolUseID := strings.TrimSpace(tool.ID)
	if toolUseID == "" {
		toolUseID = sha[:16]
	}
	root := strings.TrimSpace(a.cfg.StateDir)
	if root == "" {
		root = filepath.Join(strings.TrimSpace(a.cfg.WorkspaceDir), ".godex")
	}
	dir := filepath.Join(root, ".tool-results", safeToolResultPathSegment(sessionID))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, safeToolResultPathSegment(toolUseID)+".json")
	payload := storedModelToolResult{
		SessionID: sessionID,
		ToolUseID: tool.ID,
		ToolName:  tool.Name,
		Bytes:     bytes,
		SHA256:    sha,
		Output:    tool.Output,
		CreatedAt: time.Now(),
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", err
	}
	return path, nil
}

func (a *Agent) modelToolResultReferencePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || a == nil || a.cfg == nil {
		return path
	}
	workspace := strings.TrimSpace(a.cfg.WorkspaceDir)
	if workspace == "" {
		return path
	}
	rel, err := filepath.Rel(workspace, path)
	if err != nil || rel == "" || strings.HasPrefix(rel, "..") {
		return path
	}
	return filepath.ToSlash(rel)
}

func safeToolResultPathSegment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), ".")
	if out == "" {
		return "unknown"
	}
	return out
}

func appendUniqueString(items []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return append([]string{}, items...)
	}
	out := append([]string{}, items...)
	for _, item := range out {
		if item == value {
			return out
		}
	}
	return append(out, value)
}
