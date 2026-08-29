package api

// Flow-path observability endpoints (#469 Phase 2): a go2rtc-style live view
// of every camera's frame pipeline — producer → StreamHub → consumers — plus
// per-protocol viewer counts. The heavy data comes from StreamHub.Snapshot()
// (atomics, no hot-path cost); the frontend derives fps/bitrate by diffing
// cumulative counters across polls.

import (
	"context"
	"net/http"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
	"github.com/go-chi/chi/v5"
)

// FlowCamera is one camera's flow-path snapshot: the hub stats plus the
// identity/quality context the flow view renders around them.
type FlowCamera struct {
	streamhub.HubStats
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
	// Recording is the recording-branch snapshot (#480): current segment
	// progress, frameCh ring-buffer water level, rolling-merge backlog.
	// Nil when the camera's recorder doesn't expose stats (e.g. MJPEG/ONVIF
	// delegates) or no recorder is running.
	Recording *recorder.RecordingStats `json:"recording,omitempty"`
	// MergePending is the number of segments waiting in the rolling-merge
	// queue for this camera (#480; mirrors nvr_merge_pending_segments).
	// Omitted when the merge manager isn't wired.
	MergePending *int `json:"merge_pending,omitempty"`
	// FLVClockMs is the wallclock base (unix ms) the FLV tag StreamID
	// ingest deltas are measured from — players combine it with the delta
	// to compute end-to-end live latency (#481). 0/omitted = no FLV entry.
	FLVClockMs int64 `json:"flv_clock_ms,omitempty"`
	// Sub is the camera's on-demand sub-stream branch (#513): the low-res
	// pull serving "流畅" viewers and the sub-layer analysis recorder (#514).
	// Nil when the camera has no live sub-stream entry (never acquired, or
	// idle recycled).
	Sub *FlowSub `json:"sub,omitempty"`
}

// FlowSub mirrors FlowCamera for the sub-stream pull. HubStats carries the
// sub hub's counters (frames_in/bytes_in/per-consumer fan-out) so the flow
// tree renders both tiers with the same machinery.
type FlowSub struct {
	streamhub.HubStats
	State string       `json:"state"`
	Codec model.Format `json:"codec,omitempty"`
	Refs  int          `json:"refs"`
	// LastFrameAgeS is seconds since the sub hub's last frame; nil when it
	// never delivered one. A stale age while State is "live" is the early
	// signal of a stalled camera-side second encoder.
	LastFrameAgeS *float64 `json:"last_frame_age_s,omitempty"`
}

// recordingStatsProvider is implemented by recorders embedding baseRecorder
// (H.264/H.265) — the flow handler degrades gracefully for the rest.
type recordingStatsProvider interface {
	RecordingStats() recorder.RecordingStats
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

	// One merge-backlog query for the whole listing — buildFlowCamera would
	// otherwise hit the DB once per camera on every poll.
	var mergeCounts map[string]int
	if h.mergeMgr != nil {
		mergeCounts = h.mergeMgr.PendingCounts(r.Context())
	}

	result := make([]FlowCamera, 0, len(hubs))
	for cameraID, hub := range hubs {
		result = append(result, h.buildFlowCamera(cameraID, hub, statuses[cameraID], mergeCounts))
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
	writeJSON(w, http.StatusOK, h.buildFlowCamera(cameraID, hub, h.camMgr.CameraStatus(cameraID), nil))
}

// buildFlowCamera assembles the flow view for one camera. Resolution is parsed
// on this cold path (per API call) — never on the frame hot path.
func (h *Handler) buildFlowCamera(cameraID string, hub *streamhub.StreamHub, status model.RecorderStatus, mergeCounts map[string]int) FlowCamera {
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
	// Recording branch (#480): segment progress + ring-buffer watermark from
	// the live recorder, merge backlog from the merge manager.
	if h.camMgr != nil {
		if rec := h.camMgr.GetRecorder(cameraID); rec != nil {
			if provider, ok := unwrapDelegate(rec).(recordingStatsProvider); ok {
				stats := provider.RecordingStats()
				fc.Recording = &stats
			}
		}
	}
	if h.mergeMgr != nil {
		if mergeCounts == nil {
			// Single-camera path: fetch just this camera's backlog.
			mergeCounts = h.mergeMgr.PendingCounts(context.Background())
		}
		if n, ok := mergeCounts[cameraID]; ok {
			fc.MergePending = &n
		}
	}
	if h.wsMgr != nil {
		// WS/FLV egress registers sub-quality sessions under the suffixed
		// key (camID/sub) — sum both entries so the flow view's viewer
		// counts match reality. WHEP's PeerCount already sums both buckets.
		fc.Viewers["ws"] = h.wsMgr.ViewerCount(cameraID) + h.wsMgr.ViewerCount(subKey(cameraID))
	}
	if h.flvMgr != nil {
		fc.Viewers["flv"] = h.flvMgr.ViewerCount(cameraID) + h.flvMgr.ViewerCount(subKey(cameraID))
		fc.FLVClockMs = h.flvMgr.ClockMs(cameraID)
	}
	if h.webrtcMgr != nil {
		fc.Viewers["webrtc"] = h.webrtcMgr.PeerCount(cameraID)
	}
	if h.hlsMgr != nil && (h.hlsMgr.IsActive(cameraID) || h.hlsMgr.IsActive(subKey(cameraID))) {
		fc.Viewers["hls"] = 1 // HLS muxer active; per-client tracking is CDN-shaped
	}
	// Sub-stream branch (#513 observability): the flow tree previously
	// rendered only the main hub — the sub pull (its own hub + consumers
	// like the vision sub-layer recorder and sub-quality egress) was
	// invisible outside the journal.
	if h.camMgr != nil {
		if sm := h.camMgr.SubStreams(); sm != nil {
			if st := sm.Status(cameraID); st != nil {
				sub := &FlowSub{State: st.State, Codec: st.Codec, Refs: st.Refs}
				if hub := sm.Hub(cameraID); hub != nil {
					sub.HubStats = hub.Snapshot()
				}
				if !sub.LastFrameAt.IsZero() {
					age := time.Since(sub.LastFrameAt).Seconds()
					if age < 0 {
						age = 0
					}
					sub.LastFrameAgeS = &age
				}
				fc.Sub = sub
			}
		}
	}
	return fc
}
