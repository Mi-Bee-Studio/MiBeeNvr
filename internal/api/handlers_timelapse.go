package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
	"github.com/go-chi/chi/v5"
)

// --- Timelapse configuration endpoints ---

// handleGetCameraTimelapse returns the timelapse configuration for a camera.
// GET /api/cameras/{id}/timelapse
func (h *Handler) handleGetCameraTimelapse(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if h.config == nil {
		writeError(w, http.StatusInternalServerError, "config not available")
		return
	}

	// Find camera in config
	var tl *config.CameraTimelapseConfig
	for i := range h.config.Cameras {
		if h.config.Cameras[i].ID == id {
			tl = h.config.Cameras[i].Timelapse
			break
		}
	}

	// Return timelapse config (nil means disabled/no config)
	if tl == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled":         false,
			"interval":        "30s",
			"frame_source":    "auto",
			"paused":          false,
			"delete_original": false,
			"merge_output_fps": 30,
			"merge_mode":       "auto",
			"daily_merge":      true,
			"merge_duration":   "natural-day",
		})
		return
	}

	writeJSON(w, http.StatusOK, tl)
}

// handlePutCameraTimelapse updates the timelapse configuration for a camera.
// PUT /api/cameras/{id}/timelapse
func (h *Handler) handlePutCameraTimelapse(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if h.config == nil {
		writeError(w, http.StatusInternalServerError, "config not available")
		return
	}
	if h.configPath == "" {
		writeError(w, http.StatusInternalServerError, "config path not available")
		return
	}

	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	var body config.CameraTimelapseConfig
	if err := json.Unmarshal(raw, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Backward compat: accept mergeDuration as alias for merge_duration
	if body.MergeDuration == "" {
		var legacy struct {
			MergeDuration string `json:"mergeDuration"`
		}
		if err := json.Unmarshal(raw, &legacy); err == nil && legacy.MergeDuration != "" {
			body.MergeDuration = legacy.MergeDuration
		}
	}

	// Validate interval
	if body.Interval != "" {
		dur, err := time.ParseDuration(body.Interval)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("interval must be a valid duration (e.g., \"5s\", \"1m\"): %v", err))
			return
		}
		if dur < time.Second {
			writeError(w, http.StatusBadRequest, "interval must be at least 1s")
			return
		}
	}

	// Validate frame_source
	if body.FrameSource != "" {
		switch body.FrameSource {
		case "auto", "snapshot", "rtsp_keyframe", "mjpeg":
			// valid
		default:
			writeError(w, http.StatusBadRequest, "frame_source must be \"auto\", \"snapshot\", \"rtsp_keyframe\", or \"mjpeg\"")
			return
		}
	}

	// Validate merge_mode
	if body.MergeMode != "" && body.MergeMode != "auto" && body.MergeMode != "mp4" && body.MergeMode != "jpeg" {
		writeError(w, http.StatusBadRequest, "merge_mode must be \"auto\", \"mp4\", or \"jpeg\"")
		return
	}

	// Validate merge_output_fps
	if body.MergeOutputFPS != 0 && (body.MergeOutputFPS < 1 || body.MergeOutputFPS > 60) {
		writeError(w, http.StatusBadRequest, "merge_output_fps must be between 1 and 60")
		return
	}

	// Find and update camera config in memory
	found := false
	for i := range h.config.Cameras {
		if h.config.Cameras[i].ID == id {
			h.config.Cameras[i].Timelapse = &body
			// Apply defaults to zero-value fields
			if body.Interval == "" {
				h.config.Cameras[i].Timelapse.Interval = "30s"
			}
			if body.FrameSource == "" {
				h.config.Cameras[i].Timelapse.FrameSource = "auto"
			}
			if body.MergeMode == "" {
				h.config.Cameras[i].Timelapse.MergeMode = "auto"
			}
			if body.DailyMerge == nil {
				v := true
				h.config.Cameras[i].Timelapse.DailyMerge = &v
			}
			if body.MergeOutputFPS == 0 {
				h.config.Cameras[i].Timelapse.MergeOutputFPS = 30
			}
			// MergeEnabled default is nil (auto-detect)
			found = true
			break
		}
	}

	if !found {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}

	// Persist config to disk
	if err := config.Save(h.configPath, h.config); err != nil {
		logger.Warn("failed to save config after timelapse update", "camera_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save config")
		return
	}

	// Update merge scheduler if MergeDuration changed
	if h.mergeScheduler != nil && body.MergeDuration != "" {
		if dur, err := config.ParseMergeDuration(body.MergeDuration); err == nil {
			h.mergeScheduler.AddOrUpdate(id, dur)
			slog.Debug("timelapse: updated merge scheduler", "camera_id", id, "duration", dur)
		}
	}

	writeJSON(w, http.StatusOK, h.config.Cameras)
}

