package recframes

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/avi"
	"github.com/stretchr/testify/require"
)

// --- Frame timestamp extraction tests (moved from internal/api, #1009 batch 2) ---

func TestParseFrameFilename(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		filename string
		want     string // expected timestamp in RFC3339, empty if should fail
	}{
		{name: "timestamp in name", filename: "frame_20240101_120000.jpg", want: "2024-01-01T12:00:00Z"},
		{name: "timestamp with suffix", filename: "frame_20240101_120000_001.jpg", want: "2024-01-01T12:00:00Z"},
		{name: "timestamp .jpeg", filename: "frame_20231225_083000.jpeg", want: "2023-12-25T08:30:00Z"},
		{name: "sequential no match", filename: "frame_000001.jpg", want: ""},
		{name: "non-frame name", filename: "snapshot_20240101_120000.jpg", want: ""},
		{name: "short name", filename: "frame_12.jpg", want: ""},
		{name: "no underscore", filename: "frame20240101120000.jpg", want: ""},
		{name: "wrong separator", filename: "frame_2024-0101_120000.jpg", want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseFrameFilename(tc.filename)
			if tc.want == "" {
				require.False(t, ok, "expected no match for %q, got %v", tc.filename, got)
				return
			}
			require.True(t, ok, "expected match for %q", tc.filename)
			expected, err := time.Parse(time.RFC3339, tc.want)
			require.NoError(t, err)
			require.True(t, got.Equal(expected), "parseFrameFilename(%q) = %v, want %v", tc.filename, got, expected)
		})
	}
}

// createTestJPEG encodes a small gradient JPEG.
func createTestJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			idx := y*img.Stride + x*4
			img.Pix[idx] = byte(x * 255 / width)
			img.Pix[idx+1] = byte(y * 255 / height)
			img.Pix[idx+2] = byte((x + y) * 255 / (width + height))
			img.Pix[idx+3] = 255
		}
	}
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}))
	return buf.Bytes()
}

func TestExtractEXIFDateTime(t *testing.T) {
	t.Parallel()

	t.Run("JPEG without EXIF returns false", func(t *testing.T) {
		t.Parallel()
		tmpFile := filepath.Join(t.TempDir(), "noexif.jpg")
		require.NoError(t, os.WriteFile(tmpFile, createTestJPEG(t, 100, 100), 0o644))
		_, ok := extractEXIFDateTime(tmpFile)
		require.False(t, ok)
	})

	t.Run("JPEG with EXIF DateTimeOriginal", func(t *testing.T) {
		t.Parallel()
		tmpFile := filepath.Join(t.TempDir(), "withexif.jpg")
		require.NoError(t, os.WriteFile(tmpFile, makeTestJPEGWithEXIF(t, "2024:06:15 14:30:00"), 0o644))
		got, ok := extractEXIFDateTime(tmpFile)
		require.True(t, ok)
		require.True(t, got.Equal(time.Date(2024, 6, 15, 14, 30, 0, 0, time.UTC)), "got %v", got)
	})

	t.Run("non-existent file returns false", func(t *testing.T) {
		t.Parallel()
		_, ok := extractEXIFDateTime(filepath.Join(t.TempDir(), "missing.jpg"))
		require.False(t, ok)
	})
}

func TestExtractFrameTimestamp(t *testing.T) {
	t.Parallel()

	t.Run("filename timestamp takes priority", func(t *testing.T) {
		t.Parallel()
		got := frameTimestamp("frame_20240101_120000.jpg", "/nonexistent", time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC))
		require.True(t, got.Equal(time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)), "got %v", got)
	})

	t.Run("EXIF when filename has no timestamp", func(t *testing.T) {
		t.Parallel()
		tmpFile := filepath.Join(t.TempDir(), "frame_000001.jpg")
		require.NoError(t, os.WriteFile(tmpFile, makeTestJPEGWithEXIF(t, "2024:06:15 14:30:00"), 0o644))
		got := frameTimestamp("frame_000001.jpg", tmpFile, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
		require.True(t, got.Equal(time.Date(2024, 6, 15, 14, 30, 0, 0, time.UTC)), "got %v (from EXIF)", got)
	})

	t.Run("ModTime fallback when no timestamp available", func(t *testing.T) {
		t.Parallel()
		tmpFile := filepath.Join(t.TempDir(), "frame_000001.jpg")
		require.NoError(t, os.WriteFile(tmpFile, createTestJPEG(t, 100, 100), 0o644))
		modTime := time.Date(2025, 3, 15, 10, 0, 0, 0, time.UTC)
		require.NoError(t, os.Chtimes(tmpFile, modTime, modTime))
		got := frameTimestamp("frame_000001.jpg", tmpFile, modTime)
		require.True(t, got.Equal(modTime), "got %v (ModTime fallback)", got)
	})
}

