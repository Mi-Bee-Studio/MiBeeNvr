// SPDX-License-Identifier: MIT
//
// Xiaomi camera recorder implementing model.Recorder and model.HLSProvider interfaces.
// Connects via MISS protocol, probes codec (H264/H265), records to MP4 segments.

package xiaomi

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
)

var xiaomiLogger = slog.Default().With("component", "xiaomi-recorder")

// SegmentStore abstracts storage operations needed by the recorder.
type SegmentStore interface {
	CreateSegment(cameraID string, format string) (tempPath string, finalPath string, err error)
	CloseSegment(tempPath, finalPath string) error
}

// RecordingDB abstracts database operations needed by the recorder.
type RecordingDB interface {
	InsertRecording(ctx context.Context, r *model.Recording) error
	InsertRecordingWithRetry(ctx context.Context, r *model.Recording, maxRetries int, backoff time.Duration) error
}

// HealthEventWriter persists camera health events (camera_health_events
// table). *storage.DB satisfies it; wired via XiaomiRecorderConfig.HealthDB
// so quality transitions land in the health event timeline (issue #502).
type HealthEventWriter interface {
	InsertHealthEvent(ctx context.Context, event model.HealthEvent) error
}

// ErrorReporter abstracts camera error reporting to avoid circular imports.
// CameraManager satisfies this interface.
type ErrorReporter interface {
	SetErrorDetail(cameraID string, detail *model.CameraErrorDetail)
}

const (
	defaultSegmentDur = 10 * time.Minute

	// Quality auto-fallback/restore tuning (issue #502).
	// A connection that streamed at least qualityStableResetWindow before
	// dying counts as "stable" — it resets the no-media failure sequence, so
	// three failures spread across stable periods never trigger a downgrade
	// (defect A: the old counter accumulated across weeks).
	qualityStableResetWindow = 5 * time.Minute
	// After a downgrade, an SD connection that streams stably for at least
	// qualityUpgradeStableWindow earns one probe attempt back at HD
	// (defect B: quality previously never recovered without a restart).
	qualityUpgradeStableWindow = 10 * time.Minute
	// maxQualityUpgradeAttempts bounds the downgrade→upgrade cycle per
	// recorder lifecycle, preventing a downgrade/upgrade oscillation storm
	// (the 2K PTZ camera logged 131 SPS changes in one day — the recovery
	// must not feed that).
	maxQualityUpgradeAttempts = 2
)

// errQualityUpgradeProbe is returned by connectAndRecord when a stable SD
// connection reaches the upgrade window and should be deliberately reconnected
// at HD (issue #502 defect B). It is a planned reconnect, not a failure —
// the run loop skips the error metrics/backoff for it.
var errQualityUpgradeProbe = errors.New("quality upgrade probe")

// XiaomiCloudConfig holds Xiaomi cloud API credentials for URL resolution.
type XiaomiCloudConfig struct {
	UserID string
	Token  string
	Region string
}

// XiaomiRecorderConfig holds configuration for the Xiaomi recorder.
type XiaomiRecorderConfig struct {
	CameraID          string
	DID               string            // Device ID extracted from xiaomi:// URL (e.g. "655448418")
	Model             string            // Camera model (e.g. ModelC200, ModelC300)
	CloudCfg          XiaomiCloudConfig // Cloud API credentials for MISS URL resolution
	SegmentDur        time.Duration
	DB                RecordingDB
	HealthDB          HealthEventWriter // Optional: persists quality-change health events (issue #502)
	ErrReporter       ErrorReporter     // Optional: reports detailed errors (e.g. TUTK incompatibility)
	AudioEnabled      bool          // Capture and broadcast audio via StreamHub when true
	AudioInRecordings bool          // Keep the audio track in recorded segments (default off)
	IdleTimeout       time.Duration
	Channel           string // Xiaomi dual-lens channel ("" or "0" = main, "1" = secondary)
	Quality           string // Stream quality: "" or "auto" (HD→SD fallback), "hd", "sd"
	EventBus          *event.EventBus
	// RecordEnabled gates disk writes. nil or true = record normally;
	// false = "live-only" mode — the stream stays connected (for live preview /
	// HLS) but no segments are written to disk. Matches baseRecorder.RecordEnabled
	// semantics so recording_enabled=false takes effect on Xiaomi cameras.
	RecordEnabled *bool
	// Adaptive enables dynamic-timelapse write density (issue #435; wired for
	// Xiaomi cameras in issue #468 — previously recording_mode: adaptive was
	// silently ignored by this recorder). Nil = plain continuous recording.
	Adaptive *recorder.AdaptiveConfig
	// AudioTrigger arms loudness-triggered recording (issue #478) on top of
	// Adaptive. G.711 (PCMU/PCMA) cameras only — Xiaomi Opus audio has no
	// pure-Go decoder, those cameras log the trigger as inactive.
	AudioTrigger *recorder.AudioTriggerConfig
}

// XiaomiRecorder records H.264/H.265 video from a Xiaomi camera via MISS protocol.
type XiaomiRecorder struct {
	cfg     XiaomiRecorderConfig
	store   SegmentStore
	metrics *metrics.Metrics

	mu     sync.Mutex
	status model.RecorderStatus
	cancel context.CancelFunc
	done   chan struct{}

	muxer         *muxer.MP4Muxer
	trackID       int
	audioTrackID  int
	curFinalPath  string
	curTempPath   string
	segStart      time.Time
	frameCount    int
	lastFrameTime time.Time

	// Codec state (probed from first packets)
	codec   model.Format // "h264" or "h265"
	sps     []byte
	pps     []byte
	vps     []byte // H265 only
	codecOK bool   // true once codec type is determined

	// Audio state (probed from first audio packet)
	audioCodecID uint32 // MISS codec ID for audio (0 = not detected yet)

	Hub         *model.StreamHub // Frame fan-out to multiple consumers (HLS, WebRTC, etc.)
	streamStart time.Time        // For PTS rebase (used by forwardHLS)

	currentQuality   string // effective quality for next connectAndRecord attempt
	noMediaFailCount int    // no-media failures since the last stable-streaming window (issue #502: true consecutive semantics)
	connectFailCount int    // consecutive connect/handshake failures (miss connect i/o timeout etc.)
	lastMissURL      string // last resolved MISS URL (for error messages naming the unreachable host)

	// Quality state machine (issue #502). Owned by the run goroutine.
	mediaStart      time.Time // when StartMedia succeeded for the current connection; zero while connecting
	upgradeAttempts int       // SD→HD probe attempts consumed this recorder lifecycle
	// Test-overridable tuning (defaults from the consts above).
	stableResetWindow   time.Duration
	upgradeStableWindow time.Duration
	maxUpgradeAttempts  int

	// MISS client reference for external commands (MotorControl, GetDeviceInfo).
	missClient *MISSClient
	missMu     sync.Mutex

	// Adaptive write-density gate (issue #435/#468). Rebuilt per connection in
	// connectAndRecord so a reconnect always starts in NORMAL mode; nil when
	// adaptive recording is not configured. Owned by the video NALU processing
	// path.
	// Adaptive write-density gate (issue #435/#468) — nil in continuous mode.
	adaptive *recorder.AdaptiveGate
	// audioTrig is the audio-trigger runtime (issue #478), rebuilt per
	// connection alongside the gate. nil unless Adaptive is armed AND
	// AudioTrigger is enabled.
	audioTrig *recorder.AudioTriggerRuntime
	// opusTrigWarned suppresses the per-packet "Opus unsupported" warning.
	opusTrigWarned bool
	// audioSparse gates DISK writes of audio while the adaptive gate is in
	// sparse (timelapse) mode; live audio broadcast continues.
	audioSparse atomic.Bool
}