// handleTimelapseStatus returns global timelapse merge defaults.
// GET /api/timelapse/status
func (h *Handler) handleTimelapseStatus(w http.ResponseWriter, r *http.Request) {
	activeCount := 0
	if h.timelapseMergeMgr != nil {
		activeCount = h.timelapseMergeMgr.ActiveCount()
	}
	defaultDailyMerge := true
	writeJSON(w, http.StatusOK, map[string]any{
		"merge_enabled":    false,
		"merge_mode":       "auto",
		"daily_merge":      defaultDailyMerge,
		"merge_output_fps": 30,
		"active_count":     activeCount,
	})
}

// handleTimelapseMergeProgress handles GET /api/timelapse/merge/progress/{cameraId}.
// SSE endpoint that streams merge progress updates for a specific camera.
// It sends progress events as the merge progresses and closes when complete.
func (h *Handler) handleTimelapseMergeProgress(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "cameraId")

	if h.timelapseMergeMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "timelapse merge manager not available")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx := r.Context()

	// Check if there's any progress info for this camera.
	info, ok := h.timelapseMergeMgr.GetProgress(cameraID)
	if !ok {
		// No progress tracked yet — send an initial event with status idle.
		data, _ := json.Marshal(timelapse.MergeProgressInfo{
			CameraID: cameraID,
			Progress: 0,
			Status:   "idle",
		})
		fmt.Fprintf(w, "event: progress\ndata: %s\n\n", data)
		flusher.Flush()
		return
	}

	// If already completed or failed, send the final event and return.
	if info.Status == "completed" || info.Status == "failed" {
		data, _ := json.Marshal(info)
		fmt.Fprintf(w, "event: progress\ndata: %s\n\n", data)
		flusher.Flush()
		return
	}

	// Stream progress updates until completion.
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		case <-ticker.C:
			// Poll progress.
			info, ok = h.timelapseMergeMgr.GetProgress(cameraID)
			if !ok {
				return
			}

			data, _ := json.Marshal(info)
			fmt.Fprintf(w, "event: progress\ndata: %s\n\n", data)
			flusher.Flush()

			// Stop if merge completed or failed.
			if info.Status == "completed" || info.Status == "failed" {
				return
			}
		}
	}
}

// timelapseResolvePath resolves the full path for a timelapse recording's segment directory.
// If the path is already absolute, use it as-is; otherwise prepend the storage root.
func timelapseResolvePath(rootDir, filePath string) string {
	if filepath.IsAbs(filePath) {
		return filePath
	}
	return filepath.Join(rootDir, filePath)
}

// handleTimelapseThumbnail handles GET /api/timelapse/{id}/thumbnail.
// It generates a thumbnail from the first frame of a timelapse recording,
// caches it on disk, and serves it.
func (h *Handler) handleTimelapseThumbnail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	rec, err := h.db.GetRecording(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil {
		writeError(w, http.StatusNotFound, "recording not found")
		return
	}

	if rec.Format != model.Format("timelapse") && rec.Format != "mjpeg" {
		writeError(w, http.StatusNotFound, "not a timelapse recording")
		return
	}

	// Cache path
	cacheDir := filepath.Join(h.store.RootDir(), ".timelapse-thumbnails")
	cachePath := filepath.Join(cacheDir, id+".jpg")

	// Determine source: merged MP4 or segment dir
	var sourcePath string
	var useFFmpeg bool
	if rec.MergeStatus == "merged" && rec.MergePath != "" {
		sourcePath = rec.MergePath
		useFFmpeg = true
	} else {
		sourcePath = timelapseResolvePath(h.store.RootDir(), rec.FilePath)
	}
	segDir := timelapseResolvePath(h.store.RootDir(), rec.FilePath)
	body, err := h.generateThumbnail(sourcePath, segDir, cachePath, useFFmpeg)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "max-age=86400, private")
	w.Write(body)
}

