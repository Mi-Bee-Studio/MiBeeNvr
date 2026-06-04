package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/transcoding"
	"github.com/go-chi/chi/v5"
)

// TranscodeDownloader is the interface the API layer uses to interact with
// the FFmpeg downloader. This decouples handlers from the concrete type
// and makes testing straightforward.
type TranscodeDownloader interface {
	FFmpegPath() string
	GetFFmpegStatus() transcoding.DownloadStatus
	DownloadFFmpeg(ctx context.Context) error
}

// TranscodeManagerAPI is the interface the API layer uses to interact with
// the transcoding manager. This decouples handlers from the concrete type.
type TranscodeManagerAPI interface {
	GetStatus() transcoding.ManagerStatus
	Queue() transcoding.QueueAPI
}

// --- Self-check endpoint ---

// handleTranscodingCheck handles GET /api/transcoding/check.
// Returns cached hardware probe data and FFmpeg availability.
// Idempotent — calls ProbeHardwareCapabilities which uses sync.Once internally.
func (h *Handler) handleTranscodingCheck(w http.ResponseWriter, r *http.Request) {
	// Let probe auto-detect FFmpeg via PATH — do NOT pass downloader's custom path
	// because probeHardware() only does LookPath when ffmpegPath is empty.
	ffmpegPath := ""
	if h.config != nil && h.config.Transcoding.FFmpegPath != "" {
		ffmpegPath = h.config.Transcoding.FFmpegPath
	}

	caps := transcoding.ProbeHardwareCapabilities(ffmpegPath)

	// Check FFmpeg download status if downloader is available
	ffmpegStatus := ""
	if h.downloader != nil {
		status := h.downloader.GetFFmpegStatus()
		ffmpegStatus = status.Status
	} else if caps.FFmpegAvailable {
		ffmpegStatus = "available"
	} else {
		ffmpegStatus = "not_installed"
	}

	// Build response — omit sensitive fields (FFmpegPath)
	resp := map[string]any{
		"supported":         caps.FFmpegAvailable,
		"ffmpeg_status":     ffmpegStatus,
		"encoders": map[string]string{
			"h264": caps.H264Encoder,
			"h265": caps.H265Encoder,
		},
		"decoders": map[string]string{
			"h264": caps.H264Decoder,
			"h265": caps.H265Decoder,
		},
		"warnings":         h.transcodeWarnings(caps),
		"max_concurrent":   caps.MaxConcurrentStreams,
		"estimated_fps":    caps.EstimatedFPS,
		"total_cores":      caps.TotalCores,
		"total_memory_mb":  caps.TotalMemoryMB,
		"h264_encoder_type": string(caps.H264EncoderType),
		"h265_encoder_type": string(caps.H265EncoderType),
		"h264_decoder_type": string(caps.H264DecoderType),
		"h265_decoder_type": string(caps.H265DecoderType),
		"max_encode_width":  caps.MaxEncodeWidth,
		"max_encode_height": caps.MaxEncodeHeight,
		"devices":          caps.Devices,
	}

	writeJSON(w, http.StatusOK, resp)
}

// transcodeWarnings returns human-readable warnings about transcoding limitations.
func (h *Handler) transcodeWarnings(caps *transcoding.HardwareCapabilities) []string {
	var warnings []string

	if !caps.FFmpegAvailable {
		warnings = append(warnings, "FFmpeg is not installed — transcoding unavailable")
	}
	if caps.EstimatedFPS < 5.0 && caps.FFmpegAvailable {
		warnings = append(warnings, "Low estimated FPS — transcoding may be too slow for real-time use")
	}
	if caps.TotalMemoryMB > 0 && caps.TotalMemoryMB < 512 {
		warnings = append(warnings, "Low memory (<512 MB) — transcoding may cause system instability")
	}

	// ARM decoder warnings
	if caps.Arch == "arm64" || caps.Arch == "arm" {
		if caps.H265Decoder == "" {
			warnings = append(warnings, "No hardware H.265 decoder — H.265 input transcoding will be unavailable")
		}
		if caps.H264Decoder == "" {
			warnings = append(warnings, "No hardware H.264 decoder — H.264 input transcoding will be unavailable")
		}
	}

	return warnings
}

