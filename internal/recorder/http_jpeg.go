package recorder

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/avi"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

var httpJpegLogger = slogx.Component("http-jpeg-recorder")

// HTTPJPEGConfig holds configuration for the HTTP JPEG recorder.
type HTTPJPEGConfig struct {
	CameraID               string
	URL                    string
	SegmentDur             time.Duration
	Username               string // for basic auth (optional)
	Password               string // for basic auth (optional)
	DB                     RecordingDB
	EventBus               *event.EventBus
	AVI                    bool // when true, write AVI single-file instead of MJPEG directory
	Width                  int  // video width (0 = auto-detect from first frame)
	Height                 int  // video height (0 = auto-detect from first frame)
	DarkFrameFilterEnabled bool // skip dark/night segments
	DarkFrameThreshold     int  // luminance threshold 0-255 (default 15)
	// RecordEnabled gates segment writes (nil => record; ptr-to-false => live-only).
	// See BaseConfig.RecordEnabled for details.
	RecordEnabled *bool
}

// HTTPJPEGRecorder captures JPEG frames from a continuous MJPEG stream over HTTP.
type HTTPJPEGRecorder struct {
	cfg     HTTPJPEGConfig
	store   SegmentStore
	metrics *metrics.Metrics
	client  *http.Client

	mu           sync.Mutex
	status       model.RecorderStatus
	cancel       context.CancelFunc
	cancelStream context.CancelFunc
	done         chan struct{}
	watchdogDone chan struct{}

	lastFrameTime   atomic.Int64 // Unix timestamp of last received frame
	curTempPath     string
	curFinalPath    string
	segStart        time.Time
	frameCount      int
	Hub             *streamhub.StreamHub // Frame fan-out (nil for HTTP-JPEG — no HLS support, reserved for future consumers)
	lastHealthLogAt time.Time            // throttled log for storage health failures

	// latestFrame caches the most recent JPEG frame for snapshot polling.
	// Stored as an atomic pointer to a freshly-allocated, immutable []byte so concurrent
	// readers can share the SAME buffer with zero copy/alloc per poll (was a full
	// make+copy on every LatestFrame() call, multiplied by poll rate × viewer count).
	// Writers Store a new pointer per frame; readers Load and treat the slice as read-only.
	latestFrame atomic.Pointer[[]byte]

	// AVI recording fields
	aviMuxer *avi.Muxer
	aviFile  *os.File
}

// GetHub returns the StreamHub for frame fan-out.
func (r *HTTPJPEGRecorder) GetHub() *streamhub.StreamHub { return r.Hub }

// SetHub wires the StreamHub for frame fan-out (streamhub.HubHost).
func (r *HTTPJPEGRecorder) SetHub(hub *streamhub.StreamHub) { r.Hub = hub }

// HubSource labels the hub for the flow-path observability view.
func (r *HTTPJPEGRecorder) HubSource() string { return "http-jpeg" }

// StreamURL returns the MJPEG stream URL.
func (r *HTTPJPEGRecorder) StreamURL() string { return r.cfg.URL }

// LatestFrame returns the most recently captured JPEG frame WITHOUT copying.
// The returned slice is shared and must be treated as read-only by callers
// (the only consumer, handleLatestFrame, only reads it via w.Write). Returns
// nil if no frame has been captured yet. Safe for concurrent use.
func (r *HTTPJPEGRecorder) LatestFrame() []byte {
	p := r.latestFrame.Load()
	if p == nil {
		return nil
	}
	return *p
}

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

