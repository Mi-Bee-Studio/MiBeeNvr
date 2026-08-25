package camera

// This file defines the CameraManager: its struct fields, the constructor
// (NewCameraManager), and the option/auxiliary types. All behavioral methods
// live in focused files split out by responsibility (#225):
//   - lifecycle.go        Start/Stop + per-camera manual controls + helpers
//   - hub.go              StreamHub lifecycle + metrics wiring
//   - transcode.go        post-recording transcoding integration
//   - stream_keys.go      RTMP/SRT stream-key + push-protocol resolution
//   - config_accessors.go lock-free config/manager accessors
//   - status.go           status reads + health-manager integration
//   - registry.go         copy-on-write snapshot registry (concurrency core)
//   - crud.go, rediscovery.go, timelapse.go, recorder_factory.go, onvif_client.go, …

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/health"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/transcoding"
)

var logger = slog.Default().With("component", "camera-manager")

// CameraUpdate holds optional fields for updating a camera.
// Only non-nil fields will be applied.
type CameraUpdate struct {
	Name           *string
	URL            *string
	Protocol       *string
	Encoding       *string
	Username       *string
	Password       *string
	Description    *string
	Location       *string
	Brand          *string
	Model          *string
	SerialNumber   *string
	RetentionDays  *int
	ONVIFEndpoint  *string
	ProfileToken   *string
	StreamEncoding *string
	// Sub-stream fields (#512): manual overrides for the sub profile token
	// (ONVIF) and the sub stream URL (any RTSP-capable protocol). They do not
	// restart the recorder — sub consumers pick them up on their next start.
	SubProfileToken *string
	SubStreamURL    *string
	Channel         *string
	Transcoding     *config.CameraTranscodingConfig
	AudioEnabled    *bool
	// Dark frame filtering
	DarkFrameFilterEnabled *bool
	DarkFrameThreshold     *int
	// RecordingEnabled gates segment writes (false = live-only). nil = unchanged.
	RecordingEnabled *bool
	// CascadeEnabled gates GB28181 cascade exposure (false = hidden from the
	// upper platform's catalog and INVITEs refused). nil = unchanged. Takes
	// effect at the next catalog response / INVITE — no recorder restart.
	CascadeEnabled *bool
	// Recording schedule
	RecordingSchedule *config.ScheduleConfig
	// RecordingMode selects the write-density strategy (#435): ""/"continuous"
	// or "adaptive". nil = unchanged. Changing it (or Adaptive params) requires
	// a recorder restart — the mode is read at recorder construction.
	RecordingMode *string
	Adaptive      *config.AdaptiveRecordingConfig
	// AudioTrigger arms loudness-triggered recording (#478) on adaptive
	// cameras. nil = unchanged; {enabled:false} disarms. Changes require a
	// recorder restart (read at recorder construction).
	AudioTrigger *config.CameraAudioTriggerConfig
	// Push/ingest camera fields (SRT/RTMP). nil = unchanged.
	StreamKey     *string
	SRTPassphrase *string
	SRTStreamID   *string
	// Push-out relay targets (replace the whole list when set). nil = unchanged.
	PushTargets       *[]config.PushTargetConfig
	PushRetentionDays *int
	// IP self-healing: stable hardware ID (ONVIF serial) + candidate subnets.
	// nil = unchanged. Empty string/nil slice = clear.
	StableID    *string
	SubnetHints *[]string
	// ActivationState gates recorder startup: "active" (default) or
	// "pending_activation". Set to "active" by ActivateCamera to flip a
	// pending camera live. nil = unchanged. See CameraConfig.ActivationState.
	ActivationState *string
	// GB28181 channel binding (protocol "gb28181"). nil = unchanged; changing
	// DeviceID/ChannelID restarts the recorder (the SIP session must be
	// re-INVITEd to the new channel).
	GB28181 *config.GB28181ChannelConfig
}

