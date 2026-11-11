package recorder

import (
	"time"
)

// ReconnectLogLimiter collapses identical reconnect-error floods (#perf D⑥).
// A camera that answers every backoff tick with the same connection failure
// produced ~2.7k identical ERROR lines/day on the reference box — journald
// churn on flash storage, and genuinely NEW errors drowning in the repeats.
//
// The first occurrence logs immediately; exact repeats (same formatted error
// text) are suppressed until the message changes or LogRepeatWindow elapses.
// The absorbed count rides the next emitted line (suppressed_repeats), so the
// flood stays quantified in aggregate.
//
// Not safe for concurrent use by design: each reconnect loop is a single
// goroutine and owns its limiter.
type ReconnectLogLimiter struct {
	lastMsg    string
	lastAt     time.Time
	suppressed int
}

// LogRepeatWindow bounds how long an unchanged error stays silent.
const LogRepeatWindow = 10 * time.Minute

// Allow decides whether this occurrence should be logged now. errText is the
// formatted error used as the dedup fingerprint. The returned count is the
// number of repeats absorbed since the previous emission (0 when this call is
// itself a suppression — attach it to the next emitted line's attrs).
func (l *ReconnectLogLimiter) Allow(now time.Time, errText string) (emit bool, suppressed int) {
	if errText != l.lastMsg || now.Sub(l.lastAt) >= LogRepeatWindow {
		n := l.suppressed
		l.lastMsg = errText
		l.lastAt = now
		l.suppressed = 0
		return true, n
	}
	l.suppressed++
	return false, 0
}
