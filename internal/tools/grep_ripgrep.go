package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
)

// RipGrepBackend is a grep backend that delegates to ripgrep (rg).
type RipGrepBackend struct {
	workspace string
	rgPath    string
}

// NewRipGrepBackend creates a new ripgrep-backed grep implementation.
func NewRipGrepBackend(workspace, rgPath string) *RipGrepBackend {
	return &RipGrepBackend{workspace: workspace, rgPath: rgPath}
}

func (b *RipGrepBackend) Search(ctx context.Context, opts GrepOptions) (GrepResult, error) {
	pattern := strings.TrimSpace(opts.Pattern)
	if pattern == "" {
		return GrepResult{}, fmt.Errorf("missing pattern argument")
	}

	maxResults := opts.MaxResults

	args := b.buildArgs(pattern, opts)

	cmd := exec.CommandContext(ctx, b.rgPath, args...)
	cmd.Dir = b.workspace
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return GrepResult{}, fmt.Errorf("create pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return GrepResult{}, fmt.Errorf("start rg: %w", err)
	}

	matches, total, err := b.parseOutput(stdout, maxResults)

	waitErr := cmd.Wait()
	if err != nil {
		return GrepResult{}, err
	}
	// rg exits with code 0 (match found), 1 (no match), or 2 (error).
	if exitErr, ok := waitErr.(*exec.ExitError); ok {
		if exitErr.ExitCode() == 2 {
			return GrepResult{}, fmt.Errorf("rg search failed: %v", exitErr)
		}
	}

	truncated := total > maxResults
	if len(matches) > maxResults {
		matches = matches[:maxResults]
	}

	return GrepResult{
		Matches:      matches,
		TotalMatches: total,
		Truncated:    truncated,
	}, nil
}

func (b *RipGrepBackend) buildArgs(pattern string, opts GrepOptions) []string {
	args := []string{
		"--json",
		"--no-heading",
		"--line-number",
		"--color", "never",
		// --no-ignore: search ALL files including those in .gitignore/.ignore/.rgignore.
		// As a coding agent we need to see everything in the workspace.
		"--no-ignore",
		// --no-require-git: don't require the search root to be in a git repo.
		"--no-require-git",
	}

	if opts.CaseInsensitive {
		args = append(args, "-i")
	}

	if opts.Glob != "" {
		args = append(args, "--glob", opts.Glob)
	}

	args = append(args, "-e", pattern)

	searchPath := "."
	if strings.TrimSpace(opts.Path) != "" {
		searchPath = strings.TrimSpace(opts.Path)
	}
	args = append(args, searchPath)

	return args
}

// ── JSON parsing types ──

type rgJSON struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type rgMatchData struct {
	Path struct {
		Text string `json:"text"`
	} `json:"path"`
	Lines struct {
		Text string `json:"text"`
	} `json:"lines"`
	LineNumber int `json:"line_number"`
}

type rgSummaryData struct {
	Stats struct {
		Matches int `json:"matches"`
	} `json:"stats"`
}

// parseOutput reads rg --json lines, stopping early once maxResults matches
// have been collected. Summary events are still parsed for accurate total counts.
func (b *RipGrepBackend) parseOutput(stdout io.ReadCloser, maxResults int) (matches []grepMatch, total int, err error) {
	defer stdout.Close()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)

	collectMatches := true

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var event rgJSON
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}

		switch event.Type {
		case "match":
			var data rgMatchData
			if err := json.Unmarshal(event.Data, &data); err != nil {
				continue
			}

			total++

			if collectMatches {
				content := strings.TrimRight(data.Lines.Text, "\n\r")
				filePath := data.Path.Text
				if rel, relErr := filepath.Rel(b.workspace, filePath); relErr == nil && !strings.HasPrefix(rel, "..") {
					filePath = rel
				}

				matches = append(matches, grepMatch{
					File:    filepath.ToSlash(filePath),
					Line:    data.LineNumber,
					Content: truncateGrepLine(content, 500),
				})

				if len(matches) >= maxResults {
					collectMatches = false
					// Keep reading for summary events (accurate total), but
					// stop collecting match data.
				}
			}

		case "summary":
			if collectMatches {
				// We haven't hit the match limit yet — rg's summary is
				// authoritative for total.
				var data rgSummaryData
				if err := json.Unmarshal(event.Data, &data); err == nil && data.Stats.Matches > total {
					total = data.Stats.Matches
				}
			}
			// Once we've stopped collecting matches, we can also stop reading
			// entirely. The summary event is the last event rg emits.
			if !collectMatches {
				break
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return matches, total, fmt.Errorf("read rg output: %w", err)
	}

	return matches, total, nil
}