// --- FFmpeg status endpoint ---

// handleFFmpegStatus handles GET /api/transcoding/ffmpeg/status.
// Returns the current FFmpeg download/availability status.
// Does NOT expose the binary path (security).
func (h *Handler) handleFFmpegStatus(w http.ResponseWriter, r *http.Request) {
	if h.downloader == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":            "not_installed",
			"version":           "",
			"download_progress": 0,
		})
		return
	}

	status := h.downloader.GetFFmpegStatus()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":            status.Status,
		"version":           status.Version,
		"download_progress": status.Progress,
	})
}

// --- FFmpeg download endpoint ---

// handleFFmpegDownload handles POST /api/transcoding/ffmpeg/download.
// Idempotent: if already downloading → returns current status; if available → returns 200.
// Starts download in background goroutine, returns 202 Accepted.
func (h *Handler) handleFFmpegDownload(w http.ResponseWriter, r *http.Request) {
	if h.downloader == nil {
		writeError(w, http.StatusServiceUnavailable, "FFmpeg downloader not available")
		return
	}

	status := h.downloader.GetFFmpegStatus()

	switch status.Status {
	case "available":
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "available",
			"version": status.Version,
		})
		return
	case "downloading":
		writeJSON(w, http.StatusOK, map[string]any{
			"status":            "downloading",
			"download_progress": status.Progress,
		})
		return
	}

	// Start download in background
	go func() {
		if err := h.downloader.DownloadFFmpeg(context.Background()); err != nil {
			logger.Warn("FFmpeg download failed", "error", err)
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":            "downloading",
		"download_progress": 0,
	})
}

// --- FFmpeg download retry endpoint ---

// handleFFmpegDownloadRetry handles POST /api/transcoding/ffmpeg/download/retry.
// Only works if status is "failed". Returns 409 Conflict otherwise.
func (h *Handler) handleFFmpegDownloadRetry(w http.ResponseWriter, r *http.Request) {
	if h.downloader == nil {
		writeError(w, http.StatusServiceUnavailable, "FFmpeg downloader not available")
		return
	}

	status := h.downloader.GetFFmpegStatus()

	switch status.Status {
	case "available":
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "available",
			"version": status.Version,
		})
		return
	case "downloading":
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":  "download already in progress",
			"status": "downloading",
		})
		return
	case "failed":
		// Allowed to retry
	default:
		// "not_installed" or unknown — also allow retry
	}

	// Start download in background
	go func() {
		if err := h.downloader.DownloadFFmpeg(context.Background()); err != nil {
			logger.Warn("FFmpeg download retry failed", "error", err)
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":            "downloading",
		"download_progress": 0,
	})
}

// SetTranscodeManager sets the transcode manager on the handler.
func (h *Handler) SetTranscodeManager(mgr TranscodeManagerAPI) {
	h.transcodeMgr = mgr
}

// --- Transcoding status endpoint ---

// handleTranscodingStatus handles GET /api/transcoding/status.
// Returns the overall transcoding subsystem status: enabled, hardware, queue state.
func (h *Handler) handleTranscodingStatus(w http.ResponseWriter, r *http.Request) {
	if h.transcodeMgr == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled":        false,
			"disabled_reason": transcoding.GetDisabledReason(),
			"hardware":       nil,
			"queue_length":   0,
			"active_jobs":   0,
			"recent_results": []any{},
		})
		return
	}

	status := h.transcodeMgr.GetStatus()
	writeJSON(w, http.StatusOK, status)
}

// --- Transcoding tasks list endpoint ---

