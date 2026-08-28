package recorder

// Coverage for darkframe.go (#585): dark-segment detection over real
// JPEGs (image/jpeg encoded bright/dark fixtures) and AVI containers built
// with the internal avi muxer. Fully hermetic.

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/avi"
	"github.com/stretchr/testify/require"
)

// writeJPEGFile encodes a solid-color JPEG of the given luminance.
func writeJPEGFile(t *testing.T, path string, lum uint8) {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 64, 64))
	for i := range img.Pix {
		img.Pix[i] = lum
	}
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	require.NoError(t, jpeg.Encode(f, img, nil))
}

// jpegBytes returns an encoded solid-color JPEG.
func jpegBytes(t *testing.T, lum uint8) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 64, 64))
	for i := range img.Pix {
		img.Pix[i] = lum
	}
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, nil))
	return buf.Bytes()
}

var _ = color.Gray{} // keep image/color if fixtures evolve

func TestDetectDarkMJPEGDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for i := range 5 {
		writeJPEGFile(t, filepath.Join(dir, frameName(i)), 250) // bright
	}
	isDark, brightness, err := DetectDarkMJPEGDir(dir, 15)
	require.NoError(t, err)
	require.False(t, isDark)
	require.Greater(t, brightness, 200)

	// All-dark directory below the threshold.
	darkDir := t.TempDir()
	for i := range 5 {
		writeJPEGFile(t, filepath.Join(darkDir, frameName(i)), 2)
	}
	isDark, brightness, err = DetectDarkMJPEGDir(darkDir, 15)
	require.NoError(t, err)
	require.True(t, isDark)
	require.Less(t, brightness, 15)

	// Missing directory / no JPEGs / corrupt frames error out.
	_, _, err = DetectDarkMJPEGDir(filepath.Join(t.TempDir(), "missing"), 15)
	require.Error(t, err)

	empty := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(empty, "notes.txt"), []byte("x"), 0o644))
	_, _, err = DetectDarkMJPEGDir(empty, 15)
	require.ErrorContains(t, err, "no JPEG files")

	corrupt := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(corrupt, "a.jpg"), []byte("not a jpeg"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(corrupt, "b.jpg"), []byte("also not"), 0o644))
	_, _, err = DetectDarkMJPEGDir(corrupt, 15)
	require.ErrorContains(t, err, "failed to decode")
}

func frameName(i int) string {
	return "frame_20260828_10" + string(rune('0'+i)) + "0000.jpg"
}

func TestDetectDarkAVIFile(t *testing.T) {
	t.Parallel()

	// Bright AVI: three real JPEG frames through the muxer.
	brightPath := filepath.Join(t.TempDir(), "bright.avi")
	writeAVIWithFrames(t, brightPath, 3, 250)
	isDark, brightness, err := DetectDarkAVIFile(brightPath, 15)
	require.NoError(t, err)
	require.False(t, isDark)
	require.Greater(t, brightness, 200)

	// Dark AVI.
	darkPath := filepath.Join(t.TempDir(), "dark.avi")
	writeAVIWithFrames(t, darkPath, 3, 2)
	isDark, brightness, err = DetectDarkAVIFile(darkPath, 15)
	require.NoError(t, err)
	require.True(t, isDark)
	require.Less(t, brightness, 15)

	// Missing file errors.
	_, _, err = DetectDarkAVIFile(filepath.Join(t.TempDir(), "missing.avi"), 15)
	require.Error(t, err)
}

func writeAVIWithFrames(t *testing.T, path string, n int, lum uint8) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	mux := avi.NewMuxer(f, 64, 48, 8000, true)
	frame := jpegBytes(t, lum)
	for i := range n {
		require.NoError(t, mux.WriteVideo(frame, int64(i)*1_000_000))
	}
	require.NoError(t, mux.Close())
}

func TestJpegBrightnessFromReader(t *testing.T) {
	t.Parallel()

	b, err := jpegBrightnessFromReader(bytes.NewReader(jpegBytes(t, 128)))
	require.NoError(t, err)
	require.InDelta(t, 128, b, 2)

	// Garbage input errors.
	_, err = jpegBrightnessFromReader(bytes.NewReader([]byte("nope")))
	require.ErrorContains(t, err, "jpeg decode")
}
