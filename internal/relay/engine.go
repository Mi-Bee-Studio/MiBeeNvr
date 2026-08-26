package relay

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/backoff"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/livetranscode"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/transcoding"
)

var engineLogger = slog.Default().With("component", "relay-engine")

const (
	// relayStallAfter is how long a target may report streaming with zero
	// bytes sent before the connection is declared dead and reconnected
	// (#429: after the receiver restarted, a target sat in streaming/kbps=0
	// for hours — write errors were the only failure signal, and with no
	// frames flowing there are no writes to fail).
	relayStallAfter = 30 * time.Second
	// relayStallPoll is the watchdog's sample interval.
	relayStallPoll = 5 * time.Second
	// relayWriteTimeout bounds each outbound RTMP write on the custom
	// publish conn. Without it, a dead peer (receiver restart, silently
	// dropped NAT state) lets writes block forever once the kernel send
	// buffer fills (#429).
	relayWriteTimeout = 10 * time.Second
)

// PushTargetConfig is the user-facing configuration for one push-out target.
// Mirrors config.PushTargetConfig but kept local to avoid a config<->relay
// import cycle (the manager maps between them).
type PushTargetConfig struct {
	ID                  string // stable id within the camera
	Name                string
	Protocol            string // "rtmp" or "rtsp"
	URL                 string // rtmp://host[:port]/app/key  |  rtsp://host[:port]/path
	Enabled             bool
	Platform            string                // preset: bilibili/douyin/youtube/kuaishou/generic/empty
	TranscodePolicy     string                // auto/force_sw/off
	VideoPresetOverride *VideoPresetOverrides // optional override
	UseFFmpeg           bool                  // if true, use FFmpeg subprocess for relay (compatibility mode)
	SourceURL           string                // optional: override auto-resolved source URL for FFmpeg relay
}

// VideoPresetOverrides mirrors config.VideoPresetOverrides to avoid an
// import cycle between config and relay.
type VideoPresetOverrides struct {
	Resolution       string
	Framerate        int
	VideoBitrateKbps int
	GopSeconds       int
	Profile          string
	Bframes          int
}

// SPSProvider returns the source camera's current SPS/PPS (raw NALUs, no start
// code) so an RTMP target can initialize its track, and reports whether the
// source is H.264 (RTMP targets require H.264; H.265 sources are rejected).
type SPSProvider func() (sps, pps []byte, isH264 bool)

// SourceCodecProvider returns the source camera's current video encoding as a
// model.Format string ("h264", "h265", "mjpeg", "jpeg", ...; "" = unknown).
// The relay uses it to fail fast on sources the transcode path can never
// handle (MJPEG/JPEG — #423) instead of engaging a doomed H.265 decoder.
type SourceCodecProvider func() string

// StreamURLProvider returns the source camera's stream URL (e.g. rtsp://...)
// for FFmpeg relay mode. If it returns empty, SourceURL from config is used.
type StreamURLProvider func(cameraID string) string

// PushTarget is one push-out destination: it subscribes to a camera's StreamHub
// and forwards each access unit to the target (RTMP or RTSP) over a dedicated
// connection. Each target runs in its own goroutine with independent reconnect.
type PushTarget struct {
	CameraID string
	Config   PushTargetConfig

	hub               *model.StreamHub
	spsProvider       SPSProvider
	codecInfoProvider func() model.CodecInfo
	sourceCodec       SourceCodecProvider

	mu     sync.RWMutex
	status RelayStatus
	errMsg string
	since  time.Time // status-effective time (connect/stream start)
	done   chan struct{}

	// bitrate accounting (atomic, sampled by status())
	bytesSent   atomic.Int64
	lastSample  time.Time
	sampleBytes int64
	sampleKbps  atomic.Int64

	// Transcode dependencies (optional, nil when transcode path not needed).
	presetRegistry    *PresetRegistry
	hardwareCap       *transcoding.HardwareCapabilities
	ffmpegPath        string
	streamURLProvider StreamURLProvider

	// Runtime monitoring state (set during connect, cleared on disconnect).
	// Thread-safe via atomic.Pointer and atomic.Int64 so Status() can read
	// them concurrently from any goroutine without holding mu.
	activeTranscoder   atomic.Pointer[livetranscode.LiveTranscoder]
	activeDriftMonitor atomic.Pointer[DriftMonitor]
	latestTemperatureC atomic.Int64
	restartCount       atomic.Int64

	// Stall watchdog threshold; overridable in tests, defaults to
	// relayStallAfter (issue #429).
	stallAfter time.Duration
	// connectFn is the streaming attempt run under the stall watchdog;
	// overridden in tests, nil = t.connectAndStream.
	connectFn func(ctx context.Context) error
}

// NewPushTarget constructs an idle target. It does not connect until Run.
func NewPushTarget(cameraID string, cfg PushTargetConfig, hub *model.StreamHub, sps SPSProvider) *PushTarget {
	return &PushTarget{
		CameraID:    cameraID,
		Config:      cfg,
		hub:         hub,
		spsProvider: sps,
		status:      StatusIdle,
		stallAfter:  relayStallAfter,
	}
}

