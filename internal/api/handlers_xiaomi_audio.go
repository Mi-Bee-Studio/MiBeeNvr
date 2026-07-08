// SPDX-License-Identifier: MIT
//
// Two-way audio REST + WebSocket handlers for Xiaomi cameras.

package api

import (
	"log/slog"
	"net/http"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/xiaomi"
	"github.com/go-chi/chi/v5"
)

var xiaomiAudioLogger = slog.Default().With("component", "xiaomi-audio")

// startTwoWayAudioResponse is returned on successful two-way audio start.
type startTwoWayAudioResponse struct {
	SpeakerCodec uint32 `json:"speaker_codec"` // MISS codec ID for speaker output
}

// handleStartTwoWayAudio handles POST /api/cameras/{id}/two-way-audio/start.
func (h *Handler) handleStartTwoWayAudio(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rec := h.camMgr.GetRecorder(id)
	if rec == nil {
		WriteError(w, http.StatusNotFound, "camera not found")
		return
	}

	recorder, ok := rec.(*xiaomi.XiaomiRecorder)
	if !ok {
		WriteError(w, http.StatusBadRequest, "camera does not support two-way audio")
		return
	}

	if err := recorder.StartTwoWayAudio(); err != nil {
		xiaomiAudioLogger.Warn("failed to start two-way audio", "camera_id", id, "error", err)
		writeAPIError(w, http.StatusBadRequest, err)
		return
	}

	codec := recorder.SpeakerCodec()
	// Default to PCM (raw 16-bit LE) for unknown models.
	if codec == 0 {
		codec = missCodecPCM // 1024 — raw PCM passthrough
	}

	xiaomiAudioLogger.Info("two-way audio started", "camera_id", id, "speaker_codec", codec)
	writeJSON(w, http.StatusOK, startTwoWayAudioResponse{SpeakerCodec: codec})
}

// handleStopTwoWayAudio handles POST /api/cameras/{id}/two-way-audio/stop.
func (h *Handler) handleStopTwoWayAudio(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rec := h.camMgr.GetRecorder(id)
	if rec == nil {
		WriteError(w, http.StatusNotFound, "camera not found")
		return
	}

	recorder, ok := rec.(*xiaomi.XiaomiRecorder)
	if !ok {
		WriteError(w, http.StatusBadRequest, "camera does not support two-way audio")
		return
	}

	if err := recorder.StopTwoWayAudio(); err != nil {
		xiaomiAudioLogger.Warn("failed to stop two-way audio", "camera_id", id, "error", err)
		writeAPIError(w, http.StatusBadRequest, err)
		return
	}

	xiaomiAudioLogger.Info("two-way audio stopped", "camera_id", id)
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

// handleAudioUpstreamWS handles GET /api/ws/camera/{id}/audio-upstream.
// Upgrades to WebSocket and reads binary frames containing PCM audio data
// from the browser, encodes to the appropriate G.711 codec, and sends to
// the Xiaomi camera.
//
// WS binary frame format:
//
//	[0]          reserved byte (unused)
//	[1..640]     PCM 16-bit signed LE audio data (320 samples @ 8kHz = 40ms)
//
// The PCM data is encoded to G.711 μ-law or A-law depending on the camera's
// speaker codec, or passed through raw for PCM codec (1024).
func (h *Handler) handleAudioUpstreamWS(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rec := h.camMgr.GetRecorder(id)
	if rec == nil {
		WriteError(w, http.StatusNotFound, "camera not found")
		return
	}

	recorder, ok := rec.(*xiaomi.XiaomiRecorder)
	if !ok {
		WriteError(w, http.StatusBadRequest, "camera does not support two-way audio")
		return
	}

	// Determine the speaker codec for this camera.
	speakerCodec := recorder.SpeakerCodec()
	if speakerCodec == 0 {
		speakerCodec = missCodecPCM // default to raw PCM
	}

	// Warn about Opus (unsupported via browser).
	if speakerCodec == missCodecOPUS {
		xiaomiAudioLogger.Warn("two-way audio: camera expects Opus, browser cannot encode Opus",
			"camera_id", id)
		WriteError(w, http.StatusBadRequest, "camera uses Opus codec which is not supported for browser-based two-way audio")
		return
	}

	xiaomiAudioLogger.Info("audio upstream starting", "camera_id", id, "speaker_codec", speakerCodec)

	// Use wsstream.Manager to handle the WebSocket lifecycle.
	err := h.wsMgr.AudioUpstream(w, r, func(msg []byte) error {
		return processAudioUpstreamMessage(recorder, speakerCodec, msg)
	})
	if err != nil {
		// WebSocket errors include normal client disconnects — log at debug level.
		xiaomiAudioLogger.Debug("audio upstream connection closed", "camera_id", id, "error", err)
	}
}

// processAudioUpstreamMessage handles one binary message from the audio upstream WS.
// msg[0] is reserved, msg[1:641] is PCM 16-bit LE audio data.
func processAudioUpstreamMessage(recorder *xiaomi.XiaomiRecorder, speakerCodec uint32, msg []byte) error {
	const expectedMsgLen = 641 // 1 reserved + 640 PCM bytes
	if len(msg) < expectedMsgLen {
		return nil // skip undersized frames silently
	}

	pcmData := msg[1:expectedMsgLen]

	switch speakerCodec {
	case missCodecPCM:
		// Raw PCM passthrough — send the 640 bytes (320×16-bit samples) as-is.
		return recorder.WriteAudioToCamera(missCodecPCM, pcmData)

	case missCodecPCMU, missCodecPCMA:
		// Encode PCM to μ-law or A-law (640 bytes PCM → 320 bytes G.711).
		var encoded []byte
		if speakerCodec == missCodecPCMU {
			encoded = xiaomi.EncodePCMToMuLaw(pcmData)
		} else {
			encoded = xiaomi.EncodePCMToALaw(pcmData)
		}
		return recorder.WriteAudioToCamera(speakerCodec, encoded)

	default:
		// Unsupported codec — skip silently.
		return nil
	}
}

// MISS codec ID constants duplicated here to avoid an import cycle
// (internal/api cannot import internal/xiaomi directly).
const (
	missCodecPCM  = 1024
	missCodecPCMU = 1026
	missCodecPCMA = 1027
	missCodecOPUS = 1032
)
