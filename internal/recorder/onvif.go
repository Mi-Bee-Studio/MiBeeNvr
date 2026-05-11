package recorder

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/format"

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
}

// ONVIFRecorder implements model.Recorder by resolving the RTSP stream URI
// via ONVIF GetStreamURI, then delegating to an internal H264Recorder or H265Recorder.
type ONVIFRecorder struct {
	cfg         ONVIFConfig
	onvifClient onvif.DeviceClient
	store       SegmentStore
	metrics     *metrics.Metrics
	hlsFrameCb  func(pts int64, au [][]byte)

	// newRecorder is a function that creates the delegate recorder.
	// Overridable in tests to inject a mock recorder.
	newRecorder func(rtspURL string) model.Recorder

	mu       sync.Mutex
	status   model.RecorderStatus
	delegate model.Recorder
	rtspURL  string // Cached RTSP URL from ONVIF
}

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
// This is used by the HLS handler to access SPS/PPS and set OnHLSFrame callback.
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
		Scheme:   u.Scheme,
		Host:     u.Host,
		Protocol: &tcp,
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

// createDelegate creates the appropriate internal recorder based on encoding.
func (r *ONVIFRecorder) createDelegate(rtspURL string) model.Recorder {
	encoding := r.detectEncoding(context.Background())
	switch encoding {
	case "H265":
		cfg := H265Config{
			CameraID:    r.cfg.CameraID,
			RTSPURL:     rtspURL,
			Username:    r.cfg.Username,
			Password:    r.cfg.Password,
			SegmentDur:  r.cfg.SegmentDur,
			RingBufCap:  DefaultRingBufCap,
			MaxBackoff:  DefaultMaxBackoff,
			InitBackoff: DefaultInitBackoff,
			DB:          r.cfg.DB,
		}
		rec := NewH265Recorder(cfg, r.store, r.metrics)
		if r.hlsFrameCb != nil {
			rec.OnHLSFrame = r.hlsFrameCb
		}
		return rec
	default: // H264 or unknown
		cfg := H264Config{
			CameraID:    r.cfg.CameraID,
			RTSPURL:     rtspURL,
			Username:    r.cfg.Username,
			Password:    r.cfg.Password,
			SegmentDur:  r.cfg.SegmentDur,
			RingBufCap:  DefaultRingBufCap,
			MaxBackoff:  DefaultMaxBackoff,
			InitBackoff: DefaultInitBackoff,
			DB:          r.cfg.DB,
		}
		rec := NewH264Recorder(cfg, r.store, r.metrics)
		if r.hlsFrameCb != nil {
			rec.OnHLSFrame = r.hlsFrameCb
		}
		return rec
	}
}
