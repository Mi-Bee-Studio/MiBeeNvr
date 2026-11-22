package hls

// Retired-muxer grace-window coverage: a muxer rebuild (write-error recovery)
// changes gohlslib's random filename prefix and resets segment numbering, so
// names advertised by a playlist fetched before the error become unknown to
// the rebuilt muxer and used to surface as 404 — a fatal stream error for
// clients that do not retry (ExoPlayer: first 404 → Source error → playback
// dead). The tests pin the grace-window serving: while the rebuild waits for
// the next IDR, after the rebuild, playlist exclusion, expiry + reap, the
// stash cap, and teardown cleanup.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

// firstSegmentURI extracts the first real media URI from a media playlist
// body: non-comment, non-gap, query stripped. Empty when the playlist
// advertises nothing playable yet (pure gap padding).
func firstSegmentURI(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || line == gapPlaceholderName {
			continue
		}
		if i := strings.IndexByte(line, '?'); i >= 0 {
			line = line[:i]
		}
		return line
	}
	return ""
}

// pumpLLStream starts an LL-HLS stream on the manager and broadcasts frames
// through the hub until at least minSegs segment files exist on disk.
func pumpLLStream(t *testing.T, m *Manager, cameraID string, minSegs int) *streamEntry {
	t.Helper()
	require.NoError(t, m.StartStream(cameraID, pumpSPS, pumpPPS, 0))
	m.mu.RLock()
	entry := m.streams[cameraID]
	m.mu.RUnlock()
	require.NotNil(t, entry)

	hub := streamhub.New()
	require.NoError(t, m.SubscribeToHub(cameraID, hub, false))
	// 60 iterations = 120 messages, inside the hub consumer queue (150): a
	// bigger flood trips the queue-full IDR fast path, which delivers later
	// keyframes ahead of queued frames and makes the DTS extractor reject
	// them — an unplanned muxer rebuild that strands the playlist fetch.
	for i := range 60 {
		hub.Broadcast(int64(90000*i), [][]byte{pumpSPS, pumpPPS, pumpH264IDR}, true)
		hub.Broadcast(int64(90000*i+3000), [][]byte{pumpH264P}, false)
	}
	require.Eventually(t, func() bool { return len(segmentFiles(t, entry.dirPath)) >= minSegs },
		20*time.Second, 200*time.Millisecond, "never produced segments")

	// Let the async writer drain its queue and settle before returning:
	// retireMuxer below emulates handleWriteError from outside writeLoop,
	// which owns entry.mux/consecutiveErrors lock-free on its own goroutine —
	// a quiescent writer is what makes the emulation race-free.
	require.Eventually(t, func() bool { return len(entry.frameCh) == 0 },
		10*time.Second, 20*time.Millisecond, "frame queue never drained")
	time.Sleep(300 * time.Millisecond)
	return entry
}

// retireMuxer drives the REAL degraded-load failure through writeLoop: a
// frame whose DTS goes backwards makes gohlslib's DTS extractor reject the
// write ("DTS is not monotonically increasing"), and writeLoop's error path
// retires the muxer into the grace stash (NOT closed) and waits for the next
// IDR to rebuild. The whole failure runs on the writeLoop goroutine, so the
// test needs no synchronization with it.
func retireMuxer(t *testing.T, m *Manager, cameraID string, entry *streamEntry) {
	t.Helper()
	hub := streamhub.New()
	require.NoError(t, m.SubscribeToHub(cameraID, hub, false))
	// pts regression → DTS extractor error → writeLoop retires the muxer.
	hub.Broadcast(int64(1000), [][]byte{pumpH264P}, false)
	require.Eventually(t, func() bool {
		entry.mu.Lock()
		defer entry.mu.Unlock()
		return entry.mux == nil && len(entry.prevMuxers) > 0
	}, 10*time.Second, 20*time.Millisecond, "write error never retired the muxer")
}

