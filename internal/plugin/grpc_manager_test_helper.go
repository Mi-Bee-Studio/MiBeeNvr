package plugin

import (
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	gen "github.com/Mi-Bee-Studio/MiBeeNvr/plugin/proto/gen"
)

func AddTestPlugin(mgr *PluginManager, name string, info *gen.PluginInfo) {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	mgr.plugins[name] = &ManagedPlugin{
		Name:      name,
		Info:      info,
		Status:    StatusRunning,
		StartedAt: time.Now().Add(-1 * time.Hour),
	}
}

func NewTestPluginManager(names ...string) *PluginManager {
	cfg := &config.PluginsConfig{
		Plugins: make(map[string]config.PluginEntryConfig),
	}
	for _, n := range names {
		cfg.Plugins[n] = config.PluginEntryConfig{Enabled: true}
	}
	return NewPluginManager(cfg)
}
