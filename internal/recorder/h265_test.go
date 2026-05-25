package recorder

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph265"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtpmpeg4audio"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

var (
	// Valid HEVC NALUs from gortsplib/mediacommon test data.
	testVPS265 = []byte{0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x90, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x78, 0x99, 0x98, 0x09}
	testSPS265 = []byte{0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x90, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x78, 0xa0, 0x03, 0xc0, 0x80, 0x10, 0xe5, 0x96, 0x66, 0x69, 0x24, 0xca, 0xe0, 0x10, 0x00, 0x00, 0x03, 0x00, 0x10, 0x00, 0x00, 0x03, 0x01, 0xe0, 0x80}
	testPPS265 = []byte{0x44, 0x01, 0xc1, 0x72, 0xb4, 0x62, 0x40}
	testIDR265 = []byte{0x26, 0x01, 0xaf, 0x09, 0x40, 0xc0, 0x00, 0x10}
)

type testRTSPServerH265 struct {
	server  *gortsplib.Server
	stream  *gortsplib.ServerStream
	media   *description.Media
	rtpEnc  *rtph265.Encoder
	playCh  chan struct{}
	rtspURL string
}

func newTestRTSPServerH265(t *testing.T) *testRTSPServerH265 {
	t.Helper()
	port := findPort(t)
	time.Sleep(5 * time.Millisecond)

	forma := &format.H265{
		PayloadTyp: 96,
		VPS:        testVPS265,
		SPS:        testSPS265,
		PPS:        testPPS265,
	}
	desc := &description.Session{
		Medias: []*description.Media{{
			Type:    description.MediaTypeVideo,
			Formats: []format.Format{forma},
		}},
	}

	s := &testRTSPServerH265{playCh: make(chan struct{})}
	s.media = desc.Medias[0]

	enc, err := forma.CreateEncoder()
	require.NoError(t, err)
	s.rtpEnc = enc

	s.server = &gortsplib.Server{
		Handler:     s,
		RTSPAddress: fmt.Sprintf("127.0.0.1:%d", port),
	}
	require.NoError(t, s.server.Start())

	s.stream = &gortsplib.ServerStream{Server: s.server, Desc: desc}
	require.NoError(t, s.stream.Initialize())

	s.rtspURL = fmt.Sprintf("rtsp://127.0.0.1:%d/test", port)
	return s
}

func (s *testRTSPServerH265) close() {
	s.stream.Close()
	s.server.Close()
}

func (s *testRTSPServerH265) sendAU(au [][]byte) {
	pkts, err := s.rtpEnc.Encode(au)
	if err != nil {
		return
	}
	for _, pkt := range pkts {
		s.stream.WritePacketRTP(s.media, pkt)
	}
}

func (s *testRTSPServerH265) sendFrames(count int, interval time.Duration) {
	for i := 0; i < count; i++ {
		s.sendAU([][]byte{testVPS265, testSPS265, testPPS265, testIDR265})
		if interval > 0 {
			time.Sleep(interval)
		}
	}
}

func (s *testRTSPServerH265) waitPlay(t *testing.T, timeout time.Duration) {
	t.Helper()
	select {
	case <-s.playCh:
	case <-time.After(timeout):
		t.Fatal("timed out waiting for PLAY")
	}
}

func (s *testRTSPServerH265) OnDescribe(_ *gortsplib.ServerHandlerOnDescribeCtx) (
	*base.Response, *gortsplib.ServerStream, error,
) {
	return &base.Response{StatusCode: base.StatusOK}, s.stream, nil
}

func (s *testRTSPServerH265) OnSetup(_ *gortsplib.ServerHandlerOnSetupCtx) (
	*base.Response, *gortsplib.ServerStream, error,
) {
	return &base.Response{StatusCode: base.StatusOK}, s.stream, nil
}

func (s *testRTSPServerH265) OnPlay(_ *gortsplib.ServerHandlerOnPlayCtx) (
	*base.Response, error,
) {
	select {
	case <-s.playCh:
	default:
		close(s.playCh)
	}
	return &base.Response{StatusCode: base.StatusOK}, nil
}

// testRTSPServerH265WithAudio wraps testRTSPServerH265 with an additional AAC audio media.
type testRTSPServerH265WithAudio struct {
	*testRTSPServerH265
	audioMedia *description.Media
	audioForma *format.MPEG4Audio
	audioEnc   *rtpmpeg4audio.Encoder
}

