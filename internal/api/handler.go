package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

var logger = slog.Default().With("component", "api")

var appStartTime = time.Now()

// HealthCheck represents the result of a single health check.
type HealthCheck struct {
	Status  string `json:"status"`  // "ok" | "warning" | "error"
	Message string `json:"message,omitempty"`
}

// HealthResponse is the response from /api/health.
type HealthResponse struct {
	Status string            `json:"status"` // "ok" | "degraded" | "unhealthy"
	Checks map[string]HealthCheck `json:"checks"`
	Uptime string            `json:"uptime"`
}

// Handler holds dependencies for the REST API handlers.
	// Handler holds dependencies for the REST API handlers.

type Handler struct {
	db      *storage.DB
	store   *storage.Manager
	authMW  func(http.Handler) http.Handler
	config  *config.Config
	camMgr  *camera.CameraManager
	configPath string
}

// NewHandler creates a new API handler.
func NewHandler(db *storage.DB, store *storage.Manager, authMW func(http.Handler) http.Handler, cfg *config.Config, camMgr *camera.CameraManager, configPath string) *Handler {
	return &Handler{db: db, store: store, authMW: authMW, config: cfg, camMgr: camMgr, configPath: configPath}
}

// Routes returns a chi.Router with all routes registered.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()

	// Public routes
	r.Get("/api/health", h.handleHealth)
	r.Get("/api/readyz", h.handleReadyz)
	r.Post("/api/auth/login", h.handleLogin)

	// Protected routes
	r.Group(func(r chi.Router) {
		r.Use(h.authMW)
		r.Route("/api/recordings", func(r chi.Router) {
			r.Get("/", h.handleListRecordings)
			r.Route("/{id}", func(r chi.Router) {
				r.Get("/", h.handleGetRecording)
				r.Delete("/", h.handleDeleteRecording)
				r.Post("/pin", h.handlePinRecording)
				r.Post("/unpin", h.handleUnpinRecording)
				r.Get("/download", h.handleDownloadRecording)
				r.Get("/frames", h.handleListFrames)
			})
		})
		r.Route("/api/cameras", func(r chi.Router) {
			r.Get("/", h.handleListCameras)
			r.Post("/", h.handleCreateCamera)
			r.Route("/{id}", func(r chi.Router) {
				r.Get("/", h.handleGetCamera)
				r.Put("/", h.handleUpdateCamera)
				r.Delete("/", h.handleDeleteCamera)
			})
		})
		r.Get("/api/stats", h.handleStats)
		r.Get("/api/stats/trends", h.handleStatsTrends)
		r.Get("/api/settings", h.handleGetSettings)
		r.Put("/api/settings", h.handleUpdateSettings)
		r.Post("/api/backup", h.handleBackup)
		r.Get("/api/backups", h.handleListBackups)
	})

	return r
}

// --- Public endpoints ---

