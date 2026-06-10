package recorder

import (
	"time"
)

// Storage health state constants matching storage.HealthState values.
const (
	storageHealthHealthy = iota
	storageHealthDegraded
	storageHealthFailed
)

// isStorageFailed checks whether the SegmentStore reports a failed health state.
// Returns false if the store does not implement the health check interface.
func isStorageFailed(store SegmentStore) bool {
	type healthHint interface{ StorageHealth() int }
	if hc, ok := store.(healthHint); ok {
		return hc.StorageHealth() >= storageHealthFailed
	}
	return false
}

// shouldLogHealth returns true if it's time to emit a throttled health warning
// (at most once every 30 seconds).
func shouldLogHealth(lastLog time.Time) (time.Time, bool) {
	now := time.Now()
	if now.Sub(lastLog) < 30*time.Second {
		return lastLog, false
	}
	return now, true
}
