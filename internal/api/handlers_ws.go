package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/wsstream"
	"github.com/go-chi/chi/v5"
)

// --- WebSocket streaming endpoint ---

// handleStreamWS handles GET /api/cameras/{id}/stream/ws
// It upgrades the HTTP connection to a WebSocket and streams binary-encoded
// video frames (CodecInfo first, then VideoFrame messages).
func (h *Handler) handleStreamWS(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if h.wsMgr == nil {
		WriteError(w, http.StatusServiceUnavailable, "WebSocket streaming not available")
		return
	}

	// Check camera exists
	cam, err := h.db.GetCamera(r.Context(), id)
	if err != nil {
		slog.Error("WS: failed to get camera", "camera_id", id, "error", err)
		WriteError(w, http.StatusInternalServerError, "failed to get camera")
		return
	}
	if cam == nil {
		WriteError(w, http.StatusNotFound, "camera not found")
		return
	}

	// On-demand registration: if WebSocket stream not registered, register it
	if !h.wsMgr.IsActive(id) {
		if h.camMgr == nil {
			WriteError(w, http.StatusNotFound, "WebSocket stream not active")
			return
		}
		rec := h.camMgr.GetRecorder(id)
		if rec == nil {
			slog.Warn("WS: recorder not running", "camera_id", id)
			WriteError(w, http.StatusBadRequest, "camera recorder not running")
			return
		}

		codec, sps, pps, vps := getCodecParams(rec)
		slog.Info("WS: on-demand register", "camera_id", id, "codec", codec, "has_sps", sps != nil, "has_pps", pps != nil)
		// MJPEG/JPEG cameras don't have SPS/PPS — skip the keyframe wait.
		if codec != model.FormatMJPEG && codec != model.EncJPEG {
			if sps == nil || pps == nil {
				// Recorder is active but hasn't received a keyframe yet.
				// Poll for up to 5 seconds (typical keyframe interval is 1-4s).
				const wsCodecWait = 5 * time.Second
				const wsCodecPoll = 200 * time.Millisecond
				deadline := time.Now().Add(wsCodecWait)
				for sps == nil || pps == nil {
					if time.Now().After(deadline) {
						slog.Warn("WS: timed out waiting for codec params", "camera_id", id)
						WriteError(w, http.StatusServiceUnavailable, "waiting for video stream")
						return
					}
					time.Sleep(wsCodecPoll)
					codec, sps, pps, vps = getCodecParams(rec)
				}
				slog.Info("WS: codec params available after poll", "camera_id", id, "codec", codec)
			}
		}

		hub := getStreamHub(rec)
		if err := h.wsMgr.RegisterStream(id, codec, sps, pps, vps, hub); err != nil {
			if !errors.Is(err, wsstream.ErrStreamExists) {
				slog.Error("WS: failed to register", "camera_id", id, "error", err)
				WriteError(w, http.StatusInternalServerError, "failed to register WebSocket stream")
				return
			}
		}
		// Configure audio streaming if the recorder provides audio
		setupAudioForWS(h, id, rec)

	}

	slog.Info("WS: serving", "camera_id", id)

	// Serve WebSocket stream (blocks until client disconnects)
	if err := h.wsMgr.ServeWS(id, w, r); err != nil {
		if errors.Is(err, wsstream.ErrStreamNotActive) {
			WriteError(w, http.StatusNotFound, "WebSocket stream not active")
			return
		}
		if errors.Is(err, wsstream.ErrMaxViewers) {
			WriteError(w, http.StatusServiceUnavailable, "maximum WebSocket viewers reached")
			return
		}
		slog.Error("WS: serve failed", "camera_id", id, "error", err, "error_type", fmt.Sprintf("%T", err))
	}
}

// audioInfoProvider is the interface for recorders that expose audio parameters.
type audioInfoProvider interface {
	AudioCodec() string
	AudioSampleRate() int
	AudioChannels() int
	AudioConfig() []byte
}

// setupAudioForWS configures audio streaming on the WebSocket manager
// for a camera stream. It is called after RegisterStream.
// If the recorder has audio, it extracts the codec parameters and calls
// SetAudioInfo. Errors are non-fatal — video streaming continues.
func setupAudioForWS(h *Handler, id string, rec model.Recorder) {
	actualRec := unwrapDelegate(rec)
	provider, ok := actualRec.(audioInfoProvider)
	if !ok {
		slog.Info("WS: recorder does not expose audio info", "camera_id", id, "type", fmt.Sprintf("%T", actualRec))
		return
	}
	audioCodec := provider.AudioCodec()
	if audioCodec == "" {
		slog.Info("WS: recorder has no audio", "camera_id", id)
		return
	}

	sampleRate := provider.AudioSampleRate()
	channels := provider.AudioChannels()

	// For G.711, determine μ-law vs A-law from config bytes
	muLaw := false
	if audioCodec == "g711" {
		config := provider.AudioConfig()
		if len(config) > 0 && config[0] == 1 {
			muLaw = true
		}
	}

	if err := h.wsMgr.SetAudioInfo(id, audioCodec, muLaw, sampleRate, channels); err != nil {
		slog.Warn("WS: failed to set audio info", "camera_id", id, "error", err)
	} else {
		slog.Info("WS: audio configured", "camera_id", id, "codec", audioCodec, "muLaw", muLaw, "rate", sampleRate, "channels", channels)
	}
}
