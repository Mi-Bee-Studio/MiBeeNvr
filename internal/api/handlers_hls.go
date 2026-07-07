package api

import (
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/hls"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/xiaomi"
	"github.com/go-chi/chi/v5"
)

// --- HLS streaming endpoints ---

// subscribeHLS registers an HLS consumer on the recorder's StreamHub.
// It uses Hub.Subscribe with consumer ID "hls" so that the HLS manager
// receives frames via the fan-out architecture.
// It first unsubscribes any stale "hls" consumer left over from a previous
// session (e.g. after idle eviction), then subscribes with the new callback.
func subscribeHLS(hub *model.StreamHub, cameraID string, hlsMgr *hls.Manager, isH265 bool) error {
	if hub == nil {
		return nil // no hub, no subscription (shouldn't happen in practice)
	}
	hub.Unsubscribe("hls") // clean up stale consumer from previous session
	return hlsMgr.SubscribeToHub(cameraID, hub, isH265)
}

func (h *Handler) handleHLSStream(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if h.hlsMgr == nil || h.camMgr == nil {
		WriteError(w, http.StatusInternalServerError, "HLS not available")
		return
	}

	// Get camera to check protocol
	cam, err := h.db.GetCamera(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get camera")
		return
	}
	if cam == nil {
		WriteError(w, http.StatusNotFound, "camera not found")
		return
	}

	// If stream not active, start it
	if !h.hlsMgr.IsActive(id) {
		rec := h.camMgr.GetRecorder(id)
		if rec == nil {
			WriteError(w, http.StatusBadRequest, "camera recorder not running")
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
				WriteError(w, http.StatusServiceUnavailable, "SPS/PPS not available yet, waiting for video stream")
				return
			}

			err := h.hlsMgr.StartStream(id, sps, pps, hlsMaxFPS)
			if err != nil {
				if errors.Is(err, hls.ErrMaxStreamsReached) {
					writeAPIError(w, http.StatusServiceUnavailable, &model.HLSMaxStreamsError{})
				} else {
					logger.Error("failed to start HLS stream", "camera_id", id, "error", err)
					WriteError(w, http.StatusInternalServerError, "failed to start HLS stream")
				}
				return
			}

			// Check if sub-stream URL is configured
			if camCfg != nil && camCfg.SubStreamURL != "" {
				fallback := func() {
					_ = subscribeHLS(h264Rec.Hub, id, h.hlsMgr, false)
				}
				if subErr := h.hlsMgr.StartSubStreamReader(id, camCfg.SubStreamURL, false, fallback); subErr != nil {
					logger.Warn("failed to start HLS sub-stream reader, falling back to main stream", "camera_id", id, "error", subErr)
					fallback()
				}
				// Sub-stream reader is running — do NOT subscribe hub on recorder
			} else {
				_ = subscribeHLS(h264Rec.Hub, id, h.hlsMgr, false)
			}
		} else if h265Rec, ok := rec.(*recorder.H265Recorder); ok {
			vps := h265Rec.VPS()
			sps := h265Rec.SPS()
			pps := h265Rec.PPS()
			if vps == nil || sps == nil || pps == nil {
				WriteError(w, http.StatusServiceUnavailable, "VPS/SPS/PPS not available yet, waiting for video stream")
				return
			}

			err := h.hlsMgr.StartStreamH265(id, vps, sps, pps, hlsMaxFPS)
			if err != nil {
				if errors.Is(err, hls.ErrMaxStreamsReached) {
					writeAPIError(w, http.StatusServiceUnavailable, &model.HLSMaxStreamsError{})
				} else {
					logger.Error("failed to start HLS H265 stream", "camera_id", id, "error", err)
					WriteError(w, http.StatusInternalServerError, "failed to start HLS stream")
				}
				return
			}

			// Check if sub-stream URL is configured
			if camCfg != nil && camCfg.SubStreamURL != "" {
				fallback := func() {
					_ = subscribeHLS(h265Rec.Hub, id, h.hlsMgr, true)
				}
				if subErr := h.hlsMgr.StartSubStreamReader(id, camCfg.SubStreamURL, true, fallback); subErr != nil {
					logger.Warn("failed to start HLS sub-stream reader, falling back to main stream", "camera_id", id, "error", subErr)
					fallback()
				}
			} else {
				_ = subscribeHLS(h265Rec.Hub, id, h.hlsMgr, true)
			}
		} else if onvifRec, ok := rec.(*recorder.ONVIFRecorder); ok {
			// ONVIF recorder delegates to H264/H265 internally
			delegate := onvifRec.Delegate()
			if delegate == nil {
				WriteError(w, http.StatusServiceUnavailable, "ONVIF recorder delegate not available yet")
				return
			}
			// Unwrap the delegate and handle as H264/H265
			if h264Rec, ok := delegate.(*recorder.H264Recorder); ok {
				sps := h264Rec.SPS()
				pps := h264Rec.PPS()
				if sps == nil || pps == nil {
					WriteError(w, http.StatusServiceUnavailable, "SPS/PPS not available yet, waiting for video stream")
					return
				}
				err := h.hlsMgr.StartStream(id, sps, pps, hlsMaxFPS)
				if err != nil {
					if errors.Is(err, hls.ErrMaxStreamsReached) {
						writeAPIError(w, http.StatusServiceUnavailable, &model.HLSMaxStreamsError{})
					} else {
						WriteError(w, http.StatusInternalServerError, "failed to start HLS stream")
					}
					return
				}
				_ = subscribeHLS(h264Rec.Hub, id, h.hlsMgr, false)
			} else if h265Rec, ok := delegate.(*recorder.H265Recorder); ok {
				vps := h265Rec.VPS()
				sps := h265Rec.SPS()
				pps := h265Rec.PPS()
				if vps == nil || sps == nil || pps == nil {
					WriteError(w, http.StatusServiceUnavailable, "VPS/SPS/PPS not available yet, waiting for video stream")
					return
				}
				err := h.hlsMgr.StartStreamH265(id, vps, sps, pps, hlsMaxFPS)
				if err != nil {
					if errors.Is(err, hls.ErrMaxStreamsReached) {
						writeAPIError(w, http.StatusServiceUnavailable, &model.HLSMaxStreamsError{})
					} else {
						WriteError(w, http.StatusInternalServerError, "failed to start HLS stream")
					}
					return
				}
				_ = subscribeHLS(h265Rec.Hub, id, h.hlsMgr, true)
			} else {
				writeAPIError(w, http.StatusBadRequest, &model.HLSSupportedCodecError{CameraID: id})
				return
			}
		} else if provider, ok := rec.(model.HLSProvider); ok {
			codec, sps, pps, vps := provider.CodecParams()
			if sps == nil || pps == nil {
				WriteError(w, http.StatusServiceUnavailable, "codec params not ready yet, waiting for video stream")
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
						WriteError(w, http.StatusInternalServerError, "failed to start HLS stream")
					}
					return
				}
				_ = subscribeHLS(getRecorderHub(rec), id, h.hlsMgr, false)
			case model.FormatH265:
				if vps == nil {
					WriteError(w, http.StatusServiceUnavailable, "VPS not ready yet, waiting for video stream")
					return
				}
				err := h.hlsMgr.StartStreamH265(id, vps, sps, pps, hlsMaxFPS)
				if err != nil {
					if errors.Is(err, hls.ErrMaxStreamsReached) {
						writeAPIError(w, http.StatusServiceUnavailable, &model.HLSMaxStreamsError{})
					} else {
						logger.Error("failed to start HLS H265 stream", "camera_id", id, "error", err)
						WriteError(w, http.StatusInternalServerError, "failed to start HLS stream")
					}
					return
				}
				_ = subscribeHLS(getRecorderHub(rec), id, h.hlsMgr, true)
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
		WriteError(w, http.StatusServiceUnavailable, "HLS stream not available")
		return
	}
}

