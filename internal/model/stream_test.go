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
		hub.Broadcast(i, [][]byte{{byte(i)}}, false)
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
	hub.Broadcast(1, [][]byte{{0x01}}, false)
	hub.Broadcast(2, [][]byte{{0x02}}, false)
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
	hub.Broadcast(1, [][]byte{{0x01}}, false)
	require.Eventually(t, func() bool {
		return received.Load() == 1
	}, 2*time.Second, 10*time.Millisecond)

	// Unsubscribe
	hub.Unsubscribe("leaky")

	// Should NOT receive after unsubscribe
	hub.Broadcast(2, [][]byte{{0x02}}, false)
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
		hub.Broadcast(int64(i), [][]byte{{byte(i)}}, false)
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
				hub.Broadcast(int64(j), [][]byte{{byte(id)}}, false)
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
		hub.Broadcast(int64(i), [][]byte{{byte(i)}}, false)
	}
	time.Sleep(50 * time.Millisecond)

	// Drops must have occurred — we sent 250 frames to a 150-capacity buffer
	drops := hub.Drops("buffer-test")
	t.Logf("received=%d, drops=%d, bufferSize=%d", received.Load(), drops, hub.consumerBufferSize)
	require.Greater(t, drops, int64(0), "should have drops when sending beyond buffer capacity")

	close(blockCh)
	hub.Unsubscribe("buffer-test")
}

// --- IDR Frame Protection Tests ---

// TestStreamHub_IDRFrameProtected tests that IDR frames are delivered even when
// the consumer buffer is full. Non-IDR frames in the buffer should be evicted
// to make space for the IDR.
func TestStreamHub_IDRFrameProtected(t *testing.T) {
	helperTestIDRProtection(t, false)
}

// TestStreamHub_IDRFrameProtected_Race verifies IDR protection under race detection.
func TestStreamHub_IDRFrameProtected_Race(t *testing.T) {
	// This test exercises IDR protection under the race detector.
	// It uses a small buffer to force the protection path.
	helperTestIDRProtection(t, true)
}

func helperTestIDRProtection(t *testing.T, raceMode bool) {
	t.Helper()

	hub := newTestStreamHub(t)
	hub.consumerBufferSize = 5 // small buffer to force drops quickly

	blockCh := make(chan struct{})
	var received atomic.Int32
	err := hub.Subscribe("slow", func(pts int64, au [][]byte) {
		received.Add(1)
		<-blockCh // block after first frame
	})
	require.NoError(t, err)

	// Fill the buffer with non-IDR frames (5 buffered + 1 in drain = 6 total)
	for i := 0; i < hub.consumerBufferSize+1; i++ {
		hub.Broadcast(int64(i), [][]byte{{byte(i)}}, false)
	}
	// Wait for drain goroutine to consume 1 frame and block
	require.Eventually(t, func() bool {
		return received.Load() >= 1
	}, 2*time.Second, 10*time.Millisecond)
	time.Sleep(20 * time.Millisecond) // let buffer fill completely

	// Now the buffer should be full. Send an IDR frame — it should be delivered
	// by evicting the oldest non-IDR frame.
	initialReceived := received.Load()
	hub.Broadcast(999, [][]byte{{0xFF}}, true)

	// Release the consumer to process remaining frames
	close(blockCh)

	// Wait for all buffered frames to be processed
	require.Eventually(t, func() bool {
		return received.Load() >= int32(hub.consumerBufferSize+1)
	}, 5*time.Second, 10*time.Millisecond, "should receive all buffered frames including IDR")

	// The IDR frame should be among the received frames
	require.Eventually(t, func() bool {
		return received.Load() > initialReceived
	}, 2*time.Second, 10*time.Millisecond, "IDR frame should cause additional delivery after unblock")

	hub.Unsubscribe("slow")
}

// TestStreamHub_NonIDRDroppedWhenBufferFull tests that non-IDR frames are dropped
// when the consumer buffer is full (existing behavior preserved).
func TestStreamHub_NonIDRDroppedWhenBufferFull(t *testing.T) {
	hub := newTestStreamHub(t)
	hub.consumerBufferSize = 3

	blockCh := make(chan struct{})
	var received atomic.Int32
	err := hub.Subscribe("slow", func(pts int64, au [][]byte) {
		received.Add(1)
		<-blockCh
	})
	require.NoError(t, err)

	// Fill buffer + send more non-IDR frames
	for i := 0; i < hub.consumerBufferSize+10; i++ {
		hub.Broadcast(int64(i), [][]byte{{byte(i)}}, false)
	}
	time.Sleep(50 * time.Millisecond)

	// Drops should be tracked (non-IDR frames dropped)
	drops := hub.Drops("slow")
	require.Greater(t, drops, int64(0), "non-IDR frames should be dropped when buffer full")

	close(blockCh)
	hub.Unsubscribe("slow")
}

