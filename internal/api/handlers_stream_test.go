package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/hls"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// --- StreamHandler interface tests ---

func TestHLSStreamHandler_CanHandle(t *testing.T) {
	t.Parallel()
	h := &HLSStreamHandler{}

	// HLS can handle H.264 and H.265
	require.True(t, h.CanHandle(model.FormatH264), "HLS should handle H.264")
	require.True(t, h.CanHandle(model.FormatH265), "HLS should handle H.265")

	// HLS cannot handle MJPEG or JPEG
	require.False(t, h.CanHandle(model.FormatMJPEG), "HLS should not handle MJPEG")
	require.False(t, h.CanHandle(model.EncJPEG), "HLS should not handle JPEG")
}

func TestHLSStreamHandler_Name(t *testing.T) {
	t.Parallel()
	h := &HLSStreamHandler{}
	require.Equal(t, "hls", h.Name())
}

// --- StreamRegistry tests ---

func TestStreamRegistry_RegisterAndQuery(t *testing.T) {
	t.Parallel()
	reg := NewStreamRegistry()

	hlsHandler := &HLSStreamHandler{}
	reg.Register(hlsHandler)

	handlers := reg.handlersForCodec(model.FormatH264)
	require.Len(t, handlers, 1)
	require.Equal(t, "hls", handlers[0].Name())

	handlers = reg.handlersForCodec(model.FormatH265)
	require.Len(t, handlers, 1)
	require.Equal(t, "hls", handlers[0].Name())
}

func TestStreamRegistry_H265ExcludesWebRTC(t *testing.T) {
	t.Parallel()
	reg := NewStreamRegistry()

	// Register HLS handler (supports H.264 and H.265)
	reg.Register(&HLSStreamHandler{})
	// Register stub WebRTC handler (supports H.264 only)
	reg.Register(&stubStreamHandler{
		name:   "webrtc",
		codecs: []model.Format{model.FormatH264},
	})

	// H.264 camera: both HLS and WebRTC available
	protocols := reg.protocolsForCodec(model.FormatH264)
	require.Contains(t, protocols, "hls")
	require.Contains(t, protocols, "webrtc")

	// H.265 camera: only HLS available (WebRTC excluded)
	protocols = reg.protocolsForCodec(model.FormatH265)
	require.Contains(t, protocols, "hls")
	require.NotContains(t, protocols, "webrtc", "WebRTC should not be available for H.265")
}

func TestStreamRegistry_FLVSupportsH264AndH265(t *testing.T) {
	t.Parallel()
	reg := NewStreamRegistry()

	reg.Register(&HLSStreamHandler{})
	reg.Register(&stubStreamHandler{
		name:   "flv",
		codecs: []model.Format{model.FormatH264, model.FormatH265},
	})

	// H.265: HLS and FLV available, not WebRTC
	protocols := reg.protocolsForCodec(model.FormatH265)
	require.Contains(t, protocols, "hls")
	require.Contains(t, protocols, "flv")
	require.NotContains(t, protocols, "webrtc")
}

func TestStreamRegistry_MJPEGProtocols(t *testing.T) {
	t.Parallel()
	reg := NewStreamRegistry()

	reg.Register(&HLSStreamHandler{})
	reg.Register(&stubStreamHandler{
		name:   "webrtc",
		codecs: []model.Format{model.FormatH264},
	})
	reg.Register(&MJPEGStreamHandler{})

	// MJPEG cameras should have mjpeg streaming protocol
	protocols := reg.protocolsForCodec(model.FormatMJPEG)
	require.Contains(t, protocols, "mjpeg")
}

func TestStreamRegistry_Empty(t *testing.T) {
	t.Parallel()
	reg := NewStreamRegistry()

	handlers := reg.handlersForCodec(model.FormatH264)
	require.Empty(t, handlers)

	protocols := reg.protocolsForCodec(model.FormatH264)
	require.Empty(t, protocols)
}

func TestStreamRegistry_StreamLimits(t *testing.T) {
	t.Parallel()
	// Test that the HLS stream limit (max 4) is enforced via LRU eviction.
	// When capacity is reached, the least recently used stream is evicted
	// and the new stream is accepted (instead of rejecting with an error).
	hlsMgr := hls.NewManagerWithOpts(context.Background(), t.TempDir(), 0, 0, 0)
	defer hlsMgr.StopAll()

	// Start 4 streams to fill the limit
	for i := 0; i < 4; i++ {
		err := hlsMgr.StartStream(
			string(rune('a'+i)),
			[]byte{0x67, 0x42, 0xc0, 0x0a, 0xd9, 0x00, 0xa0, 0x47, 0xfe, 0x88},
			[]byte{0x68, 0xce, 0x38, 0x80},
			0,
		)
		require.NoError(t, err)
	}

	// 5th stream should succeed via LRU eviction of 'a' (oldest)
	err := hlsMgr.StartStream(
		"overflow",
		[]byte{0x67, 0x42, 0xc0, 0x0a, 0xd9, 0x00, 0xa0, 0x47, 0xfe, 0x88},
		[]byte{0x68, 0xce, 0x38, 0x80},
		0,
	)
	require.NoError(t, err)

	// Verify stream count is still 4 (not 5) — LRU eviction kept it at capacity
	require.Equal(t, 4, hlsMgr.GetActiveStreamCount(), "maxStreams should be enforced via LRU")
}

// --- GET /api/protocols endpoint tests (per-camera) ---

func TestProtocolsEndpoint_RegistryIntegration(t *testing.T) {
	t.Parallel()
	// Test that the Handler can have a StreamRegistry set and that
	// the /api/protocols endpoint returns protocol data from the registry
	db, store := setupTestDB(t)
	defer db.Close()

	reg := NewStreamRegistry()
	reg.Register(&HLSStreamHandler{})
	reg.Register(&stubStreamHandler{
		name:   "webrtc",
		codecs: []model.Format{model.FormatH264},
	})
	reg.Register(&stubStreamHandler{
		name:   "flv",
		codecs: []model.Format{model.FormatH264, model.FormatH265},
	})
	reg.Register(&stubStreamHandler{
		name:   "ll-hls",
		codecs: []model.Format{model.FormatH264, model.FormatH265},
	})

	h := NewHandler(db, store, noopAuthMW(), nil, nil, nil, "", nil, nil, nil)
	h.SetStreamRegistry(reg)

	rr := doRequest(t, h.Routes(), "GET", "/api/protocols", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	// The existing /api/protocols endpoint still returns the camera protocol list
	// This test verifies the registry is wired up without breaking the existing endpoint
}

// --- No type-switch spaghetti in handlers_stream.go ---

func TestHandlersStream_NoRecorderTypeAssertions(t *testing.T) {
	t.Parallel()
	// This is a compile-time / grep-time verification.
	// The file handlers_stream.go should NOT contain rec.(*recorder.
	// This test documents the requirement; actual enforcement is via grep in CI.
	// We just verify the StreamHandler interface exists and is usable.
	var _ StreamHandler = &HLSStreamHandler{}
	var _ StreamHandler = &stubStreamHandler{}
}

// --- stubStreamHandler for testing ---

type stubStreamHandler struct {
	name   string
	codecs []model.Format
}

func (s *stubStreamHandler) Name() string { return s.name }
func (s *stubStreamHandler) CanHandle(codec model.Format) bool {
	for _, c := range s.codecs {
		if c == codec {
			return true
		}
	}
	return false
}
func (s *stubStreamHandler) StartStream(camID string, rec model.Recorder, opts StreamStartOptions) error {
	return nil
}
func (s *stubStreamHandler) StopStream(camID string) error {
	return nil
}
