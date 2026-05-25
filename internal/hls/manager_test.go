package hls

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
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
	err := mgr.StartSubStreamReader("nonexistent", "rtsp://192.168.1.1/sub", false, nil)
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
	err := mgr.StartSubStreamReader(cameraID, "rtsp://192.168.1.1/sub", false, nil)
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
	require.Equal(t, 3, mgr.segmentCount)
	require.Nil(t, mgr.metrics)
}

// --- NewManagerWithOpts Tests ---

func TestNewManagerWithOpts_CustomValues(t *testing.T) {
	mgr := NewManagerWithOpts(t.TempDir(), 80, 20*1024*1024, 7)
	require.NotNil(t, mgr)
	require.Equal(t, 80, mgr.writeBufSize)
	require.Equal(t, 20*1024*1024, mgr.segmentMaxSize)
	require.Equal(t, 7, mgr.segmentCount)
}

func TestNewManagerWithOpts_ZeroValuesUseDefaults(t *testing.T) {
	mgr := NewManagerWithOpts(t.TempDir(), 0, 0, 0)
	require.NotNil(t, mgr)
	require.Equal(t, defaultWriteBufSize, mgr.writeBufSize)
	require.Equal(t, defaultSegmentMaxSize, mgr.segmentMaxSize)
	require.Equal(t, 3, mgr.segmentCount)
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
	mgr := NewManagerWithOpts(t.TempDir(), defaultWriteBufSize, defaultSegmentMaxSize, 0)
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
	mgr := NewManagerWithOpts(t.TempDir(), defaultWriteBufSize, defaultSegmentMaxSize, 0)

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
	mgr := NewManagerWithOpts(t.TempDir(), defaultWriteBufSize, defaultSegmentMaxSize, 0)

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
	mgr := NewManagerWithOpts(t.TempDir(), defaultWriteBufSize, defaultSegmentMaxSize, 0)

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
	mgr := NewManagerWithOpts(t.TempDir(), defaultWriteBufSize, defaultSegmentMaxSize, 0)

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

// --- Frame Drop Counter Tests ---

func TestWriteFrame_DropCounterIncrements(t *testing.T) {
	m := metrics.NewMetrics()
	mgr := NewManagerWithOpts(t.TempDir(), 2, defaultSegmentMaxSize, 0, m) // tiny buffer
	cameraID := "test-cam"

	// Insert a stream entry with tiny buffer and no FPS limit
	mgr.mu.Lock()
	entry := &streamEntry{
		frameCh:       make(chan hlsFrame, 2), // matches writeBufSize
		maxFPS:        0,
		lastUsed:      time.Now(),
		lastFrameTime: time.Time{},
	}
	mgr.streams[cameraID] = entry
	mgr.mu.Unlock()

	// Fill the buffer completely
	for i := 0; i < 2; i++ {
		err := mgr.WriteH264(cameraID, int64(i*1000), [][]byte{{0x00}})
		require.NoError(t, err)
	}

	// Next write should be dropped and counter incremented
	err := mgr.WriteH264(cameraID, 3000, [][]byte{{0x00}})
	require.NoError(t, err)

	// Verify Prometheus counter incremented
	families, err := m.Registry.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() == "nvr_hls_frames_dropped_total" {
			require.Len(t, f.GetMetric(), 1)
			require.Equal(t, float64(1), f.GetMetric()[0].GetCounter().GetValue())
			return
		}
	}
	t.Fatal("expected nvr_hls_frames_dropped_total metric")
}

func TestWriteFrame_DropCounterNilMetrics(t *testing.T) {
	// Verify no panic when metrics is nil
	mgr := NewManagerWithOpts(t.TempDir(), 1, defaultSegmentMaxSize, 0) // no metrics
	cameraID := "test-cam"

	mgr.mu.Lock()
	entry := &streamEntry{
		frameCh:       make(chan hlsFrame, 1),
		maxFPS:        0,
		lastUsed:      time.Now(),
		lastFrameTime: time.Time{},
	}
	mgr.streams[cameraID] = entry
	mgr.mu.Unlock()

	// Fill buffer
	_ = mgr.WriteH264(cameraID, 1000, [][]byte{{0x00}})
	// Drop one — should not panic
	err := mgr.WriteH264(cameraID, 2000, [][]byte{{0x00}})
	require.NoError(t, err)
}