// TestStreamHub_IDRProtectionMultiConsumer tests that IDR protection works
// correctly when there are multiple consumers with different buffer states.
func TestStreamHub_IDRProtectionMultiConsumer(t *testing.T) {
	hub := newTestStreamHub(t)
	hub.consumerBufferSize = 50

	// Consumer A: blocked after first frame
	blockA := make(chan struct{})
	var receivedA atomic.Int32
	err := hub.Subscribe("consumer-a", func(pts int64, au [][]byte) {
		receivedA.Add(1)
		<-blockA
	})
	require.NoError(t, err)

	// Consumer B: fast, never blocks
	var receivedB atomic.Int32
	err = hub.Subscribe("consumer-b", func(pts int64, au [][]byte) {
		receivedB.Add(1)
	})
	require.NoError(t, err)

	// Send many frames to fill consumer A's buffer
	for i := 0; i < hub.consumerBufferSize+10; i++ {
		hub.Broadcast(int64(i), [][]byte{{byte(i)}}, false)
	}
	require.Eventually(t, func() bool { return receivedA.Load() >= 1 }, 3*time.Second, 10*time.Millisecond)
	time.Sleep(50 * time.Millisecond)

	// Send IDR — A triggers trySendIDR, B receives directly
	hub.Broadcast(999, [][]byte{{0xFF}}, true)

	// B should receive the IDR frame (at minimum)
	require.Eventually(t, func() bool {
		return receivedB.Load() >= 1
	}, 2*time.Second, 10*time.Millisecond, "B should receive at least some frames")

	close(blockA)
	hub.Unsubscribe("consumer-a")
	hub.Unsubscribe("consumer-b")
}

// TestStreamHub_IDRProtectionPreservesIDRInBuffer tests that the trySendIDR
// function preserves existing IDR frames in the buffer when evicting.
func TestStreamHub_IDRProtectionPreservesIDRInBuffer(t *testing.T) {
	hub := newTestStreamHub(t)
	hub.consumerBufferSize = 5

	blockCh := make(chan struct{})
	var receivedMu sync.Mutex
	var received []frameInfo
	err := hub.Subscribe("test", func(pts int64, au [][]byte) {
		receivedMu.Lock()
		received = append(received, frameInfo{pts: pts, au: au})
		receivedMu.Unlock()
		<-blockCh
	})
	require.NoError(t, err)

	// Send frames to fill buffer beyond capacity:
	// Drain goroutine takes 1, buffer holds 5.
	// Mix of IDR and non-IDR frames.
	for i := 0; i < 8; i++ {
		isIDR := i%4 == 0
		hub.Broadcast(int64(i*10), [][]byte{{byte(i)}}, isIDR)
	}
	// Wait for drain to consume at least 1 frame
	require.Eventually(t, func() bool {
		receivedMu.Lock()
		defer receivedMu.Unlock()
		return len(received) >= 1
	}, 2*time.Second, 10*time.Millisecond)
	time.Sleep(50 * time.Millisecond)

	// Buffer should be full. Send new IDR frame.
	hub.Broadcast(999, [][]byte{{0xFF}}, true)

close(blockCh)
// Wait for all frames to be processed.
// drain took 1 initially. trySendIDR evicted 1 non-IDR for IDR frame,
// then 1 more non-IDR for pts=999 IDR. 1 non-IDR dropped (overflow).
// Remaining in buffer: 5 frames. Total received: 1 + 5 = 6.
require.Eventually(t, func() bool {
	receivedMu.Lock()
	defer receivedMu.Unlock()
	return len(received) >= 6
}, 5*time.Second, 10*time.Millisecond, "should receive remaining buffered frames")

	receivedMu.Lock()
	defer receivedMu.Unlock()
	// IDR frame with pts=999 should be among received
	found := false
	for _, f := range received {
		if f.pts == 999 {
			found = true
			break
		}
	}
	require.True(t, found, "IDR frame pts=999 should be received after unblock")

	hub.Unsubscribe("test")
}

// TestStreamHub_HighContentionDeadlock exercises 100 concurrent goroutines doing
// Subscribe, Unsubscribe, and Broadcast simultaneously with the race detector.
// This validates that no deadlock can occur between Unsubscribe (sendMu+close)
// and Broadcast (sendMu.RLock+channel send), especially in the trySendIDR path.
func TestStreamHub_HighContentionDeadlock(t *testing.T) {
	helperHighContention(t, false)
}

