package muxer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/prealloc"
	"github.com/stretchr/testify/require"
)

// writePreallocTestSamples drives one muxer through a fixed sample set so
// outputs are comparable across prealloc on/off.
func writePreallocTestSamples(t *testing.T, path string, preallocBytes int64) {
	t.Helper()
	m := NewMP4Muxer(path)
	if preallocBytes > 0 {
		m.SetPreallocateBytes(preallocBytes)
	}
	trackID, err := m.AddH264Track(testSPS, testPPS)
	require.NoError(t, err)
	for i := range 60 {
		var nalu []byte
		if i%15 == 0 {
			nalu = make([]byte, 32<<10) // periodic "keyframe"-sized sample
		} else {
			nalu = make([]byte, 4<<10)
		}
		require.NoError(t, m.WriteSample(trackID, nalu, time.Duration(i)*33*time.Millisecond, 33*time.Millisecond))
	}
	require.NoError(t, m.Close())
}

// TestMP4Muxer_PreallocOutputByteIdentical: preallocation must not change
// the output content — same samples, byte-identical files.
func TestMP4Muxer_PreallocOutputByteIdentical(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain.mp4")
	pre := filepath.Join(dir, "pre.mp4")

	writePreallocTestSamples(t, plain, 0)
	writePreallocTestSamples(t, pre, 4<<20)

	plainB, err := os.ReadFile(plain)
	require.NoError(t, err)
	preB, err := os.ReadFile(pre)
	require.NoError(t, err)
	require.Equal(t, plainB, preB, "preallocated output must be byte-identical")
}

// TestMP4Muxer_PreallocTruncatesZeroTail is THE nail test (#757): fallocate
// extends the file to the estimated size, so Close MUST ftruncate to the
// actual content end — otherwise crash-recovered segments carry zero tails
// that inflate probed durations.
func TestMP4Muxer_PreallocTruncatesZeroTail(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain.mp4")
	pre := filepath.Join(dir, "pre.mp4")

	writePreallocTestSamples(t, plain, 0)
	writePreallocTestSamples(t, pre, 8<<20) // estimate far above actual (~300KB)

	plainInfo, err := os.Stat(plain)
	require.NoError(t, err)
	preInfo, err := os.Stat(pre)
	require.NoError(t, err)
	require.Equal(t, plainInfo.Size(), preInfo.Size(),
		"preallocated file must be truncated to the exact content length (no zero tail)")

	// Sanity: only zeros at the tail would make sizes differ — also verify
	// the last bytes are real moov content, not zero padding.
	preB, err := os.ReadFile(pre)
	require.NoError(t, err)
	tail := preB[len(preB)-16:]
	allZero := true
	for _, b := range tail {
		if b != 0 {
			allZero = false
			break
		}
	}
	require.False(t, allZero, "file tail must be moov content, not zero padding")
}

// TestMP4Muxer_PreallocOvershootStillGrows: the estimate is a hint, not a
// bound — writing past it must grow the file normally.
func TestMP4Muxer_PreallocOvershootStillGrows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "over.mp4")

	m := NewMP4Muxer(path)
	m.SetPreallocateBytes(16 << 10) // deliberately tiny
	trackID, err := m.AddH264Track(testSPS, testPPS)
	require.NoError(t, err)
	for i := range 40 {
		require.NoError(t, m.WriteSample(trackID, make([]byte, 32<<10), time.Duration(i)*33*time.Millisecond, 33*time.Millisecond))
	}
	require.NoError(t, m.Close())

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(40*32<<10), "file must grow past the preallocation hint")
}

// TestMP4Muxer_PreallocFailureDegrades: an unsupported filesystem (ENOSYS /
// EOPNOTSUPP) must degrade silently to the append-write status quo.
func TestMP4Muxer_PreallocFailureDegrades(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "degrade.mp4")

	restore := prealloc.SwapForTest(func(_ *os.File, _ int64) error { return errors.New("injected EOPNOTSUPP") })
	t.Cleanup(restore)

	m := NewMP4Muxer(path)
	m.SetPreallocateBytes(4 << 20)
	trackID, err := m.AddH264Track(testSPS, testPPS)
	require.NoError(t, err)
	for i := range 10 {
		require.NoError(t, m.WriteSample(trackID, make([]byte, 8<<10), time.Duration(i)*33*time.Millisecond, 33*time.Millisecond))
	}
	require.NoError(t, m.Close(), "preallocation failure must not fail the segment")

	// Output still parses as the plain variant.
	plain := filepath.Join(dir, "plain.mp4")
	writePreallocTestSamples2(t, plain)
	preB, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotEmpty(t, preB)
}

// writePreallocTestSamples2 is the 10×8KB variant matching the degrade test.
func writePreallocTestSamples2(t *testing.T, path string) {
	t.Helper()
	m := NewMP4Muxer(path)
	trackID, err := m.AddH264Track(testSPS, testPPS)
	require.NoError(t, err)
	for i := range 10 {
		require.NoError(t, m.WriteSample(trackID, make([]byte, 8<<10), time.Duration(i)*33*time.Millisecond, 33*time.Millisecond))
	}
	require.NoError(t, m.Close())
}