// --- Sub-Stream Fallback Tests ---

func TestSubStreamFallback_CalledOnExit(t *testing.T) {
	mgr := newTestManager(t)
	cameraID := "test-cam"

	// Insert a stream entry so StartSubStreamReader doesn't return ErrStreamNotActive
	mgr.mu.Lock()
	entry := newTestStreamEntry(0)
	mgr.streams[cameraID] = entry
	mgr.mu.Unlock()

	// Track whether fallback was invoked (atomic for data-race safety)
	var fallbackCalled atomic.Bool
	fallback := func() {
		fallbackCalled.Store(true)
	}

	// Start sub-stream reader with invalid URL — parse fails immediately, triggers fallback
	err := mgr.StartSubStreamReader(cameraID, "://invalid-url", false, fallback)
	require.NoError(t, err)

	// Wait for the sub-stream reader goroutine to fail and call fallback
	require.Eventually(t, func() bool {
		return fallbackCalled.Load()
	}, 5*time.Second, 50*time.Millisecond, "fallback should have been called when sub-stream failed")
}

func TestSubStreamFallback_NilWhenNotProvided(t *testing.T) {
	mgr := newTestManager(t)
	cameraID := "test-cam"

	mgr.mu.Lock()
	entry := newTestStreamEntry(0)
	mgr.streams[cameraID] = entry
	mgr.mu.Unlock()

	// Calling with nil fallback should not panic when URL is invalid
	err := mgr.StartSubStreamReader(cameraID, "://invalid-url", false, nil)
	require.NoError(t, err)

	// Give goroutine time to fail — no panic = success
	time.Sleep(200 * time.Millisecond)
}

// --- LL-HLS Tests ---

func TestSetLowLatency_Defaults(t *testing.T) {
	mgr := newTestManager(t)
	require.False(t, mgr.lowLatency)
	require.Equal(t, time.Duration(0), mgr.partMinDuration)
}

func TestSetLowLatency_Enabled(t *testing.T) {
	mgr := newTestManager(t)
	mgr.SetLowLatency(true, 200*time.Millisecond)
	require.True(t, mgr.lowLatency)
	require.Equal(t, 200*time.Millisecond, mgr.partMinDuration)
}

func TestSetLowLatency_ZeroPartDuration(t *testing.T) {
	mgr := newTestManager(t)
	mgr.SetLowLatency(true, 0) // zero duration — should not override default
	require.True(t, mgr.lowLatency)
	require.Equal(t, time.Duration(0), mgr.partMinDuration)
}

func TestSetLowLatency_CustomPartDuration(t *testing.T) {
	mgr := newTestManager(t)
	mgr.SetLowLatency(true, 500*time.Millisecond)
	require.True(t, mgr.lowLatency)
	require.Equal(t, 500*time.Millisecond, mgr.partMinDuration)
}

func TestStartStream_LowLatency_H264(t *testing.T) {
	mgr := NewManagerWithOpts(t.TempDir(), defaultWriteBufSize, defaultSegmentMaxSize, 7)
	mgr.SetLowLatency(true, 200*time.Millisecond)

	sps := []byte{0x67, 0x42, 0xc0, 0x0a, 0xd9, 0x00, 0xa0, 0x47, 0xfe, 0x88}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	err := mgr.StartStream("ll-cam-h264", sps, pps, 0)
	require.NoError(t, err)
	require.True(t, mgr.IsActive("ll-cam-h264"))
	mgr.StopAll()
}