func newTestRTSPServerH265WithAudio(t *testing.T) *testRTSPServerH265WithAudio {
	t.Helper()
	port := findPort(t)
	time.Sleep(5 * time.Millisecond)

	videoForma := &format.H265{
		PayloadTyp: 96,
		VPS:        testVPS265,
		SPS:        testSPS265,
		PPS:        testPPS265,
	}
	audioForma := &format.MPEG4Audio{
		PayloadTyp: 97,
		Config: &mpeg4audio.AudioSpecificConfig{
			Type:          mpeg4audio.ObjectTypeAACLC,
			SampleRate:    48000,
			ChannelConfig: 2,
			ChannelCount:  2,
		},
		SizeLength:       13,
		IndexLength:      3,
		IndexDeltaLength: 3,
	}
	desc := &description.Session{
		Medias: []*description.Media{
			{
				Type:    description.MediaTypeVideo,
				Formats: []format.Format{videoForma},
			},
			{
				Type:    description.MediaTypeAudio,
				Formats: []format.Format{audioForma},
			},
		},
	}

	s := &testRTSPServerH265{playCh: make(chan struct{})}
	s.media = desc.Medias[0]

	videoEnc, err := videoForma.CreateEncoder()
	require.NoError(t, err)
	s.rtpEnc = videoEnc

	s.server = &gortsplib.Server{
		Handler:     s,
		RTSPAddress: fmt.Sprintf("127.0.0.1:%d", port),
	}
	require.NoError(t, s.server.Start())

	s.stream = &gortsplib.ServerStream{Server: s.server, Desc: desc}
	require.NoError(t, s.stream.Initialize())

	s.rtspURL = fmt.Sprintf("rtsp://127.0.0.1:%d/test", port)

	audioEnc, err := audioForma.CreateEncoder()
	require.NoError(t, err)

	return &testRTSPServerH265WithAudio{
		testRTSPServerH265: s,
		audioMedia:         desc.Medias[1],
		audioForma:         audioForma,
		audioEnc:           audioEnc,
	}
}

func (s *testRTSPServerH265WithAudio) sendAudioFrame(data []byte) {
	pkts, err := s.audioEnc.Encode([][]byte{data})
	if err != nil {
		return
	}
	for _, pkt := range pkts {
		s.stream.WritePacketRTP(s.audioMedia, pkt)
	}
}

// --- Audio Tests ---

func TestH265Recorder_AudioBroadcast(t *testing.T) {
	srv := newTestRTSPServerH265WithAudio(t)
	defer srv.close()

	mgr := newTestManager(t)
	hub := model.NewStreamHub()

	rec := NewH265Recorder(H265Config{
		CameraID:     "cam-audio-h265",
		RTSPURL:      srv.rtspURL,
		SegmentDur:   5 * time.Minute,
		RingBufCap:   100,
		AudioEnabled: true,
	}, mgr)
	rec.Hub = hub

	collector := newAudioCollector(hub, "test-audio-h265")
	defer collector.close(hub, "test-audio-h265")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	srv.waitPlay(t, 5*time.Second)
	time.Sleep(100 * time.Millisecond)

	// Send video frames to start segment.
	srv.sendFrames(3, 20*time.Millisecond)
	time.Sleep(100 * time.Millisecond)

	// Send audio frames.
	for i := 0; i < 5; i++ {
		srv.sendAudioFrame([]byte{0x01, 0x02, 0x03, 0x04})
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)

	require.NoError(t, rec.Stop())

	collector.waitFrames(t, 1, 2*time.Second)
	frames := collector.count()
	require.GreaterOrEqual(t, frames, 1, "expected at least 1 audio frame via BroadcastAudio")

	// Verify all frames are AAC.
	collector.mu.Lock()
	defer collector.mu.Unlock()
	for _, f := range collector.frames {
		require.Equal(t, model.AudioAAC, f.Codec, "audio codec should be AAC")
	}
}

func TestH265Recorder_AudioDisabled_StillRecordsVideo(t *testing.T) {
	srv := newTestRTSPServerH265WithAudio(t)
	defer srv.close()

	mgr := newTestManager(t)
	hub := model.NewStreamHub()

	rec := NewH265Recorder(H265Config{
		CameraID:     "cam-noaudio-h265",
		RTSPURL:      srv.rtspURL,
		SegmentDur:   5 * time.Minute,
		RingBufCap:   100,
		AudioEnabled: false,
	}, mgr)
	rec.Hub = hub

	collector := newAudioCollector(hub, "test-noaudio-h265")
	defer collector.close(hub, "test-noaudio-h265")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	srv.waitPlay(t, 5*time.Second)
	time.Sleep(100 * time.Millisecond)

	srv.sendFrames(5, 30*time.Millisecond)
	time.Sleep(200 * time.Millisecond)

	// Send audio — should be ignored.
	for i := 0; i < 3; i++ {
		srv.sendAudioFrame([]byte{0xAA, 0xBB})
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)

	require.NoError(t, rec.Stop())
	require.Equal(t, model.StatusStopped, rec.Status())

	// Video should still be recorded.
	files, err := mgr.ListFiles("cam-noaudio-h265")
	require.NoError(t, err)
	require.NotEmpty(t, files, "expected video recording even with audio disabled")

	// No audio frames should have been broadcast.
	require.Equal(t, 0, collector.count(), "no audio frames should be broadcast when AudioEnabled=false")
}

func TestH265Recorder_AudioEnabled_NoAudioInStream(t *testing.T) {
	// Server provides video only (no AAC media).
	srv := newTestRTSPServerH265(t)
	defer srv.close()

	mgr := newTestManager(t)

	rec := NewH265Recorder(H265Config{
		CameraID:     "cam-videoonly-h265",
		RTSPURL:      srv.rtspURL,
		SegmentDur:   5 * time.Minute,
		RingBufCap:   100,
		AudioEnabled: true,
	}, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	srv.waitPlay(t, 5*time.Second)
	time.Sleep(100 * time.Millisecond)

	srv.sendFrames(5, 30*time.Millisecond)
	time.Sleep(300 * time.Millisecond)

	require.NoError(t, rec.Stop())
	require.Equal(t, model.StatusStopped, rec.Status())

	files, err := mgr.ListFiles("cam-videoonly-h265")
	require.NoError(t, err)
	require.NotEmpty(t, files, "expected video recording even when no audio in stream")
}
