package recorder

import (
	"context"
	"errors"
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
		StreamURI:  nil, // GetStreamURI returns nil → will panic, use a different approach
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

	t.Run("unknown encoding", func(t *testing.T) {
		client := &onvif.MockDeviceClient{
			Profiles: []onvif.DeviceProfile{
				{Token: "p1", Encoding: "JPEG", Width: 640, Height: 480},
			},
		}
		r := newTestONVIFRecorder(t, client)
		encoding := r.detectEncoding(context.Background())
		// Falls through to first profile's encoding
		require.Equal(t, "JPEG", encoding)
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
