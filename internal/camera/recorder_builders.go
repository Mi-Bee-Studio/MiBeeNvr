package camera

import (
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/xiaomi"
)

// recorderBuilder constructs a recorder for one protocol family. It returns
// nil when the protocol/encoding pair is not recordable — createRecorder then
// reports the camera as non-recordable.
type recorderBuilder func(cm *CameraManager, cam config.CameraConfig, segDur time.Duration) model.Recorder

// recorderBuilders maps a camera protocol to its recorder constructor. Adding
// a protocol means adding one entry here plus the recorder type itself — no
// protocol switch arms to hunt down elsewhere (issue #609).
var recorderBuilders = map[string]recorderBuilder{
	string(model.ProtoXiaomi):    (*CameraManager).buildXiaomiRecorder,
	string(model.ProtoRTSP):      (*CameraManager).buildRTSPRecorder,
	string(model.ProtoHTTP):      (*CameraManager).buildHTTPJPEGRecorder,
	string(model.ProtoONVIF):     (*CameraManager).buildONVIFRecorder,
	string(model.ProtoTimelapse): (*CameraManager).buildTimelapseRecorder,
	string(model.ProtoGB28181):   (*CameraManager).buildGB28181Recorder,
	string(model.ProtoSRT):       (*CameraManager).buildIngestRecorder,
	string(model.ProtoRTMP):      (*CameraManager).buildIngestRecorder,
	string(model.ProtoWHIP):      (*CameraManager).buildIngestRecorder,
}

// resolveAdaptiveConfig parses the YAML adaptive-recording overrides into the
// recorder's resolved form, defaulting unspecified fields (issue #435). The
// config layer has already validated ranges.
func resolveAdaptiveConfig(a *config.AdaptiveRecordingConfig) *recorder.AdaptiveConfig {
	var calm, interval string
	var spike float64
	var gop int64
	var ambient, archive bool
	if a != nil {
		calm, interval, spike, gop, ambient, archive = a.CalmThreshold, a.TimelapseInterval, a.SpikeFactor, a.GOPBufferBytes, a.AmbientAudio, a.AmbientArchive
	}
	ac := recorder.ResolveAdaptiveConfig(calm, interval, spike, gop, ambient, archive)
	return &ac
}

// resolveAudioTriggerConfig resolves the audio-trigger overrides (issue #478);
// nil when not armed. G.711-only at the recorder level.
func resolveAudioTriggerConfig(a *config.CameraAudioTriggerConfig) *recorder.AudioTriggerConfig {
	if a == nil || !a.Enabled {
		return nil
	}
	at := recorder.ResolveAudioTriggerConfig(a.MinDBFS, a.PreCaptureS)
	return &at
}

// buildXiaomiRecorder builds the Xiaomi (CS2/TUTK P2P) recorder plugin.
func (cm *CameraManager) buildXiaomiRecorder(cam config.CameraConfig, segDur time.Duration) model.Recorder {
	plugin := &xiaomi.XiaomiPlugin{}
	plugin.SetEventBus(cm.eventBus)
	rec := plugin.NewRecorder(cam, cm.store, cm.db, cm.metrics)
	// Wire ErrorReporter for TUTK vendor error detection
	if xr, ok := rec.(*xiaomi.XiaomiRecorder); ok {
		xr.SetErrorReporter(cm)
	}
	return rec
}

