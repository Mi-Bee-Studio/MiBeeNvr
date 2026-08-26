package api

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/substream"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/webrtc"
	"github.com/go-chi/chi/v5"
)

// --- WHEP (WebRTC-HTTP Egress Protocol) endpoints ---

// handleCreateWHEPSession handles POST /api/cameras/{id}/stream/webrtc[?quality=main|sub]
// It accepts an SDP offer and returns an SDP answer with a session URL.
// quality=sub (#513) registers the session under the camera's "/sub" stream
// key and feeds it from the on-demand sub-stream puller. WebRTC carries H.264
// only, so a non-H.264 sub-stream falls back to the main stream (the
// X-Stream-Quality response header reports what was actually served). The
// sub-stream reference acquired here is released when the session ends —
// the webrtc manager's onSessionEnd hook is the per-session anchor, since
// WHEP sessions outlive this HTTP request.
func (h *Handler) handleCreateWHEPSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Validate request parameters before service availability (a bad
	// quality= must not masquerade as "streaming unavailable").
	quality, qerr := parseQuality(r)
	if qerr != nil {
		WriteError(w, http.StatusBadRequest, qerr.Error())
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

	// quality=sub: acquire the on-demand pull and try to serve it. The
	// reference pairs with the manager's onSessionEnd release; every path
	// that does NOT create a session must release inline.
	if quality == qualitySub && h.camMgr != nil {
		if subSrc := h.acquireSub(w, r, id); subSrc != nil {
			if h.tryServeWHEPSub(w, r, id, subSrc, offerSDP) {
				return
			}
			// Sub stream unusable over WebRTC (non-H.264 codec or session
			// creation failed) — release and fall through to the main path.
			// acquireSub already stamped X-Stream-Quality: sub; rewrite it so
			// the client sees the fallback.
			w.Header().Set("X-Stream-Quality", qualityMain)
			h.camMgr.ReleaseSubStream(id)
		}
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

	writeWHEPAnswer(w, id, answerSDP, sessionID)
}

// tryServeWHEPSub attempts a quality=sub WHEP session against the acquired
// sub-stream source. It reports whether the session was created (and the
// response written); callers must release the sub-stream reference when it
// returns false. On success the release is owned by the webrtc manager's
// onSessionEnd hook.
func (h *Handler) tryServeWHEPSub(w http.ResponseWriter, r *http.Request, id string, subSrc *substream.Source, offerSDP []byte) bool {
	// WebRTC tracks are H.264-only — a sub-stream the camera encodes as
	// H.265 (common: many devices switch codec families between profiles)
	// cannot be served here. Poll briefly like the WS path: acquire waits
	// for readiness, but codec params can lag by a keyframe interval.
	codec, sps, _, _ := subSrc.CodecParams()
	const whepCodecWait = 5 * time.Second
	const whepCodecPoll = 200 * time.Millisecond
	deadline := time.Now().Add(whepCodecWait)
	for codec != model.FormatH264 || sps == nil {
		if time.Now().After(deadline) {
			logger.Info("WHEP: sub-stream not servable over WebRTC, falling back to main",
				"camera_id", id, "codec", string(codec))
			return false
		}
		time.Sleep(whepCodecPoll)
		codec, sps, _, _ = subSrc.CodecParams()
	}

	key := subKey(id)
	h.webrtcMgr.RegisterStream(key, subSrc.Hub(), sps)
	answerSDP, sessionID, err := h.webrtcMgr.CreateWHEPSession(key, offerSDP)
	if err != nil {
		if !errors.Is(err, webrtc.ErrMaxPeersReached) {
			logger.Error("failed to create sub WHEP session", "camera_id", id, "error", err)
		}
		return false
	}

	writeWHEPAnswer(w, id, answerSDP, sessionID)
	return true
}

// writeWHEPAnswer emits the 201 + SDP + session Location for a created
// WHEP session. The session URL carries the camera id (quality is a creation
// parameter only — DELETE resolves by session id).
func writeWHEPAnswer(w http.ResponseWriter, cameraID string, answerSDP []byte, sessionID string) {
	sessionURL := "/api/cameras/" + cameraID + "/stream/webrtc/" + sessionID
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
