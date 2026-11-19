package streamsapi

// rtspEndpointFor tests (#686: MJPEG servable + credentials in the pull URL)
// — moved with the protocols handler into the streaming domain.

import (
	"net/http"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// --- rtspEndpointFor (#686: MJPEG servable + credentials in URL) ---

func rtspTestConfig(enabled bool, user, pass string) *config.Config {
	e := enabled
	return &config.Config{Server: config.ServerConfig{RTSP: config.RTSPOutputConfig{
		Enabled: &e, Port: 8554, Username: user, Password: pass,
	}}}
}

func TestRtspEndpointFor_MJPEGServable(t *testing.T) {
	t.Parallel()
	s := &server{deps: Deps{Config: rtspTestConfig(true, "", "")}}

	// #658 made the RTSP output server serve MJPEG; the endpoint detail must
	// agree (previously reported "codec not servable").
	detail := s.rtspEndpointFor(newReqHost(t, "192.168.1.5:9090"), "cam-1", model.FormatMJPEG)
	require.NotNil(t, detail)
	require.True(t, detail.Available)
	require.Equal(t, "rtsp://192.168.1.5:8554/cam-1", detail.URL)
}

func TestRtspEndpointFor_CredentialsInURL(t *testing.T) {
	t.Parallel()
	s := &server{deps: Deps{Config: rtspTestConfig(true, "admin", "p@ss")}}

	detail := s.rtspEndpointFor(newReqHost(t, "192.168.1.5:9090"), "cam-1", model.FormatH264)
	require.NotNil(t, detail)
	require.True(t, detail.Available)
	require.Equal(t, "rtsp://admin:p%40ss@192.168.1.5:8554/cam-1", detail.URL)
}

func TestRtspEndpointFor_DisabledNil(t *testing.T) {
	t.Parallel()
	s := &server{deps: Deps{Config: rtspTestConfig(false, "", "")}}

	require.Nil(t, s.rtspEndpointFor(newReqHost(t, "192.168.1.5:9090"), "cam-1", model.FormatH264))
}

func TestRtspEndpointFor_UnservableCodec(t *testing.T) {
	t.Parallel()
	s := &server{deps: Deps{Config: rtspTestConfig(true, "", "")}}

	detail := s.rtspEndpointFor(newReqHost(t, "192.168.1.5:9090"), "cam-1", model.Format("mpeg4"))
	require.NotNil(t, detail)
	require.False(t, detail.Available)
	require.NotEmpty(t, detail.Reason)
}

// newReqHost builds a GET request whose Host matches a real browser request to
// hostport — rtspEndpointFor derives the pull URL's host from it.
func newReqHost(t *testing.T, hostport string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+hostport+"/", nil)
	require.NoError(t, err)
	return req
}
