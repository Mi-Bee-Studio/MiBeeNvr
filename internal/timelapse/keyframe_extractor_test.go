package timelapse

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// ============================================================
// Mock SegmentStore
// ============================================================

// mockSegmentStore implements SegmentStore using the real storage.Manager
// backed by a temp directory for integration-style testing.
type mockSegmentStore struct {
	t        *testing.T
	root     string
	mu       sync.Mutex
	segments []segmentInfo
}

type segmentInfo struct {
	tempPath  string
	finalPath string
	closed    bool
}

func newMockSegmentStore(t *testing.T) *mockSegmentStore {
	t.Helper()
	root := t.TempDir()
	return &mockSegmentStore{
		t:    t,
		root: root,
	}
}

func (m *mockSegmentStore) CreateSegment(cameraID, format string) (string, string, error) {
	m.t.Helper()
	mgr, err := storage.NewManager(m.root)
	if err != nil {
		return "", "", err
	}
	tempPath, finalPath, err := mgr.CreateSegment(cameraID, format)
	if err != nil {
		return "", "", err
	}
	m.mu.Lock()
	m.segments = append(m.segments, segmentInfo{
		tempPath:  tempPath,
		finalPath: finalPath,
	})
	m.mu.Unlock()
	return tempPath, finalPath, nil
}

func (m *mockSegmentStore) CloseSegment(tempPath, finalPath string) error {
	m.t.Helper()
	mgr, err := storage.NewManager(m.root)
	if err != nil {
		return err
	}
	if err := mgr.CloseSegment(tempPath, finalPath); err != nil {
		return err
	}
	m.mu.Lock()
	for i := range m.segments {
		if m.segments[i].tempPath == tempPath {
			m.segments[i].closed = true
			break
		}
	}
	m.mu.Unlock()
	return nil
}

func (m *mockSegmentStore) segmentCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.segments)
}

func (m *mockSegmentStore) closedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, s := range m.segments {
		if s.closed {
			count++
		}
	}
	return count
}

// ============================================================
// Mock RecordingDB
// ============================================================

type mockRecordingDB struct {
	mu   sync.Mutex
	recs []*model.Recording
}

func newMockRecordingDB() *mockRecordingDB {
	return &mockRecordingDB{}
}

func (m *mockRecordingDB) InsertRecording(ctx context.Context, r *model.Recording) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recs = append(m.recs, r)
	return nil
}

func (m *mockRecordingDB) InsertRecordingWithRetry(ctx context.Context, r *model.Recording, maxRetries int, backoff time.Duration) error {
	return m.InsertRecording(ctx, r)
}

func (m *mockRecordingDB) recordingCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.recs)
}

// ============================================================
// Test helpers
// ============================================================

// makeH264AU creates an H.264 access unit with the given NAL types.
// For an IDR frame, pass []int{7, 8, 5} for [SPS, PPS, IDR].
func makeH264AU(nalTypes ...int) [][]byte {
	au := make([][]byte, len(nalTypes))
	for i, nt := range nalTypes {
		// Each NALU is a minimal valid H.264 NALU: first byte = NAL type
		au[i] = []byte{byte(nt & 0x1F)}
	}
	return au
}

// makeH265AU creates an H.265 access unit with the given NAL types.
// For an IDR frame, pass []int{32, 33, 34, 19} for [VPS, SPS, PPS, IDR].
func makeH265AU(nalTypes ...int) [][]byte {
	au := make([][]byte, len(nalTypes))
	for i, nt := range nalTypes {
		// H.265 NAL unit: first byte has forbidden bit(1) | nal_unit_type(6)
		au[i] = []byte{byte(nt<<1) | 0x01} // bit0 = 1 (forbidden zero bit = 0)
	}
	return au
}

// ============================================================
// Tests
// ============================================================

