package persistence

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"

	_ "modernc.org/sqlite"
)

// KV is one key/value pair returned by DurableMap.Entries.
type KV[V any] struct {
	Key   string
	Value V
}

// DurableMap is a durable key/value store abstraction.
//
// It mirrors `temp/qm/src/persistence/durable-map.ts` (createMemoryMap +
// createPostgresMap): values are JSON-serialized, keys are strings, and the
// store survives process restarts. The SQLite implementation backs each map
// with one table; a MemoryMap is provided for tests and in-process use.
type DurableMap[V any] interface {
	// All returns all values sorted by key.
	All() ([]V, error)
	// Entries returns all key/value pairs sorted by key.
	Entries() ([]KV[V], error)
	// Get returns the value for id, or false if absent.
	Get(id string) (V, bool, error)
	// Put stores value under id, replacing any existing value.
	Put(id string, value V) error
	// PutIfAbsent stores value under id only if absent, returning the stored value.
	PutIfAbsent(id string, value V) (V, error)
	// InsertIfAbsent stores value under id only if absent, reporting whether it inserted.
	InsertIfAbsent(id string, value V) (bool, error)
	// Update applies fn to the stored value under id; returns false if absent.
	Update(id string, fn func(V) V) (V, bool, error)
	// DeleteIf removes the value under id when predicate holds; reports whether it deleted.
	DeleteIf(id string, predicate func(V) bool) (bool, error)
	// Delete removes the value under id.
	Delete(id string) error
	// Take removes and returns the value under id, or false if absent.
	Take(id string) (V, bool, error)
}

// MemoryMap is an in-process DurableMap backed by a Go map.
type MemoryMap[V any] struct {
	mu sync.RWMutex
	m  map[string]V
}

// NewMemoryMap creates an empty in-memory DurableMap.
func NewMemoryMap[V any]() *MemoryMap[V] {
	return &MemoryMap[V]{m: map[string]V{}}
}

func (s *MemoryMap[V]) All() ([]V, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]string, 0, len(s.m))
	for key := range s.m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	values := make([]V, 0, len(keys))
	for _, key := range keys {
		values = append(values, s.m[key])
	}
	return values, nil
}

func (s *MemoryMap[V]) Entries() ([]KV[V], error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]string, 0, len(s.m))
	for key := range s.m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	entries := make([]KV[V], 0, len(keys))
	for _, key := range keys {
		entries = append(entries, KV[V]{Key: key, Value: s.m[key]})
	}
	return entries, nil
}

func (s *MemoryMap[V]) Get(id string) (V, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.m[id]
	return value, ok, nil
}

func (s *MemoryMap[V]) Put(id string, value V) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[id] = value
	return nil
}

func (s *MemoryMap[V]) PutIfAbsent(id string, value V) (V, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.m[id]; ok {
		return existing, nil
	}
	s.m[id] = value
	return value, nil
}

func (s *MemoryMap[V]) InsertIfAbsent(id string, value V) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[id]; ok {
		return false, nil
	}
	s.m[id] = value
	return true, nil
}

func (s *MemoryMap[V]) Update(id string, fn func(V) V) (V, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.m[id]
	if !ok {
		var zero V
		return zero, false, nil
	}
	next := fn(existing)
	s.m[id] = next
	return next, true, nil
}

func (s *MemoryMap[V]) DeleteIf(id string, predicate func(V) bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.m[id]
	if !ok {
		return false, nil
	}
	if !predicate(existing) {
		return false, nil
	}
	delete(s.m, id)
	return true, nil
}

func (s *MemoryMap[V]) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, id)
	return nil
}

func (s *MemoryMap[V]) Take(id string) (V, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.m[id]
	if !ok {
		var zero V
		return zero, false, nil
	}
	delete(s.m, id)
	return existing, true, nil
}

var validTableName = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// SQLiteMap is a DurableMap backed by one SQLite table (id TEXT PRIMARY KEY,
// json TEXT NOT NULL). Values are JSON-serialized; writes are durable.
type SQLiteMap[V any] struct {
	dir    string
	dbPath string
	table  string
	mu     sync.Mutex
}

// NewSQLiteMap creates a SQLite-backed DurableMap at dir/<table>.db.
func NewSQLiteMap[V any](dir, table string) (*SQLiteMap[V], error) {
	if !validTableName.MatchString(table) {
		return nil, fmt.Errorf("invalid table name: %q", table)
	}
	return &SQLiteMap[V]{
		dir:    dir,
		dbPath: filepath.Join(dir, table+".db"),
		table:  table,
	}, nil
}

