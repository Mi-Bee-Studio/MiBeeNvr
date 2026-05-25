package model

import (
	"fmt"
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

	// Send many frames — the buffer (150) will fill up, causing drops
	for i := 0; i < 250; i++ {
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

// audioFrameInfo holds a received audio frame for test assertions.
type audioFrameInfo struct {
	pts   int64
	codec AudioCodec
	data  []byte
}

func TestStreamHub_AudioSubscribeAndBroadcast(t *testing.T) {
	hub := newTestStreamHub(t)

	var (
		mu       sync.Mutex
		received map[string][]audioFrameInfo
	)
	received = make(map[string][]audioFrameInfo)

	// 3 audio consumers subscribe
	for _, id := range []string{"audio-1", "audio-2", "audio-3"} {
		cid := id
		err := hub.SubscribeAudio(cid, func(pts int64, codec AudioCodec, data []byte) {
			mu.Lock()
			received[cid] = append(received[cid], audioFrameInfo{pts: pts, codec: codec, data: data})
			mu.Unlock()
		})
		require.NoError(t, err)
	}

	// Broadcast 5 audio frames
	for i := int64(0); i < 5; i++ {
		hub.BroadcastAudio(i, AudioAAC, []byte{byte(i)})
	}

	// Wait for async delivery
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received["audio-1"]) == 5 &&
			len(received["audio-2"]) == 5 &&
			len(received["audio-3"]) == 5
	}, 2*time.Second, 10*time.Millisecond, "all audio consumers should receive all 5 frames")

	// Verify each consumer got the same frames in order
	mu.Lock()
	defer mu.Unlock()
	for _, id := range []string{"audio-1", "audio-2", "audio-3"} {
		frames := received[id]
		require.Len(t, frames, 5, "%s should have 5 frames", id)
		for i, f := range frames {
			require.Equal(t, int64(i), f.pts, "%s frame %d pts mismatch", id, i)
			require.Equal(t, AudioAAC, f.codec, "%s frame %d codec mismatch", id, i)
		}
	}
}

func TestStreamHub_AudioNonBlockingDrop(t *testing.T) {
	hub := newTestStreamHub(t)

	var slowReceived atomic.Int32
	err := hub.SubscribeAudio("slow", func(pts int64, codec AudioCodec, data []byte) {
		slowReceived.Add(1)
		time.Sleep(50 * time.Millisecond)
	})
	require.NoError(t, err)

	var fastReceived atomic.Int32
	err = hub.SubscribeAudio("fast", func(pts int64, codec AudioCodec, data []byte) {
		fastReceived.Add(1)
	})
	require.NoError(t, err)

	// Broadcast should return quickly — not blocked by slow consumer
	start := time.Now()
	hub.BroadcastAudio(1, AudioAAC, []byte{0x01})
	hub.BroadcastAudio(2, AudioAAC, []byte{0x02})
	elapsed := time.Since(start)
	require.Less(t, elapsed, 50*time.Millisecond, "BroadcastAudio should not block on slow consumers")

	// Fast consumer should receive quickly
	require.Eventually(t, func() bool {
		return fastReceived.Load() == 2
	}, 2*time.Second, 10*time.Millisecond, "fast audio consumer should receive all frames")

	// Slow consumer will eventually receive (it processes slowly)
	require.Eventually(t, func() bool {
		return slowReceived.Load() == 2
	}, 5*time.Second, 50*time.Millisecond, "slow audio consumer should eventually receive all frames")

	hub.UnsubscribeAudio("slow")
	hub.UnsubscribeAudio("fast")
}

func TestStreamHub_AudioUnsubscribeNoLeak(t *testing.T) {
	hub := newTestStreamHub(t)

	var received atomic.Int32
	err := hub.SubscribeAudio("leaky", func(pts int64, codec AudioCodec, data []byte) {
		received.Add(1)
	})
	require.NoError(t, err)

	// Should receive before unsubscribe
	hub.BroadcastAudio(1, AudioAAC, []byte{0x01})
	require.Eventually(t, func() bool {
		return received.Load() == 1
	}, 2*time.Second, 10*time.Millisecond)

	// Unsubscribe
	hub.UnsubscribeAudio("leaky")

	// Should NOT receive after unsubscribe
	hub.BroadcastAudio(2, AudioAAC, []byte{0x02})
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, int32(1), received.Load(), "should not receive audio after unsubscribe")

	require.Equal(t, 0, hub.AudioConsumerCount(), "no audio consumers should remain")
}

