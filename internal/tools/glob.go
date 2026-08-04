package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/tim5wang/godex/internal/platform/workspacefs"
)

type globResult struct {
	Root      string   `json:"root"`
	Matches   []string `json:"matches"`
	Truncated bool     `json:"truncated"`
}

type globArgs struct {
	Pattern     string `json:"pattern"`
	Root        string `json:"root,omitempty"`
	IncludeDirs bool   `json:"include_dirs,omitempty"`
	MaxResults  int    `json:"max_results,omitempty"`
}

// NewGlobTool creates a new workspace-scoped glob tool.
func NewGlobTool(workspace string, defaultMaxResults int) Tool {
	return NewGlobToolWithFS(nil, workspace, defaultMaxResults)
}

// NewGlobToolWithFS creates a glob tool that reads through the given FS.
// If fs is nil, a local FS is created from workspace.
func NewGlobToolWithFS(fs workspacefs.FS, workspace string, defaultMaxResults int) Tool {
		if defaultMaxResults <= 0 {
			defaultMaxResults = 200
		}
		spec := NewToolSpec("glob", "Find workspace files with doublestar glob patterns such as **/*.go", map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"pattern": map[string]interface{}{
					"type":        "string",
					"description": "Glob pattern such as **/*.go or cmd/*/main.go",
				},
				"root": map[string]interface{}{
					"type":        "string",
					"description": "Optional workspace-relative root directory. Default: .",
				},
				"include_dirs": map[string]interface{}{
					"type":        "boolean",
					"description": "Include directories in the results. Default: false",
				},
				"max_results": map[string]interface{}{
					"type":        "integer",
					"description": "Optional maximum number of matches to return.",
				},
			},
			"required": []string{"pattern"},
		}, nil)
		return NewTypedTool(spec, func(ctx context.Context, args globArgs) (ToolResult, error) {
			_ = ctx
			pattern := strings.TrimSpace(args.Pattern)
			if pattern == "" {
				return ToolResult{}, fmt.Errorf("missing pattern argument")
			}
			root := "."
			if strings.TrimSpace(args.Root) != "" {
				root = strings.TrimSpace(args.Root)
			}
			maxResults := defaultMaxResults
			if args.MaxResults > 0 {
				maxResults = args.MaxResults
			}

			workspaceRoot := fs
			if workspaceRoot == nil {
				var err error
				workspaceRoot, err = workspacefs.New(workspace)
				if err != nil {
					return ToolResult{}, err
				}
				defer workspaceRoot.Close()
			}
		rootAbs, err := workspaceRoot.Abs(root)
		if err != nil {
			return ToolResult{}, err
		}
		info, err := workspaceRoot.Stat(root)
		if err != nil {
			return ToolResult{}, err
		}
		if !info.IsDir() {
			return ToolResult{}, fmt.Errorf("root must resolve to a directory")
		}

		pattern = filepath.ToSlash(pattern)
		matches := make([]string, 0, minInt(maxResults, 64))
		truncated := false
		absoluteMatches, err := doublestar.FilepathGlob(filepath.Join(rootAbs, filepath.FromSlash(pattern)))
		if err != nil {
			return ToolResult{}, err
		}
		for _, absolutePath := range absoluteMatches {
			info, err := os.Stat(absolutePath)
			if err != nil {
				continue
			}
			if !args.IncludeDirs && info.IsDir() {
				continue
			}
			if !pathInsideWorkspace(workspace, absolutePath) {
				continue
			}
			rel, err := filepath.Rel(workspace, absolutePath)
			if err != nil {
				continue
			}
			matches = append(matches, filepath.ToSlash(rel))
			if len(matches) >= maxResults {
				truncated = true
				break
			}
		}
		sort.Strings(matches)
		return ToolResult{Structured: globResult{
			Root:      filepath.ToSlash(root),
			Matches:   matches,
			Truncated: truncated,
		}}, nil
	})
}

func pathInsideWorkspace(workspace, target string) bool {
	workspace = filepath.Clean(workspace)
	if resolved, err := filepath.EvalSymlinks(workspace); err == nil {
		workspace = resolved
	}
	target = filepath.Clean(target)
	evaluated := target
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		evaluated = resolved
	}
	rel, err := filepath.Rel(workspace, evaluated)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
