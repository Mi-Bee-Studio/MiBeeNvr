package camera

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
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
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
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
	Channel        *string
	Transcoding    *config.CameraTranscodingConfig
	AudioEnabled   *bool
	// Dark frame filtering
	DarkFrameFilterEnabled *bool
	DarkFrameThreshold     *int
	// RecordingEnabled gates segment writes (false = live-only). nil = unchanged.
	RecordingEnabled *bool
	// Recording schedule
	RecordingSchedule *config.ScheduleConfig
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
	// snapshot is the immutable, atomically-published registry view consumed by
	// ALL lock-free reads (GetRecorder/GetHub/Status/statusSnapshot/counts). See
	// registry.go. Writes go through apply(); lifecycle I/O runs outside any lock
	// and is serialized per-camera by the actor (actor.go).
	snapshot atomic.Pointer[snapshot]
	// configMu serializes the apply() write path (snapshot clone + mutate +
	// store) AND cfg.Cameras mutation + persistConfig disk writes. It is the
	// ONLY mutex covering registry state, and it is held only for nanosecond map
	// copies plus millisecond disk writes — never for network I/O or rec.Start.
	configMu           sync.Mutex
	onvifClients       map[string]*onvif.Client            // camera_id → cached ONVIF client
	onvifMu            sync.Mutex                          // protects onvifClients
	errorDetails       map[string]*model.CameraErrorDetail // cameraID → latest error detail
	errorDetailsMu     sync.RWMutex                        // protects errorDetails
	eventSubscribers   map[string]onvif.EventSubscriber    // camera_id → event subscriber
	deviceInfoCache    map[string]*onvif.DeviceInfo        // camera_id → cached device info
	deviceInfoMu       sync.RWMutex                        // protects deviceInfoCache
	frameSampleCounter uint64                              // atomic: 1/100 sampling for frame processing duration
	eventBus           *event.EventBus                     // event bus for publishing segment events
	// relayMgr (optional) is notified when a camera's push-out targets change so
	// the relay engine can reconcile. Interface-typed to avoid a camera<->relay
	// import cycle.
	relayMgr RelayManager
	// backfillWg tracks the startup stable_id backfill goroutine so Stop can
	// wait for it to exit before returning. Without this, the goroutine can
	// outlive the DB handle and crash with "sql: database is closed" when the
	// test/process tears down resources (flaky TestStartupBackfillStableID).
	backfillWg sync.WaitGroup
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

// SetHealthManager sets the health manager for camera health monitoring.
// Can be called with nil to disable health monitoring.
// CameraCount returns the number of configured cameras (O(1), no DB query).
// Used by stats endpoints that only need the count, avoiding a redundant
// ListCameras DB round-trip per request.
func (cm *CameraManager) CameraCount() int {
	return len(cm.loadSnapshot().configs)
}

func (cm *CameraManager) SetHealthManager(m *health.Manager) {
	cm.healthMgr = m
	if m != nil {
		m.SetStatusFunc(cm.statusSnapshot)
	}
}

// statusSnapshot returns the current status of every camera the manager knows
// about, for consumption by the health manager's periodic loop. It merges two
// snapshot sources (both lock-free):
//   - snapshot.recorders: active recorders report their real Status().
//   - snapshot.failedStarts: cameras whose recorder failed to start (e.g. ONVIF
//     endpoint unreachable after an IP change). These are NOT in the recorders
//     map (startRecorder removes them on failure), so without surfacing them
//     here they would be invisible to the health loop → never auto-remediated →
//     never rediscovered. They are reported as StatusError so the existing
//     CheckAll → restart → blacklist → rediscovery chain can self-heal them.
//
// A camera present in BOTH maps (stale failed-start entry + live recorder) is
// dominated by its real recorder status.
func (cm *CameraManager) statusSnapshot() map[string]string {
	// Lock-free: load the immutable snapshot and read each recorder's Status().
	// Each recorder guards its own status with a short internal mutex; this loop
	// never holds any CameraManager lock, so it can never block lifecycle ops.
	s := cm.loadSnapshot()
	result := make(map[string]string, len(s.recorders)+len(s.failedStarts))
	for id, rec := range s.recorders {
		result[id] = string(rec.Status())
	}
	for id := range s.failedStarts {
		// Only surface a failed-start for cameras that still exist in config.
		// A failedStarts entry can outlive its camera if the camera was removed
		// and a stale entry lingered (failedStarts is in-memory only, not
		// persisted); reporting it would surface a phantom camera to the health
		// loop and the /api/health details. The configs map is the source of
		// truth for "does this camera still exist".
		if _, stillConfigured := s.configs[id]; !stillConfigured {
			continue
		}
		if _, exists := result[id]; !exists {
			result[id] = string(model.StatusError)
		}
	}
	return result
}

