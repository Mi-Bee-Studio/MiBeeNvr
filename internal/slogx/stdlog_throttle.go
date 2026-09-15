package slogx

// Third-party protocol libraries report transient conditions through the
// STANDARD library logger, bypassing slog entirely: gortsplib v5 emits
// "23 RTP packets lost" (client.go / server_session_format.go) per sequence
// gap — several lines per second per stream. On a small journald quota this
// evicted the unit's own history twice on production M5: the 2026-09-14
// #803 forensics window and the 2026-09-15 boot window (36k lines / 3.5h
// consumed the whole 24MB journal, taking the ONVIF subscription startup
// evidence for #711 with it). The loss signal is worth keeping — just not
// at this cadence.

import (
	"io"
	"log"
	"regexp"
	"sync"
	"time"
)

// rtpLostPattern matches gortsplib's "%d RTP %s lost" lines (packet and
// stream-name variants) without matching anything an NVR component would
// print via the stdlib logger.
var rtpLostPattern = regexp.MustCompile(`^\d+ RTP \w+ lost`)

// stdLogThrottle passes unmatched bytes straight through; matched noisy
// lines pass a 1-per-minInterval token bucket.
type stdLogThrottle struct {
	out         io.Writer
	match       *regexp.Regexp
	minInterval time.Duration

	mu   sync.Mutex
	last time.Time
}

func (t *stdLogThrottle) Write(p []byte) (int, error) {
	if t.match.Match(p) {
		t.mu.Lock()
		now := time.Now()
		if now.Sub(t.last) < t.minInterval {
			t.mu.Unlock()
			// Drop silently — the next line to pass the bucket carries the
			// signal (loss counts are per-gap, so a burst says the same thing
			// as its first line).
			return len(p), nil
		}
		t.last = now
		t.mu.Unlock()
	}
	return t.out.Write(p)
}

// ThrottleStdLog redirects the standard library logger through a writer that
// rate-limits third-party RTP-loss spam (1 line per minInterval) while
// passing everything else untouched. Call once at process start, after the
// slog handler is in place.
func ThrottleStdLog(out io.Writer, minInterval time.Duration) {
	log.SetOutput(&stdLogThrottle{out: out, match: rtpLostPattern, minInterval: minInterval})
}
