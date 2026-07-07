package relay

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/backoff"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/livetranscode"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/transcoding"

	"github.com/bluenviron/gortmplib"
	"github.com/bluenviron/gortmplib/pkg/codecs"
	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtplpcm"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtpmpeg4audio"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/pion/rtp"
)

var engineLogger = slog.Default().With("component", "relay-engine")

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
}

// NewPushTarget constructs an idle target. It does not connect until Run.
func NewPushTarget(cameraID string, cfg PushTargetConfig, hub *model.StreamHub, sps SPSProvider) *PushTarget {
	return &PushTarget{
		CameraID:    cameraID,
		Config:      cfg,
		hub:         hub,
		spsProvider: sps,
		status:      StatusIdle,
	}
}

// SetCodecInfoProvider wires an optional codec info provider for audio-aware
// targets. Should be set before Run.
func (t *PushTarget) SetCodecInfoProvider(p func() model.CodecInfo) {
	t.codecInfoProvider = p
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
		err := t.connectAndStream(ctx)
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

// connectViaFFmpeg spawns an FFmpeg subprocess that pulls from the camera's
// stream URL and pushes to the target. Used when use_ffmpeg is enabled —
// some strict RTMP receivers (e.g. Douyu Live Companion) reject the native
// Go RTMP writer. The subprocess lifecycle is tied to ctx.
func (t *PushTarget) connectViaFFmpeg(ctx context.Context) error {
	ffmpegPath := t.ffmpegPath
	if ffmpegPath == "" {
		var err error
		ffmpegPath, err = exec.LookPath("ffmpeg")
		if err != nil {
			t.setStatus(StatusError, "FFmpeg not found for relay")
			return errPermanent
		}
	}

	// Resolve source URL: explicit override > auto-resolved from camera.
	sourceURL := t.Config.SourceURL
	if sourceURL == "" && t.streamURLProvider != nil {
		sourceURL = t.streamURLProvider(t.CameraID)
	}
	if sourceURL == "" {
		t.setStatus(StatusError, "cannot resolve source URL for FFmpeg relay")
		return errPermanent
	}
	targetURL := t.Config.URL

	args := []string{"-hide_banner", "-loglevel", "info"}
	if strings.HasPrefix(sourceURL, "rtsp://") {
		args = append(args, "-rtsp_transport", "tcp")
	}
	args = append(args, "-i", sourceURL, "-c", "copy")
	// Correct the FLV onMetaData frame rate to the source's ACTUAL fps.
	// RTSP cameras frequently declare an inflated fps in their SDP (e.g. 30)
	// while actually emitting fewer frames (e.g. 15). With plain -c copy,
	// FFmpeg writes the SDP-declared (wrong) fps into the FLV onMetaData, and
	// strict RTMP receivers (e.g. Douyu Live Companion) initialize a decoder
	// for the declared rate, then freeze after a few seconds of half-rate
	// input. -r only rewrites the metadata fps here -- frame data and PTS
	// intervals stay identical (verified in production: 66ms @ 15fps).
	if fps := probeSourceVideoFPS(ffmpegPath, sourceURL); fps > 0 {
		args = append(args, "-r", strconv.Itoa(fps))
		engineLogger.Info("ffmpeg relay corrected output fps",
			"camera_id", t.CameraID, "target_id", t.Config.ID, "fps", fps)
	}
	// I/O timeout (15s) for both RTSP input and RTMP output sockets. Without
	// this, a mid-stream RTMP rejection (e.g. Douyu auth token expiring hours
	// into a healthy stream) causes FFmpeg's muxer thread to die while the main
	// thread is blocked on RTSP read — a silent deadlock that stalls the relay
	// indefinitely and freezes the receiver's last frame forever.
	//
	// Pass via TWO channels because FFmpeg 7.1.5's RTMP handler doesn't honor
	// -rw_timeout as a bare CLI flag (it's silently consumed by the FLV muxer
	// instead of the TCP socket): as a CLI flag AND as a URL query parameter
	// (which the RTMP URL parser DOES forward to the URLContext/I/O layer).
	args = append(args, "-rw_timeout", "15000000")
	if strings.Contains(targetURL, "?") {
		targetURL += "&rw_timeout=15000000"
	} else {
		targetURL += "?rw_timeout=15000000"
	}
	args = append(args, "-f", "flv", "-flvflags", "no_duration_filesize", targetURL)

	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.setStatus(StatusError, "ffmpeg stderr pipe: "+err.Error())
		return err
	}

	t.setStatus(StatusConnecting, "ffmpeg relay starting")
	engineLogger.Info("relay target starting FFmpeg relay",
		"camera_id", t.CameraID, "target_id", t.Config.ID,
		"source", sourceURL, "target", targetURL)

	if err := cmd.Start(); err != nil {
		t.setStatus(StatusError, "ffmpeg start: "+err.Error())
		return err
	}

	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			if line != "" && !strings.HasPrefix(line, "frame=") {
				engineLogger.Info("ffmpeg relay stderr",
					"camera_id", t.CameraID, "target_id", t.Config.ID,
					"line", line)
			}
		}
	}()

	t.setStatus(StatusStreaming, "")
	engineLogger.Info("relay target streaming (FFmpeg)",
		"camera_id", t.CameraID, "target_id", t.Config.ID,
		"protocol", t.Config.Protocol, "url", targetURL)

	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if waitErr != nil {
		return fmt.Errorf("ffmpeg relay exited: %w", waitErr)
	}
	return nil
}

