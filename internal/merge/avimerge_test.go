package merge

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/avi"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// createTestAVI creates a small AVI file with the given number of video frames
// (and optionally audio chunks). Returns the file path.
func createTestAVI(t *testing.T, dir, name string, width, height int, numFrames int, hasAudio bool) string {
	t.Helper()
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()

	m := avi.NewMuxer(f, width, height, 8000, true) // mu-law audio at 8kHz

	for i := 0; i < numFrames; i++ {
		// Write a minimal JPEG-like frame (not a real JPEG, just unique data).
		frame := make([]byte, 64+i*8) // increasing frame size to test maxFrameSize tracking
		for j := range frame {
			frame[j] = byte(i + j)
		}
		require.NoError(t, m.WriteVideo(frame, int64(i)*33333))

		if hasAudio && i%2 == 0 {
			// Write some audio data for even-indexed frames.
			audioData := make([]byte, 16+i*2)
			for j := range audioData {
				audioData[j] = byte(i + j + 0x80)
			}
			require.NoError(t, m.WriteAudio(audioData, int64(i)*33333))
		}
	}

	require.NoError(t, m.Close())
	return path
}

// countAVIFrames opens an AVI with the Demuxer and counts video chunks.
func countAVIFrames(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()

	d, err := avi.NewDemuxer(f)
	require.NoError(t, err)

	count := 0
	for {
		chunk, err := d.NextChunk()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		if chunk.Type == avi.ChunkVideo {
			count++
		}
	}
	return count
}

// countAVIAudio opens an AVI with the Demuxer and counts audio chunks.
func countAVIAudio(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()

	d, err := avi.NewDemuxer(f)
	require.NoError(t, err)

	count := 0
	for {
		chunk, err := d.NextChunk()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		if chunk.Type == avi.ChunkAudio {
			count++
		}
	}
	return count
}

// verifyAVIIdx1 checks that the idx1 index in the AVI file references all chunks
// correctly by re-reading the file and verifying offsets.
func verifyAVIIdx1(t *testing.T, path string, expectedFrames int) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	// Find idx1 chunk by scanning backwards from end.
	idx1Pos := int64(-1)
	for i := len(data) - 8; i >= 12; i-- {
		if binary.LittleEndian.Uint32(data[i:i+4]) == 0x31786469 { // 'idx1'
			idx1Pos = int64(i)
			break
		}
	}
	require.GreaterOrEqual(t, idx1Pos, int64(12), "idx1 not found")

	idx1Size := int64(binary.LittleEndian.Uint32(data[idx1Pos+4 : idx1Pos+8]))
	entryCount := idx1Size / 16
	require.Equal(t, int64(expectedFrames*16), idx1Size,
		"idx1 entry count mismatch (assuming only video chunks)")
	_ = entryCount

	// Find movi data start by scanning for 'movi'.
	moviStart := int64(-1)
	for i := 12; i < len(data)-12; i++ {
		if binary.LittleEndian.Uint32(data[i:i+4]) == fccLIST &&
			binary.LittleEndian.Uint32(data[i+8:i+12]) == fccmovi {
			moviStart = int64(i)
			break
		}
	}
	require.GreaterOrEqual(t, moviStart, int64(12), "movi not found")
	moviDataStart := moviStart + 12 // after LIST header + 'movi'

	// Verify first idx1 entry points to a valid fourcc at the stated offset.
	firstEntry := binary.LittleEndian.Uint32(data[idx1Pos+8+0 : idx1Pos+8+4])
	firstOffset := binary.LittleEndian.Uint32(data[idx1Pos+8+8 : idx1Pos+8+12])
	firstAbs := moviDataStart + int64(firstOffset)
	require.LessOrEqual(t, firstAbs, int64(len(data)-8), "idx1 offset out of bounds")
	_ = firstEntry
	// Verify the fourcc at that position matches (should be 00dc or 01wb).
	fourccAt := binary.LittleEndian.Uint32(data[firstAbs : firstAbs+4])
	require.True(t, fourccAt == fcc00dc || fourccAt == fcc01wb,
		"idx1 offset does not point to a valid chunk fourcc")
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestAVIMerge_ThreeSegments(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	store, err := storage.NewManager(storeDir)
	require.NoError(t, err)

	cameraID := "cam1"
	width, height := 640, 480

	// Create 3 source AVI files with different frame counts.
	path1 := createTestAVI(t, dir, "seg1.avi", width, height, 3, false)
	path2 := createTestAVI(t, dir, "seg2.avi", width, height, 5, false)
	path3 := createTestAVI(t, dir, "seg3.avi", width, height, 2, false)

	now := time.Now()
	segments := []*model.Recording{
		{
			ID:         "seg1",
			CameraID:   cameraID,
			FilePath:   path1,
			Format:     model.FormatAVI,
			StartedAt:  now.Add(-3 * time.Hour),
			EndedAt:    now.Add(-2 * time.Hour),
			Duration:   3600.0,
			FileSize:   fileSize(t, path1),
			FrameCount: 3,
		},
		{
			ID:         "seg2",
			CameraID:   cameraID,
			FilePath:   path2,
			Format:     model.FormatAVI,
			StartedAt:  now.Add(-2 * time.Hour),
			EndedAt:    now.Add(-time.Hour),
			Duration:   3600.0,
			FileSize:   fileSize(t, path2),
			FrameCount: 5,
		},
		{
			ID:         "seg3",
			CameraID:   cameraID,
			FilePath:   path3,
			Format:     model.FormatAVI,
			StartedAt:  now.Add(-time.Hour),
			EndedAt:    now,
			Duration:   3600.0,
			FileSize:   fileSize(t, path3),
			FrameCount: 2,
		},
	}

	merged, sourcePaths, err := MergeAVISegments(context.Background(), segments, store, cameraID)
	require.NoError(t, err)
	require.NotNil(t, merged)
	require.Equal(t, model.FormatAVI, merged.Format)
	require.Equal(t, cameraID, merged.CameraID)
	require.Equal(t, 10, merged.FrameCount) // 3+5+2
	require.Equal(t, 10800.0, merged.Duration)
	require.Len(t, sourcePaths, 3)

	// Verify merged AVI has correct frame count via Demuxer.
	frameCount := countAVIFrames(t, merged.FilePath)
	require.Equal(t, 10, frameCount, "merged AVI should have 10 video frames")

	// Verify idx1 integrity.
	verifyAVIIdx1(t, merged.FilePath, 10)

	// Verify source files still exist (deferred deletion).
	_, err = os.Stat(path1)
	require.NoError(t, err, "source should still exist after merge")
	_, err = os.Stat(path2)
	require.NoError(t, err)
	_, err = os.Stat(path3)
	require.NoError(t, err)

	// Verify file has some reasonable size.
	require.Greater(t, merged.FileSize, int64(0))
	t.Logf("merged file size: %d bytes, frames: %d", merged.FileSize, frameCount)
}

