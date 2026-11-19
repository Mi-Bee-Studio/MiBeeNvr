package hls

// Stream lifecycle and HTTP delivery on the HLS manager: stop/evict, the LRU
// cap that bounds concurrent streams, status accessors and the playlist/segment
// handler. Split out of manager.go.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4/seekablebuffer"
	mp4codecs "github.com/bluenviron/mediacommon/v2/pkg/formats/mp4/codecs"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/frametrace"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model/nalutil"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

// StopStream stops the HLS muxer for the given camera and cleans up temp files.
func (m *Manager) StopStream(cameraID string) {
	m.mu.Lock()
	join := m.stopStreamLocked(cameraID)
	m.mu.Unlock()
	// Join writeLoop + idleWatchdog OUTSIDE m.mu. Joining under the lock would
	// deadlock if idleWatchdog is concurrently in its own m.StopStream path
	// (waiting on m.mu). See stopStreamLocked audit (#230).
	if join != nil {
		join()
	}
}

// EvictStream stops and removes an active HLS stream, freeing a slot.
// Returns ErrStreamNotActive if the stream is not running.
func (m *Manager) EvictStream(cameraID string) error {
	m.mu.Lock()
	if _, ok := m.streams[cameraID]; !ok {
		m.mu.Unlock()
		return ErrStreamNotActive
	}
	join := m.stopStreamLocked(cameraID)
	m.mu.Unlock()
	if join != nil {
		join() // join outside m.mu — see StopStream (#230)
	}
	return nil
}

// GetActiveStreamCount returns the number of currently active HLS streams.
func (m *Manager) GetActiveStreamCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.streams)
}

// evictLRULocked finds and evicts the stream with the oldest lastUsed timestamp.
// Caller must hold m.mu write lock. The newStreamID is excluded from eviction.
func (m *Manager) evictLRULocked(newStreamID string) {
	var oldestID string
	var oldestTime time.Time
	for id, entry := range m.streams {
		if id == newStreamID {
			continue
		}
		if t := entry.lastUsedTime(); oldestID == "" || t.Before(oldestTime) {
			oldestID = id
			oldestTime = t
		}
	}
	if oldestID == "" {
		return
	}
	hlsLogger.Warn("HLS max streams reached, evicting LRU stream", "camera_id", oldestID)
	if m.metrics != nil {
		m.metrics.HLSIdleEvictions.WithLabelValues(oldestID).Inc()
	}
	// This runs under m.mu (startStream holds the write lock), so joining
	// inline would deadlock with idleWatchdog's own m.StopStream path. The
	// teardown goroutine takes no locks beyond entry.mu: it waits for the
	// evicted entry's goroutines to exit, then closes the muxer and removes
	// the segment directory. StopStream/StopAll/EvictStream — the
	// user-facing stop paths — join synchronously. See stopStreamLocked (#230).
	if join := m.stopStreamLocked(oldestID); join != nil {
		go join()
	}
}

