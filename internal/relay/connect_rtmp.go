package relay

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/bluenviron/gortmplib"
	"github.com/bluenviron/gortmplib/pkg/codecs"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
)

// --- RTMP target ---

func (t *PushTarget) connectRTMP(ctx context.Context) error {
	sps, pps, isH264 := t.spsProvider()
	if !isH264 {
		// MJPEG/JPEG sources can never feed the H.265 transcode path — fail
		// fast with a clear per-target error instead of a doomed transcoder
		// (#423). The sentinel is wrapped with the detail because the Run loop
		// surfaces err.Error() in push-status/logs and would otherwise stomp
		// the message set above with the generic "no retry" text.
		if t.isJPEGSource() {
			msg := "source is " + t.sourceCodec() + "; RTMP push requires H.264/H.265"
			t.setStatus(StatusError, msg)
			return fmt.Errorf("%w: %s", errPermanent, msg)
		}
		// H.265 source, passthrough policy — publish natively via
		// enhanced-RTMP (hvc1 fourcc). Only viable between enhanced-RTMP-aware
		// receivers (our own NVR ingest, SRS, FFmpeg 6.1+); third-party
		// platforms (Douyu etc.) mostly reject hvc1, which is why `auto`
		// keeps transcoding to H.264.
		if t.Config.TranscodePolicy == "passthrough" {
			return t.connectRTMPH265Passthrough(ctx)
		}
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

// connectRTMPH265Passthrough publishes an H.265 source natively via
// enhanced-RTMP (hvc1 fourcc) without any transcoding (#433). Requires an
// enhanced-RTMP-aware receiver (our own NVR RTMP ingest, SRS, FFmpeg 6.1+).
// The gortmplib writer emits the VideoExSequenceStart (hvc1) header plus
// VideoExFramesX frame messages; the custom handshake conn passes those
// through its standard writer untouched (the Type-0 interception only
// rewrites classic AVC/Audio messages for strict FMS receivers, which do not
// accept hvc1 in the first place).
//
// Audio: AAC and G.711 are forwarded when the source has them (same as the
// H.264 path), but no silent-AAC fallback is injected — passthrough targets
// are machine-to-machine and synthetic audio would only burn bitrate.
func (t *PushTarget) connectRTMPH265Passthrough(ctx context.Context) error {
	t.setStatus(StatusConnecting, "")
	u, err := url.Parse(t.Config.URL)
	if err != nil || (u.Scheme != "rtmp" && u.Scheme != "rtmps") {
		t.setStatus(StatusError, "invalid RTMP url")
		return errPermanent
	}

	if t.codecInfoProvider == nil {
		msg := "passthrough requires a codec info provider"
		t.setStatus(StatusError, msg)
		return errPermanent
	}
	ci := t.codecInfoProvider()
	if ci.VPS == nil || ci.SPS == nil || ci.PPS == nil {
		t.setStatus(StatusError, "source stream not ready (no H.265 VPS/SPS/PPS yet)")
		return errPermanent
	}

	videoTrack := &gortmplib.Track{Codec: &codecs.H265{
		VPS: append([]byte(nil), ci.VPS...),
		SPS: append([]byte(nil), ci.SPS...),
		PPS: append([]byte(nil), ci.PPS...),
	}}

	var (
		audioTrack *gortmplib.Track
		audioSubID string
		g711Track  *gortmplib.Track
		g711SubID  string
	)
	switch ci.AudioCodec {
	case "aac":
		if asc := parseASC(ci.AudioConfig); asc != nil {
			audioTrack = &gortmplib.Track{Codec: &codecs.MPEG4Audio{Config: asc}}
			audioSubID = "relay-rtmp-h265-" + t.Config.ID + "-audio"
			engineLogger.Info("relay target adding AAC audio track (h265 passthrough)",
				"camera_id", t.CameraID, "target_id", t.Config.ID)
		}
	case "g711":
		isMULaw, _ := parseG711Config(ci.AudioConfig)
		ch := ci.AudioChannels
		if ch <= 0 {
			ch = 1
		}
		g711Track = &gortmplib.Track{Codec: &codecs.G711{MULaw: isMULaw, ChannelCount: ch}}
		g711SubID = "relay-rtmp-h265-" + t.Config.ID + "-g711"
		engineLogger.Info("relay target adding G.711 audio track (h265 passthrough)",
			"camera_id", t.CameraID, "target_id", t.Config.ID, "mu_law", isMULaw)
	}

	tracks := []*gortmplib.Track{videoTrack}
	if audioTrack != nil {
		tracks = append(tracks, audioTrack)
	}
	if g711Track != nil {
		tracks = append(tracks, g711Track)
	}

	// width/height/fps are unknown (the h265 SPS parser exposes no helpers in
	// the vendored mediacommon) — onMetaData omits them, which enhanced-RTMP
	// receivers tolerate (they configure from the hvc1 sequence header).
	conn, connCleanup, err := dialRTMPPublish(ctx, t.Config.URL, 0, 0, 0)
	if err != nil {
		return err
	}
	defer connCleanup()

	writer := &gortmplib.Writer{Conn: conn, Tracks: tracks}
	if err := writer.Initialize(); err != nil {
		return err
	}

	start := time.Now()
	consumerID := "relay-rtmp-h265-" + t.Config.ID
	cbErr := make(chan error, 1)

	if err := t.hub.Subscribe(consumerID, func(pts int64, au [][]byte) {
		// Wall-clock relative PTS, dts == pts (no B-frame reorder), same
		// convention as the H.264 path.
		d := time.Since(start)
		if d < 0 {
			d = 0
		}
		if werr := writer.WriteH265(videoTrack, d, d, au); werr != nil {
			select {
			case cbErr <- werr:
			default:
			}
		} else {
			var n int64
			for _, nalu := range au {
				n += int64(len(nalu))
			}
			t.bytesSent.Add(n)
		}
	}); err != nil {
		return err
	}
	defer t.hub.Unsubscribe(consumerID)

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

	t.setStatus(StatusStreaming, "")
	engineLogger.Info("relay target streaming (h265 passthrough, enhanced-RTMP hvc1)",
		"camera_id", t.CameraID, "target_id", t.Config.ID, "url", t.Config.URL)

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-cbErr:
		return err
	}
}
