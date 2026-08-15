package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/transcoding"
	"github.com/go-chi/chi/v5"
)

// --- Camera and stats endpoints ---

// cameraRowForAPI normalizes camera rows for API responses.
// For ONVIF cameras, it exposes onvif_endpoint as url so the frontend
// can use a single url field for all protocols.
func cameraRowForAPI(row *storage.CameraRow) {
	if row.Protocol == "onvif" && row.URL == "" && row.ONVIFEndpoint != "" {
		row.URL = row.ONVIFEndpoint
	}
}

// injectYAMLConfigFields overlays the per-camera YAML-config fields that are NOT
// stored in (or not read by) the DB query onto a CameraRow. The DB is the source
// of truth for core columns (name/protocol/encoding/url/status/…), but several
// fields live only in mibee-nvr.yaml (transcoding, channel, audio_enabled, push
// targets, recording gate, dark-frame filter, recording schedule, stable_id,
// subnet hints, ingest keys). Without this overlay the API response is missing
// them — which previously made PUT /api/cameras/{id} return a row with
// push_targets/recording_enabled absent and audio_enabled at its zero value
// (false), so any caller trusting the PUT response (instead of re-GETting) saw
// its just-saved data "missing" (issue #297).
//
// cfg may be nil (no-op). If the camera ID is not found in cfg.Cameras the row
// is returned unchanged. Callers: handleListCameras, handleGetCamera,
// handleUpdateCamera — all three now share this so list/get/update agree.
func injectYAMLConfigFields(row *storage.CameraRow, cfg *config.Config) {
	if cfg == nil {
		return
	}
	for _, cam := range cfg.Cameras {
		if cam.ID != row.ID {
			continue
		}
		if cam.Transcoding != nil {
			row.Transcoding = cam.Transcoding
		}
		if cam.Channel != "" {
			row.Channel = cam.Channel
		}
		row.AudioEnabled = cam.AudioEnabled
		row.StreamKey = cam.StreamKey
		row.SRTPassphrase = cam.SRTPassphrase
		row.SRTStreamID = cam.SRTStreamID
		row.PushTargets = cam.PushTargets
		row.PushRetentionDays = cam.PushRetentionDays
		row.StableID = cam.StableID
		row.SubnetHints = cam.SubnetHints
		row.DarkFrameFilterEnabled = cam.DarkFrameFilterEnabled
		row.DarkFrameThreshold = cam.DarkFrameThreshold
		row.RecordingEnabled = cam.RecordingEnabled
		row.RecordingSchedule = cam.RecordingSchedule
		if cam.Protocol == "gb28181" {
			gb := cam.GB28181
			row.GB28181 = &gb
		}
		return
	}
}

