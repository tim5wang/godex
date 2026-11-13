package memory

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

type memorySearchProjection struct {
	stamp   string
	files   []string
	records []StoredMemory
}

// Reuse the reconciled read projection while the authoritative index and every
// referenced Markdown file retain their versions. Eligibility, expiry and
// promoted-source validation still run on EVERY query, outside this cache.
func (m *Manager) searchProjected(opts SearchOptions) ([]StoredMemory, error) {
	if err := m.ensureStore(); err != nil {
		return nil, err
	}
	m.projectionMu.Lock()
	defer m.projectionMu.Unlock()
	stamp, err := m.projectionStamp(m.projection.files)
	if err != nil {
		return nil, err
	}
	if m.projection.stamp != "" && stamp == m.projection.stamp {
		if opts.diagnostics != nil {
			opts.diagnostics.Cache = "hit"
		}
		return m.filterProjection(opts, m.projection.records), nil
	}
	entries, err := m.readEntries()
	if opts.diagnostics != nil {
		opts.diagnostics.Cache = "miss"
	}
	if err != nil {
		return nil, err
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		files = append(files, entry.File)
	}
	before, err := m.projectionStamp(files)
	if err != nil {
		return nil, err
	}
	records, err := m.searchWithSidecar(SearchOptions{Status: StatusAll})
	if err != nil {
		return nil, err
	}
	after, err := m.projectionStamp(files)
	if err != nil {
		return nil, err
	}
	if before == after {
		m.projection = memorySearchProjection{stamp: after, files: files, records: records}
	}
	return m.filterProjection(opts, records), nil
}

func (m *Manager) projectionStamp(files []string) (string, error) {
	var stamp strings.Builder
	for _, file := range append([]string{IndexFileName}, files...) {
		info, err := m.statFile(filepath.Join(m.dir, file))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&stamp, "%s\x00%d:%d\n", file, info.ModTime().UnixNano(), info.Size())
	}
	return stamp.String(), nil
}

func (m *Manager) filterProjection(opts SearchOptions, records []StoredMemory) []StoredMemory {
	selected := make([]StoredMemory, 0, len(records))
	tag := strings.ToLower(strings.TrimSpace(opts.Tag))
	source := strings.TrimSpace(opts.Source)
	for _, r := range records {
		if opts.Type != "" && opts.Type != r.Type || !entryStatusMatches(r.Entry, opts.Status) {
			continue
		}
		if opts.Automatic && !m.autoRecallEligible(r.Entry, time.Now()) {
			continue
		}
		if tag != "" && !entryHasTag(r.Entry, tag) || source != "" && !strings.EqualFold(r.Source, source) {
			continue
		}
		selected = append(selected, r)
	}
	selected = rankStoredMemories(opts.Query, selected, opts.Limit, opts.diagnostics)
	for i := range selected {
		selected[i].Tags = append([]string(nil), selected[i].Tags...)
		if selected[i].Origin != nil {
			origin := *selected[i].Origin
			selected[i].Origin = &origin
		}
	}
	return selected
}
