package upload

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/api"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Handler handles HTTP upload endpoints for camera frames and videos.
type Handler struct {
	storageMgr    *storage.Manager
	db            *storage.DB
	maxUploadSize int64
}

// NewHandler creates a new upload Handler.
func NewHandler(mgr *storage.Manager, db *storage.DB, maxUploadSize int64) *Handler {
	return &Handler{
		storageMgr:    mgr,
		db:            db,
		maxUploadSize: maxUploadSize,
	}
}

// RegisterRoutes registers upload routes on the given chi.Router.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Post("/api/upload/{camera_id}", h.handleUploadJPEG)
	r.Post("/api/upload/{camera_id}/batch", h.handleUploadBatch)
	r.Post("/api/upload/{camera_id}/video", h.handleUploadVideo)
}

// uploadResponse is the JSON response for successful uploads.
type uploadResponse struct {
	ID         string `json:"id"`
	CameraID   string `json:"camera_id"`
	FilePath   string `json:"file_path"`
	Format     string `json:"format"`
	FrameCount int    `json:"frame_count"`
	FileSize   int64  `json:"file_size"`
}

func (h *Handler) handleUploadJPEG(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "camera_id")
	if err := h.validateCamera(r.Context(), cameraID); err != nil {
		api.WriteError(w, http.StatusNotFound, err.Error())
		return
	}
	ct := r.Header.Get("Content-Type")
	if ct != "image/jpeg" {
		api.WriteError(w, http.StatusBadRequest, fmt.Sprintf("unsupported content type %q, expected image/jpeg", ct))
		return
	}

	data, oversized, err := readBody(r.Body, h.maxUploadSize)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "failed to read request body")
		return
	}
	if oversized {
		api.WriteError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("upload exceeds maximum size of %d bytes", h.maxUploadSize))
		return
	}

	tempPath, finalPath, err := h.storageMgr.CreateSegment(cameraID, string(model.FormatMJPEG))
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "failed to create segment")
		return
	}

	if _, err := h.storageMgr.WriteFrame(tempPath, data); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "failed to write frame")
		return
	}

	if err := h.storageMgr.CloseSegment(tempPath, finalPath); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "failed to close segment")
		return
	}

	rec := &model.Recording{
		ID:          uuid.New().String(),
		CameraID:    cameraID,
		FilePath:    finalPath,
		Format:      model.FormatMJPEG,
		StartedAt:   time.Now(),
		EndedAt:     time.Now(),
		Duration:    0,
		FileSize:    int64(len(data)),
		FrameCount:  1,
		MergeStatus: model.MergeStatusPending,
	}

	if err := h.db.InsertRecording(r.Context(), rec); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "failed to save recording metadata")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(uploadResponse{
		ID:         rec.ID,
		CameraID:   rec.CameraID,
		FilePath:   rec.FilePath,
		Format:     string(rec.Format),
		FrameCount: rec.FrameCount,
		FileSize:   rec.FileSize,
	})
}

