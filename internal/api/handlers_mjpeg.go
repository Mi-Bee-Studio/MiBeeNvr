package api

import (
	"fmt"
	"hash/crc32"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

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
// Response: {"url": "http://192.168.1.100:80/stream"}
func (h *Handler) handleMjpegStream(w http.ResponseWriter, r *http.Request) {
	cameraID := r.PathValue("id")
	if cameraID == "" {
		WriteError(w, http.StatusBadRequest, "camera ID required")
		return
	}

	mjpegURL := h.resolveMjpegURL(cameraID)
	if mjpegURL == "" {
		WriteError(w, http.StatusNotFound, "MJPEG stream not available for this camera")
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
		type delegater interface{ Delegate() model.Recorder }
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
// Supports ETag conditional requests: if the client sends If-None-Match with a
// matching ETag, a 304 Not Modified is returned (0 bytes body). This dramatically
// reduces bandwidth for polling when the frame hasn't changed.
//
// Response: raw JPEG image (Content-Type: image/jpeg)
// or 304 if the frame is unchanged (ETag match)
// or 404 if no frame is available.
func (h *Handler) handleLatestFrame(w http.ResponseWriter, r *http.Request) {
	cameraID := r.PathValue("id")
	if cameraID == "" {
		WriteError(w, http.StatusBadRequest, "camera ID required")
		return
	}

	frame := h.getLatestFrameFromRecorder(cameraID)
	if frame == nil {
		WriteError(w, http.StatusNotFound, "no frame available")
		return
	}

	// Generate ETag from frame content hash (CRC32 is fast and sufficient for
	// cache validation — not a security hash).
	etag := fmt.Sprintf("\"%x\"", crc32.ChecksumIEEE(frame))
	w.Header().Set("ETag", etag)

	// Check conditional request: if the client already has this frame, skip the body.
	if match := r.Header.Get("If-None-Match"); match == etag {
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Length", strconv.Itoa(len(frame)))
	w.Write(frame)
}

// latestFrameCacheTTL bounds how long a decoded snapshot serves repeated
// latest-frame polls before re-capturing. Var so tests can manipulate expiry.
var latestFrameCacheTTL = 10 * time.Second

// getLatestFrameFromRecorder extracts the latest JPEG frame from a camera's
// recorder. JPEG-family recorders answer from their frame cache; everything
// else (H.264/H.265, or no live recorder) goes through the FFmpeg-gated
// snapshot capturer with the shared snapshot TTL cache (#657).
func (h *Handler) getLatestFrameFromRecorder(cameraID string) []byte {
	if h.camMgr != nil {
		if rec := h.camMgr.GetRecorder(cameraID); rec != nil {
			// Unwrap delegate layers (ONVIF → inner recorder)
			for {
				type delegater interface{ Delegate() model.Recorder }
				if u, ok := rec.(delegater); ok {
					if d := u.Delegate(); d != nil {
						rec = d
						continue
					}
				}
				break
			}

			type latestFramer interface{ LatestFrame() []byte }
			if lr, ok := rec.(latestFramer); ok {
				return lr.LatestFrame()
			}
		}
	}
	return h.getDecodedLatestFrame(cameraID)
}

// getDecodedLatestFrame captures one frame via the snapshot capturer (hub IDR
// decode → device snapshot URL) and caches it. Any failure degrades to nil —
// the endpoint answers 404, never 500 (FFmpeg stays optional).
func (h *Handler) getDecodedLatestFrame(cameraID string) []byte {
	if h.snapCapturer == nil {
		return nil
	}

	h.snapshotMu.RLock()
	cached, ok := h.snapshots[cameraID]
	h.snapshotMu.RUnlock()
	if ok && time.Since(cached.timestamp) < latestFrameCacheTTL {
		return cached.data
	}

	frame, err := h.snapCapturer.Capture(cameraID)
	if err != nil || len(frame) == 0 {
		mjpegLogger.Debug("latest-frame capture unavailable", "camera_id", cameraID, "error", err)
		return nil
	}

	h.snapshotMu.Lock()
	h.snapshots[cameraID] = &snapshotCache{data: frame, timestamp: time.Now()}
	h.snapshotMu.Unlock()
	return frame
}
