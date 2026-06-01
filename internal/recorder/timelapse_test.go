package recorder

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// --- Mock DB for timelapse tests ---

type mockTimelapseDB struct {
	inserted []*model.Recording
}

func (m *mockTimelapseDB) InsertRecording(_ context.Context, r *model.Recording) error {
	m.inserted = append(m.inserted, r)
	return nil
}

func (m *mockTimelapseDB) InsertRecordingWithRetry(_ context.Context, r *model.Recording, _ int, _ time.Duration) error {
	m.inserted = append(m.inserted, r)
	return nil
}

// --- Mock segment store for timelapse tests ---

type mockTimelapseStore struct {
	dataDir     string
	segmentSeq  atomic.Int64
	frameFiles  []string // tracks all written frames
}

func newMockTimelapseStore(dataDir string) *mockTimelapseStore {
	return &mockTimelapseStore{dataDir: dataDir}
}

func (s *mockTimelapseStore) CreateSegment(cameraID string, _ string) (string, string, error) {
	seq := s.segmentSeq.Add(1)
	name := fmt.Sprintf("%s_%d_tmp", cameraID, seq)
	tempPath := filepath.Join(s.dataDir, name)
	finalPath := filepath.Join(s.dataDir, fmt.Sprintf("%s_%d", cameraID, seq))
	if err := os.MkdirAll(tempPath, 0o755); err != nil {
		return "", "", err
	}
	return tempPath, finalPath, nil
}

func (s *mockTimelapseStore) WriteFrame(tempPath string, data []byte) (int, error) {
	name := fmt.Sprintf("frame_%06d.jpg", len(s.frameFiles))
	path := filepath.Join(tempPath, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return 0, err
	}
	s.frameFiles = append(s.frameFiles, path)
	return len(data), nil
}

func (s *mockTimelapseStore) CloseSegment(tempPath, finalPath string) error {
	return os.Rename(tempPath, finalPath)
}

// --- Mock FFmpeg runner for timelapse tests ---

type mockFFRunner struct {
	shouldFail bool
}

func (r *mockFFRunner) Run(_ context.Context, _ string, _ []string, outputPath string) error {
	if r.shouldFail {
		return fmt.Errorf("mock ffmpeg error")
	}
	// Simulate FFmpeg creating output MP4
	if err := os.WriteFile(outputPath, []byte("fake-mp4"), 0o644); err != nil {
		return err
	}
	return nil
}

// --- Tests ---

func TestTimelapseRecorder_ImplementsRecorder(t *testing.T) {
	var _ model.Recorder = (*TimelapseRecorder)(nil)
}

func TestTimelapseRecorder_StartStop(t *testing.T) {
	store := newMockTimelapseStore(t.TempDir())
	db := &mockTimelapseDB{}

	rec := NewTimelapseRecorder(TimelapseRecorderConfig{
		CameraID:   "cam-tl-startstop",
		Interval:   1 * time.Second,
		OutputFPS:  30,
		VideoCodec: "h264",
		SegmentDur: 5 * time.Minute,
		DataDir:    t.TempDir(),
		DB:         db,
	}, store)

	require.Equal(t, model.StatusStopped, rec.Status())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	require.Equal(t, model.StatusRecording, rec.Status())

	// Starting again should fail
	require.Error(t, rec.Start(ctx))

	require.NoError(t, rec.Stop())
	require.Equal(t, model.StatusStopped, rec.Status())

	// Double stop should be safe
	require.NoError(t, rec.Stop())
}