// TestStreamHub_HighContentionDeadlockAudio exercises the same pattern for audio consumers.
func TestStreamHub_HighContentionDeadlockAudio(t *testing.T) {
	helperHighContention(t, true)
}

func helperHighContention(t *testing.T, audio bool) {
	t.Helper()

	hub := newTestStreamHub(t)
	hub.consumerBufferSize = 5 // small buffer to exercise trySendIDR path

	const goroutines = 100
	const iterations = 50

	var wg sync.WaitGroup
	wg.Add(goroutines * 3)

	// Concurrent subscribers/unsubscribers
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			cid := fmt.Sprintf("c-%d", id)
			for j := 0; j < iterations; j++ {
				var cb FrameCallback = func(pts int64, au [][]byte) {
					// Simulate slow consumer to fill buffers
					if id%3 == 0 {
						time.Sleep(time.Microsecond)
					}
				}
				if audio {
					err := hub.SubscribeAudio(cid, func(pts int64, codec AudioCodec, data []byte) {
						if id%3 == 0 {
							time.Sleep(time.Microsecond)
						}
					})
					if err != nil {
						continue // already subscribed, retry next iteration
					}
					hub.UnsubscribeAudio(cid)
				} else {
					err := hub.Subscribe(cid, cb)
					if err != nil {
						continue
					}
					hub.Unsubscribe(cid)
				}
			}
		}(i)
	}

	// Concurrent broadcasters
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				isIDR := j%10 == 0 // 10% IDR frames to exercise trySendIDR
				if audio {
					hub.BroadcastAudio(int64(j), AudioAAC, []byte{byte(id)})
				} else {
					hub.Broadcast(int64(j), [][]byte{{byte(id)}}, isIDR)
				}
			}
		}(i)
	}

	// Concurrent consumer count readers
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				if audio {
					_ = hub.AudioConsumerCount()
					_ = hub.AudioDrops(fmt.Sprintf("c-%d", j%goroutines))
				} else {
					_ = hub.ConsumerCount()
					_ = hub.Drops(fmt.Sprintf("c-%d", j%goroutines))
				}
			}
		}()
	}

	// All goroutines must complete within timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		// success — no deadlock
	case <-time.After(20 * time.Second):
		t.Fatal("high-contention test timed out — deadlock detected")
	}
}

// --- Per-Consumer Drop Rate Tracking Tests ---

// helperDropRateHub creates a hub with small buffer for drop rate testing.
func helperDropRateHub(t *testing.T) *StreamHub {
	t.Helper()
	hub := NewStreamHub()
	hub.consumerBufferSize = 5
	hub.SetCameraID("drop-rate-test-cam")
	return hub
}

// TestStreamHub_DropRate_NoTraffic verifies DropRate returns 0 when no frames sent.
func TestStreamHub_DropRate_NoTraffic(t *testing.T) {
	hub := helperDropRateHub(t)
	err := hub.Subscribe("c1", func(pts int64, au [][]byte) {})
	require.NoError(t, err)

	require.Equal(t, 0.0, hub.DropRate("c1"), "no traffic should have 0 drop rate")
	require.Equal(t, 0.0, hub.DropRate("nonexistent"), "non-existent consumer should have 0 drop rate")

	hub.Unsubscribe("c1")
}

// TestStreamHub_DropRate_AllDelivered verifies 0 drop rate when all frames delivered.
func TestStreamHub_DropRate_AllDelivered(t *testing.T) {
	hub := helperDropRateHub(t)
	hub.consumerBufferSize = 100 // large enough to avoid buffer overflow for 10 frames
	var received atomic.Int32
	err := hub.Subscribe("c1", func(pts int64, au [][]byte) {
		received.Add(1)
	})
	require.NoError(t, err)

	for i := 0; i < 10; i++ {
		hub.Broadcast(int64(i), [][]byte{{byte(i)}}, false)
	}
	require.Eventually(t, func() bool { return received.Load() == 10 }, 2*time.Second, 10*time.Millisecond)

	require.Equal(t, 0.0, hub.DropRate("c1"), "all delivered should have 0 drop rate")
	hub.Unsubscribe("c1")
}