func (h *Handler) handleUploadBatch(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "camera_id")
	if err := h.validateCamera(r.Context(), cameraID); err != nil {
		api.WriteError(w, http.StatusNotFound, err.Error())
		return
	}
	ct := r.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "multipart/form-data") {
		api.WriteError(w, http.StatusBadRequest, fmt.Sprintf("unsupported content type %q, expected multipart/form-data", ct))
		return
	}

	if err := r.ParseMultipartForm(h.maxUploadSize); err != nil {
		api.WriteError(w, http.StatusBadRequest, "failed to parse multipart form")
		return
	}
	defer r.MultipartForm.RemoveAll()

	if r.MultipartForm == nil || r.MultipartForm.File == nil {
		api.WriteError(w, http.StatusBadRequest, "no files in upload")
		return
	}

	files := r.MultipartForm.File["frames"]
	if len(files) == 0 {
		api.WriteError(w, http.StatusBadRequest, "no frames found in upload")
		return
	}

	tempPath, finalPath, err := h.storageMgr.CreateSegment(cameraID, string(model.FormatMJPEG))
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "failed to create segment")
		return
	}

	var totalSize int64
	for _, fh := range files {
		f, err := fh.Open()
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, "failed to open uploaded file")
			return
		}
		data, err := io.ReadAll(io.LimitReader(f, h.maxUploadSize))
		f.Close()
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, "failed to read uploaded file")
			return
		}
		if _, err := h.storageMgr.WriteFrame(tempPath, data); err != nil {
			api.WriteError(w, http.StatusInternalServerError, "failed to write frame")
			return
		}
		totalSize += int64(len(data))
	}

	if err := h.storageMgr.CloseSegment(tempPath, finalPath); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "failed to close segment")
		return
	}

	rec := &model.Recording{
		ID:          uuid.New().String(),
		CameraID:    cameraID,
		FilePath:    finalPath,
		Format:      model.FormatMJPEG,
		StartedAt:   time.Now(),
		EndedAt:     time.Now(),
		Duration:    0,
		FileSize:    totalSize,
		FrameCount:  len(files),
		MergeStatus: model.MergeStatusPending,
	}

	if err := h.db.InsertRecording(r.Context(), rec); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "failed to save recording metadata")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(uploadResponse{
		ID:         rec.ID,
		CameraID:   rec.CameraID,
		FilePath:   rec.FilePath,
		Format:     string(rec.Format),
		FrameCount: rec.FrameCount,
		FileSize:   rec.FileSize,
	})
}

var allowedVideoTypes = map[string]bool{
	"video/mp4":        true,
	"video/avi":        true,
	"video/x-msvideo":  true,
	"video/quicktime":  true,
	"video/x-matroska": true,
}

func (h *Handler) handleUploadVideo(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "camera_id")
	if err := h.validateCamera(r.Context(), cameraID); err != nil {
		api.WriteError(w, http.StatusNotFound, err.Error())
		return
	}
	ct := r.Header.Get("Content-Type")
	if !allowedVideoTypes[ct] {
		api.WriteError(w, http.StatusBadRequest, fmt.Sprintf("unsupported content type %q, expected video/mp4, video/avi, video/quicktime, or video/x-matroska", ct))
		return
	}

	data, oversized, err := readBody(r.Body, h.maxUploadSize)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "failed to read request body")
		return
	}
	if oversized {
		api.WriteError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("upload exceeds maximum size of %d bytes", h.maxUploadSize))
		return
	}

	tempPath, finalPath, err := h.storageMgr.CreateSegment(cameraID, string(model.FormatH264))
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "failed to create segment")
		return
	}

	if _, err := h.storageMgr.WriteFrame(tempPath, data); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "failed to write frame")
		return
	}

	if err := h.storageMgr.CloseSegment(tempPath, finalPath); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "failed to close segment")
		return
	}

	rec := &model.Recording{
		ID:          uuid.New().String(),
		CameraID:    cameraID,
		FilePath:    finalPath,
		Format:      model.FormatH264,
		StartedAt:   time.Now(),
		EndedAt:     time.Now(),
		Duration:    0,
		FileSize:    int64(len(data)),
		FrameCount:  1,
		MergeStatus: model.MergeStatusPending,
	}

	if err := h.db.InsertRecording(r.Context(), rec); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "failed to save recording metadata")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(uploadResponse{
		ID:         rec.ID,
		CameraID:   rec.CameraID,
		FilePath:   rec.FilePath,
		Format:     string(rec.Format),
		FrameCount: rec.FrameCount,
		FileSize:   rec.FileSize,
	})
}

func (h *Handler) validateCamera(ctx context.Context, cameraID string) error {
	cam, err := h.db.GetCamera(ctx, cameraID)
	if err != nil {
		return fmt.Errorf("failed to query camera: %w", err)
	}
	if cam == nil {
		return fmt.Errorf("camera %q not found", cameraID)
	}
	return nil
}

// readBody reads up to limit+1 bytes from r. Returns the data and whether the body exceeded limit.
func readBody(r io.Reader, limit int64) ([]byte, bool, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, false, err
	}
	return data, int64(len(data)) > limit, nil
}
