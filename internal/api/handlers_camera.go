package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"net/http"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/transcoding"
	"github.com/go-chi/chi/v5"
)

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
		row.SubProfileToken = cam.SubProfileToken
		row.SubStreamURL = cam.SubStreamURL
		row.DarkFrameFilterEnabled = cam.DarkFrameFilterEnabled
		row.DarkFrameThreshold = cam.DarkFrameThreshold
		row.RecordingEnabled = cam.RecordingEnabled
		row.CascadeEnabled = cam.CascadeEnabled
		row.RecordingSchedule = cam.RecordingSchedule
		row.RecordingMode = cam.RecordingMode
		row.Adaptive = cam.Adaptive
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
	"whip": true,
	// Plugin protocols
	"xiaomi":    true,
	"timelapse": true,
	// GB/T 28181 SIP-registered devices (no URL — identified by DeviceID/ChannelID)
	"gb28181": true,
}

// gb28181ChannelPayload is the API shape of a camera's GB28181 binding.

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
		Name           *string `json:"name"`
		URL            *string `json:"url"`
		Protocol       *string `json:"protocol"`
		Encoding       *string `json:"encoding"`
		Username       *string `json:"username"`
		Password       *string `json:"password"`
		Enabled        *bool   `json:"enabled"`
		Description    *string `json:"description"`
		Location       *string `json:"location"`
		Brand          *string `json:"brand"`
		Model          *string `json:"model"`
		SerialNumber   *string `json:"serial_number"`
		RetentionDays  *int    `json:"retention_days"`
		ONVIFEndpoint  *string `json:"onvif_endpoint"`
		ProfileToken   *string `json:"profile_token"`
		StreamEncoding *string `json:"stream_encoding"`
		// Sub-stream (#512): manual sub profile token (ONVIF) and manual sub
		// stream URL (any RTSP-capable protocol). No recorder restart.
		SubProfileToken *string                         `json:"sub_profile_token"`
		SubStreamURL    *string                         `json:"sub_stream_url"`
		Transcoding     *config.CameraTranscodingConfig `json:"transcoding"`
		Channel         *string                         `json:"channel"`
		AudioEnabled    *bool                           `json:"audio_enabled"`
		// Dark frame filtering
		DarkFrameFilterEnabled *bool `json:"dark_frame_filter_enabled"`
		DarkFrameThreshold     *int  `json:"dark_frame_threshold"`
		// Recording gate: false = live-only (no segments written). nil = unchanged.
		RecordingEnabled *bool `json:"recording_enabled"`
		// Cascade gate: false = hidden from the GB28181 cascade catalog and
		// INVITEs refused. nil = unchanged.
		CascadeEnabled *bool `json:"cascade_enabled"`
		// Cascade tier: forward the sub-stream instead of main (#512).
		CascadeSubStream *bool `json:"cascade_sub_stream"`
		// Recording schedule
		RecordingSchedule *config.ScheduleConfig `json:"recording_schedule"`
		// Recording mode (#435): ""/"continuous" or "adaptive" (+ tuning).
		// Validated at this boundary with the startup rules (#402 class).
		RecordingMode *string                         `json:"recording_mode"`
		Adaptive      *config.AdaptiveRecordingConfig `json:"adaptive"`
		// Audio trigger (#478): loudness input for adaptive recording.
		AudioTrigger *config.CameraAudioTriggerConfig `json:"audio_trigger"`
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

	if body.SubStreamURL != nil && strings.TrimSpace(*body.SubStreamURL) != "" &&
		!strings.HasPrefix(*body.SubStreamURL, "rtsp://") && !strings.HasPrefix(*body.SubStreamURL, "rtsps://") {
		WriteError(w, http.StatusBadRequest, "sub_stream_url must be an rtsp:// or rtsps:// URL")
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
		SubProfileToken:        body.SubProfileToken,
		SubStreamURL:           body.SubStreamURL,
		StreamEncoding:         body.StreamEncoding,
		Transcoding:            body.Transcoding,
		Channel:                body.Channel,
		AudioEnabled:           body.AudioEnabled,
		DarkFrameFilterEnabled: body.DarkFrameFilterEnabled,
		DarkFrameThreshold:     body.DarkFrameThreshold,
		RecordingEnabled:       body.RecordingEnabled,
		CascadeEnabled:         body.CascadeEnabled,
		CascadeSubStream:       body.CascadeSubStream,
		RecordingSchedule:      body.RecordingSchedule,
		RecordingMode:          body.RecordingMode,
		Adaptive:               body.Adaptive,
		AudioTrigger:           body.AudioTrigger,
		StreamKey:              body.StreamKey,
		SRTPassphrase:          body.SRTPassphrase,
		SRTStreamID:            body.SRTStreamID,
		PushTargets:            body.PushTargets,
		PushRetentionDays:      body.PushRetentionDays,
		GB28181:                body.GB28181.toConfigPtr(),
	}

	// Validate recording mode + adaptive tuning with the same rules the
	// startup path enforces, resolved against the camera's CURRENT encoding
	// (or the new one, when the same request changes it) — adaptive needs
	// h264/h265.
	if body.RecordingMode != nil || body.Adaptive != nil || body.AudioTrigger != nil {
		probe := config.CameraConfig{ID: id}
		if h.config != nil {
			for _, cam := range h.config.Cameras {
				if cam.ID == id {
					probe = cam
					break
				}
			}
		}
		if body.Encoding != nil && *body.Encoding != "" {
			probe.Encoding = *body.Encoding
		}
		if body.RecordingMode != nil {
			probe.RecordingMode = *body.RecordingMode
		}
		if body.Adaptive != nil {
			probe.Adaptive = body.Adaptive
		}
		if body.AudioTrigger != nil {
			probe.AudioTrigger = body.AudioTrigger
		}
		if err := config.ValidateCameraRecordingMode(probe); err != nil {
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	// Validate URL format if URL is being updated (skip for ONVIF and push cameras).
	if body.URL != nil && *body.URL != "" {
		proto := ""
		if body.Protocol != nil {
			proto = *body.Protocol
		}
		if proto != "onvif" && proto != "srt" && proto != "rtmp" && proto != "whip" && proto != "gb28181" {
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

	// When protocol or encoding is being changed, validate the RESULTING combo
	// against the same rules startup enforces — otherwise a partial update
	// (e.g. encoding h264 on an http camera) saves a config that fails fatally
	// on the next restart (#402 bug class).
	if body.Protocol != nil || body.Encoding != nil {
		proto, enc := "", ""
		managed := false
		if cur := h.camMgr.GetCameraConfig(id); cur != nil {
			proto, enc = cur.Protocol, cur.Encoding
			managed = true
		}
		if body.Protocol != nil {
			proto = *body.Protocol
		}
		if body.Encoding != nil {
			enc = *body.Encoding
		}
		// Validate only when the effective protocol is known: from the body, or
		// from the camera's config entry. Orphaned DB-only cameras (no YAML
		// entry) keep the legacy no-validation behavior.
		if managed || body.Protocol != nil {
			if err := model.ValidateProtocolEncoding(proto, enc); err != nil {
				WriteError(w, http.StatusBadRequest, fmt.Sprintf("camera %q: %s", id, err.Error()))
				return
			}
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
			// Flow-path snapshot (#469)
			r.Get("/flow", h.handleCameraFlow)
			// Per-camera frame-trace sampling window (#482)
			r.Get("/trace", h.handleCameraTrace)
			r.Post("/trace", h.handleCameraTrace)
			r.Delete("/trace", h.handleCameraTrace)
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
			// External adaptive-recording trigger (issue #478): a semantic
			// classifier running outside the NVR (or a test) forces a
			// timelapse→normal transition with the usual GOP + audio back-fill.
			r.Post("/adaptive/trigger", h.handleAdaptiveTrigger)
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
