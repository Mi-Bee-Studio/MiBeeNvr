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
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
)

var httpJpegLogger = slog.Default().With("component", "http-jpeg-recorder")

// HTTPJPEGConfig holds configuration for the HTTP JPEG recorder.
type HTTPJPEGConfig struct {
	CameraID   string
	URL        string
	SegmentDur time.Duration
	Username   string // for basic auth (optional)
	Password   string // for basic auth (optional)
	DB         RecordingDB
	MaxBackoff time.Duration   // Deprecated: no longer used, tiered backoff is used instead
	InitBackoff time.Duration  // Deprecated: no longer used, tiered backoff is used instead
	EventBus    *event.EventBus
}

// HTTPJPEGRecorder captures JPEG frames from a continuous MJPEG stream over HTTP.
type HTTPJPEGRecorder struct {
	cfg     HTTPJPEGConfig
	store   SegmentStore
	metrics *metrics.Metrics
	client  *http.Client

	mu     sync.Mutex
	status model.RecorderStatus
	cancel context.CancelFunc
	cancelStream context.CancelFunc
	done         chan struct{}
	watchdogDone chan struct{}

	lastFrameTime atomic.Int64 // Unix timestamp of last received frame
	curTempPath  string
	curFinalPath string
	segStart     time.Time
	frameCount   int
	Hub *model.StreamHub // Frame fan-out (nil for HTTP-JPEG — no HLS support, reserved for future consumers)
}

// GetHub returns the StreamHub for frame fan-out.
func (r *HTTPJPEGRecorder) GetHub() *model.StreamHub { return r.Hub }

// incActive increments the active recordings gauge if metrics is available.
func (r *HTTPJPEGRecorder) incActive() {
	if r.metrics != nil {
		r.metrics.ActiveRecordings.Inc()
	}
}

// decActive decrements the active recordings gauge if metrics is available.
func (r *HTTPJPEGRecorder) decActive() {
	if r.metrics != nil {
		r.metrics.ActiveRecordings.Dec()
	}
}

// recordSegmentCreated increments the segments created counter if metrics is available.
func (r *HTTPJPEGRecorder) recordSegmentCreated() {
	if r.metrics != nil {
		r.metrics.SegmentsCreated.WithLabelValues(r.cfg.CameraID, "http_jpeg").Inc()
	}
}

// recordBytes adds to the recording bytes counter if metrics is available.
func (r *HTTPJPEGRecorder) recordBytes(bytes int64) {
	if r.metrics != nil {
		r.metrics.RecordingBytesTotal.WithLabelValues(r.cfg.CameraID, "http_jpeg").Add(float64(bytes))
	}
}

// recordError increments the camera errors counter if metrics is available.
func (r *HTTPJPEGRecorder) recordError(errorType string) {
	if r.metrics != nil {
		r.metrics.CameraErrors.WithLabelValues(r.cfg.CameraID, errorType).Inc()
	}
}

var _ model.Recorder = (*HTTPJPEGRecorder)(nil)

func NewHTTPJPEGRecorder(cfg HTTPJPEGConfig, store SegmentStore, opts ...*metrics.Metrics) *HTTPJPEGRecorder {
	var m *metrics.Metrics
	if len(opts) > 0 {
		m = opts[0]
	}
	if cfg.SegmentDur == 0 {
		cfg.SegmentDur = DefaultSegmentDur
	}
	if cfg.MaxBackoff == 0 {
		cfg.MaxBackoff = DefaultMaxBackoff
	}
	if cfg.InitBackoff == 0 {
		cfg.InitBackoff = DefaultInitBackoff
	}
	return &HTTPJPEGRecorder{
		cfg:     cfg,
		store:   store,
		metrics: m,
		client: &http.Client{
			Timeout: 0, // no timeout — stream is long-lived
			Transport: &http.Transport{
				DisableKeepAlives: true,
			},
		},
		status: model.StatusStopped,
	}
}

func (r *HTTPJPEGRecorder) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == model.StatusRecording || r.status == model.StatusReconnecting {
		return fmt.Errorf("recorder for %q already running", r.cfg.CameraID)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.done = make(chan struct{})
	r.watchdogDone = make(chan struct{})
	r.status = model.StatusRecording
	r.incActive()
	go r.run(ctx)
	go r.idleWatchdog(ctx)
	return nil
}