// markStartFailed records a camera whose recorder failed to start, so that
// statusFunc can surface it to the health manager as StatusError. This is the
// entry point that connects startup failures to the auto-remediation → IP
// markStartFailed records a camera whose recorder failed to start, so that
// statusFunc can surface it to the health manager as StatusError. This is the
// entry point that connects startup failures to the auto-remediation → IP
// rediscovery self-healing chain. Safe to call from the lifecycle actor or any
// goroutine — apply() takes only the short configMu.
func (cm *CameraManager) markStartFailed(cameraID string, err error) {
	cm.apply(func(s *snapshot) *snapshot {
		s.failedStarts[cameraID] = err
		return s
	})
}

// clearStartFailed removes a camera from the failed-start tracking. Called on
// successful (re)start so the camera transitions to normal health monitoring.
// Safe to call from the lifecycle actor or any goroutine.
func (cm *CameraManager) clearStartFailed(cameraID string) {
	cm.apply(func(s *snapshot) *snapshot {
		delete(s.failedStarts, cameraID)
		return s
	})
}

// SetTranscodeManager sets the transcoding manager for post-recording enqueue.
// Can be called with nil to disable transcoding. Thread-safe.
func (cm *CameraManager) SetTranscodeManager(m *transcoding.TranscodeManager) {
	cm.configMu.Lock()
	cm.transcodeMgr = m
	cm.configMu.Unlock()
}

// EnqueueTranscode checks per-camera transcoding config and enqueues a
// transcoding task if enabled. Non-blocking — runs the enqueue in a goroutine.
func (cm *CameraManager) EnqueueTranscode(cameraID, recordingID, inputPath, inputFormat string) {
	cm.configMu.Lock()
	tm := cm.transcodeMgr
	cm.configMu.Unlock()

	if tm == nil {
		return
	}

	// Resolve per-camera transcoding config
	tcfg := cm.cfg.ResolveTranscodingConfig(cameraID)
	if tcfg == nil || !tcfg.Enabled {
		return
	}

	// Determine target codec (default to h264)
	targetCodec := tcfg.TargetCodec
	if targetCodec == "" {
		targetCodec = "h264"
	}

	// Non-blocking enqueue — don't block recording pipeline
	bitrate := tcfg.Bitrate
	crf := tcfg.CRF
	go func() {
		if err := tm.EnqueueRecording(cameraID, recordingID, inputPath, inputFormat, targetCodec, bitrate, crf); err != nil {
			logger.Warn("failed to enqueue transcode task",
				"camera_id", cameraID,
				"recording_id", recordingID,
				"error", err)
		}
	}()
}