type CameraManager struct {
	cfg                *config.Config
	store              *storage.Manager
	db                 *storage.DB
	configPath         string
	metrics            *metrics.Metrics
	mergeMgr           *merge.MergeManager                     // segment merge manager (nil = no merge)
	timelapseMergeMgr  *timelapse.RollingMergeManager          // timelapse rolling merge (nil = no merge)
	transcodeMgr       *transcoding.TranscodeManager           // transcoding manager (nil = no transcoding)
	healthMgr          *health.Manager                         // health monitoring (nil when disabled)
	scheduler          *timelapse.Scheduler                    // timelapse schedule evaluator
	scheduleMonitors   map[string]context.CancelFunc           // camera_id -> cancel func for schedule monitor
	keyframeExtractors map[string]*timelapse.KeyframeExtractor // camera_id -> keyframe extractor (H.264/H.265)
	framePollers       map[string]*timelapse.SnapshotCapturer  // camera_id -> frame poller (MJPEG/JPEG latest_frame)
	// auxMu guards the auxiliary lifecycle bookkeeping maps (scheduleMonitors,
	// keyframeExtractors, framePollers). These are NOT part of the registry
	// snapshot (they're per-camera lifecycle handles, mutated only by the
	// lifecycle path) and have their own mutex so lifecycle operations don't
	// serialize against registry reads.
	auxMu sync.Mutex
	// lifecycleMu holds per-camera mutexes (camera_id → *sync.Mutex) used by
	// withCameraLifecycle to serialize start/stop/restart for a single camera,
	// preventing concurrent-recorder-construction leaks. See registry.go.
	lifecycleMu sync.Map
	// startStopMu serializes the top-level Start/Stop pair. App wiring runs
	// Start detached (pkg/app spawns it in a goroutine so readiness doesn't
	// wait on cameras), so Stop can arrive while Start is still registering
	// recorders — without this lock the teardown races the registration
	// (fields + the recorder snapshot itself, leaking recorders that Stop
	// never sees). Per-camera operations use withCameraLifecycle instead.
	startStopMu sync.Mutex
	// snapshot is the immutable, atomically-published registry view consumed by
	// ALL lock-free reads (GetRecorder/GetHub/Status/statusSnapshot/counts). See
	// registry.go. Writes go through apply(); lifecycle I/O runs outside any lock
	// and is serialized per-camera by the actor (actor.go).
	snapshot atomic.Pointer[snapshot]
	// configMu serializes the apply() write path (snapshot clone + mutate +
	// store) AND cfg.Cameras mutation + persistConfig disk writes. It is the
	// ONLY mutex covering registry state, and it is held only for nanosecond map
	// copies plus millisecond disk writes — never for network I/O or rec.Start.
	configMu         sync.Mutex
	onvifClients     map[string]*onvif.Client            // camera_id → cached ONVIF client
	onvifMu          sync.Mutex                          // protects onvifClients
	errorDetails     map[string]*model.CameraErrorDetail // cameraID → latest error detail
	errorDetailsMu   sync.RWMutex                        // protects errorDetails
	eventSubscribers map[string]onvif.EventSubscriber    // camera_id → event subscriber
	deviceInfoCache  map[string]*onvif.DeviceInfo        // camera_id → cached device info
	deviceInfoMu     sync.RWMutex                        // protects deviceInfoCache
	eventBus         *event.EventBus                     // event bus for publishing segment events
	// relayMgr (optional) is notified when a camera's push-out targets change so
	// the relay engine can reconcile. Interface-typed to avoid a camera<->relay
	// import cycle.
	relayMgr RelayManager
	// gb28181Inviter (optional) sends SIP INVITE to start media sessions for
	// GB28181 cameras. When set, starting a GB28181 recorder auto-triggers INVITE.
	gb28181Inviter GB28181Inviter
	// gb28181SessionEnder recycles a channel session when its recorder is
	// replaced (camera update/restart), so the next auto-INVITE binds the
	// new recorder instead of streaming into a stale one.
	gb28181SessionEnder GB28181SessionEnder
	// backfillWg tracks the startup stable_id backfill goroutine so Stop can
	// wait for it to exit before returning. Without this, the goroutine can
	// outlive the DB handle and crash with "sql: database is closed" when the
	// test/process tears down resources (flaky TestStartupBackfillStableID).
	backfillWg sync.WaitGroup
	// onvifEnsureWg tracks the per-recorder-start ONVIF backfill goroutines
	// (ensureStableID / ensureProfileToken / ensureEncoding) spawned from
	// startRecorderLocked. Each does its own configMu.Lock + persistConfig +
	// DB write with a 15s timeout ctx; without joining them in Stop, those
	// writes could race the DB/config teardown that Stop initiates (#163).
	onvifEnsureWg sync.WaitGroup
	// Hub stats flusher state (#469): periodic exporter of hub per-consumer
	// atomics to Prometheus. The maps are touched only by the flusher goroutine;
	// hubFlusherMu guards the stop channel against start/stop callers.
	hubFlusherOnce sync.Once
	hubFlusherMu   sync.Mutex
	hubFlusherStop chan struct{}
	hubFlushLast   map[string][2]int64 // camera\x00consumer → last flushed {sends, bytes}
	hubBytesLast   map[string]int64    // camera → last flushed hub bytesIn
}

