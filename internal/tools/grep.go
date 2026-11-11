package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
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
	Pattern          string `json:"pattern"`
	Path             string `json:"path,omitempty"`
	Glob             string `json:"glob,omitempty"`
	CaseInsensitive  bool   `json:"case_insensitive,omitempty"`
	MaxResults       int    `json:"max_results,omitempty"`
	Offset           int    `json:"offset,omitempty"`
	IncludeGenerated bool   `json:"include_generated,omitempty"`
	TimeoutSeconds   int    `json:"timeout_seconds,omitempty"`
}

type grepResult struct {
	Matches                  []grepMatch `json:"matches"`
	TotalMatches             int         `json:"total_matches"`
	Truncated                bool        `json:"truncated,omitempty"`
	Offset                   int         `json:"offset"`
	NextOffset               *int        `json:"next_offset,omitempty"`
	TotalMatchesIsLowerBound bool        `json:"total_matches_is_lower_bound,omitempty"`
}

type grepMatch struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Content string `json:"content"`
}

// GrepOptions configures a search. MaxResults is a positive collection limit,
// including any skipped matches and pagination lookahead requested by the tool.
type GrepOptions struct {
	Pattern          string
	Path             string
	Glob             string
	CaseInsensitive  bool
	MaxResults       int
	IncludeGenerated bool
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
		matches, total, err := b.searchDir(ctx, root, searchPath, re, opts.Glob, maxResults, opts.IncludeGenerated || targetsGeneratedGrepPath(opts.Path))
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
		Truncated:    fileTotal > len(matches),
	}, nil
}

// searchDir walks a directory tree collecting up to maxResults regex matches.
// Returns (collected matches, total matches found, error).
func (b *GoGrepBackend) searchDir(ctx context.Context, root workspacefs.FS, relDir string, re *regexp.Regexp, glob string, maxResults int, includeGenerated bool) ([]grepMatch, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	entries, err := root.ReadDir(relDir)
	if err != nil {
		return nil, 0, nil // skip unreadable directories
	}
	// Offset pagination must not depend on filesystem enumeration order.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

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
		// Links and special files are not traversed during a broad search.
		if !entry.IsDir() && !entry.Type().IsRegular() {
			continue
		}
		if shouldSkipGrepEntry(name) {
			continue
		}
		if entry.IsDir() && !includeGenerated && generatedGrepDir(name) {
			continue
		}
		entryRel := filepath.Join(relDir, name)
		if entry.IsDir() {
			subMatches, subTotal, err := b.searchDir(ctx, root, entryRel, re, glob, maxResults-len(collected), includeGenerated)
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

func generatedGrepDir(name string) bool {
	switch name {
	case "target", "temp", "dist", "build", "coverage":
		return true
	}
	return false
}

func targetsGeneratedGrepPath(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(filepath.Clean(path)), "/") {
		if generatedGrepDir(part) {
			return true
		}
	}
	return false
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
	// Ignore binary/unreadable entries without spending a full scan on build
	// products or aborting all source matches on a directory-like entry.
	var sample [4096]byte
	n, sampleErr := f.Read(sample[:])
	if sampleErr != nil && sampleErr != io.EOF {
		return nil, 0, nil
	}
	if bytes.IndexByte(sample[:n], 0) >= 0 {
		return nil, 0, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, 0, err
	}

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
		if strings.IndexByte(line, 0) >= 0 {
			return nil, 0, nil
		}
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
		if args.Offset < 0 || args.Offset > 10000 {
			return ToolResult{}, fmt.Errorf("offset must be between 0 and 10000; narrow the pattern for deeper searches")
		}
		searchCtx, cancel := withOptionalTimeout(ctx, args.TimeoutSeconds)
		defer cancel()

		result, err := backend.Search(searchCtx, GrepOptions{
			Pattern:         args.Pattern,
			Path:            args.Path,
			Glob:            args.Glob,
			CaseInsensitive: args.CaseInsensitive,
			// One lookahead match distinguishes a full final page from a
			// truncated page, including directory backends that stop early.
			MaxResults:       args.Offset + maxResults + 1,
			IncludeGenerated: args.IncludeGenerated,
		})
		if err != nil {
			return ToolResult{}, err
		}

		start := min(args.Offset, len(result.Matches))
		end := min(args.Offset+maxResults, len(result.Matches))
		truncated := len(result.Matches) > end || result.TotalMatches > args.Offset+maxResults || result.Truncated
		var next *int
		if truncated && end > start {
			value := args.Offset + end - start
			next = &value
		}
		page := grepResult{
			Matches:                  result.Matches[start:end],
			TotalMatches:             result.TotalMatches,
			Truncated:                truncated,
			Offset:                   args.Offset,
			NextOffset:               next,
			TotalMatchesIsLowerBound: result.Truncated || len(result.Matches) == args.Offset+maxResults+1,
		}
		// Keep each page below the model-visible artifact threshold so its
		// continuation cursor and line numbers remain directly available.
		for len(page.Matches) > 1 {
			data, marshalErr := json.Marshal(page)
			if marshalErr != nil {
				return ToolResult{}, marshalErr
			}
			if len(data) <= 24*1024 {
				break
			}
			page.Matches = page.Matches[:len(page.Matches)/2]
			page.Truncated = true
			value := args.Offset + len(page.Matches)
			page.NextOffset = &value
		}
		return ToolResult{Structured: page}, nil
	})
}

func truncateGrepLine(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