// Start creates and starts recorders for all enabled cameras in the config.
// If a single camera fails to start, it logs the error and continues with the rest.
func (cm *CameraManager) Start(ctx context.Context) error {
	segDur, err := time.ParseDuration(cm.cfg.Storage.SegmentDuration)
	if err != nil {
		return fmt.Errorf("camera manager: invalid segment duration %q: %w", cm.cfg.Storage.SegmentDuration, err)
	}

	for _, cam := range cm.cfg.Cameras {
		// Insert camera record into database
		if err := cm.db.UpsertCamera(ctx, cam.ID, cam.Name, string(cam.Protocol), cam.Encoding, cam.URL, cam.Username, cam.Password, cam.ONVIFEndpoint, cam.ProfileToken, cam.StreamEncoding, cam.StableID); err != nil {
			logger.Error("failed to insert camera record", "camera_id", cam.ID, "error", err)
		} else {
			logger.Info("inserted camera record", "camera_id", cam.ID)
		}

		switch cam.Protocol {
		case string(model.ProtoRTSP), string(model.ProtoHTTP):
			rec := cm.createRecorder(cam, segDur)
			if rec != nil {
				cm.apply(func(s *snapshot) *snapshot {
					s.recorders[cam.ID] = rec
					return s
				})
				if err := rec.Start(ctx); err != nil {
					logger.Error("failed to start recorder", "camera_id", cam.ID, "error", err)
					if cm.metrics != nil {
						cm.metrics.CameraConnectionErrorsTotal.WithLabelValues(cam.ID, classifyError(err)).Inc()
					}
				} else {
					logger.Info("started recorder", "camera_id", cam.ID, "protocol", cam.Protocol, "encoding", cam.Encoding)
					// Notify health manager of new camera with per-camera overrides
					var hOverrides *config.ResolvedHealthOverrides
					if cm.cfg.Health.Enabled {
						resolved := config.ResolveHealthOverrides(cm.cfg.Health, cam.HealthOverrides)
						hOverrides = &resolved
					}
					cm.healthMgr.OnCameraAdded(cam.ID, rec, hOverrides)
					// Check if recording is enabled (nil = default true).
					// When recording_enabled=true, timelapse frames come from recorded
					// segments via PeriodicMergeManager — skip starting the dedicated
					// capturer (keyframe extractor or frame poller) and rolling merge.
					recordingEnabled := cam.RecordingEnabled == nil || *cam.RecordingEnabled
					if recordingEnabled && cam.Timelapse != nil && cam.Timelapse.Enabled {
						logger.Info("skipping timelapse capturer + rolling merge: recording_enabled=true",
							"camera_id", cam.ID)
					} else {
						// Start keyframe extractor if camera has rtsp_keyframe timelapse config
						if effectiveDualModeFrameSource(cam) == "rtsp_keyframe" {
							// Runtime override: an ONVIF camera with empty encoding may have
							// resolved to rtsp_keyframe statically but actually be a JPEG device
							// (e.g. ESP32 MiBeeCam auto-detected as HTTPJPEG delegate). In that
							// case, use a frame poller instead.
							if isRecorderJPEG(rec) {
								if poller, perr := cm.startTimelapseFramePoller(cam.ID, cam, rec); perr != nil {
									logger.Error("failed to start timelapse frame poller", "camera_id", cam.ID, "error", perr)
								} else if poller != nil {
									cm.setFramePoller(cam.ID, poller)
								}
							} else if hub := getRecorderHub(rec); hub != nil {
								if err := cm.startTimelapseKeyframeExtractor(cam.ID, cam, hub, rec); err != nil {
									logger.Error("failed to start keyframe extractor", "camera_id", cam.ID, "error", err)
								}
							}
						} else if effectiveDualModeFrameSource(cam) == "latest_frame" {
							if poller, perr := cm.startTimelapseFramePoller(cam.ID, cam, rec); perr != nil {
								logger.Error("failed to start timelapse frame poller", "camera_id", cam.ID, "error", perr)
							} else if poller != nil {
								cm.setFramePoller(cam.ID, poller)
							}
						}
					}
					// Enforce timelapse schedule for dual-mode cameras (start/stop
					// the keyframe extractor or frame poller based on time-of-day).
					cm.startDualModeTimelapseScheduleMonitorForCamera(ctx, cam.ID, cam, rec)
				}
			}
		case string(model.ProtoONVIF):
			if err := cm.startRecorder(ctx, cam, segDur); err != nil {
				logger.Error("failed to start ONVIF recorder", "camera_id", cam.ID, "error", err)
			} else {
				logger.Info("started ONVIF recorder", "camera_id", cam.ID)
			}
		case string(model.ProtoSRT), string(model.ProtoRTMP):
			// Push/ingest cameras: the recorder waits for an incoming publisher
			// (it does not dial out). Its hub is registered in hubRegistry so the
			// SRT listener / RTMP server can find it.
			if err := cm.startRecorder(ctx, cam, segDur); err != nil {
				logger.Error("failed to start ingest recorder", "camera_id", cam.ID, "protocol", cam.Protocol, "error", err)
			} else {
				logger.Info("started ingest recorder, awaiting publisher",
					"camera_id", cam.ID, "protocol", cam.Protocol)
			}
		default:
			// Try plugin-registered protocols (e.g. xiaomi)
			if err := cm.startRecorder(ctx, cam, segDur); err != nil {
				logger.Warn("camera has unknown protocol, skipping", "camera_id", cam.ID, "protocol", cam.Protocol)
			} else {
				logger.Info("started plugin recorder", "camera_id", cam.ID, "protocol", cam.Protocol)
			}
		}
	}
	// Start recording schedule monitors for cameras with a recording_schedule configured.
	for _, cam := range cm.cfg.Cameras {
		if cam.RecordingSchedule != nil && len(cam.RecordingSchedule.TimeRanges) > 0 {
			cm.startRecordingScheduleMonitor(ctx, cam.ID)
		}
	}
	// Replay push-out relay targets for cameras loaded from config. Add/Update
	// reconcile targets at runtime, but Start() loads cameras directly from cfg —
	// without this, every service restart silently drops all push_targets
	// (push-status returns [] and no "relay target started" is ever logged).
	// Run in a goroutine per camera so Start isn't blocked on relay-engine init.
	if cm.relayMgr != nil {
		for _, cam := range cm.cfg.Cameras {
			if len(cam.PushTargets) == 0 {
				continue
			}
			cameraID := cam.ID
			targets := append([]config.PushTargetConfig(nil), cam.PushTargets...)
			go cm.relayMgr.SetCameraTargets(cameraID, targets)
		}
	}
	// Backfill stable_id from YAML config to DB for cameras that have a
	// YAML stable_id but no DB stable_id yet (e.g. pre-migration cameras).
	// For ONVIF cameras without stable_id, attempt to discover via ONVIF.
	// Non-blocking: runs in a goroutine and must not delay Start().
	if cm.db != nil {
		cm.backfillWg.Add(1)
		go func() {
			defer cm.backfillWg.Done()
			cm.backfillStableIDs(ctx)
		}()
	}
	return nil
}