// stopStreamLocked stops a stream. Caller must hold m.mu write lock.
// Returns a teardown function (non-nil if a stream was stopped) that the
// caller MUST invoke without m.mu held: it joins writeLoop + idleWatchdog and
// only then closes the muxer and removes the segment directory. Closing the
// muxer while writeLoop is still draining frameCh is a data race inside
// gohlslib's bufio writer (caught by -race in the sub-stream pump tests), and
// RemoveAll would delete files under an active writer.
//
// Joining under m.mu would deadlock: idleWatchdog's idle-timeout path calls
// m.StopStream (m.mu.Lock), so a wait while holding m.mu can stall forever.
// Callers that cannot leave m.mu before joining (evictLRULocked) run the
// teardown in a goroutine instead — it takes no locks beyond entry.mu.
//
// # Lock-order audit (#230): no m.mu ↔ entry.mu cycle exists.
//
// Every site that takes entry.mu first releases m.mu:
//   - writeFrame:   m.mu.RLock → read entry → m.mu.RUnlock → entry.mu.Lock …
//   - idleWatchdog: m.mu.RLock → read entry → m.mu.RUnlock → entry.mu.Lock …
//   - teardown:     runs after m.mu is released.
//
// So the lock order is uniformly m.mu → (release) → entry.mu, with no reverse
// acquisition. The idleWatchdog does call m.StopStream (m.mu.Lock) on idle
// timeout, but by then it has already released entry.mu, so there is no
// nested hold.
func (m *Manager) stopStreamLocked(cameraID string) func() {
	entry, ok := m.streams[cameraID]
	if !ok {
		return nil
	}

	entry.cancel()
	if entry.subStreamCancel != nil {
		entry.subStreamCancel()
		entry.subStreamCancel = nil
	}

	delete(m.streams, cameraID)
	if m.metrics != nil {
		m.metrics.HLSActiveStreams.WithLabelValues(cameraID).Set(0)
	}
	hlsLogger.Info("HLS stream stopped", "camera_id", cameraID)

	return func() {
		entry.wg.Wait()
		entry.mu.Lock()
		if entry.mux != nil {
			entry.mux.Close()
			entry.mux = nil
		}
		entry.mu.Unlock()
		_ = os.RemoveAll(entry.dirPath)
	}
}

// WriteH264 queues an H264 access unit for async writing to the HLS stream.
// This is non-blocking — it acquires a read lock only briefly and never blocks on disk I/O.
// If the write buffer is full, the frame is silently dropped to protect the recording pipeline.
func (m *Manager) WriteH264(cameraID string, pts int64, au [][]byte) error {
	return m.writeFrame(cameraID, pts, au)
}

// WriteH265 queues an H265 access unit for async writing to the HLS stream.
// Same non-blocking semantics as WriteH264.
func (m *Manager) WriteH265(cameraID string, pts int64, au [][]byte) error {
	return m.writeFrame(cameraID, pts, au)
}

func (m *Manager) writeFrame(cameraID string, pts int64, au [][]byte) error {
	m.mu.RLock()
	entry, ok := m.streams[cameraID]
	m.mu.RUnlock()

	if !ok {
		return nil // stream not active, silently ignore
	}

	// Hoisted loop/frame invariants (#875 M7): IsIDR rescans the whole AU —
	// compute once, reuse for both the throttle decision and the trace.
	isIDR := nalutil.IsIDR(au, entry.isH265)

	entry.mu.Lock()
	entry.touchLastUsed()

	// Credit-based FPS throttling: accumulate elapsed time between frames,
	// send only when enough credit has accumulated for one interval.
	// This produces consistent frame intervals instead of jittery drops.
	if shouldThrottle(entry.maxFPS, &entry.fpsCredit, &entry.lastFrameTime, time.Now(), isIDR) {
		traceID := "no-trace"
		if isIDR {
			traceID = fmt.Sprintf("%s-%d", cameraID, pts)
		}
		if frametrace.Active(cameraID) {
			frametrace.Log(
				cameraID,
				"trace_id", traceID,
				"camera_id", cameraID,
				"stage", "hls_drop",
				"is_idr", isIDR,
				"reason", "fps_throttle",
			)
		}
		if m.metrics != nil {
			m.metrics.HLSFramesDropped.WithLabelValues(cameraID).Inc()
		}
		entry.mu.Unlock()
		return nil
	}

	entry.mu.Unlock()

	// Non-blocking send — drop frame if buffer full to protect recording pipeline
	select {
	case entry.frameCh <- hlsFrame{pts: pts, au: au}:
	default:
		// Buffer full, drop frame. Live view tolerates dropped frames.
		isIDR := nalutil.IsIDR(au, entry.isH265)
		traceID := "no-trace"
		if isIDR {
			traceID = fmt.Sprintf("%s-%d", cameraID, pts)
		}
		if frametrace.Active(cameraID) {
			frametrace.Log(
				cameraID,
				"trace_id", traceID,
				"camera_id", cameraID,
				"stage", "hls_drop",
				"is_idr", isIDR,
				"reason", "buffer_full",
				"queue_depth", len(entry.frameCh),
			)
		}
		if m.metrics != nil {
			m.metrics.HLSFramesDropped.WithLabelValues(cameraID).Inc()
		}

	}

	return nil
}

