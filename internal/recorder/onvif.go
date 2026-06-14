package recorder

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/format"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
)

var onvifRecLogger = slog.Default().With("component", "onvif-recorder")

// ONVIFConfig holds configuration for the ONVIF recorder.
type ONVIFConfig struct {
	CameraID       string
	ProfileToken   string
	StreamEncoding string // "H264" or "H265". Empty = auto-detect via ONVIF profile or RTSP DESCRIBE.
	Username       string // RTSP credentials (may differ from ONVIF credentials)
	Password       string
	SegmentDur     time.Duration
	DB             RecordingDB
	AudioEnabled   bool
	FrameWatchdogTimeout time.Duration // default 30s (0 = use constant default)
	ONVIFEndpoint string         // ONVIF device endpoint URL (for HTTP MJPEG probe base)
	EventBus      *event.EventBus
}

// ONVIFRecorder implements model.Recorder by resolving the RTSP stream URI
// via ONVIF GetStreamURI, then delegating to an internal H264Recorder or H265Recorder.
type ONVIFRecorder struct {
	cfg         ONVIFConfig
	onvifClient onvif.DeviceClient
	store       SegmentStore
	metrics     *metrics.Metrics
	Hub         *model.StreamHub // Frame fan-out, passed to delegate recorders

	// newRecorder is a function that creates the delegate recorder.
	// Overridable in tests to inject a mock recorder.
	newRecorder func(rtspURL string) model.Recorder

	mu          sync.Mutex
	status      model.RecorderStatus
	delegate    model.Recorder
	rtspURL     string // Cached RTSP URL from ONVIF
	httpJPEGURL string // Cached MJPEG HTTP URL (protected by mu)
}

// GetHub returns the StreamHub for frame fan-out.
func (r *ONVIFRecorder) GetHub() *model.StreamHub { return r.Hub }

// Compile-time check.
var _ model.Recorder = (*ONVIFRecorder)(nil)

// NewONVIFRecorder creates a new ONVIF recorder that delegates to H264/H265 recorder.
func NewONVIFRecorder(cfg ONVIFConfig, client onvif.DeviceClient, store SegmentStore, opts ...*metrics.Metrics) *ONVIFRecorder {
	var m *metrics.Metrics
	if len(opts) > 0 {
		m = opts[0]
	}
	if cfg.SegmentDur == 0 {
		cfg.SegmentDur = DefaultSegmentDur
	}
	r := &ONVIFRecorder{
		cfg:         cfg,
		onvifClient: client,
		store:       store,
		metrics:     m,
		status:      model.StatusStopped,
	}
	r.newRecorder = r.createDelegate
	return r
}

// Start connects to the ONVIF device, resolves the RTSP URI, creates an internal
// H264Recorder or H265Recorder based on the profile encoding, and starts it.
func (r *ONVIFRecorder) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.status == model.StatusRecording || r.status == model.StatusReconnecting {
		return fmt.Errorf("recorder for %q already running", r.cfg.CameraID)
	}

	// 1. Connect to ONVIF device
	if err := r.onvifClient.Connect(ctx); err != nil {
		return fmt.Errorf("onvif connect: %w", err)
	}

	// 2. Resolve profile token if not set
	profileToken := r.cfg.ProfileToken
	if profileToken == "" {
		profiles, err := r.onvifClient.GetProfiles(ctx)
		if err != nil {
			return fmt.Errorf("onvif get profiles: %w", err)
		}
		if len(profiles) == 0 {
			return fmt.Errorf("onvif device has no media profiles")
		}
		profileToken = profiles[0].Token
		onvifRecLogger.Info("auto-selected ONVIF profile", "camera_id", r.cfg.CameraID, "profile_token", profileToken, "encoding", profiles[0].Encoding)
	}

	// 3. Get stream URI
	streamInfo, err := r.onvifClient.GetStreamURI(ctx, profileToken)
	if err != nil {
		return fmt.Errorf("onvif get stream URI: %w", err)
	}
	r.rtspURL = streamInfo.URI
	if r.rtspURL == "" {
		return fmt.Errorf("onvif device returned empty stream URI — check device credentials")
	}
	onvifRecLogger.Info("resolved ONVIF stream URI", "camera_id", r.cfg.CameraID, "rtsp_url", r.rtspURL)

	// 3. Create delegate recorder based on encoding
	r.delegate = r.newRecorder(r.rtspURL)

	// 4. Start delegate
	r.status = model.StatusRecording
	return r.delegate.Start(ctx)
}

