package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/api/camerasapi"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/api/gbapi"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/api/notifyapi"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/api/recordingsapi"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/api/relayapi"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/api/streamsapi"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/api/systemapi"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/flv"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/hls"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/httpx"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/iobudget"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/license"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recframes"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/relay"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/viewer"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/vision"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/vod"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/webrtc"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/whip"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/wsstream"
	"github.com/go-chi/chi/v5"
	"github.com/mickeyzzc/gb28181-go/platform"
)

var logger = slogx.Component("api")

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
	LocalAccess bool `json:"local_access"`
	// Gateway is true when this request arrived through the unified gateway
	// with a verified NAS identity (the X-Trim-* context, only ever set on the
	// gateway listener). The frontend uses it to decide whether an
	// Authorization header is safe to send: the gateway claims ANY
	// Authorization header as its own session credential and refuses it, while
	// direct access to the same listener needs the Bearer token. Base-path
	// deployments serve both paths, so the document alone cannot tell them
	// apart (#938).
	Gateway bool `json:"gateway"`
	// GatewayAdmin is true when the gateway-verified identity is specifically
	// an NAS admin. The setup wizard hides its first-boot code field for
	// these sessions — the gateway-admin path is exempt from the code (#1032).
	GatewayAdmin bool `json:"gateway_admin"`
	// DeviceID / DeviceName give LAN clients a stable identity to anchor on
	// instead of an IP address (#330). Empty until the config provides them
	// (the ID is generated and persisted on first config load).
	DeviceID   string `json:"device_id,omitempty"`
	DeviceName string `json:"device_name,omitempty"`
	// Cameras carries BARE camera counters for the public payload — the
	// client-compat contract: MiBeeScout (and any 0.12-era client)
	// identifies an NVR by cameras.total in /api/health; discovery and
	// manual connect both fail without it (#899). Fleet details (names,
	// ids, scores) stay behind auth on /api/health/cameras (#879).
	Cameras *HealthCameraCounts `json:"cameras,omitempty"`
	// License surfaces the licensing gate on the standard probe endpoint
	// (#49): a silently blocking gate was invisible to every monitoring
	// surface for a week. Bare summary only — the document and reason stay
	// on the authenticated /api/license. Omitted when licensing is not
	// enforced (not required) or the capability is absent.
	License *HealthLicense `json:"license,omitempty"`
	// NotifyReceivers lists the origins (scheme://host:port) of the
	// configured notification sinks, deduplicated, so unauthenticated LAN
	// clients can discover that an alert receiver is bound and where it
	// lives for relationship binding (#1121). Path and token never appear
	// here — only the origin. Omitted unless notifications.expose_origin
	// is explicitly enabled and at least one sink URL parses.
	NotifyReceivers []string `json:"notify_receivers,omitempty"`
}

// HealthLicense is the privacy-safe license summary embedded in the public
// /api/health response: state machine name, enforcement flag, gate verdict,
// and expiry horizon. No document payload, no reason text.
type HealthLicense struct {
	State       string `json:"state"` // none | invalid | expired | expiring | valid
	Required    bool   `json:"required"`
	Allowed     bool   `json:"allowed"`
	ExpiresAt   string `json:"expires_at,omitempty"`    // RFC3339; zero/absent omitted
	TimeLeftSec int64  `json:"time_left_sec,omitempty"` // >0 only
}