// Stop stops all running recorders and waits for them to complete.
func (cm *CameraManager) Stop() error {
	// Snapshot the recorders (lock-free) then stop each OUTSIDE any lock —
	// rec.Stop can join a goroutine / do I/O and must not hold configMu.
	s := cm.loadSnapshot()
	recs := make([]model.Recorder, 0, len(s.recorders))
	for _, rec := range s.recorders {
		recs = append(recs, rec)
	}

	var errs []error
	for _, rec := range recs {
		if err := rec.Stop(); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("camera manager: %d recorder(s) failed to stop", len(errs))
	}

	cm.closeAllONVIFClients()

	// Stop all timelapse schedule monitors
	cm.auxMu.Lock()
	for _, cancel := range cm.scheduleMonitors {
		cancel()
	}
	cm.scheduleMonitors = make(map[string]context.CancelFunc)
	cm.auxMu.Unlock()

	// Stop all timelapse keyframe extractors
	cm.stopAllTimelapseKeyframeExtractors()

	// Stop all timelapse frame pollers
	cm.stopAllTimelapseFramePollers()

	// Wait for the startup stable_id backfill goroutine to exit. It holds the
	// db handle; without this wait it can race with resource teardown and crash
	// with "sql: database is closed". The goroutine checks ctx between cameras
	// so this returns promptly once the caller cancels the start context.
	cm.backfillWg.Wait()

	return nil
}

// GetSPS returns the source camera's current H.264 SPS/PPS (raw NALUs, no start
// code) and whether the source is H.264. Used by the relay engine to initialize
// RTMP/RTSP target tracks. Returns nil when the camera is not yet streaming or
// is not H.264.