// generateThumbnail generates a thumbnail from sourcePath, caches it at cachePath.
// If useFFmpeg is true, it tries to extract a frame from an MP4 using ffmpeg.
// segDir is the fallback segment directory for JPEG frames when ffmpeg fails.
func (h *Handler) generateThumbnail(sourcePath, segDir, cachePath string, useFFmpeg bool) ([]byte, error) {
	// Check cache
	if data, ok := h.serveCachedThumbnail(sourcePath, cachePath); ok {
		return data, nil
	}

	var src image.Image
	if useFFmpeg {
		data, err := h.extractJPEGFromMP4(sourcePath)
		if err != nil {
			// Fallback: try recording's segment directory
			img, err := h.firstJPEGFrame(segDir)
			if err != nil {
				return nil, fmt.Errorf("thumbnail unavailable")
			}
			src = img
		} else {
			img, _, err := image.Decode(bytes.NewReader(data))
			if err != nil {
				// Fallback to recording's segment directory
				img, err := h.firstJPEGFrame(segDir)
				if err != nil {
					return nil, fmt.Errorf("thumbnail unavailable")
				}
				src = img
			} else {
				src = img
			}
		}
	} else {
		img, err := h.firstJPEGFrame(sourcePath)
		if err != nil {
			return nil, err
		}
		src = img
	}

	// Resize to max 640x360
	resized := resizeImage(src, 640, 360)

	// Encode as JPEG
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, resized, &jpeg.Options{Quality: 80}); err != nil {
		return nil, fmt.Errorf("failed to encode thumbnail: %w", err)
	}

	data := buf.Bytes()

	// Save to cache
	if err := os.MkdirAll(filepath.Dir(cachePath), 0755); err != nil {
		logger.Warn("failed to create thumbnail cache dir", "path", filepath.Dir(cachePath), "error", err)
	} else {
		if err := os.WriteFile(cachePath, data, 0644); err != nil {
			logger.Warn("failed to write thumbnail cache", "path", cachePath, "error", err)
		}
	}

	return data, nil
}

// serveCachedThumbnail checks if a cached thumbnail exists and is fresh.
// Returns the cached data and true if cache is valid.
func (h *Handler) serveCachedThumbnail(sourcePath, cachePath string) ([]byte, bool) {
	cacheInfo, err := os.Stat(cachePath)
	if err != nil {
		return nil, false
	}

	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		return nil, false
	}

	// Cache is valid if source hasn't been modified since cache was created
	if sourceInfo.ModTime().After(cacheInfo.ModTime()) {
		return nil, false
	}

	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, false
	}
	return data, true
}

// firstJPEGFrame finds and decodes the first JPEG frame from a directory.
func (h *Handler) firstJPEGFrame(dirPath string) (image.Image, error) {
	info, err := os.Stat(dirPath)
	if err != nil {
		return nil, fmt.Errorf("segment directory not found")
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("not a directory")
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read segment directory")
	}

	// Collect and sort JPEG files
	var jpgFiles []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		if strings.HasSuffix(name, ".jpg") || strings.HasSuffix(name, ".jpeg") {
			jpgFiles = append(jpgFiles, e.Name())
		}
	}

	if len(jpgFiles) == 0 {
		return nil, fmt.Errorf("no JPEG frames found")
	}

	sort.Strings(jpgFiles)

	firstPath := filepath.Join(dirPath, jpgFiles[0])
	f, err := os.Open(firstPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open first frame: %w", err)
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("failed to decode first frame: %w", err)
	}

	return img, nil
}

