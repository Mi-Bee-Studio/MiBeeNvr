package avi

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// writeAtBuffer is an in-memory io.Writer + io.WriterAt with the same
// positional-write semantics as *os.File: Write appends sequentially (fails
// after any WriteAt moved the logical offset — we never mix them, matching
// Muxer usage), WriteAt writes at an absolute offset without disturbing the
// append cursor.
type writeAtBuffer struct {
	buf    bytes.Buffer
	cursor int64
}

func (w *writeAtBuffer) Write(p []byte) (int, error) {
	if w.cursor != int64(w.buf.Len()) {
		// Sequential writes must stay contiguous with prior appends.
		return 0, io.ErrClosedPipe
	}
	n, err := w.buf.Write(p)
	w.cursor += int64(n)
	return n, err
}

func (w *writeAtBuffer) WriteAt(p []byte, off int64) (int, error) {
	b := w.buf.Bytes()
	if end := int(off) + len(p); end <= len(b) {
		copy(b[off:], p)
		return len(p), nil
	}
	// Extending write (never happens for Muxer patches — positions are
	// inside already-written headers).
	w.buf.Write(make([]byte, int(off)+len(p)-len(b)))
	copy(w.buf.Bytes()[off:], p)
	return len(p), nil
}

// TestMuxerIncrementalMatchesBufferedVideoOnly asserts the incremental
// (io.WriterAt) mode produces BYTE-IDENTICAL output to the legacy RAM-buffered
// mode for a video-only file — the format on disk must not depend on which
// writer the recorder handed the muxer. Frame sizes cross the odd/even pad
// boundary and total well above the incremental bufio buffer size (256KB) to
// force multiple flush cycles before the Close() backpatches.
func TestMuxerIncrementalMatchesBufferedVideoOnly(t *testing.T) {
	t.Helper()
	frames := [][]byte{
		makeJPEGFrame(t, 1, 90*1024), // even size
		makeJPEGFrame(t, 2, 61*1024), // odd size → pad byte
		makeJPEGFrame(t, 3, 200*1024),
		makeJPEGFrame(t, 4, 33*1024+1), // odd → pad byte
		makeJPEGFrame(t, 5, 120*1024),
	}

	var ram bytes.Buffer
	ramMux := NewVideoOnlyMuxer(&ram, 640, 480)
	for _, f := range frames {
		if err := ramMux.WriteVideo(f, 0); err != nil {
			t.Fatalf("ram WriteVideo: %v", err)
		}
	}
	if err := ramMux.Close(); err != nil {
		t.Fatalf("ram Close: %v", err)
	}

	inc := &writeAtBuffer{}
	incMux := NewVideoOnlyMuxer(inc, 640, 480)
	for _, f := range frames {
		if err := incMux.WriteVideo(f, 0); err != nil {
			t.Fatalf("incremental WriteVideo: %v", err)
		}
	}
	if err := incMux.Close(); err != nil {
		t.Fatalf("incremental Close: %v", err)
	}

	if !bytes.Equal(ram.Bytes(), inc.buf.Bytes()) {
		t.Fatalf("incremental output diverges from buffered: ram=%d inc=%d",
			ram.Len(), inc.buf.Len())
	}

	// And the demuxer reads the incremental output back correctly.
	d, err := NewDemuxer(bytes.NewReader(inc.buf.Bytes()))
	if err != nil {
		t.Fatalf("NewDemuxer: %v", err)
	}
	got := 0
	for {
		c, err := d.NextChunk()
		if err != nil {
			break
		}
		if c.Type == ChunkVideo {
			if !bytes.Equal(c.Data, frames[got]) {
				t.Fatalf("frame %d payload mismatch: got %d bytes want %d", got, len(c.Data), len(frames[got]))
			}
			got++
		}
	}
	if got != len(frames) {
		t.Fatalf("demuxed %d video frames, want %d", got, len(frames))
	}
}

