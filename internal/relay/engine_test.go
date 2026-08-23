package relay

import (
	"context"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/transcoding"
	"github.com/bluenviron/gortmplib"
	"github.com/bluenviron/gortmplib/pkg/codecs"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// parseASC helper tests
// ---------------------------------------------------------------------------

func TestParseASC_Valid(t *testing.T) {
	// AudioSpecificConfig for AAC-LC, 48 kHz, stereo → []byte{0x11, 0x90}
	config := []byte{0x11, 0x90}
	asc := parseASC(config)
	require.NotNil(t, asc)
	require.Equal(t, mpeg4audio.ObjectTypeAACLC, asc.Type)
	require.Equal(t, 48000, asc.SampleRate)
	require.Equal(t, uint8(2), asc.ChannelConfig)
}

func TestParseASC_Empty(t *testing.T) {
	require.Nil(t, parseASC(nil))
	require.Nil(t, parseASC([]byte{}))
}

func TestParseASC_Invalid(t *testing.T) {
	// Truncated / invalid config returns nil
	require.Nil(t, parseASC([]byte{0x11}))
}

// ---------------------------------------------------------------------------
// parseG711Config helper tests
// ---------------------------------------------------------------------------

func TestParseG711Config_MULaw(t *testing.T) {
	// mu-law, 8000 Hz
	config := []byte{1, 0, 0, 0x1F, 0x40} // 8000 = 0x1F40
	isMULaw, sampleRate := parseG711Config(config)
	require.True(t, isMULaw)
	require.Equal(t, 8000, sampleRate)
}

func TestParseG711Config_ALaw(t *testing.T) {
	// a-law, 8000 Hz
	config := []byte{0, 0, 0, 0x1F, 0x40}
	isMULaw, sampleRate := parseG711Config(config)
	require.False(t, isMULaw)
	require.Equal(t, 8000, sampleRate)
}

func TestParseG711Config_CustomRate(t *testing.T) {
	// mu-law, 16000 Hz = 0x3E80
	config := []byte{1, 0, 0, 0x3E, 0x80}
	isMULaw, sampleRate := parseG711Config(config)
	require.True(t, isMULaw)
	require.Equal(t, 16000, sampleRate)
}

func TestParseG711Config_Empty(t *testing.T) {
	isMULaw, sampleRate := parseG711Config(nil)
	require.False(t, isMULaw)
	require.Equal(t, 8000, sampleRate)

	isMULaw, sampleRate = parseG711Config([]byte{})
	require.False(t, isMULaw)
	require.Equal(t, 8000, sampleRate)
}

func TestParseG711Config_Short(t *testing.T) {
	// Less than 5 bytes
	isMULaw, sampleRate := parseG711Config([]byte{1, 2, 3})
	require.False(t, isMULaw)
	require.Equal(t, 8000, sampleRate)
}

// ---------------------------------------------------------------------------
// Audio media building logic
// ---------------------------------------------------------------------------

// buildRTSPMedias constructs the media list for an RTSP target, mirroring the
// logic in connectRTSP. Returns the media list and the audio media index.
func buildRTSPMedias(videoForma *format.H264, ci model.CodecInfo) ([]*description.Media, int) {
	medias := []*description.Media{{
		Type:    description.MediaTypeVideo,
		Formats: []format.Format{videoForma},
	}}
	audioMediaIdx := -1

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
			audioMediaIdx = len(medias)
			medias = append(medias, &description.Media{
				Type:    description.MediaTypeAudio,
				Formats: []format.Format{aacForma},
			})
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
		audioMediaIdx = len(medias)
		medias = append(medias, &description.Media{
			Type:    description.MediaTypeAudio,
			Formats: []format.Format{g711Forma},
		})
	}

	return medias, audioMediaIdx
}

func TestRTSPAudio_VideoOnly(t *testing.T) {
	videoForma := &format.H264{PayloadTyp: 96, SPS: []byte{0x67}, PPS: []byte{0x68}, PacketizationMode: 1}
	ci := model.CodecInfo{} // no audio

	medias, audioIdx := buildRTSPMedias(videoForma, ci)
	require.Equal(t, 1, len(medias))
	require.Equal(t, -1, audioIdx)
	require.Equal(t, description.MediaTypeVideo, medias[0].Type)
}