// aviSegmentDurCap returns the maximum segment duration for AVI-mode recorders
// (HTTPJPEG with AVI=true, MJPEG with audio). AVI muxer buffers ALL frames in
// RAM until segment close (avi/muxer.go:m.buf), so segment duration directly
// determines per-stream RAM: 1080p MJPEG @ 1.5Mbps ≈ 5.6MB/30s, 56MB/5min.
//
// Low-memory hosts (≤2GB, e.g. RPi 3B with 1GB) must stay at 30s — a single
// 5-min AVI segment would consume ~5% of total RAM per camera, and the legacy
// cap was chosen for this reason. Hosts with >2GB (Banana Pi M5, x86) can
// safely use 5min, reducing 24h fragment count from ~2880 to ~288 per camera
// (10× reduction in merge backlog pressure for JPEG cameras).
//
// Threshold mirrors config.maxSegmentDurationForMem (2GB) for consistency.
func aviSegmentDurCap() time.Duration {
	const (
		lowMemCap  = 30 * time.Second // RPi 3B (≤2GB): AVI muxer RAM safety
		highMemCap = 5 * time.Minute  // Banana Pi M5 / x86 (>2GB): 10× fewer fragments
		threshold  = 2048             // MB — 2GB
	)
	if memAvailableMB() > threshold {
		return highMemCap
	}
	return lowMemCap
}

// memAvailableMB reports available RAM in MB. Reads /proc/meminfo on Linux;
// falls back to a conservative value on other platforms (treat as low-mem).
// Local copy of config.memAvailableMB to avoid a config→recorder import cycle
// (recorder is imported by many packages including ones config depends on).
func memAvailableMB() int {
	const fallback = 1024 // conservative: assume low memory on non-Linux
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return fallback
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemAvailable:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				var kb int
				if _, err := fmt.Sscanf(fields[1], "%d", &kb); err == nil {
					return kb / 1024
				}
			}
		}
	}
	return fallback
}

