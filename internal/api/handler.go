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
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/flv"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/hls"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/webrtc"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/wsstream"
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
	Status        string                 `json:"status"` // "ok" | "degraded" | "unhealthy"
	Checks        map[string]HealthCheck `json:"checks"`
	Uptime        string                 `json:"uptime"`
	SetupRequired bool                   `json:"setup_required"`
	Cameras       *CameraHealthSummary   `json:"cameras,omitempty"`
}

// CameraHealthSummary provides aggregated camera health in the /api/health response.
type CameraHealthSummary struct {
	Total        int                  `json:"total"`
	Recording    int                  `json:"recording"`
	Reconnecting int                  `json:"reconnecting"`
	Error        int                  `json:"error"`
	Offline      int                  `json:"offline"`
	Details      []CameraHealthDetail `json:"details"`
}

// CameraHealthDetail is a per-camera summary included in /api/health.
type CameraHealthDetail struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Score  int    `json:"score"`
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

// capabilitiesResponse is the response for GET /api/capabilities.
type capabilitiesResponse struct {
	Ingest ingestCapabilities `json:"ingest"`
}

type ingestCapabilities struct {
	RTMP *protocolCapability `json:"rtmp,omitempty"`
	SRT  *protocolCapability `json:"srt,omitempty"`
}

type protocolCapability struct {
	Enabled bool `json:"enabled"`
	Port    int  `json:"port"`
}
type snapshotCache struct {
	data      []byte
	timestamp time.Time
}

// Handler holds dependencies for the REST API handlers.

type Handler struct {
	db                *storage.DB
	store             *storage.Manager
	authMW            func(http.Handler) http.Handler
	config            *config.Config
	camMgr            *camera.CameraManager
	hlsMgr            *hls.Manager
	webrtcMgr         *webrtc.Manager
	flvMgr            *flv.Manager
	wsMgr             *wsstream.Manager
	configPath        string
	snapshotMu        sync.RWMutex
	snapshots         map[string]*snapshotCache // cameraID -> cached snapshot
	mergeMgr          *merge.MergeManager
	healthMgr         HealthManager
	stabilityProvider StabilityProvider
	cloudProxy        CloudAuthProxy
	streamRegistry    *StreamRegistry
	downloader        TranscodeDownloader
	transcodeMgr      TranscodeManagerAPI
	aiEngine          AIEngine
	aiDetector        AIDetector
	eventBus          *event.EventBus
	timelapseMergeMgr *timelapse.RollingMergeManager
	timelapseDailyMgr *timelapse.DailyMergeManager
	mergeScheduler    *timelapse.MergeScheduler
	activeMerges      sync.Map
}