func TestStreamHub_AudioDoubleSubscribeError(t *testing.T) {
	hub := newTestStreamHub(t)

	err := hub.SubscribeAudio("dup", func(pts int64, codec AudioCodec, data []byte) {})
	require.NoError(t, err)

	err = hub.SubscribeAudio("dup", func(pts int64, codec AudioCodec, data []byte) {})
	require.Error(t, err, "duplicate audio subscribe should return error")

	hub.UnsubscribeAudio("dup")
}

func TestStreamHub_AudioUnsubscribeNonExistent(t *testing.T) {
	hub := newTestStreamHub(t)
	// Should not panic
	hub.UnsubscribeAudio("nonexistent")
}

func TestStreamHub_AudioDropTracking(t *testing.T) {
	hub := newTestStreamHub(t)

	blockCh := make(chan struct{})
	var received atomic.Int32
	err := hub.SubscribeAudio("tiny", func(pts int64, codec AudioCodec, data []byte) {
		received.Add(1)
		<-blockCh
	})
	require.NoError(t, err)

	// Send many frames — buffer (50) will fill up, causing drops
	for i := 0; i < 100; i++ {
		hub.BroadcastAudio(int64(i), AudioG711, []byte{byte(i)})
	}

	// Wait for buffer to fill
	time.Sleep(100 * time.Millisecond)

	// Release consumer
	close(blockCh)

	// Wait for buffered frames to be delivered
	require.Eventually(t, func() bool {
		return received.Load() >= 1
	}, 2*time.Second, 10*time.Millisecond)

	// Check drops were tracked
	drops := hub.AudioDrops("tiny")
	t.Logf("audio received=%d, drops=%d", received.Load(), drops)
	require.Greater(t, drops, int64(0), "audio drops should be tracked when buffer overflows")

	// Non-existent consumer should return 0 drops
	require.Equal(t, int64(0), hub.AudioDrops("nonexistent"))

	hub.UnsubscribeAudio("tiny")
}

func TestStreamHub_AudioConcurrentSubscribeUnsubscribe(t *testing.T) {
	hub := newTestStreamHub(t)

	const goroutines = 30
	const iterations = 20

	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// Concurrent audio subscribers
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			cid := fmt.Sprintf("audio-%d", id)
			for j := 0; j < iterations; j++ {
				_ = hub.SubscribeAudio(cid, func(pts int64, codec AudioCodec, data []byte) {})
				hub.UnsubscribeAudio(cid)
			}
		}(i)
	}

	// Concurrent audio broadcasters
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				hub.BroadcastAudio(int64(j), AudioAAC, []byte{byte(id)})
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
		t.Fatal("concurrent audio test timed out — possible deadlock")
	}
}

// --- Consumer Buffer Size Tests ---

// TestStreamHub_ConsumerBufferSize verifies the increased default buffer size.
func TestStreamHub_ConsumerBufferSize(t *testing.T) {
	hub := newTestStreamHub(t)
	require.Equal(t, 150, hub.consumerBufferSize, "consumer buffer should be 150 frames")
}

// TestStreamHub_BufferOverflow verifies drops occur when consumer buffer overflows.
func TestStreamHub_BufferOverflow(t *testing.T) {
	hub := newTestStreamHub(t)

	var received atomic.Int32
	blockCh := make(chan struct{})
	err := hub.Subscribe("buffer-test", func(pts int64, au [][]byte) {
		received.Add(1)
		<-blockCh
	})
	require.NoError(t, err)

	// Drain goroutine consumes 1 frame and blocks, leaving buffer capacity - 1 slots
	// Send well beyond capacity to force drops regardless of timing
	for i := 0; i < hub.consumerBufferSize+100; i++ {
		hub.Broadcast(int64(i), [][]byte{{byte(i)}})
	}
	time.Sleep(50 * time.Millisecond)

	// Drops must have occurred — we sent 250 frames to a 150-capacity buffer
	drops := hub.Drops("buffer-test")
	t.Logf("received=%d, drops=%d, bufferSize=%d", received.Load(), drops, hub.consumerBufferSize)
	require.Greater(t, drops, int64(0), "should have drops when sending beyond buffer capacity")

	close(blockCh)
	hub.Unsubscribe("buffer-test")
}
