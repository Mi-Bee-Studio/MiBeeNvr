package recorder

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
)

// mockRecorder is a minimal model.Recorder for testing ONVIFRecorder delegation.
type mockRecorder struct {
	mu       sync.Mutex
	status   model.RecorderStatus
	startErr error
	started  bool
	stopped  bool
}

func (m *mockRecorder) Start(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.startErr != nil {
		return m.startErr
	}
	m.started = true
	m.status = model.StatusRecording
	return nil
}

func (m *mockRecorder) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopped = true
	m.status = model.StatusStopped
	return nil
}

func (m *mockRecorder) Status() model.RecorderStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

// mockSegmentStore implements SegmentStore for testing.
type mockSegmentStore struct{}

func (m *mockSegmentStore) CreateSegment(_ string, _ string) (string, string, error) {
	return "/tmp/test-segment-tmp.mp4", "/tmp/test-segment-final.mp4", nil
}

func (m *mockSegmentStore) WriteFrame(_ string, _ []byte) (int, error) {
	return 0, nil
}

func (m *mockSegmentStore) CloseSegment(_, _ string) error {
	return nil
}

// newTestONVIFRecorder creates an ONVIFRecorder with a mock client and mock store.
// The newRecorder factory is overridden to use a mockRecorder so tests don't
// need a real RTSP server.
func newTestONVIFRecorder(t *testing.T, client onvif.DeviceClient, opts ...func(*ONVIFRecorder)) *ONVIFRecorder {
	t.Helper()
	cfg := ONVIFConfig{
		CameraID:     "test-cam-1",
		ProfileToken: "profile_1",
		Username:     "admin",
		Password:     "pass",
	}
	r := NewONVIFRecorder(cfg, client, &mockSegmentStore{})
	for _, opt := range opts {
		opt(r)
	}
	return r
}

func TestONVIFRecorder_ImplementsRecorder(t *testing.T) {
	// Compile-time interface check
	var _ model.Recorder = (*ONVIFRecorder)(nil)
}

func TestONVIFRecorder_Start_Success(t *testing.T) {
	client := &onvif.MockDeviceClient{
		DeviceInfo: &onvif.DeviceInfo{Manufacturer: "Test", Model: "Cam", Firmware: "1.0"},
		Profiles: []onvif.DeviceProfile{
			{Token: "profile_1", Name: "HD", Encoding: "H264", Width: 1920, Height: 1080},
		},
		StreamURI: &onvif.StreamInfo{
			URI:          "rtsp://192.168.1.100/stream",
			Protocol:     "RTSP",
			Encoding:     "H264",
			ProfileToken: "profile_1",
		},
	}

	mr := &mockRecorder{}
	r := newTestONVIFRecorder(t, client, func(or *ONVIFRecorder) {
		or.newRecorder = func(rtspURL string) model.Recorder {
			require.Equal(t, "rtsp://192.168.1.100/stream", rtspURL)
			return mr
		}
	})

	err := r.Start(context.Background())
	require.NoError(t, err)
	require.Equal(t, "rtsp://192.168.1.100/stream", r.RTSPURL())
	require.Equal(t, 1, client.ConnectCalls)
	require.Equal(t, 1, client.GetStreamURICalls)
	require.True(t, mr.started)
	require.Equal(t, model.StatusRecording, r.Status())

	// Cleanup
	err = r.Stop()
	require.NoError(t, err)
}

func TestONVIFRecorder_Start_ConnectFails(t *testing.T) {
	client := &onvif.MockDeviceClient{
		ConnectError: errors.New("connection refused"),
	}

	r := newTestONVIFRecorder(t, client)

	err := r.Start(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "onvif connect")
	require.Equal(t, 1, client.ConnectCalls)
	// GetStreamURI should not be called if Connect fails
	require.Equal(t, 0, client.GetStreamURICalls)
	require.Equal(t, model.StatusStopped, r.Status())
}

func TestONVIFRecorder_Start_GetStreamURIFails(t *testing.T) {
	client := &onvif.MockDeviceClient{
		DeviceInfo: &onvif.DeviceInfo{Manufacturer: "Test", Model: "Cam"},
		StreamURI:  nil, // GetStreamURI returns nil -> will panic, use a different approach
	}
	// Override GetStreamURI to return an error by wrapping
	wrappedClient := &errorStreamURIClient{MockDeviceClient: client}

	r := newTestONVIFRecorder(t, wrappedClient)

	err := r.Start(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "onvif get stream URI")
	require.Equal(t, 1, wrappedClient.ConnectCalls)
	require.Equal(t, 1, wrappedClient.StreamURICallCount)
}