// HealthCameraCounts is the privacy-safe camera aggregation embedded in the
// public /api/health response: counters only, no per-camera details.
type HealthCameraCounts struct {
	Total        int `json:"total"`
	Recording    int `json:"recording"`
	Reconnecting int `json:"reconnecting"`
	Error        int `json:"error"`
	Offline      int `json:"offline"`
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

// Handler holds dependencies for the REST API handlers.

type Handler struct {
	// ssePerIP caps concurrent /api/events streams per client IP.
	ssePerIP sync.Map // ip -> *atomic.Int32
	// setupCode arms first-boot setup against LAN race claims (#879). Set
	// via ArmFirstBootSetup while no admin credentials exist; cleared once
	// setup completes.
	setupCode string
	// playbackBudget opts media serving into the shared I/O budget as the
	// "playback" tenant (#886, gray-release). nil (default) = ServeFile
	// semantics unchanged.
	playbackBudget atomic.Pointer[iobudget.Limiter]
	db             *storage.DB
	store          *storage.Manager
	authMW         func(http.Handler) http.Handler
	config         *config.Config
	camMgr         *camera.CameraManager
	hlsMgr         *hls.Manager
	webrtcMgr      *webrtc.Manager
	flvMgr         *flv.Manager
	wsMgr          *wsstream.Manager
	viewerStore    *viewer.Store
	viewerLogin    ViewerLoginFn
	configPath     string
	// snapStore owns the shared JPEG-still TTL cache (snapshot / latest-frame /
	// HLS stale-serve) — state lives in the streamsapi domain package, the
	// capturer is wired here and forwarded into its Deps.
	snapStore         *streamsapi.SnapshotStore
	mergeMgr          *merge.MergeManager
	rollingMergeMgr   *merge.RollingMergeCoordinator
	healthMgr         HealthManager
	stabilityProvider StabilityProvider
	cloudProxy        CloudAuthProxy
	streamRegistry    *StreamRegistry
	downloader        TranscodeDownloader
	transcodeMgr      TranscodeManagerAPI
	eventBus          *event.EventBus
	notifySvc         NotifyService
	timelapseMergeMgr *timelapse.RollingMergeManager
	mergeScheduler    *timelapse.MergeScheduler
	// tlTrigger assembles and launches manual (single/batch) periodic merges
	// and owns their goroutine lifecycle (#1009 batch 3); Close joins them so
	// merges never outlive the storage tree (the #143/#125 TempDir class).
	tlTrigger         *timelapse.ManualTrigger
	aiHandler         *AIHandler
	relayMgr          *relay.Manager
	visionCoordinator *vision.Coordinator
	apiKeyStore       *middleware.APIKeyStore
	whipServer        *whip.Server
	// recFrames owns frame-level access to frame-based recordings (#1009
	// batch 2): the frame-directory listing cache, MJPEG/timelapse/AVI frame
	// indexes, frame timestamp extraction, and the merge-codec probe cache.
	recFrames         *recframes.Service
	gb28181DeviceMgr  *platform.DeviceManager
	gb28181Records    *gb28181.DeviceRecordService // device-recording domain (records/playback/download)
	gb28181SessionMgr *platform.SessionManager
	// timeSync serves the camera time-sync endpoints (#time-sync); nil in
	// tests → handlers answer 503.
	timeSync TimeSyncOps
	// Storage-migration (handlers_storage_migrate.go): the background
	// idle-time migrator service. Nil in tests — the endpoints degrade.
	migrationMgr StorageMigrator
	// Remote offload playback proxy (handlers_offload.go, issue #874 batch
	// 2). Nil = remote offload disabled — endpoints degrade to 404.
	offloadPlayback OffloadPlaybackProxy
	gb28181PTZ      *platform.PTZController
	// xiaomiPTZMotor overrides Xiaomi motor dispatch when set (tests); nil
	// routes commands through the camera manager's Xiaomi recorder.
	xiaomiPTZMotor func(cameraID, direction string, speed int) error
	// ptzSuppress blinds the pixgate pixel gate while a PTZ command moves
	// the camera (global scene change must not read as activity).
	ptzSuppress    func(cameraID string, window time.Duration)
	gb28181Catalog *platform.CatalogController
	gb28181Inviter GB28181InviteSender
	// gb28181Sessions (nil when GB28181 is disabled) feeds the /api/streams
	// gb28181 section: live session observability + watchdog recycle count.
	gb28181Sessions func() ([]GB28181SessionStatus, int64)
	gb28181Bye      GB28181ByeSender
	gb28181Cascade  GB28181CascadeStatus
	// vodMgr serves the on-demand HLS VOD fragmenter for recording playback
	// (#321 Phase 2). Self-contained (owns its segment cache) — constructed
	// here rather than threaded through NewHandler's positional params.
	vodMgr       *vod.Manager
	gb28181Media GB28181DeviceMedia
	doorbell     DoorbellService
	onboarding   OnboardingService
	// triggerDispatcher maps a trigger action (record/stop/snapshot) to camera
	// lifecycle ops (#709). The SAME func feeds the MQTT client — wired once
	// in builders.go so HTTP webhook and MQTT triggers share semantics.
	triggerDispatcher func(cameraID, action string, duration time.Duration)
	// gb28181Commander sends DeviceControl commands (#708 snapshot/record).
	gb28181Commander gbCommander
	// gb28181SnapMgr tracks on-demand snapshot sessions (#708).
	gb28181SnapMgr *gb28181.SnapshotSessionManager
	// licenseMgr 商业化收费授权（P2P 门控同源共享实例；nil = 能力缺席）。
	licenseMgr *license.Manager

	// p2pAgent 热可配的 P2P 设备代理（nil = 能力缺席，状态端点报
	// unavailable）。
	p2pAgent P2PController

	// p2pViewer App（viewer）互联质量上报的进程级内存态（仪表盘观测，
	// 重启清零）。
	p2pViewer p2pViewerState

	// p2pEvents 连接记录环（Manager 链路事件经 RecordP2pLinkEvent 推入；
	// 另有 events.jsonl 落盘，重启不清）。
	p2pEventsMu sync.Mutex
	p2pEvents   []p2pLinkEvent
}

func NewHandler(db *storage.DB, store *storage.Manager, authMW func(http.Handler) http.Handler, cfg *config.Config, camMgr *camera.CameraManager, hlsMgr *hls.Manager, configPath string, mergeMgr *merge.MergeManager, cloudProxy CloudAuthProxy, mergeScheduler *timelapse.MergeScheduler, gb28181DeviceMgr *platform.DeviceManager, gb28181SessionMgr *platform.SessionManager, timeSync TimeSyncOps) *Handler {
	h := &Handler{db: db, store: store, authMW: authMW, config: cfg, camMgr: camMgr, hlsMgr: hlsMgr, configPath: configPath, recFrames: recframes.New(), mergeMgr: mergeMgr, cloudProxy: cloudProxy, mergeScheduler: mergeScheduler, gb28181DeviceMgr: gb28181DeviceMgr, gb28181SessionMgr: gb28181SessionMgr, timeSync: timeSync, vodMgr: vod.NewManager()}
	h.snapStore = streamsapi.NewSnapshotStore()
	// The device-record domain service shares the handler's post-injection
	// wiring: media API, device registry, GB timezone (#1009).
	h.gb28181Records = gb28181.NewDeviceRecordService()
	h.gb28181Records.SetDeviceManager(gb28181DeviceMgr)
	// Manual periodic-merge trigger (#1009 batch 3): the recording-enabled
	// gate mirrors the scheduled path (nil/unknown camera ⇒ include video).
	// A nil camMgr must stay a nil interface — a typed nil would sneak past
	// the service's nil guard and panic on lookup.
	var tlCams timelapse.CameraSettings
	if camMgr != nil {
		tlCams = camMgr
	}
	h.tlTrigger = timelapse.NewManualTrigger(db, func() *config.Config { return h.config }, tlCams, func(cameraID string) bool {
		if camMgr == nil {
			return true
		}
		cam := camMgr.GetCameraConfig(cameraID)
		if cam == nil {
			return true
		}
		return h.config.RecordingGate(cam.RecordingEnabled)
	})
	return h
}

// Close cancels any in-flight manual timelapse merges (single or batch) and
// waits for them to exit — the trigger owns their goroutine lifecycle (#1009
// batch 3). It must be called during application shutdown (after the HTTP
// server stops accepting requests) so merges don't outlive the storage tree —
// the App.Service contract requires all goroutines be released, and tests
// using t.TempDir for the storage root will otherwise hit "directory not
// empty" during RemoveAll (#143 / #125 class). Idempotent.
func (h *Handler) Close() {
	if h.tlTrigger != nil {
		h.tlTrigger.Close()
	}
}

// Routes returns a chi.Router with all routes registered.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()

	// Public routes (rate-limited health/readyz/capabilities/events)
	h.registerPublicRoutes(r)
	// Anonymous routes (login, setup, public playback, model serving)
	h.registerAnonymousRoutes(r)
	h.registerViewerLoginRoutes(r)

	// Protected routes (behind authMW)
	r.Group(func(r chi.Router) {
		r.Use(h.authMW)
		recordingsapi.Register(r, recordingsDeps(h))
		camerasapi.Register(r, camerasDeps(h))
		streamsapi.Register(r, streamsDeps(h))
		systemapi.Register(r, systemDeps(h))
		// License / P2P / update / rtsp-output settings stay at the root until
		// their domains extract (handlers_license/_p2p/_update/_settings_rtsp).
		r.Get("/api/version", h.handleVersion)
		r.Get("/api/license", h.handleGetLicense)
		r.Put("/api/license", h.handleInstallLicense)
		r.Get("/api/settings/rtsp-output", h.handleGetRtspOutputSettings)
		r.Put("/api/settings/rtsp-output", h.handleUpdateRtspOutputSettings)
		r.Get("/api/p2p/status", h.handleGetP2PStatus)
		r.Post("/api/p2p/viewer-report", h.handleP2pViewerReport)
		r.Get("/api/p2p/viewer", h.handleGetP2pViewer)
		r.Get("/api/settings/p2p", h.handleGetP2PSettings)
		r.Put("/api/settings/p2p", h.handleUpdateP2PSettings)
		r.Get("/api/p2p/association", h.handleGetP2PAssociation)
		r.Post("/api/p2p/association/request", h.handleRequestP2PAssociation)
		r.Post("/api/p2p/association/confirm", h.handleConfirmP2PAssociation)
		r.Post("/api/p2p/association/enroll", h.handleEnrollP2PAssociation)
		r.Post("/api/p2p/association/unbind", h.handleUnbindP2PAssociation)
		r.Post("/api/p2p/association/cancel", h.handleCancelP2PAssociation)
		r.Get("/api/update/check", h.handleUpdateCheck)
		r.Post("/api/update/check", h.handleUpdateCheck)
		r.Post("/api/update/apply", h.handleUpdateApply)
		r.Get("/api/update/apply/status", h.handleUpdateApplyStatus)
		r.Get("/api/update/history", h.handleUpdateHistory)
		h.registerHealthRoutes(r)
		relayapi.Register(r, relayapi.Deps{Relay: relayDeps(h.relayMgr)})
		h.registerAIRoutes(r)
		h.registerVisionRoutes(r)
		h.registerTelemetryRoute(r)
		h.registerViewerRoutes(r)
		gbapi.Register(r, gbDeps(h))
		h.registerDoorbellRoutes(r)
		notifyapi.Register(r, notifyapi.Deps{Service: notifyDeps(h.notifySvc)})
		h.registerMediaRoutes(r)
		// 预投产调试：App 诊断上报（authed；文件落盘不进 DB）。
		r.Post("/api/diagnostics/app", h.handleAppDiagnosticsReport)
		r.Get("/api/health/cameras", h.handleHealthCameras)
		// Event SSE — full-bus topics (motion/AI events, file paths) are not
		// for unauthenticated listeners; browsers pass ?token=, integrations
		// use API keys (#879).
		r.Get("/api/events", h.handleEvents)
		h.registerOffloadRoutes(r)
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
		r.Get("/api/readyz", h.handleReadyz)
		r.Get("/api/capabilities", h.handleCapabilities)
		// Webhook trigger (public, rate-limited — HMAC signature is the credential, #709)
		camerasapi.RegisterPublic(r, camerasDeps(h))
		// GB28181 snapshot upload lives in the gb domain (public callback).
		gbapi.RegisterPublic(r, gbDeps(h))
		// ONVIF push-event notify consumer (public, rate-limited — the token
		// in the URL is the credential; devices cannot BasicAuth, #922)
		if h.camMgr != nil {
			r.Post("/api/onvif/notify/{cameraID}/{token}", h.handleONVIFNotify)
			r.Post("/api/onvif/n/{token}", h.handleONVIFNotifyShort)
		}
	})
}

