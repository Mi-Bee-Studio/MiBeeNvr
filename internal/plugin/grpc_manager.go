package plugin

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	goPlugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	gen "github.com/Mi-Bee-Studio/MiBeeNvr/plugin/proto/gen"
	sharedPlugin "github.com/Mi-Bee-Studio/MiBeeNvr/plugin"
)

// Handshake constants are defined in plugin/handshake.go (shared between host and plugins).
// Re-export here for backward compatibility with internal consumers.
const (
	MagicCookieKey           = sharedPlugin.MagicCookieKey
	MagicCookieValue         = sharedPlugin.MagicCookieValue
	PluginType               = sharedPlugin.PluginType
	DefaultHealthCheckInterval = 30 * time.Second
	healthCheckTimeout = 5 * time.Second
	DefaultInitBackoff = 1 * time.Second
	DefaultMaxBackoff  = 60 * time.Second
	MaxRestartAttempts = 10
)

var grpcMgrLogger = slog.Default().With("component", "plugin-manager")

// Handshake is re-exported from the shared plugin package.
var Handshake = sharedPlugin.Handshake

// PluginInterface implements hashicorp/go-plugin's Plugin interface for gRPC
// transport. The host uses it to dispense a PluginServiceClient; the plugin
// process uses it to register a PluginServiceServer.
type PluginInterface struct {
	goPlugin.NetRPCUnsupportedPlugin
	impl gen.PluginServiceServer
}

// GRPCServer registers the plugin service with the gRPC server (plugin side).
func (p *PluginInterface) GRPCServer(_ *goPlugin.GRPCBroker, s *grpc.Server) error {
	gen.RegisterPluginServiceServer(s, p.impl)
	return nil
}

// GRPCClient returns a PluginServiceClient from the gRPC connection (host side).
func (p *PluginInterface) GRPCClient(ctx context.Context, _ *goPlugin.GRPCBroker, c *grpc.ClientConn) (interface{}, error) {
	return gen.NewPluginServiceClient(c), nil
}

// PluginStatus represents the current status of a managed plugin process.
type PluginStatus string

const (
	StatusRunning PluginStatus = "running"
	StatusStopped PluginStatus = "stopped"
	StatusError   PluginStatus = "error"
)

// ManagedPlugin tracks the runtime state of a single plugin process.
type ManagedPlugin struct {
	Name         string
	Client       gen.PluginServiceClient
	Info         *gen.PluginInfo
	Status       PluginStatus
	StartedAt    time.Time
	RestartCount int

	path   string
	client *goPlugin.Client
	cancel context.CancelFunc
}

// PluginManager manages gRPC plugin process lifecycles: discovery, startup,
// health checking, crash detection, auto-restart with exponential backoff,
// and graceful shutdown.
type PluginManager struct {
	config  *config.PluginsConfig
	plugins map[string]*ManagedPlugin
	mu      sync.RWMutex
	logger  *slog.Logger
	ctx     context.Context
	cancel  context.CancelFunc
}

// NewPluginManager creates a new PluginManager with the given configuration.
func NewPluginManager(cfg *config.PluginsConfig) *PluginManager {
	return &PluginManager{
		config:  cfg,
		plugins: make(map[string]*ManagedPlugin),
		logger:  grpcMgrLogger,
	}
}

// pluginMap returns the go-plugin plugin map for client config.
func pluginMap() map[string]goPlugin.Plugin {
	return map[string]goPlugin.Plugin{
		PluginType: &PluginInterface{},
	}
}

// Start discovers and launches all enabled plugins, then starts the background
// health check loop. Errors for individual plugins are logged but do not
// prevent other plugins from starting.
func (m *PluginManager) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	m.ctx = ctx
	m.cancel = cancel

	for name, entry := range m.config.Plugins {
		if !entry.Enabled {
			m.logger.Info("plugin disabled, skipping", "plugin", name)
			continue
		}

		path := resolvePluginPath(entry.Path, m.config.Directory, name)
		if _, err := os.Stat(path); err != nil {
			m.logger.Error("plugin binary not found", "plugin", name, "path", path, "error", err)
			continue
		}

		if err := m.startPlugin(ctx, name, path); err != nil {
			m.logger.Error("failed to start plugin", "plugin", name, "error", err)
		}
	}

	go m.healthCheckLoop(ctx)
	return nil
}