// errorStreamURIClient wraps MockDeviceClient to make GetStreamURI return an error.
type errorStreamURIClient struct {
	*onvif.MockDeviceClient
	StreamURICallCount int
}

func (e *errorStreamURIClient) GetStreamURI(ctx context.Context, profileToken string) (*onvif.StreamInfo, error) {
	e.StreamURICallCount++
	return nil, errors.New("stream URI not found")
}

func TestONVIFRecorder_Stop(t *testing.T) {
	client := &onvif.MockDeviceClient{
		DeviceInfo: &onvif.DeviceInfo{Manufacturer: "Test", Model: "Cam"},
		StreamURI: &onvif.StreamInfo{
			URI:          "rtsp://192.168.1.100/stream",
			Protocol:     "RTSP",
			ProfileToken: "profile_1",
		},
	}

	mr := &mockRecorder{}
	r := newTestONVIFRecorder(t, client, func(or *ONVIFRecorder) {
		or.newRecorder = func(_ string) model.Recorder { return mr }
	})

	err := r.Start(context.Background())
	require.NoError(t, err)
	require.Equal(t, model.StatusRecording, r.Status())

	err = r.Stop()
	require.NoError(t, err)
	require.True(t, mr.stopped)
	// Status should be stopped since mock sets it
	require.Equal(t, model.StatusStopped, r.Status())
}

func TestONVIFRecorder_Stop_WithoutStart(t *testing.T) {
	client := &onvif.MockDeviceClient{}
	r := newTestONVIFRecorder(t, client)

	err := r.Stop()
	require.NoError(t, err)
	require.Equal(t, model.StatusStopped, r.Status())
}

func TestONVIFRecorder_Status(t *testing.T) {
	client := &onvif.MockDeviceClient{
		DeviceInfo: &onvif.DeviceInfo{Manufacturer: "Test", Model: "Cam"},
		StreamURI: &onvif.StreamInfo{
			URI:          "rtsp://192.168.1.100/stream",
			Protocol:     "RTSP",
			ProfileToken: "profile_1",
		},
	}

	mr := &mockRecorder{}
	r := newTestONVIFRecorder(t, client, func(or *ONVIFRecorder) {
		or.newRecorder = func(_ string) model.Recorder { return mr }
	})

	// Before start
	require.Equal(t, model.StatusStopped, r.Status())

	// After start
	err := r.Start(context.Background())
	require.NoError(t, err)
	require.Equal(t, model.StatusRecording, r.Status())

	// After stop
	err = r.Stop()
	require.NoError(t, err)
	require.Equal(t, model.StatusStopped, r.Status())
}

func TestONVIFRecorder_Start_AlreadyRunning(t *testing.T) {
	client := &onvif.MockDeviceClient{
		DeviceInfo: &onvif.DeviceInfo{Manufacturer: "Test", Model: "Cam"},
		StreamURI: &onvif.StreamInfo{
			URI:          "rtsp://192.168.1.100/stream",
			Protocol:     "RTSP",
			ProfileToken: "profile_1",
		},
	}

	mr := &mockRecorder{}
	r := newTestONVIFRecorder(t, client, func(or *ONVIFRecorder) {
		or.newRecorder = func(_ string) model.Recorder { return mr }
	})

	err := r.Start(context.Background())
	require.NoError(t, err)

	// Second start should fail
	err = r.Start(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "already running")

	r.Stop()
}

func TestONVIFRecorder_DetectEncoding_H264(t *testing.T) {
	client := &onvif.MockDeviceClient{
		Profiles: []onvif.DeviceProfile{
			{Token: "p1", Encoding: "H265", Width: 1920, Height: 1080},
			{Token: "p2", Encoding: "H264", Width: 1280, Height: 720},
		},
	}
	r := newTestONVIFRecorder(t, client)
	encoding := r.detectEncoding(context.Background())
	require.Equal(t, "H264", encoding)
}

