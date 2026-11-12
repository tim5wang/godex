package memory

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const SidecarDBFileName = "memory.db"

func (m *Manager) searchWithSidecar(opts SearchOptions) ([]StoredMemory, error) {
	if err := m.ensureStore(); err != nil {
		return nil, err
	}

	db, err := m.openSidecar()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	entries, err := m.readEntries()
	if err != nil {
		return nil, err
	}
	if err := m.reconcileSidecar(db, entries); err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}

	rows, err := db.Query(`
		SELECT id, title, file, summary, type, source, tags, created_at, updated_at, fingerprint, content, status, last_referenced_at
		FROM memories
		ORDER BY updated_at DESC, title ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tagFilter := strings.ToLower(strings.TrimSpace(opts.Tag))
	sourceFilter := strings.ToLower(strings.TrimSpace(opts.Source))

	records := make([]StoredMemory, 0, len(entries))
	// Validity metadata lives in the authoritative index/markdown, not FTS.
	metadata := make(map[string]Entry, len(entries))
	for _, entry := range entries {
		metadata[entry.ID] = entry
	}
	for rows.Next() {
		record, err := scanStoredMemory(rows)
		if err != nil {
			return nil, err
		}
		if entry, ok := metadata[record.ID]; ok {
			record.Origin = entry.Origin
			record.ExpiresAt = entry.ExpiresAt
			record.SupersededBy = entry.SupersededBy
			record.RecallPolicy = entry.RecallPolicy
		}
		if opts.Type != "" && record.Type != opts.Type {
			continue
		}
		if !entryStatusMatches(record.Entry, opts.Status) {
			continue
		}
		if opts.Automatic && !m.autoRecallEligible(record.Entry, time.Now()) {
			continue
		}
		if tagFilter != "" && !entryHasTag(record.Entry, tagFilter) {
			continue
		}
		if sourceFilter != "" && !strings.EqualFold(record.Source, sourceFilter) {
			continue
		}

		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return rankStoredMemories(opts.Query, records, opts.Limit), nil
}

func (m *Manager) syncSidecarEntry(entry Entry) {
	db, err := m.openSidecar()
	if err != nil {
		return
	}
	defer db.Close()
	_ = m.upsertSidecarEntry(db, entry)
}

func (m *Manager) deleteSidecarEntry(id string) {
	if strings.TrimSpace(id) == "" {
		return
	}
	db, err := m.openSidecar()
	if err != nil {
		return
	}
	defer db.Close()
	_ = deleteSidecarRows(db, id)
}

func (m *Manager) openSidecar() (*sql.DB, error) {
	db, err := sql.Open("sqlite", filepath.Join(m.dir, SidecarDBFileName))
	if err != nil {
		return nil, err
	}
	if err := m.ensureSidecarSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (m *Manager) ensureSidecarSchema(db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS memories (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			file TEXT NOT NULL,
			summary TEXT NOT NULL,
			type TEXT NOT NULL,
			source TEXT NOT NULL,
			tags TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			fingerprint TEXT NOT NULL,
			content TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active',
			last_referenced_at TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_memories_updated_at ON memories(updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_memories_type ON memories(type)`,
		`CREATE INDEX IF NOT EXISTS idx_memories_source ON memories(source)`,
		`CREATE INDEX IF NOT EXISTS idx_memories_status ON memories(status)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(
			id UNINDEXED,
			title,
			summary,
			content,
			source,
			tags,
			tokenize='unicode61'
		)`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	// Migrate existing databases that predate the status/last_referenced columns.
	for _, column := range []struct{ name, definition string }{
		{"status", "TEXT NOT NULL DEFAULT 'active'"},
		{"last_referenced_at", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := ensureSidecarColumn(db, "memories", column.name, column.definition); err != nil {
			return err
		}
	}
	return nil
}

// ensureSidecarColumn adds a column to a sidecar table if it is missing, so
// databases created by older versions keep working without a full rebuild.
func ensureSidecarColumn(db *sql.DB, table, name, definition string) error {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var cname, ctype string
		var notnull, pk int
		var dflt interface{}
		if err := rows.Scan(&cid, &cname, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if cname == name {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + name + ` ` + definition)
	return err
}

type sidecarEntryState struct {
	ID          string
	File        string
	UpdatedAt   time.Time
	Source      string
	Tags        string
	Fingerprint string
	Status      string
}

func (m *Manager) reconcileSidecar(db *sql.DB, entries []Entry) error {
	current, err := loadSidecarState(db)
	if err != nil {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	desired := make(map[string]Entry, len(entries))
	for _, entry := range entries {
		desired[entry.ID] = entry
	}

	for id := range current {
		if _, ok := desired[id]; ok {
			continue
		}
		if err := deleteSidecarRowsTx(tx, id); err != nil {
			return err
		}
	}

	for _, entry := range entries {
		needsUpsert, err := m.sidecarEntryNeedsRefresh(entry, current[entry.ID])
		if err != nil {
			return err
		}
		if !needsUpsert {
			continue
		}
		record, err := m.readStoredMemory(entry)
		if err != nil {
			return err
		}
		if err := upsertSidecarRecordTx(tx, record); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (m *Manager) sidecarEntryNeedsRefresh(entry Entry, current sidecarEntryState) (bool, error) {
	if current.ID == "" {
		return true, nil
	}
	if current.File != entry.File ||
		current.Fingerprint != entry.Fingerprint ||
		!current.UpdatedAt.Equal(entry.UpdatedAt.UTC()) ||
		!strings.EqualFold(current.Source, entry.Source) ||
		current.Status != string(normalizeStatus(entry.Status)) ||
		current.Tags != strings.Join(normalizeTags(entry.Tags), ",") {
		return true, nil
	}

	info, err := m.statFile(filepath.Join(m.dir, entry.File))
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, err
	}
	if info.ModTime().UTC().After(current.UpdatedAt) {
		return true, nil
	}
	return false, nil
}

func loadSidecarState(db *sql.DB) (map[string]sidecarEntryState, error) {
	rows, err := db.Query(`SELECT id, file, updated_at, source, tags, fingerprint, status FROM memories`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]sidecarEntryState)
	for rows.Next() {
		var state sidecarEntryState
		var updatedAt string
		if err := rows.Scan(&state.ID, &state.File, &updatedAt, &state.Source, &state.Tags, &state.Fingerprint, &state.Status); err != nil {
			return nil, err
		}
		if ts, err := parseSidecarTime(updatedAt); err == nil {
			state.UpdatedAt = ts
		}
		result[state.ID] = state
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (m *Manager) upsertSidecarEntry(db *sql.DB, entry Entry) error {
	record, err := m.readStoredMemory(entry)
	if err != nil {
		return err
	}
	return withSidecarTx(db, func(tx *sql.Tx) error {
		return upsertSidecarRecordTx(tx, record)
	})
}

func deleteSidecarRows(db *sql.DB, id string) error {
	return withSidecarTx(db, func(tx *sql.Tx) error {
		return deleteSidecarRowsTx(tx, id)
	})
}

func withSidecarTx(db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func upsertSidecarRecordTx(tx *sql.Tx, record StoredMemory) error {
	if err := deleteSidecarRowsTx(tx, record.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO memories(id, title, file, summary, type, source, tags, created_at, updated_at, fingerprint, content, status, last_referenced_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		record.ID,
		record.Title,
		record.File,
		record.Summary,
		string(record.Type),
		record.Source,
		strings.Join(record.Tags, ","),
		record.CreatedAt.UTC().Format(time.RFC3339Nano),
		record.UpdatedAt.UTC().Format(time.RFC3339Nano),
		record.Fingerprint,
		record.Content,
		string(normalizeStatus(record.Status)),
		formatOptionalTime(record.LastReferencedAt),
	); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO memory_fts(id, title, summary, content, source, tags)
		VALUES(?, ?, ?, ?, ?, ?)
	`,
		record.ID,
		record.Title,
		record.Summary,
		record.Content,
		record.Source,
		strings.Join(record.Tags, " "),
	); err != nil {
		return err
	}
	return nil
}

func deleteSidecarRowsTx(tx *sql.Tx, id string) error {
	if _, err := tx.Exec(`DELETE FROM memory_fts WHERE id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM memories WHERE id = ?`, id); err != nil {
		return err
	}
	return nil
}

func scanStoredMemory(scanner interface {
	Scan(dest ...any) error
}) (StoredMemory, error) {
	var record StoredMemory
	var memoryType string
	var tags string
	var createdAt string
	var updatedAt string
	var status string
	var lastReferenced string
	if err := scanner.Scan(
		&record.ID,
		&record.Title,
		&record.File,
		&record.Summary,
		&memoryType,
		&record.Source,
		&tags,
		&createdAt,
		&updatedAt,
		&record.Fingerprint,
		&record.Content,
		&status,
		&lastReferenced,
	); err != nil {
		return StoredMemory{}, err
	}
	record.Type = Type(memoryType)
	record.Tags = normalizeTags(strings.Split(tags, ","))
	record.Status = normalizeStatus(Status(strings.ToLower(strings.TrimSpace(status))))
	if ts, err := parseSidecarTime(createdAt); err == nil {
		record.CreatedAt = ts
	}
	if ts, err := parseSidecarTime(updatedAt); err == nil {
		record.UpdatedAt = ts
	}
	if ts, err := parseSidecarTime(lastReferenced); err == nil {
		record.LastReferencedAt = ts
	}
	return record, nil
}

func parseSidecarTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("empty time")
	}
	return time.Parse(time.RFC3339Nano, value)
}

func formatOptionalTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