// Stop stops the internal delegate recorder.
func (r *ONVIFRecorder) Stop() error {
	r.mu.Lock()
	if r.delegate != nil {
		r.mu.Unlock()
		return r.delegate.Stop()
	}
	r.status = model.StatusStopped
	r.mu.Unlock()
	return nil
}

// Status returns the current recorder status, delegating to the internal recorder if available.
func (r *ONVIFRecorder) Status() model.RecorderStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.delegate != nil {
		return r.delegate.Status()
	}
	return r.status
}

// RTSPURL returns the resolved RTSP URL from ONVIF (may be empty before Start).
func (r *ONVIFRecorder) RTSPURL() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rtspURL
}

// Delegate returns the internal H264/H265 recorder delegate.
// Returns nil if the recorder hasn't been started yet.
// This is used by the HLS handler to access SPS/PPS and subscribe to StreamHub for HLS streaming.
func (r *ONVIFRecorder) Delegate() model.Recorder {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.delegate
}

// detectEncoding determines the stream encoding in priority order:
// 1. Manual config (StreamEncoding field)
// 2. ONVIF profile metadata
// 3. RTSP DESCRIBE probe (most reliable)
// Falls back to H264 if detection fails.
func (r *ONVIFRecorder) detectEncoding(ctx context.Context) string {
	// 1. Manual override from config
	if r.cfg.StreamEncoding == "H264" || r.cfg.StreamEncoding == "H265" {
		onvifRecLogger.Info("using configured stream encoding", "camera_id", r.cfg.CameraID, "encoding", r.cfg.StreamEncoding)
		return r.cfg.StreamEncoding
	}

	// 2. Try ONVIF profile metadata
	profiles, err := r.onvifClient.GetProfiles(ctx)
	if err == nil && len(profiles) > 0 {
		for _, p := range profiles {
			if p.Encoding == "H264" {
				return "H264"
			}
		}
		for _, p := range profiles {
			if p.Encoding == "H265" {
				return "H265"
			}
		}
		for _, p := range profiles {
			if p.Encoding == "JPEG" {
				return "JPEG"
			}
		}
	}

	// 3. Probe via RTSP DESCRIBE
	if r.rtspURL != "" {
		if enc := r.probeRTSPEncoding(); enc != "" {
			onvifRecLogger.Info("detected encoding via RTSP DESCRIBE", "camera_id", r.cfg.CameraID, "encoding", enc)
			return enc
		}
	}

	// Default to H264
	onvifRecLogger.Warn("could not detect encoding, defaulting to H264", "camera_id", r.cfg.CameraID)
	return "H264"
}

