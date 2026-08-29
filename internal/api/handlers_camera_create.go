package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
)

// gb28181ChannelPayload is the API shape of a camera's GB28181 binding.
type gb28181ChannelPayload struct {
	DeviceID     string `json:"device_id"`
	ChannelID    string `json:"channel_id"`
	Manufacturer string `json:"manufacturer,omitempty"`
}

// createBodyHost extracts the host IP from a create-camera body's URL or
// ONVIF endpoint ("" when neither carries one). Feeds the cross-protocol
// dedup against registered GB28181 devices.

// createBodyHost extracts the host IP from a create-camera body's URL or
// ONVIF endpoint ("" when neither carries one). Feeds the cross-protocol
// dedup against registered GB28181 devices.
func createBodyHost(rawURL, onvifEndpoint string) string {
	for _, raw := range []string{strings.TrimSpace(rawURL), strings.TrimSpace(onvifEndpoint)} {
		if raw == "" {
			continue
		}
		if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
			return u.Hostname()
		}
		if host, _, err := net.SplitHostPort(raw); err == nil && host != "" {
			return host
		}
	}
	return ""
}

func (p *gb28181ChannelPayload) toConfig() config.GB28181ChannelConfig {
	return config.GB28181ChannelConfig{
		DeviceID:     p.DeviceID,
		ChannelID:    p.ChannelID,
		Manufacturer: p.Manufacturer,
	}
}

func (p *gb28181ChannelPayload) toConfigPtr() *config.GB28181ChannelConfig {
	if p == nil {
		return nil
	}
	cfg := p.toConfig()
	return &cfg
}

