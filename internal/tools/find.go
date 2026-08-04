package tools

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tim5wang/godex/internal/platform/tooling"
	"github.com/tim5wang/godex/internal/platform/workspacefs"
)

const (
	maxFindResults    = 1000
	defaultFindResult = 200
)

type findArgs struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path,omitempty"`
	MaxResults int    `json:"max_results,omitempty"`
}

type findResult struct {
	Root       string   `json:"root"`
	Files      []string `json:"files"`
	TotalFiles int      `json:"total_files"`
	Truncated  bool     `json:"truncated,omitempty"`
}

// NewFindTool creates a new find tool for locating files by glob pattern.
func NewFindTool(workspace string) Tool {
	return NewFindToolWithFS(nil, workspace)
}

// NewFindToolWithFS creates a find tool that reads through the given FS.
// If fs is nil, a local FS is created from workspace.
func NewFindToolWithFS(fs workspacefs.FS, workspace string) Tool {
	return NewTypedTool(SpecFromDefinition(tooling.FindDefinition(), nil), func(ctx context.Context, args findArgs) (ToolResult, error) {
		_ = ctx
		pattern := strings.TrimSpace(args.Pattern)
		if pattern == "" {
			return ToolResult{}, fmt.Errorf("missing pattern argument")
		}

		maxResults := defaultFindResult
		if args.MaxResults > 0 {
			maxResults = args.MaxResults
		}
		if maxResults > maxFindResults {
			maxResults = maxFindResults
		}

		searchPath := "."
		if strings.TrimSpace(args.Path) != "" {
			searchPath = strings.TrimSpace(args.Path)
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

		info, err := root.Stat(searchPath)
		if err != nil {
			return ToolResult{}, fmt.Errorf("path not found: %s: %w", searchPath, err)
		}
		if !info.IsDir() {
			return ToolResult{}, fmt.Errorf("path must be a directory: %s", searchPath)
		}

		entries, err := root.ReadDir(searchPath)
		if err != nil {
			return ToolResult{}, err
		}

		// Normalize pattern: strip leading **/ since we recurse anyway.
		filePattern := normalizeGlobPattern(pattern)

		var files []string
		total := 0
		walkFindDir(root, searchPath, entries, pattern, filePattern, &files, &total, maxResults)
		sort.Strings(files)

		if len(files) > maxResults {
			files = files[:maxResults]
		}

		return ToolResult{Structured: findResult{
			Root:       filepath.ToSlash(searchPath),
			Files:      files,
			TotalFiles: total,
			Truncated:  total > len(files),
		}}, nil
	})
}

func walkFindDir(root workspacefs.FS, relDir string, entries []fs.DirEntry, originalPattern, filePattern string, files *[]string, total *int, maxResults int) {
	for _, entry := range entries {
		if len(*files) >= maxResults {
			return
		}
		name := entry.Name()
		if shouldSkipDirEntry(name) && entry.IsDir() {
			continue
		}
		entryRel := filepath.Join(relDir, name)
		if entryRel == "" {
			continue
		}
		if entry.IsDir() {
			subEntries, err := root.ReadDir(entryRel)
			if err != nil {
				continue
			}
			walkFindDir(root, entryRel, subEntries, originalPattern, filePattern, files, total, maxResults)
		} else {
			// Match against filename.
			matched, _ := filepath.Match(filePattern, name)
			if !matched {
				// Try full relative path match for patterns like "src/*.go".
				matched, _ = filepath.Match(filePattern, entryRel)
				if !matched {
					// Try doublestar matching for patterns like "src/**/*.go".
					matched = matchDoublestar(originalPattern, entryRel)
				}
			}
			if matched {
				*total++
				if len(*files) < maxResults {
					*files = append(*files, filepath.ToSlash(entryRel))
				}
			}
		}
	}
}

func shouldSkipDirEntry(name string) bool {
	if strings.HasPrefix(name, ".") && name != "." && name != ".godex" {
		return true
	}
	return name == "node_modules" || name == "vendor" || name == "__pycache__"
}

func normalizeGlobPattern(pattern string) string {
	for strings.HasPrefix(pattern, "**/") {
		pattern = pattern[3:]
	}
	return pattern
}

func matchDoublestar(pattern, path string) bool {
	parts := strings.Split(pattern, "**/")
	if len(parts) < 2 {
		return false
	}
	prefix := parts[0]
	if prefix != "" {
		prefix = strings.TrimSuffix(prefix, "/")
		if !strings.HasPrefix(path, prefix+"/") && path != prefix {
			return false
		}
	}
	suffix := parts[len(parts)-1]
	if suffix != "" {
		name := filepath.Base(path)
		matched, _ := filepath.Match(suffix, name)
		return matched
	}
	return true
}

// Ensure fs import is used.
var _ fs.DirEntry
