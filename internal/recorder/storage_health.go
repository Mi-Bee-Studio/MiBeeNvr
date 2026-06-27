package recorder

import (
	"time"
)

// isStorageFailed checks whether the SegmentStore reports a failed health state.
// The store may optionally implement StorageFailed() bool (e.g. *storage.Manager);
// returns false if the store does not expose storage health so behavior is
// unchanged for stub/test stores.
//
// NOTE: the optional interface intentionally returns bool rather than an int
// health enum. Go requires exact return-type matching for interface satisfaction,
// so a store method returning a named int type (e.g. storage.HealthState) would
// NOT satisfy an `StorageHealth() int` interface — that was the original bug that
// silently disabled this guard for every recorder.
func isStorageFailed(store SegmentStore) bool {
	type healthHint interface{ StorageFailed() bool }
	if hc, ok := store.(healthHint); ok {
		return hc.StorageFailed()
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
