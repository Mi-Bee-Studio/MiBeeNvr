package snapshot

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- fixtures ---------------------------------------------------------------

// h264IDRAU is a complete H.264 keyframe access unit (raw NALUs, no start
// codes): SPS (type 7) + PPS (type 8) + IDR (type 5) — the minimum the hub's
// HasCompleteParamSets gate accepts for IDR-cache replay.
func h264IDRAU() [][]byte {
	return [][]byte{
		{0x67, 0x64, 0x00, 0x1f, 0xac, 0xd9, 0x40, 0x50, 0x05, 0xbb, 0x01, 0x6c, 0x80},
		{0x68, 0xeb, 0xec, 0xb2, 0x2c},
		{0x65, 0x88, 0x84, 0x21, 0xa0, 0x7b, 0xdf, 0x3f},
	}
}

func shortenHubWait(t *testing.T) {
	t.Helper()
	old := hubWaitTimeout
	hubWaitTimeout = 100 * time.Millisecond
	t.Cleanup(func() { hubWaitTimeout = old })
}

// --- recorder stubs ----------------------------------------------------------

type frameRecorder struct {
	model.Recorder // embedded nil: Start/Stop/Status never called in these tests
	frame          []byte
}

func (r *frameRecorder) LatestFrame() []byte { return r.frame }

type delegatingRecorder struct {
	model.Recorder
	inner model.Recorder
}

func (r *delegatingRecorder) Delegate() model.Recorder { return r.inner }

type hubRecorder struct {
	model.Recorder
	hub *streamhub.StreamHub
}

func (r *hubRecorder) GetHub() *streamhub.StreamHub { return r.hub }

// --- source stubs ------------------------------------------------------------

type stubCamSource struct {
	rec model.Recorder
}

func (s stubCamSource) GetRecorder(string) model.Recorder { return s.rec }

type stubConfigSource struct {
	cam *config.CameraConfig
}

func (s stubConfigSource) GetCameraConfig(string) *config.CameraConfig { return s.cam }

// --- FrameFromRecorder -------------------------------------------------------

func TestFrameFromRecorder_LatestFrame(t *testing.T) {
	want := []byte{0xff, 0xd8, 0x00, 0x01, 0xff, 0xd9}
	rec := &frameRecorder{frame: want}

	got, err := FrameFromRecorder(rec, nil)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestFrameFromRecorder_DelegateUnwrap(t *testing.T) {
	// ONVIF recorders wrap a JPEG delegate — unwrap must reach LatestFrame.
	want := []byte{0xff, 0xd8, 0x02}
	rec := &delegatingRecorder{inner: &frameRecorder{frame: want}}

	got, err := FrameFromRecorder(rec, nil)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestFrameFromRecorder_HubCachedIDRReplay(t *testing.T) {
	hub := streamhub.New()
	// Broadcast BEFORE subscribing: the cached IDR must be replayed to the new
	// one-shot subscriber without waiting for the next GOP.
	au := h264IDRAU()
	hub.Broadcast(1000, au, true)

	var gotAU [][]byte
	decode := func(in [][]byte) ([]byte, error) {
		gotAU = in
		return []byte{0xff, 0xd8}, nil
	}

	got, err := FrameFromRecorder(&hubRecorder{hub: hub}, decode)
	require.NoError(t, err)
	assert.Equal(t, []byte{0xff, 0xd8}, got)
	assert.Equal(t, au, gotAU, "decoder must receive the cached IDR AU with param sets")
}

func TestFrameFromRecorder_HubNoFrames_ErrNoFrame(t *testing.T) {
	shortenHubWait(t)
	hub := streamhub.New()

	_, err := FrameFromRecorder(&hubRecorder{hub: hub}, func([][]byte) ([]byte, error) {
		return nil, errors.New("must not be called")
	})
	assert.ErrorIs(t, err, ErrNoFrame)
}

func TestFrameFromRecorder_NoDecodeFunc_ErrNoFrame(t *testing.T) {
	hub := streamhub.New()
	hub.Broadcast(1000, h264IDRAU(), true)

	// FFmpeg absent (decode nil): hub path unavailable, no snapshot URL here.
	_, err := FrameFromRecorder(&hubRecorder{hub: hub}, nil)
	assert.ErrorIs(t, err, ErrNoFrame)
}

func TestFrameFromRecorder_DecodeErrorWraps(t *testing.T) {
	hub := streamhub.New()
	hub.Broadcast(1000, h264IDRAU(), true)

	_, err := FrameFromRecorder(&hubRecorder{hub: hub}, func([][]byte) ([]byte, error) {
		return nil, errors.New("ffmpeg failed")
	})
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNoFrame, "decode failure must surface, not alias to ErrNoFrame")
}

// --- Capturer ----------------------------------------------------------------

func TestCapturer_RecorderPathWins(t *testing.T) {
	var httpCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpCalls.Add(1)
		w.Write([]byte("url-jpeg"))
	}))
	defer srv.Close()

	c := &Capturer{
		Recorder: stubCamSource{rec: &frameRecorder{frame: []byte("rec-jpeg")}},
		Config:   stubConfigSource{cam: &config.CameraConfig{SnapshotURL: srv.URL}},
		Client:   srv.Client(),
	}

	got, err := c.Capture("cam")
	require.NoError(t, err)
	assert.Equal(t, []byte("rec-jpeg"), got)
	assert.Zero(t, httpCalls.Load(), "snapshot URL must not be hit when the recorder supplies a frame")
}