// handleTranscodingTasksList handles GET /api/transcoding/tasks.
// Returns paginated transcode tasks with optional filters.
func (h *Handler) handleTranscodingTasksList(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusInternalServerError, "database not available")
		return
	}

	// Parse query params
	filter := storage.TranscodeTaskFilter{
		Status:   r.URL.Query().Get("status"),
		CameraID: r.URL.Query().Get("camera_id"),
	}

	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if v, err := strconv.Atoi(limitStr); err == nil && v > 0 {
			filter.Limit = v
		}
	}
	if offsetStr := r.URL.Query().Get("offset"); offsetStr != "" {
		if v, err := strconv.Atoi(offsetStr); err == nil && v >= 0 {
			filter.Offset = v
		}
	} else if pageStr := r.URL.Query().Get("page"); pageStr != "" {
		// Support page-based pagination (1-indexed).
		// Convert page to offset: offset = (page - 1) * limit.
		if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
			if filter.Limit <= 0 {
				filter.Limit = 50
			}
			filter.Offset = (p - 1) * filter.Limit
		}
	}

	tasks, total, err := h.db.ListTranscodeTasks(r.Context(), filter)
	if err != nil {
		logger.Warn("failed to list transcode tasks", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list tasks")
		return
	}

	if tasks == nil {
		tasks = []storage.TranscodeTask{}
	}

	page := 1
	if filter.Limit > 0 {
		page = (filter.Offset / filter.Limit) + 1
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tasks": tasks,
		"total": total,
		"limit": filter.Limit,
		"offset": filter.Offset,
		"page":  page,
	})
}

// --- Transcoding task create endpoint ---

