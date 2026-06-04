package transcoding

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

var mgrLogger = slog.Default().With("component", "transcode-manager")

// Package-level disabled reason — set by main.go when NewTranscodeManager fails.
// Used by GetStatus() nil path and API handler to report why transcoding is disabled.
var (
	disabledReason   string
	disabledReasonMu sync.RWMutex
)


// TranscodeManager is the top-level manager for the transcoding subsystem.
// Follows the MergeManager lifecycle pattern: New → Run(ctx) → Stop.
// It is nil-safe: all methods handle nil receivers gracefully so callers
// don't need nil checks (e.g. app.transcodeMgr.Stop() is safe when disabled).
type TranscodeManager struct {
	config     ManagerConfig
	store      *storage.DB
	caps       *HardwareCapabilities
	downloader *Downloader
	queue      *TranscodeQueue
	eventBus   *event.EventBus
	m          *metrics.Metrics
	cfg        *config.Config

	mu     sync.Mutex
	cancel context.CancelFunc
}

// ManagerConfig holds the dependencies for TranscodeManager.
type ManagerConfig struct {
	Transcoding    config.TranscodingConfig
	DataDir        string
	FFmpegPath     string
	FFprobePath    string
	MaxWorkers     int
	ReplaceOriginal bool
	EventBus       *event.EventBus
	Config         *config.Config
}

// NewTranscodeManager probes hardware, validates capabilities, and creates the manager.
// Returns an error if the self-check fails (FFmpeg missing, no encoder, etc.).
// The caller should check the error and only assign the manager on success.
func NewTranscodeManager(store *storage.DB, cfg ManagerConfig, m *metrics.Metrics) (*TranscodeManager, error) {
	// 1. Probe hardware capabilities
	caps := ProbeHardwareCapabilities(cfg.FFmpegPath)

	// 2. Check sufficient for at least H.264 (most common target codec)
	if ok, reason := CheckSufficient(caps, "h264"); !ok {
		return nil, fmt.Errorf("hardware insufficient: %s", reason)
	}

	// Clamp MaxWorkers to hardware concurrency limit
	if cfg.MaxWorkers > caps.MaxConcurrentStreams {
		slog.Warn("clamping max_workers to hardware limit", "configured", cfg.MaxWorkers, "max", caps.MaxConcurrentStreams)
		cfg.MaxWorkers = caps.MaxConcurrentStreams
	}

	// 3. Create downloader for FFmpeg binary management
	dl := NewDownloader(cfg.DataDir, nil)

	// 4. Resolve FFmpeg/FFprobe paths
	ffmpegPath := cfg.FFmpegPath
	if ffmpegPath == "" {
		ffmpegPath = caps.FFmpegPath
	}
	ffprobePath := cfg.FFprobePath
	if ffprobePath == "" {
		ffprobePath = filepath.Join(filepath.Dir(ffmpegPath), "ffprobe")
	}

	// Parse job timeout from config string
	var jobTimeout time.Duration
	if cfg.Transcoding.JobTimeout != "" {
		if d, err := time.ParseDuration(cfg.Transcoding.JobTimeout); err == nil {
			jobTimeout = d
		}
	}

	// 5. Create the task queue
	queue := NewTranscodeQueue(store, caps, dl, QueueConfig{
		DataDir:         cfg.DataDir,
		MaxWorkers:      cfg.MaxWorkers,
		FFmpegPath:      ffmpegPath,
		FFprobePath:     ffprobePath,
		ReplaceOriginal: cfg.ReplaceOriginal,
		JobTimeout:      jobTimeout,
	}, m)

	return &TranscodeManager{
		config:     cfg,
		store:      store,
		caps:       caps,
		downloader: dl,
		queue:      queue,
		eventBus:   cfg.EventBus,
		m:          m,
		cfg:        cfg.Config,
	}, nil
}

// Run starts the transcoding queue workers and the FFmpeg status watcher.
// Blocks until ctx is cancelled.
func (m *TranscodeManager) Run(ctx context.Context) {
	if m == nil {
		return
	}
	m.mu.Lock()
	ctx, m.cancel = context.WithCancel(ctx)
	m.mu.Unlock()

	go m.watchFFmpegStatus(ctx)

	// Event-driven auto-enqueue: subscribe to segment completion
	if m.eventBus != nil {
		ch := make(chan event.Event, 64)
		if err := m.eventBus.Subscribe(event.TopicSegmentCompleted, ch, 0); err != nil {
			mgrLogger.Warn("failed to subscribe to segment completion events", "error", err)
		} else {
			go func() {
				defer m.eventBus.Unsubscribe(event.TopicSegmentCompleted, ch)
				for {
					select {
					case <-ctx.Done():
						return
					case evt := <-ch:
						seg, ok := evt.Data.(event.SegmentCompleted)
						if !ok {
							continue
						}
						// Skip timelapse recordings
						if seg.Format == "timelapse" {
							continue
						}
						// Check if camera has transcoding enabled
						camConfig := m.cfg.ResolveTranscodingConfig(seg.CameraID)
						if !camConfig.Enabled {
							continue
						}
						// Get target codec (default h264)
						targetCodec := camConfig.TargetCodec
						if targetCodec == "" {
							targetCodec = "h264"
						}
						// Skip if input_format equals target_codec
						if seg.Format == targetCodec {
							continue
						}
						// Build task
						outputPath := seg.FilePath + ".transcoded.mp4"
						now := time.Now().UTC().Format("2006-01-02 15:04:05.999999999")
						task := &storage.TranscodeTask{
							CameraID:        seg.CameraID,
							RecordingID:     seg.RecordingID,
							InputPath:       seg.FilePath,
							InputFormat:     seg.Format,
							OutputPath:      outputPath,
							OutputFormat:    targetCodec,
							OriginalDeleted: true,
							Framerate:       0,
							CreatedAt:       now,
						}
						// Enqueue
						if err := m.queue.Enqueue(ctx, task); err != nil {
							mgrLogger.Warn("failed to auto-enqueue transcode task", "error", err, "recording_id", seg.RecordingID)
						}
					}
				}
			}()
		}
	}

	if err := m.queue.Run(ctx); err != nil {
		mgrLogger.Error("transcode queue stopped with error", "error", err)
	}
}

