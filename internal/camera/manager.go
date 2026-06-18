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
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/xiaomi"
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
	// Push/ingest camera fields (SRT/RTMP). nil = unchanged.
	StreamKey     *string
	SRTPassphrase *string
	SRTStreamID   *string
	// Push-out relay targets (replace the whole list when set). nil = unchanged.
	PushTargets       *[]config.PushTargetConfig
	PushRetentionDays *int
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
	keyframeExtractors map[string]*timelapse.KeyframeExtractor // camera_id -> keyframe extractor
	mu                 sync.RWMutex
	onvifClients       map[string]*onvif.Client            // camera_id → cached ONVIF client
	onvifMu            sync.Mutex                          // protects onvifClients
	errorDetails       map[string]*model.CameraErrorDetail // cameraID → latest error detail
	eventSubscribers   map[string]onvif.EventSubscriber    // camera_id → event subscriber
	deviceInfoCache    map[string]*onvif.DeviceInfo        // camera_id → cached device info
	deviceInfoMu       sync.RWMutex                        // protects deviceInfoCache
	frameSampleCounter uint64                              // atomic: 1/100 sampling for frame processing duration
	eventBus           *event.EventBus                     // event bus for publishing segment events
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

func NewCameraManager(cfg *config.Config, store *storage.Manager, db *storage.DB, configPath string, opts ...interface{}) *CameraManager {
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
		errorDetails:       make(map[string]*model.CameraErrorDetail),
		onvifClients:       make(map[string]*onvif.Client),
		eventSubscribers:   make(map[string]onvif.EventSubscriber),
		deviceInfoCache:    make(map[string]*onvif.DeviceInfo),
		eventBus:           eb,
		hubRegistry:        make(map[string]*model.StreamHub),
	}
}