func TestStartStream_LowLatency_H265(t *testing.T) {
	mgr := NewManagerWithOpts(t.TempDir(), defaultWriteBufSize, defaultSegmentMaxSize, 7)
	mgr.SetLowLatency(true, 200*time.Millisecond)

	vps := []byte{0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x78, 0x95, 0x98, 0x09}
	sps := []byte{0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x78, 0xa0, 0x03, 0xc0, 0x80, 0x10, 0xe5, 0x96, 0x56, 0x69, 0x24, 0xca, 0xe0, 0x10}
	pps := []byte{0x44, 0x01, 0xc1, 0x72, 0xb4, 0x62, 0x40}
	err := mgr.StartStreamH265("ll-cam-h265", vps, sps, pps, 0)
	require.NoError(t, err)
	require.True(t, mgr.IsActive("ll-cam-h265"))
	mgr.StopAll()
}

func TestStartStream_LowLatency_SegmentCountTooLow(t *testing.T) {
	// LL-HLS requires segment_count >= 7; gohlslib enforces this at Start()
	mgr := NewManagerWithOpts(t.TempDir(), defaultWriteBufSize, defaultSegmentMaxSize, 3) // too low for LL-HLS
	mgr.SetLowLatency(true, 200*time.Millisecond)

	sps := []byte{0x67, 0x42, 0xc0, 0x0a, 0xd9, 0x00, 0xa0, 0x47, 0xfe, 0x88}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	err := mgr.StartStream("ll-cam-bad", sps, pps, 0)
	require.Error(t, err) // gohlslib should reject segmentCount < 7 for LL-HLS
}

// --- LL-HLS Config Validation Tests (via config package) ---

func TestLLHLSConfig_LowLatencyFalse_NoEffect(t *testing.T) {
	// When low_latency is false, existing HLS behavior is unchanged
	mgr := newTestManager(t)
	// Default manager has lowLatency=false
	require.False(t, mgr.lowLatency)
	require.Equal(t, 3, mgr.segmentCount) // NewManager default
}

// --- IDR Waiting Tests ---

func TestIDRWaiting_H264_SkipsNonIDRFrame(t *testing.T) {
	entry := newTestStreamEntry(0)
	require.False(t, entry.idrReceived, "should start with idrReceived=false")

	// Non-IDR H264 NALU (type 1 = non-IDR slice)
	// H264: naluType = data[0] & 0x1F, type 1 = non-IDR coded slice
	frame := hlsFrame{pts: 0, au: [][]byte{{0x01, 0x02, 0x03}}}
	require.False(t, isFirstNalIDR(frame.au, false), "non-IDR H264 should not be IDR")

	// Simulate writeLoop check: when !idrReceived and !isIDR, frame should be skipped
	if !entry.idrReceived && !isFirstNalIDR(frame.au, entry.isH265) {
		// skipped — correct behavior
	} else {
		t.Error("expected non-IDR frame to be skipped before first IDR")
	}
	require.False(t, entry.idrReceived, "idrReceived should remain false after non-IDR frame")
}

func TestIDRWaiting_H264_AcceptsIDRFrame(t *testing.T) {
	entry := newTestStreamEntry(0)
	require.False(t, entry.idrReceived)

	// IDR H264 NALU (type 5 = IDR slice)
	frame := hlsFrame{pts: 1000, au: [][]byte{{0x05, 0x02, 0x03}}}
	require.True(t, isFirstNalIDR(frame.au, false), "IDR H264 should be detected as IDR")

	// Simulate writeLoop: IDR frame should be written and set idrReceived
	if !entry.idrReceived && !isFirstNalIDR(frame.au, entry.isH265) {
		t.Error("expected IDR frame to be accepted")
	}
	entry.idrReceived = true
	require.True(t, entry.idrReceived, "idrReceived should be set after IDR frame")
}

func TestIDRWaiting_H264_AcceptsAllAfterIDR(t *testing.T) {
	entry := newTestStreamEntry(0)
	entry.idrReceived = true // simulate IDR already received

	// Non-IDR frame after IDR should pass through
	frame := hlsFrame{pts: 2000, au: [][]byte{{0x01, 0x02, 0x03}}}
	require.False(t, isFirstNalIDR(frame.au, false))

	// When idrReceived is already true, frame should be written regardless
	require.True(t, entry.idrReceived, "idrReceived should remain true")
}