// Interface compliance check.
var _ model.Recorder = (*XiaomiRecorder)(nil)

// GetHub returns the StreamHub for frame fan-out.
func (r *XiaomiRecorder) GetHub() *model.StreamHub { return r.Hub }

// SPS returns the current H.264/H.265 SPS NAL unit (without start bytes).
func (r *XiaomiRecorder) SPS() []byte { return r.sps }

// PPS returns the current H.264/H.265 PPS NAL unit (without start bytes).
func (r *XiaomiRecorder) PPS() []byte { return r.pps }

// VPS returns the current H.265 VPS NAL unit (without start bytes), or nil for H.264.
func (r *XiaomiRecorder) VPS() []byte { return r.vps }

// AudioCodec returns the audio codec name ("g711" or "" for no audio).
func (r *XiaomiRecorder) AudioCodec() string {
	c, ok := missCodecToAudio(r.audioCodecID)
	if !ok {
		return ""
	}
	return string(c)
}

// AudioConfig returns the audio codec config. G.711 returns nil (no ASC).
func (r *XiaomiRecorder) AudioConfig() []byte { return nil }

// AudioSampleRate returns the audio sample rate. G.711 defaults to 8 kHz.
func (r *XiaomiRecorder) AudioSampleRate() int {
	if _, ok := missCodecToAudio(r.audioCodecID); ok {
		return 8000
	}
	return 0
}

// AudioChannels returns the number of audio channels. G.711 is mono.
func (r *XiaomiRecorder) AudioChannels() int {
	if _, ok := missCodecToAudio(r.audioCodecID); ok {
		return 1
	}
	return 0
}

var (
	_ model.Recorder    = (*XiaomiRecorder)(nil)
	_ model.HLSProvider = (*XiaomiRecorder)(nil)
)

// NewXiaomiRecorder creates a new Xiaomi MISS protocol recorder.
func NewXiaomiRecorder(cfg XiaomiRecorderConfig, store SegmentStore, opts ...*metrics.Metrics) *XiaomiRecorder {
	var m *metrics.Metrics
	if len(opts) > 0 {
		m = opts[0]
	}
	if cfg.SegmentDur == 0 {
		cfg.SegmentDur = defaultSegmentDur
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = defaultIdleTimeout
	}
	return &XiaomiRecorder{
		cfg:                 cfg,
		store:               store,
		metrics:             m,
		status:              model.StatusStopped,
		stableResetWindow:   qualityStableResetWindow,
		upgradeStableWindow: qualityUpgradeStableWindow,
		maxUpgradeAttempts:  maxQualityUpgradeAttempts,
	}
}

// SetErrorReporter sets the error reporter for vendor error reporting.
func (r *XiaomiRecorder) SetErrorReporter(reporter ErrorReporter) {
	r.cfg.ErrReporter = reporter
}

// Start begins recording from the Xiaomi camera in a background goroutine.
func (r *XiaomiRecorder) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == model.StatusRecording || r.status == model.StatusReconnecting {
		return fmt.Errorf("recorder for %q already running", r.cfg.CameraID)
	}
	ctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	r.done = make(chan struct{})
	r.status = model.StatusRecording
	r.incActive()
	r.streamStart = time.Now() // Set PTS base for HLS — only once per Start() lifecycle
	go r.run(ctx)
	return nil
}