func TestTimelapseRecorder_SavesEveryNthFrame(t *testing.T) {
	store := newMockTimelapseStore(t.TempDir())
	db := &mockTimelapseDB{}
	ff := &mockFFRunner{}

	rec := NewTimelapseRecorderWithRunner(TimelapseRecorderConfig{
		CameraID:   "cam-tl-saves",
		Interval:   200 * time.Millisecond,
		OutputFPS:  10,
		VideoCodec: "h264",
		SegmentDur: 5 * time.Minute,
		DataDir:    t.TempDir(),
		DB:         db,
	}, store, ff.Run)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))

	jpeg := generateTestJPEG()
	// Send 10 frames rapidly — only 1st should be saved (interval=200ms)
	for i := 0; i < 10; i++ {
		rec.OnFrame(jpeg)
		time.Sleep(10 * time.Millisecond) // 10ms apart, well under 200ms interval
	}

	// Wait for interval to pass
	time.Sleep(250 * time.Millisecond)

	// Send 1 more frame — should be saved (200ms since last)
	rec.OnFrame(jpeg)
	time.Sleep(50 * time.Millisecond)

	require.NoError(t, rec.Stop())

	// With interval=200ms and frames at 10ms spacing, only ~2 frames should be captured:
	// 1st frame (always captured) + 1 frame after 200ms gap
	require.Len(t, store.frameFiles, 2, "expected 2 frames captured with 200ms interval from 11 rapid frames")
}

func TestTimelapseRecorder_SegmentMerge(t *testing.T) {
	store := newMockTimelapseStore(t.TempDir())
	db := &mockTimelapseDB{}
	ff := &mockFFRunner{}

	segmentDur := 150 * time.Millisecond
	rec := NewTimelapseRecorderWithRunner(TimelapseRecorderConfig{
		CameraID:   "cam-tl-merge",
		Interval:   10 * time.Millisecond,
		OutputFPS:  30,
		VideoCodec: "h264",
		SegmentDur: segmentDur,
		DataDir:    t.TempDir(),
		DB:         db,
	}, store, ff.Run)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))

	jpeg := generateTestJPEG()

	// Send frames for first segment
	for i := 0; i < 3; i++ {
		rec.OnFrame(jpeg)
		time.Sleep(20 * time.Millisecond)
	}

	// Wait for segment to close and FFmpeg to run
	time.Sleep(500 * time.Millisecond)

	// Send frames for second segment
	for i := 0; i < 3; i++ {
		rec.OnFrame(jpeg)
		time.Sleep(20 * time.Millisecond)
	}

	time.Sleep(500 * time.Millisecond)

	require.NoError(t, rec.Stop())

	// Should have merged recordings in DB with format="timelapse"
	timelapseCount := 0
	for _, r := range db.inserted {
		if r.Format == "timelapse" {
			timelapseCount++
			require.Contains(t, r.FilePath, "_timelapse.mp4")
			require.Greater(t, r.FrameCount, 0)
		}
	}
	require.Equal(t, 2, timelapseCount, "expected 2 timelapse recordings from 2 segments")
}

func TestTimelapseRecorder_FFmpegFailurePreservesOriginal(t *testing.T) {
	store := newMockTimelapseStore(t.TempDir())
	db := &mockTimelapseDB{}
	ff := &mockFFRunner{shouldFail: true}

	rec := NewTimelapseRecorderWithRunner(TimelapseRecorderConfig{
		CameraID:   "cam-tl-fferr",
		Interval:   10 * time.Millisecond,
		OutputFPS:  30,
		VideoCodec: "h264",
		SegmentDur: 100 * time.Millisecond,
		DataDir:    t.TempDir(),
		DB:         db,
	}, store, ff.Run)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))

	jpeg := generateTestJPEG()
	rec.OnFrame(jpeg)
	time.Sleep(400 * time.Millisecond)

	require.NoError(t, rec.Stop())

	// No timelapse recordings should be registered (FFmpeg failed)
	timelapseCount := 0
	for _, r := range db.inserted {
		if r.Format == "timelapse" {
			timelapseCount++
		}
	}
	require.Equal(t, 0, timelapseCount, "no timelapse recordings when FFmpeg fails")
}

