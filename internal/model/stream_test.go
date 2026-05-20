package model

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// helper must be used in all test helpers (project convention).
func newTestStreamHub(t *testing.T) *StreamHub {
	t.Helper()
	return NewStreamHub()
}

func TestStreamHub_SubscribeAndBroadcast(t *testing.T) {
	t.Helper() // top-level test helper
	hub := newTestStreamHub(t)

	var (
		mu       sync.Mutex
		received map[string][]frameInfo
	)
	received = make(map[string][]frameInfo)

	// 3 consumers subscribe
	for _, id := range []string{"consumer-1", "consumer-2", "consumer-3"} {
		cid := id
		err := hub.Subscribe(cid, func(pts int64, au [][]byte) {
			mu.Lock()
			received[cid] = append(received[cid], frameInfo{pts: pts, au: au})
			mu.Unlock()
		})
		require.NoError(t, err)
	}

	// Broadcast 5 frames
	for i := int64(0); i < 5; i++ {
		hub.Broadcast(i, [][]byte{{byte(i)}})
	}

	// Wait for async delivery
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received["consumer-1"]) == 5 &&
			len(received["consumer-2"]) == 5 &&
			len(received["consumer-3"]) == 5
	}, 2*time.Second, 10*time.Millisecond, "all consumers should receive all 5 frames")

	// Verify each consumer got the same frames in order
	mu.Lock()
	defer mu.Unlock()
	for _, id := range []string{"consumer-1", "consumer-2", "consumer-3"} {
		frames := received[id]
		require.Len(t, frames, 5, "%s should have 5 frames", id)
		for i, f := range frames {
			require.Equal(t, int64(i), f.pts, "%s frame %d pts mismatch", id, i)
		}
	}
}

func TestStreamHub_NonBlockingSlowConsumer(t *testing.T) {
	hub := newTestStreamHub(t)

	var fastReceived atomic.Int32
	var slowReceived atomic.Int32

	// Slow consumer: blocks 100ms per frame
	err := hub.Subscribe("slow", func(pts int64, au [][]byte) {
		slowReceived.Add(1)
		time.Sleep(100 * time.Millisecond)
	})
	require.NoError(t, err)

	// Fast consumer: returns immediately
	err = hub.Subscribe("fast", func(pts int64, au [][]byte) {
		fastReceived.Add(1)
	})
	require.NoError(t, err)

	// Broadcast should return quickly — not blocked by slow consumer
	start := time.Now()
	hub.Broadcast(1, [][]byte{{0x01}})
	hub.Broadcast(2, [][]byte{{0x02}})
	elapsed := time.Since(start)
	require.Less(t, elapsed, 50*time.Millisecond, "Broadcast should not block on slow consumers")

	// Fast consumer should receive quickly
	require.Eventually(t, func() bool {
		return fastReceived.Load() == 2
	}, 2*time.Second, 10*time.Millisecond, "fast consumer should receive all frames")

	// Slow consumer will eventually receive (it processes slowly)
	require.Eventually(t, func() bool {
		return slowReceived.Load() == 2
	}, 5*time.Second, 50*time.Millisecond, "slow consumer should eventually receive all frames")

	// Cleanup
	hub.Unsubscribe("slow")
	hub.Unsubscribe("fast")
}

func TestStreamHub_UnsubscribeNoLeak(t *testing.T) {
	hub := newTestStreamHub(t)

	var received atomic.Int32
	err := hub.Subscribe("leaky", func(pts int64, au [][]byte) {
		received.Add(1)
	})
	require.NoError(t, err)

	// Should receive before unsubscribe
	hub.Broadcast(1, [][]byte{{0x01}})
	require.Eventually(t, func() bool {
		return received.Load() == 1
	}, 2*time.Second, 10*time.Millisecond)

	// Unsubscribe
	hub.Unsubscribe("leaky")

	// Should NOT receive after unsubscribe
	hub.Broadcast(2, [][]byte{{0x02}})
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, int32(1), received.Load(), "should not receive frames after unsubscribe")

	// Verify consumer count is 0
	require.Equal(t, 0, hub.ConsumerCount(), "no consumers should remain")
}

func TestStreamHub_FrameDropTracking(t *testing.T) {
	hub := newTestStreamHub(t)

	// Create a very small buffer to force drops
	// We'll use a blocking consumer with a tiny buffer
	blockCh := make(chan struct{}) // blocks consumer until we release

	var received atomic.Int32
	err := hub.Subscribe("tiny", func(pts int64, au [][]byte) {
		received.Add(1)
		<-blockCh // block until released
	})
	require.NoError(t, err)

	// Send many frames — the buffer (100) will fill up, causing drops
	for i := 0; i < 150; i++ {
		hub.Broadcast(int64(i), [][]byte{{byte(i)}})
	}

	// Wait a bit for buffer to fill
	time.Sleep(100 * time.Millisecond)

	// Release the consumer
	close(blockCh)

	// Wait for all buffered frames to be delivered
	require.Eventually(t, func() bool {
		return received.Load() >= 1 // at least some frames received
	}, 2*time.Second, 10*time.Millisecond)

	// Check that drops were tracked
	drops := hub.Drops("tiny")
	t.Logf("received=%d, drops=%d", received.Load(), drops)
	require.Greater(t, drops, int64(0), "drops should be tracked when buffer overflows")

	// Non-existent consumer should return 0 drops
	require.Equal(t, int64(0), hub.Drops("nonexistent"))

	hub.Unsubscribe("tiny")
}

func TestStreamHub_ConcurrentSubscribeUnsubscribe(t *testing.T) {
	hub := newTestStreamHub(t)

	const goroutines = 50
	const iterations = 20

	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// Concurrent subscribers
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			cid := string(rune('A' + id%26))
			for j := 0; j < iterations; j++ {
				_ = hub.Subscribe(cid, func(pts int64, au [][]byte) {})
				hub.Unsubscribe(cid)
			}
		}(i)
	}

	// Concurrent broadcasters
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				hub.Broadcast(int64(j), [][]byte{{byte(id)}})
			}
		}(i)
	}

	// This should complete without panics or deadlocks
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		// success
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent test timed out — possible deadlock")
	}
}

func TestStreamHub_DoubleSubscribeError(t *testing.T) {
	hub := newTestStreamHub(t)

	err := hub.Subscribe("dup", func(pts int64, au [][]byte) {})
	require.NoError(t, err)

	err = hub.Subscribe("dup", func(pts int64, au [][]byte) {})
	require.Error(t, err, "duplicate subscribe should return error")

	hub.Unsubscribe("dup")
}

func TestStreamHub_UnsubscribeNonExistent(t *testing.T) {
	hub := newTestStreamHub(t)

	// Should not panic
	hub.Unsubscribe("nonexistent")
}

// frameInfo holds a received frame for test assertions.
type frameInfo struct {
	pts int64
	au  [][]byte
}