// probeSourceVideoFPS returns the actual video frame rate of a source stream
// via ffprobe's r_frame_rate. Returns 0 on any failure (the caller then skips
// -r and falls back to FFmpeg's default, preserving previous behavior).
func probeSourceVideoFPS(ffmpegPath, sourceURL string) int {
	ffprobePath := "ffprobe"
	if ffmpegPath != "" {
		cand := filepath.Join(filepath.Dir(ffmpegPath), "ffprobe")
		if _, err := exec.LookPath(cand); err == nil {
			ffprobePath = cand
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, ffprobePath, "-hide_banner",
		"-of", "csv=p=0", "-select_streams", "v:0",
		"-show_entries", "stream=r_frame_rate", sourceURL).Output()
	if err != nil {
		return 0
	}
	// r_frame_rate is "num/den", e.g. "15/1".
	numStr, denStr, ok := strings.Cut(strings.TrimSpace(string(out)), "/")
	if !ok {
		return 0
	}
	num, err1 := strconv.Atoi(numStr)
	den, err2 := strconv.Atoi(denStr)
	if err1 != nil || err2 != nil || den == 0 || num <= 0 {
		return 0
	}
	return num / den
}

// --- RTMP target ---

func (t *PushTarget) connectRTMP(ctx context.Context) error {
	sps, pps, isH264 := t.spsProvider()
	if !isH264 {
		// H.265 source — transcode if policy allows.
		if t.Config.TranscodePolicy == "off" {
			t.setStatus(StatusError, "source is not H.264 (RTMP target requires H.264)")
			return errPermanent
		}
		if t.presetRegistry == nil {
			t.setStatus(StatusError, "transcode requested but preset registry not configured")
			return errPermanent
		}
		return t.connectRTMPWithTranscode(ctx)
	}
	if sps == nil || pps == nil {
		t.setStatus(StatusError, "source stream not ready (no SPS/PPS yet)")
		return errPermanent
	}

	t.setStatus(StatusConnecting, "")
	u, err := url.Parse(t.Config.URL)
	if err != nil || (u.Scheme != "rtmp" && u.Scheme != "rtmps") {
		t.setStatus(StatusError, "invalid RTMP url")
		return errPermanent
	}

	// Build tracks list: video track + optional audio track.
	videoTrack := &gortmplib.Track{Codec: &codecs.H264{
		SPS: append([]byte(nil), sps...),
		PPS: append([]byte(nil), pps...),
	}}

	// Create cancellable context for silent AAC fallback (cancelled on exit).
	silentCtx, silentCancel := context.WithCancel(ctx)
	defer silentCancel()

	var (
		audioTrack *gortmplib.Track
		audioSubID string // non-empty for AAC passthrough
		g711Track  *gortmplib.Track
		g711SubID  string        // non-empty for G.711 passthrough
		silentCh   <-chan []byte // non-nil for silent fallback
	)

	if t.codecInfoProvider != nil {
		ci := t.codecInfoProvider()
		switch ci.AudioCodec {
		case "aac":
			asc := parseASC(ci.AudioConfig)
			if asc != nil {
				audioTrack = &gortmplib.Track{Codec: &codecs.MPEG4Audio{Config: asc}}
				audioSubID = "relay-rtmp-" + t.Config.ID + "-audio"
				engineLogger.Info("relay target adding AAC audio track",
					"camera_id", t.CameraID, "target_id", t.Config.ID,
					"sample_rate", asc.SampleRate, "channels", asc.ChannelCount)
			}

		case "g711":
			isMULaw, _ := parseG711Config(ci.AudioConfig)
			ch := ci.AudioChannels
			if ch <= 0 {
				ch = 1
			}
			g711Track = &gortmplib.Track{Codec: &codecs.G711{MULaw: isMULaw, ChannelCount: ch}}
			g711SubID = "relay-rtmp-" + t.Config.ID + "-g711"
			engineLogger.Info("relay target adding G.711 audio track",
				"camera_id", t.CameraID, "target_id", t.Config.ID,
				"mu_law", isMULaw)
		default:
			// No audio source — silent AAC fallback.
			gen := NewSilenceAACGenerator()
			emitter := NewBufferAwareSilenceEmitter(gen)
			asc := parseASC(emitter.AudioConfig())
			if asc != nil {
				audioTrack = &gortmplib.Track{Codec: &codecs.MPEG4Audio{Config: asc}}
				silentCh = emitter.Start(silentCtx)
				engineLogger.Info("relay target adding silent AAC track (no source audio)",
					"camera_id", t.CameraID, "target_id", t.Config.ID)
			}
		}
	}

	tracks := []*gortmplib.Track{videoTrack}
	if audioTrack != nil {
		tracks = append(tracks, audioTrack)
	}
	if g711Track != nil {
		tracks = append(tracks, g711Track)
	}

	// Use dialRTMPPublish for complex handshake digest support (required by
	// Douyu/Huya/Bilibili FMS backends). gortmplib.Client does plain handshake
	// without HMAC-SHA256 digest — rejected by strict FMS servers.
	// Parse SPS for width/height/fps to populate the full onMetaData that strict
	// receivers (Douyu Live Companion) require — the sparse gortmplib metadata
	// (only videocodecid/videodatarate) is rejected by such receivers.
	var vidWidth, vidHeight int
	var vidFPS float64
	var spsParsed h264.SPS
	if err := spsParsed.Unmarshal(sps); err == nil {
		vidWidth = spsParsed.Width()
		vidHeight = spsParsed.Height()
		vidFPS = spsParsed.FPS()
		engineLogger.Debug("relay onMetaData from SPS",
			"camera_id", t.CameraID, "target_id", t.Config.ID,
			"width", vidWidth, "height", vidHeight, "fps", vidFPS)
	}
	conn, connCleanup, err := dialRTMPPublish(ctx, t.Config.URL, vidWidth, vidHeight, vidFPS)
	if err != nil {
		return err
	}
	defer connCleanup()

	writer := &gortmplib.Writer{Conn: conn, Tracks: tracks}
	if err := writer.Initialize(); err != nil {
		return err
	}

	// Subscribe to the source hub; the callback runs in its own goroutine and
	// may block on the target write without affecting recording/live.
	start := time.Now()
	consumerID := "relay-rtmp-" + t.Config.ID
	cbErr := make(chan error, 1)

	monitor := NewDriftMonitor()
	monitor.StartLogging(ctx)
	t.activeDriftMonitor.Store(monitor)
	defer t.activeDriftMonitor.Store(nil)

	// Video subscription (unchanged).
	if err := t.hub.Subscribe(consumerID, func(pts int64, au [][]byte) {
		// Use wall-clock relative PTS (avoids 90kHz wraparound; matches the
		// gortmplib publish example). dts == pts (assume no B-frame reorder).
		d := time.Since(start)
		if d < 0 {
			d = 0
		}
		if werr := writer.WriteH264(videoTrack, d, d, au); werr != nil {
			select {
			case cbErr <- werr:
			default:
			}
		} else {
			// Account bytes (sum of NALU lengths, ~ payload, good enough for kbps).
			var n int64
			for _, nalu := range au {
				n += int64(len(nalu))
			}
			t.bytesSent.Add(n)
		}
		monitor.RecordVideo(pts)
		if monitor.ShouldReconnect() {
			select {
			case cbErr <- DriftReconnectError:
			default:
			}
		}
	}); err != nil {
		return err
	}
	defer t.hub.Unsubscribe(consumerID)

	// AAC passthrough: subscribe to audio bus.
	if audioSubID != "" {
		if err := t.hub.SubscribeAudio(audioSubID, func(pts int64, codec model.AudioCodec, data []byte) {
			if codec != model.AudioAAC {
				return
			}
			d := durationFromPTS(pts)
			if d < 0 {
				d = 0
			}
			if werr := writer.WriteMPEG4Audio(audioTrack, d, data); werr != nil {
				select {
				case cbErr <- werr:
				default:
				}
			} else {
				t.bytesSent.Add(int64(len(data)))
			}
			monitor.RecordAudio(pts)
			if monitor.ShouldReconnect() {
				select {
				case cbErr <- DriftReconnectError:
				default:
				}
			}
		}); err != nil {
			return err
		}
		defer t.hub.UnsubscribeAudio(audioSubID)
	}

	// G.711 passthrough: subscribe to audio bus.
	if g711SubID != "" {
		if err := t.hub.SubscribeAudio(g711SubID, func(pts int64, codec model.AudioCodec, data []byte) {
			if codec != model.AudioG711 {
				return
			}
			d := durationFromPTS(pts)
			if d < 0 {
				d = 0
			}
			if werr := writer.WriteG711(g711Track, d, data); werr != nil {
				select {
				case cbErr <- werr:
				default:
				}
			} else {
				t.bytesSent.Add(int64(len(data)))
			}
		}); err != nil {
			return err
		}
		defer t.hub.UnsubscribeAudio(g711SubID)
	}

	// Silent AAC fallback goroutine.
	// Silent AAC fallback goroutine.
	if silentCh != nil {
		go func() {
			for frame := range silentCh {
				d := time.Since(start)
				if d < 0 {
					d = 0
				}
				if werr := writer.WriteMPEG4Audio(audioTrack, d, frame); werr != nil {
					select {
					case cbErr <- werr:
					default:
					}
				} else {
					t.bytesSent.Add(int64(len(frame)))
				}
			}
		}()
	}

	t.setStatus(StatusStreaming, "")
	engineLogger.Info("relay target streaming",
		"camera_id", t.CameraID, "target_id", t.Config.ID, "protocol", "rtmp", "url", t.Config.URL)

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-cbErr:
		return err
	}
}

