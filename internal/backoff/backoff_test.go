package backoff

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTieredBackoff(t *testing.T) {
	// Low attempts → 1s.
	require.Equal(t, time.Second, TieredBackoff(0))
	require.Equal(t, time.Second, TieredBackoff(4))
	// Mid attempts → 5s.
	require.Equal(t, 5*time.Second, TieredBackoff(5))
	require.Equal(t, 5*time.Second, TieredBackoff(9))
	// Persistent → 10s.
	require.Equal(t, 10*time.Second, TieredBackoff(10))
	require.Equal(t, 10*time.Second, TieredBackoff(19))
	// Long-term → 60s.
	require.Equal(t, time.Minute, TieredBackoff(20))
	require.Equal(t, time.Minute, TieredBackoff(999))
}

func TestTieredBackoffWithJitter(t *testing.T) {
	base := TieredBackoff(3) // 1s
	jittered := TieredBackoffWithJitter(3)
	// jitter is [0, 1s), so the result is within [base, base+1s).
	require.GreaterOrEqual(t, jittered, base)
	require.Less(t, jittered, base+time.Second)
}
