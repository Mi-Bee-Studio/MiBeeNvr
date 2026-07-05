package timelapse

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

var snapshotCapturerLogger = slog.Default().With("component", "snapshot-capturer")

// SnapshotCapturerConfig holds configuration for the HTTP snapshot capturer.
type SnapshotCapturerConfig struct {
	CameraID    string
	SnapshotURL string // HTTP URL for snapshot; if empty, DeriveSnapshotURL is used
	Interval    time.Duration
	SegmentDur  time.Duration
	Username    string // optional basic auth username
	Password    string // optional basic auth password
	DB          RecordingDB
	Store       SegmentStore
	Metrics     *metrics.Metrics
	MergeMgr    *RollingMergeManager // optional rolling merge manager
	Protocol    string               // camera protocol for DeriveSnapshotURL fallback
	StreamURL   string               // camera stream URL for DeriveSnapshotURL fallback
}

// SnapshotCapturer captures JPEG snapshots from an HTTP URL at a configurable
// interval and writes them as frame sequences in segment directories.
// Implements model.Recorder.
type SnapshotCapturer struct {
	cfg      SnapshotCapturerConfig
	store    SegmentStore
	metrics  *metrics.Metrics
	mergeMgr *RollingMergeManager
	client   *http.Client

	mu     sync.Mutex
	status model.RecorderStatus
	cancel context.CancelFunc
	done   chan struct{}

	curTempPath  string
	curFinalPath string
	segStart     time.Time
	frameCount   int
}

var _ model.Recorder = (*SnapshotCapturer)(nil)

// NewSnapshotCapturer creates a new SnapshotCapturer.
func NewSnapshotCapturer(cfg SnapshotCapturerConfig, store SegmentStore, opts ...*metrics.Metrics) *SnapshotCapturer {
	var m *metrics.Metrics
	if len(opts) > 0 {
		m = opts[0]
	}
	if cfg.Interval < time.Millisecond {
		cfg.Interval = 5 * time.Second
	}
	if cfg.SegmentDur < time.Millisecond {
		cfg.SegmentDur = 10 * time.Minute
	}
	// Auto-derive snapshot URL if empty
	if cfg.SnapshotURL == "" && cfg.StreamURL != "" {
		if derived := DeriveSnapshotURL(cfg.StreamURL, cfg.Protocol); derived != "" {
			cfg.SnapshotURL = derived
			snapshotCapturerLogger.Info("auto-derived snapshot URL",
				"camera_id", cfg.CameraID,
				"url", derived)
		}
	}
	return &SnapshotCapturer{
		cfg:      cfg,
		store:    store,
		metrics:  m,
		mergeMgr: cfg.MergeMgr,
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				DisableKeepAlives: true,
			},
		},
		status: model.StatusStopped,
	}
}

// Start begins the snapshot capture loop.
func (r *SnapshotCapturer) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == model.StatusRecording {
		return fmt.Errorf("snapshot capturer for %q already running", r.cfg.CameraID)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.done = make(chan struct{})
	r.status = model.StatusRecording
	r.incActive()
	go r.run(ctx)
	return nil
}

// Stop stops the snapshot capture loop and closes the current segment.
func (r *SnapshotCapturer) Stop() error {
	r.mu.Lock()
	if r.cancel != nil {
		r.cancel()
	}
	r.mu.Unlock()
	if r.done != nil {
		<-r.done
	}
	r.decActive()
	return nil
}

// Status returns the current recorder status.
func (r *SnapshotCapturer) Status() model.RecorderStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

func (r *SnapshotCapturer) setStatus(s model.RecorderStatus) {
	r.mu.Lock()
	r.status = s
	r.mu.Unlock()
}

func (r *SnapshotCapturer) incActive() {
	if r.metrics != nil {
		r.metrics.ActiveRecordings.Inc()
	}
}

func (r *SnapshotCapturer) decActive() {
	if r.metrics != nil {
		r.metrics.ActiveRecordings.Dec()
	}
}

