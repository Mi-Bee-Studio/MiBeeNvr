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
	recorders          map[string]model.Recorder // camera_id → Recorder
	metrics            *metrics.Metrics
	mergeMgr           *merge.MergeManager                     // segment merge manager (nil = no merge)
	timelapseMergeMgr  *timelapse.RollingMergeManager          // timelapse rolling merge (nil = no merge)
	transcodeMgr       *transcoding.TranscodeManager           // transcoding manager (nil = no transcoding)
	healthMgr          *health.Manager                         // health monitoring (nil when disabled)
	scheduler          *timelapse.Scheduler                    // timelapse schedule evaluator
	scheduleMonitors   map[string]context.CancelFunc           // camera_id -> cancel func for schedule monitor
	keyframeExtractors map[string]*timelapse.KeyframeExtractor // camera_id -> keyframe extractor (H.264/H.265)
	framePollers       map[string]*timelapse.SnapshotCapturer  // camera_id -> frame poller (MJPEG/JPEG latest_frame)
	mu                 sync.RWMutex
	onvifClients       map[string]*onvif.Client            // camera_id → cached ONVIF client
	onvifMu            sync.Mutex                          // protects onvifClients
	errorDetails       map[string]*model.CameraErrorDetail // cameraID → latest error detail
	// failedStartCameras tracks cameras whose recorder failed to start (e.g. the
	// camera's IP changed and the configured endpoint is unreachable). These
	// cameras are NOT in cm.recorders (startRecorder deletes them on failure), so
	// without this tracking they would be invisible to the health manager's status
	// loop → never auto-remediated → never rediscovered. statusFunc exposes them
	// as StatusError so the existing CheckAll→restart→blacklist→rediscovery chain
	// can self-heal them. Cleared on successful (re)start.
	failedStartCameras map[string]error                 // cameraID → startup failure reason
	eventSubscribers   map[string]onvif.EventSubscriber // camera_id → event subscriber
	deviceInfoCache    map[string]*onvif.DeviceInfo     // camera_id → cached device info
	deviceInfoMu       sync.RWMutex                     // protects deviceInfoCache
	frameSampleCounter uint64                           // atomic: 1/100 sampling for frame processing duration
	eventBus           *event.EventBus                  // event bus for publishing segment events
	// hubRegistry is the central map of camera_id → StreamHub. It is the single
	// source of truth for hubs so that pull recorders (RTSP/ONVIF/...) and push
	// ingest servers (SRT listener / RTMP server) share the SAME hub object for
	// a given camera. The SRT/RTMP servers consult GetOrCreateHub(); the
	// recorder's own .Hub field points at the same instance after initStreamHub.
	hubRegistry map[string]*model.StreamHub
	// relayMgr (optional) is notified when a camera's push-out targets change so
	// the relay engine can reconcile. Interface-typed to avoid a camera<->relay
	// import cycle.
	relayMgr RelayManager
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
	return &CameraManager{
		cfg:                cfg,
		store:              store,
		db:                 db,
		configPath:         configPath,
		recorders:          make(map[string]model.Recorder),
		metrics:            m,
		mergeMgr:           mm,
		transcodeMgr:       tm,
		timelapseMergeMgr:  tmm,
		scheduler:          timelapse.NewScheduler(appLoc),
		scheduleMonitors:   make(map[string]context.CancelFunc),
		keyframeExtractors: make(map[string]*timelapse.KeyframeExtractor),
		framePollers:       make(map[string]*timelapse.SnapshotCapturer),
		errorDetails:       make(map[string]*model.CameraErrorDetail),
		failedStartCameras: make(map[string]error),
		onvifClients:       make(map[string]*onvif.Client),
		eventSubscribers:   make(map[string]onvif.EventSubscriber),
		deviceInfoCache:    make(map[string]*onvif.DeviceInfo),
		eventBus:           eb,
		hubRegistry:        make(map[string]*model.StreamHub),
	}
}

// SetHealthManager sets the health manager for camera health monitoring.
// Can be called with nil to disable health monitoring.
// CameraCount returns the number of configured cameras (O(1), no DB query).
// Used by stats endpoints that only need the count, avoiding a redundant
// ListCameras DB round-trip per request.
func (cm *CameraManager) CameraCount() int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return len(cm.cfg.Cameras)
}