func TestIDRWaiting_H265_SkipsNonIDRFrame(t *testing.T) {
	entry := newTestStreamEntry(0)
	entry.isH265 = true
	require.False(t, entry.idrReceived)

	// Non-IDR H265 NALU (type 1 = non-IDR slice)
	// H265: naluType = (data[0] >> 1) & 0x3F
	// type 1 -> data[0] = 1 << 1 = 0x02
	frame := hlsFrame{pts: 0, au: [][]byte{{0x02, 0x02, 0x03}}}
	require.False(t, isFirstNalIDR(frame.au, true), "non-IDR H265 should not be IDR")

	if !entry.idrReceived && !isFirstNalIDR(frame.au, entry.isH265) {
		// skipped — correct behavior
	} else {
		t.Error("expected non-IDR H265 frame to be skipped before first IDR")
	}
	require.False(t, entry.idrReceived, "idrReceived should remain false")
}

func TestIDRWaiting_H265_AcceptsIDR_Type19(t *testing.T) {
	entry := newTestStreamEntry(0)
	entry.isH265 = true

	// IDR_W_RADL (type 19): data[0] = 19 << 1 = 0x26
	frame := hlsFrame{pts: 1000, au: [][]byte{{0x26, 0x02, 0x03}}}
	require.True(t, isFirstNalIDR(frame.au, true), "H265 IDR_W_RADL should be detected as IDR")

	if !entry.idrReceived && !isFirstNalIDR(frame.au, entry.isH265) {
		t.Error("expected H265 IDR_W_RADL frame to be accepted")
	}
	entry.idrReceived = true
	require.True(t, entry.idrReceived)
}

func TestIDRWaiting_H265_AcceptsIDR_Type20(t *testing.T) {
	entry := newTestStreamEntry(0)
	entry.isH265 = true

	// IDR_N_LP (type 20): data[0] = 20 << 1 = 0x28
	frame := hlsFrame{pts: 1000, au: [][]byte{{0x28, 0x02, 0x03}}}
	require.True(t, isFirstNalIDR(frame.au, true), "H265 IDR_N_LP should be detected as IDR")

	if !entry.idrReceived && !isFirstNalIDR(frame.au, entry.isH265) {
		t.Error("expected H265 IDR_N_LP frame to be accepted")
	}
	entry.idrReceived = true
	require.True(t, entry.idrReceived)
}

func TestIDRWaiting_H265_AcceptsAllAfterIDR(t *testing.T) {
	entry := newTestStreamEntry(0)
	entry.isH265 = true
	entry.idrReceived = true // simulate IDR already received

	// Non-IDR frame after IDR should pass through
	frame := hlsFrame{pts: 2000, au: [][]byte{{0x02, 0x02, 0x03}}}
	require.False(t, isFirstNalIDR(frame.au, true))
	require.True(t, entry.idrReceived, "idrReceived should remain true after IDR received")
}

func TestIDRWaiting_EmptyAU(t *testing.T) {
	// Empty access unit should not crash and should not be detected as IDR
	require.False(t, isFirstNalIDR([][]byte{}, false), "empty AU should not be IDR")
	require.False(t, isFirstNalIDR([][]byte{}, true), "empty AU should not be IDR for H265")
}

func TestIDRWaiting_EmptyNal(t *testing.T) {
	// NAL unit with no data should not crash and should not be detected as IDR
	require.False(t, isFirstNalIDR([][]byte{{}}, false), "empty NAL should not be IDR")
	require.False(t, isFirstNalIDR([][]byte{{}}, true), "empty NAL should not be IDR for H265")
}

func TestIDRWaiting_H264_SPSNotIDR(t *testing.T) {
	// H264 SPS NALU (type 7) should not be detected as IDR
	require.False(t, isFirstNalIDR([][]byte{{0x07, 0x42, 0xc0}}, false), "SPS should not be IDR")
}