// buildRTSPRecorder builds an RTSP recorder for the camera's encoding
// (h264/h265/mjpeg).
func (cm *CameraManager) buildRTSPRecorder(cam config.CameraConfig, segDur time.Duration) model.Recorder {
	switch cam.Encoding {
	case string(model.FormatH264):
		h264Cfg := recorder.H264Config{
			CameraID:          cam.ID,
			RTSPURL:           cam.URL,
			Username:          cam.Username,
			Password:          cam.Password,
			SegmentDur:        segDur,
			DB:                cm.db,
			AudioEnabled:      cam.AudioEnabled,
			AudioInRecordings: cam.AudioInRecordings,
			EventBus:          cm.eventBus,
			RecordEnabled:     cam.RecordingEnabled,
			RingBufCap:        cam.RingBufCap,
		}
		if d, err := time.ParseDuration(cam.FrameWatchdogTimeout); err == nil && d > 0 {
			h264Cfg.FrameWatchdogTimeout = d
		}
		if cam.RecordingMode == "adaptive" {
			h264Cfg.Adaptive = resolveAdaptiveConfig(cam.Adaptive)
		}
		h264Cfg.AudioTrigger = resolveAudioTriggerConfig(cam.AudioTrigger)
		return recorder.NewH264Recorder(h264Cfg, cm.store, cm.metrics)
	case string(model.FormatH265):
		h265Cfg := recorder.H265Config{
			CameraID:          cam.ID,
			RTSPURL:           cam.URL,
			Username:          cam.Username,
			Password:          cam.Password,
			SegmentDur:        segDur,
			DB:                cm.db,
			AudioEnabled:      cam.AudioEnabled,
			AudioInRecordings: cam.AudioInRecordings,
			EventBus:          cm.eventBus,
			RecordEnabled:     cam.RecordingEnabled,
			RingBufCap:        cam.RingBufCap,
		}
		if d, err := time.ParseDuration(cam.FrameWatchdogTimeout); err == nil && d > 0 {
			h265Cfg.FrameWatchdogTimeout = d
		}
		if cam.RecordingMode == "adaptive" {
			h265Cfg.Adaptive = resolveAdaptiveConfig(cam.Adaptive)
		}
		h265Cfg.AudioTrigger = resolveAudioTriggerConfig(cam.AudioTrigger)
		return recorder.NewH265Recorder(h265Cfg, cm.store, cm.metrics)
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
		return recorder.NewMJPEGRecorder(mjpegCfg, cm.store, cm.metrics)
	default:
		return nil
	}
}

// buildHTTPJPEGRecorder builds the HTTP-JPEG (ESP32 MiBeeCam style) recorder.
func (cm *CameraManager) buildHTTPJPEGRecorder(cam config.CameraConfig, segDur time.Duration) model.Recorder {
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
	return recorder.NewHTTPJPEGRecorder(httpJpegCfg, cm.store, cm.metrics)
}

// buildONVIFRecorder builds the ONVIF recorder (delegates to an inner
// H264/H265/JPEG recorder once the stream profile is resolved).
func (cm *CameraManager) buildONVIFRecorder(cam config.CameraConfig, segDur time.Duration) model.Recorder {
	onvifEndpoint := cam.ONVIFEndpoint
	if onvifEndpoint == "" {
		onvifEndpoint = cam.URL
	}
	onvifClient := cm.reuseOrCreateONVIFClient(cam.ID, onvifEndpoint, cam.Username, cam.Password)
	onvifCfg := recorder.ONVIFConfig{
		CameraID:          cam.ID,
		ProfileToken:      cam.ProfileToken,
		StreamEncoding:    cam.StreamEncoding,
		Username:          cam.Username,
		Password:          cam.Password,
		SegmentDur:        segDur,
		DB:                cm.db,
		AudioEnabled:      cam.AudioEnabled,
		AudioInRecordings: cam.AudioInRecordings,
		ONVIFEndpoint:     onvifEndpoint,
		AVI:               cam.HTTPJPEGAVI,
		EventBus:          cm.eventBus,
		RecordEnabled:     cam.RecordingEnabled,
	}
	if cam.RecordingMode == "adaptive" {
		// Validated by config.ValidateCameraRecordingMode (h264/h265 only);
		// createDelegate ignores it for MJPEG/JPEG encodings.
		onvifCfg.Adaptive = resolveAdaptiveConfig(cam.Adaptive)
		onvifCfg.AudioTrigger = resolveAudioTriggerConfig(cam.AudioTrigger)
	}
	if d, err := time.ParseDuration(cam.FrameWatchdogTimeout); err == nil && d > 0 {
		onvifCfg.FrameWatchdogTimeout = d
	}
	onvifCfg.RingBufCap = cam.RingBufCap
	return recorder.NewONVIFRecorder(onvifCfg, onvifClient, cm.store, cm.metrics)
}