func (cm *CameraManager) SetHealthManager(m *health.Manager) {
	cm.healthMgr = m
	if m != nil {
		m.SetStatusFunc(cm.statusSnapshot)
	}
}

// statusSnapshot returns the current status of every camera the manager knows
// about, for consumption by the health manager's periodic loop. It merges two
// sources:
//   - cm.recorders: active recorders report their real Status().
//   - cm.failedStartCameras: cameras whose recorder failed to start (e.g. ONVIF
//     endpoint unreachable after an IP change). These are NOT in cm.recorders
//     (startRecorder deletes them on failure), so without surfacing them here
//     they would be invisible to the health loop → never auto-remediated → never
//     rediscovered. They are reported as StatusError so the existing
//     CheckAll → restart → blacklist → rediscovery chain can self-heal them.
//
// A camera present in BOTH maps (stale failed-start entry + live recorder) is
// dominated by its real recorder status.
func (cm *CameraManager) statusSnapshot() map[string]string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	result := make(map[string]string, len(cm.recorders)+len(cm.failedStartCameras))
	for id, rec := range cm.recorders {
		result[id] = string(rec.Status())
	}
	for id := range cm.failedStartCameras {
		if _, exists := result[id]; !exists {
			result[id] = string(model.StatusError)
		}
	}
	return result
}

// markStartFailed records a camera whose recorder failed to start, so that
// statusFunc can surface it to the health manager as StatusError. This is the
// entry point that connects startup failures to the auto-remediation → IP
// rediscovery self-healing chain. Caller must NOT hold cm.mu.
func (cm *CameraManager) markStartFailed(cameraID string, err error) {
	cm.mu.Lock()
	cm.failedStartCameras[cameraID] = err
	cm.mu.Unlock()
}

// clearStartFailed removes a camera from the failed-start tracking. Called on
// successful (re)start so the camera transitions to normal health monitoring.
// Caller must NOT hold cm.mu.
func (cm *CameraManager) clearStartFailed(cameraID string) {
	cm.mu.Lock()
	delete(cm.failedStartCameras, cameraID)
	cm.mu.Unlock()
}

// SetTranscodeManager sets the transcoding manager for post-recording enqueue.
// Can be called with nil to disable transcoding. Thread-safe.
func (cm *CameraManager) SetTranscodeManager(m *transcoding.TranscodeManager) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.transcodeMgr = m
}

