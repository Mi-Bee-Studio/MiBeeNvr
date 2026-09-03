package recorder

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/format"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

var onvifRecLogger = slogx.Component("onvif-recorder")

// ONVIFConfig holds configuration for the ONVIF recorder.
type ONVIFConfig struct {
	CameraID             string
	ProfileToken         string
	StreamEncoding       string // "H264" or "H265". Empty = auto-detect via ONVIF profile or RTSP DESCRIBE.
	Username             string // RTSP credentials (may differ from ONVIF credentials)
	Password             string
	SegmentDur           time.Duration
	DB                   RecordingDB
	AudioEnabled         bool
	AudioInRecordings    bool
	FrameWatchdogTimeout time.Duration // default 30s (0 = use constant default)
	// RingBufCap overrides the delegate H.264/H.265 recorders' frameCh
	// capacity (issue #521). 0 = recorder.DefaultRingBufCap.
	RingBufCap    int
	ONVIFEndpoint string // ONVIF device endpoint URL (for HTTP MJPEG probe base)
	EventBus      *event.EventBus
	AVI           bool // when true, JPEG delegate writes AVI single-file
	// RecordEnabled gates segment writes for all delegate recorders (H264/H265/
	// MJPEG/HTTP-JPEG). nil => record (default); pointer to false => live-only
	// (recorder stays connected for live preview/relay/health but writes nothing).
	// Required because ONVIF cameras delegate to the codec-specific recorder at
	// Start time — without this, recording_enabled=false had no effect on ONVIF
	// cameras (the delegate always recorded).
	RecordEnabled *bool
	// Adaptive enables dynamic-timelapse write density (issue #435) on the
	// H.264/H.265 delegates. Without forwarding it here, recording_mode:
	// adaptive on ONVIF cameras was silently ignored by the delegate (issue
	// #467) — the config validated and the UI showed adaptive, but the
	// recorder wrote continuously. Ignored for MJPEG/JPEG delegates (the
	// compressed-domain signal requires differential encoding).
	Adaptive *AdaptiveConfig
	// AudioTrigger arms loudness-triggered recording (issue #478) on the
	// delegates, on top of Adaptive. G.711 cameras only.
	AudioTrigger *AudioTriggerConfig
}

// ONVIFRecorder implements model.Recorder by resolving the RTSP stream URI
// via ONVIF GetStreamURI, then delegating to an internal H264Recorder or H265Recorder.
type ONVIFRecorder struct {
	cfg         ONVIFConfig
	onvifClient onvif.DeviceClient
	store       SegmentStore
	metrics     *metrics.Metrics
	Hub         *streamhub.StreamHub // Frame fan-out, passed to delegate recorders

	// newRecorder is a function that creates the delegate recorder.
	// Overridable in tests to inject a mock recorder.
	newRecorder func(rtspURL string) model.Recorder

	// probeEncodingFn probes the RTSP stream for its real video format.
	// Overridable in tests to avoid a live RTSP round-trip. Defaults to
	// (*ONVIFRecorder).probeRTSPEncoding.
	probeEncodingFn func() string

	mu          sync.Mutex
	status      model.RecorderStatus
	delegate    model.Recorder
	rtspURL     string // Cached RTSP URL from ONVIF
	httpJPEGURL string // Cached MJPEG HTTP URL (protected by mu)
	// resolvedEncoding is the video codec resolved by detectEncoding during
	// Start (e.g. "H264", "H265", "MJPEG", "JPEG"). Empty before Start completes
	// or when detection failed. Exposed via ResolvedEncoding() so the camera
	// manager can persist it (mirroring ResolvedProfileToken) — see issue #112.
	// Without persistence, ONVIF auto-detect cameras carry encoding="" in DB/YAML
	// and a brief device outage makes the frontend lose the codec → protocol storm.
	resolvedEncoding string
}

// GetHub returns the StreamHub for frame fan-out.
func (r *ONVIFRecorder) GetHub() *streamhub.StreamHub { return r.Hub }

// SetHub wires the StreamHub for frame fan-out (streamhub.HubHost).
func (r *ONVIFRecorder) SetHub(hub *streamhub.StreamHub) { r.Hub = hub }

// HubSource labels the hub for the flow-path observability view.
func (r *ONVIFRecorder) HubSource() string { return "onvif" }

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
	r.probeEncodingFn = r.probeRTSPEncoding
	return r
}