// TestHandle_RetiredMuxerServesPreRebuildNames : the retired muxer must keep
// serving its window — while the rebuild waits for the next IDR (entry.mux
// nil) and after the rebuild (name miss on the new prefix) — and every
// fallback serve must land in the metrics counter.
func TestHandle_RetiredMuxerServesPreRebuildNames(t *testing.T) {
	t.Helper()
	mm := metrics.NewMetrics()
	// LL-HLS requires SegmentCount >= 7 (gohlslib validation, #772 auto-lift).
	m := NewManagerWithOpts(context.Background(), t.TempDir(), 64, 1<<20, 7, mm)
	t.Cleanup(m.StopAll)
	m.SetLowLatency(true, 200*time.Millisecond)

	entry := pumpLLStream(t, m, "cam-legacy", 2)

	// Capture a real segment name from the healthy playlist and verify it
	// serves while the muxer is intact.
	rec := httptest.NewRecorder()
	require.True(t, m.Handle("cam-legacy", rec,
		httptest.NewRequest(http.MethodGet, "/hls/cam-legacy/video1_stream.m3u8", nil)))
	require.Equal(t, http.StatusOK, rec.Code)
	oldSeg := firstSegmentURI(rec.Body.String())
	require.NotEmpty(t, oldSeg, "healthy playlist should advertise real segments")
	oldSegPath := "/hls/cam-legacy/" + oldSeg

	rec = httptest.NewRecorder()
	require.True(t, m.Handle("cam-legacy", rec, httptest.NewRequest(http.MethodGet, oldSegPath, nil)))
	require.Equal(t, http.StatusOK, rec.Code, "pre-error segment should serve while healthy")

	// Write error → retire; the old name must still serve from the stash
	// while the rebuild waits for the next IDR.
	retireMuxer(t, m, "cam-legacy", entry)

	rec = httptest.NewRecorder()
	require.True(t, m.Handle("cam-legacy", rec, httptest.NewRequest(http.MethodGet, oldSegPath, nil)))
	require.Equal(t, http.StatusOK, rec.Code, "retired muxer must serve pre-error names during the nil-muxer window")

	// Rebuild on a fresh prefix; the pre-rebuild name must STILL resolve.
	idrAU := [][]byte{pumpSPS, pumpPPS, pumpH264IDR}
	require.True(t, m.rebuildMuxer("cam-legacy", entry, idrAU))

	rec = httptest.NewRecorder()
	require.True(t, m.Handle("cam-legacy", rec, httptest.NewRequest(http.MethodGet, oldSegPath, nil)))
	require.Equal(t, http.StatusOK, rec.Code, "retired muxer must keep serving pre-rebuild names after the rebuild")

	// Every fallback serve is observable — the field-verification signal for
	// the whole mechanism.
	require.Greater(t, testutil.ToFloat64(mm.HLSRetiredMuxerServes.WithLabelValues("cam-legacy")), float64(0))

	// Unrelated unknown names still 404 (nothing serves them, stash included).
	rec = httptest.NewRecorder()
	require.True(t, m.Handle("cam-legacy", rec,
		httptest.NewRequest(http.MethodGet, "/hls/cam-legacy/definitely_missing.mp4", nil)))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

// TestHandle_RetiredMuxer_PlaylistNeverFallsBack : the media playlist name
// carries no random prefix, so it always resolves on the current muxer once
// rebuilt; while the rebuild waits for the next IDR the request must FAIL
// (caller 503s, client retries into fresh state) rather than pin the client
// to a retired playlist whose window never advances.
func TestHandle_RetiredMuxer_PlaylistNeverFallsBack(t *testing.T) {
	t.Helper()
	m := NewManagerWithOpts(context.Background(), t.TempDir(), 64, 1<<20, 7)
	t.Cleanup(m.StopAll)
	m.SetLowLatency(true, 200*time.Millisecond)

	entry := pumpLLStream(t, m, "cam-legacy", 1)
	retireMuxer(t, m, "cam-legacy", entry)

	rec := httptest.NewRecorder()
	require.False(t, m.Handle("cam-legacy", rec,
		httptest.NewRequest(http.MethodGet, "/hls/cam-legacy/video1_stream.m3u8", nil)),
		"playlist requests must not fall back to the retired muxer")
}

// TestHandle_RetiredMuxer_ExpiryStopsServingAndReaps : after the grace window
// the retired names 404 again (a live client has long moved to the new
// playlist by then), and the successful-write reap closes the expired muxer,
// deleting its segment files from disk.
func TestHandle_RetiredMuxer_ExpiryStopsServingAndReaps(t *testing.T) {
	t.Helper()
	m := NewManagerWithOpts(context.Background(), t.TempDir(), 64, 1<<20, 7)
	t.Cleanup(m.StopAll)
	m.SetLowLatency(true, 200*time.Millisecond)

	entry := pumpLLStream(t, m, "cam-legacy", 1)

	rec := httptest.NewRecorder()
	require.True(t, m.Handle("cam-legacy", rec,
		httptest.NewRequest(http.MethodGet, "/hls/cam-legacy/video1_stream.m3u8", nil)))
	require.Equal(t, http.StatusOK, rec.Code)
	oldSeg := firstSegmentURI(rec.Body.String())
	require.NotEmpty(t, oldSeg)
	oldSegPath := "/hls/cam-legacy/" + oldSeg

	retireMuxer(t, m, "cam-legacy", entry)
	idrAU := [][]byte{pumpSPS, pumpPPS, pumpH264IDR}
	require.True(t, m.rebuildMuxer("cam-legacy", entry, idrAU))

	// Force expiry, then confirm the fallback no longer serves the name.
	entry.mu.Lock()
	require.Len(t, entry.prevMuxers, 1)
	entry.prevMuxers[0].expires = time.Now().Add(-time.Second)
	entry.mu.Unlock()

	rec = httptest.NewRecorder()
	require.True(t, m.Handle("cam-legacy", rec, httptest.NewRequest(http.MethodGet, oldSegPath, nil)))
	require.Equal(t, http.StatusNotFound, rec.Code, "expired retired muxer must not serve")

	// Drive writeLoop with healthy frames — the successful-write reap closes
	// the expired stash, which deletes its segment files from disk. Same
	// pump bound as pumpLLStream: stay inside the hub consumer queue.
	hub := streamhub.New()
	require.NoError(t, m.SubscribeToHub("cam-legacy", hub, false))
	for i := range 60 {
		hub.Broadcast(int64(90000*i), [][]byte{pumpSPS, pumpPPS, pumpH264IDR}, true)
		hub.Broadcast(int64(90000*i+3000), [][]byte{pumpH264P}, false)
	}
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(entry.dirPath, oldSeg))
		return os.IsNotExist(err)
	}, 20*time.Second, 200*time.Millisecond, "expired retired muxer should be closed and its files removed")

	entry.mu.Lock()
	require.Empty(t, entry.prevMuxers, "reap must drop the expired stash")
	entry.mu.Unlock()
}

