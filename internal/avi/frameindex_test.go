package avi

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// buildTestAVI writes a small AVI with nFrames video frames (fake JPEGs) and
// interleaved G.711 audio via the production muxer.
func buildTestAVI(t *testing.T, dir, name string, nFrames int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m := NewMuxer(f, 640, 480, 8000, true)
	for i := range nFrames {
		jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte{byte(i)}, 64)...)
		jpeg = append(jpeg, 0xFF, 0xD9)
		if err := m.WriteVideo(jpeg, int64(i)*100000); err != nil {
			t.Fatalf("write frame %d: %v", i, err)
		}
		if err := m.WriteAudio(bytes.Repeat([]byte{0x55}, 1600), int64(i)*100000); err != nil {
			t.Fatalf("write audio %d: %v", i, err)
		}
	}
	if err := m.Close(); err != nil {
		t.Fatalf("close muxer: %v", err)
	}
	return path
}

func TestVideoFrameIndex(t *testing.T) {
	dir := t.TempDir()
	path := buildTestAVI(t, dir, "test.avi", 10)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	dmx, err := NewDemuxer(f)
	if err != nil {
		t.Fatalf("demuxer: %v", err)
	}
	entries, err := dmx.VideoFrameIndex()
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if len(entries) != 10 {
		t.Fatalf("want 10 video frames, got %d", len(entries))
	}
	for i, e := range entries {
		if e.Index != i {
			t.Fatalf("entry %d has index %d", i, e.Index)
		}
		if e.Offset <= 0 || e.Offset+int64(e.Size) > int64(len(raw)) {
			t.Fatalf("frame %d range [%d,%d) outside file (len %d)", i, e.Offset, e.Offset+int64(e.Size), len(raw))
		}
		payload := raw[e.Offset : e.Offset+int64(e.Size)]
		if payload[0] != 0xFF || payload[1] != 0xD8 || payload[len(payload)-2] != 0xFF || payload[len(payload)-1] != 0xD9 {
			t.Fatalf("frame %d payload is not the written JPEG (got % X ... % X)", i, payload[:4], payload[len(payload)-2:])
		}
		if payload[4] != byte(i) {
			t.Fatalf("frame %d payload contains frame %d's marker byte", i, payload[4])
		}
		wantPTS := int64(i) * int64(dmx.MicroSecPerFrame())
		if e.PTSUs != wantPTS {
			t.Fatalf("frame %d PTS = %d, want %d (µs/frame=%d)", i, e.PTSUs, wantPTS, dmx.MicroSecPerFrame())
		}
	}
}

func TestVideoFrameIndex_SingleFrame(t *testing.T) {
	dir := t.TempDir()
	path := buildTestAVI(t, dir, "one.avi", 1)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	dmx, err := NewDemuxer(f)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := dmx.VideoFrameIndex()
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 frame, got %d", len(entries))
	}
}