// IsActive returns true if an HLS stream is active for the given camera.
func (m *Manager) IsActive(cameraID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.streams[cameraID]
	return ok
}

// CodecFor returns the video codec frozen into the HLS muxer for the given
// camera at stream-start time. Unlike the recorder's CodecParams(), this value
// persists across recorder reconnects (the muxer track is not torn down on a
// P2P blip), so callers that need the codec while a recorder is mid-reconnect
// (e.g. /protocols routing, which otherwise falls back to a stale DB value)
// get the correct answer. ok is false when no HLS stream entry exists for the
// camera (never started, idle-evicted, or server just restarted with no viewer).
func (m *Manager) CodecFor(cameraID string) (codec model.Format, ok bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, found := m.streams[cameraID]
	if !found {
		return "", false
	}
	if entry.isH265 {
		return model.FormatH265, true
	}
	return model.FormatH264, true
}

// GetStreamStatus returns whether a stream is active for the given camera.
// Returns (active, nil) — use IsActive() for simple boolean check.
// This method is designed for API responses that include stream metadata.
func (m *Manager) GetStreamStatus(cameraID string) (active bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.streams[cameraID]
	return ok
}

// Handle proxies an HTTP request to the HLS muxer for the given camera.
// Returns false if the stream is not active.
// Includes a 30s timeout to prevent indefinite blocking when the muxer
// has no segments (e.g. stale Hub consumer after idle eviction).
// silentResponseWriter detects that gohlslib's request router served nothing.
// Its server no-ops on filenames it never registered (only index.m3u8,
// <id>_stream.m3u8 and segment/part file names exist), so an unknown path —
// e.g. the stream.m3u8 name our docs once advertised — surfaced as
// HTTP 200 with an empty body (#772). On a definitive zero-write completion
// the caller turns this into a 404.
type silentResponseWriter struct {
	http.ResponseWriter
	wrote bool
}

func (s *silentResponseWriter) WriteHeader(code int) {
	s.wrote = true
	s.ResponseWriter.WriteHeader(code)
}

func (s *silentResponseWriter) Write(b []byte) (int, error) {
	s.wrote = true
	return s.ResponseWriter.Write(b)
}

func (m *Manager) Handle(cameraID string, w http.ResponseWriter, r *http.Request) bool {
	m.mu.RLock()
	entry, ok := m.streams[cameraID]
	m.mu.RUnlock()

	if !ok {
		return false
	}

	entry.mu.Lock()
	entry.touchLastUsed()
	// Snapshot muxer under the lock. handleWriteError sets entry.mux = nil
	// on write failures; an HTTP request arriving in that window would
	// dereference nil and panic the whole process (SIGSEGV addr=0xd0).
	mux := entry.mux
	entry.mu.Unlock()

	if mux == nil {
		// Muxer destroyed by write-error recovery and not yet recreated.
		// Returning false lets the caller (HTTP handler) respond 404 so the
		// frontend retries; the stream re-initialises on next start.
		hlsLogger.Warn("HLS Handle: muxer not initialized", "camera_id", cameraID)
		return false
	}

	// gohlslib's LL-HLS playlists open with up to SegmentCount gap placeholder
	// entries whose URI is the literal "gap.mp4" (iOS LL-HLS segment-count
	// padding). The library never registers a handler for that name, so the
	// request used to fall through to the silentResponseWriter zero-write path
	// and surfaced as 404 "unknown HLS resource". Clients that do not honour
	// EXT-X-GAP (observed with ExoPlayer) fetch it right after joining and
	// treat the 404 as fatal, killing playback on every freshly created muxer.
	// Serve a valid fMP4 init segment built from the stream's cached
	// parameter sets instead: zero samples — gap-aware clients skip it by flag
	// and never fetch, naive ones parse an empty segment and continue joining.
	if filepath.Base(r.URL.Path) == gapPlaceholderName {
		serveGapPlaceholder(w, entry)
		return true
	}

	// Guard against muxer blocking forever when no frames arrive.
	// The muxer blocks on m3u8 requests until the first segment is ready.
	// If no frames reach the muxer (e.g. Hub subscription failed), this
	// timeout ensures the HTTP request eventually returns.
	const handleTimeout = 30 * time.Second
	ctx, cancel := context.WithTimeout(r.Context(), handleTimeout)
	defer cancel()

	done := make(chan struct{})
	zw := &silentResponseWriter{ResponseWriter: w}
	go func() {
		defer close(done)
		// Recover from panics that occur when the HTTP handler context is
		// cancelled (e.g. client disconnect or timeout) but gohlslib's
		// mux.Handle goroutine still tries to write to the response writer.
		// Without this recover, the panic crashes the entire process.
		defer func() {
			if rv := recover(); rv != nil {
				hlsLogger.Warn("HLS Handle recovered from panic",
					"camera_id", cameraID, "panic", rv)
			}
		}()
		mux.Handle(zw, r)
	}()

	select {
	case <-done:
		// The muxer router completed without touching the writer: the
		// requested file name is not one it serves. gohlslib silently
		// no-ops here, which used to leak an empty 200 to the client (#772).
		if !zw.wrote {
			http.Error(w, "unknown HLS resource", http.StatusNotFound)
		}
		return true
	case <-ctx.Done():
		hlsLogger.Warn("HLS Handle timed out or cancelled", "camera_id", cameraID, "timeout", handleTimeout)
		return true
	}
}