// TestStashRetiredMuxer_CapBoundsStashes : back-to-back write errors (1s
// backoff floor) must not accumulate unbounded retired muxers — past the cap
// the oldest generation is closed immediately.
func TestStashRetiredMuxer_CapBoundsStashes(t *testing.T) {
	t.Helper()
	m := newTestManager(t)
	t.Cleanup(m.StopAll)

	entry := &streamEntry{dirPath: t.TempDir()}
	for range maxLegacyMuxers + 2 {
		mux, _, err := m.createMuxAndTrack(false, pumpSPS, pumpPPS, nil, entry.dirPath)
		require.NoError(t, err)
		entry.mu.Lock()
		stashRetiredMuxerLocked(entry, mux)
		entry.mu.Unlock()
	}

	entry.mu.Lock()
	require.Len(t, entry.prevMuxers, maxLegacyMuxers, "stash must be capped")
	entry.mu.Unlock()

	closeRetiredMuxers(entry)
	entry.mu.Lock()
	require.Empty(t, entry.prevMuxers)
	entry.mu.Unlock()
}

// TestStopStream_ClosesRetiredMuxers : the stream teardown closes the retired
// stashes too — a retired muxer must never outlive its entry (it would leak
// its segment files on disk after RemoveAll).
func TestStopStream_ClosesRetiredMuxers(t *testing.T) {
	t.Helper()
	m := NewManagerWithOpts(context.Background(), t.TempDir(), 64, 1<<20, 7)
	t.Cleanup(m.StopAll)
	m.SetLowLatency(true, 200*time.Millisecond)

	entry := pumpLLStream(t, m, "cam-legacy", 1)
	dir := entry.dirPath
	retireMuxer(t, m, "cam-legacy", entry)

	m.StopStream("cam-legacy")
	require.False(t, m.IsActive("cam-legacy"))
	_, err := os.Stat(dir)
	require.True(t, os.IsNotExist(err), "segment directory must be removed at teardown")
}
