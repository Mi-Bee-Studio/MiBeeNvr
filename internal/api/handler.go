package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
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
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/relay"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/rtsp"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/vision"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/vod"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/webrtc"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/whip"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/wsstream"
	"github.com/go-chi/chi/v5"
	"github.com/mickeyzzc/gb28181-go/manscdp"
	"github.com/mickeyzzc/gb28181-go/platform"
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
	// LocalAccess is true only when the request is genuinely from a browser on
	// the NVR host machine: a loopback connection with no proxy/gateway headers
	// AND auth.local_bypass is enabled. The frontend uses it to skip the login
	// page for local access. Derived from middleware.IsLocalIP + HasProxyHeaders
	// + config.Auth.LocalBypass in handleHealth.
	LocalAccess bool                 `json:"local_access"`
	Cameras     *CameraHealthSummary `json:"cameras,omitempty"`
	// DeviceID / DeviceName give LAN clients a stable identity to anchor on
	// instead of an IP address (#330). Empty until the config provides them
	// (the ID is generated and persisted on first config load).
	DeviceID   string `json:"device_id,omitempty"`
	DeviceName string `json:"device_name,omitempty"`
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
	WHIP *protocolCapability `json:"whip,omitempty"`
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
	rollingMergeMgr   *merge.RollingMergeCoordinator
	healthMgr         HealthManager
	stabilityProvider StabilityProvider
	cloudProxy        CloudAuthProxy
	streamRegistry    *StreamRegistry
	downloader        TranscodeDownloader
	transcodeMgr      TranscodeManagerAPI
	eventBus          *event.EventBus
	timelapseMergeMgr *timelapse.RollingMergeManager
	mergeScheduler    *timelapse.MergeScheduler
	activeMerges      sync.Map
	// mergeWg tracks the goroutines spawned by handleTimelapseMerge and
	// handleTimelapseBatchMerge (which run PeriodicMergeManager.Run in the
	// background). mergeCtx/mergeCancel let Close propagate shutdown to them.
	// Close waits on mergeWg so in-flight merges finish (or observe ctx cancel)
	// before the caller (App.Stop / a test's t.TempDir cleanup) tears down the
	// storage tree — prevents the TempDir "directory not empty" flake (#143/#125).
	mergeMu           sync.Mutex
	mergeCtx          context.Context    // set by initMergeCtx; nil ⇒ fall back to context.Background()
	mergeCancel       context.CancelFunc // set by initMergeCtx
	mergeWg           sync.WaitGroup
	isClosed          bool // guarded by mergeMu; prevents new merges after Close
	aiHandler         *AIHandler
	relayMgr          *relay.Manager
	visionCoordinator *vision.Coordinator
	apiKeyStore       *middleware.APIKeyStore
	whipServer        *whip.Server
	// frameListCache memoizes sorted file-name listings for MJPEG/timelapse frame
	// directories so repeated ?frame=N / list-frames requests don't os.ReadDir + sort
	// the whole directory on every hit. Keyed by dir path; invalidated by mtime + TTL.
	frameListMu       sync.Mutex
	frameListCache    map[string]*frameListEntry
	gb28181DeviceMgr  *platform.DeviceManager
	gb28181SessionMgr *platform.SessionManager
	// Storage-migration (handlers_storage_migrate.go): the background
	// idle-time migrator service. Nil in tests — the endpoints degrade.
	migrationMgr   StorageMigrator
	gb28181PTZ     *platform.PTZController
	gb28181Catalog *platform.CatalogController
	gb28181Inviter GB28181InviteSender
	gb28181Bye     GB28181ByeSender
	gb28181Cascade GB28181CascadeStatus
	gb28181Loc     *time.Location // GB naive-clock zone (nil → Local)
	// vodMgr serves the on-demand HLS VOD fragmenter for recording playback
	// (#321 Phase 2). Self-contained (owns its segment cache) — constructed
	// here rather than threaded through NewHandler's positional params.
	vodMgr       *vod.Manager
	gb28181Media GB28181DeviceMedia
}

// frameListEntry is a cached sorted listing of a frame directory.
type frameListEntry struct {
	names     []string // sorted image filenames
	dirMtime  int64    // unix mtime of the dir at scan time (for invalidation)
	scannedAt time.Time
}