func TestONVIFRecorder_DetectEncoding_H265(t *testing.T) {
	client := &onvif.MockDeviceClient{
		Profiles: []onvif.DeviceProfile{
			{Token: "p1", Encoding: "H265", Width: 1920, Height: 1080},
		},
	}
	r := newTestONVIFRecorder(t, client)
	encoding := r.detectEncoding(context.Background())
	require.Equal(t, "H265", encoding)
}

func TestONVIFRecorder_DetectEncoding_Default(t *testing.T) {
	t.Run("empty profiles", func(t *testing.T) {
		client := &onvif.MockDeviceClient{
			Profiles: []onvif.DeviceProfile{},
		}
		r := newTestONVIFRecorder(t, client)
		encoding := r.detectEncoding(context.Background())
		require.Equal(t, "H264", encoding)
	})

	t.Run("nil profiles", func(t *testing.T) {
		client := &onvif.MockDeviceClient{}
		r := newTestONVIFRecorder(t, client)
		encoding := r.detectEncoding(context.Background())
		require.Equal(t, "H264", encoding)
	})

	t.Run("JPEG encoding", func(t *testing.T) {
		client := &onvif.MockDeviceClient{
			Profiles: []onvif.DeviceProfile{
				{Token: "p1", Encoding: "JPEG", Width: 640, Height: 480},
			},
		}
		r := newTestONVIFRecorder(t, client)
		encoding := r.detectEncoding(context.Background())
		// JPEG encoding detected from ONVIF profile metadata
		require.Equal(t, "JPEG", encoding)
	})
	t.Run("unknown encoding", func(t *testing.T) {
		client := &onvif.MockDeviceClient{
			Profiles: []onvif.DeviceProfile{
				{Token: "p1", Encoding: "MPEG4", Width: 640, Height: 480},
			},
		}
		r := newTestONVIFRecorder(t, client)
		encoding := r.detectEncoding(context.Background())
		// Unknown encoding not in {H264, H265, JPEG} -> falls back to H264
		require.Equal(t, "H264", encoding)
	})
}

func TestONVIFRecorder_RTSPURL_BeforeStart(t *testing.T) {
	client := &onvif.MockDeviceClient{}
	r := newTestONVIFRecorder(t, client)
	require.Empty(t, r.RTSPURL())
}

func TestONVIFRecorder_CreateDelegate_H264(t *testing.T) {
	client := &onvif.MockDeviceClient{
		Profiles: []onvif.DeviceProfile{
			{Token: "p1", Encoding: "H264", Width: 1920, Height: 1080},
		},
	}

	var createdType string
	r := newTestONVIFRecorder(t, client, func(or *ONVIFRecorder) {
		or.newRecorder = func(rtspURL string) model.Recorder {
			require.Equal(t, "rtsp://192.168.1.100/stream", rtspURL)
			createdType = "mock"
			return &mockRecorder{}
		}
	})

	// Trigger createDelegate via Start
	client.StreamURI = &onvif.StreamInfo{
		URI:          "rtsp://192.168.1.100/stream",
		Protocol:     "RTSP",
		Encoding:     "H264",
		ProfileToken: "profile_1",
	}
	err := r.Start(context.Background())
	require.NoError(t, err)
	require.Equal(t, "mock", createdType)
	r.Stop()
}

func TestONVIFRecorder_Start_DelegateStartFails(t *testing.T) {
	client := &onvif.MockDeviceClient{
		DeviceInfo: &onvif.DeviceInfo{Manufacturer: "Test", Model: "Cam"},
		StreamURI: &onvif.StreamInfo{
			URI:          "rtsp://192.168.1.100/stream",
			Protocol:     "RTSP",
			ProfileToken: "profile_1",
		},
	}

	mr := &mockRecorder{startErr: errors.New("RTSP connection failed")}
	r := newTestONVIFRecorder(t, client, func(or *ONVIFRecorder) {
		or.newRecorder = func(_ string) model.Recorder { return mr }
	})

	err := r.Start(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "RTSP connection failed")
}

func TestONVIFRecorder_DetectEncoding_JPEG(t *testing.T) {
	client := &onvif.MockDeviceClient{
		Profiles: []onvif.DeviceProfile{
			{Token: "p1", Encoding: "JPEG", Width: 640, Height: 480},
		},
	}
	r := newTestONVIFRecorder(t, client)
	encoding := r.detectEncoding(context.Background())
	require.Equal(t, "JPEG", encoding)
}