// GetOrCreateHub returns the existing StreamHub for the camera ID, or creates a
// new one (with metrics callbacks wired) if none exists. This is the entry point
// used by the SRT listener and RTMP server: when a publisher pushes a stream,
// they obtain the SAME hub the recorder owns, so frames reach the live
// consumers (HLS/WebRTC/FLV/WS) that subscribe on demand. If a pull recorder
// already created the hub via initStreamHub, that instance is returned.
//
// The check-then-create is atomic via apply() (configMu held only for the map
// swap — hub construction + metrics wiring run inside apply but are pure CPU,
// no I/O). Two concurrent GetOrCreateHub calls for the same camera return the
// same hub: the second's apply sees the first's published snapshot.
func (cm *CameraManager) GetOrCreateHub(cameraID string) *model.StreamHub {
	// Fast path: hub already exists (lock-free).
	if hub := cm.snapshotHub(cameraID); hub != nil {
		return hub
	}
	// Slow path: create under configMu.
	var created *model.StreamHub
	cm.apply(func(s *snapshot) *snapshot {
		if existing, ok := s.hubs[cameraID]; ok {
			// Another goroutine won the race inside apply — reuse its hub.
			created = existing
			return s
		}
		hub := model.NewStreamHub()
		hub.SetCameraID(cameraID)
		// Wire the same observability callbacks as initStreamHub so push hubs are
		// instrumented identically to pull hubs.
		cm.wireHubMetrics(hub, cameraID, string(model.ProtoSRT))
		s.hubs[cameraID] = hub
		created = hub
		return s
	})
	return created
}

// wireHubMetrics attaches the standard StreamHub observability callbacks
// (frame counters, drop counters, buffer-depth gauges). Reads only
// construction-time fields (cm.metrics, cm.frameSampleCounter).
func (cm *CameraManager) wireHubMetrics(hub *model.StreamHub, cameraID, protocol string) {
	m := cm.metrics
	if m == nil {
		return
	}
	sampleCounter := &cm.frameSampleCounter
	hub.OnBroadcast = func(cid string, isIDR bool) {
		m.StreamHubFramesInTotal.WithLabelValues(cid).Inc()
		if sampleCounter != nil {
			count := atomic.AddUint64(sampleCounter, 1)
			if count%100 == 0 {
				start := time.Now()
				m.FrameProcessingDurationSeconds.WithLabelValues(cid, protocol).Observe(time.Since(start).Seconds())
			}
		}
	}
	hub.OnDrop = func(consumerID string) {
		m.StreamHubFramesDropped.WithLabelValues(cameraID, consumerID, "false").Inc()
	}
	hub.OnBroadcastAudio = func(cid string, codec string) {
		m.AudioFramesTotal.WithLabelValues(cid, codec).Inc()
	}
	hub.OnAudioDrop = func(cid string) {
		m.AudioFramesDroppedTotal.WithLabelValues(cid).Inc()
	}
	hub.OnBufferDepth = func(cid, consumerID string, depth int) {
		m.StreamHubBufferDepth.WithLabelValues(cid, consumerID).Set(float64(depth))
	}
	hub.OnJitterBufferDepth = func(cid string, depth int) {
		m.JitterBufferDepth.WithLabelValues(cid).Set(float64(depth))
	}
	hub.OnJitterReorder = func(cid string) {
		m.JitterBufferReordersTotal.WithLabelValues(cid).Inc()
	}
}

// GetIngestRecorder returns the IngestRecorder for a camera if it is one, else
// nil. Convenience for the SRT/RTMP servers that need to call WriteNALU /
// OnDisconnect on push cameras.
func (cm *CameraManager) GetIngestRecorder(cameraID string) *recorder.IngestRecorder {
	rec, ok := cm.GetRecorder(cameraID).(*recorder.IngestRecorder)
	if !ok {
		return nil
	}
	return rec
}

// RTMPKeyMap returns a copy of camera_id → stream_key for all RTMP cameras.
// Used by main.go to build the RTMP server's StreamKeyResolver (reverse lookup).
// Lock-free read from the snapshot.
func (cm *CameraManager) RTMPKeyMap() map[string]string {
	s := cm.loadSnapshot()
	out := make(map[string]string, len(s.configs))
	for _, cam := range s.configs {
		if cam.Protocol == string(model.ProtoRTMP) && cam.StreamKey != "" {
			out[cam.ID] = cam.StreamKey
		}
	}
	return out
}

