package api

// Flow-path observability endpoints (#469 Phase 2): a go2rtc-style live view
// of every camera's frame pipeline — producer → StreamHub → consumers — plus
// per-protocol viewer counts. The heavy data comes from StreamHub.Snapshot()
// (atomics, no hot-path cost); the frontend derives fps/bitrate by diffing
// cumulative counters across polls.

import (
	"net/http"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/go-chi/chi/v5"
)

// FlowCamera is one camera's flow-path snapshot: the hub stats plus the
// identity/quality context the flow view renders around them.
type FlowCamera struct {
	model.HubStats
	Name     string         `json:"name"`
	Status   string         `json:"status"`
	Protocol string         `json:"protocol,omitempty"`
	Encoding string         `json:"encoding,omitempty"`
	Width    int            `json:"width,omitempty"`
	Height   int            `json:"height,omitempty"`
	Viewers  map[string]int `json:"viewers"`
	// LastFrameAgeS is seconds since the hub's last video frame, or nil when
	// the camera never delivered one. Unlike the cumulative frames_in/bytes_in
	// counters, this is an INSTANT staleness signal: a camera that died
	// mid-stream still shows frames_in > 0 (historical total), so external
	// threshold checks (last_frame_age_s > N ⇒ stream stalled) need this
	// field (#490 — field-validation feedback).
	LastFrameAgeS *float64 `json:"last_frame_age_s"`
}

// registerFlowRoutes registers the flow-path observability endpoints (#469).
func (h *Handler) registerFlowRoutes(r chi.Router) {
	r.Get("/api/streams", h.handleListStreams)
}

// handleListStreams serves GET /api/streams — the flow-path snapshot for every
// camera that currently has a StreamHub (pull recorders + push publishers).
func (h *Handler) handleListStreams(w http.ResponseWriter, r *http.Request) {
	if h.camMgr == nil {
		writeJSON(w, http.StatusOK, map[string]any{"streams": []FlowCamera{}})
		return
	}
	hubs := h.camMgr.Hubs()
	statuses := h.camMgr.Status()

	result := make([]FlowCamera, 0, len(hubs))
	for cameraID, hub := range hubs {
		result = append(result, h.buildFlowCamera(cameraID, hub, statuses[cameraID]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"streams": result})
}

// handleCameraFlow serves GET /api/cameras/{id}/flow — the flow-path snapshot
// for a single camera. 404 when no hub exists (camera offline / never started).
func (h *Handler) handleCameraFlow(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")
	if h.camMgr == nil {
		http.Error(w, "no active stream hub", http.StatusNotFound)
		return
	}
	hub := h.camMgr.Hubs()[cameraID]
	if hub == nil {
		http.Error(w, "no active stream hub", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, h.buildFlowCamera(cameraID, hub, h.camMgr.CameraStatus(cameraID)))
}

// buildFlowCamera assembles the flow view for one camera. Resolution is parsed
// on this cold path (per API call) — never on the frame hot path.
func (h *Handler) buildFlowCamera(cameraID string, hub *model.StreamHub, status model.RecorderStatus) FlowCamera {
	fc := FlowCamera{
		HubStats: hub.Snapshot(),
		Status:   string(status),
		Viewers:  map[string]int{},
	}
	// Instant staleness signal (#490): nil = never had a frame.
	if !fc.LastFrameAt.IsZero() {
		age := time.Since(fc.LastFrameAt).Seconds()
		if age < 0 {
			age = 0
		}
		fc.LastFrameAgeS = &age
	}
	if cfg := h.camMgr.GetCameraConfig(cameraID); cfg != nil {
		fc.Name = cfg.Name
		fc.Protocol = string(cfg.Protocol)
		fc.Encoding = cfg.Encoding
	}
	if codec, sps, _, _ := getCodecParams(h.camMgr.GetRecorder(cameraID)); len(sps) > 0 {
		var width, height int
		if codec == model.FormatH265 {
			width, height, _ = merge.ParseHEVCSPSResolution(sps)
		} else {
			width, height, _ = merge.ParseSPSResolution(sps)
		}
		fc.Width, fc.Height = width, height
	}
	if h.wsMgr != nil {
		fc.Viewers["ws"] = h.wsMgr.ViewerCount(cameraID)
	}
	if h.flvMgr != nil {
		fc.Viewers["flv"] = h.flvMgr.ViewerCount(cameraID)
	}
	if h.webrtcMgr != nil {
		fc.Viewers["webrtc"] = h.webrtcMgr.PeerCount(cameraID)
	}
	if h.hlsMgr != nil && h.hlsMgr.IsActive(cameraID) {
		fc.Viewers["hls"] = 1 // HLS muxer active; per-client tracking is CDN-shaped
	}
	return fc
}
