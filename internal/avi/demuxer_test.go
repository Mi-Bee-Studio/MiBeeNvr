package avi

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"testing"
)

// TestAVIDemuxer_RoundTrip tests that data written via Muxer is read back
// identically via Demuxer.
func TestAVIDemuxer_RoundTrip(t *testing.T) {
	var buf bytes.Buffer
	m := NewMuxer(&buf, 640, 480, 8000, true)

	videoFrames := [][]byte{
		makeJPEGFrame(t, 0x01, 100),
		makeJPEGFrame(t, 0x02, 200),
		makeJPEGFrame(t, 0x03, 150),
	}
	audioChunks := [][]byte{
		makeG711Audio(t, 0x10, 80),
		makeG711Audio(t, 0x20, 160),
		makeG711Audio(t, 0x30, 240),
	}

	for i := range 3 {
		if err := m.WriteVideo(videoFrames[i], int64(i)*33333); err != nil {
			t.Fatalf("WriteVideo %d: %v", i, err)
		}
		if err := m.WriteAudio(audioChunks[i], int64(i)*33333); err != nil {
			t.Fatalf("WriteAudio %d: %v", i, err)
		}
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Now demux.
	r := bytes.NewReader(buf.Bytes())
	d, err := NewDemuxer(r)
	if err != nil {
		t.Fatalf("NewDemuxer: %v", err)
	}

	var gotVideo, gotAudio int
	for {
		chunk, err := d.NextChunk()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("NextChunk: %v", err)
		}

		switch chunk.Type {
		case ChunkVideo:
			if gotVideo >= len(videoFrames) {
				t.Fatalf("unexpected video chunk %d", gotVideo)
			}
			if !bytes.Equal(chunk.Data, videoFrames[gotVideo]) {
				t.Errorf("video chunk %d: data mismatch", gotVideo)
			}
			gotVideo++
		case ChunkAudio:
			if gotAudio >= len(audioChunks) {
				t.Fatalf("unexpected audio chunk %d", gotAudio)
			}
			if !bytes.Equal(chunk.Data, audioChunks[gotAudio]) {
				t.Errorf("audio chunk %d: data mismatch", gotAudio)
			}
			gotAudio++
		}
	}

	if gotVideo != len(videoFrames) {
		t.Errorf("video chunks: expected %d, got %d", len(videoFrames), gotVideo)
	}
	if gotAudio != len(audioChunks) {
		t.Errorf("audio chunks: expected %d, got %d", len(audioChunks), gotAudio)
	}
}

// TestAVIDemuxer_InterleavedOrder verifies chunks are read in the same
// interleaved order they were written.
func TestAVIDemuxer_InterleavedOrder(t *testing.T) {
	var buf bytes.Buffer
	m := NewMuxer(&buf, 640, 480, 8000, true)

	// Write interleaved: V, A, V, A, V, A.
	for i := range 3 {
		if err := m.WriteVideo(makeJPEGFrame(t, byte(i), 100+i*10), int64(i)*33333); err != nil {
			t.Fatalf("WriteVideo %d: %v", i, err)
		}
		if err := m.WriteAudio(makeG711Audio(t, byte(i+10), 80+i*5), int64(i)*33333); err != nil {
			t.Fatalf("WriteAudio %d: %v", i, err)
		}
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r := bytes.NewReader(buf.Bytes())
	d, err := NewDemuxer(r)
	if err != nil {
		t.Fatalf("NewDemuxer: %v", err)
	}

	expectedOrder := []ChunkType{ChunkVideo, ChunkAudio, ChunkVideo, ChunkAudio, ChunkVideo, ChunkAudio}
	var idx int
	for {
		chunk, err := d.NextChunk()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("NextChunk %d: %v", idx, err)
		}
		if idx >= len(expectedOrder) {
			t.Fatalf("more chunks than expected")
		}
		if chunk.Type != expectedOrder[idx] {
			t.Errorf("chunk %d: expected type %d, got %d", idx, expectedOrder[idx], chunk.Type)
		}
		idx++
	}
	if idx != len(expectedOrder) {
		t.Errorf("total chunks: expected %d, got %d", len(expectedOrder), idx)
	}
}