// --- RTSP target ---

func (t *PushTarget) connectRTSP(ctx context.Context) error {
	sps, pps, isH264 := t.spsProvider()
	if !isH264 {
		// H.265 source — transcode if policy allows.
		if t.Config.TranscodePolicy == "off" {
			t.setStatus(StatusError, "source is not H.264 (RTSP target currently requires H.264)")
			return errPermanent
		}
		if t.presetRegistry == nil {
			t.setStatus(StatusError, "transcode requested but preset registry not configured")
			return errPermanent
		}
		return t.connectRTSPWithTranscode(ctx)
	}
	if sps == nil || pps == nil {
		t.setStatus(StatusError, "source stream not ready (no SPS/PPS yet)")
		return errPermanent
	}

	t.setStatus(StatusConnecting, "")
	tcp := gortsplib.ProtocolTCP
	client := &gortsplib.Client{Protocol: &tcp, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}

	videoForma := &format.H264{
		PayloadTyp:        96,
		SPS:               append([]byte(nil), sps...),
		PPS:               append([]byte(nil), pps...),
		PacketizationMode: 1,
	}

	// Build media list starting with video.
	medias := []*description.Media{{
		Type:    description.MediaTypeVideo,
		Formats: []format.Format{videoForma},
	}}

	// Check for audio support via codecInfoProvider.
	var audioForma format.Format
	var audioMediaIdx int
	audioCodecName := ""
	if t.codecInfoProvider != nil {
		ci := t.codecInfoProvider()
		audioCodecName = ci.AudioCodec
		switch ci.AudioCodec {
		case "aac":
			asc := parseASC(ci.AudioConfig)
			if asc != nil {
				aacForma := &format.MPEG4Audio{
					PayloadTyp:       96,
					Config:           asc,
					SizeLength:       13,
					IndexLength:      3,
					IndexDeltaLength: 3,
				}
				audioForma = aacForma
			}
		case "g711":
			isMULaw, sampleRate := parseG711Config(ci.AudioConfig)
			ch := ci.AudioChannels
			if ch <= 0 {
				ch = 1
			}
			g711Forma := &format.G711{
				PayloadTyp:   0,
				MULaw:        isMULaw,
				SampleRate:   sampleRate,
				ChannelCount: ch,
			}
			audioForma = g711Forma
		}
	}

	if audioForma != nil {
		audioMediaIdx = len(medias)
		medias = append(medias, &description.Media{
			Type:    description.MediaTypeAudio,
			Formats: []format.Format{audioForma},
		})
		engineLogger.Info("RTSP relay includes audio",
			"camera_id", t.CameraID, "target_id", t.Config.ID,
			"audio_codec", audioCodecName)
	}

	desc := &description.Session{Medias: medias}
	if err := client.StartRecording(t.Config.URL, desc); err != nil {
		return err
	}
	defer client.Close()

	// --- Video encoder ---
	videoRtpEnc, err := videoForma.CreateEncoder()
	if err != nil {
		return err
	}
	videoMedia := desc.Medias[0]

	// --- Audio encoder (if configured) ---
	var audioRtpEnc interface{}
	var audioMedia *description.Media
	if audioForma != nil {
		switch af := audioForma.(type) {
		case *format.MPEG4Audio:
			enc, aerr := af.CreateEncoder()
			if aerr != nil {
				engineLogger.Warn("failed to create AAC RTP encoder, continuing video-only",
					"camera_id", t.CameraID, "target_id", t.Config.ID, "error", aerr)
			} else {
				audioRtpEnc = enc
			}
		case *format.G711:
			enc, aerr := af.CreateEncoder()
			if aerr != nil {
				engineLogger.Warn("failed to create G.711 RTP encoder, continuing video-only",
					"camera_id", t.CameraID, "target_id", t.Config.ID, "error", aerr)
			} else {
				audioRtpEnc = enc
			}
		}
		if audioRtpEnc != nil {
			audioMedia = desc.Medias[audioMediaIdx]
		}
	}

	consumerID := "relay-rtsp-" + t.Config.ID
	cbErr := make(chan error, 1)

	monitor := NewDriftMonitor()
	monitor.StartLogging(ctx)
	t.activeDriftMonitor.Store(monitor)
	defer t.activeDriftMonitor.Store(nil)

	// Subscribe to video.
	if err := t.hub.Subscribe(consumerID, func(pts int64, au [][]byte) {
		// Re-encode the access unit into RTP packets, preserving the source
		// 90kHz PTS as the packet timestamp (relay is transparent remux).
		pkts, eerr := videoRtpEnc.Encode(au)
		if eerr != nil {
			return
		}
		base := uint32(pts)
		for i, pkt := range pkts {
			if i == 0 {
				pkt.Timestamp = base
			} else {
				pkt.Timestamp = base + pkt.Timestamp
			}
			if werr := client.WritePacketRTP(videoMedia, pkt); werr != nil {
				select {
				case cbErr <- werr:
				default:
				}
				return
			}
			t.bytesSent.Add(int64(pkt.MarshalSize()))
		}
		monitor.RecordVideo(pts)
		if monitor.ShouldReconnect() {
			select {
			case cbErr <- DriftReconnectError:
			default:
			}
		}
	}); err != nil {
		return err
	}
	defer t.hub.Unsubscribe(consumerID)

	// Subscribe to audio (if configured).
	if audioMedia != nil && audioRtpEnc != nil {
		audioConsumerID := consumerID + "-audio"
		if err := t.hub.SubscribeAudio(audioConsumerID, func(pts int64, _ model.AudioCodec, data []byte) {
			var pkts []*rtp.Packet
			var eerr error
			switch enc := audioRtpEnc.(type) {
			case *rtpmpeg4audio.Encoder:
				pkts, eerr = enc.Encode([][]byte{data})
			case *rtplpcm.Encoder:
				pkts, eerr = enc.Encode(data)
			}
			if eerr != nil {
				return
			}
			base := uint32(pts)
			for i, pkt := range pkts {
				if i == 0 {
					pkt.Timestamp = base
				} else {
					pkt.Timestamp = base + pkt.Timestamp
				}
				if werr := client.WritePacketRTP(audioMedia, pkt); werr != nil {
					select {
					case cbErr <- werr:
					default:
					}
					return
				}
				t.bytesSent.Add(int64(pkt.MarshalSize()))
			}
			monitor.RecordAudio(pts)
			if monitor.ShouldReconnect() {
				select {
				case cbErr <- DriftReconnectError:
				default:
				}
			}
		}); err != nil {
			return err
		}
		defer t.hub.UnsubscribeAudio(audioConsumerID)
	}

	t.setStatus(StatusStreaming, "")
	engineLogger.Info("relay target streaming",
		"camera_id", t.CameraID, "target_id", t.Config.ID, "protocol", "rtsp", "url", t.Config.URL)

	// Drive the client read loop; it surfaces transport errors.
	waitErr := make(chan error, 1)
	go func() { waitErr <- client.Wait() }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-cbErr:
		return err
	case err := <-waitErr:
		return err
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

// --- Transcode relay path ---

// connectRTMPWithTranscode transcodes H.265→H.264 via FFmpeg and pushes to
// the RTMP target. Audio is passed through unchanged (same as normal path).
func (t *PushTarget) connectRTMPWithTranscode(ctx context.Context) error {
	// 1. Resolve encoding preset from platform config.
	resolved := t.presetRegistry.Resolve(t.Config)

	// 2. Map TranscodePolicy to EncoderType.
	encoderType := livetranscode.EncoderAuto
	if t.Config.TranscodePolicy == "force_sw" {
		encoderType = livetranscode.EncoderSW
	}

	// 3. Create and start the transcoder.
	lt := livetranscode.NewLiveTranscoder(livetranscode.TranscoderConfig{
		InputCodec:  livetranscode.CodecH265,
		EncoderType: encoderType,
		FFmpegPath:  t.ffmpegPath,
		Preset:      relayPresetToLT(resolved),
		HardwareCap: t.hardwareCap,
	})
	t.activeTranscoder.Store(lt)
	defer t.activeTranscoder.Store(nil)

	t.setStatus(StatusConnecting, "starting transcoder")
	if err := lt.Start(ctx); err != nil {
		return fmt.Errorf("transcoder start: %w", err)
	}
	defer lt.Stop()

	// 4. Subscribe to source hub — feed H.265 frames to transcoder NOW so FFmpeg
	//    has input data before we wait for SPS/PPS output.
	consumerID := "relay-rtmp-tc-" + t.Config.ID
	cbErr := make(chan error, 1)
	if err := t.hub.Subscribe(consumerID, func(pts int64, au [][]byte) {
		if err := lt.WriteInput(au); err != nil {
			select {
			case cbErr <- fmt.Errorf("transcoder write input: %w", err):
			default:
			}
		}
	}); err != nil {
		return err
	}
	defer t.hub.Unsubscribe(consumerID)

	// Start thermal monitoring for temperature reporting.
	thermalMonitor := livetranscode.NewThermalMonitor(85)
	thermalCh := thermalMonitor.Start(ctx)
	defer thermalMonitor.Stop()
	go func() {
		for evt := range thermalCh {
			t.latestTemperatureC.Store(int64(evt.Temperature))
		}
	}()

	// 5. Wait for first output AU — we need the transcoder's SPS/PPS for the H.264
	//    track init. The hub subscription above is already feeding H.265 frames,
	//    so FFmpeg will produce H.264 output including SPS/PPS.
	ps, err := waitForTranscoderParams(ctx, lt, 15*time.Second)
	if err != nil {
		return fmt.Errorf("transcoder param sets: %w", err)
	}

	engineLogger.Info("transcoder ready, engaging RTMP relay (transcoded)",
		"camera_id", t.CameraID, "target_id", t.Config.ID,
		"encoder", lt.EncoderName(), "preset", resolved.Name)

	// 5. Parse target URL.
	t.setStatus(StatusConnecting, "connecting to RTMP target")
	u, err := url.Parse(t.Config.URL)
	if err != nil || (u.Scheme != "rtmp" && u.Scheme != "rtmps") {
		t.setStatus(StatusError, "invalid RTMP url")
		return errPermanent
	}

	// 6. Build H.264 video track using transcoder's SPS/PPS (not source).
	videoTrack := &gortmplib.Track{Codec: &codecs.H264{
		SPS: append([]byte(nil), ps.SPS...),
		PPS: append([]byte(nil), ps.PPS...),
	}}

	// 7. Audio setup — identical to normal RTMP path.
	silentCtx, silentCancel := context.WithCancel(ctx)
	defer silentCancel()

	var (
		audioTrack *gortmplib.Track
		audioSubID string
		g711Track  *gortmplib.Track
		g711SubID  string
		silentCh   <-chan []byte
	)

	if t.codecInfoProvider != nil {
		ci := t.codecInfoProvider()
		switch ci.AudioCodec {
		case "aac":
			asc := parseASC(ci.AudioConfig)
			if asc != nil {
				audioTrack = &gortmplib.Track{Codec: &codecs.MPEG4Audio{Config: asc}}
				audioSubID = "relay-rtmp-tc-" + t.Config.ID + "-audio"
				engineLogger.Info("relay target adding AAC audio track (transcode)",
					"camera_id", t.CameraID, "target_id", t.Config.ID,
					"sample_rate", asc.SampleRate, "channels", asc.ChannelCount)
			}

		case "g711":
			isMULaw, _ := parseG711Config(ci.AudioConfig)
			ch := ci.AudioChannels
			if ch <= 0 {
				ch = 1
			}
			g711Track = &gortmplib.Track{Codec: &codecs.G711{MULaw: isMULaw, ChannelCount: ch}}
			g711SubID = "relay-rtmp-tc-" + t.Config.ID + "-g711"
			engineLogger.Info("relay target adding G.711 audio track (transcode)",
				"camera_id", t.CameraID, "target_id", t.Config.ID, "mu_law", isMULaw)

		default:
			gen := NewSilenceAACGenerator()
			emitter := NewBufferAwareSilenceEmitter(gen)
			asc := parseASC(emitter.AudioConfig())
			if asc != nil {
				audioTrack = &gortmplib.Track{Codec: &codecs.MPEG4Audio{Config: asc}}
				silentCh = emitter.Start(silentCtx)
				engineLogger.Info("relay target adding silent AAC track (no source audio, transcode)",
					"camera_id", t.CameraID, "target_id", t.Config.ID)
			}
		}
	}

	tracks := []*gortmplib.Track{videoTrack}
	if audioTrack != nil {
		tracks = append(tracks, audioTrack)
	}
	if g711Track != nil {
		tracks = append(tracks, g711Track)
	}

	// 8. Connect RTMP and initialize writer.
	client := &gortmplib.Client{URL: u, Publish: true}
	if err := client.Initialize(ctx); err != nil {
		return err
	}
	defer client.Close()

	writer := &gortmplib.Writer{Conn: client, Tracks: tracks}
	if err := writer.Initialize(); err != nil {
		return err
	}

	// 9. Prepare streaming loop state.
	start := time.Now()

	// 10. Audio subscriptions — AAC passthrough.
	if audioSubID != "" {
		if err := t.hub.SubscribeAudio(audioSubID, func(pts int64, codec model.AudioCodec, data []byte) {
			if codec != model.AudioAAC {
				return
			}
			d := durationFromPTS(pts)
			if d < 0 {
				d = 0
			}
			if werr := writer.WriteMPEG4Audio(audioTrack, d, data); werr != nil {
				select {
				case cbErr <- werr:
				default:
				}
			} else {
				t.bytesSent.Add(int64(len(data)))
			}
		}); err != nil {
			return err
		}
		defer t.hub.UnsubscribeAudio(audioSubID)
	}

	// G.711 passthrough.
	if g711SubID != "" {
		if err := t.hub.SubscribeAudio(g711SubID, func(pts int64, codec model.AudioCodec, data []byte) {
			if codec != model.AudioG711 {
				return
			}
			d := durationFromPTS(pts)
			if d < 0 {
				d = 0
			}
			if werr := writer.WriteG711(g711Track, d, data); werr != nil {
				select {
				case cbErr <- werr:
				default:
				}
			} else {
				t.bytesSent.Add(int64(len(data)))
			}
		}); err != nil {
			return err
		}
		defer t.hub.UnsubscribeAudio(g711SubID)
	}

	// Silent AAC fallback goroutine.
	if silentCh != nil {
		go func() {
			for frame := range silentCh {
				d := time.Since(start)
				if d < 0 {
					d = 0
				}
				if werr := writer.WriteMPEG4Audio(audioTrack, d, frame); werr != nil {
					select {
					case cbErr <- werr:
					default:
					}
				} else {
					t.bytesSent.Add(int64(len(frame)))
				}
			}
		}()
	}

	// 11. Main streaming loop: transcoder output → RTMP writer.
	t.setStatus(StatusStreaming, "")
	engineLogger.Info("relay target streaming (transcoded)",
		"camera_id", t.CameraID, "target_id", t.Config.ID,
		"protocol", "rtmp", "url", t.Config.URL, "encoder", lt.EncoderName())

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case au, ok := <-lt.Output():
			if !ok {
				return fmt.Errorf("transcoder output channel closed")
			}
			d := time.Since(start)
			if d < 0 {
				d = 0
			}
			if werr := writer.WriteH264(videoTrack, d, d, au); werr != nil {
				return werr
			}
			var n int64
			for _, nalu := range au {
				n += int64(len(nalu))
			}
			t.bytesSent.Add(n)
		case err := <-cbErr:
			return err
		}
	}
}