func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	resp := HealthResponse{Checks: make(map[string]HealthCheck)}
	hasWarning, hasError := false, false

	// Database check
	if h.db != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		err := h.db.DB().PingContext(ctx)
		if err != nil {
			resp.Checks["database"] = HealthCheck{Status: "error", Message: err.Error()}
			hasError = true
		} else {
			resp.Checks["database"] = HealthCheck{Status: "ok"}
		}
	} else {
		resp.Checks["database"] = HealthCheck{Status: "error", Message: "database not configured"}
		hasError = true
	}

	// Storage check
	if h.store != nil {
		total, used, err := h.store.GetDiskUsage()
		if err != nil {
			resp.Checks["storage"] = HealthCheck{Status: "error", Message: err.Error()}
			hasError = true
		} else {
			pct := 0
			if total > 0 {
				pct = int(float64(used) / float64(total) * 100)
			}
			msg := fmt.Sprintf("%d%% used (%d / %d bytes)", pct, used, total)
			if pct > 95 {
				resp.Checks["storage"] = HealthCheck{Status: "error", Message: msg}
				hasError = true
			} else if pct > 90 {
				resp.Checks["storage"] = HealthCheck{Status: "warning", Message: msg}
				hasWarning = true
			} else {
				resp.Checks["storage"] = HealthCheck{Status: "ok", Message: msg}
			}
		}
	} else {
		resp.Checks["storage"] = HealthCheck{Status: "error", Message: "storage not configured"}
		hasError = true
	}

	// Goroutine check
	numGoroutines := runtime.NumGoroutine()
	if numGoroutines > 1000 {
		resp.Checks["goroutines"] = HealthCheck{Status: "error", Message: fmt.Sprintf("%d goroutines (threshold: 1000)", numGoroutines)}
		hasError = true
	} else {
		resp.Checks["goroutines"] = HealthCheck{Status: "ok", Message: fmt.Sprintf("%d goroutines", numGoroutines)}
	}

	// Overall status
	switch {
	case hasError:
		resp.Status = "unhealthy"
	case hasWarning:
		resp.Status = "degraded"
	default:
		resp.Status = "ok"
	}

	// Uptime
	resp.Uptime = formatUptime(time.Since(appStartTime))

	writeJSON(w, http.StatusOK, resp)
		}

		func (h *Handler) handleReadyz(w http.ResponseWriter, r *http.Request) {
	checks := make(map[string]HealthCheck)

	// Database must be ok
	allOK := true
	if h.db == nil {
		checks["database"] = HealthCheck{Status: "error", Message: "database not configured"}
		allOK = false
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := h.db.DB().PingContext(ctx); err != nil {
			checks["database"] = HealthCheck{Status: "error", Message: err.Error()}
			allOK = false
		} else {
			checks["database"] = HealthCheck{Status: "ok"}
		}
	}

	// Storage must be < 95%
	if h.store == nil {
		checks["storage"] = HealthCheck{Status: "error", Message: "storage not configured"}
		allOK = false
	} else {
		total, used, err := h.store.GetDiskUsage()
		if err != nil {
			checks["storage"] = HealthCheck{Status: "error", Message: err.Error()}
			allOK = false
		} else {
			pct := 0
			if total > 0 {
				pct = int(float64(used) / float64(total) * 100)
			}
			if pct >= 95 {
				checks["storage"] = HealthCheck{Status: "error", Message: fmt.Sprintf("%d%% used (threshold: 95%%)", pct)}
				allOK = false
			} else {
				checks["storage"] = HealthCheck{Status: "ok"}
			}
		}
	}

	// Goroutines must be < 5000
	numGoroutines := runtime.NumGoroutine()
	if numGoroutines >= 5000 {
		checks["goroutines"] = HealthCheck{Status: "error", Message: fmt.Sprintf("%d goroutines (threshold: 5000)", numGoroutines)}
		allOK = false
	} else {
		checks["goroutines"] = HealthCheck{Status: "ok"}
	}

	if allOK {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	} else {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not ready", "checks": checks})
	}
}

func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	// Validate credentials by running through the auth middleware.
	// If auth is disabled, any request succeeds; otherwise BasicAuth is checked.
	done := make(chan int, 1)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		done <- http.StatusOK
	})
	rec := &middleware.StatusRecorder{ResponseWriter: w, Status: http.StatusUnauthorized}
	h.authMW(inner).ServeHTTP(rec, r)

	select {
	case status := <-done:
		if status == http.StatusOK {
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		}
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
	}
}


// --- Recording endpoints ---

