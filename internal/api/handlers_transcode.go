package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
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
// Returns hardware probe data and FFmpeg availability.
// When FFmpeg was downloaded after startup (probe cached "not installed"),
// re-probes with the downloader's FFmpeg path to get fresh results.
func (h *Handler) handleTranscodingCheck(w http.ResponseWriter, r *http.Request) {
	// Let probe auto-detect FFmpeg via PATH — do NOT pass downloader's custom path
	// because probeHardware() only does LookPath when ffmpegPath is empty.
	ffmpegPath := ""
	if h.config != nil && h.config.Transcoding.FFmpegPath != "" {
		ffmpegPath = h.config.Transcoding.FFmpegPath
	}

	caps := transcoding.ProbeHardwareCapabilities(ffmpegPath)

	// Check FFmpeg download status if downloader is available
	var ffmpegStatus string
	if h.downloader != nil {
		status := h.downloader.GetFFmpegStatus()
		ffmpegStatus = status.Status

		// If downloader reports FFmpeg available but cached probe says no,
		// the binary was downloaded after startup. Re-probe with the downloader's path.
		if status.Status == "available" && !caps.FFmpegAvailable {
			caps = transcoding.ProbeHardwareCapabilitiesExplicit(h.downloader.FFmpegPath())
		}
	} else if caps.FFmpegAvailable {
		ffmpegStatus = "available"
	} else {
		ffmpegStatus = "not_installed"
	}

	// Build response — omit sensitive fields (FFmpegPath)
	resp := map[string]any{
		"supported":     caps.FFmpegAvailable,
		"ffmpeg_status": ffmpegStatus,
		"encoders": map[string]string{
			"h264": caps.H264Encoder,
			"h265": caps.H265Encoder,
		},
		"decoders": map[string]string{
			"h264": caps.H264Decoder,
			"h265": caps.H265Decoder,
		},
		"warnings":          h.transcodeWarnings(caps),
		"max_concurrent":    caps.MaxConcurrentStreams,
		"estimated_fps":     caps.EstimatedFPS,
		"total_cores":       caps.TotalCores,
		"total_memory_mb":   caps.TotalMemoryMB,
		"h264_encoder_type": string(caps.H264EncoderType),
		"h265_encoder_type": string(caps.H265EncoderType),
		"h264_decoder_type": string(caps.H264DecoderType),
		"h265_decoder_type": string(caps.H265DecoderType),
		"max_encode_width":  caps.MaxEncodeWidth,
		"max_encode_height": caps.MaxEncodeHeight,
		"devices":           caps.Devices,
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
		WriteError(w, http.StatusServiceUnavailable, "FFmpeg downloader not available")
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
		WriteError(w, http.StatusServiceUnavailable, "FFmpeg downloader not available")
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
			"enabled":         false,
			"disabled_reason": transcoding.GetDisabledReason(),
			"hardware":        nil,
			"queue_length":    0,
			"active_jobs":     0,
			"recent_results":  []any{},
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
		WriteError(w, http.StatusInternalServerError, "database not available")
		return
	}

	// Parse query params
	filter := storage.TranscodeTaskFilter{
		Status:   r.URL.Query().Get("status"),
		CameraID: r.URL.Query().Get("camera_id"),
	}

	filter.Limit, filter.Offset = parsePagination(r, 0, 0)
	// When offset is not supplied, support page-based pagination (1-indexed):
	// offset = (page - 1) * limit, with a default limit of 50.
	if r.URL.Query().Get("offset") == "" {
		if pageStr := r.URL.Query().Get("page"); pageStr != "" {
			if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
				if filter.Limit <= 0 {
					filter.Limit = 50
				}
				filter.Offset = (p - 1) * filter.Limit
			}
		}
	}

	tasks, total, err := h.db.ListTranscodeTasks(r.Context(), filter)
	if err != nil {
		logger.Warn("failed to list transcode tasks", "error", err)
		WriteError(w, http.StatusInternalServerError, "failed to list tasks")
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
		"tasks":  tasks,
		"total":  total,
		"limit":  filter.Limit,
		"offset": filter.Offset,
		"page":   page,
	})
}

