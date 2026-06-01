package recorder

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

var timelapseLogger = slog.Default().With("component", "timelapse-recorder")

// TimelapseRecorderConfig holds configuration for the timelapse recorder.
type TimelapseRecorderConfig struct {
	CameraID   string
	Interval   time.Duration  // frame capture interval (e.g., 5s)
	OutputFPS  int            // output video FPS (e.g., 30)
	VideoCodec string         // "h264" or "h265"
	SegmentDur time.Duration  // segment duration before merging
	DataDir    string         // base data directory
	DB         RecordingDB
	Metrics    *metrics.Metrics
}

// ffRunnerFunc executes FFmpeg with the given arguments.
type ffRunnerFunc func(ctx context.Context, ffmpegPath string, args []string, outputPath string) error

// defaultTimelapseFFRunner runs FFmpeg via exec.CommandContext.
func defaultTimelapseFFRunner(ctx context.Context, ffmpegPath string, args []string, outputPath string) error {
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	return cmd.Run()
}

// TimelapseRecorder captures JPEG frames at a configurable interval and
// periodically merges them into MP4 via FFmpeg. It implements model.Recorder.
type TimelapseRecorder struct {
	cfg     TimelapseRecorderConfig
	store   SegmentStore
	metrics *metrics.Metrics
	runFF   ffRunnerFunc

	interval   time.Duration
	outputFPS  int
	videoCodec string
	segmentDur time.Duration

	mu     sync.Mutex
	status model.RecorderStatus
	cancel context.CancelFunc
	done   chan struct{}

	lastCapture atomic.Int64 // UnixNano of last frame capture

	// segment tracking
	segMu        sync.Mutex
	curTempPath  string
	curFinalPath string
	segStart     time.Time
	frameCount   int
	jpegSeq      int64

	// mergeWg tracks in-flight FFmpeg merge goroutines so Stop() can wait for them.
	mergeWg sync.WaitGroup

	Hub *model.StreamHub
}

// GetHub returns the StreamHub for frame fan-out (nil for timelapse — no live streaming).
func (r *TimelapseRecorder) GetHub() *model.StreamHub { return r.Hub }

// incActive increments the active recordings gauge if metrics is available.
func (r *TimelapseRecorder) incActive() {
	if r.metrics != nil {
		r.metrics.ActiveRecordings.Inc()
	}
}

// decActive decrements the active recordings gauge if metrics is available.
func (r *TimelapseRecorder) decActive() {
	if r.metrics != nil {
		r.metrics.ActiveRecordings.Dec()
	}
}

// recordSegmentCreated increments the segments created counter if metrics is available.
func (r *TimelapseRecorder) recordSegmentCreated() {
	if r.metrics != nil {
		r.metrics.SegmentsCreated.WithLabelValues(r.cfg.CameraID, "timelapse").Inc()
	}
}

// recordError increments the camera errors counter if metrics is available.
func (r *TimelapseRecorder) recordError(errorType string) {
	if r.metrics != nil {
		r.metrics.CameraErrors.WithLabelValues(r.cfg.CameraID, errorType).Inc()
	}
}

var _ model.Recorder = (*TimelapseRecorder)(nil)

// NewTimelapseRecorder creates a new TimelapseRecorder with default FFmpeg runner.
func NewTimelapseRecorder(cfg TimelapseRecorderConfig, store SegmentStore) *TimelapseRecorder {
	return NewTimelapseRecorderWithRunner(cfg, store, defaultTimelapseFFRunner)
}

// NewTimelapseRecorderWithRunner creates a TimelapseRecorder with an injectable FFmpeg runner for testing.
func NewTimelapseRecorderWithRunner(cfg TimelapseRecorderConfig, store SegmentStore, ff ffRunnerFunc) *TimelapseRecorder {
	if cfg.Interval < time.Millisecond {
		cfg.Interval = 5 * time.Second
	}
	if cfg.OutputFPS < 1 {
		cfg.OutputFPS = 30
	}
	if cfg.VideoCodec == "" {
		cfg.VideoCodec = "h264"
	}
	if cfg.SegmentDur < time.Millisecond {
		cfg.SegmentDur = DefaultSegmentDur
	}
	return &TimelapseRecorder{
		cfg:        cfg,
		store:      store,
		runFF:      ff,
		interval:   cfg.Interval,
		outputFPS:  cfg.OutputFPS,
		videoCodec: cfg.VideoCodec,
		segmentDur: cfg.SegmentDur,
		status:     model.StatusStopped,
	}
}