// frameListCacheTTL bounds how long a cached listing is served without re-stat.
// Timelapse dirs grow while a recording is active, so keep this short enough to
// pick up new frames promptly but long enough to collapse a burst of requests.
const frameListCacheTTL = 500 * time.Millisecond

func NewHandler(db *storage.DB, store *storage.Manager, authMW func(http.Handler) http.Handler, cfg *config.Config, camMgr *camera.CameraManager, hlsMgr *hls.Manager, configPath string, mergeMgr *merge.MergeManager, cloudProxy CloudAuthProxy, mergeScheduler *timelapse.MergeScheduler, gb28181DeviceMgr *platform.DeviceManager, gb28181SessionMgr *platform.SessionManager) *Handler {
	return &Handler{db: db, store: store, authMW: authMW, config: cfg, camMgr: camMgr, hlsMgr: hlsMgr, configPath: configPath, snapshots: make(map[string]*snapshotCache), frameListCache: make(map[string]*frameListEntry), mergeMgr: mergeMgr, cloudProxy: cloudProxy, mergeScheduler: mergeScheduler, gb28181DeviceMgr: gb28181DeviceMgr, gb28181SessionMgr: gb28181SessionMgr, vodMgr: vod.NewManager()}
}

// startMergeGoroutine launches fn on a tracked background goroutine using the
// Handler's merge context. It returns false (without launching) if Close has
// already been called, so shutdown isn't racing with a brand-new merge. The
// mergeWg/Add happens here (under mergeMu, before the `go`) to satisfy
// sync.WaitGroup's "Add before Wait when counter is zero" contract — Close's
// Wait cannot observe a zero counter while a new merge is being registered.
//
// This replaces the prior `go func() { ctx := context.Background(); mgr.Run(...) }()`
// pattern in handleTimelapseMerge/handleTimelapseBatchMerge that leaked
// goroutines past test/App teardown (root cause of the #143 TempDir flake).
func (h *Handler) startMergeGoroutine(fn func(ctx context.Context)) bool {
	h.mergeMu.Lock()
	defer h.mergeMu.Unlock()
	if h.isClosed {
		return false
	}
	if h.mergeCtx == nil {
		// Lazy-init: derive from Background so merges run independent of any
		// single HTTP request's lifetime, but remain cancellable via Close.
		h.mergeCtx, h.mergeCancel = context.WithCancel(context.Background())
	}
	ctx := h.mergeCtx
	h.mergeWg.Add(1)
	go func() {
		defer h.mergeWg.Done()
		fn(ctx)
	}()
	return true
}

// Close cancels any in-flight timelapse merge goroutines and waits for them to
// exit. It must be called during application shutdown (after the HTTP server
// stops accepting requests) so merges don't outlive the storage tree — the
// App.Service contract requires all goroutines be released, and tests using
// t.TempDir for the storage root will otherwise hit "directory not empty"
// during RemoveAll (#143 / #125 class). Idempotent.
func (h *Handler) Close() {
	h.mergeMu.Lock()
	if h.isClosed {
		h.mergeMu.Unlock()
		return
	}
	h.isClosed = true
	cancel := h.mergeCancel
	h.mergeMu.Unlock()

	if cancel != nil {
		cancel()
	}
	// Wait outside mergeMu: merges may still call back into handler methods
	// (e.g. reading config) that don't take mergeMu, and holding the lock while
	// waiting risks deadlock if a merge ever does take it.
	h.mergeWg.Wait()
}

// Routes returns a chi.Router with all routes registered.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()

	// Public routes (rate-limited health/readyz/capabilities/events)
	h.registerPublicRoutes(r)
	// Anonymous routes (login, setup, public playback, model serving)
	h.registerAnonymousRoutes(r)

	// Protected routes (behind authMW)
	r.Group(func(r chi.Router) {
		r.Use(h.authMW)
		h.registerRecordingRoutes(r)
		h.registerCameraRoutes(r)
		h.registerFlowRoutes(r)
		h.registerSystemRoutes(r)
		h.registerMergeRoutes(r)
		h.registerTimelapseRoutes(r)
		h.registerONVIFRoutes(r)
		h.registerArchiveRoutes(r)
		h.registerXiaomiRoutes(r)
		h.registerHealthRoutes(r)
		h.registerRelayRoutes(r)
		h.registerTranscodeRoutes(r)
		h.registerAIRoutes(r)
		h.registerVisionRoutes(r)
		h.registerTelemetryRoute(r)
		h.registerGB28181Routes(r)
	})

	return r
}