// Start connects to the ONVIF device, resolves the RTSP URI, creates an internal
// H264Recorder or H265Recorder based on the profile encoding, and starts it.
//
// Concurrency: the ONVIF handshake (Connect, GetProfiles, GetStreamURI) and the
// delegate Start run OUTSIDE r.mu — these are multi-second network operations
// that must NOT block Status()/Delegate()/RTSPURL() (polled every 500ms by the
// grid's latest-frame handler). Only the initial already-running guard and the
// final state publication take the (short) lock. SOAP goroutine-safety is
// guaranteed by the onvif.Client's own internal mutex, NOT by r.mu.
func (r *ONVIFRecorder) Start(ctx context.Context) error {
	// Already-running guard (short lock).
	r.mu.Lock()
	if r.status == model.StatusRecording || r.status == model.StatusReconnecting {
		r.mu.Unlock()
		return fmt.Errorf("recorder for %q already running", r.cfg.CameraID)
	}
	r.mu.Unlock()

	// All handshake I/O runs unlocked. On any failure we return the error
	// WITHOUT mutating r.status — matching the legacy behavior where a failed
	// Start left status at its pre-Start value (StatusStopped for a fresh
	// recorder). The health manager tracks failed starts via the camera
	// manager's failedStartCameras map, not via recorder status.
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
		// Pick the main (highest-resolution) profile rather than blindly taking
		// profiles[0] — some cameras order the low-res sub-stream first, which
		// silently recorded at the wrong resolution. See onvif.SelectMainProfile.
		profileToken = onvif.SelectMainProfile(profiles)
		r.cfg.ProfileToken = profileToken // cache so resolveProfileToken skips a redundant GetProfiles (ESP32 connection-pool exhaustion)
		selected := profileByToken(profiles, profileToken)
		onvifRecLogger.Info("auto-selected ONVIF profile", "camera_id", r.cfg.CameraID, "profile_token", profileToken, "encoding", selected.Encoding, "resolution", formatRes(selected))
	}

	// 3. Get stream URI
	streamInfo, err := r.onvifClient.GetStreamURI(ctx, profileToken)
	if err != nil {
		return fmt.Errorf("onvif get stream URI: %w", err)
	}
	rtspURL := streamInfo.URI
	if rtspURL == "" {
		return fmt.Errorf("onvif device returned empty stream URI — check device credentials")
	}
	// The stream URI's host may lag behind the ONVIF endpoint after a DHCP
	// reassignment (device still advertises its old IP). Rewrite it to the
	// known-good endpoint host so the RTSP dial reaches the right address.
	if fixed := RewriteStaleStreamHost(rtspURL, r.cfg.ONVIFEndpoint); fixed != rtspURL {
		onvifRecLogger.Info("rewrote stale stream URI host to match ONVIF endpoint",
			"camera_id", r.cfg.CameraID, "old", rtspURL, "new", fixed)
		rtspURL = fixed
	}
	onvifRecLogger.Info("resolved ONVIF stream URI", "camera_id", r.cfg.CameraID, "rtsp_url", rtspURL)

	// Publish rtspURL BEFORE createDelegate: createDelegate → detectEncoding →
	// probeRTSPEncoding reads r.rtspURL to DESCRIBE the stream and detect the
	// real codec (H264 vs H265 vs JPEG). If r.rtspURL is still empty at that
	// point, the probe no-ops and we fall back to the ONVIF-claimed encoding,
	// which is wrong for cameras that lie (e.g. an H265 stream claimed as
	// H264 → "H264 media not found in stream" death-loop).
	//
	// Set under r.mu: the recorder is registered in the manager snapshot BEFORE
	// rec.Start runs (startRecorderLocked registers then dials), so a concurrent
	// RTSPURL() reader (e.g. relay engine / GetStreamURL) can reach r.rtspURL
	// during this handshake. A bare unlocked string write is a two-word
	// non-atomic store → torn read. The short lock makes it safe.
	r.mu.Lock()
	r.rtspURL = rtspURL
	r.mu.Unlock()

	// 4. Create delegate recorder based on encoding (createDelegate may do an
	//    RTSP DESCRIBE probe + HTTP MJPEG probes — all unlocked). createDelegate
	//    also stashes the resolved encoding on r.resolvedEncoding so the camera
	//    manager can persist it via ResolvedEncoding() (issue #112).
	delegate := r.newRecorder(rtspURL)

	// 5. Start delegate (network dial — unlocked).
	if err := delegate.Start(ctx); err != nil {
		// Publish the delegate so Stop/Status can reach it (short lock).
		r.mu.Lock()
		r.delegate = delegate
		r.rtspURL = rtspURL
		r.mu.Unlock()
		return err
	}

	// 6. Publish resolved state (short lock).
	r.mu.Lock()
	r.delegate = delegate
	r.rtspURL = rtspURL
	r.status = model.StatusRecording
	r.mu.Unlock()
	return nil
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
// Snapshots the delegate pointer under the short lock, then calls its Status()
// OUTSIDE the lock — so a long-running Start handshake (which no longer holds
// r.mu) can't block this hot path (polled every 500ms by grid latest-frame).
func (r *ONVIFRecorder) Status() model.RecorderStatus {
	r.mu.Lock()
	delegate := r.delegate
	st := r.status
	r.mu.Unlock()
	if delegate != nil {
		return delegate.Status()
	}
	return st
}