// Stop performs graceful shutdown: cancels all goroutines and kills every
// plugin process.
func (m *PluginManager) Stop() {
	if m.cancel != nil {
		m.cancel()
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for name, mp := range m.plugins {
		if mp.cancel != nil {
			mp.cancel()
		}
		if mp.client != nil {
			mp.client.Kill()
		}
		mp.Status = StatusStopped
		mp.Client = nil
		m.logger.Info("stopped plugin", "plugin", name)
	}
}

// GetPlugin returns a managed plugin by name.
func (m *PluginManager) GetPlugin(name string) (*ManagedPlugin, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mp, ok := m.plugins[name]
	return mp, ok
}

// ListPlugins returns all managed plugins.
func (m *PluginManager) ListPlugins() []*ManagedPlugin {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*ManagedPlugin, 0, len(m.plugins))
	for _, mp := range m.plugins {
		result = append(result, mp)
	}
	return result
}

// GetClient returns the gRPC client for a plugin by name.
func (m *PluginManager) GetClient(name string) (gen.PluginServiceClient, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mp, ok := m.plugins[name]
	if !ok || mp.Client == nil {
		return nil, false
	}
	return mp.Client, true
}

// GetClientForProtocol searches all running plugins for one that supports the
// given protocol. Returns the gRPC client if found, or nil otherwise.
func (m *PluginManager) GetClientForProtocol(protocol string) gen.PluginServiceClient {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, mp := range m.plugins {
		if mp.Status != StatusRunning || mp.Client == nil || mp.Info == nil {
			continue
		}
		for _, p := range mp.Info.GetProtocols() {
			if p == protocol {
				return mp.Client
			}
		}
	}
	return nil
}

// RestartPlugin manually restarts a running plugin by name. Returns an error
// if the plugin is not found or the restart fails.
func (m *PluginManager) RestartPlugin(name string) error {
	m.mu.RLock()
	mp, ok := m.plugins[name]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("plugin %q not found", name)
	}

	entry, hasEntry := m.config.Plugins[name]
	if !hasEntry {
		return fmt.Errorf("plugin %q: no config entry", name)
	}

	path := resolvePluginPath(entry.Path, m.config.Directory, name)

	// Stop old monitor goroutine
	if mp.cancel != nil {
		mp.cancel()
	}

	// Kill old client
	m.mu.Lock()
	if old, exists := m.plugins[name]; exists && old.client != nil {
		old.client.Kill()
	}
	m.mu.Unlock()

	return m.startPlugin(m.ctx, name, path)
}

// --- internal helpers ---

// resolvePluginPath determines the plugin binary path.
func resolvePluginPath(entryPath, dir, name string) string {
	if entryPath != "" {
		return entryPath
	}
	return filepath.Join(dir, name)
}

// startPlugin launches a single plugin process, dispenses the gRPC client,
// fetches plugin info, and starts the crash monitor goroutine.
func (m *PluginManager) startPlugin(ctx context.Context, name, path string) error {
	client, grpcClient, info, err := m.launch(name, path)
	if err != nil {
		return err
	}

	pluginCtx, pluginCancel := context.WithCancel(ctx)

	mp := &ManagedPlugin{
		Name:      name,
		Client:    grpcClient,
		Info:      info,
		Status:    StatusRunning,
		StartedAt: time.Now(),
		path:      path,
		client:    client,
		cancel:    pluginCancel,
	}

	// Clean up any previous instance (e.g. during restart)
	m.mu.Lock()
	if existing, exists := m.plugins[name]; exists {
		if existing.cancel != nil {
			existing.cancel()
		}
		if existing.client != nil {
			existing.client.Kill()
		}
	}
	m.plugins[name] = mp
	m.mu.Unlock()

	go m.monitorPlugin(pluginCtx, name, path)

	m.logger.Info("started plugin",
		"plugin", name,
		"version", info.GetVersion(),
		"protocols", info.GetProtocols(),
	)
	return nil
}

// launch creates a go-plugin client, connects, dispenses the gRPC client, and
// fetches plugin info. Returns the go-plugin Client, the gRPC client, and
// plugin metadata. On error the go-plugin client is killed.
func (m *PluginManager) launch(name, path string) (*goPlugin.Client, gen.PluginServiceClient, *gen.PluginInfo, error) {
	client := goPlugin.NewClient(&goPlugin.ClientConfig{
		HandshakeConfig:  Handshake,
		Plugins:          pluginMap(),
		Cmd:              exec.Command(path),
		AllowedProtocols: []goPlugin.Protocol{goPlugin.ProtocolGRPC},
	})

	dispenseClient, err := client.Client()
	if err != nil {
		client.Kill()
		return nil, nil, nil, fmt.Errorf("plugin %q: connect: %w", name, err)
	}

	raw, err := dispenseClient.Dispense(PluginType)
	if err != nil {
		client.Kill()
		return nil, nil, nil, fmt.Errorf("plugin %q: dispense: %w", name, err)
	}

	grpcClient, ok := raw.(gen.PluginServiceClient)
	if !ok {
		client.Kill()
		return nil, nil, nil, fmt.Errorf("plugin %q: unexpected type %T", name, raw)
	}

	info, err := grpcClient.GetPluginInfo(context.Background(), &gen.Empty{})
	if err != nil {
		client.Kill()
		return nil, nil, nil, fmt.Errorf("plugin %q: GetPluginInfo: %w", name, err)
	}

	return client, grpcClient, info, nil
}