// gapPlaceholderName is the literal URI gohlslib emits for LL-HLS gap
// placeholders in media playlists (see Handle).
const gapPlaceholderName = "gap.mp4"

// serveGapPlaceholder answers a fetch of the LL-HLS gap placeholder with a
// valid fMP4 init segment (ftyp+moov, zero samples) marshalled from the
// stream's cached parameter sets — the same bytes the muxer's own init handler
// would serve for this codec (gohlslib caches init in RAM in LL mode and only
// spills it to disk for non-LL variants, so there is nothing to read from
// dirPath). Clients that ignore EXT-X-GAP and fetch the placeholder get an
// empty segment they can parse and move past, instead of a fatal 404.
func serveGapPlaceholder(w http.ResponseWriter, entry *streamEntry) {
	entry.mu.Lock()
	isH265, vps, sps, pps := entry.isH265, entry.vps, entry.sps, entry.pps
	entry.mu.Unlock()

	var codec mp4codecs.Codec
	switch {
	case isH265 && nalutil.HasCompleteParamSets([][]byte{vps, sps, pps}, true):
		codec = &mp4codecs.H265{VPS: vps, SPS: sps, PPS: pps}
	case !isH265 && sps != nil && pps != nil:
		codec = &mp4codecs.H264{SPS: sps, PPS: pps}
	default:
		// Parameter sets not cached yet — nothing sensible to serve; the
		// playlists that advertise gaps cannot exist before the first segment
		// either, so this window is unreachable in practice.
		http.Error(w, "init segment not ready", http.StatusNotFound)
		return
	}

	init := fmp4.Init{
		Tracks: []*fmp4.InitTrack{{
			ID:        1,
			TimeScale: 90000,
			Codec:     codec,
		}},
	}
	var buf seekablebuffer.Buffer
	if err := init.Marshal(&buf); err != nil {
		http.Error(w, "init segment marshal failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "video/mp4")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

// StopAll stops all active HLS streams and cancels the manager context.
func (m *Manager) StopAll() {
	m.mu.Lock()
	joins := make([]func(), 0, len(m.streams))
	for id := range m.streams {
		if join := m.stopStreamLocked(id); join != nil {
			joins = append(joins, join)
		}
	}
	m.cancel()
	m.mu.Unlock()
	// Join all goroutines and tear down muxers outside m.mu — see StopStream (#230).
	for _, join := range joins {
		join()
	}
}

// SubscribeToHub subscribes the HLS manager to a StreamHub for the given camera.
// It sets the HLS consumer callback ("hls") and registers an additional drop
// callback to increment the hls_frames_dropped_total Prometheus counter.
// #469: uses AddOnDrop (callback list) — the old direct assignment silently
// destroyed the camera manager's hub-level Prometheus wiring as soon as any
// HLS subscriber attached.
func (m *Manager) SubscribeToHub(cameraID string, hub *streamhub.StreamHub, isH265 bool) error {
	m.mu.Lock()
	if entry, ok := m.streams[cameraID]; ok {
		entry.hub = hub
	}
	m.mu.Unlock()
	if m.metrics != nil {
		hub.AddOnDrop(func(id string, isIDR bool) {
			if id == "hls" {
				m.metrics.HLSFramesDropped.WithLabelValues(cameraID).Inc()
			}
		})
	}
	if isH265 {
		return hub.Subscribe("hls", func(pts int64, au [][]byte) {
			_ = m.WriteH265(cameraID, pts, au)
		})
	}
	return hub.Subscribe("hls", func(pts int64, au [][]byte) {
		_ = m.WriteH264(cameraID, pts, au)
	})
}

// ActiveHub returns the StreamHub the entry's "hls" consumer is currently
// subscribed to (nil when the stream is not active). The sub-stream recycler
// (#513) compares this against the recycled hub to avoid stopping an entry
// that already rebound to a fresh pull generation.
func (m *Manager) ActiveHub(cameraID string) *streamhub.StreamHub {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.streams[cameraID]
	if !ok {
		return nil
	}
	return entry.hub
}

// RebindHub re-subscribes an active HLS entry to a new StreamHub — used when
// the sub-stream puller restarted with a fresh hub (#513). The muxer's track
// parameters stay from registration; puller restarts preserve the camera's
// parameter sets in practice, and the recycle path stops the entry anyway, so
// the rebind is only a short-window safety net.
func (m *Manager) RebindHub(cameraID string, hub *streamhub.StreamHub, isH265 bool) {
	if hub == nil {
		return
	}
	m.mu.RLock()
	entry, ok := m.streams[cameraID]
	oldHub := (*streamhub.StreamHub)(nil)
	if ok {
		oldHub = entry.hub
	}
	m.mu.RUnlock()
	if !ok || oldHub == hub {
		return
	}
	if oldHub != nil {
		oldHub.Unsubscribe("hls")
	}
	_ = m.SubscribeToHub(cameraID, hub, isH265)
	hlsLogger.Info("HLS stream rebound to new StreamHub", "camera_id", cameraID)
}

func (m *Manager) idleWatchdog(ctx context.Context, cameraID string) {
	ticker := time.NewTicker(m.idleTimeout / 2)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.mu.RLock()
			entry, ok := m.streams[cameraID]
			m.mu.RUnlock()

			if !ok {
				return
			}
			lastUsed := entry.lastUsedTime()
			if time.Since(lastUsed) > m.idleTimeout {
				hlsLogger.Info("HLS stream idle timeout, stopping", "camera_id", cameraID)
				if m.metrics != nil {
					m.metrics.HLSIdleEvictions.WithLabelValues(cameraID).Inc()
				}
				m.StopStream(cameraID)
				return
			}
		}
	}
}

// observeNewSegments scans the segment directory for new files and reports sizes.
// Throttled to at most one os.ReadDir per segmentObserveInterval per stream to avoid
// a per-frame directory-scan storm (was called after every successful frame write).
func (m *Manager) observeNewSegments(cameraID string, entry *streamEntry) {
	if m.metrics == nil {
		return
	}
	now := time.Now()
	if now.Sub(entry.lastSegObserve) < segmentObserveInterval {
		return
	}
	entry.lastSegObserve = now
	entries, err := os.ReadDir(entry.dirPath)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".m3u8") || strings.HasSuffix(name, ".tmp") {
			continue
		}
		if entry.observedSegments[name] {
			continue
		}
		entry.observedSegments[name] = true
		info, err := e.Info()
		if err != nil {
			continue
		}
		m.metrics.HLSSegmentSizeBytes.WithLabelValues(cameraID).Observe(float64(info.Size()))
	}
}