func (h *Handler) handleStopHLSStream(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if h.hlsMgr == nil {
		WriteError(w, http.StatusInternalServerError, "HLS not available")
		return
	}

	if !h.hlsMgr.IsActive(id) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "not active"})
		return
	}

	// Unsubscribe HLS consumer from StreamHub before stopping the stream
	if h.camMgr != nil {
		if rec := h.camMgr.GetRecorder(id); rec != nil {
			hub := getRecorderHub(rec)
			if hub != nil {
				hub.Unsubscribe("hls")
			}
		}
	}

	h.hlsMgr.StopStream(id)
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

// getRecorderHub extracts the StreamHub from any recorder type.
// Returns nil if the recorder doesn't have a Hub.
func getRecorderHub(rec model.Recorder) *model.StreamHub {
	switch r := rec.(type) {
	case *recorder.H264Recorder:
		return r.Hub
	case *recorder.H265Recorder:
		return r.Hub
	case *recorder.ONVIFRecorder:
		// ONVIF passes Hub to delegate, but we unsubscribe from the delegate's hub
		if delegate := r.Delegate(); delegate != nil {
			return getRecorderHub(delegate)
		}
		return r.Hub
	case *recorder.MJPEGRecorder:
		return r.Hub
	case *recorder.HTTPJPEGRecorder:
		return r.Hub
	case *xiaomi.XiaomiRecorder:
		return r.Hub
	case *recorder.IngestRecorder:
		// Push cameras (srt/rtmp): the hub is set by camera.initStreamHub,
		// same as every other recorder. Without this case, HLS would subscribe
		// to a nil hub and never receive the published frames.
		return r.Hub
	default:
		return nil
	}
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