func TestRTSPAudio_AAC(t *testing.T) {
	videoForma := &format.H264{PayloadTyp: 96, SPS: []byte{0x67}, PPS: []byte{0x68}, PacketizationMode: 1}
	ci := model.CodecInfo{
		AudioCodec:  "aac",
		AudioConfig: []byte{0x11, 0x90}, // AAC-LC 48kHz stereo
	}

	medias, audioIdx := buildRTSPMedias(videoForma, ci)
	require.Equal(t, 2, len(medias))
	require.Equal(t, 1, audioIdx)
	require.Equal(t, description.MediaTypeVideo, medias[0].Type)
	require.Equal(t, description.MediaTypeAudio, medias[1].Type)

	// Verify AAC format
	aacForma, ok := medias[1].Formats[0].(*format.MPEG4Audio)
	require.True(t, ok, "audio format should be MPEG4Audio")
	require.Equal(t, uint8(96), aacForma.PayloadTyp)
	require.NotNil(t, aacForma.Config)
	require.Equal(t, 48000, aacForma.Config.SampleRate)
	require.Equal(t, uint8(2), aacForma.Config.ChannelConfig)
	require.Equal(t, 13, aacForma.SizeLength)
	require.Equal(t, 3, aacForma.IndexLength)
	require.Equal(t, 3, aacForma.IndexDeltaLength)
}

func TestRTSPAudio_G711MULaw(t *testing.T) {
	videoForma := &format.H264{PayloadTyp: 96, SPS: []byte{0x67}, PPS: []byte{0x68}, PacketizationMode: 1}
	ci := model.CodecInfo{
		AudioCodec:      "g711",
		AudioConfig:     []byte{1, 0, 0, 0x1F, 0x40}, // mu-law 8000Hz
		AudioSampleRate: 8000,
		AudioChannels:   1,
	}

	medias, audioIdx := buildRTSPMedias(videoForma, ci)
	require.Equal(t, 2, len(medias))
	require.Equal(t, 1, audioIdx)

	g711Forma, ok := medias[1].Formats[0].(*format.G711)
	require.True(t, ok, "audio format should be G711")
	require.Equal(t, uint8(0), g711Forma.PayloadTyp)
	require.True(t, g711Forma.MULaw)
	require.Equal(t, 8000, g711Forma.SampleRate)
	require.Equal(t, 1, g711Forma.ChannelCount)
}

func TestRTSPAudio_G711ALaw(t *testing.T) {
	videoForma := &format.H264{PayloadTyp: 96, SPS: []byte{0x67}, PPS: []byte{0x68}, PacketizationMode: 1}
	ci := model.CodecInfo{
		AudioCodec:      "g711",
		AudioConfig:     []byte{0, 0, 0, 0x1F, 0x40}, // a-law 8000Hz
		AudioSampleRate: 8000,
		AudioChannels:   1,
	}

	medias, audioIdx := buildRTSPMedias(videoForma, ci)
	require.Equal(t, 2, len(medias))
	require.Equal(t, 1, audioIdx)

	g711Forma, ok := medias[1].Formats[0].(*format.G711)
	require.True(t, ok)
	require.False(t, g711Forma.MULaw)
	require.Equal(t, 8000, g711Forma.SampleRate)
}

func TestRTSPAudio_AACInvalidConfig(t *testing.T) {
	videoForma := &format.H264{PayloadTyp: 96, SPS: []byte{0x67}, PPS: []byte{0x68}, PacketizationMode: 1}
	ci := model.CodecInfo{
		AudioCodec:  "aac",
		AudioConfig: []byte{0x11}, // truncated — should fail unmarshal
	}

	medias, audioIdx := buildRTSPMedias(videoForma, ci)
	require.Equal(t, 1, len(medias), "should fall back to video-only when AAC config is invalid")
	require.Equal(t, -1, audioIdx)
}

func TestRTSPAudio_G711EmptyConfig(t *testing.T) {
	videoForma := &format.H264{PayloadTyp: 96, SPS: []byte{0x67}, PPS: []byte{0x68}, PacketizationMode: 1}
	ci := model.CodecInfo{
		AudioCodec:    "g711",
		AudioConfig:   nil, // empty — use defaults
		AudioChannels: 1,
	}

	medias, audioIdx := buildRTSPMedias(videoForma, ci)
	require.Equal(t, 2, len(medias))
	require.Equal(t, 1, audioIdx)

	g711Forma, ok := medias[1].Formats[0].(*format.G711)
	require.True(t, ok)
	require.False(t, g711Forma.MULaw)
	require.Equal(t, 8000, g711Forma.SampleRate)
}

