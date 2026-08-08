package relay

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/livetranscode"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/bluenviron/gortmplib"
	"github.com/bluenviron/gortmplib/pkg/codecs"
	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtplpcm"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtpmpeg4audio"
	"github.com/pion/rtp"
)

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