// extractJPEGFromMP4 extracts the first frame from an MP4 as JPEG data using ffmpeg.
func (h *Handler) extractJPEGFromMP4(path string) ([]byte, error) {
	// Check if ffmpeg is available
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, fmt.Errorf("ffmpeg not available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-i", path,
		"-vframes", "1",
		"-f", "image2pipe",
		"-vcodec", "mjpeg",
		"-",
	)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg extraction failed: %w, stderr: %s", err, stderr.String()[:min(len(stderr.String()), 200)])
	}

	if stdout.Len() == 0 {
		return nil, fmt.Errorf("ffmpeg returned empty output")
	}

	return stdout.Bytes(), nil
}

// resizeImage resizes an image to fit within maxW x maxH while maintaining aspect ratio.
func resizeImage(src image.Image, maxW, maxH int) image.Image {
	bounds := src.Bounds()
	srcW := bounds.Dx()
	srcH := bounds.Dy()

	w, h := srcW, srcH
	if w > maxW {
		h = h * maxW / w
		w = maxW
	}
	if h > maxH {
		w = w * maxH / h
		h = maxH
	}

	if w >= srcW && h >= srcH {
		return src
	}

	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for dy := 0; dy < h; dy++ {
		for dx := 0; dx < w; dx++ {
			sx := float64(dx) * float64(srcW) / float64(w)
			sy := float64(dy) * float64(srcH) / float64(h)
			dst.Set(dx, dy, bilinearInterpolate(src, sx, sy))
		}
	}
	return dst
}

// bilinearInterpolate performs bilinear interpolation at the given floating-point coordinates.
func bilinearInterpolate(src image.Image, x, y float64) color.Color {
	bounds := src.Bounds()
	srcW := bounds.Dx()
	srcH := bounds.Dy()

	x0 := int(math.Floor(x))
	y0 := int(math.Floor(y))
	x1 := x0 + 1
	y1 := y0 + 1

	if x1 >= srcW {
		x1 = srcW - 1
	}
	if y1 >= srcH {
		y1 = srcH - 1
	}

	// Clamp to bounds
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}

	fx := x - float64(x0)
	fy := y - float64(y0)

	c00 := color.RGBAModel.Convert(src.At(x0, y0)).(color.RGBA)
	c10 := color.RGBAModel.Convert(src.At(x1, y0)).(color.RGBA)
	c01 := color.RGBAModel.Convert(src.At(x0, y1)).(color.RGBA)
	c11 := color.RGBAModel.Convert(src.At(x1, y1)).(color.RGBA)

	r := float64(c00.R)*(1-fx)*(1-fy) + float64(c10.R)*fx*(1-fy) + float64(c01.R)*(1-fx)*fy + float64(c11.R)*fx*fy
	g := float64(c00.G)*(1-fx)*(1-fy) + float64(c10.G)*fx*(1-fy) + float64(c01.G)*(1-fx)*fy + float64(c11.G)*fx*fy
	b := float64(c00.B)*(1-fx)*(1-fy) + float64(c10.B)*fx*(1-fy) + float64(c01.B)*(1-fx)*fy + float64(c11.B)*fx*fy
	a := float64(c00.A)*(1-fx)*(1-fy) + float64(c10.A)*fx*(1-fy) + float64(c01.A)*(1-fx)*fy + float64(c11.A)*fx*fy

	return color.RGBA{
		R: uint8(clampFloat(r)),
		G: uint8(clampFloat(g)),
		B: uint8(clampFloat(b)),
		A: uint8(clampFloat(a)),
	}
}

