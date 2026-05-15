package plugin

import (
	"sync"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/go-chi/chi/v5"
)

// RecorderPlugin defines the interface for camera protocol plugins.
type RecorderPlugin interface {
	// Name returns the unique plugin identifier (e.g. "xiaomi").
	Name() string

	// Protocols returns the transport protocols this plugin handles (e.g. ["xiaomi"]).
	Protocols() []string

	// NewRecorder creates a new Recorder for the given camera configuration.
	NewRecorder(cfg config.CameraConfig, store *storage.Manager, db *storage.DB, opts ...*metrics.Metrics) model.Recorder

	// RegisterRoutes adds plugin-specific HTTP routes to the router.
	RegisterRoutes(r chi.Router)

	// ConfigSchema returns an example config struct for documentation/validation.
	ConfigSchema() interface{}
}

var (
	pluginsMu sync.RWMutex
	plugins   = make(map[string]RecorderPlugin)
)

// Register adds a plugin to the global registry.
func Register(p RecorderPlugin) {
	pluginsMu.Lock()
	defer pluginsMu.Unlock()
	plugins[p.Name()] = p
}

// Lookup finds a plugin by its unique name.
func Lookup(name string) RecorderPlugin {
	pluginsMu.RLock()
	defer pluginsMu.RUnlock()
	return plugins[name]
}

// LookupProtocol finds a plugin that handles the given protocol.
func LookupProtocol(proto string) RecorderPlugin {
	pluginsMu.RLock()
	defer pluginsMu.RUnlock()
	for _, p := range plugins {
		for _, supported := range p.Protocols() {
			if supported == proto {
				return p
			}
		}
	}
	return nil
}

// All returns all registered plugins.
func All() []RecorderPlugin {
	pluginsMu.RLock()
	defer pluginsMu.RUnlock()
	result := make([]RecorderPlugin, 0, len(plugins))
	for _, p := range plugins {
		result = append(result, p)
	}
	return result
}
