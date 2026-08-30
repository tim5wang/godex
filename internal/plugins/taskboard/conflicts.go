package taskboard

import (
	"fmt"
	"strings"
)

// PathConflict records one overlapping package-level path between the target
// card and another active card. It is the unit surfaced by the dispatch gate
// (2) and the merge precheck (4): "card X overlaps path P with active card Y".
type PathConflict struct {
	// Path is the overlapping package path as declared/observed by the target
	// card.
	Path string `json:"path"`
	// OtherPath is the overlapping package path as declared/observed by the
	// conflicting active card.
	OtherPath string `json:"other_path"`
	// OtherCard is the id of the conflicting active card.
	OtherCard string `json:"other_card"`
	// OtherTitle is the title of the conflicting active card (for the PJM to
	// recognize it without opening it).
	OtherTitle string `json:"other_title"`
}

// ConflictReport aggregates cross-card path conflicts for one card. Empty means
// the card's impact surface does not collide with any active card.
type ConflictReport struct {
	Conflicts []PathConflict `json:"conflicts,omitempty"`
}

// HasConflicts reports whether the report carries any overlap.
func (r ConflictReport) HasConflicts() bool { return len(r.Conflicts) > 0 }

// PathConflictError wraps ErrPathConflict with the concrete overlap report so
// callers (dispatch/merge) can surface which card collides on which path.
type PathConflictError struct {
	Report ConflictReport
}

func (e *PathConflictError) Error() string {
	return ErrPathConflict.Error() + ": " + formatConflict(e.Report)
}

func (e *PathConflictError) Unwrap() error { return ErrPathConflict }

// checkPathConflictsAgainst computes the cross-card overlap between a target
// card's impact surface (declared TouchedPaths ∪ observed ObservedPaths) and a
// set of active candidate cards. The target card id is excluded so a card's own
// dispatch precheck / merge precheck never flags itself.
func checkPathConflictsAgainst(target Card, active []Card) ConflictReport {
	targetPaths := cardImpactPaths(target)
	if len(targetPaths) == 0 {
		return ConflictReport{}
	}
	report := ConflictReport{}
	for _, other := range active {
		if other.ID == target.ID {
			continue
		}
		otherPaths := cardImpactPaths(other)
		if len(otherPaths) == 0 {
			continue
		}
		for _, tp := range targetPaths {
			for _, op := range otherPaths {
				if pathsOverlap(tp, op) {
					report.Conflicts = append(report.Conflicts, PathConflict{
						Path:       tp,
						OtherPath:  op,
						OtherCard:  other.ID,
						OtherTitle: other.Title,
					})
				}
			}
		}
	}
	report.Conflicts = dedupeConflicts(report.Conflicts)
	return report
}

// cardImpactPaths returns the union of a card's declared TouchedPaths and its
// runtime-observed ObservedPaths, normalized. Both gates operate on this union
// so an accurate runtime report is never lost to a stale static declaration.
func cardImpactPaths(card Card) []string {
	combined := make([]string, 0, len(card.TouchedPaths)+len(card.ObservedPaths))
	combined = append(combined, card.TouchedPaths...)
	combined = append(combined, card.ObservedPaths...)
	return normalizeTouchedPaths(combined)
}

// slicesEqual reports whether two string slices are equal in order and content.
func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// normalizeTouchedPaths trims whitespace, drops empties, strips leading and
// trailing slashes, and de-duplicates while preserving first-seen order. This is
// the single normalization point for both static declarations (TouchedPaths)
// and dynamic observations (ObservedPaths).
func normalizeTouchedPaths(paths []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		p = normalizePath(p)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// normalizePath trims whitespace and any leading/trailing slashes.
func normalizePath(p string) string {
	return strings.Trim(strings.TrimSpace(p), "/")
}

// normalizeWorkDirs trims, drops empties, and de-duplicates work directories
// while preserving first-seen order. Leading/trailing slashes are stripped via
// normalizePath so re-loading the same dir never creates two entries.
func normalizeWorkDirs(dirs []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		d = normalizePath(d)
		if d == "" {
			continue
		}
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	return out
}

// normalizeResearch returns a nil-safe, trimmed copy of the research asset.
// It drops empty facts/locations/open-questions and normalizes excluded paths
// via the same path normalization as touched_paths. A research with nothing
// useful after trimming returns nil (so the JSON omits an empty block).
func normalizeResearch(r *Research) *Research {
	if r == nil {
		return nil
	}
	facts := trimDedup(r.Facts)
	locations := trimDedup(r.Locations)
	excluded := normalizeTouchedPaths(r.ExcludedPaths)
	open := trimDedup(r.OpenQuestions)
	if len(facts) == 0 && len(locations) == 0 && len(excluded) == 0 && len(open) == 0 {
		return nil
	}
	return &Research{Facts: facts, Locations: locations, ExcludedPaths: excluded, OpenQuestions: open}
}

// trimDedup trims each line, drops empties, and de-duplicates preserving order.
func trimDedup(lines []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if _, ok := seen[l]; ok {
			continue
		}
		seen[l] = struct{}{}
		out = append(out, l)
	}
	return out
}

// pathsOverlap reports whether two normalized package paths overlap: either
// they are equal or one is a path-segment prefix (parent/child directory) of
// the other. Package-level granularity means "internal/platform/tooling"
// overlaps "internal/platform/tooling/tooling.go" (same package) and
// "internal/platform" (parent), but not "internal/tools".
func pathsOverlap(a, b string) bool {
	a = normalizePath(a)
	b = normalizePath(b)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	return isPathPrefix(a, b) || isPathPrefix(b, a)
}

// isPathPrefix reports whether full is equal to prefix or extends it across a
// path-segment boundary ("internal/platform" prefixes
// "internal/platform/tooling" but not "internal/platformabc").
func isPathPrefix(prefix, full string) bool {
	if !strings.HasPrefix(full, prefix) {
		return false
	}
	if len(full) == len(prefix) {
		return true
	}
	return full[len(prefix)] == '/'
}

// dedupeConflicts collapses duplicate overlap records so a card never shows
// the same (path, other-card) collision twice even when several overlapping
// declared/observed paths hit the same active card.
func dedupeConflicts(in []PathConflict) []PathConflict {
	seen := map[string]struct{}{}
	out := make([]PathConflict, 0, len(in))
	for _, c := range in {
		key := c.Path + "|" + c.OtherCard + "|" + c.OtherPath
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, c)
	}
	return out
}

// formatConflict renders a conflict report into a compact human/agent-readable
// single-line message (used in dispatch/merge error text).
func formatConflict(r ConflictReport) string {
	if !r.HasConflicts() {
		return ""
	}
	var b strings.Builder
	b.WriteString("与在跑卡路径重叠:")
	for i, c := range r.Conflicts {
		if i > 0 {
			b.WriteString(";")
		}
		b.WriteString(fmt.Sprintf(" %q(%s %q) ↔ %q", c.Path, c.OtherCard, c.OtherTitle, c.OtherPath))
	}
	return b.String()
}
