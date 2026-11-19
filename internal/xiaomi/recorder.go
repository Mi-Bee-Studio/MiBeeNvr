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
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/backoff"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model/nalutil"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

var xiaomiLogger = slogx.Component("xiaomi-recorder")

// ErrNotConnected marks a recorder whose control-plane client is not
// established (reconnecting/idle). Callers map it to 503 — a runtime state,
// not a server defect.
var ErrNotConnected = errors.New("xiaomi recorder not connected")

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
	// qualityUpgradeStableWindow earns one probe attempt back at HD, taken
	// on the connection's next natural disconnect (defect B: quality
	// previously never recovered without a restart). A healthy stream is
	// never torn down for the probe — see the run loop.
	qualityUpgradeStableWindow = 10 * time.Minute
	// maxQualityUpgradeAttempts bounds the downgrade→upgrade cycle per
	// recorder lifecycle, preventing a downgrade/upgrade oscillation storm
	// (the 2K PTZ camera logged 131 SPS changes in one day — the recovery
	// must not feed that).
	maxQualityUpgradeAttempts = 2
)

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
	AudioEnabled      bool              // Capture and broadcast audio via StreamHub when true
	AudioInRecordings bool              // Keep the audio track in recorded segments (default off)
	IdleTimeout       time.Duration
	Channel           string // Xiaomi dual-lens channel ("" or "0" = main, "1" = secondary)
	Quality           string // Stream quality: "" or "auto" (HD→SD fallback), "hd", "sd"
	// MergeFriendly pins the stream quality for merge-consistent recordings:
	// the SD→HD upgrade probe (issue #502 defect B) is disarmed, so quality
	// flips — and the SPS/PPS boundaries they put between segments — collapse
	// to at most the one failure-driven downgrade. Off by default; opt in per
	// camera (yaml merge_friendly) when rolling merge matters more than
	// recovering HD after a transient (#1150).
	MergeFriendly bool
	EventBus      *event.EventBus
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
	// lastSegBytes feeds segment preallocation (#757, baseRecorder parity):
	// the previous segment's final size estimates the next one. Written only
	// in closeCurrentSegment and read only at muxer creation — both on the
	// read-loop goroutine. 0 = no estimate yet.
	lastSegBytes int64

	// Codec state (probed from first packets). The raw fields are owned by
	// the connectAndRecord read-loop goroutine (single writer); every
	// mutation also publishes an immutable deep-copied snapshot behind
	// codecSnap so the cross-goroutine accessors (SPS/PPS/VPS/CodecParams —
	// HLS/WS/relay HTTP goroutines) read a consistent triplet without a
	// lock. Port of baseRecorder's Tier-2 snapshot (#219/#226 parity, #1003).
	codec     model.Format // "h264" or "h265"
	sps       []byte
	pps       []byte
	vps       []byte // H265 only
	codecOK   bool   // true once codec type is determined
	codecSnap atomic.Pointer[codecSnapshot]
	// paramGate bounds parameter-set-triggered segment rotations (#642): this
	// model family alternates decode-equivalent SPS encodings (VUI/timing-only
	// differences) per GOP — 188 rotations in 4.9h on production. Owned by the
	// video NALU processing goroutine.
	paramGate nalutil.ParamRotationGate

	// Audio state (probed from first audio packet). Single writer (read
	// loop's forwardAudio), multi-reader (external Audio* accessors from
	// HTTP goroutines; muxer track setup on the read loop itself) — atomic
	// for the same reason as codecSnap (#1003). MISS codec ID, 0 = unknown.
	audioCodecID atomic.Uint32

	Hub         *streamhub.StreamHub // Frame fan-out to multiple consumers (HLS, WebRTC, etc.)
	streamStart time.Time            // For PTS rebase (used by forwardHLS)

	currentQuality   string // effective quality for next connectAndRecord attempt
	noMediaFailCount int    // no-media failures since the last stable-streaming window (issue #502: true consecutive semantics)
	connectFailCount int    // consecutive connect/handshake failures (miss connect i/o timeout etc.)
	lastMissURL      string // last resolved MISS URL (for error messages naming the unreachable host)

	// Quality state machine (issue #502). Owned by the run goroutine.
	mediaStart      time.Time // when StartMedia succeeded for the current connection; zero while connecting
	upgradeAttempts int       // SD→HD probe attempts consumed this recorder lifecycle
	// hdProbeWanted is armed by the read loop once a stable SD session earns
	// an HD probe; the run loop consumes it on the next natural disconnect.
	// A healthy stream is never torn down deliberately — the old probe did
	// exactly that and fed a self-inflicted flapping loop on HD-refusing
	// cameras (issue #906 field evidence: healthy SD session murdered every
	// stable window, ~90s of churn per cycle).
	hdProbeWanted bool
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

