package api

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/flv"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/hls"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/webrtc"
	"github.com/go-chi/chi/v5"
)

var logger = slog.Default().With("component", "api")

var appStartTime = time.Now()

// HealthCheck represents the result of a single health check.
type HealthCheck struct {
	Status  string `json:"status"` // "ok" | "warning" | "error"
	Message string `json:"message,omitempty"`
}

// HealthResponse is the response from /api/health.
type HealthResponse struct {
	Status string                 `json:"status"` // "ok" | "degraded" | "unhealthy"
	Checks map[string]HealthCheck `json:"checks"`
	Uptime        string                 `json:"uptime"`
	SetupRequired bool                   `json:"setup_required"`
}

// SystemStats is the response from /api/stats/system.
type SystemStats struct {
	CPU       CPUStats     `json:"cpu"`
	Memory    MemoryStats  `json:"memory"`
	Network   NetworkStats `json:"network"`
	Uptime    string       `json:"uptime"`
	Timestamp int64        `json:"timestamp"`
}

type CPUStats struct {
	Total uint64 `json:"total"` // cumulative total jiffies
	Idle  uint64 `json:"idle"`  // cumulative idle jiffies
}

type MemoryStats struct {
	Total      uint64 `json:"total"`       // MemTotal bytes
	Available  uint64 `json:"available"`   // MemAvailable bytes
	ProcessRSS uint64 `json:"process_rss"` // NVR process RSS bytes
}

type NetworkStats struct {
	BytesSent uint64 `json:"bytes_sent"`
	BytesRecv uint64 `json:"bytes_recv"`
}

type snapshotCache struct {
	data      []byte
	timestamp time.Time
}

// Handler holds dependencies for the REST API handlers.

type Handler struct {
	db              *storage.DB
	store           *storage.Manager
	authMW          func(http.Handler) http.Handler
	config          *config.Config
	camMgr          *camera.CameraManager
	hlsMgr          *hls.Manager
	webrtcMgr       *webrtc.Manager
	flvMgr          *flv.Manager
	configPath      string
	snapshotMu      sync.RWMutex
	snapshots       map[string]*snapshotCache // cameraID -> cached snapshot
	mergeMgr        *merge.MergeManager
	healthMgr       HealthManager
	cloudProxy      CloudAuthProxy
	streamRegistry  *StreamRegistry
}

func NewHandler(db *storage.DB, store *storage.Manager, authMW func(http.Handler) http.Handler, cfg *config.Config, camMgr *camera.CameraManager, hlsMgr *hls.Manager, configPath string, mergeMgr *merge.MergeManager, cloudProxy CloudAuthProxy) *Handler {
	return &Handler{db: db, store: store, authMW: authMW, config: cfg, camMgr: camMgr, hlsMgr: hlsMgr, configPath: configPath, snapshots: make(map[string]*snapshotCache), mergeMgr: mergeMgr, cloudProxy: cloudProxy}
}