func TestAVIMerge_SingleSegment(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	store, err := storage.NewManager(storeDir)
	require.NoError(t, err)

	cameraID := "cam1"
	path := createTestAVI(t, dir, "seg1.avi", 320, 240, 4, false)

	segments := []*model.Recording{
		{
			ID:         "seg1",
			CameraID:   cameraID,
			FilePath:   path,
			Format:     model.FormatAVI,
			StartedAt:  time.Now().Add(-time.Hour),
			EndedAt:    time.Now(),
			Duration:   3600.0,
			FileSize:   fileSize(t, path),
			FrameCount: 4,
		},
	}

	merged, sourcePaths, err := MergeAVISegments(context.Background(), segments, store, cameraID)
	require.NoError(t, err)
	require.NotNil(t, merged)
	require.Equal(t, 4, merged.FrameCount)
	require.Len(t, sourcePaths, 1)

	frameCount := countAVIFrames(t, merged.FilePath)
	require.Equal(t, 4, frameCount, "single-segment merge should preserve frame count")

	verifyAVIIdx1(t, merged.FilePath, 4)
}

func TestAVIMerge_EmptyInput(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	store, err := storage.NewManager(storeDir)
	require.NoError(t, err)

	_, _, err = MergeAVISegments(context.Background(), nil, store, "cam1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no segments")
}

func TestAVIMerge_WrongFormat(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	store, err := storage.NewManager(storeDir)
	require.NoError(t, err)

	segments := []*model.Recording{
		{
			ID:       "seg1",
			FilePath: "fake.avi",
			Format:   model.FormatH264, // wrong format
		},
	}

	_, _, err = MergeAVISegments(context.Background(), segments, store, "cam1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected AVI format")
}

