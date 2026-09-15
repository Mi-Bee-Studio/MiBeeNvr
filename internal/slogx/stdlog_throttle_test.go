package slogx

// gortsplib v5 reports RTP sequence gaps through the standard library
// logger — slog.SetDefault bridges stdlib log into slog, so on production
// these arrive as structured Info lines WITHOUT a component field ("39 RTP
// packets lost", several per second per stream) and flooded a 24MB journald
// quota twice (#803/#711 forensics windows). These tests pin the reviewed
// semantics (#813): forwarded lines stay structured (level=/msg=), matched
// noise is limited to one per interval, and the armed throttle SURVIVES
// logger swaps — slog.SetDefault rewrites stdlib log output unconditionally,
// which silently discarded a throttle installed before it (review ①).

import (
	"log"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *lockedBuffer) count(sub string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Count(b.buf.String(), sub)
}

// captureStdlog points the default slog logger at a buffer and disarms any
// leftover throttle from earlier tests; the deferred handler in slogx loggers
// resolves slog.Default() at call time, so package loggers follow along.
func captureStdlog(t *testing.T) *lockedBuffer {
	t.Helper()
	prevDefault := slog.Default()
	prevArmed := armedInterval.Load()
	buf := &lockedBuffer{}
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() {
		armedInterval.Store(prevArmed)
		slog.SetDefault(prevDefault)
	})
	return buf
}

func TestStdLogThrottle_ForwardsStructuredAndLimitsBurst(t *testing.T) {
	buf := captureStdlog(t)
	InstallStdLogThrottle(time.Hour) // long interval → deterministic single pass

	for range 20 {
		log.Printf("39 RTP packets lost")
	}
	require.Equal(t, 1, buf.count("RTP packets lost"),
		"burst of matched lines must collapse to one")
	require.Contains(t, buf.String(), `level=INFO`,
		"passed lines must stay structured, not raw stderr text")
	require.Contains(t, buf.String(), `msg="39 RTP packets lost"`)

	// Unmatched stdlib lines keep flowing, structured.
	log.Printf("some library said something")
	require.Equal(t, 1, buf.count("some library said something"))
}

// TestStdLogThrottle_SurvivesSetDefault pins review ① (P0): installing the
// throttle and THEN swapping the default logger must not lose it —
// slog.SetDefault rewrites log output, so the swap path itself re-arms.
func TestStdLogThrottle_SurvivesSetDefault(t *testing.T) {
	buf := captureStdlog(t)
	InstallStdLogThrottle(time.Hour)

	// The reconfigure swap (main's post-config re-logger, the remote-log
	// MultiHandler wrap in pkg/app/builders.go — anything).
	SetDefault(slog.New(slog.NewTextHandler(buf, nil)))

	for range 20 {
		log.Printf("41 RTP packets lost")
	}
	require.Equal(t, 1, buf.count("RTP packets lost"),
		"throttle must survive a logger swap")
	require.Contains(t, buf.String(), `level=INFO`)
}

func TestInstallStdLogThrottle_DisabledIsNoop(t *testing.T) {
	buf := captureStdlog(t)
	SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	InstallStdLogThrottle(0) // "off" — the plain SetDefault bridge stays

	for range 5 {
		log.Printf("7 RTP packets lost")
	}
	require.Equal(t, 5, buf.count("RTP packets lost"),
		"disabled throttle must pass every line")
}

func TestStdLogThrottle_ReopensAfterInterval(t *testing.T) {
	buf := captureStdlog(t)
	InstallStdLogThrottle(40 * time.Millisecond)

	log.Printf("3 RTP packets lost")
	log.Printf("4 RTP packets lost")
	require.Equal(t, 1, buf.count("RTP packets lost"), "within interval: dropped")

	require.Eventually(t, func() bool {
		log.Printf("5 RTP packets lost")
		return buf.count("RTP packets lost") >= 2
	}, 2*time.Second, 10*time.Millisecond, "after the interval a matched line must pass again")
}
