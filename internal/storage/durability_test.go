package storage

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// syncCallCounter wraps the Sync seam to count fsyncs per finalize.
func swapSyncCounter(t *testing.T) *atomic.Int64 {
	t.Helper()
	count := &atomic.Int64{}
	prev := syncFileFn
	syncFileFn = func(f *os.File) error {
		count.Add(1)
		return f.Sync()
	}
	t.Cleanup(func() { syncFileFn = prev })
	return count
}

// TestCloseSegment_StrictDefaultFsyncs: default durability is strict — every
// file-form finalize performs exactly one fsync (regression nail for #760:
// relaxed must be opt-in).
func TestCloseSegment_StrictDefaultFsyncs(t *testing.T) {
	syncs := swapSyncCounter(t)
	dir := t.TempDir()
	m, err := NewManager(dir)
	require.NoError(t, err)

	temp, final, err := m.CreateSegment("cam1", "h264")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(temp, []byte("payload"), 0o644))

	require.NoError(t, m.CloseSegment(temp, final))
	require.Equal(t, int64(1), syncs.Load(), "strict tier: exactly one fsync per finalize")
	require.FileExists(t, final)
}

// TestCloseSegment_RelaxedSkipsFsyncButKeepsAtomicity: relaxed tier skips the
// proactive fsync (page-cache flush via bufio Close is still mandatory) while
// the temp→final rename stays atomic and the content complete.
func TestCloseSegment_RelaxedSkipsFsyncButKeepsAtomicity(t *testing.T) {
	syncs := swapSyncCounter(t)
	dir := t.TempDir()
	m, err := NewManager(dir)
	require.NoError(t, err)
	m.SetDurability("relaxed")

	temp, final, err := m.CreateSegment("cam1", "h264")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(temp, []byte("payload-relaxed"), 0o644))

	require.NoError(t, m.CloseSegment(temp, final))
	require.Zero(t, syncs.Load(), "relaxed tier: no proactive fsync")
	require.FileExists(t, final, "rename still performed")
	b, err := os.ReadFile(final)
	require.NoError(t, err)
	require.Equal(t, "payload-relaxed", string(b), "content complete (userspace flush preserved)")
}

// TestCloseSegment_RelaxedRetainedFDBufferStillFlushed: a segment written
// through WriteFrame holds a coalescing buffer — relaxed may skip fsync but
// MUST still flush buffered bytes before close/rename, or frames silently
// vanish.
func TestCloseSegment_RelaxedRetainedFDBufferStillFlushed(t *testing.T) {
	syncs := swapSyncCounter(t)
	dir := t.TempDir()
	m, err := NewManager(dir)
	require.NoError(t, err)
	m.SetDurability("relaxed")

	temp, final, err := m.CreateSegment("cam1", "h264")
	require.NoError(t, err)
	_, err = m.WriteFrame(temp, make([]byte, 4096)) // below flush threshold → sits in buffer
	require.NoError(t, err)

	require.NoError(t, m.CloseSegment(temp, final))
	require.Zero(t, syncs.Load())
	b, err := os.ReadFile(final)
	require.NoError(t, err)
	require.Len(t, b, 4096, "buffered frame flushed even without fsync")
}

// TestCloseSegmentMerged_AlwaysStrict: merged/timelapse products are
// long-term assets — CloseSegmentMerged fsyncs regardless of the tier.
func TestCloseSegmentMerged_AlwaysStrict(t *testing.T) {
	syncs := swapSyncCounter(t)
	dir := t.TempDir()
	m, err := NewManager(dir)
	require.NoError(t, err)
	m.SetDurability("relaxed")

	temp, final, err := m.CreateSegment("cam1", "h264")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(temp, []byte("merged-output"), 0o644))

	require.NoError(t, m.CloseSegmentMerged(temp, final))
	require.Equal(t, int64(1), syncs.Load(), "merged finalize keeps fsync under relaxed tier")
	require.FileExists(t, final)
}

// TestCloseSegment_RelaxedDirFormSkipsDirSync: MJPEG dir-form finalize skips
// the directory fsync under relaxed but still renames.
func TestCloseSegment_RelaxedDirFormSkipsDirSync(t *testing.T) {
	syncs := swapSyncCounter(t)
	dir := t.TempDir()
	m, err := NewManager(dir)
	require.NoError(t, err)
	m.SetDurability("relaxed")

	temp, final, err := m.CreateSegment("cam1", "mjpeg")
	require.NoError(t, err)
	require.DirExists(t, temp)
	require.NoError(t, os.WriteFile(filepath.Join(temp, "20260913_010101.000.jpg"), []byte("j"), 0o644))

	require.NoError(t, m.CloseSegment(temp, final))
	require.Zero(t, syncs.Load())
	require.NoDirExists(t, temp)
	require.DirExists(t, final)
}