// Stop cancels the manager context and gracefully drains the queue.
func (m *TranscodeManager) Stop() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Unlock()
	m.queue.Stop()
	mgrLogger.Info("transcode manager stopped")
}

// EnqueueRecording creates a transcode task for a completed recording segment.
// The call is non-blocking — the task is inserted into the database queue
// and will be picked up by a worker goroutine.
func (m *TranscodeManager) EnqueueRecording(cameraID, recordingID, inputPath, inputFormat string, targetCodec string) error {
	if m == nil {
		return nil
	}

	// Build output path next to the input
	ext := ".mp4"
	outputPath := inputPath + ".transcoded" + ext

	now := time.Now().UTC().Format("2006-01-02 15:04:05.999999999")
	task := &storage.TranscodeTask{
		CameraID:     cameraID,
		RecordingID:  recordingID,
		InputPath:    inputPath,
		InputFormat:  inputFormat,
		OutputPath:   outputPath,
		OutputFormat: targetCodec,
		CreatedAt:    now,
	}

	if err := m.queue.Enqueue(context.Background(), task); err != nil {
		return fmt.Errorf("enqueue transcode task: %w", err)
	}

	mgrLogger.Info("enqueued transcode task",
		"camera_id", cameraID,
		"recording_id", recordingID,
		"input_format", inputFormat,
		"output_format", targetCodec,
	)
	return nil
}

// GetStatus returns the current manager and queue status.
func (m *TranscodeManager) GetStatus() ManagerStatus {
	if m == nil {
		disabledReasonMu.RLock()
		reason := disabledReason
		disabledReasonMu.RUnlock()
		return ManagerStatus{Enabled: false, DisabledReason: reason}
	}
	return m.queue.GetStatus()
}

// HardwareInfo returns the probed hardware capabilities.
// Returns nil if the manager is nil (transcoding disabled).
func (m *TranscodeManager) HardwareInfo() *HardwareCapabilities {
	if m == nil {
		return nil
	}
	return m.caps
}

// Downloader returns the FFmpeg downloader for API access.
// Returns nil if the manager is nil (transcoding disabled).
func (m *TranscodeManager) Downloader() *Downloader {
	if m == nil {
		return nil
	}
	return m.downloader
}

// Queue returns the transcode queue for API access (cancel, status, etc.).
// Returns nil if the manager is nil (transcoding disabled).
func (m *TranscodeManager) Queue() QueueAPI {
	if m == nil {
		return nil
	}
	return m.queue
}

// SetDisabledReason stores the reason why transcoding was disabled.
// Thread-safe. Called from main.go when NewTranscodeManager fails.
func SetDisabledReason(reason string) {
	disabledReasonMu.Lock()
	disabledReason = reason
	disabledReasonMu.Unlock()
}

// GetDisabledReason returns the stored disabled reason.
// Returns empty string if transcoding was never disabled or reason was cleared.
func GetDisabledReason() string {
	disabledReasonMu.RLock()
	defer disabledReasonMu.RUnlock()
	return disabledReason
}

// ffmpegStatusCheckInterval controls how often the FFmpeg download status is polled.
const ffmpegStatusCheckInterval = 30 * time.Second

// statusGauge maps DownloadStatus.Status to Prometheus gauge values.
var statusGauge = map[string]float64{
	"not_installed": 0,
	"downloading":   1,
	"available":     2,
	"failed":        3,
}

// updateFFmpegStatus reads the current FFmpeg status from the downloader
// and updates the TranscodingFFmpegStatus Prometheus gauge.
func (m *TranscodeManager) updateFFmpegStatus() {
	if m == nil || m.downloader == nil || m.m == nil {
		return
	}
	status := m.downloader.GetFFmpegStatus()
	val, ok := statusGauge[status.Status]
	if !ok {
		val = 0 // default to not_installed for unknown statuses
	}
	m.m.TranscodingFFmpegStatus.Set(val)
}

// watchFFmpegStatus periodically updates the FFmpeg status gauge.
// It does an immediate update on start, then every 30s.
func (m *TranscodeManager) watchFFmpegStatus(ctx context.Context) {
	if m == nil {
		return
	}
	// Immediate first update on start
	m.updateFFmpegStatus()

	ticker := time.NewTicker(ffmpegStatusCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.updateFFmpegStatus()
		}
	}
}
