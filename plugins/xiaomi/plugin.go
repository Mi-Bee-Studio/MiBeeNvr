// SPDX-License-Identifier: MIT
//
// Xiaomi camera plugin registration for MiBee NVR.
// Licensed under the MIT License.

package xiaomi

import (
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/plugin"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/go-chi/chi/v5"
)

// XiaomiPlugin implements plugin.RecorderPlugin for Xiaomi cameras.
type XiaomiPlugin struct{}

func init() {
	plugin.Register(&XiaomiPlugin{})
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
		CameraID:   cfg.ID,
		DID:        did,
		CloudCfg:   cloudCfg,
		SegmentDur: 30 * time.Second,
		DB:         db,
	}
	return NewXiaomiRecorder(recCfg, store, opts...)
}

func (p *XiaomiPlugin) RegisterRoutes(r chi.Router) {
	// Xiaomi-specific routes registered separately in api/handler.go
}

func (p *XiaomiPlugin) ConfigSchema() interface{} {
	return config.XiaomiConfig{}
}
