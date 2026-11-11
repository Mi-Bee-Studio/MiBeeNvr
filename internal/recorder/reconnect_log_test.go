package recorder

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReconnectLogLimiter(t *testing.T) {
	base := time.Now()
	var l ReconnectLogLimiter

	// First occurrence always emits.
	emit, n := l.Allow(base, "dial tcp: refused")
	require.True(t, emit)
	require.Zero(t, n)

	// Exact repeat within the window: suppressed, count absorbed.
	emit, n = l.Allow(base.Add(time.Minute), "dial tcp: refused")
	require.False(t, emit)
	require.Zero(t, n)
	emit, n = l.Allow(base.Add(2*time.Minute), "dial tcp: refused")
	require.False(t, emit)

	// A DIFFERENT error emits immediately and reports the absorbed repeats.
	emit, n = l.Allow(base.Add(3*time.Minute), "auth failed: 401")
	require.True(t, emit)
	require.Equal(t, 2, n)

	// The new error's own repeats suppress.
	emit, n = l.Allow(base.Add(4*time.Minute), "auth failed: 401")
	require.False(t, emit)

	// Window elapsed with the same error (lastAt = 3min emission + 10min): emit, carrying the count.
	emit, n = l.Allow(base.Add(13*time.Minute), "auth failed: 401")
	require.True(t, emit)
	require.Equal(t, 1, n)
}

// The production flood shape (#perf D⑥ field case): every attempt binds a
// fresh ephemeral port, so raw text differs per retry while the failure is
// identical. The fingerprint must strip volatile endpoints.
func TestReconnectLogLimiterVolatileEndpoints(t *testing.T) {
	base := time.Now()
	var l ReconnectLogLimiter

	mk := func(port int) string {
		return `http connect: Get "http://192.0.2.140:81/stream": read tcp 192.0.2.30:` +
			itoa(port) + `->192.0.2.140:81: read: connection reset by peer`
	}

	emit, _ := l.Allow(base, mk(41634))
	require.True(t, emit, "first occurrence emits")

	emit, n := l.Allow(base.Add(time.Minute), mk(34934))
	require.False(t, emit, "same failure with a different ephemeral port must suppress")
	require.Zero(t, n)

	emit, n = l.Allow(base.Add(2*time.Minute), `miss connect: read udp [::]:36173: i/o timeout`)
	require.True(t, emit, "a genuinely different error emits")
	require.Equal(t, 1, n)
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}