func NewHTTPJPEGRecorder(cfg HTTPJPEGConfig, store SegmentStore, opts ...*metrics.Metrics) *HTTPJPEGRecorder {
	var m *metrics.Metrics
	if len(opts) > 0 {
		m = opts[0]
	}
	if cfg.AVI {
		// AVI muxer buffers all frames in RAM until segment close, so cap
		// duration by available memory. On RPi 3B this stays at 30s; on >2GB
		// hosts it rises to 5m (10× fewer fragments → 10× less merge backlog).
		if durCap := aviSegmentDurCap(); cfg.SegmentDur > durCap {
			httpJpegLogger.Warn("AVI mode: SegmentDur capped by available RAM",
				"camera_id", cfg.CameraID, "configured", cfg.SegmentDur, "capped_to", durCap,
				"mem_available_mb", memAvailableMB())
			cfg.SegmentDur = durCap
		}
	}
	if cfg.SegmentDur == 0 {
		cfg.SegmentDur = DefaultSegmentDur
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
	ctx, cancel := context.WithCancel(ctx)
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

	runReconnectLoop(ctx, reconnectDeps{
		CameraID: r.cfg.CameraID,
		Store:    r.store,
		Metrics:  r.metrics,
		Log:      httpJpegLogger,
		Connect: func(streamCtx context.Context) (error, bool) {
			// Inner cancellable ctx so the idle watchdog can kill just the
			// current HTTP stream (not the whole reconnect loop).
			streamCtx, streamCancel := context.WithCancel(streamCtx)
			r.mu.Lock()
			r.cancelStream = streamCancel
			r.mu.Unlock()
			err, connected := r.connectAndStream(streamCtx)
			r.mu.Lock()
			r.cancelStream = nil
			r.mu.Unlock()
			streamCancel()
			return err, connected
		},
		RecordError: r.recordError,
		SetStatus:   r.setStatus,
	})
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

	// Stall watchdog: after a camera hiccup the TCP connection can survive as
	// a zombie — open, but the device never sends another byte. Without a
	// read deadline the loop blocks forever on the first Read, the latest-
	// frame cache freezes, and live viewers sit on a static picture (observed
	// on the ESP32 MiBeeCam: EOF reconnect succeeds, then silence). Arm a
	// per-frame deadline; cameras send frames at ≥1fps, so 15s covers even
	// slow timelapse-y devices while catching zombies.
	const frameReadTimeout = 15 * time.Second
	armDeadline := func() {
		if rd, ok := resp.Body.(interface{ SetReadDeadline(time.Time) error }); ok {
			_ = rd.SetReadDeadline(time.Now().Add(frameReadTimeout))
		}
	}
	armDeadline()

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
		armDeadline() // frame received — re-arm the zombie-connection watchdog
		// Cache latest frame for snapshot polling (before storage check,
		// so live preview works even during storage issues). data is freshly allocated
		// each frame (make or bytes.Buffer), so storing the pointer directly is safe —
		// readers treat it as immutable.
		dp := data
		r.latestFrame.Store(&dp)
		// Broadcast to StreamHub for wsstream live preview (HTTP JPEG cameras).
		if r.Hub != nil {
			r.Hub.Broadcast(time.Now().UnixNano()/1e6*90, [][]byte{data}, true)
		}

		// Live-only mode: keep the latest-frame cache (so MJPEG live preview via
		// /latest-frame polling works) but skip all segment I/O.
		if r.cfg.RecordEnabled != nil && !*r.cfg.RecordEnabled {
			continue
		}

		if isStorageFailed(r.store, r.cfg.CameraID) {
			if r.curTempPath != "" {
				r.closeCurrentSegment()
			}
			if logNow, ok := shouldLogHealth(r.lastHealthLogAt); ok {
				r.lastHealthLogAt = logNow
				httpJpegLogger.Warn("storage health failed, skipping recording (stream kept alive)",
					"camera_id", r.cfg.CameraID)
			}
			// Continue with next frame — keep HTTP connection alive.
			continue
		}

		// Create segment if needed
		if r.curTempPath == "" {
			if r.cfg.AVI {
				tempPath, finalPath, err := r.store.CreateSegment(r.cfg.CameraID, string(model.FormatAVI))
				if err != nil {
					return fmt.Errorf("create avi segment: %w", err), true
				}
				// Determine video dimensions: config > auto-detect from first frame > fallback
				w, h := r.cfg.Width, r.cfg.Height
				if w == 0 || h == 0 {
					if dw, dh, ok := jpegDimensions(data); ok {
						w, h = dw, dh
					} else {
						w, h = 640, 480 // fallback dimensions
					}
				}
				f, err := os.OpenFile(tempPath, os.O_RDWR, 0o644)
				if err != nil {
					os.Remove(tempPath)
					return fmt.Errorf("open avi temp file: %w", err), true
				}
				r.aviFile = f
				r.aviMuxer = avi.NewVideoOnlyMuxer(f, w, h)
				r.curTempPath = tempPath
				r.curFinalPath = finalPath
				r.segStart = time.Now()
				r.frameCount = 0
			} else {
				tempPath, finalPath, err := r.store.CreateSegment(r.cfg.CameraID, string(model.FormatMJPEG))
				if err != nil {
					return fmt.Errorf("create segment: %w", err), true
				}
				r.curTempPath = tempPath
				r.curFinalPath = finalPath
				r.segStart = time.Now()
				r.frameCount = 0
			}
		}

		if r.cfg.AVI && r.aviMuxer != nil {
			r.mu.Lock()
			if err := r.aviMuxer.WriteVideo(data, 0); err != nil {
				r.mu.Unlock()
				return fmt.Errorf("write avi frame: %w", err), true
			}
			r.mu.Unlock()
			r.frameCount++
			r.recordBytes(int64(len(data)))
		} else {
			n, err := r.store.WriteFrame(r.curTempPath, data)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					// The segment tmp vanished (rotated away or cleaned while
					// this stream loop held it). The camera and HTTP stream
					// are healthy — drop the stale path so the next frame
					// opens a fresh segment instead of tearing down and
					// reconnecting into the same dead path (which also fed
					// the storage-health failure counter, #413).
					httpJpegLogger.Warn("segment temp vanished — restarting segment",
						"camera_id", r.cfg.CameraID, "path", r.curTempPath)
					r.curTempPath = ""
					r.curFinalPath = ""
					r.frameCount = 0
					continue
				}
				return fmt.Errorf("write frame: %w", err), true
			}
			r.frameCount++
			r.recordBytes(int64(n))
		}
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
	// For AVI mode: close muxer and file before renaming.
	if r.cfg.AVI {
		r.mu.Lock()
		if r.aviMuxer != nil {
			if err := r.aviMuxer.Close(); err != nil {
				httpJpegLogger.Error("failed to close AVI muxer", "camera_id", r.cfg.CameraID, "error", err)
			}
			r.aviMuxer = nil
		}
		if r.aviFile != nil {
			if err := r.aviFile.Close(); err != nil {
				httpJpegLogger.Error("failed to close AVI file", "camera_id", r.cfg.CameraID, "error", err)
			}
			r.aviFile = nil
		}
		r.mu.Unlock()
	}

	if err := r.store.CloseSegment(r.curTempPath, r.curFinalPath); err != nil {
		httpJpegLogger.Error("failed to close segment", "camera_id", r.cfg.CameraID, "error", err)
	}

	// Insert recording entry into database
	var totalSize int64
	var recordingID string
	segFormat := model.FormatMJPEG
	if r.cfg.AVI {
		segFormat = model.FormatAVI
	}
	if r.cfg.DB != nil && r.curFinalPath != "" && r.frameCount > 0 {
		now := time.Now()
		duration := now.Sub(r.segStart).Seconds()
		rec := &model.Recording{
			ID:         strconv.FormatInt(now.UnixNano(), 10),
			CameraID:   r.cfg.CameraID,
			FilePath:   r.curFinalPath,
			Format:     segFormat,
			StartedAt:  r.segStart,
			EndedAt:    now,
			Duration:   duration,
			FrameCount: r.frameCount,
		}
		recordingID = rec.ID
		if r.cfg.AVI {
			// AVI is a single file.
			if info, err := os.Stat(r.curFinalPath); err == nil {
				totalSize = info.Size()
			}
		} else {
			// MJPEG finalPath is a directory; walk to calculate total size.
			filepath.Walk(r.curFinalPath, func(path string, info os.FileInfo, err error) error {
				if err == nil && !info.IsDir() {
					totalSize += info.Size()
				}
				return nil
			})
		}
		rec.FileSize = totalSize
		if err := r.cfg.DB.InsertRecordingWithRetry(context.Background(), rec, 3, 500*time.Millisecond); err != nil {
			httpJpegLogger.Error("failed to insert recording", "camera_id", r.cfg.CameraID, "error", err)
		}

		// Dark frame detection: check if segment is too dark to be useful.
		if r.cfg.DarkFrameFilterEnabled && r.cfg.DarkFrameThreshold > 0 && recordingID != "" {
			var isDark bool
			if r.cfg.AVI {
				isDark, _, _ = DetectDarkAVIFile(r.curFinalPath, r.cfg.DarkFrameThreshold)
			} else {
				isDark, _, _ = DetectDarkMJPEGDir(r.curFinalPath, r.cfg.DarkFrameThreshold)
			}
			if isDark {
				_ = r.cfg.DB.SetMergeStatus(context.Background(), []string{recordingID}, model.MergeStatusDark)
				httpJpegLogger.Info("segment marked as dark (night/no-IR)",
					"camera_id", r.cfg.CameraID, "recording_id", recordingID)
				// Skip publishing SegmentCompleted — dark segments should not enter merge.
				r.curTempPath = ""
				r.curFinalPath = ""
				r.frameCount = 0
				return
			}
		}
	}

	// Publish SegmentCompleted event.
	if r.cfg.EventBus != nil && recordingID != "" {
		r.cfg.EventBus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
			CameraID:    r.cfg.CameraID,
			FilePath:    r.curFinalPath,
			Format:      string(segFormat),
			Encoding:    string(model.FormatMJPEG),
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