// OnFrame is called for each incoming JPEG frame. Only saves every Nth frame
// based on the configured interval. Non-blocking.
func (r *TimelapseRecorder) OnFrame(data []byte) {
	if len(data) < 2 || data[0] != 0xFF || data[1] != 0xD8 {
		return // not a valid JPEG
	}

	now := time.Now().UnixNano()
	last := r.lastCapture.Load()
	if now-last < r.interval.Nanoseconds() {
		return // too soon, skip
	}

	// Try to update lastCapture — CAS to prevent duplicate saves under concurrency
	if !r.lastCapture.CompareAndSwap(last, now) {
		return
	}

	r.segMu.Lock()
	defer r.segMu.Unlock()

	// Create segment directory if needed
	if r.curTempPath == "" {
		tempPath, finalPath, err := r.store.CreateSegment(r.cfg.CameraID, "timelapse")
		if err != nil {
			timelapseLogger.Error("failed to create timelapse segment", "camera_id", r.cfg.CameraID, "error", err)
			return
		}
		r.curTempPath = tempPath
		r.curFinalPath = finalPath
		r.segStart = time.Now()
		r.frameCount = 0
	}

	r.jpegSeq++
	if _, err := r.store.WriteFrame(r.curTempPath, data); err != nil {
		timelapseLogger.Error("failed to write timelapse frame", "camera_id", r.cfg.CameraID, "error", err)
		return
	}
	r.frameCount++

	// Check segment duration
	if time.Since(r.segStart) >= r.segmentDur {
		r.triggerMerge()
	}
}

// triggerMerge closes the current segment and starts FFmpeg conversion in a goroutine.
// Caller must hold segMu.
func (r *TimelapseRecorder) triggerMerge() {
	if r.curTempPath == "" || r.frameCount == 0 {
		return
	}

	// Snapshot segment data
	tempPath := r.curTempPath
	finalPath := r.curFinalPath
	segStart := r.segStart
	frameCount := r.frameCount
	cameraID := r.cfg.CameraID

	// Reset segment state
	r.curTempPath = ""
	r.curFinalPath = ""
	r.frameCount = 0

	// Non-blocking merge: run FFmpeg in a goroutine
	r.mergeWg.Add(1)
	go func() {
		defer r.mergeWg.Done()
		r.mergeSegment(tempPath, finalPath, segStart, frameCount, cameraID)
	}()
}

