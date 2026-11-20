package recordingsapi

import (
	"encoding/binary"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
)

// TestReadAudioOnlyMP4 probes the minimal box reader against the muxer's
// real output: fourcc, rate, channels, and exact mdat round-trip.
func TestReadAudioOnlyMP4(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "seg.mp4")
	g711 := []byte{0x00, 0x7F, 0xFF, 0x80, 0x55, 0x2A}
	writeTestG711MP4(t, path, g711)

	fourcc, rate, channels, payload, err := readAudioOnlyMP4(path)
	if err != nil {
		t.Fatalf("readAudioOnlyMP4: %v", err)
	}
	if fourcc != "ulaw" {
		t.Fatalf("fourcc = %q, want ulaw", fourcc)
	}
	if rate != 8000 || channels != 1 {
		t.Fatalf("rate/channels = %d/%d, want 8000/1", rate, channels)
	}
	if len(payload) != len(g711) {
		t.Fatalf("mdat payload = %d bytes, want %d", len(payload), len(g711))
	}
	for i := range g711 {
		if payload[i] != g711[i] {
			t.Fatalf("payload[%d] = %#x, want %#x", i, payload[i], g711[i])
		}
	}
}

// TestReadAudioOnlyMP4_Opus pins the dops shape: the muxer's Opus track must
// surface through the box reader as fourcc 'dops' with the configured
// channels/rate — the audio recorder's Opus path depends on it.
func TestReadAudioOnlyMP4_Opus(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "seg.mp4")
	m := muxer.NewMP4Muxer(path)
	trackID, err := m.AddAudioTrack("opus", []byte{2, 0, 0, 0, 0, 0xBB, 0x80}) // stereo, PreSkip 0, 48000Hz
	if err != nil {
		t.Fatalf("add opus track: %v", err)
	}
	frames := [][]byte{
		{0x01, 0x02, 0x03},
		{0x04, 0x05, 0x06, 0x07},
	}
	for i, f := range frames {
		if err := m.WriteAudioSample(trackID, f, time.Duration(i)*20*time.Millisecond, 20*time.Millisecond); err != nil {
			t.Fatalf("write opus sample: %v", err)
		}
	}
	if err := m.Close(); err != nil {
		t.Fatalf("close muxer: %v", err)
	}

	fourcc, rate, channels, payload, err := readAudioOnlyMP4(path)
	if err != nil {
		t.Fatalf("readAudioOnlyMP4: %v", err)
	}
	if fourcc != "Opus" {
		t.Fatalf("fourcc = %q, want Opus (QuickTime-style entry our muxer writes)", fourcc)
	}
	if rate != 48000 || channels != 2 {
		t.Fatalf("rate/channels = %d/%d, want 48000/2", rate, channels)
	}
	if len(payload) != 7 {
		t.Fatalf("payload = %d bytes, want 7", len(payload))
	}
}

// TestG711ToPCM pins known decode vectors through the API transcode path.
func TestG711ToPCM(t *testing.T) {
	t.Parallel()
	pcm := recorder.G711ToPCM([]byte{0xFF, 0xFF}, true, 8000)
	if len(pcm) != 4 {
		t.Fatalf("pcm length = %d", len(pcm))
	}
	if binary.LittleEndian.Uint16(pcm[0:2]) != 0 {
		t.Fatalf("μ-law 0xFF should decode to 0, got %d", binary.LittleEndian.Uint16(pcm[0:2]))
	}
}

func writeTestG711MP4(t *testing.T, path string, samples []byte) {
	t.Helper()
	m := muxer.NewMP4Muxer(path)
	trackID, err := m.AddAudioTrack("g711", []byte{1, 0, 0, 0x1f, 0x40}) // μ-law, 8000Hz
	if err != nil {
		t.Fatalf("add audio track: %v", err)
	}
	if err := m.WriteAudioSample(trackID, samples, 0, 20*time.Millisecond); err != nil {
		t.Fatalf("write sample: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("close muxer: %v", err)
	}
}

func TestParseDateParts(t *testing.T) {
	t.Parallel()
	y, m, d, err := parseDateParts("2026-08-20")
	require.NoError(t, err)
	require.Equal(t, 2026, y)
	require.Equal(t, 8, m)
	require.Equal(t, 20, d)

	for _, bad := range []string{"2026-08", "2026/08/20", "aaaa-bb-cc", ""} {
		_, _, _, err := parseDateParts(bad)
		require.Error(t, err, bad)
	}
}
