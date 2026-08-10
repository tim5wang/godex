package idempotency

import "time"

// Record is one executed idempotency key with its timestamp.
type Record struct {
	Key string    `json:"key"`
	At  time.Time `json:"at"`
}

// Store provides idempotent execution guarantees: once a key is committed,
// subsequent calls to once() with the same key skip the function.
type Store interface {
	// Once calls fn only if key has not been committed before.
	// Returns true when fn was executed (first time), false when skipped.
	Once(key string, fn func() error) (bool, error)

	// Committed returns true if key has been committed and is still within
	// the retention window.
	Committed(key string) (bool, error)
}

const (
	DefaultRetentionDays  = 14
	defaultPruneInterval  = 1 * time.Hour
)