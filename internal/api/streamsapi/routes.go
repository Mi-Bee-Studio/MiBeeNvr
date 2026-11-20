package streamsapi

import (
	"fmt"
	"net/http"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/substream"
	"github.com/go-chi/chi/v5"
)

// Register mounts the live-media egress domain on the authed router.
//
// Endpoints are registered flat (full paths, no subtree mounts): chi panics
// when the same pattern is mounted twice, and the camera CRUD tree (kept by
// the api root) shares the /api/cameras/{id} prefix with this domain's
// routes. The golden route snapshot freezes the external surface.
func Register(r chi.Router, deps Deps) {
	s := &server{deps: deps, snapStore: deps.SnapStore}
	if s.snapStore == nil {
		s.snapStore = NewSnapshotStore()
	}

	// Flow-path observability (#469): fleet-wide live pipeline snapshot.
	r.Get("/api/streams", s.handleListStreams)

	r.Get("/api/cameras/{id}/stream/ws", s.handleStreamWS)
	// HLS catch-all (playlist + segments under /stream/*; the static /stream/ws
	// above must not be shadowed — chi static beats wildcard in the same trie).
	r.Get("/api/cameras/{id}/stream/*", s.handleHLSStream)
	r.Delete("/api/cameras/{id}/stream", s.handleStopHLSStream)
	// WebRTC WHEP endpoints
	r.Post("/api/cameras/{id}/stream/webrtc", s.handleCreateWHEPSession)
	r.Delete("/api/cameras/{id}/stream/webrtc/{session}", s.handleDeleteWHEPSession)
	// HTTP-FLV + MJPEG URL + stills (all share the snapshot TTL cache)
	r.Get("/api/cameras/{id}/stream.flv", s.handleFLVStream)
	r.Get("/api/cameras/{id}/stream.mjpeg", s.handleMjpegStream)
	r.Get("/api/cameras/{id}/latest-frame", s.handleLatestFrame)
	r.Get("/api/cameras/{id}/snapshot", s.handleSnapshot)
	// Per-camera protocol capabilities + flow snapshot
	r.Get("/api/cameras/{id}/protocols", s.handleCameraProtocols)
	r.Get("/api/cameras/{id}/flow", s.handleCameraFlow)
}

// --- Stream quality negotiation (#513) ---

// Quality values for the live egress endpoints. "main" is the default;
// "sub" selects the camera's on-demand sub-stream (falls back to main when
// the camera has none — callers detect the fallback via X-Stream-Quality).
// Exported for the api root's quality-negotiation tests.
const (
	qualityMain = "main"
	qualitySub  = "sub"

	QualityMain = qualityMain
	QualitySub  = qualitySub
)

// ParseQuality parses the ?quality= parameter ("" and "main" → main).
// Exported: the api root's remaining tests reference it via the compat shim.
func ParseQuality(r *http.Request) (string, error) {
	switch q := r.URL.Query().Get("quality"); q {
	case "", qualityMain:
		return qualityMain, nil
	case qualitySub:
		return qualitySub, nil
	default:
		return "", fmt.Errorf("invalid quality %q (supported: main, sub)", q)
	}
}

// parseQuality is the in-package alias.
func parseQuality(r *http.Request) (string, error) { return ParseQuality(r) }

// SubKey is the protocol-manager stream key under which a camera's sub-stream
// egress is registered ("{cameraID}/sub"). Managers key entries by an opaque
// string, so the suffixed key reuses all of their machinery (viewer caps, GOP
// cache, idle watchdogs) without any per-quality plumbing inside them.
func SubKey(cameraID string) string { return cameraID + "/" + qualitySub }

func subKey(cameraID string) string { return SubKey(cameraID) }

// acquireSub attempts the on-demand sub-stream acquisition and reports
// whether egress should serve the sub entry. On any failure (no sub config,
// pull not ready) it falls back to main — quality negotiation degrades, never
// hard-fails — and logs the reason. The header tells the client which quality
// it actually got.
func (s *server) acquireSub(w http.ResponseWriter, r *http.Request, cameraID string) *substream.Source {
	if s.deps.Cam == nil {
		w.Header().Set("X-Stream-Quality", qualityMain)
		return nil
	}
	src, err := s.deps.Cam.AcquireSubStream(r.Context(), cameraID)
	if err != nil {
		streamLogger.Info("quality=sub unavailable, serving main stream",
			"camera_id", cameraID, "reason", err.Error())
		w.Header().Set("X-Stream-Quality", qualityMain)
		return nil
	}
	w.Header().Set("X-Stream-Quality", qualitySub)
	return src
}
