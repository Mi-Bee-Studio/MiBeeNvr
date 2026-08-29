package api

// Tests for the StreamHandler adapters + StreamRegistry + codec/hub
// extractors in handlers_stream.go / handlers_hls.go (#578). Fully
// hermetic: real hls.Manager pointed at a temp dir, stub recorders
// implementing model.Recorder + model.HLSProvider. No media pump, no
// network — the managers' start/stop bookkeeping is exercised with
// synthetic SPS/PPS bytes.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/hls"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
	"github.com/stretchr/testify/require"
)

// providerStub is a recorder that reports fixed codec parameters.
type providerStub struct {
	stubRecorder
	codec model.Format
	sps   []byte
	pps   []byte
	vps   []byte
	hub   *streamhub.StreamHub
}

func (p *providerStub) CodecParams() (model.Format, []byte, []byte, []byte) {
	return p.codec, p.sps, p.pps, p.vps
}

func (p *providerStub) GetHub() *streamhub.StreamHub { return p.hub }

// delegatingStub unwraps to an inner recorder (ONVIF-style).
type delegatingStub struct {
	stubRecorder
	inner model.Recorder
}

func (d *delegatingStub) Delegate() model.Recorder { return d.inner }

func newTestHLSHandler(t *testing.T) *HLSStreamHandler {
	t.Helper()
	mgr := hls.NewManagerWithOpts(t.Context(), t.TempDir(), 0, 0, 0)
	t.Cleanup(func() { mgr.StopAll() })
	return &HLSStreamHandler{Mgr: mgr}
}

func TestStreamRegistry_Dispatch(t *testing.T) {
	t.Parallel()
	reg := NewStreamRegistry()
	reg.Register(&HLSStreamHandler{})
	reg.Register(&WebRTCStreamHandler{})
	reg.Register(&MJPEGStreamHandler{})

	require.Equal(t, []string{"hls"}, reg.protocolsForCodec(model.FormatH265))
	require.Equal(t, []string{"hls", "webrtc"}, reg.protocolsForCodec(model.FormatH264))
	require.Equal(t, []string{"mjpeg"}, reg.protocolsForCodec(model.FormatMJPEG))
	require.Empty(t, reg.protocolsForCodec("rtsp"))

	detail := reg.ProtocolsDetailForCodec(model.FormatH264)
	require.Len(t, detail, 2)
	require.True(t, detail[0].Available)
	require.Equal(t, "hls", detail[0].Protocol)
}

// conditionalStub reports a supported-but-unavailable protocol.
type conditionalStub struct {
	stubRecorder
}

func (c *conditionalStub) Name() string                { return "cond" }
func (c *conditionalStub) CanHandle(model.Format) bool { return false }
func (c *conditionalStub) StartStream(string, model.Recorder, StreamStartOptions) error {
	return nil
}
func (c *conditionalStub) StopStream(string) error { return nil }
func (c *conditionalStub) SupportedCodec(codec model.Format) bool {
	return codec == model.FormatH265
}

func (c *conditionalStub) UnavailabilityReason(model.Format) string {
	return "disabled in settings"
}

func TestStreamRegistry_ConditionalHandler(t *testing.T) {
	t.Parallel()
	reg := NewStreamRegistry()
	reg.Register(&conditionalStub{})

	detail := reg.ProtocolsDetailForCodec(model.FormatH265)
	require.Len(t, detail, 1)
	require.False(t, detail[0].Available)
	require.Equal(t, "disabled in settings", detail[0].Reason)

	// For codecs it doesn't even nominally support, it is omitted entirely.
	require.Empty(t, reg.ProtocolsDetailForCodec(model.FormatMJPEG))
}

