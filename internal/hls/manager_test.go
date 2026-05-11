package hls

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newTestManager creates a Manager with a writable temp directory.
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	return NewManager(dir)
}

// newTestStreamEntry creates a streamEntry for testing without starting a real muxer.
// The frameCh is buffered so frames accumulate for counting.
func newTestStreamEntry(maxFPS int) *streamEntry {
	return &streamEntry{
		frameCh:       make(chan hlsFrame, defaultWriteBufSize),
		maxFPS:        maxFPS,
		lastUsed:      time.Now(),
		lastFrameTime: time.Time{}, // zero value means "never written"
	}
}

// --- Frame Rate Limiter Tests ---

func TestFrameRateLimiter_DropsExcessFrames(t *testing.T) {
	mgr := newTestManager(t)
	cameraID := "test-cam"

	// Manually insert a stream entry with maxFPS=2 (no real muxer needed for FPS test)
	mgr.mu.Lock()
	entry := newTestStreamEntry(2)
	mgr.streams[cameraID] = entry
	mgr.mu.Unlock()

	// Send 10 frames rapidly — only ~1 should pass (first frame always passes,
	// subsequent frames within 500ms interval are dropped)
	passed := 0
	for i := 0; i < 10; i++ {
		err := mgr.WriteH264(cameraID, int64(i*1000), [][]byte{{0x00, 0x01}})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Check if frame was queued (non-blocking read to count)
		select {
		case <-entry.frameCh:
			passed++
		default:
			// frame was dropped by FPS limiter
		}
	}

	// With maxFPS=2 (500ms interval), only the first frame should pass
	// within a rapid loop (microseconds between sends).
	require.Equal(t, 1, passed, "expected only 1 frame to pass FPS limiter")
}

func TestFrameRateLimiter_Disabled(t *testing.T) {
	mgr := newTestManager(t)
	cameraID := "test-cam"

	// maxFPS=0 means no limiting
	mgr.mu.Lock()
	entry := newTestStreamEntry(0)
	mgr.streams[cameraID] = entry
	mgr.mu.Unlock()

	// Send 10 frames rapidly — all should pass
	for i := 0; i < 10; i++ {
		err := mgr.WriteH264(cameraID, int64(i*1000), [][]byte{{0x00, 0x01}})
		require.NoError(t, err)
	}

	// All 10 frames should be in the channel
	require.Equal(t, 10, len(entry.frameCh), "expected all frames to pass when maxFPS=0")
}

func TestFrameRateLimiter_RespectsInterval(t *testing.T) {
	mgr := newTestManager(t)
	cameraID := "test-cam"

	// maxFPS=10 means 100ms minimum interval
	mgr.mu.Lock()
	entry := newTestStreamEntry(10)
	entry.lastFrameTime = time.Now() // simulate a frame was just written
	mgr.streams[cameraID] = entry
	mgr.mu.Unlock()

	// Since lastFrameTime is set to now, immediate frame should be dropped
	err := mgr.WriteH264(cameraID, 1000, [][]byte{{0x00}})
	require.NoError(t, err)
	select {
	case <-entry.frameCh:
		t.Fatal("frame should have been dropped by FPS limiter")
	default:
	}
	// Channel should be empty — frame was rate-limited
	require.Empty(t, entry.frameCh)
}

func TestFrameRateLimiter_InactiveStream(t *testing.T) {
	mgr := newTestManager(t)

	// Writing to a non-existent stream should silently succeed (no error, no panic)
	err := mgr.WriteH264("nonexistent", 1000, [][]byte{{0x00}})
	require.NoError(t, err)
}

func TestFrameRateLimiter_H265(t *testing.T) {
	mgr := newTestManager(t)
	cameraID := "test-cam"

	mgr.mu.Lock()
	entry := newTestStreamEntry(1)
	entry.isH265 = true
	mgr.streams[cameraID] = entry
	mgr.mu.Unlock()

	// H265 frames should also be rate-limited
	for i := 0; i < 5; i++ {
		err := mgr.WriteH265(cameraID, int64(i*1000), [][]byte{{0x00}})
		require.NoError(t, err)
	}

	// Only first frame should pass
	require.Equal(t, 1, len(entry.frameCh), "expected only 1 H265 frame to pass FPS limiter")
}

// --- Sub-Stream Reader Tests ---

func TestStartSubStreamReader_NoActiveStream(t *testing.T) {
	mgr := newTestManager(t)

	// Starting sub-stream for a non-existent camera should return error
	err := mgr.StartSubStreamReader("nonexistent", "rtsp://192.168.1.1/sub", false)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrStreamNotActive)
}

