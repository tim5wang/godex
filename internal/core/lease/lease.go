package lease

import "time"

// Lease represents one acquired lease on a worker job.
type Lease struct {
	JobID     string    `json:"job_id"`
	WorkerID  string    `json:"worker_id"`
	Token     string    `json:"token"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Store provides distributed lease semantics for worker jobs.
// A lease prevents two workers from processing the same job concurrently
// and enables crash recovery: if a worker stops heartbeating, the lease
// expires and the job can be requeued.
type Store interface {
	// Acquire creates a lease for one specific job. Returns error if the
	// job is already leased or the token conflicts.
	Acquire(jobID, workerID, token string, ttl time.Duration) error

	// Heartbeat renews the lease for ttl from now. Returns false when the
	// lease has been lost (e.g. expired or released by another worker).
	Heartbeat(token string, ttl time.Duration) (bool, error)

	// Release frees the lease. Returns false if the lease token is invalid.
	Release(token string) (bool, error)

	// ReleaseByJobID releases any lease held by the given job ID.
	ReleaseByJobID(jobID string) (bool, error)

	// ReapExpired finds all expired leases and returns their job IDs.
	ReapExpired() ([]string, error)

	// IsLeased returns true if the job has a valid (non-expired) lease.
	IsLeased(jobID string) (bool, error)
}

// Default constants.
const (
	DefaultLeaseTTL        = 5 * time.Minute
	DefaultHeartbeatInterval = 30 * time.Second
	DefaultLeaseLostLimit   = 3
)