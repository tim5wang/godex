package lease

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
// It operates independently from the job store: the lease table
// tracks which worker holds which job, and Acquire/ReapExpired
// drive the status transitions externally.
type SQLiteStore struct {
	dir    string
	dbPath string
	mu     sync.Mutex
}

// NewSQLiteStore creates a new SQLite-backed lease store.
// The database file is created at dir/leases.db.
func NewSQLiteStore(dir string) *SQLiteStore {
	return &SQLiteStore{
		dir:    dir,
		dbPath: filepath.Join(dir, "leases.db"),
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
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS job_leases (
		job_id TEXT PRIMARY KEY,
		worker_id TEXT NOT NULL,
		token TEXT NOT NULL UNIQUE,
		status TEXT NOT NULL DEFAULT 'running',
		created_at TEXT NOT NULL,
		expires_at TEXT NOT NULL
	)`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_leases_expires_at ON job_leases(expires_at)`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_leases_status ON job_leases(status)`); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// Acquire implements Store.Acquire.
// Creates a new lease entry. The caller is responsible for setting the
// job status to running before calling this, and setting it back to
// pending if the lease creation fails.
func (s *SQLiteStore) Acquire(jobID, workerID, token string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	db, err := s.open()
	if err != nil {
		return err
	}
	defer db.Close()

	now := time.Now().UTC()
	expiresAt := now.Add(ttl)

	_, err = db.Exec(
		`INSERT OR ABORT INTO job_leases(job_id, worker_id, token, status, created_at, expires_at) VALUES(?, ?, ?, 'running', ?, ?)`,
		jobID, workerID, token, now.Format(time.RFC3339), expiresAt.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("lease acquire failed for job %s: %w", jobID, err)
	}
	return nil
}

// Heartbeat implements Store.Heartbeat.
func (s *SQLiteStore) Heartbeat(token string, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	db, err := s.open()
	if err != nil {
		return false, err
	}
	defer db.Close()

	now := time.Now().UTC()
	expiresAt := now.Add(ttl)

	// Only renew if the lease exists and hasn't expired.
	res, err := db.Exec(
		`UPDATE job_leases SET expires_at = ? WHERE token = ? AND status = 'running' AND expires_at > ?`,
		expiresAt.Format(time.RFC3339), token, now.Format(time.RFC3339),
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Release implements Store.Release.
func (s *SQLiteStore) Release(token string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	db, err := s.open()
	if err != nil {
		return false, err
	}
	defer db.Close()

	res, err := db.Exec(`DELETE FROM job_leases WHERE token = ?`, token)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ReleaseByJobID releases any lease held by the given job ID.
func (s *SQLiteStore) ReleaseByJobID(jobID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	db, err := s.open()
	if err != nil {
		return false, err
	}
	defer db.Close()

	res, err := db.Exec(`DELETE FROM job_leases WHERE job_id = ?`, jobID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ReapExpired implements Store.ReapExpired.
func (s *SQLiteStore) ReapExpired() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	db, err := s.open()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	now := nowStr()
	rows, err := db.Query(`SELECT job_id FROM job_leases WHERE status = 'running' AND expires_at <= ?`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		jobIDs = append(jobIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(jobIDs) == 0 {
		return nil, nil
	}

	// Delete expired leases.
	for _, id := range jobIDs {
		if _, err := db.Exec(`DELETE FROM job_leases WHERE job_id = ?`, id); err != nil {
			return nil, err
		}
	}
	return jobIDs, nil
}

// IsLeased returns true if the job has a valid (non-expired) lease.
func (s *SQLiteStore) IsLeased(jobID string) (bool, error) {
	db, err := s.open()
	if err != nil {
		return false, err
	}
	defer db.Close()

	now := nowStr()
	var count int
	err = db.QueryRow(`SELECT COUNT(*) FROM job_leases WHERE job_id = ? AND status = 'running' AND expires_at > ?`, jobID, now).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func nowStr() string {
	return time.Now().UTC().Format(time.RFC3339)
}