func TestONVIFRecorder_ProbeHTTPMJPEG_Success(t *testing.T) {
	// Start an HTTP server that returns multipart/x-mixed-replace
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &onvif.MockDeviceClient{}
	r := newTestONVIFRecorder(t, client)
	r.cfg.ONVIFEndpoint = server.URL + "/onvif/device_service"
	r.rtspURL = "rtsp://192.168.1.100/stream"

	url, err := r.probeHTTPMJPEG(context.Background())
	require.NoError(t, err)
	require.Equal(t, server.URL+"/stream", url)
}

func TestONVIFRecorder_ProbeHTTPMJPEG_FallbackPaths(t *testing.T) {
	// Server only returns multipart at /mjpeg
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mjpeg" {
			w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := &onvif.MockDeviceClient{}
	r := newTestONVIFRecorder(t, client)
	r.cfg.ONVIFEndpoint = server.URL + "/onvif/device_service"
	r.rtspURL = "rtsp://192.168.1.100/stream"

	url, err := r.probeHTTPMJPEG(context.Background())
	require.NoError(t, err)
	require.Equal(t, server.URL+"/mjpeg", url)
}

func TestONVIFRecorder_ProbeHTTPMJPEG_NoStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &onvif.MockDeviceClient{}
	r := newTestONVIFRecorder(t, client)
	r.cfg.ONVIFEndpoint = server.URL + "/onvif/device_service"
	r.rtspURL = "rtsp://192.168.1.100/stream"

	_, err := r.probeHTTPMJPEG(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "no MJPEG stream found")
}

func TestONVIFRecorder_CreateDelegate_JPEG_HTTPProbeSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &onvif.MockDeviceClient{
		Profiles: []onvif.DeviceProfile{
			{Token: "p1", Encoding: "JPEG", Width: 640, Height: 480},
		},
	}
	r := newTestONVIFRecorder(t, client)
	r.cfg.ONVIFEndpoint = server.URL + "/onvif/device_service"
	r.rtspURL = "rtsp://192.168.1.100/stream"

	// Restore the default createDelegate (newRecorder was set by newTestONVIFRecorder)
	r.newRecorder = r.createDelegate
	rec := r.createDelegate(r.rtspURL)
	require.NotNil(t, rec)
	// Should create an HTTPJPEGRecorder
	_, ok := rec.(*HTTPJPEGRecorder)
	require.True(t, ok, "expected HTTPJPEGRecorder for JPEG with successful HTTP probe")

	// Verify cached URL
	r.mu.Lock()
	cached := r.httpJPEGURL
	r.mu.Unlock()
	require.Equal(t, server.URL+"/stream", cached)
}

func TestONVIFRecorder_CreateDelegate_JPEG_HTTPProbeFallback(t *testing.T) {
	// No server -> probe will fail, but we still use HTTPJPEGRecorder with guessed URL
	client := &onvif.MockDeviceClient{
		Profiles: []onvif.DeviceProfile{
			{Token: "p1", Encoding: "JPEG", Width: 640, Height: 480},
		},
	}
	r := newTestONVIFRecorder(t, client)
	r.cfg.ONVIFEndpoint = "http://192.168.1.999:80/onvif/device_service"
	r.rtspURL = "rtsp://192.168.1.100/stream"

	// Restore the default createDelegate
	r.newRecorder = r.createDelegate
	rec := r.createDelegate(r.rtspURL)
	require.NotNil(t, rec)
	// Should use HTTPJPEGRecorder with guessed URL when probe fails
	_, ok := rec.(*HTTPJPEGRecorder)
	require.True(t, ok, "expected HTTPJPEGRecorder with guessed URL for JPEG when HTTP probe fails")
	// guessMJPEGURL falls back to :81 (ESP32 MiBeeCam serves MJPEG on a separate
	// port from the ONVIF endpoint) — see fix(recorder): restore :81 MJPEG fallback port.
	require.Equal(t, "http://192.168.1.999:81/stream", r.httpJPEGURL, "guessed URL should be cached")
}

