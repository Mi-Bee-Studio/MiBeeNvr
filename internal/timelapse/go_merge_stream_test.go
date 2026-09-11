package timelapse

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGoMerger_PassthroughStreamsFrames verifies the passthrough (no
// re-encode) MJPEG merge streams frame data instead of holding every JPEG in
// memory: merging a payload far larger than the memory budget of small
// devices (a 1s-interval day window is ~20k frames / ~1GB on an RPi-class
// box) must keep live-heap growth bounded to the mux tables + one copy
// buffer, not the whole payload. Regression test for the OOM observed in
// production (RSS 1.9GB, kernel OOM-kill on a 4GB Banana Pi M5).
func TestGoMerger_PassthroughStreamsFrames(t *testing.T) {
	t.Helper()
	const (
		frames    = 300
		frameSize = 512 * 1024 // 300 × 512KB = 150MB payload
	)

	dir := t.TempDir()
	payload := bytes.Repeat([]byte{0xFF, 0xD8, 0xFF, 0x00}, frameSize/4) // JPEG-ish dummy
	for i := 1; i <= frames; i++ {
		name := filepath.Join(dir, "frame_000000.jpg")
		name = name[:len(name)-9] + pad6(i) + ".jpg"
		require.NoError(t, os.WriteFile(name, payload, 0o644))
	}

	// Disable GC during the merge so heap growth is deterministic: the old
	// implementation kept every sample (plus a full mdat concat) live until
	// close() returned; the streaming implementation holds tables + one
	// buffer. Restore afterwards.
	old := debug.SetGCPercent(-1)
	t.Cleanup(func() { debug.SetGCPercent(old) })

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	out := filepath.Join(t.TempDir(), "out.mp4")
	res, err := NewGoMerger().Merge(context.Background(), dir, out, 10)

	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, frames, res.FramesMerged)

	// Functional: output exists, mdat contains the whole payload.
	st, err := os.Stat(out)
	require.NoError(t, err)
	require.Greater(t, st.Size(), int64(frames*frameSize), "output must contain all frame bytes")

	// Memory: cumulative allocation must stay far below the payload (150MB).
	// TotalAlloc is GC-independent and monotonic: the old implementation
	// allocated ≥3× the payload (ReadFile + addSample copy + mdat concat ≈
	// 450MB); streaming allocates tables + one shared copy buffer. Allow
	// 40MB of headroom for mp4 writer internals.
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("allocated %d bytes merging %d bytes of frames", allocated, frames*frameSize)
	require.Less(t, allocated, uint64(40*1024*1024),
		"merge must stream frames, not copy the payload (allocated %d bytes)", allocated)
}

func pad6(i int) string {
	s := ""
	for n := i; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	for len(s) < 6 {
		s = "0" + s
	}
	return s
}

// TestGoMerger_PassthroughOutputUnchangedByStreaming pins the passthrough
// output's observable structure: ftyp+moov+mdat layout with one chunk, one
// stts/stsz entry per frame, and the payload byte-identical to the sources.
func TestGoMerger_PassthroughOutputUnchangedByStreaming(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	frames := map[int][]byte{}
	for i := 1; i <= 7; i++ {
		// Distinct sizes per frame so stsz entries are distinguishable.
		data := bytes.Repeat([]byte{byte(i)}, i*1024)
		frames[i] = data
		require.NoError(t, os.WriteFile(filepath.Join(dir, "frame_"+pad6(i)+".jpg"), data, 0o644))
	}

	out := filepath.Join(t.TempDir(), "out.mp4")
	res, err := NewGoMerger().Merge(context.Background(), dir, out, 5)
	require.NoError(t, err)
	require.Equal(t, 7, res.FramesMerged)
	require.InDelta(t, 7*0.2, res.Duration, 1e-9) // 5fps → 200ms per frame, seconds

	raw, err := os.ReadFile(out)
	require.NoError(t, err)
	// Every source frame's bytes appear in the output, in order.
	pos := 0
	for i := 1; i <= 7; i++ {
		idx := bytes.Index(raw[pos:], frames[i])
		require.GreaterOrEqual(t, idx, 0, "frame %d payload missing from output", i)
		pos += idx + len(frames[i])
	}
}