// RelayManager is the subset of *relay.Manager the camera manager calls. Kept
// here as an interface (taking config.PushTargetConfig) so internal/camera
// doesn't import internal/relay.
type RelayManager interface {
	SetCameraTargets(cameraID string, cfgs []config.PushTargetConfig)
	RemoveCamera(cameraID string)
}

func NewCameraManager(cfg *config.Config, store *storage.Manager, db *storage.DB, configPath string, opts ...any) *CameraManager {
	var m *metrics.Metrics
	var mm *merge.MergeManager
	var tm *transcoding.TranscodeManager
	var tmm *timelapse.RollingMergeManager
	var appLoc *time.Location
	var eb *event.EventBus
	for _, opt := range opts {
		switch v := opt.(type) {
		case *metrics.Metrics:
			m = v
		case *merge.MergeManager:
			mm = v
		case *transcoding.TranscodeManager:
			tm = v
		case *timelapse.RollingMergeManager:
			tmm = v
		case *time.Location:
			appLoc = v
		case *event.EventBus:
			eb = v
		}
	}
	if eb == nil {
		logger.Warn("CameraManager created with nil EventBus — segment events will not be published")
	} else {
		logger.Info("CameraManager created with EventBus")
	}
	cm := &CameraManager{
		cfg:                cfg,
		store:              store,
		db:                 db,
		configPath:         configPath,
		metrics:            m,
		mergeMgr:           mm,
		transcodeMgr:       tm,
		timelapseMergeMgr:  tmm,
		scheduler:          timelapse.NewScheduler(appLoc),
		scheduleMonitors:   make(map[string]context.CancelFunc),
		keyframeExtractors: make(map[string]*timelapse.KeyframeExtractor),
		framePollers:       make(map[string]*timelapse.SnapshotCapturer),
		errorDetails:       make(map[string]*model.CameraErrorDetail),
		onvifClients:       make(map[string]*onvif.Client),
		eventSubscribers:   make(map[string]onvif.EventSubscriber),
		deviceInfoCache:    make(map[string]*onvif.DeviceInfo),
		hubFlushLast:       make(map[string][2]int64),
		hubBytesLast:       make(map[string]int64),
		eventBus:           eb,
	}
	// Publish the initial immutable snapshot. Config pointers seed the configs
	// map from cfg.Cameras so GetCameraConfig works before Start() runs. The
	// recorders/hubs maps start empty (populated as recorders start). Guard
	// against a nil cfg (some tests construct a manager with a nil config).
	initSnap := newSnapshot()
	if cfg != nil {
		for i := range cfg.Cameras {
			initSnap.configs[cfg.Cameras[i].ID] = &cfg.Cameras[i]
		}
	}
	cm.snapshot.Store(initSnap)
	return cm
}
