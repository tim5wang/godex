package tools

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/platform/tooling"
	"github.com/tim5wang/godex/internal/platform/workspacefs"
)

const (
	maxGrepMatches   = 500
	defaultGrepMatch = 100
)

type grepArgs struct {
	Pattern         string `json:"pattern"`
	Path            string `json:"path,omitempty"`
	Glob            string `json:"glob,omitempty"`
	CaseInsensitive bool   `json:"case_insensitive,omitempty"`
	MaxResults      int    `json:"max_results,omitempty"`
	TimeoutSeconds  int    `json:"timeout_seconds,omitempty"`
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

// GrepOptions configures a grep search. MaxResults must already be clamped
// by the caller to [1, maxGrepMatches].
type GrepOptions struct {
	Pattern         string
	Path            string
	Glob            string
	CaseInsensitive bool
	MaxResults      int
}

// GrepResult contains the search results from a backend.
type GrepResult struct {
	Matches      []grepMatch
	TotalMatches int
	Truncated    bool
}

// GrepBackend is the interface for grep search implementations.
type GrepBackend interface {
	// Search performs a regex search. The caller guarantees opts.MaxResults is
	// already clamped and non-zero.
	Search(ctx context.Context, opts GrepOptions) (GrepResult, error)
}

// ──────────────────────────────────────────────────────────────
// GoGrepBackend (pure Go regexp)
// ──────────────────────────────────────────────────────────────

// regexCacheEntry stores a compiled regex with a TTL.
type regexCacheEntry struct {
	re      *regexp.Regexp
	created time.Time
}

// grepRegexCache is an LRU-style cache for compiled regex patterns,
// shared across all GoGrepBackend searches.
type grepRegexCache struct {
	mu      sync.Mutex
	entries map[string]*regexCacheEntry
	maxSize int
	maxAge  time.Duration
}

var sharedGrepRegexCache = &grepRegexCache{
	entries: make(map[string]*regexCacheEntry),
	maxSize: 50,
	maxAge:  10 * time.Minute,
}

func (c *grepRegexCache) get(key string) *regexp.Regexp {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil
	}
	if time.Since(entry.created) > c.maxAge {
		delete(c.entries, key)
		return nil
	}
	return entry.re
}

func (c *grepRegexCache) put(key string, re *regexp.Regexp) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= c.maxSize {
		var oldest string
		var oldestTime time.Time
		for k, e := range c.entries {
			if oldestTime.IsZero() || e.created.Before(oldestTime) {
				oldest = k
				oldestTime = e.created
			}
		}
		if oldest != "" {
			delete(c.entries, oldest)
		}
	}
	c.entries[key] = &regexCacheEntry{re: re, created: time.Now()}
}

// GoGrepBackend is a pure Go grep implementation using regexp.
type GoGrepBackend struct {
	workspace string
	fs        workspacefs.FS // optional pre-created FS; takes precedence over workspace
}

// NewGoGrepBackend creates a new pure Go grep backend.
func NewGoGrepBackend(workspace string) *GoGrepBackend {
	return &GoGrepBackend{workspace: workspace}
}

// NewGoGrepBackendWithFS creates a Go grep backend that reads files through fs.
func NewGoGrepBackendWithFS(fs workspacefs.FS) *GoGrepBackend {
	return &GoGrepBackend{fs: fs, workspace: fs.Dir()}
}

func (b *GoGrepBackend) fsForSearch() workspacefs.FS {
	if b.fs != nil {
		return b.fs
	}
	root, _ := workspacefs.New(b.workspace)
	return root
}

func (b *GoGrepBackend) Search(ctx context.Context, opts GrepOptions) (GrepResult, error) {
	if err := ctx.Err(); err != nil {
		return GrepResult{}, err
	}
	cacheKey := opts.Pattern
	if opts.CaseInsensitive {
		cacheKey = "(?i)" + cacheKey
	}

	re := sharedGrepRegexCache.get(cacheKey)
	if re == nil {
		flags := ""
		if opts.CaseInsensitive {
			flags = "(?i)"
		}
		var err error
		re, err = regexp.Compile(flags + opts.Pattern)
		if err != nil {
			return GrepResult{}, fmt.Errorf("invalid regex pattern: %w", err)
		}
		sharedGrepRegexCache.put(cacheKey, re)
	}

	searchPath := "."
	if strings.TrimSpace(opts.Path) != "" {
		searchPath = strings.TrimSpace(opts.Path)
	}

	root := b.fsForSearch()
	if root == nil {
		return GrepResult{}, fmt.Errorf("workspace fs unavailable")
	}

	info, err := root.Stat(searchPath)
	if err != nil {
		return GrepResult{}, fmt.Errorf("path not found: %s: %w", searchPath, err)
	}

	maxResults := opts.MaxResults

	if info.IsDir() {
		matches, total, err := b.searchDir(ctx, root, searchPath, re, opts.Glob, maxResults)
		if err != nil {
			return GrepResult{}, err
		}
		return GrepResult{
			Matches:      matches,
			TotalMatches: total,
			Truncated:    total > len(matches),
		}, nil
	}

	matches, fileTotal, err := grepFile(ctx, root, re, searchPath, maxResults)
	if err != nil {
		return GrepResult{}, err
	}
	return GrepResult{
		Matches:      matches,
		TotalMatches: fileTotal,
		Truncated:    false,
	}, nil
}