// Routes returns a chi.Router with all routes registered.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()

	// Public routes with rate limiting on health/readyz
	r.Group(func(r chi.Router) {
		rl := middleware.NewRateLimiter(middleware.RateLimiterConfig{
			MaxRequests: 60,
			Window:      time.Minute,
		})
		r.Use(rl)
		r.Get("/api/health", h.handleHealth)
		r.Get("/api/readyz", h.handleReadyz)
	})
	r.Post("/api/auth/login", h.handleLogin)
	r.Post("/api/setup", h.handleSetup)

	// Protected routes
	r.Group(func(r chi.Router) {
		r.Use(h.authMW)
		r.Route("/api/recordings", func(r chi.Router) {
			r.Get("/", h.handleListRecordings)
			r.Post("/batch-delete", h.handleBatchDeleteRecordings)
			r.Route("/{id}", func(r chi.Router) {
				r.Get("/", h.handleGetRecording)
				r.Delete("/", h.handleDeleteRecording)
				r.Get("/download", h.handleDownloadRecording)
				r.Get("/frames", h.handleListFrames)
			})
		})
		r.Route("/api/cameras", func(r chi.Router) {
			r.Get("/", h.handleListCameras)
			r.Post("/", h.handleCreateCamera)
      r.Post("/test-connection", h.handleTestConnection)
				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", h.handleGetCamera)
					r.Put("/", h.handleUpdateCamera)
					r.Delete("/", h.handleDeleteCamera)
					r.Get("/stream/*", h.handleHLSStream)
					r.Delete("/stream", h.handleStopHLSStream)
					// WebRTC WHEP endpoints
					r.Post("/stream/webrtc", h.handleCreateWHEPSession)
					r.Delete("/stream/webrtc/{session}", h.handleDeleteWHEPSession)
					// HTTP-FLV stream
					r.Get("/stream.flv", h.handleFLVStream)
					// Per-camera protocols
					r.Get("/protocols", h.handleCameraProtocols)
					r.Get("/onvif/profiles", h.handleONVIFCameraProfiles)
					r.Post("/ptz/move", h.handlePTZMove)
					r.Post("/ptz/stop", h.handlePTZStop)
					r.Get("/ptz/status", h.handlePTZStatus)
					r.Get("/snapshot", h.handleSnapshot)
					r.Put("/merge-config", h.handleUpdateCameraMergeConfig)
					r.Delete("/merge-config", h.handleDeleteCameraMergeConfig)
					r.Post("/start", h.handleStartCamera)
					r.Post("/stop", h.handleStopCamera)
				})
			})
		r.Get("/api/stats", h.handleStats)
		r.Get("/api/stats/system", h.handleSystemStats)
		r.Get("/api/stats/trends", h.handleStatsTrends)
		r.Get("/api/settings", h.handleGetSettings)
		r.Put("/api/settings", h.handleUpdateSettings)
		r.Get("/api/settings/merge", h.handleGetMergeSettings)
		r.Put("/api/settings/merge", h.handleUpdateMergeSettings)
		r.Get("/api/settings/streaming", h.handleGetStreamingSettings)
		r.Put("/api/settings/streaming", h.handleUpdateStreamingSettings)
		r.Post("/api/backup", h.handleBackup)
		r.Get("/api/backups", h.handleListBackups)
		r.Post("/api/onvif/discover", h.handleONVIFDiscover)
		r.Get("/api/onvif/discover/{ip}", h.handleONVIFDeviceDetail)
		r.Post("/api/onvif/probe", h.handleONVIFProbe)
		r.Get("/api/merge/status", h.handleMergeStatus)
		r.Get("/api/merge/pending", h.handleMergePending)
		r.Get("/api/protocols", h.handleProtocols)
		r.Get("/api/features", h.handleGetFeatures)
		r.Put("/api/features", h.handleUpdateFeatures)
		// Archive endpoints
		r.Route("/api/archives", func(r chi.Router) {
			r.Get("/", h.handleListArchives)
			r.Get("/{cameraID}/recordings", h.handleListArchiveRecordings)
			r.Delete("/{cameraID}", h.handleDeleteArchiveGroup)
			r.Delete("/{cameraID}/recordings/{recordingID}", h.handleDeleteArchiveRecording)
			r.Put("/{cameraID}/retention", h.handleSetArchiveRetention)
		})
		// Xiaomi cloud auth and device discovery
		r.Route("/api/xiaomi", func(r chi.Router) {
			r.Post("/auth", h.handleXiaomiAuth)
			r.Post("/captcha", h.handleXiaomiCaptcha)
			r.Post("/verify", h.handleXiaomiVerify)
			r.Get("/devices", h.handleXiaomiDevices)
			r.Post("/sync", h.handleXiaomiSync)
			r.Get("/check-vendor", h.handleCheckVendor)
		})
		// Health monitoring endpoints
		r.Get("/api/health/status", h.handleGetHealthStatus)
		r.Get("/api/health/events", h.handleGetHealthEvents)
		r.Get("/api/cameras/{id}/health", h.handleGetCameraHealth)
	})

	return r
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

