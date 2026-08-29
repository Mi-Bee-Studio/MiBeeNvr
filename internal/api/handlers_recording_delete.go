package api

import (
	"encoding/json"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
)

func (h *Handler) handleDeleteRecording(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx := r.Context()

	rec, err := h.db.GetRecording(ctx, id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil {
		WriteError(w, http.StatusNotFound, "recording not found")
		return
	}

	// Delete from DB first (authoritative source)
	if err := h.db.DeleteRecording(ctx, id); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to delete recording")
		return
	}

	// Then delete on-disk files (non-fatal if they fail).
	// The merged MP4 (merge_path) is the largest artifact and the one playback
	// actually loads; without this it leaked permanently because the orphan
	// scanner never reaches the nested YYYYMM/DD/HH/ tree. Mirrors
	// handleTimelapseDelete. os.RemoveAll tolerates a missing path.
	if rec.MergePath != "" {
		if err := os.RemoveAll(rec.MergePath); err != nil {
			logger.Warn("failed to delete merged file", "merge_path", rec.MergePath, "error", err)
		}
	}
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
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(body.IDs) == 0 {
		WriteError(w, http.StatusBadRequest, "ids must not be empty")
		return
	}
	if len(body.IDs) > 100 {
		WriteError(w, http.StatusBadRequest, "ids must not exceed 100")
		return
	}
	// Fetch on-disk paths before batch delete (need both source file_path and
	// the merged MP4 merge_path — see handleDeleteRecording for why merge_path
	// must be reclaimed here too).
	type recPaths struct {
		filePath  string
		mergePath string
	}
	paths := map[string]recPaths{}
	recordings, err := h.db.GetRecordingsByIDBatch(ctx, body.IDs)
	if err != nil {
		logger.Warn("batch delete: failed to fetch recordings", "error", err)
	} else {
		for _, rec := range recordings {
			paths[rec.ID] = recPaths{filePath: rec.FilePath, mergePath: rec.MergePath}
		}
	}

	// Delete DB records (transaction)
	deleted, err := h.db.DeleteRecordingsBatch(ctx, body.IDs)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to delete recordings")
		return
	}

	// Attempt file deletion for successfully deleted records (non-fatal)
	failed := []string{}
	deletedSet := make(map[string]bool, len(deleted))
	for _, id := range deleted {
		deletedSet[id] = true
		p, ok := paths[id]
		if !ok {
			continue
		}
		if p.mergePath != "" {
			if err := os.RemoveAll(p.mergePath); err != nil {
				logger.Warn("batch delete: failed to delete merged file", "merge_path", p.mergePath, "error", err)
			}
		}
		if p.filePath != "" {
			if err := h.store.DeleteFile(p.filePath); err != nil {
				logger.Warn("batch delete: failed to delete file", "file_path", p.filePath, "error", err)
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

// sortedImageFiles returns the sorted list of image filenames in dir, using a short-TTL
// cache to avoid os.ReadDir + sort on every request. The cache is invalidated when the
// directory's mtime changes (new frames written) or after frameListCacheTTL. This matters
// for MJPEG/timelapse frame dirs which can hold thousands of JPEGs; without it each
// ?frame=N / list-frames request re-scanned and re-sorted the whole directory.