// RTSPURL returns the resolved RTSP URL from ONVIF (may be empty before Start).
func (r *ONVIFRecorder) RTSPURL() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rtspURL
}

// ResolvedProfileToken returns the profile token resolved during Start (either
// from config or auto-selected via SelectMainProfile). Empty if Start hasn't
// run yet or the token was never resolved. Used by the camera manager to
// persist the auto-selected token so GetProfiles isn't re-run on every restart.
func (r *ONVIFRecorder) ResolvedProfileToken() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cfg.ProfileToken
}

// ResolvedEncoding returns the video codec resolved during Start (e.g. "H264",
// "H265", "MJPEG", "JPEG"). Empty if Start hasn't run yet or detection failed.
// Used by the camera manager to persist the resolved encoding so a later device
// outage doesn't leave the camera with an empty encoding in DB/YAML — which
// would make the frontend lose the codec and thrash through the protocol chain
// (issue #112). Mirrors ResolvedProfileToken's accessor pattern.
func (r *ONVIFRecorder) ResolvedEncoding() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.resolvedEncoding
}

// SetResolvedEncodingForTest sets the resolved encoding AND the recorder status
// (so ensureEncoding's Status()==StatusRecording gate passes) for unit tests
// that inject a recorder via CameraManager.SetTestRecorder without running a
// real Start(). Test-only: production code populates these fields in Start.
// The delegate is intentionally left nil so Status() reports r.status directly.
func (r *ONVIFRecorder) SetResolvedEncodingForTest(enc string, status model.RecorderStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resolvedEncoding = enc
	r.status = status
}

// Delegate returns the internal H264/H265 recorder delegate.
// Returns nil if the recorder hasn't been started yet.
// This is used by the HLS handler to access SPS/PPS and subscribe to StreamHub for HLS streaming.
func (r *ONVIFRecorder) Delegate() model.Recorder {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.delegate
}

// AudioTriggerEvent forwards an external audio-activity event (issue #478) to
// the current delegate — the adaptive gate lives on the codec-specific
// recorder the ONVIF shell delegates to (same pattern as the Adaptive config
// forwarding, issue #467).
func (r *ONVIFRecorder) AudioTriggerEvent(at time.Time, hold time.Duration) error {
	d := r.Delegate()
	if d == nil {
		return fmt.Errorf("camera %s has no active stream delegate", r.cfg.CameraID)
	}
	trig, ok := d.(interface {
		AudioTriggerEvent(at time.Time, hold time.Duration) error
	})
	if !ok {
		return fmt.Errorf("camera %s does not support audio triggers (codec delegate without adaptive gate)", r.cfg.CameraID)
	}
	return trig.AudioTriggerEvent(at, hold)
}

// PixelTriggerEvent forwards a pixgate CV activity confirmation (#636) to the
// current delegate (same forwarding pattern as AudioTriggerEvent).
func (r *ONVIFRecorder) PixelTriggerEvent(at time.Time, hold time.Duration) error {
	d := r.Delegate()
	if d == nil {
		return fmt.Errorf("camera %s has no active stream delegate", r.cfg.CameraID)
	}
	trig, ok := d.(interface {
		PixelTriggerEvent(at time.Time, hold time.Duration) error
	})
	if !ok {
		return fmt.Errorf("camera %s does not support pixel triggers (codec delegate without adaptive gate)", r.cfg.CameraID)
	}
	return trig.PixelTriggerEvent(at, hold)
}