// buildTimelapseRecorder builds a timelapse recorder for the configured frame
// source (snapshot / rtsp_keyframe / mjpeg).
func (cm *CameraManager) buildTimelapseRecorder(cam config.CameraConfig, segDur time.Duration) model.Recorder {
	frameSource := "auto"
	if cam.Timelapse != nil && cam.Timelapse.FrameSource != "" {
		frameSource = cam.Timelapse.FrameSource
	}
	if cam.Timelapse == nil || !cam.Timelapse.Enabled {
		return nil
	}
	switch frameSource {
	case "snapshot":
		return cm.createTimelapseSnapshotRecorder(cam, segDur)
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
				RingBufCap:   cam.RingBufCap,
			}
			if d, err := time.ParseDuration(cam.FrameWatchdogTimeout); err == nil && d > 0 {
				h264Cfg.FrameWatchdogTimeout = d
			}
			return recorder.NewH264Recorder(h264Cfg, cm.store, cm.metrics)
		case "h265":
			h265Cfg := recorder.H265Config{
				CameraID:     cam.ID,
				RTSPURL:      cam.URL,
				Username:     cam.Username,
				Password:     cam.Password,
				SegmentDur:   segDur,
				DB:           cm.db,
				AudioEnabled: cam.AudioEnabled,
				RingBufCap:   cam.RingBufCap,
			}
			if d, err := time.ParseDuration(cam.FrameWatchdogTimeout); err == nil && d > 0 {
				h265Cfg.FrameWatchdogTimeout = d
			}
			return recorder.NewH265Recorder(h265Cfg, cm.store, cm.metrics)
		default:
			logger.Warn("unsupported encoding for rtsp_keyframe timelapse frame source", "camera_id", cam.ID, "encoding", cam.Encoding)
			return nil
		}
	case "mjpeg", "auto", "":
		return cm.createTimelapseMJPEGRecorder(cam, segDur)
	default:
		logger.Warn("unknown timelapse frame source", "camera_id", cam.ID, "frame_source", frameSource)
		return nil
	}
}

// buildGB28181Recorder builds the passive GB28181 recorder — media flows in
// only after a SIP INVITE (autoInviteGB28181 drives that at recorder start).
func (cm *CameraManager) buildGB28181Recorder(cam config.CameraConfig, segDur time.Duration) model.Recorder {
	enc := cam.Encoding
	if enc == "" {
		enc = string(model.FormatH264)
	}
	// Full recording pipeline: segments on disk, recordings DB rows,
	// SegmentCompleted events, metrics — same guarantees as ingest cams.
	return recorder.NewGB28181Recorder(recorder.GB28181Config{
		CameraID:      cam.ID,
		Encoding:      enc,
		SegmentDur:    segDur,
		Store:         cm.store,
		DB:            cm.db,
		Metrics:       cm.metrics,
		EventBus:      cm.eventBus,
		RecordEnabled: cam.RecordingEnabled == nil || *cam.RecordingEnabled,
		AudioEnabled:  cam.AudioEnabled,
	}, nil)
}

// buildIngestRecorder builds the push-ingest recorder shared by SRT / RTMP /
// WHIP push-in cameras — the hub is created on publisher connect.
func (cm *CameraManager) buildIngestRecorder(cam config.CameraConfig, segDur time.Duration) model.Recorder {
	enc := cam.Encoding
	if enc == "" {
		enc = string(model.FormatH264)
	}
	return recorder.NewIngestRecorder(recorder.IngestConfig{
		CameraID:      cam.ID,
		Encoding:      enc,
		SegmentDur:    segDur,
		Store:         cm.store,
		DB:            cm.db,
		Metrics:       cm.metrics,
		EventBus:      cm.eventBus,
		RecordEnabled: cam.RecordingEnabled,
	})
}
