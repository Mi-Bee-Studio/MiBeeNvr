package recorder

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// TestSetObservedCodec pins the runtime reconciliation: a recorder configured
// h264 that observes an h265 publisher must serve h265 from CodecParams (the
// /api/cameras read path prefers the recorder's codec, #166); bogus codec
// values are ignored.
func TestSetObservedCodec(t *testing.T) {
	r := NewIngestRecorder(IngestConfig{CameraID: "c1", Encoding: "h264"})

	codec, _, _, _ := r.CodecParams()
	require.Equal(t, model.FormatH264, codec)

	r.SetObservedCodec("h265")
	codec, _, _, _ = r.CodecParams()
	require.Equal(t, model.FormatH265, codec)

	r.SetObservedCodec("vp9")
	codec, _, _, _ = r.CodecParams()
	require.Equal(t, model.FormatH265, codec)
}