// connectRTSPWithTranscode transcodes H.265→H.264 via FFmpeg and pushes to
// the RTSP target. Audio is passed through unchanged (same as normal path).
func (t *PushTarget) connectRTSPWithTranscode(ctx context.Context) error {
	// 1. Resolve encoding preset from platform config.
	resolved := t.presetRegistry.Resolve(t.Config)

	// 2. Map TranscodePolicy to EncoderType.
	encoderType := livetranscode.EncoderAuto
	if t.Config.TranscodePolicy == "force_sw" {
		encoderType = livetranscode.EncoderSW
	}

	// 3. Create and start the transcoder.
	lt := livetranscode.NewLiveTranscoder(livetranscode.TranscoderConfig{
		InputCodec:  livetranscode.CodecH265,
		EncoderType: encoderType,
		FFmpegPath:  t.ffmpegPath,
		Preset:      relayPresetToLT(resolved),
		HardwareCap: t.hardwareCap,
	})
	t.activeTranscoder.Store(lt)
	defer t.activeTranscoder.Store(nil)

	t.setStatus(StatusConnecting, "starting transcoder")
	if err := lt.Start(ctx); err != nil {
		return fmt.Errorf("transcoder start: %w", err)
	}
	defer lt.Stop()

	// 4. Subscribe to source hub — feed H.265 frames to transcoder NOW so FFmpeg
	//    has input data before we wait for SPS/PPS output.
	consumerID := "relay-rtsp-tc-" + t.Config.ID
	cbErr := make(chan error, 1)
	if err := t.hub.Subscribe(consumerID, func(pts int64, au [][]byte) {
		if err := lt.WriteInput(au); err != nil {
			select {
			case cbErr <- fmt.Errorf("transcoder write input: %w", err):
			default:
			}
		}
	}); err != nil {
		return err
	}
	defer t.hub.Unsubscribe(consumerID)

	// Start thermal monitoring for temperature reporting.
	thermalMonitor := livetranscode.NewThermalMonitor(85)
	thermalCh := thermalMonitor.Start(ctx)
	defer thermalMonitor.Stop()
	go func() {
		for evt := range thermalCh {
			t.latestTemperatureC.Store(int64(evt.Temperature))
		}
	}()

	// 5. Wait for first output AU to extract SPS/PPS from transcoder. The hub
	//    subscription above is already feeding H.265 frames, so FFmpeg will
	//    produce H.264 output including SPS/PPS.
	ps, err := waitForTranscoderParams(ctx, lt, 15*time.Second)
	if err != nil {
		return fmt.Errorf("transcoder param sets: %w", err)
	}

	engineLogger.Info("transcoder ready, engaging RTSP relay (transcoded)",
		"camera_id", t.CameraID, "target_id", t.Config.ID,
		"encoder", lt.EncoderName(), "preset", resolved.Name)

	// 5. Parse URL and connect RTSP.
	t.setStatus(StatusConnecting, "connecting to RTSP target")
	tcpProt := gortsplib.ProtocolTCP
	client := &gortsplib.Client{Protocol: &tcpProt, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}

	// 6. Build H.264 video format using transcoder's SPS/PPS.
	videoForma := &format.H264{
		PayloadTyp:        96,
		SPS:               append([]byte(nil), ps.SPS...),
		PPS:               append([]byte(nil), ps.PPS...),
		PacketizationMode: 1,
	}

	// 7. Audio media setup — identical to normal RTSP path.
	medias := []*description.Media{{
		Type:    description.MediaTypeVideo,
		Formats: []format.Format{videoForma},
	}}

	var audioForma format.Format
	var audioMediaIdx int
	audioCodecName := ""
	if t.codecInfoProvider != nil {
		ci := t.codecInfoProvider()
		audioCodecName = ci.AudioCodec
		switch ci.AudioCodec {
		case "aac":
			asc := parseASC(ci.AudioConfig)
			if asc != nil {
				audioForma = &format.MPEG4Audio{
					PayloadTyp:       96,
					Config:           asc,
					SizeLength:       13,
					IndexLength:      3,
					IndexDeltaLength: 3,
				}
			}
		case "g711":
			isMULaw, sampleRate := parseG711Config(ci.AudioConfig)
			ch := ci.AudioChannels
			if ch <= 0 {
				ch = 1
			}
			audioForma = &format.G711{
				PayloadTyp:   0,
				MULaw:        isMULaw,
				SampleRate:   sampleRate,
				ChannelCount: ch,
			}
		}
	}

	if audioForma != nil {
		audioMediaIdx = len(medias)
		medias = append(medias, &description.Media{
			Type:    description.MediaTypeAudio,
			Formats: []format.Format{audioForma},
		})
		engineLogger.Info("RTSP relay includes audio (transcode)",
			"camera_id", t.CameraID, "target_id", t.Config.ID,
			"audio_codec", audioCodecName)
	}

	desc := &description.Session{Medias: medias}
	if err := client.StartRecording(t.Config.URL, desc); err != nil {
		return err
	}
	defer client.Close()

	// 8. Create video RTP encoder.
	videoRtpEnc, err := videoForma.CreateEncoder()
	if err != nil {
		return err
	}
	videoMedia := desc.Medias[0]

	// 9. Create audio RTP encoder (if configured).
	var audioRtpEnc interface{}
	var audioMedia *description.Media
	if audioForma != nil {
		switch af := audioForma.(type) {
		case *format.MPEG4Audio:
			enc, aerr := af.CreateEncoder()
			if aerr != nil {
				engineLogger.Warn("failed to create AAC RTP encoder, continuing video-only (transcode)",
					"camera_id", t.CameraID, "target_id", t.Config.ID, "error", aerr)
			} else {
				audioRtpEnc = enc
			}
		case *format.G711:
			enc, aerr := af.CreateEncoder()
			if aerr != nil {
				engineLogger.Warn("failed to create G.711 RTP encoder, continuing video-only (transcode)",
					"camera_id", t.CameraID, "target_id", t.Config.ID, "error", aerr)
			} else {
				audioRtpEnc = enc
			}
		}
		if audioRtpEnc != nil {
			audioMedia = desc.Medias[audioMediaIdx]
		}
	}

	// 11. Audio subscription (if configured).
	if audioMedia != nil && audioRtpEnc != nil {
		audioConsumerID := consumerID + "-audio"
		if err := t.hub.SubscribeAudio(audioConsumerID, func(pts int64, _ model.AudioCodec, data []byte) {
			var pkts []*rtp.Packet
			var eerr error
			switch enc := audioRtpEnc.(type) {
			case *rtpmpeg4audio.Encoder:
				pkts, eerr = enc.Encode([][]byte{data})
			case *rtplpcm.Encoder:
				pkts, eerr = enc.Encode(data)
			}
			if eerr != nil {
				return
			}
			base := uint32(pts)
			for i, pkt := range pkts {
				if i == 0 {
					pkt.Timestamp = base
				} else {
					pkt.Timestamp = base + pkt.Timestamp
				}
				if werr := client.WritePacketRTP(audioMedia, pkt); werr != nil {
					select {
					case cbErr <- werr:
					default:
					}
					return
				}
				t.bytesSent.Add(int64(pkt.MarshalSize()))
			}
		}); err != nil {
			return err
		}
		defer t.hub.UnsubscribeAudio(audioConsumerID)
	}

	// 12. Main streaming loop: transcoder output → RTSP RTP packets.
	start := time.Now()
	t.setStatus(StatusStreaming, "")
	engineLogger.Info("relay target streaming (transcoded)",
		"camera_id", t.CameraID, "target_id", t.Config.ID,
		"protocol", "rtsp", "url", t.Config.URL, "encoder", lt.EncoderName())

	waitErr := make(chan error, 1)
	go func() { waitErr <- client.Wait() }()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case au, ok := <-lt.Output():
			if !ok {
				return fmt.Errorf("transcoder output channel closed")
			}
			// Use wall-clock time for PTS (transcoder doesn't preserve source timing).
			pts := uint32(time.Since(start) / (time.Second / 90000))
			pkts, eerr := videoRtpEnc.Encode(au)
			if eerr != nil {
				continue
			}
			for i, pkt := range pkts {
				if i == 0 {
					pkt.Timestamp = pts
				} else {
					pkt.Timestamp = pts + pkt.Timestamp
				}
				if werr := client.WritePacketRTP(videoMedia, pkt); werr != nil {
					return werr
				}
				t.bytesSent.Add(int64(pkt.MarshalSize()))
			}
		case err := <-cbErr:
			return err
		case err := <-waitErr:
			return err
		}
	}
}