// ResolveStreamKey maps an incoming RTMP stream key to its camera ID (with the
// legacy global rtmp.stream_keys map as a fallback). This is the LIVE resolver
// used by the RTMP server on every publisher connect — it reflects cameras added
// at runtime, unlike a snapshot built once at startup. Lock-free read.
func (cm *CameraManager) ResolveStreamKey(streamKey string) (cameraID string, ok bool) {
	s := cm.loadSnapshot()
	// Per-camera stream_key fields take precedence.
	for _, cam := range s.configs {
		if cam.Protocol == string(model.ProtoRTMP) && cam.StreamKey == streamKey {
			return cam.ID, true
		}
	}
	// Legacy global rtmp.stream_keys map (camera_id → stream_key).
	for camID, key := range cm.cfg.RTMP.StreamKeys {
		if key == streamKey {
			return camID, true
		}
	}
	return "", false
}

// SRTStreamConfigs returns a copy of the SRT push parameters (passphrase,
// stream_id) for all SRT cameras. Used by main.go to keep the SRT listener's
// per-stream encryption map in sync with per-camera config. Lock-free read.
func (cm *CameraManager) SRTStreamConfigs() []config.SRTStream {
	s := cm.loadSnapshot()
	out := make([]config.SRTStream, 0, len(s.configs))
	for _, cam := range s.configs {
		if cam.Protocol == string(model.ProtoSRT) {
			out = append(out, config.SRTStream{
				CameraID:   cam.ID,
				Mode:       "listener",
				Passphrase: cam.SRTPassphrase,
				StreamID:   cam.SRTStreamID,
			})
		}
	}
	return out
}

// GetTimelapseMergeMgr returns the timelapse rolling merge manager, or nil if not set.
func (cm *CameraManager) GetTimelapseMergeMgr() *timelapse.RollingMergeManager {
	return cm.timelapseMergeMgr
}

// GetCameraConfig returns the config for the given camera ID, or nil if not found.
// Lock-free read from the immutable snapshot.
func (cm *CameraManager) GetCameraConfig(cameraID string) *config.CameraConfig {
	return cm.snapshotConfig(cameraID)
}

// GetStreamURL returns the source camera's stream URL (e.g. rtsp://...)
// for FFmpeg relay mode. Returns empty string when the camera is not found.
func (cm *CameraManager) GetStreamURL(cameraID string) string {
	// For RTSP cameras, the config URL IS the stream URL.
	cam := cm.GetCameraConfig(cameraID)
	if cam == nil {
		return ""
	}
	if strings.HasPrefix(cam.URL, "rtsp://") {
		return cam.URL
	}
	// For ONVIF cameras, resolve the RTSP URL from the live recorder.
	rec := cm.GetRecorder(cameraID)
	if rec == nil {
		return ""
	}
	if onvifRec, ok := rec.(*recorder.ONVIFRecorder); ok {
		return onvifRec.RTSPURL()
	}
	return ""
}

// RestartRecorder stops and recreates the recorder for the given camera.
// The camera must be enabled. The stop+start is serialized per-camera via
// withCameraLifecycle so it can't race a concurrent restart/stop (which would
// leak a recorder or interleave with a health auto-remediation restart).
func (cm *CameraManager) RestartRecorder(ctx context.Context, cameraID string) error {
	cam := cm.snapshotConfig(cameraID)
	if cam == nil {
		return &model.CameraNotFoundError{CameraID: cameraID}
	}
	camCopy := *cam
	segDur, err := time.ParseDuration(cm.cfg.Storage.SegmentDuration)
	if err != nil {
		segDur = recorder.DefaultSegmentDur
	}
	return cm.withCameraLifecycle(cameraID, func() error {
		// Stop existing recorder (snapshot it, remove from registry via apply,
		// then Stop — rec.Stop can join a goroutine, runs under the per-camera
		// guard but NOT under any registry lock).
		if rec := cm.snapshotRecorder(cameraID); rec != nil {
			cm.apply(func(s *snapshot) *snapshot {
				delete(s.recorders, cameraID)
				return s
			})
			if err := rec.Stop(); err != nil {
				logger.Warn("failed to stop recorder", "camera_id", cameraID, "error", err)
			}
		}
		// Record reconnect attempt
		if cm.metrics != nil {
			cm.metrics.CameraReconnectAttemptsTotal.WithLabelValues(cameraID).Inc()
		}
		// startRecorderLocked registers via apply; safe to call under the guard
		// (it does not re-enter withCameraLifecycle).
		return cm.startRecorderLocked(ctx, camCopy, segDur)
	})
}

