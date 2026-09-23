package app

// builders_http.go — buildAppDeps phase 5: API handler wiring, stream
// registry, WebDAV/upload handlers, chi router and the HTTP server.
//
// flvMgr, gbLibEvents, tlSourceDeleter, snapCapturer and triggerDispatcher
// are constructed in earlier phases and consumed here without living in
// appDeps (see their construction comments for why).

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mickeyzzc/gb28181-go/platform"
	gbsip "github.com/mickeyzzc/gb28181-go/platform/sip"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/ai"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/api"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/flv"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181"
	authmw "github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/snapshot"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/transcoding"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/upload"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/webdav"
)

// buildHTTPDeps mutates deps with handler/router/httpServer.
func buildHTTPDeps(deps *appDeps, flvMgr *flv.Manager, gbLibEvents *gbsip.EventBus, tlSourceDeleter timelapseSourceDeleter, snapCapturer *snapshot.Capturer, triggerDispatcher triggerDispatcherFunc) error {
	cfg, configPath := deps.cfg, deps.configPath
	db := deps.db
	store := deps.store
	m := deps.metrics
	authMW := deps.authMW
	camMgr := deps.camMgr
	hlsMgr := deps.hlsMgr
	wsMgr := deps.wsMgr
	relayMgr := deps.relayMgr
	healthMgr := deps.healthMgr
	appLoc := deps.appLoc

	// ---- Build HTTP router ----
	cloudProxy := api.NewLocalXiaomiAuth(cfg)
	handler := api.NewHandler(db, store, authMW, cfg, camMgr, hlsMgr, configPath, deps.mergeMgr, cloudProxy, deps.mergeScheduler, deps.gb28181DevMgr, deps.gb28181SessionMgr)

	// First-boot setup code (#879): printed to the terminal/log so the person
	// at the machine claims the admin account before any LAN peer can. Loopback
	// (desktop) setup is exempt from providing it.
	if strings.TrimSpace(cfg.Auth.PasswordHash) == "" && strings.TrimSpace(cfg.Auth.Password) == "" {
		if code, err := handler.ArmFirstBootSetup(); err == nil {
			slog.Warn("首次设置校验码(完成管理员设置时填入)/ first-boot setup code: " + code)
		} else {
			slog.Error("failed to arm first-boot setup code", "error", err)
		}
	}
	handler.SetStorageMigrator(deps.migrationMgr)

	// Playback reads join the shared budget as the "playback" tenant when
	// opted in (#886 gray-release); nil keeps plain ServeFile semantics.
	if deps.ioBudget != nil && cfg.IO.PlaybackReadsBudgeted {
		handler.SetPlaybackBudget(deps.ioBudget)
	}

	// Live API key store: seeded from config, updated in place by the
	// generate/revoke handlers so key changes apply without restart (#335).
	apiKeyStore := authmw.NewAPIKeyStore()
	apiKeyStore.SetKeys(validAPIKeysFromConfig(cfg))
	handler.SetAPIKeyStore(apiKeyStore)

	if deps.whipServer != nil {
		handler.SetWHIPServer(deps.whipServer)
	}
	// Wire streaming managers
	handler.SetWebRTCManager(deps.webrtcMgr)
	handler.SetFLVManager(flvMgr)
	handler.SetWSManager(wsMgr)
	handler.SetHealthManager(healthMgr)
	handler.SetStabilityProvider(healthMgr)
	handler.SetEventBus(deps.eventBus)
	// FFmpeg-gated snapshot capturer for the latest-frame endpoint (#657).
	handler.SetSnapshotCapturer(snapCapturer)
	// Shared trigger dispatcher (MQTT + webhook, #709).
	handler.SetTriggerDispatcher(triggerDispatcher)
	// Camera-side ONVIF MotionAlarm events ride the same action surface
	// (#711) — record triggers behave identically to MQTT/webhook.
	deps.camMgr.SetMotionActionHandler(triggerDispatcher)
	api.SetAPIMetrics(m)
	if deps.rollingMergeMgr != nil {
		handler.SetTimelapseMergeMgr(deps.rollingMergeMgr)
	}
	handler.SetRollingMergeMgr(deps.recordRollingMergeMgr)
	// Remote offload playback proxy (issue #874 batch 2): remote-only
	// (evicted) archive playback rides the coalescing range proxy.
	if deps.offloadProxy != nil {
		handler.SetOffloadPlayback(deps.offloadProxy)
	}
	if deps.visionMgr != nil {
		handler.SetVisionCoordinator(deps.visionMgr)
	}
	// Wire AI handler (config + zones only, no backend inference)
	aiMgr := ai.NewManager(aiConfigFromConfig(cfg.AI), deps.eventBus)
	ah := api.NewAIHandler(aiMgr, cfg, configPath)
	handler.SetAIHandler(ah)
	// Manual merges (POST /api/timelapse/{id}/merge, batch-merge) get the same
	// opt-in delete_recordings_after_merge mechanism as the scheduled path.
	handler.SetTimelapseSourceDeleter(tlSourceDeleter)
	handler.SetRelayManager(relayMgr)
	// Wire GB28181 PTZ controller (sends DeviceControl via the SIP server) when
	// the GB28181 platform server is enabled.
	var gbPTZController *platform.PTZController
	if deps.gb28181Server != nil {
		gbPTZController = platform.NewPTZController(deps.gb28181DevMgr, deps.gb28181Server)
		handler.SetGB28181PTZ(gbPTZController)
		// GB/T 28181-2022 on-demand snapshot + manual record (#708): the
		// DeviceControl sender (snapshot/record handlers) and the session
		// registry backing the public upload endpoint. Frames persist under
		// the storage root like any snapshot and publish camera.snapshot.
		handler.SetGB28181Commander(gbPTZController)
		gbSnapMgr := gb28181.NewSnapshotSessionManager(
			&snapshot.Persistor{Root: store.RootDir()},
			30*time.Second,
			gb28181.WithSnapshotPublisher(func(topic string, data any) {
				deps.eventBus.Publish(context.Background(), topic, data)
			}),
		)
		gbSnapMgr.Start()
		// #708 loop closure: the device's UploadSnapShotFinished notify
		// (gb28181-go PR #54) closes snapshot sessions on completion instead
		// of timing out after the 30s TTL.
		gbSnapMgr.SubscribeFinished(gbLibEvents)
		handler.SetGB28181SnapshotManager(gbSnapMgr)
		handler.SetGB28181Catalog(platform.NewCatalogController(deps.gb28181DevMgr, deps.gb28181Server))
		handler.SetGB28181Inviter(deps.gb28181Server)
		handler.SetGB28181ByeSender(deps.gb28181Server)
		handler.SetGB28181DeviceMedia(gb28181.ServerAdapter{Server: deps.gb28181Server})
		handler.SetGB28181Timezone(appLoc)
		// Auto-create cameras when GB28181 devices register, matching ONVIF auto-add.
		deps.gb28181Server.SetCameraEnroller(camMgr)
		// Auto-INVITE when GB28181 recorders start (pull media on camera creation).
		camMgr.SetGB28181Inviter(deps.gb28181Server)
		camMgr.SetGB28181SessionEnder(deps.gb28181Server)
		// GB sub-channel pull sessions (#560): the SIP server structurally
		// implements substream.GBPuller (EnsureSubChannelRegistered +
		// InviteSubChannel).
		if subs := camMgr.SubStreams(); subs != nil {
			subs.SetGBPuller(deps.gb28181Server)
		}
		// Naive GB device-clock timestamps follow the app timezone — hosts in
		// a different zone than the devices (UTC container, CST cameras) would
		// otherwise skew every record window by the TZ offset.
		deps.gb28181Server.SetGBTimezone(appLoc)
	}
	// Wire the cascade client: registration status surfaced in Settings, and
	// upper-platform PTZ DeviceControl commands bridged to the local camera's
	// native PTZ (ONVIF ContinuousMove / Xiaomi motor / local GB channel).
	if deps.gb28181Cascade != nil {
		handler.SetGB28181Cascade(deps.gb28181Cascade)
		deps.gb28181Cascade.SetGBTimezone(appLoc)
		var gbSend func(channelID, direction string, speed byte) error
		if gbPTZController != nil {
			gbSend = gbPTZController.SendPTZ
		}
		deps.gb28181Cascade.SetPTZForwarder(func(cameraID, direction string, speed byte) error {
			return camera.ForwardPTZ(context.Background(), camMgr, gbSend, cameraID, direction, speed)
		})
	}
	// Create and populate StreamRegistry for protocol discovery
	reg := api.NewStreamRegistry()
	reg.Register(&api.HLSStreamHandler{Mgr: hlsMgr})
	// LL-HLS is always available (low-latency fMP4 muxer always enabled).
	reg.Register(&api.LLHLSStreamHandler{
		HLSStreamHandler: api.HLSStreamHandler{Mgr: hlsMgr},
	})
	if deps.webrtcMgr != nil {
		reg.Register(&api.WebRTCStreamHandler{})
	}
	if flvMgr != nil {
		reg.Register(&api.FLVStreamHandler{})
	}
	// WebSocket stream handler is always available
	reg.Register(&api.WSStreamHandler{})
	// MJPEG stream handler for JPEG/MJPEG cameras (proxy on-demand)
	reg.Register(&api.MJPEGStreamHandler{})
	handler.SetStreamRegistry(reg)

	// Wire FFmpeg downloader for transcoding status/download APIs
	if deps.transcodeMgr != nil {
		handler.SetDownloader(deps.transcodeMgr.Downloader())
		handler.SetTranscodeManager(deps.transcodeMgr)
	} else {
		// Always provide a downloader so FFmpeg status APIs work even when transcoding is disabled
		transcoding.DownloadMirror = cfg.Transcoding.DownloadMirror
		handler.SetDownloader(transcoding.NewDownloader(cfg.Storage.RootDir, nil))
	}

	// WebDAV
	var davHandler http.Handler
	if cfg.WebDAV.Enabled != nil && *cfg.WebDAV.Enabled {
		davSrv := webdav.NewServer(store, cfg.WebDAV.PathPrefix, authMW, db, cfg.WebDAV.ReadWrite)
		davHandler = davSrv.Handler()
	}

	// Upload handler
	uploadHandler := upload.NewHandler(store, db, 100<<20) // 100MB max

	// Register WebDAV methods with chi so it doesn't reject them as 405.
	chi.RegisterMethod("PROPFIND")
	chi.RegisterMethod("MKCOL")
	chi.RegisterMethod("LOCK")
	chi.RegisterMethod("UNLOCK")
	chi.RegisterMethod("COPY")
	chi.RegisterMethod("MOVE")

	// ---- Build HTTP router ----
	r, err := buildRouter(cfg, authMW, handler, m, davHandler, uploadHandler, apiKeyStore)
	if err != nil {
		return err
	}
	deps.handler = handler
	deps.router = r

	deps.httpServer = &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// WriteTimeout is intentionally not set: SSE endpoints, large file
		// downloads (ServeFile), and video streaming need long-lived connections.
		// Setting it would kill legitimate long responses.
	}
	return nil
}

// validAPIKeysFromConfig extracts the non-revoked API keys (token → name)
// from the config, for seeding the live APIKeyStore.
func validAPIKeysFromConfig(cfg *config.Config) map[string]string {
	valid := make(map[string]string)
	for _, k := range cfg.APIKeys {
		if !k.Revoked && k.Key != "" {
			valid[k.Key] = k.Name
		}
	}
	return valid
}