// registerAnonymousRoutes registers endpoints that require no authentication
// but are NOT rate-limited (login, setup, public video playback, AI model file).
func (h *Handler) registerAnonymousRoutes(r chi.Router) {
	// Login/setup are credential-guessing targets: always rate-limited per IP,
	// independent of the optional global auth.rate_limit switch.
	r.Group(func(r chi.Router) {
		rl := middleware.NewRateLimiter(context.Background(), middleware.RateLimiterConfig{
			MaxRequests: 10,
			Window:      time.Minute,
		})
		r.Use(rl.Handler)
		r.Post("/api/auth/login", h.handleLogin)
		r.Post("/api/setup", h.handleSetup)
	})
	// Loopback-local management endpoints (desktop tray / macOS menu-bar
	// helper): gated by middleware.IsBypassEligible INSIDE the handlers —
	// remote callers get 403.
	r.Post("/api/auth/password", h.handlePasswordChange)
	r.Post("/api/system/shutdown", handleSystemShutdown)
	r.Put("/api/system/listen", handleListenChange)
	// fnOS unified-gateway SSO (#394): mints an NVR session token when the
	// request carries a gateway-verified ADMIN identity. The identity context
	// only exists on the gateway Unix-socket listener — everywhere else this
	// always returns 401, so it cannot be used to bypass the direct login.
	r.Get("/api/auth/gateway-session", h.handleGatewaySession)
	r.Get("/models/{filename}", h.handleServeModel) // Public for browser-side AI model loading
	// WHIP push-in ingest (#369): browsers/OBS cannot send auth headers with
	// the SDP POST, and the stream key IS the credential (RTMP/SRT streamid
	// threat model). Must NOT be rate-limited — media sessions are long-lived.
	if h.whipServer != nil {
		h.whipServer.RegisterRoutes(r)
	}
}

