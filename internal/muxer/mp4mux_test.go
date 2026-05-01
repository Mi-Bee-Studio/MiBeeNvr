package muxer

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Minimal valid H.264 SPS (baseline profile 66, level 30)
var testSPS = []byte{0x67, 0x42, 0xc0, 0x1e, 0xd9, 0x00, 0xa0, 0x47, 0xfe, 0xc8}

// Minimal valid H.264 PPS
var testPPS = []byte{0x68, 0xce, 0x38, 0x80}

func TestNewMP4Muxer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.mp4")

	m := NewMP4Muxer(path)
	require.NotNil(t, m)
	assert.Equal(t, path, m.filePath)
	assert.Nil(t, m.file)
}

func TestAddH264Track(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.mp4")

	m := NewMP4Muxer(path)
	require.NotNil(t, m)

	trackID, err := m.AddH264Track(testSPS, testPPS)
	require.NoError(t, err)
	assert.Equal(t, 1, trackID)

	// Adding a second track should return ID 2
	trackID2, err := m.AddH264Track(testSPS, testPPS)
	require.NoError(t, err)
	assert.Equal(t, 2, trackID2)
}

func TestWriteAndClose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output.mp4")

	m := NewMP4Muxer(path)

	trackID, err := m.AddH264Track(testSPS, testPPS)
	require.NoError(t, err)

	// Write a few dummy H.264 NAL units (IDR frame + non-IDR frames)
	// In Annex B format, each NAL starts with 0x00 0x00 0x00 0x01
	idrNAL := []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x88, 0x80, 0x40} // IDR slice
	nonIDRNAL := []byte{0x00, 0x00, 0x00, 0x01, 0x41, 0x9a, 0x24}    // non-IDR slice

	err = m.WriteSample(trackID, idrNAL, 0, 33*time.Millisecond)
	require.NoError(t, err)

	err = m.WriteSample(trackID, nonIDRNAL, 33*time.Millisecond, 33*time.Millisecond)
	require.NoError(t, err)

	err = m.WriteSample(trackID, nonIDRNAL, 66*time.Millisecond, 33*time.Millisecond)
	require.NoError(t, err)

	err = m.Close()
	require.NoError(t, err)

	// Verify the file exists and has content
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Greater(t, info.Size(), int64(0))

	// Read the file and verify ftyp box signature
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	// MP4 files start with a box: [4-byte size][4-byte type]
	// ftyp box should be the first box
	require.GreaterOrEqual(t, len(data), 8)
	assert.True(t, bytes.HasPrefix(data[4:8], []byte("ftyp")),
		"File should start with ftyp box, got: %x", data[4:8])

	// Verify total duration
	assert.Equal(t, 99*time.Millisecond, m.Duration())
}

func TestEmptyClose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.mp4")

	m := NewMP4Muxer(path)

	// Close without writing any tracks or samples should be safe
	err := m.Close()
	require.NoError(t, err)

	// Duration should be zero
	assert.Equal(t, time.Duration(0), m.Duration())
}

func TestDuration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "duration.mp4")

	m := NewMP4Muxer(path)
	assert.Equal(t, time.Duration(0), m.Duration())

	trackID, err := m.AddH264Track(testSPS, testPPS)
	require.NoError(t, err)

	// Before writing, duration is 0
	assert.Equal(t, time.Duration(0), m.Duration())

	// Write samples and check cumulative duration
	err = m.WriteSample(trackID, []byte{0x00, 0x00, 0x01, 0x65}, 0, 40*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, 40*time.Millisecond, m.Duration())

	err = m.WriteSample(trackID, []byte{0x00, 0x00, 0x01, 0x41}, 40*time.Millisecond, 40*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, 80*time.Millisecond, m.Duration())

	err = m.Close()
	require.NoError(t, err)
}

func TestWriteSampleInvalidTrack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "invalid.mp4")

	m := NewMP4Muxer(path)
	trackID, err := m.AddH264Track(testSPS, testPPS)
	require.NoError(t, err)

	// Writing to a non-existent track should error
	err = m.WriteSample(trackID+99, []byte{0x00}, 0, 33*time.Millisecond)
	assert.Error(t, err)

	m.Close()
}

// testSPS720 is a known 1280x720 Baseline profile SPS.
// Profile 66, Level 30, pic_width_in_mbs=80, pic_height_in_map_units=45, frame_mbs_only=1.
var testSPS720 = []byte{0x67, 0x42, 0xc0, 0x1e, 0xf4, 0x02, 0x80, 0x2d, 0x80}

// testSPS1080 is a known 1920x1080 Baseline profile SPS.
// Profile 66, Level 40, pic_width_in_mbs=120, pic_height_in_map_units=68,
// frame_mbs_only=1, crop_bottom_minus1=4 (crop 8px for 4:2:0).
var testSPS1080 = []byte{0x67, 0x42, 0xc0, 0x28, 0xf4, 0x03, 0xc0, 0x11, 0x2f, 0x28}

func TestParseSPSResolution(t *testing.T) {
	tests := []struct {
		name       string
		sps        []byte
		wantWidth  int
		wantHeight int
	}{
		{
			name:       "existing testSPS (640x128)",
			sps:        testSPS,
			wantWidth:  640,
			wantHeight: 128,
		},
		{
			name:       "1280x720 Baseline",
			sps:        testSPS720,
			wantWidth:  1280,
			wantHeight: 720,
		},
		{
			name:       "1920x1080 Baseline with crop",
			sps:        testSPS1080,
			wantWidth:  1920,
			wantHeight: 1080,
		},
		{
			name:       "too short SPS returns 0,0",
			sps:        []byte{0x67, 0x42, 0xc0},
			wantWidth:  0,
			wantHeight: 0,
		},
		{
			name:       "empty SPS returns 0,0",
			sps:        []byte{},
			wantWidth:  0,
			wantHeight: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, h := parseSPSResolution(tt.sps)
			assert.Equal(t, tt.wantWidth, w)
			assert.Equal(t, tt.wantHeight, h)
		})
	}
}

func TestAddH264TrackParsesResolution(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.mp4")

	m := NewMP4Muxer(path)
	_, err := m.AddH264Track(testSPS720, testPPS)
	require.NoError(t, err)

	// Verify the track has parsed resolution.
	require.Len(t, m.tracks, 1)
	assert.Equal(t, 1280, m.tracks[0].width)
	assert.Equal(t, 720, m.tracks[0].height)
}
