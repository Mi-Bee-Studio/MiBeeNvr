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
	// EmaAlpha is the EMA smoothing factor for bounding-box positions
	// (#183): higher = more responsive (box follows motion quickly), lower =
	// smoother (box jitters less). Range 0.1–0.9; default 0.3.
	EmaAlpha float64 `yaml:"ema_alpha" json:"emaAlpha"`
	// MaxAge is how many detection cycles a disappeared box lingers before
	// removal (#183): higher = boxes stay longer when an object is briefly
	// occluded, lower = boxes vanish faster. Range 3–30; default 15.
	MaxAge int `yaml:"max_age" json:"maxAge"`
	// EnabledClasses restricts detection to the given COCO class labels
	// (#184). Empty/nil = all 80 classes (current behavior). Example:
	// ["person", "car"] shows only people and cars.
	EnabledClasses []string `yaml:"enabled_classes" json:"enabledClasses"`
}
