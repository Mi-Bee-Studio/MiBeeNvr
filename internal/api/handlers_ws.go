package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/substream"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/wsstream"
	"github.com/go-chi/chi/v5"
)

// --- WebSocket streaming endpoint ---

// handleStreamWS handles GET /api/cameras/{id}/stream/ws[?quality=main|sub]
// It upgrades the HTTP connection to a WebSocket and streams binary-encoded
// video frames (CodecInfo first, then VideoFrame messages).
// quality=sub (#513) registers the entry under the camera's "/sub" key and
// streams the on-demand sub-stream; it falls back to main when the camera has
// no usable sub-stream. Unlike FLV/HLS/WHEP there is no X-Stream-Quality
// response header here (the 101 upgrade writes its own headers) — the
// negotiated outcome is reported in-band as the first WS message (#541).
func (h *Handler) handleStreamWS(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Validate request parameters before service availability (a bad
	// quality= must not masquerade as "streaming unavailable").
	quality, qerr := parseQuality(r)
	if qerr != nil {
		WriteError(w, http.StatusBadRequest, qerr.Error())
		return
	}

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

	// quality=sub: acquire the on-demand pull and hold the reference for the
	// whole ServeWS lifetime (the handler blocks until the viewer leaves).
	// acquireSub stamps X-Stream-Quality for the pre-upgrade error paths, but
	// the 101 upgrade response discards headers — the negotiated outcome is
	// re-sent to the client in-band as the first WS message (#541).
	key := id
	servedQuality := qualityMain
	var subSrc *substream.Source
	if quality == qualitySub && h.camMgr != nil {
		subSrc = h.acquireSub(w, r, id)
		if subSrc != nil {
			key = subKey(id)
			servedQuality = qualitySub
			defer h.camMgr.ReleaseSubStream(id)
		}
	}

	// On-demand registration: if WebSocket stream not registered, register it.
	// An already-active entry may be subscribed to a STALE StreamHub (the
	// recorder reconnected and got a fresh hub — or the sub-stream puller was
	// recycled and restarted) — rebind before serving, or the viewer would
	// sit on a dead hub with zero frames forever.
	if h.wsMgr.IsActive(key) && h.camMgr != nil {
		if subSrc != nil {
			if h.wsMgr.ActiveHub(key) != subSrc.Hub() {
				h.wsMgr.RebindHub(key, subSrc.Hub())
			}
		} else if rec := h.camMgr.GetRecorder(id); rec != nil {
			if hub := getStreamHub(rec); hub != nil && hub != h.wsMgr.ActiveHub(id) {
				h.wsMgr.RebindHub(id, hub)
			}
		}
	}
	if !h.wsMgr.IsActive(key) {
		if h.camMgr == nil {
			WriteError(w, http.StatusNotFound, "WebSocket stream not active")
			return
		}

		var codec model.Format
		var sps, pps, vps []byte
		var hub *streamhub.StreamHub
		if subSrc != nil {
			// Sub-stream: parameters come from the pull's SDP/in-band
			// snapshot; the main recorder need not be running at all.
			codec, sps, pps, vps = subSrc.CodecParams()
			hub = subSrc.Hub()
			if codec != model.FormatH264 && codec != model.FormatH265 {
				// Pull came up without usable video parameters (still
				// warming up) — poll briefly like the main path below.
				const wsCodecWait = 5 * time.Second
				const wsCodecPoll = 200 * time.Millisecond
				deadline := time.Now().Add(wsCodecWait)
				for (codec != model.FormatH264 && codec != model.FormatH265) ||
					sps == nil || pps == nil {
					if time.Now().After(deadline) {
						slog.Warn("WS: timed out waiting for sub-stream codec params", "camera_id", id)
						WriteError(w, http.StatusServiceUnavailable, "waiting for video stream")
						return
					}
					time.Sleep(wsCodecPoll)
					codec, sps, pps, vps = subSrc.CodecParams()
				}
			}
		} else {
			rec := h.camMgr.GetRecorder(id)
			if rec == nil {
				slog.Warn("WS: recorder not running", "camera_id", id)
				WriteError(w, http.StatusBadRequest, "camera recorder not running")
				return
			}

			codec, sps, pps, vps = getCodecParams(rec)
			// Normalize JPEG/MJPEG codec names for wsstream: both "jpeg" and "mjpeg"
			// map to wsstream.CodecMJPEG ("mjpeg") since the wire protocol treats
			// them identically (complete JPEG frames in VideoFrame.NALUs[0]).
			if codec == model.EncJPEG {
				codec = model.FormatMJPEG
			}
			slog.Info("WS: on-demand register", "camera_id", id, "codec", codec, "has_sps", sps != nil, "has_pps", pps != nil)
			// MJPEG cameras don't have SPS/PPS — skip the keyframe wait.
			if codec != model.FormatMJPEG {
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
			hub = getStreamHub(rec)
		}

		if err := h.wsMgr.RegisterStream(key, codec, sps, pps, vps, hub); err != nil {
			if !errors.Is(err, wsstream.ErrStreamExists) {
				slog.Error("WS: failed to register", "camera_id", id, "key", key, "error", err)
				WriteError(w, http.StatusInternalServerError, "failed to register WebSocket stream")
				return
			}
		}
		// Configure audio streaming if the recorder provides audio
		// (main-stream entries only — the sub-stream pull is video-only).
		if subSrc == nil {
			if rec := h.camMgr.GetRecorder(id); rec != nil {
				setupAudioForWS(h, id, rec)
			}
		}
	}

	slog.Info("WS: serving", "camera_id", id, "key", key, "quality", servedQuality)

	// Serve WebSocket stream (blocks until client disconnects). servedQuality
	// is emitted as the first in-band message so clients detect sub→main
	// fallback even though the 101 upgrade response drops headers (#541).
	if err := h.wsMgr.ServeWS(key, servedQuality, w, r); err != nil {
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

	// For G.711, determine μ-law vs A-law from config bytes.
	// For AAC, AudioConfig() returns the AudioSpecificConfig (AASC), which the
	// client needs to configure its WebCodecs AudioDecoder.
	muLaw := false
	var audioConfig []byte
	if audioCodec == "g711" {
		config := provider.AudioConfig()
		if len(config) > 0 && config[0] == 1 {
			muLaw = true
		}
	} else if audioCodec == "aac" {
		audioConfig = provider.AudioConfig()
	}

	if err := h.wsMgr.SetAudioInfo(id, audioCodec, muLaw, sampleRate, channels, audioConfig); err != nil {
		slog.Warn("WS: failed to set audio info", "camera_id", id, "error", err)
	} else {
		slog.Info("WS: audio configured", "camera_id", id, "codec", audioCodec, "muLaw", muLaw, "rate", sampleRate, "channels", channels, "config_len", len(audioConfig))
	}
}