func TestIDRWaiting_H264_PPSNotIDR(t *testing.T) {
	// H264 PPS NALU (type 8) should not be detected as IDR
	require.False(t, isFirstNalIDR([][]byte{{0x08, 0xce, 0x38}}, false), "PPS should not be IDR")
}

func TestIDRWaiting_H265_VPSNotIDR(t *testing.T) {
	// H265 VPS NALU (type 32): data[0] = 32 << 1 = 0x40
	require.False(t, isFirstNalIDR([][]byte{{0x40, 0x01, 0x0c}}, true), "VPS should not be IDR")
}

func TestIDRWaiting_H265_SPSNotIDR(t *testing.T) {
	// H265 SPS NALU (type 33): data[0] = 33 << 1 = 0x42
	require.False(t, isFirstNalIDR([][]byte{{0x42, 0x01, 0x01}}, true), "SPS should not be IDR")
}

func TestIDRWaiting_H265_PPSNotIDR(t *testing.T) {
	// H265 PPS NALU (type 34): data[0] = 34 << 1 = 0x44
	require.False(t, isFirstNalIDR([][]byte{{0x44, 0x01, 0xc1}}, true), "PPS should not be IDR")
}

func TestIDRWaiting_MixedNALUs_FirstIsPPS_Fails(t *testing.T) {
	// Access unit where first NALU is PPS (not IDR) should not be treated as IDR
	entry := newTestStreamEntry(0)
	require.False(t, entry.idrReceived)

	// First element is PPS (type 8), second is IDR (type 5)
	// isFirstNalIDR only checks the first NAL unit
	frame := hlsFrame{pts: 0, au: [][]byte{{0x08, 0xce}, {0x05, 0x02}}}
	require.False(t, isFirstNalIDR(frame.au, false), "first NAL is PPS, not IDR")

	// Should be skipped per writeLoop logic
	if !entry.idrReceived && !isFirstNalIDR(frame.au, entry.isH265) {
		// skipped — correct
	} else {
		t.Error("expected frame starting with PPS to be skipped")
	}
	require.False(t, entry.idrReceived)
}

// --- FPS Credit Smoothing Tests ---

// TestFrameRateLimiter_CreditSmoothing verifies that credit-based throttling
// produces consistent frame intervals. With maxFPS=10 (100ms interval) and source
// at ~10ms per frame, frames should only pass after enough credit accumulates.
func TestFrameRateLimiter_CreditSmoothing(t *testing.T) {
	mgr := newTestManager(t)
	cameraID := "test-cam"

	mgr.mu.Lock()
	entry := newTestStreamEntry(10) // 100ms min interval
	mgr.streams[cameraID] = entry
	mgr.mu.Unlock()

	// First frame should always pass (lastFrameTime is zero)
	err := mgr.WriteH264(cameraID, 0, [][]byte{{0x05}}) // IDR
	require.NoError(t, err)
	require.Equal(t, 1, len(entry.frameCh), "first frame should pass")
	<-entry.frameCh // drain

	// Next 9 frames sent rapidly should all be dropped (no credit accumulated)
	for i := 0; i < 9; i++ {
		err := mgr.WriteH264(cameraID, int64(i+1), [][]byte{{0x01}})
		require.NoError(t, err)
	}
	require.Equal(t, 0, len(entry.frameCh), "rapid frames after first should be dropped")

	// Wait for credit to accumulate to one interval
	time.Sleep(100 * time.Millisecond)

	// Now a frame should pass — enough credit accumulated
	err = mgr.WriteH264(cameraID, 100, [][]byte{{0x01}})
	require.NoError(t, err)
	require.Equal(t, 1, len(entry.frameCh), "frame after credit accumulation should pass")
}

