package streamsapi

import (
	"errors"
	"net/http"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/flv"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/substream"
	"github.com/go-chi/chi/v5"
)

// --- HTTP-FLV streaming endpoint ---

// handleFLVStream handles GET /api/cameras/{id}/stream.flv[?quality=main|sub]
// It streams FLV data via HTTP chunked transfer encoding.
// quality=sub (#513) registers the entry under the camera's "/sub" key and
// streams the on-demand sub-stream; it falls back to main when the camera has
// no usable sub-stream (X-Stream-Quality response header reports the outcome).
func (s *server) handleFLVStream(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	quality, qerr := parseQuality(r)
	if qerr != nil {
		WriteError(w, http.StatusBadRequest, qerr.Error())
		return
	}

	if s.deps.FLV == nil {
		WriteError(w, http.StatusServiceUnavailable, "FLV streaming not available")
		return
	}

	cam, err := s.deps.DB.GetCamera(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get camera")
		return
	}
	if cam == nil {
		WriteError(w, http.StatusNotFound, "camera not found")
		return
	}

	// quality=sub: acquire the on-demand pull and hold the reference for the
	// whole ServeFLV lifetime (the handler blocks until the viewer leaves).
	key := id
	var subSrc *substream.Source
	if quality == qualitySub && s.deps.Cam != nil {
		subSrc = s.acquireSub(w, r, id)
		if subSrc != nil {
			key = subKey(id)
			defer s.deps.Cam.ReleaseSubStream(id)
		}
	}

	// On-demand registration: if FLV stream not registered, register it
	if !s.deps.FLV.IsActive(key) {
		if s.deps.Cam == nil {
			WriteError(w, http.StatusNotFound, "FLV stream not active")
			return
		}

		var codec model.Format
		var sps, pps, vps []byte
		var hub *streamhub.StreamHub
		if subSrc != nil {
			codec, sps, pps, vps = subSrc.CodecParams()
			hub = subSrc.Hub()
			if (codec != model.FormatH264 && codec != model.FormatH265) || sps == nil || pps == nil {
				WriteError(w, http.StatusServiceUnavailable, "waiting for video stream")
				return
			}
		} else {
			rec := s.deps.Cam.GetRecorder(id)
			if rec == nil {
				WriteError(w, http.StatusBadRequest, "camera recorder not running")
				return
			}

			codec, sps, pps, vps = recorder.CodecParamsOf(rec)
			if sps == nil || pps == nil {
				WriteError(w, http.StatusServiceUnavailable, "waiting for video stream")
				return
			}
			hub = recorder.HubOf(rec)
		}

		if err := s.deps.FLV.RegisterStream(key, codec, sps, pps, vps, hub); err != nil {
			if !errors.Is(err, flv.ErrStreamExists) {
				logger.Error("failed to register FLV stream", "camera_id", id, "key", key, "error", err)
				WriteError(w, http.StatusInternalServerError, "failed to register FLV stream")
				return
			}
		}
	} else if subSrc != nil && s.deps.FLV.ActiveHub(key) != subSrc.Hub() {
		// Active entry subscribed to a recycled puller's dead hub — rebind.
		s.deps.FLV.RebindHub(key, subSrc.Hub())
	}

	// Arm the AAC audio leg when the camera provides AAC params (#1066):
	// idempotent and retried on every request, so a camera whose audio
	// config arrives after the first viewer still gets armed by the next
	// one. Sub-streams are video-only (#513) — main quality only.
	if quality != qualitySub && s.deps.Cam != nil {
		if ci := s.deps.Cam.GetCodecInfo(id); ci.AudioCodec == "aac" && len(ci.AudioConfig) > 0 {
			s.deps.FLV.RegisterAudioConfig(key, ci.AudioConfig, ci.AudioSampleRate, ci.AudioChannels)
		}
	}

	// Serve FLV stream (blocks until client disconnects)
	if err := s.deps.FLV.ServeFLV(key, w, r); err != nil {
		if errors.Is(err, flv.ErrStreamNotActive) {
			WriteError(w, http.StatusNotFound, "FLV stream not active")
			return
		}
		if errors.Is(err, flv.ErrMaxViewers) {
			WriteError(w, http.StatusServiceUnavailable, "maximum FLV viewers reached")
			return
		}
		// Client disconnect or write error — log at debug level, not an error
		logger.Debug("FLV stream ended", "camera_id", id, "error", err)
	}
}
