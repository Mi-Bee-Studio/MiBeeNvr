package relay

import (
	"context"
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
