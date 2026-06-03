package api


import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/go-chi/chi/v5"
)

// --- Recording endpoints ---

func (h *Handler) handleListRecordings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	filter := model.RecordingFilter{
		CameraID: r.URL.Query().Get("camera_id"),
		Format:   model.Format(r.URL.Query().Get("format")),
	}

	if v := r.URL.Query().Get("merged"); v != "" {
		merged := v == "true" || v == "1"
		filter.Merged = &merged
	}

	if v := r.URL.Query().Get("start"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.StartTime = t
		}
	}

	if v := r.URL.Query().Get("end"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.EndTime = t
		}
	}

	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			filter.Limit = n
		}
	}

	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			filter.Offset = n
		}
	}

	// Sorting
	filter.SortBy = r.URL.Query().Get("sort_by")
	filter.SortOrder = r.URL.Query().Get("order")

	filter.Search = r.URL.Query().Get("search")

	recordings, err := h.db.ListRecordings(ctx, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list recordings")
		return
	}

	if recordings == nil {
		recordings = []model.Recording{}
	}

	total, err := h.db.CountRecordingsWithFilter(ctx, filter)
	if err != nil {
		total = 0 // non-fatal
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"recordings": recordings,
		"total":      total,
	})
}

func (h *Handler) handleGetRecording(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, http.StatusOK, rec)
}

func (h *Handler) handleDeleteRecording(w http.ResponseWriter, r *http.Request) {
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

	// Delete from DB first (authoritative source)
	if err := h.db.DeleteRecording(ctx, id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete recording")
		return
	}

	// Then delete file (non-fatal if fails)
	if rec.FilePath != "" {
		if err := h.store.DeleteFile(rec.FilePath); err != nil {
			logger.Warn("failed to delete file", "file_path", rec.FilePath, "error", err)
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) handleBatchDeleteRecordings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var body struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(body.IDs) == 0 {
		writeError(w, http.StatusBadRequest, "ids must not be empty")
		return
	}
	if len(body.IDs) > 100 {
		writeError(w, http.StatusBadRequest, "ids must not exceed 100")
		return
	}
	// Fetch file paths before batch delete
	filePaths := map[string]string{}
	for _, id := range body.IDs {
		rec, err := h.db.GetRecording(ctx, id)
		if err == nil && rec != nil && rec.FilePath != "" {
			filePaths[id] = rec.FilePath
		}
	}

	// Delete DB records (transaction)
	deleted, err := h.db.DeleteRecordingsBatch(ctx, body.IDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete recordings")
		return
	}

	// Attempt file deletion for successfully deleted records (non-fatal)
	failed := []string{}
	deletedSet := make(map[string]bool, len(deleted))
	for _, id := range deleted {
		deletedSet[id] = true
		if fp, ok := filePaths[id]; ok {
			if err := h.store.DeleteFile(fp); err != nil {
				logger.Warn("batch delete: failed to delete file", "file_path", fp, "error", err)
			}
		}
	}
	for _, id := range body.IDs {
		if !deletedSet[id] {
			failed = append(failed, id)
		}
	}

	result := map[string]any{"deleted": deleted}
	if len(failed) > 0 {
		result["failed"] = failed
	} else {
		result["failed"] = []string{}
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) handleDownloadRecording(w http.ResponseWriter, r *http.Request) {
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

	// Timelapse recordings are JPEG sequences — cannot be downloaded as a single file.
	if rec.Format == model.Format("timelapse") {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("Timelapse recordings (JPEG sequences) cannot be downloaded as a single file. Use /api/recordings/%s/timelapse-frames to access individual frames.", id))
		return
	}
	if rec.FilePath == "" {
		writeError(w, http.StatusNotFound, "file not available")
		return
	}

	// Validate that the recording file path is within the storage root to prevent
	// path traversal. This ensures rec.FilePath (which may come from external
	// sources like WebDAV uploads) is confined to the storage directory.
	validPath, err := storage.ValidatePath(h.store.RootDir(), rec.FilePath)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, &model.PathTraversalError{Path: rec.FilePath})
		return
	}

	// Check for frame parameter (MJPEG frame download)
	frameStr := r.URL.Query().Get("frame")
	if frameStr != "" && rec.Format == model.FormatMJPEG {
		frameIndex, err := strconv.Atoi(frameStr)
		if err == nil {
			entries, err := os.ReadDir(validPath)
			if err == nil {
				jpgFiles := []os.DirEntry{}
				for _, e := range entries {
					if !e.IsDir() && isImageFile(e.Name()) {
						jpgFiles = append(jpgFiles, e)
					}
				}
				sort.Slice(jpgFiles, func(i, j int) bool { return jpgFiles[i].Name() < jpgFiles[j].Name() })
				if frameIndex >= 0 && frameIndex < len(jpgFiles) {
					framePath := filepath.Join(validPath, jpgFiles[frameIndex].Name())
					http.ServeFile(w, r, framePath)
					return
				}
			}
		}
		http.Error(w, "frame not found", http.StatusNotFound)
		return
	}

	filePath := validPath
	info, err := os.Stat(filePath)
	if err != nil {
		writeError(w, http.StatusNotFound, "file not found")
		return
	}
	if info.IsDir() {
		entries, err := os.ReadDir(filePath)
		if err != nil || len(entries) == 0 {
			writeError(w, http.StatusNotFound, "no files in recording directory")
			return
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasSuffix(name, ".jpg") || strings.HasSuffix(name, ".jpeg") || strings.HasSuffix(name, ".mp4") {
				filePath = filepath.Join(filePath, name)
				break
			}
		}
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=\"%s\"", filepath.Base(filePath)))
	http.ServeFile(w, r, filePath)
}