// StartCamera manually starts the recorder for the given camera. Serialized
// per-camera so it can't race a concurrent restart/stop.
func (cm *CameraManager) StartCamera(ctx context.Context, cameraID string) error {
	cam := cm.snapshotConfig(cameraID)
	if cam == nil {
		return &model.CameraNotFoundError{CameraID: cameraID}
	}
	camCopy := *cam
	segDur, err := time.ParseDuration(cm.cfg.Storage.SegmentDuration)
	if err != nil {
		segDur = recorder.DefaultSegmentDur
	}
	return cm.withCameraLifecycle(cameraID, func() error {
		// Check if already running — stale recorders (error/stopped) can be restarted
		if rec := cm.snapshotRecorder(cameraID); rec != nil {
			status := rec.Status()
			if status == model.StatusRecording || status == model.StatusReconnecting {
				return &model.CameraAlreadyRunningError{CameraID: cameraID}
			}
			// Stale recorder — stop and remove so we can start fresh
			cm.apply(func(s *snapshot) *snapshot {
				delete(s.recorders, cameraID)
				return s
			})
			if err := rec.Stop(); err != nil {
				logger.Warn("failed to stop stale recorder", "camera_id", cameraID, "error", err)
			}
			if cm.metrics != nil {
				cm.metrics.ActiveCameras.Dec()
			}
		}
		return cm.startRecorderLocked(ctx, camCopy, segDur)
	})
}

// StopCamera manually stops the recorder for the given camera. Serialized
// per-camera so it can't race a concurrent start/restart.
func (cm *CameraManager) StopCamera(_ context.Context, cameraID string) error {
	return cm.withCameraLifecycle(cameraID, func() error {
		rec := cm.snapshotRecorder(cameraID)
		if rec == nil {
			return fmt.Errorf("camera %q not found", cameraID)
		}

		// Remove from registry first (apply), then Stop (under the per-camera
		// guard, not under any registry lock).
		cm.apply(func(s *snapshot) *snapshot {
			delete(s.recorders, cameraID)
			delete(s.hubs, cameraID)
			return s
		})
		if err := rec.Stop(); err != nil {
			logger.Warn("failed to stop recorder", "camera_id", cameraID, "error", err)
		}
		if cm.metrics != nil {
			cm.metrics.ActiveCameras.Dec()
		}
		logger.Info("stopped recorder for camera", "camera_id", cameraID)

		// Stop keyframe extractor if running (snapshot under auxMu, stop outside)
		cm.auxMu.Lock()
		ext, hasExt := cm.keyframeExtractors[cameraID]
		if hasExt {
			delete(cm.keyframeExtractors, cameraID)
		}
		cm.auxMu.Unlock()
		if hasExt && ext != nil {
			if err := ext.Stop(); err != nil {
				logger.Warn("failed to stop keyframe extractor", "camera_id", cameraID, "error", err)
			}
		}

		// Stop frame poller if running
		cm.stopTimelapseFramePoller(cameraID)

		// Stop dual-mode timelapse schedule monitor if running
		cm.stopDualModeTimelapseScheduleMonitor(cameraID)

		return nil
	})
}

// SetProtocolEnabled enables or disables a protocol.
// When disabling, stops all cameras using that protocol.
// When enabling, no auto-start (user starts cameras manually).
func (cm *CameraManager) SetProtocolEnabled(protocol string, enabled bool) {
	if !enabled {
		cm.stopCamerasByProtocol(protocol)
	}
}

