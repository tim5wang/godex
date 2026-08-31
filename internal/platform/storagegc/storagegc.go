package storagegc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/platform/fsutil"
)

const (
	CategoryBrowserCache      = "browser_cache"
	CategoryWebFetchSpill     = "web_fetch_spill"
	CategoryToolResult        = "tool_result"
	CategorySessionCheckpoint = "session_checkpoint"
)

type Options struct {
	StateDir                    string
	TempDir                     string
	SessionsDir                 string
	DryRun                      bool
	Now                         time.Time
	ArtifactTTL                 time.Duration
	SessionCheckpointTTL        time.Duration
	SessionCheckpointKeepLatest int
	ActiveSessionIDs            []string
}

type Item struct {
	Category string `json:"category"`
	Path     string `json:"path"`
	Bytes    int64  `json:"bytes"`
	Reason   string `json:"reason,omitempty"`
	Risk     string `json:"risk,omitempty"`
	Action   string `json:"action,omitempty"`
}

type Result struct {
	Items      []Item `json:"items"`
	Candidates int    `json:"candidates"`
	Bytes      int64  `json:"bytes"`
}

func Scan(opts Options) Result {
	opts = normalizeOptions(opts)
	var result Result
	result.Items = append(result.Items, scanBrowserCache(opts)...)
	result.Items = append(result.Items, scanWebFetchSpills(opts)...)
	result.Items = append(result.Items, scanToolResults(opts)...)
	result.Items = append(result.Items, scanSessionCheckpoints(opts)...)
	sortItems(result.Items)
	for _, item := range result.Items {
		result.Candidates++
		result.Bytes += item.Bytes
	}
	return result
}

func CleanBrowserCache(opts Options) (Result, error) {
	opts = normalizeOptions(opts)
	items := scanBrowserCache(opts)
	return cleanItems(items, opts.DryRun)
}

func CleanArtifacts(opts Options) (Result, error) {
	opts = normalizeOptions(opts)
	cutoff := opts.Now.Add(-opts.ArtifactTTL)
	active := make(map[string]struct{}, len(opts.ActiveSessionIDs))
	for _, id := range opts.ActiveSessionIDs {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			active[trimmed] = struct{}{}
		}
	}
	var items []Item
	for _, item := range append(scanWebFetchSpills(opts), scanToolResults(opts)...) {
		if isActiveToolResult(item.Path, active) {
			continue
		}
		info, err := os.Stat(item.Path)
		if err != nil || info.IsDir() || info.ModTime().After(cutoff) {
			continue
		}
		item.Action = "delete"
		item.Reason = "artifact older than retention and not referenced by active session"
		items = append(items, item)
	}
	sortItems(items)
	return cleanItems(items, opts.DryRun)
}

func CleanSessionCheckpoints(opts Options) (Result, error) {
	opts = normalizeOptions(opts)
	var items []Item
	entries, err := os.ReadDir(opts.SessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return Result{}, nil
		}
		return Result{}, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sessionDir := filepath.Join(opts.SessionsDir, entry.Name())
		items = append(items, pruneCandidatesForSession(sessionDir, opts)...)
	}
	sortItems(items)
	return cleanItems(items, opts.DryRun)
}

func normalizeOptions(opts Options) Options {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	if strings.TrimSpace(opts.TempDir) == "" && strings.TrimSpace(opts.StateDir) != "" {
		opts.TempDir = filepath.Join(opts.StateDir, ".tmp")
	}
	if strings.TrimSpace(opts.SessionsDir) == "" && strings.TrimSpace(opts.StateDir) != "" {
		opts.SessionsDir = filepath.Join(opts.StateDir, ".sessions")
	}
	if opts.ArtifactTTL <= 0 {
		opts.ArtifactTTL = 168 * time.Hour
	}
	if opts.SessionCheckpointTTL <= 0 {
		opts.SessionCheckpointTTL = 168 * time.Hour
	}
	if opts.SessionCheckpointKeepLatest <= 0 {
		opts.SessionCheckpointKeepLatest = 20
	}
	return opts
}

func scanBrowserCache(opts Options) []Item {
	browserRoot := filepath.Join(opts.TempDir, "browser", "user-data")
	targets := []string{
		"optimization_guide_model_store",
		"component_crx_cache",
		"WasmTtsEngine",
		"ShaderCache",
		"GrShaderCache",
		filepath.Join("Default", "Cache"),
		filepath.Join("Default", "Code Cache"),
	}
	var items []Item
	for _, rel := range targets {
		path := filepath.Join(browserRoot, rel)
		if bytes := fsutil.DirSizeBestEffort(path); bytes > 0 {
			items = append(items, Item{
				Category: CategoryBrowserCache,
				Path:     path,
				Bytes:    bytes,
				Reason:   "rebuildable browser cache",
				Risk:     "low",
				Action:   "delete",
			})
		}
	}
	return items
}

