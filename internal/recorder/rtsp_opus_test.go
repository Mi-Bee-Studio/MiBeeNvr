package recorder

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtpsimpleaudio"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

// testRTSPServerWithOpus wraps testRTSPServer with an Opus audio media
// (RFC 7587 — the desktop-screen source shape: H.264 video + Opus audio).
type testRTSPServerWithOpus struct {
	*testRTSPServer
	audioMedia *description.Media
	opusForma  *format.Opus
	opusEnc    *rtpsimpleaudio.Encoder
}

func newTestRTSPServerWithOpus(t *testing.T) *testRTSPServerWithOpus {
	t.Helper()
	port := findPort(t)
	time.Sleep(5 * time.Millisecond)

	videoForma := &format.H264{
		PayloadTyp:        96,
		PacketizationMode: 1,
		SPS:               testSPS,
		PPS:               testPPS,
	}
	opusForma := &format.Opus{PayloadTyp: 98, ChannelCount: 2}
	desc := &description.Session{
		Medias: []*description.Media{
			{
				Type:    description.MediaTypeVideo,
				Formats: []format.Format{videoForma},
			},
			{
				Type:    description.MediaTypeAudio,
				Formats: []format.Format{opusForma},
			},
		},
	}

	s := &testRTSPServer{playCh: make(chan struct{})}
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

	opusEnc, err := opusForma.CreateEncoder()
	require.NoError(t, err)

	return &testRTSPServerWithOpus{
		testRTSPServer: s,
		audioMedia:     desc.Medias[1],
		opusForma:      opusForma,
		opusEnc:        opusEnc,
	}
}

func (s *testRTSPServerWithOpus) sendOpusFrame(data []byte) {
	pkt, err := s.opusEnc.Encode(data)
	if err != nil {
		return
	}
	s.stream.WritePacketRTP(s.audioMedia, pkt)
}

// TestH264Recorder_OpusAudio verifies the RTSP audio negotiation picks up an
// Opus track (AAC/G.711 absent): frames reach the hub tagged Opus and the
// finalized MP4 carries a dOps audio sample entry.
func TestH264Recorder_OpusAudio(t *testing.T) {
	srv := newTestRTSPServerWithOpus(t)
	defer srv.close()

	mgr := newTestManager(t)
	hub := streamhub.New()

	rec := NewH264Recorder(H264Config{
		CameraID:          "cam-opus",
		RTSPURL:           srv.rtspURL,
		SegmentDur:        5 * time.Minute,
		RingBufCap:        100,
		AudioEnabled:      true,
		AudioInRecordings: true,
	}, mgr)
	rec.Hub = hub

	collector := newAudioCollector(hub, "test-opus")
	defer collector.close(hub, "test-opus")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	srv.waitPlay(t, 5*time.Second)
	time.Sleep(100 * time.Millisecond)

	// Video opens the segment (IDR-gated).
	srv.sendFrames(3, 20*time.Millisecond)
	time.Sleep(100 * time.Millisecond)

	// Opus audio frames.
	for range 5 {
		srv.sendOpusFrame([]byte{0x01, 0x02, 0x03, 0x04})
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)

	require.NoError(t, rec.Stop())

	// Live fan-out: frames arrive tagged Opus.
	collector.waitFrames(t, 1, 2*time.Second)
	collector.mu.Lock()
	for _, f := range collector.frames {
		require.Equal(t, model.AudioOpus, f.Codec, "audio codec should be Opus")
	}
	collector.mu.Unlock()

	// Disk: the finalized MP4 must carry an Opus track (dOps sample entry) —
	// raw-bytes probe, same as the ingest/WHIP Opus test.
	files, err := mgr.ListFiles("cam-opus")
	require.NoError(t, err)
	require.NotEmpty(t, files, "expected a finalized segment")
	found := false
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if strings.Contains(string(data), "dOps") {
			found = true
			break
		}
	}
	require.True(t, found, "MP4 must carry a dOps sample entry (Opus audio track)")
}