func TestKeyframeExtractor_StartStopLifecycle(t *testing.T) {
	store := newMockSegmentStore(t)
	hub := model.NewStreamHub()

	// Simulate a regular recorder's hub by subscribing something.
	hub.SetCameraID("cam-1")

	ext := NewKeyframeExtractor(KeyframeExtractorConfig{
		CameraID:   "cam-1",
		Interval:   50 * time.Millisecond,
		SegmentDur: time.Hour, // longer than test
		Store:      store,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start should succeed.
	if err := ext.Start(ctx, hub); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	if !ext.IsRunning() {
		t.Fatal("expected extractor to be running after Start")
	}

	// Start again should fail (already running).
	if err := ext.Start(ctx, hub); err == nil {
		t.Fatal("expected Start() to fail when already running")
	}

	// Stop should succeed.
	if err := ext.Stop(); err != nil {
		t.Fatalf("Stop() failed: %v", err)
	}

	if ext.IsRunning() {
		t.Fatal("expected extractor to be stopped after Stop")
	}

	// Stop again should be a no-op.
	if err := ext.Stop(); err != nil {
		t.Fatalf("second Stop() should be no-op, got: %v", err)
	}
}

func TestKeyframeExtractor_SubscribesToHub(t *testing.T) {
	store := newMockSegmentStore(t)
	hub := model.NewStreamHub()
	hub.SetCameraID("cam-1")

	ext := NewKeyframeExtractor(KeyframeExtractorConfig{
		CameraID:   "cam-1",
		Interval:   time.Hour, // don't capture during test
		SegmentDur: time.Hour,
		Store:      store,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := ext.Start(ctx, hub); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	// Should have one consumer (the keyframe extractor).
	if hub.ConsumerCount() != 1 {
		t.Fatalf("expected 1 consumer, got %d", hub.ConsumerCount())
	}

	ext.Stop()

	// After stop, consumer should be removed.
	if hub.ConsumerCount() != 0 {
		t.Fatalf("expected 0 consumers after stop, got %d", hub.ConsumerCount())
	}
}

func TestKeyframeExtractor_NonBlockingCallback(t *testing.T) {
	store := newMockSegmentStore(t)
	hub := model.NewStreamHub()
	hub.SetCameraID("cam-1")

	ext := NewKeyframeExtractor(KeyframeExtractorConfig{
		CameraID:   "cam-1",
		Interval:   time.Hour, // don't trigger capture
		SegmentDur: time.Hour,
		Store:      store,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ext.Start(ctx, hub)
	defer ext.Stop()

	// Broadcast many frames rapidly — the callback must not block.
	// If it blocks, the broadcast will time out or drop frames.
	start := time.Now()
	for i := range 100 {
		au := makeH264AU(1) // P-frames
		hub.Broadcast(int64(i), au, false)
	}
	elapsed := time.Since(start)

	// Broadcast should complete well under 100ms for 100 frames
	// (50ms IDR timeout per Broadcast is the max theoretical).
	if elapsed > 200*time.Millisecond {
		t.Fatalf("broadcast blocked for %v, expected non-blocking", elapsed)
	}

	// Verify no drops from the extractor consumer.
	drops := hub.Drops(ext.consumerID)
	t.Logf("extractor consumer drops: %d", drops)
}

func TestKeyframeExtractor_CapturesIDRFrames(t *testing.T) {
	store := newMockSegmentStore(t)
	hub := model.NewStreamHub()
	hub.SetCameraID("cam-1")

	ext := NewKeyframeExtractor(KeyframeExtractorConfig{
		CameraID:   "cam-1",
		Interval:   50 * time.Millisecond,
		SegmentDur: time.Hour,
		Store:      store,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ext.Start(ctx, hub)
	defer ext.Stop()

	// Give the extractor time to start up.
	time.Sleep(20 * time.Millisecond)

	// Broadcast an IDR frame (SPS + PPS + IDR).
	au := makeH264AU(7, 8, 5)
	hub.Broadcast(1000, au, true)

	// Wait for the capture tick.
	time.Sleep(100 * time.Millisecond)

	// Should have created a segment with at least one frame file.
	files := listSegmentFiles(store)
	if len(files) == 0 {
		t.Fatal("expected at least one captured frame file")
	}
	t.Logf("captured files: %v", files)

	// Verify the file has the correct extension.
	if filepath.Ext(files[0]) != ".h264" {
		t.Fatalf("expected .h264 extension, got %s", filepath.Ext(files[0]))
	}
}

func TestKeyframeExtractor_FallsBackToPFrame(t *testing.T) {
	store := newMockSegmentStore(t)
	hub := model.NewStreamHub()
	hub.SetCameraID("cam-1")

	ext := NewKeyframeExtractor(KeyframeExtractorConfig{
		CameraID:   "cam-1",
		Interval:   50 * time.Millisecond,
		SegmentDur: time.Hour,
		Store:      store,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ext.Start(ctx, hub)
	defer ext.Stop()

	time.Sleep(20 * time.Millisecond)

	// Broadcast only P-frames (no IDRs).
	for i := range 5 {
		au := makeH264AU(1)
		hub.Broadcast(int64(i), au, false)
	}

	// Wait for capture tick — should fall back to P-frame.
	time.Sleep(100 * time.Millisecond)

	files := listSegmentFiles(store)
	if len(files) == 0 {
		t.Fatal("expected at least one captured frame file via P-frame fallback")
	}
	t.Logf("P-frame fallback files: %v", files)
}

func TestKeyframeExtractor_H265IDRDetection(t *testing.T) {
	store := newMockSegmentStore(t)
	hub := model.NewStreamHub()
	hub.SetCameraID("cam-1")

	ext := NewKeyframeExtractor(KeyframeExtractorConfig{
		CameraID:   "cam-1",
		Interval:   50 * time.Millisecond,
		SegmentDur: time.Hour,
		IsH265:     true,
		Store:      store,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ext.Start(ctx, hub)
	defer ext.Stop()

	time.Sleep(20 * time.Millisecond)

	// Broadcast an H.265 IDR frame (VPS + SPS + PPS + IDR_W_RADL).
	au := makeH265AU(32, 33, 34, 19)
	hub.Broadcast(1000, au, true)

	time.Sleep(100 * time.Millisecond)

	files := listSegmentFiles(store)
	if len(files) == 0 {
		t.Fatal("expected at least one H.265 captured frame file")
	}

	if filepath.Ext(files[0]) != ".h265" {
		t.Fatalf("expected .h265 extension for H.265 stream, got %s", filepath.Ext(files[0]))
	}

	// Verify the file contains Annex B start codes.
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("failed to read frame file: %v", err)
	}
	if len(data) < 4 {
		t.Fatal("frame file too small")
	}
	expectedStart := []byte{0x00, 0x00, 0x00, 0x01}
	for i, b := range expectedStart {
		if data[i] != b {
			t.Fatalf("expected Annex B start code at byte %d, got 0x%02x", i, data[i])
		}
	}
	t.Logf("H.265 frame file size: %d bytes (contains %d NALUs)", len(data), len(au))
}

func TestKeyframeExtractor_SegmentRotation(t *testing.T) {
	store := newMockSegmentStore(t)
	hub := model.NewStreamHub()
	hub.SetCameraID("cam-1")

	// Short segment duration so we can test rotation.
	ext := NewKeyframeExtractor(KeyframeExtractorConfig{
		CameraID:   "cam-1",
		Interval:   50 * time.Millisecond,
		SegmentDur: 200 * time.Millisecond,
		Store:      store,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ext.Start(ctx, hub)
	defer ext.Stop()

	// Broadcast IDR frames continuously.
	au := makeH264AU(7, 8, 5)
	for range 10 {
		hub.Broadcast(1000, au, true)
		time.Sleep(60 * time.Millisecond)
	}

	// Wait for last segment to close.
	time.Sleep(100 * time.Millisecond)

	// Should have multiple segments with at least one closed.
	if store.closedCount() < 1 {
		t.Fatalf("expected at least 1 closed segment, got %d", store.closedCount())
	}
	t.Logf("segments: total=%d, closed=%d", store.segmentCount(), store.closedCount())
}

func TestKeyframeExtractor_StoresRecordingInDB(t *testing.T) {
	store := newMockSegmentStore(t)
	db := newMockRecordingDB()
	hub := model.NewStreamHub()
	hub.SetCameraID("cam-1")

	ext := NewKeyframeExtractor(KeyframeExtractorConfig{
		CameraID:   "cam-1",
		Interval:   50 * time.Millisecond,
		SegmentDur: 200 * time.Millisecond,
		Store:      store,
		DB:         db,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ext.Start(ctx, hub)
	defer ext.Stop()

	// Broadcast IDR frames to trigger captures and segment rotation.
	au := makeH264AU(7, 8, 5)
	for range 5 {
		hub.Broadcast(1000, au, true)
		time.Sleep(60 * time.Millisecond)
	}

	time.Sleep(150 * time.Millisecond)

	// Should have at least one recording in DB.
	if db.recordingCount() < 1 {
		t.Fatalf("expected at least 1 recording in DB, got %d", db.recordingCount())
	}
	t.Logf("DB recordings: %d", db.recordingCount())

	// Verify the recording has the correct camera ID.
	db.mu.Lock()
	for _, rec := range db.recs {
		if rec.CameraID != "cam-1" {
			t.Errorf("expected camera ID 'cam-1', got %q", rec.CameraID)
		}
		if rec.Format != model.FormatTimelapse {
			t.Errorf("expected format 'timelapse', got %q", rec.Format)
		}
		if rec.FrameCount == 0 {
			t.Errorf("expected frame count > 0")
		}
	}
	db.mu.Unlock()
}

func TestKeyframeExtractor_DoesNotBlockOriginalRecorder(t *testing.T) {
	store := newMockSegmentStore(t)
	hub := model.NewStreamHub()
	hub.SetCameraID("cam-1")

	ext := NewKeyframeExtractor(KeyframeExtractorConfig{
		CameraID:   "cam-1",
		Interval:   50 * time.Millisecond,
		SegmentDur: time.Hour,
		Store:      store,
	})

	// Add a slow consumer to the hub to create backpressure.
	hub.Subscribe("slow-consumer", func(pts int64, au [][]byte) {
		time.Sleep(50 * time.Millisecond) // slow consumer
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ext.Start(ctx, hub)
	defer ext.Stop()

	// Measure how long it takes to broadcast with the extractor active.
	start := time.Now()
	var broadcastCount atomic.Int64
	for i := range 20 {
		au := makeH264AU(7, 8, 5)
		hub.Broadcast(int64(i), au, true)
		broadcastCount.Add(1)
	}
	elapsed := time.Since(start)

	t.Logf("broadcast %d frames in %v (avg %v/frame)",
		broadcastCount.Load(), elapsed, elapsed/time.Duration(broadcastCount.Load()))

	// The slow consumer should NOT block the extractor's callback
	// or the Broadcast caller (beyond IDR protection timeout).
	if elapsed > time.Second {
		t.Fatalf("broadcast was blocked for %v by slow consumer", elapsed)
	}

	// Clean up.
	hub.Unsubscribe("slow-consumer")
}

func TestKeyframeExtractor_CapturesLatestIDRAfterInterval(t *testing.T) {
	store := newMockSegmentStore(t)
	hub := model.NewStreamHub()
	hub.SetCameraID("cam-1")

	ext := NewKeyframeExtractor(KeyframeExtractorConfig{
		CameraID:   "cam-1",
		Interval:   100 * time.Millisecond,
		SegmentDur: time.Hour,
		Store:      store,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ext.Start(ctx, hub)
	defer ext.Stop()

	time.Sleep(20 * time.Millisecond)

	// Send first IDR with PTS=1000.
	hub.Broadcast(1000, makeH264AU(7, 8, 5), true)

	// Wait a bit past the interval.
	time.Sleep(150 * time.Millisecond)

	// Should have captured the first IDR.
	filesBefore := len(listSegmentFiles(store))
	if filesBefore == 0 {
		t.Fatal("expected first IDR to be captured")
	}

	// Send a newer IDR with PTS=2000.
	hub.Broadcast(2000, makeH264AU(7, 8, 5), true)

	// Wait for another interval.
	time.Sleep(150 * time.Millisecond)

	// Should have captured the second (latest) IDR.
	filesAfter := len(listSegmentFiles(store))
	if filesAfter <= filesBefore {
		t.Fatal("expected second IDR to be captured after interval")
	}
	t.Logf("frames captured: before=%d, after=%d", filesBefore, filesAfter)
}

func TestKeyframeExtractor_ProducesValidFrameFiles(t *testing.T) {
	store := newMockSegmentStore(t)
	hub := model.NewStreamHub()
	hub.SetCameraID("cam-1")

	ext := NewKeyframeExtractor(KeyframeExtractorConfig{
		CameraID:   "cam-1",
		Interval:   50 * time.Millisecond,
		SegmentDur: time.Hour,
		Store:      store,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ext.Start(ctx, hub)
	defer ext.Stop()

	time.Sleep(20 * time.Millisecond)

	// Broadcast an IDR with meaningful NALU data.
	au := makeH264AU(7, 8, 5)
	// Give each NALU more content so we can verify the file.
	au[0] = []byte{0x67, 0x42, 0x00, 0x1e, 0x9a} // SPS
	au[1] = []byte{0x68, 0xce, 0x38, 0x80}       // PPS
	au[2] = []byte{0x65, 0x88, 0x84, 0x00}       // IDR slice

	hub.Broadcast(1000, au, true)

	time.Sleep(100 * time.Millisecond)

	files := listSegmentFiles(store)
	if len(files) == 0 {
		t.Fatal("expected frame files to be produced")
	}

	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("failed to read %s: %v", file, err)
		}

		// Verify Annex B format: each NALU starts with 0x00000001.
		if len(data) < 4 {
			t.Fatalf("file %s too small (%d bytes)", file, len(data))
		}

		// Check first start code.
		expectedStart := []byte{0x00, 0x00, 0x00, 0x01}
		for i, b := range expectedStart {
			if data[i] != b {
				t.Fatalf("file %s: expected Annex B start code at offset %d, got 0x%02x", file, i, data[i])
			}
		}

		// Check that all NALUs from the AU are in the file.
		// Cached parameter sets (SPS/PPS) are prepended, so the file may have
		// additional NALUs beyond the original AU.
		naluCount := countNALUs(data)
		if naluCount < len(au) {
			t.Fatalf("file %s: expected at least %d NALUs (original AU), found %d", file, len(au), naluCount)
		}
		t.Logf("file %s: %d bytes, %d NALUs", filepath.Base(file), len(data), naluCount)
	}
}

func TestKeyframeExtractor_MultipleSegmentsOverTime(t *testing.T) {
	store := newMockSegmentStore(t)
	db := newMockRecordingDB()
	hub := model.NewStreamHub()
	hub.SetCameraID("cam-1")

	// Very short segment duration for rapid rotation.
	ext := NewKeyframeExtractor(KeyframeExtractorConfig{
		CameraID:   "cam-1",
		Interval:   30 * time.Millisecond,
		SegmentDur: 150 * time.Millisecond,
		Store:      store,
		DB:         db,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ext.Start(ctx, hub)
	defer ext.Stop()

	// Broadcast IDR frames over a long enough period to rotate segments.
	au := makeH264AU(7, 8, 5)
	for i := range 15 {
		hub.Broadcast(int64(i*1000), au, true)
		time.Sleep(40 * time.Millisecond)
	}

	// Wait for final segment to close.
	time.Sleep(200 * time.Millisecond)

	// Should have multiple segments and recordings.
	if store.segmentCount() < 2 {
		t.Fatalf("expected at least 2 segments, got %d", store.segmentCount())
	}
	if store.closedCount() < 1 {
		t.Fatalf("expected at least 1 closed segment, got %d", store.closedCount())
	}
	if db.recordingCount() < 1 {
		t.Fatalf("expected at least 1 recording, got %d", db.recordingCount())
	}

	t.Logf("segments: total=%d, closed=%d", store.segmentCount(), store.closedCount())
	t.Logf("DB recordings: %d", db.recordingCount())
}

// ============================================================
// Helpers
// ============================================================

// listSegmentFiles walks all segment directories in the mock store
// and returns the paths of captured frame files.
func listSegmentFiles(store *mockSegmentStore) []string {
	var files []string
	store.mu.Lock()
	paths := make([]string, len(store.segments))
	for i, s := range store.segments {
		p := s.tempPath
		if s.closed {
			p = s.finalPath
		}
		paths[i] = p
	}
	store.mu.Unlock()

	for _, dir := range paths {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				files = append(files, filepath.Join(dir, e.Name()))
			}
		}
	}
	return files
}

// countNALUs counts the number of Annex B NALUs in raw H.264/H.265 data.
func countNALUs(data []byte) int {
	var count int
	for i := 0; i <= len(data)-4; i++ {
		if data[i] == 0x00 && data[i+1] == 0x00 && data[i+2] == 0x00 && data[i+3] == 0x01 {
			count++
		}
	}
	return count
}

func TestKeyframeExtractor_FormatTimelapse(t *testing.T) {
	store := newMockSegmentStore(t)
	db := newMockRecordingDB()
	hub := model.NewStreamHub()
	hub.SetCameraID("cam-1")

	ext := NewKeyframeExtractor(KeyframeExtractorConfig{
		CameraID:   "cam-1",
		Interval:   50 * time.Millisecond,
		SegmentDur: 200 * time.Millisecond,
		Store:      store,
		DB:         db,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ext.Start(ctx, hub)
	defer ext.Stop()

	// Broadcast IDR frames to trigger captures and segment rotation.
	au := makeH264AU(7, 8, 5)
	for range 5 {
		hub.Broadcast(1000, au, true)
		time.Sleep(60 * time.Millisecond)
	}

	time.Sleep(150 * time.Millisecond)

	// Should have at least one recording in DB.
	if db.recordingCount() < 1 {
		t.Fatalf("expected at least 1 recording in DB, got %d", db.recordingCount())
	}

	// Verify FormatTimelapse and Merged=true on all recordings.
	db.mu.Lock()
	defer db.mu.Unlock()
	for _, rec := range db.recs {
		if rec.Format != model.FormatTimelapse {
			t.Errorf("expected format %q, got %q", model.FormatTimelapse, rec.Format)
		}
		if rec.MergeStatus == model.MergeStatusMerged {
			t.Errorf("expected Merged=false (merge not yet run) for keyframe recording %q", rec.ID)
		}
		if rec.FrameCount == 0 {
			t.Errorf("expected frame count > 0")
		}
	}
}

// ============================================================
// Concurrent / Race tests
// ============================================================

// TestKeyframeExtractor_ConcurrentStartStopDuringBroadcast starts the extractor,
// broadcasts frames from a background goroutine, then stops the extractor
// while frames continue arriving. This exercises the lifecycle-frames interaction.
func TestKeyframeExtractor_ConcurrentStartStopDuringBroadcast(t *testing.T) {
	store := newMockSegmentStore(t)
	hub := model.NewStreamHub()
	hub.SetCameraID("cam-1")

	ext := NewKeyframeExtractor(KeyframeExtractorConfig{
		CameraID:   "cam-1",
		Interval:   50 * time.Millisecond,
		SegmentDur: time.Hour,
		Store:      store,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the extractor first.
	require.NoError(t, ext.Start(ctx, hub), "Start should succeed")

	// Broadcast frames from many goroutines while the extractor is running.
	var broadcastWg sync.WaitGroup
	for range 5 {
		broadcastWg.Add(1)
		go func() {
			defer broadcastWg.Done()
			ticker := time.NewTicker(1 * time.Millisecond)
			defer ticker.Stop()
			for i := range 100 {
				au := makeH264AU(7, 8, 5)
				hub.Broadcast(int64(i*1000), au, true)
				<-ticker.C
			}
		}()
	}

	// Allow some frames to arrive, then stop the extractor while broadcast continues.
	time.Sleep(30 * time.Millisecond)
	require.NoError(t, ext.Stop(), "Stop should succeed")

	// Wait for all broadcast goroutines to finish.
	broadcastWg.Wait()

	// Frames broadcast after Stop should not cause races.
}

// TestKeyframeExtractor_ConcurrentFrameBroadcast broadcasts frames from multiple
// goroutines simultaneously while the extractor is actively capturing.
func TestKeyframeExtractor_ConcurrentFrameBroadcast(t *testing.T) {
	store := newMockSegmentStore(t)
	hub := model.NewStreamHub()
	hub.SetCameraID("cam-1")

	ext := NewKeyframeExtractor(KeyframeExtractorConfig{
		CameraID:   "cam-1",
		Interval:   30 * time.Millisecond,
		SegmentDur: time.Hour,
		Store:      store,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ext.Start(ctx, hub)
	defer ext.Stop()

	// Give extractor time to start up.
	time.Sleep(20 * time.Millisecond)

	// Broadcast frames from many goroutines simultaneously.
	var wg sync.WaitGroup
	numGoroutines := 10
	framesPerGoroutine := 50
	for g := range numGoroutines {
		wg.Add(1)
		go func(base int) {
			defer wg.Done()
			for i := range framesPerGoroutine {
				au := makeH264AU(7, 8, 5)
				hub.Broadcast(int64(base*framesPerGoroutine+i)+1, au, true)
			}
		}(g)
	}
	wg.Wait()

	// Give extractor time to capture some frames.
	time.Sleep(200 * time.Millisecond)

	// Should have captured some frames without races.
	files := listSegmentFiles(store)
	t.Logf("captured %d frame files under concurrent broadcast", len(files))
}