func (r *HTTPJPEGRecorder) Stop() error {
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

func (r *HTTPJPEGRecorder) Status() model.RecorderStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

func (r *HTTPJPEGRecorder) setStatus(s model.RecorderStatus) {
	r.mu.Lock()
	r.status = s
	r.mu.Unlock()
}

func (r *HTTPJPEGRecorder) run(ctx context.Context) {
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
		httpJpegLogger.Error("stream error, reconnecting", "camera_id", r.cfg.CameraID, "error", err, "backoff", backoff, "attempt", retryCount)
		r.recordError("connection")
		r.setStatus(model.StatusReconnecting)

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

func (r *HTTPJPEGRecorder) idleWatchdog(ctx context.Context) {
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
				httpJpegLogger.Warn("no frames received, triggering reconnect",
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

// connectAndStream opens an HTTP connection to the MJPEG stream and parses frames.
func (r *HTTPJPEGRecorder) connectAndStream(ctx context.Context) (error, bool) {
	defer func() {
		if panicErr := recover(); panicErr != nil {
			buf := make([]byte, 4096)
			buf = buf[:runtime.Stack(buf, false)]
			httpJpegLogger.Error("PANIC recovered in connectAndStream", "camera_id", r.cfg.CameraID, "panic", panicErr, "stack", string(buf))
		}
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.cfg.URL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err), false
	}
	if r.cfg.Username != "" {
		req.SetBasicAuth(r.cfg.Username, r.cfg.Password)
	}

	httpJpegLogger.Info("connecting to MJPEG stream", "camera_id", r.cfg.CameraID, "url", r.cfg.URL)
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
	boundary := extractBoundary(ct)

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
			httpJpegLogger.Warn("skipping invalid frame (missing JPEG magic)", "camera_id", r.cfg.CameraID, "size", len(data))
			continue
		}

		// Create segment if needed
		if r.curTempPath == "" {
			tempPath, finalPath, err := r.store.CreateSegment(r.cfg.CameraID, string(model.FormatMJPEG))
			if err != nil {
				return fmt.Errorf("create segment: %w", err), true
			}
			r.curTempPath = tempPath
			r.curFinalPath = finalPath
			r.segStart = time.Now()
			r.frameCount = 0
		}

		n, err := r.store.WriteFrame(r.curTempPath, data)
		if err != nil {
			return fmt.Errorf("write frame: %w", err), true
		}
		r.frameCount++
		r.recordBytes(int64(n))
		r.lastFrameTime.Store(time.Now().Unix())

		// Check if segment duration elapsed
		if time.Since(r.segStart) >= r.cfg.SegmentDur {
			r.closeCurrentSegment()
		}
	}
}

func (r *HTTPJPEGRecorder) closeCurrentSegment() {
	if r.curTempPath == "" {
		return
	}
	if err := r.store.CloseSegment(r.curTempPath, r.curFinalPath); err != nil {
		httpJpegLogger.Error("failed to close segment", "camera_id", r.cfg.CameraID, "error", err)
	}

	// Insert recording entry into database
	var totalSize int64
	var recordingID string
	if r.cfg.DB != nil && r.curFinalPath != "" && r.frameCount > 0 {
		now := time.Now()
		duration := now.Sub(r.segStart).Seconds()
		rec := &model.Recording{
			ID:         fmt.Sprintf("%d", now.UnixNano()),
			CameraID:   r.cfg.CameraID,
			FilePath:   r.curFinalPath,
			Format:     model.FormatMJPEG,
			StartedAt:  r.segStart,
			EndedAt:    now,
			Duration:   duration,
			FrameCount: r.frameCount,
		}
		recordingID = rec.ID
		// Get file size from disk (MJPEG segments are directories)
		filepath.Walk(r.curFinalPath, func(path string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				totalSize += info.Size()
			}
			return nil
		})
		rec.FileSize = totalSize
		if err := r.cfg.DB.InsertRecordingWithRetry(context.Background(), rec, 3, 500*time.Millisecond); err != nil {
			httpJpegLogger.Error("failed to insert recording", "camera_id", r.cfg.CameraID, "error", err)
		}
	}

	// Publish SegmentCompleted event.
	if r.cfg.EventBus != nil && recordingID != "" {
		r.cfg.EventBus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
			CameraID:    r.cfg.CameraID,
			FilePath:    r.curFinalPath,
			Format:      string(model.FormatMJPEG),
			StartedAt:   r.segStart.Format(time.RFC3339Nano),
			EndedAt:     time.Now().Format(time.RFC3339Nano),
			FileSize:    totalSize,
			RecordingID: recordingID,
		})
	}

	if r.frameCount > 0 {
		r.recordSegmentCreated()
	}

	r.curTempPath = ""
	r.curFinalPath = ""
	r.frameCount = 0
}

// extractBoundary parses the boundary string from a Content-Type header.
// Example: "multipart/x-mixed-replace;boundary=123456789000000000000987654321"
func extractBoundary(ct string) string {
	idx := strings.Index(ct, "boundary=")
	if idx == -1 {
		return "frame"
	}
	val := ct[idx+len("boundary="):]
	// Remove quotes if present
	val = strings.Trim(val, `"`)
	// Trim any trailing semicolon/whitespace
	if i := strings.IndexAny(val, "; "); i != -1 {
		val = val[:i]
	}
	return val
}

// skipToBoundary reads from the reader until it finds "--<boundary>\r\n".
func (r *HTTPJPEGRecorder) skipToBoundary(reader *bufio.Reader, boundary string) error {
	marker := []byte("--" + boundary)
	for {
		line, err := reader.ReadSlice('\n')
		if err != nil {
			return err
		}
		// Trim trailing \r\n
		line = bytes.TrimRight(line, "\r\n")
		if bytes.Equal(line, marker) {
			return nil
		}
	}
}

// readPartHeaders reads MIME part headers until an empty line.
// Returns the Content-Length value, or 0 if not found.
func (r *HTTPJPEGRecorder) readPartHeaders(reader *bufio.Reader) (int, error) {
	contentLength := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return 0, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			// Empty line signals end of headers
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

// readUntilBoundary reads bytes from reader until the boundary marker is found.
// Returns the data before the boundary (with trailing \r\n stripped).
func readUntilBoundary(reader *bufio.Reader, buf *bytes.Buffer, boundary []byte) ([]byte, error) {
	buf.Reset()
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return nil, err
		}
		buf.WriteByte(b)
		// Check if buffer ends with boundary
		if bytes.HasSuffix(buf.Bytes(), boundary) {
			data := buf.Bytes()
			data = data[:len(data)-len(boundary)]
			// Strip trailing \r\n before boundary
			data = bytes.TrimRight(data, "\r\n")
			return data, nil
		}
	}
}
