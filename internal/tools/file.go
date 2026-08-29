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
	Path               string `json:"path"`
	Limit              int    `json:"limit,omitempty"`
	Offset             int    `json:"offset,omitempty"`
	IncludeLineNumbers *bool  `json:"include_line_numbers,omitempty"`
	// Deprecated: preserved for backward compat, mapped to offset/limit.
	StartLine int `json:"start_line,omitempty"`
	MaxLines  int `json:"max_lines,omitempty"`
}

func (a readFileArgs) effectiveOffset() int {
	if a.Offset > 0 {
		return a.Offset
	}
	if a.StartLine > 0 {
		return a.StartLine
	}
	return 1
}

func (a readFileArgs) effectiveLimit() int {
	if a.Limit > 0 {
		return a.Limit
	}
	return a.MaxLines
}

type writeFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type editFileArgs struct {
	Path    string          `json:"path"`
	OldText string          `json:"old_text,omitempty"`
	NewText string          `json:"new_text,omitempty"`
	Edits   []fileEdit      `json:"edits,omitempty"`
	Files   []fileEditBatch `json:"files,omitempty"`
}

type fileEdit struct {
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
}

type fileEditBatch struct {
	Path  string     `json:"path"`
	Edits []fileEdit `json:"edits"`
}

type attachFileArgs struct {
	Path string `json:"path"`
}

// NewReadFileTool creates a new read file tool with line numbers, smart defaults,
// and image detection support.
func NewReadFileTool(workspace string) Tool {
	return NewReadFileToolWithExecutor(tooling.NewWorkspaceExecutor(workspace))
}

// NewReadFileToolWithExecutor creates a read file tool that delegates to the
// given executor. The executor's FS (local or remote) is used for all file I/O.
func NewReadFileToolWithExecutor(executor *tooling.WorkspaceExecutor) Tool {
	return NewTypedTool(SpecFromDefinition(tooling.ReadFileDefinition(), map[string]string{}), func(ctx context.Context, args readFileArgs) (ToolResult, error) {
		_ = ctx
		if strings.TrimSpace(args.Path) == "" {
			return ToolResult{}, fmt.Errorf("missing path argument")
		}

		showNumbers := true
		if args.IncludeLineNumbers != nil {
			showNumbers = *args.IncludeLineNumbers
		}

		result, err := executor.ReadFileLines(tooling.ReadFileLinesOptions{
			Path:               args.Path,
			Offset:             args.effectiveOffset(),
			Limit:              args.effectiveLimit(),
			IncludeLineNumbers: showNumbers,
		})
		if err != nil {
			return ToolResult{}, err
		}

		// Handle image detection.
		if result.IsImage {
			text := fmt.Sprintf("Image file detected: %s (%s, %d bytes).",
				result.Path, result.MimeType, len(result.Data))
			text += "\nUse attach_file to include this image in the conversation for OCR text extraction and visual analysis."

			return ToolResult{
				Text: text,
				Structured: map[string]interface{}{
					"type":       "image",
					"path":       result.Path,
					"mime_type":  result.MimeType,
					"size_bytes": len(result.Data),
				},
			}, nil
		}

		return ToolResult{
			Text: result.Content,
			Structured: map[string]interface{}{
				"type":        "text",
				"path":        result.Path,
				"total_lines": result.TotalLines,
				"truncated":   result.Truncated,
			},
		}, nil
	})
}

// NewWriteFileTool creates a new write file tool.
func NewWriteFileTool(workspace string) Tool {
	return NewWriteFileToolWithExecutor(tooling.NewWorkspaceExecutor(workspace))
}

// NewWriteFileToolWithExecutor creates a write tool that delegates to the
// given executor. The executor's FS (local or remote) is used for all file I/O.
func NewWriteFileToolWithExecutor(executor *tooling.WorkspaceExecutor) Tool {
	return NewTypedTool(SpecFromDefinition(tooling.WriteFileDefinition(), nil), func(ctx context.Context, args writeFileArgs) (ToolResult, error) {
		_ = ctx
		if strings.TrimSpace(args.Path) == "" {
			return ToolResult{}, fmt.Errorf("missing path argument: write_file requires path and content, for example {\"path\":\"docs/plan.md\",\"content\":\"...\"}")
		}
		output, err := executor.WriteFile(args.Path, args.Content)
		return ToolResult{Text: output}, err
	})
}

