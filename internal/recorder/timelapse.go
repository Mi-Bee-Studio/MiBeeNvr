package recorder

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

var timelapseLogger = slog.Default().With("component", "timelapse-recorder")

// TimelapseRecorderConfig holds configuration for the timelapse recorder.
type TimelapseRecorderConfig struct {
	CameraID   string
	Interval   time.Duration // frame capture interval (e.g., 5s)
	SegmentDur time.Duration  // segment duration
	URL        string         // HTTP MJPEG stream URL
	Username   string         // for basic auth (optional)
	Password   string         // for basic auth (optional)
	DataDir    string         // base data directory
	DB         RecordingDB
	Metrics    *metrics.Metrics
	MergeMgr *timelapse.RollingMergeManager // optional rolling merge manager
}

// TimelapseRecorder captures JPEG frames at a configurable interval from an
// HTTP MJPEG stream and stores them as zero-padded JPEG sequences in
// timestamped segment directories. Implements model.Recorder.
type TimelapseRecorder struct {
	cfg     TimelapseRecorderConfig
	store   SegmentStore
	metrics *metrics.Metrics
	mergeMgr *timelapse.RollingMergeManager
	client  *http.Client

	mu     sync.Mutex
	status model.RecorderStatus
	cancel context.CancelFunc
	cancelStream context.CancelFunc
	done         chan struct{}
	watchdogDone chan struct{}

	lastFrameTime atomic.Int64
	lastCapture   atomic.Int64 // UnixNano of last frame capture

	curTempPath  string
	curFinalPath string
	segStart     time.Time
	frameCount   int

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

// recordBytes adds to the recording bytes counter if metrics is available.
func (r *TimelapseRecorder) recordBytes(bytes int64) {
	if r.metrics != nil {
		r.metrics.RecordingBytesTotal.WithLabelValues(r.cfg.CameraID, "timelapse").Add(float64(bytes))
	}
}

// recordError increments the camera errors counter if metrics is available.
func (r *TimelapseRecorder) recordError(errorType string) {
	if r.metrics != nil {
		r.metrics.CameraErrors.WithLabelValues(r.cfg.CameraID, errorType).Inc()
	}
}

var _ model.Recorder = (*TimelapseRecorder)(nil)

// NewTimelapseRecorder creates a new TimelapseRecorder.
func NewTimelapseRecorder(cfg TimelapseRecorderConfig, store SegmentStore, opts ...*metrics.Metrics) *TimelapseRecorder {
	var m *metrics.Metrics
	if len(opts) > 0 {
		m = opts[0]
	}
	if cfg.Interval < time.Millisecond {
		cfg.Interval = 5 * time.Second
	}
	if cfg.SegmentDur < time.Millisecond {
		cfg.SegmentDur = DefaultSegmentDur
	}
	return &TimelapseRecorder{
		cfg:     cfg,
		store:   store,
		metrics: m,
		mergeMgr: cfg.MergeMgr,
		client: &http.Client{
			Timeout: 0, // no timeout — stream is long-lived
			Transport: &http.Transport{
				DisableKeepAlives: true,
			},
		},
		status: model.StatusStopped,
	}
}

func (r *TimelapseRecorder) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == model.StatusRecording || r.status == model.StatusReconnecting {
		return fmt.Errorf("timelapse recorder for %q already running", r.cfg.CameraID)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.done = make(chan struct{})
	r.watchdogDone = make(chan struct{})
	r.status = model.StatusRecording
	r.lastCapture.Store(0) // reset so first frame is always captured
	r.incActive()
	go r.run(ctx)
	go r.idleWatchdog(ctx)
	return nil
}

func (r *TimelapseRecorder) Stop() error {
	r.mu.Lock()
	if r.cancel != nil {
		r.cancel()
	}
	r.mu.Unlock()
	if r.done != nil {
		<-r.done
	}
	if r.watchdogDone != nil {
		<-r.watchdogDone
	}
	r.decActive()
	return nil
}

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

func (r *TimelapseRecorder) run(ctx context.Context) {
	defer close(r.done)
	defer r.setStatus(model.StatusStopped)
	defer r.closeCurrentSegment()

	var retryCount int
	for {
		streamCtx, streamCancel := context.WithCancel(ctx)
		r.mu.Lock()
		r.cancelStream = streamCancel
		r.mu.Unlock()
		err, connected := r.connectAndStream(streamCtx)
		r.mu.Lock()
		r.cancelStream = nil
		r.mu.Unlock()
		streamCancel()

		if ctx.Err() != nil {
			return
		}
		if connected {
			retryCount = 0
		}
		retryCount++
		backoff := TieredBackoffWithJitter(retryCount)
		timelapseLogger.Error("stream error, reconnecting", "camera_id", r.cfg.CameraID, "error", err, "backoff", backoff, "attempt", retryCount)
		r.recordError("connection")
		r.setStatus(model.StatusReconnecting)

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

func (r *TimelapseRecorder) idleWatchdog(ctx context.Context) {
	defer close(r.watchdogDone)
	const idleTimeout = 60 // seconds
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			lastFrame := r.lastFrameTime.Load()
			if lastFrame > 0 && time.Now().Unix()-lastFrame > idleTimeout {
				timelapseLogger.Warn("no frames received, triggering reconnect",
					"camera_id", r.cfg.CameraID,
					"idle_seconds", time.Now().Unix()-lastFrame)
				r.recordError("idle_timeout")
				r.setStatus(model.StatusReconnecting)
				r.mu.Lock()
				if r.cancelStream != nil {
					r.cancelStream()
				}
				r.mu.Unlock()
				return
			}
		}
	}
}

// connectAndStream opens an HTTP connection to the MJPEG stream and samples
// frames at the configured interval.
func (r *TimelapseRecorder) connectAndStream(ctx context.Context) (error, bool) {
	defer func() {
		if panicErr := recover(); panicErr != nil {
			buf := make([]byte, 4096)
			buf = buf[:runtime.Stack(buf, false)]
			timelapseLogger.Error("PANIC recovered in connectAndStream", "camera_id", r.cfg.CameraID, "panic", panicErr, "stack", string(buf))
		}
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.cfg.URL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err), false
	}
	if r.cfg.Username != "" {
		req.SetBasicAuth(r.cfg.Username, r.cfg.Password)
	}

	timelapseLogger.Info("connecting to MJPEG stream", "camera_id", r.cfg.CameraID, "url", r.cfg.URL)
	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("http connect: %w", err), false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http status %d", resp.StatusCode), false
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "multipart/x-mixed-replace") {
		return fmt.Errorf("unexpected content-type %q, expected multipart/x-mixed-replace", ct), false
	}
	boundary := timelapseExtractBoundary(ct)

	r.setStatus(model.StatusRecording)
	reader := bufio.NewReader(resp.Body)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err(), true
		default:
		}

		// Read until boundary marker
		if err := r.skipToBoundary(reader, boundary); err != nil {
			return fmt.Errorf("read boundary: %w", err), true
		}

		// Read part headers to get Content-Length
		contentLength, err := r.readPartHeaders(reader)
		if err != nil {
			return fmt.Errorf("read part headers: %w", err), true
		}

		// Read JPEG body
		var data []byte
		if contentLength > 0 {
			data = make([]byte, contentLength)
			if _, err := io.ReadFull(reader, data); err != nil {
				return fmt.Errorf("read jpeg body: %w", err), true
			}
		} else {
			// Content-Length missing: read until next boundary
			var buf bytes.Buffer
			boundaryMarker := []byte("--" + boundary)
			if data, err = readUntilBoundary(reader, &buf, boundaryMarker); err != nil {
				return fmt.Errorf("read jpeg body (no content-length): %w", err), true
			}
		}

		// Validate JPEG magic bytes
		if len(data) < 2 || data[0] != 0xFF || data[1] != 0xD8 {
			timelapseLogger.Warn("skipping invalid frame (missing JPEG magic)", "camera_id", r.cfg.CameraID, "size", len(data))
			continue
		}

		// Interval-based frame sampling using CAS pattern
		now := time.Now().UnixNano()
		last := r.lastCapture.Load()
		if now-last < r.cfg.Interval.Nanoseconds() {
			continue // too soon, skip
		}
		if !r.lastCapture.CompareAndSwap(last, now) {
			continue // another goroutine captured first
		}

		// Create segment if needed
		if r.curTempPath == "" {
			tempPath, finalPath, err := r.store.CreateSegment(r.cfg.CameraID, "timelapse")
			if err != nil {
				return fmt.Errorf("create segment: %w", err), true
			}
			r.curTempPath = tempPath
			r.curFinalPath = finalPath
			r.segStart = time.Now()
			r.frameCount = 0
		}

		// Write frame with zero-padded name
		r.frameCount++
		frameName := fmt.Sprintf("frame_%06d.jpg", r.frameCount)
		jpgPath := filepath.Join(r.curTempPath, frameName)
		if err := os.WriteFile(jpgPath, data, 0644); err != nil {
			timelapseLogger.Error("failed to write timelapse frame", "camera_id", r.cfg.CameraID, "error", err)
			r.frameCount--
			continue
		}
		r.recordBytes(int64(len(data)))
		r.lastFrameTime.Store(time.Now().Unix())

		// Check if segment duration elapsed
		if time.Since(r.segStart) >= r.cfg.SegmentDur {
			r.closeCurrentSegment()
		}
	}
}

