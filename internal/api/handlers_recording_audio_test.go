package api

import (
	"encoding/binary"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
)

// writeTestG711MP4 produces a real audio-only MP4 through the production
// muxer (μ-law, 8000 Hz) — the same shape the AudioRecorder writes.
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

func TestAudioWav_G711Transcode(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	rec := makeRecording("rec-audio-1", "cam-mic", string(model.FormatAudio), now, false)
	rec.FilePath = filepath.Join(store.RootDir(), "cam-mic", "audio-seg.mp4")
	if err := os.MkdirAll(filepath.Dir(rec.FilePath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// 160 bytes ≙ 20ms of μ-law silence-ish.
	g711 := make([]byte, 160)
	for i := range g711 {
		g711[i] = 0xFF // μ-law zero level
	}
	writeTestG711MP4(t, rec.FilePath, g711)
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-audio-1/audio.wav", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "audio/wav" {
		t.Fatalf("content type = %q", ct)
	}
	body, _ := io.ReadAll(rr.Body)
	if len(body) != 44+len(g711)*2 {
		t.Fatalf("WAV length = %d, want %d", len(body), 44+len(g711)*2)
	}
	if string(body[0:4]) != "RIFF" || string(body[8:12]) != "WAVE" {
		t.Fatalf("not a WAV header: %q", body[0:12])
	}
	if binary.LittleEndian.Uint16(body[22:24]) != 1 { // channels
		t.Fatalf("channels = %d", binary.LittleEndian.Uint16(body[22:24]))
	}
	if binary.LittleEndian.Uint32(body[24:28]) != 8000 { // sample rate
		t.Fatalf("rate = %d", binary.LittleEndian.Uint32(body[24:28]))
	}
	if binary.LittleEndian.Uint16(body[34:36]) != 16 { // bits
		t.Fatalf("bits = %d", binary.LittleEndian.Uint16(body[34:36]))
	}
	// μ-law 0xFF decodes to 0 linear.
	if s := binary.LittleEndian.Uint16(body[44:46]); s != 0 {
		t.Fatalf("first PCM sample = %d, want 0", s)
	}
}

func TestAudioWav_RejectsVideoRecording(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	seedRecording(t, db, makeRecording("rec-video", "cam-1", "h264", now, false))

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-video/audio.wav", nil, "", "")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestAudioWav_NotFound(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/nope/audio.wav", nil, "", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

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
	pcm := g711ToPCM([]byte{0xFF, 0xFF}, true, 8000)
	if len(pcm) != 4 {
		t.Fatalf("pcm length = %d", len(pcm))
	}
	if binary.LittleEndian.Uint16(pcm[0:2]) != 0 {
		t.Fatalf("μ-law 0xFF should decode to 0, got %d", binary.LittleEndian.Uint16(pcm[0:2]))
	}
}