// clampFloat clamps a float64 value to the [0, 255] range.
func clampFloat(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

// min returns the smaller of two integers.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// --- Timelapse API endpoints ---

// handleTimelapseList handles GET /api/timelapse.
// Lists timelapse and MJPEG recordings with pagination.
func (h *Handler) handleTimelapseList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Parse pagination params with abuse prevention
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 1000 {
		limit = 1000
	}

	offset := 0
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	// Parse optional filters
	cameraID := r.URL.Query().Get("camera_id")
	sortBy := r.URL.Query().Get("sort_by")
	sortOrder := r.URL.Query().Get("sort_order")

	// Build filter for both timelapse and MJPEG recordings
	filter := model.RecordingFilter{
		Formats:   []model.Format{model.FormatTimelapse, model.FormatMJPEG},
		CameraID:  cameraID,
		Limit:     limit,
		Offset:    offset,
		SortBy:    sortBy,
		SortOrder: sortOrder,
	}

	// Get total count for pagination
	total, err := h.db.CountRecordingsWithFilter(ctx, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to count recordings")
		return
	}

	// Get paginated results from DB
	recordings, err := h.db.ListRecordings(ctx, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list recordings")
		return
	}

	if recordings == nil {
		recordings = []model.Recording{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"recordings": recordings,
		"total":      total,
	})
}

// handleTimelapseMerge handles POST /api/timelapse/{id}/merge.
// Triggers a merge for the specified camera. Accepts optional duration
// query param (e.g., "8h", "12h", "24h", "natural-day", "7d", "30d")
// for custom merge windows. Without duration, uses daily merge (backward compat).
func (h *Handler) handleTimelapseMerge(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")

	// Dedup: prevent concurrent merges for the same camera
	_, loaded := h.activeMerges.LoadOrStore(cameraID, struct{}{})
	if loaded {
		writeError(w, http.StatusConflict, "a merge is already in progress for this camera")
		return
	}

	// Parse optional duration query param for custom merge windows
	durationStr := r.URL.Query().Get("duration")
	if durationStr != "" {
		h.handleTimelapseMergeWithDuration(w, r, cameraID, durationStr)
		return
	}

	// No duration — use configured DailyMergeManager (backward compat)
	if h.timelapseDailyMgr == nil {
		h.activeMerges.Delete(cameraID)
		writeError(w, http.StatusServiceUnavailable, "timelapse daily merge manager not available")
		return
	}

	date := r.URL.Query().Get("date")
	if date == "" {
		// Compute "yesterday" in the configured display timezone
		loc := time.UTC
		if h.config != nil && h.config.Timezone != "" && h.config.Timezone != "UTC" {
			if l, err := time.LoadLocation(h.config.Timezone); err == nil {
				loc = l
			}
		}
		date = time.Now().In(loc).Add(-24 * time.Hour).Format("2006-01-02")
	}

	go func() {
		defer h.activeMerges.Delete(cameraID)
		ctx := context.Background()
		if err := h.timelapseDailyMgr.Run(ctx, cameraID, date); err != nil {
			logger.Warn("timelapse daily merge failed", "camera_id", cameraID, "date", date, "error", err)
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":    "merge_initiated",
		"camera_id": cameraID,
		"date":      date,
	})
}

