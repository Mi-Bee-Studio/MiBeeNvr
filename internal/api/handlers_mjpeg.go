package api

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
)

var mjpegLogger = slog.Default().With("component", "mjpeg-proxy")

// handleMjpegStreamURL returns the MJPEG stream URL for a camera.
// GET /api/cameras/{id}/stream.mjpeg
//
// Instead of proxying (which would compete with the recorder for ESP32's limited
// HTTP connections), this returns the direct MJPEG URL as JSON. The frontend
// <img> tag connects directly to the camera using its own browser connection pool.
//
// Response: {"url": "http://192.168.63.225:80/stream"}
func (h *Handler) handleMjpegStream(w http.ResponseWriter, r *http.Request) {
	cameraID := r.PathValue("id")
	if cameraID == "" {
		writeError(w, http.StatusBadRequest, "camera ID required")
		return
	}

	mjpegURL := h.resolveMjpegURL(cameraID)
	if mjpegURL == "" {
		writeError(w, http.StatusNotFound, "MJPEG stream not available for this camera")
		return
	}

	mjpegLogger.Debug("returning MJPEG stream URL", "camera_id", cameraID, "url", mjpegURL)
	writeJSON(w, http.StatusOK, map[string]string{"url": mjpegURL})
}

// resolveMjpegURL finds the MJPEG stream URL for a camera.
func (h *Handler) resolveMjpegURL(cameraID string) string {
	if h.camMgr == nil {
		return ""
	}

	// Try to get URL from the recorder delegate
	rec := h.camMgr.GetRecorder(cameraID)
	if rec != nil {
		if url := getMjpegURLFromRecorder(rec); url != "" {
			return url
		}
	}

	// Fallback: use camera's configured URL for HTTP protocol cameras
	cam := h.camMgr.GetCameraConfig(cameraID)
	if cam != nil && cam.Protocol == "http" && strings.HasPrefix(cam.URL, "http") {
		return cam.URL
	}

	return ""
}

// getMjpegURLFromRecorder extracts the MJPEG URL from a recorder by unwrapping delegates.
func getMjpegURLFromRecorder(rec interface{}) string {
	// Unwrap delegate layers (ONVIF → inner recorder)
	for {
		type delegater interface { Delegate() model.Recorder }
		if u, ok := rec.(delegater); ok {
			if d := u.Delegate(); d != nil {
				rec = d
				continue
			}
		}
		break
	}

	// Check HTTPJPEGRecorder
	if httpRec, ok := rec.(*recorder.HTTPJPEGRecorder); ok {
		return httpRec.StreamURL()
	}

	// Check MJPEGRecorder (RTSP MJPEG — no HTTP URL available)
	return ""
}

// handleLatestFrame returns the most recently captured JPEG frame from the recorder.
// GET /api/cameras/{id}/latest-frame
//
// This is used by the frontend for MJPEG live preview via snapshot polling.
// The recorder captures frames continuously; this endpoint returns the latest one
// without connecting to the camera (avoids ESP32 connection exhaustion).
//
// Response: raw JPEG image (Content-Type: image/jpeg)
// or 404 if no frame is available.
func (h *Handler) handleLatestFrame(w http.ResponseWriter, r *http.Request) {
	cameraID := r.PathValue("id")
	if cameraID == "" {
		writeError(w, http.StatusBadRequest, "camera ID required")
		return
	}

	frame := h.getLatestFrameFromRecorder(cameraID)
	if frame == nil {
		writeError(w, http.StatusNotFound, "no frame available")
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(frame)))
	w.Write(frame)
}

// getLatestFrameFromRecorder extracts the latest JPEG frame from a camera's recorder.
func (h *Handler) getLatestFrameFromRecorder(cameraID string) []byte {
	if h.camMgr == nil {
		return nil
	}

	rec := h.camMgr.GetRecorder(cameraID)
	if rec == nil {
		return nil
	}

	// Unwrap delegate layers (ONVIF → inner recorder)
	for {
		type delegater interface { Delegate() model.Recorder }
		if u, ok := rec.(delegater); ok {
			if d := u.Delegate(); d != nil {
				rec = d
				continue
			}
		}
		break
	}

	type latestFramer interface { LatestFrame() []byte }
	if lr, ok := rec.(latestFramer); ok {
		return lr.LatestFrame()
	}
	return nil
}