func (h *Handler) handleListRecordings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	filter := model.RecordingFilter{
		CameraID: r.URL.Query().Get("camera_id"),
		Format:   model.Format(r.URL.Query().Get("format")),
	}

	if v := r.URL.Query().Get("pinned"); v != "" {
		pinned := v == "true" || v == "1"
		filter.Pinned = &pinned
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
		"total":     total,
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

func (h *Handler) handlePinRecording(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.db.SetPinned(r.Context(), id, true); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to pin recording")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "pinned"})
}

func (h *Handler) handleUnpinRecording(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.db.SetPinned(r.Context(), id, false); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to unpin recording")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "unpinned"})
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

	if rec.FilePath == "" {
		writeError(w, http.StatusNotFound, "file not available")
		return
	}

	// Check for frame parameter (MJPEG frame download)
	frameStr := r.URL.Query().Get("frame")
	if frameStr != "" && rec.Format == model.FormatMJPEG {
		frameIndex, err := strconv.Atoi(frameStr)
		if err == nil {
			entries, err := os.ReadDir(rec.FilePath)
			if err == nil {
				jpgFiles := []os.DirEntry{}
				for _, e := range entries {
					if !e.IsDir() && isImageFile(e.Name()) {
						jpgFiles = append(jpgFiles, e)
					}
				}
				sort.Slice(jpgFiles, func(i, j int) bool { return jpgFiles[i].Name() < jpgFiles[j].Name() })
				if frameIndex >= 0 && frameIndex < len(jpgFiles) {
					framePath := filepath.Join(rec.FilePath, jpgFiles[frameIndex].Name())
					http.ServeFile(w, r, framePath)
					return
				}
			}
		}
		http.Error(w, "frame not found", http.StatusNotFound)
		return
	}

	filePath := rec.FilePath
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

// --- Camera and stats endpoints ---

func (h *Handler) handleListCameras(w http.ResponseWriter, r *http.Request) {
	cameras, err := h.db.ListCameras(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list cameras")
		return
	}
	if cameras == nil {
		cameras = []storage.CameraRow{}
	}
	writeJSON(w, http.StatusOK, cameras)
}

func (h *Handler) handleStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	total, used, err := h.store.GetDiskUsage()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get disk usage")
		return
	}

	count, err := h.db.CountRecordings(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to count recordings")
		return
	}

	cameras, err := h.db.ListCameras(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to count cameras")
		return
	}

	stats := model.StorageStats{
		TotalBytes:     total,
		UsedBytes:      used,
		RecordingCount: count,
		CameraCount:    len(cameras),
	}
	writeJSON(w, http.StatusOK, stats)
}

func (h *Handler) handleStatsTrends(w http.ResponseWriter, r *http.Request) {
	days := 7
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 30 {
			days = n
		}
	}
	trends, err := h.db.GetRecordingTrends(r.Context(), days)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get recording trends")
		return
	}
	writeJSON(w, http.StatusOK, trends)
}

// --- Camera CRUD endpoints ---

var validProtocols = map[string]bool{
	"rtsp_h264": true,
	"rtsp_mjpeg": true,
	"http_jpeg": true,
}

func (h *Handler) handleCreateCamera(w http.ResponseWriter, r *http.Request) {
	if h.camMgr == nil {
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}

	var body struct {
		Name     string  `json:"name"`
		Protocol string  `json:"protocol"`
		URL      string  `json:"url"`
		Username string  `json:"username"`
		Password string  `json:"password"`
		Enabled  *bool   `json:"enabled"`
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
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid protocol %q, must be one of: rtsp_h264, rtsp_mjpeg, http_jpeg", body.Protocol))
		return
	}
	if body.URL == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}

	cam := config.CameraConfig{
		Name:     body.Name,
		Protocol: body.Protocol,
		URL:      body.URL,
		Username: body.Username,
		Password: body.Password,
	}
	if body.Enabled != nil {
		cam.Enabled = *body.Enabled
	} else {
		cam.Enabled = true
	}

	id, err := h.camMgr.AddCamera(r.Context(), cam)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to add camera: %v", err))
		return
	}
	cam.ID = id
	writeJSON(w, http.StatusCreated, cam)
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
	writeJSON(w, http.StatusOK, row)
}

func (h *Handler) handleUpdateCamera(w http.ResponseWriter, r *http.Request) {
	if h.camMgr == nil {
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	id := chi.URLParam(r, "id")

	var body struct {
		Name     *string `json:"name"`
		URL      *string `json:"url"`
		Protocol *string `json:"protocol"`
		Username *string `json:"username"`
		Password *string `json:"password"`
		Enabled  *bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	updates := camera.CameraUpdate{
		Name:     body.Name,
		URL:      body.URL,
		Protocol: body.Protocol,
		Username: body.Username,
		Password: body.Password,
		Enabled:  body.Enabled,
	}

	updated, err := h.camMgr.UpdateCamera(r.Context(), id, updates)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, "camera not found")
			return
		}
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to update camera: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (h *Handler) handleDeleteCamera(w http.ResponseWriter, r *http.Request) {
	if h.camMgr == nil {
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	id := chi.URLParam(r, "id")

	if err := h.camMgr.RemoveCamera(r.Context(), id); err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, "camera not found")
			return
		}
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to delete camera: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// isImageFile checks if a filename has an image extension (jpg/jpeg/png).
func isImageFile(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") || strings.HasSuffix(lower, ".png")
}

// noopAuthMW is a middleware that passes all requests through (no auth).
func noopAuthMW() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return next
	}
}

