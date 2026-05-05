package webdav

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/google/uuid"
	"golang.org/x/net/webdav"
)

var webdavLogger = slog.Default().With("component", "webdav")

// Server provides a WebDAV server for browsing and optionally uploading camera recordings.
type Server struct {
	store     *storage.Manager
	pathPrefix string
	authMW    func(http.Handler) http.Handler
	db        *storage.DB
	readWrite bool
}

// NewServer creates a new WebDAV server.
// store provides the root directory for served files.
// pathPrefix is the URL prefix (e.g. "/dav").
// authMW is an optional authentication middleware; pass nil to skip auth.
// db is the database for registering uploaded recordings; may be nil if readWrite is false.
// readWrite controls whether write operations (PUT, MKCOL, DELETE, etc.) are allowed.
func NewServer(store *storage.Manager, pathPrefix string, authMW func(http.Handler) http.Handler, db *storage.DB, readWrite bool) *Server {
	if pathPrefix == "" {
		pathPrefix = "/dav"
	}
	return &Server{
		store:      store,
		pathPrefix: pathPrefix,
		authMW:     authMW,
		db:         db,
		readWrite:  readWrite,
	}
}

// statusCapturingWriter wraps http.ResponseWriter to capture the status code.
type statusCapturingWriter struct {
	http.ResponseWriter
	statusCode int
}

func (w *statusCapturingWriter) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

// Handler returns an http.Handler that serves WebDAV requests.
// When readWrite is false, only PROPFIND, GET, HEAD, and OPTIONS are allowed.
// When readWrite is true, all WebDAV methods are permitted and PUT uploads
// are automatically registered in the database.
func (s *Server) Handler() http.Handler {
	davHandler := &webdav.Handler{
		Prefix:     s.pathPrefix,
		FileSystem: webdav.Dir(s.store.RootDir()),
		LockSystem: webdav.NewMemLS(),
	}

	var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions, "PROPFIND":
			davHandler.ServeHTTP(w, r)
		case "PUT", "MKCOL", "DELETE", "COPY", "MOVE", "LOCK", "UNLOCK":
			if !s.readWrite {
				http.Error(w, "Forbidden: read-only WebDAV server", http.StatusForbidden)
				return
			}
			if r.Method == "PUT" {
				s.handlePut(w, r, davHandler)
				return
			}
			davHandler.ServeHTTP(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	if s.authMW != nil {
		handler = s.authMW(handler)
	}

	return handler
}

// handlePut processes a PUT request, delegates to the WebDAV handler, and
// registers the uploaded file in the database on success.
func (s *Server) handlePut(w http.ResponseWriter, r *http.Request, davHandler *webdav.Handler) {
	cw := &statusCapturingWriter{ResponseWriter: w, statusCode: http.StatusOK}
	davHandler.ServeHTTP(cw, r)

	if cw.statusCode != http.StatusOK && cw.statusCode != http.StatusCreated && cw.statusCode != http.StatusNoContent {
		return
	}

	if s.db == nil {
		return
	}

	// Extract relative file path from request URL (strip prefix)
	relPath := strings.TrimPrefix(r.URL.Path, s.pathPrefix)
	relPath = strings.TrimPrefix(relPath, "/")
	if relPath == "" {
		return
	}

	// Stat the file to get size
	fullPath := filepath.Join(s.store.RootDir(), relPath)
	info, err := os.Stat(fullPath)
	if err != nil {
		webdavLogger.Warn("failed to stat uploaded file", "path", relPath, "error", err)
		return
	}

	// Extract camera name from first path segment
	parts := strings.SplitN(relPath, "/", 3)
	cameraName := parts[0]
	if cameraName == "" {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	cameraID := s.resolveOrCreateCamera(ctx, cameraName)
	if cameraID == "" {
		webdavLogger.Warn("failed to resolve camera for upload", "camera", cameraName, "path", relPath)
		return
	}

	// Determine format from file extension
	format := formatFromExtension(relPath)

	recording := &model.Recording{
		ID:         uuid.New().String(),
		CameraID:   cameraID,
		FilePath:   relPath,
		Format:     format,
		StartedAt:  info.ModTime(),
		EndedAt:    info.ModTime(),
		Duration:   0,
		FileSize:   info.Size(),
		FrameCount: 1,
		Pinned:     false,
	}

	if err := s.db.InsertRecording(ctx, recording); err != nil {
		webdavLogger.Warn("failed to register uploaded recording", "path", relPath, "error", err)
		return
	}

	webdavLogger.Info("registered uploaded recording",
		"id", recording.ID,
		"camera_id", cameraID,
		"path", relPath,
		"size", info.Size(),
		"format", format,
	)
}

// resolveOrCreateCamera finds an existing camera by name or creates a new one.
func (s *Server) resolveOrCreateCamera(ctx context.Context, name string) string {
	// Try finding by name across all cameras
	cameras, err := s.db.ListCameras(ctx)
	if err == nil {
		for _, c := range cameras {
			if c.Name == name {
				return c.ID
			}
		}
	}

	// Create a new camera
	id := camera.GenerateCameraID()
	err = s.db.UpsertCamera(ctx, id, name, string(model.ProtoHTTPJPEG), "", "", "", true)
	if err != nil {
		webdavLogger.Warn("failed to auto-create camera", "id", id, "name", name, "error", err)
		return ""
	}

	webdavLogger.Info("auto-created camera from WebDAV upload", "id", id, "name", name)
	return id
}

// formatFromExtension determines the recording format from a file path extension.
func formatFromExtension(path string) model.Format {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".avi", ".mp4", ".mkv", ".mov", ".ts":
		return model.FormatH264
	case ".jpg", ".jpeg":
		return model.FormatMJPEG
	default:
		return model.FormatH264
	}
}