// SetCodecInfoProvider wires an optional codec info provider for audio-aware
// targets. Should be set before Run.
func (t *PushTarget) SetCodecInfoProvider(p func() model.CodecInfo) {
	t.codecInfoProvider = p
}

// SetSourceCodecProvider wires an optional source-codec provider used to fail
// fast on video encodings the transcode path cannot handle (#423). Should be
// set before Run; unset keeps the legacy behavior (non-H.264 is treated as
// H.265).
func (t *PushTarget) SetSourceCodecProvider(p SourceCodecProvider) {
	t.sourceCodec = p
}

// isJPEGSource reports whether the source video is MJPEG/JPEG. Unknown codec
// ("" — provider unset or recorder not yet resolved) returns false so the
// existing behavior is preserved.
func (t *PushTarget) isJPEGSource() bool {
	if t.sourceCodec == nil {
		return false
	}
	switch t.sourceCodec() {
	case string(model.FormatMJPEG), string(model.EncJPEG):
		return true
	}
	return false
}

// SetPresetRegistry wires the preset registry for transcode path resolution.
// Should be set before Run if transcode may be used.
func (t *PushTarget) SetPresetRegistry(r *PresetRegistry) {
	t.presetRegistry = r
}

// SetHardwareCap wires hardware capabilities for transcoder encoder selection.
// Should be set before Run if transcode may be used.
func (t *PushTarget) SetHardwareCap(hwCap *transcoding.HardwareCapabilities) {
	t.hardwareCap = hwCap
}

// SetFFmpegPath sets an explicit FFmpeg binary path for the transcoder.
// When empty, the transcoder auto-detects from HardwareCap or PATH.
func (t *PushTarget) SetFFmpegPath(path string) {
	t.ffmpegPath = path
}

// SetStreamURLProvider wires a function that resolves a camera's stream URL
// (e.g. rtsp://...) for FFmpeg relay mode.
func (t *PushTarget) SetStreamURLProvider(p StreamURLProvider) {
	t.streamURLProvider = p
}

// Status returns a snapshot of the target's runtime status for the API/UI.
func (t *PushTarget) Status() TargetStatus {
	t.mu.RLock()
	st := t.status
	errMsg := t.errMsg
	since := t.since
	t.mu.RUnlock()

	// Sample outbound bitrate over the last interval.
	now := time.Now()
	var kbps float64
	if !t.lastSample.IsZero() {
		elapsed := now.Sub(t.lastSample).Seconds()
		if elapsed > 0.1 {
			cur := t.bytesSent.Load()
			kbps = float64(cur-t.sampleBytes) / elapsed / 1024.0 * 8.0
			t.lastSample = now
			t.sampleBytes = cur
		} else {
			kbps = float64(t.sampleKbps.Load())
		}
	} else {
		t.lastSample = now
		t.sampleBytes = t.bytesSent.Load()
	}

	uptime := ""
	if (st == StatusStreaming || st == StatusReconnecting) && !since.IsZero() {
		uptime = time.Since(since).Round(time.Second).String()
	}

	// --- Extended runtime fields ---

	platform := t.Config.Platform
	transcodePolicy := t.Config.TranscodePolicy

	// Transcode status from active transcoder (if transcoding).
	transcodeStatus := "idle"
	transcodeResolution := ""
	if lt := t.activeTranscoder.Load(); lt != nil {
		en := lt.EncoderName()
		switch {
		case strings.Contains(en, "v4l2m2m"), strings.Contains(en, "omx"):
			transcodeStatus = "active_hw"
		case en != "":
			transcodeStatus = "active_sw"
		}
		transcodeResolution = lt.PresetResolution()
	}

	// Audio codec from codecInfoProvider (best-effort, may be silent when not streaming).
	audioCodec := "silent"
	if t.codecInfoProvider != nil {
		ci := t.codecInfoProvider()
		switch ci.AudioCodec {
		case "aac":
			audioCodec = "aac"
		case "g711":
			if len(ci.AudioConfig) > 0 && ci.AudioConfig[0] != 0 {
				audioCodec = "g711_mu"
			} else {
				audioCodec = "g711_a"
			}
		}
	}

	tempC := int(t.latestTemperatureC.Load())
	restartCount := int(t.restartCount.Load())

	var avDriftMs float64
	if dm := t.activeDriftMonitor.Load(); dm != nil {
		avDriftMs = dm.DriftMs()
	}
	return TargetStatus{
		ID:        t.Config.ID,
		Name:      t.Config.Name,
		Protocol:  t.Config.Protocol,
		URL:       t.Config.URL,
		Status:    st,
		Kbps:      kbps,
		Enabled:   t.Config.Enabled,
		Uptime:    uptime,
		Error:     errMsg,
		UpdatedAt: now,
		// Extended runtime fields
		Platform:            platform,
		TranscodePolicy:     transcodePolicy,
		TranscodeStatus:     transcodeStatus,
		TranscodeResolution: transcodeResolution,
		AudioCodec:          audioCodec,
		TemperatureC:        tempC,
		RestartCount:        restartCount,
		AVDriftMs:           avDriftMs,
	}
}