func (h *Handler) handleListCameras(w http.ResponseWriter, r *http.Request) {
	cameras, err := h.db.ListCameras(r.Context())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to list cameras")
		return
	}
	if cameras == nil {
		cameras = []storage.CameraRow{}
	}
	// Inject recorder status from CameraManager
	if h.camMgr != nil {
		statusMap := h.camMgr.Status()
		for i := range cameras {
			if s, ok := statusMap[cameras[i].ID]; ok {
				cameras[i].Status = s
			} else {
				cameras[i].Status = model.StatusStopped
			}
		}
		// Inject error details from CameraManager
		if h.camMgr != nil {
			for i := range cameras {
				if detail := h.camMgr.GetErrorDetail(cameras[i].ID); detail != nil {
					cameras[i].ErrorType = &detail.Type
					cameras[i].ErrorDetail = &detail.Message
				}
			}
		}
		// Inject last_seen from DB
		lastSeenMap, err := h.db.GetAllLastRecordingTimes(r.Context())
		if err == nil {
			for i := range cameras {
				if t, ok := lastSeenMap[cameras[i].ID]; ok {
					cameras[i].LastSeen = t
				}
			}
		}
	}
	// Inject per-camera transcoding config, channel, audio_enabled, and push
	// fields from config (not all stored in DB columns read by ListCameras).
	for i := range cameras {
		injectYAMLConfigFields(&cameras[i], h.config)
	}
	// For ONVIF cameras, show onvif_endpoint as url for unified frontend handling
	for i := range cameras {
		cameraRowForAPI(&cameras[i])
	}
	// Backfill encoding for cameras whose stored encoding is empty (e.g. ONVIF
	// Resolve each camera's displayed encoding from the LIVE recorder probe so
	// the list agrees with /protocols and LiveView (single source of truth). The
	// probe is authoritative: some cameras physically stream a different codec
	// than their stored config (e.g. Xiaomi H.265 cameras whose DB still says
	// h264, or ONVIF auto-detect devices). When the probe is empty (camera
	// offline / recorder not started / codec not yet detected) we keep the
	// stored value as a fallback so the UI still has something to render and
	// doesn't regress to the #112 black-screen-on-empty-encoding failure.
	//
	// For protocol-configured cameras (rtsp/http/srt/rtmp) the recorder is
	// constructed from the stored encoding, so the probe returns the SAME codec
	// — the displayed value is unchanged. For auto-detect cameras (onvif/xiaomi)
	// the probe is the real codec and the stored value may be stale or empty;
	// showing the probe here makes list/get/protocols consistent (#166).
	if h.camMgr != nil {
		for i := range cameras {
			cameras[i].Encoding = resolveEncoding(cameras[i].Encoding, probeCodec(h.camMgr, cameras[i].ID))
		}
	}
	// Summary view: return only the fields needed for grid/dashboard display.
	// Reduces response body ~60% for pages that only show status badges.
	// Supports ETag conditional requests: 304 Not Modified when status unchanged.
	if r.URL.Query().Get("view") == "summary" {
		type cameraSummary struct {
			ID          string  `json:"id"`
			Name        string  `json:"name"`
			Status      string  `json:"status"`
			Encoding    string  `json:"encoding,omitempty"`
			Protocol    string  `json:"protocol,omitempty"`
			IsRecording bool    `json:"is_recording"`
			LastSeen    *string `json:"last_seen,omitempty"`
			ErrorCode   *string `json:"error_code,omitempty"`
		}
		summaries := make([]cameraSummary, len(cameras))
		// Build ETag from camera status signatures (cheap — no JSON serialization).
		var etagHash uint32
		for i, c := range cameras {
			summaries[i] = cameraSummary{
				ID:          c.ID,
				Name:        c.Name,
				Status:      string(c.Status),
				Encoding:    string(c.Encoding),
				Protocol:    c.Protocol,
				IsRecording: c.Status == model.StatusRecording && (c.RecordingEnabled == nil || *c.RecordingEnabled),
			}
			if c.LastSeen != nil && !c.LastSeen.IsZero() {
				ts := c.LastSeen.Format(time.RFC3339)
				summaries[i].LastSeen = &ts
				etagHash = crc32.Update(etagHash, crc32.IEEETable, []byte(ts))
			}
			if c.ErrorType != nil {
				summaries[i].ErrorCode = c.ErrorType
				etagHash = crc32.Update(etagHash, crc32.IEEETable, []byte(*c.ErrorType))
			}
			etagHash = crc32.Update(etagHash, crc32.IEEETable, []byte(c.ID+string(c.Status)))
		}
		etag := fmt.Sprintf(`"s%d-%d"`, len(summaries), etagHash)
		w.Header().Set("ETag", etag)
		if match := r.Header.Get("If-None-Match"); match == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		writeJSON(w, http.StatusOK, summaries)
		return
	}
	// ETag for full list: based on camera count + status signatures (cheap).
	var fullEtagHash uint32
	for _, c := range cameras {
		fullEtagHash = crc32.Update(fullEtagHash, crc32.IEEETable, []byte(c.ID+string(c.Status)))
		if c.LastSeen != nil {
			fullEtagHash = crc32.Update(fullEtagHash, crc32.IEEETable, []byte(c.LastSeen.Format(time.RFC3339)))
		}
	}
	fullEtag := fmt.Sprintf(`"f%d-%d"`, len(cameras), fullEtagHash)
	w.Header().Set("ETag", fullEtag)
	if match := r.Header.Get("If-None-Match"); match == fullEtag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, http.StatusOK, cameras)
}

// --- Camera CRUD endpoints ---

var validProtocols = map[string]bool{
	// New transport-only protocols
	"rtsp":  true,
	"http":  true,
	"onvif": true,
	// Push/ingest protocols (publisher pushes to NVR)
	"srt":  true,
	"rtmp": true,
	// Plugin protocols
	"xiaomi":    true,
	"timelapse": true,
	// GB/T 28181 SIP-registered devices (no URL — identified by DeviceID/ChannelID)
	"gb28181": true,
}