// handleTimelapseMergeWithDuration handles merge with a custom duration.
func (h *Handler) handleTimelapseMergeWithDuration(w http.ResponseWriter, r *http.Request, cameraID, durationStr string) {
	dur, err := config.ParseMergeDuration(durationStr)
	if err != nil {
		h.activeMerges.Delete(cameraID)
		writeError(w, http.StatusBadRequest, "invalid duration: "+err.Error())
		return
	}
	if h.db == nil {
		h.activeMerges.Delete(cameraID)
		writeError(w, http.StatusInternalServerError, "database not available")
		return
	}
	if h.config == nil {
		h.activeMerges.Delete(cameraID)
		writeError(w, http.StatusInternalServerError, "config not available")
		return
	}

	// Get FPS from camera's timelapse config, default 10
	fps := 10
	for i := range h.config.Cameras {
		if h.config.Cameras[i].ID == cameraID && h.config.Cameras[i].Timelapse != nil {
			if h.config.Cameras[i].Timelapse.MergeOutputFPS > 0 {
				fps = h.config.Cameras[i].Timelapse.MergeOutputFPS
			}
			break
		}
	}

	// Load display timezone
	loc := time.UTC
	if h.config.Timezone != "" && h.config.Timezone != "UTC" {
		if l, err := time.LoadLocation(h.config.Timezone); err == nil {
			loc = l
		}
	}

	dataDir := filepath.Join(h.config.Storage.RootDir, "daily-merge")

	// Parse date or use current time as reference in the configured timezone
	dateStr := r.URL.Query().Get("date")
	refTime := time.Now().In(loc)
	if dateStr != "" {
		parsed, err := time.ParseInLocation("2006-01-02", dateStr, loc)
		if err == nil {
			refTime = parsed
		}
	}

	// Create PeriodicMergeManager with the specified duration
	mgr := timelapse.NewPeriodicMergeManager(h.db, h.db, timelapse.NewGoMerger(), fps, dataDir, dur, loc)

	go func() {
		defer h.activeMerges.Delete(cameraID)
		ctx := context.Background()
		if err := mgr.Run(ctx, cameraID, refTime); err != nil {
			logger.Warn("timelapse merge failed", "camera_id", cameraID, "duration", durationStr, "error", err)
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":    "merge_initiated",
		"camera_id": cameraID,
		"date":      refTime.Format("2006-01-02"),
		"duration":  durationStr,
	})
}