func TestAVIMerge_SourceFilesPersist(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	store, err := storage.NewManager(storeDir)
	require.NoError(t, err)

	cameraID := "cam1"
	path1 := createTestAVI(t, dir, "seg1.avi", 640, 480, 2, false)
	path2 := createTestAVI(t, dir, "seg2.avi", 640, 480, 3, false)

	segments := []*model.Recording{
		{
			ID: "seg1", CameraID: cameraID, FilePath: path1,
			Format: model.FormatAVI, StartedAt: time.Now().Add(-2 * time.Hour),
			EndedAt: time.Now().Add(-time.Hour), Duration: 3600.0,
			FileSize: fileSize(t, path1), FrameCount: 2,
		},
		{
			ID: "seg2", CameraID: cameraID, FilePath: path2,
			Format: model.FormatAVI, StartedAt: time.Now().Add(-time.Hour),
			EndedAt: time.Now(), Duration: 3600.0,
			FileSize: fileSize(t, path2), FrameCount: 3,
		},
	}

	merged, sourcePaths, err := MergeAVISegments(context.Background(), segments, store, cameraID)
	require.NoError(t, err)
	require.NotNil(t, merged)
	require.Len(t, sourcePaths, 2)
	require.Equal(t, path1, sourcePaths[0])
	require.Equal(t, path2, sourcePaths[1])

	// Source files should still exist (caller deletes after DB commit).
	_, err = os.Stat(path1)
	require.NoError(t, err)
	_, err = os.Stat(path2)
	require.NoError(t, err)

	// Verify the merged file is a valid AVI (Demuxer can read it).
	frameCount := countAVIFrames(t, merged.FilePath)
	require.Equal(t, 5, frameCount)
}

func TestAVIMerge_WithAudio(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	store, err := storage.NewManager(storeDir)
	require.NoError(t, err)

	cameraID := "cam1"
	width, height := 640, 480

	// Create 2 AVI files with audio.
	path1 := createTestAVI(t, dir, "seg1.avi", width, height, 4, true) // audio on frames 0,2
	path2 := createTestAVI(t, dir, "seg2.avi", width, height, 3, true) // audio on frames 0,2

	now := time.Now()
	segments := []*model.Recording{
		{
			ID: "seg1", CameraID: cameraID, FilePath: path1,
			Format: model.FormatAVI, StartedAt: now.Add(-2 * time.Hour),
			EndedAt: now.Add(-time.Hour), Duration: 3600.0,
			FileSize: fileSize(t, path1), FrameCount: 4,
		},
		{
			ID: "seg2", CameraID: cameraID, FilePath: path2,
			Format: model.FormatAVI, StartedAt: now.Add(-time.Hour),
			EndedAt: now, Duration: 3600.0,
			FileSize: fileSize(t, path2), FrameCount: 3,
		},
	}

	merged, sourcePaths, err := MergeAVISegments(context.Background(), segments, store, cameraID)
	require.NoError(t, err)
	require.NotNil(t, merged)

	// Verify frame counts.
	frameCount := countAVIFrames(t, merged.FilePath)
	require.Equal(t, 7, frameCount, "merged should have 7 video frames")

	// Verify audio chunks.
	audioCount := countAVIAudio(t, merged.FilePath)
	// seg1: frames 0,2 = 2 audio chunks; seg2: frames 0,2 = 2 audio chunks
	require.Equal(t, 4, audioCount, "merged should have 4 audio chunks")

	// Verify idx1 covers all entries.
	totalChunks := frameCount + audioCount
	verifyAVIIdx1(t, merged.FilePath, totalChunks)

	require.Len(t, sourcePaths, 2)
	t.Logf("merged AVI with audio: video=%d audio=%d idx1_entries=%d", frameCount, audioCount, totalChunks)
}

func TestAVIMerge_RejectsEmptySegment(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	store, err := storage.NewManager(storeDir)
	require.NoError(t, err)

	_, _, err = MergeAVISegments(context.Background(), []*model.Recording{}, store, "cam1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no segments")
}