func TestRTSPAudio_UnsupportedCodec(t *testing.T) {
	videoForma := &format.H264{PayloadTyp: 96, SPS: []byte{0x67}, PPS: []byte{0x68}, PacketizationMode: 1}
	ci := model.CodecInfo{
		AudioCodec: "opus", // unsupported — should be video-only
	}

	medias, audioIdx := buildRTSPMedias(videoForma, ci)
	require.Equal(t, 1, len(medias))
	require.Equal(t, -1, audioIdx)
}

// ---------------------------------------------------------------------------
// SetCodecInfoProvider test
// ---------------------------------------------------------------------------

func TestPushTarget_SetCodecInfoProvider(t *testing.T) {
	target := NewPushTarget("test-cam", PushTargetConfig{}, nil, func() ([]byte, []byte, bool) {
		return []byte{0x67}, []byte{0x68}, true
	})
	require.Nil(t, target.codecInfoProvider, "should be nil before SetCodecInfoProvider")

	called := false
	target.SetCodecInfoProvider(func() model.CodecInfo {
		called = true
		return model.CodecInfo{AudioCodec: "aac"}
	})
	require.NotNil(t, target.codecInfoProvider)

	ci := target.codecInfoProvider()
	require.True(t, called)
	require.Equal(t, "aac", ci.AudioCodec)
}

// ---------------------------------------------------------------------------
// durationFromPTS helper tests
// ---------------------------------------------------------------------------

func TestDurationFromPTS_Zero(t *testing.T) {
	d := durationFromPTS(0)
	require.Equal(t, time.Duration(0), d)
}

func TestDurationFromPTS_Positive(t *testing.T) {
	// 45000 ticks @ 90kHz = 0.5s
	d := durationFromPTS(45000)
	require.Equal(t, 500*time.Millisecond, d)
}

func TestDurationFromPTS_Negative(t *testing.T) {
	d := durationFromPTS(-100)
	require.Equal(t, time.Duration(0), d, "negative PTS should return 0")
}

func TestDurationFromPTS_OneSecond(t *testing.T) {
	// 90000 ticks @ 90kHz = 1s
	d := durationFromPTS(90000)
	require.Equal(t, time.Second, d)
}

// ---------------------------------------------------------------------------
// RTMP Audio path integration tests
// ---------------------------------------------------------------------------

// TestRTMPAudio_CodecInfoNil verifies nil provider is handled safely.
func TestRTMPAudio_CodecInfoNil(t *testing.T) {
	target := &PushTarget{
		CameraID: "test-cam",
		Config:   PushTargetConfig{ID: "t1"},
	}
	require.Nil(t, target.codecInfoProvider)
}

// TestRTMPAudio_AACTrackCreation verifies AAC config produces a valid MPEG4Audio track.
func TestRTMPAudio_AACTrackCreation(t *testing.T) {
	ci := model.CodecInfo{
		AudioCodec:      "aac",
		AudioConfig:     []byte{0x11, 0x90},
		AudioSampleRate: 48000,
		AudioChannels:   2,
	}

	asc := parseASC(ci.AudioConfig)
	require.NotNil(t, asc)

	audioTrack := &gortmplib.Track{Codec: &codecs.MPEG4Audio{Config: asc}}
	require.NotNil(t, audioTrack)
	require.False(t, audioTrack.Codec.IsVideo())

	mpeg4, ok := audioTrack.Codec.(*codecs.MPEG4Audio)
	require.True(t, ok)
	require.Equal(t, 48000, mpeg4.Config.SampleRate)
	require.Equal(t, uint8(2), mpeg4.Config.ChannelConfig)
}

// TestRTMPAudio_AACInvalidConfig verifies nil ASC skips audio track.
func TestRTMPAudio_AACInvalidConfig(t *testing.T) {
	ci := model.CodecInfo{
		AudioCodec:  "aac",
		AudioConfig: []byte{0x11}, // truncated — invalid
	}
	asc := parseASC(ci.AudioConfig)
	require.Nil(t, asc, "truncated ASC should return nil")
}