// handleTimelapsePause handles POST /api/timelapse/{id}/pause.
// Pauses timelapse recording for a camera.
func (h *Handler) handleTimelapsePause(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")

	if h.config == nil {
		writeError(w, http.StatusInternalServerError, "config not available")
		return
	}
	if h.configPath == "" {
		writeError(w, http.StatusInternalServerError, "config path not available")
		return
	}

	// Find camera in config
	found := false
	for i := range h.config.Cameras {
		if h.config.Cameras[i].ID == cameraID {
			if h.config.Cameras[i].Timelapse == nil {
				writeError(w, http.StatusNotFound, "camera has no timelapse configuration")
				return
			}
			h.config.Cameras[i].Timelapse.Paused = true
			found = true
			break
		}
	}

	if !found {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}

	// Stop the recorder via camera manager (immediate, not waiting for schedule monitor)
	if h.camMgr != nil {
		if err := h.camMgr.PauseTimelapse(r.Context(), cameraID); err != nil {
			logger.Warn("failed to pause timelapse recorder", "camera_id", cameraID, "error", err)
		}
	}

	// Persist config to disk
	if err := config.Save(h.configPath, h.config); err != nil {
		logger.Warn("failed to save config after timelapse pause", "camera_id", cameraID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save config")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "paused"})
}

// handleTimelapseResume handles POST /api/timelapse/{id}/resume.
// Resumes timelapse recording for a camera.
func (h *Handler) handleTimelapseResume(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")

	if h.config == nil {
		writeError(w, http.StatusInternalServerError, "config not available")
		return
	}
	if h.configPath == "" {
		writeError(w, http.StatusInternalServerError, "config path not available")
		return
	}

	// Find camera in config
	found := false
	for i := range h.config.Cameras {
		if h.config.Cameras[i].ID == cameraID {
			if h.config.Cameras[i].Timelapse == nil {
				writeError(w, http.StatusNotFound, "camera has no timelapse configuration")
				return
			}
			h.config.Cameras[i].Timelapse.Paused = false
			found = true
			break
		}
	}

	if !found {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}

	// Start the recorder via camera manager (immediate, not waiting for schedule monitor)
	if h.camMgr != nil {
		if err := h.camMgr.ResumeTimelapse(r.Context(), cameraID); err != nil {
			logger.Warn("failed to resume timelapse recorder", "camera_id", cameraID, "error", err)
		}
	}

	// Persist config to disk
	if err := config.Save(h.configPath, h.config); err != nil {
		logger.Warn("failed to save config after timelapse resume", "camera_id", cameraID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save config")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "resumed"})
}

// handleTimelapseGet handles GET /api/timelapse/{id}.
// Returns metadata for a single timelapse or MJPEG recording.
func (h *Handler) handleTimelapseGet(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	rec, err := h.db.GetRecording(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil {
		writeError(w, http.StatusNotFound, "recording not found")
		return
	}

	if rec.Format != model.Format("timelapse") && rec.Format != "mjpeg" {
		writeError(w, http.StatusNotFound, "not a timelapse recording")
		return
	}

	writeJSON(w, http.StatusOK, rec)
}

// handleTimelapseDelete handles DELETE /api/timelapse/{id}.
// Deletes a timelapse or MJPEG recording and its associated files.
func (h *Handler) handleTimelapseDelete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx := r.Context()

	rec, err := h.db.GetRecording(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil {
		writeError(w, http.StatusNotFound, "recording not found")
		return
	}

	if rec.Format != model.Format("timelapse") && rec.Format != "mjpeg" {
		writeError(w, http.StatusNotFound, "not a timelapse recording")
		return
	}

	// Delete from DB first (authoritative source)
	if err := h.db.DeleteRecording(ctx, id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete recording")
		return
	}

	// Delete merged file if exists
	if rec.MergePath != "" {
		if err := os.RemoveAll(rec.MergePath); err != nil {
			logger.Warn("failed to delete merged file", "merge_path", rec.MergePath, "error", err)
		}
	}

	// Delete source segment directory (if it's a directory)
	if rec.FilePath != "" {
		info, err := os.Stat(rec.FilePath)
		if err == nil && info.IsDir() {
			if err := os.RemoveAll(rec.FilePath); err != nil {
				logger.Warn("failed to delete segment directory", "file_path", rec.FilePath, "error", err)
			}
		} else if err == nil && !info.IsDir() {
			// File-based recording
			if err := h.store.DeleteFile(rec.FilePath); err != nil {
				logger.Warn("failed to delete file", "file_path", rec.FilePath, "error", err)
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleTimelapseDownload handles POST /api/timelapse/{id}/download.
// Serves the merged MP4 file for a timelapse recording.
func (h *Handler) handleTimelapseDownload(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	rec, err := h.db.GetRecording(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil {
		writeError(w, http.StatusNotFound, "recording not found")
		return
	}

	if rec.MergeStatus != "merged" || rec.MergePath == "" {
		writeError(w, http.StatusNotFound, "merged recording not available")
		return
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filepath.Base(rec.MergePath)))
	http.ServeFile(w, r, rec.MergePath)
}

// --- Task 7: Merge Cancellation ---
// handleTimelapseMergeCancel handles DELETE /api/timelapse/{cameraId}/merge.
// Cancels an active rolling merge for the specified camera.
func (h *Handler) handleTimelapseMergeCancel(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "cameraId")

	if h.timelapseMergeMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "timelapse merge manager not available")
		return
	}

	if !h.timelapseMergeMgr.IsActive(cameraID) {
		writeError(w, http.StatusNotFound, "no active merge for this camera")
		return
	}

	h.timelapseMergeMgr.StopSegmentMerge(cameraID)

	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

// --- Task 12: Batch Merge ---
// handleTimelapseBatchMerge handles POST /api/timelapse/batch-merge.
// Triggers a merge for multiple cameras at once (max 10).
func (h *Handler) handleTimelapseBatchMerge(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CameraIDs []string `json:"camera_ids"`
		Duration  string   `json:"duration"`
		Date      string   `json:"date"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if len(body.CameraIDs) == 0 {
		writeError(w, http.StatusBadRequest, "camera_ids must not be empty")
		return
	}

	if len(body.CameraIDs) > 10 {
		writeError(w, http.StatusBadRequest, "batch size exceeds maximum of 10 cameras")
		return
	}

	if body.Duration == "" {
		body.Duration = "natural-day"
	}

	dur, err := config.ParseMergeDuration(body.Duration)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid duration: "+err.Error())
		return
	}

	if h.db == nil {
		writeError(w, http.StatusInternalServerError, "database not available")
		return
	}
	if h.config == nil {
		writeError(w, http.StatusInternalServerError, "config not available")
		return
	}

	// Load timezone
	loc := time.UTC
	if h.config.Timezone != "" && h.config.Timezone != "UTC" {
		if l, err := time.LoadLocation(h.config.Timezone); err == nil {
			loc = l
		}
	}

	dataDir := filepath.Join(h.config.Storage.RootDir, "daily-merge")

	// Parse date
	refTime := time.Now().In(loc)
	if body.Date != "" {
		if parsed, err := time.ParseInLocation("2006-01-02", body.Date, loc); err == nil {
			refTime = parsed
		}
	}

	type batchResult struct {
		CameraID string `json:"camera_id"`
		Status   string `json:"status"`
		Error    string `json:"error,omitempty"`
	}

	results := make([]batchResult, 0, len(body.CameraIDs))
	triggered := 0

	for _, cameraID := range body.CameraIDs {
		// Get FPS from camera's timelapse config
		fps := 10
		for i := range h.config.Cameras {
			if h.config.Cameras[i].ID == cameraID && h.config.Cameras[i].Timelapse != nil {
				if h.config.Cameras[i].Timelapse.MergeOutputFPS > 0 {
					fps = h.config.Cameras[i].Timelapse.MergeOutputFPS
				}
				break
			}
		}

		mgr := timelapse.NewPeriodicMergeManager(h.db, h.db, timelapse.NewGoMerger(), fps, dataDir, dur, loc)

		// Launch merge in background
		go func(camID string, mgr *timelapse.PeriodicMergeManager, ref time.Time) {
			ctx := context.Background()
			if err := mgr.Run(ctx, camID, ref); err != nil {
				logger.Warn("timelapse batch merge failed", "camera_id", camID, "duration", body.Duration, "error", err)
			}
		}(cameraID, mgr, refTime)

		triggered++
		results = append(results, batchResult{
			CameraID: cameraID,
			Status:   "merge_initiated",
		})
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"results":   results,
		"triggered": triggered,
	})
}

// --- Task 21: Frame Preview ---
// handleTimelapsePreview handles GET /api/timelapse/{id}/preview.
// Returns N evenly-spaced frames as a JSON array.
func (h *Handler) handleTimelapsePreview(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	rec, err := h.db.GetRecording(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil {
		writeError(w, http.StatusNotFound, "recording not found")
		return
	}
	if rec.Format != model.Format("timelapse") {
		writeError(w, http.StatusNotFound, "not a timelapse recording")
		return
	}

	info, err := os.Stat(rec.FilePath)
	if err != nil {
		writeError(w, http.StatusNotFound, "timelapse directory not found")
		return
	}
	if !info.IsDir() {
		writeError(w, http.StatusNotFound, "timelapse recording is not a directory")
		return
	}

	entries, err := os.ReadDir(rec.FilePath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read timelapse directory")
		return
	}

	// Collect frames sorted by name
	var frames []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !isTimelapseFrame(name) {
			continue
		}
		frames = append(frames, name)
	}
	sort.Strings(frames)

	// Parse sample parameter (default 6, max 20)
	sampleCount := 6
	if v := r.URL.Query().Get("sample"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			sampleCount = n
		}
	}
	if sampleCount > 20 {
		sampleCount = 20
	}

	// Select evenly-spaced frames
	totalFrames := len(frames)
	var selected []string
	if totalFrames <= sampleCount {
		selected = frames
	} else {
		step := float64(totalFrames-1) / float64(sampleCount-1)
		for i := 0; i < sampleCount; i++ {
			idx := int(math.Round(float64(i) * step))
			if idx >= totalFrames {
				idx = totalFrames - 1
			}
			selected = append(selected, frames[idx])
		}
	}

	type previewFrame struct {
		Filename string `json:"filename"`
		URL      string `json:"url"`
	}

	result := make([]previewFrame, 0, len(selected))
	for _, f := range selected {
		result = append(result, previewFrame{
			Filename: f,
			URL:      fmt.Sprintf("/api/recordings/%s/timelapse-frames/%s", id, f),
		})
	}

	writeJSON(w, http.StatusOK, result)
}