// Stop terminates the recording goroutine and waits for it to finish.
func (r *XiaomiRecorder) Stop() error {
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
func (r *XiaomiRecorder) Status() model.RecorderStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

func (r *XiaomiRecorder) setStatus(s model.RecorderStatus) {
	r.mu.Lock()
	r.status = s
	r.mu.Unlock()
}

// CodecParams returns the current codec parameters detected from the stream.
// Implements model.HLSProvider.
func (r *XiaomiRecorder) CodecParams() (codec model.Format, sps, pps, vps []byte) {
	return r.codec, r.sps, r.pps, r.vps
}

// incActive increments the active recordings gauge if metrics is available.
func (r *XiaomiRecorder) incActive() {
	if r.metrics != nil {
		r.metrics.ActiveRecordings.Inc()
	}
}

// decActive decrements the active recordings gauge if metrics is available.
func (r *XiaomiRecorder) decActive() {
	if r.metrics != nil {
		r.metrics.ActiveRecordings.Dec()
	}
}

// recordSegmentCreated increments the segments created counter if metrics is available.
func (r *XiaomiRecorder) recordSegmentCreated() {
	if r.metrics != nil {
		r.metrics.SegmentsCreated.WithLabelValues(r.cfg.CameraID, string(r.codec)).Inc()
	}
}

// recordBytes adds to the recording bytes counter if metrics is available.
func (r *XiaomiRecorder) recordBytes(b int64) {
	if r.metrics != nil {
		r.metrics.RecordingBytesTotal.WithLabelValues(r.cfg.CameraID, string(r.codec)).Add(float64(b))
	}
}

// recordError increments the camera errors counter if metrics is available.
func (r *XiaomiRecorder) recordError(errorType string) {
	if r.metrics != nil {
		r.metrics.CameraErrors.WithLabelValues(r.cfg.CameraID, errorType).Inc()
	}
}

// classifyDisconnectReason maps an error to a disconnect reason label.
func classifyDisconnectReason(err error) string {
	if err == nil {
		return "network"
	}
	msg := err.Error()
	if strings.Contains(msg, "no data") {
		return "idle_timeout"
	}
	if strings.Contains(msg, "EOF") || strings.Contains(msg, "connection closed") {
		return "eof"
	}
	if strings.Contains(msg, "cloud") || strings.Contains(msg, "resolve") {
		return "cloud_resolve"
	}
	return "network"
}

// recordXiaomiDisconnect increments the Xiaomi disconnect counter if metrics is available.
func (r *XiaomiRecorder) recordXiaomiDisconnect(reason string) {
	if r.metrics != nil && r.metrics.XiaomiDisconnects != nil {
		r.metrics.XiaomiDisconnects.WithLabelValues(r.cfg.CameraID, reason).Inc()
	}
}

// recordXiaomiReconnect increments the Xiaomi reconnect counter if metrics is available.
func (r *XiaomiRecorder) recordXiaomiReconnect() {
	if r.metrics != nil && r.metrics.XiaomiReconnects != nil {
		r.metrics.XiaomiReconnects.WithLabelValues(r.cfg.CameraID).Inc()
	}
}

// reportVendorError checks if the error indicates an unsupported TUTK vendor
// and, if so, reports a detailed CameraErrorDetail via the ErrorReporter.
// This fires on every reconnect attempt so the frontend always has current state.
func (r *XiaomiRecorder) reportVendorError(err error) {
	if r.cfg.ErrReporter == nil || err == nil {
		return
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "unsupported vendor") {
		return
	}
	// Extract vendor name from error: "miss: unsupported vendor \"foo\"".
	// Note: as of v0.9.0 both "cs2" and "tutk" are supported, so this only fires
	// for genuinely unknown vendors — the message is intentionally generic.
	vendor := extractQuotedValue(errMsg)
	msg := fmt.Sprintf("Camera uses an unsupported transport vendor %q. This camera model may not be compatible.", vendor)
	r.cfg.ErrReporter.SetErrorDetail(r.cfg.CameraID, &model.CameraErrorDetail{
		Type:       "tutk_incompatible",
		Message:    msg,
		DetectedAt: time.Now(),
	})
}

// reportConnectError surfaces an actionable CameraErrorDetail when the recorder
// repeatedly fails to reach the camera (e.g. UDP handshake timeout to the LAN IP,
// which looks like "miss connect: read udp i/o timeout"). Without this, the user
// sees "reconnecting" forever with no hint that the camera is unreachable on the
// network — issue #48. After connectFailThreshold consecutive failures, we set a
// "connect_failed" detail so the frontend can show guidance ("check the camera is
// online and on the same network") instead of a bare spinner.
const connectFailThreshold = 5

func (r *XiaomiRecorder) reportConnectError(err error) {
	if r.cfg.ErrReporter == nil || err == nil {
		return
	}
	r.connectFailCount++
	if r.connectFailCount < connectFailThreshold {
		return
	}
	// Extract the host being dialed from the MISS URL for the message.
	host := r.cfg.DID
	if u, perr := url.Parse(r.lastMissURL); perr == nil && u.Host != "" {
		host = u.Host
	}
	msg := fmt.Sprintf(
		"Cannot reach the camera at %s after %d connection attempts. "+
			"Check that the camera is powered on, connected to the same network as the NVR, "+
			"and not blocked by a firewall. (Last error: %s)",
		host, r.connectFailCount, err.Error(),
	)
	r.cfg.ErrReporter.SetErrorDetail(r.cfg.CameraID, &model.CameraErrorDetail{
		Type:       "connect_failed",
		Message:    msg,
		DetectedAt: time.Now(),
	})
}

// clearConnectError resets the connect-failure counter and clears any
// "connect_failed" error detail when the recorder successfully connects.
func (r *XiaomiRecorder) clearConnectError() {
	if r.connectFailCount > 0 {
		r.connectFailCount = 0
	}
	if r.cfg.ErrReporter != nil {
		r.cfg.ErrReporter.SetErrorDetail(r.cfg.CameraID, nil)
	}
}

// extractQuotedValue extracts the content between the first pair of double quotes in s.
// Returns empty string if no quotes are found.
func extractQuotedValue(s string) string {
	start := strings.Index(s, `"`)
	if start < 0 {
		return ""
	}
	end := strings.Index(s[start+1:], `"`)
	if end < 0 {
		return ""
	}
	return s[start+1 : start+1+end]
}

// run is the main reconnect loop.
func (r *XiaomiRecorder) run(ctx context.Context) {
	defer func() {
		if panicErr := recover(); panicErr != nil {
			buf := make([]byte, 4096)
			buf = buf[:runtime.Stack(buf, false)]
			xiaomiLogger.Error("PANIC recovered in run", "camera_id", r.cfg.CameraID, "panic", panicErr, "stack", string(buf))
		}
	}()
	defer close(r.done)
	defer r.setStatus(model.StatusStopped)

	// Initialize quality for auto-fallback.
	// "auto" or "" starts at HD; downgrades to SD after 3 no-media failures.
	// Ported from go2rtc: subtype URL parameter + issue #2114 (isa.camera.hlc8 needs SD).
	r.currentQuality = r.cfg.Quality
	if r.currentQuality == "" || r.currentQuality == "auto" {
		r.currentQuality = "hd"
	}

	var retryCount int
	for {
		// Wake up battery-powered cateye/doorbell cameras before resolving URL.
		// Ported from go2rtc internal/xiaomi/xiaomi.go.
		if strings.Contains(r.cfg.Model, ".cateye.") {
			if wakeErr := WakeUpCamera(r.cfg.CloudCfg, r.cfg.DID); wakeErr != nil {
				xiaomiLogger.Warn("failed to wake up cateye camera", "camera_id", r.cfg.CameraID, "error", wakeErr)
			}
		}

		// Resolve xiaomi://deviceID to miss://... URL via cloud API.
		missURL, resolvedModel, err := ResolveMISSURL(r.cfg.CloudCfg, r.cfg.DID, r.cfg.Model)
		// Backfill the camera model from the cloud device list on first
		// resolve — CameraConfig has no model field, so without this the
		// quality/downgrade logs printed model="" (issue #502).
		if r.cfg.Model == "" && resolvedModel != "" {
			r.cfg.Model = resolvedModel
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			retryCount++
			backoff := recorder.TieredBackoffWithJitter(retryCount)
			r.reportVendorError(err)
			xiaomiLogger.Error("failed to resolve MISS URL, retrying", "camera_id", r.cfg.CameraID, "error", err, "backoff", backoff, "attempt", retryCount)
			r.recordError("cloud_resolve")
			r.recordXiaomiDisconnect("cloud_resolve")
			r.setStatus(model.StatusReconnecting)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			continue
		}

		r.lastMissURL = missURL
		err, connected := r.connectAndRecord(ctx, missURL)
		if ctx.Err() != nil {
			return
		}
		if connected {
			retryCount = 0
			r.connectFailCount = 0
			r.recordXiaomiReconnect()
		}

		// Planned SD→HD upgrade probe (issue #502 defect B): a stable SD
		// connection earned a reconnect at HD. Not a failure — reconnect
		// immediately, skip the error metrics and backoff below.
		if errors.Is(err, errQualityUpgradeProbe) {
			r.currentQuality = "hd"
			r.upgradeAttempts++
			r.recordQualityChange("sd", "hd", fmt.Sprintf(
				"stable SD streaming for %s, probe attempt %d/%d",
				r.upgradeStableWindow, r.upgradeAttempts, r.maxUpgradeAttempts))
			r.setStatus(model.StatusReconnecting)
			continue
		}

		// Quality auto-fallback: if "no media data" errors persist at HD,
		// downgrade to SD. Some models (isa.camera.hlc8) silently refuse HD
		// streaming — go2rtc issue #2114. A connection that streamed stably
		// before dying resets the failure sequence (issue #502 defect A).
		streamedStable := !r.mediaStart.IsZero() && time.Since(r.mediaStart) >= r.stableResetWindow
		r.handleQualityFailure(err, streamedStable)
		retryCount++
		// Track persistent connect failures and surface an actionable error to
		// the user after the threshold (issue #48: "reconnecting" forever with no
		// hint the camera is unreachable). Also lengthen the backoff once the
		// failure is clearly persistent, to calm the retry storm.
		r.reportConnectError(err)
		backoff := recorder.TieredBackoffWithJitter(retryCount)
		if r.connectFailCount >= connectFailThreshold {
			backoff = recorder.StorageBackoffWithJitter() // ~60s — the camera isn't coming back soon
		}
		xiaomiLogger.Error("connection error, reconnecting", "camera_id", r.cfg.CameraID, "error", err, "backoff", backoff, "attempt", retryCount, "connect_failures", r.connectFailCount)
		r.recordError("connection")
		r.recordXiaomiDisconnect(classifyDisconnectReason(err))
		r.setStatus(model.StatusReconnecting)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

// qualityAuto reports whether the auto HD↔SD state machine is armed (quality
// unset or "auto"). Explicit "hd"/"sd" pins the quality — no transitions.
func (r *XiaomiRecorder) qualityAuto() bool {
	return r.cfg.Quality == "" || r.cfg.Quality == "auto"
}

// handleQualityFailure updates the no-media failure state after a connection
// ended with err (issue #502 defect A). streamedStable means the connection
// streamed at least stableResetWindow before dying — that counts as a stable
// period and resets the failure sequence, so three failures separated by
// stable periods never trigger a downgrade. Only failures WITHOUT a stable
// period between them (rapid reconnect cycles) accumulate to the threshold.
func (r *XiaomiRecorder) handleQualityFailure(err error, streamedStable bool) {
	if streamedStable {
		r.noMediaFailCount = 0
	}
	if err == nil || !strings.Contains(err.Error(), "no media data") {
		return
	}
	r.noMediaFailCount++
	if r.noMediaFailCount < 3 || r.currentQuality != "hd" || !r.qualityAuto() {
		return
	}
	r.currentQuality = "sd"
	r.noMediaFailCount = 0
	r.recordQualityChange("hd", "sd", fmt.Sprintf(
		"%d consecutive no-media failures without a ≥%s stable window",
		3, r.stableResetWindow))
}

// shouldProbeUpgrade reports whether the current SD connection has streamed
// stably long enough to earn one bounded SD→HD probe attempt (issue #502
// defect B). Called from the read loop; the probe itself is a deliberate
// teardown + reconnect at HD, bounded by maxUpgradeAttempts per recorder
// lifecycle so a camera that refuses HD cannot oscillate forever.
func (r *XiaomiRecorder) shouldProbeUpgrade(now time.Time) bool {
	return r.currentQuality == "sd" && r.qualityAuto() &&
		r.upgradeAttempts < r.maxUpgradeAttempts &&
		!r.mediaStart.IsZero() &&
		now.Sub(r.mediaStart) >= r.upgradeStableWindow
}

// recordQualityChange logs a quality transition and records it in the health
// event stream — both the camera_health_events table (via HealthDB, wired in
// plugin.go) and the camera.quality SSE topic (issue #502: the M5 incident
// showed downgrades were invisible outside grep).
func (r *XiaomiRecorder) recordQualityChange(from, to, reason string) {
	cameraModel := r.cfg.Model
	if cameraModel == "" {
		cameraModel = r.cfg.DID
	}
	if to == "sd" {
		xiaomiLogger.Warn("auto-downgrading quality HD→SD after repeated no-media failures",
			"camera_id", r.cfg.CameraID, "reason", reason, "model", cameraModel)
	} else {
		xiaomiLogger.Info("auto-upgrading quality SD→HD after stable streaming",
			"camera_id", r.cfg.CameraID, "reason", reason, "model", cameraModel)
	}

	status := model.HealthStatusHealthy
	if to == "sd" {
		status = model.HealthStatusWarning
	}
	meta, _ := json.Marshal(map[string]string{"from": from, "to": to, "model": cameraModel})
	if r.cfg.HealthDB != nil {
		evt := model.HealthEvent{
			CameraID:  r.cfg.CameraID,
			EventType: string(model.HealthEventQualityChanged),
			Status:    string(status),
			Message:   fmt.Sprintf("stream quality %s→%s: %s", from, to, reason),
			Metadata:  string(meta),
			CreatedAt: time.Now(),
		}
		if err := r.cfg.HealthDB.InsertHealthEvent(context.Background(), evt); err != nil {
			xiaomiLogger.Warn("failed to record quality-change health event",
				"camera_id", r.cfg.CameraID, "error", err)
		}
	}
	if r.cfg.EventBus != nil {
		r.cfg.EventBus.Publish(context.Background(), event.TopicCameraQuality, map[string]any{
			"camera_id": r.cfg.CameraID,
			"from":      from,
			"to":        to,
			"reason":    reason,
			"model":     cameraModel,
		})
	}
}

// connectAndRecord connects to the Xiaomi camera, starts media, and records packets.
func (r *XiaomiRecorder) connectAndRecord(ctx context.Context, missURL string) (error, bool) {
	// Zero until StartMedia succeeds — a failed connect must not inherit the
	// previous connection's stable-streaming window (issue #502).
	r.mediaStart = time.Time{}
	client, err := NewMISSClient(missURL, r.cfg.IdleTimeout)
	if err != nil {
		return fmt.Errorf("miss connect: %w", err), false
	}
	defer func() {
		client.Conn.Close()
		r.missMu.Lock()
		r.missClient = nil
		r.missMu.Unlock()
	}()

	// Save client reference for external commands.
	r.missMu.Lock()
	r.missClient = client
	r.missMu.Unlock()
	// Start video stream using current quality (HD default, auto-downgrades to SD).
	// Channel is used for dual-lens cameras.
	if err := client.StartMedia(r.cfg.Channel, r.currentQuality, r.cfg.AudioEnabled); err != nil {
		return fmt.Errorf("miss start media: %w", err), false
	}
	defer func() {
		_ = client.StopMedia()
	}()

	// Media confirmed flowing — the stable-streaming window for the quality
	// state machine starts here (issue #502).
	r.mediaStart = time.Now()

	r.setStatus(model.StatusRecording)
	// Clear any previously-reported connect-failure detail now that we're live.
	r.clearConnectError()

	// Reset codec probe state for each new connection.
	r.codecOK = false
	r.sps = nil
	r.pps = nil
	r.vps = nil
	r.audioCodecID = 0
	// Rebuild the adaptive gate per connection: a reconnect always starts in
	// NORMAL mode with a fresh activity baseline (a reconnect storm can't
	// oscillate the mode). The audio-trigger runtime follows the gate.
	if r.cfg.Adaptive != nil {
		r.adaptive = recorder.NewAdaptiveGate(*r.cfg.Adaptive, r.cfg.CameraID, xiaomiLogger)
		r.audioSparse.Store(false)
		if r.cfg.AudioTrigger != nil && r.cfg.AudioTrigger.Enabled {
			r.audioTrig = recorder.NewAudioTriggerRuntime(*r.cfg.AudioTrigger, r.cfg.CameraID, xiaomiLogger)
		} else {
			r.audioTrig = nil
		}
	} else {
		r.adaptive = nil
		r.audioTrig = nil
	}

	var lastTimestamp uint64

	for {
		select {
		case <-ctx.Done():
			r.closeCurrentSegment()
			return ctx.Err(), true
		default:
		}

		pkt, err := client.ReadPacket()
		if err != nil {
			r.closeCurrentSegment()
			return fmt.Errorf("miss read: %w", err), false
		}

		// SD→HD upgrade probe (issue #502 defect B): a healthy SD connection
		// never disconnects on its own, so the recovery must tear it down
		// deliberately once the stable window has been earned.
		if r.shouldProbeUpgrade(time.Now()) {
			r.closeCurrentSegment()
			return errQualityUpgradeProbe, true
		}

		// Handle audio packets when AudioEnabled.
		if pkt.CodecID >= 1024 {
			if r.cfg.AudioEnabled {
				r.forwardAudio(pkt.CodecID, pkt.Payload)
			}
			continue
		}

		// Skip other non-video packets.
		if pkt.CodecID != missCodecH264 && pkt.CodecID != missCodecH265 {
			continue
		}

		// Probe codec type from first video packet.
		if !r.codecOK {
			switch pkt.CodecID {
			case missCodecH264:
				r.codec = model.FormatH264
			case missCodecH265:
				r.codec = model.FormatH265
			}
			r.codecOK = true
			xiaomiLogger.Info("codec detected", "camera_id", r.cfg.CameraID, "codec", r.codec)
		}

		// Parse Annex B NALUs from payload and process each one.
		nalus := splitAnnexBNALUs(pkt.Payload)
		for _, nalu := range nalus {
			r.processNALU(nalu, pkt.Timestamp, &lastTimestamp)
		}
	}
}

// processNALU handles a single NALU extracted from Annex B payload.
func (r *XiaomiRecorder) processNALU(nalu []byte, timestamp uint64, lastTimestamp *uint64) {
	if len(nalu) == 0 {
		return
	}

	switch r.codec {
	case model.FormatH264:
		r.processH264NALU(nalu, timestamp, lastTimestamp)
	case model.FormatH265:
		r.processH265NALU(nalu, timestamp, lastTimestamp)
	}
}

// recordDisabled reports whether this recorder is in live-only mode (no disk
// writes). Mirrors baseRecorder.RecordEnabled semantics: nil/true = record,
// false = live-only.
func (r *XiaomiRecorder) recordDisabled() bool {
	return r.cfg.RecordEnabled != nil && !*r.cfg.RecordEnabled
}

// processH264NALU handles an H.264 NAL unit.
func (r *XiaomiRecorder) processH264NALU(nalu []byte, timestamp uint64, lastTimestamp *uint64) {
	naluType := nalu[0] & 0x1F
	switch naluType {
	case 7: // SPS
		if r.sps != nil && !bytes.Equal(r.sps, nalu) {
			xiaomiLogger.Info("SPS change detected, rotating segment", "camera_id", r.cfg.CameraID)
			r.closeCurrentSegment()
		}
		r.sps = append([]byte(nil), nalu...)
		return
	case 8: // PPS
		if r.pps != nil && !bytes.Equal(r.pps, nalu) {
			xiaomiLogger.Info("PPS change detected, rotating segment", "camera_id", r.cfg.CameraID)
			r.closeCurrentSegment()
		}
		r.pps = append([]byte(nil), nalu...)
		return
	}

	// Only write video frames (IDR=5, non-IDR=1).
	if naluType != 5 && naluType != 1 {
		return
	}
	if r.sps == nil || r.pps == nil {
		return
	}

	// Live-only mode: keep the stream alive for live preview (forwardHLS) but
	// skip all disk writes (segment creation + WriteSample + rotation).
	if r.recordDisabled() {
		r.forwardHLS(nalu)
		return
	}

	// Wait for IDR frame before starting a new segment.
	if r.muxer == nil && naluType != 5 {
		return
	}

	// Adaptive write-density gate (issue #435/#468) — see processH265NALU.
	if r.adaptive != nil {
		now := time.Now()
		isIDR := naluType == 5
		_, skip, flush := r.adaptive.Observe(nalu, isIDR, now)
		// Ambient-audio cameras keep the disk audio track through sparse mode
		// (#496); the merge renders the ambient span into the atmosphere bed.
		r.audioSparse.Store(r.adaptive.Timelapse() && !r.cfg.Adaptive.AmbientAudio)
		if len(flush) > 0 {
			r.writeFlushedGOP(flush)
			// Pre-trigger audio back-fill (issue #478), mirroring the built-in
			// recorders: write the retained unwritten ring samples so the
			// segment carries pre_capture seconds of sound before the trigger.
			r.flushAudioRing()
		}
		if skip {
			r.forwardHLS(nalu)
			return
		}
	}

	if r.muxer == nil {
		tempPath, finalPath, err := r.store.CreateSegment(r.cfg.CameraID, string(model.FormatH264))
		if err != nil {
			xiaomiLogger.Error("failed to create segment", "camera_id", r.cfg.CameraID, "error", err)
			return
		}
		r.muxer = muxer.NewMP4Muxer(tempPath)
		trackID, err := r.muxer.AddH264Track(r.sps, r.pps)
		if err != nil {
			xiaomiLogger.Error("failed to add H264 track", "camera_id", r.cfg.CameraID, "error", err)
			r.muxer = nil
			os.Remove(tempPath)
			return
		}
		r.trackID = trackID

		// Add audio track only when audio is enabled AND the camera keeps
		// audio in recordings (default off — #496 follow-up, issue #520).
		// Live audio and the audio trigger read the pre-disk path and do not
		// need the track.
		if r.cfg.AudioEnabled && r.cfg.AudioInRecordings && r.audioCodecID > 0 {
			codec, cfg, ok := buildAudioMuxerConfig(r.audioCodecID)
			if ok {
				aID, err := r.muxer.AddAudioTrack(codec, cfg)
				if err != nil {
					xiaomiLogger.Debug("audio track not added to muxer", "camera_id", r.cfg.CameraID, "error", err)
				} else {
					r.audioTrackID = aID
				}
			}
		}
		r.curTempPath = tempPath
		r.curFinalPath = finalPath
		r.segStart = time.Now()
		r.lastFrameTime = r.segStart
		r.frameCount = 0
	}

	now := time.Now()
	pts := now.Sub(r.segStart)
	duration := now.Sub(r.lastFrameTime)
	if duration < time.Millisecond {
		duration = time.Millisecond
	}
	r.lastFrameTime = now

	if err := r.muxer.WriteSample(r.trackID, nalu, pts, duration); err != nil {
		xiaomiLogger.Error("failed to write sample", "camera_id", r.cfg.CameraID, "error", err)
		return
	}
	r.frameCount++
	if r.adaptive != nil {
		// Frame now on disk; a later flush into this segment must skip it (#473).
		r.adaptive.MarkLastWritten()
	}

	// Forward to HLS (non-blocking)
	r.forwardHLS(nalu)

	if time.Since(r.segStart) >= r.cfg.SegmentDur {
		r.closeCurrentSegment()
	}
}

// processH265NALU handles an H.265/HEVC NAL unit.
func (r *XiaomiRecorder) processH265NALU(nalu []byte, timestamp uint64, lastTimestamp *uint64) {
	// HEVC NALU type: 2-byte header, type is in bits 1-6 of first byte.
	naluType := (nalu[0] >> 1) & 0x3F
	switch naluType {
	case 32: // VPS
		if r.vps != nil && !bytes.Equal(r.vps, nalu) {
			xiaomiLogger.Info("VPS change detected, rotating segment", "camera_id", r.cfg.CameraID)
			r.closeCurrentSegment()
		}
		r.vps = append([]byte(nil), nalu...)
		return
	case 33: // SPS
		if r.sps != nil && !bytes.Equal(r.sps, nalu) {
			xiaomiLogger.Info("SPS change detected, rotating segment", "camera_id", r.cfg.CameraID)
			r.closeCurrentSegment()
		}
		r.sps = append([]byte(nil), nalu...)
		return
	case 34: // PPS
		if r.pps != nil && !bytes.Equal(r.pps, nalu) {
			xiaomiLogger.Info("PPS change detected, rotating segment", "camera_id", r.cfg.CameraID)
			r.closeCurrentSegment()
		}
		r.pps = append([]byte(nil), nalu...)
		return
	}

	// Only write VCL NALUs (types 0-31). Non-VCL types are 32+.
	if naluType >= 32 {
		return
	}
	if r.vps == nil || r.sps == nil || r.pps == nil {
		return
	}

	// Live-only mode: keep the stream alive for live preview (forwardHLS) but
	// skip all disk writes (segment creation + WriteSample + rotation).
	if r.recordDisabled() {
		r.forwardHLS(nalu)
		return
	}

	// Wait for IDR frame (types 19=IDR_W_RADL, 20=IDR_N_LP).
	if r.muxer == nil && naluType != 19 && naluType != 20 {
		return
	}

	// Adaptive write-density gate (issue #435/#468): while the compressed-domain
	// activity signal is calm only sparse keyframes reach the disk; on a spike
	// the retained GOP ring is flushed first so the resume has no missing
	// references. Live fan-out (forwardHLS → StreamHub) keeps flowing for every
	// frame — the gate changes write density, never the connection.
	if r.adaptive != nil {
		now := time.Now()
		isIDR := naluType == 19 || naluType == 20
		_, skip, flush := r.adaptive.Observe(nalu, isIDR, now)
		// Ambient-audio cameras keep the disk audio track through sparse mode
		// (#496); the merge renders the ambient span into the atmosphere bed.
		r.audioSparse.Store(r.adaptive.Timelapse() && !r.cfg.Adaptive.AmbientAudio)
		if len(flush) > 0 {
			r.writeFlushedGOP(flush)
			// Pre-trigger audio back-fill (issue #478), mirroring the built-in
			// recorders: write the retained unwritten ring samples so the
			// segment carries pre_capture seconds of sound before the trigger.
			r.flushAudioRing()
		}
		if skip {
			r.forwardHLS(nalu)
			return
		}
	}

	if r.muxer == nil {
		tempPath, finalPath, err := r.store.CreateSegment(r.cfg.CameraID, string(model.FormatH265))
		if err != nil {
			xiaomiLogger.Error("failed to create segment", "camera_id", r.cfg.CameraID, "error", err)
			return
		}
		r.muxer = muxer.NewMP4Muxer(tempPath)
		trackID, err := r.muxer.AddH265Track(r.vps, r.sps, r.pps)
		if err != nil {
			xiaomiLogger.Error("failed to add H265 track", "camera_id", r.cfg.CameraID, "error", err)
			r.muxer = nil
			os.Remove(tempPath)
			return
		}
		r.trackID = trackID

		// Add audio track if audio codec detected (same gating as H264 path:
		// enabled AND kept in recordings — issue #520).
		if r.cfg.AudioEnabled && r.cfg.AudioInRecordings && r.audioCodecID > 0 {
			codec, cfg, ok := buildAudioMuxerConfig(r.audioCodecID)
			if ok {
				aID, err := r.muxer.AddAudioTrack(codec, cfg)
				if err != nil {
					xiaomiLogger.Debug("audio track not added to muxer", "camera_id", r.cfg.CameraID, "error", err)
				} else {
					r.audioTrackID = aID
				}
			}
		}
		r.curTempPath = tempPath
		r.curFinalPath = finalPath
		r.segStart = time.Now()
		r.lastFrameTime = r.segStart
		r.frameCount = 0
	}

	now := time.Now()
	pts := now.Sub(r.segStart)
	duration := now.Sub(r.lastFrameTime)
	if duration < time.Millisecond {
		duration = time.Millisecond
	}
	r.lastFrameTime = now

	if err := r.muxer.WriteSample(r.trackID, nalu, pts, duration); err != nil {
		xiaomiLogger.Error("failed to write sample", "camera_id", r.cfg.CameraID, "error", err)
		return
	}
	r.frameCount++
	if r.adaptive != nil {
		// Frame now on disk; a later flush into this segment must skip it (#473).
		r.adaptive.MarkLastWritten()
	}

	// Forward to HLS (non-blocking)
	r.forwardHLS(nalu)

	if time.Since(r.segStart) >= r.cfg.SegmentDur {
		r.closeCurrentSegment()
	}
}

// forwardHLS sends a NALU to all stream consumers via StreamHub (non-blocking).
// For IDR frames, the codec parameter sets (SPS/PPS or VPS/SPS/PPS) are prepended
// so the HLS DTS extractor receives a complete access unit.
func (r *XiaomiRecorder) forwardHLS(nalu []byte) {
	if r.Hub == nil || r.Hub.ConsumerCount() == 0 {
		return
	}
	// Convert wall-clock duration to 90kHz ticks (RTP timestamp units).
	// HLS manager uses ClockRate=90000, so PTS must be in 90kHz ticks,
	// not nanoseconds. This matches built-in H264/H265 recorders which
	// pass RTP timestamps directly.
	pts := time.Since(r.streamStart).Nanoseconds() * 90000 / int64(time.Second)

	switch r.codec {
	case model.FormatH264:
		naluType := nalu[0] & 0x1F
		if naluType == 5 && r.sps != nil && r.pps != nil {
			r.Hub.Broadcast(pts, [][]byte{r.sps, r.pps, nalu}, true)
		} else {
			r.Hub.Broadcast(pts, [][]byte{nalu}, false)
		}
	case model.FormatH265:
		naluType := (nalu[0] >> 1) & 0x3F
		if (naluType == 19 || naluType == 20) && r.vps != nil && r.sps != nil && r.pps != nil {
			r.Hub.Broadcast(pts, [][]byte{r.vps, r.sps, r.pps, nalu}, true)
		} else {
			r.Hub.Broadcast(pts, [][]byte{nalu}, false)
		}
	default:
		r.Hub.Broadcast(pts, [][]byte{nalu}, false)
	}
}

// missCodecToAudio maps a MISS audio codec ID to a model.AudioCodec.
// Returns (codec, true) for known codecs, ("", false) for unknown/unsupported.
// PCMA, PCMU, and PCM all map to G.711 for StreamHub broadcast purposes.
func missCodecToAudio(codecID uint32) (model.AudioCodec, bool) {
	switch codecID {
	case missCodecPCMA, missCodecPCMU, missCodecPCM:
		return model.AudioG711, true
	case missCodecOPUS:
		return model.AudioOpus, true
	default:
		return "", false
	}
}

// buildAudioMuxerConfig returns the codec name and config bytes for the MP4 muxer
// based on the MISS audio codec ID.
func buildAudioMuxerConfig(codecID uint32) (codec string, config []byte, ok bool) {
	switch codecID {
	case missCodecPCMA:
		sr := 8000
		return "g711", []byte{0, byte(sr >> 24), byte(sr >> 16), byte(sr >> 8), byte(sr)}, true
	case missCodecPCMU:
		sr := 8000
		return "g711", []byte{1, byte(sr >> 24), byte(sr >> 16), byte(sr >> 8), byte(sr)}, true
	case missCodecOPUS:
		// Opus config: 1 byte channel count + 2 bytes PreSkip (BE) + 4 bytes InputSampleRate (BE)
		// Xiaomi cameras: mono, 16kHz input rate, no pre-skip info from MISS protocol
		opusRate := 16000
		return "opus", []byte{1, 0, 0, byte(opusRate >> 24), byte(opusRate >> 16), byte(opusRate >> 8), byte(opusRate)}, true
	default:
		return "", nil, false
	}
}

// forwardAudio broadcasts audio data via StreamHub (non-blocking)
// and writes to the MP4 muxer when an audio track is available.
// Skips silently when AudioEnabled is false.
// Unknown codec IDs are skipped with a warning log.
func (r *XiaomiRecorder) forwardAudio(codecID uint32, payload []byte) {
	if !r.cfg.AudioEnabled {
		return
	}
	audioCodec, ok := missCodecToAudio(codecID)
	if !ok {
		// Log unsupported codecs once, not per-frame (e.g. Opus codec_id=1032)
		if r.audioCodecID == 0 {
			xiaomiLogger.Info("audio codec not yet supported for recording, skipping", "camera_id", r.cfg.CameraID, "codec_id", codecID)
			r.audioCodecID = codecID // mark as seen to suppress repeated logs
		}
		return
	}

	// Remember the audio codec ID for muxer track creation.
	if r.audioCodecID == 0 {
		r.audioCodecID = codecID
		xiaomiLogger.Info("audio codec detected", "camera_id", r.cfg.CameraID, "codec_id", codecID, "codec", audioCodec)
	}

	// Broadcast to live consumers via StreamHub.
	if r.Hub != nil && r.Hub.AudioConsumerCount() > 0 {
		pts := time.Since(r.streamStart).Nanoseconds() * 90000 / int64(time.Second)
		r.Hub.BroadcastAudio(pts, audioCodec, payload)
	}

	// Audio-trigger input (issue #478): loudness window + pre-trigger ring for
	// the G.711 codecs. Opus has no pure-Go decoder — log once that the
	// trigger stays inactive for such cameras.
	if r.audioTrig != nil && r.adaptive != nil {
		if codecID == missCodecPCMU || codecID == missCodecPCMA {
			gate := r.adaptive
			trig := r.audioTrig
			trig.Ingest(codecID == missCodecPCMU, payload, 20*time.Millisecond, time.Now(),
				func(at time.Time) { gate.AudioLoud(at, 0) })
		} else if codecID == missCodecOPUS && !r.opusTrigWarned {
			r.opusTrigWarned = true
			xiaomiLogger.Warn("audio_trigger enabled but camera audio is Opus - trigger stays inactive (G.711 required)", "camera_id", r.cfg.CameraID)
		}
	}

	// Write audio to MP4 muxer if audio track is available.
	r.mu.Lock()
	m := r.muxer
	aid := r.audioTrackID
	start := r.segStart
	r.mu.Unlock()
	// Sparse (adaptive-timelapse) mode drops DISK audio, live audio continues
	// (mirrors the built-in H264/H265 recorders).
	if m != nil && aid > 0 && !r.audioSparse.Load() {
		pts := time.Since(start)
		// Audio frame duration: 20ms (G.711 and Opus both use 20ms frames).
		dur := 20 * time.Millisecond
		if err := m.WriteAudioSample(aid, payload, pts, dur); err != nil {
			xiaomiLogger.Error("failed to write audio sample", "camera_id", r.cfg.CameraID, "error", err)
		} else if r.audioTrig != nil {
			r.audioTrig.MarkWritten()
		}
	}
}

// writeFlushedGOP writes the adaptive gate's retained GOP frames (complete
// reference chain since the last IDR) into the current segment on the
// timelapse→normal transition, mirroring baseRecorder.writeFlushedGOP. If the
// segment just rotated (muxer == nil) the flush is dropped — the triggering
// frame itself still creates a fresh segment on its IDR, so at worst one
// pre-buffer is lost at a rotation boundary; no corrupt references result.
func (r *XiaomiRecorder) writeFlushedGOP(frames []recorder.AdaptiveFrame) {
	if r.muxer == nil {
		return
	}
	for _, f := range frames {
		if f.Written {
			// Already on disk in this segment — re-writing duplicates the ring's
			// IDR anchor (issue #473).
			continue
		}
		pts := f.At.Sub(r.segStart)
		if pts < 0 {
			pts = 0
		}
		dur := f.At.Sub(r.lastFrameTime)
		if dur < time.Millisecond {
			dur = time.Millisecond
		}
		if err := r.muxer.WriteSample(r.trackID, f.Nalu, pts, dur); err != nil {
			xiaomiLogger.Error("failed to write flushed sample", "camera_id", r.cfg.CameraID, "error", err)
			continue
		}
		r.lastFrameTime = f.At
		r.frameCount++
	}
}

// flushAudioRing back-fills pre-trigger audio at a timelapse→normal exit,
// mirroring baseRecorder.flushAudioRing: unwritten ring samples go into the
// current segment's audio track, pts-rebased to the current segStart; samples
// predating the segment are dropped.
func (r *XiaomiRecorder) flushAudioRing() {
	if r.audioTrig == nil {
		return
	}
	r.mu.Lock()
	m := r.muxer
	aid := r.audioTrackID
	start := r.segStart
	r.mu.Unlock()
	if m == nil || aid <= 0 {
		r.audioTrig.Drain() // nowhere to write — drop the backlog
		return
	}
	for _, s := range r.audioTrig.Drain() {
		if s.Written {
			continue
		}
		pts := s.At.Sub(start)
		if pts < 0 {
			continue
		}
		if err := m.WriteAudioSample(aid, s.Data, pts, s.Dur); err != nil {
			xiaomiLogger.Error("failed to write pre-trigger audio sample", "camera_id", r.cfg.CameraID, "error", err)
			return
		}
	}
}

// AudioTriggerEvent injects an external audio-activity event (issue #478;
// POST /api/cameras/{id}/adaptive/trigger). hold extends how long TIMELAPSE
// entry stays deferred.
func (r *XiaomiRecorder) AudioTriggerEvent(at time.Time, hold time.Duration) error {
	if r.adaptive == nil {
		return fmt.Errorf("camera %s is not in adaptive recording mode", r.cfg.CameraID)
	}
	r.adaptive.AudioLoud(at, hold)
	return nil
}

// closeCurrentSegment finalizes the current MP4 segment.
func (r *XiaomiRecorder) closeCurrentSegment() {
	if r.muxer == nil {
		return
	}
	// The retained rings' `written` markings refer to the segment being
	// closed; a later flush into a fresh segment must write those frames
	// instead of skipping them (issue #498 — mirrors baseRecorder).
	if r.adaptive != nil {
		r.adaptive.ClearWritten()
	}
	if r.audioTrig != nil {
		r.audioTrig.ClearWritten()
	}
	if err := r.muxer.Close(); err != nil {
		xiaomiLogger.Error("failed to close muxer", "camera_id", r.cfg.CameraID, "error", err)
		if r.curTempPath != "" {
			os.Remove(r.curTempPath)
		}
		r.muxer = nil
		r.curTempPath = ""
		r.curFinalPath = ""
		r.frameCount = 0
		return
	}

	// Atomic rename: temp → final
	if r.curTempPath != "" && r.curFinalPath != "" {
		if err := r.store.CloseSegment(r.curTempPath, r.curFinalPath); err != nil {
			xiaomiLogger.Error("failed to close segment", "camera_id", r.cfg.CameraID, "error", err)
		}
	}

	// Insert recording entry into database.
	var fileSize int64
	var recordingID string
	if r.cfg.DB != nil && r.curFinalPath != "" {
		now := time.Now()
		duration := now.Sub(r.segStart).Seconds()
		rec := &model.Recording{
			ID:         strconv.FormatInt(now.UnixNano(), 10),
			CameraID:   r.cfg.CameraID,
			FilePath:   r.curFinalPath,
			Format:     r.codec,
			StartedAt:  r.segStart,
			EndedAt:    now,
			Duration:   duration,
			FrameCount: r.frameCount,
		}
		recordingID = rec.ID
		if info, err := os.Stat(r.curFinalPath); err == nil {
			fileSize = info.Size()
			rec.FileSize = fileSize
		}
		if err := r.cfg.DB.InsertRecordingWithRetry(context.Background(), rec, 3, 500*time.Millisecond); err != nil {
			xiaomiLogger.Error("failed to insert recording", "camera_id", r.cfg.CameraID, "error", err)
		}
	}

	// Publish SegmentCompleted event.
	if r.cfg.EventBus != nil && recordingID != "" {
		r.cfg.EventBus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
			CameraID:    r.cfg.CameraID,
			FilePath:    r.curFinalPath,
			Format:      string(r.codec),
			Encoding:    string(r.codec),
			StartedAt:   r.segStart.Format(time.RFC3339Nano),
			EndedAt:     time.Now().Format(time.RFC3339Nano),
			FileSize:    fileSize,
			RecordingID: recordingID,
		})
	}

	// Update metrics.
	if r.frameCount > 0 && r.curFinalPath != "" {
		r.recordSegmentCreated()
		if fileSize > 0 {
			r.recordBytes(fileSize)
		}
	}

	r.muxer = nil
	r.trackID = 0
	r.audioTrackID = 0
	r.curTempPath = ""
	r.curFinalPath = ""
	r.frameCount = 0
}

// splitAnnexBNALUs splits Annex B formatted data into individual NALUs.
// It finds 00 00 00 01 or 00 00 01 start codes and returns the data between them.
func splitAnnexBNALUs(data []byte) [][]byte {
	var nalus [][]byte
	start := -1 // -1 means we haven't found the first start code yet

	for i := 0; i <= len(data)-3; i++ {
		if data[i] == 0 && data[i+1] == 0 {
			scLen := 0
			if data[i+2] == 1 {
				scLen = 3
			} else if i <= len(data)-4 && data[i+2] == 0 && data[i+3] == 1 {
				scLen = 4
			}
			if scLen > 0 {
				// If we had a previous start, extract the NALU up to this start code.
				if start >= 0 {
					// Trim trailing zeros before the start code.
					end := i
					for end > start && data[end-1] == 0 {
						end--
					}
					if end > start {
						nalus = append(nalus, bytes.Clone(data[start:end]))
					}
				}
				start = i + scLen
				i += scLen - 1
			}
		}
	}

	// Append the last NALU.
	if start >= 0 && start < len(data) {
		nalus = append(nalus, bytes.Clone(data[start:]))
	}

	return nalus
}

// annexBToAVCC converts Annex B formatted H264/H265 NALUs to AVCC format.
// It finds start codes, extracts NALUs, and prepends 4-byte big-endian length to each.
func annexBToAVCC(data []byte) []byte {
	nalus := splitAnnexBNALUs(data)
	if len(nalus) == 0 {
		return nil
	}

	// Calculate total size: sum of (4-byte length + nalu) for each NALU.
	totalSize := 0
	for _, nalu := range nalus {
		totalSize += 4 + len(nalu)
	}

	// Build AVCC buffer.
	result := make([]byte, 0, totalSize)
	lenBuf := make([]byte, 4)
	for _, nalu := range nalus {
		binary.BigEndian.PutUint32(lenBuf, uint32(len(nalu)))
		result = append(result, lenBuf...)
		result = append(result, nalu...)
	}
	return result
}

// MotorControl sends a PTZ motor control command to the Xiaomi camera.
// direction: "left", "right", "up", "down"; speed: 1-100.
func (r *XiaomiRecorder) MotorControl(direction string, speed int) error {
	r.missMu.Lock()
	client := r.missClient
	r.missMu.Unlock()
	if client == nil {
		return fmt.Errorf("xiaomi recorder not connected")
	}
	return client.MotorControl(direction, speed)
}

// GetDeviceInfo fetches device information from the Xiaomi camera.
// Returns a map with keys like "firmware_version", "hardware_version", etc.
func (r *XiaomiRecorder) GetDeviceInfo() (map[string]interface{}, error) {
	r.missMu.Lock()
	client := r.missClient
	r.missMu.Unlock()
	if client == nil {
		return nil, fmt.Errorf("xiaomi recorder not connected")
	}
	return client.GetDeviceInfo()
}

// StartTwoWayAudio starts two-way audio on the Xiaomi camera.
// Returns an error if the recorder is not connected or the camera
// uses CS2 transport (two-way audio requires TUTK).
func (r *XiaomiRecorder) StartTwoWayAudio() error {
	r.missMu.Lock()
	client := r.missClient
	r.missMu.Unlock()
	if client == nil {
		return fmt.Errorf("xiaomi recorder not connected")
	}
	return client.StartSpeaker()
}

// StopTwoWayAudio stops two-way audio playback.
func (r *XiaomiRecorder) StopTwoWayAudio() error {
	r.missMu.Lock()
	client := r.missClient
	r.missMu.Unlock()
	if client == nil {
		return fmt.Errorf("xiaomi recorder not connected")
	}
	return client.wirePacket(missCmdSpeakerStop, nil)
}

// SpeakerCodec returns the audio codec ID for two-way audio speaker output.
// Returns 0 if the camera model does not support two-way audio.
func (r *XiaomiRecorder) SpeakerCodec() uint32 {
	r.missMu.Lock()
	client := r.missClient
	r.missMu.Unlock()
	if client == nil {
		return 0
	}
	return client.SpeakerCodec()
}

// WriteAudioToCamera sends an audio payload to the Xiaomi camera
// for two-way audio playback.
// codecID is the MISS codec ID for the audio format (e.g. missCodecPCMU).
func (r *XiaomiRecorder) WriteAudioToCamera(codecID uint32, payload []byte) error {
	r.missMu.Lock()
	client := r.missClient
	r.missMu.Unlock()
	if client == nil {
		return fmt.Errorf("xiaomi recorder not connected")
	}
	return client.WriteAudio(codecID, payload)
}

// SetMISSClientForTest sets the MISS client for testing purposes only.
func (r *XiaomiRecorder) SetMISSClientForTest(c *MISSClient) {
	r.missMu.Lock()
	defer r.missMu.Unlock()
	r.missClient = c
}

// NewTestMISSClient creates a MISSClient for testing with a dummy 32-byte key.
func NewTestMISSClient(conn MISSConn) *MISSClient {
	return &MISSClient{Conn: conn, key: make([]byte, 32)}
}