// handleTranscodingTaskCreate handles POST /api/transcoding/tasks.
// Manually enqueue a transcode task. Validates recording exists and camera has transcoding enabled.
func (h *Handler) handleTranscodingTaskCreate(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusInternalServerError, "database not available")
		return
	}
	if h.transcodeMgr == nil || h.transcodeMgr.Queue() == nil {
		writeError(w, http.StatusServiceUnavailable, "transcoding is not enabled")
		return
	}

	var body struct {
		CameraID       string `json:"camera_id"`
		RecordingID    string `json:"recording_id"`
		TargetCodec    string `json:"target_codec"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate required fields
	if body.CameraID == "" {
		writeError(w, http.StatusBadRequest, "camera_id is required")
		return
	}
	if body.RecordingID == "" {
		writeError(w, http.StatusBadRequest, "recording_id is required")
		return
	}
	if body.TargetCodec == "" {
		body.TargetCodec = "h264"
	}

	// Validate target codec
	if body.TargetCodec != "h264" && body.TargetCodec != "h265" {
		writeError(w, http.StatusBadRequest, "target_codec must be h264 or h265")
		return
	}

	// Check transcoding is enabled for this camera
	if h.config != nil {
		camConfig := h.config.ResolveTranscodingConfig(body.CameraID)
		if !camConfig.Enabled {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("transcoding is not enabled for camera %s", body.CameraID))
			return
		}
	}

	// Validate recording exists
	rec, err := h.db.GetRecording(r.Context(), body.RecordingID)
	if err != nil {
		logger.Warn("failed to get recording", "error", err, "recording_id", body.RecordingID)
		writeError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil {
		writeError(w, http.StatusNotFound, "recording not found")
		return
	}

	// Build task
	ext := ".mp4"
	outputPath := rec.FilePath + ".transcoded" + ext
	now := time.Now().UTC().Format("2006-01-02 15:04:05.999999999")
	task := &storage.TranscodeTask{
		CameraID:     body.CameraID,
		RecordingID:  body.RecordingID,
		InputPath:    rec.FilePath,
		InputFormat:  string(rec.Format),
		OutputPath:   outputPath,
		OutputFormat: body.TargetCodec,
		CreatedAt:    now,
	}

	if err := h.transcodeMgr.Queue().Enqueue(r.Context(), task); err != nil {
		logger.Warn("failed to enqueue transcode task", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to enqueue task")
		return
	}

	writeJSON(w, http.StatusCreated, task)
}

// --- Transcoding task cancel endpoint ---

// handleTranscodingTaskCancel handles DELETE /api/transcoding/tasks/{id}.
// Cancels a pending or running task. Returns 409 for completed/failed/cancelled tasks.
func (h *Handler) handleTranscodingTaskCancel(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusInternalServerError, "database not available")
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid task ID")
		return
	}

	// Check current task status
	task, err := h.db.GetTaskByID(r.Context(), id)
	if err != nil {
		logger.Warn("failed to get transcode task", "error", err, "task_id", id)
		writeError(w, http.StatusInternalServerError, "failed to get task")
		return
	}
	if task == nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}

	// Only pending or running tasks can be cancelled
	switch task.Status {
	case "completed":
		writeError(w, http.StatusConflict, "cannot cancel completed task")
		return
	case "failed":
		writeError(w, http.StatusConflict, "cannot cancel failed task")
		return
	case "cancelled":
		writeError(w, http.StatusConflict, "task already cancelled")
		return
	}

	// Cancel via queue (kills FFmpeg process if running) then update DB
	if h.transcodeMgr != nil && h.transcodeMgr.Queue() != nil {
		if err := h.transcodeMgr.Queue().CancelTask(r.Context(), id); err != nil {
			logger.Warn("failed to cancel transcode task", "error", err, "task_id", id)
			writeError(w, http.StatusInternalServerError, "failed to cancel task")
			return
		}
	} else {
		// No queue manager — cancel in DB directly
		if err := h.db.CancelTask(r.Context(), id); err != nil {
			logger.Warn("failed to cancel transcode task in DB", "error", err, "task_id", id)
			writeError(w, http.StatusInternalServerError, "failed to cancel task")
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":     id,
		"status": "cancelled",
	})
}

// handleTranscodingTaskRetry handles POST /api/transcoding/tasks/{id}/retry.
// Creates a new pending transcoding task from a failed task.
func (h *Handler) handleTranscodingTaskRetry(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusInternalServerError, "database not available")
		return
	}
	if h.transcodeMgr == nil || h.transcodeMgr.Queue() == nil {
		writeError(w, http.StatusServiceUnavailable, "transcoding is not enabled")
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid task ID")
		return
	}

	// Get the existing task
	task, err := h.db.GetTaskByID(r.Context(), id)
	if err != nil {
		logger.Warn("failed to get transcode task", "error", err, "task_id", id)
		writeError(w, http.StatusInternalServerError, "failed to get task")
		return
	}
	if task == nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}

	// Only failed tasks can be retried
	if task.Status != "failed" {
		writeError(w, http.StatusConflict, "can only retry failed tasks")
		return
	}

	// Build new pending task from the failed task's parameters
	now := time.Now().UTC().Format("2006-01-02 15:04:05.999999999")
	newTask := &storage.TranscodeTask{
		CameraID:        task.CameraID,
		RecordingID:     task.RecordingID,
		InputPath:       task.InputPath,
		InputFormat:     task.InputFormat,
		OutputPath:      task.OutputPath,
		OutputFormat:    task.OutputFormat,
		OriginalDeleted: true,
		Framerate:       task.Framerate,
		CreatedAt:       now,
	}

	if err := h.transcodeMgr.Queue().Enqueue(r.Context(), newTask); err != nil {
		logger.Warn("failed to enqueue retry transcode task", "error", err, "task_id", id)
		writeError(w, http.StatusInternalServerError, "failed to enqueue retry task")
		return
	}

	writeJSON(w, http.StatusCreated, newTask)
}

// --- Per-camera transcoding config endpoint ---

// handleTranscodingCameraConfigs handles GET /api/transcoding/cameras.
// Returns resolved transcoding config for each camera.
func (h *Handler) handleTranscodingCameraConfigs(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		writeError(w, http.StatusInternalServerError, "config not available")
		return
	}

	cameras := h.config.Cameras
	configs := make([]map[string]any, 0, len(cameras))
	for _, cam := range cameras {
		resolved := h.config.ResolveTranscodingConfig(cam.ID)
		configs = append(configs, map[string]any{
			"camera_id":     cam.ID,
			"camera_name":   cam.Name,
			"enabled":       resolved.Enabled,
			"target_codec":  resolved.TargetCodec,
			"preset":        resolved.Preset,
			"bitrate":       resolved.Bitrate,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"global_enabled": h.config.Transcoding.Enabled,
		"cameras":        configs,
	})
}
