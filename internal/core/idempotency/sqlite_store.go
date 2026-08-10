package idempotency

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteStore implements Store backed by a SQLite table.
type SQLiteStore struct {
	dir           string
	dbPath        string
	retentionDays int
	mu            sync.Mutex
	inflight      map[string]bool
	lastPrune     time.Time
}

// NewSQLiteStore creates a new SQLite-backed idempotency store.
// The database file is created at dir/idempotency.db.
func NewSQLiteStore(dir string, retentionDays int) *SQLiteStore {
	if retentionDays <= 0 {
		retentionDays = DefaultRetentionDays
	}
	return &SQLiteStore{
		dir:           dir,
		dbPath:        filepath.Join(dir, "idempotency.db"),
		retentionDays: retentionDays,
		inflight:      make(map[string]bool),
	}
}

func (s *SQLiteStore) open() (*sql.DB, error) {
	if err := os.MkdirAll(s.dir, 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", s.dbPath)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS idempotency_keys (
		key TEXT PRIMARY KEY,
		created_at TEXT NOT NULL
	)`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_idempotency_created_at ON idempotency_keys(created_at)`); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (s *SQLiteStore) prune(db *sql.DB) {
	now := time.Now()
	if now.Sub(s.lastPrune) < defaultPruneInterval {
		return
	}
	s.lastPrune = now
	cutoff := now.Add(-time.Duration(s.retentionDays) * 24 * time.Hour)
	_, _ = db.Exec(`DELETE FROM idempotency_keys WHERE created_at < ?`, cutoff.Format(time.RFC3339))
}

// Once implements Store.Once.
func (s *SQLiteStore) Once(key string, fn func() error) (bool, error) {
	s.mu.Lock()
	if s.inflight[key] {
		s.mu.Unlock()
		return false, fmt.Errorf("idempotency: key %q is already in flight", key)
	}
	s.inflight[key] = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.inflight, key)
		s.mu.Unlock()
	}()

	committed, err := s.Committed(key)
	if err != nil {
		return false, err
	}
	if committed {
		return false, nil
	}

	if err := fn(); err != nil {
		return false, err
	}

	db, err := s.open()
	if err != nil {
		return false, err
	}
	defer db.Close()

	now := time.Now()
	_, err = db.Exec(`INSERT OR IGNORE INTO idempotency_keys(key, created_at) VALUES(?, ?)`, key, now.Format(time.RFC3339))
	if err != nil {
		return false, err
	}
	s.prune(db)
	return true, nil
}

// Committed implements Store.Committed.
func (s *SQLiteStore) Committed(key string) (bool, error) {
	db, err := s.open()
	if err != nil {
		return false, err
	}
	defer db.Close()

	cutoff := time.Now().Add(-time.Duration(s.retentionDays) * 24 * time.Hour)
	var count int
	err = db.QueryRow(`SELECT COUNT(*) FROM idempotency_keys WHERE key = ? AND created_at >= ?`, key, cutoff.Format(time.RFC3339)).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}