func TestONVIFRecorder_CreateDelegate_JPEG_CachedURL(t *testing.T) {
	client := &onvif.MockDeviceClient{
		Profiles: []onvif.DeviceProfile{
			{Token: "p1", Encoding: "JPEG", Width: 640, Height: 480},
		},
	}
	r := newTestONVIFRecorder(t, client)
	r.httpJPEGURL = "http://192.168.1.100:80/stream"
	r.rtspURL = "rtsp://192.168.1.100/stream"

	r.newRecorder = r.createDelegate
	rec := r.createDelegate(r.rtspURL)
	require.NotNil(t, rec)
	// Should use cached URL and create HTTPJPEGRecorder without probing
	_, ok := rec.(*HTTPJPEGRecorder)
	require.True(t, ok, "expected HTTPJPEGRecorder when cached URL is set")
}

// TestProbeRTSPEncodingFor_DetectsMJPEG verifies the RTSP DESCRIBE probe now
// recognizes an MJPEG-only stream (previously it only detected H264/H265, so
// ESP32 MiBeeCam RTSP-AVI firmware was misrouted).
func TestProbeRTSPEncodingFor_DetectsMJPEG(t *testing.T) {
	srv := newMjpegTestServer(t)
	defer srv.close()

	enc := probeRTSPEncodingFor(srv.rtspURL, "", "")
	require.Equal(t, "MJPEG", enc, "MJPEG-only RTSP stream must be detected as MJPEG")
}

// TestProbeRTSPEncodingFor_InvalidURL returns "" for an unparseable URL.
func TestProbeRTSPEncodingFor_InvalidURL(t *testing.T) {
	require.Equal(t, "", probeRTSPEncodingFor("://not-a-url", "", ""))
	require.Equal(t, "", probeRTSPEncodingFor("", "", ""))
}

// TestONVIFRecorder_DetectEncoding_JPEG_RTSPMJPEG verifies that a JPEG ONVIF
// profile whose device advertises an rtsp:// MJPEG stream is resolved to
// "MJPEG" (RTSP recorder path → AVI+audio), and that r.rtspURL is overwritten
// with the rtsp:// URL even if Start()'s GetStreamURI returned an http:// URL.
func TestONVIFRecorder_DetectEncoding_JPEG_RTSPMJPEG(t *testing.T) {
	srv := newMjpegTestServer(t)
	defer srv.close()

	client := &onvif.MockDeviceClient{
		Profiles: []onvif.DeviceProfile{
			{Token: "p1", Encoding: "JPEG", Width: 640, Height: 480},
		},
	}
	r := newTestONVIFRecorder(t, client)
	// Start() resolved an rtsp:// URI directly (some firmware returns rtsp://
	// from the default GetStreamURI).
	r.rtspURL = srv.rtspURL

	enc := r.detectEncoding(context.Background())
	require.Equal(t, "MJPEG", enc)
	require.Equal(t, srv.rtspURL, r.rtspURL)
}

// TestONVIFRecorder_DetectEncoding_JPEG_NoRTSPFallback confirms a JPEG device
// whose derived rtsp:// URL is unreachable still resolves to "JPEG" (HTTP MJPEG),
// preserving the legacy video-only behavior. Uses localhost:81 so the derived
// rtsp://127.0.0.1:554/stream fails fast (connection refused) instead of hanging.
func TestONVIFRecorder_DetectEncoding_JPEG_NoRTSPFallback(t *testing.T) {
	client := &onvif.MockDeviceClient{
		Profiles: []onvif.DeviceProfile{
			{Token: "p1", Encoding: "JPEG", Width: 640, Height: 480},
		},
	}
	r := newTestONVIFRecorder(t, client)
	r.rtspURL = "http://127.0.0.1:81/stream"

	require.Equal(t, "JPEG", r.detectEncoding(context.Background()))
}