// registerMediaRoutes registers recording download and VOD playback behind
// the auth middleware. Credential forms, in auth-priority order: Bearer
// session token (SPA fetches), BasicAuth (HA integration), API key, the
// mbs_session stream cookie (HLS segment fetches — the VOD playlist handler
// sets it via setStreamCookieOnPlaylist, same mechanism as live HLS), and
// ?token= (SPA <video src> / <a download> links, EventSource). Recording IDs
// are UnixNano-predictable, so these endpoints must not sit in the anonymous
// group. The remote offload playback proxy (#874 batch 2, tightened #893)
// shares this exposure class — outbox ids are equally predictable.
func (h *Handler) registerMediaRoutes(r chi.Router) {
	// Live camera audio (xiaomi/two-way speakers; camera-audio domain concern)
	// and the remote-offload proxy share this exposure class with the
	// recording download paths, which moved to recordingsapi.
	r.Get("/api/offload/objects/{id}", h.handleOffloadObject)
	r.Head("/api/offload/objects/{id}", h.handleOffloadObject)
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func WriteError(w http.ResponseWriter, status int, msg string) {
	httpx.WriteError(w, status, msg)
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
	return httpx.ParsePagination(r, defaultLimit, maxLimit)
}

func writeAPIError(w http.ResponseWriter, status int, err error) {
	httpx.WriteAPIError(w, status, err)
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
	return httpx.ValidateIP(ip)
}

// noopAuthMW is a middleware that passes all requests through (no auth).
func noopAuthMW() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return next
	}
}