// gb28181ChannelPayload is the API shape of a camera's GB28181 binding.
type gb28181ChannelPayload struct {
	DeviceID     string `json:"device_id"`
	ChannelID    string `json:"channel_id"`
	Manufacturer string `json:"manufacturer,omitempty"`
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
		Name           string                        `json:"name"`
		Protocol       string                        `json:"protocol"`
		URL            string                        `json:"url"`
		Username       string                        `json:"username"`
		Password       string                        `json:"password"`
		Enabled        *bool                         `json:"enabled"`
		Description    string                        `json:"description"`
		Location       string                        `json:"location"`
		Brand          string                        `json:"brand"`
		Model          string                        `json:"model"`
		SerialNumber   string                        `json:"serial_number"`
		ONVIFEndpoint  string                        `json:"onvif_endpoint"`
		ProfileToken   string                        `json:"profile_token"`
		StreamEncoding string                        `json:"stream_encoding"`
		Encoding       string                        `json:"encoding"`
		Timelapse      *config.CameraTimelapseConfig `json:"timelapse"`
		Channel        string                        `json:"channel"`
		AudioEnabled   *bool                         `json:"audio_enabled"`
		// Recording gate: false = live-only (no segments written). nil = record.
		RecordingEnabled *bool `json:"recording_enabled"`
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
		WriteError(w, http.StatusBadRequest, fmt.Sprintf("invalid protocol %q, must be one of: rtsp, http, onvif, srt, rtmp, xiaomi, timelapse, gb28181", body.Protocol))
		return
	}
	// Push/ingest cameras (srt/rtmp): no URL — the publisher connects to us.
	isPush := body.Protocol == "srt" || body.Protocol == "rtmp"
	// GB28181 cameras: no URL — identified by SIP DeviceID/ChannelID.
	isGB28181 := body.Protocol == "gb28181"
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
		case "srt", "rtmp":
			// Push cameras: encoding derived from the published stream (H.264 default).
			enc = "h264"
		case "gb28181":
			// Hint only — the recorder detects the real codec from PS stream NALUs.
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

	cam := config.CameraConfig{
		Name:              body.Name,
		Protocol:          proto,
		Encoding:          enc,
		URL:               body.URL,
		Username:          body.Username,
		Password:          body.Password,
		ONVIFEndpoint:     body.ONVIFEndpoint,
		ProfileToken:      body.ProfileToken,
		StreamEncoding:    body.StreamEncoding,
		Timelapse:         body.Timelapse,
		Channel:           body.Channel,
		AudioEnabled:      body.AudioEnabled != nil && *body.AudioEnabled,
		RecordingEnabled:  body.RecordingEnabled,
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

func (h *Handler) handleGetCamera(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	row, err := h.db.GetCamera(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get camera")
		return
	}
	if row == nil {
		WriteError(w, http.StatusNotFound, "camera not found")
		return
	}
	// Inject recorder status
	if h.camMgr != nil {
		row.Status = h.camMgr.CameraStatus(id)
	}
	// Inject last_seen from DB
	lastSeen, err := h.db.GetLastRecordingTime(r.Context(), id)
	if err == nil {
		row.LastSeen = lastSeen
	}
	// Inject per-camera transcoding config, channel, audio_enabled, and push
	// fields from config (not all stored in DB columns read by GetCamera).
	injectYAMLConfigFields(row, h.config)
	cameraRowForAPI(row)
	// Resolve displayed encoding from the live recorder probe (authoritative),
	// falling back to the stored value when the probe is empty. Matches
	// handleListCameras and /protocols so all three paths agree. See
	// handleListCameras for the full rationale (#166).
	if h.camMgr != nil {
		row.Encoding = resolveEncoding(row.Encoding, probeCodec(h.camMgr, id))
	}
	writeJSON(w, http.StatusOK, row)
}

// probeCodec returns the live recorder's runtime-detected codec for a camera,
// or "" when there is no running recorder or its codec hasn't been detected
// yet. It is a thin wrapper over GetRecorder + getCodecParams so callers don't
// repeat the nil-recorder / empty-codec dance.
func probeCodec(camMgr *camera.CameraManager, id string) model.Format {
	if camMgr == nil {
		return ""
	}
	rec := camMgr.GetRecorder(id)
	if rec == nil {
		return ""
	}
	codec, _, _, _ := getCodecParams(rec)
	return codec
}

// resolveEncoding picks the displayed encoding for a camera: the recorder's
// runtime-probed codec when it is non-empty (authoritative — the recorder reads
// the actual stream), otherwise the stored value as a fallback (covers offline
// cameras / codec-not-yet-detected). Centralizing this keeps the list, get, and
// /protocols read-paths consistent (#166) and gives a single seam to test.
func resolveEncoding(stored string, probe model.Format) string {
	if probe != "" {
		return string(probe)
	}
	return stored
}

// handleCameraPushStatus returns the runtime status of a camera's push-out relay
// targets (RTMP/RTSP). Used by the camera card/form to show live relay state.
func (h *Handler) handleCameraPushStatus(w http.ResponseWriter, r *http.Request) {
	if h.camMgr == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	id := chi.URLParam(r, "id")
	status := h.camMgr.RelayStatus(id)
	if status == nil {
		status = []interface{}{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"camera_id": id,
		"targets":   status,
	})
}

func (h *Handler) handleUpdateCamera(w http.ResponseWriter, r *http.Request) {
	if h.camMgr == nil {
		WriteError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	id := chi.URLParam(r, "id")

	var body struct {
		Name           *string                         `json:"name"`
		URL            *string                         `json:"url"`
		Protocol       *string                         `json:"protocol"`
		Encoding       *string                         `json:"encoding"`
		Username       *string                         `json:"username"`
		Password       *string                         `json:"password"`
		Enabled        *bool                           `json:"enabled"`
		Description    *string                         `json:"description"`
		Location       *string                         `json:"location"`
		Brand          *string                         `json:"brand"`
		Model          *string                         `json:"model"`
		SerialNumber   *string                         `json:"serial_number"`
		RetentionDays  *int                            `json:"retention_days"`
		ONVIFEndpoint  *string                         `json:"onvif_endpoint"`
		ProfileToken   *string                         `json:"profile_token"`
		StreamEncoding *string                         `json:"stream_encoding"`
		Transcoding    *config.CameraTranscodingConfig `json:"transcoding"`
		Channel        *string                         `json:"channel"`
		AudioEnabled   *bool                           `json:"audio_enabled"`
		// Dark frame filtering
		DarkFrameFilterEnabled *bool `json:"dark_frame_filter_enabled"`
		DarkFrameThreshold     *int  `json:"dark_frame_threshold"`
		// Recording gate: false = live-only (no segments written). nil = unchanged.
		RecordingEnabled *bool `json:"recording_enabled"`
		// Recording schedule
		RecordingSchedule *config.ScheduleConfig `json:"recording_schedule"`
		// Push/ingest fields (SRT/RTMP)
		StreamKey     *string `json:"stream_key"`
		SRTPassphrase *string `json:"srt_passphrase"`
		SRTStreamID   *string `json:"srt_stream_id"`
		// Push-out relay targets (replace whole list) + retention
		PushTargets       *[]config.PushTargetConfig `json:"push_targets"`
		PushRetentionDays *int                       `json:"push_retention_days"`
		// IP self-healing: stable hardware ID (ONVIF serial) + candidate subnets.
		StableID    *string   `json:"stable_id"`
		SubnetHints *[]string `json:"subnet_hints"`
		// GB28181 SIP device/channel binding
		GB28181 *gb28181ChannelPayload `json:"gb28181"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Reject a dirty stable_id at the API boundary (see #216). nil/empty means
	// "don't update" and is allowed.
	if body.StableID != nil && strings.TrimSpace(*body.StableID) != "" && !config.IsValidStableID(*body.StableID) {
		WriteError(w, http.StatusBadRequest, fmt.Sprintf("stable_id %q is not a valid hardware identity (must be 3–64 chars of [A-Za-z0-9:_-], not all-same-character; rejects IPs, URLs, all-zero MACs)", *body.StableID))
		return
	}

	// Harden credential updates: empty string from frontend means "don't update"
	username := body.Username
	if username != nil && *username == "" {
		username = nil
	}
	password := body.Password
	if password != nil && *password == "" {
		password = nil
	}

	// Validate transcoding config against hardware capabilities
	if body.Transcoding != nil && body.Transcoding.TargetCodec == "h265" {
		ffmpegPath := ""
		if h.config != nil && h.config.Transcoding.FFmpegPath != "" {
			ffmpegPath = h.config.Transcoding.FFmpegPath
		}
		caps := transcoding.ProbeHardwareCapabilities(ffmpegPath)
		if caps.H265EncoderType == transcoding.EncoderSoftware {
			WriteError(w, http.StatusBadRequest, "H.265 transcoding is not available on this device (no hardware encoder)")
			return
		}
	}

	updates := camera.CameraUpdate{
		Name:                   body.Name,
		URL:                    body.URL,
		Protocol:               body.Protocol,
		Encoding:               body.Encoding,
		Username:               username,
		Password:               password,
		Description:            body.Description,
		Location:               body.Location,
		Brand:                  body.Brand,
		Model:                  body.Model,
		SerialNumber:           body.SerialNumber,
		RetentionDays:          body.RetentionDays,
		ONVIFEndpoint:          body.ONVIFEndpoint,
		ProfileToken:           body.ProfileToken,
		StreamEncoding:         body.StreamEncoding,
		Transcoding:            body.Transcoding,
		Channel:                body.Channel,
		AudioEnabled:           body.AudioEnabled,
		DarkFrameFilterEnabled: body.DarkFrameFilterEnabled,
		DarkFrameThreshold:     body.DarkFrameThreshold,
		RecordingEnabled:       body.RecordingEnabled,
		RecordingSchedule:      body.RecordingSchedule,
		StreamKey:              body.StreamKey,
		SRTPassphrase:          body.SRTPassphrase,
		SRTStreamID:            body.SRTStreamID,
		PushTargets:            body.PushTargets,
		PushRetentionDays:      body.PushRetentionDays,
		GB28181:                body.GB28181.toConfigPtr(),
	}

	// Validate URL format if URL is being updated (skip for ONVIF and push cameras).
	if body.URL != nil && *body.URL != "" {
		proto := ""
		if body.Protocol != nil {
			proto = *body.Protocol
		}
		if proto != "onvif" && proto != "srt" && proto != "rtmp" && proto != "gb28181" {
			if !validateURL(*body.URL) {
				WriteError(w, http.StatusBadRequest, "invalid URL format")
				return
			}
		}
	}

	// For ONVIF cameras, sync url and onvif_endpoint
	if body.Protocol != nil && *body.Protocol == "onvif" {
		if updates.URL != nil && *updates.URL != "" {
			updates.ONVIFEndpoint = updates.URL
			updates.URL = nil
		}
		if updates.ONVIFEndpoint != nil && *updates.ONVIFEndpoint != "" {
			updates.URL = updates.ONVIFEndpoint
		}
	}

	_, err := h.camMgr.UpdateCamera(r.Context(), id, updates)
	if err != nil {
		var cnf *model.CameraNotFoundError
		if errors.As(err, &cnf) {
			writeAPIError(w, http.StatusNotFound, err)
			return
		}
		logger.Error("failed to update camera", "camera_id", id, "error", err, "path", r.URL.Path)
		WriteError(w, http.StatusInternalServerError, "failed to update camera")
		return
	}
	// Persist push/ingest fields if any were provided in the update.
	if body.StreamKey != nil || body.SRTPassphrase != nil || body.SRTStreamID != nil {
		// Read current values to merge with partial updates.
		updated := h.camMgr.GetCameraConfig(id)
		if updated != nil {
			if err := h.db.UpsertCameraIngest(r.Context(), id, updated.StreamKey, updated.SRTPassphrase, updated.SRTStreamID); err != nil {
				logger.Warn("failed to update camera ingest fields", "camera_id", id, "error", err)
			}
		}
	}
	// Return updated CameraRow with status
	row, err := h.db.GetCamera(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get camera")
		return
	}
	if row != nil {
		if h.camMgr != nil {
			row.Status = h.camMgr.CameraStatus(id)
		}
		// Inject last_seen from DB
		lastSeen, err := h.db.GetLastRecordingTime(r.Context(), id)
		if err == nil {
			row.LastSeen = lastSeen
		}
		// Inject ALL per-camera YAML-config fields (transcoding, channel,
		// audio_enabled, push targets, recording gate, dark-frame filter,
		// recording schedule, stable_id, subnet hints, ingest keys) so the PUT
		// response matches GET /api/cameras/{id}. Previously only Transcoding was
		// injected here — so push_targets/recording_enabled were missing and
		// audio_enabled serialized as its zero value (false), making any caller
		// that trusted the PUT response see its just-saved data "missing" (#297).
		injectYAMLConfigFields(row, h.config)
		cameraRowForAPI(row)
		writeJSON(w, http.StatusOK, row)
	} else {
		writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
	}
}

func (h *Handler) handleDeleteCamera(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx := r.Context()

	// Verify camera exists in DB
	cam, err := h.db.GetCamera(ctx, id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get camera")
		return
	}
	if cam == nil {
		WriteError(w, http.StatusNotFound, "camera not found")
		return
	}
	// Already archived — idempotent success
	if cam.Archived {
		writeJSON(w, http.StatusOK, map[string]string{"status": "archived"})
		return
	}

	// Archive the camera: stops recorder, merges segments, marks archived in DB, removes from config.
	// This preserves the camera row and recordings for the archive view.
	if h.camMgr != nil {
		// Check if camera is managed (in config). Orphaned DB-only cameras skip the
		// CameraManager mutex entirely to avoid blocking on merge/stop operations.
		if h.camMgr.GetCameraConfig(id) != nil {
			if err := h.camMgr.ArchiveCamera(ctx, id); err != nil {
				logger.Warn("failed to archive camera via manager, archiving in DB", "camera_id", id, "error", err)
				if dbErr := h.db.ArchiveCameraDB(ctx, id); dbErr != nil {
					WriteError(w, http.StatusInternalServerError, "failed to archive camera")
					return
				}
				if _, recErr := h.db.ArchiveAllRecordings(ctx, id); recErr != nil {
					logger.Warn("failed to archive recordings", "camera_id", id, "error", recErr)
				}
			}
		} else {
			// Orphaned camera (not in config) — archive directly in DB, no mutex needed.
			logger.Info("archiving orphaned camera directly in DB", "camera_id", id)
			if dbErr := h.db.ArchiveCameraDB(ctx, id); dbErr != nil {
				WriteError(w, http.StatusInternalServerError, "failed to archive camera")
				return
			}
			if _, recErr := h.db.ArchiveAllRecordings(ctx, id); recErr != nil {
				logger.Warn("failed to archive recordings", "camera_id", id, "error", recErr)
			}
		}
	} else {
		// No camera manager — mark archived in DB directly
		if err := h.db.ArchiveCameraDB(ctx, id); err != nil {
			WriteError(w, http.StatusInternalServerError, "failed to archive camera")
			return
		}
		if _, err := h.db.ArchiveAllRecordings(ctx, id); err != nil {
			logger.Warn("failed to archive recordings", "camera_id", id, "error", err)
		}
	}

	// Remove from merge scheduler if present
	if h.mergeScheduler != nil {
		h.mergeScheduler.Remove(id)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "archived"})
}

func (h *Handler) handleCameraRecordingStats(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	count, totalSize, err := h.db.GetCameraRecordingStats(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get camera stats")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"recording_count": count, "total_size": totalSize})
}

func (h *Handler) handleStartCamera(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if h.camMgr == nil {
		WriteError(w, http.StatusServiceUnavailable, "camera manager not available")
		return
	}
	if err := h.camMgr.StartCamera(r.Context(), id); err != nil {
		switch {
		case errors.As(err, new(*model.CameraNotFoundError)):
			writeAPIError(w, http.StatusNotFound, err)
		case errors.As(err, new(*model.CameraAlreadyRunningError)):
			writeAPIError(w, http.StatusConflict, err)
		default:
			logger.Error("failed to start camera", "camera_id", id, "error", err, "path", r.URL.Path)
			WriteError(w, http.StatusInternalServerError, "failed to start camera")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "started"})
}

// handleActivateCamera transitions a pending_activation camera to active by
// applying credentials and starting the recorder. Used by the auto-discover
// flow: an authenticated ONVIF device discovered without valid credentials is
// persisted as pending_activation; the user supplies credentials via this
// endpoint to bring it live.
func (h *Handler) handleActivateCamera(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if h.camMgr == nil {
		WriteError(w, http.StatusServiceUnavailable, "camera manager not available")
		return
	}
	if err := h.camMgr.ActivateCamera(r.Context(), id, body.Username, body.Password); err != nil {
		switch {
		case errors.As(err, new(*model.CameraNotFoundError)):
			writeAPIError(w, http.StatusNotFound, err)
		case errors.As(err, new(*model.CameraAlreadyRunningError)):
			// Idempotent: the recorder auto-restored (e.g. on NVR restart) and raced
			// with this request. Mirrors handleStartCamera's 409 — the camera is
			// already in the desired state, not a server error.
			writeAPIError(w, http.StatusConflict, err)
		default:
			logger.Error("failed to activate camera", "camera_id", id, "error", err, "path", r.URL.Path)
			WriteError(w, http.StatusInternalServerError, "failed to activate camera")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "activated"})
}

func (h *Handler) handleStopCamera(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if h.camMgr == nil {
		WriteError(w, http.StatusServiceUnavailable, "camera manager not available")
		return
	}
	if err := h.camMgr.StopCamera(r.Context(), id); err != nil {
		var cnf *model.CameraNotFoundError
		if errors.As(err, &cnf) {
			writeAPIError(w, http.StatusNotFound, err)
		} else {
			logger.Error("failed to stop camera", "camera_id", id, "error", err, "path", r.URL.Path)
			WriteError(w, http.StatusInternalServerError, "failed to stop camera")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

// handleRediscoverCamera manually triggers IP self-healing for a camera whose
// network address may have changed (e.g. after an AP reboot across per-subnet
// DHCP). It scans candidate subnets for a device whose ONVIF serial matches the
// camera's StableID and, if found, updates the config and reconnects.
func (h *Handler) handleRediscoverCamera(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if h.camMgr == nil {
		WriteError(w, http.StatusServiceUnavailable, "camera manager not available")
		return
	}
	// The unicast scan can take up to MaxDuration (default 30s) on a wide subnet
	// hint list, so do not bind it to the request's lifetime.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	found, err := h.camMgr.RediscoverAndReconnect(ctx, id)
	if err != nil {
		var cnf *model.CameraNotFoundError
		switch {
		case errors.As(err, &cnf):
			writeAPIError(w, http.StatusNotFound, err)
		default:
			logger.Error("rediscover camera failed", "camera_id", id, "error", err, "path", r.URL.Path)
			WriteError(w, http.StatusInternalServerError, "rediscovery failed")
		}
		return
	}
	if !found {
		// Not an error: camera may be unsupported (non-ONVIF), have no stable_id,
		// or simply not be online in any candidate subnet yet.
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"found":  false,
			"reason": "camera not found in candidate subnets (is it powered on? consider adding subnet_hints)",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"found":  true,
		"status": "reconnected",
	})
}

// handleTestConnection attempts to connect to a camera URL with a short timeout.
// Returns success/failure, a human-readable message, and the latency in milliseconds.
func (h *Handler) handleTestConnection(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Protocol      string `json:"protocol"`
		URL           string `json:"url"`
		Username      string `json:"username"`
		Password      string `json:"password"`
		Encoding      string `json:"encoding"`
		ONVIFEndpoint string `json:"onvif_endpoint"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.URL == "" && body.ONVIFEndpoint == "" {
		WriteError(w, http.StatusBadRequest, "url is required")
		return
	}

	target := body.URL
	if body.Protocol == "onvif" && body.ONVIFEndpoint != "" {
		target = body.ONVIFEndpoint
	}

	startTime := time.Now()

	// ONVIF cameras get a real stream-access probe: the old HTTP-HEAD check could
	// report success while the RTSP stream was unreachable or credentials were
	// wrong for GetStreamUri — the root of the "test passes but no image" reports
	// (issues #29/#30). The probe distinguishes reachable / stream-ok / codec-lie.
	if body.Protocol == "onvif" {
		probe := probeONVIFStream(r.Context(), target, body.Username, body.Password)
		writeJSON(w, http.StatusOK, map[string]any{
			"success":    probe.StreamOK,
			"reachable":  probe.Reachable,
			"stream_ok":  probe.StreamOK,
			"encoding":   probe.Encoding,
			"codec_lie":  probe.CodecLie,
			"message":    probe.reasonOrOK(),
			"latency_ms": time.Since(startTime).Milliseconds(),
		})
		return
	}

	switch {
	case strings.HasPrefix(target, "rtsp://"):
		// RTSP: try TCP connection to the host:port
		conn, err := net.DialTimeout("tcp", stripScheme(target), 3*time.Second)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"success":    false,
				"message":    fmt.Sprintf("connection refused: %v", err),
				"latency_ms": time.Since(startTime).Milliseconds(),
			})
			return
		}
		conn.Close()

	default:
		// HTTP/ONVIF: try HEAD/GET request with timeout
		client := &http.Client{Timeout: 3 * time.Second}
		// For URLs with credentials, inject them
		req, err := http.NewRequestWithContext(r.Context(), http.MethodHead, target, nil)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"success":    false,
				"message":    fmt.Sprintf("invalid URL: %v", err),
				"latency_ms": time.Since(startTime).Milliseconds(),
			})
			return
		}
		if body.Username != "" {
			req.SetBasicAuth(body.Username, body.Password)
		} else {
			// Extract credentials from URL if present (e.g., http://admin:pass@host)
			if parsed, err := url.Parse(target); err == nil && parsed.User != nil {
				if u := parsed.User.Username(); u != "" {
					p, _ := parsed.User.Password()
					req.SetBasicAuth(u, p)
				}
			}
		}
		resp, err := client.Do(req)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"success":    false,
				"message":    fmt.Sprintf("connection failed: %v", err),
				"latency_ms": time.Since(startTime).Milliseconds(),
			})
			return
		}
		resp.Body.Close()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"message":    "connection successful",
		"latency_ms": time.Since(startTime).Milliseconds(),
	})
}