// Run starts the target and blocks until ctx is canceled or the hub is gone.
// It owns the reconnect loop. Safe to call via `go t.Run(ctx)`.
func (t *PushTarget) Run(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			buf := make([]byte, 4096)
			buf = buf[:runtime.Stack(buf, false)]
			engineLogger.Error("PANIC recovered in PushTarget.Run",
				"camera_id", t.CameraID, "target_id", t.Config.ID, "panic", r, "stack", string(buf))
		}
	}()
	if t.hub == nil {
		t.setStatus(StatusError, "source camera has no stream hub")
		return
	}
	var attempt int
	for {
		if ctx.Err() != nil {
			return
		}
		err := t.connectAndStreamWatched(ctx)
		if ctx.Err() != nil {
			return
		}
		attempt++
		bo := backoff.TieredBackoffWithJitter(attempt)
		engineLogger.Warn("relay target disconnected, retrying",
			"camera_id", t.CameraID, "target_id", t.Config.ID,
			"protocol", t.Config.Protocol, "error", err, "attempt", attempt, "backoff", bo)
		t.setStatus(StatusReconnecting, err.Error())
		select {
		case <-ctx.Done():
			return
		case <-time.After(bo):
		}
	}
}

// connectAndStreamWatched runs connectAndStream under the stall watchdog
// (issue #429). Failure detection in the streaming loops is purely
// write-error driven; when the source stalls or the peer dies without RST
// there are no writes to fail, so the target sat in status=streaming with
// kbps=0 for hours. The watchdog cancels the streaming context when no bytes
// have been sent for stallAfter while the target reports streaming, turning
// the silent stall into a normal reconnect-with-backoff.
func (t *PushTarget) connectAndStreamWatched(ctx context.Context) error {
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	stalled := make(chan struct{})
	go t.stallMonitor(streamCtx, stalled, cancel)

	inner := t.connectFn
	if inner == nil {
		inner = t.connectAndStream
	}
	err := inner(streamCtx)
	select {
	case <-stalled:
		if ctx.Err() == nil {
			return fmt.Errorf("stream stalled: no bytes sent for %v while streaming (dead target connection or stalled source)", t.stallAfter)
		}
	default:
	}
	return err
}

// stallMonitor samples bytesSent every relayStallPoll and cancels the
// streaming context via cancel once the target has been streaming with a
// frozen byte counter for stallAfter. The window resets while the target is
// not yet streaming (connect/handshake phases have their own timeouts).
// Exactly one of stalled is closed / cancel is called at most once, by this
// goroutine alone.
func (t *PushTarget) stallMonitor(ctx context.Context, stalled chan struct{}, cancel context.CancelFunc) {
	poll := relayStallPoll
	if t.stallAfter < 2*poll {
		poll = max(t.stallAfter/2, 50*time.Millisecond)
	}
	lastBytes := t.bytesSent.Load()
	lastChange := time.Now()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.mu.RLock()
			st := t.status
			t.mu.RUnlock()
			if st != StatusStreaming {
				// Handshake/connect phase — reset the window.
				lastBytes = t.bytesSent.Load()
				lastChange = time.Now()
				continue
			}
			cur := t.bytesSent.Load()
			if cur != lastBytes {
				lastBytes = cur
				lastChange = time.Now()
				continue
			}
			if time.Since(lastChange) >= t.stallAfter {
				engineLogger.Warn("relay target stalled, forcing reconnect",
					"camera_id", t.CameraID, "target_id", t.Config.ID,
					"protocol", t.Config.Protocol, "stalled_for", time.Since(lastChange).Round(time.Second))
				close(stalled)
				cancel()
				return
			}
		}
	}
}

// connectAndStream establishes the target connection, subscribes to the hub,
// and blocks while streaming. Returns a non-nil error when the stream ends.
func (t *PushTarget) connectAndStream(ctx context.Context) error {
	// FFmpeg relay mode: use FFmpeg subprocess for platforms with strict RTMP
	// parsers (e.g. Douyu Live Companion) that reject the native Go RTMP writer.
	if t.Config.UseFFmpeg {
		return t.connectViaFFmpeg(ctx)
	}
	switch t.Config.Protocol {
	case "rtmp":
		return t.connectRTMP(ctx)
	case "rtsp":
		return t.connectRTSP(ctx)
	default:
		t.setStatus(StatusError, "unsupported protocol: "+t.Config.Protocol)
		return errPermanent
	}
}

func (t *PushTarget) setStatus(st RelayStatus, errMsg string) {
	t.mu.Lock()
	t.status = st
	t.errMsg = errMsg
	if st == StatusStreaming || st == StatusConnecting {
		t.since = time.Now()
	}
	t.mu.Unlock()
}