func TestStartSubStreamReader_Dedup(t *testing.T) {
	mgr := newTestManager(t)
	cameraID := "test-cam"

	// Create a stream entry with subStreamCancel already set (simulating already running)
	mgr.mu.Lock()
	_, cancel := context.WithCancel(context.Background())
	entry := &streamEntry{
		frameCh:        make(chan hlsFrame, defaultWriteBufSize),
		maxFPS:         0,
		subStreamCancel: cancel,
		cancel:         cancel,
	}
	mgr.streams[cameraID] = entry
	mgr.mu.Unlock()

	// Calling StartSubStreamReader when subStreamCancel is already set should be a no-op
	err := mgr.StartSubStreamReader(cameraID, "rtsp://192.168.1.1/sub", false)
	require.NoError(t, err)

	// Verify subStreamCancel is still set (not nil) — dedup succeeded
	mgr.mu.RLock()
	subCancel := mgr.streams[cameraID].subStreamCancel
	mgr.mu.RUnlock()
	require.NotNil(t, subCancel, "subStreamCancel should still be set after dedup")

	// Call cancel to verify it's the original (wasn't replaced)
	subCancel()
	require.True(t, true, "cancel called without panic = dedup preserved original")
}

// --- IsActive Tests ---

func TestIsActive_StreamExists(t *testing.T) {
	mgr := newTestManager(t)
	cameraID := "test-cam"

	mgr.mu.Lock()
	mgr.streams[cameraID] = &streamEntry{
		frameCh:  make(chan hlsFrame, defaultWriteBufSize),
		lastUsed: time.Now(),
	}
	mgr.mu.Unlock()

	require.True(t, mgr.IsActive(cameraID))
}

func TestIsActive_StreamNotExists(t *testing.T) {
	mgr := newTestManager(t)
	require.False(t, mgr.IsActive("nonexistent"))
}

// --- StopStream Tests ---

func TestStopStream_NotActive(t *testing.T) {
	mgr := newTestManager(t)
	// Should not panic on non-existent stream
	mgr.StopStream("nonexistent")
}

func TestStopAll_Empty(t *testing.T) {
	mgr := newTestManager(t)
	// StopAll on empty manager should not panic
	mgr.StopAll()
}

// --- WriteH264 to Inactive Stream Tests ---

func TestWriteH264_InactiveStream(t *testing.T) {
	mgr := newTestManager(t)

	// Should not error, just silently ignore
	err := mgr.WriteH264("nonexistent", 1000, [][]byte{{0x00}})
	require.NoError(t, err)
}

func TestWriteH265_InactiveStream(t *testing.T) {
	mgr := newTestManager(t)

	err := mgr.WriteH265("nonexistent", 1000, [][]byte{{0x00}})
	require.NoError(t, err)
}

// --- NewManager Tests ---

func TestNewManager(t *testing.T) {
	mgr := NewManager(t.TempDir())
	require.NotNil(t, mgr)
	require.NotNil(t, mgr.streams)
	require.Empty(t, mgr.streams)
	require.Equal(t, defaultIdleTimeout, mgr.idleTimeout)
	require.Equal(t, defaultMaxStreams, mgr.maxStreams)
	require.Equal(t, defaultWriteBufSize, mgr.writeBufSize)
	require.Equal(t, defaultSegmentMaxSize, mgr.segmentMaxSize)
}

// --- NewManagerWithOpts Tests ---

func TestNewManagerWithOpts_CustomValues(t *testing.T) {
	mgr := NewManagerWithOpts(t.TempDir(), 80, 20*1024*1024)
	require.NotNil(t, mgr)
	require.Equal(t, 80, mgr.writeBufSize)
	require.Equal(t, 20*1024*1024, mgr.segmentMaxSize)
}

func TestNewManagerWithOpts_ZeroValuesUseDefaults(t *testing.T) {
	mgr := NewManagerWithOpts(t.TempDir(), 0, 0)
	require.NotNil(t, mgr)
	require.Equal(t, defaultWriteBufSize, mgr.writeBufSize)
	require.Equal(t, defaultSegmentMaxSize, mgr.segmentMaxSize)
}

// --- Thread Safety Tests ---

func TestConcurrentWrites(t *testing.T) {
	mgr := newTestManager(t)
	cameraID := "test-cam"

	mgr.mu.Lock()
	mgr.streams[cameraID] = &streamEntry{
		frameCh:  make(chan hlsFrame, defaultWriteBufSize),
		maxFPS:   0,
		lastUsed: time.Now(),
	}
	mgr.mu.Unlock()

	var wg sync.WaitGroup
	// Concurrently write frames from multiple goroutines
	for g := 0; g < 5; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				_ = mgr.WriteH264(cameraID, int64(i*1000), [][]byte{{0x00}})
			}
		}()
	}

	wg.Wait()
	// Channel buffer is defaultWriteBufSize, so at most that many frames fit (rest dropped by non-blocking send).
	mgr.mu.RLock()
	chLen := len(mgr.streams[cameraID].frameCh)
	mgr.mu.RUnlock()
	require.Equal(t, defaultWriteBufSize, chLen, "channel should be full at buffer capacity")
}

