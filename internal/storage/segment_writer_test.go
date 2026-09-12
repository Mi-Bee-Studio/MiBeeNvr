package storage

// Tests for #750 — per-segment writer state:
//   - segment-lifetime FD retention on the append path (one open per segment,
//     not one per frame)
//   - write coalescing with size-threshold flush + mandatory flush at
//     CloseSegment (byte-equivalent output)
//   - registration-time classification (no per-frame os.Stat)
//   - per-segment locking (cross-camera writes no longer serialize on one
//     global mutex)
//   - state lifecycle: released at CloseSegment, vanished temps stay benign
//     (#413 semantics), writes after close fail
//
// Syscall-count assertions use injectable func-field seams (statFn /
// openFileFn), per the project's test-seam convention.

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// swapStatFn replaces the package stat seam for the duration of the test.
func swapStatFn(t *testing.T, fn func(string) (fs.FileInfo, error)) {
	t.Helper()
	old := statFn
	statFn = fn
	t.Cleanup(func() { statFn = old })
}

// swapOpenFileFn replaces the package open-file seam for the duration of the test.
func swapOpenFileFn(t *testing.T, fn func(name string, flag int, perm os.FileMode) (*os.File, error)) {
	t.Helper()
	old := openFileFn
	openFileFn = fn
	t.Cleanup(func() { openFileFn = old })
}

// countingStat wraps os.Stat with a counter (nil fn → plain os.Stat).
func countingStat(counter *atomic.Int64) func(string) (fs.FileInfo, error) {
	return func(name string) (fs.FileInfo, error) {
		counter.Add(1)
		return os.Stat(name)
	}
}

// countingOpenFile wraps os.OpenFile with a counter.
func countingOpenFile(counter *atomic.Int64) func(string, int, os.FileMode) (*os.File, error) {
	return func(name string, flag int, perm os.FileMode) (*os.File, error) {
		counter.Add(1)
		return os.OpenFile(name, flag, perm)
	}
}

// TestWriteFrame_AppendRetainsSegmentFD is the core #750 red test: N frames
// through the append path must ride exactly ONE lazy open (held for the
// segment's lifetime), and CloseSegment must fsync through that retained FD
// instead of reopening the temp by path.
func TestWriteFrame_AppendRetainsSegmentFD(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	require.NoError(t, err)

	var opens atomic.Int64
	swapOpenFileFn(t, countingOpenFile(&opens))

	temp, final, err := m.CreateSegment("cam-fd", "h264")
	require.NoError(t, err)
	base := opens.Load() // CreateSegment's own create is not on the seam

	var want bytes.Buffer
	for i := range 10 {
		frame := fmt.Sprintf("frame-%02d;", i)
		want.WriteString(frame)
		n, werr := m.WriteFrame(temp, []byte(frame))
		require.NoError(t, werr)
		require.Positive(t, n)
	}
	require.Equal(t, int64(1), opens.Load()-base,
		"10 frames must ride one retained FD — not open/close per frame")

	require.NoError(t, m.CloseSegment(temp, final))
	require.Equal(t, int64(1), opens.Load()-base,
		"CloseSegment must fsync via the retained FD, not reopen the temp by path")

	got, rerr := os.ReadFile(final)
	require.NoError(t, rerr)
	require.Equal(t, want.String(), string(got))
}

// TestWriteFrame_RegisteredDirSegmentSkipsStat: segments created via
// CreateSegment have their form (dir vs file) registered at creation, so the
// per-frame write path must not stat the temp at all — the old path resolved
// the full directory chain once per frame.
func TestWriteFrame_RegisteredDirSegmentSkipsStat(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	require.NoError(t, err)

	var stats atomic.Int64
	swapStatFn(t, countingStat(&stats))

	temp, final, err := m.CreateSegment("cam-stat", "mjpeg")
	require.NoError(t, err)
	base := stats.Load()

	for range 8 {
		_, werr := m.WriteFrame(temp, []byte("jpeg-bytes"))
		require.NoError(t, werr)
	}
	require.NoError(t, m.CloseSegment(temp, final))
	require.Equal(t, int64(0), stats.Load()-base,
		"registered segments must not stat the temp per frame nor at close")

	entries, lerr := os.ReadDir(final)
	require.NoError(t, lerr)
	require.Len(t, entries, 8)
}

