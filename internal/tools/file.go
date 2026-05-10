package tools

import (
	"context"
	"fmt"
	"mime"
	"path/filepath"
	"strings"

	"github.com/tim5wang/godex/internal/platform/tooling"
	"github.com/tim5wang/godex/internal/platform/workspacefs"
)

type readFileArgs struct {
	Path      string `json:"path"`
	Limit     int    `json:"limit,omitempty"`
	Offset    int    `json:"offset,omitempty"`
	StartLine int    `json:"start_line,omitempty"`
}

type writeFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type editFileArgs struct {
	Path    string `json:"path"`
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
}

type attachFileArgs struct {
	Path string `json:"path"`
}

// NewReadFileTool creates a new read file tool.
func NewReadFileTool(workspace string) Tool {
	executor := tooling.NewWorkspaceExecutor(workspace)
	return NewTypedTool(SpecFromDefinition(tooling.ReadFileDefinition(), nil), func(ctx context.Context, args readFileArgs) (ToolResult, error) {
		_ = ctx
		if strings.TrimSpace(args.Path) == "" {
			return ToolResult{}, fmt.Errorf("missing path argument")
		}
		output, err := executor.ReadFileRange(args.Path, args.Limit, args.Offset, args.StartLine)
		return ToolResult{Text: output}, err
	})
}

// NewWriteFileTool creates a new write file tool.
func NewWriteFileTool(workspace string) Tool {
	executor := tooling.NewWorkspaceExecutor(workspace)
	return NewTypedTool(SpecFromDefinition(tooling.WriteFileDefinition(), nil), func(ctx context.Context, args writeFileArgs) (ToolResult, error) {
		_ = ctx
		if strings.TrimSpace(args.Path) == "" {
			return ToolResult{}, fmt.Errorf("missing path argument")
		}
		output, err := executor.WriteFile(args.Path, args.Content)
		return ToolResult{Text: output}, err
	})
}

// NewEditFileTool creates a new edit file tool.
func NewEditFileTool(workspace string) Tool {
	executor := tooling.NewWorkspaceExecutor(workspace)
	return NewTypedTool(SpecFromDefinition(tooling.EditFileDefinition(), nil), func(ctx context.Context, args editFileArgs) (ToolResult, error) {
		_ = ctx
		if strings.TrimSpace(args.Path) == "" {
			return ToolResult{}, fmt.Errorf("missing path argument")
		}
		if args.OldText == "" {
			return ToolResult{}, fmt.Errorf("missing old_text argument")
		}
		output, err := executor.EditFile(args.Path, args.OldText, args.NewText)
		return ToolResult{Text: output}, err
	})
}

// NewAttachFileTool creates a tool that explicitly promotes one local file into
// the current session reply as an attachment.
func NewAttachFileTool(workspace string) Tool {
	return NewTypedTool(SpecFromDefinition(tooling.AttachFileDefinition(), nil), func(ctx context.Context, args attachFileArgs) (ToolResult, error) {
		_ = ctx
		if strings.TrimSpace(args.Path) == "" {
			return ToolResult{}, fmt.Errorf("missing path argument")
		}
		root, err := workspacefs.New(workspace)
		if err != nil {
			return ToolResult{}, err
		}
		defer root.Close()
		absPath, err := root.Abs(args.Path)
		if err != nil {
			return ToolResult{}, err
		}
		info, err := root.Stat(args.Path)
		if err != nil {
			return ToolResult{}, err
		}
		if info.IsDir() {
			return ToolResult{}, fmt.Errorf("path is a directory: %s", args.Path)
		}
		name := filepath.Base(absPath)
		return ToolResult{
			Structured: map[string]interface{}{
				"status":     "attached",
				"path":       filepath.ToSlash(absPath),
				"name":       name,
				"mime_type":  mime.TypeByExtension(strings.ToLower(filepath.Ext(name))),
				"size_bytes": info.Size(),
			},
			ArtifactPaths: []string{filepath.ToSlash(absPath)},
		}, nil
	})
}
