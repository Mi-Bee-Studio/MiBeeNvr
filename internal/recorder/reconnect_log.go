package recorder

import (
	"regexp"
	"time"
)

// Volatile endpoints inside connection errors: every attempt binds a fresh
// ephemeral port, so raw error text never repeats even when the failure is
// byte-for-byte the same failure ("...read tcp 192.0.2.10:41634->...:81:
// connection reset", "read udp [::]:36173: i/o timeout"). Strip the ports
// (and IPv6 bracket form) to get a stable fingerprint.
var volatileEndpointRe = regexp.MustCompile(
	`\b\d{1,3}(\.\d{1,3}){3}:\d+\b|` + // ipv4:port
		`\[[0-9a-fA-F:.]+\]:\d+`, // [ipv6]:port
)

// fingerprint normalizes an error for dedup.
func fingerprint(errText string) string {
	return volatileEndpointRe.ReplaceAllString(errText, "")
}

// ReconnectLogLimiter collapses identical reconnect-error floods (#perf D⑥).
// A camera that answers every backoff tick with the same connection failure
// produced ~2.7k identical ERROR lines/day on the reference box — journald
// churn on flash storage, and genuinely NEW errors drowning in the repeats.
//
// The first occurrence logs immediately; repeats whose fingerprint matches
// (error text modulo volatile endpoints) are suppressed until the message
// changes or LogRepeatWindow elapses. The absorbed count rides the next
// emitted line (suppressed_repeats), so the flood stays quantified in
// aggregate.
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
// formatted error (normalized internally — ephemeral ports stripped — before
// dedup). The returned count is the number of repeats absorbed since the
// previous emission (0 when this call is itself a suppression — attach it to
// the next emitted line's attrs).
func (l *ReconnectLogLimiter) Allow(now time.Time, errText string) (emit bool, suppressed int) {
	fp := fingerprint(errText)
	if fp != l.lastMsg || now.Sub(l.lastAt) >= LogRepeatWindow {
		n := l.suppressed
		l.lastMsg = fp
		l.lastAt = now
		l.suppressed = 0
		return true, n
	}
	l.suppressed++
	return false, 0
}