// TestONVIFRecorder_CreateDelegate_JPEG_RTSPMJPEG verifies the full delegate
// path: a JPEG ONVIF device with an rtsp:// MJPEG stream yields an
// *MJPEGRecorder wired with the RTSP URL and AudioEnabled flag (so G.711 audio
// is captured into AVI segments).
func TestONVIFRecorder_CreateDelegate_JPEG_RTSPMJPEG(t *testing.T) {
	srv := newMjpegTestServer(t)
	defer srv.close()

	client := &onvif.MockDeviceClient{
		Profiles: []onvif.DeviceProfile{
			{Token: "p1", Encoding: "JPEG", Width: 640, Height: 480},
		},
	}
	r := newTestONVIFRecorder(t, client, func(or *ONVIFRecorder) {
		or.cfg.AudioEnabled = true
	})
	r.rtspURL = srv.rtspURL

	rec := r.createDelegate(r.rtspURL)
	require.NotNil(t, rec)
	mjpegRec, ok := rec.(*MJPEGRecorder)
	require.True(t, ok, "expected *MJPEGRecorder for JPEG device with RTSP MJPEG stream")
	// Credentials are injected into the RTSP URL (ESP32 RTSP-AVI firmware requires
	// auth; ONVIF GetStreamURI returns a credential-less URL).
	require.Equal(t, injectRTSPCredentials(srv.rtspURL, "admin", "pass"), mjpegRec.cfg.RTSPURL, "recorder must use the rtsp:// URL with credentials embedded")
	require.True(t, mjpegRec.cfg.AudioEnabled, "AudioEnabled must propagate for AVI+audio recording")
}

func TestInjectRTSPCredentials(t *testing.T) {
	tests := []struct {
		name, in, user, pass, want string
	}{
		{"embeds creds", "rtsp://1.2.3.4:554/stream", "admin", "admin", "rtsp://admin:admin@1.2.3.4:554/stream"},
		{"keeps existing userinfo", "rtsp://admin:admin@1.2.3.4:554/stream", "other", "x", "rtsp://admin:admin@1.2.3.4:554/stream"},
		{"no username = unchanged", "rtsp://1.2.3.4:554/stream", "", "", "rtsp://1.2.3.4:554/stream"},
		{"non-rtsp = unchanged", "http://1.2.3.4:81/stream", "admin", "admin", "http://1.2.3.4:81/stream"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, injectRTSPCredentials(tc.in, tc.user, tc.pass))
		})
	}
}

// TestResolveJPEGEncoding_ProbesCachedRTSPURL verifies that when Start() already
// resolved an rtsp:// URL (the common case), resolveJPEGEncoding probes it
// directly WITHOUT making extra GetStreamURIWithProtocol calls — critical for
// the ESP32, whose tiny HTTP pool is exhausted by redundant ONVIF calls.
func TestResolveJPEGEncoding_ProbesCachedRTSPURL(t *testing.T) {
	srv := newMjpegTestServer(t)
	defer srv.close()

	client := &onvif.MockDeviceClient{
		Profiles: []onvif.DeviceProfile{
			{Token: "p1", Encoding: "JPEG", Width: 640, Height: 480},
		},
		// Deliberately do NOT set StreamURIWithProtocol — the direct path must not need it.
	}
	r := newTestONVIFRecorder(t, client)
	r.rtspURL = srv.rtspURL // Start() would have set this to rtsp://...

	require.Equal(t, "MJPEG", r.detectEncoding(context.Background()))
	require.Equal(t, 0, client.GetStreamURIWithProtocolCalls, "must probe cached rtsp:// URL directly, no extra ONVIF call")
}

func TestDeriveRTSPURL(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"http with port", "http://192.168.63.224:81/stream", "rtsp://192.168.63.224:554/stream"},
		{"http default port", "http://192.168.63.224/stream", "rtsp://192.168.63.224:554/stream"},
		{"already rtsp", "rtsp://1.2.3.4:554/stream", "rtsp://1.2.3.4:554/stream"},
		{"strips userinfo", "http://admin:admin@1.2.3.4:81/stream", "rtsp://1.2.3.4:554/stream"},
		{"garbage → empty", "://not-a-url", ""},
		{"no host → empty", "http:///path", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, deriveRTSPURL(tc.in))
		})
	}
}

