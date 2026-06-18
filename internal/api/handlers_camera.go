package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
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
		writeError(w, http.StatusInternalServerError, "failed to list cameras")
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
					break
				}
			}
		}
	}
	// For ONVIF cameras, show onvif_endpoint as url for unified frontend handling
	for i := range cameras {
		cameraRowForAPI(&cameras[i])
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
	// Legacy combined protocols (accepted, will be normalized)
	"rtsp_h264":  true,
	"rtsp_h265":  true,
	"rtsp_mjpeg": true,
	"http_jpeg":  true,
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
		// Push/ingest fields (SRT/RTMP)
		StreamKey     string `json:"stream_key"`
		SRTPassphrase string `json:"srt_passphrase"`
		SRTStreamID   string `json:"srt_stream_id"`
		// Push-out relay targets + retention
		PushTargets       []config.PushTargetConfig `json:"push_targets"`
		PushRetentionDays *int                      `json:"push_retention_days"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if body.Protocol == "" {
		writeError(w, http.StatusBadRequest, "protocol is required")
		return
	}
	if !validProtocols[body.Protocol] {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid protocol %q, must be one of: rtsp, http, onvif, srt, rtmp, xiaomi, timelapse", body.Protocol))
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
			writeError(w, http.StatusBadRequest, "url or onvif_endpoint is required for ONVIF cameras")
			return
		}
		body.ONVIFEndpoint = endpoint
		body.URL = "" // Don't store in url field for ONVIF
		// Check for duplicate ONVIF endpoint
		if h.db != nil {
			existingCams, _ := h.db.ListCameras(r.Context())
			for _, ec := range existingCams {
				if ec.Protocol == "onvif" && ec.ONVIFEndpoint == body.ONVIFEndpoint {
					writeError(w, http.StatusConflict, "ONVIF camera with this endpoint already exists")
					return
				}
			}
		}
	} else if !isPush && body.URL == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}
	// Validate URL format for cameras that have one (not ONVIF, not push)
	if body.Protocol != "onvif" && !isPush && body.URL != "" && !validateURL(body.URL) {
		writeError(w, http.StatusBadRequest, "invalid URL format")
		return
	}
	// Normalize protocol — handle legacy combined formats
	proto := body.Protocol
	enc := body.Encoding
	if strings.Contains(proto, "_") {
		parsedProto, parsedEnc, err := model.ParseLegacyProtocol(proto)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid protocol %q", proto))
			return
		}
		proto = parsedProto
		if enc == "" {
			enc = parsedEnc
		}
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
		StreamKey:         body.StreamKey,
		SRTPassphrase:     body.SRTPassphrase,
		SRTStreamID:       body.SRTStreamID,
		PushTargets:       body.PushTargets,
		PushRetentionDays: body.PushRetentionDays,
	}

	if h.camMgr == nil {
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	id, err := h.camMgr.AddCamera(r.Context(), cam)
	if err != nil {
		var cae *model.CameraAlreadyExistsError
		if errors.As(err, &cae) {
			writeAPIError(w, http.StatusConflict, err)
		} else {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to add camera: %v", err))
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
	row, err := h.db.GetCamera(r.Context(), id)
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
		writeError(w, http.StatusInternalServerError, "failed to get camera")
		return
	}
	if row == nil {
		writeError(w, http.StatusNotFound, "camera not found")
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
				break
			}
		}
	}
	cameraRowForAPI(row)
	writeJSON(w, http.StatusOK, row)
}

// handleCameraPushStatus returns the runtime status of a camera's push-out relay
// targets (RTMP/RTSP). Used by the camera card/form to show live relay state.
func (h *Handler) handleCameraPushStatus(w http.ResponseWriter, r *http.Request) {
	if h.camMgr == nil {
		writeError(w, http.StatusInternalServerError, "camera manager not available")
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
		writeError(w, http.StatusInternalServerError, "camera manager not available")
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
		// Push/ingest fields (SRT/RTMP)
		StreamKey     *string `json:"stream_key"`
		SRTPassphrase *string `json:"srt_passphrase"`
		SRTStreamID   *string `json:"srt_stream_id"`
		// Push-out relay targets (replace whole list) + retention
		PushTargets       *[]config.PushTargetConfig `json:"push_targets"`
		PushRetentionDays *int                       `json:"push_retention_days"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
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
			writeError(w, http.StatusBadRequest, "H.265 transcoding is not available on this device (no hardware encoder)")
			return
		}
	}

	updates := camera.CameraUpdate{
		Name:              body.Name,
		URL:               body.URL,
		Protocol:          body.Protocol,
		Encoding:          body.Encoding,
		Username:          username,
		Password:          password,
		Description:       body.Description,
		Location:          body.Location,
		Brand:             body.Brand,
		Model:             body.Model,
		SerialNumber:      body.SerialNumber,
		RetentionDays:     body.RetentionDays,
		ONVIFEndpoint:     body.ONVIFEndpoint,
		ProfileToken:      body.ProfileToken,
		StreamEncoding:    body.StreamEncoding,
		Transcoding:       body.Transcoding,
		Channel:           body.Channel,
		AudioEnabled:      body.AudioEnabled,
		StreamKey:         body.StreamKey,
		SRTPassphrase:     body.SRTPassphrase,
		SRTStreamID:       body.SRTStreamID,
		PushTargets:       body.PushTargets,
		PushRetentionDays: body.PushRetentionDays,
	}

	// Validate URL format if URL is being updated (skip for ONVIF and push cameras).
	if body.URL != nil && *body.URL != "" {
		proto := ""
		if body.Protocol != nil {
			proto = *body.Protocol
		}
		if proto != "onvif" && proto != "srt" && proto != "rtmp" {
			if !validateURL(*body.URL) {
				writeError(w, http.StatusBadRequest, "invalid URL format")
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
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to update camera: %v", err))
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
		writeError(w, http.StatusInternalServerError, "failed to get camera")
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
		writeError(w, http.StatusInternalServerError, "failed to get camera")
		return
	}
	if cam == nil {
		writeError(w, http.StatusNotFound, "camera not found")
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
					writeError(w, http.StatusInternalServerError, "failed to archive camera")
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
				writeError(w, http.StatusInternalServerError, "failed to archive camera")
				return
			}
			if _, recErr := h.db.ArchiveAllRecordings(ctx, id); recErr != nil {
				logger.Warn("failed to archive recordings", "camera_id", id, "error", recErr)
			}
		}
	} else {
		// No camera manager — mark archived in DB directly
		if err := h.db.ArchiveCameraDB(ctx, id); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to archive camera")
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
		writeError(w, http.StatusInternalServerError, "failed to get camera stats")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"recording_count": count, "total_size": totalSize})
}