// makeTestJPEGWithEXIF creates a valid JPEG with an EXIF APP1 segment
// containing DateTimeOriginal (moved from internal/api, #1009 batch 2).
func makeTestJPEGWithEXIF(t *testing.T, dateTimeStr string) []byte {
	t.Helper()

	baseJPEG := createTestJPEG(t, 32, 32)

	// TIFF header (8 bytes), Little-Endian
	tiff := &bytes.Buffer{}
	tiff.Write([]byte("II"))
	binary.Write(tiff, binary.LittleEndian, uint16(0x002A))
	binary.Write(tiff, binary.LittleEndian, uint32(8))

	// IFD0: DateTime (0x0132) + ExifIFD pointer (0x8769)
	numEntries := uint16(2)
	binary.Write(tiff, binary.LittleEndian, numEntries)

	dateTimeBytes := []byte(dateTimeStr + "\x00")
	if len(dateTimeBytes) > 256 {
		t.Fatal("dateTime string too long")
	}

	binary.Write(tiff, binary.LittleEndian, uint16(0x0132))
	binary.Write(tiff, binary.LittleEndian, uint16(2))
	binary.Write(tiff, binary.LittleEndian, uint32(len(dateTimeBytes)))
	valueOffset := 8 + 2 + 2*12 + 4
	binary.Write(tiff, binary.LittleEndian, uint32(valueOffset))

	binary.Write(tiff, binary.LittleEndian, uint16(0x8769))
	binary.Write(tiff, binary.LittleEndian, uint16(4))
	binary.Write(tiff, binary.LittleEndian, uint32(1))
	exifIFDOffset := valueOffset + len(dateTimeBytes)
	binary.Write(tiff, binary.LittleEndian, uint32(exifIFDOffset))

	// Next IFD pointer (0) + DateTime string value
	binary.Write(tiff, binary.LittleEndian, uint32(0))
	tiff.Write(dateTimeBytes)

	// EXIF IFD: DateTimeOriginal (0x9003)
	for tiff.Len()%2 != 0 {
		tiff.WriteByte(0)
	}
	if tiff.Len() != exifIFDOffset {
		t.Fatalf("EXIF IFD offset mismatch: expected %d, got %d", exifIFDOffset, tiff.Len())
	}

	binary.Write(tiff, binary.LittleEndian, uint16(1))
	dateTimeOrigBytes := []byte(dateTimeStr + "\x00")
	origValueOffset := exifIFDOffset + 2 + 1*12 + 4

	binary.Write(tiff, binary.LittleEndian, uint16(0x9003))
	binary.Write(tiff, binary.LittleEndian, uint16(2))
	binary.Write(tiff, binary.LittleEndian, uint32(len(dateTimeOrigBytes)))
	binary.Write(tiff, binary.LittleEndian, uint32(origValueOffset))

	binary.Write(tiff, binary.LittleEndian, uint32(0))
	for tiff.Len() < origValueOffset {
		tiff.WriteByte(0)
	}
	tiff.Write(dateTimeOrigBytes)

	// APP1 segment after the SOI marker
	app1 := &bytes.Buffer{}
	segLen := 2 + 6 + tiff.Len()
	app1.Write([]byte{0xFF, 0xE1})
	binary.Write(app1, binary.BigEndian, uint16(segLen))
	app1.Write([]byte("Exif\x00\x00"))
	app1.Write(tiff.Bytes())

	require.Equal(t, byte(0xFF), baseJPEG[0])
	result := append(append([]byte{}, baseJPEG[:2]...), app1.Bytes()...)
	return append(result, baseJPEG[2:]...)
}

// --- Service tests ---

func writeFrameDir(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		require.NoError(t, os.WriteFile(filepath.Join(dir, n), []byte(strings.Repeat("j", 64)), 0o644))
	}
	return dir
}

func TestSortedImageFiles_FiltersAndSorts(t *testing.T) {
	t.Parallel()
	s := New()
	dir := writeFrameDir(t, "frame_000002.jpg", "frame_000001.jpg", "notes.txt", "clip.h264", "shot.png")

	names, err := s.SortedImageFiles(dir)
	require.NoError(t, err)
	require.Equal(t, []string{"frame_000001.jpg", "frame_000002.jpg", "shot.png"}, names,
		"listing-cache filter accepts jpg/jpeg/png only, sorted")

	_, err = s.SortedImageFiles(filepath.Join(dir, "missing"))
	require.Error(t, err)
}

