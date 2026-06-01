package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/tim5wang/godex/internal/platform/tooling"
	"github.com/tim5wang/godex/internal/platform/workspacefs"
)

const maxLsEntries = 1000

type lsArgs struct {
	Path string `json:"path,omitempty"`
}

type lsEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

type lsResult struct {
	Entries      []lsEntry `json:"entries"`
	TotalEntries int       `json:"total_entries"`
	Truncated    bool      `json:"truncated,omitempty"`
}

// NewLsTool creates a new ls tool for listing directory contents.
func NewLsTool(workspace string) Tool {
	return NewTypedTool(SpecFromDefinition(tooling.LsDefinition(), nil), func(ctx context.Context, args lsArgs) (ToolResult, error) {
		_ = ctx
		dir := "."
		if strings.TrimSpace(args.Path) != "" {
			dir = strings.TrimSpace(args.Path)
		}

		root, err := workspacefs.New(workspace)
		if err != nil {
			return ToolResult{}, err
		}
		defer root.Close()

		info, err := root.Stat(dir)
		if err != nil {
			return ToolResult{}, fmt.Errorf("reading directory: %w", err)
		}
		if !info.IsDir() {
			// Single file: return its info.
			return ToolResult{Structured: lsResult{
				Entries: []lsEntry{{
					Name:  filepath.Base(dir),
					IsDir: false,
					Size:  info.Size(),
				}},
				TotalEntries: 1,
			}}, nil
		}

		entries, err := root.ReadDir(dir)
		if err != nil {
			return ToolResult{}, fmt.Errorf("reading directory: %w", err)
		}

		total := len(entries)
		if total > maxLsEntries {
			entries = entries[:maxLsEntries]
		}

		result := make([]lsEntry, 0, len(entries))
		for _, e := range entries {
			entryInfo, err := e.Info()
			if err != nil {
				continue
			}
			result = append(result, lsEntry{
				Name:  filepath.Join(dir, e.Name()),
				IsDir: e.IsDir(),
				Size:  entryInfo.Size(),
			})
		}

		return ToolResult{Structured: lsResult{
			Entries:      result,
			TotalEntries: total,
			Truncated:    total > len(result),
		}}, nil
	})
}