// codecSnapshot is the immutable published view of the codec parameter sets,
// deep-copied on every publish (see baseRecorder.codecParams, #219). Storing
// the full triplet keeps concurrent readers from observing a torn old/new mix
// (e.g. new SPS with the previous PPS).
type codecSnapshot struct {
	codec model.Format
	sps   []byte
	pps   []byte
	vps   []byte
}

// publishCodecSnap replaces the published codec view from the writer-owned raw
// fields. Read-loop goroutine only, after any codec-state mutation.
func (r *XiaomiRecorder) publishCodecSnap() {
	r.codecSnap.Store(&codecSnapshot{
		codec: r.codec,
		sps:   append([]byte(nil), r.sps...),
		pps:   append([]byte(nil), r.pps...),
		vps:   append([]byte(nil), r.vps...),
	})
}

// GetHub returns the StreamHub for frame fan-out.
func (r *XiaomiRecorder) GetHub() *streamhub.StreamHub { return r.Hub }

// SetHub wires the StreamHub for frame fan-out (streamhub.HubHost).
func (r *XiaomiRecorder) SetHub(hub *streamhub.StreamHub) { r.Hub = hub }

// HubSource labels the hub for the flow-path observability view.
func (r *XiaomiRecorder) HubSource() string { return "xiaomi" }

// SPS returns the current H.264/H.265 SPS NAL unit (without start bytes).
// Read from the published snapshot — race-free from any goroutine (#1003).
func (r *XiaomiRecorder) SPS() []byte {
	if s := r.codecSnap.Load(); s != nil {
		return s.sps
	}
	return nil
}

// PPS returns the current H.264/H.265 PPS NAL unit (without start bytes).
func (r *XiaomiRecorder) PPS() []byte {
	if s := r.codecSnap.Load(); s != nil {
		return s.pps
	}
	return nil
}

// VPS returns the current H.265 VPS NAL unit (without start bytes), or nil for H.264.
func (r *XiaomiRecorder) VPS() []byte {
	if s := r.codecSnap.Load(); s != nil {
		return s.vps
	}
	return nil
}

// AudioCodec returns the audio codec name ("g711" or "" for no audio).
func (r *XiaomiRecorder) AudioCodec() string {
	c, ok := missCodecToAudio(r.audioCodecID.Load())
	if !ok {
		return ""
	}
	return string(c)
}

// AudioConfig returns the audio codec config. G.711 returns nil (no ASC).
func (r *XiaomiRecorder) AudioConfig() []byte { return nil }

// AudioSampleRate returns the audio sample rate. G.711 defaults to 8 kHz.
func (r *XiaomiRecorder) AudioSampleRate() int {
	if _, ok := missCodecToAudio(r.audioCodecID.Load()); ok {
		return 8000
	}
	return 0
}