// mergeSegment runs FFmpeg to convert JPEG directory → MP4 timelapse.
func (r *TimelapseRecorder) mergeSegment(tempPath, finalPath string, segStart time.Time, frameCount int, cameraID string) {
	defer func() {
		if panicErr := recover(); panicErr != nil {
			buf := make([]byte, 4096)
			buf = buf[:runtime.Stack(buf, false)]
			timelapseLogger.Error("PANIC recovered in mergeSegment", "camera_id", cameraID, "panic", panicErr, "stack", string(buf))
		}
	}()

	// Close segment (atomic rename)
	if err := r.store.CloseSegment(tempPath, finalPath); err != nil {
		timelapseLogger.Error("failed to close timelapse segment", "camera_id", cameraID, "error", err)
		return
	}

	outputPath := finalPath + "_timelapse.mp4"
	ffmpeg := "ffmpeg"

	// Build FFmpeg args
	args := []string{
		"-framerate", strconv.Itoa(r.outputFPS),
		"-pattern_type", "glob",
		"-i", filepath.Join(finalPath, "*.jpg"),
	}

	switch r.videoCodec {
	case "h265":
		args = append(args, "-c:v", "libx265", "-preset", "faster", "-crf", "28")
	default:
		args = append(args, "-c:v", "libx264", "-preset", "faster", "-crf", "23")
	}
	args = append(args, "-pix_fmt", "yuv420p", "-y", outputPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	timelapseLogger.Info("starting timelapse merge",
		"camera_id", cameraID,
		"segment", finalPath,
		"output", outputPath,
		"frames", frameCount,
		"fps", r.outputFPS,
		"codec", r.videoCodec,
	)

	if err := r.runFF(ctx, ffmpeg, args, outputPath); err != nil {
		timelapseLogger.Warn("timelapse FFmpeg merge failed",
			"camera_id", cameraID,
			"segment", finalPath,
			"error", err,
		)
		return
	}

	// Verify output exists
	if _, err := os.Stat(outputPath); err != nil {
		timelapseLogger.Warn("timelapse output missing after merge",
			"camera_id", cameraID,
			"output", outputPath,
		)
		return
	}

	// Register in DB with format="timelapse"
	var fileSize int64
	if info, err := os.Stat(outputPath); err == nil {
		fileSize = info.Size()
	}

	now := time.Now()
	recording := &model.Recording{
		ID:         fmt.Sprintf("tl_%d", now.UnixNano()),
		CameraID:   cameraID,
		FilePath:   outputPath,
		Format:     "timelapse",
		StartedAt:  segStart,
		EndedAt:    now,
		Duration:   now.Sub(segStart).Seconds(),
		FileSize:   fileSize,
		FrameCount: frameCount,
		Merged:     false,
	}

	if r.cfg.DB != nil {
		if err := r.cfg.DB.InsertRecordingWithRetry(context.Background(), recording, 3, 500*time.Millisecond); err != nil {
			timelapseLogger.Error("failed to insert timelapse recording", "camera_id", cameraID, "error", err)
			return
		}
	}

	r.recordSegmentCreated()

	timelapseLogger.Info("timelapse merge completed",
		"camera_id", cameraID,
		"output", outputPath,
		"size", fileSize,
		"frames", frameCount,
	)

	// Clean up original JPEG directory
	if err := os.RemoveAll(finalPath); err != nil {
		timelapseLogger.Warn("failed to cleanup timelapse JPEG dir",
			"camera_id", cameraID,
			"path", finalPath,
			"error", err,
		)
	}
}

// Start begins timelapse recording. Implements model.Recorder.
func (r *TimelapseRecorder) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == model.StatusRecording || r.status == model.StatusReconnecting {
		return fmt.Errorf("timelapse recorder for %q already running", r.cfg.CameraID)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.done = make(chan struct{})
	r.status = model.StatusRecording
	r.lastCapture.Store(0) // reset so first frame is always captured
	r.incActive()
	go r.run(ctx)
	return nil
}

// Stop stops the timelapse recorder and waits for all pending merges to complete.
// Implements model.Recorder.
func (r *TimelapseRecorder) Stop() error {
	r.mu.Lock()
	if r.cancel != nil {
		r.cancel()
	}
	r.mu.Unlock()
	if r.done != nil {
		<-r.done
	}
	// Wait for all in-flight FFmpeg merges to complete
	r.mergeWg.Wait()
	r.decActive()
	return nil
}

// Status returns the current recorder status. Implements model.Recorder.
func (r *TimelapseRecorder) Status() model.RecorderStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

func (r *TimelapseRecorder) setStatus(s model.RecorderStatus) {
	r.mu.Lock()
	r.status = s
	r.mu.Unlock()
}

// run is the main goroutine. It manages lifecycle and flushes the final segment on stop.
func (r *TimelapseRecorder) run(ctx context.Context) {
	defer close(r.done)
	defer r.setStatus(model.StatusStopped)

	// Wait for cancellation, then flush remaining segment
	<-ctx.Done()

	// Close any remaining open segment
	r.segMu.Lock()
	if r.curTempPath != "" && r.frameCount > 0 {
		r.triggerMerge()
	}
	r.segMu.Unlock()
}