func (r *SnapshotCapturer) recordSegmentCreated() {
	if r.metrics != nil {
		r.metrics.SegmentsCreated.WithLabelValues(r.cfg.CameraID, "timelapse").Inc()
	}
}

func (r *SnapshotCapturer) recordBytes(bytes int64) {
	if r.metrics != nil {
		r.metrics.RecordingBytesTotal.WithLabelValues(r.cfg.CameraID, "timelapse").Add(float64(bytes))
	}
}

func (r *SnapshotCapturer) recordError(errorType string) {
	if r.metrics != nil {
		r.metrics.CameraErrors.WithLabelValues(r.cfg.CameraID, errorType).Inc()
	}
}

// run is the main capture loop. It fires a timer at the configured interval
// and captures a snapshot on each tick.
func (r *SnapshotCapturer) run(ctx context.Context) {
	defer close(r.done)
	defer r.setStatus(model.StatusStopped)
	defer r.closeCurrentSegment()

	// Validate that we have a URL to work with
	if r.cfg.SnapshotURL == "" {
		snapshotCapturerLogger.Error("no snapshot URL configured and none could be derived",
			"camera_id", r.cfg.CameraID)
		return
	}

	ticker := time.NewTicker(r.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.captureFrame(ctx)
		}
	}
}

// captureFrame fetches a single snapshot and writes it to the current segment.
func (r *SnapshotCapturer) captureFrame(ctx context.Context) {
	data, err := r.fetchSnapshot(ctx)
	if err != nil {
		snapshotCapturerLogger.Warn("failed to fetch snapshot, skipping frame",
			"camera_id", r.cfg.CameraID,
			"url", r.cfg.SnapshotURL,
			"error", err)
		r.recordError("snapshot_fetch")
		return
	}
	if data == nil {
		// nil data means skip (e.g., 404 handled gracefully)
		return
	}

	// Create segment if needed
	r.mu.Lock()
	if r.curTempPath == "" {
		tempPath, finalPath, err := r.store.CreateSegment(r.cfg.CameraID, "timelapse")
		if err != nil {
			r.mu.Unlock()
			snapshotCapturerLogger.Error("failed to create segment",
				"camera_id", r.cfg.CameraID, "error", err)
			r.recordError("segment_create")
			return
		}
		r.curTempPath = tempPath
		r.curFinalPath = finalPath
		r.segStart = time.Now()
		r.frameCount = 0
	}
	r.mu.Unlock()

	// Write frame as sequential JPEG file
	r.mu.Lock()
	r.frameCount++
	frameCount := r.frameCount
	curTempPath := r.curTempPath
	r.mu.Unlock()

	frameName := fmt.Sprintf("frame_%06d.jpg", frameCount)
	jpgPath := filepath.Join(curTempPath, frameName)
	if err := os.WriteFile(jpgPath, data, 0o644); err != nil {
		snapshotCapturerLogger.Error("failed to write snapshot frame",
			"camera_id", r.cfg.CameraID, "error", err)
		r.mu.Lock()
		r.frameCount--
		r.mu.Unlock()
		return
	}
	r.recordBytes(int64(len(data)))

	// Check if segment duration elapsed
	r.mu.Lock()
	shouldRotate := time.Since(r.segStart) >= r.cfg.SegmentDur
	r.mu.Unlock()
	if shouldRotate {
		r.closeCurrentSegment()
	}
}