// probeRTSPEncoding connects to the RTSP stream and checks the media format.
func (r *ONVIFRecorder) probeRTSPEncoding() string {
	u, err := base.ParseURL(r.rtspURL)
	if err != nil {
		return ""
	}
	if u.User == nil && r.cfg.Username != "" {
		u.User = url.UserPassword(r.cfg.Username, r.cfg.Password)
	}
	tcp := gortsplib.ProtocolTCP
	client := &gortsplib.Client{
		Scheme:      u.Scheme,
		Host:        u.Host,
		Protocol:    &tcp,
		ReadTimeout: 10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	if err := client.Start(); err != nil {
		return ""
	}
	defer client.Close()

	desc, _, err := client.Describe(u)
	if err != nil {
		return ""
	}
	// Check for H265 first (many ONVIF cameras report as H264 but stream H265)
	var h265Forma *format.H265
	if desc.FindFormat(&h265Forma) != nil {
		return "H265"
	}
	var h264Forma *format.H264
	if desc.FindFormat(&h264Forma) != nil {
		return "H264"
	}
	return ""
}

// probeHTTPMJPEG probes the ONVIF device for an HTTP MJPEG stream by trying
// candidate URLs and checking for multipart/x-mixed-replace Content-Type.
func (r *ONVIFRecorder) probeHTTPMJPEG(ctx context.Context) (string, error) {
	onvifURL, err := url.Parse(r.cfg.ONVIFEndpoint)
	if err != nil {
		return "", fmt.Errorf("parse ONVIF endpoint: %w", err)
	}

	// Extract path from rtspURL (e.g., /stream from rtsp://host:554/stream)
	rtspPath := ""
	if u, err := url.Parse(r.rtspURL); err == nil && u.Path != "" {
		rtspPath = u.Path
	}

	// Build candidate list (deduplicated)
	candidates := make([]string, 0, 4)
	seen := make(map[string]bool)
	if rtspPath != "" && !seen[rtspPath] {
		candidates = append(candidates, rtspPath)
		seen[rtspPath] = true
	}
	for _, path := range []string{"/stream", "/mjpeg", "/video"} {
		if !seen[path] {
			candidates = append(candidates, path)
			seen[path] = true
		}
	}

	// Build candidate base URLs: probe the MJPEG preview port (81) first,
	// then the ONVIF port. Some devices (ESP32-S3 MiBeeCam) separate MJPEG
	// preview onto port 81 to avoid blocking the main HTTP server on port 80.
	host := onvifURL.Hostname()
	baseURLs := []string{
		fmt.Sprintf("http://%s:81", host),
		fmt.Sprintf("http://%s", onvifURL.Host),
	}

	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
		},
	}

	onvifRecLogger.Info("probing HTTP MJPEG", "camera_id", r.cfg.CameraID, "bases", baseURLs, "candidates", candidates)

	for _, base := range baseURLs {
		for _, path := range candidates {
			testURL := base + path
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, testURL, nil)
			if err != nil {
				continue
			}
			req.Header.Set("Connection", "close")
			req.Close = true

			resp, err := client.Do(req)
			if err != nil {
				onvifRecLogger.Debug("HTTP MJPEG probe candidate failed", "camera_id", r.cfg.CameraID, "url", testURL, "error", err)
				continue
			}
			ct := resp.Header.Get("Content-Type")
			resp.Body.Close()
			onvifRecLogger.Debug("HTTP MJPEG probe response", "camera_id", r.cfg.CameraID, "url", testURL, "content_type", ct)
			if strings.Contains(ct, "multipart/x-mixed-replace") {
				onvifRecLogger.Info("HTTP MJPEG stream found", "camera_id", r.cfg.CameraID, "url", testURL)
				return testURL, nil
			}
		}
	}
	return "", fmt.Errorf("no MJPEG stream found at any candidate URL")
}

// guessMJPEGURL constructs a best-guess HTTP MJPEG URL from the ONVIF endpoint
// and RTSP stream path. Used when the probe fails (e.g. ESP32-S3 with limited
// concurrent HTTP connections — the ONVIF client holds one, blocking the probe).
// The HTTPJPEGRecorder will retry automatically if the guessed URL is wrong.
func (r *ONVIFRecorder) guessMJPEGURL() string {
	onvifURL, err := url.Parse(r.cfg.ONVIFEndpoint)
	if err != nil {
		return ""
	}
	path := "/stream"
	if u, err := url.Parse(r.rtspURL); err == nil && u.Path != "" {
		path = u.Path
	}
	// Fall back to the ONVIF device's own HTTP host:port — the known-reachable
	// HTTP server. The HTTPJPEGRecorder retries automatically if this guess is
	// wrong; probeHTTPMJPEG (when it succeeds) already prefers MJPEG preview port 81.
	return fmt.Sprintf("http://%s%s", onvifURL.Host, path)
}