func (h *Handler) handleStartCamera(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if h.camMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "camera manager not available")
		return
	}
	if err := h.camMgr.StartCamera(r.Context(), id); err != nil {
		switch {
		case errors.As(err, new(*model.CameraNotFoundError)):
			writeAPIError(w, http.StatusNotFound, err)
		case errors.As(err, new(*model.CameraAlreadyRunningError)):
			writeAPIError(w, http.StatusConflict, err)
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "started"})
}

func (h *Handler) handleStopCamera(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if h.camMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "camera manager not available")
		return
	}
	if err := h.camMgr.StopCamera(r.Context(), id); err != nil {
		var cnf *model.CameraNotFoundError
		if errors.As(err, &cnf) {
			writeAPIError(w, http.StatusNotFound, err)
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
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
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.URL == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}

	target := body.URL
	if body.Protocol == "onvif" && body.ONVIFEndpoint != "" {
		target = body.ONVIFEndpoint
	}

	startTime := time.Now()

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
func probeONVIFEncoding(ctx context.Context, endpoint, username, password string) string {
	client := onvif.NewClient(endpoint, username, password)
	if err := client.Connect(ctx); err != nil {
		return ""
	}
	profiles, err := client.GetProfiles(ctx)
	if err != nil || len(profiles) == 0 {
		return ""
	}
	return profiles[0].Encoding
}