// SetHealthManager sets the health manager for camera health monitoring.
// Can be called with nil to disable health monitoring.
func (cm *CameraManager) SetHealthManager(m *health.Manager) {
	cm.healthMgr = m
	if m != nil {
		m.SetStatusFunc(func() map[string]string {
			cm.mu.RLock()
			defer cm.mu.RUnlock()
			result := make(map[string]string, len(cm.recorders))
			for id, rec := range cm.recorders {
				result[id] = string(rec.Status())
			}
			return result
		})
	}
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

// createRecorder creates a recorder for the given camera config.
// Returns nil for unknown protocols.
func (cm *CameraManager) createRecorder(cam config.CameraConfig, segDur time.Duration) model.Recorder {
	var rec model.Recorder
	switch cam.Protocol {
	case "xiaomi":
		rec = new(xiaomi.XiaomiPlugin).NewRecorder(cam, cm.store, cm.db, cm.metrics)
		// Wire ErrorReporter for TUTK vendor error detection
		if xr, ok := rec.(*xiaomi.XiaomiRecorder); ok {
			xr.SetErrorReporter(cm)
		}
	case string(model.ProtoRTSP):
		switch cam.Encoding {
		case string(model.FormatH264):
			h264Cfg := recorder.H264Config{
				CameraID:     cam.ID,
				RTSPURL:      cam.URL,
				Username:     cam.Username,
				Password:     cam.Password,
				SegmentDur:   segDur,
				DB:           cm.db,
				AudioEnabled: cam.AudioEnabled,
			}
			if d, err := time.ParseDuration(cam.FrameWatchdogTimeout); err == nil && d > 0 {
				h264Cfg.FrameWatchdogTimeout = d
			}
			rec = recorder.NewH264Recorder(h264Cfg, cm.store, cm.metrics)
		case string(model.FormatH265):
			h265Cfg := recorder.H265Config{
				CameraID:     cam.ID,
				RTSPURL:      cam.URL,
				Username:     cam.Username,
				Password:     cam.Password,
				SegmentDur:   segDur,
				DB:           cm.db,
				AudioEnabled: cam.AudioEnabled,
			}
			if d, err := time.ParseDuration(cam.FrameWatchdogTimeout); err == nil && d > 0 {
				h265Cfg.FrameWatchdogTimeout = d
			}
			rec = recorder.NewH265Recorder(h265Cfg, cm.store, cm.metrics)
		case string(model.FormatMJPEG):
			mjpegCfg := recorder.MJPEGConfig{
				CameraID:       cam.ID,
				RTSPURL:        cam.URL,
				SegmentDur:     segDur,
				SampleInterval: cam.SampleInterval,
				DB:             cm.db,
			}
			rec = recorder.NewMJPEGRecorder(mjpegCfg, cm.store, cm.metrics)
		default:
			return nil
		}
	case string(model.ProtoHTTP):
		if cam.Encoding != string(model.EncJPEG) {
			return nil
		}
		httpJpegCfg := recorder.HTTPJPEGConfig{
			CameraID:   cam.ID,
			URL:        cam.URL,
			SegmentDur: segDur,
			Username:   cam.Username,
			Password:   cam.Password,
			DB:         cm.db,
		}
		rec = recorder.NewHTTPJPEGRecorder(httpJpegCfg, cm.store, cm.metrics)
	case string(model.ProtoONVIF):
		onvifEndpoint := cam.ONVIFEndpoint
		if onvifEndpoint == "" {
			onvifEndpoint = cam.URL
		}
		onvifClient := onvif.NewClient(onvifEndpoint, cam.Username, cam.Password)
		onvifCfg := recorder.ONVIFConfig{
			CameraID:       cam.ID,
			ProfileToken:   cam.ProfileToken,
			StreamEncoding: cam.StreamEncoding,
			Username:       cam.Username,
			Password:       cam.Password,
			SegmentDur:     segDur,
			DB:             cm.db,
			AudioEnabled:   cam.AudioEnabled,
			ONVIFEndpoint:  onvifEndpoint,
			EventBus:       cm.eventBus,
		}
		if d, err := time.ParseDuration(cam.FrameWatchdogTimeout); err == nil && d > 0 {
			onvifCfg.FrameWatchdogTimeout = d
		}
		rec = recorder.NewONVIFRecorder(onvifCfg, onvifClient, cm.store, cm.metrics)
	case "timelapse":
		frameSource := "auto"
		if cam.Timelapse != nil && cam.Timelapse.FrameSource != "" {
			frameSource = cam.Timelapse.FrameSource
		}
		if cam.Timelapse == nil || !cam.Timelapse.Enabled {
			return nil
		}
		switch frameSource {
		case "snapshot":
			rec = cm.createTimelapseSnapshotRecorder(cam, segDur)
		case "rtsp_keyframe":
			// For standalone timelapse with rtsp_keyframe, create an RTSP recorder
			// to provide frames for keyframe extraction.
			switch cam.Encoding {
			case "h264", "":
				h264Cfg := recorder.H264Config{
					CameraID:     cam.ID,
					RTSPURL:      cam.URL,
					Username:     cam.Username,
					Password:     cam.Password,
					SegmentDur:   segDur,
					DB:           cm.db,
					AudioEnabled: cam.AudioEnabled,
				}
				if d, err := time.ParseDuration(cam.FrameWatchdogTimeout); err == nil && d > 0 {
					h264Cfg.FrameWatchdogTimeout = d
				}
				rec = recorder.NewH264Recorder(h264Cfg, cm.store, cm.metrics)
			case "h265":
				h265Cfg := recorder.H265Config{
					CameraID:     cam.ID,
					RTSPURL:      cam.URL,
					Username:     cam.Username,
					Password:     cam.Password,
					SegmentDur:   segDur,
					DB:           cm.db,
					AudioEnabled: cam.AudioEnabled,
				}
 
				if d, err := time.ParseDuration(cam.FrameWatchdogTimeout); err == nil && d > 0 {
					h265Cfg.FrameWatchdogTimeout = d
				}
				rec = recorder.NewH265Recorder(h265Cfg, cm.store, cm.metrics)
			default:
				logger.Warn("unsupported encoding for rtsp_keyframe timelapse frame source", "camera_id", cam.ID, "encoding", cam.Encoding)
				return nil
			}
		case "mjpeg", "auto", "":
			rec = cm.createTimelapseMJPEGRecorder(cam, segDur)
		default:
			logger.Warn("unknown timelapse frame source", "camera_id", cam.ID, "frame_source", frameSource)
			return nil
		}
	case string(model.ProtoSRT), string(model.ProtoRTMP):
		// Push/ingest protocols: the NVR does NOT dial out. A remote publisher
		// (ffmpeg, OBS, another NVR) pushes a stream into the SRT listener or
		// RTMP server; frames arrive via IngestRecorder.WriteNALU. RTMP is
		// H.264 only; SRT config-layer accepts h265 but the current MPEG-TS
		// demux emits H.264, so we force h264 here.
		enc := cam.Encoding
		if enc == "" {
			enc = string(model.FormatH264)
		}
		rec = recorder.NewIngestRecorder(recorder.IngestConfig{
			CameraID:   cam.ID,
			Encoding:   enc,
			SegmentDur: segDur,
			Store:      cm.store,
			DB:         cm.db,
			Metrics:    cm.metrics,
			EventBus:   cm.eventBus,
		})
	default:
		return nil
	}

	// Initialize StreamHub for frame fan-out on all recorders
	initStreamHub(rec, cam.ID, cam.Protocol, &cm.frameSampleCounter, cm.metrics)
	// Register the recorder's hub in the central registry so that push ingest
	// servers (SRT listener / RTMP server) share the SAME hub object and their
	// frames reach the live consumers (HLS/WebRTC/FLV/WS) attached on demand.
	// NOTE: createRecorder is always called under cm.mu (from startRecorder /
	// Start), so the registry write is lock-free here — re-locking would
	// self-deadlock (Go mutexes are not reentrant).
	if hub := getRecorderHub(rec); hub != nil {
		cm.hubRegistry[cam.ID] = hub
	}
	return rec
}

// initStreamHub sets a new StreamHub on the recorder if it has a Hub field.
// It also sets the cameraID for structured logging and wires up the OnBroadcast callback.
func initStreamHub(rec model.Recorder, cameraID string, protocol string, sampleCounter *uint64, m *metrics.Metrics) {
	var hub *model.StreamHub
	switch r := rec.(type) {
	case *recorder.H264Recorder:
		hub = model.NewStreamHub()
		r.Hub = hub
	case *recorder.H265Recorder:
		hub = model.NewStreamHub()
		r.Hub = hub
	case *recorder.ONVIFRecorder:
		hub = model.NewStreamHub()
		r.Hub = hub
	case *recorder.MJPEGRecorder:
		hub = model.NewStreamHub()
		r.Hub = hub
	case *recorder.HTTPJPEGRecorder:
		hub = model.NewStreamHub()
		r.Hub = hub
	case *xiaomi.XiaomiRecorder:
		hub = model.NewStreamHub()
		r.Hub = hub
	case *recorder.TimelapseRecorder:
		hub = model.NewStreamHub()
		r.Hub = hub
	case *recorder.StubRecorder:
		hub = model.NewStreamHub()
		r.Hub = hub
	case *recorder.IngestRecorder:
		hub = model.NewStreamHub()
		r.Hub = hub
	}
	if hub != nil {
		hub.SetCameraID(cameraID)
		if m != nil {
			hub.OnBroadcast = func(cid string, isIDR bool) {
				m.StreamHubFramesInTotal.WithLabelValues(cid).Inc()

				// 1/100 sampling: measure frame processing duration
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
	}
}

// startRecorder creates and starts a recorder for the given camera config.
// The caller must hold cm.mu (or at least a write lock) if cm.recorders is being modified.
// If the recorder is created, it will be registered in cm.recorders.
func (cm *CameraManager) startRecorder(ctx context.Context, cam config.CameraConfig, segDur time.Duration) error {
	rec := cm.createRecorder(cam, segDur)
	if rec == nil {
		return fmt.Errorf("camera %q: protocol %q does not support recording", cam.ID, cam.Protocol)
	}
	cm.recorders[cam.ID] = rec

	// For timelapse recorders, check scheduler before starting
	if cam.Protocol == "timelapse" && cam.Timelapse != nil {
		if !cm.scheduler.IsRecordingTime(*cam.Timelapse) {
			logger.Info("timelapse schedule: not recording time, delaying start", "camera_id", cam.ID)
			// Start schedule monitor anyway so it can start the recorder when the schedule says so
			cm.startTimelapseScheduleMonitor(ctx, cam.ID, rec, *cam.Timelapse)
			return nil
		}
	}

	// Recorders derive their run context from context.Background() internally,
	// so their lifecycle is independent of this ctx (e.g. HTTP request context).
	// The ctx is only used for short initial setup (e.g. ONVIF device probe).
	if err := rec.Start(ctx); err != nil {
		delete(cm.recorders, cam.ID)
		// Record connection error metric
		if cm.metrics != nil {
			cm.metrics.CameraConnectionErrorsTotal.WithLabelValues(cam.ID, classifyError(err)).Inc()
		}
		return fmt.Errorf("camera %q: failed to start recorder: %w", cam.ID, err)
	}

	// Start schedule monitor for timelapse recorders
	if cam.Protocol == "timelapse" && cam.Timelapse != nil {
		cm.startTimelapseScheduleMonitor(ctx, cam.ID, rec, *cam.Timelapse)
	}

	// Start keyframe extractor for recorders with rtsp_keyframe timelapse config
	if effectiveDualModeFrameSource(cam) == "rtsp_keyframe" {
		if hub := getRecorderHub(rec); hub != nil {
			if err := cm.startTimelapseKeyframeExtractor(cam.ID, cam, hub, rec); err != nil {
				logger.Error("failed to start keyframe extractor", "camera_id", cam.ID, "error", err)
			}
		}
	}

	cm.errorDetails[cam.ID] = nil
	if cm.metrics != nil {
		cm.metrics.ActiveCameras.Inc()
	}
	// Notify health manager of new camera with per-camera overrides
	var overrides *config.ResolvedHealthOverrides
	if cm.cfg.Health.Enabled {
		resolved := config.ResolveHealthOverrides(cm.cfg.Health, cam.HealthOverrides)
		overrides = &resolved
	}
	cm.healthMgr.OnCameraAdded(cam.ID, rec, overrides)
	logger.Info("started recorder for camera", "camera_id", cam.ID)
	return nil
}

// persistConfig saves the current config to disk if configPath is set.
func (cm *CameraManager) persistConfig() error {
	if cm.configPath != "" {
		if err := config.Save(cm.configPath, cm.cfg); err != nil {
			return fmt.Errorf("camera manager: failed to save config: %w", err)
		}
	}
	return nil
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
						if hub := getRecorderHub(rec); hub != nil {
							if err := cm.startTimelapseKeyframeExtractor(cam.ID, cam, hub, rec); err != nil {
								logger.Error("failed to start keyframe extractor", "camera_id", cam.ID, "error", err)
							}
						}
					}
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

	return nil
}

// Status returns the status of all managed recorders.
func (cm *CameraManager) Status() map[string]model.RecorderStatus {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	result := make(map[string]model.RecorderStatus, len(cm.recorders))
	for id, rec := range cm.recorders {
		result[id] = rec.Status()
	}
	return result
}

// CameraStatus returns the status of a single camera recorder.
func (cm *CameraManager) CameraStatus(cameraID string) model.RecorderStatus {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	rec, ok := cm.recorders[cameraID]
	if !ok {
		return model.StatusError
	}
	return rec.Status()
}

// SetErrorDetail sets the error detail for a camera. Thread-safe.
func (cm *CameraManager) SetErrorDetail(cameraID string, detail *model.CameraErrorDetail) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.errorDetails[cameraID] = detail
}

// GetErrorDetail returns the error detail for a camera, or nil if none. Thread-safe.
func (cm *CameraManager) GetErrorDetail(cameraID string) *model.CameraErrorDetail {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.errorDetails[cameraID]
}

// RecorderCount returns the number of managed recorders.
func (cm *CameraManager) RecorderCount() int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return len(cm.recorders)
}

