package mediaprobe

// Coverage for FastProbeDuration + MediaInfo.FormatDuration (#580). The MP4
// fixture is built with the real internal muxer — same pattern as the merge
// package's tests.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
	"github.com/stretchr/testify/require"
)

var (
	fastProbeSPS = []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	fastProbePPS = []byte{0x68, 0xce, 0x38, 0x80}
)

func buildProbeMP4(t *testing.T, samples int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "seg.mp4")
	m := muxer.NewMP4Muxer(path)
	trackID, err := m.AddH264Track(fastProbeSPS, fastProbePPS)
	require.NoError(t, err)
	for i := range samples {
		nalu := []byte{0x41, 0x10, 0x00, byte(i)}
		require.NoError(t, m.WriteSample(trackID, nalu, time.Duration(i)*33*time.Millisecond, 33*time.Millisecond))
	}
	require.NoError(t, m.Close())
	return path
}

func TestFastProbeDuration(t *testing.T) {
	t.Parallel()
	p := buildProbeMP4(t, 30)
	d, err := FastProbeDuration(p)
	require.NoError(t, err)
	require.InDelta(t, 0.99, d, 0.1) // 30 samples × 33ms

	// Missing / non-MP4 files error, never panic.
	_, err = FastProbeDuration(filepath.Join(t.TempDir(), "missing.mp4"))
	require.Error(t, err)
	_, err = FastProbeDuration("/etc/hostname")
	require.Error(t, err)
}

func TestFormatDuration(t *testing.T) {
	t.Parallel()
	m := &MediaInfo{}
	require.Equal(t, "0s", m.FormatDuration())
	m2 := &MediaInfo{Duration: 90}
	require.Contains(t, m2.FormatDuration(), "1m30s")
}
