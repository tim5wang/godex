package agent

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/compress"
)

const (
	repoMapTokenBudget     = 2500
	repoMapEntryLimit      = 160
	repoMapChangeNoteLimit = 12
	repoMapQueryFocusLimit = 8
)

// repoMapEntry is one deterministic repo map line plus the stat fingerprint
// used to detect workspace changes for the change note.
type repoMapEntry struct {
	path    string // workspace-relative slash path
	symbols string // exported Go symbol summary ("" for non-Go files)
	size    int64
	mtime   time.Time
}

func (e repoMapEntry) render() string {
	if e.symbols != "" {
		return "- " + e.path + " :: " + e.symbols
	}
	return "- " + e.path
}

// repoMapCache is the session-scoped snapshot of the workspace map. It is
// rebuilt only at session start and after compaction, so the rendered text —
// which sits BEFORE conversation history in the prompt — stays byte-stable
// across turns and file edits, keeping the provider prefix cache intact. File
// edits are surfaced as a small change note AFTER history instead.
type repoMapCache struct {
	workspaceDir string
	entries      []repoMapEntry
	text         string
}

// repoMapSnapshot returns the cached deterministic repo map section, rebuilding
// it when the cache is empty (session start), the workspace changed, or force
// is set (compaction boundary where the prefix cache breaks anyway). The text
// is query-independent: sorting is by path, never by per-turn relevance.
func (a *Agent) repoMapSnapshot(force bool) (string, []repoMapEntry) {
	a.repoMapMu.Lock()
	defer a.repoMapMu.Unlock()
	workspace := ""
	if a != nil && a.cfg != nil {
		workspace = strings.TrimSpace(a.cfg.WorkspaceDir)
	}
	if !force && a.repoMap.text != "" && a.repoMap.workspaceDir == workspace {
		return a.repoMap.text, a.repoMap.entries
	}
	entries := collectRepoMapEntries(workspace)
	a.repoMap = repoMapCache{workspaceDir: workspace, entries: entries, text: renderRepoMap(entries)}
	return a.repoMap.text, a.repoMap.entries
}

// repoMapSnapshotText returns the cached deterministic repo map section text
// (lazy rebuild at session start / workspace change). It is used for the
// quasi-stable "repo_map" runtime section placed before conversation history.
func (a *Agent) repoMapSnapshotText() string {
	text, _ := a.repoMapSnapshot(false)
	return text
}

// repoMapInvalidate clears the snapshot so the next access rebuilds it. Called
// at compaction boundaries: the history prefix cache breaks there anyway, so
// refreshing the map text is free and resets the accumulated change note.
func (a *Agent) repoMapInvalidate() {
	a.repoMapMu.Lock()
	defer a.repoMapMu.Unlock()
	a.repoMap = repoMapCache{}
}

// repoMapChangeNote diffs the current workspace against the session snapshot
// and renders a bounded "# Repo Map Changes" note for the volatile tail.
func (a *Agent) repoMapChangeNote() string {
	_, snapshot := a.repoMapSnapshot(false)
	current := collectRepoMapEntries(repoMapWorkspaceDir(a))
	return renderRepoMapChangeNote(snapshot, current)
}

// repoMapQueryFocus renders up to repoMapQueryFocusLimit query-relevant paths
// for the volatile tail. It preserves the old query-aware value of the repo map
// (relevant file pointers) without letting per-turn relevance churn the stable
// snapshot text in front of history.
func (a *Agent) repoMapQueryFocus(query string) string {
	return renderRepoMapQueryFocus(collectRepoMapEntries(repoMapWorkspaceDir(a)), query)
}

func repoMapWorkspaceDir(a *Agent) string {
	if a == nil || a.cfg == nil {
		return ""
	}
	return strings.TrimSpace(a.cfg.WorkspaceDir)
}

func renderRepoMap(entries []repoMapEntry) string {
	if len(entries) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("# Repo Map\n")
	builder.WriteString("Lightweight coding context. Use read/search tools for exact file contents.\n")
	for _, entry := range entries {
		next := builder.String() + entry.render() + "\n"
		if compress.CountTokens(next) > repoMapTokenBudget {
			break
		}
		builder.WriteString(entry.render())
		builder.WriteByte('\n')
	}
	return strings.TrimSpace(builder.String())
}

// collectRepoMapEntries walks the workspace and returns deterministic entries:
// paths are sorted lexically (never by query), Go files carry an exported
// symbol summary, and each entry keeps its stat fingerprint for change notes.
func collectRepoMapEntries(root string) []repoMapEntry {
	root = filepath.Clean(root)
	if root == "" || root == "." {
		return nil
	}
	entries := make([]repoMapEntry, 0, 128)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if shouldSkipRepoMapDir(name) && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if shouldSkipRepoMapFile(rel) {
			return nil
		}
		entry := repoMapEntry{path: rel}
		if info, statErr := d.Info(); statErr == nil {
			entry.size = info.Size()
			entry.mtime = info.ModTime()
		}
		if filepath.Ext(path) == ".go" {
			if symbols := goFileSymbolSummary(path); symbols != "" {
				entry.symbols = symbols
			}
		}
		entries = append(entries, entry)
		if len(entries) >= repoMapEntryLimit {
			return fs.SkipAll
		}
		return nil
	})
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	return entries
}

