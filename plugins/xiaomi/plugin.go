// SPDX-License-Identifier: MIT
//
// Xiaomi camera plugin registration for MiBee NVR.
// Licensed under the MIT License.

package xiaomi

import (
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

func (p *XiaomiPlugin) NewRecorder(cfg config.CameraConfig, store *storage.Manager, db *storage.DB, opts ...*metrics.Metrics) model.Recorder {
	recCfg := XiaomiRecorderConfig{
		CameraID:   cfg.ID,
		MISSURL:    cfg.URL,
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
