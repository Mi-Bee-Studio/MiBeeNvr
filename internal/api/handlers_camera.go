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
	if h.config != nil {
		for i := range cameras {
			for _, cam := range h.config.Cameras {
				if cam.ID == cameras[i].ID {
					if cam.Transcoding != nil {
						cameras[i].Transcoding = cam.Transcoding
					}
					if cam.Channel != "" {
						cameras[i].Channel = cam.Channel
					}
					cameras[i].AudioEnabled = cam.AudioEnabled
					cameras[i].StreamKey = cam.StreamKey
					cameras[i].SRTPassphrase = cam.SRTPassphrase
					cameras[i].SRTStreamID = cam.SRTStreamID
					cameras[i].PushTargets = cam.PushTargets
					cameras[i].PushRetentionDays = cam.PushRetentionDays
					cameras[i].StableID = cam.StableID
					cameras[i].SubnetHints = cam.SubnetHints
					cameras[i].RecordingEnabled = cam.RecordingEnabled
					break
				}
			}
		}
	}
	// For ONVIF cameras, show onvif_endpoint as url for unified frontend handling
	for i := range cameras {
		cameraRowForAPI(&cameras[i])
	}
	// Backfill encoding for cameras whose stored encoding is empty (e.g. ONVIF
	// auto-detect cameras like the ESP32 MiBeeCam, where encoding is resolved at
	// runtime by the recorder). Without this, the camera list reports encoding=""
	// and frontend pages that select a player from the list (Surveillance grid)
	// cannot tell a JPEG camera from an unknown one → they fall back to HLS and
	// render black (the per-camera LiveView page works because it queries /protocols
	// which already probes the live recorder). Probe the running recorder here so
	// every list consumer sees the same resolved encoding.
	if h.camMgr != nil {
		for i := range cameras {
			if cameras[i].Encoding != "" {
				continue
			}
			if rec := h.camMgr.GetRecorder(cameras[i].ID); rec != nil {
				if codec, _, _, _ := getCodecParams(rec); codec != "" {
					cameras[i].Encoding = string(codec)
				}
			}
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
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Name == "" {
		WriteError(w, http.StatusBadRequest, "name is required")
		return
	}
	if body.Protocol == "" {
		WriteError(w, http.StatusBadRequest, "protocol is required")
		return
	}
	if !validProtocols[body.Protocol] {
		WriteError(w, http.StatusBadRequest, fmt.Sprintf("invalid protocol %q, must be one of: rtsp, http, onvif, srt, rtmp, xiaomi, timelapse", body.Protocol))
		return
	}
	// Push/ingest cameras (srt/rtmp): no URL — the publisher connects to us.
	isPush := body.Protocol == "srt" || body.Protocol == "rtmp"
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
	} else if !isPush && body.URL == "" {
		WriteError(w, http.StatusBadRequest, "url is required")
		return
	}
	// Validate URL format for cameras that have one (not ONVIF, not push)
	if body.Protocol != "onvif" && !isPush && body.URL != "" && !validateURL(body.URL) {
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
	if h.config != nil {
		for _, cam := range h.config.Cameras {
			if cam.ID == id {
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
				break
			}
		}
	}
	cameraRowForAPI(row)
	// Backfill encoding from the live recorder when the stored value is empty
	// (ONVIF auto-detect cameras). See handleListCameras for the rationale.
	if row.Encoding == "" && h.camMgr != nil {
		if rec := h.camMgr.GetRecorder(id); rec != nil {
			if codec, _, _, _ := getCodecParams(rec); codec != "" {
				row.Encoding = string(codec)
			}
		}
	}
	writeJSON(w, http.StatusOK, row)
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
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
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
	}

	// Validate URL format if URL is being updated (skip for ONVIF and push cameras).
	if body.URL != nil && *body.URL != "" {
		proto := ""
		if body.Protocol != nil {
			proto = *body.Protocol
		}
		if proto != "onvif" && proto != "srt" && proto != "rtmp" {
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
		// Inject per-camera transcoding config
		if h.config != nil {
			for _, cam := range h.config.Cameras {
				if cam.ID == id && cam.Transcoding != nil {
					row.Transcoding = cam.Transcoding
					break
				}
			}
		}
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