func (r *TimelapseRecorder) closeCurrentSegment() {
	if r.curTempPath == "" {
		return
	}
	if err := r.store.CloseSegment(r.curTempPath, r.curFinalPath); err != nil {
		timelapseLogger.Error("failed to close segment", "camera_id", r.cfg.CameraID, "error", err)
	}

	// Insert recording entry into database
	var recordingID string
	var totalSize int64
	if r.cfg.DB != nil && r.curFinalPath != "" && r.frameCount > 0 {
		now := time.Now()
		recordingID = fmt.Sprintf("%d", now.UnixNano())
		duration := now.Sub(r.segStart).Seconds()
		rec := &model.Recording{
			ID:         recordingID,
			CameraID:   r.cfg.CameraID,
			FilePath:   r.curFinalPath,
			Format:     model.FormatTimelapse,
			StartedAt:  r.segStart,
			EndedAt:    now,
			Duration:   duration,
			FrameCount: r.frameCount,
		}
		// Calculate directory size by walking files
		filepath.Walk(r.curFinalPath, func(path string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				totalSize += info.Size()
			}
			return nil
		})
		rec.FileSize = totalSize
		if err := r.cfg.DB.InsertRecordingWithRetry(context.Background(), rec, 3, 500*time.Millisecond); err != nil {
			timelapseLogger.Error("failed to insert timelapse recording", "camera_id", r.cfg.CameraID, "error", err)
		}
	}

	if r.frameCount > 0 {
		r.recordSegmentCreated()
	}

	// Trigger async rolling merge if merge manager is configured.
	if r.mergeMgr != nil && r.curFinalPath != "" && r.frameCount > 0 {
		r.mergeMgr.StartSegmentMerge(context.Background(), r.cfg.CameraID, r.curFinalPath, r.curFinalPath+".mp4", recordingID)
	}

	r.curTempPath = ""
	r.curFinalPath = ""
	r.frameCount = 0
}