// EnqueueTranscode checks per-camera transcoding config and enqueues a
// transcoding task if enabled. Non-blocking — runs the enqueue in a goroutine.
func (cm *CameraManager) EnqueueTranscode(cameraID, recordingID, inputPath, inputFormat string) {
	cm.mu.RLock()
	tm := cm.transcodeMgr
	cm.mu.RUnlock()

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
		if err := cm.db.UpsertCamera(ctx, cam.ID, cam.Name, string(cam.Protocol), cam.Encoding, cam.URL, cam.Username, cam.Password, cam.ONVIFEndpoint, cam.ProfileToken, cam.StreamEncoding); err != nil {
			logger.Error("failed to insert camera record", "camera_id", cam.ID, "error", err)
		} else {
			logger.Info("inserted camera record", "camera_id", cam.ID)
		}

		switch cam.Protocol {
		case string(model.ProtoRTSP), string(model.ProtoHTTP):
			rec := cm.createRecorder(cam, segDur)
			if rec != nil {
				cm.mu.Lock()
				cm.recorders[cam.ID] = rec
				cm.mu.Unlock()
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
								cm.mu.Lock()
								cm.framePollers[cam.ID] = poller
								cm.mu.Unlock()
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
							cm.mu.Lock()
							cm.framePollers[cam.ID] = poller
							cm.mu.Unlock()
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
	// Run in a goroutine per camera: SetCameraTargets calls GetHub which re-locks
	// cm.mu (RLock), and re-entering under a held Lock would self-deadlock.
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
	return nil
}

// Stop stops all running recorders and waits for them to complete.
func (cm *CameraManager) Stop() error {
	cm.mu.RLock()
	recs := make([]model.Recorder, 0, len(cm.recorders))
	for _, rec := range cm.recorders {
		recs = append(recs, rec)
	}
	cm.mu.RUnlock()

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
	cm.mu.Lock()
	for _, cancel := range cm.scheduleMonitors {
		cancel()
	}
	cm.scheduleMonitors = make(map[string]context.CancelFunc)
	cm.mu.Unlock()

	// Stop all timelapse keyframe extractors
	cm.stopAllTimelapseKeyframeExtractors()

	// Stop all timelapse frame pollers
	cm.stopAllTimelapseFramePollers()

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
func (cm *CameraManager) GetOrCreateHub(cameraID string) *model.StreamHub {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if hub, ok := cm.hubRegistry[cameraID]; ok {
		return hub
	}
	hub := model.NewStreamHub()
	hub.SetCameraID(cameraID)
	// Wire the same observability callbacks as initStreamHub so push hubs are
	// instrumented identically to pull hubs.
	cm.wireHubMetricsLocked(hub, cameraID, string(model.ProtoSRT))
	cm.hubRegistry[cameraID] = hub
	return hub
}

// wireHubMetricsLocked attaches the standard StreamHub observability callbacks
// (frame counters, drop counters, buffer-depth gauges). Caller must hold cm.mu.
func (cm *CameraManager) wireHubMetricsLocked(hub *model.StreamHub, cameraID, protocol string) {
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
func (cm *CameraManager) RTMPKeyMap() map[string]string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	out := make(map[string]string, len(cm.cfg.Cameras))
	for _, cam := range cm.cfg.Cameras {
		if cam.Protocol == string(model.ProtoRTMP) && cam.StreamKey != "" {
			out[cam.ID] = cam.StreamKey
		}
	}
	return out
}

// ResolveStreamKey maps an incoming RTMP stream key to its camera ID (with the
// legacy global rtmp.stream_keys map as a fallback). This is the LIVE resolver
// used by the RTMP server on every publisher connect — it reflects cameras added
// at runtime, unlike a snapshot built once at startup.
func (cm *CameraManager) ResolveStreamKey(streamKey string) (cameraID string, ok bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	// Per-camera stream_key fields take precedence.
	for _, cam := range cm.cfg.Cameras {
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
// per-stream encryption map in sync with per-camera config.
func (cm *CameraManager) SRTStreamConfigs() []config.SRTStream {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	out := make([]config.SRTStream, 0, len(cm.cfg.Cameras))
	for _, cam := range cm.cfg.Cameras {
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
func (cm *CameraManager) GetCameraConfig(cameraID string) *config.CameraConfig {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	for i := range cm.cfg.Cameras {
		if cm.cfg.Cameras[i].ID == cameraID {
			return &cm.cfg.Cameras[i]
		}
	}
	return nil
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
// The camera must be enabled.
func (cm *CameraManager) RestartRecorder(ctx context.Context, cameraID string) error {
	cm.mu.Lock()

	// Find camera config
	var cam *config.CameraConfig
	for i := range cm.cfg.Cameras {
		if cm.cfg.Cameras[i].ID == cameraID {
			cam = &cm.cfg.Cameras[i]
			break
		}
	}
	if cam == nil {
		cm.mu.Unlock()
		return &model.CameraNotFoundError{CameraID: cameraID}
	}

	// Stop existing recorder
	if rec, ok := cm.recorders[cameraID]; ok {
		if err := rec.Stop(); err != nil {
			logger.Warn("failed to stop recorder", "camera_id", cameraID, "error", err)
		}
		delete(cm.recorders, cameraID)
	}
	// Record reconnect attempt
	if cm.metrics != nil {
		cm.metrics.CameraReconnectAttemptsTotal.WithLabelValues(cameraID).Inc()
	}

	// Snapshot the config + segDur so we can start WITHOUT holding cm.mu.
	// startRecorder's timelapse sub-helpers (startTimelapseKeyframeExtractor etc.)
	// acquire cm.mu themselves, so re-entering under a held Lock would self-deadlock.
	camCopy := *cam
	segDur, err := time.ParseDuration(cm.cfg.Storage.SegmentDuration)
	cm.mu.Unlock()
	if err != nil {
		segDur = recorder.DefaultSegmentDur
	}
	return cm.startRecorder(ctx, camCopy, segDur)
}

// StartCamera manually starts the recorder for the given camera.
func (cm *CameraManager) StartCamera(ctx context.Context, cameraID string) error {
	cm.mu.Lock()

	// Find camera config
	var cam *config.CameraConfig
	for i := range cm.cfg.Cameras {
		if cm.cfg.Cameras[i].ID == cameraID {
			cam = &cm.cfg.Cameras[i]
			break
		}
	}
	if cam == nil {
		cm.mu.Unlock()
		return &model.CameraNotFoundError{CameraID: cameraID}
	}

	// Check if already running — stale recorders (error/stopped) can be restarted
	if rec, ok := cm.recorders[cameraID]; ok {
		status := rec.Status()
		if status == model.StatusRecording || status == model.StatusReconnecting {
			cm.mu.Unlock()
			return &model.CameraAlreadyRunningError{CameraID: cameraID}
		}
		// Stale recorder — stop and remove so we can start fresh
		if err := rec.Stop(); err != nil {
			logger.Warn("failed to stop stale recorder", "camera_id", cameraID, "error", err)
		}
		delete(cm.recorders, cameraID)
		if cm.metrics != nil {
			cm.metrics.ActiveCameras.Dec()
		}
	}

	// Snapshot config + segDur, then release the lock before startRecorder
	// (its timelapse sub-helpers acquire cm.mu themselves — re-entering under a
	// held Lock would self-deadlock).
	camCopy := *cam
	segDur, err := time.ParseDuration(cm.cfg.Storage.SegmentDuration)
	cm.mu.Unlock()
	if err != nil {
		segDur = recorder.DefaultSegmentDur
	}
	return cm.startRecorder(ctx, camCopy, segDur)
}

// StopCamera manually stops the recorder for the given camera.
func (cm *CameraManager) StopCamera(_ context.Context, cameraID string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	rec, ok := cm.recorders[cameraID]
	if !ok {
		return fmt.Errorf("camera %q not found", cameraID)
	}

	if err := rec.Stop(); err != nil {
		logger.Warn("failed to stop recorder", "camera_id", cameraID, "error", err)
	}
	delete(cm.recorders, cameraID)
	delete(cm.hubRegistry, cameraID)
	if cm.metrics != nil {
		cm.metrics.ActiveCameras.Dec()
	}
	logger.Info("stopped recorder for camera", "camera_id", cameraID)

	// Stop keyframe extractor if running
	if ext, ok := cm.keyframeExtractors[cameraID]; ok {
		delete(cm.keyframeExtractors, cameraID)
		if err := ext.Stop(); err != nil {
			logger.Warn("failed to stop keyframe extractor", "camera_id", cameraID, "error", err)
		}
	}

	// Stop frame poller if running
	cm.stopTimelapseFramePoller(cameraID)

	// Stop dual-mode timelapse schedule monitor if running
	cm.stopDualModeTimelapseScheduleMonitor(cameraID)

	return nil
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
	cm.mu.Lock()
	defer cm.mu.Unlock()
	for id, rec := range cm.recorders {
		var camProtocol string
		for _, cam := range cm.cfg.Cameras {
			if cam.ID == id {
				camProtocol = cam.Protocol
				break
			}
		}
		if camProtocol == protocol {
			if err := rec.Stop(); err != nil {
				logger.Warn("failed to stop recorder", "camera_id", id, "error", err)
			}
			delete(cm.recorders, id)
			if cm.metrics != nil {
				cm.metrics.ActiveCameras.Dec()
			}
			// Stop keyframe extractor if running
			if ext, ok := cm.keyframeExtractors[id]; ok {
				delete(cm.keyframeExtractors, id)
				if err := ext.Stop(); err != nil {
					logger.Warn("failed to stop keyframe extractor", "camera_id", id, "error", err)
				}
			}
			// Stop frame poller if running (caller holds cm.mu)
			cm.stopTimelapseFramePoller(id)
			// Stop dual-mode timelapse schedule monitor
			cm.stopDualModeTimelapseScheduleMonitor(id)
		}
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

	// Update SnapshotURL under write lock
	cm.mu.Lock()
	for i := range cm.cfg.Cameras {
		if cm.cfg.Cameras[i].ID == cameraID && cm.cfg.Cameras[i].SnapshotURL == "" {
			cm.cfg.Cameras[i].SnapshotURL = uri
			break
		}
	}
	if err := cm.persistConfig(); err != nil {
		logger.Warn("failed to persist snapshot URL", "camera_id", cameraID, "error", err)
	}
	cm.mu.Unlock()

	logger.Info("auto-populated snapshot URL from ONVIF device", "camera_id", cameraID, "url", uri)
}