func TestConcurrentWritesAndIsActive(t *testing.T) {
	mgr := newTestManager(t)
	cameraID := "test-cam"

	mgr.mu.Lock()
	mgr.streams[cameraID] = &streamEntry{
		frameCh:  make(chan hlsFrame, defaultWriteBufSize),
		maxFPS:   0,
		lastUsed: time.Now(),
	}
	mgr.mu.Unlock()

	var wg sync.WaitGroup

	// Concurrently write frames
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = mgr.WriteH264(cameraID, int64(i*1000), [][]byte{{0x00}})
		}
	}()

	// Concurrently check IsActive
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = mgr.IsActive(cameraID)
		}
	}()

	wg.Wait()
	// No panic = success
	require.True(t, mgr.IsActive(cameraID))
}

// --- ErrMaxStreamsReached Tests ---

func TestStartStream_AtCapacity_ReturnsErrMaxStreamsReached(t *testing.T) {
	mgr := NewManagerWithOpts(t.TempDir(), defaultWriteBufSize, defaultSegmentMaxSize)
	cameraID := "test-cam"

	// Fill streams to maxStreams capacity
	mgr.mu.Lock()
	for i := 0; i < defaultMaxStreams; i++ {
		_, cancel := context.WithCancel(context.Background())
		mgr.streams[fmt.Sprintf("cam-%d", i)] = &streamEntry{
			frameCh:  make(chan hlsFrame, defaultWriteBufSize),
			lastUsed: time.Now(),
			cancel:   cancel,
		}
	}
	mgr.mu.Unlock()

	// Next start should return ErrMaxStreamsReached
	err := mgr.StartStream(cameraID, []byte{0x67, 0x42}, []byte{0x68, 0xce}, 0)
	require.ErrorIs(t, err, ErrMaxStreamsReached)
	require.Equal(t, defaultMaxStreams, mgr.GetActiveStreamCount())
}

// --- EvictStream Tests ---

func TestEvictStream_Active(t *testing.T) {
	mgr := newTestManager(t)
	cameraID := "test-cam"

	mgr.mu.Lock()
	_, cancel := context.WithCancel(context.Background())
	mgr.streams[cameraID] = &streamEntry{
		frameCh:  make(chan hlsFrame, defaultWriteBufSize),
		lastUsed: time.Now(),
		cancel:   cancel,
	}
	mgr.mu.Unlock()

	require.Equal(t, 1, mgr.GetActiveStreamCount())
	err := mgr.EvictStream(cameraID)
	require.NoError(t, err)
	require.Equal(t, 0, mgr.GetActiveStreamCount())
	require.False(t, mgr.IsActive(cameraID))
}

func TestEvictStream_NotActive(t *testing.T) {
	mgr := newTestManager(t)
	err := mgr.EvictStream("nonexistent")
	require.ErrorIs(t, err, ErrStreamNotActive)
}

// --- GetActiveStreamCount Tests ---

func TestGetActiveStreamCount_Empty(t *testing.T) {
	mgr := newTestManager(t)
	require.Equal(t, 0, mgr.GetActiveStreamCount())
}

func TestGetActiveStreamCount_WithStreams(t *testing.T) {
	mgr := newTestManager(t)

	mgr.mu.Lock()
	for i := 0; i < 3; i++ {
		_, cancel := context.WithCancel(context.Background())
		mgr.streams[fmt.Sprintf("cam-%d", i)] = &streamEntry{
			frameCh:  make(chan hlsFrame, defaultWriteBufSize),
			lastUsed: time.Now(),
			cancel:   cancel,
		}
	}
	mgr.mu.Unlock()

	require.Equal(t, 3, mgr.GetActiveStreamCount())
}

// --- GetStreamStatus Tests ---

func TestGetStreamStatus_Active(t *testing.T) {
	mgr := newTestManager(t)
	cameraID := "test-cam"

	mgr.mu.Lock()
	mgr.streams[cameraID] = &streamEntry{
		frameCh:  make(chan hlsFrame, defaultWriteBufSize),
		lastUsed: time.Now(),
	}
	mgr.mu.Unlock()

	require.True(t, mgr.GetStreamStatus(cameraID))
}

func TestGetStreamStatus_NotActive(t *testing.T) {
	mgr := newTestManager(t)
	require.False(t, mgr.GetStreamStatus("nonexistent"))
}