// GetRecorder returns the recorder for the given camera ID, or nil if not found.
func (cm *CameraManager) GetRecorder(cameraID string) model.Recorder {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.recorders[cameraID]
}

// GetHub returns the StreamHub registered for the given camera ID, or nil if
// none exists. This is the read-only lookup consumed by HLS/WebRTC/FLV/WS
// handlers (they fall back to getRecorderHub, but push-only cameras — srt/rtmp —
// expose their hub through this registry).
func (cm *CameraManager) GetHub(cameraID string) *model.StreamHub {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.hubRegistry[cameraID]
}

// SetRelayManager wires the relay engine (optional). When set, the camera
// manager reconciles a camera's push-out targets on Add/Update/Remove.
func (cm *CameraManager) SetRelayManager(rm RelayManager) {
	cm.relayMgr = rm
}

// RelayStatus returns the runtime status of a camera's push-out targets, for the
// push-status API and the camera card UI. Empty when no relay manager is wired.
// RelayStatusProvider is the minimal shape the API handler needs.
func (cm *CameraManager) RelayStatus(cameraID string) []interface{} {
	if rm, ok := cm.relayMgr.(relayStatusProvider); ok {
		return rm.CameraStatusJSON(cameraID)
	}
	return nil
}

// relayStatusProvider is implemented by *relay.Manager to return JSON-serializable
// status without internal/camera importing internal/relay.
type relayStatusProvider interface {
	CameraStatusJSON(cameraID string) []interface{}
}

// GetSPS returns the source camera's current H.264 SPS/PPS (raw NALUs, no start
// code) and whether the source is H.264. Used by the relay engine to initialize
// RTMP/RTSP target tracks. Returns nil when the camera is not yet streaming or
// is not H.264.
func (cm *CameraManager) GetSPS(cameraID string) (sps, pps []byte, isH264 bool) {
	rec := cm.GetRecorder(cameraID)
	if rec == nil {
		return nil, nil, false
	}
	switch r := rec.(type) {
	case *recorder.H264Recorder:
		return r.SPS(), r.PPS(), true
	case *recorder.IngestRecorder:
		_, s, p, _ := r.CodecParams()
		if s == nil || p == nil {
			return nil, nil, true
		}
		return s, p, true
	case *recorder.ONVIFRecorder:
		// Delegate may be nil momentarily; unwrap via CodecParams if available.
		if d := r.Delegate(); d != nil {
			if h264, ok := d.(*recorder.H264Recorder); ok {
				return h264.SPS(), h264.PPS(), true
			}
		}
	}
	return nil, nil, false
}