// waitForTranscoderParams drains the transcoder output until valid SPS/PPS are
// observed, or the timeout expires. These parameter sets are used to initialise
// the target connection (the transcoder's H.264 output, not the source H.265).
func waitForTranscoderParams(ctx context.Context, lt *livetranscode.LiveTranscoder, timeout time.Duration) (livetranscode.ParamSets, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return livetranscode.ParamSets{}, ctx.Err()
		case <-timer.C:
			// Check once more before declaring timeout.
			ps := lt.ParamSets()
			if len(ps.SPS) > 0 && len(ps.PPS) > 0 {
				return ps, nil
			}
			return livetranscode.ParamSets{}, fmt.Errorf("timed out after %v waiting for transcoder SPS/PPS", timeout)
		case au, ok := <-lt.Output():
			if !ok {
				return livetranscode.ParamSets{}, fmt.Errorf("transcoder output channel closed while waiting for params")
			}
			_ = au // drain — param sets are extracted by the parser internally
			ps := lt.ParamSets()
			if len(ps.SPS) > 0 && len(ps.PPS) > 0 {
				return ps, nil
			}
		}
	}
}

// relayPresetToLT converts a relay.ResolvedPreset to livetranscode.ResolvedPreset.
// The structs are identical but in separate packages to avoid an import cycle.
func relayPresetToLT(p ResolvedPreset) livetranscode.ResolvedPreset {
	return livetranscode.ResolvedPreset{
		Name:             p.Name,
		GopSeconds:       p.GopSeconds,
		VideoBitrateKbps: p.VideoBitrateKbps,
		AudioBitrateKbps: p.AudioBitrateKbps,
		Resolution:       p.Resolution,
		Framerate:        p.Framerate,
		Profile:          p.Profile,
		Bframes:          p.Bframes,
	}
}