// monitorPlugin watches for plugin process exit and restarts with exponential
// backoff. Runs until ctx is cancelled or max restart attempts are exceeded.
func (m *PluginManager) monitorPlugin(ctx context.Context, name, path string) {
	backoff := DefaultInitBackoff

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(DefaultHealthCheckInterval):
		}

		m.mu.RLock()
		mp, exists := m.plugins[name]
		m.mu.RUnlock()

		if !exists || mp.client == nil {
			return
		}

		// Still alive — reset backoff and continue
		if !mp.client.Exited() {
			backoff = DefaultInitBackoff
			continue
		}

		// --- Plugin process exited (crash) ---

		m.mu.Lock()
		mp, exists = m.plugins[name]
		if !exists {
			m.mu.Unlock()
			return
		}

		if mp.RestartCount >= MaxRestartAttempts {
			mp.Status = StatusError
			m.mu.Unlock()
			m.logger.Error("plugin exceeded max restart attempts, permanently errored",
				"plugin", name,
				"max_attempts", MaxRestartAttempts,
			)
			return
		}
		mp.RestartCount++
		count := mp.RestartCount
		m.mu.Unlock()

		m.logger.Error("plugin process exited, restarting",
			"plugin", name,
			"attempt", count,
			"backoff", backoff,
		)

		// Wait with exponential backoff + jitter
		jitter := time.Duration(rand.Int63n(int64(backoff / 2)))
		wait := backoff + jitter
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		// Kill old process
		m.mu.Lock()
		if old, ok := m.plugins[name]; ok && old.client != nil {
			old.client.Kill()
		}
		m.mu.Unlock()

		// Relaunch
		newClient, newGrpcClient, newInfo, err := m.launch(name, path)
		if err != nil {
			m.logger.Error("plugin restart failed", "plugin", name, "error", err)
			m.mu.Lock()
			if p, ok := m.plugins[name]; ok {
				p.Status = StatusError
			}
			m.mu.Unlock()

			backoff = backoff * 2
			if backoff > DefaultMaxBackoff {
				backoff = DefaultMaxBackoff
			}
			continue
		}

		// Update plugin entry
		m.mu.Lock()
		if old, ok := m.plugins[name]; ok {
			old.client = newClient
			old.Client = newGrpcClient
			old.Info = newInfo
			old.Status = StatusRunning
			old.StartedAt = time.Now()
		}
		m.mu.Unlock()

		backoff = DefaultInitBackoff
		m.logger.Info("plugin restarted successfully",
			"plugin", name,
			"attempt", count,
		)
	}
}

// healthCheckLoop periodically calls HealthCheck on every running plugin.
func (m *PluginManager) healthCheckLoop(ctx context.Context) {
	ticker := time.NewTicker(DefaultHealthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.checkAllPlugins(ctx)
		}
	}
}

// checkAllPlugins performs health checks on all running plugins.
func (m *PluginManager) checkAllPlugins(ctx context.Context) {
	m.mu.RLock()
	snapshots := make([]*ManagedPlugin, 0, len(m.plugins))
	for _, mp := range m.plugins {
		snapshots = append(snapshots, mp)
	}
	m.mu.RUnlock()

	for _, mp := range snapshots {
		if mp.Status != StatusRunning || mp.Client == nil {
			continue
		}

		hctx, cancel := context.WithTimeout(ctx, healthCheckTimeout)
		_, err := mp.Client.HealthCheck(hctx, &gen.Empty{})
		cancel()

		if err != nil {
			m.logger.Warn("plugin health check failed",
				"plugin", mp.Name,
				"error", err,
			)
			m.mu.Lock()
			if p, ok := m.plugins[mp.Name]; ok && p.Status == StatusRunning {
				p.Status = StatusError
			}
			m.mu.Unlock()
		}
	}
}