// TestRTMPAudio_SilentFallbackConfig verifies the silent AAC fallback
// produces a valid parseable ASC.
func TestRTMPAudio_SilentFallbackConfig(t *testing.T) {
	gen := NewSilenceAACGenerator()
	emitter := NewBufferAwareSilenceEmitter(gen)
	configBytes := emitter.AudioConfig()
	require.NotNil(t, configBytes)
	require.Equal(t, []byte{0x11, 0x90}, configBytes)

	// Verify parseASC can round-trip.
	asc := parseASC(configBytes)
	require.NotNil(t, asc)
	require.Equal(t, mpeg4audio.ObjectTypeAACLC, asc.Type)
	require.Equal(t, 48000, asc.SampleRate)

	audioTrack := &gortmplib.Track{Codec: &codecs.MPEG4Audio{Config: asc}}
	require.NotNil(t, audioTrack)
	require.False(t, audioTrack.Codec.IsVideo())
}

// TestG711Passthrough verifies G.711 codec info produces a valid G.711 track
// using the same parseG711Config → codecs.G711 path used in connectRTMP.
func TestG711Passthrough(t *testing.T) {
	ci := model.CodecInfo{
		AudioCodec:      "g711",
		AudioConfig:     []byte{0x01, 0x00, 0x00, 0x1f, 0x40}, // mu-law, 8000 Hz
		AudioSampleRate: 8000,
		AudioChannels:   1,
	}
	require.Equal(t, "g711", ci.AudioCodec)

	isMULaw, sampleRate := parseG711Config(ci.AudioConfig)
	require.True(t, isMULaw, "config should be mu-law")
	require.Equal(t, 8000, sampleRate)

	g711Track := &gortmplib.Track{Codec: &codecs.G711{MULaw: isMULaw, ChannelCount: ci.AudioChannels}}
	require.NotNil(t, g711Track)
	require.False(t, g711Track.Codec.IsVideo())

	g711, ok := g711Track.Codec.(*codecs.G711)
	require.True(t, ok, "track codec should be *codecs.G711")
	require.True(t, g711.MULaw)
	require.Equal(t, 1, g711.ChannelCount)
}

// TestRTMPAudio_NoAudioSource verifies empty codec info falls to silent path.
func TestRTMPAudio_NoAudioSource(t *testing.T) {
	ci := model.CodecInfo{
		AudioCodec:      "",
		AudioConfig:     nil,
		AudioSampleRate: 0,
		AudioChannels:   0,
	}
	require.Equal(t, "", ci.AudioCodec)
	require.Nil(t, ci.AudioConfig)
}

// ---------------------------------------------------------------------------
// Transcode path tests
// ---------------------------------------------------------------------------

func TestRelayPresetToLT(t *testing.T) {
	rp := ResolvedPreset{
		Name: "test", GopSeconds: 2, VideoBitrateKbps: 3000,
		AudioBitrateKbps: 128, Resolution: "1920x1080",
		Framerate: 30, Profile: "main", Bframes: 0,
	}
	lt := relayPresetToLT(rp)
	require.Equal(t, "test", lt.Name)
	require.Equal(t, 2, lt.GopSeconds)
	require.Equal(t, 3000, lt.VideoBitrateKbps)
	require.Equal(t, 128, lt.AudioBitrateKbps)
	require.Equal(t, "1920x1080", lt.Resolution)
	require.Equal(t, 30, lt.Framerate)
	require.Equal(t, "main", lt.Profile)
	require.Equal(t, 0, lt.Bframes)
}

func TestConnectRTMP_H265TranscodePolicyOff(t *testing.T) {
	// When source is H.265 and TranscodePolicy is "off", connectRTMP should
	// return errPermanent without attempting transcode.
	target := NewPushTarget("test-cam", PushTargetConfig{
		ID: "t1", URL: "rtmp://invalid/does/not/matter",
		Protocol: "rtmp", TranscodePolicy: "off",
	}, nil, func() ([]byte, []byte, bool) {
		return nil, nil, false // H.265 source
	})

	err := target.connectRTMP(context.Background())
	require.Error(t, err)
	require.Equal(t, errPermanent, err)
	require.Equal(t, StatusError, target.status)
}

func TestConnectRTSP_H265TranscodePolicyOff(t *testing.T) {
	target := NewPushTarget("test-cam", PushTargetConfig{
		ID: "t1", URL: "rtsp://invalid/does/not/matter",
		Protocol: "rtsp", TranscodePolicy: "off",
	}, nil, func() ([]byte, []byte, bool) {
		return nil, nil, false // H.265 source
	})

	err := target.connectRTSP(context.Background())
	require.Error(t, err)
	require.Equal(t, errPermanent, err)
	require.Equal(t, StatusError, target.status)
}