func (h *Handler) handleListFrames(w http.ResponseWriter, r *http.Request) {
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

	if rec.Format != "mjpeg" {
		writeError(w, http.StatusBadRequest, "not a JPEG recording")
		return
	}

	filePath := rec.FilePath
	info, err := os.Stat(filePath)
	if err != nil {
		writeError(w, http.StatusNotFound, "recording files not found")
		return
	}
	if !info.IsDir() {
		writeError(w, http.StatusNotFound, "recording is not a directory")
		return
	}

	entries, err := os.ReadDir(filePath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read recording directory")
		return
	}

	type FrameInfo struct {
		Index    int    `json:"index"`
		Filename string `json:"filename"`
		Size     int64  `json:"size"`
	}

	var frames []FrameInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".jpg") && !strings.HasSuffix(strings.ToLower(name), ".jpeg") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		frames = append(frames, FrameInfo{
			Filename: name,
			Size:     fi.Size(),
		})
	}

	// Sort by filename (natural order - timestamp-based names sort correctly)
	sort.Slice(frames, func(i, j int) bool {
		return frames[i].Filename < frames[j].Filename
	})

	// Assign sequential indices
	for i := range frames {
		frames[i].Index = i
	}

	if frames == nil {
		frames = []FrameInfo{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"frames": frames,
	})
}

// --- Timelapse endpoints ---

// handleTimelapseFrames handles GET /api/recordings/{id}/timelapse-frames.
// Returns JSON array of JPEG frame metadata for timelapse recordings.
func (h *Handler) handleTimelapseFrames(w http.ResponseWriter, r *http.Request) {
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

	type TimelapseFrameInfo struct {
		Filename  string `json:"filename"`
		URL       string `json:"url"`
		Size      int64  `json:"size"`
		Timestamp string `json:"timestamp"`
	}

	var frames []TimelapseFrameInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".jpg") && !strings.HasSuffix(strings.ToLower(name), ".jpeg") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		frames = append(frames, TimelapseFrameInfo{
			Filename:  name,
			URL:       fmt.Sprintf("/api/recordings/%s/timelapse-frames/%s", id, name),
			Size:      fi.Size(),
			Timestamp: fi.ModTime().UTC().Format(time.RFC3339),
		})
	}

	sort.Slice(frames, func(i, j int) bool {
		return frames[i].Filename < frames[j].Filename
	})

	if frames == nil {
		frames = []TimelapseFrameInfo{}
	}

	writeJSON(w, http.StatusOK, frames)
}

// handleTimelapseFrame handles GET /api/recordings/{id}/timelapse-frames/{filename}.
// Serves an individual JPEG frame from a timelapse recording.
func (h *Handler) handleTimelapseFrame(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	filename := chi.URLParam(r, "filename")

	// Validate filename — only allow alphanumeric, underscore, dash, dot
	for _, c := range filename {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.') {
			writeError(w, http.StatusBadRequest, "invalid filename")
			return
		}
	}
	if filename == "" || filename == "." || filename == ".." {
		writeError(w, http.StatusBadRequest, "invalid filename")
		return
	}

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

	filePath := filepath.Join(rec.FilePath, filename)
	http.ServeFile(w, r, filePath)
}
