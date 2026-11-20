package streamsapi

// Test stubs shared by the streaming-domain tests. Mirrors of the api-package
// stubs (handlers_xiaomi_audio_test.go / handlers_mjpeg_test.go) — kept local
// so the domain tests stay hermetic after the extraction.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// stubRecorder is a bare model.Recorder with no behavior.
type stubRecorder struct{}

func (stubRecorder) Start(context.Context) error  { return nil }
func (stubRecorder) Stop() error                  { return nil }
func (stubRecorder) Status() model.RecorderStatus { return "" }

// fakeSegmentStore is a no-op recorder.SegmentStore for constructing recorders
// in tests without a real storage manager.
type fakeSegmentStore struct{}

func (fakeSegmentStore) CreateSegment(_ string, _ string) (string, string, error) {
	return "", "", nil
}
func (fakeSegmentStore) WriteFrame(_ string, _ []byte) (int, error) { return 0, nil }
func (fakeSegmentStore) CloseSegment(_, _ string) error             { return nil }

// stubStreamHandler is a configurable StreamHandler stub (mirror of the
// api-package stub of the same name).
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

func (s *stubStreamHandler) StartStream(string, model.Recorder, StreamStartOptions) error { return nil }

func (s *stubStreamHandler) StopStream(string) error { return nil }

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