func TestConnectRTMP_H265TranscodeNoRegistry(t *testing.T) {
	// When source is H.265, TranscodePolicy is "auto" but no PresetRegistry
	// is configured, connectRTMP should return errPermanent.
	target := NewPushTarget("test-cam", PushTargetConfig{
		ID: "t1", URL: "rtmp://invalid/does/not/matter",
		Protocol: "rtmp", TranscodePolicy: "auto",
	}, nil, func() ([]byte, []byte, bool) {
		return nil, nil, false // H.265 source
	})

	err := target.connectRTMP(context.Background())
	require.Error(t, err)
	require.Equal(t, errPermanent, err)
	require.Equal(t, StatusError, target.status)
	require.Contains(t, target.errMsg, "preset registry not configured")
}

func TestConnectRTSP_H265TranscodeNoRegistry(t *testing.T) {
	target := NewPushTarget("test-cam", PushTargetConfig{
		ID: "t1", URL: "rtsp://invalid/does/not/matter",
		Protocol: "rtsp", TranscodePolicy: "auto",
	}, nil, func() ([]byte, []byte, bool) {
		return nil, nil, false // H.265 source
	})

	err := target.connectRTSP(context.Background())
	require.Error(t, err)
	require.Equal(t, errPermanent, err)
	require.Equal(t, StatusError, target.status)
	require.Contains(t, target.errMsg, "preset registry not configured")
}

func TestConnectRTMP_H264PassThrough(t *testing.T) {
	// When source is H.264 with valid SPS/PPS, the existing pass-through path
	// should be taken (not transcode). It will fail on connect, but the error
	// should NOT be about H.265 or transcode.
	target := NewPushTarget("test-cam", PushTargetConfig{
		ID: "t1", URL: "rtmp://invalid/does/not/matter",
		Protocol: "rtmp", TranscodePolicy: "auto",
	}, nil, func() ([]byte, []byte, bool) {
		return []byte{0x67}, []byte{0x68}, true // H.264 source
	})

	err := target.connectRTMP(context.Background())
	require.Error(t, err)
	// Should NOT be errPermanent (no transcode mismatch) — it will be a
	// connection error, not a codec error.
	require.NotEqual(t, errPermanent, err)
}

func TestPushTarget_SetPresetRegistry(t *testing.T) {
	target := NewPushTarget("test-cam", PushTargetConfig{}, nil, func() ([]byte, []byte, bool) {
		return []byte{0x67}, []byte{0x68}, true
	})
	require.Nil(t, target.presetRegistry)

	r := NewPresetRegistry()
	target.SetPresetRegistry(r)
	require.NotNil(t, target.presetRegistry)
	require.Same(t, r, target.presetRegistry)
}

func TestPushTarget_SetHardwareCap(t *testing.T) {
	target := NewPushTarget("test-cam", PushTargetConfig{}, nil, func() ([]byte, []byte, bool) {
		return []byte{0x67}, []byte{0x68}, true
	})
	require.Nil(t, target.hardwareCap)

	target.SetHardwareCap(&transcoding.HardwareCapabilities{Arch: "arm64"})
	require.NotNil(t, target.hardwareCap)
	require.Equal(t, "arm64", target.hardwareCap.Arch)
}

// ---------------------------------------------------------------------------
// Status extended fields tests
// ---------------------------------------------------------------------------

func TestPushTarget_Status_ExtendedFields_Defaults(t *testing.T) {
	target := NewPushTarget("test-cam", PushTargetConfig{
		ID: "t1", Name: "Test Target", Protocol: "rtmp",
		Platform: "youtube", TranscodePolicy: "auto",
	}, nil, func() ([]byte, []byte, bool) {
		return []byte{0x67}, []byte{0x68}, true
	})

	st := target.Status()

	assert.Equal(t, "youtube", st.Platform)
	assert.Equal(t, "auto", st.TranscodePolicy)
	assert.Equal(t, "idle", st.TranscodeStatus, "no active transcoder")
	assert.Equal(t, "", st.TranscodeResolution)
	assert.Equal(t, "silent", st.AudioCodec, "no codec info provider")
	assert.Equal(t, 0, st.TemperatureC)
	assert.Equal(t, 0, st.RestartCount)
	assert.Equal(t, 0.0, st.AVDriftMs)
}