// registerPublicRoutes registers rate-limited public endpoints (health, readyz,
// capabilities, generic SSE events). These are callable without authentication.
func (h *Handler) registerPublicRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		rl := middleware.NewRateLimiter(context.Background(), middleware.RateLimiterConfig{
			MaxRequests: 60,
			Window:      time.Minute,
		})
		r.Use(rl.Handler)
		r.Get("/api/health", h.handleHealth)
		r.Get("/api/health/cameras", h.handleHealthCameras)
		r.Get("/api/readyz", h.handleReadyz)
		r.Get("/api/capabilities", h.handleCapabilities)
		// Generic event streaming (SSE)
		r.Get("/api/events", h.handleEvents)
		// Vision heartbeat (public, rate-limited — Vision has no BasicAuth)
		h.registerVisionPublicRoutes(r)
	})
}

// registerAnonymousRoutes registers endpoints that require no authentication
// but are NOT rate-limited (login, setup, public video playback, AI model file).
func (h *Handler) registerAnonymousRoutes(r chi.Router) {
	r.Post("/api/auth/login", h.handleLogin)
	r.Post("/api/setup", h.handleSetup)
	// fnOS unified-gateway SSO (#394): mints an NVR session token when the
	// request carries a gateway-verified ADMIN identity. The identity context
	// only exists on the gateway Unix-socket listener — everywhere else this
	// always returns 401, so it cannot be used to bypass the direct login.
	r.Get("/api/auth/gateway-session", h.handleGatewaySession)
	// Public routes
	r.Get("/api/recordings/{id}/download", h.handleDownloadRecording)  // Public for video playback
	r.Head("/api/recordings/{id}/download", h.handleDownloadRecording) // HEAD for browser <video> probe
	r.Get("/api/recordings/{id}/merged", h.handleMergedRecording)      // Public for timelapse video playback
	r.Head("/api/recordings/{id}/merged", h.handleMergedRecording)     // HEAD for browser <video> probe
	r.Get("/models/{filename}", h.handleServeModel)                    // Public for browser-side AI model loading
	// VOD HLS recording playback (#321) — same exposure class as /download:
	// hls.js requests these same-origin without auth headers, and they serve
	// the same media bytes /download already exposes.
	r.Get("/api/cameras/{cameraID}/playback/playlist.m3u8", h.handlePlaybackPlaylist)
	r.Get("/api/cameras/{cameraID}/playback/{recordingID}/{segName}", h.handlePlaybackSegment)
	// WHIP push-in ingest (#369): browsers/OBS cannot send auth headers with
	// the SDP POST, and the stream key IS the credential (RTMP/SRT streamid
	// threat model). Must NOT be rate-limited — media sessions are long-lived.
	if h.whipServer != nil {
		h.whipServer.RegisterRoutes(r)
	}
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func WriteError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// parsePagination parses the limit/offset query params with a caller-specified
// default limit and a hard upper bound. It is the single source of truth for the
// limit/offset parsing that was previously duplicated (with subtly different
// default/max values) across the recording, archive, AI, timelapse, and
// transcode list handlers (#222).
//
// defaultLimit is used when ?limit is absent or invalid; maxLimit<=0 disables
// the upper-bound clamp. offset defaults to 0 and is never negative.
func parsePagination(r *http.Request, defaultLimit, maxLimit int) (limit, offset int) {
	limit = defaultLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if maxLimit > 0 && limit > maxLimit {
		limit = maxLimit
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	return limit, offset
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
	// Support ip:port format (e.g., "192.168.1.100:8080")
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
	return NewHandler(db, store, noopAuthMW(), nil, nil, nil, "", nil, nil, nil, nil, nil)
}

// --- Test helper exported for handler_test.go ---

// TestHandler creates a Handler with a no-op auth middleware for testing.
func TestHandler(db *storage.DB, store *storage.Manager) *Handler {
	return noopHandler(db, store)
}

// testHandlerWithAuth creates a Handler with real auth middleware for testing.
func testHandlerWithAuth(db *storage.DB, store *storage.Manager, username, passwordHash string) *Handler {
	authMW, _ := middleware.NewAuthMiddleware(middleware.AuthProvider{
		GetUsername: func() string { return username },
		GetHash:     func() string { return passwordHash },
	}, "", middleware.AuthRateLimitConfig{})
	return NewHandler(db, store, authMW, nil, nil, nil, "", nil, nil, nil, nil, nil)
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

// SetRollingMergeMgr sets the recording rolling merge coordinator on the handler.
func (h *Handler) SetRollingMergeMgr(mgr *merge.RollingMergeCoordinator) {
	h.rollingMergeMgr = mgr
}

// SetVisionCoordinator sets the Vision push coordinator on the handler.
func (h *Handler) SetVisionCoordinator(mgr *vision.Coordinator) {
	h.visionCoordinator = mgr
}

// SetWHIPServer wires the WHIP push-in ingest server. When set, the anonymous
// route group mounts /whip/{streamKey} and capabilities reports it (#369).
func (h *Handler) SetWHIPServer(s *whip.Server) {
	h.whipServer = s
}

// SetAPIKeyStore wires the live API key store. The generate/revoke handlers
// keep it in sync with the config so key changes apply on the next request
// without a service restart (#335); last-used timestamps surface in the key
// list API.
func (h *Handler) SetAPIKeyStore(s *middleware.APIKeyStore) {
	h.apiKeyStore = s
}

// SetAIHandler sets the AI handler on the Handler.
func (h *Handler) SetAIHandler(ah *AIHandler) {
	h.aiHandler = ah
}

// SetRelayManager wires the relay manager for the relay-presets endpoints.
func (h *Handler) SetRelayManager(mgr *relay.Manager) {
	h.relayMgr = mgr
}

// --- Per-camera streaming protocols endpoint ---

// cameraProtocolsResponse is the response for GET /api/cameras/{id}/protocols.
type cameraProtocolsResponse struct {
	Protocols []ProtocolDetail `json:"protocols"`
	Encoding  string           `json:"encoding"`
	Default   string           `json:"default"`
	// RTSP exposes the built-in RTSP output server pull URL (#522) — the
	// address third-party platforms (Synology etc.) fill in as a camera
	// source. Nil when the server is disabled.
	RTSP *rtspEndpointDetail `json:"rtsp,omitempty"`
	// SubStream reports the camera's sub-stream capability (#512): where a
	// lower-resolution secondary feed exists (or can be configured), for
	// consumers like grid preview, cascade and external AI push.
	SubStream subStreamDetail `json:"sub_stream"`
}

// rtspEndpointDetail describes the RTSP pull endpoint for one camera.
type rtspEndpointDetail struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	URL       string `json:"url,omitempty"`
}

// subStreamDetail describes the sub-stream capability of one camera.
type subStreamDetail struct {
	Available bool   `json:"available"`
	Source    string `json:"source,omitempty"` // "onvif" (auto-discovered) | "manual" (URL set)
	Reason    string `json:"reason,omitempty"` // why not, when unavailable
	// Codec is the sub-stream's ACTUAL codec once a pull has come up — it can
	// differ from the main stream's (devices switching codec families between
	// profiles are common). Empty until first observed; consumers gate on it
	// when the decode path is codec-sensitive (e.g. MSE H.265 browsers).
	Codec string `json:"codec,omitempty"`
}

// handleCameraProtocols handles GET /api/cameras/{id}/protocols.
// It returns the available streaming protocols for a specific camera
// based on its encoding and the registered stream handlers.
func (h *Handler) handleCameraProtocols(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	cam, err := h.db.GetCamera(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get camera")
		return
	}
	if cam == nil {
		WriteError(w, http.StatusNotFound, "camera not found")
		return
	}

	encoding := strings.ToLower(cam.Encoding)
	if encoding == "" {
		encoding = strings.ToLower(cam.StreamEncoding)
	}

	// Probe the running recorder for the actual codec. The recorder reads the
	// REAL codec from the live stream (RTSP DESCRIBE for ONVIF/RTSP, MISS
	// CodecID for Xiaomi, etc.), which is authoritative over the DB-stored
	// encoding. The stored encoding may be stale or wrong — e.g. Xiaomi CS2
	// cameras that were initially configured as h264 but actually stream h265
	// (the recorder detects codec=h265, but DB still says h264). Without this
	// probe, /protocols returns the wrong encoding, the frontend picks protocols
	// that can't handle the actual codec (WebRTC for H.265, FLV without MSE
	// H.265), and EVERY protocol renders black.
	//
	// Previously this probe was gated to ONVIF + empty-encoding only, out of
	// fear that a runtime probe might mislabel a correctly-configured camera.
	// But the recorder's codec detection is reliable (it reads actual stream
	// data), and the cost of a wrong DB encoding (all-protocols-black-screen)
	// is far worse than an occasional over-correction. Trust the recorder.
	if h.camMgr != nil {
		if rec := h.camMgr.GetRecorder(id); rec != nil {
			if codec, _, _, _ := getCodecParams(rec); codec != "" {
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

	// Determine default protocol by latency-optimal fallback order. The global
	// streaming.default_protocol config was removed — the frontend Player
	// Orchestrator now auto-selects per camera based on this hint + browser
	// capability, demoting on health failure. Per-camera overrides remain via
	// the Protocol Switcher on each camera's LiveView page.
	defaultProto := ""
	for _, preferred := range []string{"webrtc", "flv", "ll-hls", "hls", "mjpeg"} {
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

	writeJSON(w, http.StatusOK, cameraProtocolsResponse{
		Protocols: protocols,
		Encoding:  encoding,
		Default:   defaultProto,
		RTSP:      h.rtspEndpointFor(r, id, model.Format(encoding)),
		SubStream: h.subStreamDetailFor(id),
	})
}

// subStreamDetailFor derives the camera's sub-stream capability (#512) from
// its protocol and configured/discovered sub fields. "Available" means a
// consumer can request a sub stream today; unavailable reasons guide the UI.
func (h *Handler) subStreamDetailFor(cameraID string) subStreamDetail {
	var cam *config.CameraConfig
	if h.camMgr != nil {
		cam = h.camMgr.GetCameraConfig(cameraID)
	}
	if cam == nil {
		return subStreamDetail{Reason: "camera config unavailable"}
	}
	switch {
	case cam.SubStreamURL != "":
		return h.withSubStreamCodec(subStreamDetail{Available: true, Source: "manual"}, cameraID)
	case cam.SubProfileToken != "":
		return h.withSubStreamCodec(subStreamDetail{Available: true, Source: "onvif"}, cameraID)
	case cam.Protocol == string(model.ProtoGB28181) && strings.TrimSpace(cam.GB28181.SubChannelID) != "":
		return h.withSubStreamCodec(subStreamDetail{Available: true, Source: "gb-sub-channel"}, cameraID)
	}
	switch cam.Protocol {
	case "onvif":
		// Discovery runs once after the recorder comes online; until then (or
		// on single-profile cameras) there is no sub stream to consume.
		return subStreamDetail{Reason: "no secondary ONVIF profile discovered (single-stream camera, or discovery pending)"}
	case "rtsp_h264", "rtsp_mjpeg", string(model.ProtoRTSP):
		return subStreamDetail{Reason: "set sub_stream_url to use this camera's sub stream"}
	case "gb28181":
		// Prober (#560) runs once per channel per boot after the catalog
		// merges (vendor-gated in auto mode); no code = device has no
		// usable sub channel — degrade to main, never an error state.
		return subStreamDetail{Reason: "no sub-channel probed (device has no vendor-convention sub stream, or probe pending)"}
	default:
		// xiaomi (proprietary single stream), srt/rtmp push (publisher owns
		// the encode), http_jpeg/mjpeg (already low-bitrate stills).
		return subStreamDetail{Reason: "protocol does not expose a sub stream"}
	}
}

// withSubStreamCodec decorates an availability verdict with the puller's
// observed codec when a sub-stream source is currently alive — the /protocols
// consumer (SPA quality gating) uses it to predict decodability before
// requesting quality=sub.
func (h *Handler) withSubStreamCodec(d subStreamDetail, cameraID string) subStreamDetail {
	if !d.Available || h.camMgr == nil {
		return d
	}
	for _, st := range h.camMgr.SubStreams().Snapshot() {
		if st.CameraID == cameraID && st.Codec != "" {
			d.Codec = string(st.Codec)
			break
		}
	}
	return d
}

// rtspEndpointFor builds the RTSP output-server entry for the protocols
// response (#522): available when the server is enabled, the camera's codec
// is servable (H.264/H.265), and its parameter sets have been detected. The
// URL targets the requesting host so it is directly copy-pasteable.
func (h *Handler) rtspEndpointFor(r *http.Request, cameraID string, codec model.Format) *rtspEndpointDetail {
	if h.config == nil || h.config.Server.RTSP.Enabled == nil || !*h.config.Server.RTSP.Enabled {
		return nil
	}
	detail := &rtspEndpointDetail{}
	switch codec {
	case model.FormatH264, model.FormatH265:
		// Parameter readiness mirrors the RTSP server's own gate — an idle or
		// warming-up camera still gets the URL (clients retry DESCRIBE).
		detail.Available = true
		detail.URL = rtsp.URLFor(r.Host, h.config.Server.RTSP.Port, cameraID)
	default:
		detail.Reason = "codec not servable over RTSP (H.264/H.265 only)"
	}
	return detail
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

	// WHIP rides the main HTTP listener — Port mirrors server.listen when set
	// so clients can build the endpoint URL from capabilities.
	whipPort := 0
	if _, port, err := net.SplitHostPort(h.config.Server.Listen); err == nil {
		if p, perr := strconv.Atoi(port); perr == nil {
			whipPort = p
		}
	}
	resp.Ingest.WHIP = &protocolCapability{
		Enabled: h.whipServer != nil,
		Port:    whipPort,
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleServeModel serves AI model files from the storage root directory.
// This is a public endpoint (no auth) so the browser can load ONNX models.
func (h *Handler) handleServeModel(w http.ResponseWriter, r *http.Request) {
	filename := chi.URLParam(r, "filename")
	if filename == "" {
		WriteError(w, http.StatusBadRequest, "filename required")
		return
	}
	// Serve from {storage_root}/models/ directory
	modelDir := filepath.Join(h.config.Storage.RootDir, "models")

	// Sanitize: prevent path traversal
	cleanPath := filepath.Clean(filepath.Join(modelDir, filename))
	modelDirWithSep := modelDir + string(filepath.Separator)
	if cleanPath != modelDir && !strings.HasPrefix(cleanPath, modelDirWithSep) {
		WriteError(w, http.StatusBadRequest, "invalid filename")
		return
	}

	http.ServeFile(w, r, cleanPath)
}

// SetGB28181PTZ wires the GB28181 PTZ controller for the channel PTZ endpoint.
func (h *Handler) SetGB28181PTZ(ptz *platform.PTZController) {
	h.gb28181PTZ = ptz
}

// SetGB28181Catalog wires the GB28181 catalog controller for the device
// catalog-refresh endpoint.
func (h *Handler) SetGB28181Catalog(c *platform.CatalogController) {
	h.gb28181Catalog = c
}

// GB28181CascadeStatus reports the lower-level cascade client's registration
// state. Implemented by the cascade service; declared here to avoid importing
// cascade in Handler.
type GB28181CascadeStatus interface {
	Online() bool
	RegistrationSince() (time.Duration, bool)
	ForwardCount() int
}

// SetGB28181Cascade wires the cascade client for the status endpoint.
func (h *Handler) SetGB28181Cascade(s GB28181CascadeStatus) {
	h.gb28181Cascade = s
}

// SetGB28181Timezone pins the zone used to interpret/format naive GB/T 28181
// device-clock timestamps (RecordInfo entries). nil keeps time.Local.
func (h *Handler) SetGB28181Timezone(loc *time.Location) {
	h.gb28181Loc = loc
}

// GB28181InviteSender sends a SIP INVITE to start a media session on a channel.
// Implemented by the SIP server; declared here to avoid importing sip in Handler.
type GB28181InviteSender interface {
	InviteChannel(deviceID, channelID string) error
}

// SetGB28181Inviter wires the SIP server for the channel INVITE endpoint.
func (h *Handler) SetGB28181Inviter(s GB28181InviteSender) {
	h.gb28181Inviter = s
}

// GB28181ByeSender stops a channel media session end-to-end (SIP BYE to the
// device, local receiver teardown, bound-camera recorder state). Implemented
// by the SIP server; declared here to avoid importing sip in Handler.
type GB28181ByeSender interface {
	ByeChannelByID(channelID string) error
}

// SetGB28181ByeSender wires the SIP server for the channel BYE endpoint.
func (h *Handler) SetGB28181ByeSender(s GB28181ByeSender) {
	h.gb28181Bye = s
}

// GB28181DeviceMedia is the device-side recording API (RecordInfo query,
// playback INVITE fetch, MANSRTSP control) implemented by the SIP server;
// declared here to avoid importing sip in Handler (#337).
type GB28181DeviceMedia interface {
	// QueryChannelRecords sends a RecordInfo query and collects the paged
	// responses for a time range.
	QueryChannelRecords(deviceID, channelID string, start, end time.Time) ([]manscdp.RecordItem, error)
	// StartPlayback starts a fetch that muxes the device recording into the
	// normal recordings pipeline of the bound camera.
	StartPlayback(deviceID, channelID string, start, end time.Time) error
	// StartDownload starts an s=Download fetch (file-speed transfer, #378) —
	// media lands in the recordings pipeline like a playback fetch.
	StartDownload(deviceID, channelID string, start, end time.Time) error
	// StopPlayback stops a fetch (SIP BYE + finalize).
	StopPlayback(channelID string) error
	// PlaybackStatusFor reports fetch progress (ok=false when idle).
	PlaybackStatusFor(channelID string) (platform.PlaybackInfo, bool)
	// PlaybackControl sends a MANSRTSP control (pause/resume/seek).
	PlaybackControl(channelID, action string, scale, position float64) error
	// StartTalk establishes a voice intercom with a channel (audio-only
	// INVITE; #341). Idempotent.
	StartTalk(cameraID, deviceID, channelID string) error
	// StopTalk ends the intercom (SIP BYE + socket close).
	StopTalk(channelID string) error
	// WriteTalkAudio packetizes one G.711 A-law frame to the device.
	WriteTalkAudio(channelID string, alaw []byte)
	// TalkStatusFor reports the intercom state of a camera.
	TalkStatusFor(cameraID string) platform.TalkStatus
	// GB28181Alarms returns the device's recent alarms (latest first).
	GB28181Alarms(deviceID string) []event.GB28181AlarmEvent
	// GB28181Positions returns the device's recent mobile positions.
	GB28181Positions(deviceID string) []platform.GBPosition
}

// SetGB28181DeviceMedia wires the SIP server for the device-recording
// endpoints.
func (h *Handler) SetGB28181DeviceMedia(s GB28181DeviceMedia) {
	h.gb28181Media = s
}

// registerGB28181Routes registers GB28181 device and channel endpoints.
func (h *Handler) registerGB28181Routes(r chi.Router) {
	r.Route("/api/gb28181", func(r chi.Router) {
		r.Get("/devices", h.handleListGB28181Devices)
		r.Get("/cascade/status", h.handleGB28181CascadeStatus)
		r.Route("/devices/{id}", func(r chi.Router) {
			r.Get("/channels", h.handleListGB28181Channels)
			r.Post("/catalog-refresh", h.handleCatalogRefresh)
			r.Get("/alarms", h.handleGB28181DeviceAlarms)
			r.Get("/positions", h.handleGB28181DevicePositions)
		})
		r.Route("/channels/{id}", func(r chi.Router) {
			r.Post("/invite", h.handleInviteChannel)
			r.Post("/bye", h.handleByeChannel)
			r.Post("/ptz", h.handlePTZChannel)
			r.Post("/lens", h.handleChannelLensControl)
			r.Post("/aux-switch", h.handleChannelAuxSwitch)
			r.Post("/control", h.handleChannelDeviceControl)
			r.Get("/records", h.handleChannelRecords)
			r.Post("/playback", h.handleChannelPlaybackStart)
			r.Get("/playback", h.handleChannelPlaybackStatus)
			r.Delete("/playback", h.handleChannelPlaybackStop)
			r.Post("/playback/control", h.handleChannelPlaybackControl)
			r.Post("/download", h.handleChannelDownloadStart)
			r.Get("/download", h.handleChannelPlaybackStatus)
			r.Delete("/download", h.handleChannelPlaybackStop)
		})
	})
}
