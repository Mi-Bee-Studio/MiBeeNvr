package api


import (
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/hls"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/go-chi/chi/v5"
)

// --- HLS streaming endpoints ---

func (h *Handler) handleHLSStream(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if h.hlsMgr == nil || h.camMgr == nil {
		writeError(w, http.StatusInternalServerError, "HLS not available")
		return
	}

	// Get camera to check protocol
	cam, err := h.db.GetCamera(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get camera")
		return
	}
	if cam == nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}

	// If stream not active, start it
	if !h.hlsMgr.IsActive(id) {
		rec := h.camMgr.GetRecorder(id)
		if rec == nil {
			writeError(w, http.StatusBadRequest, "camera recorder not running")
			return
		}

		// Get camera config for HLS options
		camCfg := h.camMgr.GetCameraConfig(id)
		hlsMaxFPS := 0
		if camCfg != nil {
			hlsMaxFPS = camCfg.HLSMaxFPS
		}

		// Try H264 recorder first
		if h264Rec, ok := rec.(*recorder.H264Recorder); ok {
			sps := h264Rec.SPS()
			pps := h264Rec.PPS()
			if sps == nil || pps == nil {
				writeError(w, http.StatusServiceUnavailable, "SPS/PPS not available yet, waiting for video stream")
				return
			}

			err := h.hlsMgr.StartStream(id, sps, pps, hlsMaxFPS)
			if err != nil {
				if errors.Is(err, hls.ErrMaxStreamsReached) {
					writeAPIError(w, http.StatusServiceUnavailable, &model.HLSMaxStreamsError{})
				} else {
					logger.Error("failed to start HLS stream", "camera_id", id, "error", err)
					writeError(w, http.StatusInternalServerError, "failed to start HLS stream")
				}
				return
			}

			// Check if sub-stream URL is configured
			if camCfg != nil && camCfg.SubStreamURL != "" {
				if subErr := h.hlsMgr.StartSubStreamReader(id, camCfg.SubStreamURL, false); subErr != nil {
					logger.Warn("failed to start HLS sub-stream reader, falling back to main stream", "camera_id", id, "error", subErr)
					// Fall back to main stream OnHLSFrame
					h264Rec.OnHLSFrame = func(pts int64, au [][]byte) {
						_ = h.hlsMgr.WriteH264(id, pts, au)
					}
				}
				// Sub-stream reader is running — do NOT set OnHLSFrame on recorder
			} else {
				h264Rec.OnHLSFrame = func(pts int64, au [][]byte) {
					_ = h.hlsMgr.WriteH264(id, pts, au)
				}
			}
		} else if h265Rec, ok := rec.(*recorder.H265Recorder); ok {
			vps := h265Rec.VPS()
			sps := h265Rec.SPS()
			pps := h265Rec.PPS()
			if vps == nil || sps == nil || pps == nil {
				writeError(w, http.StatusServiceUnavailable, "VPS/SPS/PPS not available yet, waiting for video stream")
				return
			}

			err := h.hlsMgr.StartStreamH265(id, vps, sps, pps, hlsMaxFPS)
			if err != nil {
				if errors.Is(err, hls.ErrMaxStreamsReached) {
					writeAPIError(w, http.StatusServiceUnavailable, &model.HLSMaxStreamsError{})
				} else {
					logger.Error("failed to start HLS H265 stream", "camera_id", id, "error", err)
					writeError(w, http.StatusInternalServerError, "failed to start HLS stream")
				}
				return
			}

			// Check if sub-stream URL is configured
			if camCfg != nil && camCfg.SubStreamURL != "" {
				if subErr := h.hlsMgr.StartSubStreamReader(id, camCfg.SubStreamURL, true); subErr != nil {
					logger.Warn("failed to start HLS sub-stream reader, falling back to main stream", "camera_id", id, "error", subErr)
					// Fall back to main stream OnHLSFrame
					h265Rec.OnHLSFrame = func(pts int64, au [][]byte) {
						_ = h.hlsMgr.WriteH265(id, pts, au)
					}
				}
			} else {
				h265Rec.OnHLSFrame = func(pts int64, au [][]byte) {
					_ = h.hlsMgr.WriteH265(id, pts, au)
				}
			}
		} else if onvifRec, ok := rec.(*recorder.ONVIFRecorder); ok {
			// ONVIF recorder delegates to H264/H265 internally
			delegate := onvifRec.Delegate()
			if delegate == nil {
				writeError(w, http.StatusServiceUnavailable, "ONVIF recorder delegate not available yet")
				return
			}
			// Unwrap the delegate and handle as H264/H265
			if h264Rec, ok := delegate.(*recorder.H264Recorder); ok {
				sps := h264Rec.SPS()
				pps := h264Rec.PPS()
				if sps == nil || pps == nil {
					writeError(w, http.StatusServiceUnavailable, "SPS/PPS not available yet, waiting for video stream")
					return
				}
				err := h.hlsMgr.StartStream(id, sps, pps, hlsMaxFPS)
				if err != nil {
					if errors.Is(err, hls.ErrMaxStreamsReached) {
						writeAPIError(w, http.StatusServiceUnavailable, &model.HLSMaxStreamsError{})
					} else {
						writeError(w, http.StatusInternalServerError, "failed to start HLS stream")
					}
					return
				}
				h264Rec.OnHLSFrame = func(pts int64, au [][]byte) {
					_ = h.hlsMgr.WriteH264(id, pts, au)
				}
			} else if h265Rec, ok := delegate.(*recorder.H265Recorder); ok {
				vps := h265Rec.VPS()
				sps := h265Rec.SPS()
				pps := h265Rec.PPS()
				if vps == nil || sps == nil || pps == nil {
					writeError(w, http.StatusServiceUnavailable, "VPS/SPS/PPS not available yet, waiting for video stream")
					return
				}
				err := h.hlsMgr.StartStreamH265(id, vps, sps, pps, hlsMaxFPS)
				if err != nil {
					if errors.Is(err, hls.ErrMaxStreamsReached) {
						writeAPIError(w, http.StatusServiceUnavailable, &model.HLSMaxStreamsError{})
					} else {
						writeError(w, http.StatusInternalServerError, "failed to start HLS stream")
					}
					return
				}
				h265Rec.OnHLSFrame = func(pts int64, au [][]byte) {
					_ = h.hlsMgr.WriteH265(id, pts, au)
				}
			} else {
				writeAPIError(w, http.StatusBadRequest, &model.HLSSupportedCodecError{CameraID: id})
				return
			}
		} else if provider, ok := rec.(model.HLSProvider); ok {
			codec, sps, pps, vps := provider.CodecParams()
			if sps == nil || pps == nil {
				writeError(w, http.StatusServiceUnavailable, "codec params not ready yet, waiting for video stream")
				return
			}
			switch codec {
			case model.FormatH264:
				err := h.hlsMgr.StartStream(id, sps, pps, hlsMaxFPS)
				if err != nil {
					if errors.Is(err, hls.ErrMaxStreamsReached) {
					writeAPIError(w, http.StatusServiceUnavailable, &model.HLSMaxStreamsError{})
				} else {
					logger.Error("failed to start HLS stream", "camera_id", id, "error", err)
					writeError(w, http.StatusInternalServerError, "failed to start HLS stream")
				}
					return
				}
				provider.SetOnHLSFrame(func(pts int64, au [][]byte) {
					_ = h.hlsMgr.WriteH264(id, pts, au)
				})
			case model.FormatH265:
				if vps == nil {
					writeError(w, http.StatusServiceUnavailable, "VPS not ready yet, waiting for video stream")
					return
				}
				err := h.hlsMgr.StartStreamH265(id, vps, sps, pps, hlsMaxFPS)
				if err != nil {
					if errors.Is(err, hls.ErrMaxStreamsReached) {
					writeAPIError(w, http.StatusServiceUnavailable, &model.HLSMaxStreamsError{})
				} else {
					logger.Error("failed to start HLS H265 stream", "camera_id", id, "error", err)
					writeError(w, http.StatusInternalServerError, "failed to start HLS stream")
				}
					return
				}
				provider.SetOnHLSFrame(func(pts int64, au [][]byte) {
					_ = h.hlsMgr.WriteH265(id, pts, au)
				})
			default:
				writeAPIError(w, http.StatusBadRequest, &model.HLSSupportedCodecError{CameraID: id})
				return
			}
		} else {
			writeAPIError(w, http.StatusBadRequest, &model.HLSSupportedCodecError{CameraID: id})
			return
		}
	}
	// Proxy to muxer handler
	if !h.hlsMgr.Handle(id, w, r) {
		writeError(w, http.StatusServiceUnavailable, "HLS stream not available")
		return
	}
}