// TestSegmentStateReleasedAfterClose: the writer state (and its FD) must be
// dropped when the segment finalizes — across rotation, camera removal, and
// recording toggles, states are bounded by in-flight segments.
func TestSegmentStateReleasedAfterClose(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	require.NoError(t, err)

	temp, final, err := m.CreateSegment("cam-rel", "h264")
	require.NoError(t, err)
	_, werr := m.WriteFrame(temp, []byte("payload"))
	require.NoError(t, werr)
	require.Equal(t, 1, m.segmentStateCount())

	require.NoError(t, m.CloseSegment(temp, final))
	require.Zero(t, m.segmentStateCount(), "state must be released at CloseSegment")

	// Write-after-close must fail (state resurrected lazily → path is gone).
	_, werr = m.WriteFrame(temp, []byte("more"))
	require.Error(t, werr)
}

// TestCloseSegment_FlushesBufferedAppendData: sub-threshold frames sit in the
// userspace buffer until CloseSegment flushes them — the finalized file must
// be byte-identical to the unbuffered output.
func TestCloseSegment_FlushesBufferedAppendData(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	require.NoError(t, err)

	temp, final, err := m.CreateSegment("cam-flush", "h264")
	require.NoError(t, err)

	var want bytes.Buffer
	for i, sz := range []int{1000, 2000, 500} {
		chunk := bytes.Repeat([]byte{byte('a' + i)}, sz)
		want.Write(chunk)
		_, werr := m.WriteFrame(temp, chunk)
		require.NoError(t, werr)
	}
	require.NoError(t, m.CloseSegment(temp, final))

	got, rerr := os.ReadFile(final)
	require.NoError(t, rerr)
	require.Equal(t, want.Bytes(), got)
}

// TestWriteFrame_FlushesAtThreshold: buffered bytes must hit the kernel once
// the coalescing threshold is crossed, not only at CloseSegment — bounded
// loss window on crash, bounded memory per segment.
func TestWriteFrame_FlushesAtThreshold(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	require.NoError(t, err)

	temp, final, err := m.CreateSegment("cam-thr", "h264")
	require.NoError(t, err)

	_, werr := m.WriteFrame(temp, bytes.Repeat([]byte{1}, 100_000))
	require.NoError(t, werr)
	info1, serr := os.Stat(temp)
	require.NoError(t, serr)

	// 300KB buffered total ≥ threshold → the third write must have flushed.
	_, werr = m.WriteFrame(temp, bytes.Repeat([]byte{2}, 100_000))
	require.NoError(t, werr)
	_, werr = m.WriteFrame(temp, bytes.Repeat([]byte{3}, 100_000))
	require.NoError(t, werr)
	info2, serr := os.Stat(temp)
	require.NoError(t, serr)

	require.Greater(t, info2.Size(), info1.Size(),
		"bytes must reach the file once the flush threshold is crossed")
	require.GreaterOrEqual(t, info2.Size(), int64(300_000))

	require.NoError(t, m.CloseSegment(temp, final))
	got, rerr := os.ReadFile(final)
	require.NoError(t, rerr)
	require.Len(t, got, 300_000)
}

// TestWriteFrame_VanishedTempStaysBenign: a temp removed under us (stale
// cleanup, rotation race) must stay a benign frame-drop — NOT a storage
// health failure (#413) — for both dir-form and file-form segments.
func TestWriteFrame_VanishedTempStaysBenign(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	require.NoError(t, err)

	dirTemp, _, err := m.CreateSegment("cam-van-dir", "mjpeg")
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(dirTemp))
	_, werr := m.WriteFrame(dirTemp, []byte("j"))
	require.ErrorIs(t, werr, fs.ErrNotExist)
	require.False(t, m.StorageFailed("cam-van-dir"), "vanished temp must not count as an I/O failure")

	fileTemp, _, err := m.CreateSegment("cam-van-file", "h264")
	require.NoError(t, err)
	require.NoError(t, os.Remove(fileTemp))
	_, werr = m.WriteFrame(fileTemp, []byte("j"))
	require.ErrorIs(t, werr, fs.ErrNotExist)
	require.False(t, m.StorageFailed("cam-van-file"), "vanished temp must not count as an I/O failure")
}