func TestSortedImageFiles_CacheInvalidation(t *testing.T) {
	t.Parallel()
	s := New()
	dir := writeFrameDir(t, "frame_000001.jpg")

	names, err := s.SortedImageFiles(dir)
	require.NoError(t, err)
	require.Len(t, names, 1)

	// New frame → dir mtime changes → next listing picks it up even inside the TTL window.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "frame_000002.jpg"), []byte("j"), 0o644))
	// Ensure the directory mtime actually ticks (coarse filesystem timestamps).
	time.Sleep(1100 * time.Millisecond)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "frame_000003.jpg"), []byte("j"), 0o644))
	names, err = s.SortedImageFiles(dir)
	require.NoError(t, err)
	require.Len(t, names, 3, "mtime invalidation must observe new frames")
}

func TestMJPEGFrames(t *testing.T) {
	t.Parallel()
	s := New()
	dir := writeFrameDir(t, "b.jpeg", "a.jpg", "c.png", "d.txt", "e.h264")

	frames, err := s.MJPEGFrames(dir)
	require.NoError(t, err)
	require.Len(t, frames, 2, "jpg/jpeg only")
	require.Equal(t, "a.jpg", frames[0].Filename)
	require.Equal(t, "b.jpeg", frames[1].Filename)
	require.EqualValues(t, 64, frames[0].Size)

	_, err = s.MJPEGFrames(filepath.Join(dir, "missing"))
	require.Error(t, err)
}

func TestTimelapseFrames_FiltersAndTimestamps(t *testing.T) {
	t.Parallel()
	s := New()
	dir := writeFrameDir(t,
		"frame_20260820_100001.jpg",
		"frame_20260820_100002.jpg",
		"notes.txt",
		"chunk.h264",
		"chunk.h265",
	)

	frames, err := s.TimelapseFrames(dir)
	require.NoError(t, err)
	require.Len(t, frames, 4, "jpg/jpeg/h264/h265 accepted, others dropped")
	require.Equal(t, "chunk.h264", frames[0].Filename)
	require.Equal(t, "chunk.h265", frames[1].Filename)
	require.Equal(t, "frame_20260820_100001.jpg", frames[2].Filename)
	want, err := time.Parse("20060102_150405", "20260820_100001")
	require.NoError(t, err)
	require.True(t, frames[2].Timestamp.Equal(want), "filename-pattern timestamp, got %v", frames[2].Timestamp)
}

// buildTestAVI muxes n synthetic JPEG frames into an AVI container.
func buildTestAVI(t *testing.T, n int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "seg.avi")
	f, err := os.Create(p)
	require.NoError(t, err)
	defer f.Close()
	mux := avi.NewMuxer(f, 64, 48, 8000, true)
	for i := range n {
		frame := []byte(fmt.Sprintf("jpeg-frame-%02d-", i) + strings.Repeat("x", 32))
		require.NoError(t, mux.WriteVideo(frame, int64(i)*1_000_000))
	}
	require.NoError(t, mux.Close())
	return p
}

func TestAVIFrames(t *testing.T) {
	t.Parallel()
	s := New()
	p := buildTestAVI(t, 3)

	frames, err := s.AVIFrames(p)
	require.NoError(t, err)
	require.Len(t, frames, 3)
	for i, f := range frames {
		require.Equal(t, i, f.Index)
		require.Positive(t, f.Size)
		require.GreaterOrEqual(t, f.Offset, int64(0))
		if i > 0 {
			require.Greater(t, f.PTSUs, frames[i-1].PTSUs, "PTS must be monotonically increasing")
		}
	}

	// Payload bytes at the indexed offset match the muxed frame prefix.
	data, err := os.ReadFile(p)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(data[frames[1].Offset:frames[1].Offset+frames[1].Size]), "jpeg-frame-01"))

	_, err = s.AVIFrames(filepath.Join(t.TempDir(), "missing.avi"))
	require.Error(t, err)
}

// TestSortedImageFiles_Concurrent pins the cache locking (CI runs -race).
func TestSortedImageFiles_Concurrent(t *testing.T) {
	t.Parallel()
	s := New()
	dir := writeFrameDir(t, "frame_000001.jpg")
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				_, _ = s.SortedImageFiles(dir)
				_ = s.ProbeCodec(dir)
			}
		}()
	}
	wg.Wait()
}