func (h *Handler) handleStopHLSStream(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if h.hlsMgr == nil {
		writeError(w, http.StatusInternalServerError, "HLS not available")
		return
	}

	if !h.hlsMgr.IsActive(id) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "not active"})
		return
	}

	h.hlsMgr.StopStream(id)
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

// --- Snapshot endpoint ---

func (h *Handler) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")

	// Find camera config to get SnapshotURL
	var snapshotURL string
	if h.config != nil {
		for _, cam := range h.config.Cameras {
			if cam.ID == cameraID {
				snapshotURL = cam.SnapshotURL
				break
			}
		}
	}
	if snapshotURL == "" {
		http.Error(w, "Snapshot URL not configured", http.StatusNotFound)
		return
	}

	// Check cache (10 second TTL)
	const cacheTTL = 10 * time.Second
	h.snapshotMu.RLock()
	cached, ok := h.snapshots[cameraID]
	h.snapshotMu.RUnlock()

	if ok && time.Since(cached.timestamp) < cacheTTL {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "max-age=5")
		w.Write(cached.data)
		return
	}

	// Fetch from camera
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(snapshotURL)
	if err != nil {
		// Return stale cache if available
		if ok {
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set("X-Cache", "stale")
			w.Write(cached.data)
			return
		}
		logger.Warn("failed to fetch snapshot", "camera_id", cameraID, "error", err)
		http.Error(w, "Failed to fetch snapshot", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		http.Error(w, "Camera returned error", http.StatusBadGateway)
		return
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024)) // 10MB max
	if err != nil || len(data) == 0 {
		http.Error(w, "Failed to read snapshot", http.StatusBadGateway)
		return
	}

	// Update cache
	h.snapshotMu.Lock()
	h.snapshots[cameraID] = &snapshotCache{data: data, timestamp: time.Now()}
	h.snapshotMu.Unlock()

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "max-age=5")
	w.Write(data)
}