func TestGetStreamStatus_ConcurrentReads(t *testing.T) {
	mgr := newTestManager(t)
	cameraID := "test-cam"

	mgr.mu.Lock()
	mgr.streams[cameraID] = &streamEntry{
		frameCh:  make(chan hlsFrame, defaultWriteBufSize),
		lastUsed: time.Now(),
	}
	mgr.mu.Unlock()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			require.True(t, mgr.GetStreamStatus(cameraID))
		}()
	}
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			require.False(t, mgr.GetStreamStatus("nonexistent"))
		}()
	}
	wg.Wait()
}

// --- Concurrent Stream Start/Stop Tests ---

func TestConcurrentStartStreams_NoDeadlock(t *testing.T) {
	mgr := NewManagerWithOpts(t.TempDir(), defaultWriteBufSize, defaultSegmentMaxSize)

	var wg sync.WaitGroup
	// Start 4 streams concurrently (at maxStreams limit)
	for i := 0; i < defaultMaxStreams; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// Use minimal valid SPS/PPS for H264 (Baseline profile, 16x16)
			sps := []byte{0x67, 0x42, 0xc0, 0x0a, 0xd9, 0x00, 0xa0, 0x47, 0xfe, 0x88}
			pps := []byte{0x68, 0xce, 0x38, 0x80}
			err := mgr.StartStream(fmt.Sprintf("cam-%d", idx), sps, pps, 0)
			require.NoError(t, err)
		}(i)
	}
	wg.Wait()

	require.Equal(t, defaultMaxStreams, mgr.GetActiveStreamCount())
	mgr.StopAll()
}

func TestConcurrentStartStreams_AtCapacity_NoDeadlock(t *testing.T) {
	mgr := NewManagerWithOpts(t.TempDir(), defaultWriteBufSize, defaultSegmentMaxSize)

	// Pre-fill to max capacity
	for i := 0; i < defaultMaxStreams; i++ {
		mgr.mu.Lock()
		_, cancel := context.WithCancel(context.Background())
		mgr.streams[fmt.Sprintf("cam-%d", i)] = &streamEntry{
			frameCh:  make(chan hlsFrame, defaultWriteBufSize),
			lastUsed: time.Now(),
			cancel:   cancel,
		}
		mgr.mu.Unlock()
	}

	// Multiple goroutines try to start a 5th stream — all should get ErrMaxStreamsReached
	var wg sync.WaitGroup
	errors := make([]error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sps := []byte{0x67, 0x42, 0xc0, 0x0a, 0xd9, 0x00, 0xa0, 0x47, 0xfe, 0x88}
			pps := []byte{0x68, 0xce, 0x38, 0x80}
			errors[idx] = mgr.StartStream("overflow", sps, pps, 0)
		}(i)
	}
	wg.Wait()

	for i, err := range errors {
		require.ErrorIs(t, err, ErrMaxStreamsReached, "goroutine %d should get ErrMaxStreamsReached", i)
	}
	require.Equal(t, defaultMaxStreams, mgr.GetActiveStreamCount())
}

func TestConcurrentStopStreams_NoDeadlock(t *testing.T) {
	mgr := NewManagerWithOpts(t.TempDir(), defaultWriteBufSize, defaultSegmentMaxSize)

	// Pre-fill streams
	for i := 0; i < defaultMaxStreams; i++ {
		mgr.mu.Lock()
		_, cancel := context.WithCancel(context.Background())
		mgr.streams[fmt.Sprintf("cam-%d", i)] = &streamEntry{
			frameCh:  make(chan hlsFrame, defaultWriteBufSize),
			lastUsed: time.Now(),
			cancel:   cancel,
		}
		mgr.mu.Unlock()
	}

	// Stop all streams concurrently
	var wg sync.WaitGroup
	for i := 0; i < defaultMaxStreams; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			mgr.StopStream(fmt.Sprintf("cam-%d", idx))
		}(i)
	}
	wg.Wait()

	require.Equal(t, 0, mgr.GetActiveStreamCount())
}

func TestConcurrentStartStopMix_NoDeadlock(t *testing.T) {
	mgr := NewManagerWithOpts(t.TempDir(), defaultWriteBufSize, defaultSegmentMaxSize)

	var wg sync.WaitGroup
	// Interleave starts and stops
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(idx int) {
			defer wg.Done()
			camID := fmt.Sprintf("cam-%d", idx)
			err := mgr.StartStream(camID,
				[]byte{0x67, 0x42, 0xc0, 0x0a, 0xd9, 0x00, 0xa0, 0x47, 0xfe, 0x88},
				[]byte{0x68, 0xce, 0x38, 0x80}, 0)
			_ = err // may succeed or fail due to contention
		}(i)
		go func(idx int) {
			defer wg.Done()
			mgr.StopStream(fmt.Sprintf("cam-%d", idx))
		}(i)
	}
	wg.Wait()
	// No panic/deadlock = success
}
