package app

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/api"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/cleanup"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/ftp"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/health"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/hls"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware/remotelog"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/migration"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/motion"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/mqtt"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/pixgate"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/relay"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/rtmp"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/rtsp"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/snapshot"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/srt"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/transcoding"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/vision"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/webrtc"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/whip"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/wsstream"
	"github.com/mickeyzzc/gb28181-go/platform"
	gbcascade "github.com/mickeyzzc/gb28181-go/platform/cascade"
	gbsip "github.com/mickeyzzc/gb28181-go/platform/sip"
)

// appDeps holds every manager/handler/router constructed by buildAppDeps and
// consumed by registerServices. Splitting RunFree into construct + register
// phases requires a shared state object so the registration closures (which
// capture the managers) can live in a separate function/file from the
// construction that builds them (#138).
//
// Field order matches construction order in buildAppDeps, not registration
// order — these are independent. The Start/Stop sequence registered in
// registerServices is what matters for correctness; buildAppDeps order only
// matters insofar as each manager needs its dependencies constructed first.
type appDeps struct {
	// Inputs / config
	cfg        *config.Config
	configPath string

	// Storage + observability
	db           *storage.DB
	store        *storage.Manager
	migrationMgr *migration.Migrator
	metrics      *metrics.Metrics
	eventBus     *event.EventBus
	authMW       func(http.Handler) http.Handler
	remoteLogH   *remotelog.Handler
	appLoc       *time.Location

	// Merge / transcode / timelapse
	mergeMgr              *merge.MergeManager
	recordRollingMergeMgr *merge.RollingMergeCoordinator // quasi-real-time merge (registered as "rolling-merge" service)
	transcodeMgr          *transcoding.TranscodeManager
	rollingMergeMgr       *timelapse.RollingMergeManager // timelapse rolling merge (wired to API handler)
	mergeScheduler        *timelapse.MergeScheduler
	visionMgr             *vision.Coordinator // NVR→Vision push coordinator
	motionAnalyzer        *motion.Analyzer    // offline motion-score service (issue #435)
	pixgateMgr            *pixgate.Manager    // pixel-domain fine gate (issue #636)

	// Camera + health + relay
	camMgr    *camera.CameraManager
	healthMgr *health.Manager
	relayMgr  *relay.Manager

	// Streaming
	hlsMgr    *hls.Manager
	webrtcMgr *webrtc.Manager
	wsMgr     *wsstream.Manager

	// Ingest listeners (optional)
	mqttClient        *mqtt.Client
	mqttStatusPub     *mqtt.StatusPublisher
	snapRunner        *snapshot.Runner // MQTT snapshot trigger: capture → persist → event (#656)
	ftpServer         *ftp.Server
	rtmpServer        *rtmp.Server
	srtListener       *srt.Listener
	whipServer        *whip.Server
	rtspServer        *rtsp.Server
	gb28181Server     *gbsip.Server
	gb28181Cascade    *gbcascade.Service
	gb28181DevMgr     *platform.DeviceManager
	gb28181SessionMgr *platform.SessionManager

	// Cleanup
	cleanupMgr     *cleanup.CleanupManager
	archiveDeleter *cleanup.ArchiveDeleter

	// HTTP layer
	handler    *api.Handler
	router     http.Handler
	httpServer *http.Server

	// Startup background goroutine lifecycle (CleanupTempFiles +
	// ReconcileOrphanedFiles). The cancel+wg are consumed by the "startup-bg"
	// service's Stop so those goroutines exit before App returns (#143).
	startupBgCancel context.CancelFunc
	startupBgWG     *sync.WaitGroup
}
