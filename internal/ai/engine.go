package ai

import (
	"sync"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
)

// Config mirrors config.AIConfig fields needed by the AI manager.
// Defined locally to avoid a circular import (config imports ai for the ROI type).
type Config struct {
	Enabled             bool             `yaml:"enabled"`
	EnabledCameras      []string         `yaml:"enabled_cameras"`
	ModelURL            string           `yaml:"model_url"`
	Zones               map[string][]ROI `yaml:"zones"`
	FrameSkipRate       int              `yaml:"frame_skip_rate"`
	ConfidenceThreshold float64          `yaml:"confidence_threshold"`
}

// Manager manages AI configuration. No backend inference — the browser
// handles all AI inference via ONNX Runtime Web. This manager serves
// as a pure config store for AI settings and ROI zones.
type Manager struct {
	cfg Config
	bus *event.EventBus
	mu  sync.Mutex
}

// NewManager creates a new AI Manager.
func NewManager(cfg Config, bus *event.EventBus) *Manager {
	return &Manager{
		cfg: cfg,
		bus: bus,
	}
}

// GetConfig returns a copy of the current AI configuration.
func (m *Manager) GetConfig() Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

// UpdateConfig replaces the current AI configuration with the given one.
func (m *Manager) UpdateConfig(cfg Config) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg = cfg
}
