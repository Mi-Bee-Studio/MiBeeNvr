package slogx

// Third-party protocol libraries report transient conditions through the
// STANDARD library logger. slog.SetDefault bridges stdlib log into slog, so
// those lines show up structured (level=INFO) but WITHOUT a component field:
// gortsplib v5 emits "23 RTP packets lost" per sequence gap — several lines
// per second per stream. On a small journald quota this evicted the unit's
// own history twice on production M5 (the 2026-09-14 #803 forensics window
// and the 2026-09-15 boot window: 36k lines / 3.5h consumed the whole 24MB
// journal, taking the ONVIF subscription startup evidence for #711 with it).
// The loss signal is worth keeping — just not at that cadence.
//
// Two properties are load-bearing here (#813 review):
//
//   - Forwarding, not raw stderr: passed lines re-enter via slog.Info so
//     they keep the bridge's structured form and respect the configured
//     level (#813 review ③).
//   - Armed memory across logger swaps: slog.SetDefault UNCONDITIONALLY
//     rewrites stdlib log output with its own bridge writer, silently
//     discarding any writer installed before it. InstallStdLogThrottle
//     remembers the armed interval and SetDefault re-installs the writer
//     after every swap, so the throttle survives main's post-config
//     re-logger AND the remote-log MultiHandler wrap in pkg/app (#813
//     review ①/P0).

import (
	"log"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// rtpLostPattern matches gortsplib's "%d RTP %s lost" lines (packet and
// stream-name variants) without matching anything an NVR component would
// print through the stdlib logger.
var rtpLostPattern = regexp.MustCompile(`^\d+ RTP \w+ lost`)

// armedInterval is the currently armed throttle interval (nanoseconds); 0 =
// off. Read at write time (so a disarm takes effect immediately on the
// already-installed writer) and on every SetDefault swap (re-arm).
var armedInterval atomic.Int64

// stdLogThrottle drops matched noisy lines inside the armed interval and
// forwards everything else into slog. A fresh instance per install means a
// full token after every logger swap — the first post-swap line always
// passes, which is exactly what a reconfigured process should report.
type stdLogThrottle struct {
	mu   sync.Mutex
	last time.Time
}

func (t *stdLogThrottle) Write(p []byte) (int, error) {
	d := time.Duration(armedInterval.Load())
	if d > 0 && rtpLostPattern.Match(p) {
		t.mu.Lock()
		now := time.Now()
		if now.Sub(t.last) < d {
			t.mu.Unlock()
			// Drop silently — loss counts are per-gap, so the next line to
			// pass the bucket carries the same signal as this one.
			return len(p), nil
		}
		t.last = now
		t.mu.Unlock()
	}
	slog.Info(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// InstallStdLogThrottle arms (d > 0) or disables (d <= 0) the RTP-loss
// throttle and installs the forwarding writer as the stdlib log output.
// Once armed, the interval survives any later SetDefault swap; use 0 to
// explicitly disarm (an already-installed writer then forwards everything).
func InstallStdLogThrottle(d time.Duration) {
	if d <= 0 {
		armedInterval.Store(0)
		return
	}
	armedInterval.Store(int64(d))
	log.SetFlags(0)
	log.SetOutput(&stdLogThrottle{})
}

// SetDefault swaps the process logger, re-installing the armed stdlib-log
// throttle afterwards. Use this instead of a bare slog.SetDefault wherever
// the default logger changes at runtime — a bare swap silently discards the
// throttle writer (slog.SetDefault rewrites log output unconditionally).
func SetDefault(logger *slog.Logger) {
	slog.SetDefault(logger)
	if armedInterval.Load() > 0 {
		log.SetFlags(0)
		log.SetOutput(&stdLogThrottle{})
	}
}