// NewEditFileTool creates a new edit file tool with multi-edit support.
// Supports both single edit (old_text/new_text) and multi-edit (edits[]) for backward compatibility.
func NewEditFileTool(workspace string) Tool {
	return NewEditFileToolWithExecutor(tooling.NewWorkspaceExecutor(workspace))
}

// NewEditFileToolWithExecutor creates an edit tool that delegates to the
// given executor. The executor's FS (local or remote) is used for all file I/O.
func NewEditFileToolWithExecutor(executor *tooling.WorkspaceExecutor) Tool {
	return NewTypedTool(SpecFromDefinition(tooling.EditFileDefinition(), nil), func(ctx context.Context, args editFileArgs) (ToolResult, error) {
		_ = ctx
		// Multi-file mode takes highest precedence (path not required).
		if len(args.Files) > 0 {
			output, err := executor.EditFilesMulti(args.FilesToToolingBatches())
			return ToolResult{Text: output}, err
		}
		if strings.TrimSpace(args.Path) == "" {
			return ToolResult{}, fmt.Errorf("missing path argument: provide path (with old_text/new_text or edits[]) or files[] array")
		}
		// Multi-edit mode takes precedence
		if len(args.Edits) > 0 {
			output, err := executor.EditFileMulti(args.Path, args.EditsToToolingEdits())
			return ToolResult{Text: output}, err
		}
		// Legacy single-edit mode
		if args.OldText == "" {
			return ToolResult{}, fmt.Errorf("missing old_text argument: edit_file needs old_text and (optionally) new_text to locate text to replace.\n" +
				"Minimal usage: {\"path\":\"file.go\",\"old_text\":\"<exact existing text>\",\"new_text\":\"<replacement>\"}\n" +
				"- To APPEND to an existing file: pass the last line(s) of the current file verbatim as old_text, and set new_text to that anchor + your new content.\n" +
				"- To create a NEW file: use write_file ({\"path\":\"file.go\",\"content\":\"...\"}) instead of edit_file.\n" +
				"- Multiple replacements: use edits[] array (path + edits[]); multiple files: use files[] array.")
		}
		output, err := executor.EditFile(args.Path, args.OldText, args.NewText)
		return ToolResult{Text: output}, err
	})
}

func (a editFileArgs) EditsToToolingEdits() []tooling.FileEdit {
	edits := make([]tooling.FileEdit, len(a.Edits))
	for i, e := range a.Edits {
		edits[i] = tooling.FileEdit{OldText: e.OldText, NewText: e.NewText}
	}
	return edits
}

func (a editFileArgs) FilesToToolingBatches() []tooling.FileEditBatch {
	batches := make([]tooling.FileEditBatch, len(a.Files))
	for i, f := range a.Files {
		edits := make([]tooling.FileEdit, len(f.Edits))
		for j, e := range f.Edits {
			edits[j] = tooling.FileEdit{OldText: e.OldText, NewText: e.NewText}
		}
		batches[i] = tooling.FileEditBatch{Path: f.Path, Edits: edits}
	}
	return batches
}

// NewAttachFileTool creates a tool that explicitly promotes one local file into
// the current session reply as an attachment.
func NewAttachFileTool(workspace string) Tool {
	return NewAttachFileToolWithFS(nil, workspace)
}

// NewAttachFileToolWithFS creates an attach tool that reads through the given FS.
// If fs is nil, a local FS is created from workspace.
func NewAttachFileToolWithFS(fs workspacefs.FS, workspace string) Tool {
	return NewTypedTool(SpecFromDefinition(tooling.AttachFileDefinition(), nil), func(ctx context.Context, args attachFileArgs) (ToolResult, error) {
		_ = ctx
		if strings.TrimSpace(args.Path) == "" {
			return ToolResult{}, fmt.Errorf("missing path argument")
		}
		root := fs
		if root == nil {
			var err error
			root, err = workspacefs.New(workspace)
			if err != nil {
				return ToolResult{}, err
			}
			defer root.Close()
		}
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
