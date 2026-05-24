package api

import (
	"errors"
	"io"
	"net/http"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/webrtc"
	"github.com/go-chi/chi/v5"
)

// --- WHEP (WebRTC-HTTP Egress Protocol) endpoints ---

// handleCreateWHEPSession handles POST /api/cameras/{id}/stream/webrtc
// It accepts an SDP offer and returns an SDP answer with a session URL.
func (h *Handler) handleCreateWHEPSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if h.webrtcMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "WebRTC not available")
		return
	}

	// Validate content type
	if ct := r.Header.Get("Content-Type"); ct != "application/sdp" {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/sdp")
		return
	}

	// Check camera exists
	cam, err := h.db.GetCamera(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get camera")
		return
	}
	if cam == nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}

	// Read SDP offer
	offerSDP, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB max
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read SDP offer")
		return
	}
	if len(offerSDP) == 0 {
		writeError(w, http.StatusBadRequest, "empty SDP offer")
		return
	}

	// On-demand StreamHub registration for WebRTC
	if h.camMgr != nil {
		rec := h.camMgr.GetRecorder(id)
		if rec != nil {
			hub := getStreamHub(rec)
			if hub != nil {
				h.webrtcMgr.RegisterStream(id, hub)
			}
		}
	}

	// Create WHEP session
	answerSDP, sessionID, err := h.webrtcMgr.CreateWHEPSession(id, offerSDP)
	if err != nil {
		if errors.Is(err, webrtc.ErrMaxPeersReached) {
			writeError(w, http.StatusServiceUnavailable, "maximum WebRTC viewers reached for this camera")
			return
		}
		logger.Error("failed to create WHEP session", "camera_id", id, "error", err)
		writeError(w, http.StatusBadRequest, "failed to negotiate WebRTC session")
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
		writeError(w, http.StatusServiceUnavailable, "WebRTC not available")
		return
	}

	if err := h.webrtcMgr.DeleteWHEPSession(sessionID); err != nil {
		if errors.Is(err, webrtc.ErrSessionNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		logger.Error("failed to delete WHEP session", "session_id", sessionID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete session")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
