package api

import (
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/go-chi/chi/v5"
)

// --- Archive endpoints ---

// archiveGroupItem is the JSON response for a single archive group.
type archiveGroupItem struct {
	ID                   string     `json:"id"`
	Name                 string     `json:"name"`
	RecordingCount       int        `json:"recording_count"`
	TotalSize            int64      `json:"total_size"`
	ArchivedAt           *time.Time `json:"archived_at,omitempty"`
	ArchiveRetentionDays int        `json:"archive_retention_days"`
}

func (h *Handler) handleListArchives(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cameras, err := h.db.ListArchivedCameras(ctx)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to list archived cameras")
		return
	}
	if cameras == nil {
		cameras = []storage.CameraRow{}
	}

	items := make([]archiveGroupItem, 0, len(cameras))
	for _, cam := range cameras {
		count, totalSize, err := h.db.GetArchiveGroupStats(ctx, cam.ID)
		if err != nil {
			logger.Warn("failed to get archive stats", "camera_id", cam.ID, "error", err)
			count, totalSize = 0, 0
		}
		items = append(items, archiveGroupItem{
			ID:                   cam.ID,
			Name:                 cam.Name,
			RecordingCount:       count,
			TotalSize:            totalSize,
			ArchivedAt:           cam.ArchivedAt,
			ArchiveRetentionDays: cam.ArchiveRetentionDays,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"archives": items})
}

func (h *Handler) handleListArchiveRecordings(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "cameraID")
	ctx := r.Context()

	trueVal := true
	filter := model.RecordingFilter{
		CameraID: cameraID,
		Archived: &trueVal,
	}
	filter.Limit, filter.Offset = parsePagination(r, 0, 0)
	filter.SortBy = r.URL.Query().Get("sort_by")
	filter.SortOrder = r.URL.Query().Get("order")

	recordings, total, err := h.db.ListRecordingsWithTotal(ctx, filter)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to list archived recordings")
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

func (h *Handler) handleDeleteArchiveGroup(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "cameraID")
	ctx := r.Context()

	// Verify camera is archived
	cam, err := h.db.GetCamera(ctx, cameraID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get camera")
		return
	}
	if cam == nil || !cam.Archived {
		WriteError(w, http.StatusNotFound, "archived camera not found")
		return
	}

	// Get all archived recording IDs for batch DB deletion
	trueVal := true
	recordings, err := h.db.ListRecordings(ctx, model.RecordingFilter{CameraID: cameraID, Archived: &trueVal})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to list recordings")
		return
	}

	// Delete all recording rows from DB (single transaction)
	ids := make([]string, len(recordings))
	for i, rec := range recordings {
		ids[i] = rec.ID
	}
	if len(ids) > 0 {
		if _, err := h.db.DeleteRecordingsBatch(ctx, ids); err != nil {
			WriteError(w, http.StatusInternalServerError, "failed to delete recordings")
			return
		}
	}

	// Delete camera row from DB
	if err := h.db.DeleteCamera(ctx, cameraID); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to delete camera")
		return
	}

	// Remove entire camera directory from disk (handles all files at once)
	if err := h.store.DeleteCameraDir(cameraID); err != nil {
		logger.Warn("failed to remove camera directory", "camera_id", cameraID, "error", err)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) handleDeleteArchiveRecording(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "cameraID")
	recordingID := chi.URLParam(r, "recordingID")
	ctx := r.Context()

	// Get the recording
	rec, err := h.db.GetRecording(ctx, recordingID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil || !rec.Archived || rec.CameraID != cameraID {
		WriteError(w, http.StatusNotFound, "archived recording not found")
		return
	}

	// Delete on-disk files (non-fatal). Reclaim the merged MP4 (merge_path) too
	// — it is the largest artifact and would otherwise leak permanently.
	// Mirrors handleDeleteRecording / handleTimelapseDelete.
	if rec.MergePath != "" {
		if err := os.RemoveAll(rec.MergePath); err != nil {
			logger.Warn("failed to delete archived merged file", "merge_path", rec.MergePath, "error", err)
		}
	}
	if rec.FilePath != "" {
		if err := h.store.DeleteFile(rec.FilePath); err != nil {
			logger.Warn("failed to delete archived recording file", "file_path", rec.FilePath, "error", err)
		}
	}

	// Delete recording from DB
	if err := h.db.DeleteRecording(ctx, recordingID); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to delete recording")
		return
	}

	// Check if this was the last archived recording for this camera
	count, _, err := h.db.GetArchiveGroupStats(ctx, cameraID)
	if err == nil && count == 0 {
		// No more recordings — clean up camera row and directory
		if err := h.db.DeleteCamera(ctx, cameraID); err != nil {
			logger.Warn("failed to delete empty archive camera", "camera_id", cameraID, "error", err)
		}
		if err := h.store.DeleteCameraDir(cameraID); err != nil {
			logger.Warn("failed to remove camera directory", "camera_id", cameraID, "error", err)
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) handleSetArchiveRetention(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "cameraID")
	ctx := r.Context()

	var body struct {
		RetentionDays int `json:"retention_days"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.RetentionDays < 0 {
		WriteError(w, http.StatusBadRequest, "retention_days must be >= 0")
		return
	}

	if err := h.db.SetArchiveRetention(ctx, cameraID, body.RetentionDays); err != nil {
		WriteError(w, http.StatusNotFound, "archived camera not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// registerArchiveRoutes registers archive list/delete/retention routes.
func (h *Handler) registerArchiveRoutes(r chi.Router) {
	r.Route("/api/archives", func(r chi.Router) {
		r.Get("/", h.handleListArchives)
		r.Get("/{cameraID}/recordings", h.handleListArchiveRecordings)
		r.Delete("/{cameraID}", h.handleDeleteArchiveGroup)
		r.Delete("/{cameraID}/recordings/{recordingID}", h.handleDeleteArchiveRecording)
		r.Put("/{cameraID}/retention", h.handleSetArchiveRetention)
	})
}
