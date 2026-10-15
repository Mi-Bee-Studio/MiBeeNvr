package api

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/stretchr/testify/require"
)

// rtspProbeServer is a minimal DESCRIBE-only RTSP server for the
// test-connection probe (#964). No media flows; the probe reads the SDP.
type rtspProbeServer struct {
	URL    string
	stream *gortsplib.ServerStream
	server *gortsplib.Server
}

func startRTSPProbeServer(t *testing.T, medias ...*description.Media) *rtspProbeServer {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	s := &rtspProbeServer{}
	desc := &description.Session{Medias: medias}
	s.server = &gortsplib.Server{
		Handler:     s,
		RTSPAddress: fmt.Sprintf("127.0.0.1:%d", port),
	}
	require.NoError(t, s.server.Start())
	s.stream = &gortsplib.ServerStream{Server: s.server, Desc: desc}
	require.NoError(t, s.stream.Initialize())
	t.Cleanup(func() {
		s.stream.Close()
		s.server.Close()
	})
	s.URL = fmt.Sprintf("rtsp://127.0.0.1:%d/test", port)
	return s
}

func (s *rtspProbeServer) OnDescribe(_ *gortsplib.ServerHandlerOnDescribeCtx) (
	*base.Response, *gortsplib.ServerStream, error,
) {
	return &base.Response{StatusCode: base.StatusOK}, s.stream, nil
}

func g711Media() *description.Media {
	return &description.Media{
		Type: description.MediaTypeAudio,
		Formats: []format.Format{&format.G711{
			PayloadTyp: 8,
			SampleRate: 8000,
		}},
	}
}

func h264Media() *description.Media {
	return &description.Media{
		Type: description.MediaTypeVideo,
		Formats: []format.Format{&format.H264{
			PayloadTyp:        96,
			PacketizationMode: 1,
			SPS:               []byte{0x67, 0x42, 0x00, 0x0a, 0xf8, 0x41, 0xa2},
			PPS:               []byte{0x68, 0xce, 0x38, 0x80},
		}},
	}
}

// TestTestConnection_RTSPAudioProbe_G711: an audio-source form (encoding
// "audio") probing a G.711 URL must succeed and surface the audio codec —
// the probe helper acceptance for #964.
func TestTestConnection_RTSPAudioProbe_G711(t *testing.T) {
	t.Helper()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	srv := startRTSPProbeServer(t, g711Media())

	body := fmt.Sprintf(`{"protocol":"rtsp","url":%q,"encoding":"audio"}`, srv.URL)
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/test-connection", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]any
	parseJSON(t, rr, &resp)
	require.Equal(t, true, resp["success"])
	require.Equal(t, true, resp["stream_ok"])
	require.Equal(t, true, resp["has_audio"])
	require.Equal(t, true, resp["audio_supported"])
	require.Equal(t, "PCMA 8000Hz", resp["audio"])
}

// TestTestConnection_RTSPAudioProbe_NoAudioTrack: an audio-source form
// probing a video-only stream fails with an explicit "no audio track"
// message instead of the old dial-only success.
func TestTestConnection_RTSPAudioProbe_NoAudioTrack(t *testing.T) {
	t.Helper()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	srv := startRTSPProbeServer(t, h264Media())

	body := fmt.Sprintf(`{"protocol":"rtsp","url":%q,"encoding":"audio"}`, srv.URL)
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/test-connection", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]any
	parseJSON(t, rr, &resp)
	require.Equal(t, false, resp["success"])
	require.Equal(t, true, resp["reachable"])
	require.Equal(t, false, resp["has_audio"])
	require.Contains(t, resp["message"], "no audio track")
}

// TestTestConnection_RTSPVideoProbe reports the stream's real encoding and
// audio layout for video-camera forms.
func TestTestConnection_RTSPVideoProbe(t *testing.T) {
	t.Helper()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	srv := startRTSPProbeServer(t, h264Media(), g711Media())

	body := fmt.Sprintf(`{"protocol":"rtsp","url":%q}`, srv.URL)
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/test-connection", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]any
	parseJSON(t, rr, &resp)
	require.Equal(t, true, resp["success"])
	require.Equal(t, true, resp["stream_ok"])
	require.Equal(t, "H264", resp["encoding"])
	require.Equal(t, true, resp["audio_supported"])
	require.Equal(t, "PCMA 8000Hz", resp["audio"])
}

// TestTestConnection_RTSPVideoProbe_DescribeFailure: a reachable port that
// fails DESCRIBE (wrong path/credentials) is reachable=true but
// stream_ok=false — the same honest split the ONVIF probe uses.
func TestTestConnection_RTSPVideoProbe_DescribeFailure(t *testing.T) {
	t.Helper()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	// A plain TCP listener that speaks no RTSP: dial succeeds, DESCRIBE fails.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			// Hold the connection open without answering until read timeout.
			time.Sleep(15 * time.Second)
			c.Close()
		}
	}()

	body := fmt.Sprintf(`{"protocol":"rtsp","url":"rtsp://%s/nope"}`, l.Addr().String())
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/test-connection", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]any
	parseJSON(t, rr, &resp)
	require.Equal(t, false, resp["success"])
	require.Equal(t, true, resp["reachable"])
	require.Equal(t, false, resp["stream_ok"])
}