// detectEncoding determines the stream encoding in priority order:
//  1. RTSP DESCRIBE probe (authoritative — what the stream actually carries)
//  2. Manual config (StreamEncoding field) — fallback when DESCRIBE is unavailable
//  3. ONVIF profile metadata — fallback when DESCRIBE is unavailable
//  4. H264 default
//
// RTSP DESCRIBE must outrank the ONVIF profile and the stored config because some
// HiSilison-OEM cameras (e.g. IPCAM Model C6F0SoZ0N0PpL2) advertise H264 in their
// ONVIF GetProfiles response while actually streaming H.265. Trusting the ONVIF
// declaration creates an H264Recorder that waits forever for an H.264 SPS NAL and
// never gets a frame. The real stream is the ground truth.
func (r *ONVIFRecorder) detectEncoding(ctx context.Context) string {
	// Gather the "claimed" encoding (config first, then ONVIF) so we can both
	// fall back to it and log a warning when it disagrees with reality.
	claimed := r.claimedEncoding(ctx)

	// recordProbe emits a codec-probe metric event (#140). result is one of:
	//   ok          — live RTSP DESCRIBE resolved the codec (authoritative)
	//   unsupported — probe empty; fell back to a claimed/default encoding (device works but wasn't verified live)
	//   fail        — probe empty AND no claim; defaulted to H264 (highest stale-encoding risk, see #112)
	recordProbe := func(encoding, result string) {
		if r.metrics == nil {
			return
		}
		r.metrics.CodecProbeTotal.WithLabelValues(r.cfg.CameraID, strings.ToLower(encoding), result).Inc()
	}

	// 1. RTSP DESCRIBE is authoritative. Some ONVIF cameras lie about their codec.
	if r.rtspURL != "" {
		probeStart := time.Now()
		probed := r.probeEncodingFn()
		if r.metrics != nil {
			r.metrics.CodecProbeDurationSeconds.WithLabelValues(r.cfg.CameraID).Observe(time.Since(probeStart).Seconds())
		}
		if probed != "" {
			if claimed != "" && !strings.EqualFold(probed, claimed) {
				onvifRecLogger.Warn("ONVIF-declared encoding disagrees with actual RTSP stream; trusting the stream",
					"camera_id", r.cfg.CameraID, "declared", claimed, "actual", probed)
			} else {
				onvifRecLogger.Info("detected encoding via RTSP DESCRIBE", "camera_id", r.cfg.CameraID, "encoding", probed)
			}
			recordProbe(probed, "ok")
			return probed
		}
	}

	// 2/3. Fall back to the claimed encoding (config or ONVIF).
	if claimed != "" {
		onvifRecLogger.Info("RTSP DESCRIBE unavailable, using claimed encoding", "camera_id", r.cfg.CameraID, "encoding", claimed)
		recordProbe(claimed, "unsupported")
		return claimed
	}

	// 4. Default to H264
	onvifRecLogger.Warn("could not detect encoding, defaulting to H264", "camera_id", r.cfg.CameraID)
	recordProbe("H264", "fail")
	return "H264"
}

// claimedEncoding returns the encoding asserted by the config and/or ONVIF profile
// metadata — i.e. everything EXCEPT the live RTSP stream. Returns "" if nothing is
// claimed. The JPEG case may overwrite r.rtspURL (see resolveJPEGEncoding).
func (r *ONVIFRecorder) claimedEncoding(ctx context.Context) string {
	// 1. Manual override from config
	if r.cfg.StreamEncoding == "H264" || r.cfg.StreamEncoding == "H265" {
		return r.cfg.StreamEncoding
	}

	// 2. ONVIF profile metadata
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
				// A JPEG (MJPEG) profile may be served over RTSP or HTTP. RTSP is
				// preferred because it enables AVI+G.711 audio recording; fall back to
				// HTTP MJPEG (video-only) if the device exposes no rtsp:// MJPEG stream.
				return r.resolveJPEGEncoding()
			}
		}
	}
	return ""
}