func (s *SQLiteMap[V]) open() (*sql.DB, error) {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", s.dbPath)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (id TEXT PRIMARY KEY, json TEXT NOT NULL)`, s.table)); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (s *SQLiteMap[V]) All() ([]V, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(fmt.Sprintf(`SELECT json FROM %s ORDER BY id`, s.table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []V
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var value V
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *SQLiteMap[V]) Entries() ([]KV[V], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(fmt.Sprintf(`SELECT id, json FROM %s ORDER BY id`, s.table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []KV[V]
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var value V
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return nil, err
		}
		entries = append(entries, KV[V]{Key: id, Value: value})
	}
	return entries, rows.Err()
}

func (s *SQLiteMap[V]) Get(id string) (V, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var zero V
	db, err := s.open()
	if err != nil {
		return zero, false, err
	}
	defer db.Close()
	var raw string
	err = db.QueryRow(fmt.Sprintf(`SELECT json FROM %s WHERE id = ?`, s.table), id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return zero, false, nil
	}
	if err != nil {
		return zero, false, err
	}
	var value V
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return zero, false, err
	}
	return value, true, nil
}

func (s *SQLiteMap[V]) Put(id string, value V) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.open()
	if err != nil {
		return err
	}
	defer db.Close()
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = db.Exec(fmt.Sprintf(`INSERT INTO %s (id, json) VALUES (?, ?) ON CONFLICT(id) DO UPDATE SET json = excluded.json`, s.table), id, string(raw))
	return err
}

func (s *SQLiteMap[V]) PutIfAbsent(id string, value V) (V, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.open()
	if err != nil {
		var zero V
		return zero, err
	}
	defer db.Close()
	existing, found, err := s.getLocked(db, id)
	if err != nil {
		var zero V
		return zero, err
	}
	if found {
		return existing, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		var zero V
		return zero, err
	}
	if _, err := db.Exec(fmt.Sprintf(`INSERT INTO %s (id, json) VALUES (?, ?)`, s.table), id, string(raw)); err != nil {
		var zero V
		return zero, err
	}
	return value, nil
}

func (s *SQLiteMap[V]) InsertIfAbsent(id string, value V) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.open()
	if err != nil {
		return false, err
	}
	defer db.Close()
	raw, err := json.Marshal(value)
	if err != nil {
		return false, err
	}
	result, err := db.Exec(fmt.Sprintf(`INSERT OR IGNORE INTO %s (id, json) VALUES (?, ?)`, s.table), id, string(raw))
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

func (s *SQLiteMap[V]) Update(id string, fn func(V) V) (V, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var zero V
	db, err := s.open()
	if err != nil {
		return zero, false, err
	}
	defer db.Close()
	existing, found, err := s.getLocked(db, id)
	if err != nil {
		return zero, false, err
	}
	if !found {
		return zero, false, nil
	}
	next := fn(existing)
	raw, err := json.Marshal(next)
	if err != nil {
		return zero, false, err
	}
	if _, err := db.Exec(fmt.Sprintf(`UPDATE %s SET json = ? WHERE id = ?`, s.table), string(raw), id); err != nil {
		return zero, false, err
	}
	return next, true, nil
}

func (s *SQLiteMap[V]) DeleteIf(id string, predicate func(V) bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.open()
	if err != nil {
		return false, err
	}
	defer db.Close()
	existing, found, err := s.getLocked(db, id)
	if err != nil {
		return false, err
	}
	if !found || !predicate(existing) {
		return false, nil
	}
	if _, err := db.Exec(fmt.Sprintf(`DELETE FROM %s WHERE id = ?`, s.table), id); err != nil {
		return false, err
	}
	return true, nil
}

func (s *SQLiteMap[V]) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.open()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(fmt.Sprintf(`DELETE FROM %s WHERE id = ?`, s.table), id)
	return err
}

func (s *SQLiteMap[V]) Take(id string) (V, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var zero V
	db, err := s.open()
	if err != nil {
		return zero, false, err
	}
	defer db.Close()
	existing, found, err := s.getLocked(db, id)
	if err != nil {
		return zero, false, err
	}
	if !found {
		return zero, false, nil
	}
	if _, err := db.Exec(fmt.Sprintf(`DELETE FROM %s WHERE id = ?`, s.table), id); err != nil {
		return zero, false, err
	}
	return existing, true, nil
}

// getLocked reads one row assuming s.mu is held and db is open.
func (s *SQLiteMap[V]) getLocked(db *sql.DB, id string) (V, bool, error) {
	var zero V
	var raw string
	err := db.QueryRow(fmt.Sprintf(`SELECT json FROM %s WHERE id = ?`, s.table), id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return zero, false, nil
	}
	if err != nil {
		return zero, false, err
	}
	var value V
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return zero, false, err
	}
	return value, true, nil
}