func TestTimelapseRecorder_H265Codec(t *testing.T) {
	store := newMockTimelapseStore(t.TempDir())
	db := &mockTimelapseDB{}
	ff := &mockFFRunner{}

	rec := NewTimelapseRecorderWithRunner(TimelapseRecorderConfig{
		CameraID:   "cam-tl-h265",
		Interval:   10 * time.Millisecond,
		OutputFPS:  25,
		VideoCodec: "h265",
		SegmentDur: 100 * time.Millisecond,
		DataDir:    t.TempDir(),
		DB:         db,
	}, store, ff.Run)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))

	jpeg := generateTestJPEG()
	rec.OnFrame(jpeg)
	time.Sleep(400 * time.Millisecond)

	require.NoError(t, rec.Stop())

	require.Len(t, db.inserted, 1)
	require.Equal(t, model.Format("timelapse"), db.inserted[0].Format)
}

func TestTimelapseRecorder_DropInvalidFrames(t *testing.T) {
	store := newMockTimelapseStore(t.TempDir())
	db := &mockTimelapseDB{}

	rec := NewTimelapseRecorder(TimelapseRecorderConfig{
		CameraID:   "cam-tl-invalid",
		Interval:   10 * time.Millisecond,
		OutputFPS:  30,
		VideoCodec: "h264",
		SegmentDur: 5 * time.Minute,
		DataDir:    t.TempDir(),
		DB:         db,
	}, store)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))

	// Invalid frame (not JPEG magic bytes)
	rec.OnFrame([]byte("not-a-jpeg"))
	time.Sleep(50 * time.Millisecond)

	// Valid frame
	rec.OnFrame(generateTestJPEG())
	time.Sleep(50 * time.Millisecond)

	require.NoError(t, rec.Stop())

	// Only the valid frame should be written
	require.Len(t, store.frameFiles, 1, "expected only 1 valid frame, invalid frames dropped")
}

func TestTimelapseRecorder_ContextCancellation(t *testing.T) {
	store := newMockTimelapseStore(t.TempDir())
	db := &mockTimelapseDB{}

	rec := NewTimelapseRecorder(TimelapseRecorderConfig{
		CameraID:   "cam-tl-cancel",
		Interval:   1 * time.Second,
		OutputFPS:  30,
		VideoCodec: "h264",
		SegmentDur: 5 * time.Minute,
		DataDir:    t.TempDir(),
		DB:         db,
	}, store)

	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, rec.Start(ctx))

	// Send some frames
	rec.OnFrame(generateTestJPEG())
	time.Sleep(50 * time.Millisecond)

	// Cancel context
	cancel()

	done := make(chan struct{})
	go func() {
		rec.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop() did not return within timeout after context cancellation")
	}

	require.Equal(t, model.StatusStopped, rec.Status())
}

func TestTimelapseRecorder_Defaults(t *testing.T) {
	store := newMockTimelapseStore(t.TempDir())

	// Zero-value config — should apply defaults
	rec := NewTimelapseRecorder(TimelapseRecorderConfig{
		CameraID:   "cam-tl-defaults",
		DataDir:    t.TempDir(),
	}, store)

	require.Equal(t, model.StatusStopped, rec.Status())
	// Interval default should be set (non-zero)
	require.True(t, rec.interval > 0, "interval should have default value")
	// OutputFPS default should be set
	require.Greater(t, rec.outputFPS, 0, "outputFPS should have default value")
	// VideoCodec default should be set
	require.NotEmpty(t, rec.videoCodec, "videoCodec should have default value")
}

func TestTimelapseRecorder_StreamHub(t *testing.T) {
	rec := NewTimelapseRecorder(TimelapseRecorderConfig{
		CameraID:   "cam-tl-hub",
		Interval:   1 * time.Second,
		DataDir:    t.TempDir(),
	}, newMockTimelapseStore(t.TempDir()))

	// Hub should be nil before Start (no streaming support needed for timelapse)
	require.Nil(t, rec.GetHub())
}
