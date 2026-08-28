package recorder

// Long-tail coverage for recorder small surfaces (#585): NAL driver
// parameter-set extraction (h264/h265), ambient sidecar lifecycle, codec
// accessor labels, backoff wrappers, storage-health helpers, the StubRecorder,
// and audio-trigger config resolution. Hermetic — recorders are constructed
// but never started.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// newRecTestStore builds a real storage.Manager on a temp dir.
func newRecTestStore(t *testing.T) *storage.Manager {
	t.Helper()
	m, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)
	return m
}

func TestH264ParamSetExtraction(t *testing.T) {
	t.Parallel()
	store := newRecTestStore(t)
	rec := NewH264Recorder(H264Config{CameraID: "c", SegmentDur: time.Second}, store)

	require.Equal(t, "h264", H264NALDriver{}.codecLabel())
	require.NotNil(t, H264NALDriver{}.rtpFormat())

	sps := []byte{0x67, 0x42, 0x00, 0x0a}
	pps := []byte{0x68, 0xce, 0x38}
	// One access unit carrying SPS(7), PPS(8), IDR(5) and a non-param NAL.
	au := [][]byte{sps, pps, {0x65, 0x88}, {0x41, 0x10}}
	rec.Hub = model.NewStreamHub() // wired by camera.initStreamHub in production
	H264NALDriver{}.extractParamSets(rec.baseRecorder, au)

	require.Equal(t, sps, rec.SPS())
	require.Equal(t, pps, rec.PPS())
	require.NotNil(t, rec.GetHub())
}

func TestH265ParamSetExtraction(t *testing.T) {
	t.Parallel()
	rec := NewH265Recorder(H265Config{CameraID: "c", SegmentDur: time.Second}, newRecTestStore(t))

	require.Equal(t, "h265", H265NALDriver{}.codecLabel())
	require.NotNil(t, H265NALDriver{}.rtpFormat())

	// H265 NAL type = (firstByte>>1)&0x3F: VPS=32 (0x40), SPS=33 (0x42), PPS=34 (0x44).
	vps := []byte{0x40, 0x01}
	sps := []byte{0x42, 0x01}
	pps := []byte{0x44, 0x01}
	au := [][]byte{vps, sps, pps, {0x26, 0x01}}
	H265NALDriver{}.extractParamSets(rec.baseRecorder, au)

	require.Equal(t, vps, rec.VPS())
	require.Equal(t, sps, rec.SPS())
	require.Equal(t, pps, rec.PPS())
}

func TestAmbientSidecarLifecycle(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	rec := NewH264Recorder(H264Config{
		CameraID:   "c",
		SegmentDur: time.Second,
		Adaptive:   &AdaptiveConfig{AmbientAudio: true, AmbientArchive: true},
	}, newRecTestStore(t))
	b := rec.baseRecorder

	// Disabled path first: a recorder without the adaptive flags gets nil.
	bare := NewH264Recorder(H264Config{CameraID: "bare", SegmentDur: time.Second}, newRecTestStore(t))
	require.Nil(t, bare.openAmbientSidecar(filepath.Join(dir, "bare.mp4")))

	// Enabled path: sidecar file is created and appended to.
	final := filepath.Join(dir, "seg.mp4")
	f := b.openAmbientSidecar(final)
	require.NotNil(t, f)
	b.mu.Lock()
	b.ambientFile = f
	b.mu.Unlock()

	b.writeAmbientArchive(nil) // empty write is a no-op
	b.writeAmbientArchive([]byte{0x01, 0x02})
	b.closeAmbientSidecar()

	data, err := os.ReadFile(final + ".g711")
	require.NoError(t, err)
	require.Equal(t, []byte{0x01, 0x02}, data)

	// Close is idempotent after nil-out.
	b.closeAmbientSidecar()
}

func TestBackoffWrappers(t *testing.T) {
	t.Parallel()
	require.Equal(t, TieredBackoff(0), TieredBackoff(0))
	require.Greater(t, TieredBackoff(5).Nanoseconds(), int64(0))
	require.GreaterOrEqual(t, TieredBackoffWithJitter(3), TieredBackoff(3))
	require.GreaterOrEqual(t, StorageBackoffWithJitter(), 55*time.Second)
}

func TestStorageHealthHelpers(t *testing.T) {
	t.Parallel()
	require.False(t, isStorageFailed(nil, "c")) // nil store → not failed

	last := time.Now().Add(-2 * time.Minute)
	newLast, ok := shouldLogHealth(last)
	require.True(t, ok)
	require.True(t, newLast.After(last))

	_, ok = shouldLogHealth(time.Now())
	require.False(t, ok, "health logs are rate-limited")
}

func TestStubRecorderSurface(t *testing.T) {
	t.Parallel()
	s := &StubRecorder{Hub: model.NewStreamHub()}
	require.NoError(t, s.Start(context.Background()))
	require.Equal(t, model.StatusRecording, s.Status())
	require.NoError(t, s.Stop())
	require.Equal(t, model.StatusStopped, s.Status())
	require.NotNil(t, s.GetHub())
}

func TestResolveAudioTriggerConfig(t *testing.T) {
	t.Parallel()
	cfg := ResolveAudioTriggerConfig(-30, 4)
	require.Equal(t, -30.0, cfg.MinDBFS)
	require.Equal(t, 4*time.Second, cfg.PreCapture)
}