// GetCodecInfo returns the source camera's current video and audio codec
// parameters as a single CodecInfo struct. Used by the relay engine to
// initialize target tracks with complete codec information.
// Returns a zero-value CodecInfo when the camera is not found or not streaming.
func (cm *CameraManager) GetCodecInfo(cameraID string) model.CodecInfo {
	rec := cm.GetRecorder(cameraID)
	if rec == nil {
		return model.CodecInfo{}
	}

	// Helper to build CodecInfo from audio-getter interface.
	type audioInfo interface {
		AudioCodec() string
		AudioConfig() []byte
		AudioSampleRate() int
		AudioChannels() int
	}

	// Try to extract SPS/PPS and audio info from the concrete recorder.
	switch r := rec.(type) {
	case *recorder.H264Recorder:
		ci := model.CodecInfo{
			SPS:    r.SPS(),
			PPS:    r.PPS(),
			IsH264: true,
		}
		if ai, ok := rec.(audioInfo); ok {
			ci.AudioCodec = ai.AudioCodec()
			ci.AudioConfig = ai.AudioConfig()
			ci.AudioSampleRate = ai.AudioSampleRate()
			ci.AudioChannels = ai.AudioChannels()
		}
		return ci

	case *recorder.H265Recorder:
		ci := model.CodecInfo{
			VPS: r.VPS(),
			SPS: r.SPS(),
			PPS: r.PPS(),
		}
		if ai, ok := rec.(audioInfo); ok {
			ci.AudioCodec = ai.AudioCodec()
			ci.AudioConfig = ai.AudioConfig()
			ci.AudioSampleRate = ai.AudioSampleRate()
			ci.AudioChannels = ai.AudioChannels()
		}
		return ci

	case *recorder.IngestRecorder:
		_, s, p, v := r.CodecParams()
		ci := model.CodecInfo{
			SPS:    s,
			PPS:    p,
			VPS:    v,
			IsH264: true,
		}
		if ai, ok := rec.(audioInfo); ok {
			ci.AudioCodec = ai.AudioCodec()
			ci.AudioConfig = ai.AudioConfig()
			ci.AudioSampleRate = ai.AudioSampleRate()
			ci.AudioChannels = ai.AudioChannels()
		}
		return ci

	case *recorder.ONVIFRecorder:
		// Delegate may be nil momentarily; recurse via GetCodecInfo if available.
		if d := r.Delegate(); d != nil {
			// Recursively get codec info from delegate via its CameraManager.
			// NOTE: This recurse only handles one level of ONVIF delegation.
			// The delegate's cameraID is the same — the ONVIF recorder doesn't
			// have its own camera entry. We re-dispatch on the delegate type.
			switch delegate := d.(type) {
			case *recorder.H264Recorder:
				ci := model.CodecInfo{
					SPS:    delegate.SPS(),
					PPS:    delegate.PPS(),
					IsH264: true,
				}
				ci.AudioCodec = delegate.AudioCodec()
				ci.AudioConfig = delegate.AudioConfig()
				ci.AudioSampleRate = delegate.AudioSampleRate()
				ci.AudioChannels = delegate.AudioChannels()
				return ci
			case *recorder.H265Recorder:
				ci := model.CodecInfo{
					VPS: delegate.VPS(),
					SPS: delegate.SPS(),
					PPS: delegate.PPS(),
				}
				ci.AudioCodec = delegate.AudioCodec()
				ci.AudioConfig = delegate.AudioConfig()
				ci.AudioSampleRate = delegate.AudioSampleRate()
				ci.AudioChannels = delegate.AudioChannels()
				return ci
			}
		}

	case *xiaomi.XiaomiRecorder:
		xc, _, _, _ := r.CodecParams()
		ci := model.CodecInfo{
			SPS:    r.SPS(),
			PPS:    r.PPS(),
			VPS:    r.VPS(),
			IsH264: xc != model.FormatH265,
		}
		if ai, ok := rec.(audioInfo); ok {
			ci.AudioCodec = ai.AudioCodec()
			ci.AudioConfig = ai.AudioConfig()
			ci.AudioSampleRate = ai.AudioSampleRate()
			ci.AudioChannels = ai.AudioChannels()
		}
		return ci
	}

	// Fallback: try audioInfo interface on any unknown recorder type.
	ci := model.CodecInfo{IsH264: true}
	if ai, ok := rec.(audioInfo); ok {
		ci.AudioCodec = ai.AudioCodec()
		ci.AudioConfig = ai.AudioConfig()
		ci.AudioSampleRate = ai.AudioSampleRate()
		ci.AudioChannels = ai.AudioChannels()
	}
	return ci
}

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