func NewHandler(db *storage.DB, store *storage.Manager, authMW func(http.Handler) http.Handler, cfg *config.Config, camMgr *camera.CameraManager, hlsMgr *hls.Manager, configPath string, mergeMgr *merge.MergeManager, cloudProxy CloudAuthProxy, mergeScheduler *timelapse.MergeScheduler) *Handler {
	return &Handler{db: db, store: store, authMW: authMW, config: cfg, camMgr: camMgr, hlsMgr: hlsMgr, configPath: configPath, snapshots: make(map[string]*snapshotCache), mergeMgr: mergeMgr, cloudProxy: cloudProxy, mergeScheduler: mergeScheduler}
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
		r.Get("/api/health/cameras", h.handleHealthCameras)
		r.Get("/api/readyz", h.handleReadyz)
		r.Get("/api/capabilities", h.handleCapabilities)
	})
	r.Post("/api/auth/login", h.handleLogin)
	r.Post("/api/setup", h.handleSetup)
	// Public routes
	r.Get("/api/recordings/{id}/download", h.handleDownloadRecording) // Public for video playback
	r.Head("/api/recordings/{id}/download", h.handleDownloadRecording) // HEAD for browser <video> probe

	// Protected routes
	r.Group(func(r chi.Router) {
		r.Use(h.authMW)
		r.Route("/api/recordings", func(r chi.Router) {
			r.Get("/", h.handleListRecordings)
			r.Post("/batch-delete", h.handleBatchDeleteRecordings)
			r.Route("/{id}", func(r chi.Router) {
				r.Get("/", h.handleGetRecording)
				r.Delete("/", h.handleDeleteRecording)

				r.Get("/frames", h.handleListFrames)
				r.Get("/timelapse-frames", h.handleTimelapseFrames)
				r.Get("/timelapse-frames/{filename}", h.handleTimelapseFrame)
				r.Get("/merged", h.handleMergedRecording)
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
				// WebSocket stream (must be before HLS catch-all /stream/*)
				r.Get("/stream/ws", h.handleStreamWS)
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
				r.Get("/onvif/capabilities", h.handleONVIFCapabilities)
				r.Post("/ptz/move", h.handlePTZMove)
				r.Post("/ptz/stop", h.handlePTZStop)
				r.Get("/ptz/status", h.handlePTZStatus)
				r.Get("/ptz/presets", h.handlePTZGetPresets)
				r.Post("/ptz/presets", h.handlePTZCreatePreset)
				r.Post("/ptz/presets/{token}/goto", h.handlePTZGoToPreset)
				r.Delete("/ptz/presets/{token}", h.handlePTZDeletePreset)
				r.Get("/snapshot/uri", h.handleSnapshotGetUri)
				r.Get("/imaging/settings", h.handleImagingGetSettings)
				r.Put("/imaging/settings", h.handleImagingSetSettings)
				r.Get("/imaging/options", h.handleImagingGetOptions)
				// Device management
				r.Post("/onvif/reboot", h.handleONVIFReboot)
				r.Get("/onvif/network", h.handleONVIFGetNetwork)
				r.Put("/onvif/network", h.handleONVIFSetNetwork)
				r.Get("/onvif/users", h.handleONVIFGetUsers)
				r.Post("/onvif/users", h.handleONVIFCreateUsers)
				r.Delete("/onvif/users", h.handleONVIFDeleteUsers)
				r.Put("/onvif/users/{username}", h.handleONVIFSetUser)
				r.Get("/snapshot", h.handleSnapshot)
				r.Put("/merge-config", h.handleUpdateCameraMergeConfig)
				r.Delete("/merge-config", h.handleDeleteCameraMergeConfig)
				r.Get("/stats", h.handleCameraRecordingStats)
				// Per-camera timelapse configuration
				r.Get("/timelapse", h.handleGetCameraTimelapse)
				r.Put("/timelapse", h.handlePutCameraTimelapse)
				// Camera-specific events (SSE)
				r.Get("/events", h.handleCameraEvents)
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
		r.Get("/api/settings/transcoding", h.handleGetTranscodingSettings)
		r.Put("/api/settings/transcoding", h.handleUpdateTranscodingSettings)
		r.Post("/api/backup", h.handleBackup)
		r.Get("/api/backups", h.handleListBackups)
		r.Post("/api/onvif/discover", h.handleONVIFDiscover)
		r.Get("/api/onvif/discover/{ip}", h.handleONVIFDeviceDetail)
		r.Post("/api/onvif/probe", h.handleONVIFProbe)
		r.Get("/api/merge/status", h.handleMergeStatus)
		r.Get("/api/merge/pending", h.handleMergePending)
		// Timelapse endpoints
		r.Get("/api/timelapse", h.handleTimelapseList)
		r.Get("/api/timelapse/status", h.handleTimelapseStatus)
		r.Post("/api/timelapse/{id}/merge", h.handleTimelapseMerge)
		r.Post("/api/timelapse/{id}/pause", h.handleTimelapsePause)
		r.Post("/api/timelapse/{id}/resume", h.handleTimelapseResume)
		r.Get("/api/timelapse/{id}", h.handleTimelapseGet)
		r.Delete("/api/timelapse/{id}", h.handleTimelapseDelete)
		r.Post("/api/timelapse/{id}/download", h.handleTimelapseDownload)
		r.Get("/api/timelapse/{id}/thumbnail", h.handleTimelapseThumbnail)
		r.Get("/api/timelapse/merge/progress/{cameraId}", h.handleTimelapseMergeProgress)
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
		r.Get("/api/health/stability", h.handleGetStability)
		r.Get("/api/health/stability/{camera_id}", h.handleGetCameraStability)
		r.Get("/api/cameras/{id}/health", h.handleGetCameraHealth)
		// Transcoding endpoints
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
		// AI Detection routes
		r.Route("/api/ai", func(r chi.Router) {
			r.Get("/status", h.handleGetAIStatus)
			r.Post("/enable", h.handleEnableAI)
			r.Post("/disable", h.handleDisableAI)
			r.Get("/events", h.handleAIEvents)
		})
		// Generic event streaming (SSE)
		r.Get("/api/events", h.handleEvents)
		// Telemetry
		r.With(telemetryRateLimiter()).Post("/api/telemetry", h.HandleTelemetry)
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

// validateIP checks that a string is a valid IPv4 or IPv6 address, supporting ip:port format.
func validateIP(ip string) bool {
	// Support ip:port format (e.g., "192.168.63.162:8080")
	if host, _, err := net.SplitHostPort(ip); err == nil {
		return net.ParseIP(host) != nil
	}
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
	return NewHandler(db, store, noopAuthMW(), nil, nil, nil, "", nil, nil, nil)
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
	return NewHandler(db, store, authMW, nil, nil, nil, "", nil, nil, nil)
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

// SetWSManager sets the WebSocket stream manager on the handler.
func (h *Handler) SetWSManager(mgr *wsstream.Manager) {
	h.wsMgr = mgr
}

// SetHealthManager sets the health manager on the handler.
func (h *Handler) SetHealthManager(mgr HealthManager) {
	h.healthMgr = mgr
}

// SetStabilityProvider sets the stability data provider on the handler.
func (h *Handler) SetStabilityProvider(p StabilityProvider) {
	h.stabilityProvider = p
}

// SetDownloader sets the FFmpeg downloader on the handler.
func (h *Handler) SetDownloader(d TranscodeDownloader) {
	h.downloader = d
}

// SetEventBus sets the event bus on the handler.
func (h *Handler) SetEventBus(bus *event.EventBus) {
	h.eventBus = bus
}

// SetTimelapseMergeMgr sets the timelapse rolling merge manager on the handler.
func (h *Handler) SetTimelapseMergeMgr(mgr *timelapse.RollingMergeManager) {
	h.timelapseMergeMgr = mgr
}

// SetTimelapseDailyMgr sets the timelapse daily merge manager on the handler.
func (h *Handler) SetTimelapseDailyMgr(mgr *timelapse.DailyMergeManager) {
	h.timelapseDailyMgr = mgr
}

// --- Per-camera streaming protocols endpoint ---

// cameraProtocolsResponse is the response for GET /api/cameras/{id}/protocols.
type cameraProtocolsResponse struct {
	Protocols []ProtocolDetail `json:"protocols"`
	Encoding  string           `json:"encoding"`
	Default   string           `json:"default"`
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

	// If encoding still unknown (e.g. ONVIF auto-detect), probe the running recorder
	if encoding == "" && h.camMgr != nil {
		if rec := h.camMgr.GetRecorder(id); rec != nil {
			codec, _, _, _ := getCodecParams(rec)
			if codec != "" {
				encoding = string(codec)
			}
		}
	}

	var protocols []ProtocolDetail
	if h.streamRegistry != nil {
		protocols = h.streamRegistry.ProtocolsDetailForCodec(model.Format(encoding))
	}
	if protocols == nil {
		protocols = []ProtocolDetail{}
	}

	// Determine default protocol: prefer user-configured default, then fallback order
	defaultProto := ""
	if h.config != nil && h.config.Streaming.DefaultProtocol != "" {
		for _, p := range protocols {
			if p.Protocol == h.config.Streaming.DefaultProtocol && p.Available {
				defaultProto = h.config.Streaming.DefaultProtocol
				break
			}
		}
	}
	if defaultProto == "" {
		for _, preferred := range []string{"webrtc", "flv", "ll-hls", "hls"} {
			for _, p := range protocols {
				if p.Protocol == preferred && p.Available {
					defaultProto = preferred
					break
				}
			}
			if defaultProto != "" {
				break
			}
		}
	}

	writeJSON(w, http.StatusOK, cameraProtocolsResponse{
		Protocols: protocols,
		Encoding:  encoding,
		Default:   defaultProto,
	})
}

// handleCapabilities handles GET /api/capabilities.
// It returns the server's ingest capabilities (RTMP, SRT).
func (h *Handler) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	resp := capabilitiesResponse{}

	if h.config.RTMP.Enabled != nil && *h.config.RTMP.Enabled {
		resp.Ingest.RTMP = &protocolCapability{
			Enabled: true,
			Port:    h.config.RTMP.Port,
		}
	} else {
		resp.Ingest.RTMP = &protocolCapability{
			Enabled: false,
			Port:    h.config.RTMP.Port,
		}
	}

	if h.config.SRT.Enabled != nil && *h.config.SRT.Enabled {
		resp.Ingest.SRT = &protocolCapability{
			Enabled: true,
			Port:    h.config.SRT.Port,
		}
	} else {
		resp.Ingest.SRT = &protocolCapability{
			Enabled: false,
			Port:    h.config.SRT.Port,
		}
	}

	writeJSON(w, http.StatusOK, resp)
}