// TestWriteFrame_ConcurrentAcrossSegments: frames from different segments
// (different cameras) written concurrently must all land intact — the sharded
// per-segment locks must be race-clean (run under -race).
func TestWriteFrame_ConcurrentAcrossSegments(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	require.NoError(t, err)

	const cams = 4
	const frames = 200

	temps := make([]string, cams)
	finals := make([]string, cams)
	for i := range cams {
		temps[i], finals[i], err = m.CreateSegment(fmt.Sprintf("cam-cc-%d", i), "mjpeg")
		require.NoError(t, err)
	}

	var wg sync.WaitGroup
	for i := range cams {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for f := range frames {
				payload := fmt.Sprintf("cam%d-frame%04d;", idx, f)
				_, werr := m.WriteFrame(temps[idx], []byte(payload))
				if werr != nil {
					t.Errorf("camera %d frame %d: %v", idx, f, werr)
					return
				}
			}
		}(i)
	}
	wg.Wait()

	for i := range cams {
		require.NoError(t, m.CloseSegment(temps[i], finals[i]))
	}
	for i := range cams {
		entries, lerr := os.ReadDir(finals[i])
		require.NoError(t, lerr)
		require.Len(t, entries, frames, "camera %d lost frames", i)
		for _, e := range entries {
			content, rerr := os.ReadFile(fmt.Sprintf("%s/%s", finals[i], e.Name()))
			require.NoError(t, rerr)
			require.True(t, bytes.HasPrefix(content, []byte(fmt.Sprintf("cam%d-frame", i))),
				"camera %d got another camera's frame: %q", i, content[:min(16, len(content))])
		}
	}
}

// TestWriteFrame_HealthAccountingPerCamera: successes route through the
// segment's cached camera attribution, and real I/O failures (non-ENOENT)
// still escalate storage health for the owning camera.
func TestWriteFrame_HealthAccountingPerCamera(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	require.NoError(t, err)

	okTemp, okFinal, err := m.CreateSegment("cam-health", "mjpeg")
	require.NoError(t, err)
	_, werr := m.WriteFrame(okTemp, []byte("j"))
	require.NoError(t, werr)
	require.Equal(t, HealthHealthy, m.StorageHealth("cam-health"))
	require.NoError(t, m.CloseSegment(okTemp, okFinal))

	// Real I/O failure on the append path: the lazy open returns a non-ENOENT
	// error → storage health for the OWNING camera must degrade.
	failTemp, _, err := m.CreateSegment("cam-health-fail", "h264")
	require.NoError(t, err)
	swapOpenFileFn(t, func(name string, flag int, perm os.FileMode) (*os.File, error) {
		return nil, &os.PathError{Op: "open", Path: name, Err: errors.New("io: host is down")}
	})
	_, werr = m.WriteFrame(failTemp, []byte("x"))
	require.Error(t, werr)
	require.False(t, errors.Is(werr, fs.ErrNotExist))
	require.NotEqual(t, HealthHealthy, m.StorageHealth("cam-health-fail"),
		"a real open failure must degrade the owning camera's storage health")

	// An unrelated camera stays healthy — failures are attributed per segment.
	require.Equal(t, HealthHealthy, m.StorageHealth("cam-health"))
}

// TestSegmentStateLazyClassification: temps registered externally (no form
// known — tierrec-style writers) get classified once on first WriteFrame.
func TestSegmentStateLazyClassification(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	require.NoError(t, err)

	external := filepath.Join(dir, "cam-lazy", "202601", "01", "02", "999.tmp")
	require.NoError(t, os.MkdirAll(external, 0o755))
	m.RegisterActiveTemp(external, "cam-lazy")

	var stats atomic.Int64
	swapStatFn(t, countingStat(&stats))
	base := stats.Load()

	for range 4 {
		_, werr := m.WriteFrame(external, []byte("j"))
		require.NoError(t, werr)
	}
	require.Equal(t, int64(1), stats.Load()-base,
		"unregistered temps classify ONCE on first write, not per frame")
	m.UnregisterActiveTemp(external)
	require.Zero(t, m.segmentStateCount())
}