func TestPushTarget_Status_AudioCodecFromProvider(t *testing.T) {
	target := NewPushTarget("test-cam", PushTargetConfig{ID: "t1"}, nil, func() ([]byte, []byte, bool) {
		return []byte{0x67}, []byte{0x68}, true
	})

	// Wire a codecInfoProvider returning AAC.
	target.codecInfoProvider = func() model.CodecInfo {
		return model.CodecInfo{AudioCodec: "aac"}
	}
	st := target.Status()
	assert.Equal(t, "aac", st.AudioCodec)

	// G.711 mu-law
	target.codecInfoProvider = func() model.CodecInfo {
		return model.CodecInfo{AudioCodec: "g711", AudioConfig: []byte{1, 0, 0, 0x1F, 0x40}}
	}
	st = target.Status()
	assert.Equal(t, "g711_mu", st.AudioCodec)

	// G.711 a-law
	target.codecInfoProvider = func() model.CodecInfo {
		return model.CodecInfo{AudioCodec: "g711", AudioConfig: []byte{0, 0, 0, 0x1F, 0x40}}
	}
	st = target.Status()
	assert.Equal(t, "g711_a", st.AudioCodec)

	// No audio (empty codec)
	target.codecInfoProvider = func() model.CodecInfo {
		return model.CodecInfo{AudioCodec: ""}
	}
	st = target.Status()
	assert.Equal(t, "silent", st.AudioCodec)
}

func TestPushTarget_Status_TranscodeRuntime(t *testing.T) {
	target := NewPushTarget("test-cam", PushTargetConfig{ID: "t1"}, nil, func() ([]byte, []byte, bool) {
		return []byte{0x67}, []byte{0x68}, true
	})

	// Without active transcoder, transcode_status is "idle".
	st := target.Status()
	assert.Equal(t, "idle", st.TranscodeStatus)
}

// JPEG sources (MJPEG/JPEG recorders) can never feed the H.265 transcode
// path — connectRTMP/connectRTSP must fail fast with a clear per-target
// error instead of engaging a doomed transcoder (#423).
func TestConnectRTMP_JPEGSourceFailsFast(t *testing.T) {
	for _, codec := range []string{"mjpeg", "jpeg"} {
		target := NewPushTarget("test-cam", PushTargetConfig{
			ID: "t1", URL: "rtmp://invalid/does/not/matter",
			Protocol: "rtmp", TranscodePolicy: "auto",
		}, nil, func() ([]byte, []byte, bool) {
			return nil, nil, false // not H.264 — the JPEG trap path
		})
		target.SetSourceCodecProvider(func() string { return codec })

		err := target.connectRTMP(context.Background())
		require.ErrorIs(t, err, errPermanent)
		require.Contains(t, err.Error(), "source is "+codec)
		require.Equal(t, StatusError, target.status)
		require.Contains(t, target.errMsg, "source is "+codec)
		require.Contains(t, target.errMsg, "requires H.264/H.265")
	}
}

func TestConnectRTSP_JPEGSourceFailsFast(t *testing.T) {
	target := NewPushTarget("test-cam", PushTargetConfig{
		ID: "t1", URL: "rtsp://invalid/does/not/matter",
		Protocol: "rtsp", TranscodePolicy: "auto",
	}, nil, func() ([]byte, []byte, bool) {
		return nil, nil, false // not H.264 — the JPEG trap path
	})
	target.SetSourceCodecProvider(func() string { return "mjpeg" })

	err := target.connectRTSP(context.Background())
	require.ErrorIs(t, err, errPermanent)
	require.Contains(t, err.Error(), "source is mjpeg")
	require.Equal(t, StatusError, target.status)
	require.Contains(t, target.errMsg, "source is mjpeg")
}

// Without a source-codec provider the legacy behavior is preserved: a
// non-H.264 source with TranscodePolicy "off" gets the generic message.
func TestConnectRTMP_NoSourceCodecProviderKeepsLegacyBehavior(t *testing.T) {
	target := NewPushTarget("test-cam", PushTargetConfig{
		ID: "t1", URL: "rtmp://invalid/does/not/matter",
		Protocol: "rtmp", TranscodePolicy: "off",
	}, nil, func() ([]byte, []byte, bool) {
		return nil, nil, false
	})

	err := target.connectRTMP(context.Background())
	require.Equal(t, errPermanent, err)
	require.Equal(t, StatusError, target.status)
	require.Contains(t, target.errMsg, "source is not H.264")
}
