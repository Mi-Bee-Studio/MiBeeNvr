// SPDX-License-Identifier: MIT
//
// Xiaomi camera plugin registration for MiBee NVR.
// Licensed under the MIT License.

package xiaomi

import (
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/go-chi/chi/v5"
)

// XiaomiPlugin provides Xiaomi camera recorder creation.
type XiaomiPlugin struct {
	eventBus *event.EventBus
}

// SetEventBus injects the event bus so Xiaomi recorders can publish SegmentCompleted events.
func (p *XiaomiPlugin) SetEventBus(bus *event.EventBus) {
	p.eventBus = bus
}

func (p *XiaomiPlugin) Name() string { return "xiaomi" }

func (p *XiaomiPlugin) Protocols() []string { return []string{"xiaomi"} }

// cloudCfg stores the Xiaomi cloud credentials, set via SetCloudConfig.
var cloudCfg XiaomiCloudConfig

// SetCloudConfig stores the Xiaomi cloud credentials for use by recorders.
// Must be called before any xiaomi recorder is created.
func SetCloudConfig(cfg config.XiaomiConfig) {
	cloudCfg = XiaomiCloudConfig{
		UserID: cfg.UserID,
		Token:  cfg.Token,
		Region: cfg.Region,
	}
}

// extractDID extracts the device ID from a xiaomi:// URL.
// Input: "xiaomi://655448418" → Output: "655448418"
func extractDID(rawURL string) string {
	return strings.TrimPrefix(rawURL, "xiaomi://")
}

func (p *XiaomiPlugin) NewRecorder(cfg config.CameraConfig, store *storage.Manager, db *storage.DB, opts ...*metrics.Metrics) model.Recorder {
	did := cfg.DID
	if did == "" {
		did = extractDID(cfg.URL)
	}

	recCfg := XiaomiRecorderConfig{
		CameraID:          cfg.ID,
		DID:               did,
		CloudCfg:          cloudCfg,
		SegmentDur:        30 * time.Second,
		DB:                db,
		HealthDB:          db, // quality-change health events (issue #502)
		AudioEnabled:      cfg.AudioEnabled,
		AudioInRecordings: cfg.AudioInRecordings,
		Channel:           cfg.Channel,
		Quality:           cfg.Quality,
		EventBus:          p.eventBus,
		RecordEnabled:     cfg.RecordingEnabled,
	}
	// Adaptive write-density (issue #435/#468): recording_mode: adaptive was
	// silently ignored by the Xiaomi recorder until the gate was ported.
	// Config validation (ValidateCameraRecordingMode) already restricts it to
	// h264/h265 cameras.
	if cfg.RecordingMode == "adaptive" {
		var a *config.AdaptiveRecordingConfig
		if cfg.Adaptive != nil {
			a = cfg.Adaptive
		}
		calm, interval, spike, gop, ambient, archive := "", "", 0.0, int64(0), false, false
		if a != nil {
			calm, interval, spike, gop, ambient, archive = a.CalmThreshold, a.TimelapseInterval, a.SpikeFactor, a.GOPBufferBytes, a.AmbientAudio, a.AmbientArchive
		}
		ac := recorder.ResolveAdaptiveConfig(calm, interval, spike, gop, ambient, archive)
		recCfg.Adaptive = &ac
		// Audio-trigger (issue #478): only meaningful on top of adaptive, and
		// only for G.711 cameras — the recorder logs Opus as inactive.
		if cfg.AudioTrigger != nil && cfg.AudioTrigger.Enabled {
			at := recorder.ResolveAudioTriggerConfig(cfg.AudioTrigger.MinDBFS, cfg.AudioTrigger.PreCaptureS)
			recCfg.AudioTrigger = &at
		}
	}
	return NewXiaomiRecorder(recCfg, store, opts...)
}

func (p *XiaomiPlugin) RegisterRoutes(r chi.Router) {
	// Xiaomi-specific routes registered separately in api/handler.go
}

func (p *XiaomiPlugin) ConfigSchema() interface{} {
	return config.XiaomiConfig{}
}