// --- Audio helpers ---

// parseASC unmarshals an AudioSpecificConfig from raw bytes.
// Returns nil if config is empty or unparsable.
func parseASC(config []byte) *mpeg4audio.AudioSpecificConfig {
	if len(config) == 0 {
		return nil
	}
	asc := &mpeg4audio.AudioSpecificConfig{}
	if err := asc.Unmarshal(config); err != nil {
		engineLogger.Warn("failed to unmarshal AudioSpecificConfig", "error", err)
		return nil
	}
	return asc
}

// parseG711Config parses the G.711 audio config bytes stored by the recorder.
// Format: [muLawFlag (1 byte), sampleRate (4 bytes big-endian)].
func parseG711Config(config []byte) (isMULaw bool, sampleRate int) {
	if len(config) < 5 {
		return false, 8000
	}
	isMULaw = config[0] != 0
	sampleRate = int(config[1])<<24 | int(config[2])<<16 | int(config[3])<<8 | int(config[4])
	if sampleRate <= 0 {
		sampleRate = 8000
	}
	return
}

// durationFromPTS converts a 90kHz PTS value to a time.Duration.
// Example: pts=45000 -> 500ms (45000/90000 = 0.5s).
func durationFromPTS(pts int64) time.Duration {
	if pts < 0 {
		return 0
	}
	return time.Duration(pts) * time.Second / 90000
}

// Sentinel errors.
var (
	errPermanent = errPermanentDef{}
)

type errPermanentDef struct{}

func (errPermanentDef) Error() string { return "permanent relay error (no retry)" }