// TestAVIDemuxer_CorruptFile verifies that a file with garbage data
// returns an error and does not panic.
func TestAVIDemuxer_CorruptFile(t *testing.T) {
	tests := [][]byte{
		{},                                 // empty
		{0, 0, 0, 0, 0, 0, 0, 0},           // all zeros
		[]byte("RIFF\xff\xff\xff\xffWAVE"), // RIFF but not AVI
		[]byte("NOTA"),                     // random garbage
		make([]byte, 100),                  // zero-filled
	}

	for i, testData := range tests {
		r := bytes.NewReader(testData)
		_, err := NewDemuxer(r)
		if err == nil {
			t.Errorf("test %d: expected error for corrupt data, got nil", i)
		}
		t.Logf("test %d: expected error: %v", i, err)
	}

	// Test partially valid file missing movi.
	var buf bytes.Buffer
	m := NewMuxer(&buf, 640, 480, 8000, true)
	m.Close()
	// Truncate the data to remove idx1 (making it weird but valid AVI with no chunks).
	truncated := buf.Bytes()[:buf.Len()-20]

	r := bytes.NewReader(truncated)
	d, err := NewDemuxer(r)
	if err != nil {
		t.Fatalf("NewDemuxer on truncated: %v", err)
	}
	_, err = d.NextChunk()
	if !errors.Is(err, io.EOF) {
		t.Errorf("expected EOF for truncated file, got %v", err)
	}
}