func writeAPIError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error(), "code": model.ErrorCode(err)})
}

// isImageFile checks if a filename has an image extension (jpg/jpeg/png).
func isImageFile(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") || strings.HasSuffix(lower, ".png")
}

// validateURL checks that a URL has a valid scheme and non-empty host.
// This is a basic sanity check — specific protocol validation is handled separately.
func validateURL(rawURL string) bool {
	if rawURL == "" {
		return false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return false
	}
	return true
}

// validateIP checks that a string is a valid IPv4 address.
func validateIP(ip string) bool {
	return net.ParseIP(ip) != nil
}

// noopAuthMW is a middleware that passes all requests through (no auth).
func noopAuthMW() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return next
	}
}

// noopHandler is a helper for creating a Handler without real auth.
func noopHandler(db *storage.DB, store *storage.Manager) *Handler {
	return NewHandler(db, store, noopAuthMW(), nil, nil, nil, "", nil, nil)
}

// --- Test helper exported for handler_test.go ---

// TestHandler creates a Handler with a no-op auth middleware for testing.
func TestHandler(db *storage.DB, store *storage.Manager) *Handler {
	return noopHandler(db, store)
}

// TestHandlerWithAuth creates a Handler with real auth middleware for testing.
func TestHandlerWithAuth(db *storage.DB, store *storage.Manager, username, passwordHash string) *Handler {
	authMW, _ := middleware.NewAuthMiddleware(middleware.AuthProvider{
		GetUsername: func() string { return username },
		GetHash:     func() string { return passwordHash },
	}, "")
	return NewHandler(db, store, authMW, nil, nil, nil, "", nil, nil)
}

// extractDIDFromURL parses the DID from a xiaomi:// URL.
func extractDIDFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Host
}

// strPtr returns a pointer to the given string.
func strPtr(s string) *string {
	return &s
}

// SetWebRTCManager sets the WebRTC manager on the handler.
func (h *Handler) SetWebRTCManager(mgr *webrtc.Manager) {
	h.webrtcMgr = mgr
}

// SetFLVManager sets the FLV manager on the handler.
func (h *Handler) SetFLVManager(mgr *flv.Manager) {
	h.flvMgr = mgr
}

// SetHealthManager sets the health manager on the handler.
func (h *Handler) SetHealthManager(mgr HealthManager) {
	h.healthMgr = mgr
}

// --- Per-camera streaming protocols endpoint ---

// cameraProtocolsResponse is the response for GET /api/cameras/{id}/protocols.
type cameraProtocolsResponse struct {
	Protocols []string `json:"protocols"`
	Encoding  string   `json:"encoding"`
	Default   string   `json:"default"`
}

// handleCameraProtocols handles GET /api/cameras/{id}/protocols.
// It returns the available streaming protocols for a specific camera
// based on its encoding and the registered stream handlers.
func (h *Handler) handleCameraProtocols(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	cam, err := h.db.GetCamera(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get camera")
		return
	}
	if cam == nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}

	encoding := cam.Encoding
	if encoding == "" {
		encoding = cam.StreamEncoding
	}

	var protocols []string
	if h.streamRegistry != nil {
		protocols = h.streamRegistry.ProtocolsForCodec(model.Format(encoding))
	}
	if protocols == nil {
		protocols = []string{}
	}

	// Determine default protocol preference
	defaultProto := ""
	for _, preferred := range []string{"webrtc", "flv", "hls"} {
		for _, p := range protocols {
			if p == preferred {
				defaultProto = preferred
				break
			}
		}
		if defaultProto != "" {
			break
		}
	}

	writeJSON(w, http.StatusOK, cameraProtocolsResponse{
		Protocols: protocols,
		Encoding:  encoding,
		Default:   defaultProto,
	})
}