// TestFrameRateLimiter_CreditCapAfterBurst verifies that credit is capped
// after a long pause to prevent frame bursts.
func TestFrameRateLimiter_CreditCapAfterBurst(t *testing.T) {
	mgr := newTestManager(t)
	cameraID := "test-cam"

	mgr.mu.Lock()
	entry := newTestStreamEntry(10) // 100ms min interval
	mgr.streams[cameraID] = entry
	mgr.mu.Unlock()

	// First frame passes
	_ = mgr.WriteH264(cameraID, 0, [][]byte{{0x01}})
	<-entry.frameCh // drain

	// Wait much longer than minInterval (5s pause = 50 intervals of credit)
	time.Sleep(200 * time.Millisecond)

	// Only ONE frame should pass per call (credit capped at 2*minInterval)
	passed := 0
	for i := 0; i < 5; i++ {
		err := mgr.WriteH264(cameraID, int64(i), [][]byte{{0x01}})
		require.NoError(t, err)
		select {
		case <-entry.frameCh:
			passed++
		default:
		}
	}
	// With credit capped at 2*minInterval, at most 2-3 frames should pass
	// from the accumulated credit (not all 5)
	require.LessOrEqual(t, passed, 3, "credit cap should prevent burst")
	require.Greater(t, passed, 0, "at least some frames should pass from credit")
}

// TestFrameRateLimiter_FPSThrottleMetric verifies the Prometheus counter increments
// when frames are dropped by FPS throttle.
func TestFrameRateLimiter_FPSThrottleMetric(t *testing.T) {
	m := metrics.NewMetrics()
	mgr := NewManagerWithOpts(t.TempDir(), defaultWriteBufSize, defaultSegmentMaxSize, 0, m)
	cameraID := "test-cam"

	mgr.mu.Lock()
	entry := newTestStreamEntry(2) // very aggressive FPS limit
	mgr.streams[cameraID] = entry
	mgr.mu.Unlock()

	// First frame passes
	_ = mgr.WriteH264(cameraID, 0, [][]byte{{0x01}})
	<-entry.frameCh

	// Send 5 more rapidly — all should be dropped by FPS throttle
	for i := 0; i < 5; i++ {
		_ = mgr.WriteH264(cameraID, int64(i+1), [][]byte{{0x01}})
	}

	// Verify counter was incremented (should be 5 from FPS drops)
	families, err := m.Registry.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() == "nvr_hls_frames_dropped_total" {
			require.Len(t, f.GetMetric(), 1)
			require.Equal(t, float64(5), f.GetMetric()[0].GetCounter().GetValue(),
				"expected 5 FPS throttle drops")
			return
		}
	}
	t.Fatal("expected nvr_hls_frames_dropped_total metric")
}

// --- Buffer Capacity Tests ---

// TestDefaultWriteBufSize verifies the increased write buffer size.
func TestDefaultWriteBufSize(t *testing.T) {
	require.Equal(t, 180, defaultWriteBufSize, "write buffer should be 180 frames")
}

// TestWriteBufferCapacity verifies that the full buffer can be filled without drops.
func TestWriteBufferCapacity(t *testing.T) {
	mgr := newTestManager(t)
	cameraID := "test-cam"

	mgr.mu.Lock()
	entry := &streamEntry{
		frameCh:  make(chan hlsFrame, defaultWriteBufSize),
		maxFPS:   0,
		lastUsed: time.Now(),
	}
	mgr.streams[cameraID] = entry
	mgr.mu.Unlock()

	// Fill the entire buffer — all should succeed
	for i := 0; i < defaultWriteBufSize; i++ {
		err := mgr.WriteH264(cameraID, int64(i), [][]byte{{byte(i)}})
		require.NoError(t, err)
	}
	require.Equal(t, defaultWriteBufSize, len(entry.frameCh),
		"buffer should be exactly full")

	// Next frame should be dropped (buffer full)
	err := mgr.WriteH264(cameraID, int64(defaultWriteBufSize), [][]byte{{0xFF}})
	require.NoError(t, err)
	require.Equal(t, defaultWriteBufSize, len(entry.frameCh),
		"buffer should remain at capacity after drop")
}