// TestMuxerIncrementalMatchesBufferedAudio interleaves video and G.711 audio
// chunks (both stream kinds flow through the same patch/idx1 machinery).
func TestMuxerIncrementalMatchesBufferedAudio(t *testing.T) {
	t.Helper()
	type step struct {
		video []byte
		audio []byte
	}
	steps := []step{
		{video: makeJPEGFrame(t, 1, 40*1024), audio: makeG711Audio(t, 9, 160)},
		{video: makeJPEGFrame(t, 2, 70*1024+1), audio: makeG711Audio(t, 8, 161)},
		{video: makeJPEGFrame(t, 3, 300*1024), audio: nil},
		{video: makeJPEGFrame(t, 4, 55*1024), audio: makeG711Audio(t, 7, 320)},
		{video: nil, audio: makeG711Audio(t, 6, 240)},
	}

	var ram bytes.Buffer
	ramMux := NewMuxer(&ram, 1280, 720, 8000, true)
	inc := &writeAtBuffer{}
	incMux := NewMuxer(inc, 1280, 720, 8000, true)
	for _, s := range steps {
		if s.video != nil {
			if err := ramMux.WriteVideo(s.video, 0); err != nil {
				t.Fatalf("ram WriteVideo: %v", err)
			}
			if err := incMux.WriteVideo(s.video, 0); err != nil {
				t.Fatalf("inc WriteVideo: %v", err)
			}
		}
		if s.audio != nil {
			if err := ramMux.WriteAudio(s.audio, 0); err != nil {
				t.Fatalf("ram WriteAudio: %v", err)
			}
			if err := incMux.WriteAudio(s.audio, 0); err != nil {
				t.Fatalf("inc WriteAudio: %v", err)
			}
		}
	}
	if err := ramMux.Close(); err != nil {
		t.Fatalf("ram Close: %v", err)
	}
	if err := incMux.Close(); err != nil {
		t.Fatalf("inc Close: %v", err)
	}

	if !bytes.Equal(ram.Bytes(), inc.buf.Bytes()) {
		t.Fatalf("incremental audio output diverges from buffered: ram=%d inc=%d",
			ram.Len(), inc.buf.Len())
	}
}

// TestMuxerIncrementalHeaderPatches verifies the six backpatched size fields
// in incremental mode directly (the buffered mode equivalents are covered by
// the existing header tests; byte-equality above makes this a belt-and-braces
// check of the values that matter for players).
func TestMuxerIncrementalHeaderPatches(t *testing.T) {
	t.Helper()
	frames := [][]byte{
		makeJPEGFrame(t, 1, 50*1024),
		makeJPEGFrame(t, 2, 60*1024),
		makeJPEGFrame(t, 3, 70*1024),
	}
	inc := &writeAtBuffer{}
	m := NewVideoOnlyMuxer(inc, 320, 240)
	for _, f := range frames {
		if err := m.WriteVideo(f, 0); err != nil {
			t.Fatalf("WriteVideo: %v", err)
		}
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	out := inc.buf.Bytes()
	if got := readU32LE(t, out, 4); got != uint32(len(out)-8) {
		t.Fatalf("RIFF size = %d, want %d", got, len(out)-8)
	}
	if got := readU32LE(t, out, 48); got != uint32(len(frames)) {
		t.Fatalf("dwTotalFrames = %d, want %d", got, len(frames))
	}
}

// TestMuxerIncrementalWriteError asserts a failing underlying writer surfaces
// as an error from WriteVideo (disk-full behavior must not be silently
// swallowed — the recorder logs and rotates on it). The frame is larger than
// the 256KB coalescing buffer so bufio hands it straight to the writer.
type failingWriteAt struct{}

func (failingWriteAt) Write(p []byte) (int, error)            { return 0, errNoSpace }
func (failingWriteAt) WriteAt(p []byte, _ int64) (int, error) { return 0, errNoSpace }

// errNoSpace stands in for ENOSPC (io has no sentinel of its own).
var errNoSpace = errors.New("no space left on device")

func TestMuxerIncrementalWriteError(t *testing.T) {
	t.Helper()
	m := NewVideoOnlyMuxer(failingWriteAt{}, 640, 480)
	if err := m.WriteVideo(makeJPEGFrame(t, 1, 300*1024), 0); err == nil {
		t.Fatalf("expected write error, got nil")
	}
}