// resolveJPEGEncoding decides how to record a device whose ONVIF profile reports
// JPEG (MJPEG) encoding. Such devices may serve the stream over RTSP (MJPEG
// video, often with G.711 audio — recordable into AVI) or over HTTP (multipart
// MJPEG, video-only). RTSP is preferred because it enables audio capture.
//
// Start() resolved r.rtspURL via ONVIF GetStreamURI, but ESP32 RTSP-AVI firmware
// often returns the http:// preview URL there even though it ALSO serves
// rtsp://<host>:554/<same-path>. Rather than making another ONVIF call
// (GetStreamURIWithProtocol) — which frequently fails because the ESP32's tiny
// HTTP connection pool is already strained by Start's GetProfiles + GetStreamURI
// — we DERIVE the rtsp:// URL from the http:// one and probe it. Returns "MJPEG"
// (→ MJPEGRecorder over RTSP) or "JPEG" (→ HTTPJPEGRecorder).
func (r *ONVIFRecorder) resolveJPEGEncoding() string {
	candidate := r.rtspURL
	if !strings.HasPrefix(candidate, "rtsp://") {
		candidate = deriveRTSPURL(candidate)
	}
	if candidate == "" {
		return "JPEG"
	}
	if enc := probeRTSPEncodingFor(candidate, r.cfg.Username, r.cfg.Password); enc == "MJPEG" {
		onvifRecLogger.Info("JPEG device serves MJPEG over RTSP — using RTSP recorder (AVI+audio capable)", "camera_id", r.cfg.CameraID, "rtsp_url", candidate)
		r.rtspURL = candidate
		return "MJPEG"
	}
	return "JPEG"
}

// deriveRTSPURL converts an http(s):// MJPEG URL to its rtsp:// equivalent on port
// 554. ESP32 RTSP-AVI firmware serves MJPEG+G.711 at rtsp://<host>:554/<same-path>
// alongside the http:// preview, so the RTSP URL is derivable without another
// ONVIF round-trip. Returns "" if the input can't be converted.
func deriveRTSPURL(httpURL string) string {
	u, err := url.Parse(httpURL)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	u.Scheme = "rtsp"
	u.User = nil
	u.Host = u.Hostname() + ":554"
	return u.String()
}

// RewriteStaleStreamHost fixes a GetStreamUri response whose host lags behind
// the ONVIF endpoint we are actually connected to. After a DHCP reassignment the
// camera's GetStreamUri often still returns the OLD IP (e.g. rtsp://192.168.63.200
// while the ONVIF service is reachable at .199). The library's fixLocalhostURL
// corrects capability XAddrs but NOT values inside SOAP bodies like the stream
// URI, so we rewrite the host here using the known-good ONVIF endpoint.
// Port is preserved from the stream URI (RTSP commonly lives on 554, distinct
// from the ONVIF port). Returns rtspURL unchanged if either URL fails to parse
// or the hosts already agree.
// Exported for the camera manager's sub-stream resolver (#513) — same
// GetStreamUri staleness applies to secondary-profile URIs.
func RewriteStaleStreamHost(rtspURL, onvifEndpoint string) string {
	if rtspURL == "" || onvifEndpoint == "" {
		return rtspURL
	}
	stream, err := url.Parse(rtspURL)
	if err != nil {
		return rtspURL
	}
	ep, err := url.Parse(onvifEndpoint)
	if err != nil {
		return rtspURL
	}
	if stream.Hostname() == ep.Hostname() {
		return rtspURL // already consistent
	}
	// Replace only the host; keep the stream's own port.
	streamHost := ep.Hostname()
	if port := stream.Port(); port != "" {
		stream.Host = streamHost + ":" + port
	} else {
		stream.Host = streamHost
	}
	return stream.String()
}

// injectRTSPCredentials embeds userinfo into an rtsp:// URL when none is present.
// This mirrors how manually-added rtsp+mjpeg cameras carry credentials in the URL
// (e.g. rtsp://admin:admin@host/stream). ONVIF GetStreamURI returns a credential-
// less URL, but ESP32 RTSP-AVI firmware requires auth, and MJPEGRecorder has no
// separate auth fields — so we embed the creds here. Returns the original URL
// unchanged if it already has userinfo, isn't rtsp://, or no username is set.
func injectRTSPCredentials(rtspURL, username, password string) string {
	if username == "" || !strings.HasPrefix(rtspURL, "rtsp://") {
		return rtspURL
	}
	u, err := url.Parse(rtspURL)
	if err != nil || u.User != nil {
		return rtspURL
	}
	u.User = url.UserPassword(username, password)
	return u.String()
}

