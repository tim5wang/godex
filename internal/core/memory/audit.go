package memory

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/platform/fsutil"
)

const AuditLogFileName = "audit.jsonl"

type AuditAction string

const (
	AuditRemember         AuditAction = "remember"
	AuditUpdate           AuditAction = "update"
	AuditForget           AuditAction = "forget"
	AuditAcceptCandidate  AuditAction = "accept_candidate"
	AuditDismissCandidate AuditAction = "dismiss_candidate"
	AuditRestore          AuditAction = "restore"
	AuditArchive          AuditAction = "archive"
	AuditRestoreStatus    AuditAction = "restore_status"
	AuditUnsuppress       AuditAction = "unsuppress"
)

// AuditLogEntry is one append-only durable memory change record.
type AuditLogEntry struct {
	ID                   string        `json:"id"`
	Action               AuditAction   `json:"action"`
	MemoryID             string        `json:"memory_id,omitempty"`
	Title                string        `json:"title,omitempty"`
	Type                 Type          `json:"memory_type,omitempty"`
	Source               string        `json:"source,omitempty"`
	CandidateFingerprint string        `json:"candidate_fingerprint,omitempty"`
	CreatedAt            time.Time     `json:"created_at"`
	Before               *StoredMemory `json:"before,omitempty"`
	After                *StoredMemory `json:"after,omitempty"`
	Message              string        `json:"message,omitempty"`
}

// ListAudit returns recent memory audit entries, newest first.
func (m *Manager) ListAudit(limit int) ([]AuditLogEntry, error) {
	if err := m.ensureStore(); err != nil {
		return nil, err
	}
	path := filepath.Join(m.dir, AuditLogFileName)
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []AuditLogEntry{}, nil
		}
		return nil, err
	}
	defer file.Close()

	entries := make([]AuditLogEntry, 0)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry AuditLogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

// RestoreAudit rolls durable memory back to the before snapshot for one audit
// entry. Passing target "after" reapplies the recorded after snapshot.
func (m *Manager) RestoreAudit(auditID, target string) (*AuditLogEntry, error) {
	auditID = strings.TrimSpace(auditID)
	if auditID == "" {
		return nil, fmt.Errorf("missing audit id")
	}
	if err := m.ensureStore(); err != nil {
		return nil, err
	}
	entry, err := m.findAudit(auditID)
	if err != nil {
		return nil, err
	}

	target = strings.ToLower(strings.TrimSpace(target))
	if target == "" {
		target = "before"
	}
	var snapshot *StoredMemory
	switch target {
	case "before":
		snapshot = entry.Before
	case "after":
		snapshot = entry.After
	default:
		return nil, fmt.Errorf("target must be before or after")
	}

	before, _ := m.currentMemorySnapshot(entry.MemoryID, entry.Title)
	if err := m.applyAuditSnapshot(entry.MemoryID, entry.Title, snapshot); err != nil {
		return nil, err
	}
	after, _ := m.currentMemorySnapshot(entry.MemoryID, entry.Title)
	restore := AuditLogEntry{
		Action:   AuditRestore,
		MemoryID: entry.MemoryID,
		Title:    entry.Title,
		Type:     entry.Type,
		Source:   "memory-restore",
		Before:   before,
		After:    after,
		Message:  "restored " + target + " snapshot from " + entry.ID,
	}
	if err := m.appendAudit(restore); err != nil {
		return nil, err
	}
	return &entry, nil
}

func (m *Manager) appendAudit(entry AuditLogEntry) error {
	if strings.TrimSpace(m.dir) == "" {
		return nil
	}
	if err := os.MkdirAll(m.dir, 0755); err != nil {
		return err
	}
	now := time.Now().UTC()
	entry.ID = newAuditID(entry.Action, now)
	entry.CreatedAt = now
	if entry.MemoryID == "" {
		if entry.After != nil {
			entry.MemoryID = entry.After.ID
		} else if entry.Before != nil {
			entry.MemoryID = entry.Before.ID
		}
	}
	if entry.Title == "" {
		if entry.After != nil {
			entry.Title = entry.After.Title
		} else if entry.Before != nil {
			entry.Title = entry.Before.Title
		}
	}
	if entry.Type == "" {
		if entry.After != nil {
			entry.Type = entry.After.Type
		} else if entry.Before != nil {
			entry.Type = entry.Before.Type
		}
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(m.dir, AuditLogFileName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

func (m *Manager) findAudit(id string) (AuditLogEntry, error) {
	entries, err := m.ListAudit(0)
	if err != nil {
		return AuditLogEntry{}, err
	}
	for _, entry := range entries {
		if entry.ID == id {
			return entry, nil
		}
	}
	return AuditLogEntry{}, fmt.Errorf("memory audit entry not found")
}

func (m *Manager) currentMemorySnapshot(memoryID, title string) (*StoredMemory, error) {
	entries, err := m.readEntries()
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if (memoryID != "" && entry.ID == memoryID) || (title != "" && strings.EqualFold(entry.Title, title)) {
			record, err := m.readStoredMemory(entry)
			if err != nil {
				return nil, err
			}
			return &record, nil
		}
	}
	return nil, nil
}

func (m *Manager) applyAuditSnapshot(memoryID, title string, snapshot *StoredMemory) error {
	entries, err := m.readEntries()
	if err != nil {
		return err
	}
	matchIndex := -1
	for i, entry := range entries {
		if (memoryID != "" && entry.ID == memoryID) || (title != "" && strings.EqualFold(entry.Title, title)) {
			matchIndex = i
			break
		}
	}
	if snapshot == nil {
		if matchIndex < 0 {
			return nil
		}
		entry := entries[matchIndex]
		if err := os.Remove(filepath.Join(m.dir, entry.File)); err != nil && !os.IsNotExist(err) {
			return err
		}
		m.invalidateMemoryFile(entry.File)
		entries = append(entries[:matchIndex], entries[matchIndex+1:]...)
		if err := m.writeEntries(entries); err != nil {
			return err
		}
		m.deleteSidecarEntry(entry.ID)
		return nil
	}

	restored := snapshot.Entry
	if restored.ID == "" {
		restored.ID = memoryID
	}
	if restored.File == "" {
		restored.File = uniqueFileName(entries, slugify(restored.Title))
	}
	rendered := renderMemoryFile(restored, strings.TrimSpace(snapshot.Content))
	if err := fsutil.WriteFileAtomic(filepath.Join(m.dir, restored.File), []byte(rendered), 0644); err != nil {
		return err
	}
	m.invalidateMemoryFile(restored.File)
	if matchIndex >= 0 {
		old := entries[matchIndex]
		if old.File != restored.File {
			_ = os.Remove(filepath.Join(m.dir, old.File))
			m.invalidateMemoryFile(old.File)
		}
		entries[matchIndex] = restored
	} else {
		entries = append(entries, restored)
	}
	if err := m.writeEntries(entries); err != nil {
		return err
	}
	m.syncSidecarEntry(restored)
	return nil
}

func newAuditID(action AuditAction, now time.Time) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("%s:%d", action, now.UnixNano())))
	return fmt.Sprintf("audit_%s_%s", action, hex.EncodeToString(sum[:])[:10])
}
