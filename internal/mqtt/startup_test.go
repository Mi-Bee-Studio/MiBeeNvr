package mqtt

// Lifecycle guard tests (#570): unconfigured Start is a no-op, an
// unreachable broker fails fast (no hangs), Stop is idempotent, and
// PublishAIDetection refuses without a live connection. Deterministic —
// port 1 on loopback refuses instantly.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStart_UnconfiguredNoop(t *testing.T) {
	c := NewClient("", "cid", "nvr", "", "", nil)
	require.NoError(t, c.Start(context.Background()))
	require.NoError(t, c.Stop(), "Stop without a client is a clean no-op")
}

// TestStart_UnreachableBrokerRetriesUntilCtxDone: since #661 an unreachable
// broker no longer fails Start — it retries with backoff until the context
// is done, then surfaces the last dial error without hanging.
func TestStart_UnreachableBrokerRetriesUntilCtxDone(t *testing.T) {
	c := NewClient("tcp://127.0.0.1:1", "cid", "nvr", "", "", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := c.Start(ctx)
	require.Error(t, err, "context done before connecting must surface the dial error")
	require.Less(t, time.Since(start), 5*time.Second, "must not hang past the context deadline")
}

func TestPublishAIDetection_NotConnected(t *testing.T) {
	c := NewClient("tcp://127.0.0.1:1", "cid", "nvr", "", "", nil)
	require.Error(t, c.PublishAIDetection(context.Background(), "cam1", "person", nil),
		"publishing without a connection must fail cleanly")
}