func (cm *CameraManager) stopCamerasByProtocol(protocol string) {
	// Snapshot the matching recorders (lock-free), remove each from the
	// registry via apply, then Stop OUTSIDE any lock.
	s := cm.loadSnapshot()
	type stopTarget struct {
		id  string
		rec model.Recorder
	}
	var targets []stopTarget
	for id, rec := range s.recorders {
		if c := s.configs[id]; c != nil && c.Protocol == protocol {
			targets = append(targets, stopTarget{id, rec})
		}
	}
	for _, t := range targets {
		cm.apply(func(st *snapshot) *snapshot {
			delete(st.recorders, t.id)
			return st
		})
		if err := t.rec.Stop(); err != nil {
			logger.Warn("failed to stop recorder", "camera_id", t.id, "error", err)
		}
		if cm.metrics != nil {
			cm.metrics.ActiveCameras.Dec()
		}
		// Stop keyframe extractor if running
		cm.auxMu.Lock()
		ext, ok := cm.keyframeExtractors[t.id]
		if ok {
			delete(cm.keyframeExtractors, t.id)
		}
		cm.auxMu.Unlock()
		if ok && ext != nil {
			if err := ext.Stop(); err != nil {
				logger.Warn("failed to stop keyframe extractor", "camera_id", t.id, "error", err)
			}
		}
		// Stop frame poller if running
		cm.stopTimelapseFramePoller(t.id)
		// Stop dual-mode timelapse schedule monitor
		cm.stopDualModeTimelapseScheduleMonitor(t.id)
	}
}

// classifyError categorizes a connection error into a Prometheus label value.
// Values: "timeout", "auth", "network", "unknown".
// getRecorderHub safely extracts the StreamHub from a recorder using type assertion.
// Returns nil if the recorder does not implement the hubber interface.
func getRecorderHub(rec model.Recorder) *model.StreamHub {
	type hubber interface {
		GetHub() *model.StreamHub
	}
	if h, ok := rec.(hubber); ok {
		return h.GetHub()
	}
	return nil
}

func classifyError(err error) string {
	if err == nil {
		return "unknown"
	}
	msg := err.Error()
	// Check for common error patterns
	switch {
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline"):
		return "timeout"
	case strings.Contains(msg, "401") || strings.Contains(msg, "403") || strings.Contains(msg, "unauthorized") || strings.Contains(msg, "auth"):
		return "auth"
	case strings.Contains(msg, "connection refused") || strings.Contains(msg, "network") || strings.Contains(msg, "dial") || strings.Contains(msg, "no such host"):
		return "network"
	default:
		return "unknown"
	}
}

// autoPopulateSnapshotURL fetches the ONVIF snapshot URI and sets cam.SnapshotURL if empty.
// Runs in a goroutine — manages its own locking to avoid deadlock with callers.
func (cm *CameraManager) autoPopulateSnapshotURL(ctx context.Context, cameraID string) {
	// Use a short-lived context to avoid blocking forever
	fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	client, err := cm.getOrCreateONVIFClient(fetchCtx, cameraID)
	if err != nil {
		logger.Warn("failed to get ONVIF client for snapshot URL", "camera_id", cameraID, "error", err)
		return
	}

	profiles, err := client.GetProfiles(fetchCtx)
	if err != nil {
		logger.Warn("failed to get profiles for snapshot URL", "camera_id", cameraID, "error", err)
		return
	}
	if len(profiles) == 0 {
		logger.Warn("no profiles found for snapshot URL", "camera_id", cameraID)
		return
	}

	provider := client.NewSnapshotProvider(profiles[0].Token)
	if provider == nil {
		logger.Warn("failed to create snapshot provider", "camera_id", cameraID)
		return
	}

	uri, err := provider.GetSnapshotUri(fetchCtx)
	if err != nil {
		logger.Warn("failed to get snapshot URI from ONVIF device", "camera_id", cameraID, "error", err)
		return
	}

	// Update SnapshotURL under configMu (cfg.Cameras is the mutable config slice).
	// The snapshot's configs[id] points into cfg.Cameras, so the new value is
	// visible through the existing pointer without republishing the snapshot.
	cm.configMu.Lock()
	for i := range cm.cfg.Cameras {
		if cm.cfg.Cameras[i].ID == cameraID && cm.cfg.Cameras[i].SnapshotURL == "" {
			cm.cfg.Cameras[i].SnapshotURL = uri
			break
		}
	}
	if err := cm.persistConfig(); err != nil {
		logger.Warn("failed to persist snapshot URL", "camera_id", cameraID, "error", err)
	}
	cm.configMu.Unlock()

	logger.Info("auto-populated snapshot URL from ONVIF device", "camera_id", cameraID, "url", uri)
}
