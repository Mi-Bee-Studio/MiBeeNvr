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

	// adoptedResp/adoptedCancel hold a probe's already-open MJPEG response
	// (#723): the ONVIF recorder probes candidate URLs before handing over,
	// and re-dialing the just-probed URL trips ESP32 anti-hammer guards
	// (<5s between connections arms them). Consumed by the first
	// connectAndStream; later reconnects dial normally. Guarded by mu.
	adoptedResp   *http.Response
	adoptedCancel context.CancelFunc

	// manual is the timed forced-recording window (#660); manualSegmentOpen
	// tracks an in-flight windowed segment for expiry close. Only touched
	// from the frame loop goroutine.
	manual            manualRecordWindow
	manualSegmentOpen bool

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

// ArmManualRecording opens (or extends) a timed forced-recording window
// (#660): for the duration, segments are written even when RecordEnabled
// is false (live-only). Used by MQTT {"action":"record","duration":"60s"}.
func (r *HTTPJPEGRecorder) ArmManualRecording(d time.Duration) { r.manual.Arm(d) }

// ManualRecordingActive reports whether a manual recording window covers now.
func (r *HTTPJPEGRecorder) ManualRecordingActive() bool { return r.manual.Active(time.Now()) }

// HubSource labels the hub for the flow-path observability view.
func (r *HTTPJPEGRecorder) HubSource() string { return "http-jpeg" }

// AVIForm reports whether the recorder writes single-file AVI segments
// (#761). Exposed read-only for wiring tests (builder → config resolution) —
// mirrors the pixgate MetricsWired precedent.
func (r *HTTPJPEGRecorder) AVIForm() bool { return r.cfg.AVI }

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

// aviRotateBytes is the early-rotation threshold for AVI segments (#761): the
// RIFF chunk/list size fields are uint32, so a segment must rotate before the
// file reaches 4 GiB. 3.5 GiB leaves headroom for the idx1 tail and the final
// frames in flight. The old RAM-based segment-duration cap (aviSegmentDurCap)
// is gone with it — the AVI muxer now streams incrementally to disk (avi.Muxer
// io.WriterAt mode), so memory no longer scales with segment size; duration is
// bounded by this byte ceiling instead. A package var (not const) purely so
// tests can shrink it.
var aviRotateBytes int64 = int64(7) << 29 // 3.5 GiB