func TestRewriteStaleStreamHost(t *testing.T) {
	tests := []struct {
		name, rtspURL, onvifEndpoint, want string
	}{
		{
			name:          "stale host rewritten (DHCP reassignment)",
			rtspURL:       "rtsp://192.168.63.200:554/11",
			onvifEndpoint: "http://192.168.63.199:8080/onvif/device_service",
			want:          "rtsp://192.168.63.199:554/11",
		},
		{
			name:          "hosts agree → unchanged",
			rtspURL:       "rtsp://192.168.1.10:554/stream",
			onvifEndpoint: "http://192.168.1.10:80/onvif/device_service",
			want:          "rtsp://192.168.1.10:554/stream",
		},
		{
			name:          "stale host, RTSP default port preserved",
			rtspURL:       "rtsp://10.0.0.5/stream",
			onvifEndpoint: "http://10.0.0.9:8080/onvif/device_service",
			want:          "rtsp://10.0.0.9/stream",
		},
		{
			name:          "empty rtspURL → unchanged",
			rtspURL:       "",
			onvifEndpoint: "http://1.2.3.4/onvif/device_service",
			want:          "",
		},
		{
			name:          "garbage rtspURL → unchanged",
			rtspURL:       "://not-a-url",
			onvifEndpoint: "http://1.2.3.4/onvif/device_service",
			want:          "://not-a-url",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, rewriteStaleStreamHost(tc.rtspURL, tc.onvifEndpoint))
		})
	}
}

// TestDetectEncoding_RTSPAuthoritativeOverLyingConfig covers the regression where a
// HiSilicon-OEM camera declares H264 in ONVIF (and that lie was persisted to config)
// while the RTSP stream is actually H.265. The RTSP DESCRIBE result must win.
func TestDetectEncoding_RTSPAuthoritativeOverLyingConfig(t *testing.T) {
	client := &onvif.MockDeviceClient{
		Profiles: []onvif.DeviceProfile{
			{Token: "profile_1", Name: "HD", Encoding: "H264", Width: 2880, Height: 1620},
		},
	}
	r := newTestONVIFRecorder(t, client, func(or *ONVIFRecorder) {
		// Simulate the persisted lie: config says H264 …
		or.cfg.StreamEncoding = "H264"
		// … rtspURL is set (Start resolved it) …
		or.rtspURL = "rtsp://192.168.63.200:554/11"
		// … but the real stream is H265.
		or.probeEncodingFn = func() string { return "H265" }
	})

	enc := r.detectEncoding(context.Background())
	require.Equal(t, "H265", enc, "RTSP DESCRIBE must override the lying ONVIF/config value")

	// And createDelegate must produce an H265Recorder, not an H264Recorder.
	delegate := r.createDelegate(r.rtspURL)
	require.IsType(t, &H265Recorder{}, delegate)
}

// TestDetectEncoding_DESCRIBEFails_FallsBackToConfig ensures that when the RTSP
// probe cannot determine the format (e.g. device requires exotic auth at DESCRIBE
// time), the explicitly configured encoding is used. No regression for honest
// cameras behind auth.
func TestDetectEncoding_DESCRIBEFails_FallsBackToConfig(t *testing.T) {
	client := &onvif.MockDeviceClient{
		Profiles: []onvif.DeviceProfile{
			{Token: "profile_1", Name: "HD", Encoding: "H264"},
		},
	}
	r := newTestONVIFRecorder(t, client, func(or *ONVIFRecorder) {
		or.cfg.StreamEncoding = "H264"
		or.rtspURL = "rtsp://1.2.3.4/stream"
		or.probeEncodingFn = func() string { return "" } // DESCRIBE failed
	})

	enc := r.detectEncoding(context.Background())
	require.Equal(t, "H264", enc)
}

// TestDetectEncoding_DESCRIBEFails_FallsBackToONVIF covers the case with no manual
// config: DESCRIBE fails, so we trust the ONVIF profile declaration. This is the
// pre-fix behavior for cameras whose RTSP probe is unavailable at add time.
func TestDetectEncoding_DESCRIBEFails_FallsBackToONVIF(t *testing.T) {
	client := &onvif.MockDeviceClient{
		Profiles: []onvif.DeviceProfile{
			{Token: "profile_1", Name: "HD", Encoding: "H265"},
		},
	}
	r := newTestONVIFRecorder(t, client, func(or *ONVIFRecorder) {
		// No manual override; ONVIF says H265.
		or.cfg.StreamEncoding = ""
		or.rtspURL = "rtsp://1.2.3.4/stream"
		or.probeEncodingFn = func() string { return "" } // DESCRIBE failed
	})

	enc := r.detectEncoding(context.Background())
	require.Equal(t, "H265", enc)
}
