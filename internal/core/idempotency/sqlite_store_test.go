package idempotency

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOnce_FirstCallExecutes(t *testing.T) {
	dir := t.TempDir()
	s := NewSQLiteStore(dir, 1)

	called := false
	executed, err := s.Once("test-key", func() error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !executed {
		t.Fatal("expected executed=true on first call")
	}
	if !called {
		t.Fatal("expected fn to be called")
	}
}

func TestOnce_SecondCallSkips(t *testing.T) {
	dir := t.TempDir()
	s := NewSQLiteStore(dir, 1)

	callCount := 0
	executed, err := s.Once("dup-key", func() error {
		callCount++
		return nil
	})
	if err != nil || !executed || callCount != 1 {
		t.Fatalf("first call: executed=%v err=%v calls=%d", executed, err, callCount)
	}

	executed, err = s.Once("dup-key", func() error {
		callCount++
		return nil
	})
	if err != nil {
		t.Fatalf("second call unexpected error: %v", err)
	}
	if executed {
		t.Fatal("expected executed=false on second call (idempotent)")
	}
	if callCount != 1 {
		t.Fatalf("fn should not be called again, got %d calls", callCount)
	}
}

func TestOnce_PropagatesFnError(t *testing.T) {
	dir := t.TempDir()
	s := NewSQLiteStore(dir, 1)

	sentinel := errors.New("fn error")
	executed, err := s.Once("err-key", func() error {
		return sentinel
	})
	if err != sentinel {
		t.Fatalf("expected sentinel error, got %v", err)
	}
	if executed {
		t.Fatal("expected executed=false when fn returns error")
	}
}

func TestCommitted_ReturnsTrueAfterOnce(t *testing.T) {
	dir := t.TempDir()
	s := NewSQLiteStore(dir, 1)

	_, err := s.Once("check-key", func() error { return nil })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	committed, err := s.Committed("check-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !committed {
		t.Fatal("expected committed=true after Once succeeds")
	}
}

func TestCommitted_ReturnsFalseForUnknownKey(t *testing.T) {
	dir := t.TempDir()
	s := NewSQLiteStore(dir, 1)

	committed, err := s.Committed("unknown-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if committed {
		t.Fatal("expected committed=false for unknown key")
	}
}

func TestOnce_ConcurrentInflightReturnsError(t *testing.T) {
	dir := t.TempDir()
	s := NewSQLiteStore(dir, 1)

	// Use a channel to ensure the goroutine acquires the lock before we proceed.
	started := make(chan struct{})
	block := make(chan struct{})
	done := make(chan struct{})
	go func() {
		_, _ = s.Once("inflight-key", func() error {
			close(started) // signal that we're inside the critical section
			<-block
			return nil
		})
		close(done)
	}()

	<-started // wait until the goroutine has the lock and is blocking

	// Second call with same key should get in-flight error.
	_, err := s.Once("inflight-key", func() error { return nil })
	if err == nil {
		t.Fatal("expected in-flight error on concurrent Once")
	}

	close(block)
	<-done
}

func TestRetentionAndPrune(t *testing.T) {
	dir := t.TempDir()
	// retention=1 day. Note: NewSQLiteStore treats <=0 as DefaultRetentionDays.
	s := NewSQLiteStore(dir, 1)

	db, err := s.open()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// Fresh key within the 1-day retention window stays.
	_, err = db.Exec(`INSERT OR IGNORE INTO idempotency_keys(key, created_at) VALUES(?, ?)`, "fresh-key", time.Now().Format(time.RFC3339))
	if err != nil {
		t.Fatalf("insert fresh key: %v", err)
	}
	// Stale key 48h old is beyond the 1-day retention window.
	past := time.Now().Add(-48 * time.Hour).Format(time.RFC3339)
	if _, err := db.Exec(`INSERT OR IGNORE INTO idempotency_keys(key, created_at) VALUES(?, ?)`, "old-key", past); err != nil {
		t.Fatalf("insert old key: %v", err)
	}

	// Force prune by pushing lastPrune beyond the interval.
	s.lastPrune = time.Now().Add(-2 * defaultPruneInterval)
	s.prune(db)

	for _, k := range []string{"fresh-key", "old-key"} {
		committed, err := s.Committed(k)
		if err != nil {
			t.Fatalf("Committed(%q): %v", k, err)
		}
		want := k == "fresh-key"
		if committed != want {
			t.Fatalf("key %q: committed=%v, want %v", k, committed, want)
		}
	}
}

func TestOnce_MultipleKeysIndependent(t *testing.T) {
	dir := t.TempDir()
	s := NewSQLiteStore(dir, 1)

	keys := []string{"a", "b", "c"}
	for _, k := range keys {
		executed, err := s.Once(k, func() error { return nil })
		if err != nil || !executed {
			t.Fatalf("key %q: executed=%v err=%v", k, executed, err)
		}
	}

	for _, k := range keys {
		executed, err := s.Once(k, func() error { return nil })
		if err != nil {
			t.Fatalf("key %q second call: %v", k, err)
		}
		if executed {
			t.Fatalf("key %q second call should be skipped", k)
		}
	}
}

func TestNewSQLiteStoreDefaults(t *testing.T) {
	s := NewSQLiteStore(os.TempDir(), 0)
	if s.retentionDays != DefaultRetentionDays {
		t.Fatalf("expected default retention %d, got %d", DefaultRetentionDays, s.retentionDays)
	}
}

func TestSQLiteStore_CreatesDBFile(t *testing.T) {
	dir := t.TempDir()
	s := NewSQLiteStore(dir, 1)

	_, err := s.Once("create-test", func() error { return nil })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "idempotency.db")); os.IsNotExist(err) {
		t.Fatal("expected idempotency.db to be created")
	}
}