func NewHTTPJPEGRecorder(cfg HTTPJPEGConfig, store SegmentStore, opts ...*metrics.Metrics) *HTTPJPEGRecorder {
	var m *metrics.Metrics
	if len(opts) > 0 {
		m = opts[0]
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
	r.discardAdoptedStream() // covers never-started recorders holding an adopted probe
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
	defer r.discardAdoptedStream()

	runReconnectLoop(ctx, reconnectDeps{
		CameraID: r.cfg.CameraID,
		Store:    r.store,
		Metrics:  r.metrics,
		Log:      httpJpegLogger,
		// ESP32-class MJPEG cameras (MiBeeCam family) treat sub-5s reconnects
		// as hammering — their single-slot HTTP stream server collapses and
		// the firmware's anti-hammer guard answers 503 with exponential
		// backoff (#711). The shared tier-1 (1s+jitter) amplifies stream-death
		// into a 1-2s reconnect storm; floor every retry at 5s.
		MinBackoff: 5 * time.Second,
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

// AdoptStream hands the recorder an already-open MJPEG response — typically
// the ONVIF recorder's probe connection — so the first stream cycle continues
// it instead of re-dialing the URL (#723: two TCP connects within the ESP32
// anti-hammer window arm the device's guard). The cancel func releases the
// detached request context once the response is done.
func (r *HTTPJPEGRecorder) AdoptStream(resp *http.Response, cancel context.CancelFunc) {
	if resp == nil {
		return
	}
	r.mu.Lock()
	r.adoptedResp = resp
	r.adoptedCancel = cancel
	r.mu.Unlock()
}

// discardAdoptedStream closes a never-consumed adopted response (recorder
// stopped before the first connect cycle ran).
func (r *HTTPJPEGRecorder) discardAdoptedStream() {
	r.mu.Lock()
	resp := r.adoptedResp
	cancel := r.adoptedCancel
	r.adoptedResp = nil
	r.adoptedCancel = nil
	r.mu.Unlock()
	if resp != nil {
		_ = resp.Body.Close()
	}
	if cancel != nil {
		cancel()
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

	// Prefer an adopted probe connection (#723): continuing it avoids a
	// second TCP dial within the device's anti-hammer window. Consumed once;
	// reconnects after this stream ends dial normally (through the ≥5s
	// reconnect floor).
	r.mu.Lock()
	resp := r.adoptedResp
	adoptedCancel := r.adoptedCancel
	r.adoptedResp = nil
	r.adoptedCancel = nil
	r.mu.Unlock()
	if resp != nil {
		httpJpegLogger.Info("continuing probed MJPEG connection", "camera_id", r.cfg.CameraID, "url", r.cfg.URL)
		if adoptedCancel != nil {
			defer adoptedCancel()
		}
	} else {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.cfg.URL, nil)
		if err != nil {
			return fmt.Errorf("create request: %w", err), false
		}
		if r.cfg.Username != "" {
			req.SetBasicAuth(r.cfg.Username, r.cfg.Password)
		}

		httpJpegLogger.Info("connecting to MJPEG stream", "camera_id", r.cfg.CameraID, "url", slogx.RedactURL(r.cfg.URL))
		resp, err = r.client.Do(req)
		if err != nil {
			return fmt.Errorf("http connect: %w", err), false
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return &HTTPStatusError{
			Code:       resp.StatusCode,
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
		}, false
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

		if err := r.skipToBoundary(reader, boundary); err != nil {
			return fmt.Errorf("read boundary: %w", err), true
		}

		// Read part headers to get Content-Length
		contentLength, err := r.readPartHeaders(reader)
		if err != nil {
			return fmt.Errorf("read part headers: %w", err), true
		}

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
			// Manual recording window (#660): an armed window overrides the
			// live-only gate for its duration.
			if !r.manual.Active(time.Now()) {
				if r.manualSegmentOpen {
					r.closeCurrentSegment()
					r.manualSegmentOpen = false
				}
				continue
			}
			r.manualSegmentOpen = true
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

		// Check if segment duration elapsed, or the AVI size ceiling approached
		rotate := false
		if r.cfg.AVI && r.aviMuxer != nil {
			r.mu.Lock()
			rotate = r.aviMuxer.TotalBytes() >= aviRotateBytes
			r.mu.Unlock()
		}
		if rotate || time.Since(r.segStart) >= r.cfg.SegmentDur {
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

	// Segment-loss gate (2026-09-08 incident): when the final file/directory
	// never materialized, a DB row would be a permanent 404 entry — skip the
	// insert, event, and metrics instead.
	segmentFinalized := false
	if r.curFinalPath != "" {
		if _, err := os.Stat(r.curFinalPath); err == nil {
			segmentFinalized = true
		} else {
			httpJpegLogger.Warn("segment missing after finalize — recording row skipped",
				"camera_id", r.cfg.CameraID, "path", r.curFinalPath)
		}
	}

	// Insert recording entry into database
	var totalSize int64
	var recordingID string
	segFormat := model.FormatMJPEG
	if r.cfg.AVI {
		segFormat = model.FormatAVI
	}
	if r.cfg.DB != nil && segmentFinalized && r.frameCount > 0 {
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
		if err := r.cfg.DB.InsertRecordingWithRetry(context.Background(), rec, dbInsertRetries, dbInsertBackoff); err != nil {
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

	if r.frameCount > 0 && segmentFinalized {
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
		if bytes.HasSuffix(buf.Bytes(), boundary) {
			data := buf.Bytes()
			data = data[:len(data)-len(boundary)]
			// Strip trailing \r\n before boundary
			data = bytes.TrimRight(data, "\r\n")
			return data, nil
		}
	}
}

// HTTPStatusError is a non-200 stream response. RetryAfter carries the
// server's Retry-After hint when present (seconds form): the mibee_cam
// anti-hammer guard sends the remaining cooldown on 503 so clients can wait
// out the window instead of re-triggering its renewal (#711).
type HTTPStatusError struct {
	Code       int
	RetryAfter time.Duration
}

func (e *HTTPStatusError) Error() string { return fmt.Sprintf("http status %d", e.Code) }

// RetryAfterHint exposes the cooldown for the reconnect loop (longer wins).
func (e *HTTPStatusError) RetryAfterHint() time.Duration { return e.RetryAfter }

// parseRetryAfter parses the delta-seconds form of Retry-After. The HTTP-date
// form is treated as absent — no known camera firmware uses it.
func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	secs, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}
