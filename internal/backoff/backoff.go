// Package backoff provides shared retry-backoff helpers used across the NVR
// (camera recorders, stream relay targets, ingest reconnects).
//
// The strategy tiers delays so that transient failures recover fast while
// long-term-unreachable targets back off to a minute, with jitter to avoid
// thundering herd when many goroutines reconnect at once.
package backoff

import (
	"math/rand"
	"time"
)

// TieredBackoff returns a retry delay based on attempt count, following go2rtc's strategy:
//   - attempts 0–4:  1s  (fast recovery for transient failures)
//   - attempts 5–9:  5s  (short network issues)
//   - attempts 10–19: 10s (persistent problems)
//   - attempts 20+:   60s (long-term unreachable)
func TieredBackoff(attempt int) time.Duration {
	switch {
	case attempt < 5:
		return time.Second
	case attempt < 10:
		return 5 * time.Second
	case attempt < 20:
		return 10 * time.Second
	default:
		return time.Minute
	}
}

// TieredBackoffWithJitter returns TieredBackoff(attempt) with up to 1 second of random jitter
// to avoid thundering herd when multiple cameras reconnect simultaneously.
func TieredBackoffWithJitter(attempt int) time.Duration {
	return TieredBackoff(attempt) + time.Duration(rand.Int63n(int64(time.Second)))
}

// StorageBackoffWithJitter returns a long backoff (60s ± up to 10s jitter) for
// use when storage is in a failed state. Recording attempts are pointless
// while the disk is unavailable (e.g. filesystem remounted read-only, no space
// left), so we slow the reconnect loop down to ~once per minute instead of
// spamming logs and burning CPU with sub-second retries. We still retry
// periodically so recording resumes quickly once storage recovers.
func StorageBackoffWithJitter() time.Duration {
	return time.Minute + time.Duration(rand.Int63n(int64(10*time.Second)))
}
