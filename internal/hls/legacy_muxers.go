package hls

// Grace-window serving from retired muxers. A HLS muxer rebuild (write-error
// recovery: handleWriteError → rebuildMuxer) changes gohlslib's random
// filename prefix and resets segment numbering, while clients keep requesting
// names from the playlist they fetched before the error: every
// <prefix>_segN / <prefix>_partN / <prefix>_init name the old muxer
// registered becomes unknown to the rebuilt one. gohlslib routes by exact
// registered name and silently no-ops on everything else, which Handle turns
// into a 404 — and for a client that is a fatal stream error, not a retryable
// hiccup (observed with ExoPlayer: first 404 → Source error → playback dead
// until a manual restart). Under load the write-error backoff is 1-16s, so a
// playlist fetched just before an error can have its ENTIRE advertised window
// orphaned at once — this is the stable "playlist 200, every segment 404"
// degraded-state shape.
//
// Closing the old muxer (the previous behavior) also deletes its segment
// files from disk, so no disk fallback is possible. Instead the retired
// muxer stays alive for legacyMuxerGrace and Handle replays unknown names
// against it: the old window keeps serving while the client drains its stale
// playlist, and its next playlist fetch moves it onto the new prefix (the
// media playlist name <id>_stream.m3u8 carries no random prefix and always
// resolves on the current muxer).

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/bluenviron/gohlslib/v2"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"
)

var legacyLogger = slogx.Component("hls-legacy")

const (
	// legacyMuxerGrace outlives any live client's playlist-to-segment lag: the
	// advertised window is SegmentCount segments (≤10) of a few seconds plus
	// LL-HLS part hold-back, and a healthy client refreshes the playlist every
	// segment, so a minute covers even a badly backpressured one.
	legacyMuxerGrace = 60 * time.Second
	// maxLegacyMuxers bounds the stash when write errors repeat back-to-back
	// (the recovery backoff floor is 1s): three generations cover any client
	// overlap while keeping the retired disk footprint bounded.
	maxLegacyMuxers = 3
	// legacyServeTimeout bounds each retired-muxer replay. Segment/init/part
	// handlers return promptly; the only blocking handler is a future-part
	// preload hint whose condition can no longer fire on a retired muxer —
	// it resolves when the stash is closed (gohlslib broadcasts the condition),
	// not by frame progress.
	legacyServeTimeout = 15 * time.Second
)

// legacyMuxer is a retired muxer kept alive — deliberately not Closed, which
// would delete its segment files from disk — so its registered names keep
// resolving for clients holding a pre-rebuild playlist.
type legacyMuxer struct {
	mux     *gohlslib.Muxer
	expires time.Time
}

// stashRetiredMuxerLocked moves a muxer retired by write-error recovery into
// the entry's grace list. Caller holds entry.mu (writeLoop domain). Evicts the
// oldest stash past the cap, closing it immediately — a stash that old is
// already beyond every live client's playlist lag.
func stashRetiredMuxerLocked(entry *streamEntry, mux *gohlslib.Muxer) {
	entry.prevMuxers = append(entry.prevMuxers, &legacyMuxer{
		mux:     mux,
		expires: time.Now().Add(legacyMuxerGrace),
	})
	for len(entry.prevMuxers) > maxLegacyMuxers {
		oldest := entry.prevMuxers[0]
		entry.prevMuxers = entry.prevMuxers[1:]
		oldest.mux.Close()
	}
}

// reapRetiredMuxers closes and drops grace-expired stashes. Called from
// writeLoop after successful writes (the recovery has proven the new muxer
// works, so the old window is pure cleanup) — a no-op while nothing is
// stashed.
func reapRetiredMuxers(entry *streamEntry) {
	entry.mu.Lock()
	var expired []*legacyMuxer
	kept := entry.prevMuxers[:0]
	for _, lm := range entry.prevMuxers {
		if time.Now().After(lm.expires) {
			expired = append(expired, lm)
		} else {
			kept = append(kept, lm)
		}
	}
	entry.prevMuxers = kept
	entry.mu.Unlock()

	for _, lm := range expired {
		lm.mux.Close()
	}
}

// closeRetiredMuxers closes every stash; the stream-teardown path calls it so
// no retired muxer outlives the entry. In-flight replays racing the close are
// serialized by gohlslib's own mutex and may see their file vanish — a single
// errored request at teardown is acceptable.
func closeRetiredMuxers(entry *streamEntry) {
	entry.mu.Lock()
	stashed := entry.prevMuxers
	entry.prevMuxers = nil
	entry.mu.Unlock()

	for _, lm := range stashed {
		lm.mux.Close()
	}
}

// serveRetiredMuxers replays a request the current muxer could not resolve
// against the stashed pre-rebuild muxers, newest generation first. Returns
// true once one of them wrote a response. Playlist names never fall back —
// see the file comment. Every replay is bounded by legacyServeTimeout (the
// one gohlslib handler that can block on a retired muxer is a future-part
// preload hint) and recovers handler panics exactly like the primary
// mux.Handle path.
func serveRetiredMuxers(m *Manager, cameraID string, entry *streamEntry, w http.ResponseWriter, r *http.Request) bool {
	if strings.HasSuffix(strings.ToLower(r.URL.Path), ".m3u8") {
		return false
	}

	entry.mu.Lock()
	stashed := make([]*legacyMuxer, len(entry.prevMuxers))
	copy(stashed, entry.prevMuxers)
	entry.mu.Unlock()

	now := time.Now()
	for i := len(stashed) - 1; i >= 0; i-- {
		lm := stashed[i]
		if now.After(lm.expires) {
			continue
		}
		zw := &silentResponseWriter{ResponseWriter: w}
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() {
				if rv := recover(); rv != nil {
					legacyLogger.Warn("HLS retired-muxer replay recovered from panic",
						"camera_id", cameraID, "panic", rv)
				}
			}()
			lm.mux.Handle(zw, r)
		}()
		ctx, cancel := context.WithTimeout(r.Context(), legacyServeTimeout)
		served := false
		select {
		case <-done:
			served = zw.wrote
		case <-ctx.Done():
			legacyLogger.Warn("HLS retired-muxer replay timed out",
				"camera_id", cameraID, "path", r.URL.Path)
		}
		cancel()
		if !served {
			continue
		}
		if m.metrics != nil {
			m.metrics.HLSRetiredMuxerServes.WithLabelValues(cameraID).Inc()
		}
		legacyLogger.Debug("HLS served pre-rebuild name from retired muxer",
			"camera_id", cameraID, "path", r.URL.Path)
		return true
	}
	return false
}
