package recorder

import (
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/backoff"
)

// TieredBackoff returns a retry delay based on attempt count.
// Thin wrapper over backoff.TieredBackoff; retained so existing recorder call
// sites keep compiling. New code outside this package should use backoff.* directly.
func TieredBackoff(attempt int) time.Duration {
	return backoff.TieredBackoff(attempt)
}

// TieredBackoffWithJitter returns TieredBackoff(attempt) with up to 1 second of jitter.
// Thin wrapper over backoff.TieredBackoffWithJitter.
func TieredBackoffWithJitter(attempt int) time.Duration {
	return backoff.TieredBackoffWithJitter(attempt)
}