// renderRepoMapChangeNote computes a bounded added/updated/removed diff between
// the snapshot and the current workspace. "updated" means the stat fingerprint
// changed (content or size), which covers new exported symbols and edits.
func renderRepoMapChangeNote(snapshot, current []repoMapEntry) string {
	byPath := func(entries []repoMapEntry) map[string]repoMapEntry {
		out := make(map[string]repoMapEntry, len(entries))
		for _, e := range entries {
			out[e.path] = e
		}
		return out
	}
	snap := byPath(snapshot)
	cur := byPath(current)

	var added, updated, removed []string
	for path, e := range cur {
		if old, ok := snap[path]; !ok {
			added = append(added, path)
		} else if old.size != e.size || !old.mtime.Equal(e.mtime) {
			updated = append(updated, path)
		}
	}
	for path := range snap {
		if _, ok := cur[path]; !ok {
			removed = append(removed, path)
		}
	}
	sort.Strings(added)
	sort.Strings(updated)
	sort.Strings(removed)

	total := len(added) + len(updated) + len(removed)
	if total == 0 {
		return ""
	}
	lines := []string{"# Repo Map Changes"}
	shown := 0
	appendLine := func(prefix, path string) {
		if shown >= repoMapChangeNoteLimit {
			return
		}
		lines = append(lines, "- "+prefix+" "+path)
		shown++
	}
	for _, path := range added {
		appendLine("added", path)
	}
	for _, path := range updated {
		appendLine("updated", path)
	}
	for _, path := range removed {
		appendLine("removed", path)
	}
	if rest := total - shown; rest > 0 {
		lines = append(lines, fmt.Sprintf("(and %d more changes)", rest))
	}
	return strings.Join(lines, "\n")
}

// renderRepoMapQueryFocus scores the current entries against the latest user
// query and renders the top hits as a small volatile section. It reuses the
// pre-existing relevance scoring, but only for this tail hint.
func renderRepoMapQueryFocus(entries []repoMapEntry, query string) string {
	tokens := repoMapQueryTokens(query)
	if len(tokens) == 0 {
		return ""
	}
	type scored struct {
		path  string
		score int
	}
	var hits []scored
	for _, entry := range entries {
		// score >= 10 means at least one query token matched; the +1/+2
		// baseline bonuses are ordering tiebreakers, not relevance signals.
		if score := repoMapScore(entry.render(), tokens); score >= 10 {
			hits = append(hits, scored{path: entry.path, score: score})
		}
	}
	if len(hits) == 0 {
		return ""
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	if len(hits) > repoMapQueryFocusLimit {
		hits = hits[:repoMapQueryFocusLimit]
	}
	var builder strings.Builder
	builder.WriteString("# Repo Map (query focus)\n")
	for _, hit := range hits {
		builder.WriteString("- ")
		builder.WriteString(hit.path)
		builder.WriteByte('\n')
	}
	return strings.TrimSpace(builder.String())
}

func shouldSkipRepoMapDir(name string) bool {
	switch name {
	case ".git", ".godex", "node_modules", "vendor", "dist", "build", ".next", "coverage", "tmp", "log":
		return true
	default:
		return false
	}
}

func shouldSkipRepoMapFile(rel string) bool {
	base := filepath.Base(rel)
	if strings.HasPrefix(base, ".") && base != ".env.example" {
		return true
	}
	switch filepath.Ext(base) {
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".md", ".yaml", ".yml", ".json", ".toml", ".css":
		return false
	default:
		return true
	}
}

func goFileSymbolSummary(path string) string {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		return ""
	}
	symbols := make([]string, 0, 8)
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok || typeSpec.Name == nil || !typeSpec.Name.IsExported() {
					continue
				}
				symbols = append(symbols, "type "+typeSpec.Name.Name)
			}
		case *ast.FuncDecl:
			if d.Name != nil && d.Name.IsExported() {
				symbols = append(symbols, "func "+d.Name.Name)
			}
		}
		if len(symbols) >= 8 {
			break
		}
	}
	return strings.Join(symbols, ", ")
}

func repoMapQueryTokens(query string) []string {
	fields := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '_' && r != '-' && r != '/'
	})
	out := make([]string, 0, len(fields))
	seen := map[string]struct{}{}
	for _, field := range fields {
		if len(field) < 3 {
			continue
		}
		if _, ok := seen[field]; ok {
			continue
		}
		seen[field] = struct{}{}
		out = append(out, field)
	}
	return out
}

func repoMapScore(entry string, tokens []string) int {
	score := 0
	lower := strings.ToLower(entry)
	for _, token := range tokens {
		if strings.Contains(lower, token) {
			score += 10
		}
	}
	if strings.Contains(entry, " :: ") {
		score += 2
	}
	if strings.HasPrefix(entry, "- internal/") || strings.HasPrefix(entry, "- cmd/") {
		score++
	}
	return score
}