// --- Transcoding task create endpoint ---

// handleTranscodingTaskCreate handles POST /api/transcoding/tasks.
// Manually enqueue a transcode task. Validates recording exists and camera has transcoding enabled.
func (h *Handler) handleTranscodingTaskCreate(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		WriteError(w, http.StatusInternalServerError, "database not available")
		return
	}
	if h.transcodeMgr == nil || h.transcodeMgr.Queue() == nil {
		WriteError(w, http.StatusServiceUnavailable, "transcoding is not enabled")
		return
	}

	var body struct {
		CameraID    string `json:"camera_id"`
		RecordingID string `json:"recording_id"`
		TargetCodec string `json:"target_codec"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate required fields
	if body.CameraID == "" {
		WriteError(w, http.StatusBadRequest, "camera_id is required")
		return
	}
	if body.RecordingID == "" {
		WriteError(w, http.StatusBadRequest, "recording_id is required")
		return
	}
	if body.TargetCodec == "" {
		body.TargetCodec = "h264"
	}

	// Validate target codec
	if body.TargetCodec != "h264" && body.TargetCodec != "h265" {
		WriteError(w, http.StatusBadRequest, "target_codec must be h264 or h265")
		return
	}

	// Check transcoding is enabled for this camera
	if h.config != nil {
		camConfig := h.config.ResolveTranscodingConfig(body.CameraID)
		if !camConfig.Enabled {
			WriteError(w, http.StatusBadRequest, "transcoding is not enabled for camera "+body.CameraID)
			return
		}
	}

	// Validate recording exists
	rec, err := h.db.GetRecording(r.Context(), body.RecordingID)
	if err != nil {
		logger.Warn("failed to get recording", "error", err, "recording_id", body.RecordingID)
		WriteError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil {
		WriteError(w, http.StatusNotFound, "recording not found")
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
		WriteError(w, http.StatusInternalServerError, "failed to enqueue task")
		return
	}

	writeJSON(w, http.StatusCreated, task)
}

// --- Transcoding task cancel endpoint ---

// handleTranscodingTaskCancel handles DELETE /api/transcoding/tasks/{id}.
// Cancels a pending or running task. Returns 409 for completed/failed/cancelled tasks.
func (h *Handler) handleTranscodingTaskCancel(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		WriteError(w, http.StatusInternalServerError, "database not available")
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid task ID")
		return
	}

	// Check current task status
	task, err := h.db.GetTaskByID(r.Context(), id)
	if err != nil {
		logger.Warn("failed to get transcode task", "error", err, "task_id", id)
		WriteError(w, http.StatusInternalServerError, "failed to get task")
		return
	}
	if task == nil {
		WriteError(w, http.StatusNotFound, "task not found")
		return
	}

	// Only pending or running tasks can be cancelled
	switch task.Status {
	case "completed":
		WriteError(w, http.StatusConflict, "cannot cancel completed task")
		return
	case "failed":
		WriteError(w, http.StatusConflict, "cannot cancel failed task")
		return
	case "cancelled":
		WriteError(w, http.StatusConflict, "task already cancelled")
		return
	}

	// Cancel via queue (kills FFmpeg process if running) then update DB
	if h.transcodeMgr != nil && h.transcodeMgr.Queue() != nil {
		if err := h.transcodeMgr.Queue().CancelTask(r.Context(), id); err != nil {
			logger.Warn("failed to cancel transcode task", "error", err, "task_id", id)
			WriteError(w, http.StatusInternalServerError, "failed to cancel task")
			return
		}
	} else {
		// No queue manager — cancel in DB directly
		if err := h.db.CancelTask(r.Context(), id); err != nil {
			logger.Warn("failed to cancel transcode task in DB", "error", err, "task_id", id)
			WriteError(w, http.StatusInternalServerError, "failed to cancel task")
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
		WriteError(w, http.StatusInternalServerError, "database not available")
		return
	}
	if h.transcodeMgr == nil || h.transcodeMgr.Queue() == nil {
		WriteError(w, http.StatusServiceUnavailable, "transcoding is not enabled")
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid task ID")
		return
	}

	// Get the existing task
	task, err := h.db.GetTaskByID(r.Context(), id)
	if err != nil {
		logger.Warn("failed to get transcode task", "error", err, "task_id", id)
		WriteError(w, http.StatusInternalServerError, "failed to get task")
		return
	}
	if task == nil {
		WriteError(w, http.StatusNotFound, "task not found")
		return
	}

	// Only failed tasks can be retried
	if task.Status != "failed" {
		WriteError(w, http.StatusConflict, "can only retry failed tasks")
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
		WriteError(w, http.StatusInternalServerError, "failed to enqueue retry task")
		return
	}

	writeJSON(w, http.StatusCreated, newTask)
}

// handleTranscodingBackfill handles POST /api/transcoding/backfill.
// Enqueues all untranscoded recordings for a camera into the transcode queue.
func (h *Handler) handleTranscodingBackfill(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		WriteError(w, http.StatusInternalServerError, "database not available")
		return
	}

	cameraID := r.URL.Query().Get("camera_id")
	if cameraID == "" {
		WriteError(w, http.StatusBadRequest, "camera_id is required")
		return
	}

	if h.transcodeMgr == nil || h.transcodeMgr.Queue() == nil {
		WriteError(w, http.StatusServiceUnavailable, "transcoding is not enabled")
		return
	}

	// Check camera exists in config
	if h.config != nil {
		cameraFound := false
		for _, cam := range h.config.Cameras {
			if cam.ID == cameraID {
				cameraFound = true
				break
			}
		}
		if !cameraFound {
			WriteError(w, http.StatusBadRequest, "camera "+cameraID+" not found")
			return
		}

		// Check if transcoding is enabled for this camera
		camConfig := h.config.ResolveTranscodingConfig(cameraID)
		if !camConfig.Enabled {
			WriteError(w, http.StatusBadRequest, "transcoding is not enabled for camera "+cameraID)
			return
		}
	}

	// Get target codec (default h264)
	targetCodec := "h264"
	if h.config != nil {
		camConfig := h.config.ResolveTranscodingConfig(cameraID)
		if camConfig.TargetCodec != "" {
			targetCodec = camConfig.TargetCodec
		}
	}

	// Get total recordings count for this camera
	allRecordings, err := h.db.ListRecordings(r.Context(), model.RecordingFilter{
		CameraID: cameraID,
	})
	if err != nil {
		logger.Warn("failed to list recordings", "error", err, "camera_id", cameraID)
		WriteError(w, http.StatusInternalServerError, "failed to list recordings")
		return
	}

	// Get recordings without transcode
	recordings, err := h.db.ListRecordingsWithoutTranscode(r.Context(), cameraID)
	if err != nil {
		logger.Warn("failed to list recordings without transcode", "error", err, "camera_id", cameraID)
		WriteError(w, http.StatusInternalServerError, "failed to list recordings")
		return
	}

	// Collect all recordings without an existing transcode task
	tasks := make([]storage.TranscodeTask, 0, len(recordings))
	for _, rec := range recordings {
		// Skip formats that are already browser-playable and don't need transcoding.
		// Transcoding AVI/MJPEG/JPEG is unnecessary (WebSocket playback handles them)
		// and harmful (would replace the original and break the playback path).
		if rec.Format == model.FormatAVI || rec.Format == model.FormatMJPEG || string(rec.Format) == "jpeg" {
			continue
		}
		outputPath := rec.FilePath + ".transcoded.mp4"
		now := time.Now().UTC().Format("2006-01-02 15:04:05.999999999")
		tasks = append(tasks, storage.TranscodeTask{
			CameraID:        cameraID,
			RecordingID:     rec.ID,
			InputPath:       rec.FilePath,
			InputFormat:     string(rec.Format),
			OutputPath:      outputPath,
			OutputFormat:    targetCodec,
			OriginalDeleted: true,
			Framerate:       0,
			CreatedAt:       now,
		})
	}

	// Batch enqueue all tasks in a single transaction
	if err := h.db.EnqueueTasksBatch(r.Context(), tasks); err != nil {
		logger.Warn("failed to enqueue backfill tasks", "error", err, "camera_id", cameraID, "count", len(tasks))
		WriteError(w, http.StatusInternalServerError, "failed to enqueue backfill tasks")
		return
	}
	logger.Info("backfill tasks enqueued", "camera_id", cameraID, "count", len(tasks))

	writeJSON(w, http.StatusOK, map[string]any{
		"enqueued": len(tasks),
		"skipped":  len(allRecordings) - len(tasks),
		"total":    len(allRecordings),
	})
}

// --- Per-camera transcoding config endpoint ---

// handleTranscodingCameraConfigs handles GET /api/transcoding/cameras.
// Returns resolved transcoding config for each camera.
func (h *Handler) handleTranscodingCameraConfigs(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}

	cameras := h.config.Cameras
	configs := make([]map[string]any, 0, len(cameras))
	for _, cam := range cameras {
		resolved := h.config.ResolveTranscodingConfig(cam.ID)
		configs = append(configs, map[string]any{
			"camera_id":    cam.ID,
			"camera_name":  cam.Name,
			"enabled":      resolved.Enabled,
			"target_codec": resolved.TargetCodec,
			"preset":       resolved.Preset,
			"bitrate":      resolved.Bitrate,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"global_enabled": h.config.Transcoding.Enabled,
		"cameras":        configs,
	})
}

// handleTranscodingRecordingsWithoutTranscode handles GET /api/transcoding/recordings-without-transcode.
// Returns the count of recordings that have not been transcoded for a camera.
func (h *Handler) handleTranscodingRecordingsWithoutTranscode(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		WriteError(w, http.StatusInternalServerError, "database not available")
		return
	}

	cameraID := r.URL.Query().Get("camera_id")
	if cameraID == "" {
		WriteError(w, http.StatusBadRequest, "camera_id is required")
		return
	}

	recordings, err := h.db.ListRecordingsWithoutTranscode(r.Context(), cameraID)
	if err != nil {
		logger.Warn("failed to list recordings without transcode", "error", err, "camera_id", cameraID)
		WriteError(w, http.StatusInternalServerError, "failed to list recordings")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"count": len(recordings),
	})
}

// registerTranscodeRoutes registers transcoding check/status/task/download routes.
func (h *Handler) registerTranscodeRoutes(r chi.Router) {
	r.Get("/api/transcoding/check", h.handleTranscodingCheck)
	r.Get("/api/transcoding/ffmpeg/status", h.handleFFmpegStatus)
	r.Post("/api/transcoding/ffmpeg/download", h.handleFFmpegDownload)
	r.Post("/api/transcoding/ffmpeg/download/retry", h.handleFFmpegDownloadRetry)
	r.Get("/api/transcoding/status", h.handleTranscodingStatus)
	r.Get("/api/transcoding/tasks", h.handleTranscodingTasksList)
	r.Post("/api/transcoding/tasks", h.handleTranscodingTaskCreate)
	r.Delete("/api/transcoding/tasks/{id}", h.handleTranscodingTaskCancel)
	r.Post("/api/transcoding/tasks/{id}/retry", h.handleTranscodingTaskRetry)
	r.Post("/api/transcoding/backfill", h.handleTranscodingBackfill)
	r.Get("/api/transcoding/cameras", h.handleTranscodingCameraConfigs)
	r.Get("/api/transcoding/recordings-without-transcode", h.handleTranscodingRecordingsWithoutTranscode)
}
