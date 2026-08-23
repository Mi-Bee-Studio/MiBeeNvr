package relay

import (
	"context"
	"fmt"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtplpcm"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtpmpeg4audio"
	"github.com/pion/rtp"
)

// --- RTSP target ---

func (t *PushTarget) connectRTSP(ctx context.Context) error {
	sps, pps, isH264 := t.spsProvider()
	if !isH264 {
		// MJPEG/JPEG sources can never feed the H.265 transcode path — fail
		// fast with a clear per-target error instead of a doomed transcoder
		// (#423). Wrapped so the detail survives the Run loop's err.Error()
		// surfacing (see connect_rtmp.go).
		if t.isJPEGSource() {
			msg := "source is " + t.sourceCodec() + "; RTSP push requires H.264/H.265"
			t.setStatus(StatusError, msg)
			return fmt.Errorf("%w: %s", errPermanent, msg)
		}
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
