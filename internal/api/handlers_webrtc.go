package api

import (
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/webrtc"
	"github.com/go-chi/chi/v5"
)

// --- WHEP (WebRTC-HTTP Egress Protocol) endpoints ---

// handleCreateWHEPSession handles POST /api/cameras/{id}/stream/webrtc
// It accepts an SDP offer and returns an SDP answer with a session URL.
func (h *Handler) handleCreateWHEPSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// quality=sub is not wired for WHEP yet (#513 v1): WHEP sessions outlive
	// the creating HTTP request, so the sub-stream puller's reference count
	// has no anchor to decrement on session close — the manager needs a
	// session-scoped release hook first. Clients get an explicit error and
	// can use ws/flv/hls sub endpoints meanwhile. Validated before service
	// availability so the contract is observable even with WebRTC disabled.
	if q := r.URL.Query().Get("quality"); q != "" && q != qualityMain {
		WriteError(w, http.StatusBadRequest,
			"quality=sub is not supported for WebRTC yet; use stream/ws, stream.flv (?quality=sub) or stream/sub/index.m3u8")
		return
	}

	if h.webrtcMgr == nil {
		WriteError(w, http.StatusServiceUnavailable, "WebRTC not available")
		return
	}

	// Validate content type
	if ct := r.Header.Get("Content-Type"); ct != "application/sdp" {
		WriteError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/sdp")
		return
	}

	// Check camera exists
	cam, err := h.db.GetCamera(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get camera")
		return
	}
	if cam == nil {
		WriteError(w, http.StatusNotFound, "camera not found")
		return
	}

	// Read SDP offer
	offerSDP, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB max
	if err != nil {
		WriteError(w, http.StatusBadRequest, "failed to read SDP offer")
		return
	}
	if len(offerSDP) == 0 {
		WriteError(w, http.StatusBadRequest, "empty SDP offer")
		return
	}

	// On-demand StreamHub registration for WebRTC. The recorder's SPS picks
	// the offered H.264 profile variant (High-profile streams need the
	// 640028 track or browsers reject every frame — see webrtc.NewManager).
	if h.camMgr != nil {
		rec := h.camMgr.GetRecorder(id)
		if rec != nil {
			hub := getStreamHub(rec)
			if hub != nil {
				_, sps, _, _ := getCodecParams(rec)
				h.webrtcMgr.RegisterStream(id, hub, sps)
				// Audio muxing (#372): G.711/Opus ride the WebRTC track;
				// AAC cameras are left video-only by the manager (keep the
				// separate audio-WS path). Best-effort — video continues on
				// any error.
				setupAudioForWebRTC(h, id, rec)
			}
		}
	}

	// Create WHEP session
	answerSDP, sessionID, err := h.webrtcMgr.CreateWHEPSession(id, offerSDP)
	if err != nil {
		if errors.Is(err, webrtc.ErrMaxPeersReached) {
			WriteError(w, http.StatusServiceUnavailable, "maximum WebRTC viewers reached for this camera")
			return
		}
		logger.Error("failed to create WHEP session", "camera_id", id, "error", err)
		WriteError(w, http.StatusBadRequest, "failed to negotiate WebRTC session")
		return
	}

	// Build session URL for Location header
	sessionURL := "/api/cameras/" + id + "/stream/webrtc/" + sessionID

	w.Header().Set("Content-Type", "application/sdp")
	w.Header().Set("Location", sessionURL)
	w.WriteHeader(http.StatusCreated)
	w.Write(answerSDP)
}

// handleDeleteWHEPSession handles DELETE /api/cameras/{id}/stream/webrtc/{session}
// It tears down an active WHEP session.
func (h *Handler) handleDeleteWHEPSession(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "session")

	if h.webrtcMgr == nil {
		WriteError(w, http.StatusServiceUnavailable, "WebRTC not available")
		return
	}

	if err := h.webrtcMgr.DeleteWHEPSession(sessionID); err != nil {
		if errors.Is(err, webrtc.ErrSessionNotFound) {
			WriteError(w, http.StatusNotFound, "session not found")
			return
		}
		logger.Error("failed to delete WHEP session", "session_id", sessionID, "error", err)
		WriteError(w, http.StatusInternalServerError, "failed to delete session")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// setupAudioForWebRTC configures audio muxing on the WebRTC manager for a
// camera stream (#372). Mirrors setupAudioForWS: extracts codec params from
// the recorder and calls SetAudioInfo. AAC and unknown codecs are ignored by
// the manager (video-only WHEP, separate audio-WS path stays). Errors are
// non-fatal — video streaming continues.
func setupAudioForWebRTC(h *Handler, id string, rec model.Recorder) {
	actualRec := unwrapDelegate(rec)
	provider, ok := actualRec.(audioInfoProvider)
	if !ok {
		return
	}
	audioCodec := provider.AudioCodec()
	if audioCodec == "" {
		return
	}

	muLaw := false
	if audioCodec == "g711" {
		config := provider.AudioConfig()
		if len(config) > 0 && config[0] == 1 {
			muLaw = true
		}
	}

	if err := h.webrtcMgr.SetAudioInfo(id, audioCodec, muLaw, provider.AudioSampleRate(), provider.AudioChannels()); err != nil {
		slog.Warn("WebRTC: failed to set audio info", "camera_id", id, "error", err)
	}
}
