// Package timelapse provides post-processing conversion of MJPEG segments to MP4 timelapse videos.
// Phase 1: Listens for SegmentCompleted events and runs FFmpeg to convert JPEG directories → MP4.
// Phase 2: Enqueues FFmpeg jobs via TranscodeQueue for bounded worker pool execution.
package timelapse

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

var logger = slog.Default().With("component", "timelapse")

// recordingDB abstracts the InsertRecording method needed by the timelapse manager.
type recordingDB interface {
	InsertRecording(ctx context.Context, r *model.Recording) error
}

// queueAPI abstracts the transcode queue for enqueue operations.
type queueAPI interface {
	Enqueue(ctx context.Context, task *storage.TranscodeTask) error
}

// ffRunResult holds the result of an FFmpeg execution.
type ffRunResult struct {
	err    error
	output string // path to output file (if successful)
}

// ffRunner executes FFmpeg with the given arguments and returns the result.
// In production, this wraps exec.CommandContext. In tests, it can be replaced
// with a mock that creates the output file directly.
type ffRunner func(ctx context.Context, ffmpegPath string, args []string, outputPath string) ffRunResult

// defaultFFRunner is the production FFmpeg runner using exec.CommandContext.
func defaultFFRunner(ctx context.Context, ffmpegPath string, args []string, outputPath string) ffRunResult {
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	if err := cmd.Run(); err != nil {
		return ffRunResult{err: err}
	}
	return ffRunResult{output: outputPath}
}

// segmentMeta stores per-segment metadata needed for post-completion processing.
type segmentMeta struct {
	cameraID       string
	segmentPath    string
	deleteOriginal bool
	jpegCount      int
}

// Manager handles post-processing of MJPEG segments into MP4 timelapse videos.
// It subscribes to SegmentCompleted events via EventBus and runs FFmpeg conversions
// via the TranscodeQueue (preferred) or inline FFmpeg (fallback for tests without queue).
type Manager struct {
	bus        *event.EventBus
	store      *storage.Manager
	db         recordingDB
	config     map[string]*config.CameraTimelapseConfig // cameraID → config
	ffmpegPath string
	runFF      ffRunner
	queue      queueAPI // nil = use inline ffRunner (test mode)
	pendingMu  map[int64]*segmentMeta
}

// NewManager creates a new timelapse Manager. Nil bus or db results in a no-op manager.
func NewManager(bus *event.EventBus, store *storage.Manager, db recordingDB, config map[string]*config.CameraTimelapseConfig, ffmpegPath string) *Manager {
	return &Manager{
		bus:       bus,
		store:     store,
		db:        db,
		config:    config,
		ffmpegPath: ffmpegPath,
		runFF:     defaultFFRunner,
		pendingMu: make(map[int64]*segmentMeta),
	}
}

// NewManagerWithRunner creates a Manager with a custom FFmpeg runner (for testing).
func NewManagerWithRunner(bus *event.EventBus, store *storage.Manager, db recordingDB, config map[string]*config.CameraTimelapseConfig, ffmpegPath string, runner ffRunner) *Manager {
	return &Manager{
		bus:       bus,
		store:     store,
		db:        db,
		config:    config,
		ffmpegPath: ffmpegPath,
		runFF:     runner,
		pendingMu: make(map[int64]*segmentMeta),
	}
}

// SetQueue sets the transcode queue for async job processing.
// When set, timelapse jobs are enqueued instead of running FFmpeg inline.
func (m *Manager) SetQueue(q queueAPI) {
	m.queue = q
}