// noopHandler is a helper for creating a Handler without real auth.
func noopHandler(db *storage.DB, store *storage.Manager) *Handler {
	return NewHandler(db, store, noopAuthMW(), nil, nil, "")
}
// --- Test helper exported for handler_test.go ---

// TestHandler creates a Handler with a no-op auth middleware for testing.
func TestHandler(db *storage.DB, store *storage.Manager) *Handler {
	return noopHandler(db, store)
}

// TestHandlerWithAuth creates a Handler with real auth middleware for testing.
func TestHandlerWithAuth(db *storage.DB, store *storage.Manager, username, passwordHash string) *Handler {
	authMW := middleware.NewAuthMiddleware(username, passwordHash)
	return NewHandler(db, store, authMW, nil, nil, "")
}
// --- Settings endpoints ---

func (h *Handler) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		writeError(w, http.StatusInternalServerError, "config not available")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"cleanup": map[string]any{
			"retention_days":         h.config.Cleanup.RetentionDays,
			"check_interval":         h.config.Cleanup.CheckInterval,
			"disk_threshold_percent": h.config.Cleanup.DiskThresholdPercent,
		},
	})
	}

func (h *Handler) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		writeError(w, http.StatusInternalServerError, "config not available")
		return
	}

	var body struct {
		Cleanup *struct {
			RetentionDays        *int    `json:"retention_days"`
			DiskThresholdPercent *int    `json:"disk_threshold_percent"`
			CheckInterval        *string `json:"check_interval"`
		} `json:"cleanup"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Update cleanup settings
	if body.Cleanup != nil {
		if body.Cleanup.RetentionDays != nil {
			if *body.Cleanup.RetentionDays < 1 {
				writeError(w, http.StatusBadRequest, "retention_days must be >= 1")
				return
			}
			h.config.Cleanup.RetentionDays = *body.Cleanup.RetentionDays
		}
		if body.Cleanup.DiskThresholdPercent != nil {
			if *body.Cleanup.DiskThresholdPercent < 1 || *body.Cleanup.DiskThresholdPercent > 100 {
				writeError(w, http.StatusBadRequest, "disk_threshold_percent must be between 1 and 100")
				return
			}
			h.config.Cleanup.DiskThresholdPercent = *body.Cleanup.DiskThresholdPercent
		}
		if body.Cleanup.CheckInterval != nil {
			if _, err := time.ParseDuration(*body.Cleanup.CheckInterval); err != nil {
				writeError(w, http.StatusBadRequest, "check_interval must be a valid duration (e.g., \"30m\", \"1h\")")
				return
			}
			h.config.Cleanup.CheckInterval = *body.Cleanup.CheckInterval
		}


	// Persist config to disk

	if err := config.Save(h.configPath, h.config); err != nil {

		logger.Warn("failed to save config", "error", err)

	}


	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
	}
}

// --- Backup endpoints ---

func (h *Handler) handleBackup(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusInternalServerError, "database not available")
		return
	}
	backupDir := filepath.Join(filepath.Dir(h.configPath), "backups")
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create backup directory")
		return
	}
	filename := fmt.Sprintf("nvr-backup-%s.db", time.Now().Format("20060102-150405"))
	destPath := filepath.Join(backupDir, filename)
	if err := h.db.Backup(r.Context(), destPath); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create backup")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "created", "file": filename})
}

func (h *Handler) handleListBackups(w http.ResponseWriter, r *http.Request) {
	backupDir := filepath.Join(filepath.Dir(h.configPath), "backups")
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		writeJSON(w, http.StatusOK, []string{})
		return
	}
	var backups []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".db") {
			backups = append(backups, e.Name())
		}
	}
	if backups == nil {
		backups = []string{}
	}
	writeJSON(w, http.StatusOK, backups)
}

// formatUptime converts a duration to a human-readable string like "2h 15m 30s".
func formatUptime(d time.Duration) string {
	rounded := d.Round(time.Second)
	h := rounded / time.Hour
	rounded -= h * time.Hour
	m := rounded / time.Minute
	rounded -= m * time.Minute
	s := rounded / time.Second
	if h > 0 {
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}