// stripScheme extracts host:port from a URL string for TCP dialing.
// Handles URLs with credentials (user:pass@host) by stripping userinfo.
func stripScheme(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL // fallback
	}
	host := parsed.Hostname()
	port := parsed.Port()
	if port == "" {
		switch parsed.Scheme {
		case "rtsp":
			port = "554"
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			port = "554"
		}
	}
	return net.JoinHostPort(host, port)
}

// probeONVIFEncoding connects to an ONVIF device and retrieves the encoding
// from the first media profile. Returns "H264" or "H265", or empty string on failure.
// A bounded timeout is applied so a stuck device (e.g. ESP32 MiBeeCam with very
// limited concurrent HTTP capacity) cannot block the camera-create request — the
// caller context may outlive both the user's patience and the frontend fetch timeout.
// probeONVIFEncoding returns the best-known video encoding ("H264" or "H265") for
// an ONVIF device. It starts from the ONVIF profile declaration but verifies it
// against the actual RTSP stream, because some HiSilicon-OEM cameras advertise H264
// in their ONVIF profile while streaming H.265 (see ONVIFRecorder.detectEncoding).
// If the RTSP DESCRIBE probe succeeds, its result is authoritative; otherwise the
// ONVIF-declared encoding is returned as-is.
// onvifStreamProbe is the structured result of probing an ONVIF device for real
// stream access. It distinguishes "device reachable" from "stream actually
// playable" — the old test-connection endpoint conflated the two (an HTTP HEAD
// to the device_service URL can return 200 while the RTSP stream is unreachable
// or the credentials are wrong for GetStreamUri), producing the "test passes but
// no image" reports in issues #29/#30.
type onvifStreamProbe struct {
	Reachable bool   // ONVIF device_service responded to GetDeviceInformation/GetProfiles
	StreamOK  bool   // an RTSP stream URI was resolved AND a DESCRIBE succeeded
	Encoding  string // the REAL codec (RTSP DESCRIBE is authoritative; may differ from declared)
	CodecLie  bool   // the ONVIF-declared codec disagrees with the real stream
	Reason    string // human-readable explanation when Reachable/StreamOK is false
}