func TestHLSAdapter_StartStop(t *testing.T) {
	t.Parallel()
	adapter := newTestHLSHandler(t)

	// Happy path via HLSProvider with synthetic parameter sets.
	rec := &providerStub{codec: model.FormatH264, sps: []byte{0x67, 0x64}, pps: []byte{0x68}}
	require.NoError(t, adapter.StartStream("cam-1", rec, StreamStartOptions{}))
	require.True(t, adapter.Mgr.IsActive("cam-1"))

	// Stop with the recorder (hub path) and without.
	require.NoError(t, adapter.StopStreamWithRecorder("cam-1", rec))
	require.False(t, adapter.Mgr.IsActive("cam-1"))
	require.NoError(t, adapter.StopStream("cam-1"))

	// H.265 happy path.
	h265 := &providerStub{codec: model.FormatH265, sps: []byte{0x42}, pps: []byte{0x44}, vps: []byte{0x40}}
	require.NoError(t, adapter.StartStream("cam-2", h265, StreamStartOptions{}))
	require.True(t, adapter.Mgr.IsActive("cam-2"))
	require.NoError(t, adapter.StopStream("cam-2"))
}

func TestHLSAdapter_StartErrors(t *testing.T) {
	t.Parallel()

	// Nil manager.
	nilMgr := &HLSStreamHandler{}
	require.ErrorContains(t, nilMgr.StartStream("cam", &providerStub{codec: model.FormatH264}, StreamStartOptions{}), "not available")

	adapter := newTestHLSHandler(t)

	// Params not ready yet.
	require.ErrorContains(t, adapter.StartStream("cam", &providerStub{codec: model.FormatH264}, StreamStartOptions{}), "not ready")
	require.ErrorContains(t, adapter.StartStream("cam", &providerStub{codec: model.FormatH265}, StreamStartOptions{}), "not ready")

	// Provider reporting a codec HLS cannot mux → HLSSupportedCodecError.
	err := adapter.StartStream("cam", &providerStub{codec: model.FormatMJPEG}, StreamStartOptions{})
	var codecErr *model.HLSSupportedCodecError
	require.ErrorAs(t, err, &codecErr)

	// Non-provider recorder of an unrecognized type → same error.
	err = adapter.StartStream("cam", stubRecorder{}, StreamStartOptions{})
	require.ErrorAs(t, err, &codecErr)
}

func TestHLSAdapter_SubStreamFallback(t *testing.T) {
	t.Parallel()
	adapter := newTestHLSHandler(t)
	rec := &providerStub{codec: model.FormatH264, sps: []byte{0x67}, pps: []byte{0x68}}

	// A sub-stream URL that cannot be pulled falls back to hub subscription
	// (nil hub here) — the stream still starts.
	opts := StreamStartOptions{SubStreamURL: "rtsp://127.0.0.1:9/none"}
	require.NoError(t, adapter.StartStream("cam-sub", rec, opts))
	require.True(t, adapter.Mgr.IsActive("cam-sub"))
}

func TestStreamAdapters_NoOpRegistrations(t *testing.T) {
	t.Parallel()
	reg := NewStreamRegistry()
	reg.Register(&WebRTCStreamHandler{})
	reg.Register(&FLVStreamHandler{})
	reg.Register(&WSStreamHandler{})
	reg.Register(&MJPEGStreamHandler{})

	for _, codec := range []model.Format{model.FormatH264, model.FormatH265, model.FormatMJPEG, model.EncJPEG} {
		detail := reg.ProtocolsDetailForCodec(codec)
		require.NotEmpty(t, detail, string(codec))
		for _, d := range detail {
			require.True(t, d.Available, d.Protocol)
		}
	}

	// The no-op start/stop contracts hold for every adapter.
	for _, h := range []StreamHandler{
		&WebRTCStreamHandler{}, &FLVStreamHandler{}, &WSStreamHandler{}, &MJPEGStreamHandler{},
	} {
		require.NoError(t, h.StartStream("cam", stubRecorder{}, StreamStartOptions{}))
		require.NoError(t, h.StopStream("cam"))
	}

	// Naming contract the frontend depends on.
	require.Equal(t, "webrtc", (&WebRTCStreamHandler{}).Name())
	require.Equal(t, "flv", (&FLVStreamHandler{}).Name())
	require.Equal(t, "wasm", (&WSStreamHandler{}).Name())
	require.Equal(t, "mjpeg", (&MJPEGStreamHandler{}).Name())
}