// fetchSnapshot performs an HTTP GET to the snapshot URL with retry logic.
// Returns nil, nil on 404 (skip silently).
// Retries up to 3 times with exponential backoff on transient errors.
func (r *SnapshotCapturer) fetchSnapshot(ctx context.Context) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 200ms, 400ms, 800ms
			backoff := time.Duration(200<<uint(attempt-1)) * time.Millisecond
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.cfg.SnapshotURL, nil)
		if err != nil {
			lastErr = fmt.Errorf("create request: %w", err)
			continue
		}
		if r.cfg.Username != "" {
			req.SetBasicAuth(r.cfg.Username, r.cfg.Password)
		}

		resp, err := r.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("http request: %w", err)
			continue
		}

		// 404 is non-fatal — skip this frame
		if resp.StatusCode == http.StatusNotFound {
			resp.Body.Close()
			snapshotCapturerLogger.Debug("snapshot returned 404, skipping frame",
				"camera_id", r.cfg.CameraID,
				"url", r.cfg.SnapshotURL)
			return nil, nil
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("http status %d", resp.StatusCode)
			continue
		}

		// Validate Content-Type
		ct := resp.Header.Get("Content-Type")
		ct = strings.ToLower(ct)
		isJPEG := strings.HasPrefix(ct, "image/jpeg")
		isPNG := strings.HasPrefix(ct, "image/png")
		if !isJPEG && !isPNG {
			resp.Body.Close()
			lastErr = fmt.Errorf("unexpected content-type %q, expected image/jpeg or image/png", ct)
			continue
		}

		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("read body: %w", err)
			continue
		}

		if len(data) == 0 {
			lastErr = fmt.Errorf("empty response body")
			continue
		}

		// Validate JPEG magic bytes (0xFF 0xD8)
		if isJPEG && (len(data) < 2 || data[0] != 0xFF || data[1] != 0xD8) {
			snapshotCapturerLogger.Warn("snapshot has image/jpeg content-type but invalid JPEG magic bytes",
				"camera_id", r.cfg.CameraID,
				"size", len(data),
				"first_bytes", fmt.Sprintf("%02x %02x", data[0], data[1]))
			// Still write it — the content-type header is our primary gate
		}

		return data, nil
	}

	return nil, lastErr
}

// closeCurrentSegment finalizes the current segment and creates a recording entry.
func (r *SnapshotCapturer) closeCurrentSegment() {
	r.mu.Lock()
	tempPath := r.curTempPath
	finalPath := r.curFinalPath
	frameCount := r.frameCount
	segStart := r.segStart
	r.curTempPath = ""
	r.curFinalPath = ""
	r.frameCount = 0
	r.mu.Unlock()

	if tempPath == "" {
		return
	}

	if err := r.store.CloseSegment(tempPath, finalPath); err != nil {
		snapshotCapturerLogger.Error("failed to close segment",
			"camera_id", r.cfg.CameraID, "error", err)
	}

	// Insert recording entry into database
	var recordingID string
	var totalSize int64
	if r.cfg.DB != nil && finalPath != "" && frameCount > 0 {
		now := time.Now()
		duration := now.Sub(segStart).Seconds()
		recordingID = fmt.Sprintf("%d", now.UnixNano())
		rec := &model.Recording{
			ID:         recordingID,
			CameraID:   r.cfg.CameraID,
			FilePath:   finalPath,
			Format:     model.FormatTimelapse,
			StartedAt:  segStart,
			EndedAt:    now,
			Duration:   duration,
			FrameCount: frameCount,
		}
		// Calculate directory size by walking files
		filepath.Walk(finalPath, func(path string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				totalSize += info.Size()
			}
			return nil
		})
		rec.FileSize = totalSize
		if err := r.cfg.DB.InsertRecordingWithRetry(context.Background(), rec, 3, 500*time.Millisecond); err != nil {
			snapshotCapturerLogger.Error("failed to insert recording",
				"camera_id", r.cfg.CameraID, "error", err)
		}
	}

	if frameCount > 0 {
		r.recordSegmentCreated()
	}

	// Trigger async rolling merge if merge manager is configured
	if r.mergeMgr != nil && finalPath != "" && frameCount > 0 {
		r.mergeMgr.StartSegmentMerge(context.Background(), r.cfg.CameraID, finalPath, finalPath+".mp4", recordingID)
	}
}