// onvifLooksLikeAuthError reports whether an ONVIF error smells like a
// WS-Security rejection (the trigger for running the time-skew diagnosis).
// Mirrors the onvif package's internal isAuthError but lives here to avoid
// widening the onvif package's API surface.
func onvifLooksLikeAuthError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "NotAuthorized") ||
		strings.Contains(s, "status 401") ||
		strings.Contains(s, "status 403") ||
		strings.Contains(s, "status 400")
}

// reasonOrOK returns the failure reason, or a success message when the stream
// is healthy (used by the test-connection response so the frontend always has a
// sensible message to show).
func (p onvifStreamProbe) reasonOrOK() string {
	if p.StreamOK {
		if p.CodecLie {
			return "stream accessible (declared codec corrected by RTSP probe)"
		}
		return "connection successful"
	}
	return p.Reason
}

// probeONVIFStream connects to an ONVIF device, resolves the stream URI, and
// verifies the stream actually plays via an RTSP DESCRIBE. This is the
// "does it really work?" check that the test-connection flow needs. It is also
// the engine behind probeONVIFEncoding (which discards everything but encoding).
func probeONVIFStream(ctx context.Context, endpoint, username, password string) onvifStreamProbe {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	client := onvif.NewClient(endpoint, username, password)
	if err := client.Connect(ctx); err != nil {
		reason := fmt.Sprintf("could not connect to ONVIF service: %v", err)
		// If it looks like an auth rejection, run the time-skew diagnosis so the
		// user gets an actionable "sync the camera's clock" hint instead of a
		// generic failure (Hikvision cameras reject digest auth on clock skew).
		if onvifLooksLikeAuthError(err) {
			if diag := client.DiagnoseAuth(ctx); diag.SkewDetected {
				reason = diag.Diagnosis
			}
		}
		return onvifStreamProbe{Reason: reason}
	}
	profiles, err := client.GetProfiles(ctx)
	if err != nil {
		reason := fmt.Sprintf("device responded but GetProfiles failed (credentials may be wrong, or ONVIF may be limited): %v", err)
		if onvifLooksLikeAuthError(err) {
			if diag := client.DiagnoseAuth(ctx); diag.SkewDetected {
				reason = diag.Diagnosis
			}
		}
		return onvifStreamProbe{
			Reachable: true,
			Reason:    reason,
		}
	}
	if len(profiles) == 0 {
		return onvifStreamProbe{
			Reachable: true,
			Reason:    "device responded but exposed no media profiles — check the camera's ONVIF/stream configuration",
		}
	}
	declared := profiles[0].Encoding

	si, err := client.GetStreamURI(ctx, profiles[0].Token)
	if err != nil || si.URI == "" {
		return onvifStreamProbe{
			Reachable: true,
			Reason:    "device responded but GetStreamUri failed — credentials may lack stream access, or the camera does not expose RTSP",
		}
	}

	// RTSP DESCRIBE is authoritative for the codec (corrects cameras that lie —
	// e.g. HiSilicon OEMs advertising H264 while streaming H265) AND confirms the
	// stream is actually playable with the supplied credentials.
	actual := recorder.ProbeRTSPEncoding(si.URI, username, password)
	if actual == "" {
		return onvifStreamProbe{
			Reachable: true,
			Reason:    "stream URI resolved but RTSP DESCRIBE failed — check that the RTSP port is reachable and credentials are correct",
		}
	}
	codecLie := declared != "" && !strings.EqualFold(actual, declared)
	if codecLie {
		logger.Info("ONVIF-declared encoding corrected by RTSP probe",
			"endpoint", endpoint, "declared", declared, "actual", actual)
	}
	return onvifStreamProbe{
		Reachable: true,
		StreamOK:  true,
		Encoding:  actual,
		CodecLie:  codecLie,
	}
}