// AddCamera adds a new camera to the manager and starts its recorder if enabled.
// If cam.ID is empty, a new ID is generated automatically.
// Returns the camera ID.
func (cm *CameraManager) AddCamera(ctx context.Context, cam config.CameraConfig) (string, error) {
	if cam.ID == "" {
		cam.ID = GenerateCameraID()
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Check for duplicate ID
	for _, existing := range cm.cfg.Cameras {
		if existing.ID == cam.ID {
			return "", &model.CameraAlreadyExistsError{CameraID: cam.ID}
		}
	}

	// Append to config
	cm.cfg.Cameras = append(cm.cfg.Cameras, cam)

	// Persist to database
	if cm.db != nil {
		if err := cm.db.UpsertCamera(ctx, cam.ID, cam.Name, string(cam.Protocol), cam.Encoding, cam.URL, cam.Username, cam.Password, cam.ONVIFEndpoint, cam.ProfileToken, cam.StreamEncoding); err != nil {
			logger.Error("failed to upsert camera record", "camera_id", cam.ID, "error", err)
		}
	}

	// Start recorder if protocol supports it
	segDur, err := time.ParseDuration(cm.cfg.Storage.SegmentDuration)
	if err != nil {
		segDur = recorder.DefaultSegmentDur
	}
	if err := cm.startRecorder(ctx, cam, segDur); err != nil {
		logger.Error("failed to start recorder", "error", err)
	}

	// Persist config to disk (rollback on failure)
	if err := cm.persistConfig(); err != nil {
		// Rollback: remove the camera we just added
		for i, c := range cm.cfg.Cameras {
			if c.ID == cam.ID {
				cm.cfg.Cameras = append(cm.cfg.Cameras[:i], cm.cfg.Cameras[i+1:]...)
				break
			}
		}
		return "", fmt.Errorf("failed to persist config: %w", err)
	}

	// Auto-populate SnapshotURL for ONVIF cameras (non-blocking)
	if cam.Protocol == string(model.ProtoONVIF) && cam.SnapshotURL == "" {
		go cm.autoPopulateSnapshotURL(context.Background(), cam.ID)
	}

	// Reconcile push-out relay targets. Run in a goroutine so it does NOT execute
	// under cm.mu — SetCameraTargets calls back into GetHub which re-locks cm.mu
	// (RLock), and re-entering under a held Lock would self-deadlock (Go's
	// RWMutex is not reentrant).
	if cm.relayMgr != nil {
		cameraID := cam.ID
		targets := append([]config.PushTargetConfig(nil), cam.PushTargets...)
		go cm.relayMgr.SetCameraTargets(cameraID, targets)
	}

	return cam.ID, nil
}

// RemoveCamera removes a camera from the manager, stops its recorder, and removes it from config.
// Does NOT delete the camera record from the database.
func (cm *CameraManager) RemoveCamera(ctx context.Context, cameraID string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Find camera index and save original for potential rollback
	idx := -1
	for i, cam := range cm.cfg.Cameras {
		if cam.ID == cameraID {
			idx = i
			break
		}
	}
	if idx == -1 {
		return &model.CameraNotFoundError{CameraID: cameraID}
	}
	savedCam := cm.cfg.Cameras[idx]

	// Stop and remove recorder if running
	if rec, ok := cm.recorders[cameraID]; ok {
		if err := rec.Stop(); err != nil {
			logger.Warn("failed to stop recorder", "camera_id", cameraID, "error", err)
		}
		// Notify health manager of camera removal
		cm.healthMgr.OnCameraRemoved(cameraID, rec)
		delete(cm.recorders, cameraID)
		delete(cm.hubRegistry, cameraID)
		if cm.metrics != nil {
			cm.metrics.ActiveCameras.Dec()
		}
	}
	// Stop all relay targets for this camera.
	if cm.relayMgr != nil {
		cm.relayMgr.RemoveCamera(cameraID)
	}

	// Stop keyframe extractor if running
	if ext, ok := cm.keyframeExtractors[cameraID]; ok {
		delete(cm.keyframeExtractors, cameraID)
		if err := ext.Stop(); err != nil {
			logger.Warn("failed to stop keyframe extractor", "camera_id", cameraID, "error", err)
		}
	}

	// Remove from config slice
	cm.cfg.Cameras = append(cm.cfg.Cameras[:idx], cm.cfg.Cameras[idx+1:]...)

	// Persist config to disk (rollback on failure)
	if err := cm.persistConfig(); err != nil {
		// Rollback: re-insert camera at original position
		cm.cfg.Cameras = append(cm.cfg.Cameras[:idx], append([]config.CameraConfig{savedCam}, cm.cfg.Cameras[idx:]...)...)
		return fmt.Errorf("failed to persist config: %w", err)
	}

	return nil
}

// ArchiveCamera archives a camera: stops recorder, merges segments, marks archived in DB,
// marks all recordings archived, and removes from config YAML.
// The camera row and recordings are preserved in the database.
// Merge failure is non-blocking (logged but continues).
func (cm *CameraManager) ArchiveCamera(ctx context.Context, cameraID string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Verify camera exists in config
	idx := -1
	for i, cam := range cm.cfg.Cameras {
		if cam.ID == cameraID {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("camera %q not found", cameraID)
	}
	savedCam := cm.cfg.Cameras[idx]

	// 1. Stop recorder if running
	if rec, ok := cm.recorders[cameraID]; ok {
		if err := rec.Stop(); err != nil {
			logger.Warn("failed to stop recorder", "camera_id", cameraID, "error", err)
		}
		// Notify health manager of camera removal
		cm.healthMgr.OnCameraRemoved(cameraID, rec)
		delete(cm.recorders, cameraID)
		delete(cm.hubRegistry, cameraID)
		if cm.metrics != nil {
			cm.metrics.ActiveCameras.Dec()
		}
	}

	// Stop keyframe extractor if running
	if ext, ok := cm.keyframeExtractors[cameraID]; ok {
		delete(cm.keyframeExtractors, cameraID)
		if err := ext.Stop(); err != nil {
			logger.Warn("failed to stop keyframe extractor", "camera_id", cameraID, "error", err)
		}
	}

	// 2. Merge segments (non-blocking — failure is logged but does not stop archival)
	if cm.mergeMgr != nil {
		if err := cm.mergeMgr.MergeCamera(ctx, cameraID); err != nil {
			logger.Warn("merge before archive failed", "camera_id", cameraID, "error", err)
		}
	}

	// 3. Mark camera archived in DB
	if err := cm.db.ArchiveCameraDB(ctx, cameraID); err != nil {
		return fmt.Errorf("failed to archive camera in DB: %w", err)
	}

	// 4. Mark all recordings archived in DB
	affected, err := cm.db.ArchiveAllRecordings(ctx, cameraID)
	if err != nil {
		logger.Warn("failed to archive recordings", "camera_id", cameraID, "error", err)
	} else {
		logger.Info("archived recordings", "camera_id", cameraID, "count", affected)
	}

	// 5. Remove from in-memory config slice and persist
	cm.cfg.Cameras = append(cm.cfg.Cameras[:idx], cm.cfg.Cameras[idx+1:]...)
	if err := cm.persistConfig(); err != nil {
		// Rollback: re-insert camera at original position
		cm.cfg.Cameras = append(cm.cfg.Cameras[:idx], append([]config.CameraConfig{savedCam}, cm.cfg.Cameras[idx:]...)...)
		return fmt.Errorf("failed to persist config: %w", err)
	}

	logger.Info("archived camera", "camera_id", cameraID)
	return nil
}

// UpdateCamera applies partial updates to an existing camera.
// Returns the updated CameraConfig.
func (cm *CameraManager) UpdateCamera(ctx context.Context, cameraID string, updates CameraUpdate) (*config.CameraConfig, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Find camera
	idx := -1
	var cam *config.CameraConfig
	for i := range cm.cfg.Cameras {
		if cm.cfg.Cameras[i].ID == cameraID {
			idx = i
			cam = &cm.cfg.Cameras[i]
			break
		}
	}
	if idx == -1 {
		return nil, &model.CameraNotFoundError{CameraID: cameraID}
	}
	// Save original for potential rollback
	savedCam := *cam

	// Determine if recorder needs restart
	needsRestart := false
	onvifEndpointChanged := false
	if updates.URL != nil && *updates.URL != cam.URL {
		needsRestart = true
	}
	if updates.Protocol != nil && *updates.Protocol != cam.Protocol {
		needsRestart = true
	}
	if updates.Username != nil && *updates.Username != cam.Username {
		needsRestart = true
	}
	if updates.Password != nil && *updates.Password != cam.Password {
		needsRestart = true
	}
	if updates.Channel != nil && *updates.Channel != cam.Channel {
		needsRestart = true
	}

	// Apply updates
	if updates.Name != nil {
		cam.Name = *updates.Name
	}
	if updates.URL != nil {
		cam.URL = *updates.URL
	}
	if updates.Protocol != nil {
		cam.Protocol = *updates.Protocol
	}
	if updates.Encoding != nil {
		if *updates.Encoding != cam.Encoding {
			needsRestart = true
		}
		cam.Encoding = *updates.Encoding
	}
	if updates.Username != nil {
		cam.Username = *updates.Username
	}
	if updates.Password != nil {
		cam.Password = *updates.Password
	}
	if updates.ONVIFEndpoint != nil {
		if *updates.ONVIFEndpoint != cam.ONVIFEndpoint {
			onvifEndpointChanged = true
		}
		cam.ONVIFEndpoint = *updates.ONVIFEndpoint
	}
	if updates.ProfileToken != nil {
		cam.ProfileToken = *updates.ProfileToken
	}
	if updates.StreamEncoding != nil {
		if *updates.StreamEncoding != cam.StreamEncoding {
			needsRestart = true
		}
		cam.StreamEncoding = *updates.StreamEncoding
	}

	if updates.Transcoding != nil {
		cam.Transcoding = updates.Transcoding
	}
	if updates.Channel != nil {
		cam.Channel = *updates.Channel
	}
	if updates.AudioEnabled != nil {
		cam.AudioEnabled = *updates.AudioEnabled
	}
	// Push/ingest fields (SRT/RTMP)
	if updates.StreamKey != nil {
		cam.StreamKey = *updates.StreamKey
	}
	if updates.SRTPassphrase != nil {
		cam.SRTPassphrase = *updates.SRTPassphrase
	}
	if updates.SRTStreamID != nil {
		cam.SRTStreamID = *updates.SRTStreamID
	}
	if updates.PushTargets != nil {
		cam.PushTargets = *updates.PushTargets
	}
	if updates.PushRetentionDays != nil {
		cam.PushRetentionDays = updates.PushRetentionDays
	}

	// Persist to database
	if cm.db != nil {
		if err := cm.db.UpsertCamera(ctx, cam.ID, cam.Name, string(cam.Protocol), cam.Encoding, cam.URL, cam.Username, cam.Password, cam.ONVIFEndpoint, cam.ProfileToken, cam.StreamEncoding); err != nil {
			logger.Error("failed to upsert camera record", "camera_id", cam.ID, "error", err)
		}
		// Persist DB-only metadata fields
		if updates.Description != nil || updates.Location != nil || updates.Brand != nil || updates.Model != nil || updates.SerialNumber != nil || updates.RetentionDays != nil {
			desc := strPtrOrEmpty(updates.Description)
			loc := strPtrOrEmpty(updates.Location)
			br := strPtrOrEmpty(updates.Brand)
			mo := strPtrOrEmpty(updates.Model)
			sn := strPtrOrEmpty(updates.SerialNumber)
			rd := intPtrOrZero(updates.RetentionDays)
			if err := cm.db.UpdateCameraMetadata(ctx, cam.ID, desc, loc, br, mo, sn, rd); err != nil {
				logger.Error("failed to update camera metadata", "camera_id", cam.ID, "error", err)
			}
		}
	}

	segDur, err := time.ParseDuration(cm.cfg.Storage.SegmentDuration)
	if err != nil {
		segDur = recorder.DefaultSegmentDur
	}

	// Stop existing recorder if needs restart
	if needsRestart {
		if rec, ok := cm.recorders[cam.ID]; ok {
			if err := rec.Stop(); err != nil {
				logger.Warn("failed to stop recorder", "camera_id", cam.ID, "error", err)
			}
			delete(cm.recorders, cam.ID)
		}
		// Stop keyframe extractor if running
		if ext, ok := cm.keyframeExtractors[cam.ID]; ok {
			delete(cm.keyframeExtractors, cam.ID)
			if err := ext.Stop(); err != nil {
				logger.Warn("failed to stop keyframe extractor", "camera_id", cam.ID, "error", err)
			}
		}
	}

	// Start recorder if protocol changed to a recordable one
	if needsRestart {
		// Only start if we don't already have a recorder (needsRestart cleared it, or was never running)
		if _, exists := cm.recorders[cam.ID]; !exists {
			if err := cm.startRecorder(ctx, *cam, segDur); err != nil {
				logger.Error("failed to start recorder", "error", err)
			}
		}
	}

	// If ONVIF endpoint changed, close cached client so a fresh one is created
	if onvifEndpointChanged {
		cm.CloseONVIFClient(cam.ID)
	}

	// Persist config to disk (rollback on failure)
	if err := cm.persistConfig(); err != nil {
		// Rollback: restore original camera config
		cm.cfg.Cameras[idx] = savedCam
		return nil, fmt.Errorf("failed to persist config: %w", err)
	}

	// Auto-populate SnapshotURL for ONVIF cameras (non-blocking)
	protocolChangedToOnvif := updates.Protocol != nil && *updates.Protocol == string(model.ProtoONVIF)
	if (protocolChangedToOnvif || onvifEndpointChanged) && cam.SnapshotURL == "" {
		go cm.autoPopulateSnapshotURL(context.Background(), cam.ID)
	}

	// Reconcile push-out relay targets. Run in a goroutine so it does NOT execute
	// under cm.mu — SetCameraTargets calls back into GetHub which re-locks cm.mu,
	// which would self-deadlock under the held Lock (see AddCamera for rationale).
	if cm.relayMgr != nil {
		cameraID := cam.ID
		targets := append([]config.PushTargetConfig(nil), cam.PushTargets...)
		go cm.relayMgr.SetCameraTargets(cameraID, targets)
	}

	return cam, nil
}

// RestartRecorder stops and recreates the recorder for the given camera.
// The camera must be enabled.
func (cm *CameraManager) RestartRecorder(ctx context.Context, cameraID string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Find camera config
	var cam *config.CameraConfig
	for i := range cm.cfg.Cameras {
		if cm.cfg.Cameras[i].ID == cameraID {
			cam = &cm.cfg.Cameras[i]
			break
		}
	}
	if cam == nil {
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

	// Create and start new recorder
	segDur, err := time.ParseDuration(cm.cfg.Storage.SegmentDuration)
	if err != nil {
		segDur = recorder.DefaultSegmentDur
	}
	return cm.startRecorder(ctx, *cam, segDur)
}

// StartCamera manually starts the recorder for the given camera.
func (cm *CameraManager) StartCamera(ctx context.Context, cameraID string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Find camera config
	var cam *config.CameraConfig
	for i := range cm.cfg.Cameras {
		if cm.cfg.Cameras[i].ID == cameraID {
			cam = &cm.cfg.Cameras[i]
			break
		}
	}
	if cam == nil {
		return &model.CameraNotFoundError{CameraID: cameraID}
	}

	// Check if already running — stale recorders (error/stopped) can be restarted
	if rec, ok := cm.recorders[cameraID]; ok {
		status := rec.Status()
		if status == model.StatusRecording || status == model.StatusReconnecting {
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

	segDur, err := time.ParseDuration(cm.cfg.Storage.SegmentDuration)
	if err != nil {
		segDur = recorder.DefaultSegmentDur
	}
	return cm.startRecorder(ctx, *cam, segDur)
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

	return nil
}

// getOrCreateONVIFClient returns a cached ONVIF client for the given camera,
// creating one if it doesn't exist in the cache.
// Camera config lookup is done OUTSIDE the onvifMu lock to avoid deadlock with cm.mu.
func (cm *CameraManager) getOrCreateONVIFClient(ctx context.Context, cameraID string) (*onvif.Client, error) {
	cam := cm.GetCameraConfig(cameraID)
	if cam == nil {
		return nil, &model.CameraNotFoundError{CameraID: cameraID}
	}
	if cam.Protocol != string(model.ProtoONVIF) {
		return nil, &model.ONVIFNotCameraError{CameraID: cameraID}
	}
	endpoint := cam.ONVIFEndpoint
	if endpoint == "" {
		endpoint = cam.URL
	}

	cm.onvifMu.Lock()
	defer cm.onvifMu.Unlock()

	if cached, ok := cm.onvifClients[cameraID]; ok {
		return cached, nil
	}

	client := onvif.NewClient(endpoint, cam.Username, cam.Password)
	if err := client.Connect(ctx); err != nil {
		return nil, &model.ONVIFConnectionError{CameraID: cameraID, Err: err}
	}
	cm.onvifClients[cameraID] = client
	return client, nil
}

// CloseONVIFClient removes a cached ONVIF client for the given camera.
// Also cleans up any cached device info for this camera.
func (cm *CameraManager) CloseONVIFClient(cameraID string) {
	cm.onvifMu.Lock()
	delete(cm.onvifClients, cameraID)
	cm.onvifMu.Unlock()

	cm.deviceInfoMu.Lock()
	delete(cm.deviceInfoCache, cameraID)
	cm.deviceInfoMu.Unlock()
}

// GetCachedDeviceInfo returns cached device info for the given ONVIF camera.
// On first call, fetches from the device and caches the result.
// Returns nil if the camera is not ONVIF, not found, or the device info cannot be fetched.
func (cm *CameraManager) GetCachedDeviceInfo(ctx context.Context, cameraID string) *onvif.DeviceInfo {
	// Check cache with read lock
	cm.deviceInfoMu.RLock()
	if info, ok := cm.deviceInfoCache[cameraID]; ok {
		cm.deviceInfoMu.RUnlock()
		return info
	}
	cm.deviceInfoMu.RUnlock()

	// Not cached — fetch from device
	client, err := cm.getOrCreateONVIFClient(ctx, cameraID)
	if err != nil {
		logger.Warn("failed to get ONVIF client for device info", "camera_id", cameraID, "error", err)
		return nil
	}

	info, err := client.GetDeviceInformation(ctx)
	if err != nil {
		logger.Warn("failed to get device information", "camera_id", cameraID, "error", err)
		return nil
	}

	// Cache with write lock
	cm.deviceInfoMu.Lock()
	cm.deviceInfoCache[cameraID] = info
	cm.deviceInfoMu.Unlock()

	return info
}

// GetONVIFClient returns a cached ONVIF client for the given camera.
// Returns error if camera is not found, not ONVIF, or client creation fails.
func (cm *CameraManager) GetONVIFClient(ctx context.Context, cameraID string) (*onvif.Client, error) {
	return cm.getOrCreateONVIFClient(ctx, cameraID)
}

// closeAllONVIFClients clears the entire ONVIF client and device info caches.
func (cm *CameraManager) closeAllONVIFClients() {
	cm.onvifMu.Lock()
	cm.onvifClients = make(map[string]*onvif.Client)
	cm.onvifMu.Unlock()

	cm.deviceInfoMu.Lock()
	cm.deviceInfoCache = make(map[string]*onvif.DeviceInfo)
	cm.deviceInfoMu.Unlock()
}

// GetONVIFPTZController returns a PTZController for the given ONVIF camera.
// Returns error if camera is not found, not ONVIF, or client creation fails.
func (cm *CameraManager) GetONVIFPTZController(ctx context.Context, cameraID string) (onvif.PTZController, error) {
	client, err := cm.getOrCreateONVIFClient(ctx, cameraID)
	if err != nil {
		return nil, err
	}
	profiles, err := client.GetProfiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("get profiles for camera %q: %w", cameraID, err)
	}
	if len(profiles) == 0 {
		return nil, &model.ONVIFNoProfilesError{CameraID: cameraID}
	}
	return client.NewPTZController(profiles[0].Token), nil
}

// GetImagingController returns an ImagingController for the given ONVIF camera.
// Returns error if camera is not found, not ONVIF, or client creation fails.
func (cm *CameraManager) GetImagingController(ctx context.Context, cameraID string) (onvif.ImagingController, error) {
	client, err := cm.getOrCreateONVIFClient(ctx, cameraID)
	if err != nil {
		return nil, err
	}
	profiles, err := client.GetProfiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("get profiles for camera %q: %w", cameraID, err)
	}
	if len(profiles) == 0 {
		return nil, &model.ONVIFNoProfilesError{CameraID: cameraID}
	}
	ctrl := client.NewImagingController(profiles[0].Token)
	if ctrl == nil {
		return nil, fmt.Errorf("failed to create imaging controller for camera %q", cameraID)
	}
	// Use device endpoint as imaging service base — most cameras serve imaging
	// on the same host with /onvif/imaging_service path.
	endpoint := client.GetEndpoint()
	imgEndpoint := strings.TrimSuffix(endpoint, "/device_service") + "/imaging_service"
	ctrl.SetImagingEndpoint(imgEndpoint)
	return ctrl, nil
}

// GetSnapshotProvider returns a SnapshotProvider for the given ONVIF camera.
// Returns error if camera is not found, not ONVIF, or client creation fails.
func (cm *CameraManager) GetSnapshotProvider(ctx context.Context, cameraID string) (onvif.SnapshotProvider, error) {
	client, err := cm.getOrCreateONVIFClient(ctx, cameraID)
	if err != nil {
		return nil, err
	}
	profiles, err := client.GetProfiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("get profiles for camera %q: %w", cameraID, err)
	}
	if len(profiles) == 0 {
		return nil, &model.ONVIFNoProfilesError{CameraID: cameraID}
	}
	return client.NewSnapshotProvider(profiles[0].Token), nil
}

// GetDeviceManager returns a DeviceManager for the given ONVIF camera.
// Returns error if camera is not found, not ONVIF, or client creation fails.
func (cm *CameraManager) GetDeviceManager(ctx context.Context, cameraID string) (onvif.DeviceManager, error) {
	client, err := cm.getOrCreateONVIFClient(ctx, cameraID)
	if err != nil {
		return nil, err
	}
	dm := client.NewDeviceManager()
	if dm == nil {
		return nil, fmt.Errorf("failed to create device manager for camera %q", cameraID)
	}
	return dm, nil
}

// strPtrOrEmpty returns the string value of a *string pointer, or empty string if nil.
func strPtrOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// intPtrOrZero returns the int value of a *int pointer, or 0 if nil.
func intPtrOrZero(i *int) int {
	if i == nil {
		return 0
	}
	return *i
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
		}
	}
}