// probeRTSPEncoding connects to the cached RTSP stream and checks the media format.
func (r *ONVIFRecorder) probeRTSPEncoding() string {
	return probeRTSPEncodingFor(r.rtspURL, r.cfg.Username, r.cfg.Password)
}

// ProbeRTSPEncoding connects to an RTSP stream and reports its video format.
// It is the exported wrapper around probeRTSPEncodingFor for callers outside the
// recorder package (e.g. the add-camera API handler validating an ONVIF camera's
// declared encoding against the real stream). Returns "H265", "H264", "MJPEG",
// or "" if the format is unknown or the probe fails.
func ProbeRTSPEncoding(rtspURL, username, password string) string {
	return probeRTSPEncodingFor(rtspURL, username, password)
}

// probeRTSPEncodingFor connects to an RTSP stream and reports its video format.
// Returns "H265", "H264", "MJPEG", or "" if the format is unknown or the probe
// fails. MJPEG is reported as a distinct value (not the ONVIF profile string
// "JPEG") so callers can tell RTSP-served MJPEG — which can carry G.711 audio
// and record into AVI — apart from HTTP-only multipart MJPEG.
func probeRTSPEncodingFor(rtspURL, username, password string) string {
	u, err := base.ParseURL(rtspURL)
	if err != nil {
		return ""
	}
	if u.User == nil && username != "" {
		u.User = url.UserPassword(username, password)
	}
	tcp := gortsplib.ProtocolTCP
	client := &gortsplib.Client{
		Scheme:       u.Scheme,
		Host:         u.Host,
		Protocol:     &tcp,
		ReadTimeout:  10 * time.Second,
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
	var mjpegForma *format.MJPEG
	if desc.FindFormat(&mjpegForma) != nil {
		return "MJPEG"
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
		"http://" + host + ":81",
		"http://" + onvifURL.Host,
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
	// ESP32 MiBeeCam and similar minimal ONVIF devices serve MJPEG on a separate
	// port (81) from the ONVIF endpoint (80). Falling back to the ONVIF port
	// causes a 404 death-loop (commit 25f58b6 regressed this).
	// Use :81 as the fallback — probeHTTPMJPEG already tries the ONVIF port too.
	return fmt.Sprintf("http://%s:81%s", onvifURL.Hostname(), path)
}

// createDelegate creates the appropriate internal recorder based on encoding.
// It probes the encoding once (via detectEncoding, which has side effects on
// r.rtspURL when the device serves JPEG over RTSP — see resolveJPEGEncoding —
// so it must run exactly once) and stashes the result on r.resolvedEncoding so
// the camera manager can persist it via ResolvedEncoding() (issue #112).
func (r *ONVIFRecorder) createDelegate(rtspURL string) model.Recorder {
	encoding := r.detectEncoding(context.Background())
	r.mu.Lock()
	r.resolvedEncoding = encoding
	r.mu.Unlock()
	switch encoding {
	case "H265":
		cfg := H265Config{
			CameraID:             r.cfg.CameraID,
			RTSPURL:              rtspURL,
			Username:             r.cfg.Username,
			Password:             r.cfg.Password,
			SegmentDur:           r.cfg.SegmentDur,
			RingBufCap:           r.cfg.RingBufCap,
			DB:                   r.cfg.DB,
			AudioEnabled:         r.cfg.AudioEnabled,
			AudioInRecordings:    r.cfg.AudioInRecordings,
			FrameWatchdogTimeout: r.cfg.FrameWatchdogTimeout,
			EventBus:             r.cfg.EventBus,
			RecordEnabled:        r.cfg.RecordEnabled,
			Adaptive:             r.cfg.Adaptive,
			AudioTrigger:         r.cfg.AudioTrigger,
		}
		rec := NewH265Recorder(cfg, r.store, r.metrics)
		rec.Hub = r.Hub
		return rec
	case "MJPEG":
		// RTSP-served MJPEG (e.g. ESP32 MiBeeCam running the RTSP-AVI firmware).
		// Routes through MJPEGRecorder so the stream's G.711 audio is captured into
		// AVI segments when AudioEnabled. r.rtspURL was overwritten with the rtsp://
		// URL by resolveJPEGEncoding(); prefer it over the delegate param, which may
		// be an http:// URL when ONVIF GetStreamURI returned the HTTP variant.
		// Embed ONVIF credentials: ESP32 RTSP-AVI firmware requires auth (e.g.
		// admin:admin) but ONVIF GetStreamURI returns a credential-less URL, and
		// MJPEGRecorder has no separate auth fields.
		mjpegRTSPURL := r.rtspURL
		if mjpegRTSPURL == "" {
			mjpegRTSPURL = rtspURL
		}
		mjpegRTSPURL = injectRTSPCredentials(mjpegRTSPURL, r.cfg.Username, r.cfg.Password)
		mjpegCfg := MJPEGConfig{
			CameraID:      r.cfg.CameraID,
			RTSPURL:       mjpegRTSPURL,
			SegmentDur:    r.cfg.SegmentDur,
			DB:            r.cfg.DB,
			EventBus:      r.cfg.EventBus,
			AudioEnabled:  r.cfg.AudioEnabled,
			RecordEnabled: r.cfg.RecordEnabled,
		}
		mjpegRec := NewMJPEGRecorder(mjpegCfg, r.store, r.metrics)
		mjpegRec.Hub = r.Hub
		return mjpegRec

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
			CameraID:             r.cfg.CameraID,
			RTSPURL:              rtspURL,
			Username:             r.cfg.Username,
			Password:             r.cfg.Password,
			SegmentDur:           r.cfg.SegmentDur,
			RingBufCap:           r.cfg.RingBufCap,
			DB:                   r.cfg.DB,
			AudioEnabled:         r.cfg.AudioEnabled,
			AudioInRecordings:    r.cfg.AudioInRecordings,
			FrameWatchdogTimeout: r.cfg.FrameWatchdogTimeout,
			EventBus:             r.cfg.EventBus,
			RecordEnabled:        r.cfg.RecordEnabled,
			Adaptive:             r.cfg.Adaptive,
			AudioTrigger:         r.cfg.AudioTrigger,
		}
		rec := NewH264Recorder(cfg, r.store, r.metrics)
		rec.Hub = r.Hub
		return rec
	}
}

// newHTTPJPEGRecorder creates an HTTPJPEGRecorder with the given URL.
func (r *ONVIFRecorder) newHTTPJPEGRecorder(httpURL string) model.Recorder {
	cfg := HTTPJPEGConfig{
		CameraID:      r.cfg.CameraID,
		URL:           httpURL,
		SegmentDur:    r.cfg.SegmentDur,
		Username:      r.cfg.Username,
		Password:      r.cfg.Password,
		DB:            r.cfg.DB,
		EventBus:      r.cfg.EventBus,
		AVI:           r.cfg.AVI,
		RecordEnabled: r.cfg.RecordEnabled,
	}
	rec := NewHTTPJPEGRecorder(cfg, r.store, r.metrics)
	rec.Hub = r.Hub
	return rec
}

// resolveProfileToken returns the configured profile token or auto-selects the
// best (highest-resolution) profile from the ONVIF device.
func (r *ONVIFRecorder) resolveProfileToken() string {
	if r.cfg.ProfileToken != "" {
		return r.cfg.ProfileToken
	}
	profiles, err := r.onvifClient.GetProfiles(context.Background())
	if err != nil || len(profiles) == 0 {
		return ""
	}
	return onvif.SelectMainProfile(profiles)
}

// profileByToken returns the profile matching token, or the zero value.
func profileByToken(profiles []onvif.DeviceProfile, token string) onvif.DeviceProfile {
	for _, p := range profiles {
		if p.Token == token {
			return p
		}
	}
	if len(profiles) > 0 {
		return profiles[0]
	}
	return onvif.DeviceProfile{}
}

// formatRes renders "WxH" for a profile (or "" when unset).
func formatRes(p onvif.DeviceProfile) string {
	if p.Width <= 0 || p.Height <= 0 {
		return ""
	}
	return fmt.Sprintf("%dx%d", p.Width, p.Height)
}