// OnTaskComplete is the completion callback registered with the TranscodeQueue.
// It handles DB registration and optional deletion of original JPEG directories.
// Safe to call on nil manager.
func (m *Manager) OnTaskComplete(task *storage.TranscodeTask, success bool) {
	if m == nil || m.db == nil {
		return
	}

	meta := func() *segmentMeta {
		if m.pendingMu == nil {
			return nil
		}
		return m.pendingMu[task.ID]
	}()
	if meta == nil {
		return
	}

	// Clean up pending metadata
	delete(m.pendingMu, task.ID)

	if !success {
		logger.Warn("timelapse queue task failed",
			"task_id", task.ID,
			"camera_id", meta.cameraID,
			"segment", meta.segmentPath,
		)
		// On failure, do NOT delete original
		return
	}

	// Verify output exists
	if _, err := os.Stat(task.OutputPath); err != nil {
		logger.Warn("timelapse output file missing after queue task",
			"camera_id", meta.cameraID,
			"output", task.OutputPath,
			"error", err,
		)
		return
	}

	// Register timelapse in DB
	outputSize, _ := os.Stat(task.OutputPath)
	fileSize := int64(0)
	if outputSize != nil {
		fileSize = outputSize.Size()
	}

	now := time.Now()
	recording := &model.Recording{
		ID:         fmt.Sprintf("tl_%d", time.Now().UnixNano()),
		CameraID:   meta.cameraID,
		FilePath:   task.OutputPath,
		Format:     "timelapse",
		StartedAt:  now,
		EndedAt:    now,
		FileSize:   fileSize,
		FrameCount: meta.jpegCount,
		Merged:     false,
	}

	if err := m.db.InsertRecording(context.Background(), recording); err != nil {
		logger.Warn("failed to register timelapse recording in DB",
			"camera_id", meta.cameraID,
			"output", task.OutputPath,
			"error", err,
		)
		return
	}

	logger.Info("timelapse conversion completed via queue",
		"camera_id", meta.cameraID,
		"output", task.OutputPath,
		"size", fileSize,
		"task_id", task.ID,
	)

	// Optionally delete original JPEG directory
	if meta.deleteOriginal {
		if err := os.RemoveAll(meta.segmentPath); err != nil {
			logger.Warn("failed to delete original JPEG directory after timelapse",
				"camera_id", meta.cameraID,
				"path", meta.segmentPath,
				"error", err,
			)
		} else {
			logger.Info("deleted original JPEG directory after timelapse",
				"camera_id", meta.cameraID,
				"path", meta.segmentPath,
			)
		}
	}
}

// Start subscribes to SegmentCompleted events and processes them.
// Blocks until ctx is cancelled. Nil-safe: does nothing if receiver is nil.
func (m *Manager) Start(ctx context.Context) {
	if m == nil || m.bus == nil || m.db == nil {
		return
	}

	ch := make(chan event.Event, 16)
	if err := m.bus.Subscribe(event.TopicSegmentCompleted, ch, 16); err != nil {
		logger.Warn("failed to subscribe to segment completed events", "error", err)
		return
	}

	for {
		select {
		case <-ctx.Done():
			m.bus.Unsubscribe(event.TopicSegmentCompleted, ch)
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			seg, ok := evt.Data.(event.SegmentCompleted)
			if !ok {
				continue
			}
			m.handleSegment(ctx, seg)
		}
	}
}

// handleSegment checks if a segment should be converted to timelapse.
func (m *Manager) handleSegment(ctx context.Context, seg event.SegmentCompleted) {
	// Only process MJPEG segments
	if seg.Format != "mjpeg" {
		return
	}

	// Check if camera has timelapse enabled
	cfg, ok := m.config[seg.CameraID]
	if !ok || cfg == nil || !cfg.Enabled {
		return
	}

	// Get output FPS (default 30, must be at least 1)
	fps := cfg.OutputFPS
	if fps < 1 {
		fps = 30
	}

	// Determine codec
	codec := cfg.VideoCodec
	if codec == "" {
		codec = "h264"
	}

	// Build output path: same directory as segment, with _timelapse.mp4 suffix
	outputPath := seg.FilePath + "_timelapse.mp4"

	if m.queue != nil {
		go m.enqueueSegment(ctx, seg, outputPath, fps, codec, cfg.DeleteOriginal)
	} else {
		go m.convertSegment(ctx, seg, outputPath, fps, codec, cfg.DeleteOriginal)
	}
}

// enqueueSegment enqueues a timelapse FFmpeg job via the TranscodeQueue.
// Non-blocking: enqueues and returns immediately.
func (m *Manager) enqueueSegment(ctx context.Context, seg event.SegmentCompleted, outputPath string, fps int, codec string, deleteOriginal bool) {
	// Count JPEG files for DB registration metadata
	jpegCount, err := countJPGFiles(seg.FilePath)
	if err != nil {
		logger.Warn("failed to count JPEG files in segment dir, skipping timelapse enqueue",
			"camera_id", seg.CameraID,
			"path", seg.FilePath,
			"error", err,
		)
		return
	}
	if jpegCount == 0 {
		logger.Warn("no JPEG files found in segment dir, skipping timelapse enqueue",
			"camera_id", seg.CameraID,
			"path", seg.FilePath,
		)
		return
	}

	task := &storage.TranscodeTask{
		CameraID:     seg.CameraID,
		RecordingID: seg.RecordingID,
		InputPath:   seg.FilePath,
		InputFormat: "mjpeg",
		OutputPath:  outputPath,
		OutputFormat: codec,
		Framerate:   fps,
		CreatedAt:   time.Now().UTC().Format("2006-01-02 15:04:05.999999999"),
	}

	if err := m.queue.Enqueue(ctx, task); err != nil {
		logger.Warn("failed to enqueue timelapse task",
			"camera_id", seg.CameraID,
			"segment", seg.FilePath,
			"error", err,
		)
		return
	}

	// Store segment metadata for post-completion callback
	if m.pendingMu != nil {
		m.pendingMu[task.ID] = &segmentMeta{
			cameraID:       seg.CameraID,
			segmentPath:    seg.FilePath,
			deleteOriginal: deleteOriginal,
			jpegCount:      jpegCount,
		}
	}

	logger.Info("enqueued timelapse conversion",
		"camera_id", seg.CameraID,
		"segment", seg.FilePath,
		"output", outputPath,
		"task_id", task.ID,
		"fps", fps,
		"codec", codec,
		"jpeg_count", jpegCount,
	)
}