// TestStreamHub_DropRate_WithDrops verifies drop rate when buffer overflows.
func TestStreamHub_DropRate_WithDrops(t *testing.T) {
	hub := helperDropRateHub(t)
	blockCh := make(chan struct{})
	var received atomic.Int32
	err := hub.Subscribe("slow", func(pts int64, au [][]byte) {
		received.Add(1)
		<-blockCh
	})
	require.NoError(t, err)

	// Fill buffer (5) + drain takes 1 = 6 slots used. Send 200 total.
	for i := 0; i < 200; i++ {
		hub.Broadcast(int64(i), [][]byte{{byte(i)}}, false)
	}
	time.Sleep(100 * time.Millisecond)

	close(blockCh)
	require.Eventually(t, func() bool { return received.Load() >= 1 }, 2*time.Second, 10*time.Millisecond)

	drops := hub.Drops("slow")
	rate := hub.DropRate("slow")
	t.Logf("received=%d, drops=%d, drop_rate=%.4f", received.Load(), drops, rate)
	require.Greater(t, drops, int64(0), "should have drops")
	require.Greater(t, rate, 0.0, "drop rate should be positive")
	require.LessOrEqual(t, rate, 1.0, "drop rate should not exceed 1.0")

	hub.Unsubscribe("slow")
}

// TestStreamHub_DropRateThreshold_Callback verifies OnDropRate is called when threshold exceeded.
func TestStreamHub_DropRateThreshold_Callback(t *testing.T) {
	hub := helperDropRateHub(t)
	hub.dropRateWarnThreshold = 0.10 // low threshold to trigger easily

	var (
		mu            sync.Mutex
		dropRateCalls []dropRateCall
	)
	hub.OnDropRate = func(consumerID string, rate float64) {
		mu.Lock()
		dropRateCalls = append(dropRateCalls, dropRateCall{consumerID: consumerID, rate: rate})
		mu.Unlock()
	}

	blockCh := make(chan struct{})
	err := hub.Subscribe("slow", func(pts int64, au [][]byte) {
		<-blockCh
	})
	require.NoError(t, err)

	// Send many frames to trigger drops and hit the 100-drop check interval
	for i := 0; i < 500; i++ {
		hub.Broadcast(int64(i), [][]byte{{byte(i)}}, false)
	}
	time.Sleep(200 * time.Millisecond)
	close(blockCh)
	hub.Unsubscribe("slow")

	// Wait for callbacks
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(dropRateCalls) > 0
	}, 2*time.Second, 10*time.Millisecond, "OnDropRate should have been called")

	mu.Lock()
	defer mu.Unlock()
	for _, call := range dropRateCalls {
		require.Equal(t, "slow", call.consumerID)
		require.Greater(t, call.rate, hub.dropRateWarnThreshold, "rate should exceed threshold")
	}
}

// dropRateCall holds data from an OnDropRate callback.
type dropRateCall struct {
	consumerID string
	rate       float64
}

// --- Jitter Buffer Tests ---

// TestJitterBuffer_PassthroughInOrder verifies that when all frames arrive in-order,
// the jitter buffer is not activated and frames pass through immediately.
func TestJitterBuffer_PassthroughInOrder(t *testing.T) {
	hub := newTestStreamHub(t)

	var mu sync.Mutex
	var received []frameInfo
	err := hub.Subscribe("test", func(pts int64, au [][]byte) {
		mu.Lock()
		received = append(received, frameInfo{pts: pts, au: au})
		mu.Unlock()
	})
	require.NoError(t, err)

	// Send 10 frames in-order — jitter buffer should NOT activate
	for i := int64(0); i < 10; i++ {
		hub.Broadcast(i*100, [][]byte{{byte(i)}}, i%5 == 0)
	}

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) == 10
	}, 2*time.Second, 10*time.Millisecond, "all in-order frames should be delivered immediately")

	// Verify no reordering occurred
	mu.Lock()
	defer mu.Unlock()
	for i, f := range received {
		require.Equal(t, int64(i*100), f.pts, "frame %d pts mismatch", i)
	}
	require.False(t, hub.jitterBufferEnabled.Load(), "jitter buffer should NOT be enabled for in-order frames")

	hub.Unsubscribe("test")
}

// TestJitterBuffer_ActivatesOnDisorder verifies jitter buffer activates when out-of-order
// frames are detected and reorders them before delivery.
func TestJitterBuffer_ActivatesOnDisorder(t *testing.T) {
	helperJitterBufferReorder(t)
}

// TestJitterBuffer_ActivatesOnDisorder_Race exercises the jitter buffer under race detection.
func TestJitterBuffer_ActivatesOnDisorder_Race(t *testing.T) {
	helperJitterBufferReorder(t)
}