// searchDir walks a directory tree collecting up to maxResults regex matches.
// Returns (collected matches, total matches found, error).
func (b *GoGrepBackend) searchDir(ctx context.Context, root workspacefs.FS, relDir string, re *regexp.Regexp, glob string, maxResults int) ([]grepMatch, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	entries, err := root.ReadDir(relDir)
	if err != nil {
		return nil, 0, nil // skip unreadable directories
	}

	collected := make([]grepMatch, 0, min(maxResults, 64))
	total := 0

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return collected, total, err
		}
		if len(collected) >= maxResults {
			break
		}
		name := entry.Name()
		if shouldSkipGrepEntry(name) {
			continue
		}
		entryRel := filepath.Join(relDir, name)
		if entry.IsDir() {
			subMatches, subTotal, err := b.searchDir(ctx, root, entryRel, re, glob, maxResults-len(collected))
			if err != nil {
				return collected, total, err
			}
			total += subTotal
			collected = append(collected, subMatches...)
		} else {
			if glob != "" {
				matched, _ := filepath.Match(glob, name)
				if !matched {
					continue
				}
			}
			fileMatches, fileTotal, err := grepFile(ctx, root, re, entryRel, maxResults-len(collected))
			if err != nil {
				return collected, total, err
			}
			total += fileTotal
			collected = append(collected, fileMatches...)
		}
	}
	return collected, total, nil
}

func shouldSkipGrepEntry(name string) bool {
	return strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "__pycache__"
}

// grepFile searches a single file for regex matches.
// collectLimit caps how many matches are returned; total still counts all matches.
func grepFile(ctx context.Context, root workspacefs.FS, re *regexp.Regexp, path string, collectLimit int) ([]grepMatch, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	f, err := root.Open(path)
	if err != nil {
		return nil, 0, nil
	}
	defer f.Close()

	matches := make([]grepMatch, 0, min(collectLimit, 32))
	total := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 256*1024), 2*1024*1024)
	lineNum := 0
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return matches, total, err
		}
		lineNum++
		line := scanner.Text()
		if re.MatchString(line) {
			total++
			if len(matches) < collectLimit {
				matches = append(matches, grepMatch{
					File:    filepath.ToSlash(path),
					Line:    lineNum,
					Content: truncateGrepLine(line, 500),
				})
			}
		}
	}
	return matches, total, scanner.Err()
}

// ──────────────────────────────────────────────────────────────
// Tool construction
// ──────────────────────────────────────────────────────────────

// newGrepBackend creates the best available grep backend.
// Prefers ripgrep if available, falls back to pure Go regexp.
func newGrepBackend(workspace string) GrepBackend {
	if rgPath, err := exec.LookPath("rg"); err == nil {
		return NewRipGrepBackend(workspace, rgPath)
	}
	return NewGoGrepBackend(workspace)
}

// NewGrepTool creates a new grep tool with automatic backend selection.
func NewGrepTool(workspace string) Tool {
	return NewGrepToolWithFS(nil, workspace)
}

// NewGrepToolWithFS creates a grep tool that reads files through fs.
// If fs is nil, a local FS is created from workspace.
func NewGrepToolWithFS(fs workspacefs.FS, workspace string) Tool {
	var backend GrepBackend
	if fs != nil {
		backend = NewGoGrepBackendWithFS(fs)
	} else {
		backend = newGrepBackend(workspace)
	}
	return NewGrepToolWithBackend(backend)
}

// NewGrepToolWithBackend creates a grep tool using a specific backend (for testing).
func NewGrepToolWithBackend(backend GrepBackend) Tool {
	return NewTypedTool(SpecFromDefinition(tooling.GrepDefinition(), nil), func(ctx context.Context, args grepArgs) (ToolResult, error) {
		pattern := strings.TrimSpace(args.Pattern)
		if pattern == "" {
			return ToolResult{}, fmt.Errorf("missing pattern argument")
		}

		// Clamp maxResults once here; backends trust the clamped value.
		maxResults := defaultGrepMatch
		if args.MaxResults > 0 {
			maxResults = args.MaxResults
		}
		if maxResults > maxGrepMatches {
			maxResults = maxGrepMatches
		}
		searchCtx, cancel := withOptionalTimeout(ctx, args.TimeoutSeconds)
		defer cancel()

		result, err := backend.Search(searchCtx, GrepOptions{
			Pattern:         args.Pattern,
			Path:            args.Path,
			Glob:            args.Glob,
			CaseInsensitive: args.CaseInsensitive,
			MaxResults:      maxResults,
		})
		if err != nil {
			return ToolResult{}, err
		}

		return ToolResult{Structured: grepResult{
			Matches:      result.Matches,
			TotalMatches: result.TotalMatches,
			Truncated:    result.Truncated,
		}}, nil
	})
}

func truncateGrepLine(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
