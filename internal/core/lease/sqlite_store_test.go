package lease

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAcquire_CreatesLease(t *testing.T) {
	s := NewSQLiteStore(t.TempDir())

	err := s.Acquire("job-1", "worker:godex:local", "token-1", time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	leased, err := s.IsLeased("job-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !leased {
		t.Fatal("expected job-1 to be leased")
	}
}

func TestAcquire_ConflictingJobFails(t *testing.T) {
	s := NewSQLiteStore(t.TempDir())

	if err := s.Acquire("job-1", "worker:godex:local", "token-1", time.Minute); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	// Same job, different token -> must fail (already leased).
	if err := s.Acquire("job-1", "worker:godex:local", "token-2", time.Minute); err == nil {
		t.Fatal("expected conflicting acquire to fail")
	}
}

func TestAcquire_ConflictingTokenFails(t *testing.T) {
	s := NewSQLiteStore(t.TempDir())

	if err := s.Acquire("job-1", "worker:godex:local", "token-1", time.Minute); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	// Different job but same token -> token is UNIQUE, must fail.
	if err := s.Acquire("job-2", "worker:godex:local", "token-1", time.Minute); err == nil {
		t.Fatal("expected conflicting token acquire to fail")
	}
}

func TestHeartbeat_RenewsLease(t *testing.T) {
	s := NewSQLiteStore(t.TempDir())

	if err := s.Acquire("job-1", "worker:godex:local", "token-1", time.Minute); err != nil {
		t.Fatalf("acquire: %v", err)
	}

	leased, err := s.IsLeased("job-1")
	if err != nil || !leased {
		t.Fatalf("expected leased, err=%v", err)
	}

	// Heartbeat before expiry keeps it alive.
	alive, err := s.Heartbeat("token-1", time.Minute)
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if !alive {
		t.Fatal("expected heartbeat to keep lease alive")
	}
}

func TestHeartbeat_ExpiredLeaseReturnsFalse(t *testing.T) {
	s := NewSQLiteStore(t.TempDir())

	// Acquire with a TTL already in the past so the lease is expired.
	if err := s.Acquire("job-1", "worker:godex:local", "token-1", -time.Minute); err != nil {
		t.Fatalf("acquire: %v", err)
	}

	alive, err := s.Heartbeat("token-1", time.Minute)
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if alive {
		t.Fatal("expected heartbeat on expired lease to return alive=false")
	}

	leased, _ := s.IsLeased("job-1")
	if leased {
		t.Fatal("expected job-1 to no longer be leased after expired heartbeat")
	}
}

func TestRelease_FreesLease(t *testing.T) {
	s := NewSQLiteStore(t.TempDir())

	if err := s.Acquire("job-1", "worker:godex:local", "token-1", time.Minute); err != nil {
		t.Fatalf("acquire: %v", err)
	}

	ok, err := s.Release("token-1")
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if !ok {
		t.Fatal("expected release to succeed")
	}

	leased, _ := s.IsLeased("job-1")
	if leased {
		t.Fatal("expected job-1 to be released")
	}
}

func TestRelease_InvalidTokenReturnsFalse(t *testing.T) {
	s := NewSQLiteStore(t.TempDir())

	ok, err := s.Release("nonexistent-token")
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if ok {
		t.Fatal("expected release of unknown token to return false")
	}
}

func TestReleaseByJobID(t *testing.T) {
	s := NewSQLiteStore(t.TempDir())

	if err := s.Acquire("job-1", "worker:godex:local", "token-1", time.Minute); err != nil {
		t.Fatalf("acquire: %v", err)
	}

	ok, err := s.ReleaseByJobID("job-1")
	if err != nil {
		t.Fatalf("release by job id: %v", err)
	}
	if !ok {
		t.Fatal("expected release by job id to succeed")
	}

	leased, _ := s.IsLeased("job-1")
	if leased {
		t.Fatal("expected job-1 to be released")
	}
}

func TestReapExpired_CollectsExpiredLeases(t *testing.T) {
	s := NewSQLiteStore(t.TempDir())

	// One fresh, one expired.
	if err := s.Acquire("fresh-job", "worker:godex:local", "token-fresh", time.Hour); err != nil {
		t.Fatalf("acquire fresh: %v", err)
	}
	if err := s.Acquire("expired-job", "worker:godex:local", "token-expired", -time.Minute); err != nil {
		t.Fatalf("acquire expired: %v", err)
	}

	expired, err := s.ReapExpired()
	if err != nil {
		t.Fatalf("reap expired: %v", err)
	}
	if len(expired) != 1 || expired[0] != "expired-job" {
		t.Fatalf("expected only expired-job reaped, got %v", expired)
	}

	// Expired job should no longer be leased after reap.
	leased, _ := s.IsLeased("expired-job")
	if leased {
		t.Fatal("expected expired-job to be released after reap")
	}
	// Fresh job unaffected.
	leased, _ = s.IsLeased("fresh-job")
	if !leased {
		t.Fatal("expected fresh-job to remain leased")
	}
}

func TestReapExpired_NoExpiredReturnsEmpty(t *testing.T) {
	s := NewSQLiteStore(t.TempDir())

	expired, err := s.ReapExpired()
	if err != nil {
		t.Fatalf("reap expired: %v", err)
	}
	if len(expired) != 0 {
		t.Fatalf("expected no expired leases, got %v", expired)
	}
}

func TestRelease_AcquireAllowsReuse(t *testing.T) {
	s := NewSQLiteStore(t.TempDir())

	if err := s.Acquire("job-1", "worker:godex:local", "token-1", time.Minute); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := s.Release("token-1"); err != nil {
		t.Fatalf("release: %v", err)
	}

	// After release, the job can be re-acquired.
	if err := s.Acquire("job-1", "worker:godex:local", "token-2", time.Minute); err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
}

func TestSQLiteStore_CreatesDBFile(t *testing.T) {
	dir := t.TempDir()
	s := NewSQLiteStore(dir)

	if err := s.Acquire("job-1", "worker:godex:local", "token-1", time.Minute); err != nil {
		t.Fatalf("acquire: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "leases.db")); os.IsNotExist(err) {
		t.Fatal("expected leases.db to be created")
	}
}

// fakeStore keeps Store as a compile-time interface check and guards against
// signature drift between the declaration and implementation.
func TestStore_InterfaceSatisfied(t *testing.T) {
	var _ Store = (*SQLiteStore)(nil) // nolint:staticcheck
	_ = errors.New // silence unused import
}