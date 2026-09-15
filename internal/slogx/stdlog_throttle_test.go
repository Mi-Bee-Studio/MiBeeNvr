package slogx

// gortsplib v5 reports RTP sequence gaps through the STANDARD library
// logger ("23 RTP packets lost" per gap, several per second per stream). On
// a small journald quota this evicted the unit's own history twice
// (2026-09-14 #803 forensics window; 2026-09-15 boot window — 36k lines /
// 3.5h consumed the whole 24MB journal, including subscription-startup
// evidence for #711). The throttle keeps the signal at 1 line per interval.

import (
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

func TestStdLogThrottle_RateLimitsRTPLostOnly(t *testing.T) {
	out := &lockedBuffer{}
	th := &stdLogThrottle{out: out, match: rtpLostPattern, minInterval: time.Second}

	// Burst of 20 matched lines → exactly one passes.
	for i := 0; i < 20; i++ {
		n, err := th.Write([]byte("23 RTP packets lost\n"))
		require.NoError(t, err)
		require.Equal(t, len("23 RTP packets lost\n"), n)
	}
	require.Equal(t, 1, strings.Count(out.String(), "RTP"), "only the first matched line may pass within the interval")

	// Unmatched lines flow untouched, even in bursts.
	for i := 0; i < 20; i++ {
		_, err := th.Write([]byte("some other library message\n"))
		require.NoError(t, err)
	}
	require.Equal(t, 21, strings.Count(out.String(), "\n"), "1 throttled RTP line + 20 pass-through lines")

	// After the interval elapses, matched lines flow again.
	th.mu.Lock()
	th.last = time.Now().Add(-2 * time.Second)
	th.mu.Unlock()
	_, err := th.Write([]byte("5 RTP packets lost\n"))
	require.NoError(t, err)
	require.Equal(t, 2, strings.Count(out.String(), "RTP"))
}

func TestStdLogThrottle_PatternMatchesGortsplibFormats(t *testing.T) {
	require.True(t, rtpLostPattern.MatchString("1 RTP packets lost\n"))
	require.True(t, rtpLostPattern.MatchString("42 RTP packets lost\n"))
	require.False(t, rtpLostPattern.MatchString("some other line\n"))
	require.False(t, rtpLostPattern.MatchString("2026/09/15 08:14:00 something\n"))
}