// convertSegment runs FFmpeg inline to convert a JPEG directory to MP4.
// Used when no TranscodeQueue is available (test mode).
func (m *Manager) convertSegment(ctx context.Context, seg event.SegmentCompleted, outputPath string, fps int, codec string, deleteOriginal bool) {
	// Resolve FFmpeg path
	ffmpeg := m.ffmpegPath
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}

	// Count JPEG files for framerate normalization
	jpegCount, err := countJPGFiles(seg.FilePath)
	if err != nil {
		logger.Warn("failed to count JPEG files in segment dir, skipping timelapse",
			"camera_id", seg.CameraID,
			"path", seg.FilePath,
			"error", err,
		)
		return
	}
	if jpegCount == 0 {
		logger.Warn("no JPEG files found in segment dir, skipping timelapse",
			"camera_id", seg.CameraID,
			"path", seg.FilePath,
		)
		return
	}

	// Build FFmpeg args for MJPEG directory → MP4
	args := []string{
		"-framerate", strconv.Itoa(fps),
		"-pattern_type", "glob",
		"-i", filepath.Join(seg.FilePath, "*.jpg"),
	}

	// Encoder selection — always software for MJPEG (v4l2m2m may hang on MJPEG input)
	switch codec {
	case "h265":
		args = append(args, "-c:v", "libx265", "-preset", "faster", "-crf", "28")
	default:
		args = append(args, "-c:v", "libx264", "-preset", "faster", "-crf", "23")
	}

	args = append(args, "-pix_fmt", "yuv420p", "-y", outputPath)

	logger.Info("starting timelapse conversion",
		"camera_id", seg.CameraID,
		"segment", seg.FilePath,
		"output", outputPath,
		"fps", fps,
		"codec", codec,
		"jpeg_count", jpegCount,
	)

	result := m.runFF(ctx, ffmpeg, args, outputPath)
	if result.err != nil {
		logger.Warn("timelapse FFmpeg conversion failed",
			"camera_id", seg.CameraID,
			"segment", seg.FilePath,
			"error", result.err,
		)
		// On failure, do NOT delete original
		return
	}

	// Verify output exists
	if _, err := os.Stat(outputPath); err != nil {
		logger.Warn("timelapse output file missing after FFmpeg",
			"camera_id", seg.CameraID,
			"output", outputPath,
			"error", err,
		)
		return
	}

	// Register timelapse in DB
	outputSize, _ := os.Stat(outputPath)
	fileSize := int64(0)
	if outputSize != nil {
		fileSize = outputSize.Size()
	}

	now := time.Now()
	recording := &model.Recording{
		ID:         fmt.Sprintf("tl_%d", time.Now().UnixNano()),
		CameraID:   seg.CameraID,
		FilePath:   outputPath,
		Format:     "timelapse",
		StartedAt:  now,
		EndedAt:    now,
		FileSize:   fileSize,
		FrameCount: jpegCount,
		Merged:     false,
	}

	if err := m.db.InsertRecording(ctx, recording); err != nil {
		logger.Warn("failed to register timelapse recording in DB",
			"camera_id", seg.CameraID,
			"output", outputPath,
			"error", err,
		)
		// Don't delete — DB registration failed, keep the MP4 for manual inspection
		return
	}

	logger.Info("timelapse conversion completed",
		"camera_id", seg.CameraID,
		"output", outputPath,
		"size", fileSize,
	)

	// Optionally delete original JPEG directory
	if deleteOriginal {
		if err := os.RemoveAll(seg.FilePath); err != nil {
			logger.Warn("failed to delete original JPEG directory after timelapse",
				"camera_id", seg.CameraID,
				"path", seg.FilePath,
				"error", err,
			)
		} else {
			logger.Info("deleted original JPEG directory after timelapse",
				"camera_id", seg.CameraID,
				"path", seg.FilePath,
			)
		}
	}
}

// countJPGFiles counts .jpg files in a directory.
func countJPGFiles(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("cannot read directory: %w", err)
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(strings.ToLower(entry.Name()), ".jpg") {
			count++
		}
	}
	return count, nil
}