// probeONVIFEncoding returns just the resolved encoding (empty on failure),
// preserving the original contract of the create-camera path. It now delegates
// to probeONVIFStream so both paths share one probing implementation.
func probeONVIFEncoding(ctx context.Context, endpoint, username, password string) string {
	return probeONVIFStream(ctx, endpoint, username, password).Encoding
}

// registerCameraRoutes registers all /api/cameras* routes (including nested
// stream, ONVIF, PTZ, snapshot, merge-config, timelapse config, events, and
// Xiaomi sub-routes) on the given (already auth-protected) router.
func (h *Handler) registerCameraRoutes(r chi.Router) {
	r.Route("/api/cameras", func(r chi.Router) {
		r.Get("/", h.handleListCameras)
		r.Post("/", h.handleCreateCamera)
		r.Post("/test-connection", h.handleTestConnection)
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", h.handleGetCamera)
			r.Put("/", h.handleUpdateCamera)
			r.Delete("/", h.handleDeleteCamera)
			// WebSocket stream (must be before HLS catch-all /stream/*)
			r.Get("/stream/ws", h.handleStreamWS)
			r.Get("/stream/*", h.handleHLSStream)
			r.Delete("/stream", h.handleStopHLSStream)
			// WebRTC WHEP endpoints
			r.Post("/stream/webrtc", h.handleCreateWHEPSession)
			r.Delete("/stream/webrtc/{session}", h.handleDeleteWHEPSession)
			// HTTP-FLV stream
			r.Get("/stream.flv", h.handleFLVStream)
			r.Get("/stream.mjpeg", h.handleMjpegStream)
			r.Get("/latest-frame", h.handleLatestFrame)
			// Per-camera protocols
			r.Get("/protocols", h.handleCameraProtocols)
			// GB28181 voice intercom (talk): WS ingest + status (#341)
			r.Get("/gb28181/talk", h.handleGB28181TalkWS)
			r.Get("/gb28181/talk/status", h.handleGB28181TalkStatus)
			r.Get("/onvif/profiles", h.handleONVIFCameraProfiles)
			r.Get("/onvif/capabilities", h.handleONVIFCapabilities)
			r.Post("/ptz/move", h.handlePTZMove)
			r.Post("/ptz/stop", h.handlePTZStop)
			r.Get("/ptz/status", h.handlePTZStatus)
			r.Get("/ptz/presets", h.handlePTZGetPresets)
			r.Post("/ptz/presets", h.handlePTZCreatePreset)
			r.Post("/ptz/presets/{token}/goto", h.handlePTZGoToPreset)
			r.Delete("/ptz/presets/{token}", h.handlePTZDeletePreset)
			r.Get("/snapshot/uri", h.handleSnapshotGetUri)
			r.Get("/imaging/settings", h.handleImagingGetSettings)
			r.Put("/imaging/settings", h.handleImagingSetSettings)
			r.Get("/imaging/options", h.handleImagingGetOptions)
			// Device management
			r.Post("/onvif/reboot", h.handleONVIFReboot)
			r.Get("/onvif/network", h.handleONVIFGetNetwork)
			r.Put("/onvif/network", h.handleONVIFSetNetwork)
			r.Get("/onvif/users", h.handleONVIFGetUsers)
			r.Post("/onvif/users", h.handleONVIFCreateUsers)
			r.Delete("/onvif/users", h.handleONVIFDeleteUsers)
			r.Put("/onvif/users/{username}", h.handleONVIFSetUser)
			r.Get("/snapshot", h.handleSnapshot)
			r.Get("/merge-config", h.handleGetCameraMergeConfig)
			r.Put("/merge-config", h.handleUpdateCameraMergeConfig)
			r.Delete("/merge-config", h.handleDeleteCameraMergeConfig)
			r.Get("/stats", h.handleCameraRecordingStats)
			// Per-camera timelapse configuration
			r.Get("/timelapse", h.handleGetCameraTimelapse)
			r.Put("/timelapse", h.handlePutCameraTimelapse)
			// Camera-specific events (SSE)
			r.Get("/events", h.handleCameraEvents)
			r.Post("/start", h.handleStartCamera)
			r.Post("/stop", h.handleStopCamera)
			// Activate a pending_activation camera: supply credentials and start
			// its recorder. Used by the auto-discover flow.
			r.Post("/activate", h.handleActivateCamera)
			// Manually trigger IP self-healing for a camera whose address changed.
			r.Post("/rediscover", h.handleRediscoverCamera)
			// Xiaomi-specific PTZ and device info endpoints
			r.Route("/xiaomi", func(r chi.Router) {
				r.Post("/ptz/move", h.handleXiaomiPTZMove)
				r.Post("/ptz/stop", h.handleXiaomiPTZStop)
				r.Get("/device-info", h.handleXiaomiDeviceInfo)
				// Xiaomi two-way audio endpoints
				r.Post("/two-way-audio/start", h.handleStartTwoWayAudio)
				r.Post("/two-way-audio/stop", h.handleStopTwoWayAudio)
			})
		})
	})
	// Push-out relay status (per-camera) — flat route, kept with cameras
	r.Get("/api/cameras/{id}/push-status", h.handleCameraPushStatus)
	// Per-camera health (flat route)
	r.Get("/api/cameras/{id}/health", h.handleGetCameraHealth)
}