// createDelegate creates the appropriate internal recorder based on encoding.
func (r *ONVIFRecorder) createDelegate(rtspURL string) model.Recorder {
	encoding := r.detectEncoding(context.Background())
	switch encoding {
	case "H265":
		cfg := H265Config{
			CameraID:            r.cfg.CameraID,
			RTSPURL:             rtspURL,
			Username:            r.cfg.Username,
			Password:            r.cfg.Password,
			SegmentDur:          r.cfg.SegmentDur,
			RingBufCap:          DefaultRingBufCap,
			DB:                  r.cfg.DB,
			AudioEnabled:        r.cfg.AudioEnabled,
			FrameWatchdogTimeout: r.cfg.FrameWatchdogTimeout,
		}
		rec := NewH265Recorder(cfg, r.store, r.metrics)
		rec.Hub = r.Hub
		return rec
	case "JPEG":
		// 1. Try cached HTTP MJPEG URL (caller holds mu, no need to re-lock)
		if r.httpJPEGURL != "" {
			return r.newHTTPJPEGRecorder(r.httpJPEGURL)
		}

		// 2. Try ONVIF GetStreamUri with Protocol=HTTP.
		//    Per ONVIF spec, HTTP protocol is for RTSP-over-HTTP tunneling, but
		//    some cameras may return a direct HTTP MJPEG URL.
		//    Only use if the returned URI starts with http:// (not rtsp://).
		profileToken := r.resolveProfileToken()
		if profileToken != "" {
			onvifCtx, onvifCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer onvifCancel()
			info, err := r.onvifClient.GetStreamURIWithProtocol(onvifCtx, profileToken, "HTTP")
			if err != nil {
				onvifRecLogger.Debug("ONVIF GetStreamURIWithProtocol(HTTP) failed", "camera_id", r.cfg.CameraID, "error", err)
			} else if info != nil && strings.HasPrefix(info.URI, "http://") {
				onvifRecLogger.Info("ONVIF returned HTTP stream URI", "camera_id", r.cfg.CameraID, "url", info.URI)
				r.httpJPEGURL = info.URI
				return r.newHTTPJPEGRecorder(info.URI)
			} else if info != nil {
				onvifRecLogger.Debug("ONVIF HTTP protocol returned non-HTTP URI, ignoring", "camera_id", r.cfg.CameraID, "url", info.URI)
			}
		}

		// 3. Probe for HTTP MJPEG stream on the ONVIF device.
		//    NOTE: ONVIF client may hold an active HTTP connection to the same
		//    device (ESP32-S3 web servers often support only 1-2 concurrent
		//    connections). The probe may fail due to connection exhaustion.
		probeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if httpURL, err := r.probeHTTPMJPEG(probeCtx); err == nil {
			r.httpJPEGURL = httpURL
			return r.newHTTPJPEGRecorder(httpURL)
		}

		// 4. Probe failed (likely connection exhaustion on ESP32-S3).
		//    Construct best-guess HTTP MJPEG URL from ONVIF endpoint + RTSP path.
		//    HTTPJPEGRecorder will retry automatically on connection failure.
		guessURL := r.guessMJPEGURL()
		onvifRecLogger.Info("HTTP MJPEG probe failed, using best-guess URL", "camera_id", r.cfg.CameraID, "url", guessURL)
		r.httpJPEGURL = guessURL
		return r.newHTTPJPEGRecorder(guessURL)
	default: // H264 or unknown
		cfg := H264Config{
			CameraID:            r.cfg.CameraID,
			RTSPURL:             rtspURL,
			Username:            r.cfg.Username,
			Password:            r.cfg.Password,
			SegmentDur:          r.cfg.SegmentDur,
			RingBufCap:          DefaultRingBufCap,
			DB:                  r.cfg.DB,
			AudioEnabled:        r.cfg.AudioEnabled,
			FrameWatchdogTimeout: r.cfg.FrameWatchdogTimeout,
		}
		rec := NewH264Recorder(cfg, r.store, r.metrics)
		rec.Hub = r.Hub
		return rec
	}
}

// newHTTPJPEGRecorder creates an HTTPJPEGRecorder with the given URL.
func (r *ONVIFRecorder) newHTTPJPEGRecorder(httpURL string) model.Recorder {
	cfg := HTTPJPEGConfig{
		CameraID:   r.cfg.CameraID,
		URL:        httpURL,
		SegmentDur: r.cfg.SegmentDur,
		Username:   r.cfg.Username,
		Password:   r.cfg.Password,
		DB:         r.cfg.DB,
		EventBus:   r.cfg.EventBus,
	}
	rec := NewHTTPJPEGRecorder(cfg, r.store, r.metrics)
	rec.Hub = r.Hub
	return rec
}

// resolveProfileToken returns the configured profile token or auto-selects
// the first available profile from the ONVIF device.
func (r *ONVIFRecorder) resolveProfileToken() string {
	if r.cfg.ProfileToken != "" {
		return r.cfg.ProfileToken
	}
	profiles, err := r.onvifClient.GetProfiles(context.Background())
	if err != nil || len(profiles) == 0 {
		return ""
	}
	return profiles[0].Token
}