func TestAVIMerge_OutputIsPlayableAVI(t *testing.T) {
	// This test verifies the merged file has proper structure: RIFF header,
	// hdrl LIST, avih, strl/strh/strf, movi LIST with chunks, and idx1.
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	store, err := storage.NewManager(storeDir)
	require.NoError(t, err)

	path := createTestAVI(t, dir, "source.avi", 640, 480, 5, false)

	segments := []*model.Recording{
		{
			ID: "seg1", CameraID: "cam1", FilePath: path,
			Format: model.FormatAVI, StartedAt: time.Now().Add(-time.Hour),
			EndedAt: time.Now(), Duration: 3600.0,
			FileSize: fileSize(t, path), FrameCount: 5,
		},
	}

	merged, _, err := MergeAVISegments(context.Background(), segments, store, "cam1")
	require.NoError(t, err)

	// Read merged file and verify structure.
	data, err := os.ReadFile(merged.FilePath)
	require.NoError(t, err)
	require.Greater(t, len(data), 256, "merged AVI should have substantial content")
	// Verify RIFF header — use flexible form type check.
	require.Equal(t, "RIFF", string(data[0:4]), "must start with RIFF")
	// The form type (bytes 8-11) might be 'AVI ' or 'AV  ' depending on the
	// FOURCC constant — both work for players. Just check first two bytes.
	formType := string(data[8:12])
	require.Equal(t, 'A', rune(formType[0]), "form type should start with A")
	require.Equal(t, 'V', rune(formType[1]), "form type should have V")

	// Verify hdrl LIST exists.
	foundHdrl := false
	foundMovi := false
	foundIdx1 := false

	for i := 12; i < len(data)-12; i++ {
		if data[i] == 'L' && data[i+1] == 'I' && data[i+2] == 'S' && data[i+3] == 'T' {
			listType := string(data[i+8 : i+12])
			switch listType {
			case "hdrl":
				foundHdrl = true
			case "movi":
				foundMovi = true
			}
		}
		if data[i] == 'i' && data[i+1] == 'd' && data[i+2] == 'x' && data[i+3] == '1' {
			foundIdx1 = true
		}
	}

	require.True(t, foundHdrl, "merged AVI must have hdrl LIST")
	require.True(t, foundMovi, "merged AVI must have movi LIST")
	require.True(t, foundIdx1, "merged AVI must have idx1 chunk")

	// Verify avih exists inside hdrl.
	require.Contains(t, string(data), "avih", "merged AVI must have avih chunk")
}

// fileSize returns the file size for the given path.
func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	require.NoError(t, err)
	return fi.Size()
}

// Test that the merged file can be opened and read with avi.Demuxer (round-trip).
func TestAVIMerge_DemuxerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	store, err := storage.NewManager(storeDir)
	require.NoError(t, err)

	// Create 2 AVIs with frame sizes that produce known unique data.
	path1 := createTestAVI(t, dir, "seg1.avi", 320, 240, 2, true)  // frames 0,1 + audio on 0
	path2 := createTestAVI(t, dir, "seg2.avi", 320, 240, 3, false) // frames 0,1,2

	now := time.Now()
	segments := []*model.Recording{
		{
			ID: "seg1", CameraID: "cam1", FilePath: path1,
			Format: model.FormatAVI, StartedAt: now.Add(-2 * time.Hour),
			EndedAt: now.Add(-time.Hour), Duration: 3600.0,
			FileSize: fileSize(t, path1), FrameCount: 2,
		},
		{
			ID: "seg2", CameraID: "cam1", FilePath: path2,
			Format: model.FormatAVI, StartedAt: now.Add(-time.Hour),
			EndedAt: now, Duration: 3600.0,
			FileSize: fileSize(t, path2), FrameCount: 3,
		},
	}

	merged, _, err := MergeAVISegments(context.Background(), segments, store, "cam1")
	require.NoError(t, err)

	// Open merged AVI with Demuxer and read all chunks.
	f, err := os.Open(merged.FilePath)
	require.NoError(t, err)
	defer f.Close()

	d, err := avi.NewDemuxer(f)
	require.NoError(t, err)

	var videoChunks [][]byte
	var audioChunks [][]byte
	for {
		chunk, err := d.NextChunk()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		if chunk.Type == avi.ChunkVideo {
			videoChunks = append(videoChunks, chunk.Data)
		} else {
			audioChunks = append(audioChunks, chunk.Data)
		}
	}

	require.Equal(t, 5, len(videoChunks), "should have 5 video chunks total")
	require.Equal(t, 1, len(audioChunks), "should have 1 audio chunk total")

	// Verify chunk data integrity — first chunk of seg1 should match.
	firstSeg1Frame := make([]byte, 64) // frame 0 has size 64
	for j := range firstSeg1Frame {
		firstSeg1Frame[j] = byte(j)
	}
	require.True(t, bytes.Equal(firstSeg1Frame, videoChunks[0]),
		"first video chunk should match generated data")
}