// AudioChannels returns the number of audio channels. G.711 is mono.
func (r *XiaomiRecorder) AudioChannels() int {
	if _, ok := missCodecToAudio(r.audioCodecID.Load()); ok {
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
// Implements model.HLSProvider. Snapshot read — consistent triplet, race-free
// from any goroutine (#1003).
func (r *XiaomiRecorder) CodecParams() (codec model.Format, sps, pps, vps []byte) {
	if s := r.codecSnap.Load(); s != nil {
		return s.codec, s.sps, s.pps, s.vps
	}
	return model.Format(""), nil, nil, nil
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
			delay := backoff.TieredBackoffWithJitter(retryCount)
			r.reportVendorError(err)
			xiaomiLogger.Error("failed to resolve MISS URL, retrying", "camera_id", r.cfg.CameraID, "error", err, "backoff", delay, "attempt", retryCount)
			r.recordError("cloud_resolve")
			r.recordXiaomiDisconnect("cloud_resolve")
			r.setStatus(model.StatusReconnecting)
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
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

		// SD→HD upgrade (issue #502 defect B), piggybacked on a natural
		// disconnect: the read loop only ARMS hdProbeWanted — reconnecting a
		// healthy stream just to probe HD fed a self-inflicted flapping loop
		// on cameras that refuse HD (issue #906: a healthy SD session was
		// torn down every stable window, each cycle costing ~90s of churn).
		if r.hdProbeWanted {
			r.hdProbeWanted = false
			r.currentQuality = "hd"
			r.upgradeAttempts++
			r.recordQualityChange("sd", "hd", fmt.Sprintf(
				"stable SD streaming, probe attempt %d/%d on natural reconnect",
				r.upgradeAttempts, r.maxUpgradeAttempts))
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
		delay := backoff.TieredBackoffWithJitter(retryCount)
		if r.connectFailCount >= connectFailThreshold {
			delay = backoff.StorageBackoffWithJitter() // ~60s — the camera isn't coming back soon
		}
		xiaomiLogger.Error("connection error, reconnecting", "camera_id", r.cfg.CameraID, "error", err, "backoff", delay, "attempt", retryCount, "connect_failures", r.connectFailCount)
		r.recordError("connection")
		r.recordXiaomiDisconnect(classifyDisconnectReason(err))
		r.setStatus(model.StatusReconnecting)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
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
// defect B). Arming only — the attempt itself is taken on the connection's
// next natural disconnect, bounded by maxUpgradeAttempts per recorder
// lifecycle so a camera that refuses HD cannot oscillate forever.
func (r *XiaomiRecorder) shouldProbeUpgrade(now time.Time) bool {
	return r.currentQuality == "sd" && r.qualityAuto() &&
		!r.cfg.MergeFriendly &&
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
	r.audioCodecID.Store(0)
	r.publishCodecSnap()
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

		// Arrival time, captured once per packet (#506 / #1003 parity with
		// baseRecorder): recording timestamps, rotation checks and the
		// adaptive cadence follow it — never the consumption-time wall clock.
		// When the read loop stalls and then drains a backlog in a burst,
		// consumption-time timestamps collapse onto the same millisecond and
		// break the decode reference chain (duplicate-dts → missing-ref).
		at := time.Now()

		// SD→HD upgrade (issue #502 defect B): arm the probe once the stable
		// window has been earned; the run loop consumes the flag on the next
		// natural disconnect. NEVER disconnect here — a healthy stream must
		// not be murdered to probe a better one (issue #906 field evidence).
		if !r.hdProbeWanted && r.shouldProbeUpgrade(at) {
			r.hdProbeWanted = true
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
			r.publishCodecSnap()
			xiaomiLogger.Info("codec detected", "camera_id", r.cfg.CameraID, "codec", r.codec)
		}

		// Parse Annex B NALUs from payload and process each one.
		nalus := splitAnnexBNALUs(pkt.Payload)
		for _, nalu := range nalus {
			r.processNALU(nalu, at)
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

// PixelTriggerEvent injects a pixgate CV activity confirmation (#636).
func (r *XiaomiRecorder) PixelTriggerEvent(at time.Time, hold time.Duration) error {
	if r.adaptive == nil {
		return fmt.Errorf("camera %s is not in adaptive recording mode", r.cfg.CameraID)
	}
	r.adaptive.PixelLoud(at, hold)
	return nil
}

// closeCurrentSegment finalizes the current MP4 segment via the shared
// recorder.FinalizeSegment skeleton (close → rename → insert row → event →
// metrics; issue #1012 block 1 — this copy drifted from baseRecorder before,
// see #1003). Xiaomi-specific bits live in the hooks: the adaptive/audio ring
// written-flag resets, the field set reset (no baseRecorder locking here —
// the segment fields are owned by the read-loop goroutine), and the metrics
// counters (no #469 wall-time observation on this recorder).
func (r *XiaomiRecorder) closeCurrentSegment() {
	if r.muxer == nil {
		return
	}
	recorder.FinalizeSegment(recorder.SegmentFinalizeParams{
		Log:          xiaomiLogger,
		CameraID:     r.cfg.CameraID,
		Store:        r.store,
		DB:           r.cfg.DB,
		EventBus:     r.cfg.EventBus,
		Muxer:        r.muxer,
		TempPath:     r.curTempPath,
		FinalPath:    r.curFinalPath,
		SegStart:     r.segStart,
		FrameCount:   int64(r.frameCount),
		Format:       r.codec,
		LastSegBytes: &r.lastSegBytes,
		ResetRings: func() {
			// The retained rings' `written` markings refer to the segment
			// being closed; a later flush into a fresh segment must write
			// those frames instead of skipping them (issue #498 — mirrors
			// baseRecorder).
			if r.adaptive != nil {
				r.adaptive.ClearWritten()
			}
			if r.audioTrig != nil {
				r.audioTrig.ClearWritten()
			}
		},
		OnCloseError: func() {
			r.muxer = nil
			r.curTempPath = ""
			r.curFinalPath = ""
			r.frameCount = 0
		},
		OnMetrics: func(fileSize int64, _ time.Duration) {
			r.recordSegmentCreated()
			if fileSize > 0 {
				r.recordBytes(fileSize)
			}
		},
		OnFinished: func() {
			r.muxer = nil
			r.trackID = 0
			r.audioTrackID = 0
			r.curTempPath = ""
			r.curFinalPath = ""
			r.frameCount = 0
		},
	})
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
		return ErrNotConnected
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
		return nil, ErrNotConnected
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
		return ErrNotConnected
	}
	return client.StartSpeaker()
}

// StopTwoWayAudio stops two-way audio playback.
func (r *XiaomiRecorder) StopTwoWayAudio() error {
	r.missMu.Lock()
	client := r.missClient
	r.missMu.Unlock()
	if client == nil {
		return ErrNotConnected
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
		return ErrNotConnected
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