func (h *Handler) handleCreateCamera(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name          string `json:"name"`
		Protocol      string `json:"protocol"`
		URL           string `json:"url"`
		Username      string `json:"username"`
		Password      string `json:"password"`
		Enabled       *bool  `json:"enabled"`
		Description   string `json:"description"`
		Location      string `json:"location"`
		Brand         string `json:"brand"`
		Model         string `json:"model"`
		SerialNumber  string `json:"serial_number"`
		ONVIFEndpoint string `json:"onvif_endpoint"`
		ProfileToken  string `json:"profile_token"`
		// Sub-stream (#512): manual sub profile token + manual sub stream URL.
		SubProfileToken string                        `json:"sub_profile_token"`
		SubStreamURL    string                        `json:"sub_stream_url"`
		StreamEncoding  string                        `json:"stream_encoding"`
		Encoding        string                        `json:"encoding"`
		Timelapse       *config.CameraTimelapseConfig `json:"timelapse"`
		Channel         string                        `json:"channel"`
		AudioEnabled    *bool                         `json:"audio_enabled"`
		// Keep the camera's real audio track in recorded segments (default off).
		AudioInRecordings *bool `json:"audio_in_recordings"`
		// Recording gate: false = live-only (no segments written). nil = record.
		RecordingEnabled *bool `json:"recording_enabled"`
		// Cascade gate: false = hidden from the GB28181 cascade catalog and
		// INVITEs refused. nil = default (exposed).
		CascadeEnabled *bool `json:"cascade_enabled"`
		// Cascade tier: forward the sub-stream instead of main (#512).
		CascadeSubStream bool `json:"cascade_sub_stream"`
		// Recording mode (#435): ""/"continuous" or "adaptive" (+ tuning).
		RecordingMode string                          `json:"recording_mode"`
		Adaptive      *config.AdaptiveRecordingConfig `json:"adaptive"`
		// Audio trigger (#478): loudness input for adaptive recording.
		AudioTrigger *config.CameraAudioTriggerConfig `json:"audio_trigger"`
		// Push/ingest fields (SRT/RTMP)
		StreamKey     string `json:"stream_key"`
		SRTPassphrase string `json:"srt_passphrase"`
		SRTStreamID   string `json:"srt_stream_id"`
		// Push-out relay targets + retention
		PushTargets       []config.PushTargetConfig `json:"push_targets"`
		PushRetentionDays *int                      `json:"push_retention_days"`
		// IP self-healing: stable hardware ID (ONVIF serial) + candidate subnets.
		StableID    string   `json:"stable_id"`
		SubnetHints []string `json:"subnet_hints"`
		// GB28181 SIP device/channel binding (protocol "gb28181")
		GB28181 *gb28181ChannelPayload `json:"gb28181"`
		// AllowDuplicate opts in to adding a pull camera (onvif/rtsp/http)
		// whose host IP already belongs to a registered GB28181 device.
		// Default false: one camera per physical device.
		AllowDuplicate bool `json:"allow_duplicate"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Name == "" {
		WriteError(w, http.StatusBadRequest, "name is required")
		return
	}
	if isGBCamera := body.Protocol == "gb28181"; isGBCamera {
		if body.GB28181 == nil || strings.TrimSpace(body.GB28181.DeviceID) == "" || strings.TrimSpace(body.GB28181.ChannelID) == "" {
			WriteError(w, http.StatusBadRequest, "gb28181 device_id and channel_id are required for gb28181 cameras")
			return
		}
	}
	if body.Protocol == "" {
		WriteError(w, http.StatusBadRequest, "protocol is required")
		return
	}
	if !validProtocols[body.Protocol] {
		WriteError(w, http.StatusBadRequest, fmt.Sprintf("invalid protocol %q, must be one of: rtsp, http, onvif, srt, rtmp, whip, xiaomi, timelapse, gb28181", body.Protocol))
		return
	}
	// Push/ingest cameras (srt/rtmp/whip): no URL — the publisher connects to us.
	isPush := body.Protocol == "srt" || body.Protocol == "rtmp" || body.Protocol == "whip"
	// GB28181 cameras: no URL — identified by SIP DeviceID/ChannelID.
	isGB28181 := body.Protocol == "gb28181"
	// Cross-protocol dedup: a pull camera (onvif/rtsp/http) whose host IP
	// matches a registered GB28181 device is the same physical camera — the
	// GB28181 auto-enroll skips it symmetrically. Refuse unless the caller
	// explicitly opts in (dual-protocol setups) with allow_duplicate.
	if !isPush && !isGB28181 && h.gb28181DeviceMgr != nil {
		if host := createBodyHost(body.URL, body.ONVIFEndpoint); host != "" {
			conflict := ""
			for _, d := range h.gb28181DeviceMgr.AllDevices() {
				d.Mu.RLock()
				netAddr := d.NetAddr
				d.Mu.RUnlock()
				devHost := netAddr
				if h2, _, err := net.SplitHostPort(netAddr); err == nil && h2 != "" {
					devHost = h2
				}
				if devHost == host {
					conflict = fmt.Sprintf("a GB28181 device (%s) is already connected from %s", d.ID, host)
					break
				}
			}
			// L2: dual-NIC devices register GB28181 from a different interface
			// IP than the ONVIF endpoint. Probe the create-host's ONVIF serial
			// and match it against cached GB28181 device fingerprints. Skipped
			// entirely when no fingerprints exist (no dual-protocol device has
			// ever registered) so normal setups pay no probe latency.
			if conflict == "" && h.db != nil {
				if fps, err := h.db.ListGB28181Fingerprints(r.Context()); err == nil && len(fps) > 0 {
					if serial, ok := onvif.ProbeSerial(r.Context(), host); ok {
						for _, fp := range fps {
							if fp.Serial == serial {
								conflict = fmt.Sprintf("a GB28181 device (%s) with the same hardware serial (%s) is already connected", fp.DeviceID, serial)
								break
							}
						}
					}
				}
			}
			if conflict != "" {
				if !body.AllowDuplicate {
					logger.Warn("create camera refused: same physical camera already connected via GB28181", "host", host)
					WriteError(w, http.StatusConflict, conflict+
						" — this looks like the same physical camera. Use it as-is, remove/archive it first, or pass allow_duplicate=true to keep both")
					return
				}
				logger.Info("create camera: allow_duplicate set despite GB28181 dedup match", "host", host)
			}
		}
	}
	// ONVIF cameras: accept url OR onvif_endpoint
	if body.Protocol == "onvif" {
		endpoint := body.ONVIFEndpoint
		if endpoint == "" {
			endpoint = body.URL
		}
		if endpoint == "" {
			WriteError(w, http.StatusBadRequest, "url or onvif_endpoint is required for ONVIF cameras")
			return
		}
		body.ONVIFEndpoint = endpoint
		body.URL = "" // Don't store in url field for ONVIF
		// Check for duplicate ONVIF endpoint
		if h.db != nil {
			existingCams, _ := h.db.ListCameras(r.Context())
			for _, ec := range existingCams {
				if ec.Protocol == "onvif" && ec.ONVIFEndpoint == body.ONVIFEndpoint {
					WriteError(w, http.StatusConflict, "ONVIF camera with this endpoint already exists")
					return
				}
			}
		}
	} else if !isPush && !isGB28181 && body.URL == "" {
		WriteError(w, http.StatusBadRequest, "url is required")
		return
	}
	// Validate URL format for cameras that have one (not ONVIF, not push, not gb28181)
	if body.Protocol != "onvif" && !isPush && !isGB28181 && body.URL != "" && !validateURL(body.URL) {
		WriteError(w, http.StatusBadRequest, "invalid URL format")
		return
	}
	// Sub-stream URL must be RTSP when provided (#512).
	if body.SubStreamURL != "" && !strings.HasPrefix(body.SubStreamURL, "rtsp://") && !strings.HasPrefix(body.SubStreamURL, "rtsps://") {
		WriteError(w, http.StatusBadRequest, "sub_stream_url must be an rtsp:// or rtsps:// URL")
		return
	}
	// 0.10.0+: combined protocol strings are no longer accepted.
	proto := body.Protocol
	enc := body.Encoding
	if strings.Contains(proto, "_") {
		WriteError(w, http.StatusBadRequest,
			fmt.Sprintf("protocol %q: combined format is no longer supported; use separate protocol and encoding fields", proto))
		return
	}
	// Set default encoding if still empty
	if enc == "" {
		switch proto {
		case "rtsp":
			enc = "h264"
		case "http":
			enc = "jpeg"
		case "srt", "rtmp", "whip":
			// Push cameras: encoding derived from the published stream (H.264 default).
			enc = "h264"
		case "gb28181":
			// Hint only — the recorder detects the real codec from PS stream NALUs.
			enc = "h264"
		case "xiaomi":
			// Hint only — the Xiaomi recorder probes H264/H265 from the MISS
			// stream at runtime; the list/get probe backfill corrects the label
			// for H.265 devices. Without this default the UI's deliberate
			// encoding omission (auto-detect protocols send none) persisted an
			// empty encoding that failed startup validation fatally (#402).
			enc = "h264"
		case "onvif":
			// Auto-detect encoding from ONVIF device profiles
			if body.StreamEncoding == "" {
				if detected := probeONVIFEncoding(r.Context(), body.ONVIFEndpoint, body.Username, body.Password); detected != "" {
					body.StreamEncoding = detected
					enc = strings.ToLower(detected)
					logger.Info("auto-detected ONVIF encoding", "camera", body.Name, "encoding", enc)
				}
			} else {
				enc = strings.ToLower(body.StreamEncoding)
			}
		}
	}
	// Guard the API write boundary with the same protocol+encoding validation
	// the startup path enforces: an invalid combo saved here (e.g. http+h264
	// from a non-UI client) bricks the NEXT restart with a fatal config
	// validation error (#402 bug class).
	if err := model.ValidateProtocolEncoding(proto, enc); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Same boundary validation for the recording mode (#435, #402 class).
	if err := config.ValidateCameraRecordingMode(config.CameraConfig{
		ID:            body.Name,
		Encoding:      enc,
		RecordingMode: body.RecordingMode,
		Adaptive:      body.Adaptive,
	}); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	cam := config.CameraConfig{
		Name:              body.Name,
		Protocol:          proto,
		Encoding:          enc,
		URL:               body.URL,
		Username:          body.Username,
		Password:          body.Password,
		ONVIFEndpoint:     body.ONVIFEndpoint,
		ProfileToken:      body.ProfileToken,
		SubProfileToken:   body.SubProfileToken,
		SubStreamURL:      body.SubStreamURL,
		StreamEncoding:    body.StreamEncoding,
		Timelapse:         body.Timelapse,
		Channel:           body.Channel,
		AudioEnabled:      body.AudioEnabled != nil && *body.AudioEnabled,
		AudioInRecordings: body.AudioInRecordings != nil && *body.AudioInRecordings,
		RecordingEnabled:  body.RecordingEnabled,
		CascadeEnabled:    body.CascadeEnabled,
		CascadeSubStream:  body.CascadeSubStream,
		RecordingMode:     body.RecordingMode,
		Adaptive:          body.Adaptive,
		AudioTrigger:      body.AudioTrigger,
		StreamKey:         body.StreamKey,
		SRTPassphrase:     body.SRTPassphrase,
		SRTStreamID:       body.SRTStreamID,
		PushTargets:       body.PushTargets,
		PushRetentionDays: body.PushRetentionDays,
		StableID:          body.StableID,
		SubnetHints:       body.SubnetHints,
	}
	if body.GB28181 != nil {
		cam.GB28181 = body.GB28181.toConfig()
	}

	// Reject a dirty stable_id at the API boundary so it can't be frozen as the
	// camera's permanent identity (which would break rediscovery — see #216).
	// An empty stable_id is allowed (ONVIF cameras auto-populate it later).
	if strings.TrimSpace(cam.StableID) != "" && !config.IsValidStableID(cam.StableID) {
		WriteError(w, http.StatusBadRequest, fmt.Sprintf("stable_id %q is not a valid hardware identity (must be 3–64 chars of [A-Za-z0-9:_-], not all-same-character; rejects IPs, URLs, all-zero MACs)", cam.StableID))
		return
	}

	if h.camMgr == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	id, err := h.camMgr.AddCamera(r.Context(), cam)
	if err != nil {
		var cae *model.CameraAlreadyExistsError
		if errors.As(err, &cae) {
			writeAPIError(w, http.StatusConflict, err)
		} else {
			logger.Error("failed to add camera", "camera_id", id, "error", err, "path", r.URL.Path)
			WriteError(w, http.StatusInternalServerError, "failed to add camera")
		}
		return
	}

	// Notify merge scheduler if camera has timelapse config
	if body.Timelapse != nil && body.Timelapse.MergeDuration != "" && h.mergeScheduler != nil {
		if dur, err := config.ParseMergeDuration(body.Timelapse.MergeDuration); err == nil {
			h.mergeScheduler.AddOrUpdate(id, dur)
		}
	}
	// Persist DB-only metadata fields
	if body.Description != "" || body.Location != "" || body.Brand != "" || body.Model != "" || body.SerialNumber != "" {
		if err := h.db.UpdateCameraMetadata(r.Context(), id, body.Description, body.Location, body.Brand, body.Model, body.SerialNumber, 0); err != nil {
			logger.Warn("failed to set camera metadata", "camera_id", id, "error", err)
		}
	}
	// Persist push/ingest fields for srt/rtmp cameras.
	if isPush {
		if err := h.db.UpsertCameraIngest(r.Context(), id, body.StreamKey, body.SRTPassphrase, body.SRTStreamID); err != nil {
			logger.Warn("failed to set camera ingest fields", "camera_id", id, "error", err)
		}
	}
	// Return CameraRow with status
	row, _ := h.db.GetCamera(r.Context(), id)
	if row != nil {
		if h.camMgr != nil {
			row.Status = h.camMgr.CameraStatus(id)
		}
		// Inject last_seen from DB
		lastSeen, err := h.db.GetLastRecordingTime(r.Context(), id)
		if err == nil {
			row.LastSeen = lastSeen
		}
		cameraRowForAPI(row)
		writeJSON(w, http.StatusCreated, row)
	} else {
		cam.ID = id
		writeJSON(w, http.StatusCreated, cam)
	}
}
