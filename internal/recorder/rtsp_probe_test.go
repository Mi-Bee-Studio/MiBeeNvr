package recorder

import (
	"fmt"
	"testing"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/stretchr/testify/require"
)

// startProbeServer stands up a minimal RTSP server serving the given session
// description — a DESCRIBE target for ProbeRTSPMedia. No media ever flows;
// the probe only reads the SDP.
func startProbeServer(t *testing.T, desc *description.Session) string {
	t.Helper()
	port := findPort(t)
	time.Sleep(5 * time.Millisecond)

	h := &probeServerHandler{desc: desc}
	s := &gortsplib.Server{
		Handler:     h,
		RTSPAddress: fmt.Sprintf("127.0.0.1:%d", port),
	}
	require.NoError(t, s.Start())
	h.stream = &gortsplib.ServerStream{Server: s, Desc: desc}
	require.NoError(t, h.stream.Initialize())
	t.Cleanup(func() {
		h.stream.Close()
		s.Close()
	})

	return fmt.Sprintf("rtsp://127.0.0.1:%d/test", port)
}

type probeServerHandler struct {
	desc   *description.Session
	stream *gortsplib.ServerStream
}

func (h *probeServerHandler) OnDescribe(_ *gortsplib.ServerHandlerOnDescribeCtx) (
	*base.Response, *gortsplib.ServerStream, error,
) {
	return &base.Response{StatusCode: base.StatusOK}, h.stream, nil
}

func TestProbeRTSPMedia_G711Source(t *testing.T) {
	desc := &description.Session{Medias: []*description.Media{{
		Type: description.MediaTypeAudio,
		Formats: []format.Format{&format.G711{
			PayloadTyp: 8,
			SampleRate: 8000,
		}},
	}}}
	url := startProbeServer(t, desc)

	info := ProbeRTSPMedia(url, "", "")
	require.True(t, info.DescribeOK)
	require.Empty(t, info.Video)
	require.Equal(t, "PCMA", info.AudioCodec)
	require.Equal(t, 8000, info.ClockRate)
	require.Equal(t, 1, info.Channels)
	require.True(t, info.RecordableAudio())
	require.Equal(t, "PCMA 8000Hz", info.AudioDescription())
}

func TestProbeRTSPMedia_G711MuLaw(t *testing.T) {
	desc := &description.Session{Medias: []*description.Media{{
		Type: description.MediaTypeAudio,
		Formats: []format.Format{&format.G711{
			PayloadTyp: 0,
			MULaw:      true,
			SampleRate: 8000,
		}},
	}}}
	url := startProbeServer(t, desc)

	info := ProbeRTSPMedia(url, "", "")
	require.True(t, info.DescribeOK)
	require.Equal(t, "PCMU", info.AudioCodec)
}

func TestProbeRTSPMedia_VideoOnly(t *testing.T) {
	srv := newTestRTSPServer(t)
	defer srv.close()

	info := ProbeRTSPMedia(srv.rtspURL, "", "")
	require.True(t, info.DescribeOK)
	require.Equal(t, "H264", info.Video)
	require.Empty(t, info.AudioCodec)
	require.False(t, info.RecordableAudio())
	require.Empty(t, info.AudioOther)
	require.Empty(t, info.AudioDescription())
}

func TestProbeRTSPMedia_UnsupportedAudioCodec(t *testing.T) {
	desc := &description.Session{Medias: []*description.Media{{
		Type: description.MediaTypeAudio,
		Formats: []format.Format{&format.G726{
			PayloadTyp: 97,
			BitRate:    32, // kbps — rtpmap codec is "G726-32"
		}},
	}}}
	url := startProbeServer(t, desc)

	info := ProbeRTSPMedia(url, "", "")
	require.True(t, info.DescribeOK)
	require.Empty(t, info.AudioCodec)
	require.False(t, info.RecordableAudio())
	require.Equal(t, "G726", info.AudioOther)
	require.Equal(t, "G726 (unsupported)", info.AudioDescription())
}

func TestProbeRTSPMedia_AACDescription(t *testing.T) {
	// Pure classification test — no server needed for the AAC branch.
	desc := &description.Session{Medias: []*description.Media{{
		Type: description.MediaTypeAudio,
		Formats: []format.Format{&format.MPEG4Audio{
			PayloadTyp: 96,
			Config: &mpeg4audio.Config{
				SampleRate:   44100,
				ChannelCount: 2,
			},
		}},
	}}}
	info := describeMediaInfo(desc)
	require.True(t, info.DescribeOK)
	require.Equal(t, "AAC", info.AudioCodec)
	require.Equal(t, 44100, info.ClockRate)
	require.Equal(t, 2, info.Channels)
}

func TestProbeRTSPMedia_Unreachable(t *testing.T) {
	info := ProbeRTSPMedia("rtsp://127.0.0.1:1/test", "", "")
	require.False(t, info.DescribeOK)
	require.NotEmpty(t, info.DescribeErr)
	require.False(t, info.RecordableAudio())
}

func TestProbeRTSPMedia_InvalidURL(t *testing.T) {
	info := ProbeRTSPMedia("://not-a-url", "", "")
	require.False(t, info.DescribeOK)
	require.Contains(t, info.DescribeErr, "invalid URL")
}
