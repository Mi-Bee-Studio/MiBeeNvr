package streamsapi

import (
	"net/http"
	"strings"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/rtsp"
	"github.com/go-chi/chi/v5"
)

// CameraProtocolsResponse is the response for GET /api/cameras/{id}/protocols.
type CameraProtocolsResponse struct {
	Protocols []ProtocolDetail `json:"protocols"`
	Encoding  string           `json:"encoding"`
	Default   string           `json:"default"`
	// RTSP exposes the built-in RTSP output server pull URL (#522) — the
	// address third-party platforms (Synology etc.) fill in as a camera
	// source. Nil when the server is disabled.
	RTSP *RtspEndpointDetail `json:"rtsp,omitempty"`
	// SubStream reports the camera's sub-stream capability (#512): where a
	// lower-resolution secondary feed exists (or can be configured), for
	// consumers like grid preview, cascade and external AI push.
	SubStream SubStreamDetail `json:"sub_stream"`
}

// RtspEndpointDetail describes the RTSP pull endpoint for one camera.
type RtspEndpointDetail struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	URL       string `json:"url,omitempty"`
}

// SubStreamDetail describes the sub-stream capability of one camera.
type SubStreamDetail struct {
	Available bool   `json:"available"`
	Source    string `json:"source,omitempty"` // "onvif" (auto-discovered) | "manual" (URL set)
	Reason    string `json:"reason,omitempty"` // why not, when unavailable
	// Codec is the sub-stream's ACTUAL codec once a pull has come up — it can
	// differ from the main stream's (devices switching codec families between
	// profiles are common). Empty until first observed; consumers gate on it
	// when the decode path is codec-sensitive (e.g. MSE H.265 browsers).
	Codec string `json:"codec,omitempty"`
}

// handleCameraProtocols handles GET /api/cameras/{id}/protocols.
// It returns the available streaming protocols for a specific camera
// based on its encoding and the registered stream handlers.
func (s *server) handleCameraProtocols(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	cam, err := s.deps.DB.GetCamera(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get camera")
		return
	}
	if cam == nil {
		WriteError(w, http.StatusNotFound, "camera not found")
		return
	}

	encoding := strings.ToLower(cam.Encoding)
	if encoding == "" {
		encoding = strings.ToLower(cam.StreamEncoding)
	}

	// Probe the running recorder for the actual codec. The recorder reads the
	// REAL codec from the live stream (RTSP DESCRIBE for ONVIF/RTSP, MISS
	// CodecID for Xiaomi, etc.), which is authoritative over the DB-stored
	// encoding. The stored encoding may be stale or wrong — e.g. Xiaomi CS2
	// cameras that were initially configured as h264 but actually stream h265
	// (the recorder detects codec=h265, but DB still says h264). Without this
	// probe, /protocols returns the wrong encoding, the frontend picks protocols
	// that can't handle the actual codec (WebRTC for H.265, FLV without MSE
	// H.265), and EVERY protocol renders black.
	//
	// Previously this probe was gated to ONVIF + empty-encoding only, out of
	// fear that a runtime probe might mislabel a correctly-configured camera.
	// But the recorder's codec detection is reliable (it reads actual stream
	// data), and the cost of a wrong DB encoding (all-protocols-black-screen)
	// is far worse than an occasional over-correction. Trust the recorder.
	if s.deps.Cam != nil {
		if rec := s.deps.Cam.GetRecorder(id); rec != nil {
			if codec, _, _, _ := recorder.CodecParamsOf(rec); codec != "" {
				encoding = string(codec)
			}
		}
	}

	var protocols []ProtocolDetail
	if s.deps.Registry != nil {
		protocols = s.deps.Registry.ProtocolsDetailForCodec(model.Format(encoding))
	}
	if protocols == nil {
		protocols = []ProtocolDetail{}
	}

	// Determine default protocol by latency-optimal fallback order. The global
	// streaming.default_protocol config was removed — the frontend Player
	// Orchestrator now auto-selects per camera based on this hint + browser
	// capability, demoting on health failure. Per-camera overrides remain via
	// the Protocol Switcher on each camera's LiveView page.
	defaultProto := ""
	for _, preferred := range []string{"webrtc", "flv", "ll-hls", "hls", "mjpeg"} {
		for _, p := range protocols {
			if p.Protocol == preferred && p.Available {
				defaultProto = preferred
				break
			}
		}
		if defaultProto != "" {
			break
		}
	}

	writeJSON(w, http.StatusOK, CameraProtocolsResponse{
		Protocols: protocols,
		Encoding:  encoding,
		Default:   defaultProto,
		RTSP:      s.rtspEndpointFor(r, id, model.Format(encoding)),
		SubStream: s.SubStreamDetailFor(id),
	})
}