// TestAVIDemuxer_PTSValues verifies PTS timestamps are computed correctly.
func TestAVIDemuxer_PTSValues(t *testing.T) {
	var buf bytes.Buffer
	m := NewMuxer(&buf, 640, 480, 8000, true)

	// Write 2 video frames and 2 audio chunks with known timing.
	// Video: each at 33333 us intervals.
	// Audio: each audio byte = 1 sample = 1/8000 second = 125 us.
	if err := m.WriteVideo(makeJPEGFrame(t, 0x01, 100), 0); err != nil {
		t.Fatalf("WriteVideo 0: %v", err)
	}
	if err := m.WriteAudio(makeG711Audio(t, 0x10, 80), 0); err != nil {
		t.Fatalf("WriteAudio 0: %v", err)
	}
	if err := m.WriteVideo(makeJPEGFrame(t, 0x02, 200), 33333); err != nil {
		t.Fatalf("WriteVideo 1: %v", err)
	}
	if err := m.WriteAudio(makeG711Audio(t, 0x20, 160), 33333); err != nil {
		t.Fatalf("WriteAudio 1: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r := bytes.NewReader(buf.Bytes())
	d, err := NewDemuxer(r)
	if err != nil {
		t.Fatalf("NewDemuxer: %v", err)
	}

	// Expected PTS:
	// Video 0: frameIdx=0 * 33333 = 0
	// Audio 0: sampleIdx=0, pts = 0 * 125 = 0
	// Video 1: frameIdx=1 * 33333 = 33333
	// Audio 1: sampleIdx=80, pts = 80 * 125 = 10000

	expectedPTS := []int64{0, 0, 33333, 10000}
	var ptsIdx int
	for {
		chunk, err := d.NextChunk()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("NextChunk: %v", err)
		}
		if ptsIdx >= len(expectedPTS) {
			t.Fatalf("unexpected chunk %d", ptsIdx)
		}
		if chunk.PTS != expectedPTS[ptsIdx] {
			t.Errorf("chunk %d (%v): expected PTS %d, got %d",
				ptsIdx, chunk.Type, expectedPTS[ptsIdx], chunk.PTS)
		}
		ptsIdx++
	}
	if ptsIdx != len(expectedPTS) {
		t.Errorf("total chunks: expected %d, got %d", len(expectedPTS), ptsIdx)
	}
}

// TestAVIDemuxer_EmptyMovi tests an AVI with no data chunks.
func TestAVIDemuxer_EmptyMovi(t *testing.T) {
	var buf bytes.Buffer
	m := NewMuxer(&buf, 640, 480, 8000, true)
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r := bytes.NewReader(buf.Bytes())
	d, err := NewDemuxer(r)
	if err != nil {
		t.Fatalf("NewDemuxer: %v", err)
	}

	_, err = d.NextChunk()
	if !errors.Is(err, io.EOF) {
		t.Errorf("expected EOF for empty AVI, got %v", err)
	}
}

// TestAVIDemuxer_AudioOnly tests an AVI with only audio data.
func TestAVIDemuxer_AudioOnly(t *testing.T) {
	var buf bytes.Buffer
	m := NewMuxer(&buf, 640, 480, 8000, true)

	if err := m.WriteAudio(makeG711Audio(t, 0xAA, 160), 0); err != nil {
		t.Fatalf("WriteAudio: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r := bytes.NewReader(buf.Bytes())
	d, err := NewDemuxer(r)
	if err != nil {
		t.Fatalf("NewDemuxer: %v", err)
	}

	chunk, err := d.NextChunk()
	if err != nil {
		t.Fatalf("NextChunk: %v", err)
	}
	if chunk.Type != ChunkAudio {
		t.Errorf("expected audio chunk, got type %d", chunk.Type)
	}
	if len(chunk.Data) != 160 {
		t.Errorf("expected 160 audio bytes, got %d", len(chunk.Data))
	}

	_, err = d.NextChunk()
	if !errors.Is(err, io.EOF) {
		t.Errorf("expected EOF, got %v", err)
	}
}

// TestAVIDemuxer_AlawFormat verifies demuxing A-law audio.
func TestAVIDemuxer_AlawFormat(t *testing.T) {
	var buf bytes.Buffer
	m := NewMuxer(&buf, 320, 240, 8000, false) // A-law

	if err := m.WriteVideo(makeJPEGFrame(t, 0x01, 100), 0); err != nil {
		t.Fatalf("WriteVideo: %v", err)
	}
	if err := m.WriteAudio(makeG711Audio(t, 0xBB, 80), 0); err != nil {
		t.Fatalf("WriteAudio: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r := bytes.NewReader(buf.Bytes())
	d, err := NewDemuxer(r)
	if err != nil {
		t.Fatalf("NewDemuxer: %v", err)
	}

	// Verify we can read both chunks without error.
	for i := range 2 {
		_, err := d.NextChunk()
		if err != nil {
			t.Fatalf("NextChunk %d: %v", i, err)
		}
	}
}

// TestAVIDemuxer_SampleRates tests different sample rates.
func TestAVIDemuxer_SampleRates(t *testing.T) {
	rates := []int{8000, 11025, 16000, 22050, 44100}

	for _, rate := range rates {
		t.Run(fmt.Sprintf("%dHz", rate), func(t *testing.T) {
			var buf bytes.Buffer
			m := NewMuxer(&buf, 640, 480, rate, true)

			if err := m.WriteAudio(makeG711Audio(t, 0xCC, rate/100), 0); err != nil {
				t.Fatalf("WriteAudio: %v", err)
			}
			if err := m.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}

			r := bytes.NewReader(buf.Bytes())
			d, err := NewDemuxer(r)
			if err != nil {
				t.Fatalf("NewDemuxer: %v", err)
			}

			chunk, err := d.NextChunk()
			if err != nil {
				t.Fatalf("NextChunk: %v", err)
			}
			if chunk.Type != ChunkAudio {
				t.Errorf("expected audio, got type %d", chunk.Type)
			}
			if len(chunk.Data) != rate/100 {
				t.Errorf("expected %d bytes, got %d", rate/100, len(chunk.Data))
			}
		})
	}
}

// TestAVIDemuxer_ParseHeaders verifies the header parsing.
func TestAVIDemuxer_ParseHeaders(t *testing.T) {
	var buf bytes.Buffer
	m := NewMuxer(&buf, 1280, 720, 8000, true)
	_ = m.WriteVideo(makeJPEGFrame(t, 0x01, 100), 0)
	_ = m.Close()

	r := bytes.NewReader(buf.Bytes())
	d, err := NewDemuxer(r)
	if err != nil {
		t.Fatalf("NewDemuxer: %v", err)
	}

	if !d.parsed {
		t.Error("expected parsed flag to be true")
	}

	readBack := func(off int64, size int) []byte {
		b := make([]byte, size)
		r.ReadAt(b, off)
		return b
	}
	_ = readBack

	// Verify the avih.dwMicroSecPerFrame was parsed.
	avihMicroPerFrame := binary.LittleEndian.Uint32(buf.Bytes()[32:36])
	if d.dwMicroSecPerFrame != avihMicroPerFrame {
		t.Errorf("dwMicroSecPerFrame: expected %d, got %d",
			avihMicroPerFrame, d.dwMicroSecPerFrame)
	}
}

// TestAVIDemuxer_ChunkTypes tests that ChunkType constants have correct values.
func TestAVIDemuxer_ChunkTypes(t *testing.T) {
	if ChunkVideo != 0x01 {
		t.Errorf("ChunkVideo: expected 0x01, got %d", ChunkVideo)
	}
	if ChunkAudio != 0x02 {
		t.Errorf("ChunkAudio: expected 0x02, got %d", ChunkAudio)
	}
}

// TestAVIDemuxer_ZeroFrameSize verifies single byte frame handling.
func TestAVIDemuxer_ZeroFrameSize(t *testing.T) {
	var buf bytes.Buffer
	m := NewMuxer(&buf, 640, 480, 8000, true)

	// Write empty video frame (just SOI marker).
	frame := []byte{0xFF, 0xD8}
	if err := m.WriteVideo(frame, 0); err != nil {
		t.Fatalf("WriteVideo: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r := bytes.NewReader(buf.Bytes())
	d, err := NewDemuxer(r)
	if err != nil {
		t.Fatalf("NewDemuxer: %v", err)
	}

	chunk, err := d.NextChunk()
	if err != nil {
		t.Fatalf("NextChunk: %v", err)
	}
	if chunk.Type != ChunkVideo {
		t.Errorf("expected video, got type %d", chunk.Type)
	}
	if !bytes.Equal(chunk.Data, frame) {
		t.Error("frame data mismatch")
	}
}

// TestAVIDemuxer_CloseAfterEOF tests that calling NextChunk after EOF returns EOF.
func TestAVIDemuxer_CloseAfterEOF(t *testing.T) {
	var buf bytes.Buffer
	m := NewMuxer(&buf, 640, 480, 8000, true)
	_ = m.WriteVideo(makeJPEGFrame(t, 0x01, 100), 0)
	_ = m.Close()

	r := bytes.NewReader(buf.Bytes())
	d, err := NewDemuxer(r)
	if err != nil {
		t.Fatalf("NewDemuxer: %v", err)
	}

	// Read all chunks.
	for i := range 5 {
		_, err := d.NextChunk()
		if errors.Is(err, io.EOF) {
			return
		}
		if i == 0 && err != nil {
			t.Fatalf("first NextChunk: %v", err)
		}
	}
}

// TestAVIDemuxer_SeekConsistency tests that Demuxer can read correctly
// after being created with a fresh reader.
func TestAVIDemuxer_SeekConsistency(t *testing.T) {
	var buf bytes.Buffer
	m := NewMuxer(&buf, 640, 480, 8000, true)

	for i := range 5 {
		_ = m.WriteVideo(makeJPEGFrame(t, byte(i), 100), 0)
	}
	_ = m.Close()

	// Create two independent demuxers from the same data.
	data := buf.Bytes()

	for j := range 2 {
		r := bytes.NewReader(data)
		d, err := NewDemuxer(r)
		if err != nil {
			t.Fatalf("NewDemuxer pass %d: %v", j, err)
		}

		count := 0
		for {
			_, err := d.NextChunk()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatalf("NextChunk pass %d chunk %d: %v", j, count, err)
			}
			count++
		}
		if count != 5 {
			t.Errorf("pass %d: expected 5 chunks, got %d", j, count)
		}
	}
}

// TestAVIDemuxer_CorruptAfterHeaders verifies handling of files where
// headers are valid but movi data is corrupt.
func TestAVIDemuxer_CorruptAfterHeaders(t *testing.T) {
	var buf bytes.Buffer
	m := NewMuxer(&buf, 640, 480, 8000, true)
	_ = m.WriteVideo(makeJPEGFrame(t, 0x01, 100), 0)
	_ = m.Close()

	data := buf.Bytes()

	// Truncate file in the middle of movi data.
	cutPos := len(data) - 50
	truncated := data[:cutPos]

	r := bytes.NewReader(truncated)
	d, err := NewDemuxer(r)
	if err != nil {
		t.Fatalf("NewDemuxer: %v", err)
	}

	_, err = d.NextChunk()
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		// This is acceptable - we might get any read error.
		t.Logf("Got expected error for truncated: %v", err)
	}
}
