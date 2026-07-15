package camera

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/xiaomi"
)

// createRecorder creates a recorder for the given camera config.
// Returns nil for unknown protocols.
func (cm *CameraManager) createRecorder(cam config.CameraConfig, segDur time.Duration) model.Recorder {
	var rec model.Recorder
	switch cam.Protocol {
	case "xiaomi":
		plugin := &xiaomi.XiaomiPlugin{}
		plugin.SetEventBus(cm.eventBus)
		rec = plugin.NewRecorder(cam, cm.store, cm.db, cm.metrics)
		// Wire ErrorReporter for TUTK vendor error detection
		if xr, ok := rec.(*xiaomi.XiaomiRecorder); ok {
			xr.SetErrorReporter(cm)
		}
	case string(model.ProtoRTSP):
		switch cam.Encoding {
		case string(model.FormatH264):
			h264Cfg := recorder.H264Config{
				CameraID:      cam.ID,
				RTSPURL:       cam.URL,
				Username:      cam.Username,
				Password:      cam.Password,
				SegmentDur:    segDur,
				DB:            cm.db,
				AudioEnabled:  cam.AudioEnabled,
				EventBus:      cm.eventBus,
				RecordEnabled: cam.RecordingEnabled,
			}
			if d, err := time.ParseDuration(cam.FrameWatchdogTimeout); err == nil && d > 0 {
				h264Cfg.FrameWatchdogTimeout = d
			}
			rec = recorder.NewH264Recorder(h264Cfg, cm.store, cm.metrics)
		case string(model.FormatH265):
			h265Cfg := recorder.H265Config{
				CameraID:      cam.ID,
				RTSPURL:       cam.URL,
				Username:      cam.Username,
				Password:      cam.Password,
				SegmentDur:    segDur,
				DB:            cm.db,
				AudioEnabled:  cam.AudioEnabled,
				EventBus:      cm.eventBus,
				RecordEnabled: cam.RecordingEnabled,
			}
			if d, err := time.ParseDuration(cam.FrameWatchdogTimeout); err == nil && d > 0 {
				h265Cfg.FrameWatchdogTimeout = d
			}
			rec = recorder.NewH265Recorder(h265Cfg, cm.store, cm.metrics)
		case string(model.FormatMJPEG):
			mjpegCfg := recorder.MJPEGConfig{
				CameraID:               cam.ID,
				RTSPURL:                cam.URL,
				SegmentDur:             segDur,
				SampleInterval:         cam.SampleInterval,
				DB:                     cm.db,
				AudioEnabled:           cam.AudioEnabled,
				EventBus:               cm.eventBus,
				DarkFrameFilterEnabled: cam.DarkFrameFilterEnabled,
				DarkFrameThreshold:     cam.DarkFrameThreshold,
				RecordEnabled:          cam.RecordingEnabled,
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
			CameraID:               cam.ID,
			URL:                    cam.URL,
			SegmentDur:             segDur,
			Username:               cam.Username,
			Password:               cam.Password,
			DB:                     cm.db,
			AVI:                    cam.HTTPJPEGAVI,
			EventBus:               cm.eventBus,
			DarkFrameFilterEnabled: cam.DarkFrameFilterEnabled,
			DarkFrameThreshold:     cam.DarkFrameThreshold,
			RecordEnabled:          cam.RecordingEnabled,
		}
		rec = recorder.NewHTTPJPEGRecorder(httpJpegCfg, cm.store, cm.metrics)
	case string(model.ProtoONVIF):
		onvifEndpoint := cam.ONVIFEndpoint
		if onvifEndpoint == "" {
			onvifEndpoint = cam.URL
		}
		onvifClient := cm.reuseOrCreateONVIFClient(cam.ID, onvifEndpoint, cam.Username, cam.Password)
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
			AVI:            cam.HTTPJPEGAVI,
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
		// Runtime override: an ONVIF camera with empty encoding may have resolved
		// to rtsp_keyframe statically but actually be a JPEG device. Use a frame
		// poller instead.
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

	// Enforce timelapse schedule for dual-mode cameras.
	cm.startDualModeTimelapseScheduleMonitorForCamera(ctx, cam.ID, cam, rec)

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

	// For ONVIF cameras without a stable_id yet, auto-populate it asynchronously
	// so IP self-healing (internal/rediscovery) can later re-acquire the camera.
	// This is best-effort and non-blocking: failures are logged and ignored.
	if (cam.Protocol == "onvif" || cam.Protocol == string(model.ProtoONVIF)) && strings.TrimSpace(cam.StableID) == "" {
		go cm.ensureStableID(cam.ID)
	}
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