// timelapseExtractBoundary parses the boundary string from a Content-Type header.
// Example: "multipart/x-mixed-replace;boundary=123456789000000000000987654321"
func timelapseExtractBoundary(ct string) string {
	idx := strings.Index(ct, "boundary=")
	if idx == -1 {
		return "frame"
	}
	val := ct[idx+len("boundary="):]
	val = strings.Trim(val, `"`)
	if i := strings.IndexAny(val, "; "); i != -1 {
		val = val[:i]
	}
	return val
}

// skipToBoundary reads from the reader until it finds "--<boundary>\r\n".
func (r *TimelapseRecorder) skipToBoundary(reader *bufio.Reader, boundary string) error {
	marker := []byte("--" + boundary)
	for {
		line, err := reader.ReadSlice('\n')
		if err != nil {
			return err
		}
		line = bytes.TrimRight(line, "\r\n")
		if bytes.Equal(line, marker) {
			return nil
		}
	}
}

// readPartHeaders reads MIME part headers until an empty line.
// Returns the Content-Length value, or 0 if not found.
func (r *TimelapseRecorder) readPartHeaders(reader *bufio.Reader) (int, error) {
	contentLength := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return 0, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return contentLength, nil
		}
		if contentLength == 0 && strings.HasPrefix(strings.ToLower(line), "content-length:") {
			val := strings.TrimSpace(line[len("content-length:"):])
			n, err := strconv.Atoi(val)
			if err != nil {
				return 0, fmt.Errorf("invalid content-length %q: %w", val, err)
			}
			contentLength = n
		}
	}
}
