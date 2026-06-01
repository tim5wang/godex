package tools

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tim5wang/godex/internal/platform/tooling"
	"github.com/tim5wang/godex/internal/platform/workspacefs"
)

const (
	maxGrepMatches   = 500
	defaultGrepMatch = 100
)

type grepArgs struct {
	Pattern        string `json:"pattern"`
	Path           string `json:"path,omitempty"`
	Glob           string `json:"glob,omitempty"`
	CaseInsensitive bool   `json:"case_insensitive,omitempty"`
	MaxResults     int    `json:"max_results,omitempty"`
}

type grepResult struct {
	Matches      []grepMatch `json:"matches"`
	TotalMatches int         `json:"total_matches"`
	Truncated    bool        `json:"truncated,omitempty"`
}

type grepMatch struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Content string `json:"content"`
}

// NewGrepTool creates a new grep tool for searching file contents.
func NewGrepTool(workspace string) Tool {
	return NewTypedTool(SpecFromDefinition(tooling.GrepDefinition(), nil), func(ctx context.Context, args grepArgs) (ToolResult, error) {
		_ = ctx
		pattern := strings.TrimSpace(args.Pattern)
		if pattern == "" {
			return ToolResult{}, fmt.Errorf("missing pattern argument")
		}

		maxResults := defaultGrepMatch
		if args.MaxResults > 0 {
			maxResults = args.MaxResults
		}
		if maxResults > maxGrepMatches {
			maxResults = maxGrepMatches
		}

		// Build the regex.
		flags := ""
		if args.CaseInsensitive {
			flags = "(?i)"
		}
		re, err := regexp.Compile(flags + pattern)
		if err != nil {
			return ToolResult{}, fmt.Errorf("invalid regex pattern: %w", err)
		}

		searchPath := "."
		if strings.TrimSpace(args.Path) != "" {
			searchPath = strings.TrimSpace(args.Path)
		}

		root, err := workspacefs.New(workspace)
		if err != nil {
			return ToolResult{}, err
		}
		defer root.Close()

		info, err := root.Stat(searchPath)
		if err != nil {
			return ToolResult{}, fmt.Errorf("path not found: %s: %w", searchPath, err)
		}

		var matches []grepMatch
		total := 0

		if info.IsDir() {
			absSearchDir, err := root.Abs(searchPath)
			if err != nil {
				return ToolResult{}, err
			}
			entries, err := root.ReadDir(searchPath)
			if err != nil {
				return ToolResult{}, err
			}
			walkGrepDir(root, absSearchDir, searchPath, entries, re, args.Glob, &matches, &total, maxResults)
		} else {
			fileMatches := grepFile(root, re, searchPath)
			total = len(fileMatches)
			matches = trimGrepMatches(fileMatches, maxResults)
		}

		return ToolResult{Structured: grepResult{
			Matches:      matches,
			TotalMatches: total,
			Truncated:    total > len(matches),
		}}, nil
	})
}

func walkGrepDir(root *workspacefs.FS, absDir, relDir string, entries []fs.DirEntry, re *regexp.Regexp, glob string, matches *[]grepMatch, total *int, maxResults int) {
	for _, entry := range entries {
		if len(*matches) >= maxResults {
			return
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "__pycache__" {
			continue
		}
		entryRel := filepath.Join(relDir, name)
		if entry.IsDir() {
			subEntries, err := root.ReadDir(entryRel)
			if err != nil {
				continue
			}
			walkGrepDir(root, filepath.Join(absDir, name), entryRel, subEntries, re, glob, matches, total, maxResults)
		} else {
			if glob != "" {
				matched, _ := filepath.Match(glob, name)
				if !matched {
					continue
				}
			}
			fileMatches := grepFile(root, re, entryRel)
			*total += len(fileMatches)
			remaining := maxResults - len(*matches)
			if remaining > 0 {
				if len(fileMatches) > remaining {
					fileMatches = fileMatches[:remaining]
				}
				*matches = append(*matches, fileMatches...)
			}
		}
	}
}

func grepFile(root *workspacefs.FS, re *regexp.Regexp, path string) []grepMatch {
	f, err := root.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var matches []grepMatch
	scanner := bufio.NewScanner(f)
	// Increase buffer for long lines.
	scanner.Buffer(make([]byte, 0, 256*1024), 2*1024*1024)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if re.MatchString(line) {
			matches = append(matches, grepMatch{
				File:    filepath.ToSlash(path),
				Line:    lineNum,
				Content: truncateGrepLine(line, 500),
			})
		}
	}
	return matches
}

func trimGrepMatches(matches []grepMatch, maxResults int) []grepMatch {
	if len(matches) > maxResults {
		return matches[:maxResults]
	}
	return matches
}

func truncateGrepLine(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