// noopHandler is a helper for creating a Handler without real auth.
func noopHandler(db *storage.DB, store *storage.Manager) *Handler {
	return NewHandler(db, store, noopAuthMW(), nil, nil, nil, "", nil, nil, nil, nil, nil, nil)
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
	return NewHandler(db, store, authMW, nil, nil, nil, "", nil, nil, nil, nil, nil, nil)
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

// handleServeModel serves AI model files from the models directory.
// This is a public endpoint (no auth) so the browser can load ONNX models.
func (h *Handler) handleServeModel(w http.ResponseWriter, r *http.Request) {
	filename := chi.URLParam(r, "filename")
	if filename == "" {
		WriteError(w, http.StatusBadRequest, "filename required")
		return
	}
	// Serve from ModelsDir() — the data-volume models dir when the platform
	// provides NVR_DATA_DIR (docker-entrypoint seeds $NVR_DATA_DIR/models),
	// matching download_model and GET /api/ai/models. Joining RootDir here
	// instead made the listing and the file server disagree on fnOS, where
	// the data dir and the recording root are different volumes.
	modelDir := h.config.Storage.ModelsDir()

	// Sanitize: prevent path traversal
	cleanPath := filepath.Clean(filepath.Join(modelDir, filename))
	modelDirWithSep := modelDir + string(filepath.Separator)
	if cleanPath != modelDir && !strings.HasPrefix(cleanPath, modelDirWithSep) {
		WriteError(w, http.StatusBadRequest, "invalid filename")
		return
	}

	h.serveFileBudgeted(w, r, cleanPath)
}

// PTZ suppression windows. Move/goto must outlast a long press or a preset
// goto crossing the full pan range on a slow motor — if the window expires
// mid-move the moving scene reads as activity and pollutes adaptive
// recording and motion events. Stop only needs the scene to settle.
const ()

// SetPTZSuppressor wires the pixgate suppression hook fired on every PTZ
// command (move / stop / goto preset), all protocols.
func (h *Handler) SetPTZSuppressor(f func(cameraID string, window time.Duration)) {
	h.ptzSuppress = f
}

// suppressPTZMotion fires the PTZ suppression hook if wired (nil-safe).
func (h *Handler) suppressPTZMotion(cameraID string, window time.Duration) {
	if h.ptzSuppress != nil {
		h.ptzSuppress(cameraID, window)
	}
}

// SetXiaomiPTZMotor wires an override for Xiaomi PTZ motor dispatch.
// When nil, commands go through the camera manager's Xiaomi recorder.
func (h *Handler) SetXiaomiPTZMotor(f func(cameraID, direction string, speed int) error) {
	h.xiaomiPTZMotor = f
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
