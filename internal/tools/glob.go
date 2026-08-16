package tools

import (
	"context"
	"fmt"
	"io/fs"
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

// globSkipDirs are derived/generated directories a coding agent never needs to
// scan with glob (mirrors the repo map skip set). Skipping them keeps
// recursive patterns like **/*{foo,bar}* fast on repositories that contain
// node_modules, vendored deps, or Go build caches — without the skip the tool
// walks tens of millions of files and appears hung for up to the tool timeout.
// A skipped directory is only skipped when nested: if the walk root IS one of
// these (root: "node_modules"), it is still scanned.
var globSkipDirs = map[string]bool{
	".git": true, ".godex": true, ".cache": true, ".next": true,
	"node_modules": true, "vendor": true, "dist": true, "build": true,
	"coverage": true, "tmp": true, "log": true,
}

func globSkipDir(name string) bool {
	if globSkipDirs[name] {
		return true
	}
	// Covers .godex-build-cache, .godex-state etc.
	return strings.HasPrefix(name, ".godex")
}

// NewGlobTool creates a new workspace-scoped glob tool.
func NewGlobTool(workspace string, defaultMaxResults int) Tool {
	return NewGlobToolWithFS(nil, workspace, defaultMaxResults)
}

// NewGlobToolWithFS creates a glob tool that reads through the given FS.
// If fs is nil, a local FS is created from workspace.
func NewGlobToolWithFS(fsys workspacefs.FS, workspace string, defaultMaxResults int) Tool {
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
		pattern := strings.TrimSpace(args.Pattern)
		if pattern == "" {
			return ToolResult{}, fmt.Errorf("missing pattern argument")
		}
		if !doublestar.ValidatePattern(pattern) {
			return ToolResult{}, fmt.Errorf("invalid glob pattern: %s", pattern)
		}
		root := "."
		if strings.TrimSpace(args.Root) != "" {
			root = strings.TrimSpace(args.Root)
		}
		maxResults := defaultMaxResults
		if args.MaxResults > 0 {
			maxResults = args.MaxResults
		}

		workspaceRoot := fsys
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
		// Manual ctx-aware walk: doublestar.FilepathGlob walks the entire tree
		// (tens of millions of files on node_modules / build caches) before
		// returning, cannot be cancelled, and follows symlinks. Walking
		// ourselves with Match lets us skip derived dirs, honor the context
		// (so the runner timeout actually stops the tool), stop early once
		// maxResults is reached, and never follow symlink loops.
		_ = filepath.WalkDir(rootAbs, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if entry.IsDir() {
				if path != rootAbs && globSkipDir(entry.Name()) {
					return filepath.SkipDir
				}
				if !args.IncludeDirs {
					return nil
				}
			}
			rel, relErr := filepath.Rel(rootAbs, path)
			if relErr != nil {
				return nil
			}
			ok, matchErr := doublestar.Match(pattern, filepath.ToSlash(rel))
			if matchErr != nil || !ok {
				return nil
			}
			if !pathInsideWorkspace(workspace, path) {
				return nil
			}
			relToWorkspace, relErr := filepath.Rel(workspace, path)
			if relErr != nil {
				return nil
			}
			matches = append(matches, filepath.ToSlash(relToWorkspace))
			if len(matches) >= maxResults {
				truncated = true
				return fs.SkipAll
			}
			return nil
		})
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ToolResult{}, ctxErr
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
