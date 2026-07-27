package config

import (
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/ai"
)

// AI detection configuration (browser-side ONNX Runtime Web).
// internal/ai/ is a config + ROI-zone store ONLY — no backend inference.

type AIConfig struct {
	Enabled             bool                `yaml:"enabled" json:"enabled"`
	EnabledCameras      []string            `yaml:"enabled_cameras" json:"enabledCameras"`
	ModelURL            string              `yaml:"model_url" json:"modelUrl"`
	Zones               map[string][]ai.ROI `yaml:"zones" json:"zones"`
	FrameSkipRate       int                 `yaml:"frame_skip_rate" json:"frameSkipRate"`
	ConfidenceThreshold float64             `yaml:"confidence_threshold" json:"confidenceThreshold"`
}
