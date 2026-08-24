package memory

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/platform/fsutil"
)

// referenceThrottle is the minimum age before an entry's LastReferencedAt is
// persisted again. Recalls happen on every turn; rewriting files that often
// would churn the store, so we only flush a new reference timestamp when the
// last recorded one is at least this old.
const referenceThrottle = time.Hour

// Archive marks one durable memory as archived. Archived memories are hidden
// from default recall and prompt injection but remain recoverable.
func (m *Manager) Archive(input ForgetInput) (*Entry, error) {
	return m.setEntryStatus(input, StatusArchived, AuditArchive)
}

// Restore un-archives one durable memory so it participates in recall and
// injection again.
func (m *Manager) Restore(input ForgetInput) (*Entry, error) {
	return m.setEntryStatus(input, StatusActive, AuditRestoreStatus)
}

func (m *Manager) setEntryStatus(input ForgetInput, status Status, action AuditAction) (*Entry, error) {
	if strings.TrimSpace(input.Title) == "" && strings.TrimSpace(input.File) == "" {
		return nil, fmt.Errorf("missing title or file")
	}
	if err := m.ensureStore(); err != nil {
		return nil, err
	}

	entries, err := m.readEntries()
	if err != nil {
		return nil, err
	}

	matchIndex := -1
	for i, entry := range entries {
		if matchesForgetInput(entry, input) {
			matchIndex = i
			break
		}
	}
	if matchIndex < 0 {
		return nil, fmt.Errorf("memory not found")
	}

	entry := entries[matchIndex]
	before, err := m.readStoredMemory(entry)
	if err != nil {
		return nil, err
	}
	if normalizeStatus(entry.Status) == status {
		return &entry, nil
	}
	entry.Status = normalizeStatus(status)
	entry.UpdatedAt = time.Now().UTC()

	record, err := m.readStoredMemory(entry)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(m.dir, entry.File)
	rendered := renderMemoryFile(entry, record.Content)
	if err := fsutil.WriteFileAtomic(path, []byte(rendered), 0644); err != nil {
		return nil, err
	}
	m.invalidateMemoryFile(entry.File)

	entries[matchIndex] = entry
	if err := m.writeEntries(entries); err != nil {
		return nil, err
	}
	m.syncSidecarEntry(entry)
	after := StoredMemory{Entry: entry, Content: record.Content}
	if err := m.appendAudit(AuditLogEntry{
		Action: action,
		Before: &before,
		After:  &after,
		Source: "memory-lifecycle",
	}); err != nil {
		return nil, err
	}
	return &entry, nil
}

// markReferenced updates LastReferencedAt for the given memory IDs when they
// have not been referenced recently, then persists the change. It is
// best-effort: recall failures never block the turn, so errors are swallowed.
func (m *Manager) markReferenced(ids map[string]struct{}) {
	if len(ids) == 0 {
		return
	}
	now := time.Now().UTC()
	entries, err := m.readEntries()
	if err != nil {
		return
	}
	changed := make(map[string]struct{})
	for i := range entries {
		id := entries[i].ID
		if _, ok := ids[id]; !ok {
			continue
		}
		if !entries[i].LastReferencedAt.IsZero() && now.Sub(entries[i].LastReferencedAt) < referenceThrottle {
			continue
		}
		entries[i].LastReferencedAt = now
		changed[id] = struct{}{}
	}
	if len(changed) == 0 {
		return
	}
	// Persist the updated index + sidecar. Also rewrite the individual memory
	// files (the durable source of truth) so the reference timestamp survives
	// an index rebuild; rewrite is throttled to at most once per hour per
	// memory so it does not churn on every turn.
	for _, entry := range entries {
		if _, ok := changed[entry.ID]; !ok {
			continue
		}
		record, err := m.readStoredMemory(entry)
		if err != nil {
			continue
		}
		path := filepath.Join(m.dir, entry.File)
		if err := fsutil.WriteFileAtomic(path, []byte(renderMemoryFile(entry, record.Content)), 0644); err != nil {
			continue
		}
		m.invalidateMemoryFile(entry.File)
		m.syncSidecarEntry(entry)
	}
	if err := m.writeEntries(entries); err != nil {
		return
	}
}

// MilestonePatterns are title fragments that mark a durable memory as a
// completed implementation milestone rather than durable project knowledge.
// Memories matching these (plus project type) are safe to archive en masse.
var MilestonePatterns = []string{
	"完成", "全部完成", "完成状态", "已落地", "落地", "收官",
	"Phase", "phase", "PHASE", "阶段",
}

// ListMilestoneMemories returns active project-type memories whose titles look
// like completed implementation milestones (e.g. "Phase 5 全部完成…").
func (m *Manager) ListMilestoneMemories() ([]Entry, error) {
	if err := m.ensureStore(); err != nil {
		return nil, err
	}
	entries, err := m.readEntries()
	if err != nil {
		return nil, err
	}
	matches := make([]Entry, 0, 4)
	for _, entry := range entries {
		if normalizeStatus(entry.Status) == StatusArchived {
			continue
		}
		if entry.Type != TypeProject && entry.Type != TypeWorkflow {
			continue
		}
		if isMilestoneTitle(entry.Title) {
			matches = append(matches, entry)
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		return matches[i].UpdatedAt.After(matches[j].UpdatedAt)
	})
	return matches, nil
}

// ArchiveMilestones archives all active project/workflow memories whose titles
// read as completed implementation milestones. It returns the archived entries.
func (m *Manager) ArchiveMilestones() ([]Entry, error) {
	matches, err := m.ListMilestoneMemories()
	if err != nil {
		return nil, err
	}
	archived := make([]Entry, 0, len(matches))
	for _, entry := range matches {
		archivedEntry, err := m.Archive(ForgetInput{File: entry.File})
		if err != nil {
			return nil, err
		}
		archived = append(archived, *archivedEntry)
	}
	return archived, nil
}

func isMilestoneTitle(title string) bool {
	lower := strings.ToLower(strings.TrimSpace(title))
	if lower == "" {
		return false
	}
	for _, pattern := range MilestonePatterns {
		needle := strings.ToLower(strings.TrimSpace(pattern))
		if needle != "" && strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

// RemoveSuppression deletes one dismissed-candidate suppression so the same
// automatic suggestion can be reviewed again. It accepts a fingerprint, a
// semantic key, or a source+createdAt hint.
func (m *Manager) RemoveSuppression(keyOrFingerprint string) error {
	keyOrFingerprint = strings.TrimSpace(keyOrFingerprint)
	if keyOrFingerprint == "" {
		return fmt.Errorf("missing suppression key or fingerprint")
	}
	suppressions, err := LoadCandidateSuppressions(m.SuppressionsPath())
	if err != nil {
		return err
	}
	kept := make([]CandidateSuppression, 0, len(suppressions))
	removed := false
	for _, suppression := range suppressions {
		if suppression.Fingerprint == keyOrFingerprint || suppression.Key == keyOrFingerprint {
			removed = true
			continue
		}
		kept = append(kept, suppression)
	}
	if !removed {
		return fmt.Errorf("suppression not found")
	}
	if err := m.writeSuppressions(kept); err != nil {
		return err
	}
	if err := m.appendAudit(AuditLogEntry{
		Action:  AuditUnsuppress,
		Source:  "memory-lifecycle",
		Message: "removed suppression " + keyOrFingerprint,
	}); err != nil {
		return err
	}
	return nil
}