// SubStreamDetailFor derives the camera's sub-stream capability (#512) from
// its protocol and configured/discovered sub fields. "Available" means a
// consumer can request a sub stream today; unavailable reasons guide the UI.
func (s *server) SubStreamDetailFor(cameraID string) SubStreamDetail {
	var cam *config.CameraConfig
	if s.deps.Cam != nil {
		cam = s.deps.Cam.GetCameraConfig(cameraID)
	}
	if cam == nil {
		return SubStreamDetail{Reason: "camera config unavailable"}
	}
	switch {
	case cam.SubStreamURL != "":
		return s.withSubStreamCodec(SubStreamDetail{Available: true, Source: "manual"}, cameraID)
	case cam.SubProfileToken != "":
		return s.withSubStreamCodec(SubStreamDetail{Available: true, Source: "onvif"}, cameraID)
	case cam.Protocol == string(model.ProtoGB28181) && strings.TrimSpace(cam.GB28181.SubChannelID) != "":
		return s.withSubStreamCodec(SubStreamDetail{Available: true, Source: "gb-sub-channel"}, cameraID)
	}
	switch cam.Protocol {
	case "onvif":
		// Discovery runs once after the recorder comes online; until then (or
		// on single-profile cameras) there is no sub stream to consume.
		return SubStreamDetail{Reason: "no secondary ONVIF profile discovered (single-stream camera, or discovery pending)"}
	case "rtsp_h264", "rtsp_mjpeg", string(model.ProtoRTSP):
		return SubStreamDetail{Reason: "set sub_stream_url to use this camera's sub stream"}
	case "gb28181":
		// Prober (#560) runs once per channel per boot after the catalog
		// merges (vendor-gated in auto mode); no code = device has no
		// usable sub channel — degrade to main, never an error state.
		return SubStreamDetail{Reason: "no sub-channel probed (device has no vendor-convention sub stream, or probe pending)"}
	default:
		// xiaomi (proprietary single stream), srt/rtmp push (publisher owns
		// the encode), http_jpeg/mjpeg (already low-bitrate stills).
		return SubStreamDetail{Reason: "protocol does not expose a sub stream"}
	}
}

// withSubStreamCodec decorates an availability verdict with the puller's
// observed codec when a sub-stream source is currently alive — the /protocols
// consumer (SPA quality gating) uses it to predict decodability before
// requesting quality=sub.
func (s *server) withSubStreamCodec(d SubStreamDetail, cameraID string) SubStreamDetail {
	if !d.Available || s.deps.Cam == nil {
		return d
	}
	for _, st := range s.deps.Cam.SubStreams().Snapshot() {
		if st.CameraID == cameraID && st.Codec != "" {
			d.Codec = string(st.Codec)
			break
		}
	}
	return d
}

// rtspEndpointFor builds the RTSP output-server entry for the protocols
// response (#522): available when the server is enabled and the camera's codec
// is servable (H.264/H.265/MJPEG). The URL targets the requesting host (with
// configured credentials embedded, #686) so it is directly copy-pasteable.
func (s *server) rtspEndpointFor(r *http.Request, cameraID string, codec model.Format) *RtspEndpointDetail {
	if s.deps.Config == nil || s.deps.Config.Server.RTSP.Enabled == nil || !*s.deps.Config.Server.RTSP.Enabled {
		return nil
	}
	detail := &RtspEndpointDetail{}
	switch codec {
	case model.FormatH264, model.FormatH265, model.FormatMJPEG:
		// Parameter readiness mirrors the RTSP server's own gate — an idle or
		// warming-up camera still gets the URL (clients retry DESCRIBE).
		detail.Available = true
		detail.URL = rtsp.URLForAuth(r.Host, s.deps.Config.Server.RTSP.Port, cameraID,
			s.deps.Config.Server.RTSP.Username, s.deps.Config.Server.RTSP.Password)
	default:
		detail.Reason = "codec not servable over RTSP (H.264/H.265/MJPEG only)"
	}
	return detail
}
