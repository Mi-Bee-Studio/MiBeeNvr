package recorder

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

// A mid-GOP session start delivers P-frames first: the recorder latches the
// configured encoding guess (builder default h264 when the camera row is
// empty). When the first real parameter-set NALUs arrive and reveal the other
// codec, the definitive detection must OVERRIDE the guess — otherwise the
// recorder waits for the wrong parameter sets forever (MiBeeNvr #625: H.265
// cascade channel mis-latched as h264 → FLV/WS 503 "waiting for video
// stream" on the upper platform).
func TestGB28181Recorder_DefinitiveCodecOverridesFallback(t *testing.T) {
	store, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)
	rec := NewGB28181Recorder(GB28181Config{
		CameraID:      "latch-cam",
		Encoding:      "h264", // the fallback guess (actual stream is H.265)
		SegmentDur:    time.Hour,
		Store:         store,
		RecordEnabled: true,
	}, nil)
	rec.Hub = streamhub.New()
	rec.Hub.SetCameraID("latch-cam")
	require.NoError(t, rec.Start(context.Background()))
	defer rec.Stop()

	// 1) P-frame first (H.265 TRAIL_R): latches the h264 fallback.
	rec.WriteNALU([][]byte{{0x02, 0x01, 0x02, 0x03}}, 90000, false)
	codec0, sps, _, _ := rec.CodecParams()
	require.Equal(t, "h264", string(codec0))
	require.Nil(t, sps)

	// 2) The stream's real IDR arrives with H.265 parameter sets → override.
	h265IDR := [][]byte{
		{0x40, 0x01, 0x0c, 0x01}, // VPS
		{0x42, 0x01, 0x01, 0x90}, // SPS
		{0x44, 0x01, 0xc0, 0xac}, // PPS
		{0x26, 0x01, 0xaf, 0x06}, // IDR_W_RADL slice
	}
	rec.WriteNALU(h265IDR, 93600, true)

	codec, sps2, _, vps2 := rec.CodecParams()
	require.Equal(t, "h265", string(codec), "definitive param sets must override the fallback guess")
	require.NotNil(t, vps2, "H.265 VPS must be captured after the override")
	require.NotNil(t, sps2, "H.265 SPS must be captured after the override")
}

// Once decided definitively, later AUs must not flip the codec again.
func TestGB28181Recorder_DefinitiveCodecIsSticky(t *testing.T) {
	store, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)
	rec := NewGB28181Recorder(GB28181Config{
		CameraID:      "sticky-cam",
		Encoding:      "",
		SegmentDur:    time.Hour,
		Store:         store,
		RecordEnabled: true,
	}, nil)
	rec.Hub = streamhub.New()
	rec.Hub.SetCameraID("sticky-cam")
	require.NoError(t, rec.Start(context.Background()))
	defer rec.Stop()

	h264AU := [][]byte{{0x67, 0x42, 0x00, 0x1e}, {0x68, 0xce, 0x38, 0x80}, {0x65, 0x88, 0x84}}
	rec.WriteNALU(h264AU, 90000, true)
	rec.WriteNALU([][]byte{{0x40, 0x01, 0x0c}}, 93600, false) // stray H.265-looking byte
	codec, _, _, _ := rec.CodecParams()
	require.Equal(t, "h264", string(codec), "definitive decision must be sticky")
}