func scanWebFetchSpills(opts Options) []Item {
	return scanFiles(filepath.Join(opts.TempDir, "web_fetch"), CategoryWebFetchSpill, "web_fetch spill artifact")
}

func scanToolResults(opts Options) []Item {
	return scanFiles(filepath.Join(opts.StateDir, ".tool-results"), CategoryToolResult, "large tool result artifact")
}

func scanSessionCheckpoints(opts Options) []Item {
	return scanDirs(filepath.Join(opts.SessionsDir), "checkpoints", CategorySessionCheckpoint, "session recovery checkpoint")
}

func scanFiles(root, category, reason string) []Item {
	var items []Item
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		info, statErr := entry.Info()
		if statErr != nil {
			return nil
		}
		items = append(items, Item{Category: category, Path: path, Bytes: info.Size(), Reason: reason, Risk: "medium", Action: "delete"})
		return nil
	})
	return items
}

func scanDirs(root, targetName, category, reason string) []Item {
	var items []Item
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || !entry.IsDir() || entry.Name() != targetName {
			return nil
		}
		children, readErr := os.ReadDir(path)
		if readErr != nil {
			return filepath.SkipDir
		}
		for _, child := range children {
			if !child.IsDir() {
				continue
			}
			childPath := filepath.Join(path, child.Name())
			if bytes := fsutil.DirSizeBestEffort(childPath); bytes > 0 {
				items = append(items, Item{Category: category, Path: childPath, Bytes: bytes, Reason: reason, Risk: "medium", Action: "delete"})
			}
		}
		return filepath.SkipDir
	})
	return items
}

func pruneCandidatesForSession(sessionDir string, opts Options) []Item {
	checkpointsDir := filepath.Join(sessionDir, "checkpoints")
	entries, err := os.ReadDir(checkpointsDir)
	if err != nil {
		return nil
	}
	pointer := readCheckpointPointer(filepath.Join(sessionDir, "checkpoint.json"))
	type checkpoint struct {
		id   string
		path string
		at   time.Time
		size int64
	}
	var checkpoints []checkpoint
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(checkpointsDir, entry.Name())
		checkpoints = append(checkpoints, checkpoint{
			id:   entry.Name(),
			path: path,
			at:   checkpointTime(entry.Name()),
			size: fsutil.DirSizeBestEffort(path),
		})
	}
	sort.Slice(checkpoints, func(i, j int) bool {
		if !checkpoints[i].at.Equal(checkpoints[j].at) {
			return checkpoints[i].at.After(checkpoints[j].at)
		}
		return checkpoints[i].id > checkpoints[j].id
	})
	keep := map[string]struct{}{}
	if pointer != "" {
		keep[pointer] = struct{}{}
	}
	for i, checkpoint := range checkpoints {
		if i < opts.SessionCheckpointKeepLatest {
			keep[checkpoint.id] = struct{}{}
		}
	}
	cutoff := opts.Now.Add(-opts.SessionCheckpointTTL)
	var items []Item
	for _, checkpoint := range checkpoints {
		if _, ok := keep[checkpoint.id]; ok {
			continue
		}
		reason := "checkpoint outside latest checkpoint retention window"
		if !checkpoint.at.IsZero() && checkpoint.at.Before(cutoff) {
			reason = "checkpoint older than retention and outside latest checkpoint window"
		}
		items = append(items, Item{
			Category: CategorySessionCheckpoint,
			Path:     checkpoint.path,
			Bytes:    checkpoint.size,
			Reason:   reason,
			Risk:     "medium",
			Action:   "delete",
		})
	}
	return items
}

func readCheckpointPointer(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var payload struct {
		Current string `json:"current"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return ""
	}
	if payload.Current != filepath.Base(payload.Current) {
		return ""
	}
	return strings.TrimSpace(payload.Current)
}

func checkpointTime(id string) time.Time {
	prefix := id
	if idx := strings.Index(prefix, "-"); idx > 0 {
		prefix = prefix[:idx]
	}
	parsed, err := time.Parse("20060102T150405.000000000Z", prefix)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func cleanItems(items []Item, dryRun bool) (Result, error) {
	var result Result
	for _, item := range items {
		result.Candidates++
		result.Bytes += item.Bytes
		if !dryRun {
			if err := os.RemoveAll(item.Path); err != nil {
				return result, fmt.Errorf("remove %s: %w", item.Path, err)
			}
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func isActiveToolResult(path string, active map[string]struct{}) bool {
	if len(active) == 0 {
		return false
	}
	dir := filepath.Base(filepath.Dir(path))
	_, ok := active[dir]
	return ok
}

func sortItems(items []Item) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Category != items[j].Category {
			return items[i].Category < items[j].Category
		}
		if items[i].Bytes != items[j].Bytes {
			return items[i].Bytes > items[j].Bytes
		}
		return items[i].Path < items[j].Path
	})
}
