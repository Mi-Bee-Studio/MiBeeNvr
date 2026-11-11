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