func TestUnwrapDelegate(t *testing.T) {
	t.Parallel()
	inner := &providerStub{codec: model.FormatH264}
	outer := &delegatingStub{inner: inner}
	require.Equal(t, inner, unwrapDelegate(outer))

	// Delegation chain terminates at nil-safe innermost recorder.
	chain := &delegatingStub{inner: &delegatingStub{inner: stubRecorder{}}}
	_, ok := unwrapDelegate(chain).(stubRecorder)
	require.True(t, ok)

	// Plain recorder is returned unchanged.
	plain := stubRecorder{}
	require.Equal(t, plain, unwrapDelegate(plain))
}

func TestGetCodecParams(t *testing.T) {
	t.Parallel()

	// Provider path (through a delegation layer).
	deep := &delegatingStub{inner: &providerStub{codec: model.FormatH265, vps: []byte{1}}}
	codec, sps, _, vps := getCodecParams(deep)
	require.Equal(t, model.FormatH265, codec)
	require.Nil(t, sps)
	require.NotNil(t, vps)

	// JPEG/MJPEG concrete fallbacks.
	codec, _, _, _ = getCodecParams(&recorder.HTTPJPEGRecorder{})
	require.Equal(t, model.EncJPEG, codec)
	codec, _, _, _ = getCodecParams(&recorder.MJPEGRecorder{})
	require.Equal(t, model.FormatMJPEG, codec)

	// Unknown type → empty codec.
	codec, _, _, _ = getCodecParams(stubRecorder{})
	require.Empty(t, codec)
}

func TestGetStreamHubs(t *testing.T) {
	t.Parallel()

	// GetHub interface path.
	hub := &streamhub.StreamHub{}
	require.Equal(t, hub, getStreamHub(&providerStub{hub: hub}))

	// Recorders without a hub surface fall through to nil. (Concrete
	// recorder types embed *baseRecorder — a zero value panics on promoted
	// field reads, so they are exercised via their constructors in the
	// recorder package's own tests instead.)
	require.Nil(t, getStreamHub(stubRecorder{}))
	require.Nil(t, getRecorderHub(stubRecorder{}))
}

func TestParseQualityValues(t *testing.T) {
	t.Parallel()
	for q, want := range map[string]string{"": "main", "main": "main", "sub": "sub"} {
		got, err := parseQuality(httptest.NewRequest(http.MethodGet, "/x?quality="+q, nil))
		require.NoError(t, err, q)
		require.Equal(t, want, got)
	}
	_, err := parseQuality(httptest.NewRequest(http.MethodGet, "/x?quality=ultra", nil))
	require.ErrorContains(t, err, "invalid quality")
}

func TestAcquireSub(t *testing.T) {
	t.Parallel()

	// No camera manager → main-stream header, nil source.
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	w := httptest.NewRecorder()
	require.Nil(t, h.acquireSub(w, httptest.NewRequest(http.MethodGet, "/x", nil), "cam-1"))
	require.Equal(t, "main", w.Header().Get("X-Stream-Quality"))

	// Real manager, unknown camera → AcquireSubStream errors → main fallback.
	cfg := &config.Config{Storage: config.StorageConfig{RootDir: store.RootDir()}, Cameras: []config.CameraConfig{}}
	h2 := NewHandler(db, store, noopAuthMW(), cfg, camera.NewCameraManager(cfg, store, db, ""), nil, "", nil, nil, nil, nil, nil)
	w2 := httptest.NewRecorder()
	require.Nil(t, h2.acquireSub(w2, httptest.NewRequest(http.MethodGet, "/x", nil), "ghost"))
	require.Equal(t, "main", w2.Header().Get("X-Stream-Quality"))
}