// SubscribeONVIFEvents subscribes to PullPoint events for the given camera.
// The eventCallback is invoked when events are received.
// Returns error if camera is not found, not ONVIF, or subscription fails.
func (cm *CameraManager) SubscribeONVIFEvents(ctx context.Context, cameraID string, eventCallback onvif.EventCallback) error {
	client, err := cm.getOrCreateONVIFClient(ctx, cameraID)
	if err != nil {
		return err
	}

	cm.onvifMu.Lock()
	defer cm.onvifMu.Unlock()

	if _, exists := cm.eventSubscribers[cameraID]; exists {
		return nil // Already subscribed
	}

	sub := client.NewEventSubscriber(onvif.WithEventCallback(eventCallback))
	if sub == nil {
		return fmt.Errorf("camera %q: failed to create event subscriber", cameraID)
	}
	if err := sub.Subscribe(ctx, cameraID); err != nil {
		return fmt.Errorf("camera %q: subscribe to events: %w", cameraID, err)
	}
	cm.eventSubscribers[cameraID] = sub
	logger.Info("subscribed to ONVIF events", "camera_id", cameraID)
	return nil
}

// UnsubscribeONVIFEvents unsubscribes from PullPoint events for the given camera.
func (cm *CameraManager) UnsubscribeONVIFEvents(ctx context.Context, cameraID string) error {
	cm.onvifMu.Lock()
	defer cm.onvifMu.Unlock()

	sub, exists := cm.eventSubscribers[cameraID]
	if !exists {
		return nil
	}

	if err := sub.Unsubscribe(ctx, cameraID); err != nil {
		logger.Warn("failed to unsubscribe from events", "camera_id", cameraID, "error", err)
	}
	delete(cm.eventSubscribers, cameraID)
	logger.Info("unsubscribed from ONVIF events", "camera_id", cameraID)
	return nil
}

// StopAllONVIFEvents unsubscribes from all ONVIF event subscriptions.
func (cm *CameraManager) StopAllONVIFEvents(ctx context.Context) {
	cm.onvifMu.Lock()
	for id, sub := range cm.eventSubscribers {
		_ = sub.Unsubscribe(ctx, id)
	}
	cm.eventSubscribers = make(map[string]onvif.EventSubscriber)
	cm.onvifMu.Unlock()
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