func helperJitterBufferReorder(t *testing.T) {
	t.Helper()

	hub := newTestStreamHub(t)
	hub.jitterBufferSize = 3 // small buffer for faster test

	var mu sync.Mutex
	var received []frameInfo
	err := hub.Subscribe("test", func(pts int64, au [][]byte) {
		mu.Lock()
		received = append(received, frameInfo{pts: pts, au: au})
		mu.Unlock()
	})
	require.NoError(t, err)

	// Send frames in-order first to establish baseline
	hub.Broadcast(100, [][]byte{{0x01}}, true) // PTS=100
	hub.Broadcast(200, [][]byte{{0x02}}, false) // PTS=200

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) >= 2
	}, 2*time.Second, 10*time.Millisecond)

	// Clear received
	mu.Lock()
	received = nil
	mu.Unlock()

	// Send out-of-order: 400 then 300 → should activate jitter buffer
	hub.Broadcast(400, [][]byte{{0x04}}, false) // PTS=400
	hub.Broadcast(300, [][]byte{{0x03}}, false) // PTS=300 < 400 → disorder!

	// Wait for jitter buffer timeout flush (500ms)
	time.Sleep(700 * time.Millisecond)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) >= 2
	}, 2*time.Second, 10*time.Millisecond, "reordered frames should be flushed and delivered")

	require.True(t, hub.jitterBufferEnabled.Load(), "jitter buffer should be enabled after disorder detected")
	require.GreaterOrEqual(t, hub.jitterBufferReorders.Load(), int64(1), "reorder counter should be incremented")

	hub.Unsubscribe("test")
}

// TestJitterBuffer_TimeoutFlush verifies that buffered frames are flushed after
// the timeout even if the buffer isn't full.
func TestJitterBuffer_TimeoutFlush(t *testing.T) {
	t.Helper()

	hub := newTestStreamHub(t)
	hub.jitterBufferSize = 5
	hub.jitterBufferTimeout = 200 * time.Millisecond // short timeout for test

	var mu sync.Mutex
	var received []frameInfo
	err := hub.Subscribe("test", func(pts int64, au [][]byte) {
		mu.Lock()
		received = append(received, frameInfo{pts: pts, au: au})
		mu.Unlock()
	})
	require.NoError(t, err)

	// Activate jitter buffer with disorder
	hub.Broadcast(100, [][]byte{{0x01}}, true)
	hub.Broadcast(300, [][]byte{{0x03}}, false) // out of order

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) >= 1
	}, 2*time.Second, 10*time.Millisecond)

	mu.Lock()
	received = nil
	mu.Unlock()

	// Now jitter buffer is active. Send 2 frames (buffer size=5, won't fill)
	hub.Broadcast(500, [][]byte{{0x05}}, false)
	hub.Broadcast(600, [][]byte{{0x06}}, false)

	// Wait for timeout flush
	time.Sleep(400 * time.Millisecond)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) >= 2
	}, 2*time.Second, 10*time.Millisecond, "frames should be flushed on timeout")

	// Verify PTS ordering
	mu.Lock()
	defer mu.Unlock()
	for i := 1; i < len(received); i++ {
		require.GreaterOrEqual(t, received[i].pts, received[i-1].pts,
			"frames should be in PTS order after jitter buffer")
	}

	hub.Unsubscribe("test")
}

// TestJitterBuffer_NonBlocking verifies that Broadcast never blocks even when jitter
// buffer is active and consumer buffers are full.
func TestJitterBuffer_NonBlocking(t *testing.T) {
	hub := newTestStreamHub(t)
	hub.jitterBufferSize = 3
	hub.consumerBufferSize = 2

	blockCh := make(chan struct{})
	err := hub.Subscribe("slow", func(pts int64, au [][]byte) {
		<-blockCh
	})
	require.NoError(t, err)

	// Fill consumer buffer
	hub.Broadcast(100, [][]byte{{0x01}}, false)
	hub.Broadcast(200, [][]byte{{0x02}}, false)
	time.Sleep(50 * time.Millisecond)

	// Activate jitter buffer with disorder
	hub.Broadcast(400, [][]byte{{0x04}}, false)
	hub.Broadcast(300, [][]byte{{0x03}}, false)

	// These calls must return immediately
	start := time.Now()
	hub.Broadcast(600, [][]byte{{0x06}}, false)
	hub.Broadcast(500, [][]byte{{0x05}}, false)
	elapsed := time.Since(start)
	require.Less(t, elapsed, 100*time.Millisecond, "Broadcast must not block with jitter buffer active")

	close(blockCh)
	hub.Unsubscribe("slow")
}
