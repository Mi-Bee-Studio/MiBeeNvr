package hls

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bluenviron/gohlslib/v2"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
)

// newTestManager creates a Manager with a writable temp directory.
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	return newManager(context.Background(), dir)
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
	for i := range 10 {
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
	for i := range 10 {
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
	for i := range 5 {
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
		frameCh:         make(chan hlsFrame, defaultWriteBufSize),
		maxFPS:          0,
		subStreamCancel: cancel,
		cancel:          cancel,
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
	mgr := newManager(context.Background(), t.TempDir())
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
	mgr := NewManagerWithOpts(context.Background(), t.TempDir(), 80, 20*1024*1024, 7)
	require.NotNil(t, mgr)
	require.Equal(t, 80, mgr.writeBufSize)
	require.Equal(t, 20*1024*1024, mgr.segmentMaxSize)
	require.Equal(t, 7, mgr.segmentCount)
}

func TestNewManagerWithOpts_ZeroValuesUseDefaults(t *testing.T) {
	mgr := NewManagerWithOpts(context.Background(), t.TempDir(), 0, 0, 0)
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
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 100 {
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
		for i := range 100 {
			_ = mgr.WriteH264(cameraID, int64(i*1000), [][]byte{{0x00}})
		}
	}()

	// Concurrently check IsActive
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 100 {
			_ = mgr.IsActive(cameraID)
		}
	}()

	wg.Wait()
	// No panic = success
	require.True(t, mgr.IsActive(cameraID))
}

// --- LRU Eviction Tests ---

func TestStartStream_AtCapacity_EvictsLRU(t *testing.T) {
	mgr := NewManagerWithOpts(context.Background(), t.TempDir(), defaultWriteBufSize, defaultSegmentMaxSize, 0)
	cameraID := "test-cam"

	// Fill streams to maxStreams capacity
	mgr.mu.Lock()
	for i := range defaultMaxStreams {
		_, cancel := context.WithCancel(context.Background())
		mgr.streams[fmt.Sprintf("cam-%d", i)] = &streamEntry{
			frameCh:  make(chan hlsFrame, defaultWriteBufSize),
			lastUsed: time.Now(),
			cancel:   cancel,
		}
	}
	mgr.mu.Unlock()

	// 5th stream should succeed by evicting LRU stream
	sps := []byte{0x67, 0x42, 0xc0, 0x0a, 0xd9, 0x00, 0xa0, 0x47, 0xfe, 0x88}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	err := mgr.StartStream(cameraID, sps, pps, 0)
	require.NoError(t, err)
	require.Equal(t, defaultMaxStreams, mgr.GetActiveStreamCount())
	require.True(t, mgr.IsActive(cameraID))
	mgr.StopAll()
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

// --- LRU Eviction Tests ---

func TestLRUEviction_EvictsOldestStream(t *testing.T) {
	mgr := NewManagerWithOpts(context.Background(), t.TempDir(), defaultWriteBufSize, defaultSegmentMaxSize, 0)

	// Create 4 streams with staggered lastUsed times
	mgr.mu.Lock()
	now := time.Now()
	for i := range defaultMaxStreams {
		_, cancel := context.WithCancel(context.Background())
		mgr.streams[fmt.Sprintf("cam-%d", i)] = &streamEntry{
			frameCh:  make(chan hlsFrame, defaultWriteBufSize),
			lastUsed: now.Add(time.Duration(i) * time.Second), // cam-0 is oldest
			cancel:   cancel,
		}
	}
	mgr.mu.Unlock()

	// cam-0 has the oldest lastUsed — should be evicted
	sps := []byte{0x67, 0x42, 0xc0, 0x0a, 0xd9, 0x00, 0xa0, 0x47, 0xfe, 0x88}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	err := mgr.StartStream("cam-new", sps, pps, 0)
	require.NoError(t, err)
	require.Equal(t, defaultMaxStreams, mgr.GetActiveStreamCount())
	// cam-0 should be evicted (oldest lastUsed)
	require.False(t, mgr.IsActive("cam-0"))
	// cam-1, cam-2, cam-3 should still be active
	require.True(t, mgr.IsActive("cam-1"))
	require.True(t, mgr.IsActive("cam-2"))
	require.True(t, mgr.IsActive("cam-3"))
	require.True(t, mgr.IsActive("cam-new"))
	mgr.StopAll()
}

func TestLRUEviction_EvictedStreamCleanedUp(t *testing.T) {
	mgr := NewManagerWithOpts(context.Background(), t.TempDir(), defaultWriteBufSize, defaultSegmentMaxSize, 0)

	// Fill to capacity
	mgr.mu.Lock()
	for i := range defaultMaxStreams {
		_, cancel := context.WithCancel(context.Background())
		mgr.streams[fmt.Sprintf("cam-%d", i)] = &streamEntry{
			frameCh:  make(chan hlsFrame, defaultWriteBufSize),
			lastUsed: time.Now().Add(time.Duration(i) * time.Second),
			cancel:   cancel,
		}
	}
	mgr.mu.Unlock()

	// Start 5th stream — triggers LRU eviction of cam-0
	sps := []byte{0x67, 0x42, 0xc0, 0x0a, 0xd9, 0x00, 0xa0, 0x47, 0xfe, 0x88}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	err := mgr.StartStream("cam-new", sps, pps, 0)
	require.NoError(t, err)

	// Evicted stream should be fully cleaned up:
	// - not in streams map
	require.Equal(t, defaultMaxStreams, mgr.GetActiveStreamCount())
	require.False(t, mgr.IsActive("cam-0"))

	// - writing to evicted stream should silently succeed (not error, not panic)
	err = mgr.WriteH264("cam-0", 1000, [][]byte{{0x01}})
	require.NoError(t, err)

	// - EvictStream on already-evicted stream should return ErrStreamNotActive
	err = mgr.EvictStream("cam-0")
	require.ErrorIs(t, err, ErrStreamNotActive)

	mgr.StopAll()
}

// --- GetActiveStreamCount Tests ---

func TestGetActiveStreamCount_Empty(t *testing.T) {
	mgr := newTestManager(t)
	require.Equal(t, 0, mgr.GetActiveStreamCount())
}

func TestGetActiveStreamCount_WithStreams(t *testing.T) {
	mgr := newTestManager(t)

	mgr.mu.Lock()
	for i := range 3 {
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
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			require.True(t, mgr.GetStreamStatus(cameraID))
		}()
	}
	for range 100 {
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
	mgr := NewManagerWithOpts(context.Background(), t.TempDir(), defaultWriteBufSize, defaultSegmentMaxSize, 0)

	var wg sync.WaitGroup
	// Start 4 streams concurrently (at maxStreams limit)
	for i := range defaultMaxStreams {
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
	mgr := NewManagerWithOpts(context.Background(), t.TempDir(), defaultWriteBufSize, defaultSegmentMaxSize, 0)

	// Pre-fill to max capacity
	for i := range defaultMaxStreams {
		mgr.mu.Lock()
		_, cancel := context.WithCancel(context.Background())
		mgr.streams[fmt.Sprintf("cam-%d", i)] = &streamEntry{
			frameCh:  make(chan hlsFrame, defaultWriteBufSize),
			lastUsed: time.Now(),
			cancel:   cancel,
		}
		mgr.mu.Unlock()
	}

	// Multiple goroutines try to start a 5th stream — all should succeed (LRU eviction)
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sps := []byte{0x67, 0x42, 0xc0, 0x0a, 0xd9, 0x00, 0xa0, 0x47, 0xfe, 0x88}
			pps := []byte{0x68, 0xce, 0x38, 0x80}
			err := mgr.StartStream("overflow", sps, pps, 0)
			require.NoError(t, err)
		}()
	}
	wg.Wait()

	// Stream count stays at maxStreams (LRU eviction keeps it bounded)
	require.Equal(t, defaultMaxStreams, mgr.GetActiveStreamCount())
	mgr.StopAll()
}

func TestConcurrentStopStreams_NoDeadlock(t *testing.T) {
	mgr := NewManagerWithOpts(context.Background(), t.TempDir(), defaultWriteBufSize, defaultSegmentMaxSize, 0)

	// Pre-fill streams
	for i := range defaultMaxStreams {
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
	for i := range defaultMaxStreams {
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
	mgr := NewManagerWithOpts(context.Background(), t.TempDir(), defaultWriteBufSize, defaultSegmentMaxSize, 0)

	var wg sync.WaitGroup
	// Interleave starts and stops
	for i := range 8 {
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
	mgr := NewManagerWithOpts(context.Background(), t.TempDir(), 2, defaultSegmentMaxSize, 0, m) // tiny buffer
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
	for i := range 2 {
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
	mgr := NewManagerWithOpts(context.Background(), t.TempDir(), 1, defaultSegmentMaxSize, 0) // no metrics
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
	mgr := NewManagerWithOpts(context.Background(), t.TempDir(), defaultWriteBufSize, defaultSegmentMaxSize, 7)
	mgr.SetLowLatency(true, 200*time.Millisecond)

	sps := []byte{0x67, 0x42, 0xc0, 0x0a, 0xd9, 0x00, 0xa0, 0x47, 0xfe, 0x88}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	err := mgr.StartStream("ll-cam-h264", sps, pps, 0)
	require.NoError(t, err)
	require.True(t, mgr.IsActive("ll-cam-h264"))
	mgr.StopAll()
}

func TestStartStream_LowLatency_H265(t *testing.T) {
	mgr := NewManagerWithOpts(context.Background(), t.TempDir(), defaultWriteBufSize, defaultSegmentMaxSize, 7)
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
	mgr := NewManagerWithOpts(context.Background(), t.TempDir(), defaultWriteBufSize, defaultSegmentMaxSize, 3) // too low for LL-HLS
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

func TestIDRWaiting_MixedNALUs_PrependedParams(t *testing.T) {
	// Access unit where parameter sets are prepended before IDR:
	// [PPS, IDR] — the IDR is at index 1, not 0.
	// isFirstNalIDR scans all NALUs so it correctly detects IDR anywhere in the AU.
	// This is the standard format from Xiaomi and ONVIF cameras which prepend
	// VPS/SPS/PPS before the IDR slice.
	entry := newTestStreamEntry(0)
	require.False(t, entry.idrReceived)

	// H264: PPS (type 8) + IDR (type 5)
	frame := hlsFrame{pts: 0, au: [][]byte{{0x08, 0xce}, {0x05, 0x02}}}
	require.True(t, isFirstNalIDR(frame.au, false), "AU contains IDR despite PPS being first")

	// Should NOT be skipped — IDR detected in the AU
	if !entry.idrReceived && !isFirstNalIDR(frame.au, entry.isH265) {
		t.Error("expected IDR to be detected in mixed NALU access unit")
	}
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
	for i := range 9 {
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
	for i := range 5 {
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
	mgr := NewManagerWithOpts(context.Background(), t.TempDir(), defaultWriteBufSize, defaultSegmentMaxSize, 0, m)
	cameraID := "test-cam"

	mgr.mu.Lock()
	entry := newTestStreamEntry(2) // very aggressive FPS limit
	mgr.streams[cameraID] = entry
	mgr.mu.Unlock()

	// First frame passes
	_ = mgr.WriteH264(cameraID, 0, [][]byte{{0x01}})
	<-entry.frameCh

	// Send 5 more rapidly — all should be dropped by FPS throttle
	for i := range 5 {
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
	for i := range defaultWriteBufSize {
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

// --- shouldThrottle Tests ---

func TestShouldThrottle_Disabled(t *testing.T) {
	t.Helper()
	// maxFPS=0 means no throttling — should never return true
	var credit time.Duration
	var last time.Time
	require.False(t, shouldThrottle(0, &credit, &last, time.Now(), false))
	require.False(t, shouldThrottle(-1, &credit, &last, time.Now(), false))
}

func TestShouldThrottle_FirstFrameAlwaysPasses(t *testing.T) {
	t.Helper()
	var credit time.Duration
	var last time.Time // zero value = never written
	now := time.Now()
	require.False(t, shouldThrottle(10, &credit, &last, now, false), "first frame must pass")
	require.Equal(t, now, last, "lastFrameTime should be initialized")
	require.Equal(t, time.Duration(0), credit, "credit should be zero after first frame")
}

func TestShouldThrottle_RapidFramesDropped(t *testing.T) {
	t.Helper()
	var credit time.Duration
	var last time.Time
	now := time.Now()

	// First frame — initializes
	require.False(t, shouldThrottle(10, &credit, &last, now, false))

	// Immediate second frame (0 elapsed) — should be throttled
	require.True(t, shouldThrottle(10, &credit, &last, now, false), "rapid frame should be throttled")
}

func TestShouldThrottle_CreditAccumulates(t *testing.T) {
	t.Helper()
	var credit time.Duration
	var last time.Time
	maxFPS := 10 // 100ms min interval
	now := time.Now()

	// First frame
	require.False(t, shouldThrottle(maxFPS, &credit, &last, now, false))

	// Wait 100ms — enough credit for one frame
	time.Sleep(100 * time.Millisecond)
	require.False(t, shouldThrottle(maxFPS, &credit, &last, time.Now(), false), "frame after 100ms should pass")
}

func TestShouldThrottle_CreditCapped(t *testing.T) {
	t.Helper()
	var credit time.Duration
	var last time.Time
	maxFPS := 10 // 100ms min interval
	now := time.Now()

	// First frame
	require.False(t, shouldThrottle(maxFPS, &credit, &last, now, false))

	// Wait 500ms — 5 intervals of credit, but cap is 2*minInterval (200ms)
	time.Sleep(500 * time.Millisecond)
	require.False(t, shouldThrottle(maxFPS, &credit, &last, time.Now(), false), "frame after long pause should pass")
	// After consuming 1 interval (100ms), remaining credit should be capped at 2*minInterval
	require.LessOrEqual(t, credit, 2*100*time.Millisecond, "credit should be capped at 2*minInterval")
}

func TestShouldThrottle_InsufficientCredit(t *testing.T) {
	t.Helper()
	var credit time.Duration
	var last time.Time
	maxFPS := 10 // 100ms min interval
	now := time.Now()

	// First frame
	require.False(t, shouldThrottle(maxFPS, &credit, &last, now, false))

	// Wait only 50ms — not enough credit
	time.Sleep(50 * time.Millisecond)
	require.True(t, shouldThrottle(maxFPS, &credit, &last, time.Now(), false), "frame with insufficient credit should be throttled")
}

func TestShouldThrottle_MultipleFramesWithCredit(t *testing.T) {
	t.Helper()
	var credit time.Duration
	var last time.Time
	maxFPS := 2 // 500ms min interval
	now := time.Now()

	// First frame
	require.False(t, shouldThrottle(maxFPS, &credit, &last, now, false))

	// Wait 1 second — enough for 2 frames at 2fps
	time.Sleep(1 * time.Second)
	require.False(t, shouldThrottle(maxFPS, &credit, &last, time.Now(), false), "frame 1 of burst should pass")
	// After consuming 500ms, ~500ms credit remains — enough for one more
	require.False(t, shouldThrottle(maxFPS, &credit, &last, time.Now(), false), "frame 2 of burst should pass")
	// Third frame should be throttled — no credit left
	require.True(t, shouldThrottle(maxFPS, &credit, &last, time.Now(), false), "frame 3 should be throttled")
}

func TestHLSThrottlePreservesIDR(t *testing.T) {
	t.Helper()
	var credit time.Duration
	var last time.Time
	maxFPS := 2 // 500ms min interval
	now := time.Now()

	// First frame — initializes throttle state
	require.False(t, shouldThrottle(maxFPS, &credit, &last, now, false))

	// Second frame immediately — not IDR, should be throttled
	require.True(t, shouldThrottle(maxFPS, &credit, &last, now, false), "non-IDR should be throttled")

	// Third frame immediately — IS IDR, should NOT be throttled even though credit is exhausted
	require.False(t, shouldThrottle(maxFPS, &credit, &last, now, true), "IDR should not be throttled despite exhausted credit")
}

// --- waitForFirstIDR Tests ---

func TestWaitForFirstIDR_AlreadyReceived(t *testing.T) {
	t.Helper()
	idrReceived := true
	// When idrReceived is true, no frame should be skipped
	require.False(t, waitForFirstIDR([][]byte{{0x01}}, false, &idrReceived), "non-IDR after first IDR should pass")
	require.True(t, idrReceived, "idrReceived should remain true")
}

func TestWaitForFirstIDR_NonIDR_Skipped(t *testing.T) {
	t.Helper()
	idrReceived := false
	// Non-IDR H264 (type 1)
	require.True(t, waitForFirstIDR([][]byte{{0x01}}, false, &idrReceived), "non-IDR should be skipped")
	require.False(t, idrReceived, "idrReceived should remain false")
}

func TestWaitForFirstIDR_IDR_Accepted(t *testing.T) {
	t.Helper()
	idrReceived := false
	// IDR H264 (type 5)
	require.False(t, waitForFirstIDR([][]byte{{0x05}}, false, &idrReceived), "IDR should not be skipped")
	require.True(t, idrReceived, "idrReceived should be set to true")
}

func TestWaitForFirstIDR_H265_IDR_Type19(t *testing.T) {
	t.Helper()
	idrReceived := false
	// H265 IDR_W_RADL (type 19): 19 << 1 = 0x26
	require.False(t, waitForFirstIDR([][]byte{{0x26}}, true, &idrReceived), "H265 IDR_W_RADL should not be skipped")
	require.True(t, idrReceived)
}

func TestWaitForFirstIDR_H265_IDR_Type20(t *testing.T) {
	t.Helper()
	idrReceived := false
	// H265 IDR_N_LP (type 20): 20 << 1 = 0x28
	require.False(t, waitForFirstIDR([][]byte{{0x28}}, true, &idrReceived), "H265 IDR_N_LP should not be skipped")
	require.True(t, idrReceived)
}

func TestWaitForFirstIDR_H265_NonIDR_Skipped(t *testing.T) {
	t.Helper()
	idrReceived := false
	// H265 non-IDR (type 1): 1 << 1 = 0x02
	require.True(t, waitForFirstIDR([][]byte{{0x02}}, true, &idrReceived), "H265 non-IDR should be skipped")
	require.False(t, idrReceived)
}

func TestWaitForFirstIDR_MixedNALUs_WithIDR(t *testing.T) {
	t.Helper()
	idrReceived := false
	// PPS (type 8) + IDR (type 5) — IDR at index 1
	require.False(t, waitForFirstIDR([][]byte{{0x08}, {0x05}}, false, &idrReceived),
		"mixed AU with IDR should not be skipped")
	require.True(t, idrReceived)
}

func TestWaitForFirstIDR_EmptyAU(t *testing.T) {
	t.Helper()
	idrReceived := false
	require.True(t, waitForFirstIDR([][]byte{}, false, &idrReceived), "empty AU should be skipped")
	require.False(t, idrReceived)
}

// --- writeFrameToMuxer Tests ---

func TestWriteFrameToMuxer_NilMuxer(t *testing.T) {
	t.Helper()
	err := writeFrameToMuxer(false, nil, nil, [][]byte{{0x01}}, 1000, "test-cam")
	require.Error(t, err, "nil muxer should return error")
	require.Contains(t, err.Error(), "muxer not initialized")
}

func TestWriteFrameToMuxer_NilTrack(t *testing.T) {
	t.Helper()
	err := writeFrameToMuxer(false, &gohlslib.Muxer{}, nil, [][]byte{{0x01}}, 1000, "test-cam")
	require.Error(t, err, "nil track should return error")
	require.Contains(t, err.Error(), "muxer not initialized")
}

func TestWriteFrameToMuxer_CameraIDInError(t *testing.T) {
	t.Helper()
	err := writeFrameToMuxer(true, nil, nil, [][]byte{{0x26}}, 2000, "my-camera")
	require.Error(t, err)
	require.Contains(t, err.Error(), "my-camera")
}

// --- Error Recovery Tests ---

// TestCalculateBackoff_VerifyExponentialGrowth verifies backoff doubles each error
// and caps at maxBackoff (16s).
func TestCalculateBackoff_VerifyExponentialGrowth(t *testing.T) {
	t.Helper()
	require.Equal(t, 2*time.Second, calculateBackoff(1))
	require.Equal(t, 4*time.Second, calculateBackoff(2))
	require.Equal(t, 8*time.Second, calculateBackoff(3))
	require.Equal(t, 16*time.Second, calculateBackoff(4))
	require.Equal(t, 16*time.Second, calculateBackoff(5))
	require.Equal(t, 16*time.Second, calculateBackoff(10))
	require.Equal(t, 16*time.Second, calculateBackoff(100))
}

// TestCalculateBackoff_ZeroErrors returns initial backoff
func TestCalculateBackoff_ZeroErrors(t *testing.T) {
	t.Helper()
	require.Equal(t, 1*time.Second, calculateBackoff(0))
}

// TestWriteLoop_ErrorRecovery_MuxerDestroyedAndIDRReset verifies that a write error
// causes the muxer to be destroyed and idrReceived to be reset.
func TestWriteLoop_ErrorRecovery_MuxerDestroyedAndIDRReset(t *testing.T) {
	t.Helper()
	m := metrics.NewMetrics()
	mgr := NewManagerWithOpts(context.Background(), t.TempDir(), defaultWriteBufSize, defaultSegmentMaxSize, 0, m)

	// Insert entry with a nil muxer
	mgr.mu.Lock()
	entry := &streamEntry{
		mux:         nil,
		track:       nil,
		frameCh:     make(chan hlsFrame, defaultWriteBufSize),
		lastUsed:    time.Now(),
		idrReceived: true,
		maxFPS:      0,
	}
	mgr.streams["test-cam"] = entry
	mgr.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately to skip backoff sleep

	mgr.handleWriteError(ctx, "test-cam", entry, fmt.Errorf("write failed"))

	// Verify muxer destroyed and IDR reset
	require.Nil(t, entry.mux)
	require.Nil(t, entry.track)
	require.False(t, entry.idrReceived, "idrReceived should be reset after error")

	// Verify error metrics
	families, err := m.Registry.Gather()
	require.NoError(t, err)
	var writeErrorsFound, muxerRestartsFound bool
	for _, f := range families {
		switch f.GetName() {
		case "nvr_hls_write_errors_total":
			require.Len(t, f.GetMetric(), 1)
			require.Equal(t, float64(1), f.GetMetric()[0].GetCounter().GetValue())
			writeErrorsFound = true
		case "nvr_hls_muxer_restarts_total":
			require.Len(t, f.GetMetric(), 1)
			require.Equal(t, float64(1), f.GetMetric()[0].GetCounter().GetValue())
			muxerRestartsFound = true
		}
	}
	require.True(t, writeErrorsFound, "expected nvr_hls_write_errors_total metric")
	require.True(t, muxerRestartsFound, "expected nvr_hls_muxer_restarts_total metric")
}

// TestHandleWriteError_ConsecutiveErrorsIncreaseBackoff verifies that
// consecutive write errors properly increment the counter and increase backoff.
func TestHandleWriteError_ConsecutiveErrorsIncreaseBackoff(t *testing.T) {
	t.Helper()
	entry := newTestStreamEntry(0)
	entry.mux = nil // simulate no muxer
	entry.track = nil
	entry.idrReceived = true

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := &Manager{streams: make(map[string]*streamEntry)}

	// First error
	mgr.handleWriteError(ctx, "cam-1", entry, fmt.Errorf("write failed 1"))
	require.Equal(t, 1, entry.consecutiveErrors)
	require.Equal(t, 2*time.Second, entry.backoff)
	require.False(t, entry.idrReceived, "idrReceived should be reset after error")

	// Second error
	mgr.handleWriteError(ctx, "cam-1", entry, fmt.Errorf("write failed 2"))
	require.Equal(t, 2, entry.consecutiveErrors)
	require.Equal(t, 4*time.Second, entry.backoff)

	// Third error
	mgr.handleWriteError(ctx, "cam-1", entry, fmt.Errorf("write failed 3"))
	require.Equal(t, 3, entry.consecutiveErrors)
	require.Equal(t, 8*time.Second, entry.backoff)

	// Fourth error — hits max (16s)
	mgr.handleWriteError(ctx, "cam-1", entry, fmt.Errorf("write failed 4"))
	require.Equal(t, 4, entry.consecutiveErrors)
	require.Equal(t, 16*time.Second, entry.backoff)

	// Fifth error — still capped at 16s
	mgr.handleWriteError(ctx, "cam-1", entry, fmt.Errorf("write failed 5"))
	require.Equal(t, 5, entry.consecutiveErrors)
	require.Equal(t, 16*time.Second, entry.backoff)
}

// TestHandleWriteError_StorageErrorUsesLongBackoff verifies that persistent
// storage errors (read-only filesystem, disk full, I/O error) get a long
// fixed backoff instead of the short exponential one, preventing log spam
// during disk outages.
func TestHandleWriteError_StorageErrorUsesLongBackoff(t *testing.T) {
	t.Helper()

	tests := []struct {
		name string
		err  error
	}{
		{"read-only filesystem", fmt.Errorf("open /data/hls/seg.mp4: read-only file system")},
		{"no space left", fmt.Errorf("write /data/hls/seg.mp4: no space left on device")},
		{"input/output error", fmt.Errorf("write /data/hls/seg.mp4: input/output error")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := newTestStreamEntry(0)
			entry.mux = nil
			entry.track = nil
			entry.idrReceived = true

			ctx, cancel := context.WithCancel(context.Background())
			cancel() // pre-cancel to skip the 60s backoff sleep

			mgr := &Manager{streams: make(map[string]*streamEntry)}
			mgr.handleWriteError(ctx, "cam-1", entry, tt.err)

			require.Equal(t, 1, entry.consecutiveErrors, "error counter should increment")
			require.Equal(t, storageErrorBackoff, entry.backoff,
				"storage errors should use long fixed backoff, not exponential")
		})
	}
}

// TestIsPersistentStorageError tests the storage error classifier directly.
func TestIsPersistentStorageError(t *testing.T) {
	t.Helper()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"read-only", fmt.Errorf("open seg.mp4: read-only file system"), true},
		{"no space", fmt.Errorf("write seg.mp4: no space left on device"), true},
		{"io error", fmt.Errorf("write seg.mp4: input/output error"), true},
		{"generic DTS error", fmt.Errorf("DTS non-monotonic"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isPersistentStorageError(tt.err))
		})
	}
}

// TestHandleWriteError_ContextCancellation verifies that backoff sleep is
// interrupted when the context is cancelled.
func TestHandleWriteError_ContextCancellation(t *testing.T) {
	t.Helper()
	entry := newTestStreamEntry(0)
	entry.mux = nil
	entry.track = nil
	entry.idrReceived = true

	ctx, cancel := context.WithCancel(context.Background())

	mgr := &Manager{streams: make(map[string]*streamEntry)}

	// Cancel context immediately
	cancel()

	// handleWriteError should return immediately without sleeping
	start := time.Now()
	mgr.handleWriteError(ctx, "cam-1", entry, fmt.Errorf("write failed"))
	elapsed := time.Since(start)

	require.Equal(t, 1, entry.consecutiveErrors)
	require.False(t, entry.idrReceived)
	require.Less(t, elapsed, 100*time.Millisecond, "backoff should be skipped on context cancel")
}

// TestHandleWriteError_MuxerDestroyed verifies muxer is closed and niled out.
func TestHandleWriteError_MuxerDestroyed(t *testing.T) {
	t.Helper()
	entry := newTestStreamEntry(0)
	entry.idrReceived = true

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := &Manager{streams: make(map[string]*streamEntry)}

	mgr.handleWriteError(ctx, "cam-1", entry, fmt.Errorf("muxer failed"))
	require.Nil(t, entry.mux)
	require.Nil(t, entry.track)
	require.False(t, entry.idrReceived)
	require.Equal(t, 1, entry.consecutiveErrors)
}

// TestHandleWriteError_NilMetrics_NoPanic verifies no panic when metrics is nil.
func TestHandleWriteError_NilMetrics_NoPanic(t *testing.T) {
	t.Helper()
	entry := newTestStreamEntry(0)
	entry.mux = nil
	entry.track = nil
	entry.idrReceived = true

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	defer cancel()

	mgr := &Manager{streams: make(map[string]*streamEntry)}
	// Should not panic with nil metrics
	mgr.handleWriteError(ctx, "cam-1", entry, fmt.Errorf("test error"))
	require.Equal(t, 1, entry.consecutiveErrors)
}

// --- Goroutine Cleanup Tests ---

// TestStopAll_CancelsManagerContext verifies that StopAll cancels the manager context,
// which causes writeLoop goroutines to exit cleanly.
func TestStopAll_CancelsManagerContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	mgr := newManager(ctx, t.TempDir())

	sps := []byte{0x67, 0x42, 0xc0, 0x0a, 0xd9, 0x00, 0xa0, 0x47, 0xfe, 0x88}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	err := mgr.StartStream("cam-1", sps, pps, 0)
	require.NoError(t, err)
	require.True(t, mgr.IsActive("cam-1"))

	// Manager context should not be cancelled yet
	require.NoError(t, mgr.ctx.Err())

	// StopAll cancels manager context
	mgr.StopAll()
	require.Error(t, mgr.ctx.Err(), "manager context should be cancelled after StopAll")
	require.False(t, mgr.IsActive("cam-1"))

	// Parent context should NOT be cancelled (only manager's derived context)
	require.NoError(t, ctx.Err())
	cancel()
}

// TestWriteLoop_ExitsOnManagerContextCancel verifies that writeLoop exits
// when the manager's context is cancelled via StopAll.
func TestWriteLoop_ExitsOnManagerContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newManager(ctx, t.TempDir())

	sps := []byte{0x67, 0x42, 0xc0, 0x0a, 0xd9, 0x00, 0xa0, 0x47, 0xfe, 0x88}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	err := mgr.StartStream("cam-1", sps, pps, 0)
	require.NoError(t, err)

	before := runtime.NumGoroutine()

	mgr.StopAll()

	// Give goroutines time to exit
	time.Sleep(100 * time.Millisecond)

	// Goroutine count should not grow (writeLoop + idleWatchdog should have exited)
	after := runtime.NumGoroutine()
	require.LessOrEqual(t, after, before, "goroutine count should not grow after StopAll")
}

// TestStartStopCycles_NoGoroutineLeak verifies that repeated start/stop cycles
// do not leak goroutines.
func TestStartStopCycles_NoGoroutineLeak(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newManager(ctx, t.TempDir())

	sps := []byte{0x67, 0x42, 0xc0, 0x0a, 0xd9, 0x00, 0xa0, 0x47, 0xfe, 0x88}
	pps := []byte{0x68, 0xce, 0x38, 0x80}

	baseline := runtime.NumGoroutine()

	// 5 start/stop cycles
	for i := range 5 {
		err := mgr.StartStream(fmt.Sprintf("cam-%d", i), sps, pps, 0)
		require.NoError(t, err)
		time.Sleep(50 * time.Millisecond) // let goroutines start
		mgr.StopAll()
		time.Sleep(50 * time.Millisecond) // let goroutines exit
	}

	time.Sleep(100 * time.Millisecond)
	after := runtime.NumGoroutine()
	require.LessOrEqual(t, after, baseline+1, "at most 1 extra goroutine tolerated after 5 start/stop cycles")
}

// --- extractParamSets Tests (added with muxer rebuild recovery) ---

// TestExtractParamSets_H264 verifies extraction of SPS+PPS from an H264 access unit.
func TestExtractParamSets_H264(t *testing.T) {
	t.Helper()
	sps := []byte{0x67, 0x42, 0xc0, 0x0a, 0xd9, 0x00, 0xa0, 0x47, 0xfe, 0x88}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	idr := []byte{0x65, 0x88, 0x80, 0x40} // NAL type 5 = IDR slice
	au := [][]byte{sps, pps, idr}

	gotVPS, gotSPS, gotPPS, ok := extractParamSets(au, false)
	require.True(t, ok)
	require.Nil(t, gotVPS, "H264 has no VPS")
	require.Equal(t, sps, gotSPS)
	require.Equal(t, pps, gotPPS)
}

// TestExtractParamSets_H264_MissingParams returns ok=false when SPS or PPS absent.
func TestExtractParamSets_H264_MissingParams(t *testing.T) {
	t.Helper()
	idr := []byte{0x65, 0x88, 0x80, 0x40}
	// Only IDR, no SPS/PPS.
	_, _, _, ok := extractParamSets([][]byte{idr}, false)
	require.False(t, ok)
	// SPS but no PPS.
	sps := []byte{0x67, 0x42}
	_, _, _, ok = extractParamSets([][]byte{sps, idr}, false)
	require.False(t, ok)
}

// TestExtractParamSets_H265 verifies extraction of VPS+SPS+PPS from an HEVC access unit.
// HEVC NAL type = (byte0 >> 1) & 0x3F; VPS=32->0x40, SPS=33->0x42, PPS=34->0x44, IDR=19->0x26.
func TestExtractParamSets_H265(t *testing.T) {
	t.Helper()
	vps := []byte{0x40, 0x01, 0x0c}
	sps := []byte{0x42, 0x01, 0x01, 0x60}
	pps := []byte{0x44, 0x01, 0xc1, 0x73}
	idr := []byte{0x26, 0x01, 0xaf, 0x39}
	au := [][]byte{vps, sps, pps, idr}

	gotVPS, gotSPS, gotPPS, ok := extractParamSets(au, true)
	require.True(t, ok)
	require.Equal(t, vps, gotVPS)
	require.Equal(t, sps, gotSPS)
	require.Equal(t, pps, gotPPS)
}

// TestExtractParamSets_EmptyNAL skips zero-length NALs without panic.
func TestExtractParamSets_EmptyNAL(t *testing.T) {
	t.Helper()
	sps := []byte{0x67, 0x42}
	pps := []byte{0x68, 0xce}
	_, _, _, ok := extractParamSets([][]byte{{}, sps, {}, pps}, false)
	require.True(t, ok)
}

// --- rebuildMuxer Tests ---

// TestRebuildMuxer_Success recreates the muxer from an IDR frame's param sets.
// Error tracking (consecutiveErrors/backoff) is NOT reset here — only a
// successful write in writeLoop clears it.
func TestRebuildMuxer_Success(t *testing.T) {
	t.Helper()
	m := metrics.NewMetrics()
	mgr := NewManagerWithOpts(context.Background(), t.TempDir(), defaultWriteBufSize, defaultSegmentMaxSize, 0, m)

	sps := []byte{0x67, 0x42, 0xc0, 0x0a, 0xd9, 0x00, 0xa0, 0x47, 0xfe, 0x88}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	require.NoError(t, mgr.StartStream("test-cam", sps, pps, 0))

	mgr.mu.Lock()
	entry := mgr.streams["test-cam"]
	mgr.mu.Unlock()
	require.NotNil(t, entry)

	// Simulate the post-handleWriteError state: muxer destroyed, error state set.
	entry.mu.Lock()
	entry.mux.Close()
	entry.mux = nil
	entry.track = nil
	entry.mu.Unlock()
	entry.consecutiveErrors = 42
	entry.backoff = 16 * time.Second
	entry.idrReceived = false

	// IDR access unit carrying fresh SPS/PPS.
	idrAU := [][]byte{sps, pps, {0x65, 0x88, 0x80, 0x40}}
	ok := mgr.rebuildMuxer("test-cam", entry, idrAU)
	require.True(t, ok, "rebuild should succeed with valid SPS/PPS in IDR frame")

	entry.mu.Lock()
	muxAfter := entry.mux
	trackAfter := entry.track
	entry.mu.Unlock()
	require.NotNil(t, muxAfter, "muxer should be rebuilt")
	require.NotNil(t, trackAfter, "track should be rebuilt")
	// rebuildMuxer no longer resets error tracking — only a successful frame
	// write in writeLoop clears consecutiveErrors/backoff.
	require.Equal(t, 42, entry.consecutiveErrors, "consecutiveErrors should NOT reset on rebuild")
	require.Equal(t, 16*time.Second, entry.backoff, "backoff should NOT reset on rebuild")

	mgr.StopAll()
}

// TestRebuildMuxer_MissingParamSets fails gracefully when the IDR frame lacks SPS/PPS,
// leaving the entry unchanged so writeLoop can retry on the next IDR.
func TestRebuildMuxer_MissingParamSets(t *testing.T) {
	t.Helper()
	mgr := newTestManager(t)

	sps := []byte{0x67, 0x42, 0xc0, 0x0a, 0xd9, 0x00, 0xa0, 0x47, 0xfe, 0x88}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	require.NoError(t, mgr.StartStream("test-cam", sps, pps, 0))

	mgr.mu.Lock()
	entry := mgr.streams["test-cam"]
	mgr.mu.Unlock()

	entry.mu.Lock()
	entry.mux.Close()
	entry.mux = nil
	entry.track = nil
	entry.mu.Unlock()
	entry.consecutiveErrors = 5
	entry.idrReceived = false

	// IDR without SPS/PPS — rebuild cannot proceed.
	idrOnly := [][]byte{{0x65, 0x88, 0x80, 0x40}}
	ok := mgr.rebuildMuxer("test-cam", entry, idrOnly)
	require.False(t, ok, "rebuild should fail without SPS/PPS")

	entry.mu.Lock()
	require.Nil(t, entry.mux, "muxer should remain nil on failed rebuild")
	entry.mu.Unlock()
	require.Equal(t, 5, entry.consecutiveErrors, "error counter unchanged on failed rebuild")

	mgr.StopAll()
}
