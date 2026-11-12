package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/tim5wang/godex/internal/platform/fsutil"
)

var storeMutations sync.Map

func (m *Manager) lockMutation() func() {
	lock, _ := storeMutations.LoadOrStore(m.dir, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// ImportLegacyPreferences copies only explicit user preferences for the local
// operator. Original IDs, evidence files and all legacy work records remain.
// Other users/tenants must never inherit this instance-wide compatibility data.
func ImportLegacyPreferences(base string, owner Owner, target *Manager) error {
	if owner.TenantID != "" || owner.UserID != "local-operator" {
		return nil
	}
	unlock := target.lockMutation()
	defer unlock()
	marker := filepath.Join(target.dir, "legacy-preferences-imported.json")
	if _, err := os.Stat(marker); err == nil {
		return nil
	}
	if err := target.ensureStore(); err != nil {
		return err
	}
	original := NewManager(base)
	rows, err := original.Search(SearchOptions{Type: TypeUser, Status: StatusAll})
	if err != nil {
		return err
	}
	entries, err := target.readEntries()
	if err != nil {
		return err
	}
	var ids []string
	for _, row := range rows {
		found := false
		for _, entry := range entries {
			if entry.ID == row.ID || entry.Title == row.Title {
				found = true
				break
			}
		}
		if found {
			continue
		}
		row.File = uniqueFileName(entries, slugify(row.Title))
		if err = fsutil.WriteFileAtomic(filepath.Join(target.dir, row.File), []byte(renderMemoryFile(row.Entry, row.Content)), 0600); err != nil {
			return err
		}
		entries = append(entries, row.Entry)
		ids = append(ids, row.ID)
		if err = target.appendAudit(AuditLogEntry{Action: AuditRestore, MemoryID: row.ID, After: &row, Source: "legacy-preference-migration", Message: "Copied explicit local-operator preference; original retained."}); err != nil {
			return err
		}
	}
	if err = target.writeEntries(entries); err != nil {
		return err
	}
	raw, _ := json.Marshal(map[string]any{"imported_ids": ids, "source": base})
	return fsutil.WriteFileAtomic(marker, raw, 0600)
}

// LegacyReport is a read-only attribution inventory, not an automatic migration
// based on titles or project-name matches.
func LegacyReport(base string) ([]map[string]string, error) {
	rows, err := NewManager(base).Search(SearchOptions{Status: StatusAll})
	if err != nil {
		return nil, err
	}
	out := []map[string]string{}
	for _, r := range rows {
		state := "unassigned_work_record"
		if r.Type == TypeUser {
			state = "explicit_user_preference"
		}
		out = append(out, map[string]string{"id": r.ID, "title": r.Title, "source": r.Source, "attribution": state, "lookup": fmt.Sprintf("legacy:%s", r.ID)})
	}
	return out, nil
}

func (m *Manager) lockCandidates() func() {
	lock, _ := storeMutations.LoadOrStore(m.dir+"/candidates", &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (m *Manager) lockInitialization() func() {
	lock, _ := storeMutations.LoadOrStore(m.dir+"/initialization", &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}