func TestCapturer_SnapshotURLFallbackWithAuth(t *testing.T) {
	var user, pass string
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte("url-jpeg"))
	}))
	defer srv.Close()
	user, pass = "admin", "secret"

	c := &Capturer{
		Recorder: stubCamSource{rec: nil},
		Config: stubConfigSource{cam: &config.CameraConfig{
			SnapshotURL: srv.URL,
			Username:    user,
			Password:    pass,
		}},
		Client: srv.Client(),
	}

	got, err := c.Capture("cam")
	require.NoError(t, err)
	assert.Equal(t, []byte("url-jpeg"), got)

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	req.SetBasicAuth(user, pass)
	assert.Equal(t, req.Header.Get("Authorization"), gotAuth, "camera credentials must be sent as Basic auth")
}

func TestCapturer_DecodeFailureFallsBackToURL(t *testing.T) {
	hub := streamhub.New()
	hub.Broadcast(1000, h264IDRAU(), true)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("url-jpeg"))
	}))
	defer srv.Close()

	c := &Capturer{
		Decode:   func([][]byte) ([]byte, error) { return nil, errors.New("ffmpeg failed") },
		Recorder: stubCamSource{rec: &hubRecorder{hub: hub}},
		Config:   stubConfigSource{cam: &config.CameraConfig{SnapshotURL: srv.URL}},
		Client:   srv.Client(),
	}

	got, err := c.Capture("cam")
	require.NoError(t, err)
	assert.Equal(t, []byte("url-jpeg"), got, "a decode failure must still try the snapshot URL")
}

func TestCapturer_NoPathAvailable_ErrNoFrame(t *testing.T) {
	c := &Capturer{
		Recorder: stubCamSource{rec: nil},
		Config:   stubConfigSource{cam: &config.CameraConfig{}}, // no SnapshotURL
		Client:   http.DefaultClient,
	}

	_, err := c.Capture("cam")
	assert.ErrorIs(t, err, ErrNoFrame)
}

func TestCapturer_URLNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	c := &Capturer{
		Recorder: stubCamSource{rec: nil},
		Config:   stubConfigSource{cam: &config.CameraConfig{SnapshotURL: srv.URL}},
		Client:   srv.Client(),
	}

	_, err := c.Capture("cam")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNoFrame)
}

func TestCapturer_URLBodyCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(make([]byte, snapshotBodyCap+1))
	}))
	defer srv.Close()

	c := &Capturer{
		Recorder: stubCamSource{rec: nil},
		Config:   stubConfigSource{cam: &config.CameraConfig{SnapshotURL: srv.URL}},
		Client:   srv.Client(),
	}

	_, err := c.Capture("cam")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cap")
}

func TestFrameFromRecorder_SubscribeAlwaysUnsubscribed(t *testing.T) {
	hub := streamhub.New()
	hub.Broadcast(1000, h264IDRAU(), true)

	before := subSeq.Load()
	_, err := FrameFromRecorder(&hubRecorder{hub: hub}, func([][]byte) ([]byte, error) {
		return []byte("j"), nil
	})
	require.NoError(t, err)

	// The one-shot ID must be released after capture — a leaked subscriber
	// pins frames forever. Re-subscribing the SAME id succeeding proves the
	// Unsubscribe ran.
	used := fmt.Sprintf("snapshot-%d", before+1)
	reSubErr := hub.Subscribe(used, func(int64, [][]byte) {})
	require.NoError(t, reSubErr, "one-shot subscription %q must be released after capture", used)
	hub.Unsubscribe(used)
}
